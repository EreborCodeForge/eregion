package integration_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/server"
)

func fixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "fixtures")
}

func requirePHP(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not found")
	}
	return path
}

func pickTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testConfig(t *testing.T, workerScript string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.ShutdownTimeout = 5 * time.Second
	cfg.PHP.Binary = requirePHP(t)
	abs, err := filepath.Abs(workerScript)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PHP.WorkerScript = abs
	cfg.PHP.WorkingDirectory = filepath.Dir(abs)
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.StartupTimeout = 8 * time.Second
	cfg.Workers.HandshakeTimeout = 5 * time.Second
	cfg.Workers.RequestTimeout = 5 * time.Second
	cfg.Workers.AcquireTimeout = 3 * time.Second
	cfg.Workers.ShutdownTimeout = 2 * time.Second
	cfg.Workers.MaxRequests = 0
	cfg.Queue.Capacity = 8
	cfg.Socket.Directory = filepath.Join(t.TempDir(), "socks")
	cfg.Logging.Level = "error"
	cfg.Logging.Format = "text"
	cfg.Logging.AccessLog = false
	return cfg
}

func startServer(t *testing.T, cfg config.Config) string {
	t.Helper()
	cfg.Server.Port = pickTCPPort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := server.New(cfg, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	baseURL := "http://" + cfg.Addr()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + cfg.Liveness.Path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				resp2, err2 := http.Get(baseURL + cfg.Readiness.Path)
				if err2 == nil {
					resp2.Body.Close()
					if resp2.StatusCode == 200 {
						t.Cleanup(func() {
							cancel()
							select {
							case <-errCh:
							case <-time.After(10 * time.Second):
							}
						})
						return baseURL
					}
				}
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("server exited early: %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	t.Fatal("server not ready")
	return ""
}

func TestHealthyGET(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("ok:GET:/hello")) {
		t.Fatalf("body %q", body)
	}
}

func TestBinaryEcho(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	base := startServer(t, cfg)
	payload := []byte{0, 1, 2, 255, 10}
	resp, err := http.Post(base+"/echo", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, payload) {
		t.Fatalf("got %v", body)
	}
}

func TestRepeatedHeaders(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	base := startServer(t, cfg)
	req, _ := http.NewRequest(http.MethodGet, base+"/headers", nil)
	req.Header.Add("X-Multi", "one")
	req.Header.Add("X-Multi", "two")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cookies := resp.Header["Set-Cookie"]
	if len(cookies) < 2 {
		t.Fatalf("set-cookie %v", cookies)
	}
}

func TestWorkerTimeout(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "slow-worker.php"))
	cfg.Workers.RequestTimeout = 300 * time.Millisecond
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/x?delay=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

func TestCrashBusy(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "crashing-worker.php"))
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/crash")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
}

func TestMismatchID(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "mismatch-id-worker.php"))
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMalformed(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "malformed-worker.php"))
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPlannedRecycle(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "recycling-worker.php"))
	base := startServer(t, cfg)
	for i := 0; i < 3; i++ {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("iter %d status %d body %s", i, resp.StatusCode, body)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func TestOpsEndpoints(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	base := startServer(t, cfg)
	for _, path := range []string{cfg.Liveness.Path, cfg.Readiness.Path, cfg.Health.Path, cfg.Metrics.Path} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s => %d", path, resp.StatusCode)
		}
	}
}

func TestQueueFull(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "slow-worker.php"))
	cfg.Workers.Count = 1
	cfg.Workers.RequestTimeout = 5 * time.Second
	cfg.Workers.AcquireTimeout = 200 * time.Millisecond
	cfg.Queue.Capacity = 1
	base := startServer(t, cfg)

	done := make(chan *http.Response, 3)
	for i := 0; i < 3; i++ {
		go func() {
			resp, err := http.Get(base + "/x?delay=2000")
			if err != nil {
				done <- nil
				return
			}
			done <- resp
		}()
	}
	saw503 := false
	for i := 0; i < 3; i++ {
		resp := <-done
		if resp == nil {
			continue
		}
		if resp.StatusCode == http.StatusServiceUnavailable {
			saw503 = true
		}
		resp.Body.Close()
	}
	if !saw503 {
		t.Fatal("expected at least one 503")
	}
}

