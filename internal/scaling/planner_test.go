package scaling_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

const (
	mib = 1024 * 1024
	gib = 1024 * mib
)

func budgetSample(current uint64) resources.Sample {
	return resources.Sample{
		At:                 time.Unix(100, 0),
		Source:             resources.SourceCgroupV2,
		Scope:              "cgroup",
		HierarchyKnown:     true,
		MemoryLimitBytes:   gib,
		MemoryLimitValid:   resources.ValidityKnown,
		MemoryCurrentBytes: current,
		MemoryCurrentValid: resources.ValidityKnown,
	}
}

func budgetMemory() config.MemoryGuardConfig {
	return config.MemoryGuardConfig{
		ReserveMB:        128,
		ReservePercent:   0,
		WorkerFallbackMB: 256,
		WarningRatio:     0.80,
		CriticalRatio:    0.90,
		HardRatio:        0.95,
	}
}

func find(t *testing.T, decisions []scaling.CapacityDecision, name string) scaling.CapacityDecision {
	t.Helper()
	for _, d := range decisions {
		if d.Workload == name {
			return d
		}
	}
	t.Fatalf("missing %s in %#v", name, decisions)
	return scaling.CapacityDecision{}
}

func TestECO09T02MinFitsWithoutWarning(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(0),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 2, Max: 2, Requested: 2, Policy: 2, MemoryMB: 128,
		}},
	})
	got := find(t, decisions, "http")
	if got.Admitted != 2 || got.Severity != scaling.SeverityOK || got.ConstrainedBy != scaling.ReasonNone {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T06SharedBudgetAndFairShare(t *testing.T) {
	// 1 GiB limit, 256 MiB current, 128 MiB reserve, 256 MiB workers → 2 new workers.
	base := scaling.PlanRequest{
		Mode:    config.ResourceGuardEnforce,
		Memory:  budgetMemory(),
		Sample:  budgetSample(256 * mib),
		Now:     time.Unix(100, 0),
		Startup: true,
	}
	one := func(name string) scaling.WorkloadDemand {
		return scaling.WorkloadDemand{Name: name, Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256}
	}
	first := append([]scaling.WorkloadDemand{}, one("b"), one("a"))
	second := append([]scaling.WorkloadDemand{}, one("a"), one("b"))
	base.Demands = first
	left := scaling.Plan(base)
	base.Demands = second
	right := scaling.Plan(base)
	if find(t, left, "a").Safe != find(t, right, "a").Safe || find(t, left, "b").Safe != find(t, right, "b").Safe {
		t.Fatalf("order changed the plan: %+v vs %+v", left, right)
	}
	if find(t, left, "a").Safe != 1 || find(t, left, "b").Safe != 1 {
		t.Fatalf("fair share = a:%d b:%d, want 1 and 1", find(t, left, "a").Safe, find(t, left, "b").Safe)
	}
	if find(t, left, "a").Safe+find(t, left, "b").Safe > 2 {
		t.Fatal("allocation exceeded headroom")
	}
}

func TestECO09T08NoStarvation(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(256 * mib),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{
			{Name: "zzz", Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256},
			{Name: "aaa", Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256},
		},
	})
	if find(t, decisions, "zzz").Safe == 2 && find(t, decisions, "aaa").Safe == 0 {
		t.Fatal("first map entry took the whole budget")
	}
}

func TestECO09T09ConsumerMinZero(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(0),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "jobs", Min: 0, Max: 4, Requested: 0, Policy: 0, MemoryMB: 128,
		}},
	})
	got := find(t, decisions, "jobs")
	if got.Admitted != 0 || got.Severity != scaling.SeverityOK {
		t.Fatalf("%+v", got)
	}
	later := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(0),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "jobs", Min: 0, Max: 4, Requested: 2, Policy: 2, MemoryMB: 128,
		}},
	})
	if find(t, later, "jobs").Admitted != 2 {
		t.Fatalf("later scale admitted %d, want 2", find(t, later, "jobs").Admitted)
	}
}

func TestECO09T04ObserveKeepsLegacyTarget(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardObserve,
		Memory: budgetMemory(),
		Sample: budgetSample(256 * mib),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 4, Max: 8, Requested: 8, Policy: 4, MemoryMB: 256,
		}},
	})
	got := find(t, decisions, "http")
	if got.Admitted != 4 {
		t.Fatalf("observe applied %d, want the legacy policy 4", got.Admitted)
	}
	if got.Safe > 2 || got.WouldLimit < 1 {
		t.Fatalf("would-limit not visible: %+v", got)
	}
}

