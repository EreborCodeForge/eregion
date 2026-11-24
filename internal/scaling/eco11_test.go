package scaling_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestECO11T13CPUHighRequestsSixThenSteps(t *testing.T) {
	spec := cpuSpec()
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{Value: 0.90, Valid: true}
	w.Epoch = 1
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 6 {
		t.Fatalf("raw=%d", got.RawRequested)
	}
	c := scaling.NewController()
	now := time.Unix(1_700_000_000, 0)
	first := c.Apply(spec, got, pool, now, false, "")
	if first.PolicyRequested != 4 || first.Reason != signals.ReasonConfirming {
		t.Fatalf("first=%+v", first)
	}
	w.Epoch = 2
	second := scaling.EvaluateDemand(spec, w, pool, now)
	got2 := c.Apply(spec, second, pool, now.Add(10*time.Second), false, "")
	if got2.PolicyRequested != 5 {
		t.Fatalf("step=%+v", got2)
	}
}

func TestECO11T14CPULowStepsDown(t *testing.T) {
	spec := cpuSpec()
	spec.Scaling.Control.DownConfirmations = 1
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{Value: 0.30, Valid: true}
	w.Epoch = 1
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 2 {
		t.Fatalf("raw=%d", got.RawRequested)
	}
	c := scaling.NewController()
	now := time.Unix(1_700_000_000, 0)
	w.Epoch = 1
	_ = c.Apply(spec, got, pool, now, false, "")
	w.Epoch = 2
	again := scaling.EvaluateDemand(spec, w, pool, now)
	out := c.Apply(spec, again, pool, now.Add(31*time.Second), false, "")
	if out.PolicyRequested != 3 {
		t.Fatalf("down=%+v", out)
	}
}

func TestECO11T15GlobalCPUVetoDoesNotShrink(t *testing.T) {
	spec := cpuSpec()
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{Value: 0.90, Valid: true}
	w.ResourceCPURatio = signals.Sample[float64]{Value: 0.95, Valid: true}
	w.Epoch = 3
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	raw := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	veto, why := scaling.ExpansionVeto(spec.Scaling.Strategy, spec.Mode, w)
	out := scaling.NewController().Apply(spec, raw, pool, time.Unix(100, 0), veto, why)
	if out.PolicyRequested != 4 || out.Reason != signals.ReasonCPUSaturation {
		t.Fatalf("%+v", out)
	}
}

func TestECO11T16ConsumerCPUWithoutBacklogDoesNotScaleDown(t *testing.T) {
	spec := cpuSpec()
	spec.Mode = workload.ModeConsumer
	spec.Command = []string{"php"}
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{Value: 0.10, Valid: true}
	w.BacklogDepth = signals.Sample[uint64]{}
	pool := scaling.PoolStability{Desired: 2, Running: 2}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 2 || got.Reason != signals.ReasonConsumerDownSignalMissing {
		t.Fatalf("%+v", got)
	}
	w.BacklogDepth = signals.Sample[uint64]{Value: 0, Valid: true}
	got = scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 1 {
		t.Fatalf("fresh zero backlog raw=%d", got.RawRequested)
	}
}

