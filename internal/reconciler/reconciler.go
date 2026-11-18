package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Reconciler compares desired workload specs to live pools and scales.
type Reconciler struct {
	registry   *workload.Registry
	pools      *worker.Manager
	strategies *scaling.Registry
	runtime    resources.RuntimeResources
	logger     *slog.Logger
	interval   time.Duration
	hysteresis map[string]*scaling.Hysteresis
	guard      *scaling.Guard
}

// New creates a reconciler.
func New(
	registry *workload.Registry,
	pools *worker.Manager,
	strategies *scaling.Registry,
	runtime resources.RuntimeResources,
	logger *slog.Logger,
) *Reconciler {
	return &Reconciler{
		registry:   registry,
		pools:      pools,
		strategies: strategies,
		runtime:    runtime,
		logger:     logger,
		interval:   time.Second,
		hysteresis: make(map[string]*scaling.Hysteresis),
	}
}

// Run loops until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.ReconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.ReconcileOnce(ctx)
		}
	}
}

// SetGuard attaches the resource safety controller. Nil or mode off keeps the legacy reconcile loop.
func (r *Reconciler) SetGuard(g *scaling.Guard) {
	if r == nil {
		return
	}
	r.guard = g
}

// ReconcileOnce performs a single reconcile pass.
func (r *Reconciler) ReconcileOnce(ctx context.Context) {
	if r.guard == nil || r.guard.Mode() == "off" {
		r.reconcileLegacy(ctx)
		return
	}
	r.reconcileGuarded(ctx)
}

func (r *Reconciler) reconcileLegacy(ctx context.Context) {
	desired := r.registry.List()
	seen := make(map[string]struct{}, len(desired))

	for _, spec := range desired {
		seen[spec.Name] = struct{}{}
		pool, err := r.pools.Ensure(spec)
		if err != nil {
			r.logger.Error("ensure pool failed", "workload", spec.Name, "error", err)
			continue
		}

		snap := pool.Snapshot()
		metrics := workload.WorkloadMetrics{
			Desired:  snap.Desired,
			Running:  snap.Running,
			Idle:     snap.Idle,
			Busy:     snap.Busy,
			Draining: snap.Draining,
			Failed:   snap.Failed,
		}

		inner := r.strategies.For(spec.Scaling.Strategy)
		h, ok := r.hysteresis[spec.Name]
		if !ok {
			h = scaling.NewHysteresis(inner)
			r.hysteresis[spec.Name] = h
		} else {
			h.Inner = inner
		}

		target := h.DesiredWorkers(spec, metrics, r.runtime)
		if target != snap.Desired {
			r.logger.Info("reconciling workers",
				"workload", spec.Name,
				"from", snap.Desired,
				"to", target,
				"strategy", spec.Scaling.Strategy,
			)
			pool.SetDesired(target)
		}
	}

	// Remove pools no longer in registry.
	for _, pool := range r.pools.List() {
		if _, ok := seen[pool.Name()]; ok {
			continue
		}
		r.logger.Info("removing workload pool", "workload", pool.Name())
		_ = r.pools.Remove(ctx, pool.Name())
		delete(r.hysteresis, pool.Name())
	}
}

func (r *Reconciler) reconcileGuarded(ctx context.Context) {
	specs := r.registry.List()
	seen := make(map[string]struct{}, len(specs))
	demands := make([]scaling.WorkloadDemand, 0, len(specs))
	pools := make(map[string]*worker.Pool, len(specs))

	for _, spec := range specs {
		seen[spec.Name] = struct{}{}
		pool, err := r.pools.Ensure(spec)
		if err != nil {
			r.logger.Error("ensure pool failed", "workload", spec.Name, "error", err)
			continue
		}
		pools[spec.Name] = pool
		snap := pool.Snapshot()
		metrics := workload.WorkloadMetrics{
			Desired:  snap.Desired,
			Running:  snap.Running,
			Idle:     snap.Idle,
			Busy:     snap.Busy,
			Draining: snap.Draining,
			Failed:   snap.Failed,
		}
		inner := r.strategies.For(spec.Scaling.Strategy)
		h, ok := r.hysteresis[spec.Name]
		if !ok {
			h = scaling.NewHysteresis(inner)
			r.hysteresis[spec.Name] = h
		} else {
			h.Inner = inner
		}
		ev, policy := h.Decide(spec, metrics, r.runtime)
		demands = append(demands, scaling.WorkloadDemand{
			Name:      spec.Name,
			Strategy:  string(spec.Scaling.Strategy),
			Min:       spec.Workers.Min,
			Max:       spec.Workers.Max,
			Requested: ev.RawDemand,
			Policy:    policy,
			Running:   snap.Running,
			Starting:  snap.Starting,
			MemoryMB:  spec.Resources.MemoryMB,
		})
	}

	decisions, _ := r.guard.Decide(ctx, demands, false)
	byName := make(map[string]scaling.CapacityDecision, len(decisions))
	for _, decision := range decisions {
		byName[decision.Workload] = decision
	}
	for name, pool := range pools {
		decision, ok := byName[name]
		if !ok {
			continue
		}
		snap := pool.Snapshot()
		if r.guard.Mode() == "enforce" {
			if decision.Admitted != snap.Desired {
				r.logger.Info("reconciling workers",
					"workload", name,
					"from", snap.Desired,
					"to", decision.Admitted,
					"strategy", decision.Strategy,
					"reason", string(decision.ConstrainedBy),
				)
			}
			pool.SetAdmitted(decision.Admitted)
			continue
		}
		if decision.PolicyTarget != snap.Desired {
			r.logger.Info("reconciling workers",
				"workload", name,
				"from", snap.Desired,
				"to", decision.PolicyTarget,
				"strategy", decision.Strategy,
			)
			pool.SetDesired(decision.PolicyTarget)
		}
	}

	for _, pool := range r.pools.List() {
		if _, ok := seen[pool.Name()]; ok {
			continue
		}
		r.logger.Info("removing workload pool", "workload", pool.Name())
		_ = r.pools.Remove(ctx, pool.Name())
		delete(r.hysteresis, pool.Name())
	}
}
