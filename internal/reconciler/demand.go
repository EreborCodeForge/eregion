package reconciler

import (
	"context"
	"sort"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func (r *Reconciler) observeAdvanced(specs []workload.ResolvedWorkloadSpec, pools map[string]*worker.Pool) {
	if r.signals == nil {
		return
	}
	now := r.now()
	loads := make([]signals.TickWorkload, 0, len(specs))
	var httpSnap *signals.HTTPSnap
	if r.metrics != nil && r.metrics.SignalCaptureEnabled() {
		got := r.metrics.HTTPSignalSnapshot()
		httpSnap = &signals.HTTPSnap{
			Offered: got.Offered, Completed: got.Completed, Success: got.Success,
			ServerErr: got.ServerErr, ClientErr: got.ClientErr, LocalReject: got.LocalReject,
			QueueReject: got.QueueRejects, Duration: got.Duration, QueueWait: got.QueueWait,
		}
	}
	global := signals.GlobalSnap{}
	if r.guard != nil && r.guard.Collector != nil {
		sample := r.guard.Collector.Current(now)
		global = signals.GlobalSnap{
			CPU: sample.CPUUtilization, CPUKnown: resources.Known(sample.CPUUtilizationValid),
			Throttle: sample.CPUThrottledRatio, ThrotKnow: resources.Known(sample.CPUThrottlingValid),
			At: sample.At, Source: string(sample.Source),
			Capacity: sample.CPUCapacity, CapacityKnown: resources.Known(sample.CPUCapacityValid),
		}
	}
	for _, spec := range specs {
		if !workload.IsAdvanced(spec.Scaling.Strategy) {
			continue
		}
		if r.guard != nil && r.guard.Mode() == "observe" && !r.warned {
			r.logger.Warn("resource_guard_not_enforcing", "workload", spec.Name, "strategy", spec.Scaling.Strategy)
			r.warned = true
		}
		pool := pools[spec.Name]
		if pool == nil {
			continue
		}
		snap := pool.Snapshot()
		load := signals.TickWorkload{
			Name: spec.Name, Window: spec.Scaling.Control.Window, MaxAge: spec.Scaling.Control.MaxSignalAge,
			Pool: signals.PoolSnap{
				Desired: snap.Desired, Running: snap.Running, Idle: snap.Idle, Busy: snap.Busy,
				Starting: snap.Starting, Draining: snap.Draining, Waiting: uint64(snap.Waiting),
			},
		}
		if spec.Mode == workload.ModeHTTP && httpSnap != nil {
			load.HTTP = httpSnap
		}
		if r.backlog != nil {
			r.backlog.Refresh(spec.Name, now)
			value, valid, at, configured := r.backlog.Snapshot(spec.Name, now)
			if configured && value >= 0 {
				depth := uint64(value)
				load.Backlog = signals.BacklogSnap{Depth: depth, Valid: valid, At: at}
			}
		}
		for _, proc := range pool.ManagedProcesses() {
			load.Procs = append(load.Procs, signals.ProcID{PID: proc.PID, Generation: proc.Generation})
		}
		loads = append(loads, load)
	}
	if len(loads) == 0 {
		return
	}
	started := time.Now()
	r.signals.Observe(now, global, loads)
	if r.metrics != nil {
		r.metrics.ObserveScalingPhase("signal", time.Since(started))
	}
}

func (r *Reconciler) advancedTarget(spec workload.ResolvedWorkloadSpec, pool *worker.Pool, snap worker.PoolSnapshot) (int, int) {
	now := r.now()
	window, ok := r.signals.Closed(spec.Name)
	poolState := scaling.PoolStability{Desired: snap.Desired, Running: snap.Running, Starting: snap.Starting, Draining: snap.Draining}
	var decision signals.DemandDecision
	if spec.Scaling.Strategy == workload.StrategyAdaptive {
		decision = r.adaptiveDecision(spec, window, ok, poolState, now)
	} else if spec.Scaling.Strategy == workload.StrategyScheduled {
		decision = scaling.EvaluateDemand(spec, window, poolState, now)
		decision.Valid = true
	} else if !ok {
		decision = signals.DemandDecision{RawRequested: poolState.N(), Reason: signals.ReasonSignalUnavailable, Epoch: 0, Action: "hold"}
	} else {
		decision = scaling.EvaluateDemand(spec, window, poolState, now)
	}
	veto, why := scaling.ExpansionVeto(spec.Scaling.Strategy, spec.Mode, window)
	if decision.RawRequested <= poolState.N() {
		veto = false
	}
	out := r.control.Apply(spec, decision, poolState, now, veto, why)
	r.noteDecision(spec, out)
	return out.RawRequested, out.PolicyRequested
}

func (r *Reconciler) adaptiveDecision(spec workload.ResolvedWorkloadSpec, window signals.SignalWindow, ok bool, pool scaling.PoolStability, now time.Time) signals.DemandDecision {
	view := scaling.ProbeView{
		Window: window, WindowOK: ok,
		Desired: pool.Desired, Running: pool.Running, Starting: pool.Starting, Draining: pool.Draining,
		Admitted: pool.Desired,
	}
	if r.guard != nil && r.guard.Collector != nil {
		sample := r.guard.Sample(context.Background())
		memKnown := resources.Known(sample.MemoryCurrentValid)
		cpuKnown := resources.Known(sample.CPUUtilizationValid)
		view.ResourcesStale = !memKnown || (!cpuKnown && !view.Window.GlobalFresh)
		view.Saturated = cpuKnown && sample.CPUUtilization >= 0.90
		view.MemoryPressure = memKnown && sample.MemoryLimitBytes > 0 && float64(sample.MemoryCurrentBytes) >= 0.90*float64(sample.MemoryLimitBytes)
	}
	var names []string
	for _, other := range r.registry.List() {
		if other.Scaling.Strategy == workload.StrategyAdaptive {
			names = append(names, other.Name)
		}
	}
	if r.probeEpoch == nil {
		r.probeEpoch = map[string]uint64{}
		r.probeLast = map[string]signals.DemandDecision{}
	}
	// The probe counts closed windows. Reconcile runs every second, so a repeated epoch must not add another sample.
	if ok && window.Epoch != 0 && window.Epoch == r.probeEpoch[spec.Name] {
		if last, found := r.probeLast[spec.Name]; found {
			return last
		}
	}
	out := r.probes.Step(spec.Name, names, spec, view, now)
	if ok && window.Epoch != 0 {
		r.probeEpoch[spec.Name] = window.Epoch
		r.probeLast[spec.Name] = out
	}
	return out
}

func (r *Reconciler) noteDecision(spec workload.ResolvedWorkloadSpec, out signals.DemandDecision) {
	if r.signals != nil {
		r.signals.NoteDecision(spec.Name, out.Epoch, string(out.Reason), out.PolicyRequested)
	}
	if r.metrics != nil {
		r.publishScaling(spec, out)
	}
	key := string(out.Action) + "|" + string(out.Reason) + "|" + itoa(out.PolicyRequested)
	if r.lastLog[spec.Name] == key {
		return
	}
	r.lastLog[spec.Name] = key
	if r.metrics != nil && out.Action != "" && out.Action != "hold" {
		r.metrics.IncScalingDecision(spec.Name, string(spec.Scaling.Strategy), out.Action, string(out.Reason))
	}
}

func (r *Reconciler) publishScaling(spec workload.ResolvedWorkloadSpec, out signals.DemandDecision) {
	window, ok := r.signals.Closed(spec.Name)
	up, down := r.control.Streaks(spec.Name)
	row := telemetry.ScalingWorkloadPublication{
		Name: spec.Name, Strategy: string(spec.Scaling.Strategy), Target: out.PolicyRequested,
		UpStreak: up, DownStreak: down,
	}
	if spec.Scaling.Strategy == workload.StrategyAdaptive {
		row.ProbeState = r.probes.Phase(spec.Name)
		if gain, valid := r.probes.Gain(spec.Name); valid {
			row.Gain = gain
			row.GainValid = true
		}
	}
	if ok {
		row.Signals = signalRows(window)
	}
	if r.published == nil {
		r.published = map[string]telemetry.ScalingWorkloadPublication{}
	}
	r.published[spec.Name] = row
	names := make([]string, 0, len(r.published))
	for name := range r.published {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]telemetry.ScalingWorkloadPublication, 0, len(names))
	for _, name := range names {
		rows = append(rows, r.published[name])
	}
	r.metrics.SetScalingPublication(telemetry.ScalingPublication{Workloads: rows})
}

func signalRows(w signals.SignalWindow) []telemetry.ScalingSignal {
	age := time.Since(w.To).Seconds()
	one := func(name, unit string, valid bool, value float64) telemetry.ScalingSignal {
		return telemetry.ScalingSignal{Name: name, Unit: unit, Valid: valid, Value: value, Age: age, AgeOK: !w.To.IsZero()}
	}
	return []telemetry.ScalingSignal{
		one("offered_rps", "requests_per_second", w.OfferedRPS.Valid, w.OfferedRPS.Value),
		one("successful_rps", "requests_per_second", w.SuccessfulRPS.Valid, w.SuccessfulRPS.Value),
		one("request_p95", "seconds", w.RequestP95Seconds.Valid, w.RequestP95Seconds.Value),
		one("worker_cpu_ratio", "ratio", w.WorkerCPURatio.Valid, w.WorkerCPURatio.Value),
		one("resource_cpu_ratio", "ratio", w.ResourceCPURatio.Valid, w.ResourceCPURatio.Value),
		one("backlog_depth", "messages", w.BacklogDepth.Valid, float64(w.BacklogDepth.Value)),
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
