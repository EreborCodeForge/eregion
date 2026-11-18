package config

import (
	"fmt"
	"strings"
	"time"
)

// Validate checks the resolved configuration.
func (c *Config) Validate() error {
	if c.Version != "" && c.Version != "1" {
		return fmt.Errorf("unsupported config version %q", c.Version)
	}
	if c.Server.Host == "" {
		return fmt.Errorf("server.host is required")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if c.Server.MaxHeaderBytes <= 0 {
		return fmt.Errorf("server.max_header_bytes must be positive")
	}
	if c.Server.MaxBodyBytes <= 0 {
		return fmt.Errorf("server.max_body_bytes must be positive")
	}
	if c.Server.ReadHeaderTimeout <= 0 || c.Server.ReadTimeout <= 0 ||
		c.Server.WriteTimeout <= 0 || c.Server.IdleTimeout <= 0 ||
		c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server timeouts must be positive")
	}

	if strings.TrimSpace(c.PHP.Binary) == "" {
		return fmt.Errorf("php.binary is required")
	}
	if strings.TrimSpace(c.PHP.WorkerScript) == "" {
		return fmt.Errorf("php.worker_script is required")
	}
	if strings.TrimSpace(c.PHP.WorkingDirectory) == "" {
		return fmt.Errorf("php.working_directory is required")
	}

	if c.Workers.Count < 1 {
		return fmt.Errorf("workers.count must be at least 1")
	}
	if c.Workers.MinReady < 0 || c.Workers.MinReady > c.Workers.Count {
		return fmt.Errorf("workers.min_ready must be between 0 and workers.count")
	}
	if c.Workers.MaxRequests < 0 {
		return fmt.Errorf("workers.max_requests must be >= 0")
	}
	if c.Workers.MemoryLimitMB < 0 {
		return fmt.Errorf("workers.memory_limit_mb must be >= 0")
	}
	if c.Workers.StartupTimeout <= 0 || c.Workers.HandshakeTimeout <= 0 ||
		c.Workers.RequestTimeout <= 0 || c.Workers.AcquireTimeout <= 0 ||
		c.Workers.ShutdownTimeout <= 0 {
		return fmt.Errorf("worker timeouts must be positive")
	}
	if c.Workers.RestartLimit < 0 {
		return fmt.Errorf("workers.restart_limit must be >= 0")
	}
	if c.Workers.RestartWindow <= 0 {
		return fmt.Errorf("workers.restart_window must be positive")
	}
	b := c.Workers.RestartBackoff
	if b.Initial <= 0 || b.Maximum <= 0 {
		return fmt.Errorf("workers.restart_backoff initial/maximum must be positive")
	}
	if b.Multiplier < 1 {
		return fmt.Errorf("workers.restart_backoff.multiplier must be >= 1")
	}
	if b.Jitter < 0 || b.Jitter > 1 {
		return fmt.Errorf("workers.restart_backoff.jitter must be between 0 and 1")
	}

	if strings.TrimSpace(c.Socket.Directory) == "" {
		return fmt.Errorf("socket.directory is required")
	}

	if c.Protocol.Version != 1 {
		return fmt.Errorf("protocol.version must be 1")
	}
	if c.Protocol.MaxFrameBytes == 0 {
		return fmt.Errorf("protocol.max_frame_bytes must be positive")
	}
	if int64(c.Protocol.MaxFrameBytes) <= c.Server.MaxBodyBytes {
		return fmt.Errorf("protocol.max_frame_bytes must be greater than server.max_body_bytes")
	}

	if c.Queue.Capacity < 0 {
		return fmt.Errorf("queue.capacity must be >= 0")
	}
	if c.Queue.RetryAfter < 0 {
		return fmt.Errorf("queue.retry_after must be >= 0")
	}

	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	switch strings.ToLower(c.Logging.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("logging.format must be text or json")
	}

	if c.Operations.Prefix == "" {
		return fmt.Errorf("operations.prefix is required")
	}
	if !strings.HasPrefix(c.Operations.Prefix, "/") {
		return fmt.Errorf("operations.prefix must start with /")
	}

	switch c.Metrics.Access {
	case "", "loopback", "public":
	default:
		return fmt.Errorf("metrics.access must be loopback or public")
	}
	if err := c.ResolveEndpoints(); err != nil {
		return err
	}
	if err := validateEndpointCollisions(*c); err != nil {
		return err
	}

	if _, err := c.ResolveWorkloads(); err != nil {
		return fmt.Errorf("workloads: %w", err)
	}
	if err := c.validateGuardRecycle(); err != nil {
		return err
	}
	if err := c.validateResourceGuard(); err != nil {
		return err
	}

	return nil
}

func (c *Config) validateGuardRecycle() error {
	g := c.WorkerGuardRecycle
	if g.MaxRecycles < 1 || g.MaxRecycles > 100 {
		return fmt.Errorf("worker_guard_recycle_safety.max_recycles must be between 1 and 100")
	}
	if g.Window < time.Second || g.Window > 10*time.Minute {
		return fmt.Errorf("worker_guard_recycle_safety.window must be between 1s and 10m")
	}
	if g.Cooldown < time.Second || g.Cooldown > 30*time.Minute {
		return fmt.Errorf("worker_guard_recycle_safety.cooldown must be between 1s and 30m")
	}
	if g.HalfOpenMaxProbes < 1 || g.HalfOpenMaxProbes > 5 {
		return fmt.Errorf("worker_guard_recycle_safety.half_open_max_probes must be between 1 and 5")
	}
	if g.RecoverySuccessfulRequests < 1 || g.RecoverySuccessfulRequests > 10000 {
		return fmt.Errorf("worker_guard_recycle_safety.recovery_successful_requests must be between 1 and 10000")
	}
	if g.RecoveryMinUptime < 0 || g.RecoveryMinUptime > time.Hour {
		return fmt.Errorf("worker_guard_recycle_safety.recovery_min_uptime must be between 0 and 1h")
	}
	return nil
}

