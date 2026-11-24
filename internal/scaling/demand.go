package scaling

import (
	"math"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

const (
	cpuVetoRatio      = 0.90
	throttleVetoRatio = 0.20
	errorVetoRatio    = 0.05
)

// PoolStability is the pool state the formula is allowed to treat as N.
type PoolStability struct {
	Desired  int
	Running  int
	Starting int
	Draining int
}

func (p PoolStability) Stable() bool {
	return p.Starting == 0 && p.Draining == 0 && p.Desired == p.Running
}

func (p PoolStability) N() int {
	if p.Desired > 0 {
		return p.Desired
	}
	return p.Running
}

// EvaluateDemand runs one ECO-11 strategy. It does not admit workers.
func EvaluateDemand(spec workload.ResolvedWorkloadSpec, window signals.SignalWindow, pool PoolStability, now time.Time) signals.DemandDecision {
	if !pool.Stable() {
		return hold(pool.N(), signals.ReasonPoolTransition, window.Epoch, false)
	}
	n := pool.N()
	switch spec.Scaling.Strategy {
	case workload.StrategyCPUUtilization:
		return evalCPU(spec, window, n)
	case workload.StrategyLatency:
		return evalLatency(spec, window, n)
	case workload.StrategyThroughput:
		return evalThroughput(spec, window, n)
	case workload.StrategyHybrid:
		if spec.Mode == workload.ModeConsumer {
			return evalHybridConsumer(spec, window, n)
		}
		return evalHybridHTTP(spec, window, n)
	case workload.StrategyScheduled:
		return evalScheduled(spec, now, n)
	default:
		return hold(n, signals.ReasonSignalUnavailable, window.Epoch, false)
	}
}

func hold(n int, reason signals.DecisionReason, epoch uint64, valid bool) signals.DemandDecision {
	return signals.DemandDecision{RawRequested: n, PolicyRequested: n, Reason: reason, Valid: valid, Epoch: epoch, Action: "hold"}
}

func propose(raw, n int, epoch uint64, reason signals.DecisionReason) signals.DemandDecision {
	action := "hold"
	if raw > n {
		action = "up"
		if reason == signals.ReasonWithinBand || reason == "" {
			reason = signals.ReasonDemandUp
		}
	} else if raw < n {
		action = "down"
		if reason == signals.ReasonWithinBand || reason == "" {
			reason = signals.ReasonDemandDown
		}
	} else if reason == "" {
		reason = signals.ReasonWithinBand
	}
	return signals.DemandDecision{RawRequested: raw, PolicyRequested: raw, Reason: reason, Valid: true, Epoch: epoch, Action: action}
}

func evalCPU(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow, n int) signals.DemandDecision {
	u := w.WorkerCPURatio
	if !u.Valid || n < 1 {
		return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
	}
	target := spec.Scaling.CPU.TargetRatio
	tol := spec.Scaling.CPU.ToleranceRatio
	if u.Value > target+tol {
		raw, ok := finiteCeil(float64(n) * u.Value / target)
		if !ok {
			return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
		}
		return propose(raw, n, w.Epoch, signals.ReasonDemandUp)
	}
	if u.Value < target-tol {
		if spec.Mode == workload.ModeHTTP && !httpQueueClear(w) {
			return hold(n, signals.ReasonWithinBand, w.Epoch, true)
		}
		if spec.Mode == workload.ModeConsumer {
			if !w.BacklogDepth.Valid {
				return hold(n, signals.ReasonConsumerDownSignalMissing, w.Epoch, true)
			}
			if w.BacklogDepth.Value != 0 {
				return hold(n, signals.ReasonWithinBand, w.Epoch, true)
			}
		}
		raw, ok := finiteCeil(float64(n) * u.Value / target)
		if !ok {
			return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
		}
		if raw > n-1 {
			raw = n - 1
		}
		if raw < spec.Workers.Min {
			raw = spec.Workers.Min
		}
		return propose(raw, n, w.Epoch, signals.ReasonDemandDown)
	}
	return propose(n, n, w.Epoch, signals.ReasonWithinBand)
}

func evalLatency(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow, n int) signals.DemandDecision {
	p95 := w.RequestP95Seconds
	target := spec.Scaling.Latency.TargetP95.Seconds()
	tol := spec.Scaling.Latency.ToleranceRatio
	high := target * (1 + tol)
	low := target * (1 - tol)
	pressure, pressureKnown := latencyPressure(spec, w)
	busy := w.BusyRatio
	above := p95.Valid && p95.Value > high && !bucketStraddles(w.RequestP95Low, w.RequestP95High, high)
	below := p95.Valid && p95.Value < low && !bucketStraddles(w.RequestP95Low, w.RequestP95High, low)
	if above && pressure && busy.Valid && busy.Value >= spec.Scaling.Latency.BusyThreshold {
		return propose(n+1, n, w.Epoch, signals.ReasonDemandUp)
	}
	if above && !pressure {
		return hold(n, signals.ReasonLatencyWithoutQueuePressure, w.Epoch, true)
	}
	if below && pressureKnown && !pressure && busy.Valid && busy.Value < 0.50 && queueSignalsLow(w) {
		return propose(n-1, n, w.Epoch, signals.ReasonDemandDown)
	}
	if !p95.Valid {
		return hold(n, signals.ReasonInsufficientSamples, w.Epoch, false)
	}
	return propose(n, n, w.Epoch, signals.ReasonWithinBand)
}

func evalThroughput(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow, n int) signals.DemandDecision {
	if n < 1 {
		return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
	}
	off := w.OfferedRPS
	target := spec.Scaling.Throughput.TargetRPSPerWorker
	if !off.Valid || target <= 0 {
		return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
	}
	x := off.Value / target
	tol := spec.Scaling.Throughput.ToleranceRatio
	if x > float64(n)*(1+tol) {
		raw, ok := finiteCeil(x)
		if !ok {
			return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
		}
		return propose(raw, n, w.Epoch, signals.ReasonDemandUp)
	}
	if x < float64(n)*(1-tol) && httpQueueClear(w) && w.BusyRatio.Valid && w.BusyRatio.Value < 0.50 {
		raw, ok := finiteCeil(x)
		if !ok {
			return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
		}
		if raw > n-1 {
			raw = n - 1
		}
		return propose(raw, n, w.Epoch, signals.ReasonDemandDown)
	}
	return propose(n, n, w.Epoch, signals.ReasonWithinBand)
}

func evalHybridHTTP(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow, n int) signals.DemandDecision {
	var ups []int
	var downs []int
	holds := 0
	if spec.Scaling.CPU.Configured && w.WorkerCPURatio.Valid {
		d := evalCPU(spec, w, n)
		collectProposal(&ups, &downs, &holds, d, n)
	}
	if spec.Scaling.Latency.Configured {
		if w.RequestP95Seconds.Valid {
			d := evalLatency(spec, w, n)
			collectProposal(&ups, &downs, &holds, d, n)
		} else {
			holds++
		}
	}
	if spec.Scaling.Throughput.Configured && spec.Scaling.Throughput.TargetRPSPerWorker > 0 {
		if w.OfferedRPS.Valid {
			d := evalThroughput(spec, w, n)
			collectProposal(&ups, &downs, &holds, d, n)
		} else {
			holds++
		}
	}
	if queuePressureKnown(w) && queuePressure(spec, w) && w.BusyRatio.Valid && w.BusyRatio.Value >= 0.85 {
		ups = append(ups, n+1)
	}
	if len(ups) > 0 {
		return propose(maxDemand(ups), n, w.Epoch, signals.ReasonDemandUp)
	}
	if idleHTTP(w) && n > spec.Workers.Min {
		return propose(n-1, n, w.Epoch, signals.ReasonDemandDown)
	}
	if holds > 0 || len(downs) == 0 {
		return propose(n, n, w.Epoch, signals.ReasonWithinBand)
	}
	return propose(maxDemand(downs), n, w.Epoch, signals.ReasonDemandDown)
}

func evalHybridConsumer(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow, n int) signals.DemandDecision {
	if !w.BacklogDepth.Valid {
		return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
	}
	raw, ok := backlogRaw(w.BacklogDepth.Value, spec.Scaling.AvgJobDuration, spec.Scaling.TargetDrainTime)
	if !ok {
		return hold(n, signals.ReasonSignalUnavailable, w.Epoch, false)
	}
	if raw < spec.Workers.Min {
		raw = spec.Workers.Min
	}
	if n == 0 && w.BacklogDepth.Value > 0 && raw < 1 {
		raw = 1
	}
	return propose(raw, n, w.Epoch, "")
}

func evalScheduled(spec workload.ResolvedWorkloadSpec, now time.Time, n int) signals.DemandDecision {
	raw, windowed := matchSchedule(spec.Scaling.Scheduled, now.UTC())
	reason := signals.ReasonScheduleDefault
	if windowed {
		reason = signals.ReasonScheduleWindow
	}
	return propose(raw, n, 0, reason)
}

func matchSchedule(s workload.ScheduledPolicy, utc time.Time) (int, bool) {
	mins := utc.Hour()*60 + utc.Minute()
	day := (int(utc.Weekday()) + 6) % 7
	names := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	today := names[day]
	for _, w := range s.Windows {
		if mins < w.StartMin || mins >= w.EndMin {
			continue
		}
		for _, d := range w.Days {
			if d == today {
				return w.Target, true
			}
		}
	}
	return s.DefaultWorkers, false
}

func collectProposal(ups, downs *[]int, holds *int, d signals.DemandDecision, n int) {
	if !d.Valid || d.Action == "hold" || d.RawRequested == n {
		*holds++
		return
	}
	if d.RawRequested > n {
		*ups = append(*ups, d.RawRequested)
		return
	}
	*downs = append(*downs, d.RawRequested)
}

func httpQueueClear(w signals.SignalWindow) bool {
	if !w.QueueWaitingMax.Valid || !w.QueueRejects.Valid {
		return false
	}
	if w.QueueWaitingMax.Value > 0 || w.QueueRejects.Value > 0 {
		return false
	}
	if w.QueueWaitP95Seconds.Valid && w.QueueWaitP95Seconds.Value > 0 {
		return false
	}
	return true
}

func queueSignalsLow(w signals.SignalWindow) bool {
	return httpQueueClear(w)
}

func queuePressureKnown(w signals.SignalWindow) bool {
	return w.QueueWaitingMax.Valid && w.QueueRejects.Valid
}

func queuePressure(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow) bool {
	if !queuePressureKnown(w) {
		return false
	}
	if w.QueueWaitingMax.Value > 0 || w.QueueRejects.Value > 0 {
		return true
	}
	if w.QueueWaitP95Seconds.Valid && spec.Scaling.Latency.QueueWaitTargetP95 > 0 &&
		w.QueueWaitP95Seconds.Value > spec.Scaling.Latency.QueueWaitTargetP95.Seconds() {
		return true
	}
	return false
}

func latencyPressure(spec workload.ResolvedWorkloadSpec, w signals.SignalWindow) (bool, bool) {
	if !w.QueueWaitingMax.Valid || !w.QueueRejects.Valid {
		return false, false
	}
	return queuePressure(spec, w), true
}

func idleHTTP(w signals.SignalWindow) bool {
	if !w.OfferedRPS.Valid || w.OfferedRPS.Value != 0 {
		return false
	}
	if !w.QueueWaitingMax.Valid || w.QueueWaitingMax.Value != 0 {
		return false
	}
	if !w.QueueRejects.Valid || w.QueueRejects.Value != 0 {
		return false
	}
	if !w.BusyRatio.Valid || w.BusyRatio.Value != 0 {
		return false
	}
	if w.Pool.Starting != 0 || w.Pool.Draining != 0 {
		return false
	}
	return true
}

func bucketStraddles(low, high, threshold float64) bool {
	if high == 0 && low == 0 {
		return false
	}
	if math.IsInf(high, 1) {
		return low < threshold
	}
	return low < threshold && threshold <= high
}

func backlogRaw(depth uint64, avg, drain time.Duration) (int, bool) {
	if avg <= 0 || drain <= 0 {
		return 0, false
	}
	x := float64(depth) * avg.Seconds() / drain.Seconds()
	return finiteCeil(x)
}

func finiteCeil(x float64) (int, bool) {
	if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 || x > 1e9 {
		return 0, false
	}
	n := math.Ceil(x)
	if n > float64(math.MaxInt) {
		return 0, false
	}
	return int(n), true
}

func maxDemand(v []int) int {
	m := v[0]
	for _, n := range v[1:] {
		if n > m {
			m = n
		}
	}
	return m
}

// ExpansionVeto blocks speculative growth. It does not force a scale-down.
func ExpansionVeto(strategy workload.ScalingStrategyName, mode workload.WorkloadMode, w signals.SignalWindow) (bool, signals.DecisionReason) {
	if strategy == workload.StrategyScheduled {
		return false, ""
	}
	if !w.ResourceCPURatio.Valid || !w.CPUThrottleRatio.Valid {
		if strategy == workload.StrategyCPUUtilization || strategy == workload.StrategyHybrid ||
			strategy == workload.StrategyLatency || strategy == workload.StrategyThroughput || strategy == workload.StrategyAdaptive {
			return true, signals.ReasonCPUSaturation
		}
	}
	if w.ResourceCPURatio.Valid && (w.ResourceCPURatio.Value >= cpuVetoRatio || (w.CPUThrottleRatio.Valid && w.CPUThrottleRatio.Value >= throttleVetoRatio)) {
		return true, signals.ReasonCPUSaturation
	}
	if mode == workload.ModeHTTP {
		if !w.HTTPErrorRatio.Valid {
			return true, signals.ReasonNonlocalErrorPressure
		}
		if w.HTTPErrorRatio.Value >= errorVetoRatio {
			return true, signals.ReasonNonlocalErrorPressure
		}
	}
	return false, ""
}
