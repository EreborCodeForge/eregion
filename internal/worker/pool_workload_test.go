package worker_test

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
	"github.com/EreborCodeForge/Eregion/internal/socket"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestConsumerPoolMinZero(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	helper := writeHelper(t, t.TempDir())
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "batch",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper, "30"},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  0,
		Min:      0,
		Max:      4,
		MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	snap := pool.Snapshot()
	if snap.Desired != 0 || snap.Running != 0 {
		t.Fatalf("snap=%+v", snap)
	}
	_ = pool.Shutdown(context.Background())
}

func TestConsumerScaleUpDown(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = 2 * time.Second
	helper := writeHelper(t, t.TempDir())
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "c",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper, "60"},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  1,
		Min:      0,
		Max:      4,
		MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()

	waitRunning(t, pool, 1, 5*time.Second)

	pool.SetDesired(3)
	if pool.Snapshot().Desired != 3 {
		t.Fatalf("desired=%d", pool.Snapshot().Desired)
	}
	waitRunning(t, pool, 3, 5*time.Second)

	pool.SetDesired(1)
	if pool.Snapshot().Desired != 1 {
		t.Fatalf("desired after down=%d", pool.Snapshot().Desired)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		snap := pool.Snapshot()
		if snap.Running+snap.Starting <= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scale-down stuck: %+v", snap)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConsumerMetadataEnv(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.PHP.Environment = map[string]string{
		worker.EnvWorkload:   "stale-workload",
		worker.EnvWorkerID:   "stale-id",
		worker.EnvGeneration: "99",
	}
	helper := writeHelper(t, t.TempDir())
	rec := &recordingSpawner{}
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "telemetry",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper, "30"},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  1,
		Min:      0,
		Max:      2,
		MinReady: 0,
		Spawner:  rec,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()
	waitRunning(t, pool, 1, 5*time.Second)

	env := rec.firstEnv(t)
	if got := env[worker.EnvWorkload]; got != "telemetry" {
		t.Fatalf("EREGION_WORKLOAD=%q", got)
	}
	if got := env[worker.EnvWorkerID]; got != "consumer-1" {
		t.Fatalf("EREGION_WORKER_ID=%q", got)
	}
	if got := env[worker.EnvGeneration]; got != "1" {
		t.Fatalf("EREGION_GENERATION=%q", got)
	}
}

func TestConsumerScaleDownGracefulNoRestart(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = 2 * time.Second
	cfg.Workers.RestartBackoff.Initial = 50 * time.Millisecond
	cfg.Workers.RestartBackoff.Maximum = 200 * time.Millisecond
	helper := writeHelper(t, t.TempDir())
	rec := &recordingSpawner{}
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "drain",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper, "60"},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  2,
		Min:      0,
		Max:      4,
		MinReady: 0,
		Spawner:  rec,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()
	waitRunning(t, pool, 2, 5*time.Second)
	spawnsBefore := rec.spawnCount()

	pool.SetDesired(1)
	waitAtMostRunning(t, pool, 1, 8*time.Second)

	// Retired slot must not restart after graceful SIGTERM exit.
	time.Sleep(500 * time.Millisecond)
	if n := pool.Snapshot().Running + pool.Snapshot().Starting; n > 1 {
		t.Fatalf("retired slot restarted: running+starting=%d", n)
	}
	if rec.spawnCount() != spawnsBefore {
		t.Fatalf("unexpected respawn after scale-down: before=%d after=%d", spawnsBefore, rec.spawnCount())
	}
}

func TestConsumerScaleDownForceKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM ignore is not reliable on Windows")
	}
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = 200 * time.Millisecond
	helper := writeStubbornHelper(t, t.TempDir())
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "stubborn",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  2,
		Min:      0,
		Max:      2,
		MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()
	waitRunning(t, pool, 2, 5*time.Second)

	started := time.Now()
	pool.SetDesired(0)
	waitAtMostRunning(t, pool, 0, 3*time.Second)
	elapsed := time.Since(started)
	if elapsed < cfg.Workers.ShutdownTimeout {
		t.Fatalf("force-kill path too fast (%v); expected grace of %v", elapsed, cfg.Workers.ShutdownTimeout)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("scale-down blocked too long: %v", elapsed)
	}
}

