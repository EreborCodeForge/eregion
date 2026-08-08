package resources

import (
	"errors"
	"io/fs"
	"os"
)

const procSelfCgroup = "/proc/self/cgroup"

// ApplyCgroupDetection fills CPU/memory fields from the process cgroup (leaf then root).
// Safe on any OS when given a FileReader (used by Linux Detect and unit tests).
func ApplyCgroupDetection(rd FileReader, root string, r *RuntimeResources) {
	ApplyCgroupDetectionLogged(rd, root, r, SystemDetector{})
}

// ApplyCgroupDetectionLogged is like ApplyCgroupDetection but emits DEBUG via d.Logger.
func ApplyCgroupDetectionLogged(rd FileReader, root string, r *RuntimeResources, d SystemDetector) {
	if rd == nil || root == "" {
		return
	}
	root = pathCleanRoot(root)

	membership := CgroupMembership{}
	data, err := rd.ReadFile(procSelfCgroup)
	if err != nil {
		d.debug("resource detection: /proc/self/cgroup unavailable", "err", err.Error())
	} else {
		membership = ParseSelfCgroup(data)
		if membership.Version == 0 {
			d.debug("resource detection: /proc/self/cgroup empty or malformed; falling back to cgroup root")
		}
	}

	var (
		quotaCPU  float64
		quotaOK   bool
		cpusetCPU float64
		cpusetOK  bool
		sawV2     bool
		sawV1     bool
	)

	// --- CPU quota ---
	if membership.Version == 2 {
		sawV2 = true
		if leaf, ok := ResolveCgroupPath(root, membership.Unified); ok {
			quotaCPU, quotaOK = readV2CPUAt(rd, leaf, d)
		} else {
			d.debug("resource detection: cgroup v2 path escape rejected; falling back to root")
		}
		if !quotaOK {
			d.debug("resource detection: leaf cpu.max unavailable or unlimited; trying cgroup root")
			quotaCPU, quotaOK = readV2CPUAt(rd, root, d)
		}
	} else if membership.Version == 1 && membership.CPU != "" {
		sawV1 = true
		cpuRoot := joinRoot(root, "cpu")
		if leaf, ok := ResolveCgroupPath(cpuRoot, membership.CPU); ok {
			quotaCPU, quotaOK = readV1CPUAt(rd, leaf, d)
		} else {
			d.debug("resource detection: cgroup v1 cpu path escape rejected; falling back to root")
		}
		if !quotaOK {
			d.debug("resource detection: leaf cfs quota unavailable or unlimited; trying cgroup root")
			quotaCPU, quotaOK = readV1CPUAt(rd, cpuRoot, d)
		}
	} else {
		// No membership: try root v2 then v1.
		if q, ok := readV2CPUAt(rd, root, d); ok {
			quotaCPU, quotaOK, sawV2 = q, true, true
		} else if q, ok := readV1CPUAt(rd, joinRoot(root, "cpu"), d); ok {
			quotaCPU, quotaOK, sawV1 = q, true, true
		}
	}

	// --- cpuset ---
	if membership.Version == 2 {
		sawV2 = true
		if leaf, ok := ResolveCgroupPath(root, membership.Unified); ok {
			cpusetCPU, cpusetOK = readCPUSetAt(rd, leaf, true, d)
		}
		if !cpusetOK {
			cpusetCPU, cpusetOK = readCPUSetAt(rd, root, true, d)
		}
	} else if membership.Version == 1 {
		sawV1 = true
		csRoot := joinRoot(root, "cpuset")
		rel := membership.CPUSet
		if rel == "" {
			rel = membership.CPU
		}
		if rel != "" {
			if leaf, ok := ResolveCgroupPath(csRoot, rel); ok {
				cpusetCPU, cpusetOK = readCPUSetAt(rd, leaf, false, d)
			}
		}
		if !cpusetOK {
			cpusetCPU, cpusetOK = readCPUSetAt(rd, csRoot, false, d)
		}
	} else {
		if n, ok := readCPUSetAt(rd, root, true, d); ok {
			cpusetCPU, cpusetOK, sawV2 = n, true, true
		} else if n, ok := readCPUSetAt(rd, joinRoot(root, "cpuset"), false, d); ok {
			cpusetCPU, cpusetOK, sawV1 = n, true, true
		}
	}

	switch {
	case quotaOK && cpusetOK:
		r.AvailableCPUs = minPositive(quotaCPU, cpusetCPU)
		r.CPUQuotaDetected = true
		r.CPUSetDetected = true
		r.CPUSource = CPUDetectionCombined
	case quotaOK:
		r.AvailableCPUs = quotaCPU
		r.CPUQuotaDetected = true
		r.CPUSetDetected = false
		r.CPUSource = CPUDetectionCgroupQuota
	case cpusetOK:
		r.AvailableCPUs = cpusetCPU
		r.CPUQuotaDetected = false
		r.CPUSetDetected = true
		r.CPUSource = CPUDetectionCPUSet
	}

	// Environment / version (diagnostic only).
	switch {
	case membership.Version == 2 || sawV2:
		r.Environment = EnvCgroupV2
		r.CgroupVersion = 2
	case membership.Version == 1 || sawV1:
		r.Environment = EnvCgroupV1
		r.CgroupVersion = 1
	default:
		r.Environment = EnvHost
		r.CgroupVersion = 0
	}

	// --- memory ---
	if limit, known := readMemoryEffective(rd, root, membership, d); known {
		r.MemoryLimitBytes = limit
		r.MemoryLimitKnown = true
	}
}

