package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
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

func TestMithrilHTTPBenchLoad(t *testing.T) {
	if os.Getenv("EREGION_HTTP_BENCH") == "" {
		t.Skip("N/A unless EREGION_HTTP_BENCH=1; this is a manual reproducible bench, not a unit gate")
	}
	if runtime.GOOS != "linux" {
		t.Skip("representative concurrent bench is measured on Linux")
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
		summary := runHTTPModeLoad(t, worker, app, autoload, mode.config, mode.delay)
		t.Logf("%s rps_p50=%.1f latency_p50=%s latency_p95=%s latency_p99=%s php_rss_kb=%d php_cpu_pct=%.1f php_workers=%d rounds=%v",
			mode.name, summary.rpsP50, summary.p50, summary.p95, summary.p99, summary.phpRSSKB, summary.phpCPU, summary.phpN, summary.rounds)
	}
}

func observeConfig() string {
	return "<?php\nreturn [\n  'mode' => 'observe',\n  'sample_every_n_requests' => 10,\n  'static_watchpoints' => [],\n];\n"
}

type httpSummary struct {
	rpsP50   float64
	p50      time.Duration
	p95      time.Duration
	p99      time.Duration
	phpRSSKB uint64
	phpCPU   float64
	phpN     int
	rounds   []float64
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
	base, stop := startManaged(t, cfg)
	defer stop()

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

func runHTTPModeLoad(t *testing.T, worker, app, autoload, guardPHP, delay string) httpSummary {
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
	cfg.Workers.Count = 4
	cfg.Workers.MinReady = 4
	cfg.Workers.MaxRequests = 0
	base, stop := startManaged(t, cfg)
	defer stop()

	const requests = 1600
	const workers = 8
	const rounds = 3
	client := &http.Client{Transport: &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32}}
	warmup, err := client.Get(base + "/item")
	if err != nil {
		t.Fatal(err)
	}
	warmup.Body.Close()
	if warmup.StatusCode != 200 {
		t.Fatalf("warmup status %d", warmup.StatusCode)
	}

	var rps []float64
	var p50 []time.Duration
	var p95 []time.Duration
	var p99 []time.Duration
	var rss []uint64
	var cpu []float64
	var phpN int
	for round := 0; round < rounds; round++ {
		rssBefore, ticksBefore, nBefore := phpSample("eregion-worker")
		latencies := make([]time.Duration, requests)
		errCh := make(chan error, workers)
		start := time.Now()
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for i := offset; i < requests; i += workers {
					one := time.Now()
					resp, err := client.Get(base + "/item")
					if err != nil {
						errCh <- err
						return
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					latencies[i] = time.Since(one)
					if resp.StatusCode != 200 {
						errCh <- fmt.Errorf("status %d", resp.StatusCode)
						return
					}
				}
			}(w)
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		rssAfter, ticksAfter, nAfter := phpSample("eregion-worker")
		rps = append(rps, float64(requests)/elapsed.Seconds())
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		p50 = append(p50, latencies[percentile(latencies, 0.50)])
		p95 = append(p95, latencies[percentile(latencies, 0.95)])
		p99 = append(p99, latencies[percentile(latencies, 0.99)])
		if nAfter > phpN {
			phpN = nAfter
		}
		if rssAfter > rssBefore {
			rss = append(rss, rssAfter)
		} else if rssBefore > 0 {
			rss = append(rss, rssBefore)
		}
		if nBefore > 0 && elapsed > 0 && ticksAfter >= ticksBefore {
			cpu = append(cpu, float64(ticksAfter-ticksBefore)/100.0/elapsed.Seconds()*100.0)
		}
	}
	logged := append([]float64(nil), rps...)
	sort.Float64s(rps)
	sort.Slice(p50, func(i, j int) bool { return p50[i] < p50[j] })
	sort.Slice(p95, func(i, j int) bool { return p95[i] < p95[j] })
	sort.Slice(p99, func(i, j int) bool { return p99[i] < p99[j] })
	var rssP50 uint64
	if len(rss) > 0 {
		sort.Slice(rss, func(i, j int) bool { return rss[i] < rss[j] })
		rssP50 = rss[len(rss)/2]
	}
	var cpuP50 float64
	if len(cpu) > 0 {
		sort.Float64s(cpu)
		cpuP50 = cpu[len(cpu)/2]
	}
	return httpSummary{
		rpsP50: rps[len(rps)/2], p50: p50[len(p50)/2], p95: p95[len(p95)/2], p99: p99[len(p99)/2],
		phpRSSKB: rssP50, phpCPU: cpuP50, phpN: phpN, rounds: logged,
	}
}

