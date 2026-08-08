package resources

import (
	"fmt"
	"strconv"
	"strings"
)

// CPUQuota holds a parsed cgroup CPU quota result.
type CPUQuota struct {
	Available float64
	Limited   bool // true when an explicit quota was present
}

// ParseCPUMax parses cgroup v2 cpu.max content ("quota period" or "max period").
// When quota is "max", Limited is false (unlimited / unavailable).
func ParseCPUMax(content string) (CPUQuota, error) {
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return CPUQuota{}, fmt.Errorf("cpu.max: expected quota period, got %q", strings.TrimSpace(content))
	}
	if fields[0] == "max" {
		return CPUQuota{Limited: false}, nil
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return CPUQuota{}, fmt.Errorf("cpu.max quota: %w", err)
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return CPUQuota{}, fmt.Errorf("cpu.max period: %w", err)
	}
	if period <= 0 {
		return CPUQuota{}, fmt.Errorf("cpu.max: invalid period %g", period)
	}
	return CPUQuota{Available: quota / period, Limited: true}, nil
}

// ParseCFSQuota parses cgroup v1 cpu.cfs_quota_us and cpu.cfs_period_us.
// quota == -1 means unlimited.
func ParseCFSQuota(quotaContent, periodContent string) (CPUQuota, error) {
	quota, err := strconv.ParseInt(strings.TrimSpace(quotaContent), 10, 64)
	if err != nil {
		return CPUQuota{}, fmt.Errorf("cfs_quota_us: %w", err)
	}
	if quota < 0 {
		return CPUQuota{Limited: false}, nil
	}
	period, err := strconv.ParseInt(strings.TrimSpace(periodContent), 10, 64)
	if err != nil {
		return CPUQuota{}, fmt.Errorf("cfs_period_us: %w", err)
	}
	if period <= 0 {
		return CPUQuota{}, fmt.Errorf("cfs_period_us: invalid period %d", period)
	}
	return CPUQuota{Available: float64(quota) / float64(period), Limited: true}, nil
}

// ParseMemoryMax parses cgroup v2 memory.max.
// "max" or empty means unlimited / unknown.
func ParseMemoryMax(content string) (limit uint64, known bool, err error) {
	s := strings.TrimSpace(content)
	if s == "" || s == "max" {
		return 0, false, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("memory.max: %w", err)
	}
	return v, true, nil
}

// ParseMemoryLimitBytes parses cgroup v1 memory.limit_in_bytes.
// Values at or above 1<<62 are treated as unlimited (kernel sentinel).
func ParseMemoryLimitBytes(content string) (limit uint64, known bool, err error) {
	s := strings.TrimSpace(content)
	if s == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("memory.limit_in_bytes: %w", err)
	}
	const unlimitedThreshold = uint64(1) << 62
	if v >= unlimitedThreshold {
		return 0, false, nil
	}
	return v, true, nil
}

// ParseCPUSet counts CPUs listed in a cpuset.cpus / cpuset.cpus.effective string.
// Supports forms like "0-3", "0,2,4-6", "1".
func ParseCPUSet(content string) (count int, err error) {
	s := strings.TrimSpace(content)
	if s == "" {
		return 0, fmt.Errorf("cpuset: empty")
	}
	seen := make(map[int]struct{})
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			if len(bounds) != 2 {
				return 0, fmt.Errorf("cpuset: bad range %q", part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return 0, fmt.Errorf("cpuset range start: %w", err)
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return 0, fmt.Errorf("cpuset range end: %w", err)
			}
			if end < start {
				return 0, fmt.Errorf("cpuset: inverted range %q", part)
			}
			for i := start; i <= end; i++ {
				seen[i] = struct{}{}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, fmt.Errorf("cpuset cpu: %w", err)
		}
		seen[n] = struct{}{}
	}
	if len(seen) == 0 {
		return 0, fmt.Errorf("cpuset: no CPUs in %q", s)
	}
	return len(seen), nil
}
