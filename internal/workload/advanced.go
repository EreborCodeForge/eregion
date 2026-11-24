package workload

import (
	"fmt"
	"math"
	"time"
)

// PlatformCaps is what this process can measure. A missing capability is permanent,
// distinct from a sample that has not arrived yet.
type PlatformCaps struct {
	WorkerCPU bool
}

// ValidateAdvanced checks ECO-11 strategy blocks. Legacy strategies reject those blocks.
func ValidateAdvanced(spec ResolvedWorkloadSpec, guardMode string, caps PlatformCaps) error {
	name := spec.Name
	s := spec.Scaling
	if !IsAdvanced(s.Strategy) {
		if s.Control.Configured || s.CPU.Configured || s.Latency.Configured || s.Throughput.Configured || s.Scheduled.Configured || s.Adaptive.Configured {
			return fmt.Errorf("workload %q: scaling blocks for a new strategy require that strategy", name)
		}
		return nil
	}
	switch guardMode {
	case "observe", "enforce":
	case "off":
		return fmt.Errorf("workload %q: strategy %s requires resource_guard mode observe or enforce", name, s.Strategy)
	default:
		return fmt.Errorf("workload %q: strategy %s requires a resolved resource_guard mode", name, s.Strategy)
	}
	if s.Strategy == StrategyAdaptive && guardMode != "enforce" {
		return fmt.Errorf("workload %q: strategy adaptive requires resource_guard mode enforce", name)
	}
	if spec.Workers.Max < 1 {
		return fmt.Errorf("workload %q: workers.max must be >= 1", name)
	}
	if err := validateControl(name, s); err != nil {
		return err
	}
	switch s.Strategy {
	case StrategyCPUUtilization:
		if spec.Workers.Min < 1 {
			return fmt.Errorf("workload %q: cpu_utilization requires workers.min >= 1", name)
		}
		if !caps.WorkerCPU {
			return fmt.Errorf("workload %q: cpu_utilization requires workload CPU attribution, which this platform does not provide", name)
		}
		if err := validateCPU(name, s.CPU, true); err != nil {
			return err
		}
		if err := rejectBlocks(name, s, blockSet{cpu: true}); err != nil {
			return err
		}
	case StrategyLatency:
		if spec.Mode != ModeHTTP {
			return fmt.Errorf("workload %q: latency is HTTP only", name)
		}
		if spec.Workers.Min < 1 {
			return fmt.Errorf("workload %q: latency requires workers.min >= 1", name)
		}
		if err := validateLatency(name, s.Latency, true); err != nil {
			return err
		}
		if err := rejectBlocks(name, s, blockSet{latency: true}); err != nil {
			return err
		}
	case StrategyThroughput:
		if spec.Mode != ModeHTTP {
			return fmt.Errorf("workload %q: throughput is HTTP only", name)
		}
		if spec.Workers.Min < 1 {
			return fmt.Errorf("workload %q: throughput requires workers.min >= 1", name)
		}
		if err := validateThroughput(name, s.Throughput, true); err != nil {
			return err
		}
		if err := rejectBlocks(name, s, blockSet{throughput: true}); err != nil {
			return err
		}
	case StrategyHybrid:
		if spec.Mode == ModeHTTP {
			if spec.Workers.Min < 1 {
				return fmt.Errorf("workload %q: hybrid HTTP requires workers.min >= 1", name)
			}
			if s.CPU.Configured {
				if err := validateCPU(name, s.CPU, true); err != nil {
					return err
				}
			}
			if s.Latency.Configured {
				if err := validateLatency(name, s.Latency, true); err != nil {
					return err
				}
			}
			if s.Throughput.Configured {
				if err := validateThroughput(name, s.Throughput, true); err != nil {
					return err
				}
			}
			if err := rejectBlocks(name, s, blockSet{cpu: true, latency: true, throughput: true}); err != nil {
				return err
			}
		} else {
			if s.BacklogSource.Type != "http_json" || s.BacklogSource.URLEnv == "" {
				return fmt.Errorf("workload %q: hybrid consumer requires backlog_source.type=http_json and url_env", name)
			}
			if err := rejectBlocks(name, s, blockSet{cpu: true}); err != nil {
				return err
			}
			if s.CPU.Configured {
				if err := validateCPU(name, s.CPU, false); err != nil {
					return err
				}
			}
		}
	case StrategyScheduled:
		if spec.Mode == ModeHTTP && spec.Workers.Min < 1 {
			return fmt.Errorf("workload %q: scheduled HTTP requires workers.min >= 1", name)
		}
		if err := validateScheduled(spec); err != nil {
			return err
		}
		if err := rejectBlocks(name, s, blockSet{}); err != nil {
			return err
		}
	case StrategyAdaptive:
		if spec.Mode != ModeHTTP {
			return fmt.Errorf("workload %q: adaptive is HTTP only", name)
		}
		if spec.Workers.Min < 1 {
			return fmt.Errorf("workload %q: adaptive requires workers.min >= 1", name)
		}
		if !caps.WorkerCPU {
			return fmt.Errorf("workload %q: adaptive requires workload CPU attribution, which this platform does not provide", name)
		}
		if err := validateAdaptive(name, s); err != nil {
			return err
		}
		if err := rejectBlocks(name, s, blockSet{}); err != nil {
			return err
		}
	}
	return nil
}

