package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const eco11BaselineRev = "7d6463764aeb965e8dfe4644237139f28dec9ef6"

const (
	eco11HTTPWorkers = 2
	eco11WorkWorkers = 1
	eco11Clients     = 4
)

// TestECO11GateRefusesIncomplete locks the closure rule: a short, dirty,
// drifted, or statistically open sample is not PASS. The 2% and 5% limits
// stay the ECO-09 constants.
func TestECO11GateRefusesIncomplete(t *testing.T) {
	if rpsDropLimit != 2 || p99RiseLimit != 5 || gateRounds != 20 || gateWindow != 60*time.Second || gateWarmup != 5*time.Second {
		t.Fatalf("official limits moved: rps=%v p99=%v rounds=%d window=%s warmup=%s", rpsDropLimit, p99RiseLimit, gateRounds, gateWindow, gateWarmup)
	}
	pass := judgeGate(1, -1, 2, 1, -1, 5, false)
	if eco11OfficialVerdict(gateRounds, false, false, false, pass) != "PASS" {
		t.Fatal("complete clean PASS was refused")
	}
	cases := []struct {
		rounds  int
		dirty   bool
		killed  bool
		drifted bool
		stat    string
		want    string
	}{
		{19, false, false, false, "PASS", "INCOMPLETE"},
		{20, true, false, false, "PASS", "INCOMPLETE"},
		{20, false, true, false, "PASS", "INCOMPLETE"},
		{20, false, false, true, "PASS", "INCOMPLETE"},
		{20, false, false, false, "INCONCLUSIVE", "INCONCLUSIVE"},
		{20, false, false, false, "FAIL", "FAIL"},
	}
	for _, tc := range cases {
		if got := eco11OfficialVerdict(tc.rounds, tc.dirty, tc.killed, tc.drifted, tc.stat); got != tc.want {
			t.Fatalf("%+v -> %s", tc, got)
		}
	}
	if eco11Fold([]string{"PASS", "INCONCLUSIVE"}) != "INCONCLUSIVE" {
		t.Fatal("fold kept PASS over an open interval")
	}
	if eco11Fold([]string{"INCONCLUSIVE", "FAIL"}) != "FAIL" {
		t.Fatal("fold kept INCONCLUSIVE over FAIL")
	}
	if eco11Fold([]string{"FAIL", "INCOMPLETE"}) != "INCOMPLETE" {
		t.Fatal("fold treated a partial run as a finished FAIL")
	}
}

func eco11OfficialVerdict(rounds int, dirty, killed, drifted bool, statistical string) string {
	if rounds != gateRounds || dirty || killed || drifted {
		return "INCOMPLETE"
	}
	switch statistical {
	case "PASS", "FAIL", "INCONCLUSIVE":
		return statistical
	default:
		return "INCOMPLETE"
	}
}

func eco11Fold(verdicts []string) string {
	rank := map[string]int{"PASS": 0, "INCONCLUSIVE": 1, "FAIL": 2, "INCOMPLETE": 3}
	best := "PASS"
	for _, verdict := range verdicts {
		if rank[verdict] > rank[best] {
			best = verdict
		}
	}
	if best == "" {
		return "INCOMPLETE"
	}
	return best
}

type eco11Plan struct {
	Name      string
	GuardMode string
	HTTP      string
	BaseBin   string
	CandBin   string
}

