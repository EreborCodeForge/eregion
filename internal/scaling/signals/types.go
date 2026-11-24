package signals

import "time"

// InvalidReason is why a sample cannot be used. Empty means the sample was not rejected.
type InvalidReason string

const (
	ReasonMissing         InvalidReason = "missing"
	ReasonStale           InvalidReason = "stale"
	ReasonReset           InvalidReason = "reset"
	ReasonPartial         InvalidReason = "partial"
	ReasonInsufficient    InvalidReason = "insufficient_samples"
	ReasonMixedGeneration InvalidReason = "mixed_generation"
	ReasonOverflow        InvalidReason = "overflow"
	ReasonUnsupported     InvalidReason = "unsupported"
	ReasonAmbiguous       InvalidReason = "ambiguous"
)

// Sample is one measured value. Valid is false when Value must be ignored.
type Sample[T any] struct {
	Value  T
	Valid  bool
	Source string
	From   time.Time
	To     time.Time
	Reason InvalidReason
}

// PoolSnapshot is the pool view captured with the window.
type PoolSnapshot struct {
	Desired  int
	Running  int
	Idle     int
	Busy     int
	Starting int
	Draining int
}

// SignalWindow is the immutable evidence for one closed epoch.
type SignalWindow struct {
	Workload string
	Epoch    uint64
	From     time.Time
	To       time.Time
	Pool     PoolSnapshot

	OfferedRPS    Sample[float64]
	CompletedRPS  Sample[float64]
	SuccessfulRPS Sample[float64]

	QueueWaitP95Seconds Sample[float64]
	QueueP95Low         float64
	QueueP95High        float64
	RequestP95Seconds   Sample[float64]
	RequestP95Low       float64
	RequestP95High      float64
	QueueWaitingMax     Sample[uint64]
	QueueRejects        Sample[uint64]
	HTTPErrorRatio      Sample[float64]

	BusyRatio        Sample[float64]
	WorkerCPURatio   Sample[float64]
	ResourceCPURatio Sample[float64]
	CPUThrottleRatio Sample[float64]
	BacklogDepth     Sample[uint64]

	GlobalFresh bool
}

// DecisionReason explains a demand decision. It is not a guard admission reason.
type DecisionReason string

const (
	ReasonDemandUp                    DecisionReason = "demand_up"
	ReasonDemandDown                  DecisionReason = "demand_down"
	ReasonWithinBand                  DecisionReason = "within_band"
	ReasonSignalUnavailable           DecisionReason = "signal_unavailable"
	ReasonInsufficientSamples         DecisionReason = "insufficient_samples"
	ReasonConsumerDownSignalMissing   DecisionReason = "consumer_down_signal_missing"
	ReasonPoolTransition              DecisionReason = "pool_transition"
	ReasonCooldown                    DecisionReason = "cooldown"
	ReasonConfirming                  DecisionReason = "confirming"
	ReasonCPUSaturation               DecisionReason = "cpu_saturation"
	ReasonNonlocalErrorPressure       DecisionReason = "nonlocal_error_pressure"
	ReasonLatencyWithoutQueuePressure DecisionReason = "latency_without_queue_pressure"
	ReasonScheduleWindow              DecisionReason = "schedule_window"
	ReasonScheduleDefault             DecisionReason = "schedule_default"
	ReasonProbeRequested              DecisionReason = "probe_requested"
	ReasonProbeGain                   DecisionReason = "probe_gain"
	ReasonProbeNoGain                 DecisionReason = "probe_no_gain"
	ReasonProbeConfounded             DecisionReason = "probe_confounded"
	ReasonProbeGuardBlocked           DecisionReason = "probe_guard_blocked"
	ReasonProbeTimeout                DecisionReason = "probe_timeout"
	ReasonGuardLimited                DecisionReason = "guard_limited"
	ReasonIdle                        DecisionReason = "demand_down"
)

// DemandDecision is strategy output before the resource planner.
type DemandDecision struct {
	RawRequested    int
	PolicyRequested int
	Reason          DecisionReason
	Valid           bool
	Epoch           uint64
	Action          string
}
