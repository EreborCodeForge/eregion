package scaling

import (
	"math"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Strategy computes the desired worker count for a workload.
type Strategy interface {
	DesiredWorkers(
		spec workload.ResolvedWorkloadSpec,
		metrics workload.WorkloadMetrics,
		runtime resources.RuntimeResources,
	) int
}

// BacklogProvider supplies broker backlog depth. Nil/absent → backlog strategy uses min.
type BacklogProvider interface {
	Backlog(workloadName string) (int, bool)
}

// StrategyEvaluation separates raw demand from the historical clamp.
// LegacyTarget matches the pre-ECO-09 DesiredWorkers result, including min reapplied after a hard cap.
type StrategyEvaluation struct {
	RawDemand    int
	LegacyTarget int
}

// StrategyEvaluator computes raw demand and the legacy target in one pass.
// Backlog providers are consulted at most once.
type StrategyEvaluator interface {
	Evaluate(
		spec workload.ResolvedWorkloadSpec,
		metrics workload.WorkloadMetrics,
		runtime resources.RuntimeResources,
	) StrategyEvaluation
}

// Registry selects a strategy by name.
type Registry struct {
	Fixed     Strategy
	Backlog   Strategy
	Resources Strategy
}

// NewRegistry returns the built-in strategies.
func NewRegistry(backlog BacklogProvider) *Registry {
	return &Registry{
		Fixed:     Fixed{},
		Backlog:   Backlog{Provider: backlog},
		Resources: Resources{},
	}
}

// For returns the strategy for a policy name (defaults to fixed).
func (r *Registry) For(name workload.ScalingStrategyName) Strategy {
	switch name {
	case workload.StrategyBacklog:
		return r.Backlog
	case workload.StrategyResources:
		return r.Resources
	default:
		return r.Fixed
	}
}

// ClampDesired clamps n into [min,max] and then applies a memory hard cap.
// workers.min is not reapplied after that hard cap. A CPU ceiling is soft:
// without a memory hard cap it does not pull the result below min.
func ClampDesired(n, min, max int, spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) int {
	if min < 0 {
		min = 0
	}
	if n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	if n < 0 {
		n = 0
	}
	capped, hard := memoryHardCap(n, spec, runtime)
	if hard {
		n = capped
		if cpu, ok := cpuSoftCap(spec, runtime); ok && cpu < n {
			n = cpu
		}
	} else if cpu, ok := cpuSoftCap(spec, runtime); ok && cpu < n {
		if cpu < min {
			n = min
		} else {
			n = cpu
		}
	}
	if max > 0 && n > max {
		n = max
	}
	if n < 0 {
		n = 0
	}
	return n
}

// clampLegacy preserves the historical ClampDesired result used by DesiredWorkers.
// off and observe keep this target so existing projects do not change size.
func clampLegacy(n, min, max int, spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) int {
	if n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	n = applyResourceClamp(n, spec, runtime)
	if n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	return n
}

func applyResourceClamp(n int, spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) int {
	if n <= 0 {
		return n
	}
	if capped, hard := memoryHardCap(n, spec, runtime); hard {
		n = capped
	}
	if cpu, ok := cpuSoftCap(spec, runtime); ok && cpu < n {
		n = cpu
	}
	return n
}

func memoryHardCap(n int, spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) (int, bool) {
	if n <= 0 || spec.Resources.MemoryMB <= 0 || !runtime.MemoryLimitKnown || runtime.MemoryLimitBytes == 0 {
		return n, false
	}
	per := uint64(spec.Resources.MemoryMB) * 1024 * 1024
	if per == 0 {
		return n, false
	}
	maxByMem := int(runtime.MemoryLimitBytes / per)
	if maxByMem < n {
		return maxByMem, true
	}
	return n, false
}

func cpuSoftCap(spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) (int, bool) {
	if runtime.AvailableCPUs <= 0 {
		return 0, false
	}
	perCPU := workersPerCPU(spec.Resources.Class)
	maxByCPU := int(math.Ceil(runtime.AvailableCPUs * perCPU))
	if maxByCPU < 1 {
		maxByCPU = 1
	}
	return maxByCPU, true
}

func workersPerCPU(class workload.ResourceClass) float64 {
	switch class {
	case workload.ClassCPU:
		return 1
	case workload.ClassIO:
		return 4
	default:
		return 2
	}
}

func evaluateInner(
	inner Strategy,
	spec workload.ResolvedWorkloadSpec,
	metrics workload.WorkloadMetrics,
	runtime resources.RuntimeResources,
) StrategyEvaluation {
	if ev, ok := inner.(StrategyEvaluator); ok {
		return ev.Evaluate(spec, metrics, runtime)
	}
	n := inner.DesiredWorkers(spec, metrics, runtime)
	return StrategyEvaluation{RawDemand: n, LegacyTarget: n}
}

// Fixed keeps capacity at workers.max (steady configured size).
type Fixed struct{}

func (f Fixed) DesiredWorkers(spec workload.ResolvedWorkloadSpec, metrics workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	return f.Evaluate(spec, metrics, runtime).LegacyTarget
}

func (Fixed) Evaluate(spec workload.ResolvedWorkloadSpec, _ workload.WorkloadMetrics, runtime resources.RuntimeResources) StrategyEvaluation {
	desired := spec.Workers.Max
	if desired < spec.Workers.Min {
		desired = spec.Workers.Min
	}
	return StrategyEvaluation{
		RawDemand:    desired,
		LegacyTarget: clampLegacy(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime),
	}
}

// Backlog scales from broker depth when a provider is available.
type Backlog struct {
	Provider BacklogProvider
}

func (b Backlog) DesiredWorkers(spec workload.ResolvedWorkloadSpec, metrics workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	return b.Evaluate(spec, metrics, runtime).LegacyTarget
}

func (b Backlog) Evaluate(spec workload.ResolvedWorkloadSpec, metrics workload.WorkloadMetrics, runtime resources.RuntimeResources) StrategyEvaluation {
	backlog := metrics.Backlog
	if b.Provider != nil {
		if v, ok := b.Provider.Backlog(spec.Name); ok {
			backlog = v
		} else {
			return legacyMin(spec, runtime)
		}
	} else {
		return legacyMin(spec, runtime)
	}

	avg := spec.Scaling.AvgJobDuration.Seconds()
	drain := spec.Scaling.TargetDrainTime.Seconds()
	if avg <= 0 {
		avg = 1
	}
	if drain <= 0 {
		drain = 10
	}
	desired := int(math.Ceil(float64(backlog) * avg / drain))
	return StrategyEvaluation{
		RawDemand:    desired,
		LegacyTarget: clampLegacy(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime),
	}
}

func legacyMin(spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) StrategyEvaluation {
	return StrategyEvaluation{
		RawDemand:    spec.Workers.Min,
		LegacyTarget: clampLegacy(spec.Workers.Min, spec.Workers.Min, spec.Workers.Max, spec, runtime),
	}
}

// Resources sizes from CPU/memory class without backlog.
type Resources struct{}

func (r Resources) DesiredWorkers(spec workload.ResolvedWorkloadSpec, metrics workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	return r.Evaluate(spec, metrics, runtime).LegacyTarget
}

func (Resources) Evaluate(spec workload.ResolvedWorkloadSpec, _ workload.WorkloadMetrics, runtime resources.RuntimeResources) StrategyEvaluation {
	cpus := runtime.AvailableCPUs
	if cpus <= 0 {
		cpus = float64(runtime.LogicalCPUs)
	}
	if cpus <= 0 {
		cpus = 1
	}
	desired := int(math.Ceil(cpus * workersPerCPU(spec.Resources.Class)))
	return StrategyEvaluation{
		RawDemand:    desired,
		LegacyTarget: clampLegacy(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime),
	}
}
