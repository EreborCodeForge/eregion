package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/socket"
)

var (
	ErrPoolShuttingDown = errors.New("worker pool is shutting down")
	ErrNoWorkers        = errors.New("no workers available")
	ErrAcquireTimeout   = errors.New("acquire worker timed out")
	ErrSlotFailed       = errors.New("worker slot failed restart policy")
)

// PoolSnapshot is an aggregate view of the pool.
type PoolSnapshot struct {
	Desired  int
	Running  int
	Idle     int
	Busy     int
	Starting int
	Failed   int
	Draining int
	Waiting  int
	Capacity int
}

// Pool manages a fixed set of PHP workers.
type Pool struct {
	cfg     config.Config
	sockets *socket.Manager
	starter *Starter
	client  *Client
	logger  *slog.Logger

	mu       sync.Mutex
	slots    []*slot
	idle     chan *Worker
	waiting  int32
	stopping bool
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	ctx      context.Context

	onChange func()
}

type slot struct {
	index        int
	generation   uint64
	worker       *Worker
	backoff      *Backoff
	restarts     []time.Time
	restartCount uint64
	plannedExit  bool
	monitorDone  chan struct{}
}

// NewPool builds a fixed worker pool.
func NewPool(cfg config.Config, sockets *socket.Manager, logger *slog.Logger, version string) *Pool {
	return &Pool{
		cfg:     cfg,
		sockets: sockets,
		starter: NewStarter(cfg, sockets, nil, logger, version),
		client:  NewClient(cfg.Protocol.MaxFrameBytes),
		logger:  logger,
		idle:    make(chan *Worker, cfg.Workers.Count),
	}
}

// SetOnChange registers a callback after pool state changes (metrics).
func (p *Pool) SetOnChange(fn func()) { p.onChange = fn }

func (p *Pool) notify() {
	if p.onChange != nil {
		p.onChange()
	}
}

// Start boots all workers.
func (p *Pool) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)
	if err := p.sockets.Prepare(); err != nil {
		return err
	}
	p.slots = make([]*slot, p.cfg.Workers.Count)
	for i := 0; i < p.cfg.Workers.Count; i++ {
		p.slots[i] = &slot{
			index:   i + 1,
			backoff: NewBackoff(p.cfg.Workers.RestartBackoff),
		}
	}

	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range p.slots {
		wg.Add(1)
		go func(s *slot) {
			defer wg.Done()
			if err := p.bootSlot(s, false); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				p.logger.Error("worker start failed", "slot", s.index, "error", err)
			}
		}(s)
	}
	wg.Wait()

	// Require at least min_ready workers.
	snap := p.Snapshot()
	if snap.Running < p.cfg.Workers.MinReady {
		if firstErr != nil {
			return fmt.Errorf("failed to start enough workers: %w", firstErr)
		}
		return fmt.Errorf("only %d workers ready, need %d", snap.Running, p.cfg.Workers.MinReady)
	}
	p.notify()
	return nil
}

func (p *Pool) bootSlot(s *slot, afterCrash bool) error {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return ErrPoolShuttingDown
	}
	s.generation++
	gen := s.generation
	restartCount := s.restartCount
	p.mu.Unlock()

	w, err := p.starter.Start(p.ctx, s.index, gen, restartCount)
	if err != nil {
		p.mu.Lock()
		s.worker = nil
		if afterCrash {
			p.recordCrashLocked(s)
		}
		failed := p.exceededRestartPolicyLocked(s)
		p.mu.Unlock()
		if failed {
			return fmt.Errorf("%w: slot %d", ErrSlotFailed, s.index)
		}
		p.scheduleRestart(s, true)
		return err
	}

	p.mu.Lock()
	s.worker = w
	s.plannedExit = false
	s.backoff.Reset()
	s.monitorDone = make(chan struct{})
	p.mu.Unlock()

	p.wg.Add(1)
	go p.monitor(s, w)

	select {
	case p.idle <- w:
	default:
		// Should not happen at capacity == count with empty idle.
		p.logger.Warn("idle channel full on boot", "worker_id", w.ID)
	}
	p.notify()
	return nil
}

