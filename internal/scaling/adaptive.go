package scaling

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

const (
	phaseBaseline = "collect_baseline"
	phaseRequest  = "request_probe"
	phaseSettle   = "settle"
	phaseEvaluate = "evaluate"
	phaseRollback = "rollback"
	phaseCooldown = "cooldown"
)

// ProbeView is one closed epoch as the adaptive machine sees it.
type ProbeView struct {
	Window         signals.SignalWindow
	WindowOK       bool
	Desired        int
	Running        int
	Starting       int
	Draining       int
	Admitted       int
	GuardLimited   bool
	MemoryPressure bool
	PredictionRisk bool
	Saturated      bool
	ResourcesStale bool
	Quota          string
	Source         string
	ConfigGen      uint64
	Generation     uint64
	Competing      int
	PoolScaled     bool
}

// ProbeHub allows one adaptive probe at a time and rotates candidates by name.
type ProbeHub struct {
	mu       sync.Mutex
	active   string
	last     map[string]time.Time
	machines map[string]*probeMachine
}

func NewProbeHub() *ProbeHub {
	return &ProbeHub{last: map[string]time.Time{}, machines: map[string]*probeMachine{}}
}

func (h *ProbeHub) Forget(name string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == name {
		h.active = ""
	}
	delete(h.machines, name)
	delete(h.last, name)
}

func (h *ProbeHub) Reset(name string) {
	h.Forget(name)
}

// Phase reports the current probe phase for metrics.
func (h *ProbeHub) Phase(name string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.machines[name]
	if m == nil || m.phase == "" {
		return phaseBaseline
	}
	return m.phase
}

