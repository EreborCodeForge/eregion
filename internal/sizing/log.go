package sizing

import (
	"log/slog"

	"github.com/EreborCodeForge/Eregion/internal/resources"
)

// LogRuntimeResources emits startup diagnostics for detected resources and sizing.
// Does not log hostnames, pod names, cgroup paths, or environment variables.
func LogRuntimeResources(logger *slog.Logger, r resources.RuntimeResources, s WorkerSizing) {
	if logger == nil {
		return
	}

	attrs := []any{
		"cpu_logical", r.LogicalCPUs,
		"cpu_available", r.AvailableCPUs,
		"gomaxprocs", r.GOMAXPROCS,
		"cpu_detection_source", string(r.CPUSource),
		"cpu_quota_detected", r.CPUQuotaDetected,
		"cpu_set_detected", r.CPUSetDetected,
		"environment", r.Environment,
	}
	if r.CgroupVersion > 0 {
		attrs = append(attrs, "cgroup_version", r.CgroupVersion)
	}
	if r.MemoryLimitKnown {
		memMB := float64(r.MemoryLimitBytes) / (1024 * 1024)
		attrs = append(attrs, "memory_limit_mb", memMB)
	}
	logger.Info("runtime resources detected", attrs...)

	sizingAttrs := []any{
		"workers_configured", s.ConfiguredWorkers,
		"workers_per_cpu", s.WorkersPerCPU,
		"workers_recommended", s.RecommendedWorkers,
		"workers_recommended_min", s.RecommendedMin,
		"workers_recommended_max", s.RecommendedMax,
		"classification", s.Classification,
	}

	if s.Warning {
		logger.Warn("worker pool may be oversized for available CPU",
			append(sizingAttrs,
				"cpu_available", s.AvailableCPUs,
			)...,
		)
		return
	}

	logger.Info("worker pool sizing", sizingAttrs...)

	if s.WorkersPerCPU < 1.0 {
		logger.Info("worker pool is configured conservatively for available CPU",
			"workers_configured", s.ConfiguredWorkers,
			"cpu_available", s.AvailableCPUs,
			"workers_per_cpu", s.WorkersPerCPU,
		)
	}
}

// LogMemoryEnvelope logs an optional informational envelope when container memory
// and per-worker memory_limit_mb are both known. Never fails startup.
// The envelope is a configured theoretical upper bound (workers × memory_limit_mb),
// not measured RSS or reserved memory.
func LogMemoryEnvelope(logger *slog.Logger, r resources.RuntimeResources, configuredWorkers, memoryLimitMB int) {
	if logger == nil || !r.MemoryLimitKnown || memoryLimitMB <= 0 || configuredWorkers < 1 {
		return
	}
	envelopeBytes := uint64(configuredWorkers) * uint64(memoryLimitMB) * 1024 * 1024
	logger.Info("worker memory envelope estimate",
		"workers_configured", configuredWorkers,
		"worker_memory_limit_mb", memoryLimitMB,
		"envelope_bytes", envelopeBytes,
		"cgroup_memory_limit_bytes", r.MemoryLimitBytes,
		"envelope_exceeds_cgroup", envelopeBytes > r.MemoryLimitBytes,
	)
}
