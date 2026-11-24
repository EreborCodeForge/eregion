package reconciler

import (
	"context"
	"log/slog"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
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
	signals    *signals.Engine
	control    *scaling.Controller
	probes     *scaling.ProbeHub
	backlog    *scaling.HTTPJSONBacklogProvider
	metrics    *telemetry.Registry
	clock      resources.Clock
	lastLog    map[string]string
	warned     bool
	published  map[string]telemetry.ScalingWorkloadPublication
	probeEpoch map[string]uint64
	probeLast  map[string]signals.DemandDecision
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
		control:    scaling.NewController(),
		probes:     scaling.NewProbeHub(),
		signals:    signals.NewEngine(nil, signals.OSReader{}),
		lastLog:    map[string]string{},
		published:  map[string]telemetry.ScalingWorkloadPublication{},
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

// SetBacklog attaches the cached backlog provider. Nil keeps backlog signals unknown.
func (r *Reconciler) SetBacklog(p *scaling.HTTPJSONBacklogProvider) {
	if r == nil {
		return
	}
	r.backlog = p
}

// SetMetrics receives cached scaling publications. Nil skips them.
func (r *Reconciler) SetMetrics(m *telemetry.Registry) {
	if r == nil {
		return
	}
	r.metrics = m
}

// SetClock injects the reconciler clock. Nil uses time.Now.
func (r *Reconciler) SetClock(c resources.Clock) {
	if r == nil {
		return
	}
	r.clock = c
}

func (r *Reconciler) now() time.Time {
	if r != nil && r.clock != nil {
		return r.clock.Now()
	}
	return time.Now()
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
		r.forgetWorkload(pool.Name())
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
	}
	r.observeAdvanced(specs, pools)

	for _, spec := range specs {
		pool := pools[spec.Name]
		if pool == nil {
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
		var raw, policy int
		if workload.IsAdvanced(spec.Scaling.Strategy) {
			raw, policy = r.advancedTarget(spec, pool, snap)
		} else {
			ev, legacy := h.Decide(spec, metrics, r.runtime)
			raw, policy = ev.RawDemand, legacy
		}
		demands = append(demands, scaling.WorkloadDemand{
			Name:      spec.Name,
			Strategy:  string(spec.Scaling.Strategy),
			Min:       spec.Workers.Min,
			Max:       spec.Workers.Max,
			Requested: raw,
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
			if workload.IsAdvanced(workload.ScalingStrategyName(decision.Strategy)) && decision.Admitted < decision.PolicyTarget {
				r.control.RevertLast(name)
			}
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
		r.forgetWorkload(pool.Name())
	}
}

func (r *Reconciler) forgetWorkload(name string) {
	delete(r.hysteresis, name)
	delete(r.lastLog, name)
	if r.control != nil {
		r.control.Forget(name)
	}
	if r.signals != nil {
		r.signals.Forget(name)
	}
	if r.probes != nil {
		r.probes.Forget(name)
	}
	delete(r.published, name)
	delete(r.probeEpoch, name)
	delete(r.probeLast, name)
}
