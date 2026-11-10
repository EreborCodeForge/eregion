package worker

import (
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

// Guard recycle reasons that may open the breaker.
// Any other string is ignored here so Prometheus labels stay on the allowlist.
const guardStateViolationReason = "guard_state_violation"

// BootCause says why a slot wants a new process.
type BootCause int

const (
	// BootInitial is the first boot of a slot during pool start.
	BootInitial BootCause = iota
	// BootGuardReplacement replaces a worker that exited for guard_state_violation.
	BootGuardReplacement
	// BootHealthyReplacement replaces a worker that exited for any other reason.
	BootHealthyReplacement
	// BootScaleUp is a new slot added after the pool has started.
	BootScaleUp
)

// BootPermit is the breaker's answer for one boot attempt.
type BootPermit struct {
	Allowed bool
	// Probe is true when this boot consumes the half-open trial token.
	Probe bool
}

// GuardRecycleRecorder receives bounded breaker events.
// reason is guard_state_violation or other. result is admitted, success or failure.
type GuardRecycleRecorder interface {
	IncGuardRecycle(workload, reason string)
	IncGuardSuppressed(workload string)
	IncGuardHalfOpen(workload, result string)
}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// GuardRecycleBreaker limits new boots caused by guard_state_violation.
// It never returns a signaled worker to service; the pool owns that.
// The mutex covers only this state. Process I/O stays outside it.
type GuardRecycleBreaker struct {
	workload string
	cfg      config.GuardRecycleSafetyConfig
	now      func() time.Time
	recorder GuardRecycleRecorder

	mu               sync.Mutex
	state            breakerState
	events           []time.Time
	cooldownUntil    time.Time
	halfOpenInFlight int
	probeGeneration  uint64
	probeSuccesses   int
}

// NewGuardRecycleBreaker builds a closed breaker for one workload.
func NewGuardRecycleBreaker(workload string, cfg config.GuardRecycleSafetyConfig, now func() time.Time) *GuardRecycleBreaker {
	if workload == "" {
		workload = "http"
	}
	if now == nil {
		now = time.Now
	}
	if cfg.HalfOpenMaxProbes < 1 {
		cfg.HalfOpenMaxProbes = 1
	}
	return &GuardRecycleBreaker{
		workload: workload,
		cfg:      cfg,
		now:      now,
		state:    breakerClosed,
	}
}

// SetRecorder attaches metrics. Nil disables emission.
func (b *GuardRecycleBreaker) SetRecorder(recorder GuardRecycleRecorder) {
	b.mu.Lock()
	b.recorder = recorder
	b.mu.Unlock()
}

// IsGuardRecycleReason reports the only reason this breaker counts.
func IsGuardRecycleReason(reason string) bool {
	return reason == guardStateViolationReason
}

// NormalizeGuardReason collapses metric labels. Unknown text becomes other.
func NormalizeGuardReason(reason string) string {
	if reason == guardStateViolationReason {
		return guardStateViolationReason
	}
	return "other"
}

// ObserveRecycle counts a finished guard_state_violation recycle.
// The caller has already retired that worker. generation identifies the process.
func (b *GuardRecycleBreaker) ObserveRecycle(reason string, generation uint64) {
	if b == nil || !b.cfg.Enabled || !IsGuardRecycleReason(reason) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.advanceLocked(now)
	b.emitRecycleLocked()

	if b.state == breakerHalfOpen && (b.probeGeneration == 0 || generation == b.probeGeneration) {
		b.failProbeLocked(now)
		return
	}
	if b.state == breakerOpen {
		return
	}
	if b.state == breakerHalfOpen {
		return
	}

	cutoff := now.Add(-b.cfg.Window)
	kept := b.events[:0]
	for _, at := range b.events {
		if !at.Before(cutoff) {
			kept = append(kept, at)
		}
	}
	b.events = append(kept, now)
	if len(b.events) >= b.cfg.MaxRecycles {
		b.openLocked(now)
	}
}

// AllowBoot decides whether a new generation may start.
// Healthy replacements and the initial start are not blocked by an open breaker.
func (b *GuardRecycleBreaker) AllowBoot(cause BootCause) BootPermit {
	if b == nil || !b.cfg.Enabled {
		return BootPermit{Allowed: true}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.advanceLocked(now)

	switch cause {
	case BootInitial, BootHealthyReplacement:
		return BootPermit{Allowed: true}
	case BootScaleUp:
		if b.state != breakerClosed {
			b.emitSuppressedLocked()
			return BootPermit{}
		}
		return BootPermit{Allowed: true}
	case BootGuardReplacement:
		switch b.state {
		case breakerClosed:
			return BootPermit{Allowed: true}
		case breakerOpen:
			b.emitSuppressedLocked()
			return BootPermit{}
		default:
			if b.halfOpenInFlight >= b.cfg.HalfOpenMaxProbes {
				b.emitSuppressedLocked()
				return BootPermit{}
			}
			b.halfOpenInFlight++
			b.emitHalfOpenLocked("admitted")
			return BootPermit{Allowed: true, Probe: true}
		}
	default:
		b.emitSuppressedLocked()
		return BootPermit{}
	}
}

// NoteBoot records the generation that holds the half-open token.
func (b *GuardRecycleBreaker) NoteBoot(permit BootPermit, generation uint64) {
	if b == nil || !permit.Probe {
		return
	}
	b.mu.Lock()
	b.probeGeneration = generation
	b.probeSuccesses = 0
	b.mu.Unlock()
}

// NoteProbeFailed reopens the breaker after a half-open boot does not come up.
func (b *GuardRecycleBreaker) NoteProbeFailed() {
	if b == nil || !b.cfg.Enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != breakerHalfOpen {
		return
	}
	b.failProbeLocked(b.now())
}

// NoteHealthyRequest counts a finished request on the half-open probe worker.
// Recovery requires both the request count and the minimum uptime. A live PID is not enough.
func (b *GuardRecycleBreaker) NoteHealthyRequest(generation uint64, started time.Time) {
	if b == nil || !b.cfg.Enabled || generation == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.advanceLocked(now)
	if b.state != breakerHalfOpen || generation != b.probeGeneration {
		return
	}
	b.probeSuccesses++
	if b.probeSuccesses < b.cfg.RecoverySuccessfulRequests {
		return
	}
	if now.Before(started.Add(b.cfg.RecoveryMinUptime)) {
		return
	}
	b.state = breakerClosed
	b.events = nil
	b.halfOpenInFlight = 0
	b.probeGeneration = 0
	b.probeSuccesses = 0
	b.emitHalfOpenLocked("success")
}

// State returns closed, open or half_open and applies a due cooldown transition.
func (b *GuardRecycleBreaker) State() string {
	if b == nil || !b.cfg.Enabled {
		return "closed"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.advanceLocked(b.now())
	return b.stateNameLocked()
}

func (b *GuardRecycleBreaker) advanceLocked(now time.Time) {
	if b.state == breakerOpen && !now.Before(b.cooldownUntil) {
		b.state = breakerHalfOpen
		b.halfOpenInFlight = 0
		b.probeGeneration = 0
		b.probeSuccesses = 0
	}
}

func (b *GuardRecycleBreaker) openLocked(now time.Time) {
	b.state = breakerOpen
	b.cooldownUntil = now.Add(b.cfg.Cooldown)
	b.halfOpenInFlight = 0
	b.probeGeneration = 0
	b.probeSuccesses = 0
	b.events = nil
}

func (b *GuardRecycleBreaker) failProbeLocked(now time.Time) {
	b.emitHalfOpenLocked("failure")
	b.openLocked(now)
}

func (b *GuardRecycleBreaker) stateNameLocked() string {
	switch b.state {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

func (b *GuardRecycleBreaker) emitRecycleLocked() {
	if b.recorder == nil {
		return
	}
	// Recorder must not call back into the breaker. It only updates atomics.
	b.recorder.IncGuardRecycle(b.workload, guardStateViolationReason)
}

func (b *GuardRecycleBreaker) emitSuppressedLocked() {
	if b.recorder == nil {
		return
	}
	b.recorder.IncGuardSuppressed(b.workload)
}

func (b *GuardRecycleBreaker) emitHalfOpenLocked(result string) {
	if b.recorder == nil {
		return
	}
	switch result {
	case "admitted", "success", "failure":
	default:
		result = "failure"
	}
	b.recorder.IncGuardHalfOpen(b.workload, result)
}
