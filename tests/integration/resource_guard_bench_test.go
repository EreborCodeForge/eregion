package integration_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

const (
	gateRounds     = 20
	gateWindow     = 60 * time.Second
	gateWarmup     = 5 * time.Second
	studentT19     = 2.093
	rpsDropLimit   = 2.0
	p99RiseLimit   = 5.0
	profileSeconds = 20
	metricsPath    = "/_eregion/metrics"
	livenessPath   = "/_eregion/live"
)

// guardProc is one eregion serve child, independent of the load generator.
type guardProc struct {
	cmd *exec.Cmd
}

// procSnap is the supervisor process and its descendants. Threads are OS
// threads from /proc; goroutine counts are not available there.
type procSnap struct {
	RSSKB      uint64
	Ticks      uint64
	Threads    int
	ChildRSSKB uint64
	ChildTicks uint64
	ChildN     int
}

// TestResourceGuardVerdictRules locks the PASS, FAIL and INCONCLUSIVE rule
// before any measurement is interpreted.
func TestResourceGuardVerdictRules(t *testing.T) {
	if got := judgeGate(1, -1, 2, 1, -1, 5, false); got != "PASS" {
		t.Fatalf("boundary PASS = %s", got)
	}
	if got := judgeGate(1, -1, 2.01, 1, -1, 5, false); got != "INCONCLUSIVE" {
		t.Fatalf("crossing rps = %s", got)
	}
	if got := judgeGate(1, -1, 2, 1, -1, 5.01, false); got != "INCONCLUSIVE" {
		t.Fatalf("crossing p99 = %s", got)
	}
	if got := judgeGate(3, 0, 6, 1, -1, 4, false); got != "INCONCLUSIVE" {
		t.Fatalf("median outside a crossing interval = %s", got)
	}
	if got := judgeGate(3, 2.1, 4, 1, -1, 4, false); got != "FAIL" {
		t.Fatalf("rps interval above the limit = %s", got)
	}
	if got := judgeGate(1, -1, 2, 8, 5.1, 11, false); got != "FAIL" {
		t.Fatalf("p99 interval above the limit = %s", got)
	}
	if got := judgeGate(1, -1, 2, 1, -1, 5, true); got != "FAIL" {
		t.Fatalf("5xx = %s", got)
	}
	median := medianFloat([]float64{1, 2, 3, 4})
	if median != 2.5 {
		t.Fatalf("median = %v", median)
	}
}

// TestResourceGuardAB runs 20 paired windows of 60s. The load generator is
// this test. eregion serve is a separate process group.
func TestResourceGuardAB(t *testing.T) {
	if os.Getenv("EREGION_RESOURCE_GUARD_BENCH") == "" {
		t.Skip("N/A unless EREGION_RESOURCE_GUARD_BENCH=1")
	}
	if runtime.GOOS != "linux" {
		t.Skip("resource_guard A/B is measured on Linux")
	}
	root := mithrilRoot(t)
	refuseMntC(t, root)
	worker, app, autoload := benchPaths(t, root)
	phpBin := requirePHP(t)
	bin := buildEregion(t, "")
	method := gateMethod()
	writeJSON(t, filepath.Join(root, "bench", "results", "resource-guard-ab-method.json"), method)

	modes := []string{config.ResourceGuardOff, config.ResourceGuardObserve, config.ResourceGuardEnforce}
	budget := guardBudget{
		hardLimitMB: 4096,
		reserveMB:   128,
		fallbackMB:  128,
		memoryMB:    128,
		httpMin:     1,
		httpMax:     4,
		workMin:     1,
		workMax:     1,
		queue:       128,
		clients:     8,
		warmup:      gateWarmup,
		window:      gateWindow,
	}
	byMode := map[string][]guardRun{}
	for round := 0; round < gateRounds; round++ {
		for step := 0; step < len(modes); step++ {
			mode := modes[(round+step)%len(modes)]
			run := measureGuard(t, bin, phpBin, worker, app, autoload, mode, budget)
			byMode[mode] = append(byMode[mode], run)
			t.Logf("round %d %s rps=%.1f p50=%s p99=%s supervisor_cpu=%.1f supervisor_rss_kb=%d threads=%d php_rss_kb=%d php_cpu=%.1f php_n=%d s200=%d s502=%d s503=%d s504=%d other=%d",
				round+1, mode, run.RPS, run.P50, run.P99, run.SelfCPU, run.SelfRSSKB, run.Threads, run.PHPRSSKB, run.PHPCPU, run.ChildN, run.Status200, run.Status502, run.Status503, run.Status504, run.StatusOther)
		}
		writeJSON(t, filepath.Join(root, "bench", "results", "resource-guard-ab-progress.json"), byMode)
	}

	report := pairedReport(method, byMode)
	out := filepath.Join(root, "bench", "results", "resource-guard-ab.json")
	writeJSON(t, out, report)
	verdict, _ := report["verdict"].(string)
	t.Logf("verdict %s wrote %s", verdict, out)
	if verdict == "FAIL" {
		t.Errorf("observe gate FAIL: %+v", report["observe_vs_off"])
	}
}

