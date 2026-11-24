package config

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// Config is the fully resolved Eregion configuration.
type Config struct {
	Version    string
	Server     ServerConfig
	PHP        PHPConfig
	Workers    WorkersConfig
	Socket     SocketConfig
	Protocol   ProtocolConfig
	Queue      QueueConfig
	Logging    LoggingConfig
	Operations OperationsConfig
	Metrics    EndpointConfig
	Health     EndpointConfig
	Readiness  EndpointConfig
	Liveness   EndpointConfig

	// WorkloadTemplates are operational templates keyed by name.
	WorkloadTemplates map[string]WorkloadTemplateConfig
	// Workloads are explicit workload definitions keyed by name.
	Workloads map[string]WorkloadConfig
	// HasExplicitWorkloads is true when the YAML declared a workloads section.
	HasExplicitWorkloads bool

	// Manifest is set from the CLI and forwarded to PHP workers.
	Manifest string

	// QueueCapacityDerived is true when queue.capacity came from workers.count*8
	// (default or recompute) rather than an explicit YAML value.
	QueueCapacityDerived bool

	// WorkerGuardRecycle limits replacement boots caused by guard_state_violation.
	// It does not inspect PHP state and does not change EREGION/1.
	WorkerGuardRecycle GuardRecycleSafetyConfig

	// ResourceGuard is the global resource safety controller.
	// Missing YAML keeps Mode observe so existing projects do not change worker counts.
	ResourceGuard ResourceGuardConfig
}

// WorkloadTemplateConfig is the YAML/runtime form of a workload template.
type WorkloadTemplateConfig struct {
	Mode      string                   `yaml:"mode"`
	Workers   *WorkloadWorkersConfig   `yaml:"workers"`
	Resources *WorkloadResourcesConfig `yaml:"resources"`
	Scaling   *WorkloadScalingConfig   `yaml:"scaling"`
}

// WorkloadConfig is one named workload from YAML.
type WorkloadConfig struct {
	Template  string                   `yaml:"template"`
	Mode      string                   `yaml:"mode"`
	Command   []string                 `yaml:"command"`
	Queue     *WorkloadQueueConfig     `yaml:"queue"`
	Workers   *WorkloadWorkersConfig   `yaml:"workers"`
	Resources *WorkloadResourcesConfig `yaml:"resources"`
	Scaling   *WorkloadScalingConfig   `yaml:"scaling"`
}

// WorkloadWorkersConfig is min/max workers.
type WorkloadWorkersConfig struct {
	Min *int `yaml:"min"`
	Max *int `yaml:"max"`
}

// WorkloadResourcesConfig is resource class + memory.
type WorkloadResourcesConfig struct {
	Class    *string `yaml:"class"`
	MemoryMB *int    `yaml:"memory_mb"`
}

// WorkloadScalingConfig is scaling policy knobs.
type WorkloadScalingConfig struct {
	Strategy         *string                  `yaml:"strategy"`
	ScaleUpCooldown  *string                  `yaml:"scale_up_cooldown"`
	ScaleDownIdleFor *string                  `yaml:"scale_down_idle_for"`
	AvgJobDuration   *string                  `yaml:"avg_job_duration"`
	TargetDrainTime  *string                  `yaml:"target_drain_time"`
	BacklogSource    *BacklogSourceConfig     `yaml:"backlog_source"`
	Control          *ScalingControlConfig    `yaml:"control"`
	CPU              *ScalingCPUConfig        `yaml:"cpu"`
	Latency          *ScalingLatencyConfig    `yaml:"latency"`
	Throughput       *ScalingThroughputConfig `yaml:"throughput"`
	Scheduled        *ScalingScheduledConfig  `yaml:"scheduled"`
	Adaptive         *ScalingAdaptiveConfig   `yaml:"adaptive"`
}

// ScalingControlConfig is the shared ECO-11 stabilizer.
type ScalingControlConfig struct {
	Window            *string `yaml:"window"`
	MaxSignalAge      *string `yaml:"max_signal_age"`
	UpConfirmations   *int    `yaml:"up_confirmations"`
	DownConfirmations *int    `yaml:"down_confirmations"`
	MaxStepUp         *int    `yaml:"max_step_up"`
	MaxStepDown       *int    `yaml:"max_step_down"`
}