// Gain returns the last accepted comparison.
func (h *ProbeHub) Gain(name string) (float64, bool) {
	if h == nil {
		return 0, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.machines[name]
	if m == nil || !m.gainValid {
		return 0, false
	}
	return m.gain, true
}

// Step advances one workload. names is the sorted adaptive set used for rotation.
func (h *ProbeHub) Step(name string, names []string, spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time) signals.DemandDecision {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.machines[name]
	if m == nil {
		m = &probeMachine{phase: phaseBaseline}
		h.machines[name] = m
	}
	wants := m.wants(spec, view, now)
	granted := false
	if h.active == name {
		granted = true
	} else if h.active == "" && wants && fairPick(h.wantList(names, spec, view, now), h.last) == name {
		granted = true
	}
	out, release, claim := m.step(spec, view, now, granted)
	if claim {
		h.active = name
	}
	if release && h.active == name {
		h.active = ""
		h.last[name] = now
	}
	return out
}

func (h *ProbeHub) wantList(names []string, spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time) []string {
	var out []string
	for _, name := range names {
		m := h.machines[name]
		if m == nil {
			m = &probeMachine{phase: phaseBaseline}
		}
		if name == spec.Name {
			if m.wants(spec, view, now) || m.probing() {
				out = append(out, name)
			}
			continue
		}
		if m.probing() || m.wants(spec, view, now) {
			out = append(out, name)
		}
	}
	return out
}

func fairPick(wants []string, last map[string]time.Time) string {
	if len(wants) == 0 {
		return ""
	}
	best := wants[0]
	for _, name := range wants[1:] {
		if fairBefore(name, best, last) {
			best = name
		}
	}
	return best
}

func fairBefore(a, b string, last map[string]time.Time) bool {
	ta, tb := last[a], last[b]
	switch {
	case ta.IsZero() && !tb.IsZero():
		return true
	case !ta.IsZero() && tb.IsZero():
		return false
	case !ta.Equal(tb):
		return ta.Before(tb)
	default:
		return a < b
	}
}

type probeMachine struct {
	phase       string
	base        []probeSample
	eval        []probeSample
	settleLeft  int
	origin      int
	started     time.Time
	coolUntil   time.Time
	gain        float64
	gainValid   bool
	anchorGen   uint64
	anchorQuota string
	anchorSrc   string
	anchorCfg   uint64
	anchorComp  int
	haveAnchor  bool
	reason      signals.DecisionReason
}

type probeSample struct {
	offered float64
	success float64
	p95     float64
	err     float64
}

func (m *probeMachine) probing() bool {
	switch m.phase {
	case phaseRequest, phaseSettle, phaseEvaluate, phaseRollback:
		return true
	default:
		return false
	}
}

func (m *probeMachine) wants(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time) bool {
	if m.probing() {
		return true
	}
	if m.phase == phaseCooldown && now.Before(m.coolUntil) {
		return false
	}
	if !probePressure(spec, view) {
		return false
	}
	if view.Desired >= spec.Workers.Max || view.Saturated || view.MemoryPressure || view.PredictionRisk || view.ResourcesStale || view.GuardLimited {
		return false
	}
	need := spec.Scaling.Adaptive.BaselineWindows
	if need == 0 {
		need = 3
	}
	return len(m.base)+1 >= need
}

func (m *probeMachine) step(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, granted bool) (signals.DemandDecision, bool, bool) {
	n := view.Desired
	if n == 0 {
		n = spec.Workers.Min
	}
	if m.phase == "" {
		m.phase = phaseBaseline
	}
	if m.haveAnchor && (view.ConfigGen != m.anchorCfg || view.Source != m.anchorSrc || view.Quota != m.anchorQuota) {
		m.reset(phaseBaseline)
	}
	if m.phase == phaseCooldown && !now.Before(m.coolUntil) {
		m.phase = phaseBaseline
		m.base = nil
	}
	if !m.probing() && sustainedIdle(view) && n > spec.Workers.Min {
		return propose(n-1, n, view.Window.Epoch, signals.ReasonDemandDown), false, false
	}
	switch m.phase {
	case phaseRequest:
		return m.stepRequest(spec, view, now, n)
	case phaseSettle:
		return m.stepSettle(spec, view, now, n)
	case phaseEvaluate:
		return m.stepEvaluate(spec, view, now, n)
	case phaseRollback:
		return m.stepRollback(spec, view, now, n)
	default:
		return m.stepBaseline(spec, view, now, n, granted)
	}
}

func (m *probeMachine) stepBaseline(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, n int, granted bool) (signals.DemandDecision, bool, bool) {
	if !acceptBaseline(view) {
		m.base = nil
		return hold(n, signals.ReasonSignalUnavailable, view.Window.Epoch, false), false, false
	}
	sample := takeSample(view)
	if len(m.base) > 0 && !withinTolerance(append(m.base, sample), spec.Scaling.Adaptive.DemandToleranceRatio) {
		m.base = nil
		return hold(n, signals.ReasonProbeConfounded, view.Window.Epoch, true), false, false
	}
	m.base = append(m.base, sample)
	need := spec.Scaling.Adaptive.BaselineWindows
	if len(m.base) < need || !probePressure(spec, view) || !granted {
		return hold(n, signals.ReasonConfirming, view.Window.Epoch, true), false, false
	}
	m.origin = n
	m.started = now
	m.phase = phaseRequest
	m.remember(view)
	m.settleLeft = spec.Scaling.Adaptive.SettleWindows
	m.eval = nil
	return propose(n+1, n, view.Window.Epoch, signals.ReasonProbeRequested), false, true
}

func (m *probeMachine) stepRequest(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, n int) (signals.DemandDecision, bool, bool) {
	if !m.started.IsZero() && now.Sub(m.started) > spec.Scaling.Adaptive.ProbeTimeout {
		m.finish(phaseCooldown, now, spec)
		m.reason = signals.ReasonProbeTimeout
		return hold(m.origin, signals.ReasonProbeTimeout, view.Window.Epoch, true), true, false
	}
	if view.GuardLimited || view.Saturated || view.MemoryPressure || view.ResourcesStale || (m.haveAnchor && view.Generation != m.anchorGen) {
		m.finish(phaseCooldown, now, spec)
		m.reason = signals.ReasonProbeGuardBlocked
		return hold(m.origin, signals.ReasonProbeGuardBlocked, view.Window.Epoch, true), true, false
	}
	if view.Quota != m.anchorQuota || view.Source != m.anchorSrc || view.ConfigGen != m.anchorCfg {
		m.finish(phaseCooldown, now, spec)
		return hold(m.origin, signals.ReasonProbeConfounded, view.Window.Epoch, true), true, false
	}
	target := m.origin + 1
	if view.Running >= target && view.Starting == 0 && view.Draining == 0 && view.Admitted >= target {
		m.phase = phaseSettle
		if m.settleLeft <= 0 {
			m.phase = phaseEvaluate
		}
	}
	out := propose(target, n, view.Window.Epoch, signals.ReasonProbeRequested)
	out.Action = "probe"
	return out, false, true
}

func (m *probeMachine) stepSettle(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, n int) (signals.DemandDecision, bool, bool) {
	if badProbe(view, m) {
		m.phase = phaseRollback
		m.reason = signals.ReasonProbeConfounded
		return propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeConfounded), false, true
	}
	if view.Running < m.origin+1 || view.Starting != 0 || view.Draining != 0 {
		return propose(m.origin+1, n, view.Window.Epoch, signals.ReasonProbeRequested), false, true
	}
	m.settleLeft--
	if m.settleLeft > 0 {
		return propose(m.origin+1, n, view.Window.Epoch, signals.ReasonProbeRequested), false, true
	}
	m.phase = phaseEvaluate
	return propose(m.origin+1, n, view.Window.Epoch, signals.ReasonProbeRequested), false, true
}