// Resource guard modes. Absent configuration uses observe.
const (
	ResourceGuardOff     = "off"
	ResourceGuardObserve = "observe"
	ResourceGuardEnforce = "enforce"
)

// ResourceGuardConfig is the global CPU/memory safety policy.
type ResourceGuardConfig struct {
	Mode           string
	SampleInterval time.Duration
	MaxSampleAge   time.Duration
	Memory         MemoryGuardConfig
	CPU            CPUGuardConfig
	Prediction     PredictionGuardConfig
	Warnings       WarningGuardConfig
}

// MemoryGuardConfig is the shared memory budget.
type MemoryGuardConfig struct {
	ReserveMB        int
	HardLimitMB      int
	ReservePercent   int
	WorkerFallbackMB int
	WarningRatio     float64
	CriticalRatio    float64
	HardRatio        float64
}

// CPUGuardConfig holds soft CPU thresholds. CPU never becomes an OOM kill signal.
type CPUGuardConfig struct {
	WarningUtilizationRatio float64
	CriticalThrottlingRatio float64
}

// PredictionGuardConfig configures the conservative memory projection.
type PredictionGuardConfig struct {
	Enabled    bool
	Horizon    time.Duration
	MinSamples int
}

// WarningGuardConfig deduplicates guard logs.
type WarningGuardConfig struct {
	DedupeWindow time.Duration
}

// DefaultResourceGuard returns the compatibility defaults from ECO-09.
func DefaultResourceGuard() ResourceGuardConfig {
	return ResourceGuardConfig{
		Mode:           ResourceGuardObserve,
		SampleInterval: time.Second,
		MaxSampleAge:   5 * time.Second,
		Memory: MemoryGuardConfig{
			ReserveMB:        256,
			HardLimitMB:      0,
			ReservePercent:   10,
			WorkerFallbackMB: 256,
			WarningRatio:     0.80,
			CriticalRatio:    0.90,
			HardRatio:        0.95,
		},
		CPU: CPUGuardConfig{
			WarningUtilizationRatio: 0.85,
			CriticalThrottlingRatio: 0.20,
		},
		Prediction: PredictionGuardConfig{
			Enabled:    true,
			Horizon:    60 * time.Second,
			MinSamples: 10,
		},
		Warnings: WarningGuardConfig{DedupeWindow: 60 * time.Second},
	}
}

func (c *Config) validateResourceGuard() error {
	g := c.ResourceGuard
	switch g.Mode {
	case ResourceGuardOff, ResourceGuardObserve, ResourceGuardEnforce:
	default:
		return fmt.Errorf("resource_guard.mode must be off, observe, or enforce")
	}
	if g.SampleInterval < 250*time.Millisecond || g.SampleInterval > 30*time.Second {
		return fmt.Errorf("resource_guard.sample_interval must be between 250ms and 30s")
	}
	if g.MaxSampleAge < g.SampleInterval {
		return fmt.Errorf("resource_guard.max_sample_age must be >= resource_guard.sample_interval")
	}
	m := g.Memory
	if m.ReserveMB < 0 {
		return fmt.Errorf("resource_guard.memory.reserve_mb must be >= 0")
	}
	if m.HardLimitMB < 0 {
		return fmt.Errorf("resource_guard.memory.hard_limit_mb must be >= 0")
	}
	if m.ReservePercent < 0 || m.ReservePercent > 40 {
		return fmt.Errorf("resource_guard.memory.reserve_percent must be between 0 and 40")
	}
	if m.WorkerFallbackMB <= 0 {
		return fmt.Errorf("resource_guard.memory.worker_fallback_mb must be > 0")
	}
	if m.WarningRatio < 0 || m.WarningRatio >= 1 {
		return fmt.Errorf("resource_guard.memory.warning_ratio must be in [0,1)")
	}
	if m.CriticalRatio <= m.WarningRatio || m.CriticalRatio >= m.HardRatio {
		return fmt.Errorf("resource_guard.memory.critical_ratio must be greater than warning_ratio and less than hard_ratio")
	}
	if m.HardRatio <= m.CriticalRatio || m.HardRatio > 1 {
		return fmt.Errorf("resource_guard.memory.hard_ratio must be greater than critical_ratio and at most 1")
	}
	if g.CPU.WarningUtilizationRatio < 0 || g.CPU.WarningUtilizationRatio > 1 {
		return fmt.Errorf("resource_guard.cpu.warning_utilization_ratio must be between 0 and 1")
	}
	if g.CPU.CriticalThrottlingRatio < 0 || g.CPU.CriticalThrottlingRatio > 1 {
		return fmt.Errorf("resource_guard.cpu.critical_throttling_ratio must be between 0 and 1")
	}
	if g.Prediction.Horizon < 10*time.Second || g.Prediction.Horizon > 300*time.Second {
		return fmt.Errorf("resource_guard.prediction.horizon must be between 10s and 300s")
	}
	if g.Prediction.MinSamples < 1 || g.Prediction.MinSamples > 120 {
		return fmt.Errorf("resource_guard.prediction.min_samples must be between 1 and 120")
	}
	if g.Warnings.DedupeWindow < 0 {
		return fmt.Errorf("resource_guard.warnings.dedupe_window must be >= 0")
	}
	return nil
}
