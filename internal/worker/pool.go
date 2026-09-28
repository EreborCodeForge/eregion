package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/socket"
	"github.com/EreborCodeForge/Eregion/internal/workload"
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

// PoolMetrics records recycle and restart events.
type PoolMetrics interface {
	IncRecycle(reason string)
	IncRestart()
}

// ScaleEventRecorder records scale up/down events (optional).
type ScaleEventRecorder interface {
	IncScale(workload, direction string)
}

// Pool manages workers for one workload.
type Pool struct {
	name    string
	mode    workload.WorkloadMode
	cfg     config.Config
	sockets *socket.Manager
	starter SlotStarter
	client  *Client
	logger  *slog.Logger
	metrics PoolMetrics
	scale   ScaleEventRecorder

	mu       sync.Mutex
	slots    []*slot
	idle     chan *Worker
	desired  int
	minReady int
	minW     int
	maxW     int
	stopping bool
	wg       sync.WaitGroup
	cancel   context.CancelFunc
	ctx      context.Context

	onChange func()
	// queueWaiting is set by the server to the dispatcher waiting counter (single source of truth).
	queueWaiting func() int
}

type slot struct {
	index        int
	generation   uint64
	worker       *Worker
	backoff      *Backoff
	restarts     []time.Time
	restartCount uint64
	plannedExit  bool
	retired      bool // true when slot index is above desired; do not restart
	monitorDone  chan struct{}
}

// NewPool builds a fixed HTTP worker pool from legacy config.
func NewPool(cfg config.Config, sockets *socket.Manager, logger *slog.Logger, version string) *Pool {
	return NewWorkloadPool(PoolOptions{
		Name:     "http",
		Mode:     workload.ModeHTTP,
		Cfg:      cfg,
		Sockets:  sockets,
		Logger:   logger,
		Version:  version,
		Desired:  cfg.Workers.Count,
		Min:      cfg.Workers.Count,
		Max:      cfg.Workers.Count,
		MinReady: cfg.Workers.MinReady,
	})
}

// PoolOptions configures a workload-scoped pool.
type PoolOptions struct {
	Name     string
	Mode     workload.WorkloadMode
	Command  []string
	Cfg      config.Config
	Sockets  *socket.Manager
	Logger   *slog.Logger
	Version  string
	Desired  int
	Min      int
	Max      int
	MinReady int
	Spawner  ProcessSpawner
}

// NewWorkloadPool builds a pool for http or consumer mode.
func NewWorkloadPool(opts PoolOptions) *Pool {
	if opts.Name == "" {
		opts.Name = "http"
	}
	if opts.Mode == "" {
		opts.Mode = workload.ModeHTTP
	}
	if opts.Max < 1 {
		opts.Max = opts.Desired
	}
	if opts.Max < opts.Min {
		opts.Max = opts.Min
	}
	if opts.Desired < opts.Min {
		opts.Desired = opts.Min
	}
	if opts.Desired > opts.Max {
		opts.Desired = opts.Max
	}
	idleCap := opts.Max
	if idleCap < 1 {
		idleCap = 1
	}

	p := &Pool{
		name:     opts.Name,
		mode:     opts.Mode,
		cfg:      opts.Cfg,
		sockets:  opts.Sockets,
		client:   NewClient(opts.Cfg.Protocol.MaxFrameBytes),
		logger:   opts.Logger.With("workload", opts.Name),
		idle:     make(chan *Worker, idleCap),
		desired:  opts.Desired,
		minReady: opts.MinReady,
		minW:     opts.Min,
		maxW:     opts.Max,
	}

	switch opts.Mode {
	case workload.ModeConsumer:
		p.starter = NewConsumerStarter(
			opts.Command,
			opts.Cfg.PHP.WorkingDirectory,
			opts.Cfg.PHP.Environment,
			opts.Cfg.Workers.StartupTimeout,
			opts.Cfg.Workers.ShutdownTimeout,
			opts.Spawner,
			p.logger,
		)
	default:
		p.starter = NewStarter(opts.Cfg, opts.Sockets, opts.Spawner, p.logger, opts.Version)
	}
	return p
}

// Name returns the workload name.
func (p *Pool) Name() string { return p.name }

// Mode returns the workload mode.
func (p *Pool) Mode() workload.WorkloadMode { return p.mode }

// SetOnChange registers a callback after pool state changes (metrics).
func (p *Pool) SetOnChange(fn func()) { p.onChange = fn }

// SetMetrics wires recycle/restart counters.
func (p *Pool) SetMetrics(m PoolMetrics) { p.metrics = m }

// SetScaleRecorder wires scale event counters.
func (p *Pool) SetScaleRecorder(s ScaleEventRecorder) { p.scale = s }

// SetQueueWaitingProvider registers the single source of truth for queue_waiting.
func (p *Pool) SetQueueWaitingProvider(fn func() int) { p.queueWaiting = fn }

// IsStopping reports whether the pool is shutting down.
func (p *Pool) IsStopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopping
}

func (p *Pool) notify() {
	if p.onChange != nil {
		p.onChange()
	}
}