type eco11Run struct {
	Comparison    string        `json:"comparison"`
	Role          string        `json:"role"`
	Order         string        `json:"order"`
	Strategy      string        `json:"strategy"`
	GuardMode     string        `json:"guard_mode"`
	BinarySHA256  string        `json:"binary_sha256"`
	RPS           float64       `json:"rps"`
	P50           time.Duration `json:"p50"`
	P95           time.Duration `json:"p95"`
	P99           time.Duration `json:"p99"`
	Status200     int           `json:"http_200"`
	Status502     int           `json:"http_502"`
	Status503     int           `json:"http_503"`
	Status504     int           `json:"http_504"`
	StatusOther   int           `json:"http_other"`
	HTTPRunning   int           `json:"http_running"`
	HTTPRequested int           `json:"http_requested"`
	HTTPAdmitted  int           `json:"http_admitted"`
	WorkRunning   int           `json:"work_running"`
	PHPRSSKB      uint64        `json:"php_rss_kb"`
	PHPCPU        float64       `json:"php_cpu_pct"`
	ChildN        int           `json:"php_processes"`
	SelfRSSKB     uint64        `json:"supervisor_rss_kb"`
	SelfCPU       float64       `json:"supervisor_cpu_pct"`
	Threads       int           `json:"supervisor_os_threads"`
	SupervisorPID int           `json:"supervisor_pid"`
	Window        time.Duration `json:"window"`
	StopError     string        `json:"stop_error,omitempty"`
	Killed        bool          `json:"killed"`
	Short         bool          `json:"short_window"`
	Drifted       bool          `json:"worker_count_drift"`
}

type eco11Pair struct {
	Round int      `json:"round"`
	Order string   `json:"order"`
	Base  eco11Run `json:"base"`
	Cand  eco11Run `json:"candidate"`
}

