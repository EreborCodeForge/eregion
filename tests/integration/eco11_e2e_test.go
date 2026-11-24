//go:build linux

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const eco11OfficialSHA = "d5a74b13c20e70a218b6f125164037a580de901b5a13857def5c1ff1be020996"

// TestECO11FunctionalE2E runs T55–T60 and the single-supervisor churn of T63.
// It stays out of the default suite unless EREGION_ECO11_E2E=1, and it never
// rewrites the official performance artifact.
func TestECO11FunctionalE2E(t *testing.T) {
	if os.Getenv("EREGION_ECO11_E2E") != "1" {
		t.Skip("set EREGION_ECO11_E2E=1 on Linux ext4 to run T55–T60 and T63")
	}
	root := eregionRoot(t)
	refuseMntC(t, root)
	official := filepath.Join(root, "docs", "eco-11", "results", "eco11-performance.json")
	if got := fileSHA256(t, official); got != eco11OfficialSHA {
		t.Fatalf("official performance artifact changed before E2E: %s", got)
	}
	phpBin := requirePHP(t)
	mithril := mithrilRoot(t)
	worker, app, autoload := benchPaths(t, mithril)
	bin := buildEregion(t, "")
	outDir := filepath.Join(root, "docs", "eco-11", "results", "e2e")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id  string
		run func(t *testing.T) map[string]any
	}{
		{"T55", func(t *testing.T) map[string]any { return e2eT55(t, bin, phpBin, worker, app, autoload) }},
		{"T56", func(t *testing.T) map[string]any { return e2eT56(t, bin, phpBin, worker, app, autoload) }},
		{"T57", func(t *testing.T) map[string]any { return e2eT57(t, bin, phpBin, worker, app, autoload) }},
		{"T58", func(t *testing.T) map[string]any { return e2eT58(t, bin, phpBin, worker, app, autoload) }},
		{"T59", func(t *testing.T) map[string]any { return e2eT59(t, bin, phpBin, worker, app, autoload) }},
		{"T60", func(t *testing.T) map[string]any { return e2eT60(t, bin, phpBin, worker, app, autoload) }},
		{"T63", func(t *testing.T) map[string]any { return e2eT63(t, bin, phpBin, worker, app, autoload, root) }},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			report := tc.run(t)
			raw, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(outDir, strings.ToLower(tc.id)+".json")
			if tc.id == "T63" {
				path = filepath.Join(root, "docs", "eco-11", "results", "eco11-churn.json")
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			status, _ := report["status"].(string)
			if status != "PASS" {
				t.Errorf("%s status %s", tc.id, status)
			}
		})
	}
	if got := fileSHA256(t, official); got != eco11OfficialSHA {
		t.Fatalf("official performance artifact changed during E2E: %s", got)
	}
}

type e2eCfg struct {
	php, worker, dir, script, sink, upstream, mode string
	port, hard, reserve, fallback                  int
	httpMin, httpMax, httpMem                      int
	workMin, workMax                               int
	strategy                                       string
	targetRPS, cpuTarget                           float64
	targetP95                                      string
	dbMicros, cpuIters, holdMB, queue              int
	backlog                                        bool
	adaptive                                       bool
}