func TestQueueCapacityZero(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "slow-worker.php"))
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.RequestTimeout = 5 * time.Second
	cfg.Workers.AcquireTimeout = 2 * time.Second
	cfg.Queue.Capacity = 0
	base := startServer(t, cfg)

	started := time.Now()
	done := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			resp, err := http.Get(base + "/x?delay=1500")
			if err != nil {
				done <- 0
				return
			}
			code := resp.StatusCode
			resp.Body.Close()
			done <- code
		}()
	}
	codes := []int{<-done, <-done}
	elapsed := time.Since(started)
	saw200, saw503 := false, false
	for _, c := range codes {
		if c == 200 {
			saw200 = true
		}
		if c == http.StatusServiceUnavailable {
			saw503 = true
		}
	}
	if !saw200 || !saw503 {
		t.Fatalf("codes=%v want one 200 and one 503", codes)
	}
	// Immediate reject should not wait full acquire timeout for the 503.
	if elapsed > 3*time.Second {
		t.Fatalf("capacity=0 reject too slow: %v", elapsed)
	}
}

func TestOpsPrefixRelative(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	cfg.Operations.Prefix = "/_ops"
	cfg.Liveness.Path = "/live"
	cfg.Readiness.Path = "/ready"
	cfg.Health.Path = "/health"
	cfg.Metrics.Path = "/metrics"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Health.Path != "/_ops/health" {
		t.Fatalf("health path = %q", cfg.Health.Path)
	}
	base := startServer(t, cfg)
	resp, err := http.Get(base + "/_ops/live")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("live status %d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("alive")) {
		t.Fatalf("live body %s", body)
	}
	resp, err = http.Get(base + "/_eregion/live")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if bytes.Contains(body, []byte(`"alive"`)) {
		t.Fatal("old prefix should not serve liveness")
	}
}

func TestMetricsDisabled404(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	cfg.Metrics.Enabled = false
	base := startServer(t, cfg)
	resp, err := http.Get(base + cfg.Metrics.Path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestStartupFailure(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "startup-failure-worker.php"))
	cfg.Workers.StartupTimeout = 800 * time.Millisecond
	cfg.Workers.MinReady = 1
	cfg.Workers.RestartLimit = 0 // allow retries? 0 means unlimited - use high min and short timeout
	cfg.Server.Port = pickTCPPort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := server.New(cfg, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = srv.Run(ctx)
	if err == nil {
		t.Fatal("expected startup failure")
	}
}

// Socket paths on Windows are limited to about 108 bytes, so these test names stay short.
func TestWGRecycle(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "guard-recycle-worker.php"))
	base := startServer(t, cfg)

	first := getBody(t, base+"/")
	if first.status != 200 || !bytes.Contains(first.body, []byte("guard-body gen=")) {
		t.Fatalf("first status %d body %s", first.status, first.body)
	}

	var second httpResult
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		second = getBody(t, base+"/")
		if second.status == 200 && !bytes.Equal(second.body, first.body) && bytes.Contains(second.body, []byte("guard-body gen=")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if second.status != 200 || bytes.Equal(second.body, first.body) {
		t.Fatalf("replacement body %s", second.body)
	}

	metrics := getBody(t, base+cfg.Metrics.Path)
	if !bytes.Contains(metrics.body, []byte(`eregion_worker_recycles_total{reason="guard_state_violation"}`)) {
		t.Fatalf("metrics missing guard reason: %s", metrics.body)
	}
	if bytes.Contains(metrics.body, []byte("UserContext")) {
		t.Fatalf("metrics leaked a class name: %s", metrics.body)
	}
}

