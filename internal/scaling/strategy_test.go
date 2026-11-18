package scaling_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestFixedStrategy(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:      "w",
		Workers:   workload.WorkerPolicy{Min: 2, Max: 2},
		Scaling:   workload.DefaultScaling(),
		Resources: workload.DefaultResources(),
	}
	n := scaling.Fixed{}.DesiredWorkers(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{})
	if n != 2 {
		t.Fatalf("got %d", n)
	}
}

func TestBacklogWithoutProviderUsesMin(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:    "w",
		Workers: workload.WorkerPolicy{Min: 0, Max: 8},
		Scaling: workload.ScalingPolicy{
			Strategy:        workload.StrategyBacklog,
			AvgJobDuration:  time.Second,
			TargetDrainTime: 10 * time.Second,
		},
		Resources: workload.DefaultResources(),
	}
	n := scaling.Backlog{}.DesiredWorkers(spec, workload.WorkloadMetrics{Backlog: 100}, resources.RuntimeResources{})
	if n != 0 {
		t.Fatalf("expected min=0 without provider, got %d", n)
	}
}

type stubBacklog struct{ n int }

func (s stubBacklog) Backlog(string) (int, bool) { return s.n, true }

func TestBacklogWithProvider(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:    "w",
		Workers: workload.WorkerPolicy{Min: 1, Max: 8},
		Scaling: workload.ScalingPolicy{
			Strategy:        workload.StrategyBacklog,
			AvgJobDuration:  time.Second,
			TargetDrainTime: 10 * time.Second,
		},
		Resources: workload.DefaultResources(),
	}
	n := scaling.Backlog{Provider: stubBacklog{n: 50}}.DesiredWorkers(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{})
	// ceil(50 * 1 / 10) = 5
	if n != 5 {
		t.Fatalf("got %d want 5", n)
	}
}

func TestECO09T01ClampDesiredDoesNotReapplyMin(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:      "w",
		Workers:   workload.WorkerPolicy{Min: 4, Max: 8},
		Resources: workload.ResourcePolicy{MemoryMB: 512},
		Scaling:   workload.DefaultScaling(),
	}
	runtime := resources.RuntimeResources{
		MemoryLimitBytes: 1 << 30,
		MemoryLimitKnown: true,
	}
	n := scaling.ClampDesired(8, 4, 8, spec, runtime)
	if n > 2 {
		t.Fatalf("hard cap admitted %d, want <= 2", n)
	}
	legacy := scaling.Fixed{}.DesiredWorkers(spec, workload.WorkloadMetrics{}, runtime)
	if legacy != 4 {
		t.Fatalf("legacy desired = %d, want 4 so observe keeps the historical minimum", legacy)
	}
}

func TestECO09T10ResourcesIgnoreGOMAXPROCS(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:      "php",
		Workers:   workload.WorkerPolicy{Min: 1, Max: 64},
		Resources: workload.ResourcePolicy{Class: workload.ClassIO},
		Scaling:   workload.DefaultScaling(),
	}
	runtime := resources.RuntimeResources{
		AvailableCPUs: 2,
		GOMAXPROCS:    1,
		LogicalCPUs:   8,
	}
	ev := scaling.Resources{}.Evaluate(spec, workload.WorkloadMetrics{}, runtime)
	if ev.RawDemand != 8 {
		t.Fatalf("raw demand = %d, want 8 (2 vCPU * io 4); GOMAXPROCS must not cap PHP workers", ev.RawDemand)
	}
}

func TestBacklogEvaluateReadsProviderOnce(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:    "jobs",
		Workers: workload.WorkerPolicy{Min: 1, Max: 8},
		Scaling: workload.ScalingPolicy{
			Strategy:        workload.StrategyBacklog,
			AvgJobDuration:  time.Second,
			TargetDrainTime: 10 * time.Second,
		},
		Resources: workload.DefaultResources(),
	}
	counter := &countingBacklog{n: 50}
	ev := (scaling.Backlog{Provider: counter}).Evaluate(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{})
	if counter.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", counter.calls)
	}
	if ev.RawDemand != 5 || ev.LegacyTarget != 5 {
		t.Fatalf("evaluation = %+v, want raw 5 legacy 5", ev)
	}
}

type countingBacklog struct {
	n     int
	calls int
}

func (c *countingBacklog) Backlog(string) (int, bool) {
	c.calls++
	return c.n, true
}

func TestResourceClamp(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:      "w",
		Workers:   workload.WorkerPolicy{Min: 1, Max: 100},
		Resources: workload.ResourcePolicy{Class: workload.ClassCPU, MemoryMB: 1024},
		Scaling:   workload.DefaultScaling(),
	}
	runtime := resources.RuntimeResources{
		AvailableCPUs:    2,
		MemoryLimitBytes: 2 * 1024 * 1024 * 1024,
		MemoryLimitKnown: true,
	}
	n := scaling.ClampDesired(100, 1, 100, spec, runtime)
	// CPU class: ceil(2*1)=2; memory: 2GiB/1GiB=2 → 2
	if n != 2 {
		t.Fatalf("got %d want 2", n)
	}
}

func TestHysteresisScaleDownDelay(t *testing.T) {
	inner := scaling.Fixed{}
	h := scaling.NewHysteresis(fixedAt{n: 4})
	_ = inner
	spec := workload.ResolvedWorkloadSpec{
		Name:    "w",
		Workers: workload.WorkerPolicy{Min: 0, Max: 8},
		Scaling: workload.ScalingPolicy{
			Strategy:         workload.StrategyFixed,
			ScaleUpCooldown:  time.Millisecond,
			ScaleDownIdleFor: 50 * time.Millisecond,
		},
		Resources: workload.DefaultResources(),
	}
	// Prime at 4
	h.Inner = fixedAt{n: 4}
	if got := h.DesiredWorkers(spec, workload.WorkloadMetrics{Desired: 4}, resources.RuntimeResources{}); got != 4 {
		t.Fatalf("prime=%d", got)
	}
	// Request down to 1 — should hold at 4 until idle window passes
	h.Inner = fixedAt{n: 1}
	if got := h.DesiredWorkers(spec, workload.WorkloadMetrics{Desired: 4}, resources.RuntimeResources{}); got != 4 {
		t.Fatalf("immediate down=%d want 4", got)
	}
	time.Sleep(60 * time.Millisecond)
	if got := h.DesiredWorkers(spec, workload.WorkloadMetrics{Desired: 4}, resources.RuntimeResources{}); got != 1 {
		t.Fatalf("after idle=%d want 1", got)
	}
}

type fixedAt struct{ n int }

func (f fixedAt) DesiredWorkers(workload.ResolvedWorkloadSpec, workload.WorkloadMetrics, resources.RuntimeResources) int {
	return f.n
}
