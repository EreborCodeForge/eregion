package telemetry

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ScalingSignal is one cached signal row. Valid false omits the value.
type ScalingSignal struct {
	Name  string
	Valid bool
	Value float64
	Age   float64
	AgeOK bool
	Unit  string
}

// ScalingWorkloadPublication is the last decision for one workload.
type ScalingWorkloadPublication struct {
	Name       string
	Strategy   string
	Target     int
	UpStreak   int
	DownStreak int
	ProbeState string
	Gain       float64
	GainValid  bool
	Signals    []ScalingSignal
}

// ScalingPublication is scraped from cache. Collecting it does not sample.
type ScalingPublication struct {
	Workloads []ScalingWorkloadPublication
}

// HTTPSignalSnapshot is the cumulative view used to build a signal window.
type HTTPSignalSnapshot struct {
	Offered      uint64
	Completed    uint64
	Success      uint64
	ServerErr    uint64
	ClientErr    uint64
	LocalReject  uint64
	QueueRejects uint64
	Duration     []uint64
	QueueWait    []uint64
	Enabled      bool
}

// EnableSignalCapture turns on the bounded request-path counters.
func (r *Registry) EnableSignalCapture() {
	if r == nil {
		return
	}
	r.signalCapture.Store(true)
}

// SignalCaptureEnabled reports whether the extra counters are active.
func (r *Registry) SignalCaptureEnabled() bool {
	return r != nil && r.signalCapture.Load()
}

// ObserveClass counts one finalized request. It is a no-op until capture is enabled.
func (r *Registry) ObserveClass(class string, duration, queueWait float64) {
	if r == nil || !r.signalCapture.Load() {
		return
	}
	switch class {
	case "success":
		r.successTotal.Add(1)
		r.completedTotal.Add(1)
	case "client_error":
		r.clientErrorTotal.Add(1)
		r.completedTotal.Add(1)
	case "server_error":
		r.serverErrorTotal.Add(1)
		r.completedTotal.Add(1)
	case "local_reject":
		r.localRejectTotal.Add(1)
		r.completedTotal.Add(1)
	default:
		return
	}
	r.signalDuration.observe(duration)
	r.signalQueue.observe(queueWait)
}

// HTTPSignalSnapshot reads counters. It does not sample.
func (r *Registry) HTTPSignalSnapshot() HTTPSignalSnapshot {
	if r == nil {
		return HTTPSignalSnapshot{}
	}
	return HTTPSignalSnapshot{
		Offered:      r.requestsTotal.Load(),
		Completed:    r.completedTotal.Load(),
		Success:      r.successTotal.Load(),
		ServerErr:    r.serverErrorTotal.Load(),
		ClientErr:    r.clientErrorTotal.Load(),
		LocalReject:  r.localRejectTotal.Load(),
		QueueRejects: r.queueRejects.Load(),
		Duration:     r.signalDuration.snapshot(),
		QueueWait:    r.signalQueue.snapshot(),
		Enabled:      r.signalCapture.Load(),
	}
}

// SetScalingPublication replaces the cached scaling snapshot.
func (r *Registry) SetScalingPublication(pub ScalingPublication) {
	if r == nil {
		return
	}
	r.scalingPub.Store(pub)
}

// IncScalingDecision counts a relevant decision transition.
func (r *Registry) IncScalingDecision(workload, strategy, action, reason string) {
	if r == nil || workload == "" || strategy == "" || action == "" || reason == "" {
		return
	}
	key := workload + "|" + strategy + "|" + action + "|" + reason
	val, _ := r.scalingDecisions.LoadOrStore(key, &atomic.Uint64{})
	val.(*atomic.Uint64).Add(1)
}

// ObserveScalingPhase records one reconcile phase. phase is signal, evaluate or plan.
func (r *Registry) ObserveScalingPhase(phase string, d time.Duration) {
	if r == nil || d < 0 {
		return
	}
	switch phase {
	case "signal":
		r.phaseSignal.observe(d.Seconds())
	case "evaluate":
		r.phaseEvaluate.observe(d.Seconds())
	case "plan":
		r.phasePlan.observe(d.Seconds())
	}
}