func TestWGStorm(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "guard-recycle-worker.php"))
	cfg.WorkerGuardRecycle.MaxRecycles = 2
	cfg.WorkerGuardRecycle.Cooldown = time.Hour
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	base := startServer(t, cfg)

	first := getBody(t, base+"/")
	if first.status != 200 || !bytes.Contains(first.body, []byte("guard-body gen=")) {
		t.Fatalf("first status %d body %s", first.status, first.body)
	}

	var second httpResult
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		second = getBody(t, base+"/")
		if second.status == 200 && !bytes.Equal(second.body, first.body) && bytes.Contains(second.body, []byte("guard-body gen=")) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if second.status != 200 || bytes.Equal(second.body, first.body) {
		t.Fatalf("second status %d body %s", second.status, second.body)
	}

	time.Sleep(300 * time.Millisecond)
	third := getBody(t, base+"/")
	if third.status == 200 && !bytes.Equal(third.body, second.body) {
		t.Fatalf("storm started another generation: %s", third.body)
	}

	metrics := getBody(t, base+cfg.Metrics.Path)
	if !bytes.Contains(metrics.body, []byte(`eregion_worker_guard_breaker_state{workload="http",state="open"} 1`)) {
		t.Fatalf("metrics missing open breaker: %s", metrics.body)
	}
	if !bytes.Contains(metrics.body, []byte(`eregion_worker_guard_recycle_events_total{workload="http",reason="guard_state_violation"}`)) {
		t.Fatalf("metrics missing guard events: %s", metrics.body)
	}
	if bytes.Contains(metrics.body, []byte("UserContext")) || bytes.Contains(metrics.body, []byte("synthetic-secret-token")) {
		t.Fatalf("metrics leaked application data: %s", metrics.body)
	}
}

func TestWGReason(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "unknown-recycle-worker.php"))
	base := startServer(t, cfg)

	resp := getBody(t, base+"/")
	if resp.status != 200 || !bytes.Equal(resp.body, []byte("plain-body")) {
		t.Fatalf("status %d body %s", resp.status, resp.body)
	}

	var metrics httpResult
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		metrics = getBody(t, base+cfg.Metrics.Path)
		if bytes.Contains(metrics.body, []byte(`eregion_worker_recycles_total{reason="worker_requested"}`)) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if bytes.Contains(metrics.body, []byte("UserContext")) || bytes.Contains(metrics.body, []byte("Security")) {
		t.Fatalf("metrics leaked recycle reason: %s", metrics.body)
	}
	if !bytes.Contains(metrics.body, []byte(`eregion_worker_recycles_total{reason="worker_requested"}`)) {
		t.Fatalf("metrics = %s", metrics.body)
	}
}