func TestECO09T05OffDoesNotPublishABlock(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardOff,
		Memory: budgetMemory(),
		Sample: budgetSample(256 * mib),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 4, Max: 8, Requested: 8, Policy: 4, MemoryMB: 256,
		}},
	})
	got := find(t, decisions, "http")
	if got.Admitted != 4 || got.Limited != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T25CPUSoftCapDoesNotShrink(t *testing.T) {
	sample := budgetSample(0)
	sample.CPUUtilization = 0.95
	sample.CPUUtilizationValid = resources.ValidityKnown
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		CPU:    config.CPUGuardConfig{WarningUtilizationRatio: 0.85, CriticalThrottlingRatio: 0.20},
		Sample: sample,
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 8, Requested: 8, Policy: 4, Running: 2, MemoryMB: 128,
		}},
	})
	got := find(t, decisions, "http")
	if got.Safe != 2 || got.ConstrainedBy != scaling.ReasonCPUSoftCap {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T26CPUThrottlingIsCritical(t *testing.T) {
	sample := budgetSample(0)
	sample.CPUThrottledRatio = 0.4
	sample.CPUThrottlingValid = resources.ValidityKnown
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		CPU:    config.CPUGuardConfig{WarningUtilizationRatio: 0.85, CriticalThrottlingRatio: 0.20},
		Sample: sample,
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 8, Requested: 4, Policy: 4, Running: 1, MemoryMB: 128,
		}},
	})
	got := find(t, decisions, "http")
	if got.ConstrainedBy != scaling.ReasonCPUThrottling || got.Severity != scaling.SeverityCritical || got.Safe != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T27MemoryCriticalBlocksGrowth(t *testing.T) {
	decisions := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(96 * gib / 100),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 8, Requested: 4, Policy: 4, Running: 1, MemoryMB: 128,
		}},
	})
	got := find(t, decisions, "http")
	if got.Severity != scaling.SeverityCritical || got.Safe != 1 {
		t.Fatalf("%+v", got)
	}
	if got.ConstrainedBy != scaling.ReasonMemoryPressure {
		t.Fatalf("reason=%s", got.ConstrainedBy)
	}
}

func TestECO09T16StaleSampleBlocksAdmit(t *testing.T) {
	ledger := scaling.NewLedger()
	sample := budgetSample(0)
	ledger.SetEconomics(sample, map[string]uint64{"http": 256 * mib}, budgetMemory(), time.Second, sample.At.Add(10*time.Second))
	_, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 1, Generation: 1})
	if err == nil || !worker.IsAdmissionDenial(err) {
		t.Fatalf("err=%v", err)
	}
}