func runECO11OfficialGate(t *testing.T) {
	t.Helper()
	if os.Getenv("EREGION_ECO11_ROUNDS") != "" || os.Getenv("EREGION_ECO11_WINDOW") != "" {
		t.Fatal("refusing to shrink the official 20 x 60s sample")
	}
	root := mithrilRoot(t)
	refuseMntC(t, root)
	worker, app, autoload := benchPaths(t, root)
	phpBin := requirePHP(t)
	baseline, baselineHash := buildRevision(t, eco11BaselineRev)
	candidate := buildEregion(t, "")
	candidateHash := fileSHA256(t, candidate)
	backlog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"backlog":0}`)
	}))
	defer backlog.Close()

	outDir := filepath.Join(eregionRoot(t), "docs", "eco-11", "results")
	method := eco11Method(phpBin, baselineHash, candidateHash)
	writeJSON(t, filepath.Join(outDir, "eco11-performance-method.json"), method)

	plans := []eco11Plan{
		{Name: "legacy_observe", GuardMode: "observe", HTTP: "fixed", BaseBin: baseline, CandBin: candidate},
		{Name: "scheduled_overhead", GuardMode: "enforce", HTTP: "scheduled", BaseBin: candidate, CandBin: candidate},
		{Name: "cpu_minmax_overhead", GuardMode: "enforce", HTTP: "cpu_utilization", BaseBin: candidate, CandBin: candidate},
	}
	reports := make([]map[string]any, 0, len(plans))
	verdicts := make([]string, 0, len(plans))
	for _, plan := range plans {
		report := runEco11Comparison(t, plan, phpBin, worker, app, autoload, backlog.URL, baselineHash, candidateHash, outDir)
		reports = append(reports, report)
		verdict, _ := report["verdict"].(string)
		verdicts = append(verdicts, verdict)
		t.Logf("%s verdict %s", plan.Name, verdict)
	}
	overall := eco11Fold(verdicts)
	final := map[string]any{
		"method":      method,
		"verdict":     overall,
		"limits":      map[string]any{"rps_drop_median_and_ci95_upper_pct": rpsDropLimit, "p99_rise_median_and_ci95_upper_pct": p99RiseLimit, "samples_removed": 0},
		"comparisons": reports,
	}
	finalPath := filepath.Join(outDir, "eco11-performance.json")
	writeJSON(t, finalPath, final)
	sum := fileSHA256(t, finalPath)
	if err := os.WriteFile(finalPath+".sha256", []byte(sum+"  eco11-performance.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("verdict %s wrote %s", overall, finalPath)
	if overall != "PASS" {
		t.Errorf("ECO-11 official gate %s; a partial or open result is not PASS", overall)
	}
}

func runEco11Comparison(t *testing.T, plan eco11Plan, phpBin, worker, app, autoload, backlogURL, baselineHash, candidateHash, outDir string) map[string]any {
	t.Helper()
	pairs := make([]eco11Pair, 0, gateRounds)
	dirty, killed, drifted := false, false, false
	for round := 0; round < gateRounds; round++ {
		order := "AB"
		firstRole, secondRole := "base", "candidate"
		firstBin, secondBin := plan.BaseBin, plan.CandBin
		firstHash, secondHash := baselineHash, candidateHash
		firstStrategy, secondStrategy := "fixed", plan.HTTP
		if plan.Name == "legacy_observe" {
			firstStrategy, secondStrategy = "fixed", "fixed"
		}
		if plan.BaseBin == plan.CandBin {
			firstHash, secondHash = candidateHash, candidateHash
		}
		if round%2 == 1 {
			order = "BA"
			firstRole, secondRole = secondRole, firstRole
			firstBin, secondBin = secondBin, firstBin
			firstHash, secondHash = secondHash, firstHash
			firstStrategy, secondStrategy = secondStrategy, firstStrategy
		}
		first := measureEco11(t, firstBin, firstHash, firstRole, firstStrategy, order, plan, phpBin, worker, app, autoload, backlogURL)
		second := measureEco11(t, secondBin, secondHash, secondRole, secondStrategy, order, plan, phpBin, worker, app, autoload, backlogURL)
		pair := eco11Pair{Round: round + 1, Order: order}
		if first.Role == "base" {
			pair.Base, pair.Cand = first, second
		} else {
			pair.Base, pair.Cand = second, first
		}
		for _, run := range []eco11Run{pair.Base, pair.Cand} {
			if run.Short || run.StatusOther > 0 {
				dirty = true
			}
			if run.Killed {
				killed = true
			}
			if run.Drifted {
				drifted = true
			}
		}
		pairs = append(pairs, pair)
		t.Logf("%s round %d %s base rps=%.1f p95=%s p99=%s cand rps=%.1f p95=%s p99=%s req=%d/%d adm=%d/%d 5xx=%d other=%d",
			plan.Name, round+1, order,
			pair.Base.RPS, pair.Base.P95, pair.Base.P99,
			pair.Cand.RPS, pair.Cand.P95, pair.Cand.P99,
			pair.Base.HTTPRequested, pair.Cand.HTTPRequested,
			pair.Base.HTTPAdmitted, pair.Cand.HTTPAdmitted,
			pair.Base.Status502+pair.Base.Status503+pair.Base.Status504+pair.Cand.Status502+pair.Cand.Status503+pair.Cand.Status504,
			pair.Base.StatusOther+pair.Cand.StatusOther)
		writeJSON(t, filepath.Join(outDir, "eco11-performance-progress.json"), map[string]any{
			"comparison": plan.Name,
			"pairs":      pairs,
			"complete":   false,
		})
	}
	drops := make([]float64, len(pairs))
	rises := make([]float64, len(pairs))
	bad := false
	for i, pair := range pairs {
		drops[i] = percentDrop(pair.Base.RPS, pair.Cand.RPS)
		rises[i] = percentRise(float64(pair.Base.P99), float64(pair.Cand.P99))
		if pair.Base.Status502+pair.Base.Status503+pair.Base.Status504+pair.Cand.Status502+pair.Cand.Status503+pair.Cand.Status504 > 0 {
			bad = true
		}
	}
	dropMean, dropLo, dropHi := studentInterval(drops, studentT19)
	riseMean, riseLo, riseHi := studentInterval(rises, studentT19)
	statistical := judgeGate(medianFloat(drops), dropLo, dropHi, medianFloat(rises), riseLo, riseHi, bad)
	verdict := eco11OfficialVerdict(len(pairs), dirty, killed, drifted, statistical)
	return map[string]any{
		"name":               plan.Name,
		"guard_mode":         plan.GuardMode,
		"base_strategy":      "fixed",
		"candidate_strategy": plan.HTTP,
		"rounds":             len(pairs),
		"order_balance":      "10 AB / 10 BA",
		"samples_removed":    0,
		"verdict":            verdict,
		"statistical":        statistical,
		"rps_drop_series":    drops,
		"rps_drop_median":    medianFloat(drops),
		"rps_drop_mean":      dropMean,
		"rps_drop_ci95":      []float64{dropLo, dropHi},
		"p99_rise_series":    rises,
		"p99_rise_median":    medianFloat(rises),
		"p99_rise_mean":      riseMean,
		"p99_rise_ci95":      []float64{riseLo, riseHi},
		"unsaturated_5xx":    bad,
		"short_or_other":     dirty,
		"sigkill":            killed,
		"worker_drift":       drifted,
		"pairs":              pairs,
	}
}

func measureEco11(t *testing.T, bin, hash, role, strategy, order string, plan eco11Plan, phpBin, worker, app, autoload, backlogURL string) eco11Run {
	t.Helper()
	proc, base, cleanup := startEco11(t, bin, phpBin, worker, app, autoload, backlogURL, plan.GuardMode, strategy)
	var stopErr error
	defer func() { _ = cleanup(&stopErr) }()
	if err := waitEco11Ready(base + metricsPath); err != nil {
		t.Fatalf("%s %s did not become ready: %v", plan.Name, role, err)
	}
	if gateWarmup > 0 {
		_ = issueFor(base, eco11Clients, gateWarmup)
	}
	before := sampleSupervisor(proc.pid())
	window := issueFor(base, eco11Clients, gateWindow)
	after := sampleSupervisor(proc.pid())
	metrics := scrape(t, base+metricsPath)
	run := eco11Run{
		Comparison:    plan.Name,
		Role:          role,
		Order:         order,
		Strategy:      strategy,
		GuardMode:     plan.GuardMode,
		BinarySHA256:  hash,
		RPS:           window.rps,
		P50:           window.p50,
		P95:           window.p95,
		P99:           window.p99,
		Status200:     window.status200,
		Status502:     window.status502,
		Status503:     window.status503,
		Status504:     window.status504,
		StatusOther:   window.statusOther,
		Window:        window.elapsed,
		SupervisorPID: proc.pid(),
		Threads:       after.Threads,
		Short:         window.elapsed < gateWindow-time.Second,
	}
	run.HTTPRunning = metricInt(metrics, "eregion_workload_running_workers", "http")
	run.WorkRunning = metricInt(metrics, "eregion_workload_running_workers", "work")
	run.HTTPRequested = metricInt(metrics, "eregion_scaling_requested_workers", "http")
	run.HTTPAdmitted = metricInt(metrics, "eregion_scaling_admitted_workers", "http")
	run.SelfRSSKB = after.RSSKB
	if before.RSSKB > run.SelfRSSKB {
		run.SelfRSSKB = before.RSSKB
	}
	run.PHPRSSKB = after.ChildRSSKB
	if before.ChildRSSKB > run.PHPRSSKB {
		run.PHPRSSKB = before.ChildRSSKB
	}
	run.ChildN = after.ChildN
	elapsed := window.elapsed.Seconds()
	hz := clockTicks()
	if elapsed > 0 && hz > 0 && after.Ticks >= before.Ticks {
		run.SelfCPU = float64(after.Ticks-before.Ticks) / hz / elapsed * 100
	}
	if elapsed > 0 && hz > 0 && after.ChildTicks >= before.ChildTicks {
		run.PHPCPU = float64(after.ChildTicks-before.ChildTicks) / hz / elapsed * 100
	}
	if run.HTTPRunning != eco11HTTPWorkers || run.WorkRunning != eco11WorkWorkers || run.HTTPAdmitted != eco11HTTPWorkers {
		run.Drifted = true
	}
	if err := cleanup(&stopErr); err != nil {
		run.StopError = err.Error()
	}
	cleanup = func(*error) error { return nil }
	if stopErr != nil && strings.Contains(stopErr.Error(), "SIGKILL") {
		run.Killed = true
		run.StopError = stopErr.Error()
	}
	return run
}

func startEco11(t *testing.T, bin, phpBin, worker, app, autoload, backlogURL, mode, strategy string) (*guardProc, string, func(*error) error) {
	t.Helper()
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "bootstrap.php")
	if err := os.WriteFile(bootstrap, []byte(fmt.Sprintf("<?php\nrequire %q;\nrequire %q;\n", autoload, app)), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	rawManifest := fmt.Sprintf("{\n  \"application\": \"Bench\\\\Http\\\\App\",\n  \"autoload\": %q,\n  \"workingDirectory\": %q\n}\n", filepath.ToSlash(bootstrap), filepath.ToSlash(dir))
	if err := os.WriteFile(manifest, []byte(rawManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "work.php")
	workPHP := "<?php\ndeclare(strict_types=1);\nwhile (true) { usleep(200000); }\n"
	if err := os.WriteFile(script, []byte(workPHP), 0o644); err != nil {
		t.Fatal(err)
	}
	port := pickTCPPort(t)
	cfgPath := filepath.Join(dir, "eregion.yaml")
	if err := os.WriteFile(cfgPath, []byte(eco11YAML(dir, phpBin, worker, script, mode, strategy, port)), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := startGuardProc(bin, []string{"serve", "--config", cfgPath, "--manifest", manifest}, []string{"EREGION_BENCH_BACKLOG_URL=" + backlogURL}, logFile)
	if err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var stopped bool
	cleanup := func(dest *error) error {
		if stopped {
			return nil
		}
		stopped = true
		err := proc.stop()
		logFile.Close()
		if dest != nil {
			*dest = err
		}
		if err != nil && !strings.Contains(err.Error(), "signal:") && !strings.Contains(err.Error(), "SIGKILL") {
			t.Logf("stop pid %d: %v\n%s", proc.pid(), err, tailFile(logPath))
		}
		return err
	}
	if err := waitListening(base+livenessPath, 20*time.Second); err != nil {
		_ = cleanup(nil)
		t.Fatalf("serve did not listen: %v\n%s", err, tailFile(logPath))
	}
	return proc, base, cleanup
}

func waitEco11Ready(metricsURL string) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		body, err := scrapeOK(metricsURL)
		if err != nil {
			last = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if metricInt(body, "eregion_workload_running_workers", "http") >= eco11HTTPWorkers &&
			metricInt(body, "eregion_workload_running_workers", "work") >= eco11WorkWorkers {
			return nil
		}
		last = fmt.Errorf("http=%d work=%d", metricInt(body, "eregion_workload_running_workers", "http"), metricInt(body, "eregion_workload_running_workers", "work"))
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func eco11YAML(dir, phpBin, worker, script, mode, strategy string, port int) string {
	httpScaling := "      strategy: fixed\n"
	switch strategy {
	case "scheduled":
		httpScaling = `      strategy: scheduled
      control:
        window: 10s
        max_signal_age: 30s
        up_confirmations: 2
        down_confirmations: 3
        max_step_up: 1
        max_step_down: 1
      scheduled:
        timezone: UTC
        default_workers: 2
