package reconciler_test

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/reconciler"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestReconcileFixedScalesToMax(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Workers.ShutdownTimeout = time.Second

	cmd := []string{"sleep", "30"}
	if runtime.GOOS == "windows" {
		cmd = []string{"powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 30"}
	}

	reg := workload.NewRegistry()
	_ = reg.Upsert(workload.ResolvedWorkloadSpec{
		Name: "c", Mode: workload.ModeConsumer,
		Command: cmd,
		Workers: workload.WorkerPolicy{Min: 0, Max: 2},
		Scaling: workload.ScalingPolicy{
			Strategy:         workload.StrategyFixed,
			ScaleUpCooldown:  time.Millisecond,
			ScaleDownIdleFor: time.Millisecond,
		},
		Resources: workload.DefaultResources(),
	})

	mgr := worker.NewManager(cfg, slog.Default(), "test")
	rec := reconciler.New(reg, mgr, scaling.NewRegistry(nil), resources.RuntimeResources{AvailableCPUs: 8}, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := mgr.Ensure(reg.List()[0]); err != nil {
		t.Fatal(err)
	}
	if err := mgr.StartAll(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mgr.ShutdownAll(context.Background()) }()

	rec.ReconcileOnce(ctx)
	pool, ok := mgr.Get("c")
	if !ok {
		t.Fatal("missing pool")
	}
	if pool.Snapshot().Desired != 2 {
		t.Fatalf("desired=%d want 2", pool.Snapshot().Desired)
	}
}
