package signals

import (
	"sync"
	"time"
)

const (
	minP95Samples = 100
	maxBuckets    = 120
	maxSummaries  = 12
	maxTickGap    = 1500 * time.Millisecond
)

// HTTPSnap is a cumulative counter reading. The engine stores the delta.
type HTTPSnap struct {
	Offered     uint64
	Completed   uint64
	Success     uint64
	ServerErr   uint64
	ClientErr   uint64
	LocalReject uint64
	QueueReject uint64
	Duration    []uint64
	QueueWait   []uint64
}

// BacklogSnap is a cached provider reading. Valid is false when the value is unknown.
type BacklogSnap struct {
	Depth uint64
	Valid bool
	At    time.Time
}

// GlobalSnap is the resource-guard CPU sample. It is not divided across workloads.
type GlobalSnap struct {
	CPU           float64
	CPUKnown      bool
	Throttle      float64
	ThrotKnow     bool
	At            time.Time
	Source        string
	Capacity      float64
	CapacityKnown bool
}

// PoolSnap is one pool observation.
type PoolSnap struct {
	Desired  int
	Running  int
	Idle     int
	Busy     int
	Starting int
	Draining int
	Waiting  uint64
}

// TickWorkload is one workload on the shared sampling tick.
type TickWorkload struct {
	Name    string
	Window  time.Duration
	MaxAge  time.Duration
	HTTP    *HTTPSnap
	Procs   []ProcID
	Backlog BacklogSnap
	Pool    PoolSnap
}

// Engine accumulates 1s ticks into non-overlapping epochs.
type Engine struct {
	mu        sync.Mutex
	bounds    []float64
	reader    CPUReader
	ticks     float64
	series    map[string]*series
	closed    map[string]SignalWindow
	summaries map[string][]DecisionSummary
}

// DecisionSummary is one retained scaling decision.
type DecisionSummary struct {
	Epoch  uint64
	Reason string
	Policy int
}

type series struct {
	window        time.Duration
	maxAge        time.Duration
	lastAt        time.Time
	epoch         uint64
	open          []tickDelta
	prevHTTP      *HTTPSnap
	prevCPU       []procPoint
	haveCPU       bool
	haveGlobal    bool
	globalSource  string
	capacity      float64
	capacityKnown bool
}

type tickDelta struct {
	at                                              time.Time
	offered, completed, success, server, local, rej uint64
	http                                            bool
	duration, queue                                 []uint64
	waiting                                         uint64
	busy                                            float64
	busyKnown                                       bool
	cpu                                             float64
	cpuValid                                        bool
	backlog                                         uint64
	backlogValid                                    bool
	globalCPU, throttle                             float64
	globalOK, throttleOK                            bool
	pool                                            PoolSnap
	source                                          string
}

// NewEngine builds an engine. A nil reader disables workload CPU.
func NewEngine(bounds []float64, reader CPUReader) *Engine {
	if len(bounds) == 0 {
		bounds = HistogramBounds
	}
	copied := append([]float64(nil), bounds...)
	ticks := 0.0
	if r, ok := reader.(interface{ ticksPerSecond() float64 }); ok {
		ticks = r.ticksPerSecond()
	}
	if ticks == 0 && reader != nil {
		ticks = linuxTicksPerSecond
	}
	return &Engine{
		bounds:    copied,
		reader:    reader,
		ticks:     ticks,
		series:    map[string]*series{},
		closed:    map[string]SignalWindow{},
		summaries: map[string][]DecisionSummary{},
	}
}

// Forget drops one workload. Removed pools must not keep a series.
func (e *Engine) Forget(name string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.series, name)
	delete(e.closed, name)
	delete(e.summaries, name)
}

// NoteDecision keeps a bounded ring of decision summaries.
func (e *Engine) NoteDecision(name string, epoch uint64, reason string, policy int) {
	if e == nil || name == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ring := append(e.summaries[name], DecisionSummary{Epoch: epoch, Reason: reason, Policy: policy})
	if len(ring) > maxSummaries {
		ring = ring[len(ring)-maxSummaries:]
	}
	e.summaries[name] = ring
}

// Summaries returns a copy of the decision ring.
func (e *Engine) Summaries(name string) []DecisionSummary {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]DecisionSummary(nil), e.summaries[name]...)
}