`
	case "cpu_utilization":
		httpScaling = `      strategy: cpu_utilization
      control:
        window: 10s
        max_signal_age: 30s
        up_confirmations: 2
        down_confirmations: 3
        max_step_up: 1
        max_step_down: 1
      cpu:
        target_ratio: 0.70
        tolerance_ratio: 0.05
`
	}
	return fmt.Sprintf(`version: "1"
server:
  host: 127.0.0.1
  port: %d
  shutdown_timeout: 3s
php:
  binary: %s
  worker_script: %s
  working_directory: %s
  environment:
    BENCH_DB_MICROS: "200"
workers:
  count: %d
  min_ready: 1
  max_requests: 0
  shutdown_timeout: 2s
socket:
  directory: %s
queue:
  capacity: 256
logging:
  level: error
  format: text
  access_log: false
resource_guard:
  mode: %s
  memory:
    reserve_mb: 256
    hard_limit_mb: 8192
    reserve_percent: 0
    worker_fallback_mb: 64
workloads:
  http:
    mode: http
    workers:
      min: %d
      max: %d
    resources:
      class: io
      memory_mb: 64
    scaling:
%s  work:
    mode: consumer
    command: [%s, %s]
    workers:
      min: %d
      max: %d
    resources:
      class: io
      memory_mb: 64
    scaling:
      strategy: backlog
      avg_job_duration: 50ms
      target_drain_time: 5s
      backlog_source:
        type: http_json
        url_env: EREGION_BENCH_BACKLOG_URL
        timeout: 500ms
        max_staleness: 30s
