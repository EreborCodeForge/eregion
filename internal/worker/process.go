package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/socket"
)

// ProcessSpecification describes how to spawn a PHP worker.
type ProcessSpecification struct {
	Binary           string
	WorkerScript     string
	WorkingDirectory string
	Environment      map[string]string
	Manifest         string
	SocketPath       string
	WorkerID         string
	Generation       uint64
	MaxRequests      int
	MemoryLimitMB    int
}

// ProcessSpawner starts OS processes.
type ProcessSpawner interface {
	Spawn(ctx context.Context, spec ProcessSpecification) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error)
}

// ExecSpawner uses os/exec.
type ExecSpawner struct{}

// Spawn starts the PHP worker process.
func (ExecSpawner) Spawn(ctx context.Context, spec ProcessSpecification) (*exec.Cmd, io.ReadCloser, io.ReadCloser, error) {
	args := []string{
		spec.WorkerScript,
		"--socket=" + spec.SocketPath,
		"--worker-id=" + spec.WorkerID,
		"--generation=" + strconv.FormatUint(spec.Generation, 10),
		"--max-requests=" + strconv.Itoa(spec.MaxRequests),
		"--memory-limit-mb=" + strconv.Itoa(spec.MemoryLimitMB),
	}
	if spec.Manifest != "" {
		args = append(args, "--manifest="+spec.Manifest)
	}
	_ = ctx
	cmd := exec.Command(spec.Binary, args...)
	cmd.Dir = spec.WorkingDirectory
	env := os.Environ()
	for k, v := range spec.Environment {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	return cmd, stdout, stderr, nil
}

// Worker is one pool slot generation.
type Worker struct {
	ID              string
	Slot            int
	Generation      uint64
	PID             int
	State           State
	SocketPath      string
	RequestsHandled uint64
	StartedAt       time.Time
	RestartCount    uint64

	conn   *protocol.Conn
	cmd    *exec.Cmd
	mu     sync.Mutex
	logger *slog.Logger
}

// Snapshot is a concurrency-safe view of a worker.
type Snapshot struct {
	ID              string
	Slot            int
	Generation      uint64
	PID             int
	State           State
	SocketPath      string
	RequestsHandled uint64
	StartedAt       time.Time
	RestartCount    uint64
}

// GetSnapshot returns a copy of worker fields.
func (w *Worker) GetSnapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Snapshot{
		ID: w.ID, Slot: w.Slot, Generation: w.Generation, PID: w.PID,
		State: w.State, SocketPath: w.SocketPath, RequestsHandled: w.RequestsHandled,
		StartedAt: w.StartedAt, RestartCount: w.RestartCount,
	}
}

func (w *Worker) setState(s State) {
	w.mu.Lock()
	w.State = s
	w.mu.Unlock()
}

func (w *Worker) getState() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.State
}

// Client sends requests over the worker's persistent connection.
type Client struct {
	maxFrameBytes uint32
}

// NewClient creates a WorkerClient.
func NewClient(maxFrameBytes uint32) *Client {
	return &Client{maxFrameBytes: maxFrameBytes}
}

// Send delivers one request to the worker.
func (c *Client) Send(ctx context.Context, w *Worker, req protocol.RequestEnvelope) (protocol.ResponseEnvelope, error) {
	w.mu.Lock()
	conn := w.conn
	w.mu.Unlock()
	if conn == nil {
		return protocol.ResponseEnvelope{}, fmt.Errorf("worker %s has no connection", w.ID)
	}

	timeout := time.Until(deadlineFrom(ctx))
	if timeout <= 0 {
		if dl, ok := ctx.Deadline(); ok {
			timeout = time.Until(dl)
		}
	}
	if timeout <= 0 {
		timeout = time.Second
	}

	type result struct {
		resp protocol.ResponseEnvelope
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := conn.SendRequest(req, timeout)
		ch <- result{resp, err}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		// If the worker already produced a result (e.g. protocol failure), prefer it
		// over the deadline so callers can return 502 instead of 504.
		select {
		case r := <-ch:
			if r.err != nil {
				return r.resp, r.err
			}
			return r.resp, nil
		default:
			return protocol.ResponseEnvelope{}, ctx.Err()
		}
	case r := <-ch:
		return r.resp, r.err
	}
}

func deadlineFrom(ctx context.Context) time.Time {
	dl, ok := ctx.Deadline()
	if !ok {
		return time.Time{}
	}
	return dl
}