func (p *Pool) monitor(s *slot, w *Worker) {
	defer p.wg.Done()
	defer close(s.monitorDone)

	err := w.cmd.Wait()
	exitCode := 0
	if err != nil {
		p.logger.Info("worker exited", "worker_id", w.ID, "generation", w.Generation, "error", err)
	} else {
		p.logger.Info("worker exited cleanly", "worker_id", w.ID, "generation", w.Generation)
	}
	if w.cmd.ProcessState != nil {
		exitCode = w.cmd.ProcessState.ExitCode()
	}

	p.mu.Lock()
	// Ignore stale generations.
	if s.worker == nil || s.worker.Generation != w.Generation {
		p.mu.Unlock()
		_ = p.sockets.Remove(w.SocketPath)
		return
	}
	planned := s.plannedExit || exitCode == 0 && w.getState() == StateDraining
	w.setState(StateDead)
	if w.conn != nil {
		_ = w.conn.Close()
	}
	_ = p.sockets.Remove(w.SocketPath)
	s.worker = nil
	stopping := p.stopping
	p.mu.Unlock()

	// Drain this worker from idle channel if present.
	p.purgeIdle(w)

	if stopping {
		p.notify()
		return
	}

	if planned {
		p.logger.Info("planned recycle complete", "worker_id", w.ID, "generation", w.Generation)
		p.scheduleRestart(s, false)
	} else {
		p.mu.Lock()
		p.recordCrashLocked(s)
		failed := p.exceededRestartPolicyLocked(s)
		p.mu.Unlock()
		if failed {
			p.logger.Error("worker slot entered failed state", "slot", s.index)
			p.notify()
			return
		}
		p.scheduleRestart(s, true)
	}
	p.notify()
}

func (p *Pool) purgeIdle(w *Worker) {
	// Non-blocking filter: rebuild idle channel contents carefully.
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.idle)
	for i := 0; i < n; i++ {
		select {
		case cur := <-p.idle:
			if cur == w || (cur.ID == w.ID && cur.Generation == w.Generation) {
				continue
			}
			select {
			case p.idle <- cur:
			default:
			}
		default:
			return
		}
	}
}

func (p *Pool) recordCrashLocked(s *slot) {
	s.restartCount++
	now := time.Now()
	s.restarts = append(s.restarts, now)
	windowStart := now.Add(-p.cfg.Workers.RestartWindow)
	filtered := s.restarts[:0]
	for _, t := range s.restarts {
		if t.After(windowStart) {
			filtered = append(filtered, t)
		}
	}
	s.restarts = filtered
}

func (p *Pool) exceededRestartPolicyLocked(s *slot) bool {
	if p.cfg.Workers.RestartLimit <= 0 {
		return false
	}
	return len(s.restarts) > p.cfg.Workers.RestartLimit
}

func (p *Pool) scheduleRestart(s *slot, useBackoff bool) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		var delay time.Duration
		if useBackoff {
			delay = s.backoff.Next()
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
			return
		case <-timer.C:
		}
		if err := p.bootSlot(s, useBackoff); err != nil {
			p.logger.Error("restart failed", "slot", s.index, "error", err)
		}
	}()
}

// Acquire waits for an idle worker.
func (p *Pool) Acquire(ctx context.Context) (*Worker, error) {
	atomic.AddInt32(&p.waiting, 1)
	defer atomic.AddInt32(&p.waiting, -1)

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrAcquireTimeout
			}
			return nil, ctx.Err()
		case <-p.ctx.Done():
			return nil, ErrPoolShuttingDown
		case w := <-p.idle:
			p.mu.Lock()
			stopping := p.stopping
			current := p.slots[w.Slot-1].worker
			p.mu.Unlock()
			if stopping {
				return nil, ErrPoolShuttingDown
			}
			if current == nil || current.Generation != w.Generation || w.getState() != StateIdle {
				continue
			}
			w.setState(StateBusy)
			p.notify()
			return w, nil
		}
	}
}

