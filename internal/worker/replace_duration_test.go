package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

const holdProcSource = `package main

import (
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	dir, gen := os.Args[1], os.Args[2]
	exitPath := filepath.Join(dir, "exit-"+gen)
	crashPath := filepath.Join(dir, "crash-"+gen)
	for {
		if _, err := os.Stat(exitPath); err == nil {
			os.Exit(0)
		}
		if _, err := os.Stat(crashPath); err == nil {
			os.Exit(1)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
`

type replaceSample struct {
	reason string
	sec    float64
}

type replaceSpy struct {
	mu       sync.Mutex
	replaces []replaceSample
	restarts int
}

func (s *replaceSpy) IncRecycle(string) {}
func (s *replaceSpy) IncRestart() {
	s.mu.Lock()
	s.restarts++
	s.mu.Unlock()
}
func (s *replaceSpy) ObserveReplace(reason string, sec float64) {
	s.mu.Lock()
	s.replaces = append(s.replaces, replaceSample{reason: reason, sec: sec})
	s.mu.Unlock()
}
func (s *replaceSpy) snapshot() (samples []replaceSample, restarts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]replaceSample(nil), s.replaces...)
	return out, s.restarts
}

// gateStarter boots a process that exits only when its generation file appears.
// The second start blocks until gate is closed, so a test can observe the gap.
type gateStarter struct {
	bin     string
	dir     string
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
	n       int
	mu      sync.Mutex
	block   bool
}

func (g *gateStarter) Start(ctx context.Context, slot int, gen uint64, _ uint64) (*Worker, error) {
	g.mu.Lock()
	g.n++
	n := g.n
	g.mu.Unlock()
	if n > 1 && g.block {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-g.gate:
		}
	}
	cmd := exec.Command(g.bin, g.dir, strconv.FormatUint(gen, 10))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Worker{
		ID:         strconv.FormatUint(gen, 10),
		Slot:       slot,
		Generation: gen,
		State:      StateIdle,
		StartedAt:  time.Now(),
		cmd:        cmd,
		logger:     slog.Default(),
	}, nil
}

func testReplaceConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.MaxRequests = 1
	cfg.Workers.ShutdownTimeout = 50 * time.Millisecond
	cfg.Workers.RestartLimit = 10
	cfg.Workers.RestartBackoff.Initial = time.Millisecond
	cfg.Workers.RestartBackoff.Maximum = 5 * time.Millisecond
	cfg.Workers.RestartBackoff.Multiplier = 1
	cfg.Workers.RestartBackoff.Jitter = 0
	cfg.Logging.AccessLog = false
	return cfg
}

func newReplacePool(t *testing.T, cfg config.Config, starter SlotStarter, metrics PoolMetrics) *Pool {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := NewWorkloadPool(PoolOptions{
		Name:     "http",
		Cfg:      cfg,
		Logger:   logger,
		Desired:  1,
		Min:      1,
		Max:      1,
		MinReady: 1,
		Starter:  starter,
	})
	p.SetMetrics(metrics)
	return p
}

func waitTryAcquire(t *testing.T, p *Pool) *Worker {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w, ok := p.TryAcquire(); ok {
			return w
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for an idle worker")
	return nil
}

func buildHoldProc(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "holdproc.go")
	bin := filepath.Join(dir, "holdproc")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(src, []byte(holdProcSource), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("build holdproc: %v", err)
	}
	return bin
}

func shutdownReplacePool(t *testing.T, p *Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestPlannedReplaceIsUnservableUntilIdle(t *testing.T) {
	cfg := testReplaceConfig(t)
	dir := t.TempDir()
	bin := buildHoldProc(t, t.TempDir())
	starter := &gateStarter{
		bin:     bin,
		dir:     dir,
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
		block:   true,
	}
	spy := &replaceSpy{}
	pool := newReplacePool(t, cfg, starter, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer shutdownReplacePool(t, pool)

	if samples, _ := spy.snapshot(); len(samples) != 0 {
		t.Fatal("initial boot emitted a replace sample")
	}
	w := waitTryAcquire(t, pool)
	pool.Release(w, nil)
	if err := os.WriteFile(filepath.Join(dir, "exit-1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-starter.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement boot did not start")
	}
	if _, ok := pool.TryAcquire(); ok {
		t.Fatal("worker acquirable while its replacement is blocked")
	}
	if samples, _ := spy.snapshot(); len(samples) != 0 {
		t.Fatal("replace sample emitted before the new worker was idle")
	}

	close(starter.gate)
	deadline := time.Now().Add(3 * time.Second)
	for {
		samples, _ := spy.snapshot()
		if len(samples) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the replace sample")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := pool.TryAcquire(); !ok {
		t.Fatal("replacement was not idle after the sample")
	}
	samples, restarts := spy.snapshot()
	if len(samples) != 1 {
		t.Fatalf("samples=%d restarts=%d", len(samples), restarts)
	}
	if samples[0].reason != "max_requests" || samples[0].sec <= 0 {
		t.Fatalf("sample=%+v", samples[0])
	}
	if restarts != 0 {
		t.Fatalf("planned replace counted as restart: %d", restarts)
	}
}

func TestCrashRestartDoesNotObserveReplace(t *testing.T) {
	cfg := testReplaceConfig(t)
	cfg.Workers.MaxRequests = 0
	dir := t.TempDir()
	bin := buildHoldProc(t, t.TempDir())
	starter := &gateStarter{
		bin:     bin,
		dir:     dir,
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
		block:   false,
	}
	spy := &replaceSpy{}
	pool := newReplacePool(t, cfg, starter, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer shutdownReplacePool(t, pool)

	if err := os.WriteFile(filepath.Join(dir, "crash-1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		samples, restarts := spy.snapshot()
		if restarts >= 1 {
			if _, ok := pool.TryAcquire(); ok {
				if len(samples) != 0 {
					t.Fatalf("crash emitted replace samples: %+v", samples)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			samples, restarts = spy.snapshot()
			t.Fatalf("crash restart did not yield an idle worker (restarts=%d samples=%d)", restarts, len(samples))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestShutdownDuringReplaceEmitsNoSample(t *testing.T) {
	cfg := testReplaceConfig(t)
	dir := t.TempDir()
	bin := buildHoldProc(t, t.TempDir())
	starter := &gateStarter{
		bin:     bin,
		dir:     dir,
		gate:    make(chan struct{}),
		entered: make(chan struct{}),
		block:   true,
	}
	spy := &replaceSpy{}
	pool := newReplacePool(t, cfg, starter, spy)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer shutdownReplacePool(t, pool)

	w := waitTryAcquire(t, pool)
	pool.Release(w, nil)
	if err := os.WriteFile(filepath.Join(dir, "exit-1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-starter.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement boot did not start")
	}

	shutdownReplacePool(t, pool)
	if samples, _ := spy.snapshot(); len(samples) != 0 {
		t.Fatalf("shutdown emitted replace samples: %+v", samples)
	}
}