func (c e2eCfg) yaml() string {
	var b strings.Builder
	fmt.Fprintf(&b, "version: \"1\"\nserver:\n  host: 127.0.0.1\n  port: %d\n  shutdown_timeout: 3s\n", c.port)
	fmt.Fprintf(&b, "php:\n  binary: %s\n  worker_script: %s\n  working_directory: %s\n  environment:\n", strconv.Quote(c.php), strconv.Quote(c.worker), strconv.Quote(c.dir))
	fmt.Fprintf(&b, "    BENCH_DB_MICROS: %q\n    BENCH_CPU_ITERS: %q\n    BENCH_HOLD_MB: %q\n", strconv.Itoa(c.dbMicros), strconv.Itoa(c.cpuIters), strconv.Itoa(c.holdMB))
	if c.upstream != "" {
		fmt.Fprintf(&b, "    BENCH_UPSTREAM_URL: %q\n", c.upstream)
	}
	fmt.Fprintf(&b, "workers:\n  count: %d\n  min_ready: 1\n  max_requests: 0\n  acquire_timeout: 20s\n  shutdown_timeout: 2s\n", max(c.httpMax, 1))
	queue := c.queue
	if queue <= 0 {
		queue = 128
	}
	fmt.Fprintf(&b, "socket:\n  directory: %s\nqueue:\n  capacity: %d\nlogging:\n  level: error\n  format: text\n  access_log: false\n", strconv.Quote(filepath.Join(c.dir, "s")), queue)
	fmt.Fprintf(&b, "resource_guard:\n  mode: %s\n  memory:\n    reserve_mb: %d\n    hard_limit_mb: %d\n    reserve_percent: 0\n    worker_fallback_mb: %d\n", c.mode, c.reserve, c.hard, c.fallback)
	fmt.Fprintf(&b, "workloads:\n  http:\n    mode: http\n    workers:\n      min: %d\n      max: %d\n    resources:\n      class: io\n      memory_mb: %d\n    scaling:\n", c.httpMin, c.httpMax, c.httpMem)
	fmt.Fprintf(&b, "      strategy: %s\n", c.strategy)
	if c.strategy != "fixed" {
		fmt.Fprintf(&b, "      control:\n        window: 10s\n        max_signal_age: 20s\n        up_confirmations: 1\n        down_confirmations: 1\n        max_step_up: 1\n        max_step_down: 1\n")
	}
	switch c.strategy {
	case "throughput":
		fmt.Fprintf(&b, "      throughput:\n        target_rps_per_worker: %g\n        tolerance_ratio: 0.10\n", c.targetRPS)
	case "latency":
		p95 := c.targetP95
		if p95 == "" {
			p95 = "50ms"
		}
		fmt.Fprintf(&b, "      latency:\n        target_p95: %s\n        queue_wait_target_p95: 20ms\n        tolerance_ratio: 0.20\n        busy_threshold: 0.90\n", p95)
	case "cpu_utilization":
		fmt.Fprintf(&b, "      cpu:\n        target_ratio: %g\n        tolerance_ratio: 0.05\n", c.cpuTarget)
	case "adaptive":
		fmt.Fprintf(&b, "      adaptive:\n        min_gain_ratio: 0.05\n        max_p95_regression_ratio: 0.25\n        demand_tolerance_ratio: 0.15\n        baseline_windows: 2\n        evaluation_windows: 2\n        settle_windows: 1\n        probe_cooldown: 10s\n        probe_timeout: 90s\n        busy_threshold: 0.85\n")
	}
	if c.backlog {
		fmt.Fprintf(&b, "  work:\n    mode: consumer\n    command: [%s, %s]\n    workers:\n      min: %d\n      max: %d\n    resources:\n      class: io\n      memory_mb: 64\n    scaling:\n      strategy: backlog\n      avg_job_duration: 1s\n      target_drain_time: 5s\n      backlog_source:\n        type: http_json\n        url_env: EREGION_BENCH_BACKLOG_URL\n        timeout: 500ms\n        max_staleness: 15s\n", strconv.Quote(c.php), strconv.Quote(c.script), c.workMin, c.workMax)
	}
	return b.String()
}

type e2eRun struct {
	t       *testing.T
	proc    *guardProc
	base    string
	logPath string
	stop    func() error
}

