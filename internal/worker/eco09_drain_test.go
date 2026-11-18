package worker

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/workload"
)

type countingProcStarter struct {
	bin string
	dir string
	n   atomic.Int32
}

func (s *countingProcStarter) Start(_ context.Context, slot int, gen uint64, _ uint64) (*Worker, error) {
	s.n.Add(1)
	cmd := exec.Command(s.bin, s.dir, strconv.FormatUint(gen, 10))
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
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, nil
}

func TestECO09T23AdmittedScaleDownUsesExistingDrain(t *testing.T) {
	cfg := testReplaceConfig(t)
	cfg.Workers.ShutdownTimeout = 200 * time.Millisecond
	dir := t.TempDir()
	starter := &countingProcStarter{bin: buildHoldProc(t, t.TempDir()), dir: dir}
	pool := NewWorkloadPool(PoolOptions{
		Name:     "jobs",
		Mode:     workload.ModeConsumer,
		Cfg:      cfg,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Desired:  2,
		Min:      2,
		Max:      2,
		MinReady: 0,
		Starter:  starter,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer shutdownReplacePool(t, pool)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && pool.Snapshot().Running < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Snapshot().Running != 2 {
		t.Fatalf("before scale-down: %+v", pool.Snapshot())
	}
	spawned := starter.n.Load()
	pool.SetAdmitted(1)
	if pool.Snapshot().Desired != 1 {
		t.Fatalf("admitted target was raised back to the minimum: %+v", pool.Snapshot())
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && pool.Snapshot().Running > 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if pool.Snapshot().Running > 1 {
		t.Fatalf("drain did not retire the extra worker: %+v", pool.Snapshot())
	}
	if starter.n.Load() != spawned {
		t.Fatalf("scale-down spawned a replacement: before=%d after=%d", spawned, starter.n.Load())
	}
}

func TestECO09T24BusyHTTPScaleDownWaitsForRelease(t *testing.T) {
	cfg := testReplaceConfig(t)
	dir := t.TempDir()
	starter := &countingProcStarter{bin: buildHoldProc(t, t.TempDir()), dir: dir}
	pool := NewWorkloadPool(PoolOptions{
		Name:     "http",
		Mode:     workload.ModeHTTP,
		Cfg:      cfg,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Desired:  1,
		Min:      1,
		Max:      1,
		MinReady: 0,
		Starter:  starter,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := pool.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer shutdownReplacePool(t, pool)
	w := waitTryAcquire(t, pool)
	if w.GetSnapshot().State != StateBusy {
		t.Fatalf("state=%s", w.GetSnapshot().State)
	}
	pool.SetAdmitted(0)
	time.Sleep(cfg.Workers.ShutdownTimeout + 100*time.Millisecond)
	if w.cmd == nil || w.cmd.Process == nil || !processStillAlive(w.cmd.Process) {
		t.Fatal("busy HTTP worker was killed before Release")
	}
	if w.GetSnapshot().State != StateBusy {
		t.Fatalf("state after admitted scale-down=%s", w.GetSnapshot().State)
	}
	if starter.n.Load() != 1 {
		t.Fatalf("spawns=%d", starter.n.Load())
	}
	pool.Release(w, nil)
}
