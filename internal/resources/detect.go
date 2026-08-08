package resources

import (
	"os"
	"path"
	"runtime"
)

type osFileReader struct{}

func (osFileReader) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(p)
}

func (d SystemDetector) reader() FileReader {
	if d.Reader != nil {
		return d.Reader
	}
	return osFileReader{}
}

func (d SystemDetector) cgroupRoot() string {
	if d.CgroupRoot != "" {
		return d.CgroupRoot
	}
	return "/sys/fs/cgroup"
}

// Detect gathers logical CPUs, GOMAXPROCS, and platform-specific quotas.
// Missing cgroup data never fails; AvailableCPUs always ends >= 1.
func (d SystemDetector) Detect() RuntimeResources {
	r := RuntimeResources{
		LogicalCPUs: runtime.NumCPU(),
		GOMAXPROCS:  runtime.GOMAXPROCS(0),
		Environment: EnvUnknown,
	}
	if r.LogicalCPUs < 1 {
		r.LogicalCPUs = 1
	}

	d.platformDetect(&r)
	d.applyCPUFallbacks(&r)
	return r
}

func (d SystemDetector) applyCPUFallbacks(r *RuntimeResources) {
	if r.AvailableCPUs > 0 {
		return
	}
	if r.GOMAXPROCS > 0 {
		r.AvailableCPUs = float64(r.GOMAXPROCS)
		r.CPUQuotaDetected = false
		r.CPUSource = CPUDetectionGOMAXPROCS
		d.debug("resource detection: falling back to GOMAXPROCS", "gomaxprocs", r.GOMAXPROCS)
		return
	}
	if r.LogicalCPUs > 0 {
		r.AvailableCPUs = float64(r.LogicalCPUs)
		r.CPUQuotaDetected = false
		r.CPUSource = CPUDetectionNumCPU
		d.debug("resource detection: falling back to NumCPU", "logical_cpus", r.LogicalCPUs)
		return
	}
	r.AvailableCPUs = 1
	r.CPUQuotaDetected = false
	r.CPUSource = CPUDetectionFallback
	d.debug("resource detection: falling back to AvailableCPUs=1")
}

// applyCPUFallbacks is the package-level helper used by tests.
func applyCPUFallbacks(r *RuntimeResources) {
	SystemDetector{}.applyCPUFallbacks(r)
}

// joinRoot joins Unix-style cgroup paths (always forward slashes).
func joinRoot(root string, elems ...string) string {
	parts := append([]string{root}, elems...)
	return path.Join(parts...)
}

// minPositive returns the smaller of a and b when both > 0; otherwise the positive one.
func minPositive(a, b float64) float64 {
	switch {
	case a > 0 && b > 0:
		if a < b {
			return a
		}
		return b
	case a > 0:
		return a
	default:
		return b
	}
}