// Closed returns the latest complete epoch.
func (e *Engine) Closed(name string) (SignalWindow, bool) {
	if e == nil {
		return SignalWindow{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	w, ok := e.closed[name]
	return w, ok
}

// Observe ingests one shared tick. The first tick only establishes a baseline.
func (e *Engine) Observe(now time.Time, global GlobalSnap, loads []TickWorkload) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[string]struct{}{}
	for _, load := range loads {
		seen[load.Name] = struct{}{}
		s := e.series[load.Name]
		if s == nil {
			s = &series{}
			e.series[load.Name] = s
		}
		window := load.Window
		if window <= 0 {
			window = 10 * time.Second
		}
		if s.window != window && len(s.open) > 0 {
			s.open = nil
			s.haveCPU = false
		}
		s.window = window
		s.maxAge = load.MaxAge
		if s.maxAge <= 0 {
			s.maxAge = 2 * window
		}
		if !s.lastAt.IsZero() && (now.Before(s.lastAt) || now.Sub(s.lastAt) > maxTickGap) {
			s.open = nil
			s.haveCPU = false
			s.prevHTTP = nil
		}
		if s.haveGlobal && globalIdentityChanged(s, global) {
			s.open = nil
			s.haveCPU = false
			s.prevHTTP = nil
		}
		s.haveGlobal = true
		s.globalSource = global.Source
		s.capacity = global.Capacity
		s.capacityKnown = global.CapacityKnown
		delta, ok := s.consume(now, global, load, e.reader, e.bounds, e.ticks)
		s.lastAt = now
		if !ok {
			continue
		}
		s.open = append(s.open, delta)
		if len(s.open) > maxBuckets {
			s.open = s.open[len(s.open)-maxBuckets:]
		}
		need := int(window / time.Second)
		if need < 1 {
			need = 1
		}
		if len(s.open) >= need {
			chunk := s.open[:need]
			s.open = nil
			s.epoch++
			e.closed[load.Name] = closeWindow(load.Name, s.epoch, chunk, e.bounds, s.maxAge, now)
		}
	}
}

func globalIdentityChanged(s *series, global GlobalSnap) bool {
	if global.Source != s.globalSource || s.capacityKnown != global.CapacityKnown {
		return true
	}
	return global.CapacityKnown && global.Capacity != s.capacity
}

func (s *series) consume(now time.Time, global GlobalSnap, load TickWorkload, reader CPUReader, bounds []float64, ticks float64) (tickDelta, bool) {
	curCPU := readSet(reader, load.Procs)
	var d tickDelta
	d.at = now
	d.pool = load.Pool
	d.waiting = load.Pool.Waiting
	d.source = global.Source
	d.globalCPU = global.CPU
	d.globalOK = global.CPUKnown && !global.At.IsZero() && now.Sub(global.At) <= s.maxAge && !now.Before(global.At)
	d.throttle = global.Throttle
	d.throttleOK = global.ThrotKnow && d.globalOK
	if load.HTTP != nil && load.Pool.Busy+load.Pool.Idle > 0 {
		d.busy = float64(load.Pool.Busy) / float64(load.Pool.Busy+load.Pool.Idle)
		d.busyKnown = true
	}
	if load.Backlog.Valid && !load.Backlog.At.IsZero() {
		age := now.Sub(load.Backlog.At)
		if age >= 0 && age <= s.maxAge {
			d.backlog = load.Backlog.Depth
			d.backlogValid = true
		}
	}
	if s.haveCPU && reader != nil {
		ratio, reason := AttributeCPU(s.prevCPU, curCPU, now.Sub(s.lastAt), equivalentWorkers(load), ticks)
		d.cpu = ratio
		d.cpuValid = reason == ""
	}
	s.prevCPU = curCPU
	s.haveCPU = true

	if load.HTTP == nil {
		if s.prevHTTP == nil && s.lastAt.IsZero() {
			return tickDelta{}, false
		}
		if s.lastAt.IsZero() {
			return tickDelta{}, false
		}
		return d, true
	}
	if s.prevHTTP == nil {
		s.prevHTTP = cloneSnap(load.HTTP)
		return tickDelta{}, false
	}
	prev := s.prevHTTP
	next := load.HTTP
	var ok bool
	if d.offered, ok = mono(prev.Offered, next.Offered); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	if d.completed, ok = mono(prev.Completed, next.Completed); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	if d.success, ok = mono(prev.Success, next.Success); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	if d.server, ok = mono(prev.ServerErr, next.ServerErr); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	if d.local, ok = mono(prev.LocalReject, next.LocalReject); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	if d.rej, ok = mono(prev.QueueReject, next.QueueReject); !ok {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	var reason InvalidReason
	d.duration, reason = deltaBuckets(prev.Duration, next.Duration)
	if reason != "" {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	d.queue, reason = deltaBuckets(prev.QueueWait, next.QueueWait)
	if reason != "" {
		s.prevHTTP = cloneSnap(next)
		s.open = nil
		return tickDelta{}, false
	}
	d.http = true
	s.prevHTTP = cloneSnap(next)
	return d, true
}

func equivalentWorkers(load TickWorkload) int {
	n := 0
	for _, p := range load.Procs {
		if p.PID > 0 {
			n++
		}
	}
	return n
}

func readSet(reader CPUReader, procs []ProcID) []procPoint {
	if reader == nil {
		return nil
	}
	out := make([]procPoint, 0, len(procs))
	for _, p := range procs {
		got := reader.Read(p.PID)
		if !got.OK {
			continue
		}
		out = append(out, procPoint{pid: p.PID, gen: p.Generation, start: got.Start, ticks: got.User + got.Sys})
	}
	return out
}

func mono(prev, next uint64) (uint64, bool) {
	if next < prev {
		return 0, false
	}
	return next - prev, true
}

func deltaBuckets(prev, next []uint64) ([]uint64, InvalidReason) {
	if len(prev) == 0 && len(next) == 0 {
		return nil, ""
	}
	if len(prev) != len(next) {
		return nil, ReasonReset
	}
	out := make([]uint64, len(next))
	for i := range next {
		if next[i] < prev[i] {
			return nil, ReasonReset
		}
		out[i] = next[i] - prev[i]
	}
	return out, ""
}

func cloneSnap(in *HTTPSnap) *HTTPSnap {
	if in == nil {
		return nil
	}
	out := *in
	out.Duration = append([]uint64(nil), in.Duration...)
	out.QueueWait = append([]uint64(nil), in.QueueWait...)
	return &out
}

func closeWindow(name string, epoch uint64, ticks []tickDelta, bounds []float64, maxAge time.Duration, now time.Time) SignalWindow {
	w := SignalWindow{Workload: name, Epoch: epoch}
	if len(ticks) == 0 {
		return w
	}
	w.From = ticks[0].at.Add(-time.Second)
	w.To = ticks[len(ticks)-1].at
	last := ticks[len(ticks)-1]
	w.Pool = PoolSnapshot{
		Desired: last.pool.Desired, Running: last.pool.Running, Idle: last.pool.Idle,
		Busy: last.pool.Busy, Starting: last.pool.Starting, Draining: last.pool.Draining,
	}
	seconds := w.To.Sub(w.From).Seconds()
	if seconds <= 0 {
		seconds = float64(len(ticks))
	}
	var offered, completed, success, server, local, rejects, waitMax uint64
	httpTicks := 0
	var busySum float64
	var busyN int
	cpuSum := 0.0
	cpuN := 0
	cpuBad := false
	globalOK := true
	throttleOK := true
	var gCPU, gThr float64
	backOK := true
	var backlog uint64
	dur := make([]uint64, len(bounds)+1)
	que := make([]uint64, len(bounds)+1)
	for _, t := range ticks {
		if t.http {
			httpTicks++
		}
		offered += t.offered
		completed += t.completed
		success += t.success
		server += t.server
		local += t.local
		rejects += t.rej
		if t.waiting > waitMax {
			waitMax = t.waiting
		}
		if t.busyKnown {
			busySum += t.busy
			busyN++
		}
		if t.cpuValid {
			cpuSum += t.cpu
			cpuN++
		} else {
			cpuBad = true
		}
		if !t.globalOK {
			globalOK = false
		} else {
			gCPU = t.globalCPU
		}
		if !t.throttleOK {
			throttleOK = false
		} else {
			gThr = t.throttle
		}
		if !t.backlogValid {
			backOK = false
		} else {
			backlog = t.backlog
		}
		addBuckets(dur, t.duration)
		addBuckets(que, t.queue)
	}
	fresh := func(v float64) Sample[float64] {
		return Sample[float64]{Value: v, Valid: true, Source: last.source, From: w.From, To: w.To}
	}
	if httpTicks == len(ticks) {
		w.OfferedRPS = fresh(float64(offered) / seconds)
		w.CompletedRPS = fresh(float64(completed) / seconds)
		w.SuccessfulRPS = fresh(float64(success) / seconds)
		w.QueueWaitingMax = Sample[uint64]{Value: waitMax, Valid: true, From: w.From, To: w.To}
		w.QueueRejects = Sample[uint64]{Value: rejects, Valid: true, From: w.From, To: w.To}
		if completed > 0 {
			w.HTTPErrorRatio = fresh(float64(server) / float64(completed))
		} else {
			w.HTTPErrorRatio = Sample[float64]{Reason: ReasonInsufficient, From: w.From, To: w.To}
		}
	} else {
		missing := Sample[float64]{Reason: ReasonMissing, From: w.From, To: w.To}
		w.OfferedRPS = missing
		w.CompletedRPS = missing
		w.SuccessfulRPS = missing
		w.HTTPErrorRatio = missing
		w.QueueWaitingMax = Sample[uint64]{Reason: ReasonMissing, From: w.From, To: w.To}
		w.QueueRejects = Sample[uint64]{Reason: ReasonMissing, From: w.From, To: w.To}
	}
	if busyN == len(ticks) && busyN > 0 {
		w.BusyRatio = fresh(busySum / float64(busyN))
	} else {
		w.BusyRatio = Sample[float64]{Reason: ReasonInsufficient, From: w.From, To: w.To}
	}
	if !cpuBad && cpuN == len(ticks) {
		w.WorkerCPURatio = fresh(cpuSum / float64(cpuN))
		w.WorkerCPURatio.Source = "proc"
	} else if !WorkerCPUSupported() {
		w.WorkerCPURatio = Sample[float64]{Reason: ReasonUnsupported, From: w.From, To: w.To}
	} else {
		w.WorkerCPURatio = Sample[float64]{Reason: ReasonMixedGeneration, From: w.From, To: w.To}
	}
	if globalOK {
		w.ResourceCPURatio = fresh(gCPU)
		w.GlobalFresh = true
	} else {
		w.ResourceCPURatio = Sample[float64]{Reason: ReasonMissing, From: w.From, To: w.To}
	}
	if throttleOK {
		w.CPUThrottleRatio = Sample[float64]{Value: gThr, Valid: true, Source: last.source, From: w.From, To: w.To}
	} else {
		w.CPUThrottleRatio = Sample[float64]{Reason: ReasonMissing, From: w.From, To: w.To}
	}
	if backOK {
		w.BacklogDepth = Sample[uint64]{Value: backlog, Valid: true, Source: "http_json", From: w.From, To: w.To}
	} else {
		w.BacklogDepth = Sample[uint64]{Reason: ReasonStale, From: w.From, To: w.To}
	}
	req := Quantile(bounds, dur, 0.95, minP95Samples)
	w.RequestP95Seconds = percentileSample(req, w.From, w.To)
	w.RequestP95Low = req.Low
	w.RequestP95High = req.High
	qw := Quantile(bounds, que, 0.95, minP95Samples)
	w.QueueWaitP95Seconds = percentileSample(qw, w.From, w.To)
	w.QueueP95Low = qw.Low
	w.QueueP95High = qw.High
	if now.Sub(w.To) > maxAge {
		invalidate(&w, ReasonStale)
	}
	return w
}

func percentileSample(p Percentile, from, to time.Time) Sample[float64] {
	if !p.Valid {
		return Sample[float64]{Reason: p.Reason, From: from, To: to}
	}
	return Sample[float64]{Value: p.Value, Valid: true, From: from, To: to}
}

func addBuckets(dst, src []uint64) {
	if len(src) == 0 {
		return
	}
	for i := range dst {
		if i < len(src) {
			dst[i] += src[i]
		}
	}
}

func invalidate(w *SignalWindow, reason InvalidReason) {
	w.OfferedRPS.Valid = false
	w.OfferedRPS.Reason = reason
	w.CompletedRPS.Valid = false
	w.CompletedRPS.Reason = reason
	w.SuccessfulRPS.Valid = false
	w.SuccessfulRPS.Reason = reason
	w.RequestP95Seconds.Valid = false
	w.RequestP95Seconds.Reason = reason
	w.QueueWaitP95Seconds.Valid = false
	w.QueueWaitP95Seconds.Reason = reason
	w.WorkerCPURatio.Valid = false
	w.WorkerCPURatio.Reason = reason
	w.ResourceCPURatio.Valid = false
	w.ResourceCPURatio.Reason = reason
	w.GlobalFresh = false
}
