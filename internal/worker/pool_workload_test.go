package worker_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func writeHelper(t *testing.T, dir string) string {
	t.Helper()
	src := filepath.Join(dir, "helper.go")
	bin := filepath.Join(dir, "helper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	code := `package main
import ("os"; "strconv"; "time")
func main() {
  sec := 1
  if len(os.Args) > 1 {
    if v, err := strconv.Atoi(os.Args[1]); err == nil { sec = v }
  }
  time.Sleep(time.Duration(sec) * time.Second)
}
`
	if err := os.WriteFile(src, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("build helper: %v", err)
	}
	return bin
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
