package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestLoadWorkloadsSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eregion.yaml")
	content := `
version: "1"
workload_templates:
  io-consumer:
    mode: consumer
    workers:
      min: 1
      max: 8
    resources:
      class: io
      memory_mb: 128
    scaling:
      strategy: backlog
      scale_up_cooldown: 1s
      scale_down_idle_for: 30s
workloads:
  telemetry:
    template: io-consumer
    command:
      - php
      - vendor/bin/job-worker
      - --kernel=App\TelemetryKernel
    queue:
      transport: mqtt
      name: devices/+/temperature
    workers:
      max: 6
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.HasExplicitWorkloads {
		t.Fatal("expected explicit workloads")
	}
	resolved, err := cfg.ResolveWorkloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 {
		t.Fatalf("len=%d", len(resolved))
	}
	if resolved[0].Name != "telemetry" || resolved[0].Mode != workload.ModeConsumer {
		t.Fatalf("%+v", resolved[0])
	}
	if resolved[0].Workers.Max != 6 || resolved[0].Workers.Min != 1 {
		t.Fatalf("workers %+v", resolved[0].Workers)
	}
}

func TestLegacyResolvesHTTPWorkload(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.Count = 3
	resolved, err := cfg.ResolveWorkloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Name != "http" {
		t.Fatalf("%+v", resolved)
	}
	if resolved[0].Mode != workload.ModeHTTP {
		t.Fatalf("mode=%s", resolved[0].Mode)
	}
}