func (m *probeMachine) stepEvaluate(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, n int) (signals.DemandDecision, bool, bool) {
	if !m.started.IsZero() && now.Sub(m.started) > spec.Scaling.Adaptive.ProbeTimeout {
		m.phase = phaseRollback
		m.reason = signals.ReasonProbeTimeout
		return propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeTimeout), false, true
	}
	if badProbe(view, m) || !view.WindowOK {
		m.phase = phaseRollback
		m.reason = signals.ReasonProbeConfounded
		return propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeConfounded), false, true
	}
	sample := takeSample(view)
	if !offeredNear(sample.offered, medianOf(m.base, func(s probeSample) float64 { return s.offered }), spec.Scaling.Adaptive.DemandToleranceRatio) {
		m.phase = phaseRollback
		m.reason = signals.ReasonProbeConfounded
		return propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeConfounded), false, true
	}
	m.eval = append(m.eval, sample)
	if len(m.eval) < spec.Scaling.Adaptive.EvaluationWindows {
		return propose(m.origin+1, n, view.Window.Epoch, signals.ReasonProbeRequested), false, true
	}
	baseSuccess := medianOf(m.base, func(s probeSample) float64 { return s.success })
	probeSuccess := medianOf(m.eval, func(s probeSample) float64 { return s.success })
	baseP95 := medianOf(m.base, func(s probeSample) float64 { return s.p95 })
	probeP95 := medianOf(m.eval, func(s probeSample) float64 { return s.p95 })
	baseErr := medianOf(m.base, func(s probeSample) float64 { return s.err })
	probeErr := medianOf(m.eval, func(s probeSample) float64 { return s.err })
	if baseSuccess <= 0 || baseP95 <= 0 {
		m.phase = phaseRollback
		m.reason = signals.ReasonProbeConfounded
		return propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeConfounded), false, true
	}
	gain := (probeSuccess - baseSuccess) / baseSuccess
	reg := (probeP95 - baseP95) / baseP95
	m.gain = gain
	m.gainValid = true
	if gain >= spec.Scaling.Adaptive.MinGainRatio && reg <= spec.Scaling.Adaptive.MaxP95RegressionRatio && probeErr-baseErr <= 0.01 {
		m.finish(phaseCooldown, now, spec)
		m.reason = signals.ReasonProbeGain
		out := propose(m.origin+1, n, view.Window.Epoch, signals.ReasonProbeGain)
		out.Action = "probe"
		return out, true, false
	}
	m.phase = phaseRollback
	m.reason = signals.ReasonProbeNoGain
	out := propose(m.safeTarget(view), n, view.Window.Epoch, signals.ReasonProbeNoGain)
	out.Action = "rollback"
	return out, false, true
}

func (m *probeMachine) stepRollback(spec workload.ResolvedWorkloadSpec, view ProbeView, now time.Time, n int) (signals.DemandDecision, bool, bool) {
	target := m.safeTarget(view)
	if view.Running <= target && view.Draining == 0 && view.Starting == 0 {
		m.finish(phaseCooldown, now, spec)
		if m.reason == "" {
			m.reason = signals.ReasonProbeNoGain
		}
		out := hold(target, m.reason, view.Window.Epoch, true)
		out.Action = "rollback"
		out.RawRequested = target
		out.PolicyRequested = target
		return out, true, false
	}
	out := propose(target, n, view.Window.Epoch, m.reason)
	out.Action = "rollback"
	return out, false, true
}