// Starter boots a worker process and completes handshake.
type Starter struct {
	cfg     config.Config
	sockets *socket.Manager
	spawner ProcessSpawner
	logger  *slog.Logger
	version string
}

// NewStarter constructs a worker starter.
func NewStarter(cfg config.Config, sockets *socket.Manager, spawner ProcessSpawner, logger *slog.Logger, version string) *Starter {
	if spawner == nil {
		spawner = ExecSpawner{}
	}
	return &Starter{cfg: cfg, sockets: sockets, spawner: spawner, logger: logger, version: version}
}

// Start launches generation for a slot.
func (s *Starter) Start(ctx context.Context, slot int, generation uint64, restartCount uint64) (*Worker, error) {
	id := fmt.Sprintf("worker-%d", slot)
	sockPath := s.sockets.Path(slot, generation)
	_ = s.sockets.Remove(sockPath)

	w := &Worker{
		ID:           id,
		Slot:         slot,
		Generation:   generation,
		State:        StateStarting,
		SocketPath:   sockPath,
		StartedAt:    time.Now(),
		RestartCount: restartCount,
		logger:       s.logger,
	}

	spec := ProcessSpecification{
		Binary:           s.cfg.PHP.Binary,
		WorkerScript:     s.cfg.PHP.WorkerScript,
		WorkingDirectory: s.cfg.PHP.WorkingDirectory,
		Environment:      s.cfg.PHP.Environment,
		Manifest:         s.cfg.Manifest,
		SocketPath:       sockPath,
		WorkerID:         id,
		Generation:       generation,
		MaxRequests:      s.cfg.Workers.MaxRequests,
		MemoryLimitMB:    s.cfg.Workers.MemoryLimitMB,
	}

	startCtx, cancel := context.WithTimeout(ctx, s.cfg.Workers.StartupTimeout)
	defer cancel()

	cmd, stdout, stderr, err := s.spawner.Spawn(startCtx, spec)
	if err != nil {
		w.State = StateFailed
		return nil, fmt.Errorf("spawn %s: %w", id, err)
	}
	w.cmd = cmd
	if cmd.Process != nil {
		w.PID = cmd.Process.Pid
	}

	go drainLog(s.logger, id, "stdout", stdout)
	go drainLog(s.logger, id, "stderr", stderr)

	if err := waitForSocket(startCtx, sockPath); err != nil {
		_ = terminateProcess(cmd, s.cfg.Workers.ShutdownTimeout)
		w.State = StateFailed
		return nil, fmt.Errorf("wait socket %s: %w", sockPath, err)
	}
	_ = os.Chmod(sockPath, s.sockets.SocketPerm())

	conn, err := dialUnix(startCtx, sockPath)
	if err != nil {
		_ = terminateProcess(cmd, s.cfg.Workers.ShutdownTimeout)
		w.State = StateFailed
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}

	hello := protocol.Hello{
		Type:            protocol.TypeHello,
		Protocol:        protocol.ProtocolName,
		ProtocolVersion: protocol.ProtocolVersion,
		RuntimeVersion:  s.version,
		WorkerID:        id,
		Generation:      generation,
	}
	ready, err := protocol.PerformHandshake(startCtx, conn, s.cfg.Protocol.MaxFrameBytes, hello, s.cfg.Workers.HandshakeTimeout)
	if err != nil {
		_ = conn.Close()
		_ = terminateProcess(cmd, s.cfg.Workers.ShutdownTimeout)
		w.State = StateFailed
		return nil, fmt.Errorf("handshake %s: %w", id, err)
	}
	if ready.PID > 0 {
		w.PID = ready.PID
	}
	w.conn = protocol.NewConn(conn, s.cfg.Protocol.MaxFrameBytes)
	w.State = StateIdle
	s.logger.Info("worker ready", "worker_id", id, "generation", generation, "pid", w.PID)
	return w, nil
}

func drainLog(logger *slog.Logger, workerID, stream string, r io.ReadCloser) {
	defer r.Close()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			logger.Debug("worker output", "worker_id", workerID, "stream", stream, "data", string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func waitForSocket(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func terminateProcess(cmd *exec.Cmd, grace time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(syscallSIGTERM()); err != nil {
		return cmd.Process.Kill()
	}
	done := make(chan struct{})
	go func() {
		// Best-effort: another goroutine may own Wait(); we only enforce Kill deadline.
		time.Sleep(grace)
		close(done)
	}()
	<-done
	if cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
	}
	return nil
}
