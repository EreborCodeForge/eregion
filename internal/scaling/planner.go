package scaling

import (
	"errors"
	"sort"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
)

// LimitReason is the closed set of capacity constraints.
type LimitReason string

const (
	ReasonNone                 LimitReason = "none"
	ReasonMemoryHeadroom       LimitReason = "memory_headroom"
	ReasonMemoryPressure       LimitReason = "memory_pressure"
	ReasonCPUSoftCap           LimitReason = "cpu_soft_cap"
	ReasonCPUThrottling        LimitReason = "cpu_throttling"
	ReasonUnknownResource      LimitReason = "unknown_resource"
	ReasonInsufficientCapacity LimitReason = "insufficient_capacity"
	ReasonPolicyMax            LimitReason = "policy_max"
	ReasonStartupReservation   LimitReason = "startup_reservation"
)

// RiskSeverity is the closed set of guard severities.
type RiskSeverity string

const (
	SeverityOK       RiskSeverity = "ok"
	SeverityWarning  RiskSeverity = "warning"
	SeverityCritical RiskSeverity = "critical"
	SeverityDegraded RiskSeverity = "degraded"
)

// CapacityDecision is the immutable result of one planner pass.
type CapacityDecision struct {
	Workload       string
	Strategy       string
	Requested      int
	PolicyTarget   int
	Safe           int
	Admitted       int
	Current        int
	Min            int
	ConstrainedBy  LimitReason
	Severity       RiskSeverity
	Recommendation string
	SampleAt       time.Time
	SampleValid    bool
	Deficit        int
	WouldLimit     int
	Limited        int
}

var (
	// ErrInsufficientCapacity means the configured minimums do not fit a known hard budget.
	ErrInsufficientCapacity = errors.New("insufficient_capacity")
	// ErrResourceLimitUnknown means enforce was asked to protect an unproven limit.
	ErrResourceLimitUnknown = errors.New("resource_limit_unknown")
)

// WorkloadDemand is one workload's demand for a single planner pass.
type WorkloadDemand struct {
	Name      string
	Strategy  string
	Min       int
	Max       int
	Requested int
	Policy    int
	Running   int
	Starting  int
	MemoryMB  int
	RSSP95    uint64
	RSSKnown  bool
}

// PlanRequest is the pure input of one global allocation.
type PlanRequest struct {
	Mode         string
	Memory       config.MemoryGuardConfig
	CPU          config.CPUGuardConfig
	Sample       resources.Sample
	Now          time.Time
	MaxSampleAge time.Duration
	PendingBytes uint64
	Startup      bool
	Demands      []WorkloadDemand
}

