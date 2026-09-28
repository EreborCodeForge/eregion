package telemetry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

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

	requestsTotal  atomic.Uint64
	inFlight       atomic.Int64
	errorsTotal    sync.Map // kind -> *atomic.Uint64
	queueRejects   atomic.Uint64
	durationCount  atomic.Uint64
	durationSum    atomic.Uint64 // milliseconds
	queueWaitCount atomic.Uint64
	queueWaitSum   atomic.Uint64 // milliseconds
	recycleTotal   sync.Map
	restartTotal   atomic.Uint64
	restartByWL    sync.Map // workload -> *atomic.Uint64
	scaleByWL      sync.Map // "name|direction" -> *atomic.Uint64
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
	}
}

func (r *Registry) IncRequests()    { r.requestsTotal.Add(1) }
func (r *Registry) IncInFlight()    { r.inFlight.Add(1) }
func (r *Registry) DecInFlight()    { r.inFlight.Add(-1) }
func (r *Registry) IncQueueReject() { r.queueRejects.Add(1) }
func (r *Registry) ObserveDuration(sec float64) {
	r.durationCount.Add(1)
	r.durationSum.Add(uint64(sec * 1000))
}
func (r *Registry) ObserveQueueWait(sec float64) {
	r.queueWaitCount.Add(1)
	r.queueWaitSum.Add(uint64(sec * 1000))
}
func (r *Registry) IncError(kind string) {
	v, _ := r.errorsTotal.LoadOrStore(kind, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}
func (r *Registry) IncRecycle(reason string) {
	v, _ := r.recycleTotal.LoadOrStore(reason, &atomic.Uint64{})
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

// ForWorkload returns a PoolMetrics scoped to a workload name.
func (r *Registry) ForWorkload(name string) worker.PoolMetrics {
	return workloadMetrics{reg: r, name: name}
}

type workloadMetrics struct {
	reg  *Registry
	name string
}

func (m workloadMetrics) IncRecycle(reason string) { m.reg.IncRecycle(reason) }
func (m workloadMetrics) IncRestart() {
	m.reg.restartTotal.Add(1)
	m.reg.incRestartWL(m.name)
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
		fmt.Fprintf(&b, "# TYPE eregion_http_request_duration_seconds summary\n")
		fmt.Fprintf(&b, "eregion_http_request_duration_seconds_count %d\n", r.durationCount.Load())
		fmt.Fprintf(&b, "eregion_http_request_duration_seconds_sum %g\n", float64(r.durationSum.Load())/1000)
		fmt.Fprintf(&b, "# TYPE eregion_queue_wait_duration_seconds summary\n")
		fmt.Fprintf(&b, "eregion_queue_wait_duration_seconds_count %d\n", r.queueWaitCount.Load())
		fmt.Fprintf(&b, "eregion_queue_wait_duration_seconds_sum %g\n", float64(r.queueWaitSum.Load())/1000)

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

		writeGauge("eregion_workers_per_cpu", r.workersPerCPU)
		writeGauge("eregion_workers_recommended", r.workersRecommended)
		writeGauge("eregion_workers_recommended_min", r.workersRecommendedMin)
		writeGauge("eregion_workers_recommended_max", r.workersRecommendedMax)
		writeCounter("eregion_worker_restarts_total", r.restartTotal.Load())
		r.recycleTotal.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_worker_recycles_total{reason=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})

		// Per-workload metrics.
		fmt.Fprintf(&b, "# TYPE eregion_workload_desired_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_running_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_busy_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_draining_workers gauge\n")
		fmt.Fprintf(&b, "# TYPE eregion_workload_failed_workers gauge\n")
		for _, p := range r.pools.List() {
			snap := p.Snapshot()
			name := p.Name()
			fmt.Fprintf(&b, "eregion_workload_desired_workers{workload=%q} %d\n", name, snap.Desired)
			fmt.Fprintf(&b, "eregion_workload_running_workers{workload=%q} %d\n", name, snap.Running)
			fmt.Fprintf(&b, "eregion_workload_busy_workers{workload=%q} %d\n", name, snap.Busy)
			fmt.Fprintf(&b, "eregion_workload_draining_workers{workload=%q} %d\n", name, snap.Draining)
			fmt.Fprintf(&b, "eregion_workload_failed_workers{workload=%q} %d\n", name, snap.Failed)
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

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
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
