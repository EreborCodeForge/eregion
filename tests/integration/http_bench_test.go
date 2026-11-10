package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func TestMithrilHTTPBench(t *testing.T) {
	if os.Getenv("EREGION_HTTP_BENCH") == "" {
		t.Skip("N/A unless EREGION_HTTP_BENCH=1; this is a manual reproducible bench, not a unit gate")
	}
	root := mithrilRoot(t)
	worker := filepath.Join(root, "bin", "eregion-worker")
	app := filepath.Join(root, "bench", "http", "App.php")
	autoload := filepath.Join(root, "vendor", "autoload.php")
	for _, path := range []string{worker, app, autoload} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}

	modes := []struct {
		name   string
		config string
		delay  string
	}{
		{name: "off", config: "", delay: "0"},
		{name: "observe", config: observeConfig(), delay: "0"},
		{name: "representative_off", config: "", delay: "200"},
		{name: "representative", config: observeConfig(), delay: "200"},
	}
	for _, mode := range modes {
		summary := runHTTPMode(t, worker, app, autoload, mode.config, mode.delay)
		t.Logf("%s rps_p50=%.1f latency_p95=%s latency_p99=%s rounds=%v", mode.name, summary.rpsP50, summary.p95, summary.p99, summary.rounds)
	}
}

func observeConfig() string {
	return "<?php\nreturn [\n  'mode' => 'observe',\n  'sample_every_n_requests' => 10,\n  'static_watchpoints' => [],\n];\n"
}

type httpSummary struct {
	rpsP50 float64
	p95    time.Duration
	p99    time.Duration
	rounds []float64
}

func runHTTPMode(t *testing.T, worker, app, autoload, guardPHP, delay string) httpSummary {
	t.Helper()
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "bootstrap.php")
	body := fmt.Sprintf("<?php\nrequire %q;\nrequire %q;\n", autoload, app)
	if err := os.WriteFile(bootstrap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if guardPHP != "" {
		if err := os.Mkdir(filepath.Join(dir, "config"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config", "worker_guard.php"), []byte(guardPHP), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(dir, "manifest.json")
	raw := fmt.Sprintf("{\n  \"application\": \"Bench\\\\Http\\\\App\",\n  \"autoload\": %q,\n  \"workingDirectory\": %q\n}\n", filepath.ToSlash(bootstrap), filepath.ToSlash(dir))
	if err := os.WriteFile(manifest, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(t, worker)
	cfg.Manifest = manifest
	cfg.PHP.WorkingDirectory = dir
	cfg.PHP.Environment = map[string]string{"BENCH_DB_MICROS": delay}
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.MaxRequests = 0
	base := startServer(t, cfg)

	const requests = 300
	const rounds = 3
	// Warm the worker before measuring.
	if got := getBody(t, base+"/item"); got.status != 200 {
		t.Fatalf("warmup status %d body %s", got.status, got.body)
	}
	var rps []float64
	var p95 []time.Duration
	var p99 []time.Duration
	for round := 0; round < rounds; round++ {
		latencies := make([]time.Duration, 0, requests)
		start := time.Now()
		for i := 0; i < requests; i++ {
			one := time.Now()
			got := getBody(t, base+"/item")
			latencies = append(latencies, time.Since(one))
			if got.status != 200 {
				t.Fatalf("status %d body %s", got.status, got.body)
			}
		}
		elapsed := time.Since(start)
		rps = append(rps, float64(requests)/elapsed.Seconds())
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p95 = append(p95, latencies[percentile(latencies, 0.95)])
		p99 = append(p99, latencies[percentile(latencies, 0.99)])
	}
	logged := append([]float64(nil), rps...)
	sort.Float64s(rps)
	sort.Slice(p95, func(i, j int) bool { return p95[i] < p95[j] })
	sort.Slice(p99, func(i, j int) bool { return p99[i] < p99[j] })
	return httpSummary{rpsP50: rps[len(rps)/2], p95: p95[len(p95)/2], p99: p99[len(p99)/2], rounds: logged}
}

func percentile(samples []time.Duration, p float64) int {
	if len(samples) == 0 {
		return 0
	}
	index := int(float64(len(samples)-1) * p)
	if index < 0 {
		return 0
	}
	if index >= len(samples) {
		return len(samples) - 1
	}
	return index
}

func mithrilRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "mithrilphp"))
}
