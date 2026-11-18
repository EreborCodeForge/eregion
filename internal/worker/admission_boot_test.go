package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

type forbidStarter struct {
	n atomic.Int32
}

func (f *forbidStarter) Start(context.Context, int, uint64, uint64) (*worker.Worker, error) {
	f.n.Add(1)
	return nil, errors.New("spawned")
}

type countGate struct {
	n atomic.Int32
}

func (g *countGate) Admit(req worker.SpawnRequest) (worker.SpawnGrant, error) {
	g.n.Add(1)
	return worker.SpawnGrant{}, errors.New("gate should not run")
}
func (*countGate) Commit(worker.SpawnGrant)      {}
func (*countGate) Release(worker.SpawnGrant)     {}
func (*countGate) ReleaseExit(worker.SpawnGrant) {}

func TestECO09T03StartAllSpawnsNothingWhenMinimumsDoNotFit(t *testing.T) {
	cfg := config.Default()
	cfg.ResourceGuard.Mode = config.ResourceGuardEnforce
	cfg.ResourceGuard.Memory.ReserveMB = 128
	cfg.ResourceGuard.Memory.ReservePercent = 0
	cfg.ResourceGuard.Memory.WorkerFallbackMB = 256
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := worker.NewManager(cfg, logger, "test")
	spec := workload.ResolvedWorkloadSpec{
		Name:      "jobs",
		Mode:      workload.ModeConsumer,
		Command:   []string{"php", "vendor/bin/job-worker"},
		Workers:   workload.WorkerPolicy{Min: 4, Max: 8},
		Resources: workload.ResourcePolicy{MemoryMB: 256, Class: workload.ClassIO},
		Scaling:   workload.DefaultScaling(),
	}
	if _, err := mgr.Ensure(spec); err != nil {
		t.Fatal(err)
	}
	gate := &countGate{}
	sample := resources.Sample{
		At:                 time.Unix(50, 0),
		Source:             resources.SourceCgroupV2,
		Scope:              "cgroup",
		HierarchyKnown:     true,
		MemoryLimitBytes:   1 << 30,
		MemoryLimitValid:   resources.ValidityKnown,
		MemoryCurrentBytes: 256 << 20,
		MemoryCurrentValid: resources.ValidityKnown,
	}
	guard := &scaling.Guard{
		Cfg: cfg.ResourceGuard,
		Collector: &resources.Collector{Sampler: resources.SamplerFunc(func(context.Context) (resources.Sample, error) {
			return sample, nil
		})},
		Ledger: scaling.NewLedger(),
		Logger: logger,
	}
	mgr.SetSpawnGate(gate)
	mgr.SetBeforeStart(func(ctx context.Context) error {
		return guard.Preflight(ctx, []workload.ResolvedWorkloadSpec{spec}, mgr)
	})
	err := mgr.StartAll(context.Background())
	if !errors.Is(err, scaling.ErrInsufficientCapacity) {
		t.Fatalf("err=%v", err)
	}
	if gate.n.Load() != 0 {
		t.Fatalf("admission calls=%d, want 0 before the first spawn", gate.n.Load())
	}
}

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(step string) {
	l.mu.Lock()
	l.ev = append(l.ev, step)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

type orderGate struct {
	log *eventLog
}

func (g orderGate) Admit(req worker.SpawnRequest) (worker.SpawnGrant, error) {
	g.log.add("admit")
	return worker.SpawnGrant{ID: 1, Workload: req.Workload, Slot: req.Slot, Generation: req.Generation}, nil
}
func (orderGate) Commit(worker.SpawnGrant)      {}
func (orderGate) Release(worker.SpawnGrant)     {}
func (orderGate) ReleaseExit(worker.SpawnGrant) {}

type orderStarter struct {
	log *eventLog
}

func (s orderStarter) Start(context.Context, int, uint64, uint64) (*worker.Worker, error) {
	s.log.add("start")
	return nil, errors.New("stop after order check")
}

func TestECO09T22BootSlotAdmitsBeforeStart(t *testing.T) {
	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	log := &eventLog{}
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name:     "jobs",
		Mode:     workload.ModeConsumer,
		Command:  []string{"php"},
		Cfg:      cfg,
		Logger:   logger,
		Desired:  1,
		Min:      1,
		Max:      1,
		MinReady: 0,
		Starter:  orderStarter{log: log},
	})
	pool.SetSpawnGate(orderGate{log: log})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = pool.Start(ctx)
	events := log.snapshot()
	cancel()
	shCtx, shCancel := context.WithTimeout(context.Background(), time.Second)
	defer shCancel()
	_ = pool.Shutdown(shCtx)
	if len(events) < 2 || events[0] != "admit" || events[1] != "start" {
		t.Fatalf("order=%v", events)
	}
}

func TestForbidStarterIsUsedByOptions(t *testing.T) {
	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	starter := &forbidStarter{}
	pool := worker.NewWorkloadPool(worker.PoolOptions{
		Name: "jobs", Mode: workload.ModeConsumer, Command: []string{"php"},
		Cfg: cfg, Logger: logger, Desired: 1, Min: 0, Max: 1, MinReady: 0,
		Starter: starter,
	})
	pool.SetSpawnGate(&countGate{})
	_ = pool.Start(context.Background())
	if starter.n.Load() != 0 {
		t.Fatalf("starter ran despite admission denial setup, n=%d", starter.n.Load())
	}
}