func phpSample(needle string) (rssKB uint64, ticks uint64, n int) {
	if runtime.GOOS != "linux" {
		return 0, 0, 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, 0
	}
	for _, entry := range entries {
		cmd, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil || !bytes.Contains(cmd, []byte(needle)) {
			continue
		}
		status, err := os.ReadFile("/proc/" + entry.Name() + "/status")
		if err == nil {
			for _, line := range strings.Split(string(status), "\n") {
				if !strings.HasPrefix(line, "VmRSS:") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					v, _ := strconv.ParseUint(fields[1], 10, 64)
					rssKB += v
				}
			}
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		text := string(stat)
		cut := strings.LastIndex(text, ")")
		if cut < 0 || cut+2 >= len(text) {
			continue
		}
		fields := strings.Fields(text[cut+2:])
		if len(fields) < 13 {
			continue
		}
		user, _ := strconv.ParseUint(fields[11], 10, 64)
		sys, _ := strconv.ParseUint(fields[12], 10, 64)
		ticks += user + sys
		n++
	}
	return rssKB, ticks, n
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

func TestMithrilHTTPBenchPairs(t *testing.T) {
	if os.Getenv("EREGION_HTTP_BENCH") == "" {
		t.Skip("N/A unless EREGION_HTTP_BENCH=1; this is a manual reproducible bench, not a unit gate")
	}
	if runtime.GOOS != "linux" {
		t.Skip("paired HTTP bench is measured on Linux")
	}
	root := mithrilRoot(t)
	if strings.Contains(root, "/mnt/c/") || strings.Contains(mustSelf(t), "/mnt/c/") {
		t.Fatal("repositories must sit on the Linux filesystem, not /mnt/c")
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("mithril root must be a real directory: %s", root)
	}
	worker := filepath.Join(root, "bin", "eregion-worker")
	app := filepath.Join(root, "bench", "http", "App.php")
	autoload := filepath.Join(root, "vendor", "autoload.php")
	for _, path := range []string{worker, app, autoload} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}

	const pairs = 10
	type pairResult struct {
		OffRPS         float64 `json:"off_rps"`
		ObserveRPS     float64 `json:"observe_rps"`
		DropPercent    float64 `json:"drop_percent"`
		OffP50MS       float64 `json:"off_p50_ms"`
		ObserveP50MS   float64 `json:"observe_p50_ms"`
		OffP95MS       float64 `json:"off_p95_ms"`
		ObserveP95MS   float64 `json:"observe_p95_ms"`
		OffP99MS       float64 `json:"off_p99_ms"`
		ObserveP99MS   float64 `json:"observe_p99_ms"`
		OffRSSKB       uint64  `json:"off_rss_kb"`
		ObserveRSSKB   uint64  `json:"observe_rss_kb"`
		OffCPU         float64 `json:"off_cpu_pct"`
		ObserveCPU     float64 `json:"observe_cpu_pct"`
		OffWorkers     int     `json:"off_workers"`
		ObserveWorkers int     `json:"observe_workers"`
	}
	results := make([]pairResult, 0, pairs)
	drops := make([]float64, 0, pairs)
	for i := 0; i < pairs; i++ {
		off := runHTTPPairSide(t, worker, app, autoload, "")
		if hits := workerProcesses(worker); len(hits) != 0 {
			t.Fatalf("off left workers: %v", hits)
		}
		observe := runHTTPPairSide(t, worker, app, autoload, observeConfig())
		if hits := workerProcesses(worker); len(hits) != 0 {
			t.Fatalf("observe left workers: %v", hits)
		}
		drop := (off.rps - observe.rps) / off.rps * 100
		drops = append(drops, drop)
		row := pairResult{
			OffRPS: off.rps, ObserveRPS: observe.rps, DropPercent: drop,
			OffP50MS: ms(off.p50), ObserveP50MS: ms(observe.p50),
			OffP95MS: ms(off.p95), ObserveP95MS: ms(observe.p95),
			OffP99MS: ms(off.p99), ObserveP99MS: ms(observe.p99),
			OffRSSKB: off.rssKB, ObserveRSSKB: observe.rssKB,
			OffCPU: off.cpu, ObserveCPU: observe.cpu,
			OffWorkers: off.workers, ObserveWorkers: observe.workers,
		}
		results = append(results, row)
		t.Logf("pair %d off=%.1f observe=%.1f drop=%.2f%% p50=%s/%s p95=%s/%s p99=%s/%s rss=%d/%d cpu=%.1f/%.1f",
			i+1, off.rps, observe.rps, drop, off.p50, observe.p50, off.p95, observe.p95, off.p99, observe.p99, off.rssKB, observe.rssKB, off.cpu, observe.cpu)
	}
	mean, half := confidence95(drops)
	payload := map[string]any{
		"pairs":                  results,
		"drop_percent_mean":      mean,
		"drop_percent_ci95_low":  mean - half,
		"drop_percent_ci95_high": mean + half,
		"ci_excludes_above_5":    mean+half <= 5,
		"hook_ns":                "N/A on the HTTP path; see bench/results/hook-opt-recheck.json",
		"layout":                 "both repositories on the Linux filesystem, no /mnt/c symlink",
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "bench", "results", "http-pairs-linux.json")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("drop mean=%.2f%% ci95=[%.2f, %.2f] wrote %s", mean, mean-half, mean+half, out)
}

