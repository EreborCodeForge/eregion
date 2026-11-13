package dispatcher_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/dispatcher"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

const sleeperSource = `package main

import "time"

func main() { time.Sleep(30 * time.Second) }
`

type pathMetrics struct {
	duration atomic.Int32
	queue    atomic.Int32
	round    atomic.Int32
}

func (m *pathMetrics) IncRequests()             {}
func (m *pathMetrics) DecInFlight()             {}
func (m *pathMetrics) IncInFlight()             {}
func (m *pathMetrics) ObserveDuration(float64)  { m.duration.Add(1) }
func (m *pathMetrics) ObserveQueueWait(float64) { m.queue.Add(1) }
func (m *pathMetrics) ObserveRoundtrip(float64) { m.round.Add(1) }
func (m *pathMetrics) IncError(string)          {}
func (m *pathMetrics) IncQueueReject()          {}

type pipeStarter struct {
	bin  string
	conn *protocol.Conn
	once atomic.Bool
}

func (s *pipeStarter) Start(ctx context.Context, slot int, gen uint64, _ uint64) (*worker.Worker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.bin)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	w := &worker.Worker{
		ID:         "pipe",
		Slot:       slot,
		Generation: gen,
		State:      worker.StateIdle,
		StartedAt:  time.Now(),
	}
	var conn *protocol.Conn
	if s.once.CompareAndSwap(false, true) {
		conn = s.conn
	}
	worker.BindForTest(w, conn, cmd)
	return w, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func baseCfg(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 0
	cfg.Workers.MaxRequests = 0
	cfg.Workers.AcquireTimeout = 40 * time.Millisecond
	cfg.Workers.RequestTimeout = 100 * time.Millisecond
	cfg.Workers.ShutdownTimeout = 50 * time.Millisecond
	cfg.Queue.Capacity = 0
	cfg.Server.MaxBodyBytes = 4
	cfg.Logging.AccessLog = false
	cfg.Operations.Prefix = "/_eregion"
	return cfg
}

func buildSleeper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "sleeper.go")
	bin := filepath.Join(dir, "sleeper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(src, []byte(sleeperSource), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("build sleeper: %v", err)
	}
	return bin
}

func startPool(t *testing.T, cfg config.Config, starter worker.SlotStarter, desired int) *worker.Pool {
	t.Helper()
	p := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "http",
		Cfg:      cfg,
		Logger:   discardLogger(),
		Desired:  desired,
		Min:      0,
		Max:      1,
		MinReady: desired,
		Starter:  starter,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sh, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = p.Shutdown(sh)
	})
	return p
}

func TestRequestPathsObserveDurationOnce(t *testing.T) {
	tests := []struct {
		name                   string
		status                 int
		duration, queue, round int32
		run                    func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request)
	}{
		{
			name:     "body too large",
			status:   http.StatusRequestEntityTooLarge,
			duration: 1,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				pool := startPool(t, cfg, nil, 0)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodPost, "/ping", strings.NewReader("12345"))
				return d, m, req
			},
		},
		{
			name:     "queue full",
			status:   http.StatusServiceUnavailable,
			duration: 1,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				cfg.Queue.Capacity = 0
				pool := startPool(t, cfg, nil, 0)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodGet, "/ping", nil)
				return d, m, req
			},
		},
		{
			name:     "acquire timeout",
			status:   http.StatusServiceUnavailable,
			duration: 1,
			queue:    1,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				cfg.Queue.Capacity = 1
				pool := startPool(t, cfg, nil, 0)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodGet, "/ping", nil)
				return d, m, req
			},
		},
		{
			name:     "worker timeout",
			status:   http.StatusGatewayTimeout,
			duration: 1,
			round:    1,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				cfg.Workers.RequestTimeout = 80 * time.Millisecond
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					_, _ = protocol.ReadFrame(server, 1<<20)
					// Keep the pipe open so the client hits its deadline instead of EOF.
					_, _ = protocol.ReadFrame(server, 1<<20)
				}()
				starter := &pipeStarter{bin: buildSleeper(t), conn: protocol.NewConn(client, 1<<20)}
				pool := startPool(t, cfg, starter, 1)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodGet, "/ping", nil)
				return d, m, req
			},
		},
		{
			name:     "success",
			status:   http.StatusOK,
			duration: 1,
			round:    1,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				cfg.Workers.RequestTimeout = 2 * time.Second
				client, server := net.Pipe()
				go func() {
					defer server.Close()
					raw, err := protocol.ReadFrame(server, 1<<20)
					if err != nil {
						return
					}
					var env protocol.RequestEnvelope
					if err := protocol.Decode(raw, &env); err != nil {
						return
					}
					payload, err := protocol.Encode(protocol.ResponseEnvelope{
						Type:    protocol.TypeResponse,
						Version: protocol.ProtocolVersion,
						ID:      env.ID,
						Status:  200,
						Headers: map[string][]string{"Content-Type": {"text/plain"}},
						Body:    []byte("ok"),
					})
					if err != nil {
						return
					}
					_ = protocol.WriteFrame(server, payload)
				}()
				starter := &pipeStarter{bin: buildSleeper(t), conn: protocol.NewConn(client, 1<<20)}
				pool := startPool(t, cfg, starter, 1)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodGet, "/ping", nil)
				return d, m, req
			},
		},
		{
			name:   "operations prefix",
			status: http.StatusNotFound,
			run: func(t *testing.T) (*dispatcher.Dispatcher, *pathMetrics, *http.Request) {
				cfg := baseCfg(t)
				pool := startPool(t, cfg, nil, 0)
				m := &pathMetrics{}
				d := dispatcher.New(cfg, pool, discardLogger(), m)
				req := httptest.NewRequest(http.MethodGet, "/_eregion/metrics", nil)
				return d, m, req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, m, req := tt.run(t)
			rec := httptest.NewRecorder()
			d.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tt.status, rec.Body.String())
			}
			if got := m.duration.Load(); got != tt.duration {
				t.Fatalf("duration observations=%d want %d", got, tt.duration)
			}
			if got := m.queue.Load(); got != tt.queue {
				t.Fatalf("queue observations=%d want %d", got, tt.queue)
			}
			if got := m.round.Load(); got != tt.round {
				t.Fatalf("roundtrip observations=%d want %d", got, tt.round)
			}
		})
	}
}
