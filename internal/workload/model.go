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
	StrategyFixed          ScalingStrategyName = "fixed"
	StrategyBacklog        ScalingStrategyName = "backlog"
	StrategyResources      ScalingStrategyName = "resources"
	StrategyCPUUtilization ScalingStrategyName = "cpu_utilization"
	StrategyLatency        ScalingStrategyName = "latency"
	StrategyThroughput     ScalingStrategyName = "throughput"
	StrategyHybrid         ScalingStrategyName = "hybrid"
	StrategyScheduled      ScalingStrategyName = "scheduled"
	StrategyAdaptive       ScalingStrategyName = "adaptive"
)

// IsAdvanced reports strategies introduced by ECO-11. Legacy names stay false.
func IsAdvanced(name ScalingStrategyName) bool {
	switch name {
	case StrategyCPUUtilization, StrategyLatency, StrategyThroughput, StrategyHybrid, StrategyScheduled, StrategyAdaptive:
		return true
	default:
		return false
	}
}

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
	Class    ResourceClass
	MemoryMB int
}

// BacklogSource is a resolved http_json adapter. It stores env var names, never token values or URLs.
type BacklogSource struct {
	Type         string
	URLEnv       string
	TokenEnv     string
	Timeout      time.Duration
	MaxStaleness time.Duration
}

// ControlPolicy is the shared stabilizer for ECO-11 strategies.
type ControlPolicy struct {
	Window            time.Duration
	MaxSignalAge      time.Duration
	UpConfirmations   int
	DownConfirmations int
	MaxStepUp         int
	MaxStepDown       int
	Configured        bool
}

// CPUPolicy sizes from workload CPU.
type CPUPolicy struct {
	TargetRatio    float64
	ToleranceRatio float64
	Configured     bool
}

// LatencyPolicy sizes HTTP from p95 plus queue pressure.
type LatencyPolicy struct {
	TargetP95          time.Duration
	QueueWaitTargetP95 time.Duration
	ToleranceRatio     float64
	BusyThreshold      float64
	Configured         bool
}

// ThroughputPolicy sizes HTTP from offered demand and a calibrated per-worker rate.
type ThroughputPolicy struct {
	TargetRPSPerWorker float64
	ToleranceRatio     float64
	Configured         bool
}

// ScheduleWindow is one UTC half-open interval.
type ScheduleWindow struct {
	Days     []string
	StartMin int
	EndMin   int
	Target   int
}

// ScheduledPolicy is an explicit UTC calendar.
type ScheduledPolicy struct {
	Timezone       string
	DefaultWorkers int
	Windows        []ScheduleWindow
	Configured     bool
}

// AdaptivePolicy is the bounded +1 probe.
type AdaptivePolicy struct {
	MinGainRatio          float64
	MaxP95RegressionRatio float64
	DemandToleranceRatio  float64
	BaselineWindows       int
	EvaluationWindows     int
	SettleWindows         int
	ProbeCooldown         time.Duration
	ProbeTimeout          time.Duration
	BusyThreshold         float64
	Configured            bool
}

// ScalingPolicy controls autoscaling behaviour.
type ScalingPolicy struct {
	Strategy         ScalingStrategyName
	ScaleUpCooldown  time.Duration
	ScaleDownIdleFor time.Duration
	CooldownSet      bool
	IdleSet          bool
	// Backlog formula knobs (used when strategy=backlog and a provider exists).
	AvgJobDuration  time.Duration
	TargetDrainTime time.Duration
	BacklogSource   BacklogSource
	Control         ControlPolicy
	CPU             CPUPolicy
	Latency         LatencyPolicy
	Throughput      ThroughputPolicy
	Scheduled       ScheduledPolicy
	Adaptive        AdaptivePolicy
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