func pathCleanRoot(root string) string {
	return joinRoot(root) // path.Join with single elem cleans
}

func readV2CPUAt(rd FileReader, dir string, d SystemDetector) (float64, bool) {
	data, err := rd.ReadFile(joinRoot(dir, "cpu.max"))
	if err != nil {
		logReadErr(d, "cpu.max", err)
		return 0, false
	}
	q, err := ParseCPUMax(string(data))
	if err != nil {
		d.debug("resource detection: cpu.max parse failed", "err", err.Error())
		return 0, false
	}
	if !q.Limited {
		d.debug("resource detection: cpu.max unlimited")
		return 0, false
	}
	if q.Available <= 0 {
		d.debug("resource detection: cpu.max non-positive quota")
		return 0, false
	}
	return q.Available, true
}

func readV1CPUAt(rd FileReader, dir string, d SystemDetector) (float64, bool) {
	quotaData, err := rd.ReadFile(joinRoot(dir, "cpu.cfs_quota_us"))
	if err != nil {
		logReadErr(d, "cpu.cfs_quota_us", err)
		return 0, false
	}
	periodData, err := rd.ReadFile(joinRoot(dir, "cpu.cfs_period_us"))
	if err != nil {
		logReadErr(d, "cpu.cfs_period_us", err)
		return 0, false
	}
	q, err := ParseCFSQuota(string(quotaData), string(periodData))
	if err != nil {
		d.debug("resource detection: cfs quota parse failed", "err", err.Error())
		return 0, false
	}
	if !q.Limited {
		d.debug("resource detection: cfs quota unlimited")
		return 0, false
	}
	if q.Available <= 0 {
		return 0, false
	}
	return q.Available, true
}

func readCPUSetAt(rd FileReader, dir string, unified bool, d SystemDetector) (float64, bool) {
	var candidates []string
	if unified {
		candidates = []string{
			joinRoot(dir, "cpuset.cpus.effective"),
			joinRoot(dir, "cpuset.cpus"),
		}
	} else {
		candidates = []string{
			joinRoot(dir, "cpuset.cpus.effective"),
			joinRoot(dir, "cpuset.cpus"),
		}
	}
	for _, p := range candidates {
		data, err := rd.ReadFile(p)
		if err != nil {
			continue
		}
		n, err := ParseCPUSet(string(data))
		if err != nil || n <= 0 {
			continue
		}
		return float64(n), true
	}
	d.debug("resource detection: cpuset unavailable at directory")
	return 0, false
}

func readMemoryEffective(rd FileReader, root string, m CgroupMembership, d SystemDetector) (uint64, bool) {
	if m.Version == 2 {
		if leaf, ok := ResolveCgroupPath(root, m.Unified); ok {
			if limit, known := readMemoryV2(rd, leaf, d); known {
				return limit, true
			}
		}
		return readMemoryV2(rd, root, d)
	}
	if m.Version == 1 {
		memRoot := joinRoot(root, "memory")
		rel := m.Memory
		if rel == "" {
			rel = m.CPU
		}
		if rel != "" {
			if leaf, ok := ResolveCgroupPath(memRoot, rel); ok {
				if limit, known := readMemoryV1(rd, leaf, d); known {
					return limit, true
				}
			}
		}
		return readMemoryV1(rd, memRoot, d)
	}
	if limit, known := readMemoryV2(rd, root, d); known {
		return limit, true
	}
	return readMemoryV1(rd, joinRoot(root, "memory"), d)
}

func readMemoryV2(rd FileReader, dir string, d SystemDetector) (uint64, bool) {
	data, err := rd.ReadFile(joinRoot(dir, "memory.max"))
	if err != nil {
		logReadErr(d, "memory.max", err)
		return 0, false
	}
	limit, known, err := ParseMemoryMax(string(data))
	if err != nil {
		d.debug("resource detection: memory.max parse failed", "err", err.Error())
		return 0, false
	}
	if !known {
		d.debug("resource detection: memory.max unlimited")
	}
	return limit, known
}

func readMemoryV1(rd FileReader, dir string, d SystemDetector) (uint64, bool) {
	data, err := rd.ReadFile(joinRoot(dir, "memory.limit_in_bytes"))
	if err != nil {
		logReadErr(d, "memory.limit_in_bytes", err)
		return 0, false
	}
	limit, known, err := ParseMemoryLimitBytes(string(data))
	if err != nil {
		d.debug("resource detection: memory.limit_in_bytes parse failed", "err", err.Error())
		return 0, false
	}
	return limit, known
}

func logReadErr(d SystemDetector, name string, err error) {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
		d.debug("resource detection: file absent", "file", name)
		return
	}
	if errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission) {
		d.debug("resource detection: permission denied", "file", name)
		return
	}
	d.debug("resource detection: file read failed", "file", name, "err", err.Error())
}
