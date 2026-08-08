// Package resources detects CPU and memory available to the Eregion runtime.
package resources

import "log/slog"

// RuntimeResources describes host/container resources visible to this process.
// Environment is diagnostic metadata only and must not drive control logic.
type RuntimeResources struct {
	LogicalCPUs      int
	AvailableCPUs    float64
	GOMAXPROCS       int
	CPUQuotaDetected bool
	CPUSetDetected   bool
	CPUSource        CPUDetectionSource

	MemoryLimitBytes uint64
	MemoryLimitKnown bool

	Environment   string
	CgroupVersion int // 0 unknown, 1 or 2 when known
}

// CPUDetectionSource identifies how AvailableCPUs was determined.
type CPUDetectionSource string

const (
	CPUDetectionCgroupQuota CPUDetectionSource = "cgroup_quota"
	CPUDetectionCPUSet      CPUDetectionSource = "cpuset"
	CPUDetectionCombined    CPUDetectionSource = "quota_cpuset"
	CPUDetectionGOMAXPROCS  CPUDetectionSource = "gomaxprocs"
	CPUDetectionNumCPU      CPUDetectionSource = "numcpu"
	CPUDetectionFallback    CPUDetectionSource = "fallback"
)

// Environment diagnostic values (gap-closure §15).
const (
	EnvHost     = "host"
	EnvCgroupV1 = "cgroup-v1"
	EnvCgroupV2 = "cgroup-v2"
	EnvUnknown  = "unknown"
)

// FileReader abstracts filesystem reads for testability.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// Detector detects runtime resources once.
type Detector interface {
	Detect() RuntimeResources
}

// SystemDetector reads local cgroup/OS metadata.
// Zero value is valid: uses OS filesystem and /sys/fs/cgroup.
type SystemDetector struct {
	Reader     FileReader
	CgroupRoot string
	Logger     *slog.Logger
}

// Detect returns resources for the current process using a SystemDetector.
func Detect() RuntimeResources {
	return SystemDetector{}.Detect()
}

func (d SystemDetector) debug(msg string, args ...any) {
	if d.Logger != nil {
		d.Logger.Debug(msg, args...)
	}
}