type blockSet struct {
	cpu, latency, throughput bool
}

func rejectBlocks(name string, s ScalingPolicy, allow blockSet) error {
	if s.CPU.Configured && !allow.cpu {
		return fmt.Errorf("workload %q: scaling.cpu is not valid for strategy %s", name, s.Strategy)
	}
	if s.Latency.Configured && !allow.latency {
		return fmt.Errorf("workload %q: scaling.latency is not valid for strategy %s", name, s.Strategy)
	}
	if s.Throughput.Configured && !allow.throughput {
		return fmt.Errorf("workload %q: scaling.throughput is not valid for strategy %s", name, s.Strategy)
	}
	if s.Scheduled.Configured && s.Strategy != StrategyScheduled {
		return fmt.Errorf("workload %q: scaling.scheduled is only valid for strategy scheduled", name)
	}
	if s.Adaptive.Configured && s.Strategy != StrategyAdaptive {
		return fmt.Errorf("workload %q: scaling.adaptive is only valid for strategy adaptive", name)
	}
	return nil
}

func validateControl(name string, s ScalingPolicy) error {
	c := s.Control
	if c.Window < 10*time.Second || c.Window > 120*time.Second || c.Window%time.Second != 0 {
		return fmt.Errorf("workload %q: scaling.control.window must be an integer number of seconds in 10..120", name)
	}
	if c.MaxSignalAge < c.Window || c.MaxSignalAge > 3*c.Window {
		return fmt.Errorf("workload %q: scaling.control.max_signal_age must be between window and 3*window", name)
	}
	if c.UpConfirmations < 1 || c.UpConfirmations > 6 {
		return fmt.Errorf("workload %q: scaling.control.up_confirmations must be 1..6", name)
	}
	if c.DownConfirmations < 1 || c.DownConfirmations > 12 {
		return fmt.Errorf("workload %q: scaling.control.down_confirmations must be 1..12", name)
	}
	if c.MaxStepUp < 1 || c.MaxStepUp > 16 || c.MaxStepDown < 1 || c.MaxStepDown > 16 {
		return fmt.Errorf("workload %q: scaling.control max_step_up/down must be 1..16", name)
	}
	if s.IdleSet && s.ScaleDownIdleFor < c.Window {
		return fmt.Errorf("workload %q: scale_down_idle_for must be >= window when set", name)
	}
	if s.CooldownSet && s.ScaleUpCooldown <= 0 {
		return fmt.Errorf("workload %q: scale_up_cooldown must be positive", name)
	}
	return nil
}

func validateCPU(name string, cpu CPUPolicy, required bool) error {
	if !cpu.Configured {
		if required {
			return fmt.Errorf("workload %q: scaling.cpu is required", name)
		}
		return nil
	}
	if cpu.TargetRatio <= 0 || cpu.TargetRatio > 1 || math.IsNaN(cpu.TargetRatio) {
		return fmt.Errorf("workload %q: cpu.target_ratio must be in (0,1]", name)
	}
	limit := math.Min(cpu.TargetRatio, 1-cpu.TargetRatio)
	if cpu.ToleranceRatio <= 0 || cpu.ToleranceRatio >= limit || math.IsNaN(cpu.ToleranceRatio) {
		return fmt.Errorf("workload %q: cpu.tolerance_ratio must be in (0, min(target, 1-target))", name)
	}
	return nil
}