func startE2E(t *testing.T, bin string, cfg e2eCfg, extra []string) *e2eRun {
	t.Helper()
	dir := t.TempDir()
	cfg.dir = dir
	if cfg.mode == "" {
		cfg.mode = "observe"
	}
	if cfg.hard == 0 {
		cfg.hard = 8192
	}
	if cfg.reserve == 0 {
		cfg.reserve = 256
	}
	if cfg.fallback == 0 {
		cfg.fallback = 64
	}
	if cfg.httpMem == 0 {
		cfg.httpMem = 64
	}
	if cfg.port == 0 {
		cfg.port = pickTCPPort(t)
	}
	bootstrap := filepath.Join(dir, "bootstrap.php")
	if err := os.WriteFile(bootstrap, []byte(fmt.Sprintf("<?php\nrequire %q;\nrequire %q;\n", autoloadOf(t), appOf(t))), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	rawManifest := fmt.Sprintf("{\n  \"application\": \"Bench\\\\Http\\\\App\",\n  \"autoload\": %q,\n  \"workingDirectory\": %q\n}\n", filepath.ToSlash(bootstrap), filepath.ToSlash(dir))
	if err := os.WriteFile(manifest, []byte(rawManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg.backlog {
		cfg.sink = filepath.Join(dir, "work-sink")
		cfg.script = filepath.Join(dir, "work.php")
		body := "<?php\ndeclare(strict_types=1);\n$sink = getenv('WORK_SINK') ?: '/tmp/work-sink';\nwhile (true) {\n    $buf = random_bytes(65536);\n    hash('sha256', $buf, true);\n    file_put_contents($sink, \"1\\n\", FILE_APPEND);\n    usleep(20000);\n}\n"
		if err := os.WriteFile(cfg.script, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		extra = append(extra, "WORK_SINK="+cfg.sink)
	}
	cfgPath := filepath.Join(dir, "eregion.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg.yaml()), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := startGuardProc(bin, []string{"serve", "--config", cfgPath, "--manifest", manifest}, extra, logFile)
	if err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.port)
	run := &e2eRun{t: t, proc: proc, base: base, logPath: logPath}
	run.stop = func() error {
		err := proc.stop()
		logFile.Close()
		return err
	}
	if err := waitListening(base+livenessPath, 20*time.Second); err != nil {
		_ = run.stop()
		t.Fatalf("serve did not listen: %v\n%s", err, tailFile(logPath))
	}
	return run
}

func autoloadOf(t *testing.T) string {
	t.Helper()
	_, app, autoload := benchPaths(t, mithrilRoot(t))
	_ = app
	return autoload
}

func appOf(t *testing.T) string {
	t.Helper()
	_, app, _ := benchPaths(t, mithrilRoot(t))
	return app
}

type e2eSnap struct {
	At        string  `json:"at"`
	Phase     string  `json:"phase"`
	Requested int     `json:"requested"`
	Admitted  int     `json:"admitted"`
	Running   int     `json:"running"`
	Draining  int     `json:"draining"`
	Up        int     `json:"scale_up"`
	Down      int     `json:"scale_down"`
	CPU       float64 `json:"cpu"`
	CPUValid  int     `json:"cpu_valid"`
	Throttle  float64 `json:"throttle"`
	ThrValid  int     `json:"throttle_valid"`
	Offered   float64 `json:"offered_rps"`
	WorkerCPU float64 `json:"worker_cpu_ratio"`
	P95       float64 `json:"request_p95_seconds"`
	Memory    float64 `json:"memory_bytes"`
	Headroom  float64 `json:"headroom_bytes"`
	Denied    int     `json:"admission_denied"`
	Queue     float64 `json:"queue_waiting"`
	Probe     string  `json:"probe_state,omitempty"`
	Gain      float64 `json:"marginal_gain"`
	Reason    string  `json:"reason,omitempty"`
	PID       int     `json:"pid"`
}

func (r *e2eRun) scrape() string {
	body, err := scrapeOK(r.base + metricsPath)
	if err != nil {
		return ""
	}
	return body
}

func (r *e2eRun) snap(workload, phase string) e2eSnap {
	body := r.scrape()
	cpu, _ := metricGauge(body, "eregion_resource_cpu_utilization_ratio")
	thr, _ := metricGauge(body, "eregion_resource_cpu_throttling_ratio")
	mem, _ := metricGauge(body, "eregion_resource_memory_current_bytes")
	head, _ := metricGauge(body, "eregion_resource_memory_headroom_bytes")
	return e2eSnap{
		At: time.Now().UTC().Format(time.RFC3339Nano), Phase: phase, PID: r.proc.pid(),
		Requested: metricInt(body, "eregion_scaling_requested_workers", workload),
		Admitted:  metricInt(body, "eregion_scaling_admitted_workers", workload),
		Running:   metricInt(body, "eregion_workload_running_workers", workload),
		Draining:  metricInt(body, "eregion_workload_draining_workers", workload),
		Up:        metricInt(body, "eregion_workload_scale_events_total", workload),
		Down:      scaleDirection(body, workload, "down"),
		CPU:       cpu, CPUValid: labeledInt(body, "eregion_resource_sample_valid", "", "cpu_usage"),
		Throttle: thr, ThrValid: labeledInt(body, "eregion_resource_sample_valid", "", "cpu_throttling"),
		Memory: mem, Headroom: head,
		Denied: decisionSum(body, workload, "memory_headroom"),
		Reason: latestReason(body, workload),
	}
}

func (r *e2eRun) until(timeout time.Duration, workload, phase string, ok func(e2eSnap, string) bool) (e2eSnap, []e2eSnap, bool) {
	deadline := time.Now().Add(timeout)
	var series []e2eSnap
	var last e2eSnap
	var prev e2eSnap
	for {
		body := r.scrape()
		last = r.snap(workload, phase)
		last.Up = scaleDirection(body, workload, "up")
		last.Down = scaleDirection(body, workload, "down")
		last.Denied = labeledInt(body, "eregion_scaling_admission_denied_total", workload, "memory_headroom")
		last.Reason = latestReason(body, workload)
		last.Offered, _ = signalValue(body, workload, "offered_rps")
		last.WorkerCPU, _ = signalValue(body, workload, "worker_cpu_ratio")
		last.P95, _ = signalValue(body, workload, "request_p95")
		last.Queue, _ = metricGauge(body, "eregion_queue_waiting")
		last.Probe = probeState(body, workload)
		last.Gain, _ = metricGauge(body, "eregion_scaling_efficiency_marginal_gain_ratio")
		last.CPU = finite(last.CPU)
		last.Throttle = finite(last.Throttle)
		last.Offered = finite(last.Offered)
		last.WorkerCPU = finite(last.WorkerCPU)
		last.P95 = finite(last.P95)
		last.Gain = finite(last.Gain)
		last.Memory = finite(last.Memory)
		last.Headroom = finite(last.Headroom)
		last.Queue = finite(last.Queue)
		if len(series) == 0 || last.Running != prev.Running || last.Admitted != prev.Admitted || last.Requested != prev.Requested || last.Draining != prev.Draining || last.Reason != prev.Reason || last.CPUValid != prev.CPUValid || last.ThrValid != prev.ThrValid || last.Probe != prev.Probe {
			series = append(series, last)
			prev = last
		}
		if body != "" && ok(last, body) {
			return last, series, true
		}
		if time.Now().After(deadline) {
			if len(series) == 0 || series[len(series)-1].At != last.At {
				series = append(series, last)
			}
			return last, series, false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func e2eT55(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "observe", httpMin: 1, httpMax: 3, strategy: "throughput", targetRPS: 8, dbMicros: 200}, nil)
	defer func() { _ = run.stop() }()
	if _, _, ok := run.until(25*time.Second, "http", "ready", func(s e2eSnap, _ string) bool { return s.Running >= 1 }); !ok {
		return failReport("T55", "workers did not become ready", nil)
	}
	lowCtx, lowStop := context.WithCancel(context.Background())
	low := &traffic{}
	go drive(lowCtx, run.base+"/", 500*time.Millisecond, 2, low)
	_, lowSeries, lowOK := run.until(20*time.Second, "http", "low", func(s e2eSnap, _ string) bool { return false })
	lowStop()
	_ = lowOK
	if lowSeries[len(lowSeries)-1].Running != 1 || lowSeries[len(lowSeries)-1].Up != 0 {
		return failReport("T55", "low load changed the worker count", lowSeries)
	}
	highCtx, highStop := context.WithCancel(context.Background())
	high := &traffic{}
	go drive(highCtx, run.base+"/", 12*time.Millisecond, 24, high)
	up, upSeries, upOK := run.until(70*time.Second, "http", "burst", func(s e2eSnap, _ string) bool {
		return s.Running >= 2 && s.Admitted >= 2 && s.Requested >= 2 && s.Up >= 1
	})
	highStop()
	if !upOK {
		return failReport("T55", "burst did not step up requested, admitted and running", upSeries)
	}
	_, downSeries, downOK := run.until(100*time.Second, "http", "idle", func(s e2eSnap, _ string) bool {
		return s.Running == 1 && s.Down >= 1
	})
	if !downOK {
		return failReport("T55", "idle did not step back to the minimum", append(upSeries, downSeries...))
	}
	okN, failN, p95 := high.snapshot()
	return map[string]any{
		"id": "T55", "status": "PASS", "pid": run.proc.pid(),
		"acceptance": "throughput steps up one worker under offered load and steps down after idle",
		"burst":      up, "low": lowSeries, "up": upSeries, "down": downSeries,
		"offered_ok": okN, "offered_fail": failN, "client_p95_seconds": p95,
	}
}

func e2eT56(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	var depth int
	var mu sync.Mutex
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n := depth
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf("{\"backlog\":%d}\n", n))
	}))
	defer provider.Close()
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "observe", httpMin: 1, httpMax: 1, strategy: "fixed", backlog: true, workMin: 0, workMax: 2}, []string{"EREGION_BENCH_BACKLOG_URL=" + provider.URL})
	defer func() { _ = run.stop() }()
	sink := filepath.Join(filepath.Dir(run.logPath), "work-sink")
	_, idleSeries, idleOK := run.until(25*time.Second, "work", "idle", func(s e2eSnap, _ string) bool { return s.Running == 0 && s.Admitted == 0 })
	if !idleOK {
		return failReport("T56", "consumer did not stay at zero without backlog", idleSeries)
	}
	before := fileSize(sink)
	mu.Lock()
	depth = 20
	mu.Unlock()
	_, upSeries, upOK := run.until(70*time.Second, "work", "wake", func(s e2eSnap, _ string) bool {
		return s.Running >= 1 && s.Admitted >= 1 && fileSize(sink) > before
	})
	if !upOK {
		return failReport("T56", "backlog did not wake a worker that processed bytes", upSeries)
	}
	grown := fileSize(sink)
	mu.Lock()
	depth = 0
	mu.Unlock()
	_, downSeries, downOK := run.until(90*time.Second, "work", "drain", func(s e2eSnap, _ string) bool {
		return s.Running == 0 && s.Admitted == 0
	})
	if !downOK {
		return failReport("T56", "zero backlog did not drain the consumer", downSeries)
	}
	err := run.stop()
	run.stop = func() error { return nil }
	if err != nil && strings.Contains(err.Error(), "SIGKILL") {
		return failReport("T56", "drain ended with SIGKILL", downSeries)
	}
	return map[string]any{
		"id": "T56", "status": "PASS", "pid": run.proc.pid(),
		"acceptance":   "real http_json backlog wakes a worker, the sink grows, and zero depth drains",
		"bytes_before": before, "bytes_processed": grown - before,
		"idle": idleSeries, "wake": upSeries, "drain": downSeries,
	}
}

