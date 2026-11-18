//go:build linux

package resources

import (
	"os"
	"strconv"
	"strings"
)

// ProcessTreeRSS sums the RSS of descendant processes. The caller itself is
// excluded. The boolean is false when /proc cannot be read.
func ProcessTreeRSS() (uint64, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	type proc struct {
		ppid int
	}
	procs := make(map[int]proc, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		ppid, ok := ppidFromStat(string(raw))
		if !ok {
			continue
		}
		procs[pid] = proc{ppid: ppid}
	}
	children := make(map[int][]int)
	for pid, info := range procs {
		children[info.ppid] = append(children[info.ppid], pid)
	}
	var sum uint64
	stack := append([]int(nil), children[os.Getpid()]...)
	seen := map[int]struct{}{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		stack = append(stack, children[pid]...)
		rss, ok := vmRSS(pid)
		if ok {
			sum += rss
		}
	}
	return sum, true
}

func ppidFromStat(raw string) (int, bool) {
	end := strings.LastIndex(raw, ")")
	if end < 0 || end+2 >= len(raw) {
		return 0, false
	}
	fields := strings.Fields(raw[end+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

func vmRSS(pid int) (uint64, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