// Plan allocates a shared memory budget. It does not spawn processes or read a broker.
func Plan(req PlanRequest) []CapacityDecision {
	demands := append([]WorkloadDemand(nil), req.Demands...)
	sort.Slice(demands, func(i, j int) bool { return demands[i].Name < demands[j].Name })

	usable := memoryUsable(req.Sample, req.Now, req.MaxSampleAge)
	headroom, headroomOK := HeadroomBytes(req.Sample, req.Memory, req.PendingBytes)
	if !usable {
		headroomOK = false
	}
	cpuBlock, cpuReason := cpuBlocksGrowth(req.Sample, req.CPU)
	ratio := memoryRatio(req.Sample)
	pressure := usable && ratio >= req.Memory.CriticalRatio

	type state struct {
		demand   WorkloadDemand
		admitted int
		base     int
		cost     uint64
		reason   LimitReason
	}
	states := make([]state, len(demands))
	for i, d := range demands {
		if d.Min < 0 {
			d.Min = 0
		}
		if d.Max < 0 {
			d.Max = 0
		}
		if d.Policy < 0 {
			d.Policy = 0
		}
		if d.Requested < 0 {
			d.Requested = 0
		}
		base := d.Running + d.Starting
		if base < 0 {
			base = 0
		}
		states[i] = state{
			demand:   d,
			base:     base,
			cost:     IncrementalCost(d.MemoryMB, d.RSSP95, d.RSSKnown, req.Memory.WorkerFallbackMB),
			admitted: base,
		}
	}

	minsFit := headroomOK && !pressure
	if minsFit {
		var minCost uint64
		for _, st := range states {
			need := st.demand.Min - st.base
			if need > 0 {
				minCost += uint64(need) * st.cost
			}
		}
		if minCost > headroom {
			minsFit = false
		}
	}

	remaining := headroom
	for i := range states {
		states[i].admitted = states[i].base
	}
	if minsFit {
		for i := range states {
			target := states[i].demand.Min
			if states[i].demand.Policy >= states[i].demand.Min && target > states[i].demand.Policy {
				target = states[i].demand.Policy
			}
			if target < states[i].base {
				target = states[i].base
			}
			if grow := target - states[i].base; grow > 0 {
				remaining -= uint64(grow) * states[i].cost
			}
			states[i].admitted = target
		}
		if !cpuBlock {
			progress := true
			for progress {
				progress = false
				for i := range states {
					st := &states[i]
					ceiling := st.demand.Policy
					if st.demand.Max > 0 && ceiling > st.demand.Max {
						ceiling = st.demand.Max
					}
					if st.admitted >= ceiling {
						continue
					}
					if st.cost > remaining {
						continue
					}
					remaining -= st.cost
					st.admitted++
					progress = true
				}
			}
		}
	}
	for i := range states {
		if states[i].demand.Policy < states[i].admitted {
			states[i].admitted = states[i].demand.Policy
			if states[i].admitted < 0 {
				states[i].admitted = 0
			}
		}
	}

	out := make([]CapacityDecision, len(states))
	for i, st := range states {
		safe := st.admitted
		if st.demand.Max > 0 && safe > st.demand.Max {
			safe = st.demand.Max
		}
		if safe < 0 {
			safe = 0
		}
		reason := st.reason
		if !headroomOK {
			reason = ReasonUnknownResource
			if safe > st.base {
				safe = st.base
			}
		} else if pressure {
			reason = ReasonMemoryPressure
		} else if !minsFit && safe < st.demand.Min {
			reason = ReasonInsufficientCapacity
		}
		if reason == "" && safe < st.demand.Policy {
			if pressure {
				reason = ReasonMemoryPressure
			} else if cpuBlock {
				reason = cpuReason
			} else if headroomOK {
				if req.Startup {
					reason = ReasonStartupReservation
				} else {
					reason = ReasonMemoryHeadroom
				}
			}
		}
		if reason == "" && st.demand.Requested > st.demand.Policy && st.demand.Max > 0 && st.demand.Policy >= st.demand.Max {
			reason = ReasonPolicyMax
		}
		severity := severityFor(reason, ratio, req.Memory, req.Sample)
		applied := st.demand.Policy
		if req.Mode == config.ResourceGuardEnforce {
			applied = safe
		}
		would := 0
		limited := 0
		if req.Mode == config.ResourceGuardEnforce {
			limited = maxInt(0, st.demand.Policy-applied)
		} else if req.Mode == config.ResourceGuardObserve {
			would = maxInt(0, st.demand.Policy-safe)
		}
		deficit := 0
		if safe < st.demand.Min {
			deficit = st.demand.Min - safe
		}
		out[i] = CapacityDecision{
			Workload:       st.demand.Name,
			Strategy:       st.demand.Strategy,
			Requested:      st.demand.Requested,
			PolicyTarget:   st.demand.Policy,
			Safe:           safe,
			Admitted:       applied,
			Current:        st.demand.Running,
			Min:            st.demand.Min,
			ConstrainedBy:  reason,
			Severity:       severity,
			Recommendation: Recommendation(reason, req.Sample),
			SampleAt:       req.Sample.At,
			SampleValid:    req.Sample.MemoryKnown() && memoryFresh(req.Sample, req.Now, req.MaxSampleAge),
			Deficit:        deficit,
			WouldLimit:     would,
			Limited:        limited,
		}
		if out[i].ConstrainedBy == "" {
			out[i].ConstrainedBy = ReasonNone
		}
	}
	return out
}

// HeadroomBytes is the memory available for new workers.
// Current usage already includes running processes and is not added again.
func HeadroomBytes(sample resources.Sample, mem config.MemoryGuardConfig, pending uint64) (uint64, bool) {
	if !sample.MemoryKnown() {
		return 0, false
	}
	limit := sample.MemoryLimitBytes
	current := sample.MemoryCurrentBytes
	reserve := uint64(mem.ReserveMB) * 1024 * 1024
	if mem.ReservePercent > 0 {
		pct := limit * uint64(mem.ReservePercent) / 100
		if pct > reserve {
			reserve = pct
		}
	}
	used := current
	if pending > 0 {
		used += pending
	}
	if used > limit || reserve > limit-used {
		return 0, true
	}
	return limit - used - reserve, true
}