func TestECO09T18ConcurrentReservationsDoNotOversubscribe(t *testing.T) {
	ledger := scaling.NewLedger()
	sample := budgetSample(256 * mib)
	ledger.SetEconomics(sample, map[string]uint64{"a": 256 * mib, "b": 256 * mib}, budgetMemory(), time.Minute, sample.At)
	// 640 MiB headroom, each worker 256 → two succeed, the third fails.
	type result struct {
		ok bool
	}
	ch := make(chan result, 3)
	for i, name := range []string{"a", "b", "a"} {
		go func(slot int, name string) {
			_, err := ledger.Admit(worker.SpawnRequest{Workload: name, Slot: slot, Generation: 1})
			ch <- result{ok: err == nil}
		}(i+1, name)
	}
	ok := 0
	for i := 0; i < 3; i++ {
		if (<-ch).ok {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("admitted %d, want 2", ok)
	}
	if ledger.PendingBytes() != 512*mib {
		t.Fatalf("pending=%d", ledger.PendingBytes())
	}
}

func TestECO09T19ReleaseOnce(t *testing.T) {
	ledger := scaling.NewLedger()
	sample := budgetSample(0)
	ledger.SetEconomics(sample, map[string]uint64{"http": 256 * mib}, budgetMemory(), time.Minute, sample.At)
	grant, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 1, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	ledger.Release(grant)
	ledger.Release(grant)
	if ledger.PendingBytes() != 0 {
		t.Fatalf("pending=%d", ledger.PendingBytes())
	}
}

func TestECO09T20ExitReleaseOnce(t *testing.T) {
	ledger := scaling.NewLedger()
	sample := budgetSample(0)
	ledger.SetEconomics(sample, map[string]uint64{"http": 256 * mib}, budgetMemory(), time.Minute, sample.At)
	grant, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 1, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	ledger.Commit(grant)
	ledger.ReleaseExit(grant)
	ledger.ReleaseExit(grant)
	if ledger.PendingBytes() != 0 {
		t.Fatalf("pending=%d", ledger.PendingBytes())
	}
}

func TestECO09T21ReplacementDoesNotDoubleCharge(t *testing.T) {
	ledger := scaling.NewLedger()
	mem := budgetMemory()
	initial := budgetSample(gib - 128*mib - 256*mib)
	ledger.SetEconomics(initial, map[string]uint64{"http": 256 * mib}, mem, time.Minute, initial.At)
	grant, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 1, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	ledger.Commit(grant)
	full := initial
	full.At = initial.At.Add(time.Second)
	full.MemoryCurrentBytes = gib - 128*mib
	ledger.SetEconomics(full, map[string]uint64{"http": 256 * mib}, mem, time.Minute, full.At)
	if ledger.PendingBytes() != 0 {
		t.Fatalf("committed worker still pending after a newer sample: %d", ledger.PendingBytes())
	}
	ledger.ReleaseExit(grant)
	before := ledger.PendingBytes()
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 1, Generation: 2, Replacement: true}); err != nil {
		t.Fatal(err)
	}
	if ledger.PendingBytes() != before {
		t.Fatalf("replacement changed pending %d → %d", before, ledger.PendingBytes())
	}
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 2, Generation: 1}); err == nil {
		t.Fatal("a new worker was admitted with no headroom")
	}
}

func rssSample(at time.Time, limit, current uint64) resources.Sample {
	sample := budgetSample(current)
	sample.At = at
	sample.MemoryLimitBytes = limit
	return sample
}

// TestECO09LedgerBudgetFollowsReservationThenRSS walks the three closure cases:
// a reservation blocks projected growth, a later sample replaces that reservation
// with real RSS, and RSS growth of workers already admitted blocks only new spawns.
func TestECO09LedgerBudgetFollowsReservationThenRSS(t *testing.T) {
	const limit = 512 * mib
	mem := budgetMemory()
	mem.ReserveMB = 64
	cost := map[string]uint64{"http": 128 * mib}
	ledger := scaling.NewLedger()

	t0 := time.Unix(200, 0)
	ledger.SetEconomics(rssSample(t0, limit, 32*mib), cost, mem, time.Minute, t0)
	grants := make([]worker.SpawnGrant, 0, 3)
	for slot := 1; slot <= 3; slot++ {
		grant, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: slot, Generation: 1})
		if err != nil {
			t.Fatalf("initial admit %d: %v", slot, err)
		}
		grants = append(grants, grant)
	}
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 4, Generation: 1}); err == nil {
		t.Fatal("fourth admission ignored the pending reservation")
	}
	if ledger.PendingBytes() != 384*mib {
		t.Fatalf("pending reservation=%d", ledger.PendingBytes())
	}
	for _, grant := range grants {
		ledger.Commit(grant)
	}

	t1 := t0.Add(time.Second)
	ledger.SetEconomics(rssSample(t1, limit, 80*mib), cost, mem, time.Minute, t1)
	if ledger.PendingBytes() != 0 {
		t.Fatalf("RSS reconcile left the reservation pending: %d", ledger.PendingBytes())
	}
	fourth, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 4, Generation: 1})
	if err != nil {
		t.Fatalf("real RSS reopened headroom but admission failed: %v", err)
	}
	ledger.Commit(fourth)

	t2 := t1.Add(time.Second)
	grown := rssSample(t2, limit, 420*mib)
	ledger.SetEconomics(grown, cost, mem, time.Minute, t2)
	if ledger.PendingBytes() != 0 {
		t.Fatalf("grown RSS was added on top of the old reservation: pending=%d", ledger.PendingBytes())
	}
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 5, Generation: 1}); err == nil {
		t.Fatal("admission succeeded after admitted workers grew past the budget")
	}

	decision := find(t, scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: mem,
		Sample: grown,
		Now:    t2,
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 8, Requested: 8, Policy: 8, Running: 4, MemoryMB: 128,
		}},
	}), "http")
	if decision.Admitted != 4 || decision.Current != 4 {
		t.Fatalf("workers already admitted were interrupted: %+v", decision)
	}
	if decision.ConstrainedBy != scaling.ReasonMemoryHeadroom {
		t.Fatalf("reason=%s", decision.ConstrainedBy)
	}
}

