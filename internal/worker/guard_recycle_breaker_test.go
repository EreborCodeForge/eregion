package worker

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
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

type countingStarter struct {
	n atomic.Int32
}

func (c *countingStarter) Start(context.Context, int, uint64, uint64) (*Worker, error) {
	c.n.Add(1)
	return nil, errors.New("starter should not run")
}

func testBreaker(now *time.Time, cfg config.GuardRecycleSafetyConfig) *GuardRecycleBreaker {
	return NewGuardRecycleBreaker("alpha", cfg, func() time.Time { return *now })
}

func openSafety() config.GuardRecycleSafetyConfig {
	return config.GuardRecycleSafetyConfig{
		Enabled:                    true,
		MaxRecycles:                3,
		Window:                     30 * time.Second,
		Cooldown:                   60 * time.Second,
		HalfOpenMaxProbes:          1,
		RecoverySuccessfulRequests: 20,
		RecoveryMinUptime:          30 * time.Second,
	}
}

func TestE01GuardStormOpensBreaker(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBreaker(&now, openSafety())
	for i := 0; i < 3; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
		now = now.Add(time.Second)
	}
	if b.State() != "open" {
		t.Fatalf("state=%s", b.State())
	}
	if permit := b.AllowBoot(BootGuardReplacement); permit.Allowed {
		t.Fatal("guard replacement was allowed after the budget")
	}
}

func TestE02ReleaseDoesNotReturnGuardWorkerToIdle(t *testing.T) {
	cfg := config.Default()
	p := NewWorkloadPool(PoolOptions{
		Name: "alpha", Mode: workload.ModeHTTP, Cfg: cfg,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Desired: 1, Min: 0, Max: 2, MinReady: 0,
	})
	w1 := &Worker{ID: "a", Slot: 1, Generation: 1, State: StateIdle, StartedAt: time.Now(), logger: slog.Default()}
	w2 := &Worker{ID: "b", Slot: 2, Generation: 1, State: StateIdle, StartedAt: time.Now(), logger: slog.Default()}
	p.slots = []*slot{
		{index: 1, generation: 1, worker: w1, everBooted: true, backoff: NewBackoff(cfg.Workers.RestartBackoff)},
		{index: 2, generation: 1, worker: w2, everBooted: true, backoff: NewBackoff(cfg.Workers.RestartBackoff)},
	}
	p.desired = 2
	var wg sync.WaitGroup
	release := func(w *Worker) {
		defer wg.Done()
		env := &protocol.ResponseEnvelope{}
		env.Meta.Recycle = true
		env.Meta.RecycleReason = guardStateViolationReason
		p.Release(w, env)
	}
	wg.Add(2)
	go release(w1)
	go release(w2)
	wg.Wait()
	if w1.getState() == StateIdle || w2.getState() == StateIdle {
		t.Fatalf("states %s %s", w1.getState(), w2.getState())
	}
	select {
	case got := <-p.idle:
		t.Fatalf("worker returned to idle: %s", got.ID)
	default:
	}
}

func TestE03NoEligibleBootWhenOpen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBreaker(&now, openSafety())
	for i := 0; i < 3; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	if b.AllowBoot(BootGuardReplacement).Allowed {
		t.Fatal("allowed a guard boot with no healthy replacement")
	}
	if !b.AllowBoot(BootHealthyReplacement).Allowed {
		t.Fatal("healthy replacement must keep running")
	}
}

func TestE04HalfOpenAdmitsOneProbe(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := openSafety()
	cfg.MaxRecycles = 1
	cfg.Cooldown = time.Second
	b := testBreaker(&now, cfg)
	b.ObserveRecycle(guardStateViolationReason, 1)
	now = now.Add(time.Second)
	var allowed atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if b.AllowBoot(BootGuardReplacement).Probe {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("probes=%d", allowed.Load())
	}
	if b.AllowBoot(BootScaleUp).Allowed {
		t.Fatal("scale-up took a second trial")
	}
}

func TestE05RecoveryRequiresRequestsAndUptime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := openSafety()
	cfg.Cooldown = time.Second
	b := testBreaker(&now, cfg)
	for i := 0; i < 3; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	now = now.Add(time.Second)
	permit := b.AllowBoot(BootGuardReplacement)
	if !permit.Probe {
		t.Fatal("expected the half-open probe")
	}
	b.NoteBoot(permit, 9)
	if b.State() != "half_open" {
		t.Fatalf("state=%s", b.State())
	}
	now = now.Add(time.Hour)
	if b.State() != "half_open" {
		t.Fatal("aged half-open closed with zero healthy requests")
	}
	fresh := now
	for i := 0; i < 19; i++ {
		b.NoteHealthyRequest(9, fresh)
	}
	if b.State() != "half_open" {
		t.Fatal("closed before 20 healthy requests")
	}
	b.NoteHealthyRequest(9, fresh)
	if b.State() != "half_open" {
		t.Fatal("closed before minimum uptime")
	}
	b.NoteHealthyRequest(9, now.Add(-31*time.Second))
	if b.State() != "closed" {
		t.Fatalf("state=%s", b.State())
	}
	b.ObserveRecycle(guardStateViolationReason, 10)
	if b.State() == "open" {
		t.Fatal("budget was not reset")
	}
}