// Start boots desired workers.
func (p *Pool) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)
	if p.mode == workload.ModeHTTP && p.sockets != nil {
		if err := p.sockets.Prepare(); err != nil {
			return err
		}
	}

	p.mu.Lock()
	n := p.desired
	p.slots = make([]*slot, n)
	for i := 0; i < n; i++ {
		p.slots[i] = &slot{
			index:   i + 1,
			backoff: NewBackoff(p.cfg.Workers.RestartBackoff),
		}
	}
	p.mu.Unlock()

	if n == 0 {
		p.notify()
		return nil
	}

	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	p.mu.Lock()
	slots := append([]*slot(nil), p.slots...)
	p.mu.Unlock()
	for _, s := range slots {
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

	// Require at least min_ready workers (HTTP). Consumer min=0 is allowed.
	snap := p.Snapshot()
	need := p.minReady
	if need < 0 {
		need = 0
	}
	if snap.Running < need {
		if firstErr != nil {
			return fmt.Errorf("failed to start enough workers: %w", firstErr)
		}
		return fmt.Errorf("only %d workers ready, need %d", snap.Running, need)
	}
	p.notify()
	return nil
}

func (p *Pool) bootSlot(s *slot, afterCrash bool) error {
	p.mu.Lock()
	if p.stopping || s.retired || s.index > p.desired {
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
	if p.stopping || s.retired || s.index > p.desired {
		p.mu.Unlock()
		if w.cmd != nil {
			_ = terminateProcess(w.cmd, p.cfg.Workers.ShutdownTimeout)
		}
		return ErrPoolShuttingDown
	}
	s.worker = w
	s.plannedExit = false
	s.backoff.Reset()
	s.monitorDone = make(chan struct{})
	p.mu.Unlock()

	p.wg.Add(1)
	go p.monitor(s, w)

	if p.mode == workload.ModeHTTP {
		select {
		case p.idle <- w:
		default:
			p.logger.Warn("idle channel full on boot", "worker_id", w.ID)
		}
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
		if w.SocketPath != "" && p.sockets != nil {
			_ = p.sockets.Remove(w.SocketPath)
		}
		return
	}
	planned := s.plannedExit || exitCode == 0 && w.getState() == StateDraining
	w.setState(StateDead)
	if w.conn != nil {
		_ = w.conn.Close()
	}
	if w.SocketPath != "" && p.sockets != nil {
		_ = p.sockets.Remove(w.SocketPath)
	}
	s.worker = nil
	stopping := p.stopping
	retired := s.retired || s.index > p.desired
	p.mu.Unlock()

	// Drain this worker from idle channel if present.
	p.purgeIdle(w)

	if stopping || retired {
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
		if p.metrics != nil {
			p.metrics.IncRestart()
		}
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
		p.mu.Lock()
		retired := s.retired || s.index > p.desired || p.stopping
		p.mu.Unlock()
		if retired {
			return
		}
		if err := p.bootSlot(s, useBackoff); err != nil {
			p.logger.Error("restart failed", "slot", s.index, "error", err)
		}
	}()
}

// SetDesired reconciles pool size toward n (clamped to min/max).
// Scale-up boots new slots; scale-down drains idle/consumer workers only.
func (p *Pool) SetDesired(n int) {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	if n < p.minW {
		n = p.minW
	}
	if p.maxW > 0 && n > p.maxW {
		n = p.maxW
	}
	old := p.desired
	if n == old && len(p.slots) >= n {
		// Still ensure non-retired slots are booted.
		needBoot := false
		for _, s := range p.slots {
			if s.index <= n && s.worker == nil && !s.retired && !p.exceededRestartPolicyLocked(s) {
				needBoot = true
				break
			}
		}
		if !needBoot {
			p.mu.Unlock()
			return
		}
	}
	p.desired = n
	direction := ""
	if n > old {
		direction = "up"
	} else if n < old {
		direction = "down"
	}

	// Ensure slot slice covers desired.
	for len(p.slots) < n {
		idx := len(p.slots) + 1
		p.slots = append(p.slots, &slot{
			index:   idx,
			backoff: NewBackoff(p.cfg.Workers.RestartBackoff),
		})
	}
	for i, s := range p.slots {
		s.retired = i+1 > n
	}

	toBoot := make([]*slot, 0)
	toDrain := make([]*Worker, 0)
	for _, s := range p.slots {
		if s.index > n {
			if s.worker != nil {
				st := s.worker.getState()
				if st == StateBusy && p.mode == workload.ModeHTTP {
					// Do not kill busy HTTP workers; mark retired and drain on Release.
					continue
				}
				if st != StateDraining && st != StateDead && st != StateStopped {
					s.plannedExit = true
					s.worker.setState(StateDraining)
					toDrain = append(toDrain, s.worker)
				}
			}
			continue
		}
		if s.worker != nil {
			continue
		}
		if !p.exceededRestartPolicyLocked(s) {
			toBoot = append(toBoot, s)
		}
	}
	p.mu.Unlock()

	for _, w := range toDrain {
		p.purgeIdle(w)
		if w.conn != nil {
			_ = w.conn.Close()
		}
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Signal(syscallSIGTERM())
		}
	}
	for _, s := range toBoot {
		go func(s *slot) {
			if err := p.bootSlot(s, false); err != nil {
				p.logger.Error("scale-up boot failed", "slot", s.index, "error", err)
			}
		}(s)
	}
	if direction != "" && p.scale != nil {
		p.scale.IncScale(p.name, direction)
	}
	p.notify()
}

