package workload

import "time"

// WorkloadMode is the runtime mode for a workload.
type WorkloadMode string

const (
	ModeHTTP     WorkloadMode = "http"
	ModeConsumer WorkloadMode = "consumer"
)

// ResourceClass influences sizing advice; never replaces hard limits.
type ResourceClass string

const (
	ClassIO       ResourceClass = "io"
	ClassCPU      ResourceClass = "cpu"
	ClassBalanced ResourceClass = "balanced"
)

// ScalingStrategyName identifies a scaling strategy.
type ScalingStrategyName string

const (
	StrategyFixed     ScalingStrategyName = "fixed"
	StrategyBacklog   ScalingStrategyName = "backlog"
	StrategyResources ScalingStrategyName = "resources"
)

// QueueMetadata describes broker metadata (not consumed by Eregion).
type QueueMetadata struct {
	Transport string
	Name      string
}

// WorkerPolicy is min/max worker counts for a workload.
type WorkerPolicy struct {
	Min int
	Max int
}

// ResourcePolicy is resource class and memory envelope.
type ResourcePolicy struct {
	Class     ResourceClass
	MemoryMB  int
}

// ScalingPolicy controls autoscaling behaviour.
type ScalingPolicy struct {
	Strategy         ScalingStrategyName
	ScaleUpCooldown  time.Duration
	ScaleDownIdleFor time.Duration
	// Backlog formula knobs (used when strategy=backlog and a provider exists).
	AvgJobDuration  time.Duration
	TargetDrainTime time.Duration
}

// WorkloadTemplate is operational behaviour shared by workloads.
type WorkloadTemplate struct {
	Name      string
	Mode      WorkloadMode
	Workers   WorkerPolicy
	Resources ResourcePolicy
	Scaling   ScalingPolicy
}

// WorkloadSpec is an unresolved workload definition (may reference a template).
type WorkloadSpec struct {
	Name      string
	Template  string
	Mode      WorkloadMode
	Command   []string
	Queue     QueueMetadata
	Workers   *WorkerPolicyPatch // nil = inherit template
	Resources *ResourcePolicy    // nil = inherit template
	Scaling   *ScalingPolicy     // nil = inherit template
}

// WorkerPolicyPatch carries optional min/max overrides.
type WorkerPolicyPatch struct {
	Min *int
	Max *int
}

// ResolvedWorkloadSpec is the fully merged spec consumed by the reconciler.
type ResolvedWorkloadSpec struct {
	Name      string
	Template  string
	Mode      WorkloadMode
	Command   []string
	Queue     QueueMetadata
	Workers   WorkerPolicy
	Resources ResourcePolicy
	Scaling   ScalingPolicy
}

// WorkloadMetrics is a snapshot used by scaling strategies.
type WorkloadMetrics struct {
	Desired  int
	Running  int
	Idle     int
	Busy     int
	Draining int
	Failed   int
	Backlog  int // from BacklogProvider; 0 when unavailable
}