type pairSide struct {
	rps     float64
	p50     time.Duration
	p95     time.Duration
	p99     time.Duration
	rssKB   uint64
	cpu     float64
	workers int
}

func runHTTPPairSide(t *testing.T, worker, app, autoload, guardPHP string) pairSide {
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
	cfg.PHP.Environment = map[string]string{"BENCH_DB_MICROS": "0"}
	cfg.Workers.Count = 4
	cfg.Workers.MinReady = 4
	cfg.Workers.MaxRequests = 0
	base, stop := startManaged(t, cfg)
	defer stop()

	client := &http.Client{Transport: &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 32}}
	for i := 0; i < 100; i++ {
		resp, err := client.Get(base + "/item")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("warmup status %d", resp.StatusCode)
		}
	}

	const requests = 1000
	const clients = 8
	rssBefore, ticksBefore, _ := phpSample("eregion-worker")
	latencies := make([]time.Duration, requests)
	errCh := make(chan error, clients)
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < clients; w++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := offset; i < requests; i += clients {
				one := time.Now()
				resp, err := client.Get(base + "/item")
				if err != nil {
					errCh <- err
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				latencies[i] = time.Since(one)
				if resp.StatusCode != 200 {
					errCh <- fmt.Errorf("status %d", resp.StatusCode)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	rssAfter, ticksAfter, nAfter := phpSample("eregion-worker")
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	rss := rssAfter
	if rss < rssBefore {
		rss = rssBefore
	}
	cpu := 0.0
	if elapsed > 0 && ticksAfter >= ticksBefore {
		cpu = float64(ticksAfter-ticksBefore) / 100.0 / elapsed.Seconds() * 100.0
	}
	return pairSide{
		rps:   float64(requests) / elapsed.Seconds(),
		p50:   latencies[percentile(latencies, 0.50)],
		p95:   latencies[percentile(latencies, 0.95)],
		p99:   latencies[percentile(latencies, 0.99)],
		rssKB: rss, cpu: cpu, workers: nAfter,
	}
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func confidence95(values []float64) (mean float64, half float64) {
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	std := math.Sqrt(sum / float64(len(values)-1))
	const tCrit = 2.26215716274 // Student t, df=9, two-sided 95%
	return mean, tCrit * std / math.Sqrt(float64(len(values)))
}

func mustSelf(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return file
}

func mithrilRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "mithrilphp"))
}
