package telemetry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

// Registry holds Prometheus-style counters and gauges.
type Registry struct {
	cfg  config.Config
	pool *worker.Pool

	requestsTotal atomic.Uint64
	inFlight      atomic.Int64
	errorsTotal   sync.Map // kind -> *atomic.Uint64
	queueRejects  atomic.Uint64
	durationCount atomic.Uint64
	durationSum   atomic.Uint64 // milliseconds
	recycleTotal  sync.Map
	restartTotal  atomic.Uint64
}

// NewRegistry creates metrics backed by the pool snapshot.
func NewRegistry(cfg config.Config, pool *worker.Pool) *Registry {
	return &Registry{cfg: cfg, pool: pool}
}

func (r *Registry) IncRequests()    { r.requestsTotal.Add(1) }
func (r *Registry) IncInFlight()    { r.inFlight.Add(1) }
func (r *Registry) DecInFlight()    { r.inFlight.Add(-1) }
func (r *Registry) IncQueueReject() { r.queueRejects.Add(1) }
func (r *Registry) ObserveDuration(sec float64) {
	r.durationCount.Add(1)
	r.durationSum.Add(uint64(sec * 1000))
}
func (r *Registry) IncError(kind string) {
	v, _ := r.errorsTotal.LoadOrStore(kind, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}
func (r *Registry) IncRecycle(reason string) {
	v, _ := r.recycleTotal.LoadOrStore(reason, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}
func (r *Registry) IncRestart() { r.restartTotal.Add(1) }

// Handler serves /metrics in Prometheus text format.
func (r *Registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snap := r.pool.Snapshot()
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

		r.errorsTotal.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_http_errors_total{kind=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})

		writeGauge("eregion_workers_desired", float64(snap.Desired))
		writeGauge("eregion_workers_running", float64(snap.Running))
		writeGauge("eregion_workers_idle", float64(snap.Idle))
		writeGauge("eregion_workers_busy", float64(snap.Busy))
		writeGauge("eregion_workers_starting", float64(snap.Starting))
		writeGauge("eregion_workers_failed", float64(snap.Failed))
		writeGauge("eregion_queue_waiting", float64(snap.Waiting))
		writeGauge("eregion_queue_capacity", float64(snap.Capacity))
		writeCounter("eregion_worker_restarts_total", r.restartTotal.Load())
		r.recycleTotal.Range(func(k, v any) bool {
			fmt.Fprintf(&b, "eregion_worker_recycles_total{reason=%q} %d\n", k, v.(*atomic.Uint64).Load())
			return true
		})

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}

// HealthHandler serves JSON health.
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

// ReadyHandler returns 200 when enough workers are healthy.
func ReadyHandler(pool *worker.Pool, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if pool.HealthyCount() >= cfg.Workers.MinReady {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ready"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}
}

// LiveHandler always returns 200 while the Go process serves traffic.
func LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	}
}