func TestECO09T37PreflightUnknownLimit(t *testing.T) {
	cfg := config.Default()
	cfg.ResourceGuard.Mode = config.ResourceGuardEnforce
	cfg.ResourceGuard.Memory.HardLimitMB = 0
	guard := &scaling.Guard{
		Cfg: cfg.ResourceGuard,
		Collector: &resources.Collector{Sampler: resources.SamplerFunc(func(context.Context) (resources.Sample, error) {
			return resources.UnknownSample(time.Unix(1, 0)), nil
		})},
		Ledger: scaling.NewLedger(),
	}
	spec := workload.ResolvedWorkloadSpec{
		Name: "http", Mode: workload.ModeHTTP,
		Workers: workload.WorkerPolicy{Min: 1, Max: 1},
	}
	err := guard.Preflight(context.Background(), []workload.ResolvedWorkloadSpec{spec}, nil)
	if !errors.Is(err, scaling.ErrResourceLimitUnknown) {
		t.Fatalf("err=%v", err)
	}
}

func TestECO09T07PermutedSpecsProduceTheSamePlan(t *testing.T) {
	mem := budgetMemory()
	sample := budgetSample(256 * mib)
	now := time.Unix(100, 0)
	demands := []scaling.WorkloadDemand{
		{Name: "c", Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256},
		{Name: "a", Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256},
		{Name: "b", Min: 0, Max: 8, Requested: 8, Policy: 8, MemoryMB: 256},
	}
	base := scaling.Plan(scaling.PlanRequest{Mode: config.ResourceGuardEnforce, Memory: mem, Sample: sample, Now: now, Demands: demands})
	reversed := append([]scaling.WorkloadDemand{}, demands...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	other := scaling.Plan(scaling.PlanRequest{Mode: config.ResourceGuardEnforce, Memory: mem, Sample: sample, Now: now, Demands: reversed})
	for _, name := range []string{"a", "b", "c"} {
		left, right := find(t, base, name), find(t, other, name)
		if left.Safe != right.Safe || left.Admitted != right.Admitted || left.ConstrainedBy != right.ConstrainedBy {
			t.Fatalf("%s diverged: %+v vs %+v", name, left, right)
		}
	}
}

func TestECO09T17InvalidSampleBlocksThenFreshSampleAllowsGrowth(t *testing.T) {
	unknown := resources.UnknownSample(time.Unix(1, 0))
	blocked := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: unknown,
		Now:    time.Unix(2, 0),
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 4, Requested: 4, Policy: 4, Running: 1, MemoryMB: 128,
		}},
	})
	got := find(t, blocked, "http")
	if got.ConstrainedBy != scaling.ReasonUnknownResource || got.Severity != scaling.SeverityDegraded || got.Safe != 1 || got.Admitted != 1 {
		t.Fatalf("invalid sample still grew: %+v", got)
	}
	ledger := scaling.NewLedger()
	ledger.SetEconomics(unknown, map[string]uint64{"http": 128 * mib}, budgetMemory(), time.Minute, time.Unix(2, 0))
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 2, Generation: 1}); err == nil || !worker.IsAdmissionDenial(err) {
		t.Fatalf("admit during unknown sample: %v", err)
	}
	fresh := budgetSample(0)
	ledger.SetEconomics(fresh, map[string]uint64{"http": 128 * mib}, budgetMemory(), time.Minute, fresh.At)
	if _, err := ledger.Admit(worker.SpawnRequest{Workload: "http", Slot: 2, Generation: 1}); err != nil {
		t.Fatalf("fresh sample still blocked: %v", err)
	}
	recovered := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: fresh,
		Now:    fresh.At,
		Demands: []scaling.WorkloadDemand{{
			Name: "http", Min: 1, Max: 4, Requested: 4, Policy: 4, Running: 1, MemoryMB: 128,
		}},
	})
	if find(t, recovered, "http").Admitted < 2 {
		t.Fatalf("recovered plan did not allow growth: %+v", find(t, recovered, "http"))
	}
}

