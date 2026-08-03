package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/dispatcher"
	"github.com/EreborCodeForge/Eregion/internal/lifecycle"
	"github.com/EreborCodeForge/Eregion/internal/socket"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

// Server is the top-level Eregion HTTP application server.
type Server struct {
	cfg     config.Config
	logger  *slog.Logger
	version string
	pool    *worker.Pool
	metrics *telemetry.Registry
	http    *http.Server
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
	sockets := socket.NewManager(cfg.Socket.Directory, cfg.Socket.DirectoryPermissions, cfg.Socket.SocketPermissions)
	pool := worker.NewPool(cfg, sockets, logger, version)
	metrics := telemetry.NewRegistry(cfg, pool)
	pool.SetOnChange(func() {})

	mux := http.NewServeMux()
	s := &Server{cfg: cfg, logger: logger, version: version, pool: pool, metrics: metrics}

	if cfg.Liveness.Enabled {
		mux.HandleFunc(cfg.Liveness.Path, telemetry.LiveHandler())
	}
	if cfg.Readiness.Enabled {
		mux.HandleFunc(cfg.Readiness.Path, telemetry.ReadyHandler(pool, cfg))
	}
	if cfg.Health.Enabled {
		mux.HandleFunc(cfg.Health.Path, telemetry.HealthHandler(pool, cfg))
	}
	if cfg.Metrics.Enabled {
		mux.HandleFunc(cfg.Metrics.Path, metrics.Handler())
	}

	disp := dispatcher.New(cfg, pool, logger, metrics)
	mux.Handle("/", disp)

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

// Run starts workers and the HTTP server until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("starting eregion", "version", s.version, "addr", s.cfg.Addr(), "workers", s.cfg.Workers.Count)

	if err := s.pool.Start(ctx); err != nil {
		return fmt.Errorf("start workers: %w", err)
	}

	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		_ = s.pool.Shutdown(context.Background())
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
		return s.shutdown()
	case err := <-errCh:
		_ = s.pool.Shutdown(context.Background())
		return err
	}
}

func (s *Server) shutdown() error {
	s.logger.Info("graceful shutdown started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
	defer cancel()

	httpErr := s.http.Shutdown(shutdownCtx)
	poolCtx, poolCancel := context.WithTimeout(context.Background(), s.cfg.Workers.ShutdownTimeout+time.Second)
	defer poolCancel()
	poolErr := s.pool.Shutdown(poolCtx)
	lifecycle.Cleanup(s.cfg.Socket.Directory)

	if httpErr != nil {
		return httpErr
	}
	return poolErr
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