func e2eT57(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(40 * time.Millisecond)
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "slow")
	}))
	defer upstream.Close()
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "observe", httpMin: 2, httpMax: 4, strategy: "latency", targetP95: "25ms", upstream: upstream.URL, dbMicros: 0}, nil)
	defer func() { _ = run.stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &traffic{}
	go driveFor(ctx, run.base+"/", 5*time.Millisecond, 1, tr, 5*time.Second)
	last, series, ok := run.until(70*time.Second, "http", "slow", func(s e2eSnap, body string) bool {
		p95, p95OK := signalValue(body, "http", "request_p95")
		queue, _ := metricGauge(body, "eregion_queue_waiting")
		return p95OK && p95 > 0.030 && queue == 0 && s.Running == 2 && s.Up == 0 && hits.Load() > 100
	})
	if !ok {
		report := failReport("T57", "slow dependency without a queue did not hold the worker count", series)
		report["upstream_hits"] = hits.Load()
		report["client"] = trafficMap(tr)
		return report
	}
	return map[string]any{
		"id": "T57", "status": "PASS", "pid": run.proc.pid(), "observed": last, "series": series,
		"upstream_hits": hits.Load(),
		"acceptance":    "request p95 stays high while the queue is empty and the worker count does not grow",
	}
}

func e2eT58(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "enforce", httpMin: 1, httpMax: 3, strategy: "cpu_utilization", cpuTarget: 0.40, cpuIters: 150000}, nil)
	defer func() { _ = run.stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go drive(ctx, run.base+"/", 5*time.Millisecond, 8, &traffic{})
	note, err := tightenCPU(run.proc.pid())
	if err != nil {
		last, series, _ := run.until(20*time.Second, "http", "cpu", func(e2eSnap, string) bool { return false })
		report := failReport("T58", "BLOCKED: cgroup cpu quota was not applied: "+err.Error(), series)
		report["status"] = "BLOCKED"
		report["last"] = last
		report["platform_limit"] = err.Error()
		return report
	}
	defer os.RemoveAll(note)
	last, series, ok := run.until(70*time.Second, "http", "throttle", func(s e2eSnap, body string) bool {
		thrValid := labeledInt(body, "eregion_resource_sample_valid", "", "cpu_throttling")
		cpuValid := labeledInt(body, "eregion_resource_sample_valid", "", "cpu_usage")
		return (thrValid == 1 && s.Throttle >= 0.20) || (cpuValid == 1 && s.CPU >= 0.95)
	})
	if !ok {
		report := failReport("T58", "BLOCKED: sampler did not reach the existing CPU or throttle veto threshold", series)
		report["status"] = "BLOCKED"
		return report
	}
	held := last.Running
	_, holdSeries, _ := run.until(25*time.Second, "http", "hold", func(e2eSnap, string) bool { return false })
	end := holdSeries[len(holdSeries)-1]
	if end.Running > held || end.Admitted > last.Admitted {
		return failReport("T58", "veto allowed another expansion", append(series, holdSeries...))
	}
	if end.Running < held {
		return failReport("T58", "veto forced a scale-down", append(series, holdSeries...))
	}
	if end.WorkerCPU <= 0.45 {
		return failReport("T58", "worker CPU stayed inside the band, so no expansion was requested", append(series, holdSeries...))
	}
	err = run.stop()
	run.stop = func() error { return nil }
	if err != nil && strings.Contains(err.Error(), "SIGKILL") {
		return failReport("T58", "SIGKILL during CPU veto", series)
	}
	return map[string]any{
		"id": "T58", "status": "PASS", "pid": run.proc.pid(), "cgroup": note,
		"acceptance": "cpu saturation veto stops expansion and does not force a scale-down",
		"veto":       last, "series": append(series, holdSeries...),
	}
}