// ScalingCPUConfig is the cpu_utilization block.
type ScalingCPUConfig struct {
	TargetRatio    *float64 `yaml:"target_ratio"`
	ToleranceRatio *float64 `yaml:"tolerance_ratio"`
}

// ScalingLatencyConfig is the latency block.
type ScalingLatencyConfig struct {
	TargetP95          *string  `yaml:"target_p95"`
	QueueWaitTargetP95 *string  `yaml:"queue_wait_target_p95"`
	ToleranceRatio     *float64 `yaml:"tolerance_ratio"`
	BusyThreshold      *float64 `yaml:"busy_threshold"`
}

// ScalingThroughputConfig is the calibrated offered-rate block.
type ScalingThroughputConfig struct {
	TargetRPSPerWorker *float64 `yaml:"target_rps_per_worker"`
	ToleranceRatio     *float64 `yaml:"tolerance_ratio"`
}

// ScalingScheduleWindowConfig is one UTC window.
type ScalingScheduleWindowConfig struct {
	Days          []string `yaml:"days"`
	Start         *string  `yaml:"start"`
	End           *string  `yaml:"end"`
	TargetWorkers *int     `yaml:"target_workers"`
}

// ScalingScheduledConfig is the UTC calendar.
type ScalingScheduledConfig struct {
	Timezone       *string                       `yaml:"timezone"`
	DefaultWorkers *int                          `yaml:"default_workers"`
	Windows        []ScalingScheduleWindowConfig `yaml:"windows"`
}

// ScalingAdaptiveConfig is the bounded probe.
type ScalingAdaptiveConfig struct {
	MinGainRatio          *float64 `yaml:"min_gain_ratio"`
	MaxP95RegressionRatio *float64 `yaml:"max_p95_regression_ratio"`
	DemandToleranceRatio  *float64 `yaml:"demand_tolerance_ratio"`
	BaselineWindows       *int     `yaml:"baseline_windows"`
	EvaluationWindows     *int     `yaml:"evaluation_windows"`
	SettleWindows         *int     `yaml:"settle_windows"`
	ProbeCooldown         *string  `yaml:"probe_cooldown"`
	ProbeTimeout          *string  `yaml:"probe_timeout"`
	BusyThreshold         *float64 `yaml:"busy_threshold"`
}

// BacklogSourceConfig names an HTTP JSON backlog adapter. Env fields are variable names, not secrets.
type BacklogSourceConfig struct {
	Type         *string `yaml:"type"`
	URLEnv       *string `yaml:"url_env"`
	TokenEnv     *string `yaml:"token_env"`
	Timeout      *string `yaml:"timeout"`
	MaxStaleness *string `yaml:"max_staleness"`
}

// WorkloadQueueConfig is broker metadata (not consumed by Eregion).
type WorkloadQueueConfig struct {
	Transport *string `yaml:"transport"`
	Name      *string `yaml:"name"`
}

type ServerConfig struct {
	Host              string
	Port              int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
}

type PHPConfig struct {
	Binary           string
	WorkerScript     string
	WorkingDirectory string
	Environment      map[string]string
}

type WorkersConfig struct {
	Count            int
	MinReady         int
	MaxRequests      int
	StartupTimeout   time.Duration
	HandshakeTimeout time.Duration
	RequestTimeout   time.Duration
	AcquireTimeout   time.Duration
	ShutdownTimeout  time.Duration
	MemoryLimitMB    int
	RestartLimit     int
	RestartWindow    time.Duration
	RestartBackoff   BackoffConfig
}

type BackoffConfig struct {
	Initial    time.Duration
	Maximum    time.Duration
	Multiplier float64
	Jitter     float64
}

type SocketConfig struct {
	Directory            string
	DirectoryPermissions os.FileMode
	SocketPermissions    os.FileMode
}

type ProtocolConfig struct {
	Version       uint8
	MaxFrameBytes uint32
}

