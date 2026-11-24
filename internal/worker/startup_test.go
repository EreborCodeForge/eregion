package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/socket"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestStarterRejectsSocketPathBeforeSpawn(t *testing.T) {
	limit := socket.SunPathBytes()
	name := "worker-3-1.sock"
	sep := string(filepath.Separator)
	dirLen := limit - len(sep) - len(name)
	prefix := "/"
	if runtime.GOOS == "windows" {
		prefix = `C:\`
	}
	dir := prefix + strings.Repeat("a", dirLen-len(prefix))
	socks := socket.NewManager(dir, 0o700, 0o600)
	if got := len(socks.Path(3, 1)); got != limit {
		t.Fatalf("final path len=%d want %d", got, limit)
	}

	cfg := config.Default()
	cfg.Workers.StartupTimeout = 8 * time.Second
	starter := worker.NewStarter(cfg, socks, forbidSpawner{t: t}, discardLogger(), "test", "http")
	started := time.Now()
	_, err := starter.Start(context.Background(), 3, 1, 0)
	elapsed := time.Since(started)
	if elapsed >= time.Second {
		t.Fatalf("invalid path waited %s", elapsed)
	}
	se := requireStartup(t, err)
	if se.Workload != "http" || se.WorkerID != "worker-3" {
		t.Fatalf("identity workload=%s worker=%s", se.Workload, se.WorkerID)
	}
	if se.Stage != worker.StageSocketPath || se.Cause != worker.CausePathTooLong {
		t.Fatalf("stage=%s cause=%s", se.Stage, se.Cause)
	}
	var lengthErr *socket.PathLengthError
	if !errors.As(err, &lengthErr) {
		t.Fatalf("missing length error: %v", err)
	}
}

func TestStarterStartupFailures(t *testing.T) {
	bin := buildGoHelper(t, t.TempDir(), "bootfail", bootFailSource)
	before := runtime.NumGoroutine()

	t.Run("valid path then missing socket", func(t *testing.T) {
		err, elapsed := startMode(t, bin, "hang", 400*time.Millisecond)
		if elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
			t.Fatalf("missing socket elapsed %s", elapsed)
		}
		se := requireStartup(t, err)
		if se.Stage != worker.StageWaitSocket || se.Cause != worker.CauseSocketMissing {
			t.Fatalf("stage=%s cause=%s err=%v", se.Stage, se.Cause, err)
		}
		if se.Workload != "http" || se.WorkerID != "worker-1" {
			t.Fatalf("identity %+v", se)
		}
	})

	t.Run("worker exits before socket", func(t *testing.T) {
		err, elapsed := startMode(t, bin, "exit", 8*time.Second)
		if elapsed >= 3*time.Second {
			t.Fatalf("exit was not fail-fast: %s", elapsed)
		}
		se := requireStartup(t, err)
		if se.Stage != worker.StageWaitSocket || se.Cause != worker.CauseWorkerExited {
			t.Fatalf("stage=%s cause=%s err=%v", se.Stage, se.Cause, err)
		}
		if !strings.Contains(se.Stderr, "worker exploded before bind") {
			t.Fatalf("stderr=%q", se.Stderr)
		}
	})

	t.Run("bind error", func(t *testing.T) {
		err, elapsed := startMode(t, bin, "bind", 8*time.Second)
		if elapsed >= 3*time.Second {
			t.Fatalf("bind failure was not fail-fast: %s", elapsed)
		}
		se := requireStartup(t, err)
		if se.Stage != worker.StageWaitSocket || se.Cause != worker.CauseBindFailed {
			t.Fatalf("stage=%s cause=%s err=%v", se.Stage, se.Cause, err)
		}
		if !strings.Contains(se.Stderr, "socket_bind failed") {
			t.Fatalf("stderr=%q", se.Stderr)
		}
	})

	t.Run("healthy worker can close stderr", func(t *testing.T) {
		err, elapsed := startMode(t, bin, "closerr", 8*time.Second)
		if elapsed >= 3*time.Second {
			t.Fatalf("stderr close waited for startup timeout: %s", elapsed)
		}
		se := requireStartup(t, err)
		if se.Stage == worker.StageWaitSocket {
			t.Fatalf("stderr close aborted wait_socket: %+v", se)
		}
		if se.Stage != worker.StageDial && se.Stage != worker.StageHandshake {
			t.Fatalf("stage=%s cause=%s err=%v", se.Stage, se.Cause, err)
		}
		if !strings.Contains(se.Stderr, "healthy-before-stderr-close") {
			t.Fatalf("stderr=%q", se.Stderr)
		}
	})

	t.Run("stderr is bounded", func(t *testing.T) {
		err, elapsed := startMode(t, bin, "flood", 8*time.Second)
		if elapsed >= 3*time.Second {
			t.Fatalf("flooded stderr blocked startup: %s", elapsed)
		}
		se := requireStartup(t, err)
		if se.Cause != worker.CauseBindFailed {
			t.Fatalf("cause=%s stderr=%q", se.Cause, se.Stderr)
		}
		if !strings.Contains(se.Stderr, "startup-begin") || !strings.Contains(se.Stderr, "socket_bind failed") {
			t.Fatalf("lost startup text: %q", se.Stderr)
		}
		if !strings.Contains(se.Stderr, "[stderr truncated]") {
			t.Fatalf("expected truncation marker: %q", se.Stderr)
		}
		if len(se.Stderr) > 9000 {
			t.Fatalf("stderr kept %d bytes", len(se.Stderr))
		}
	})

	time.Sleep(200 * time.Millisecond)
	if delta := runtime.NumGoroutine() - before; delta > 2 {
		t.Fatalf("goroutine leak delta=%d", delta)
	}
}

func startMode(t *testing.T, bin, mode string, startup time.Duration) (error, time.Duration) {
	t.Helper()
	dir := shortWorkerDir(t)
	cfg := config.Default()
	cfg.PHP.Binary = bin
	cfg.PHP.WorkerScript = mode
	cfg.PHP.WorkingDirectory = dir
	cfg.Workers.StartupTimeout = startup
	cfg.Workers.ShutdownTimeout = 30 * time.Millisecond
	cfg.Workers.HandshakeTimeout = time.Second
	socks := socket.NewManager(dir, 0o700, 0o600)
	if err := socks.Prepare(); err != nil {
		t.Fatal(err)
	}
	starter := worker.NewStarter(cfg, socks, nil, discardLogger(), "test", "http")
	started := time.Now()
	_, err := starter.Start(context.Background(), 1, 1, 0)
	return err, time.Since(started)
}

func shortWorkerDir(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(os.TempDir(), "e")
	if runtime.GOOS == "windows" {
		if drive := os.Getenv("SystemDrive"); drive != "" {
			parent = drive + `\e`
		}
	} else {
		parent = "/tmp/e"
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		parent = filepath.Join(os.TempDir(), "e")
		if err2 := os.MkdirAll(parent, 0o700); err2 != nil {
			t.Fatal(err2)
		}
	}
	dir, err := os.MkdirTemp(parent, "w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sample := filepath.Join(dir, "worker-1-1.sock")
	if err := socket.ValidateBindPath(sample); err != nil {
		t.Fatalf("socket path still too long: %s", sample)
	}
	return dir
}

func requireStartup(t *testing.T, err error) *worker.StartupError {
	t.Helper()
	var se *worker.StartupError
	if !errors.As(err, &se) {
		t.Fatalf("got %v", err)
	}
	return se
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const bootFailSource = `package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	mode := "exit"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "hang":
		time.Sleep(30 * time.Second)
	case "bind":
		fmt.Fprintln(os.Stderr, "socket_bind failed: address in use")
		os.Exit(1)
	case "closerr":
		fmt.Fprintln(os.Stderr, "healthy-before-stderr-close")
		_ = os.Stderr.Close()
		socketPath := ""
		for _, arg := range os.Args {
			if strings.HasPrefix(arg, "--socket=") {
				socketPath = strings.TrimPrefix(arg, "--socket=")
			}
		}
		if socketPath != "" {
			_ = os.WriteFile(socketPath, []byte("x"), 0o600)
		}
		time.Sleep(30 * time.Second)
	case "flood":
		fmt.Fprint(os.Stderr, "startup-begin\n")
		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = 'A'
		}
		_, _ = os.Stderr.Write(buf)
		fmt.Fprintln(os.Stderr, "socket_bind failed: noisy")
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "worker exploded before bind")
		os.Exit(1)
	}
}
`

type forbidSpawner struct{ t *testing.T }

func (f forbidSpawner) Spawn(context.Context, worker.ProcessSpecification) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	f.t.Fatal("spawned despite an invalid socket path")
	return nil, nil, nil, errors.New("spawned")
}
