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

// ClampDesired clamps n into [min,max] then applies resource headroom.
func ClampDesired(n, min, max int, spec workload.ResolvedWorkloadSpec, runtime resources.RuntimeResources) int {
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
	// Memory: if workload declares memory_mb and host limit known, clamp by envelope.
	if spec.Resources.MemoryMB > 0 && runtime.MemoryLimitKnown && runtime.MemoryLimitBytes > 0 {
		per := uint64(spec.Resources.MemoryMB) * 1024 * 1024
		if per > 0 {
			maxByMem := int(runtime.MemoryLimitBytes / per)
			if maxByMem < n {
				n = maxByMem
			}
		}
	}
	// CPU contributes a soft ceiling but never alone decides (combined with memory clamp above).
	if runtime.AvailableCPUs > 0 {
		var perCPU float64
		switch spec.Resources.Class {
		case workload.ClassCPU:
			perCPU = 1.0
		case workload.ClassIO:
			perCPU = 4.0
		default:
			perCPU = 2.0
		}
		maxByCPU := int(math.Ceil(runtime.AvailableCPUs * perCPU))
		if maxByCPU < 1 {
			maxByCPU = 1
		}
		if maxByCPU < n {
			n = maxByCPU
		}
	}
	return n
}

// Fixed keeps capacity at workers.max (steady configured size).
type Fixed struct{}

func (Fixed) DesiredWorkers(spec workload.ResolvedWorkloadSpec, _ workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	desired := spec.Workers.Max
	if desired < spec.Workers.Min {
		desired = spec.Workers.Min
	}
	return ClampDesired(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime)
}

// Backlog scales from broker depth when a provider is available.
type Backlog struct {
	Provider BacklogProvider
}

func (b Backlog) DesiredWorkers(spec workload.ResolvedWorkloadSpec, metrics workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	backlog := metrics.Backlog
	if b.Provider != nil {
		if v, ok := b.Provider.Backlog(spec.Name); ok {
			backlog = v
		} else {
			// No provider data → stay at min (spec: first version may use fixed).
			return ClampDesired(spec.Workers.Min, spec.Workers.Min, spec.Workers.Max, spec, runtime)
		}
	} else {
		return ClampDesired(spec.Workers.Min, spec.Workers.Min, spec.Workers.Max, spec, runtime)
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
	return ClampDesired(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime)
}

// Resources sizes from CPU/memory class without backlog.
type Resources struct{}

func (Resources) DesiredWorkers(spec workload.ResolvedWorkloadSpec, _ workload.WorkloadMetrics, runtime resources.RuntimeResources) int {
	var perCPU float64
	switch spec.Resources.Class {
	case workload.ClassCPU:
		perCPU = 1.0
	case workload.ClassIO:
		perCPU = 4.0
	default:
		perCPU = 2.0
	}
	cpus := runtime.AvailableCPUs
	if cpus <= 0 {
		cpus = float64(runtime.LogicalCPUs)
	}
	if cpus <= 0 {
		cpus = 1
	}
	desired := int(math.Ceil(cpus * perCPU))
	return ClampDesired(desired, spec.Workers.Min, spec.Workers.Max, spec, runtime)
}