// TestResourceGuardEnforceSaturation checks that enforce stays inside the
// 384 MiB budget: one http worker and one work consumer.
func TestResourceGuardEnforceSaturation(t *testing.T) {
	if os.Getenv("EREGION_RESOURCE_GUARD_BENCH") == "" {
		t.Skip("N/A unless EREGION_RESOURCE_GUARD_BENCH=1")
	}
	if runtime.GOOS != "linux" {
		t.Skip("resource_guard saturation is measured on Linux")
	}
	root := mithrilRoot(t)
	refuseMntC(t, root)
	worker, app, autoload := benchPaths(t, root)
	phpBin := requirePHP(t)
	bin := buildEregion(t, "")
	run := measureGuard(t, bin, phpBin, worker, app, autoload, config.ResourceGuardEnforce, guardBudget{
		hardLimitMB: 384,
		reserveMB:   64,
		fallbackMB:  128,
		memoryMB:    128,
		httpMin:     1,
		httpMax:     4,
		workMin:     1,
		workMax:     1,
		queue:       2,
		clients:     8,
		warmup:      2 * time.Second,
		window:      10 * time.Second,
	})
	t.Logf("saturation http=%d work=%d admitted_http=%d admitted_work=%d limited_http=%d s200=%d s502=%d s503=%d s504=%d other=%d failed=%d",
		run.HTTPRunning, run.WorkRunning, run.HTTPAdmitted, run.WorkAdmitted, run.HTTPLimited, run.Status200, run.Status502, run.Status503, run.Status504, run.StatusOther, run.Failed)
	if run.HTTPRunning > 1 || run.WorkRunning > 1 || run.HTTPAdmitted > 1 || run.WorkAdmitted > 1 {
		t.Fatalf("enforce admitted past the budget: http running=%d admitted=%d work running=%d admitted=%d", run.HTTPRunning, run.HTTPAdmitted, run.WorkRunning, run.WorkAdmitted)
	}
	if run.Status200 == 0 || run.Status503 == 0 {
		t.Fatalf("saturation did not both serve and refuse: 200=%d 503=%d", run.Status200, run.Status503)
	}
	if run.Status502 != 0 || run.Status504 != 0 {
		t.Fatalf("saturation returned unexpected 5xx: 502=%d 504=%d", run.Status502, run.Status504)
	}
	writeJSON(t, filepath.Join(root, "bench", "results", "resource-guard-saturation.json"), run)
}

// TestResourceGuardProfile collects CPU, alloc, goroutine and mutex profiles
// from a pprof-tagged binary. It is not part of the 20 paired rounds.
func TestResourceGuardProfile(t *testing.T) {
	if os.Getenv("EREGION_RESOURCE_GUARD_PROFILE") == "" {
		t.Skip("N/A unless EREGION_RESOURCE_GUARD_PROFILE=1")
	}
	if runtime.GOOS != "linux" {
		t.Skip("resource_guard profiles are collected on Linux")
	}
	root := mithrilRoot(t)
	refuseMntC(t, root)
	worker, app, autoload := benchPaths(t, root)
	phpBin := requirePHP(t)
	bin := buildEregion(t, "pprof")
	dir := filepath.Join(root, "bench", "results", "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	index := map[string]any{
		"binary_tags": "pprof",
		"note":        "separate from the 20 paired gate rounds; the gate binary is built without this tag",
		"load":        "mithril bench/http/App.php plus consumer workload work, hard_limit_mb=4096",
		"profiles":    map[string]string{},
	}
	profiles := index["profiles"].(map[string]string)
	for _, mode := range []string{config.ResourceGuardObserve, config.ResourceGuardOff} {
		collectProfile(t, bin, phpBin, worker, app, autoload, mode, dir, profiles)
	}
	writeJSON(t, filepath.Join(dir, "index.json"), index)
}