func e2eT59(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	run := startE2E(t, bin, e2eCfg{
		php: php, worker: worker, mode: "enforce", hard: 320, reserve: 32, fallback: 64,
		httpMin: 1, httpMax: 3, httpMem: 128, strategy: "throughput", targetRPS: 4, holdMB: 220, dbMicros: 200,
	}, nil)
	defer func() { _ = run.stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go drive(ctx, run.base+"/", 10*time.Millisecond, 16, &traffic{})
	last, series, ok := run.until(80*time.Second, "http", "budget", func(s e2eSnap, body string) bool {
		return s.Denied > 0 && s.Admitted <= 1 && s.Running <= 1
	})
	if !ok {
		return failReport("T59", "memory budget did not block the next admission", series)
	}
	err := run.stop()
	run.stop = func() error { return nil }
	if err != nil && strings.Contains(err.Error(), "SIGKILL") {
		return failReport("T59", "SIGKILL under memory pressure", series)
	}
	return map[string]any{
		"id": "T59", "status": "PASS", "pid": run.proc.pid(), "observed": last, "series": series,
		"acceptance": "enforce denies a new admission once RSS leaves no headroom for another worker",
	}
}

func e2eT60(t *testing.T, bin, php, worker, app, autoload string) map[string]any {
	_ = app
	_ = autoload
	accept := probePhase(t, bin, php, worker, true)
	reject := probePhase(t, bin, php, worker, false)
	status := "PASS"
	if accept["status"] != "PASS" || reject["status"] != "PASS" {
		status = "FAIL"
	}
	return map[string]any{
		"id": "T60", "status": status,
		"acceptance": "adaptive accepts only a measured gain and rolls a confounded probe back by drain",
		"accepted":   accept, "rejected": reject,
	}
}

func probePhase(t *testing.T, bin, php, worker string, steady bool) map[string]any {
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "enforce", httpMin: 1, httpMax: 2, strategy: "adaptive", dbMicros: 80000, queue: 1024}, nil)
	defer func() { _ = run.stop() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := &traffic{}
	load, stopLoad := context.WithCancel(ctx)
	go driveFor(load, run.base+"/", 70*time.Millisecond, 400, tr, 25*time.Second)
	if !steady {
		_, armed, armOK := run.until(120*time.Second, "http", "arm", func(s e2eSnap, _ string) bool {
			return s.Running >= 2 && s.Admitted >= 2
		})
		stopLoad()
		if !armOK {
			report := failReport("T60", "probe did not add a worker before the offered load shifted", armed)
			report["client"] = trafficMap(tr)
			return report
		}
		load, stopLoad = context.WithCancel(ctx)
		go driveFor(load, run.base+"/", 8*time.Millisecond, 400, tr, 30*time.Second)
		last, series, ok := run.until(120*time.Second, "http", "rollback", func(s e2eSnap, body string) bool {
			state := probeState(body, "http")
			return (state == "rollback" || state == "cooldown") && s.Running <= 1 && s.Draining == 0
		})
		stopLoad()
		series = append(armed, series...)
		if !ok {
			report := failReport("T60", "shifting offered load did not roll the probe back", series)
			report["client"] = trafficMap(tr)
			return report
		}
		err := run.stop()
		run.stop = func() error { return nil }
		if err != nil && strings.Contains(err.Error(), "SIGKILL") {
			return failReport("T60", "probe rollback used SIGKILL", series)
		}
		return map[string]any{"status": "PASS", "pid": run.proc.pid(), "observed": last, "series": series, "steady": false}
	}
	last, series, ok := run.until(180*time.Second, "http", "gain", func(s e2eSnap, body string) bool {
		gain, gainOK := metricGauge(body, "eregion_scaling_efficiency_marginal_gain_ratio")
		return gainOK && gain >= 0.05 && s.Running >= 2 && s.Admitted >= 2 && probeState(body, "http") == "cooldown"
	})
	stopLoad()
	if !ok {
		report := failReport("T60", "controlled load did not produce a measured probe gain", series)
		report["client"] = trafficMap(tr)
		return report
	}
	err := run.stop()
	run.stop = func() error { return nil }
	if err != nil && strings.Contains(err.Error(), "SIGKILL") {
		return failReport("T60", "probe acceptance used SIGKILL", series)
	}
	return map[string]any{
		"status": "PASS", "pid": run.proc.pid(), "observed": last, "series": series, "steady": true,
		"marginal_gain": last.Gain,
	}
}

