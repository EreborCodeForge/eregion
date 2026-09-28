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
		Name:    "w",
		Workers: workload.WorkerPolicy{Min: 2, Max: 2},
		Scaling: workload.DefaultScaling(),
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