// Release returns a healthy worker to the idle pool, or drains for recycle.
func (p *Pool) Release(w *Worker, resp *protocol.ResponseEnvelope) {
	if w == nil {
		return
	}
	if resp != nil {
		w.mu.Lock()
		w.RequestsHandled = resp.Meta.RequestsHandled
		if w.RequestsHandled == 0 {
			w.RequestsHandled++
		}
		w.mu.Unlock()
	}

	recycle := resp != nil && resp.Meta.Recycle
	if !recycle && p.cfg.Workers.MaxRequests > 0 {
		if int(w.GetSnapshot().RequestsHandled) >= p.cfg.Workers.MaxRequests {
			recycle = true
			if resp != nil && resp.Meta.RecycleReason == "" {
				resp.Meta.Recycle = true
				resp.Meta.RecycleReason = "max_requests"
			}
		}
	}
	if !recycle && p.cfg.Workers.MemoryLimitMB > 0 && resp != nil && resp.Meta.MemoryUsage > 0 {
		limit := uint64(p.cfg.Workers.MemoryLimitMB) * 1024 * 1024
		if resp.Meta.MemoryUsage >= limit {
			recycle = true
			resp.Meta.Recycle = true
			if resp.Meta.RecycleReason == "" {
				resp.Meta.RecycleReason = "memory_limit"
			}
		}
	}

	p.mu.Lock()
	stopping := p.stopping
	s := p.slots[w.Slot-1]
	if s.worker == nil || s.worker.Generation != w.Generation {
		p.mu.Unlock()
		return
	}
	if stopping || recycle {
		s.plannedExit = true
		w.setState(StateDraining)
		conn := w.conn
		p.mu.Unlock()
		if recycle && resp != nil {
			reason := resp.Meta.RecycleReason
			if reason == "" {
				reason = "planned"
			}
			p.logger.Info("planned recycle", "worker_id", w.ID, "reason", reason)
		}
		if conn != nil {
			_ = conn.Close() // encourage PHP to exit after recycle intent
		}
		// Ask process to exit gently if still alive.
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Signal(syscallSIGTERM())
		}
		p.notify()
		return
	}
	w.setState(StateIdle)
	p.mu.Unlock()

	select {
	case p.idle <- w:
	default:
		p.logger.Warn("idle channel full on release", "worker_id", w.ID)
	}
	p.notify()
}

// Discard invalidates a worker after protocol/timeout failure.
func (p *Pool) Discard(w *Worker, reason error) {
	if w == nil {
		return
	}
	p.logger.Warn("discarding worker", "worker_id", w.ID, "generation", w.Generation, "error", reason)
	p.mu.Lock()
	s := p.slots[w.Slot-1]
	if s.worker == nil || s.worker.Generation != w.Generation {
		p.mu.Unlock()
		return
	}
	s.plannedExit = false
	w.setState(StateDraining)
	conn := w.conn
	cmd := w.cmd
	p.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = terminateProcess(cmd, p.cfg.Workers.ShutdownTimeout)
	}
	p.notify()
}

// Client returns the shared worker client.
func (p *Pool) Client() *Client { return p.client }

// Waiting returns current queue waiters (approx).
func (p *Pool) Waiting() int { return int(atomic.LoadInt32(&p.waiting)) }

// Snapshot returns pool counters.
func (p *Pool) Snapshot() PoolSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := PoolSnapshot{
		Desired:  p.cfg.Workers.Count,
		Capacity: p.cfg.Queue.Capacity,
		Waiting:  int(atomic.LoadInt32(&p.waiting)),
	}
	for _, s := range p.slots {
		if s.worker == nil {
			if len(s.restarts) > p.cfg.Workers.RestartLimit && p.cfg.Workers.RestartLimit > 0 {
				snap.Failed++
			} else {
				snap.Starting++
			}
			continue
		}
		switch s.worker.getState() {
		case StateIdle:
			snap.Idle++
			snap.Running++
		case StateBusy:
			snap.Busy++
			snap.Running++
		case StateStarting:
			snap.Starting++
		case StateDraining:
			snap.Draining++
			snap.Running++
		case StateFailed:
			snap.Failed++
		default:
			snap.Running++
		}
	}
	return snap
}

// HealthyCount returns idle+busy+draining workers.
func (p *Pool) HealthyCount() int {
	s := p.Snapshot()
	return s.Idle + s.Busy + s.Draining
}

// Shutdown stops accepting workers and terminates processes.
func (p *Pool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.stopping = true
	p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}

	// Stop idle workers first.
	for {
		select {
		case w := <-p.idle:
			p.mu.Lock()
			w.setState(StateStopped)
			cmd := w.cmd
			conn := w.conn
			p.mu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
			if cmd != nil {
				_ = terminateProcess(cmd, p.cfg.Workers.ShutdownTimeout)
			}
		default:
			goto doneIdle
		}
	}
doneIdle:

	p.mu.Lock()
	for _, s := range p.slots {
		if s.worker != nil && s.worker.cmd != nil {
			_ = s.worker.cmd.Process.Signal(syscallSIGTERM())
		}
	}
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		p.mu.Lock()
		for _, s := range p.slots {
			if s.worker != nil && s.worker.cmd != nil && s.worker.cmd.Process != nil {
				_ = s.worker.cmd.Process.Kill()
			}
		}
		p.mu.Unlock()
		<-done
	case <-done:
	}
	_ = p.sockets.CleanupDir()
	p.notify()
	return nil
}