func e2eT63(t *testing.T, bin, php, worker, app, autoload, root string) map[string]any {
	_ = app
	_ = autoload
	_ = root
	limits := map[string]any{
		"ceiling":          "15m",
		"window":           "10s",
		"max_step":         1,
		"cycles":           4,
		"rss_idle_factor":  1.5,
		"admitted_max":     3,
		"pid_stable":       true,
		"orphan_processes": 0,
		"recorded_before":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	run := startE2E(t, bin, e2eCfg{php: php, worker: worker, mode: "enforce", httpMin: 1, httpMax: 3, strategy: "throughput", targetRPS: 8, dbMicros: 1000}, nil)
	pid := run.proc.pid()
	baseSnap := sampleSupervisor(pid)
	limits["baseline_tree_rss_kb"] = baseSnap.RSSKB + baseSnap.ChildRSSKB
	limits["baseline_threads"] = baseSnap.Threads
	var series []e2eSnap
	var idleRSS []uint64
	deadline := time.Now().Add(15 * time.Minute)
	fail := ""
	for cycle := 1; cycle <= 4 && fail == "" && time.Now().Before(deadline); cycle++ {
		phase := fmt.Sprintf("low-%d", cycle)
		ctx, cancel := context.WithCancel(context.Background())
		go drive(ctx, run.base+"/", 400*time.Millisecond, 1, &traffic{})
		_, part, _ := run.until(12*time.Second, "http", phase, func(e2eSnap, string) bool { return false })
		cancel()
		series = append(series, part...)
		ctx, cancel = context.WithCancel(context.Background())
		go drive(ctx, run.base+"/", 12*time.Millisecond, 20, &traffic{})
		_, part, upOK := run.until(50*time.Second, "http", fmt.Sprintf("high-%d", cycle), func(s e2eSnap, _ string) bool {
			return s.Running >= 2 && s.Admitted <= 3
		})
		series = append(series, part...)
		if !upOK {
			fail = phase + " did not step up"
		}
		_, part, _ = run.until(10*time.Second, "http", fmt.Sprintf("burst-%d", cycle), func(e2eSnap, string) bool { return false })
		series = append(series, part...)
		cancel()
		_, part, downOK := run.until(130*time.Second, "http", fmt.Sprintf("idle-%d", cycle), func(s e2eSnap, _ string) bool {
			return s.Running == 1
		})
		series = append(series, part...)
		if !downOK && fail == "" {
			fail = fmt.Sprintf("idle-%d did not return to the minimum", cycle)
		}
		now := sampleSupervisor(pid)
		if nowPID := run.proc.pid(); nowPID != pid {
			fail = "supervisor pid changed"
		}
		tree := now.RSSKB + now.ChildRSSKB
		idleRSS = append(idleRSS, tree)
		if tree > uint64(float64(baseSnap.RSSKB+baseSnap.ChildRSSKB)*1.5) && fail == "" {
			fail = "idle RSS exceeded 1.5x baseline"
		}
		if len(part) > 0 && part[len(part)-1].Admitted > 3 && fail == "" {
			fail = "admitted above max"
		}
	}
	err := run.stop()
	if err != nil && strings.Contains(err.Error(), "SIGKILL") && fail == "" {
		fail = "shutdown required SIGKILL"
	}
	time.Sleep(500 * time.Millisecond)
	if len(descendantPIDs(pid)) != 0 && fail == "" {
		fail = "orphan processes remained"
	}
	if oscillating(series) && fail == "" {
		fail = "a step larger than 1 or a same-direction change inside the cooldown"
	}
	status := "PASS"
	if fail != "" {
		status = "FAIL"
	}
	return map[string]any{
		"id": "T63", "status": status, "pid": pid, "limits": limits, "failure": fail,
		"idle_tree_rss_kb": idleRSS, "series": series,
		"acceptance": "one supervisor pid across four load cycles without leaks, orphans, or step violations",
	}
}

// oscillating reports a step above 1, or a second change in the same direction
// inside 8s. The controller cooldown is the 10s window; the running gauge lags
// that decision by the worker start, so the observed gap can be a little shorter.
func oscillating(series []e2eSnap) bool {
	var lastUp, lastDown time.Time
	prevRun := -1
	for _, s := range series {
		at, err := time.Parse(time.RFC3339Nano, s.At)
		if err != nil || prevRun < 0 {
			prevRun = s.Running
			continue
		}
		delta := s.Running - prevRun
		if delta > 1 || delta < -1 {
			return true
		}
		if delta > 0 {
			if !lastUp.IsZero() && at.Sub(lastUp) < 8*time.Second {
				return true
			}
			lastUp = at
		}
		if delta < 0 {
			if !lastDown.IsZero() && at.Sub(lastDown) < 8*time.Second {
				return true
			}
			lastDown = at
		}
		prevRun = s.Running
	}
	return false
}

func failReport(id, reason string, series []e2eSnap) map[string]any {
	return map[string]any{"id": id, "status": "FAIL", "failure": reason, "series": series}
}

type traffic struct {
	mu   sync.Mutex
	ok   int
	fail int
	lats []float64
}

func (tr *traffic) add(ok bool, d time.Duration) {
	if tr == nil {
		return
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if ok {
		tr.ok++
		tr.lats = append(tr.lats, d.Seconds())
	} else {
		tr.fail++
	}
}

func (tr *traffic) snapshot() (int, int, float64) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.lats) == 0 {
		return tr.ok, tr.fail, 0
	}
	cp := append([]float64(nil), tr.lats...)
	for i := 1; i < len(cp); i++ {
		j := i
		for j > 0 && cp[j] < cp[j-1] {
			cp[j], cp[j-1] = cp[j-1], cp[j]
			j--
		}
	}
	return tr.ok, tr.fail, cp[len(cp)*95/100]
}