// IncrementalCost is the projected cost of one additional worker.
// memory_mb of 0 is not free. Fallback is included whenever it is positive.
func IncrementalCost(memoryMB int, rss uint64, rssKnown bool, fallbackMB int) uint64 {
	var cost uint64
	if memoryMB > 0 {
		cost = uint64(memoryMB) * 1024 * 1024
	}
	if rssKnown && rss > cost {
		cost = rss
	}
	fallback := uint64(0)
	if fallbackMB > 0 {
		fallback = uint64(fallbackMB) * 1024 * 1024
	}
	if fallback > cost {
		cost = fallback
	}
	if cost == 0 {
		cost = 256 * 1024 * 1024
	}
	return cost
}

// Recommendation maps a limit reason to a closed operator hint.
func Recommendation(reason LimitReason, sample resources.Sample) string {
	switch reason {
	case ReasonNone:
		return ""
	case ReasonMemoryPressure:
		return "inspect_memory_growth"
	case ReasonCPUSoftCap, ReasonCPUThrottling:
		return "reduce_cpu_parallelism"
	case ReasonPolicyMax:
		return "reduce_workers_max"
	case ReasonUnknownResource:
		if sample.MemoryLimitValid == resources.ValidityStale || sample.MemoryCurrentValid == resources.ValidityStale {
			return "restore_resource_sampler"
		}
		return "check_cgroup_limits"
	case ReasonInsufficientCapacity, ReasonMemoryHeadroom, ReasonStartupReservation:
		return "increase_memory"
	default:
		return "increase_memory"
	}
}

func memoryUsable(sample resources.Sample, now time.Time, maxAge time.Duration) bool {
	return sample.MemoryKnown() && memoryFresh(sample, now, maxAge)
}

func memoryFresh(sample resources.Sample, now time.Time, maxAge time.Duration) bool {
	if sample.At.IsZero() || now.Before(sample.At) {
		return false
	}
	if maxAge > 0 && now.Sub(sample.At) > maxAge {
		return false
	}
	return true
}

func memoryRatio(sample resources.Sample) float64 {
	if !sample.MemoryKnown() {
		return 0
	}
	return float64(sample.MemoryCurrentBytes) / float64(sample.MemoryLimitBytes)
}

func cpuBlocksGrowth(sample resources.Sample, cpu config.CPUGuardConfig) (bool, LimitReason) {
	if resources.Known(sample.CPUThrottlingValid) && sample.CPUThrottledRatio >= cpu.CriticalThrottlingRatio && cpu.CriticalThrottlingRatio > 0 {
		return true, ReasonCPUThrottling
	}
	if resources.Known(sample.CPUUtilizationValid) && sample.CPUUtilization >= cpu.WarningUtilizationRatio && cpu.WarningUtilizationRatio > 0 {
		return true, ReasonCPUSoftCap
	}
	return false, ""
}

func severityFor(reason LimitReason, ratio float64, mem config.MemoryGuardConfig, sample resources.Sample) RiskSeverity {
	switch reason {
	case ReasonInsufficientCapacity, ReasonUnknownResource:
		return SeverityDegraded
	case ReasonCPUThrottling, ReasonMemoryPressure:
		return SeverityCritical
	case ReasonMemoryHeadroom, ReasonStartupReservation, ReasonCPUSoftCap:
		if ratio >= mem.CriticalRatio && sample.MemoryKnown() {
			return SeverityCritical
		}
		return SeverityWarning
	case ReasonPolicyMax:
		return SeverityOK
	default:
		if sample.MemoryKnown() && ratio >= mem.CriticalRatio {
			return SeverityCritical
		}
		if sample.MemoryKnown() && ratio >= mem.WarningRatio {
			return SeverityWarning
		}
		if sample.PredictionValid && sample.MemoryKnown() {
			predicted := float64(sample.PredictionBytes) / float64(sample.MemoryLimitBytes)
			if predicted >= mem.HardRatio {
				return SeverityWarning
			}
		}
		return SeverityOK
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
