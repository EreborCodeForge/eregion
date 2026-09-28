package workload

import (
	"fmt"
	"time"
)

// DefaultScaling returns sensible defaults matching the workloads spec.
func DefaultScaling() ScalingPolicy {
	return ScalingPolicy{
		Strategy:         StrategyFixed,
		ScaleUpCooldown:  time.Second,
		ScaleDownIdleFor: 30 * time.Second,
		AvgJobDuration:   time.Second,
		TargetDrainTime:  10 * time.Second,
	}
}

// DefaultResources returns balanced class with no memory override (0 = inherit runtime).
func DefaultResources() ResourcePolicy {
	return ResourcePolicy{Class: ClassBalanced, MemoryMB: 0}
}

// Resolve merges template + workload overrides into a ResolvedWorkloadSpec.
// Workload overrides take precedence over the template.
func Resolve(spec WorkloadSpec, templates map[string]WorkloadTemplate) (ResolvedWorkloadSpec, error) {
	if spec.Name == "" {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload name is required")
	}

	out := ResolvedWorkloadSpec{
		Name:      spec.Name,
		Template:  spec.Template,
		Mode:      spec.Mode,
		Command:   append([]string(nil), spec.Command...),
		Queue:     spec.Queue,
		Workers:   WorkerPolicy{Min: 1, Max: 1},
		Resources: DefaultResources(),
		Scaling:   DefaultScaling(),
	}

	if spec.Template != "" {
		tpl, ok := templates[spec.Template]
		if !ok {
			return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q references unknown template %q", spec.Name, spec.Template)
		}
		if out.Mode == "" {
			out.Mode = tpl.Mode
		}
		out.Workers = tpl.Workers
		out.Resources = tpl.Resources
		if out.Resources.Class == "" {
			out.Resources.Class = ClassBalanced
		}
		out.Scaling = mergeScaling(DefaultScaling(), tpl.Scaling)
	}

	if spec.Mode != "" {
		out.Mode = spec.Mode
	}
	if spec.Workers != nil {
		if spec.Workers.Min != nil {
			out.Workers.Min = *spec.Workers.Min
		}
		if spec.Workers.Max != nil {
			out.Workers.Max = *spec.Workers.Max
		}
	}
	if spec.Resources != nil {
		if spec.Resources.Class != "" {
			out.Resources.Class = spec.Resources.Class
		}
		if spec.Resources.MemoryMB > 0 {
			out.Resources.MemoryMB = spec.Resources.MemoryMB
		}
	}
	if spec.Scaling != nil {
		out.Scaling = mergeScaling(out.Scaling, *spec.Scaling)
	}

	if out.Mode == "" {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: mode is required", spec.Name)
	}
	if out.Mode != ModeHTTP && out.Mode != ModeConsumer {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: unsupported mode %q", spec.Name, out.Mode)
	}
	if out.Workers.Max < out.Workers.Min {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: workers.max (%d) < workers.min (%d)", spec.Name, out.Workers.Max, out.Workers.Min)
	}
	if out.Workers.Min < 0 {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: workers.min must be >= 0", spec.Name)
	}
	if out.Mode == ModeConsumer && len(out.Command) == 0 {
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: consumer mode requires command argv", spec.Name)
	}
	switch out.Resources.Class {
	case ClassIO, ClassCPU, ClassBalanced, "":
		if out.Resources.Class == "" {
			out.Resources.Class = ClassBalanced
		}
	default:
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: invalid resource class %q", spec.Name, out.Resources.Class)
	}
	switch out.Scaling.Strategy {
	case StrategyFixed, StrategyBacklog, StrategyResources, "":
		if out.Scaling.Strategy == "" {
			out.Scaling.Strategy = StrategyFixed
		}
	default:
		return ResolvedWorkloadSpec{}, fmt.Errorf("workload %q: unknown scaling strategy %q", spec.Name, out.Scaling.Strategy)
	}

	return out, nil
}

func mergeScaling(base, over ScalingPolicy) ScalingPolicy {
	out := base
	if over.Strategy != "" {
		out.Strategy = over.Strategy
	}
	if over.ScaleUpCooldown > 0 {
		out.ScaleUpCooldown = over.ScaleUpCooldown
	}
	if over.ScaleDownIdleFor > 0 {
		out.ScaleDownIdleFor = over.ScaleDownIdleFor
	}
	if over.AvgJobDuration > 0 {
		out.AvgJobDuration = over.AvgJobDuration
	}
	if over.TargetDrainTime > 0 {
		out.TargetDrainTime = over.TargetDrainTime
	}
	return out
}