func collectProfile(t *testing.T, bin, phpBin, worker, app, autoload, mode, dir string, profiles map[string]string) {
	t.Helper()
	httpPort := pickTCPPort(t)
	pprofPort := pickTCPPort(t)
	for pprofPort == httpPort {
		pprofPort = pickTCPPort(t)
	}
	proc, base, _, cleanup := startGuard(t, bin, phpBin, worker, app, autoload, mode, guardBudget{
		hardLimitMB: 4096,
		reserveMB:   128,
		fallbackMB:  128,
		memoryMB:    128,
		httpMin:     1,
		httpMax:     4,
		workMin:     1,
		workMax:     1,
		queue:       128,
		clients:     8,
	}, httpPort, fmt.Sprintf("EREGION_PPROF=127.0.0.1:%d", pprofPort))
	defer cleanup()
	if err := waitListening(fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/", pprofPort), 5*time.Second); err != nil {
		t.Fatalf("pprof did not listen: %v", err)
	}
	waitWorkloads(t, base+metricsPath, 4, 1, false)
	stopLoad := make(chan struct{})
	loadDone := make(chan struct{})
	go func() {
		driveUntil(base, 8, stopLoad)
		close(loadDone)
	}()
	time.Sleep(2 * time.Second)
	pprofBase := fmt.Sprintf("http://127.0.0.1:%d", pprofPort)
	cpu := filepath.Join(dir, mode+"-cpu.pprof")
	if err := fetchTo(pprofBase+"/debug/pprof/profile?seconds="+strconv.Itoa(profileSeconds), cpu); err != nil {
		t.Fatal(err)
	}
	profiles[mode+"_cpu"] = cpu
	allocs := filepath.Join(dir, mode+"-allocs.pprof")
	if err := fetchTo(pprofBase+"/debug/pprof/allocs", allocs); err != nil {
		t.Fatal(err)
	}
	profiles[mode+"_allocs"] = allocs
	if mode == config.ResourceGuardObserve {
		goroutine := filepath.Join(dir, "observe-goroutine.txt")
		if err := fetchTo(pprofBase+"/debug/pprof/goroutine?debug=1", goroutine); err != nil {
			t.Fatal(err)
		}
		profiles["observe_goroutine"] = goroutine
		raw, err := os.ReadFile(goroutine)
		if err != nil {
			t.Fatal(err)
		}
		profiles["observe_goroutine_total"] = goroutineTotal(string(raw))
		mutex := filepath.Join(dir, "observe-mutex.pprof")
		if err := fetchTo(pprofBase+"/debug/pprof/mutex", mutex); err != nil {
			t.Fatal(err)
		}
		profiles["observe_mutex"] = mutex
		writePprofText(t, bin, mutex, "", filepath.Join(dir, "observe-mutex.top.txt"))
	}
	writePprofText(t, bin, cpu, "", filepath.Join(dir, mode+"-cpu.top.txt"))
	writePprofText(t, bin, allocs, "alloc_space", filepath.Join(dir, mode+"-alloc-space.top.txt"))
	writePprofText(t, bin, allocs, "alloc_objects", filepath.Join(dir, mode+"-alloc-objects.top.txt"))
	close(stopLoad)
	<-loadDone
	_ = proc
}