func TestWGRecover(t *testing.T) {
	script := filepath.Join(fixturesDir(t), "guard-recover-worker.php")
	cfg := testConfig(t, script)
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.RestartLimit = 20
	cfg.Workers.AcquireTimeout = 400 * time.Millisecond
	cfg.WorkerGuardRecycle.MaxRecycles = 2
	cfg.WorkerGuardRecycle.Window = 30 * time.Second
	cfg.WorkerGuardRecycle.Cooldown = 2 * time.Second
	cfg.WorkerGuardRecycle.HalfOpenMaxProbes = 1
	cfg.WorkerGuardRecycle.RecoverySuccessfulRequests = 4
	cfg.WorkerGuardRecycle.RecoveryMinUptime = time.Second
	base, stop := startManaged(t, cfg)

	pids := map[string]string{}
	var violated time.Time
	var recovered time.Time
	var closed []byte
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := tryGet(base + "/"); ok && got.status == 200 {
			gen, pid, parsed := parseRecover(got.body)
			if !parsed {
				t.Fatalf("body %q", got.body)
			}
			if prev, exists := pids[gen]; exists && prev != pid {
				t.Fatalf("generation %s changed process %s -> %s", gen, prev, pid)
			}
			pids[gen] = pid
			if gen == "2" && violated.IsZero() {
				violated = time.Now()
			}
			if gen == "3" && recovered.IsZero() {
				recovered = time.Now()
			}
			if gen != "1" && gen != "2" && gen != "3" {
				t.Fatalf("unexpected generation %s pids=%v", gen, pids)
			}
		}
		if metrics, ok := tryGet(base + cfg.Metrics.Path); ok && !recovered.IsZero() {
			success, _ := promValue(metrics.body, `eregion_worker_guard_half_open_attempts_total{workload="http",result="success"}`)
			if success == 1 && bytes.Contains(metrics.body, []byte(`eregion_worker_guard_breaker_state{workload="http",state="closed"} 1`)) {
				closed = metrics.body
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	if violated.IsZero() || pids["1"] == "" || pids["2"] == "" {
		t.Fatalf("breaker was not opened by two real violations: %+v", pids)
	}
	if recovered.IsZero() || pids["3"] == "" || closed == nil {
		t.Fatalf("cooldown did not recover a healthy worker: pids=%v\n%s", pids, breakerLines(closed))
	}
	gap := recovered.Sub(violated)
	if gap < 1500*time.Millisecond {
		t.Fatalf("probe returned %s after the second violation; restart loop", gap)
	}
	if pids["1"] == pids["2"] || pids["1"] == pids["3"] || pids["2"] == pids["3"] {
		t.Fatalf("compromised worker was reused: %+v", pids)
	}
	admitted, _ := promValue(closed, `eregion_worker_guard_half_open_attempts_total{workload="http",result="admitted"}`)
	if admitted != 1 {
		t.Fatalf("half-open admissions=%d, want 1\n%s", admitted, breakerLines(closed))
	}
	if bytes.Contains(closed, []byte(`result="failure"`)) {
		t.Fatalf("probe failed\n%s", breakerLines(closed))
	}
	restarts, ok := promValue(closed, "eregion_worker_restarts_total")
	if !ok {
		t.Fatalf("missing restart counter\n%s", breakerLines(closed))
	}
	time.Sleep(1200 * time.Millisecond)
	for i := 0; i < 8; i++ {
		got, ok := tryGet(base + "/")
		if !ok || got.status != 200 {
			t.Fatalf("recovered worker stopped serving: %+v", got)
		}
		gen, pid, parsed := parseRecover(got.body)
		if !parsed || gen != "3" || pid != pids["3"] {
			t.Fatalf("recovered worker was replaced: gen=%s pid=%s want pid=%s", gen, pid, pids["3"])
		}
	}
	later, ok := tryGet(base + cfg.Metrics.Path)
	if !ok {
		t.Fatal("metrics unavailable after recovery")
	}
	restartsLater, _ := promValue(later.body, "eregion_worker_restarts_total")
	if restartsLater != restarts {
		t.Fatalf("restarts grew after recovery: %d -> %d", restarts, restartsLater)
	}
	t.Logf("pids=%v gap=%s restarts=%d", pids, gap, restarts)

	started := time.Now()
	stop()
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
	if hits := workerProcesses(script); len(hits) != 0 {
		t.Fatalf("orphan workers after shutdown: %v", hits)
	}
}

func TestWGShutdown(t *testing.T) {
	script := filepath.Join(fixturesDir(t), "guard-recycle-worker.php")
	cfg := testConfig(t, script)
	cfg.Workers.Count = 1
	cfg.Workers.MinReady = 1
	cfg.Workers.RestartLimit = 20
	cfg.Workers.AcquireTimeout = 400 * time.Millisecond
	cfg.WorkerGuardRecycle.MaxRecycles = 1
	cfg.WorkerGuardRecycle.Cooldown = 45 * time.Second
	base, stop := startManaged(t, cfg)

	deadline := time.Now().Add(10 * time.Second)
	opened := false
	for time.Now().Before(deadline) {
		_, _ = tryGet(base + "/")
		metrics, ok := tryGet(base + cfg.Metrics.Path)
		if ok && bytes.Contains(metrics.body, []byte(`eregion_worker_guard_breaker_state{workload="http",state="open"} 1`)) {
			opened = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !opened {
		t.Fatal("breaker did not open")
	}

	started := time.Now()
	stop()
	elapsed := time.Since(started)
	t.Logf("shutdown_during_cooldown=%s", elapsed)
	if elapsed > 8*time.Second {
		t.Fatalf("shutdown waited on the cooldown timer: %s", elapsed)
	}
	if hits := workerProcesses(script); len(hits) != 0 {
		t.Fatalf("orphan workers after shutdown: %v", hits)
	}
}

func startManaged(t *testing.T, cfg config.Config) (string, func()) {
	t.Helper()
	cfg.Server.Port = pickTCPPort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := server.New(cfg, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-errCh:
			case <-time.After(15 * time.Second):
				t.Errorf("shutdown did not return")
			}
		})
	}
	t.Cleanup(stop)

	baseURL := "http://" + cfg.Addr()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + cfg.Liveness.Path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				resp2, err2 := http.Get(baseURL + cfg.Readiness.Path)
				if err2 == nil {
					resp2.Body.Close()
					if resp2.StatusCode == 200 {
						return baseURL, stop
					}
				}
			}
		}
		select {
		case err := <-errCh:
			errCh <- err
			t.Fatalf("server exited early: %v", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server not ready")
	return "", stop
}

func tryGet(url string) (httpResult, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return httpResult{}, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpResult{}, false
	}
	return httpResult{status: resp.StatusCode, body: body}, true
}