func (m *probeMachine) safeTarget(view ProbeView) int {
	target := m.origin
	if view.GuardLimited && view.Admitted < target {
		target = view.Admitted
	}
	if target < 0 {
		target = 0
	}
	return target
}

func (m *probeMachine) finish(phase string, now time.Time, spec workload.ResolvedWorkloadSpec) {
	m.phase = phase
	m.base = nil
	m.eval = nil
	cool := spec.Scaling.Adaptive.ProbeCooldown
	if cool <= 0 {
		cool = 60 * time.Second
	}
	m.coolUntil = now.Add(cool)
}

func (m *probeMachine) reset(phase string) {
	*m = probeMachine{phase: phase}
}

func (m *probeMachine) remember(view ProbeView) {
	m.anchorGen = view.Generation
	m.anchorQuota = view.Quota
	m.anchorSrc = view.Source
	m.anchorCfg = view.ConfigGen
	m.anchorComp = view.Competing
	m.haveAnchor = true
}

func acceptBaseline(view ProbeView) bool {
	w := view.Window
	if !view.WindowOK || view.ResourcesStale || !view.Window.GlobalFresh {
		return false
	}
	if !w.SuccessfulRPS.Valid || !w.RequestP95Seconds.Valid || !w.OfferedRPS.Valid || w.SuccessfulRPS.Value <= 0 {
		return false
	}
	if !w.HTTPErrorRatio.Valid || w.HTTPErrorRatio.Value > 0 {
		return false
	}
	return true
}

func probePressure(spec workload.ResolvedWorkloadSpec, view ProbeView) bool {
	w := view.Window
	busy := spec.Scaling.Adaptive.BusyThreshold
	if busy <= 0 {
		busy = 0.85
	}
	if !w.BusyRatio.Valid || w.BusyRatio.Value < busy {
		return false
	}
	return w.QueueWaitingMax.Valid && (w.QueueWaitingMax.Value > 0 || (w.QueueRejects.Valid && w.QueueRejects.Value > 0))
}

func sustainedIdle(view ProbeView) bool {
	w := view.Window
	return view.WindowOK && w.BusyRatio.Valid && w.BusyRatio.Value < 0.50 && w.QueueWaitingMax.Valid && w.QueueWaitingMax.Value == 0 && w.QueueRejects.Valid && w.QueueRejects.Value == 0 && w.OfferedRPS.Valid
}

func badProbe(view ProbeView, m *probeMachine) bool {
	if view.GuardLimited || view.Saturated || view.MemoryPressure || view.ResourcesStale || view.PoolScaled {
		return true
	}
	if m.haveAnchor && view.Generation != m.anchorGen {
		return true
	}
	if m.haveAnchor && view.Competing != m.anchorComp {
		return true
	}
	w := view.Window
	if w.HTTPErrorRatio.Valid && w.HTTPErrorRatio.Value > 0 {
		return true
	}
	return false
}

func takeSample(view ProbeView) probeSample {
	w := view.Window
	s := probeSample{}
	if w.OfferedRPS.Valid {
		s.offered = w.OfferedRPS.Value
	}
	if w.SuccessfulRPS.Valid {
		s.success = w.SuccessfulRPS.Value
	}
	if w.RequestP95Seconds.Valid {
		s.p95 = w.RequestP95Seconds.Value
	}
	if w.HTTPErrorRatio.Valid {
		s.err = w.HTTPErrorRatio.Value
	}
	return s
}

func withinTolerance(samples []probeSample, tol float64) bool {
	med := medianOf(samples, func(s probeSample) float64 { return s.offered })
	for _, s := range samples {
		if !offeredNear(s.offered, med, tol) {
			return false
		}
	}
	return true
}

func offeredNear(v, med, tol float64) bool {
	if med == 0 {
		return v == 0
	}
	return math.Abs(v-med)/med <= tol
}

func medianOf(samples []probeSample, pick func(probeSample) float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	vals := make([]float64, len(samples))
	for i, s := range samples {
		vals[i] = pick(s)
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[mid]
	}
	return (vals[mid-1] + vals[mid]) / 2
}
