package integration_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
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