func writePprofText(t *testing.T, bin, profile, sampleIndex, out string) {
	t.Helper()
	args := []string{"tool", "pprof", "-top", "-cum", "-nodecount=40"}
	if sampleIndex != "" {
		args = append(args, "-sample_index="+sampleIndex)
	}
	args = append(args, bin, profile)
	cmd := exec.Command("go", args...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pprof %s: %v\n%s", profile, err, raw)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(raw))
}

func goroutineTotal(body string) string {
	line, _, _ := strings.Cut(body, "\n")
	return strings.TrimSpace(line)
}

func fetchTo(url, path string) error {
	client := &http.Client{Timeout: time.Duration(profileSeconds+20) * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %s %s", url, resp.Status, raw)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func driveUntil(base string, clients int, stop <-chan struct{}) {
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
			for {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := client.Get(base + "/item")
				if err != nil {
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

func gateMethod() map[string]any {
	return map[string]any{
		"process":              "eregion serve is a separate process group; the test only generates HTTP against /item",
		"workload":             "mithril bench/http/App.php plus consumer workload work (SHA-256 and append)",
		"window":               "60s after an equal 5s warmup, after running workers reach the target",
		"rounds":               gateRounds,
		"order":                "balanced rotation; round 0 starts off, observe, enforce, then the start advances",
		"pair":                 "round i compares observe with off from the same round; enforce keeps the rotation symmetric",
		"rps_drop":             "(rps_off - rps_observe) / rps_off * 100",
		"p99_rise":             "(p99_observe - p99_off) / p99_off * 100",
		"estimators":           "median of the 20 paired differences, mean, and Student t 95% interval with 19 degrees of freedom (t=2.093)",
		"sample_interval":      "default 1s, unchanged",
		"hard_limit_mb":        4096,
		"cpu_rss":              "supervisor CPU, RSS and OS threads from /proc/<pid> of the child; PHP RSS and CPU from that pid's descendants only",
		"goroutines":           "/proc exposes OS threads, not goroutines; goroutine, CPU, alloc and mutex profiles come from a separate pprof-tagged run",
		"verdict_pass":         "median and upper 95% bound of rps_drop <= 2; median and upper 95% bound of p99_rise <= 5; zero 5xx in unsaturated runs",
		"verdict_fail":         "the entire interval lies above the limit, or any unsaturated 5xx",
		"verdict_inconclusive": "the interval crosses the limit; that result is not PASS",
	}
}

func pairedReport(method map[string]any, byMode map[string][]guardRun) map[string]any {
	offRuns := byMode[config.ResourceGuardOff]
	obsRuns := byMode[config.ResourceGuardObserve]
	n := len(offRuns)
	if len(obsRuns) < n {
		n = len(obsRuns)
	}
	drops := make([]float64, 0, n)
	rises := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		drops = append(drops, percentDrop(offRuns[i].RPS, obsRuns[i].RPS))
		rises = append(rises, percentRise(float64(offRuns[i].P99), float64(obsRuns[i].P99)))
	}
	dropMean, dropLo, dropHi := studentInterval(drops, studentT19)
	riseMean, riseLo, riseHi := studentInterval(rises, studentT19)
	dropMed := medianFloat(drops)
	riseMed := medianFloat(rises)
	bad := unsaturated5xx(byMode)
	verdict := judgeGate(dropMed, dropLo, dropHi, riseMed, riseLo, riseHi, bad)
	return map[string]any{
		"method":  method,
		"verdict": verdict,
		"observe_vs_off": map[string]any{
			"rps_drop_series": drops,
			"rps_drop_median": dropMed,
			"rps_drop_mean":   dropMean,
			"rps_drop_ci95":   []float64{dropLo, dropHi},
			"p99_rise_series": rises,
			"p99_rise_median": riseMed,
			"p99_rise_mean":   riseMean,
			"p99_rise_ci95":   []float64{riseLo, riseHi},
			"unsaturated_5xx": bad,
		},
		"runs": byMode,
	}
}

func judgeGate(dropMed, dropLo, dropHi, riseMed, riseLo, riseHi float64, bad5xx bool) string {
	if bad5xx || dropLo > rpsDropLimit || riseLo > p99RiseLimit {
		return "FAIL"
	}
	if dropMed <= rpsDropLimit && dropHi <= rpsDropLimit && riseMed <= p99RiseLimit && riseHi <= p99RiseLimit {
		return "PASS"
	}
	return "INCONCLUSIVE"
}

func unsaturated5xx(byMode map[string][]guardRun) bool {
	for _, runs := range byMode {
		for _, run := range runs {
			if run.Status502+run.Status503+run.Status504 > 0 {
				return true
			}
		}
	}
	return false
}

func studentInterval(values []float64, tCrit float64) (mean, lo, hi float64) {
	n := len(values)
	if n == 0 {
		return 0, 0, 0
	}
	for _, value := range values {
		mean += value
	}
	mean /= float64(n)
	if n < 2 {
		return mean, mean, mean
	}
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	std := math.Sqrt(sum / float64(n-1))
	half := tCrit * std / math.Sqrt(float64(n))
	return mean, mean - half, mean + half
}

func percentDrop(base, next float64) float64 {
	if base == 0 {
		return 0
	}
	return (base - next) / base * 100
}

func percentRise(base, next float64) float64 {
	if base == 0 {
		return 0
	}
	return (next - base) / base * 100
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

type guardBudget struct {
	hardLimitMB int
	reserveMB   int
	fallbackMB  int
	memoryMB    int
	httpMin     int
	httpMax     int
	workMin     int
	workMax     int
	queue       int
	clients     int
	warmup      time.Duration
	window      time.Duration
}

type guardRun struct {
	Mode          string        `json:"mode"`
	RPS           float64       `json:"rps"`
	P50           time.Duration `json:"p50"`
	P95           time.Duration `json:"p95"`
	P99           time.Duration `json:"p99"`
	HTTPRunning   int           `json:"http_running"`
	WorkRunning   int           `json:"work_running"`
	HTTPAdmitted  int           `json:"http_admitted"`
	WorkAdmitted  int           `json:"work_admitted"`
	HTTPLimited   int           `json:"http_limited"`
	WorkLimited   int           `json:"work_limited"`
	PHPRSSKB      uint64        `json:"php_rss_kb"`
	PHPCPU        float64       `json:"php_cpu_pct"`
	ChildN        int           `json:"php_processes"`
	SelfRSSKB     uint64        `json:"supervisor_rss_kb"`
	SelfCPU       float64       `json:"supervisor_cpu_pct"`
	Threads       int           `json:"supervisor_os_threads"`
	SupervisorPID int           `json:"supervisor_pid"`
	ScaleEvents   int           `json:"scale_events"`
	Restarts      int           `json:"restarts"`
	Failed        int           `json:"failed_workers"`
	Status200     int           `json:"http_200"`
	Status502     int           `json:"http_502"`
	Status503     int           `json:"http_503"`
	Status504     int           `json:"http_504"`
	StatusOther   int           `json:"http_other"`
	Window        time.Duration `json:"window"`
}

func measureGuard(t *testing.T, bin, phpBin, worker, app, autoload, mode string, budget guardBudget) guardRun {
	t.Helper()
	proc, base, logPath, cleanup := startGuard(t, bin, phpBin, worker, app, autoload, mode, budget, 0)
	defer cleanup()
	tight := budget.hardLimitMB < 1024
	waitWorkloads(t, base+metricsPath, budget.httpMax, budget.workMax, tight)
	if budget.warmup > 0 {
		_ = issueFor(base, budget.clients, budget.warmup)
	}
	before := sampleSupervisor(proc.pid())
	window := issueFor(base, budget.clients, budget.window)
	after := sampleSupervisor(proc.pid())
	metrics := scrape(t, base+metricsPath)
	run := guardRun{
		Mode:          mode,
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
	}
	run.HTTPRunning = metricInt(metrics, "eregion_workload_running_workers", "http")
	run.WorkRunning = metricInt(metrics, "eregion_workload_running_workers", "work")
	run.HTTPAdmitted = metricInt(metrics, "eregion_scaling_admitted_workers", "http")
	run.WorkAdmitted = metricInt(metrics, "eregion_scaling_admitted_workers", "work")
	run.HTTPLimited = metricInt(metrics, "eregion_scaling_limited_workers", "http")
	run.WorkLimited = metricInt(metrics, "eregion_scaling_limited_workers", "work")
	run.Failed = metricInt(metrics, "eregion_workload_failed_workers", "http") + metricInt(metrics, "eregion_workload_failed_workers", "work")
	run.Restarts = metricInt(metrics, "eregion_workload_restarts_total", "http") + metricInt(metrics, "eregion_workload_restarts_total", "work")
	run.ScaleEvents = sumMatching(metrics, "eregion_workload_scale_events_total")
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
	if !tight && (run.Status502+run.Status503+run.Status504+run.StatusOther) > 0 {
		t.Errorf("%s unsaturated window was not clean: 502=%d 503=%d 504=%d other=%d", mode, run.Status502, run.Status503, run.Status504, run.StatusOther)
	}
	if tight {
		for _, line := range strings.Split(metrics, "\n") {
			if strings.Contains(line, "eregion_resource_") || strings.Contains(line, "eregion_scaling_") || strings.Contains(line, "running_workers") {
				t.Log(line)
			}
		}
		t.Logf("serve log:\n%s", tailFile(logPath))
	}
	return run
}

type windowResult struct {
	rps         float64
	p50         time.Duration
	p95         time.Duration
	p99         time.Duration
	elapsed     time.Duration
	status200   int
	status502   int
	status503   int
	status504   int
	statusOther int
}

func issueFor(base string, clients int, window time.Duration) windowResult {
	type tally struct {
		lat                    []time.Duration
		s200, s502, s503, s504 int
		other                  int
	}
	tallies := make([]tally, clients)
	var wg sync.WaitGroup
	start := time.Now()
	deadline := start.Add(window)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
			for time.Now().Before(deadline) {
				one := time.Now()
				resp, err := client.Get(base + "/item")
				if err != nil {
					tallies[i].other++
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				switch resp.StatusCode {
				case 200:
					tallies[i].s200++
					tallies[i].lat = append(tallies[i].lat, time.Since(one))
				case 502:
					tallies[i].s502++
				case 503:
					tallies[i].s503++
				case 504:
					tallies[i].s504++
				default:
					tallies[i].other++
				}
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	var result windowResult
	result.elapsed = elapsed
	var good []time.Duration
	for _, tally := range tallies {
		result.status200 += tally.s200
		result.status502 += tally.s502
		result.status503 += tally.s503
		result.status504 += tally.s504
		result.statusOther += tally.other
		good = append(good, tally.lat...)
	}
	if result.status200 > 0 && elapsed > 0 {
		result.rps = float64(result.status200) / elapsed.Seconds()
		sort.Slice(good, func(i, j int) bool { return good[i] < good[j] })
		result.p50 = good[percentile(good, 0.50)]
		result.p95 = good[percentile(good, 0.95)]
		result.p99 = good[percentile(good, 0.99)]
	}
	return result
}

func startGuard(t *testing.T, bin, phpBin, worker, app, autoload, mode string, budget guardBudget, listenPort int, extraEnv ...string) (*guardProc, string, string, func()) {
	t.Helper()
	dir := t.TempDir()
	bootstrap := filepath.Join(dir, "bootstrap.php")
	body := fmt.Sprintf("<?php\nrequire %q;\nrequire %q;\n", autoload, app)
	if err := os.WriteFile(bootstrap, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.json")
	rawManifest := fmt.Sprintf("{\n  \"application\": \"Bench\\\\Http\\\\App\",\n  \"autoload\": %q,\n  \"workingDirectory\": %q\n}\n", filepath.ToSlash(bootstrap), filepath.ToSlash(dir))
	if err := os.WriteFile(manifest, []byte(rawManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := filepath.Join(dir, "work-sink")
	script := filepath.Join(dir, "work.php")
	// The 200 MiB hold makes the 384 MiB ceiling bind after the ledger absorbs the
	// initial workers. Configured memory_mb is not charged again once a worker is
	// running, so a small RSS would reopen headroom and admit past the budget.
	workPHP := "<?php\ndeclare(strict_types=1);\n$sink = getenv('WORK_SINK') ?: '/tmp/work-sink';\n$hold = [];\nfor ($i = 0; $i < 200; $i++) { $hold[] = random_bytes(1048576); }\nwhile (true) {\n    $buf = random_bytes(65536);\n    hash('sha256', $buf, true);\n    $hold[array_key_last($hold)][0] = $buf[0];\n    file_put_contents($sink, bin2hex(substr($buf, 0, 64)).\"\\n\", FILE_APPEND);\n    clearstatcache(true, $sink);\n    if (filesize($sink) > 8388608) { file_put_contents($sink, ''); }\n    usleep(20000);\n}\n"
	if err := os.WriteFile(script, []byte(workPHP), 0o644); err != nil {
		t.Fatal(err)
	}
	port := listenPort
	if port == 0 {
		port = pickTCPPort(t)
	}
	cfgPath := filepath.Join(dir, "eregion.yaml")
	if err := os.WriteFile(cfgPath, []byte(guardYAML(dir, phpBin, worker, script, sink, mode, port, budget)), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	proc, err := startGuardProc(bin, []string{"serve", "--config", cfgPath, "--manifest", manifest}, extraEnv, logFile)
	if err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cleanup := func() {
		err := proc.stop()
		logFile.Close()
		if err != nil && !strings.Contains(err.Error(), "signal:") {
			t.Logf("stop %s pid %d: %v\n%s", mode, proc.pid(), err, tailFile(logPath))
		}
	}
	if err := waitListening(base+livenessPath, 20*time.Second); err != nil {
		cleanup()
		t.Fatalf("serve did not listen: %v\n%s", err, tailFile(logPath))
	}
	return proc, base, logPath, cleanup
}

func logLevel(budget guardBudget) string {
	if budget.hardLimitMB > 0 && budget.hardLimitMB < 1024 {
		return "info"
	}
	return "error"
}

func guardYAML(dir, phpBin, worker, script, sink, mode string, port int, budget guardBudget) string {
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
    WORK_SINK: %s
workers:
  count: %d
  min_ready: 1
  max_requests: 0
  shutdown_timeout: 2s
socket:
  directory: %s
queue:
  capacity: %d
logging:
  level: %s
  format: text
  access_log: false
resource_guard:
  mode: %s
  memory:
    reserve_mb: %d
    hard_limit_mb: %d
    reserve_percent: 0
    worker_fallback_mb: %d
workloads:
  http:
    mode: http
    workers:
      min: %d
      max: %d
    resources:
      class: io
      memory_mb: %d
    scaling:
      strategy: fixed
  work:
    mode: consumer
    command: [%s, %s]
    workers:
      min: %d
      max: %d
    resources:
      class: io
      memory_mb: %d
    scaling:
      strategy: fixed
`, port, strconv.Quote(phpBin), strconv.Quote(worker), strconv.Quote(dir), strconv.Quote(sink),
		budget.httpMax, strconv.Quote(filepath.Join(dir, "sockets")), budget.queue, logLevel(budget), mode,
		budget.reserveMB, budget.hardLimitMB, budget.fallbackMB,
		budget.httpMin, budget.httpMax, budget.memoryMB,
		strconv.Quote(phpBin), strconv.Quote(script),
		budget.workMin, budget.workMax, budget.memoryMB)
}

func waitListening(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

func waitWorkloads(t *testing.T, metricsURL string, httpWant, workWant int, tight bool) {
	t.Helper()
	if tight {
		httpWant, workWant = 1, 1
	}
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		body, err := scrapeOK(metricsURL)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		last = body
		if metricInt(body, "eregion_workload_running_workers", "http") >= httpWant &&
			metricInt(body, "eregion_workload_running_workers", "work") >= workWant {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("workloads did not reach http=%d work=%d\n%s", httpWant, workWant, last)
}

func buildEregion(t *testing.T, tags string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "eregion")
	args := []string{"build", "-o", out}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./cmd/eregion")
	cmd := exec.Command("go", args...)
	cmd.Dir = eregionRoot(t)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build -tags %q: %v\n%s", tags, err, raw)
	}
	return out
}

func eregionRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(filepath.Dir(mustSelf(t)), "..", ".."))
}

func refuseMntC(t *testing.T, root string) {
	t.Helper()
	if strings.Contains(root, "/mnt/c/") || strings.Contains(mustSelf(t), "/mnt/c/") || strings.Contains(eregionRoot(t), "/mnt/c/") {
		t.Fatal("repositories must sit on the Linux filesystem, not /mnt/c")
	}
}

func benchPaths(t *testing.T, root string) (string, string, string) {
	t.Helper()
	worker := filepath.Join(root, "bin", "eregion-worker")
	app := filepath.Join(root, "bench", "http", "App.php")
	autoload := filepath.Join(root, "vendor", "autoload.php")
	for _, path := range []string{worker, app, autoload} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	return worker, app, autoload
}

func scrape(t *testing.T, url string) string {
	t.Helper()
	body, err := scrapeOK(url)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func scrapeOK(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func metricInt(body, name, workload string) int {
	needle := name + "{workload=\"" + workload + "\""
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, needle) {
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

func sumMatching(body, name string) int {
	sum := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.Atoi(fields[len(fields)-1])
		sum += v
	}
	return sum
}

func tailFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	if len(raw) > 4000 {
		raw = raw[len(raw)-4000:]
	}
	return string(raw)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
