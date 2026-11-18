package telemetry_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestECO09T32GuardMetricsUseClosedLabels(t *testing.T) {
	reg := newGuardReg(t)
	reg.SetGuardObservation(telemetry.GuardObservation{
		Mode:  "enforce",
		State: "critical",
		Sample: resources.Sample{
			At:                  time.Unix(10, 0),
			Source:              resources.SourceCgroupV2,
			MemoryLimitBytes:    1024,
			MemoryLimitValid:    resources.ValidityKnown,
			MemoryCurrentBytes:  900,
			MemoryCurrentValid:  resources.ValidityKnown,
			CPUUtilization:      0.5,
			CPUUtilizationValid: resources.ValidityKnown,
			Scope:               "cgroup",
		},
		Headroom:      100,
		HeadroomKnown: true,
		AgeKnown:      true,
		AgeSeconds:    1.5,
		Horizon:       "60s",
		Workloads: []telemetry.GuardWorkload{{
			Name: "http", Requested: 8, Policy: 4, Admitted: 2,
			Limited: 2, Reason: "memory_headroom", Deficit: 0,
		}},
	})
	reg.IncAdmissionDenied("http", "memory_headroom")
	body := scrapeMetrics(t, reg)
	for _, needle := range []string{
		`eregion_resource_guard_mode{mode="enforce"} 1`,
		`eregion_resource_guard_state{state="critical"} 1`,
		`eregion_resource_sample_valid{metric="memory"} 1`,
		`eregion_resource_sample_valid{metric="cpu_throttling"} 0`,
		`eregion_resource_memory_current_bytes 900`,
		`eregion_scaling_admitted_workers{workload="http"} 2`,
		`eregion_scaling_limited_workers{workload="http",reason="memory_headroom"} 2`,
		`eregion_scaling_admission_denied_total{workload="http",reason="memory_headroom"} 1`,
		`eregion_resource_prediction_valid{horizon="60s"} 0`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s\n%s", needle, body)
		}
	}
	if strings.Contains(body, "eregion_resource_memory_prediction_bytes") {
		t.Fatal("invalid prediction was published as bytes")
	}
	if strings.Contains(body, "pid=") || strings.Contains(body, "url=") {
		t.Fatal(body)
	}
}

func TestECO09T33ScrapeDoesNotInventUnknownMemory(t *testing.T) {
	reg := newGuardReg(t)
	reg.SetGuardObservation(telemetry.GuardObservation{
		Mode:  "observe",
		State: "degraded",
		Sample: resources.Sample{
			At:                 time.Unix(3, 0),
			Source:             resources.SourceUnknown,
			MemoryLimitValid:   resources.ValidityUnknown,
			MemoryCurrentValid: resources.ValidityError,
		},
		AgeKnown:   true,
		AgeSeconds: 4,
		Horizon:    "60s",
	})
	body := scrapeMetrics(t, reg)
	again := scrapeMetrics(t, reg)
	if body != again {
		t.Fatal("scrape changed the cached guard snapshot")
	}
	if strings.Contains(body, "eregion_resource_memory_current_bytes") {
		t.Fatal("unknown memory was published as a gauge")
	}
	if !strings.Contains(body, `eregion_resource_sample_valid{metric="memory"} 0`) {
		t.Fatalf("%s", body)
	}
}

func newGuardReg(t *testing.T) *telemetry.Registry {
	t.Helper()
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	mgr := worker.NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	return telemetry.NewRegistry(cfg, mgr, resources.RuntimeResources{LogicalCPUs: 1, AvailableCPUs: 1, GOMAXPROCS: 1}, sizing.WorkerSizing{})
}
