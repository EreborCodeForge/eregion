package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

func TestResolveOperationPath(t *testing.T) {
	cases := []struct {
		prefix, path, want string
	}{
		{"/_eregion", "/metrics", "/_eregion/metrics"},
		{"/_eregion", "metrics", "/_eregion/metrics"},
		{"/_eregion", "/_eregion/metrics", "/_eregion/metrics"},
		{"/_eregion/", "/health", "/_eregion/health"},
		{"/_ops", "/_ops/ready", "/_ops/ready"},
	}
	for _, tc := range cases {
		got, err := config.ResolveOperationPath(tc.prefix, tc.path)
		if err != nil {
			t.Fatalf("%s + %s: %v", tc.prefix, tc.path, err)
		}
		if got != tc.want {
			t.Fatalf("%s + %s = %q, want %q", tc.prefix, tc.path, got, tc.want)
		}
	}
}

func TestDefaultRelativeEndpointsResolve(t *testing.T) {
	cfg := config.Default()
	if cfg.Metrics.Path != "/_eregion/metrics" {
		t.Fatalf("metrics path = %q", cfg.Metrics.Path)
	}
	if cfg.Health.Path != "/_eregion/health" {
		t.Fatalf("health path = %q", cfg.Health.Path)
	}
}

func TestEndpointCollisionRejected(t *testing.T) {
	cfg := config.Default()
	cfg.Health.Path = "/metrics"
	cfg.Metrics.Path = "/metrics"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected collision error")
	}
}

func TestApplyCLIOverridesRecomputesDerivedQueue(t *testing.T) {
	cfg := config.Default()
	origWorkers := cfg.Workers.Count
	cfg.ApplyCLIOverrides("", 0, origWorkers*2)
	if cfg.Workers.Count != origWorkers*2 {
		t.Fatalf("workers = %d", cfg.Workers.Count)
	}
	if cfg.Queue.Capacity != config.DerivedQueueCapacity(origWorkers*2) {
		t.Fatalf("queue = %d", cfg.Queue.Capacity)
	}
}

func TestApplyCLIOverridesKeepsExplicitQueue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eregion.yaml")
	content := `
version: "1"
workers:
  count: 2
queue:
  capacity: 3
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QueueCapacityDerived {
		t.Fatal("expected explicit queue")
	}
	cfg.ApplyCLIOverrides("", 0, 8)
	if cfg.Queue.Capacity != 3 {
		t.Fatalf("queue capacity changed to %d", cfg.Queue.Capacity)
	}
	if cfg.Workers.Count != 8 {
		t.Fatalf("workers = %d", cfg.Workers.Count)
	}
}
