package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

func TestResourceGuardDefaultsToObserve(t *testing.T) {
	cfg := config.Default()
	if cfg.ResourceGuard.Mode != config.ResourceGuardObserve {
		t.Fatalf("mode=%s", cfg.ResourceGuard.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestECO09T35InvalidGuardPaths(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		path string
	}{
		{
			name: "reserve percent",
			yaml: "resource_guard:\n  memory:\n    reserve_percent: -1\n",
			path: "resource_guard.memory.reserve_percent",
		},
		{
			name: "hard limit",
			yaml: "resource_guard:\n  memory:\n    hard_limit_mb: -1\n",
			path: "resource_guard.memory.hard_limit_mb",
		},
		{
			name: "interval",
			yaml: "resource_guard:\n  sample_interval: 1ms\n",
			path: "resource_guard.sample_interval",
		},
		{
			name: "ratios",
			yaml: "resource_guard:\n  memory:\n    warning_ratio: 0.95\n    critical_ratio: 0.90\n    hard_ratio: 0.99\n",
			path: "resource_guard.memory.critical_ratio",
		},
		{
			name: "mode",
			yaml: "resource_guard:\n  mode: aggressive\n",
			path: "resource_guard.mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "eregion.yaml")
			if err := os.WriteFile(path, []byte("version: \"1\"\n"+tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("err=%v, want path %s", err, tc.path)
			}
		})
	}
}
