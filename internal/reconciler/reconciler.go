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
	registry  *workload.Registry
	pools     *worker.Manager
	strategies *scaling.Registry
	runtime   resources.RuntimeResources
	logger    *slog.Logger
	interval  time.Duration
	hysteresis map[string]*scaling.Hysteresis
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

// ReconcileOnce performs a single reconcile pass.
func (r *Reconciler) ReconcileOnce(ctx context.Context) {
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