func validateLatency(name string, lat LatencyPolicy, required bool) error {
	if !lat.Configured {
		if required {
			return fmt.Errorf("workload %q: scaling.latency is required", name)
		}
		return nil
	}
	if lat.TargetP95 < time.Millisecond || lat.TargetP95 > 60*time.Second {
		return fmt.Errorf("workload %q: latency.target_p95 must be from 1ms to 60s", name)
	}
	if lat.QueueWaitTargetP95 <= 0 || lat.QueueWaitTargetP95 > lat.TargetP95 {
		return fmt.Errorf("workload %q: latency.queue_wait_target_p95 must be positive and <= target_p95", name)
	}
	if lat.ToleranceRatio <= 0 || lat.ToleranceRatio > 0.5 || math.IsNaN(lat.ToleranceRatio) {
		return fmt.Errorf("workload %q: latency.tolerance_ratio must be in (0,0.5]", name)
	}
	if lat.BusyThreshold <= 0 || lat.BusyThreshold > 1 || math.IsNaN(lat.BusyThreshold) {
		return fmt.Errorf("workload %q: latency.busy_threshold must be in (0,1]", name)
	}
	return nil
}

func validateThroughput(name string, tp ThroughputPolicy, required bool) error {
	if !tp.Configured || tp.TargetRPSPerWorker <= 0 || math.IsNaN(tp.TargetRPSPerWorker) || math.IsInf(tp.TargetRPSPerWorker, 0) || tp.TargetRPSPerWorker > 1e9 {
		if required || tp.Configured {
			return fmt.Errorf("workload %q: throughput.target_rps_per_worker must be a finite number in (0,1e9]", name)
		}
	}
	if !tp.Configured {
		return nil
	}
	if tp.ToleranceRatio <= 0 || tp.ToleranceRatio > 0.5 || math.IsNaN(tp.ToleranceRatio) {
		return fmt.Errorf("workload %q: throughput.tolerance_ratio must be in (0,0.5]", name)
	}
	return nil
}

func validateScheduled(spec ResolvedWorkloadSpec) error {
	s := spec.Scaling.Scheduled
	if !s.Configured {
		return fmt.Errorf("workload %q: scaling.scheduled is required", spec.Name)
	}
	if s.Timezone != "UTC" {
		return fmt.Errorf("workload %q: scheduled.timezone must be UTC", spec.Name)
	}
	if s.DefaultWorkers < spec.Workers.Min || s.DefaultWorkers > spec.Workers.Max {
		return fmt.Errorf("workload %q: scheduled.default_workers must be within workers min..max", spec.Name)
	}
	if len(s.Windows) > 32 {
		return fmt.Errorf("workload %q: scheduled.windows supports at most 32 entries", spec.Name)
	}
	type span struct{ day, start, end int }
	var spans []span
	for i, w := range s.Windows {
		if len(w.Days) == 0 {
			return fmt.Errorf("workload %q: scheduled.windows[%d].days is required", spec.Name, i)
		}
		if w.Target < spec.Workers.Min || w.Target > spec.Workers.Max {
			return fmt.Errorf("workload %q: scheduled.windows[%d].target_workers must be within workers min..max", spec.Name, i)
		}
		if w.StartMin < 0 || w.EndMin > 24*60 || w.StartMin >= w.EndMin {
			return fmt.Errorf("workload %q: scheduled.windows[%d] must be a non-empty same-day interval", spec.Name, i)
		}
		seenDay := map[string]bool{}
		for _, day := range w.Days {
			if seenDay[day] {
				return fmt.Errorf("workload %q: scheduled.windows[%d] repeats %s", spec.Name, i, day)
			}
			seenDay[day] = true
			n, ok := weekdayIndex(day)
			if !ok {
				return fmt.Errorf("workload %q: scheduled.windows[%d] has unknown day %q", spec.Name, i, day)
			}
			spans = append(spans, span{n, w.StartMin, w.EndMin})
		}
	}
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[i].day != spans[j].day {
				continue
			}
			if spans[i].start < spans[j].end && spans[j].start < spans[i].end {
				return fmt.Errorf("workload %q: scheduled windows overlap", spec.Name)
			}
		}
	}
	return nil
}

func weekdayIndex(day string) (int, bool) {
	switch day {
	case "mon":
		return 0, true
	case "tue":
		return 1, true
	case "wed":
		return 2, true
	case "thu":
		return 3, true
	case "fri":
		return 4, true
	case "sat":
		return 5, true
	case "sun":
		return 6, true
	default:
		return 0, false
	}
}

