//go:build linux

package integration_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func startGuardProc(bin string, args []string, extraEnv []string, log io.Writer) (*guardProc, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &guardProc{cmd: cmd}, nil
}

func (p *guardProc) pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// stop signals the child's process group and waits until it has exited.
// The next variant must not start before this returns.
func (p *guardProc) stop() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	pid := p.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		err := <-done
		return fmt.Errorf("process group %d required SIGKILL: %v", pid, err)
	}
}

func sampleSupervisor(pid int) procSnap {
	rss, threads := readRSSThreads(pid)
	snap := procSnap{RSSKB: rss, Ticks: readTicks(pid), Threads: threads}
	for _, child := range descendantPIDs(pid) {
		childRSS, _ := readRSSThreads(child)
		snap.ChildRSSKB += childRSS
		snap.ChildTicks += readTicks(child)
		snap.ChildN++
	}
	return snap
}

func clockTicks() float64 {
	clockOnce.Do(func() {
		out, err := exec.Command("getconf", "CLK_TCK").Output()
		if err != nil {
			clockHz = 100
			return
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil || v <= 0 {
			clockHz = 100
			return
		}
		clockHz = v
	})
	return clockHz
}

var (
	clockOnce sync.Once
	clockHz   float64
)

func readRSSThreads(pid int) (rssKB uint64, threads int) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				rssKB, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		case strings.HasPrefix(line, "Threads:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				threads, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return rssKB, threads
}

func readTicks(pid int) uint64 {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	text := string(raw)
	cut := strings.LastIndex(text, ")")
	if cut < 0 || cut+2 >= len(text) {
		return 0
	}
	fields := strings.Fields(text[cut+2:])
	if len(fields) < 13 {
		return 0
	}
	user, _ := strconv.ParseUint(fields[11], 10, 64)
	sys, _ := strconv.ParseUint(fields[12], 10, 64)
	return user + sys
}

func descendantPIDs(root int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		ppid, ok := ppidOf(string(raw))
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	stack := append([]int(nil), children[root]...)
	seen := map[int]struct{}{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
		stack = append(stack, children[pid]...)
	}
	return out
}

func ppidOf(raw string) (int, bool) {
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
