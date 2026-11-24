package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
)

func TestECO11T02AbsentZeroAndUnknownFields(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard:
  mode: observe
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 2, max: 4}
    scaling:
      strategy: latency
      latency:
        target_p95: 100ms
        queue_wait_target_p95: 10ms
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := cfg.ResolveWorkloads()
	if err != nil {
		t.Fatal(err)
	}
	if specs[0].Workers.Min != 2 || specs[0].Scaling.Strategy != "latency" {
		t.Fatalf("%+v", specs[0])
	}
	bad := writeCfg(t, `
version: "1"
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 2}
    scaling:
      strategy: latency
      not_a_field: 1
`)
	if _, err := config.Load(bad); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestECO11T17CPUMinZeroRejected(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: observe}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 0, max: 2}
    scaling:
      strategy: cpu_utilization
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "cpu_utilization requires workers.min >= 1") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveWorkloadsRejectsCPUUtilizationMinZero(t *testing.T) {
	min, max := 0, 2
	strategy := "cpu_utilization"
	cfg := config.Default()
	cfg.HasExplicitWorkloads = true
	cfg.Workloads = map[string]config.WorkloadConfig{
		"http": {
			Mode:    "http",
			Command: []string{"php", "worker.php"},
			Workers: &config.WorkloadWorkersConfig{Min: &min, Max: &max},
			Scaling: &config.WorkloadScalingConfig{Strategy: &strategy},
		},
	}
	_, err := cfg.ResolveWorkloads()
	if err == nil || !strings.Contains(err.Error(), "cpu_utilization requires workers.min >= 1") {
		t.Fatalf("err=%v", err)
	}
}

func TestECO11T22ThroughputTargetRequired(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: observe}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 2}
    scaling: {strategy: throughput}
`)
	cfg, err := config.Load(path)
	if err == nil {
		_, err = cfg.ResolveWorkloads()
	}
	if err == nil || !strings.Contains(err.Error(), "target_rps_per_worker") {
		t.Fatalf("err=%v", err)
	}
}

func TestECO11T31HybridConsumerWithoutSourceRejected(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: observe}
workloads:
  email:
    mode: consumer
    command: ["php", "worker.php"]
    workers: {min: 0, max: 2}
    scaling: {strategy: hybrid}
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "hybrid consumer requires backlog_source") {
		t.Fatalf("err=%v", err)
	}
}

func TestECO11T34OffRejectsNewStrategy(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: off}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 2}
    scaling: {strategy: scheduled, scheduled: {timezone: UTC, default_workers: 1}}
`)
	cfg, err := config.Load(path)
	if err == nil {
		_, err = cfg.ResolveWorkloads()
	}
	if err == nil || !strings.Contains(err.Error(), "observe") {
		t.Fatalf("err=%v", err)
	}
}

func TestECO11T36OverlapAndTimezoneRejected(t *testing.T) {
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: observe}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 4}
    scaling:
      strategy: scheduled
      scheduled:
        timezone: America/Sao_Paulo
        default_workers: 1
        windows:
          - {days: [mon], start: "09:00", end: "12:00", target_workers: 2}
`)
	cfg, err := config.Load(path)
	if err == nil {
		_, err = cfg.ResolveWorkloads()
	}
	if err == nil || !strings.Contains(err.Error(), "UTC") {
		t.Fatalf("err=%v", err)
	}
}

func TestECO11T44AdaptiveRequiresEnforceAndHTTP(t *testing.T) {
	if !signals.WorkerCPUSupported() {
		path := writeCfg(t, `
version: "1"
resource_guard: {mode: enforce}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 4}
    scaling: {strategy: adaptive}
`)
		cfg, err := config.Load(path)
		if err == nil {
			_, err = cfg.ResolveWorkloads()
		}
		if err == nil || !strings.Contains(err.Error(), "CPU") {
			t.Fatalf("unsupported platform err=%v", err)
		}
		return
	}
	path := writeCfg(t, `
version: "1"
resource_guard: {mode: observe}
workloads:
  http:
    mode: http
    command: ["php", "worker.php"]
    workers: {min: 1, max: 4}
    scaling: {strategy: adaptive}
`)
	cfg, err := config.Load(path)
	if err == nil {
		_, err = cfg.ResolveWorkloads()
	}
	if err == nil || !strings.Contains(err.Error(), "enforce") {
		t.Fatalf("err=%v", err)
	}
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eregion.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
