package scaling

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
)

// Warner deduplicates guard logs and applies alert hysteresis.
type Warner struct {
	Window time.Duration
	Clock  resources.Clock

	mu          sync.Mutex
	last        map[string]warnEmit
	memLevel    RiskSeverity
	belowWarn   int
	belowCrit   int
	cpuWarning  bool
	cpuCrit     bool
	cpuCool     int
	cpuWarnCool int
}

type warnEmit struct {
	at       time.Time
	severity RiskSeverity
}

// ObserveSample updates latched alert levels. Hard-cap decisions are not delayed here.
func (w *Warner) ObserveSample(sample resources.Sample, warning, critical float64, cpuWarn, cpuCrit float64) RiskSeverity {
	if w == nil {
		return SeverityOK
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if sample.MemoryKnown() && sample.MemoryLimitBytes > 0 {
		ratio := float64(sample.MemoryCurrentBytes) / float64(sample.MemoryLimitBytes)
		switch {
		case ratio >= critical:
			w.memLevel = SeverityCritical
			w.belowCrit = 0
			w.belowWarn = 0
		case ratio >= warning:
			if w.memLevel == SeverityCritical {
				w.belowCrit++
				if w.belowCrit >= 3 && ratio < critical-0.05 {
					w.memLevel = SeverityWarning
					w.belowCrit = 0
				}
			} else {
				w.memLevel = SeverityWarning
				w.belowWarn = 0
			}
		default:
			if w.memLevel == SeverityCritical || w.memLevel == SeverityWarning {
				if ratio < warning-0.05 {
					w.belowWarn++
				} else {
					w.belowWarn = 0
				}
				if w.belowWarn >= 3 && (w.memLevel != SeverityCritical || w.belowCrit >= 3 || ratio < critical-0.05) {
					w.memLevel = SeverityOK
					w.belowWarn = 0
					w.belowCrit = 0
				}
			}
		}
	}
	if resources.Known(sample.CPUThrottlingValid) && sample.CPUThrottledRatio >= cpuCrit && cpuCrit > 0 {
		w.cpuCrit = true
		w.cpuCool = 0
	} else if w.cpuCrit && resources.Known(sample.CPUThrottlingValid) && sample.CPUThrottledRatio < cpuCrit*0.9 {
		w.cpuCool++
		if w.cpuCool >= 3 {
			w.cpuCrit = false
			w.cpuCool = 0
		}
	}
	if resources.Known(sample.CPUUtilizationValid) && sample.CPUUtilization >= cpuWarn && cpuWarn > 0 {
		w.cpuWarning = true
		w.cpuWarnCool = 0
	} else if w.cpuWarning && resources.Known(sample.CPUUtilizationValid) && sample.CPUUtilization < cpuWarn*0.9 {
		w.cpuWarnCool++
		if w.cpuWarnCool >= 3 {
			w.cpuWarning = false
			w.cpuWarnCool = 0
		}
	}
	level := w.memLevel
	if w.cpuCrit && worse(SeverityCritical, level) {
		level = SeverityCritical
	} else if w.cpuWarning && worse(SeverityWarning, level) {
		level = SeverityWarning
	}
	return level
}

// Log emits one structured record per workload/reason/severity inside the dedupe window.
// A worse severity is emitted immediately. Returns true when a record was written.
func (w *Warner) Log(logger *slog.Logger, mode string, decision CapacityDecision, sample resources.Sample) bool {
	if w == nil || logger == nil {
		return false
	}
	if decision.ConstrainedBy == ReasonNone && decision.Severity == SeverityOK {
		return false
	}
	now := time.Now()
	if w.Clock != nil {
		now = w.Clock.Now()
	}
	key := decision.Workload + "|" + string(decision.ConstrainedBy) + "|" + string(decision.Severity)
	w.mu.Lock()
	if w.last == nil {
		w.last = make(map[string]warnEmit)
	}
	prev, ok := w.last[key]
	if ok && !worse(decision.Severity, prev.severity) && w.Window > 0 && now.Sub(prev.at) < w.Window {
		w.mu.Unlock()
		return false
	}
	w.last[key] = warnEmit{at: now, severity: decision.Severity}
	w.mu.Unlock()

	logger.Log(context.Background(), slog.LevelWarn, "scaling capacity limited",
		"event", "scaling_capacity_limited",
		"component", "resource_guard",
		"workload", decision.Workload,
		"strategy", decision.Strategy,
		"mode", mode,
		"severity", string(decision.Severity),
		"reason", string(decision.ConstrainedBy),
		"requested_workers", decision.Requested,
		"admitted_workers", decision.Admitted,
		"running_workers", decision.Current,
		"memory_limit_bytes", sample.MemoryLimitBytes,
		"memory_current_bytes", sample.MemoryCurrentBytes,
		"sample_valid", decision.SampleValid,
		"recommendation", decision.Recommendation,
	)
	return true
}

func worse(next, prev RiskSeverity) bool {
	return severityRank(next) > severityRank(prev)
}

func severityRank(s RiskSeverity) int {
	switch s {
	case SeverityWarning:
		return 1
	case SeverityCritical:
		return 2
	case SeverityDegraded:
		return 3
	default:
		return 0
	}
}
