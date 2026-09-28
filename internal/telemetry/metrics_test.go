package telemetry_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q\n%s", want, body)
		}
	}
}