func (r *Registry) writeScaling(b *strings.Builder) {
	raw := r.scalingPub.Load()
	pub, _ := raw.(ScalingPublication)
	if len(pub.Workloads) == 0 && !r.signalCapture.Load() {
		r.writePhaseHist(b)
		return
	}
	fmt.Fprintf(b, "# TYPE eregion_scaling_strategy_info gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_signal_valid gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_signal_age_seconds gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_signal_value gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_confirmations gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_efficiency_probe_state gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_efficiency_marginal_gain_ratio gauge\n")
	for _, wl := range pub.Workloads {
		fmt.Fprintf(b, "eregion_scaling_strategy_info{workload=%q,strategy=%q} 1\n", wl.Name, wl.Strategy)
		fmt.Fprintf(b, "eregion_scaling_confirmations{workload=%q,direction=%q} %d\n", wl.Name, "up", wl.UpStreak)
		fmt.Fprintf(b, "eregion_scaling_confirmations{workload=%q,direction=%q} %d\n", wl.Name, "down", wl.DownStreak)
		for _, state := range []string{"collect_baseline", "request_probe", "settle", "evaluate", "rollback", "cooldown"} {
			v := 0
			if wl.ProbeState == state {
				v = 1
			}
			if wl.ProbeState == "" {
				continue
			}
			fmt.Fprintf(b, "eregion_scaling_efficiency_probe_state{workload=%q,state=%q} %d\n", wl.Name, state, v)
		}
		if wl.GainValid {
			fmt.Fprintf(b, "eregion_scaling_efficiency_marginal_gain_ratio{workload=%q} %g\n", wl.Name, wl.Gain)
		}
		for _, sig := range wl.Signals {
			valid := 0
			if sig.Valid {
				valid = 1
			}
			fmt.Fprintf(b, "eregion_scaling_signal_valid{workload=%q,signal=%q} %d\n", wl.Name, sig.Name, valid)
			if sig.AgeOK {
				fmt.Fprintf(b, "eregion_scaling_signal_age_seconds{workload=%q,signal=%q} %g\n", wl.Name, sig.Name, sig.Age)
			}
			if sig.Valid {
				fmt.Fprintf(b, "eregion_scaling_signal_value{workload=%q,signal=%q,unit=%q} %g\n", wl.Name, sig.Name, sig.Unit, sig.Value)
			}
		}
	}
	fmt.Fprintf(b, "# TYPE eregion_scaling_decisions_total counter\n")
	r.scalingDecisions.Range(func(k, v any) bool {
		parts := strings.Split(k.(string), "|")
		if len(parts) != 4 {
			return true
		}
		fmt.Fprintf(b, "eregion_scaling_decisions_total{workload=%q,strategy=%q,action=%q,reason=%q} %d\n", parts[0], parts[1], parts[2], parts[3], v.(*atomic.Uint64).Load())
		return true
	})
	r.writePhaseHist(b)
}

func (r *Registry) writePhaseHist(b *strings.Builder) {
	if r.phaseSignal == nil {
		return
	}
	writePhase(b, "signal", r.phaseSignal)
	writePhase(b, "evaluate", r.phaseEvaluate)
	writePhase(b, "plan", r.phasePlan)
}

func writePhase(b *strings.Builder, phase string, h *histogram) {
	if h == nil {
		return
	}
	fmt.Fprintf(b, "# TYPE eregion_scaling_cycle_duration_seconds histogram\n")
	var cumulative uint64
	bounds := HistogramBounds()
	counts := h.snapshot()
	for i, bound := range bounds {
		if i < len(counts) {
			cumulative += counts[i]
		}
		fmt.Fprintf(b, "eregion_scaling_cycle_duration_seconds_bucket{phase=%q,le=%q} %d\n", phase, fmt.Sprintf("%g", bound), cumulative)
	}
	if len(counts) > len(bounds) {
		cumulative += counts[len(bounds)]
	}
	fmt.Fprintf(b, "eregion_scaling_cycle_duration_seconds_bucket{phase=%q,le=\"+Inf\"} %d\n", phase, cumulative)
}
