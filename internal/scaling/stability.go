package scaling

import (
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Controller stabilizes ECO-11 demand per workload. It does not spawn.
type Controller struct {
	mu     sync.Mutex
	states map[string]*stab
}

type stab struct {
	policy     int
	prev       int
	ready      bool
	upStreak   int
	downStreak int
	lastEpoch  uint64
	counted    bool
	downSince  time.Time
	lastUp     time.Time
	lastDown   time.Time
}

func NewController() *Controller {
	return &Controller{states: map[string]*stab{}}
}

// Forget drops stabilizer state for a removed workload.
func (c *Controller) Forget(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, name)
}

// Streaks returns the current confirmation streaks.
func (c *Controller) Streaks(name string) (up, down int) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[name]
	if s == nil {
		return 0, 0
	}
	return s.upStreak, s.downStreak
}

// Policy returns the stabilized target, or false before the first decision.
func (c *Controller) Policy(name string) (int, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[name]
	if s == nil || !s.ready {
		return 0, false
	}
	return s.policy, true
}

// Apply turns raw demand into a stepped target. Guard refusal is reverted separately.
func (c *Controller) Apply(spec workload.ResolvedWorkloadSpec, decision signals.DemandDecision, pool PoolStability, now time.Time, veto bool, vetoReason signals.DecisionReason) signals.DemandDecision {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[spec.Name]
	if s == nil {
		s = &stab{}
		c.states[spec.Name] = s
	}
	floor := spec.Workers.Min
	if !s.ready {
		start := pool.N()
		if start < floor {
			start = floor
		}
		s.policy = start
		s.prev = start
		s.ready = true
	}
	current := s.policy
	if !pool.Stable() {
		s.upStreak = 0
		s.downStreak = 0
		s.downSince = time.Time{}
		out := hold(current, signals.ReasonPoolTransition, decision.Epoch, true)
		out.PolicyRequested = current
		return out
	}
	if !decision.Valid {
		s.upStreak = 0
		s.downStreak = 0
		s.downSince = time.Time{}
		s.counted = false
		out := hold(current, decision.Reason, decision.Epoch, false)
		if out.Reason == "" {
			out.Reason = signals.ReasonSignalUnavailable
		}
		out.PolicyRequested = current
		return out
	}
	requested := clip(decision.RawRequested, floor, spec.Workers.Max)
	if veto && requested > current {
		s.upStreak = 0
		out := hold(current, vetoReason, decision.Epoch, true)
		out.PolicyRequested = current
		return out
	}
	if requested == current {
		s.upStreak = 0
		s.downStreak = 0
		s.downSince = time.Time{}
		out := propose(current, current, decision.Epoch, decision.Reason)
		if decision.Reason != "" {
			out.Reason = decision.Reason
		}
		out.PolicyRequested = current
		return out
	}
	if decision.Epoch != 0 && s.counted && decision.Epoch == s.lastEpoch {
		out := hold(current, signals.ReasonConfirming, decision.Epoch, true)
		out.PolicyRequested = current
		return out
	}
	if decision.Epoch != 0 {
		s.lastEpoch = decision.Epoch
		s.counted = true
	}
	window := spec.Scaling.Control.Window
	if window <= 0 {
		window = 10 * time.Second
	}
	upCool := spec.Scaling.ScaleUpCooldown
	if upCool < window {
		upCool = window
	}
	downIdle := spec.Scaling.ScaleDownIdleFor
	if downIdle < 30*time.Second {
		downIdle = 30 * time.Second
	}
	if downIdle < window {
		downIdle = window
	}
	confirmUp := spec.Scaling.Control.UpConfirmations
	confirmDown := spec.Scaling.Control.DownConfirmations
	if spec.Scaling.Strategy == workload.StrategyScheduled || decision.Reason == signals.ReasonProbeRequested {
		confirmUp = 1
		confirmDown = 1
		downIdle = 0
	}
	if requested > current {
		s.downStreak = 0
		s.downSince = time.Time{}
		if !s.lastUp.IsZero() && now.Sub(s.lastUp) < upCool {
			out := hold(current, signals.ReasonCooldown, decision.Epoch, true)
			out.PolicyRequested = current
			return out
		}
		s.upStreak++
		if s.upStreak < confirmUp {
			out := hold(current, signals.ReasonConfirming, decision.Epoch, true)
			out.PolicyRequested = current
			return out
		}
		step := requested - current
		if step > spec.Scaling.Control.MaxStepUp {
			step = spec.Scaling.Control.MaxStepUp
		}
		s.prev = current
		s.policy = current + step
		s.upStreak = 0
		s.lastUp = now
		out := propose(s.policy, current, decision.Epoch, signals.ReasonDemandUp)
		out.RawRequested = decision.RawRequested
		out.PolicyRequested = s.policy
		return out
	}
	s.upStreak = 0
	if s.downSince.IsZero() {
		s.downSince = now
	}
	s.downStreak++
	if s.downStreak < confirmDown || (downIdle > 0 && now.Sub(s.downSince) < downIdle) {
		reason := signals.ReasonConfirming
		if s.downStreak >= confirmDown {
			reason = signals.ReasonCooldown
		}
		out := hold(current, reason, decision.Epoch, true)
		out.PolicyRequested = current
		return out
	}
	step := current - requested
	if step > spec.Scaling.Control.MaxStepDown {
		step = spec.Scaling.Control.MaxStepDown
	}
	s.prev = current
	s.policy = current - step
	s.downStreak = 0
	s.downSince = time.Time{}
	s.lastDown = now
	out := propose(s.policy, current, decision.Epoch, signals.ReasonDemandDown)
	out.RawRequested = decision.RawRequested
	out.PolicyRequested = s.policy
	return out
}

// RevertLast undoes a policy step the guard did not admit.
func (c *Controller) RevertLast(name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.states[name]
	if s == nil {
		return
	}
	s.policy = s.prev
	s.lastUp = time.Time{}
	s.lastDown = time.Time{}
	s.upStreak = 0
	s.downStreak = 0
}

func clip(n, min, max int) int {
	if n < min {
		n = min
	}
	if max > 0 && n > max {
		n = max
	}
	if n < 0 {
		n = 0
	}
	return n
}
