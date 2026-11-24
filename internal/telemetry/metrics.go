package telemetry

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

// GuardObservation is the last resource-guard publication. The metrics handler only reads it.
type GuardObservation struct {
	Mode          string
	State         string
	Sample        resources.Sample
	Headroom      uint64
	HeadroomKnown bool
	AgeSeconds    float64
	AgeKnown      bool
	Horizon       string
	Workloads     []GuardWorkload
}

// GuardWorkload is one workload row of the guard publication.
type GuardWorkload struct {
	Name       string
	Requested  int
	Policy     int
	Admitted   int
	WouldLimit int
	Limited    int
	Deficit    int
	Reason     string
}

// Registry holds Prometheus-style counters and gauges.
type Registry struct {
	cfg   config.Config
	pools *worker.Manager
	pool  *worker.Pool // legacy HTTP pool helper when present

	// Cached once at startup (resource detection is not re-run per scrape).
	runtimeCPULogical     float64
	runtimeCPUAvailable   float64
	runtimeGOMAXPROCS     float64
	runtimeMemoryLimit    float64
	workersPerCPU         float64
	workersRecommended    float64
	workersRecommendedMin float64
	workersRecommendedMax float64

	requestsTotal   atomic.Uint64
	inFlight        atomic.Int64
	errorsTotal     sync.Map // kind -> *atomic.Uint64
	queueRejects    atomic.Uint64
	durationCount   atomic.Uint64
	durationSum     atomic.Uint64 // nanoseconds
	durationHist    *histogram
	queueWaitCount  atomic.Uint64
	queueWaitSum    atomic.Uint64 // nanoseconds
	queueWaitHist   *histogram
	roundtripCount  atomic.Uint64
	roundtripSum    atomic.Uint64 // nanoseconds
	roundtripHist   *histogram
	replaceDuration sync.Map // reason -> *nsAccum
	recycleTotal    sync.Map
	restartTotal    atomic.Uint64
	restartByWL     sync.Map // workload -> *atomic.Uint64
	scaleByWL       sync.Map // "name|direction" -> *atomic.Uint64
	guardRecycle    sync.Map // "workload|reason" -> *atomic.Uint64
	guardSuppressed sync.Map // workload -> *atomic.Uint64
	guardHalfOpen   sync.Map // "workload|result" -> *atomic.Uint64
	exitEvents      sync.Map // "workload|reason" -> *atomic.Uint64
	backlog         BacklogStats
	guardObs        atomic.Value // GuardObservation
	admissionDenied sync.Map     // "workload|reason" -> *atomic.Uint64

	signalCapture    atomic.Bool
	completedTotal   atomic.Uint64
	successTotal     atomic.Uint64
	clientErrorTotal atomic.Uint64
	serverErrorTotal atomic.Uint64
	localRejectTotal atomic.Uint64
	signalDuration   *histogram
	signalQueue      *histogram
	scalingPub       atomic.Value // ScalingPublication
	scalingDecisions sync.Map     // workload|strategy|action|reason -> *atomic.Uint64
	phaseSignal      *histogram
	phaseEvaluate    *histogram
	phasePlan        *histogram
}

// BacklogStats is the last backlog sample for one workload. measured is false when the value is unknown.
type BacklogStats interface {
	Stats(workloadName string) (messages int, measured bool, ageSeconds float64, configured bool)
}