func TestE06HalfOpenViolationReopens(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := openSafety()
	cfg.MaxRecycles = 1
	cfg.Cooldown = time.Minute
	b := testBreaker(&now, cfg)
	b.ObserveRecycle(guardStateViolationReason, 4)
	now = now.Add(time.Minute)
	permit := b.AllowBoot(BootGuardReplacement)
	b.NoteBoot(permit, 8)
	now = now.Add(time.Second)
	b.ObserveRecycle(guardStateViolationReason, 8)
	if b.State() != "open" {
		t.Fatalf("state=%s", b.State())
	}
	if b.AllowBoot(BootGuardReplacement).Allowed {
		t.Fatal("probe was allowed back during cooldown")
	}
}

func TestE07ScaleUpDoesNotBypassOpen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBreaker(&now, openSafety())
	for i := 0; i < 3; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	if b.AllowBoot(BootScaleUp).Allowed {
		t.Fatal("scale-up bypassed the open breaker")
	}
	if !b.AllowBoot(BootInitial).Allowed {
		t.Fatal("initial start is not a guard restart")
	}
}

func TestE08ShutdownDropsPendingRestart(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.RestartBackoff.Initial = time.Hour
	cfg.Workers.RestartBackoff.Maximum = time.Hour
	starter := &countingStarter{}
	p := NewWorkloadPool(PoolOptions{
		Name: "alpha", Mode: workload.ModeHTTP, Cfg: cfg, Starter: starter,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Desired: 0, Min: 0, Max: 1, MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := &slot{
		index: 1, everBooted: true, exitReason: guardStateViolationReason,
		backoff: NewBackoff(cfg.Workers.RestartBackoff),
	}
	p.mu.Lock()
	p.slots = []*slot{s}
	p.desired = 1
	p.mu.Unlock()
	p.scheduleRestart(s, true)
	cancel()
	p.wg.Wait()
	if starter.n.Load() != 0 {
		t.Fatalf("starts=%d", starter.n.Load())
	}
}

func TestE09WorkloadsAreIndependent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := NewGuardRecycleBreaker("alpha", openSafety(), func() time.Time { return now })
	b := NewGuardRecycleBreaker("beta", openSafety(), func() time.Time { return now })
	for i := 0; i < 3; i++ {
		a.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	if a.State() != "open" || b.State() != "closed" {
		t.Fatalf("alpha=%s beta=%s", a.State(), b.State())
	}
	if !b.AllowBoot(BootGuardReplacement).Allowed {
		t.Fatal("beta was blocked by alpha")
	}
}

func TestE10OtherRecycleReasonsStayOnTheExistingPath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBreaker(&now, openSafety())
	for i := 0; i < 5; i++ {
		b.ObserveRecycle("max_requests", uint64(i+1))
		b.ObserveRecycle("memory_limit", uint64(i+1))
		b.ObserveRecycle("shutdown", uint64(i+1))
	}
	if b.State() != "closed" {
		t.Fatalf("state=%s", b.State())
	}
	for i := 0; i < 3; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	if !b.AllowBoot(BootHealthyReplacement).Allowed {
		t.Fatal("memory/max-requests replacement was blocked")
	}
}

func TestE11UnknownReasonIsNotALabel(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := testBreaker(&now, openSafety())
	b.ObserveRecycle("App\\Security\\UserContext", 1)
	b.ObserveRecycle("guard_state_violation_user-42", 1)
	if b.State() != "closed" {
		t.Fatalf("state=%s", b.State())
	}
	if got := NormalizeGuardReason("App\\Security\\UserContext"); got != "other" {
		t.Fatalf("normalized=%s", got)
	}
	if got := NormalizeGuardReason(guardStateViolationReason); got != guardStateViolationReason {
		t.Fatalf("normalized=%s", got)
	}
}

func TestGuardBreakerDisabledIsLegacy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := openSafety()
	cfg.Enabled = false
	b := testBreaker(&now, cfg)
	for i := 0; i < 10; i++ {
		b.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	if !b.AllowBoot(BootGuardReplacement).Allowed || b.State() != "closed" {
		t.Fatal("disabled breaker changed restart behavior")
	}
}

func TestScaleUpBootIsSuppressedWhileOpen(t *testing.T) {
	cfg := config.Default()
	starter := &countingStarter{}
	p := NewWorkloadPool(PoolOptions{
		Name: "alpha", Mode: workload.ModeHTTP, Cfg: cfg, Starter: starter,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Desired: 0, Min: 0, Max: 2, MinReady: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p.breaker.ObserveRecycle(guardStateViolationReason, uint64(i+1))
	}
	s := &slot{index: 1, backoff: NewBackoff(cfg.Workers.RestartBackoff)}
	p.mu.Lock()
	p.slots = []*slot{s}
	p.desired = 1
	p.mu.Unlock()
	err := p.bootSlot(s, false)
	if !errors.Is(err, ErrGuardRestartSuppressed) {
		t.Fatalf("err=%v", err)
	}
	if starter.n.Load() != 0 {
		t.Fatalf("starts=%d", starter.n.Load())
	}
}