type depthBacklog struct {
	n     int
	depth int
}

func (c *depthBacklog) Backlog(string) (int, bool) {
	c.n++
	return c.depth, true
}

func TestECO09T36MixedStrategiesKeepTheirContracts(t *testing.T) {
	runtime := resources.RuntimeResources{AvailableCPUs: 2, LogicalCPUs: 2}
	http := workload.ResolvedWorkloadSpec{
		Name: "http", Mode: workload.ModeHTTP,
		Workers:   workload.WorkerPolicy{Min: 1, Max: 4},
		Resources: workload.ResourcePolicy{MemoryMB: 128, Class: workload.ClassIO},
		Scaling:   workload.ScalingPolicy{Strategy: workload.StrategyFixed},
	}
	jobs := workload.ResolvedWorkloadSpec{
		Name: "jobs", Mode: workload.ModeConsumer,
		Workers:   workload.WorkerPolicy{Min: 0, Max: 16},
		Resources: workload.ResourcePolicy{MemoryMB: 128, Class: workload.ClassIO},
		Scaling:   workload.ScalingPolicy{Strategy: workload.StrategyResources},
	}
	mail := workload.ResolvedWorkloadSpec{
		Name: "mail", Mode: workload.ModeConsumer,
		Workers: workload.WorkerPolicy{Min: 0, Max: 8},
		Scaling: workload.ScalingPolicy{Strategy: workload.StrategyBacklog, AvgJobDuration: time.Second, TargetDrainTime: 10 * time.Second},
	}
	provider := &depthBacklog{depth: 20}
	fixed := scaling.Fixed{}.Evaluate(http, workload.WorkloadMetrics{}, runtime)
	resourcesEval := scaling.Resources{}.Evaluate(jobs, workload.WorkloadMetrics{}, runtime)
	backlog := scaling.Backlog{Provider: provider}.Evaluate(mail, workload.WorkloadMetrics{}, runtime)
	if provider.n != 1 {
		t.Fatalf("backlog provider calls=%d, want 1", provider.n)
	}
	if fixed.LegacyTarget != 4 || resourcesEval.RawDemand != 8 || backlog.LegacyTarget != 2 {
		t.Fatalf("fixed=%d resources=%d backlog=%d", fixed.LegacyTarget, resourcesEval.RawDemand, backlog.LegacyTarget)
	}
	observe := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardObserve,
		Memory: budgetMemory(),
		Sample: budgetSample(0),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{
			{Name: "http", Strategy: "fixed", Min: 1, Max: 4, Requested: fixed.RawDemand, Policy: fixed.LegacyTarget, MemoryMB: 128},
			{Name: "jobs", Strategy: "resources", Min: 0, Max: 16, Requested: resourcesEval.RawDemand, Policy: resourcesEval.LegacyTarget, MemoryMB: 128},
			{Name: "mail", Strategy: "backlog", Min: 0, Max: 8, Requested: backlog.RawDemand, Policy: backlog.LegacyTarget, MemoryMB: 128},
		},
	})
	if find(t, observe, "http").Admitted != 4 || find(t, observe, "jobs").Admitted != 8 || find(t, observe, "mail").Admitted != 2 {
		t.Fatalf("observe changed strategy targets: %+v", observe)
	}
	enforced := scaling.Plan(scaling.PlanRequest{
		Mode:   config.ResourceGuardEnforce,
		Memory: budgetMemory(),
		Sample: budgetSample(256 * mib),
		Now:    time.Unix(100, 0),
		Demands: []scaling.WorkloadDemand{
			{Name: "http", Strategy: "fixed", Min: 1, Max: 4, Requested: 4, Policy: 4, MemoryMB: 256},
			{Name: "jobs", Strategy: "resources", Min: 0, Max: 16, Requested: 8, Policy: 8, MemoryMB: 256},
			{Name: "mail", Strategy: "backlog", Min: 0, Max: 8, Requested: 2, Policy: 2, MemoryMB: 256},
		},
	})
	total := find(t, enforced, "http").Admitted + find(t, enforced, "jobs").Admitted + find(t, enforced, "mail").Admitted
	if total > 2 {
		t.Fatalf("enforce admitted %d workers into a 2-worker headroom", total)
	}
	if find(t, enforced, "http").Admitted < 1 {
		t.Fatalf("http minimum was dropped: %+v", find(t, enforced, "http"))
	}
}