type QueueConfig struct {
	Capacity   int
	RetryAfter time.Duration
}

type LoggingConfig struct {
	Level                 string
	Format                string
	AccessLog             bool
	IncludeRequestHeaders bool
	IncludeRequestBody    bool
	IncludeResponseBody   bool
}

type OperationsConfig struct {
	Prefix string
}

type EndpointConfig struct {
	Enabled bool
	Path    string
	// Access is metrics-only: "" keeps the previous open behavior, loopback or public are explicit.
	Access string
}

// GuardRecycleSafetyConfig is the per-process policy for guard_state_violation storms.
// Reasons are not taken from arbitrary PHP strings: only guard_state_violation counts.
type GuardRecycleSafetyConfig struct {
	Enabled                    bool
	MaxRecycles                int
	Window                     time.Duration
	Cooldown                   time.Duration
	HalfOpenMaxProbes          int
	RecoverySuccessfulRequests int
	RecoveryMinUptime          time.Duration
}

// Default returns safe local-development defaults (spec §7.3).
func Default() Config {
	workerCount := runtime.NumCPU()
	if workerCount > 8 {
		workerCount = 8
	}
	if workerCount < 1 {
		workerCount = 1
	}

	cfg := Config{
		Version: "1",
		Server: ServerConfig{
			Host:              "127.0.0.1",
			Port:              8080,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   20 * time.Second,
			MaxHeaderBytes:    1 << 20,
			MaxBodyBytes:      10 << 20,
		},
		PHP: PHPConfig{
			Binary:           "php",
			WorkerScript:     "bin/eregion-worker",
			WorkingDirectory: ".",
			Environment:      map[string]string{},
		},
		Workers: WorkersConfig{
			Count:            workerCount,
			MinReady:         1,
			MaxRequests:      1000,
			StartupTimeout:   10 * time.Second,
			HandshakeTimeout: 5 * time.Second,
			RequestTimeout:   30 * time.Second,
			AcquireTimeout:   2 * time.Second,
			ShutdownTimeout:  5 * time.Second,
			MemoryLimitMB:    256,
			RestartLimit:     5,
			RestartWindow:    30 * time.Second,
			RestartBackoff: BackoffConfig{
				Initial:    100 * time.Millisecond,
				Maximum:    5 * time.Second,
				Multiplier: 2.0,
				Jitter:     0.2,
			},
		},
		Socket: SocketConfig{
			Directory:            "/tmp/eregion",
			DirectoryPermissions: 0o700,
			SocketPermissions:    0o600,
		},
		Protocol: ProtocolConfig{
			Version:       1,
			MaxFrameBytes: 16 << 20,
		},
		Queue: QueueConfig{
			Capacity:   DerivedQueueCapacity(workerCount),
			RetryAfter: time.Second,
		},
		Logging: LoggingConfig{
			Level:                 "info",
			Format:                "text",
			AccessLog:             true,
			IncludeRequestHeaders: false,
			IncludeRequestBody:    false,
			IncludeResponseBody:   false,
		},
		Operations:           OperationsConfig{Prefix: "/_eregion"},
		Metrics:              EndpointConfig{Enabled: true, Path: "/metrics"},
		Health:               EndpointConfig{Enabled: true, Path: "/health"},
		Readiness:            EndpointConfig{Enabled: true, Path: "/ready"},
		Liveness:             EndpointConfig{Enabled: true, Path: "/live"},
		WorkloadTemplates:    map[string]WorkloadTemplateConfig{},
		Workloads:            map[string]WorkloadConfig{},
		QueueCapacityDerived: true,
		WorkerGuardRecycle: GuardRecycleSafetyConfig{
			Enabled:                    true,
			MaxRecycles:                3,
			Window:                     30 * time.Second,
			Cooldown:                   60 * time.Second,
			HalfOpenMaxProbes:          1,
			RecoverySuccessfulRequests: 20,
			RecoveryMinUptime:          30 * time.Second,
		},
		ResourceGuard: DefaultResourceGuard(),
	}
	_ = cfg.ResolveEndpoints()
	return cfg
}

// Addr returns host:port for the HTTP server.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