// NewRegistry creates metrics backed by the pool manager and cached resource sizing.
func NewRegistry(cfg config.Config, pools *worker.Manager, res resources.RuntimeResources, sz sizing.WorkerSizing) *Registry {
	memLimit := float64(0)
	if res.MemoryLimitKnown {
		memLimit = float64(res.MemoryLimitBytes)
	}
	httpPool, _ := pools.HTTP()
	return &Registry{
		cfg:                   cfg,
		pools:                 pools,
		pool:                  httpPool,
		runtimeCPULogical:     float64(res.LogicalCPUs),
		runtimeCPUAvailable:   res.AvailableCPUs,
		runtimeGOMAXPROCS:     float64(res.GOMAXPROCS),
		runtimeMemoryLimit:    memLimit,
		workersPerCPU:         sz.WorkersPerCPU,
		workersRecommended:    float64(sz.RecommendedWorkers),
		workersRecommendedMin: float64(sz.RecommendedMin),
		workersRecommendedMax: float64(sz.RecommendedMax),
		durationHist:          newHistogram(),
		queueWaitHist:         newHistogram(),
		roundtripHist:         newHistogram(),
		signalDuration:        newHistogram(),
		signalQueue:           newHistogram(),
		phaseSignal:           newHistogram(),
		phaseEvaluate:         newHistogram(),
		phasePlan:             newHistogram(),
	}
}

// SetBacklogStats attaches the opt-in backlog sampler. Nil keeps the series absent.
func (r *Registry) SetBacklogStats(stats BacklogStats) {
	if r == nil {
		return
	}
	r.backlog = stats
}

// SetGuardObservation replaces the cached resource-guard snapshot. It does not sample.
func (r *Registry) SetGuardObservation(obs GuardObservation) {
	if r == nil {
		return
	}
	r.guardObs.Store(obs)
}

// IncAdmissionDenied counts a refused spawn. workload and reason must already be closed enums.
func (r *Registry) IncAdmissionDenied(workload, reason string) {
	if r == nil || workload == "" || reason == "" {
		return
	}
	key := workload + "|" + reason
	val, _ := r.admissionDenied.LoadOrStore(key, &atomic.Uint64{})
	val.(*atomic.Uint64).Add(1)
}

func (r *Registry) IncRequests()    { r.requestsTotal.Add(1) }
func (r *Registry) IncInFlight()    { r.inFlight.Add(1) }
func (r *Registry) DecInFlight()    { r.inFlight.Add(-1) }
func (r *Registry) IncQueueReject() { r.queueRejects.Add(1) }
func (r *Registry) ObserveDuration(sec float64) {
	observeNanos(&r.durationCount, &r.durationSum, sec)
	r.durationHist.observe(sec)
}
func (r *Registry) ObserveQueueWait(sec float64) {
	observeNanos(&r.queueWaitCount, &r.queueWaitSum, sec)
	r.queueWaitHist.observe(sec)
}
func (r *Registry) ObserveRoundtrip(sec float64) {
	observeNanos(&r.roundtripCount, &r.roundtripSum, sec)
	r.roundtripHist.observe(sec)
}

// ObserveReplace records one completed planned replacement.
// seconds is the gap from the worker leaving service until its replacement is idle.
func (r *Registry) ObserveReplace(reason string, sec float64) {
	if reason == "" {
		reason = "planned"
	}
	v, _ := r.replaceDuration.LoadOrStore(reason, &nsAccum{})
	v.(*nsAccum).observe(sec)
}

// nsAccum is a count and a nanosecond sum for one labeled series.
type nsAccum struct {
	count atomic.Uint64
	sum   atomic.Uint64
}

func (a *nsAccum) observe(sec float64) {
	observeNanos(&a.count, &a.sum, sec)
}

// observeNanos counts one sample. Negative, NaN, and infinite values add nothing
// to the sum so sub-millisecond samples are not truncated to zero.
func observeNanos(count, sum *atomic.Uint64, sec float64) {
	count.Add(1)
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return
	}
	ns := math.Round(sec * 1e9)
	if math.IsNaN(ns) || math.IsInf(ns, 0) || ns <= 0 {
		return
	}
	// Stay inside the exact integer range of float64 so the uint64 conversion is defined.
	if ns > float64(1<<53) {
		ns = float64(1 << 53)
	}
	sum.Add(uint64(ns))
}

func nanosToSeconds(ns uint64) float64 {
	return float64(ns) / 1e9
}

