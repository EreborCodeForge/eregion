package scaling

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/telemetry"
	"github.com/EreborCodeForge/Eregion/internal/worker"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Guard is the process-wide resource safety controller.
// Strategies still compute demand. Guard only admits it.
type Guard struct {
	Cfg       config.ResourceGuardConfig
	Collector *resources.Collector
	Ledger    *Ledger
	Warner    *Warner
	Logger    *slog.Logger
	Clock     resources.Clock
	Metrics   *telemetry.Registry
	Managed   func() (uint64, bool)
}

// Mode returns the configured guard mode.
func (g *Guard) Mode() string {
	if g == nil || g.Cfg.Mode == "" {
		return config.ResourceGuardObserve
	}
	return g.Cfg.Mode
}

// Sample reads the cached collector, collecting once when the cache is empty.
func (g *Guard) Sample(ctx context.Context) resources.Sample {
	now := time.Now()
	if g != nil && g.Clock != nil {
		now = g.Clock.Now()
	}
	if g == nil || g.Collector == nil {
		return g.overlay(resources.UnknownSample(now))
	}
	if !g.Collector.Have() {
		fresh, _ := g.Collector.CollectOnce(ctx)
		return g.overlay(fresh)
	}
	return g.overlay(g.Collector.Current(now))
}

func (g *Guard) overlay(sample resources.Sample) resources.Sample {
	if g == nil {
		return sample
	}
	var managed uint64
	known := false
	if g.Managed != nil {
		managed, known = g.Managed()
	}
	return resources.ApplyLocalCeiling(sample, g.Cfg.Memory.HardLimitMB, managed, known)
}

// Preflight blocks StartAll in enforce when the minimums are not provably safe.
func (g *Guard) Preflight(ctx context.Context, specs []workload.ResolvedWorkloadSpec, pools *worker.Manager) error {
	if g == nil || g.Mode() != config.ResourceGuardEnforce {
		return nil
	}
	sample := g.forceSample(ctx)
	demands := make([]WorkloadDemand, 0, len(specs))
	for _, spec := range specs {
		running, starting, desired := 0, 0, spec.Workers.Min
		if pools != nil {
			if pool, ok := pools.Get(spec.Name); ok {
				snap := pool.Snapshot()
				running = snap.Running
				starting = snap.Starting
				desired = snap.Desired
			}
		}
		demands = append(demands, WorkloadDemand{
			Name:      spec.Name,
			Strategy:  string(spec.Scaling.Strategy),
			Min:       spec.Workers.Min,
			Max:       spec.Workers.Max,
			Requested: desired,
			Policy:    desired,
			Running:   running,
			Starting:  starting,
			MemoryMB:  spec.Resources.MemoryMB,
		})
	}
	now := sample.At
	if g.Clock != nil {
		now = g.Clock.Now()
	}
	if !memoryUsable(sample, now, g.Cfg.MaxSampleAge) {
		g.publish(sample, nil, now)
		return ErrResourceLimitUnknown
	}
	decisions := g.decide(sample, demands, now, true)
	for _, d := range decisions {
		if d.Safe < d.Min {
			g.publish(sample, decisions, now)
			return fmt.Errorf("%w: workload %s requested %d admitted %d", ErrInsufficientCapacity, d.Workload, d.Requested, d.Safe)
		}
	}
	g.armLedger(sample, demands, now)
	if pools != nil {
		for _, d := range decisions {
			if pool, ok := pools.Get(d.Workload); ok {
				pool.SetAdmitted(d.Safe)
			}
		}
	}
	g.publish(sample, decisions, now)
	return nil
}

// Decide plans one reconcile cycle. The caller applies the returned targets.
func (g *Guard) Decide(ctx context.Context, demands []WorkloadDemand, startup bool) ([]CapacityDecision, resources.Sample) {
	sample := g.Sample(ctx)
	now := sample.At
	if g != nil && g.Clock != nil {
		now = g.Clock.Now()
	}
	if g == nil {
		return nil, sample
	}
	decisions := g.decide(sample, demands, now, startup)
	g.armLedger(sample, demands, now)
	g.publish(sample, decisions, now)
	return decisions, sample
}

