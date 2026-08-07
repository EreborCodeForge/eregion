package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

// Metrics hooks for the dispatcher.
type Metrics interface {
	IncRequests()
	DecInFlight()
	IncInFlight()
	ObserveDuration(seconds float64)
	ObserveQueueWait(seconds float64)
	IncError(kind string)
	IncQueueReject()
}

// Dispatcher admits HTTP requests and routes them to workers.
type Dispatcher struct {
	cfg     config.Config
	pool    *worker.Pool
	logger  *slog.Logger
	metrics Metrics
	reqSeq  atomic.Uint64

	queueMu sync.Mutex
	waiting int
}

// New creates a dispatcher.
func New(cfg config.Config, pool *worker.Pool, logger *slog.Logger, metrics Metrics) *Dispatcher {
	return &Dispatcher{cfg: cfg, pool: pool, logger: logger, metrics: metrics}
}

// Waiting returns current queue waiters (requests waiting for a worker, not executing).
func (d *Dispatcher) Waiting() int {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	return d.waiting
}

func (d *Dispatcher) tryEnterQueue() bool {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	// capacity 0 means no queue: never admit waiters.
	if d.cfg.Queue.Capacity <= 0 {
		return false
	}
	if d.waiting >= d.cfg.Queue.Capacity {
		return false
	}
	d.waiting++
	return true
}

func (d *Dispatcher) leaveQueue() {
	d.queueMu.Lock()
	if d.waiting > 0 {
		d.waiting--
	}
	d.queueMu.Unlock()
}

// ServeHTTP handles application requests.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Operations endpoints are registered on the mux; anything else under the
	// operations prefix (including disabled endpoints) must not hit PHP workers.
	prefix := d.cfg.Operations.Prefix
	if prefix != "" && (r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")) {
		http.NotFound(w, r)
		return
	}

	if d.metrics != nil {
		d.metrics.IncRequests()
		d.metrics.IncInFlight()
		defer d.metrics.DecInFlight()
	}

	body, err := readBodyLimited(r, d.cfg.Server.MaxBodyBytes)
	if err != nil {
		if d.metrics != nil {
			d.metrics.IncError("body")
		}
		http.Error(w, `{"error":"request_entity_too_large","message":"Request body exceeds configured limit."}`, http.StatusRequestEntityTooLarge)
		d.accessLog("method", r.Method, "path", r.URL.Path, "status", http.StatusRequestEntityTooLarge,
			"duration_ms", time.Since(start).Milliseconds(), "error", "request_entity_too_large")
		return
	}

	var wrk *worker.Worker
	var queueWait time.Duration

	if wtry, ok := d.pool.TryAcquire(); ok {
		wrk = wtry
	} else {
		if !d.tryEnterQueue() {
			if d.metrics != nil {
				d.metrics.IncQueueReject()
				d.metrics.IncError("capacity")
			}
			d.writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":   "server_capacity_exceeded",
				"message": "No worker capacity is currently available.",
			}, RetryAfterSeconds(d.cfg.Queue.RetryAfter))
			d.accessLog("method", r.Method, "path", r.URL.Path, "status", http.StatusServiceUnavailable,
				"duration_ms", time.Since(start).Milliseconds(), "error", "queue_full")
			return
		}

		acquireCtx, cancel := context.WithTimeout(r.Context(), d.cfg.Workers.AcquireTimeout)
		queueWaitStart := time.Now()
		acquired, err := d.pool.Acquire(acquireCtx)
		queueWait = time.Since(queueWaitStart)
		cancel()
		d.leaveQueue()
		if d.metrics != nil && queueWait > 0 {
			d.metrics.ObserveQueueWait(queueWait.Seconds())
		}
		if err != nil {
			if d.metrics != nil {
				d.metrics.IncError("acquire")
			}
			d.writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error":   "server_capacity_exceeded",
				"message": "No worker capacity is currently available.",
			}, RetryAfterSeconds(d.cfg.Queue.RetryAfter))
			d.accessLog("method", r.Method, "path", r.URL.Path, "status", http.StatusServiceUnavailable,
				"duration_ms", time.Since(start).Milliseconds(), "queue_wait_ms", queueWait.Milliseconds(),
				"error", "acquire_timeout")
			return
		}
		wrk = acquired
	}

	reqID := fmt.Sprintf("req-%d", d.reqSeq.Add(1))
	env := protocol.RequestEnvelope{
		Type:          protocol.TypeRequest,
		Version:       protocol.ProtocolVersion,
		ID:            reqID,
		Method:        r.Method,
		URI:           r.URL.RequestURI(),
		Path:          r.URL.Path,
		Query:         r.URL.RawQuery,
		Protocol:      r.Proto,
		Headers:       cloneHeaders(r.Header),
		Body:          body,
		RemoteAddress: remoteAddr(r),
		Host:          r.Host,
		Scheme:        scheme(r),
		TimeoutMs:     uint32(d.cfg.Workers.RequestTimeout / time.Millisecond),
	}

	reqCtx, reqCancel := context.WithTimeout(r.Context(), d.cfg.Workers.RequestTimeout)
	defer reqCancel()

	resp, err := d.pool.Client().Send(reqCtx, wrk, env)
	if err != nil {
		d.pool.Discard(wrk, err)
		timedOut := errors.Is(err, context.DeadlineExceeded) || isTimeoutErr(err)
		status := http.StatusBadGateway
		errKind := "protocol"
		if timedOut {
			status = http.StatusGatewayTimeout
			errKind = "timeout"
		}
		if d.metrics != nil {
			d.metrics.IncError(errKind)
			d.metrics.ObserveDuration(time.Since(start).Seconds())
		}
		if timedOut {
			http.Error(w, `{"error":"gateway_timeout","message":"Worker request timed out."}`, http.StatusGatewayTimeout)
		} else {
			http.Error(w, `{"error":"bad_gateway","message":"Worker failed while processing the request."}`, http.StatusBadGateway)
		}
		attrs := []any{
			"request_id", reqID, "worker_id", wrk.ID, "generation", wrk.Generation,
			"method", r.Method, "path", r.URL.Path, "status", status,
			"duration_ms", time.Since(start).Milliseconds(), "queue_wait_ms", queueWait.Milliseconds(),
			"error", errKind,
		}
		attrs = append(attrs, d.maybeLogExtras(r, body, nil)...)
		d.accessLog(attrs...)
		return
	}

	d.pool.Release(wrk, &resp)

	for k, vals := range resp.Headers {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" && len(resp.Body) > 0 {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.WriteHeader(int(resp.Status))
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}

	if d.metrics != nil {
		d.metrics.ObserveDuration(time.Since(start).Seconds())
	}
	attrs := []any{
		"request_id", reqID,
		"worker_id", wrk.ID,
		"generation", wrk.Generation,
		"method", r.Method,
		"path", r.URL.Path,
		"status", resp.Status,
		"duration_ms", time.Since(start).Milliseconds(),
		"queue_wait_ms", queueWait.Milliseconds(),
	}
	attrs = append(attrs, d.maybeLogExtras(r, body, resp.Body)...)
	d.accessLog(attrs...)
}

// RetryAfterSeconds converts a duration to an HTTP Retry-After integer (ceil, min 1).
func RetryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		return 1
	}
	return s
}

func (d *Dispatcher) writeJSON(w http.ResponseWriter, status int, body map[string]string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func readBodyLimited(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return []byte{}, nil
	}
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("body too large")
	}
	return data, nil
}

func cloneHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		return v
	}
	return "http"
}

func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isTimeoutErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
