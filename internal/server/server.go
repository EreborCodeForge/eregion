package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/dispatcher"
	"github.com/EreborCodeForge/Eregion/internal/lifecycle"
	"github.com/EreborCodeForge/Eregion/internal/reconciler"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Server is the top-level Eregion workload supervisor.
type Server struct {
	cfg          config.Config
	logger       *slog.Logger
	version      string
	registry     *workload.Registry
	pools        *worker.Manager
	httpPool     *worker.Pool
	metrics      *telemetry.Registry
	runtimeRes   resources.RuntimeResources
	workerSizing sizing.WorkerSizing
	http         *http.Server
	shuttingDown atomic.Bool
	reconciler   *reconciler.Reconciler
}

// New constructs a server from config.
func New(cfg config.Config, logger *slog.Logger, version string) (*Server, error) {
	sockDir := cfg.Socket.Directory
	if !filepath.IsAbs(sockDir) {
		abs, err := filepath.Abs(sockDir)
		if err != nil {
			return nil, err
		}
		sockDir = abs
		cfg.Socket.Directory = sockDir
	}

	resolved, err := cfg.ResolveWorkloads()
	if err != nil {
		return nil, fmt.Errorf("resolve workloads: %w", err)
	}

	// Observe-and-advise only: never mutates worker counts.
	runtimeRes := resources.SystemDetector{Logger: logger}.Detect()
	httpCount := cfg.Workers.Count
	for _, w := range resolved {
		if w.Mode == workload.ModeHTTP {
			httpCount = w.Workers.Max
			if httpCount < w.Workers.Min {
				httpCount = w.Workers.Min
			}
			break
		}
	}
	workerSizing := sizing.Advisor{}.Analyze(runtimeRes, httpCount)

	registry := workload.NewRegistry()
	for _, spec := range resolved {
		if err := registry.Upsert(spec); err != nil {
			return nil, err
		}
	}

	pools := worker.NewManager(cfg, logger, version)
	for _, spec := range resolved {
		if _, err := pools.Ensure(spec); err != nil {
			return nil, err
		}
	}

	httpPool, hasHTTP := pools.HTTP()
	metrics := telemetry.NewRegistry(cfg, pools, runtimeRes, workerSizing)
	if hasHTTP {
		httpPool.SetMetrics(metrics)
		httpPool.SetScaleRecorder(metrics)
	}
	for _, p := range pools.List() {
		p.SetScaleRecorder(metrics)
		if p.Mode() == workload.ModeHTTP {
			p.SetMetrics(metrics)
		} else {
			p.SetMetrics(metrics.ForWorkload(p.Name()))
		}
	}

	strategies := scaling.NewRegistry(nil)
	rec := reconciler.New(registry, pools, strategies, runtimeRes, logger)

	mux := http.NewServeMux()
	s := &Server{
		cfg:          cfg,
		logger:       logger,
		version:      version,
		registry:     registry,
		pools:        pools,
		httpPool:     httpPool,
		metrics:      metrics,
		runtimeRes:   runtimeRes,
		workerSizing: workerSizing,
		reconciler:   rec,
	}

	if cfg.Liveness.Enabled {
		mux.HandleFunc(cfg.Liveness.Path, telemetry.LiveHandler())
	}
	if hasHTTP {
		if cfg.Readiness.Enabled {
			mux.HandleFunc(cfg.Readiness.Path, telemetry.ReadyHandler(httpPool, cfg, s.IsShuttingDown))
		}
		if cfg.Health.Enabled {
			mux.HandleFunc(cfg.Health.Path, telemetry.HealthHandler(httpPool, cfg))
		}
	} else {
		if cfg.Readiness.Enabled {
			mux.HandleFunc(cfg.Readiness.Path, telemetry.ReadyHandlerMulti(pools, s.IsShuttingDown))
		}
		if cfg.Health.Enabled {
			mux.HandleFunc(cfg.Health.Path, telemetry.HealthHandlerMulti(pools))
		}
	}
	if cfg.Metrics.Enabled {
		mux.HandleFunc(cfg.Metrics.Path, metrics.Handler())
	}

	if hasHTTP {
		disp := dispatcher.New(cfg, httpPool, logger, metrics)
		httpPool.SetQueueWaitingProvider(disp.Waiting)
		mux.Handle("/", disp)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no http workload configured", http.StatusNotFound)
		})
	}

	s.http = &http.Server{
		Addr:              cfg.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}
	return s, nil
}

// Run starts workloads, reconciler, and the HTTP server until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	names := make([]string, 0)
	for _, p := range s.pools.List() {
		names = append(names, p.Name())
	}
	s.logger.Info("starting eregion", "version", s.version, "addr", s.cfg.Addr(), "workloads", names)
	sizing.LogRuntimeResources(s.logger, s.runtimeRes, s.workerSizing)
	sizing.LogMemoryEnvelope(s.logger, s.runtimeRes, s.cfg.Workers.Count, s.cfg.Workers.MemoryLimitMB)

	if err := s.pools.StartAll(ctx); err != nil {
		return fmt.Errorf("start workers: %w", err)
	}

	recCtx, recCancel := context.WithCancel(ctx)
	defer recCancel()
	go s.reconciler.Run(recCtx)

	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		_ = s.pools.ShutdownAll(context.Background())
		return fmt.Errorf("listen: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("server listening", "addr", ln.Addr().String())
		err := s.http.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		recCancel()
		return s.shutdown()
	case err := <-errCh:
		recCancel()
		_ = s.pools.ShutdownAll(context.Background())
		return err
	}
}

func (s *Server) shutdown() error {
	s.logger.Info("graceful shutdown started")
	s.shuttingDown.Store(true)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	httpErr := s.http.Shutdown(shutdownCtx)
	poolCtx, poolCancel := context.WithTimeout(context.Background(), s.cfg.Workers.ShutdownTimeout+time.Second)
	defer poolCancel()
	poolErr := s.pools.ShutdownAll(poolCtx)
	lifecycle.Cleanup(s.cfg.Socket.Directory)

	if httpErr != nil {
		return httpErr
	}
	return poolErr
}

// IsShuttingDown reports whether graceful shutdown has started.
func (s *Server) IsShuttingDown() bool {
	return s.shuttingDown.Load()
}

// PrintStatus fetches a health URL and prints it.
func PrintStatus(url string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	fmt.Printf("HTTP %s\n", resp.Status)
	_, _ = os.Stdout.ReadFrom(resp.Body)
	fmt.Println()
	if resp.StatusCode >= 400 {
		return 1
	}
	return 0
}