func drive(ctx context.Context, url string, every time.Duration, maxInflight int, tr *traffic) {
	driveFor(ctx, url, every, maxInflight, tr, 5*time.Second)
}

func driveFor(ctx context.Context, url string, every time.Duration, maxInflight int, tr *traffic, timeout time.Duration) {
	if every <= 0 {
		every = 10 * time.Millisecond
	}
	if maxInflight < 1 {
		maxInflight = 1
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	sem := make(chan struct{}, maxInflight)
	tick := time.NewTicker(every)
	defer tick.Stop()
	client := &http.Client{Timeout: timeout}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			select {
			case sem <- struct{}{}:
				go func() {
					defer func() { <-sem }()
					start := time.Now()
					resp, err := client.Get(url)
					if err != nil {
						tr.add(false, 0)
						return
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					tr.add(resp.StatusCode >= 200 && resp.StatusCode < 500, time.Since(start))
				}()
			default:
			}
		}
	}
}

func driveSerial(ctx context.Context, url string) {
	client := &http.Client{Timeout: 5 * time.Second}
	for ctx.Err() == nil {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func scaleDirection(body, workload, direction string) int {
	needle := fmt.Sprintf("eregion_workload_scale_events_total{workload=%q,direction=%q}", workload, direction)
	return lineInt(body, needle)
}

func decisionSum(body, workload, reason string) int {
	sum := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "eregion_scaling_decisions_total{") || !strings.Contains(line, `workload="`+workload+`"`) || !strings.Contains(line, `reason="`+reason+`"`) {
			continue
		}
		sum += lineInt(line, strings.Fields(line)[0])
	}
	return sum
}