func (g *Guard) forceSample(ctx context.Context) resources.Sample {
	if g == nil || g.Collector == nil {
		now := time.Now()
		if g != nil && g.Clock != nil {
			now = g.Clock.Now()
		}
		return g.overlay(resources.UnknownSample(now))
	}
	sample, _ := g.Collector.CollectOnce(ctx)
	return g.overlay(sample)
}

func (g *Guard) decide(sample resources.Sample, demands []WorkloadDemand, now time.Time, startup bool) []CapacityDecision {
	pending := uint64(0)
	if g.Ledger != nil {
		pending = g.Ledger.PendingBytes()
	}
	return Plan(PlanRequest{
		Mode:         g.Mode(),
		Memory:       g.Cfg.Memory,
		CPU:          g.Cfg.CPU,
		Sample:       sample,
		Now:          now,
		MaxSampleAge: g.Cfg.MaxSampleAge,
		PendingBytes: pending,
		Startup:      startup,
		Demands:      demands,
	})
}

func (g *Guard) armLedger(sample resources.Sample, demands []WorkloadDemand, now time.Time) {
	if g.Ledger == nil || g.Mode() != config.ResourceGuardEnforce {
		return
	}
	costs := make(map[string]uint64, len(demands))
	for _, d := range demands {
		costs[d.Name] = IncrementalCost(d.MemoryMB, d.RSSP95, d.RSSKnown, g.Cfg.Memory.WorkerFallbackMB)
	}
	g.Ledger.SetEconomics(sample, costs, g.Cfg.Memory, g.Cfg.MaxSampleAge, now)
}

func (g *Guard) publish(sample resources.Sample, decisions []CapacityDecision, now time.Time) {
	if g == nil {
		return
	}
	level := SeverityOK
	if g.Warner != nil {
		level = g.Warner.ObserveSample(sample, g.Cfg.Memory.WarningRatio, g.Cfg.Memory.CriticalRatio, g.Cfg.CPU.WarningUtilizationRatio, g.Cfg.CPU.CriticalThrottlingRatio)
		for _, d := range decisions {
			if rank := severityRank(d.Severity); rank > severityRank(level) {
				level = d.Severity
			}
			if d.Severity != SeverityOK || d.ConstrainedBy != ReasonNone {
				g.Warner.Log(g.Logger, g.Mode(), d, sample)
			}
		}
	}
	if g.Metrics == nil {
		return
	}
	head, headOK := HeadroomBytes(sample, g.Cfg.Memory, 0)
	if g.Ledger != nil {
		head, headOK = HeadroomBytes(sample, g.Cfg.Memory, g.Ledger.PendingBytes())
	}
	obs := telemetry.GuardObservation{
		Mode:          g.Mode(),
		State:         string(level),
		Sample:        sample,
		Headroom:      head,
		HeadroomKnown: headOK && sample.MemoryKnown(),
		AgeKnown:      !sample.At.IsZero(),
		Horizon:       g.Cfg.Prediction.Horizon.String(),
	}
	if obs.AgeKnown && !now.Before(sample.At) {
		obs.AgeSeconds = now.Sub(sample.At).Seconds()
	}
	obs.Workloads = make([]telemetry.GuardWorkload, 0, len(decisions))
	for _, d := range decisions {
		obs.Workloads = append(obs.Workloads, telemetry.GuardWorkload{
			Name:       d.Workload,
			Requested:  d.Requested,
			Policy:     d.PolicyTarget,
			Admitted:   d.Admitted,
			WouldLimit: d.WouldLimit,
			Limited:    d.Limited,
			Deficit:    d.Deficit,
			Reason:     string(d.ConstrainedBy),
		})
	}
	g.Metrics.SetGuardObservation(obs)
}