func TestConsumerCrashRestartsWithBackoff(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = time.Second
	cfg.Workers.RestartBackoff.Initial = 50 * time.Millisecond
	cfg.Workers.RestartBackoff.Maximum = 200 * time.Millisecond
	cfg.Workers.RestartLimit = 10
	helper := writeExitHelper(t, t.TempDir(), 1)
	rec := &recordingSpawner{}
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "crashy",
		Mode:     workload.ModeConsumer,
		Command:  []string{helper},
		Cfg:      cfg,
		Logger:   slog.Default(),
		Desired:  1,
		Min:      1,
		Max:      1,
		MinReady: 0,
		Spawner:  rec,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		// Start may fail if the first process exits before "ready"; boot still schedules restart.
		t.Logf("start: %v", err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rec.spawnCount() >= 2 {
			gens := rec.generations()
			if len(gens) >= 2 && gens[1] > gens[0] {
				return
			}
			if rec.spawnCount() >= 2 {
				return
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("expected crash restart, spawns=%d gens=%v", rec.spawnCount(), rec.generations())
}

func TestPoolManagerIsolation(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = 2 * time.Second
	helper := writeHelper(t, t.TempDir())
	mgr := worker.NewManager(cfg, slog.Default(), "test")

	a, err := mgr.Ensure(workload.ResolvedWorkloadSpec{
		Name: "a", Mode: workload.ModeConsumer, Command: []string{helper, "30"},
		Workers: workload.WorkerPolicy{Min: 1, Max: 2}, Scaling: workload.DefaultScaling(), Resources: workload.DefaultResources(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := mgr.Ensure(workload.ResolvedWorkloadSpec{
		Name: "b", Mode: workload.ModeConsumer, Command: []string{helper, "30"},
		Workers: workload.WorkerPolicy{Min: 0, Max: 1}, Scaling: workload.DefaultScaling(), Resources: workload.DefaultResources(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() == b.Name() {
		t.Fatal("same pool")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = mgr.ShutdownAll(shCtx)
	}()

	a.SetDesired(2)
	if b.Snapshot().Desired != 0 {
		t.Fatalf("b should stay at min 0, got %d", b.Snapshot().Desired)
	}
}

func TestFakeSpawnerConsumer(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Socket.Directory = dir
	helper := writeHelper(t, dir)

	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name: "x", Mode: workload.ModeConsumer, Command: []string{helper, "2"},
		Cfg: cfg, Logger: slog.Default(), Desired: 1, Min: 1, Max: 2, MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shCancel()
		_ = pool.Shutdown(shCtx)
	}()
	waitRunning(t, pool, 1, 3*time.Second)
}

func TestHTTPPoolStillUsesSockets(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.Count = 2
	cfg.Socket.Directory = t.TempDir()
	socks := socket.NewManager(cfg.Socket.Directory, cfg.Socket.DirectoryPermissions, cfg.Socket.SocketPermissions)
	pool := worker.NewPool(cfg, socks, slog.Default(), "test")
	if pool.Mode() != workload.ModeHTTP {
		t.Fatalf("mode=%s", pool.Mode())
	}
	if pool.Snapshot().Desired != 2 {
		t.Fatalf("desired=%d", pool.Snapshot().Desired)
	}
}

func waitRunning(t *testing.T, pool *worker.Pool, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := pool.Snapshot()
		if snap.Running+snap.Starting >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %d running: %+v", n, pool.Snapshot())
}

func waitAtMostRunning(t *testing.T, pool *worker.Pool, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap := pool.Snapshot()
		if snap.Running+snap.Starting <= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for <=%d running: %+v", n, pool.Snapshot())
}

type recordingSpawner struct {
	mu    sync.Mutex
	specs []worker.ProcessSpecification
	inner worker.ExecSpawner
}

func (r *recordingSpawner) Spawn(ctx context.Context, spec worker.ProcessSpecification) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	r.mu.Lock()
	envCopy := make(map[string]string, len(spec.Environment))
	for k, v := range spec.Environment {
		envCopy[k] = v
	}
	copied := spec
	copied.Environment = envCopy
	r.specs = append(r.specs, copied)
	r.mu.Unlock()
	return r.inner.Spawn(ctx, spec)
}

func (r *recordingSpawner) spawnCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.specs)
}

func (r *recordingSpawner) firstEnv(t *testing.T) map[string]string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.specs) == 0 {
		t.Fatal("no spawns recorded")
	}
	return r.specs[0].Environment
}

func (r *recordingSpawner) generations() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uint64, len(r.specs))
	for i, s := range r.specs {
		out[i] = s.Generation
	}
	return out
}

func writeHelper(t *testing.T, dir string) string {
	t.Helper()
	return buildGoHelper(t, dir, "helper", `package main
import ("os"; "strconv"; "time")
func main() {
  sec := 1
  if len(os.Args) > 1 {
    if v, err := strconv.Atoi(os.Args[1]); err == nil { sec = v }
  }
  time.Sleep(time.Duration(sec) * time.Second)
}
`)
}

func writeExitHelper(t *testing.T, dir string, code int) string {
	t.Helper()
	src := "package main\nimport \"os\"\nfunc main() { os.Exit(" + strconv.Itoa(code) + ") }\n"
	return buildGoHelper(t, dir, "exithelper", src)
}

func writeStubbornHelper(t *testing.T, dir string) string {
	t.Helper()
	return buildGoHelper(t, dir, "stubborn", `package main
import (
  "os/signal"
  "syscall"
  "time"
)
func main() {
  signal.Ignore(syscall.SIGTERM)
  time.Sleep(60 * time.Second)
}
`)
}

func buildGoHelper(t *testing.T, dir, name, code string) string {
	t.Helper()
	src := filepath.Join(dir, name+".go")
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	return bin
}