// TryAcquire attempts to take an idle worker without blocking.
func (p *Pool) TryAcquire() (*Worker, bool) {
	if p.mode != workload.ModeHTTP {
		return nil, false
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return nil, false
	}
	p.mu.Unlock()

	for {
		select {
		case w := <-p.idle:
			p.mu.Lock()
			stopping := p.stopping
			if w.Slot < 1 || w.Slot > len(p.slots) {
				p.mu.Unlock()
				continue
			}
			current := p.slots[w.Slot-1].worker
			p.mu.Unlock()
			if stopping {
				return nil, false
			}
			if current == nil || current.Generation != w.Generation || w.getState() != StateIdle {
				continue
			}
			w.setState(StateBusy)
			p.notify()
			return w, true
		default:
			return nil, false
		}
	}
}

// Acquire waits for an idle worker.
func (p *Pool) Acquire(ctx context.Context) (*Worker, error) {
	if p.mode != workload.ModeHTTP {
		return nil, ErrNoWorkers
	}
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
			if w.Slot < 1 || w.Slot > len(p.slots) {
				p.mu.Unlock()
				continue
			}
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

	w.mu.Lock()
	w.RequestsHandled++
	handled := w.RequestsHandled
	w.mu.Unlock()

	if resp != nil && resp.Meta.RequestsHandled > 0 && resp.Meta.RequestsHandled != handled {
		p.logger.Debug("worker meta requests_handled mismatch",
			"worker_id", w.ID,
			"runtime", handled,
			"meta", resp.Meta.RequestsHandled,
		)
	}

	recycle := false
	reason := ""
	if resp != nil && resp.Meta.Recycle {
		recycle = true
		reason = resp.Meta.RecycleReason
		if reason == "" {
			reason = "worker_requested"
		}
	}
	if !recycle && p.cfg.Workers.MaxRequests > 0 && int(handled) >= p.cfg.Workers.MaxRequests {
		recycle = true
		reason = "max_requests"
	}
	if !recycle && p.cfg.Workers.MemoryLimitMB > 0 && resp != nil && resp.Meta.MemoryUsage > 0 {
		limit := uint64(p.cfg.Workers.MemoryLimitMB) * 1024 * 1024
		if resp.Meta.MemoryUsage >= limit {
			recycle = true
			reason = "memory_limit"
		}
	}
	if recycle && resp != nil {
		resp.Meta.Recycle = true
		if resp.Meta.RecycleReason == "" {
			resp.Meta.RecycleReason = reason
		}
	}

	p.mu.Lock()
	stopping := p.stopping
	if w.Slot < 1 || w.Slot > len(p.slots) {
		p.mu.Unlock()
		return
	}
	s := p.slots[w.Slot-1]
	if s.worker == nil || s.worker.Generation != w.Generation {
		p.mu.Unlock()
		return
	}
	retired := s.retired || s.index > p.desired
	if stopping || recycle || retired {
		s.plannedExit = true
		w.setState(StateDraining)
		conn := w.conn
		p.mu.Unlock()
		if recycle {
			if reason == "" {
				reason = "planned"
			}
			p.logger.Info("planned recycle", "worker_id", w.ID, "reason", reason)
			if p.metrics != nil {
				p.metrics.IncRecycle(reason)
			}
		}
		if conn != nil {
			_ = conn.Close()
		}
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
	if w.Slot < 1 || w.Slot > len(p.slots) {
		p.mu.Unlock()
		return
	}
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

// Waiting returns current queue waiters from the dispatcher provider.
func (p *Pool) Waiting() int {
	if p.queueWaiting != nil {
		return p.queueWaiting()
	}
	return 0
}

// Snapshot returns pool counters.
func (p *Pool) Snapshot() PoolSnapshot {
	waiting := 0
	if p.queueWaiting != nil {
		waiting = p.queueWaiting()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := PoolSnapshot{
		Desired:  p.desired,
		Capacity: p.cfg.Queue.Capacity,
		Waiting:  waiting,
	}
	for _, s := range p.slots {
		if s.retired && s.worker == nil {
			continue
		}
		if s.worker == nil {
			if len(s.restarts) > p.cfg.Workers.RestartLimit && p.cfg.Workers.RestartLimit > 0 {
				snap.Failed++
			} else if !s.retired && s.index <= p.desired {
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
		if s.worker != nil && s.worker.cmd != nil && s.worker.cmd.Process != nil {
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
	if p.sockets != nil {
		_ = p.sockets.CleanupDir()
	}
	p.notify()
	return nil
}