func parseRecover(body []byte) (string, string, bool) {
	text := strings.TrimSpace(string(body))
	const prefix = "recover gen="
	if !strings.HasPrefix(text, prefix) {
		return "", "", false
	}
	gen, pid, ok := strings.Cut(strings.TrimPrefix(text, prefix), " pid=")
	if !ok || gen == "" || pid == "" {
		return "", "", false
	}
	return gen, pid, true
}

func promValue(body []byte, name string) (int, bool) {
	prefix := []byte(name + " ")
	labeled := []byte(name)
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, prefix) || (strings.Contains(name, "{") && bytes.HasPrefix(line, labeled)) {
			fields := bytes.Fields(line)
			if len(fields) < 2 {
				continue
			}
			n, err := strconv.Atoi(string(fields[len(fields)-1]))
			if err != nil {
				continue
			}
			return n, true
		}
	}
	return 0, false
}

func breakerLines(body []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "guard_") || strings.Contains(line, "restarts_total") || strings.Contains(line, "recycles_total") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func workerProcesses(script string) []string {
	needle := filepath.Base(script)
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell", "-NoProfile", "-Command", `Get-CimInstance Win32_Process -Filter "Name = 'php.exe'" | ForEach-Object { $_.CommandLine }`).CombinedOutput()
		if err != nil {
			return []string{err.Error()}
		}
		var hits []string
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, needle) {
				hits = append(hits, strings.TrimSpace(line))
			}
		}
		return hits
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return []string{err.Error()}
	}
	var hits []string
	for _, entry := range entries {
		cmd, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil || !bytes.Contains(cmd, []byte(needle)) {
			continue
		}
		hits = append(hits, entry.Name())
	}
	return hits
}

func TestWGLegacy(t *testing.T) {
	cfg := testConfig(t, filepath.Join(fixturesDir(t), "healthy-worker.php"))
	base := startServer(t, cfg)
	resp := getBody(t, base+"/")
	if resp.status != 200 {
		t.Fatalf("status %d body %s", resp.status, resp.body)
	}
}

type httpResult struct {
	status int
	body   []byte
}

func getBody(t *testing.T, url string) httpResult {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: resp.StatusCode, body: body}
}
