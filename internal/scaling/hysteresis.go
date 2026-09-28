package scaling

import (
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Hysteresis wraps a Strategy with scale-up cooldown and scale-down idle delay.
type Hysteresis struct {
	Inner Strategy

	mu            sync.Mutex
	lastScaleUp   time.Time
	scaleDownSince map[string]time.Time // workload → when desired first dropped
	lastDesired   map[string]int
}

// NewHysteresis wraps inner with per-workload hysteresis state.
func NewHysteresis(inner Strategy) *Hysteresis {
	return &Hysteresis{
		Inner:          inner,
		scaleDownSince: make(map[string]time.Time),
		lastDesired:    make(map[string]int),
	}
}

// DesiredWorkers applies hysteresis around the inner strategy.
func (h *Hysteresis) DesiredWorkers(
	spec workload.ResolvedWorkloadSpec,
	metrics workload.WorkloadMetrics,
	runtime resources.RuntimeResources,
) int {
	raw := h.Inner.DesiredWorkers(spec, metrics, runtime)
	h.mu.Lock()
	defer h.mu.Unlock()

	current := metrics.Desired
	if current == 0 {
		current = metrics.Running
	}
	prev, hadPrev := h.lastDesired[spec.Name]
	if !hadPrev {
		prev = current
	}

	now := time.Now()
	upCooldown := spec.Scaling.ScaleUpCooldown
	if upCooldown <= 0 {
		upCooldown = time.Second
	}
	downIdle := spec.Scaling.ScaleDownIdleFor
	if downIdle <= 0 {
		downIdle = 30 * time.Second
	}

	if raw > prev {
		// Scale up: respect cooldown since last up.
		if !h.lastScaleUp.IsZero() && now.Sub(h.lastScaleUp) < upCooldown {
			raw = prev
		} else if raw > prev {
			h.lastScaleUp = now
			delete(h.scaleDownSince, spec.Name)
		}
	} else if raw < prev {
		// Scale down: require sustained lower desire for ScaleDownIdleFor.
		since, ok := h.scaleDownSince[spec.Name]
		if !ok {
			h.scaleDownSince[spec.Name] = now
			raw = prev
		} else if now.Sub(since) < downIdle {
			raw = prev
		} else {
			delete(h.scaleDownSince, spec.Name)
		}
	} else {
		delete(h.scaleDownSince, spec.Name)
	}

	h.lastDesired[spec.Name] = raw
	return raw
}