func validateAdaptive(name string, s ScalingPolicy) error {
	a := s.Adaptive
	if !a.Configured {
		return fmt.Errorf("workload %q: scaling.adaptive is required", name)
	}
	if a.MinGainRatio < 0.01 || a.MinGainRatio > 0.5 || math.IsNaN(a.MinGainRatio) {
		return fmt.Errorf("workload %q: adaptive.min_gain_ratio must be 0.01..0.5", name)
	}
	if a.MaxP95RegressionRatio < 0 || a.MaxP95RegressionRatio > 0.5 || math.IsNaN(a.MaxP95RegressionRatio) {
		return fmt.Errorf("workload %q: adaptive.max_p95_regression_ratio must be 0..0.5", name)
	}
	if a.DemandToleranceRatio < 0.01 || a.DemandToleranceRatio > 0.30 || math.IsNaN(a.DemandToleranceRatio) {
		return fmt.Errorf("workload %q: adaptive.demand_tolerance_ratio must be 0.01..0.30", name)
	}
	if a.BaselineWindows < 2 || a.BaselineWindows > 6 || a.EvaluationWindows < 2 || a.EvaluationWindows > 6 {
		return fmt.Errorf("workload %q: adaptive baseline_windows and evaluation_windows must be 2..6", name)
	}
	if a.SettleWindows < 1 || a.SettleWindows > 3 {
		return fmt.Errorf("workload %q: adaptive.settle_windows must be 1..3", name)
	}
	if a.ProbeCooldown < s.Control.Window {
		return fmt.Errorf("workload %q: adaptive.probe_cooldown must be >= window", name)
	}
	need := time.Duration(a.SettleWindows+a.EvaluationWindows)*s.Control.Window + s.ScaleUpCooldown
	if a.ProbeTimeout < need {
		return fmt.Errorf("workload %q: adaptive.probe_timeout must cover settle, evaluation and scale-up cooldown", name)
	}
	if a.BusyThreshold <= 0 || a.BusyThreshold > 1 {
		return fmt.Errorf("workload %q: adaptive.busy_threshold must be in (0,1]", name)
	}
	return nil
}

// ApplyAdvancedDefaults fills omitted ECO-11 control knobs without changing legacy defaults.
func ApplyAdvancedDefaults(s ScalingPolicy) ScalingPolicy {
	if !IsAdvanced(s.Strategy) {
		return s
	}
	if !s.Control.Configured {
		s.Control.Window = 10 * time.Second
		s.Control.MaxSignalAge = 20 * time.Second
		s.Control.UpConfirmations = 2
		s.Control.DownConfirmations = 3
		s.Control.MaxStepUp = 1
		s.Control.MaxStepDown = 1
		s.Control.Configured = true
	} else {
		if s.Control.Window == 0 {
			s.Control.Window = 10 * time.Second
		}
		if s.Control.MaxSignalAge == 0 {
			s.Control.MaxSignalAge = 2 * s.Control.Window
		}
		if s.Control.UpConfirmations == 0 {
			s.Control.UpConfirmations = 2
		}
		if s.Control.DownConfirmations == 0 {
			s.Control.DownConfirmations = 3
		}
		if s.Control.MaxStepUp == 0 {
			s.Control.MaxStepUp = 1
		}
		if s.Control.MaxStepDown == 0 {
			s.Control.MaxStepDown = 1
		}
	}
	if !s.CooldownSet {
		s.ScaleUpCooldown = 10 * time.Second
	}
	if !s.IdleSet {
		s.ScaleDownIdleFor = 30 * time.Second
	}
	if s.Strategy == StrategyCPUUtilization && !s.CPU.Configured {
		s.CPU = CPUPolicy{TargetRatio: 0.65, ToleranceRatio: 0.10, Configured: true}
	}
	if s.Strategy == StrategyLatency && !s.Latency.Configured {
		s.Latency = LatencyPolicy{
			TargetP95: 100 * time.Millisecond, QueueWaitTargetP95: 10 * time.Millisecond,
			ToleranceRatio: 0.15, BusyThreshold: 0.85, Configured: true,
		}
	}
	if s.Strategy == StrategyAdaptive && !s.Adaptive.Configured {
		s.Adaptive = defaultAdaptive(s.Control.Window)
	}
	if s.Strategy == StrategyAdaptive && s.Adaptive.Configured && s.Adaptive.BusyThreshold == 0 {
		s.Adaptive.BusyThreshold = 0.85
	}
	return s
}

func defaultAdaptive(window time.Duration) AdaptivePolicy {
	if window == 0 {
		window = 10 * time.Second
	}
	return AdaptivePolicy{
		MinGainRatio:          0.03,
		MaxP95RegressionRatio: 0.10,
		DemandToleranceRatio:  0.10,
		BaselineWindows:       3,
		EvaluationWindows:     3,
		SettleWindows:         1,
		ProbeCooldown:         60 * time.Second,
		ProbeTimeout:          180 * time.Second,
		BusyThreshold:         0.85,
		Configured:            true,
	}
}
