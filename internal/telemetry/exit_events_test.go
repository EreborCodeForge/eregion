package telemetry_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestExitReasonIsClosedEnum(t *testing.T) {
	reg := newExitRegistry(t)
	reg.IncExit("mail", "planned")
	reg.IncExit("mail", "timeout")
	reg.IncExit("mail", "protocol handshake failed token=secret")

	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, `eregion_workload_worker_exit_events_total{workload="mail",reason="planned"} 1`) {
		t.Fatalf("planned exit missing\n%s", body)
	}
	if !strings.Contains(body, `eregion_workload_worker_exit_events_total{workload="mail",reason="other"} 1`) {
		t.Fatalf("unknown reason was not collapsed to other\n%s", body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "handshake") {
		t.Fatalf("exit label leaked the raw message\n%s", body)
	}
}

func TestExitReasonRace(t *testing.T) {
	reg := newExitRegistry(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				reg.IncExit("mail", "crash")
				rec := httptest.NewRecorder()
				reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			}
		}()
	}
	wg.Wait()
	body := scrapeMetrics(t, reg)
	if !strings.Contains(body, `reason="crash"`) {
		t.Fatalf("crash counter missing\n%s", body)
	}
}

func TestGatewayErrorsAndHealthAlias(t *testing.T) {
	var tally telemetry.StatusTally
	tally.Observe(200)
	tally.Observe(503)
	tally.Observe(502)
	if tally.GatewayErrors() != 2 {
		t.Fatalf("gateway errors=%d", tally.GatewayErrors())
	}
	if telemetry.FrameworkRouteReport("/api/health") || telemetry.FrameworkRouteReport("/_eregion/health") {
		t.Fatal("health aliases were accepted as framework evidence")
	}
	if !telemetry.FrameworkRouteReport("/api/ping") {
		t.Fatal("/api/ping was rejected")
	}
	manifest := telemetry.NewEnvironmentManifest("eregion", "0.4.2")
	if manifest.Go == "" || manifest.Version != "0.4.2" || manifest.Binary != "eregion" {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func newExitRegistry(t *testing.T) *telemetry.Registry {
	t.Helper()
	cfg := config.Default()
	return telemetry.NewRegistry(cfg, worker.NewManager(cfg, slog.Default(), "test"), resources.RuntimeResources{}, sizing.WorkerSizing{})
}