`, port, strconv.Quote(phpBin), strconv.Quote(worker), strconv.Quote(dir),
		eco11HTTPWorkers, strconv.Quote(filepath.Join(dir, "s")), mode,
		eco11HTTPWorkers, eco11HTTPWorkers, httpScaling,
		strconv.Quote(phpBin), strconv.Quote(script), eco11WorkWorkers, eco11WorkWorkers)
}

func buildRevision(t *testing.T, rev string) (string, string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "archive", rev)
	cmd.Dir = eregionRoot(t)
	tar := exec.Command("tar", "-x", "-C", src)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tar.Stdin = pipe
	var archiveErr, extractErr strings.Builder
	cmd.Stderr = &archiveErr
	tar.Stderr = &extractErr
	if err := tar.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		_ = tar.Wait()
		t.Fatalf("git archive %s: %v\n%s", rev, err, archiveErr.String())
	}
	if err := tar.Wait(); err != nil {
		t.Fatalf("extract %s: %v\n%s", rev, err, extractErr.String())
	}
	out := filepath.Join(t.TempDir(), "eregion-baseline")
	build := exec.Command("go", "build", "-o", out, "./cmd/eregion")
	build.Dir = src
	raw, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline build: %v\n%s", err, raw)
	}
	return out, fileSHA256(t, out)
}

func eco11Method(phpBin, baselineHash, candidateHash string) map[string]any {
	return map[string]any{
		"protocol":           "20 paired rounds, AB/BA balanced 10/10, 5s warmup after readiness, 60s measured window",
		"load":               "external httptest-independent generator in the test process; eregion serve is a separate process group",
		"application":        "MithrilPHP bench/http/App.php",
		"consumer":           "backlog strategy, http_json depth 0, min=max=1",
		"http_workers":       eco11HTTPWorkers,
		"clients":            eco11Clients,
		"warmup":             gateWarmup.String(),
		"window":             gateWindow.String(),
		"rounds":             gateRounds,
		"student_t_df19":     studentT19,
		"rps_drop_limit_pct": rpsDropLimit,
		"p99_rise_limit_pct": p99RiseLimit,
		"outliers":           "retained; no sample is removed from the official series",
		"goroutines":         "not exported by the supervisor; OS threads from /proc are recorded instead",
		"baseline_revision":  eco11BaselineRev,
		"baseline_sha256":    baselineHash,
		"candidate_sha256":   candidateHash,
		"go":                 commandLine("go", "version"),
		"php":                phpBin + " " + commandLine(phpBin, "-v"),
		"os":                 runtime.GOOS + "/" + runtime.GOARCH + " " + commandLine("uname", "-a"),
		"nproc":              strings.TrimSpace(commandLine("nproc")),
		"mem_total":          memTotal(),
		"clock_utc":          time.Now().UTC().Format(time.RFC3339),
		"command":            "EREGION_ECO11_BENCH=1 go test -count=1 -timeout 6h -run TestECO11PerformanceGate ./tests/integration/",
		"comparisons": []string{
			"legacy_observe: frozen ECO-09 binary versus the working tree, identical fixed HTTP plus backlog consumer, resource_guard observe",
			"scheduled_overhead: candidate fixed versus scheduled default_workers=2, min=max, resource_guard enforce",
			"cpu_minmax_overhead: candidate fixed versus cpu_utilization min=max, resource_guard enforce",
		},
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func commandLine(name string, args ...string) string {
	raw, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(raw)) + " (" + err.Error() + ")"
	}
	line := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line
}

func memTotal() string {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