func TestECO11T18LatencyPressureStepsUp(t *testing.T) {
	spec := latencySpec()
	w := baseWindow()
	w.RequestP95Seconds = signals.Sample[float64]{Value: 0.160, Valid: true}
	w.RequestP95Low, w.RequestP95High = 0.15, 0.160
	w.QueueWaitP95Seconds = signals.Sample[float64]{Value: 0.040, Valid: true}
	w.BusyRatio = signals.Sample[float64]{Value: 0.95, Valid: true}
	w.QueueWaitingMax = signals.Sample[uint64]{Value: 2, Valid: true}
	pool := scaling.PoolStability{Desired: 3, Running: 3}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 4 {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T20LatencyLowWithUnknownQueueHolds(t *testing.T) {
	spec := latencySpec()
	w := baseWindow()
	w.RequestP95Seconds = signals.Sample[float64]{Value: 0.020, Valid: true}
	w.RequestP95Low, w.RequestP95High = 0.015, 0.020
	w.BusyRatio = signals.Sample[float64]{Value: 0.20, Valid: true}
	w.QueueWaitingMax = signals.Sample[uint64]{}
	w.QueueRejects = signals.Sample[uint64]{}
	pool := scaling.PoolStability{Desired: 3, Running: 3}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 3 || got.Reason == signals.ReasonDemandDown {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T19LatencyWithoutQueueHolds(t *testing.T) {
	spec := latencySpec()
	w := baseWindow()
	w.RequestP95Seconds = signals.Sample[float64]{Value: 0.160, Valid: true}
	w.RequestP95Low, w.RequestP95High = 0.15, 0.160
	pool := scaling.PoolStability{Desired: 3, Running: 3}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 3 || got.Reason != signals.ReasonLatencyWithoutQueuePressure {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T21ThroughputUsesOffered(t *testing.T) {
	spec := throughputSpec()
	w := baseWindow()
	w.OfferedRPS = signals.Sample[float64]{Value: 650, Valid: true}
	w.CompletedRPS = signals.Sample[float64]{Value: 500, Valid: true}
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 6 {
		t.Fatalf("raw=%d", got.RawRequested)
	}
}

func TestECO11T23CooldownDoesNotCrossWorkloads(t *testing.T) {
	httpSpec := scheduleSpec()
	api := scheduleSpec()
	api.Name = "api"
	when := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	raw := scaling.EvaluateDemand(httpSpec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, when)
	pool := scaling.PoolStability{Desired: 2, Running: 2}
	c := scaling.NewController()
	first := c.Apply(httpSpec, raw, pool, when, false, "")
	second := c.Apply(api, raw, pool, when, false, "")
	if first.PolicyRequested != 3 || second.PolicyRequested != 3 {
		t.Fatalf("http %+v api %+v", first, second)
	}
	held := c.Apply(httpSpec, raw, scaling.PoolStability{Desired: 3, Running: 3}, when.Add(time.Second), false, "")
	if held.PolicyRequested != 3 || held.Reason != signals.ReasonCooldown {
		t.Fatalf("http cooldown %+v", held)
	}
}

func TestECO11T24LocalRejectsDoNotVeto(t *testing.T) {
	spec := throughputSpec()
	w := baseWindow()
	w.OfferedRPS = signals.Sample[float64]{Value: 800, Valid: true}
	w.HTTPErrorRatio = signals.Sample[float64]{Value: 0, Valid: true}
	w.QueueRejects = signals.Sample[uint64]{Value: 40, Valid: true}
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	raw := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	veto, _ := scaling.ExpansionVeto(spec.Scaling.Strategy, spec.Mode, w)
	if veto || raw.RawRequested <= 4 {
		t.Fatalf("veto=%v raw=%+v", veto, raw)
	}
	w.HTTPErrorRatio = signals.Sample[float64]{Value: 0.08, Valid: true}
	veto, why := scaling.ExpansionVeto(spec.Scaling.Strategy, spec.Mode, w)
	out := scaling.NewController().Apply(spec, raw, pool, time.Unix(10, 0), veto, why)
	if out.PolicyRequested != 4 || out.Reason != signals.ReasonNonlocalErrorPressure {
		t.Fatalf("%+v", out)
	}
}

func TestECO11T25HybridTakesMaxUpNotSum(t *testing.T) {
	spec := hybridHTTPSpec()
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{Value: 0.90, Valid: true}
	w.RequestP95Seconds = signals.Sample[float64]{Value: 0.160, Valid: true}
	w.RequestP95Low, w.RequestP95High = 0.15, 0.160
	w.QueueWaitP95Seconds = signals.Sample[float64]{Value: 0.04, Valid: true}
	w.BusyRatio = signals.Sample[float64]{Value: 0.95, Valid: true}
	w.QueueWaitingMax = signals.Sample[uint64]{Value: 1, Valid: true}
	w.OfferedRPS = signals.Sample[float64]{Value: 650, Valid: true}
	pool := scaling.PoolStability{Desired: 4, Running: 4}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 6 {
		t.Fatalf("raw=%d want max(6,5) not a sum", got.RawRequested)
	}
}

func TestECO11T26HybridQueueUpWhenCPUAndP95Missing(t *testing.T) {
	spec := hybridHTTPSpec()
	w := baseWindow()
	w.WorkerCPURatio = signals.Sample[float64]{}
	w.RequestP95Seconds = signals.Sample[float64]{}
	w.OfferedRPS = signals.Sample[float64]{}
	w.BusyRatio = signals.Sample[float64]{Value: 0.90, Valid: true}
	w.QueueWaitingMax = signals.Sample[uint64]{Value: 4, Valid: true}
	w.QueueRejects = signals.Sample[uint64]{Valid: true}
	pool := scaling.PoolStability{Desired: 3, Running: 3}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 4 || got.Reason != signals.ReasonDemandUp {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T27IdleHTTPStepsDownWithoutFakeP95(t *testing.T) {
	spec := hybridHTTPSpec()
	w := baseWindow()
	w.OfferedRPS = signals.Sample[float64]{Value: 0, Valid: true}
	w.BusyRatio = signals.Sample[float64]{Value: 0, Valid: true}
	w.RequestP95Seconds = signals.Sample[float64]{}
	w.QueueWaitingMax = signals.Sample[uint64]{Valid: true}
	w.QueueRejects = signals.Sample[uint64]{Valid: true}
	pool := scaling.PoolStability{Desired: 3, Running: 3}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T28HybridConsumerBacklog(t *testing.T) {
	spec := hybridConsumerSpec()
	w := baseWindow()
	w.BacklogDepth = signals.Sample[uint64]{Value: 80, Valid: true}
	pool := scaling.PoolStability{Desired: 2, Running: 2}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 4 {
		t.Fatalf("raw=%d", got.RawRequested)
	}
}

func TestECO11T29MinZeroWakesOnBacklog(t *testing.T) {
	spec := hybridConsumerSpec()
	spec.Workers.Min = 0
	w := baseWindow()
	w.BacklogDepth = signals.Sample[uint64]{Value: 3, Valid: true}
	pool := scaling.PoolStability{Desired: 0, Running: 0}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested < 1 {
		t.Fatalf("%+v", got)
	}
	w.BacklogDepth = signals.Sample[uint64]{Valid: true}
	pool = scaling.PoolStability{Desired: 1, Running: 1}
	got = scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.RawRequested != 0 {
		t.Fatalf("measured zero = %+v", got)
	}
}

func TestECO11T30StaleBacklogHolds(t *testing.T) {
	spec := hybridConsumerSpec()
	w := baseWindow()
	w.BacklogDepth = signals.Sample[uint64]{Reason: signals.ReasonStale}
	pool := scaling.PoolStability{Desired: 2, Running: 2}
	got := scaling.EvaluateDemand(spec, w, pool, time.Unix(0, 0))
	if got.Valid || got.RawRequested != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestECO11T35ScheduleEdgesAreHalfOpen(t *testing.T) {
	spec := scheduleSpec()
	utc := time.Date(2026, 11, 16, 11, 30, 0, 0, time.UTC) // Monday
	got := scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, utc)
	if got.RawRequested != 6 || got.Reason != signals.ReasonScheduleWindow {
		t.Fatalf("start %+v", got)
	}
	end := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	got = scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, end)
	if got.RawRequested != 2 || got.Reason != signals.ReasonScheduleDefault {
		t.Fatalf("end %+v", got)
	}
	sunday := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	got = scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, sunday)
	if got.RawRequested != 2 {
		t.Fatalf("sunday %+v", got)
	}
}

func TestECO11T37ScheduleClockJumpDoesNotReplay(t *testing.T) {
	spec := scheduleSpec()
	inside := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	got := scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, inside)
	if got.RawRequested != 6 {
		t.Fatalf("inside %+v", got)
	}
	jumped := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	got = scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 6, Running: 6}, jumped)
	if got.RawRequested != 2 || got.Reason != signals.ReasonScheduleDefault {
		t.Fatalf("jump %+v", got)
	}
	consumer := spec
	consumer.Mode = workload.ModeConsumer
	consumer.Workers.Min = 0
	consumer.Scaling.Scheduled.DefaultWorkers = 0
	got = scaling.EvaluateDemand(consumer, signals.SignalWindow{}, scaling.PoolStability{}, inside)
	if got.RawRequested != 6 {
		t.Fatalf("consumer wake %+v", got)
	}
}

func TestECO11T38ScheduleStepAndVeto(t *testing.T) {
	spec := scheduleSpec()
	spec.Scaling.Control.MaxStepUp = 1
	raw := scaling.EvaluateDemand(spec, signals.SignalWindow{}, scaling.PoolStability{Desired: 2, Running: 2}, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC))
	c := scaling.NewController()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	stepped := c.Apply(spec, raw, scaling.PoolStability{Desired: 2, Running: 2}, now, false, "")
	if stepped.PolicyRequested != 3 {
		t.Fatalf("step %+v", stepped)
	}
	held := c.Apply(spec, raw, scaling.PoolStability{Desired: 3, Running: 3}, now.Add(time.Second), false, "")
	if held.PolicyRequested != 3 || held.Reason != signals.ReasonCooldown {
		t.Fatalf("cooldown %+v", held)
	}
	vetoed := c.Apply(spec, raw, scaling.PoolStability{Desired: 3, Running: 3}, now.Add(time.Minute), true, signals.ReasonCPUSaturation)
	if vetoed.PolicyRequested != 3 {
		t.Fatalf("veto %+v", vetoed)
	}
}

func TestECO11T39AdaptiveAcceptsGain(t *testing.T) {
	spec := adaptiveSpec()
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	poolN := 4
	for i := 0; i < 3; i++ {
		out := hub.Step("http", []string{"http"}, spec, probeView(poolN, 1000, 1400, 0.040, true), now)
		now = now.Add(10 * time.Second)
		if i < 2 && out.RawRequested != poolN {
			t.Fatalf("baseline %d %+v", i, out)
		}
		if i == 2 && out.Reason != signals.ReasonProbeRequested {
			t.Fatalf("request %+v", out)
		}
	}
	running := probeView(5, 1080, 1400, 0.042, true)
	running.Running = 5
	running.Admitted = 5
	running.Desired = 5
	_ = hub.Step("http", []string{"http"}, spec, running, now) // settle
	now = now.Add(10 * time.Second)
	var last signals.DemandDecision
	for i := 0; i < 5; i++ {
		last = hub.Step("http", []string{"http"}, spec, running, now)
		now = now.Add(10 * time.Second)
		if last.Reason == signals.ReasonProbeGain {
			break
		}
	}
	if last.Reason != signals.ReasonProbeGain || last.RawRequested != 5 {
		t.Fatalf("accept %+v", last)
	}
}

func TestECO11T40AdaptiveRollsBackWeakGain(t *testing.T) {
	spec := adaptiveSpec()
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		_ = hub.Step("http", []string{"http"}, spec, probeView(4, 1000, 1400, 0.040, true), now)
		now = now.Add(10 * time.Second)
	}
	running := probeView(4, 1005, 1400, 0.052, true)
	running.Desired, running.Running, running.Admitted = 5, 5, 5
	_ = hub.Step("http", []string{"http"}, spec, running, now)
	now = now.Add(10 * time.Second)
	var last signals.DemandDecision
	for i := 0; i < 5; i++ {
		last = hub.Step("http", []string{"http"}, spec, running, now)
		now = now.Add(10 * time.Second)
		if last.Reason == signals.ReasonProbeNoGain {
			break
		}
	}
	if last.Reason != signals.ReasonProbeNoGain || last.RawRequested != 4 {
		t.Fatalf("rollback %+v", last)
	}
}

func TestECO11T41OfferedShiftIsConfounded(t *testing.T) {
	spec := adaptiveSpec()
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		_ = hub.Step("http", []string{"http"}, spec, probeView(4, 1000, 1400, 0.040, true), now)
		now = now.Add(10 * time.Second)
	}
	shifted := probeView(4, 1400, 1750, 0.040, true)
	shifted.Desired, shifted.Running, shifted.Admitted = 5, 5, 5
	_ = hub.Step("http", []string{"http"}, spec, shifted, now)
	now = now.Add(10 * time.Second)
	var got signals.DemandDecision
	for i := 0; i < 3; i++ {
		got = hub.Step("http", []string{"http"}, spec, shifted, now)
		now = now.Add(10 * time.Second)
		if got.Reason == signals.ReasonProbeConfounded {
			return
		}
	}
	t.Fatalf("%+v", got)
}

func TestECO11T42BlockedProbeDoesNotSucceed(t *testing.T) {
	spec := adaptiveSpec()
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	var requested signals.DemandDecision
	for i := 0; i < 3; i++ {
		requested = hub.Step("http", []string{"http"}, spec, probeView(4, 1000, 1400, 0.040, true), now)
		now = now.Add(10 * time.Second)
	}
	if requested.Reason != signals.ReasonProbeRequested {
		t.Fatalf("setup %+v", requested)
	}
	blocked := probeView(4, 1000, 1400, 0.040, true)
	blocked.GuardLimited = true
	blocked.Admitted = 4
	got := hub.Step("http", []string{"http"}, spec, blocked, now)
	if got.Reason != signals.ReasonProbeGuardBlocked || got.RawRequested != 4 {
		t.Fatalf("%+v", got)
	}
	if _, ok := hub.Gain("http"); ok {
		t.Fatal("blocked probe recorded a gain")
	}
}

func TestECO11T43ProbeTimeoutRollsBackWithoutGain(t *testing.T) {
	spec := adaptiveSpec()
	spec.Scaling.Adaptive.ProbeTimeout = 30 * time.Second
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	var requested signals.DemandDecision
	for i := 0; i < 3; i++ {
		requested = hub.Step("http", []string{"http"}, spec, probeView(4, 1000, 1400, 0.040, true), now)
		now = now.Add(10 * time.Second)
	}
	if requested.Reason != signals.ReasonProbeRequested {
		t.Fatalf("setup %+v", requested)
	}
	stuck := probeView(4, 1000, 1400, 0.040, true)
	got := hub.Step("http", []string{"http"}, spec, stuck, now.Add(spec.Scaling.Adaptive.ProbeTimeout+time.Second))
	if got.Reason != signals.ReasonProbeTimeout || got.RawRequested != 4 {
		t.Fatalf("%+v", got)
	}
	if _, ok := hub.Gain("http"); ok {
		t.Fatal("timed out probe recorded a gain")
	}
}

func TestECO11T45OneProbeAtATime(t *testing.T) {
	spec := adaptiveSpec()
	other := spec
	other.Name = "api"
	hub := scaling.NewProbeHub()
	now := time.Unix(1_700_000_000, 0)
	view := probeView(4, 1000, 1400, 0.040, true)
	var first, second signals.DemandDecision
	for i := 0; i < 3; i++ {
		first = hub.Step("api", []string{"api", "http"}, other, view, now)
		second = hub.Step("http", []string{"api", "http"}, spec, view, now)
		now = now.Add(10 * time.Second)
	}
	if first.Reason == signals.ReasonProbeRequested && second.Reason == signals.ReasonProbeRequested {
		t.Fatal("two probes started together")
	}
	if first.Reason != signals.ReasonProbeRequested {
		t.Fatalf("fair first %+v second %+v", first, second)
	}
}

func cpuSpec() workload.ResolvedWorkloadSpec {
	s := workload.ResolvedWorkloadSpec{
		Name: "http", Mode: workload.ModeHTTP,
		Workers: workload.WorkerPolicy{Min: 1, Max: 8},
		Scaling: workload.ApplyAdvancedDefaults(workload.ScalingPolicy{Strategy: workload.StrategyCPUUtilization}),
	}
	s.Scaling.CPU = workload.CPUPolicy{TargetRatio: 0.65, ToleranceRatio: 0.10, Configured: true}
	return s
}

func latencySpec() workload.ResolvedWorkloadSpec {
	s := cpuSpec()
	s.Scaling.Strategy = workload.StrategyLatency
	s.Scaling = workload.ApplyAdvancedDefaults(s.Scaling)
	s.Scaling.Latency = workload.LatencyPolicy{
		TargetP95: 100 * time.Millisecond, QueueWaitTargetP95: 10 * time.Millisecond,
		ToleranceRatio: 0.15, BusyThreshold: 0.85, Configured: true,
	}
	return s
}

func throughputSpec() workload.ResolvedWorkloadSpec {
	s := latencySpec()
	s.Scaling.Strategy = workload.StrategyThroughput
	s.Scaling.Throughput = workload.ThroughputPolicy{TargetRPSPerWorker: 120, ToleranceRatio: 0.10, Configured: true}
	return s
}

func hybridHTTPSpec() workload.ResolvedWorkloadSpec {
	s := throughputSpec()
	s.Scaling.Strategy = workload.StrategyHybrid
	s.Scaling.CPU = workload.CPUPolicy{TargetRatio: 0.65, ToleranceRatio: 0.10, Configured: true}
	return s
}

func hybridConsumerSpec() workload.ResolvedWorkloadSpec {
	s := workload.ResolvedWorkloadSpec{
		Name: "email", Mode: workload.ModeConsumer, Command: []string{"php"},
		Workers: workload.WorkerPolicy{Min: 0, Max: 8},
		Scaling: workload.ApplyAdvancedDefaults(workload.ScalingPolicy{
			Strategy: workload.StrategyHybrid, AvgJobDuration: 500 * time.Millisecond, TargetDrainTime: 10 * time.Second,
		}),
	}
	return s
}

func scheduleSpec() workload.ResolvedWorkloadSpec {
	s := workload.ResolvedWorkloadSpec{
		Name: "http", Mode: workload.ModeHTTP,
		Workers: workload.WorkerPolicy{Min: 1, Max: 8},
		Scaling: workload.ApplyAdvancedDefaults(workload.ScalingPolicy{Strategy: workload.StrategyScheduled}),
	}
	s.Scaling.Scheduled = workload.ScheduledPolicy{
		Timezone: "UTC", DefaultWorkers: 2, Configured: true,
		Windows: []workload.ScheduleWindow{{
			Days: []string{"mon", "tue", "wed", "thu", "fri"}, StartMin: 11*60 + 30, EndMin: 14 * 60, Target: 6,
		}},
	}
	return s
}

func adaptiveSpec() workload.ResolvedWorkloadSpec {
	s := workload.ResolvedWorkloadSpec{
		Name: "http", Mode: workload.ModeHTTP,
		Workers: workload.WorkerPolicy{Min: 1, Max: 8},
		Scaling: workload.ApplyAdvancedDefaults(workload.ScalingPolicy{Strategy: workload.StrategyAdaptive}),
	}
	return s
}

func baseWindow() signals.SignalWindow {
	return signals.SignalWindow{
		Epoch:             1,
		OfferedRPS:        signals.Sample[float64]{Value: 100, Valid: true},
		CompletedRPS:      signals.Sample[float64]{Value: 100, Valid: true},
		SuccessfulRPS:     signals.Sample[float64]{Value: 100, Valid: true},
		RequestP95Seconds: signals.Sample[float64]{Value: 0.04, Valid: true},
		RequestP95Low:     0.025, RequestP95High: 0.04,
		QueueWaitP95Seconds: signals.Sample[float64]{Value: 0, Valid: true},
		QueueWaitingMax:     signals.Sample[uint64]{Valid: true},
		QueueRejects:        signals.Sample[uint64]{Valid: true},
		HTTPErrorRatio:      signals.Sample[float64]{Valid: true},
		BusyRatio:           signals.Sample[float64]{Value: 0.2, Valid: true},
		WorkerCPURatio:      signals.Sample[float64]{Value: 0.5, Valid: true},
		ResourceCPURatio:    signals.Sample[float64]{Value: 0.4, Valid: true},
		CPUThrottleRatio:    signals.Sample[float64]{Valid: true},
		GlobalFresh:         true,
	}
}

func probeView(desired int, success, offered, p95 float64, pressure bool) scaling.ProbeView {
	w := baseWindow()
	w.SuccessfulRPS = signals.Sample[float64]{Value: success, Valid: true}
	w.OfferedRPS = signals.Sample[float64]{Value: offered, Valid: true}
	w.RequestP95Seconds = signals.Sample[float64]{Value: p95, Valid: true}
	w.BusyRatio = signals.Sample[float64]{Value: 0.90, Valid: true}
	if pressure {
		w.QueueWaitingMax = signals.Sample[uint64]{Value: 2, Valid: true}
	}
	return scaling.ProbeView{
		Window: w, WindowOK: true, Desired: desired, Running: desired, Admitted: desired,
		Generation: 1, Quota: "100", Source: "cgroup", ConfigGen: 1,
	}
}
