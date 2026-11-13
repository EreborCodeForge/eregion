package telemetry_test

import (
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestWorkloadMetricLabels(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	mgr := worker.NewManager(cfg, slog.Default(), "test")
	_, err := mgr.Ensure(workload.ResolvedWorkloadSpec{
		Name: "telemetry", Mode: workload.ModeConsumer,
		Command: []string{"true"},
		Workers: workload.WorkerPolicy{Min: 0, Max: 1},
		Scaling: workload.DefaultScaling(), Resources: workload.DefaultResources(),
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := telemetry.NewRegistry(cfg, mgr, resources.RuntimeResources{}, sizing.WorkerSizing{})
	reg.IncScale("telemetry", "up")
	reg.ForWorkload("telemetry").IncRestart()

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`eregion_workload_desired_workers{workload="telemetry"}`,
		`eregion_workload_scale_events_total{workload="telemetry",direction="up"}`,
		`eregion_workload_restarts_total{workload="telemetry"}`,
		`eregion_worker_guard_breaker_state{workload="telemetry",state="closed"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q\n%s", want, body)
		}
	}
}

func TestSubMillisecondDurationsArePreserved(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.MaxRequests = 10000
	reg := telemetry.NewRegistry(cfg, worker.NewManager(cfg, slog.Default(), "test"), resources.RuntimeResources{}, sizing.WorkerSizing{})

	reg.ObserveDuration(0.0004)
	reg.ObserveDuration(0.0004)
	reg.ObserveDuration(0.0004)
	reg.ObserveQueueWait(0.0004)
	reg.ObserveRoundtrip(0.0004)
	reg.ObserveReplace("max_requests", 0.0004)

	body := scrapeMetrics(t, reg)
	if got := metricValue(t, body, "eregion_http_request_duration_seconds_count"); got != 3 {
		t.Fatalf("duration count=%v", got)
	}
	sum := metricValue(t, body, "eregion_http_request_duration_seconds_sum")
	if math.Abs(sum-0.0012) > 1e-6 {
		t.Fatalf("duration sum=%v, want ~0.0012 (sub-millisecond samples must not become 0)", sum)
	}
	queue := metricValue(t, body, "eregion_queue_wait_duration_seconds_sum")
	if math.Abs(queue-0.0004) > 1e-6 {
		t.Fatalf("queue sum=%v", queue)
	}
	round := metricValue(t, body, "eregion_worker_roundtrip_duration_seconds_sum")
	if math.Abs(round-0.0004) > 1e-6 {
		t.Fatalf("roundtrip sum=%v", round)
	}
	replaceCount := metricValue(t, body, `eregion_worker_replace_duration_seconds_count{reason="max_requests"}`)
	replaceSum := metricValue(t, body, `eregion_worker_replace_duration_seconds_sum{reason="max_requests"}`)
	if replaceCount != 1 || math.Abs(replaceSum-0.0004) > 1e-6 {
		t.Fatalf("replace count=%v sum=%v", replaceCount, replaceSum)
	}
	if got := metricValue(t, body, "eregion_workers_max_requests"); got != 10000 {
		t.Fatalf("max_requests=%v", got)
	}

	reg.ObserveDuration(-1)
	body = scrapeMetrics(t, reg)
	if got := metricValue(t, body, "eregion_http_request_duration_seconds_count"); got != 4 {
		t.Fatalf("count after negative sample=%v", got)
	}
	after := metricValue(t, body, "eregion_http_request_duration_seconds_sum")
	if math.Abs(after-sum) > 1e-6 {
		t.Fatalf("negative duration changed sum from %v to %v", sum, after)
	}
}

func scrapeMetrics(t *testing.T, reg *telemetry.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") {
			fields := strings.Fields(line)
			v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return v
		}
	}
	t.Fatalf("metrics missing %q\n%s", name, body)
	return 0
}