func (r *Registry) IncError(kind string) {
	v, _ := r.errorsTotal.LoadOrStore(kind, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}
func (r *Registry) IncRecycle(reason string) {
	// Planned recycles stay on eregion_worker_recycles_total.
	// guard_state_violation is also counted on eregion_worker_guard_recycle_events_total.
	v, _ := r.recycleTotal.LoadOrStore(reason, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}
func (r *Registry) IncExit(workload, reason string) {
	if workload == "" {
		workload = "http"
	}
	switch reason {
	case "planned", "crash", "timeout", "protocol", "other":
	default:
		reason = "other"
	}
	key := workload + "|" + reason
	v, _ := r.exitEvents.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

func (r *Registry) IncRestart() {
	r.restartTotal.Add(1)
	name := "http"
	if r.pool != nil {
		name = r.pool.Name()
	}
	r.incRestartWL(name)
}

func (r *Registry) incRestartWL(workload string) {
	v, _ := r.restartByWL.LoadOrStore(workload, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// IncScale implements worker.ScaleEventRecorder.
func (r *Registry) IncScale(workload, direction string) {
	key := workload + "|" + direction
	v, _ := r.scaleByWL.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// IncGuardRecycle counts a guard_state_violation recycle. Other reasons collapse to other.
func (r *Registry) IncGuardRecycle(workload, reason string) {
	reason = worker.NormalizeGuardReason(reason)
	key := workload + "|" + reason
	v, _ := r.guardRecycle.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// IncGuardSuppressed counts a boot the breaker refused.
func (r *Registry) IncGuardSuppressed(workload string) {
	v, _ := r.guardSuppressed.LoadOrStore(workload, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// IncGuardHalfOpen counts a half-open trial result: admitted, success or failure.
func (r *Registry) IncGuardHalfOpen(workload, result string) {
	switch result {
	case "admitted", "success", "failure":
	default:
		result = "failure"
	}
	key := workload + "|" + result
	v, _ := r.guardHalfOpen.LoadOrStore(key, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

// ForWorkload returns a PoolMetrics scoped to a workload name.
func (r *Registry) ForWorkload(name string) worker.PoolMetrics {
	return workloadMetrics{reg: r, name: name}
}

type workloadMetrics struct {
	reg  *Registry
	name string
}

func (m workloadMetrics) IncRecycle(reason string) { m.reg.IncRecycle(reason) }
func (m workloadMetrics) ObserveReplace(reason string, sec float64) {
	m.reg.ObserveReplace(reason, sec)
}
func (m workloadMetrics) IncExit(workload, reason string) {
	if workload == "" {
		workload = m.name
	}
	m.reg.IncExit(workload, reason)
}

func (m workloadMetrics) IncRestart() {
	m.reg.restartTotal.Add(1)
	m.reg.incRestartWL(m.name)
}
func (m workloadMetrics) IncGuardRecycle(workload, reason string) {
	m.reg.IncGuardRecycle(workload, reason)
}
func (m workloadMetrics) IncGuardSuppressed(workload string) { m.reg.IncGuardSuppressed(workload) }
func (m workloadMetrics) IncGuardHalfOpen(workload, result string) {
	m.reg.IncGuardHalfOpen(workload, result)
}

// Handler serves /metrics in Prometheus text format.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		writeGauge := func(name string, val float64) {
			fmt.Fprintf(&b, "# TYPE %s gauge\n%s %g\n", name, name, val)
		}
		writeCounter := func(name string, val uint64) {
			fmt.Fprintf(&b, "# TYPE %s counter\n%s %d\n", name, name, val)
		}

		writeCounter("eregion_http_requests_total", r.requestsTotal.Load())
		writeGauge("eregion_http_requests_in_flight", float64(r.inFlight.Load()))
		writeCounter("eregion_http_queue_rejects_total", r.queueRejects.Load())
		r.durationHist.write(&b, "eregion_http_request_duration_seconds")
		fmt.Fprintf(&b, "eregion_http_request_duration_seconds_count %d\n", r.durationCount.Load())
		fmt.Fprintf(&b, "eregion_http_request_duration_seconds_sum %g\n", nanosToSeconds(r.durationSum.Load()))
		r.queueWaitHist.write(&b, "eregion_queue_wait_duration_seconds")
		fmt.Fprintf(&b, "eregion_queue_wait_duration_seconds_count %d\n", r.queueWaitCount.Load())
		fmt.Fprintf(&b, "eregion_queue_wait_duration_seconds_sum %g\n", nanosToSeconds(r.queueWaitSum.Load()))
		r.roundtripHist.write(&b, "eregion_worker_roundtrip_duration_seconds")
		fmt.Fprintf(&b, "eregion_worker_roundtrip_duration_seconds_count %d\n", r.roundtripCount.Load())
		fmt.Fprintf(&b, "eregion_worker_roundtrip_duration_seconds_sum %g\n", nanosToSeconds(r.roundtripSum.Load()))

		r.errorsTotal.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_http_errors_total{kind=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})

		writeGauge("eregion_runtime_cpu_logical", r.runtimeCPULogical)
		writeGauge("eregion_runtime_cpu_available", r.runtimeCPUAvailable)
		writeGauge("eregion_runtime_gomaxprocs", r.runtimeGOMAXPROCS)
		writeGauge("eregion_runtime_memory_limit_bytes", r.runtimeMemoryLimit)

		// Legacy single-pool gauges (HTTP when present).
		if r.pool != nil {
			snap := r.pool.Snapshot()
			writeGauge("eregion_workers_desired", float64(snap.Desired))
			writeGauge("eregion_workers_running", float64(snap.Running))
			writeGauge("eregion_workers_idle", float64(snap.Idle))
			writeGauge("eregion_workers_busy", float64(snap.Busy))
			writeGauge("eregion_workers_starting", float64(snap.Starting))
			writeGauge("eregion_workers_draining", float64(snap.Draining))
			writeGauge("eregion_workers_failed", float64(snap.Failed))
			writeGauge("eregion_queue_waiting", float64(snap.Waiting))
			writeGauge("eregion_queue_capacity", float64(snap.Capacity))
			writeGauge("eregion_worker_failed_slots", float64(snap.Failed))
		}

		writeGauge("eregion_workers_max_requests", float64(r.cfg.Workers.MaxRequests))
		writeGauge("eregion_workers_per_cpu", r.workersPerCPU)
		writeGauge("eregion_workers_recommended", r.workersRecommended)
		writeGauge("eregion_workers_recommended_min", r.workersRecommendedMin)
		writeGauge("eregion_workers_recommended_max", r.workersRecommendedMax)
		writeCounter("eregion_worker_restarts_total", r.restartTotal.Load())
		fmt.Fprintf(&b, "# TYPE eregion_workload_worker_exit_events_total counter\n")
		r.exitEvents.Range(func(k, v any) bool {
			parts := strings.SplitN(k.(string), "|", 2)
			if len(parts) != 2 {
				return true
			}
			fmt.Fprintf(&b, "eregion_workload_worker_exit_events_total{workload=%q,reason=%q} %d\n", parts[0], parts[1], v.(*atomic.Uint64).Load())
			return true
		})
		r.recycleTotal.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_worker_recycles_total{reason=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})
		fmt.Fprintf(&b, "# TYPE eregion_worker_replace_duration_seconds summary\n")
		r.replaceDuration.Range(func(k, v any) bool {
			acc := v.(*nsAccum)
			reason := k.(string)
			fmt.Fprintf(&b, "eregion_worker_replace_duration_seconds_count{reason=%q} %d\n", reason, acc.count.Load())
			fmt.Fprintf(&b, "eregion_worker_replace_duration_seconds_sum{reason=%q} %g\n", reason, nanosToSeconds(acc.sum.Load()))
			return true
		})

		// Per-workload metrics.
		fmt.Fprintf(&b, "# TYPE eregion_workload_desired_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_running_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_busy_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_draining_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_failed_workers gauge\n")
		if r.backlog != nil {
			fmt.Fprintf(&b, "# TYPE eregion_workload_backlog_messages gauge\n")
			fmt.Fprintf(&b, "# TYPE eregion_workload_backlog_collection_success gauge\n")
			fmt.Fprintf(&b, "# TYPE eregion_workload_backlog_last_success_age_seconds gauge\n")
		}
		for _, p := range r.pools.List() {
			snap := p.Snapshot()
			name := p.Name()
			fmt.Fprintf(&b, "eregion_workload_desired_workers{workload=%q} %d\n", name, snap.Desired)
			fmt.Fprintf(&b, "eregion_workload_running_workers{workload=%q} %d\n", name, snap.Running)
			fmt.Fprintf(&b, "eregion_workload_busy_workers{workload=%q} %d\n", name, snap.Busy)
			fmt.Fprintf(&b, "eregion_workload_draining_workers{workload=%q} %d\n", name, snap.Draining)
			fmt.Fprintf(&b, "eregion_workload_failed_workers{workload=%q} %d\n", name, snap.Failed)
			if r.backlog != nil {
				messages, measured, age, configured := r.backlog.Stats(name)
				if !configured {
					continue
				}
				success := 0
				if measured {
					success = 1
				}
				fmt.Fprintf(&b, "eregion_workload_backlog_collection_success{workload=%q} %d\n", name, success)
				if measured {
					fmt.Fprintf(&b, "eregion_workload_backlog_messages{workload=%q} %d\n", name, messages)
				}
				if age > 0 || measured {
					fmt.Fprintf(&b, "eregion_workload_backlog_last_success_age_seconds{workload=%q} %g\n", name, age)
				}
			}
		}
		fmt.Fprintf(&b, "# TYPE eregion_workload_restarts_total counter\n")
		r.restartByWL.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_workload_restarts_total{workload=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})
		fmt.Fprintf(&b, "# TYPE eregion_workload_scale_events_total counter\n")
		r.scaleByWL.Range(func(k, v any) bool {
			parts := strings.SplitN(k.(string), "|", 2)
			if len(parts) != 2 {
				return true
			}
			fmt.Fprintf(&b, "eregion_workload_scale_events_total{workload=%q,direction=%q} %d\n", parts[0], parts[1], v.(*atomic.Uint64).Load())
			return true
		})

		fmt.Fprintf(&b, "# TYPE eregion_worker_guard_recycle_events_total counter\n")
		r.guardRecycle.Range(func(k, v any) bool {
			parts := strings.SplitN(k.(string), "|", 2)
			if len(parts) != 2 {
				return true
			}
			fmt.Fprintf(&b, "eregion_worker_guard_recycle_events_total{workload=%q,reason=%q} %d\n", parts[0], parts[1], v.(*atomic.Uint64).Load())
			return true
		})
		fmt.Fprintf(&b, "# TYPE eregion_worker_guard_restart_suppressed_total counter\n")
		r.guardSuppressed.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_worker_guard_restart_suppressed_total{workload=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})
		fmt.Fprintf(&b, "# TYPE eregion_worker_guard_half_open_attempts_total counter\n")
		r.guardHalfOpen.Range(func(k, v any) bool {
			parts := strings.SplitN(k.(string), "|", 2)
			if len(parts) != 2 {
				return true
			}
			fmt.Fprintf(&b, "eregion_worker_guard_half_open_attempts_total{workload=%q,result=%q} %d\n", parts[0], parts[1], v.(*atomic.Uint64).Load())
			return true
		})
		fmt.Fprintf(&b, "# TYPE eregion_worker_guard_breaker_state gauge\n")
		if r.pools != nil {
			for _, p := range r.pools.List() {
				current := p.GuardBreakerState()
				for _, state := range []string{"closed", "open", "half_open"} {
					val := 0
					if current == state {
						val = 1
					}
					fmt.Fprintf(&b, "eregion_worker_guard_breaker_state{workload=%q,state=%q} %d\n", p.Name(), state, val)
				}
			}
		}

		r.writeResourceGuard(&b)
		r.writeScaling(&b)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}

func (r *Registry) writeResourceGuard(b *strings.Builder) {
	obs, _ := r.guardObs.Load().(GuardObservation)
	mode := obs.Mode
	if mode == "" {
		mode = r.cfg.ResourceGuard.Mode
	}
	if mode == "" {
		mode = "observe"
	}
	fmt.Fprintf(b, "# TYPE eregion_resource_guard_mode gauge\n")
	for _, name := range []string{"off", "observe", "enforce"} {
		val := 0
		if mode == name {
			val = 1
		}
		fmt.Fprintf(b, "eregion_resource_guard_mode{mode=%q} %d\n", name, val)
	}
	state := obs.State
	if state == "" {
		state = "ok"
	}
	fmt.Fprintf(b, "# TYPE eregion_resource_guard_state gauge\n")
	for _, name := range []string{"ok", "warning", "critical", "degraded"} {
		val := 0
		if state == name {
			val = 1
		}
		fmt.Fprintf(b, "eregion_resource_guard_state{state=%q} %d\n", name, val)
	}

	sample := obs.Sample
	fmt.Fprintf(b, "# TYPE eregion_resource_sample_valid gauge\n")
	memValid := 0
	if sample.MemoryKnown() {
		memValid = 1
	}
	cpuValid := 0
	if resources.Known(sample.CPUUtilizationValid) {
		cpuValid = 1
	}
	throttleValid := 0
	if resources.Known(sample.CPUThrottlingValid) {
		throttleValid = 1
	}
	fmt.Fprintf(b, "eregion_resource_sample_valid{metric=%q} %d\n", "memory", memValid)
	fmt.Fprintf(b, "eregion_resource_sample_valid{metric=%q} %d\n", "cpu_usage", cpuValid)
	fmt.Fprintf(b, "eregion_resource_sample_valid{metric=%q} %d\n", "cpu_throttling", throttleValid)
	if obs.AgeKnown {
		fmt.Fprintf(b, "# TYPE eregion_resource_sample_age_seconds gauge\n")
		fmt.Fprintf(b, "eregion_resource_sample_age_seconds %g\n", obs.AgeSeconds)
	}
	if resources.Known(sample.MemoryCurrentValid) {
		fmt.Fprintf(b, "# TYPE eregion_resource_memory_current_bytes gauge\n")
		fmt.Fprintf(b, "eregion_resource_memory_current_bytes %d\n", sample.MemoryCurrentBytes)
	}
	if obs.HeadroomKnown {
		fmt.Fprintf(b, "# TYPE eregion_resource_memory_headroom_bytes gauge\n")
		fmt.Fprintf(b, "eregion_resource_memory_headroom_bytes %d\n", obs.Headroom)
	}
	if resources.Known(sample.CPUUtilizationValid) {
		fmt.Fprintf(b, "# TYPE eregion_resource_cpu_utilization_ratio gauge\n")
		fmt.Fprintf(b, "eregion_resource_cpu_utilization_ratio %g\n", sample.CPUUtilization)
	}
	if resources.Known(sample.CPUThrottlingValid) {
		fmt.Fprintf(b, "# TYPE eregion_resource_cpu_throttling_ratio gauge\n")
		fmt.Fprintf(b, "eregion_resource_cpu_throttling_ratio %g\n", sample.CPUThrottledRatio)
	}
	horizon := obs.Horizon
	if horizon == "" {
		horizon = "60s"
	}
	pred := 0
	if sample.PredictionValid {
		pred = 1
	}
	fmt.Fprintf(b, "# TYPE eregion_resource_prediction_valid gauge\n")
	fmt.Fprintf(b, "eregion_resource_prediction_valid{horizon=%q} %d\n", horizon, pred)
	if sample.PredictionValid {
		fmt.Fprintf(b, "# TYPE eregion_resource_memory_prediction_bytes gauge\n")
		fmt.Fprintf(b, "eregion_resource_memory_prediction_bytes %d\n", sample.PredictionBytes)
	}

	if len(obs.Workloads) == 0 {
		r.writeAdmissionDenied(b)
		return
	}
	fmt.Fprintf(b, "# TYPE eregion_scaling_requested_workers gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_policy_target_workers gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_admitted_workers gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_limited_workers gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_would_limit_workers gauge\n")
	fmt.Fprintf(b, "# TYPE eregion_scaling_capacity_deficit_workers gauge\n")
	for _, wl := range obs.Workloads {
		fmt.Fprintf(b, "eregion_scaling_requested_workers{workload=%q} %d\n", wl.Name, wl.Requested)
		fmt.Fprintf(b, "eregion_scaling_policy_target_workers{workload=%q} %d\n", wl.Name, wl.Policy)
		fmt.Fprintf(b, "eregion_scaling_admitted_workers{workload=%q} %d\n", wl.Name, wl.Admitted)
		reason := wl.Reason
		if reason == "" {
			reason = "none"
		}
		fmt.Fprintf(b, "eregion_scaling_limited_workers{workload=%q,reason=%q} %d\n", wl.Name, reason, wl.Limited)
		fmt.Fprintf(b, "eregion_scaling_would_limit_workers{workload=%q,reason=%q} %d\n", wl.Name, reason, wl.WouldLimit)
		fmt.Fprintf(b, "eregion_scaling_capacity_deficit_workers{workload=%q} %d\n", wl.Name, wl.Deficit)
	}
	r.writeAdmissionDenied(b)
}

func (r *Registry) writeAdmissionDenied(b *strings.Builder) {
	fmt.Fprintf(b, "# TYPE eregion_scaling_admission_denied_total counter\n")
	r.admissionDenied.Range(func(k, v any) bool {
		parts := strings.SplitN(k.(string), "|", 2)
		if len(parts) != 2 {
			return true
		}
		fmt.Fprintf(b, "eregion_scaling_admission_denied_total{workload=%q,reason=%q} %d\n", parts[0], parts[1], v.(*atomic.Uint64).Load())
		return true
	})
}

// HealthHandler serves JSON health for the HTTP pool.
func HealthHandler(pool *worker.Pool, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snap := pool.Snapshot()
		status := "healthy"
		healthy := snap.Idle + snap.Busy + snap.Draining
		if healthy < cfg.Workers.MinReady {
			status = "unhealthy"
		} else if snap.Failed > 0 || healthy < snap.Desired {
			status = "degraded"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"workers": map[string]int{
				"desired":  snap.Desired,
				"running":  snap.Running,
				"idle":     snap.Idle,
				"busy":     snap.Busy,
				"starting": snap.Starting,
				"failed":   snap.Failed,
			},
			"queue": map[string]int{
				"waiting":  snap.Waiting,
				"capacity": snap.Capacity,
			},
		})
	}
}

// HealthHandlerMulti serves aggregate health across all pools.
func HealthHandlerMulti(pools *worker.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		workloads := map[string]any{}
		status := "healthy"
		for _, p := range pools.List() {
			snap := p.Snapshot()
			workloads[p.Name()] = map[string]int{
				"desired":  snap.Desired,
				"running":  snap.Running,
				"idle":     snap.Idle,
				"busy":     snap.Busy,
				"starting": snap.Starting,
				"failed":   snap.Failed,
			}
			if snap.Failed > 0 {
				status = "degraded"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    status,
			"workloads": workloads,
		})
	}
}

// ReadyHandler returns 200 when enough workers are healthy and the server is not shutting down.
func ReadyHandler(pool *worker.Pool, cfg config.Config, isShuttingDown func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if isShuttingDown != nil && isShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		if pool.IsStopping() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		if pool.HealthyCount() >= cfg.Workers.MinReady {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}
}

// ReadyHandlerMulti is ready when no pool is stopping (consumer-only deployments).
func ReadyHandlerMulti(pools *worker.Manager, isShuttingDown func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if isShuttingDown != nil && isShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		for _, p := range pools.List() {
			if p.IsStopping() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"not_ready"}`))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
}

// LiveHandler always returns 200 while the Go process serves traffic.
func LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	}
}