func latestReason(body, workload string) string {
	best := ""
	bestN := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "eregion_scaling_decisions_total{") || !strings.Contains(line, `workload="`+workload+`"`) {
			continue
		}
		n := lineInt(line, strings.Fields(line)[0])
		if n >= bestN {
			bestN = n
			if i := strings.Index(line, `reason="`); i >= 0 {
				rest := line[i+len(`reason="`):]
				if j := strings.IndexByte(rest, '"'); j >= 0 {
					best = rest[:j]
				}
			}
		}
	}
	return best
}

func labeledInt(body, name, workload, label string) int {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		if workload != "" && !strings.Contains(line, `workload="`+workload+`"`) {
			continue
		}
		if label != "" && !strings.Contains(line, `"`+label+`"`) {
			continue
		}
		return lineInt(line, strings.Fields(line)[0])
	}
	return 0
}

func lineInt(body, prefix string) int {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		v, _ := strconv.Atoi(fields[len(fields)-1])
		return v
	}
	return 0
}

func signalValue(body, workload, name string) (float64, bool) {
	needle := fmt.Sprintf("eregion_scaling_signal_value{workload=%q,signal=%q", workload, name)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, needle) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		return v, err == nil
	}
	return 0, false
}

func metricGauge(body, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			if err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func trafficMap(tr *traffic) map[string]any {
	ok, fail, p95 := tr.snapshot()
	return map[string]any{"ok": ok, "fail": fail, "p95_seconds": p95}
}

func probeState(body, workload string) string {
	found := ""
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "eregion_scaling_efficiency_probe_state{") || !strings.Contains(line, `workload="`+workload+`"`) {
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(line), " 1") {
			continue
		}
		const key = `state="`
		i := strings.Index(line, key)
		if i < 0 {
			continue
		}
		rest := line[i+len(key):]
		j := strings.Index(rest, `"`)
		if j > 0 {
			found = rest[:j]
		}
	}
	return found
}

func tightenCPU(pid int) (string, error) {
	var reasons []string
	if dir, err := tightenCPUv2(pid); err == nil {
		return dir, nil
	} else {
		reasons = append(reasons, "cgroup2: "+err.Error())
	}
	if dir, err := tightenCPUv1(pid); err == nil {
		return dir, nil
	} else {
		reasons = append(reasons, "cgroup v1 cpu: "+err.Error())
	}
	return "", fmt.Errorf("cpu quota is not available on this host (%s)", strings.Join(reasons, "; "))
}

func tightenCPUv2(pid int) (string, error) {
	root := "/sys/fs/cgroup"
	mount := cgroup2MountPoint()
	if mount != "" {
		root = mount
	}
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return "", fmt.Errorf("cgroup v2 is not mounted: %w", err)
	}
	parentControl := filepath.Join(root, "cgroup.subtree_control")
	raw, _ := os.ReadFile(parentControl)
	if !strings.Contains(string(raw), "cpu") {
		_ = os.WriteFile(parentControl, []byte("+cpu"), 0o644)
	}
	dir := filepath.Join(root, fmt.Sprintf("eco11-e2e-%d", pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte("70000 100000\n"), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	pids := append([]int{pid}, descendantPIDs(pid)...)
	var moved int
	var last error
	for _, p := range pids {
		err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(p)+"\n"), 0o644)
		if err != nil {
			last = err
			continue
		}
		moved++
	}
	if moved == 0 {
		os.RemoveAll(dir)
		if last == nil {
			last = fmt.Errorf("no process could be moved")
		}
		return "", last
	}
	return dir, nil
}

func tightenCPUv1(pid int) (string, error) {
	root := "/sys/fs/cgroup/cpu"
	if _, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("not mounted: %w", err)
	}
	dir := filepath.Join(root, fmt.Sprintf("eco11-e2e-%d", pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.cfs_period_us"), []byte("100000\n"), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "cpu.cfs_quota_us"), []byte("70000\n"), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	pids := append([]int{pid}, descendantPIDs(pid)...)
	var moved int
	var last error
	for _, p := range pids {
		err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(p)+"\n"), 0o644)
		if err != nil {
			last = err
			continue
		}
		moved++
	}
	if moved == 0 {
		os.RemoveAll(dir)
		if last == nil {
			last = fmt.Errorf("no process could be moved")
		}
		return "", last
	}
	return dir, nil
}

func cgroup2MountPoint() string {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, " - cgroup2 ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 5 && strings.HasPrefix(fields[4], "/") {
			return fields[4]
		}
	}
	return ""
}
