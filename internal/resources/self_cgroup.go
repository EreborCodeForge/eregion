package resources

import (
	"bufio"
	"bytes"
	"strings"
)

// CgroupMembership describes the process cgroup from /proc/self/cgroup.
type CgroupMembership struct {
	Version int // 0 = unknown/empty, 1 = v1, 2 = v2
	Unified string
	CPU     string
	Memory  string
	CPUSet  string
}

// ParseSelfCgroup parses /proc/self/cgroup contents. Never panics.
// Empty or malformed input returns a zero membership (Version=0).
func ParseSelfCgroup(data []byte) CgroupMembership {
	var m CgroupMembership
	if len(bytes.TrimSpace(data)) == 0 {
		return m
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		// cgroup v2: "0::/path"
		if strings.HasPrefix(line, "0::") {
			m.Version = 2
			m.Unified = strings.TrimPrefix(line, "0::")
			if m.Unified == "" {
				m.Unified = "/"
			}
			continue
		}
		// cgroup v1: "id:controllers:/path"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers := parts[1]
		cgPath := parts[2]
		if controllers == "" {
			continue
		}
		if m.Version == 0 {
			m.Version = 1
		}
		for _, c := range strings.Split(controllers, ",") {
			c = strings.TrimSpace(c)
			switch c {
			case "cpu", "cpuacct":
				if m.CPU == "" {
					m.CPU = cgPath
				}
			case "memory":
				if m.Memory == "" {
					m.Memory = cgPath
				}
			case "cpuset":
				if m.CPUSet == "" {
					m.CPUSet = cgPath
				}
			}
		}
	}
	// Prefer v2 when both styles somehow appear.
	if m.Unified != "" {
		m.Version = 2
	}
	return m
}
