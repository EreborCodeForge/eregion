package resources

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

type mapFiles map[string][]byte

func (m mapFiles) ReadFile(name string) ([]byte, error) {
	b, ok := m[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if string(b) == "PERM" {
		return nil, fs.ErrPermission
	}
	return b, nil
}

func TestECO09T12CgroupV2Memory(t *testing.T) {
	s := &CgroupSampler{Reader: mapFiles{
		"/proc/self/cgroup":                 []byte("0::/app\n"),
		"/sys/fs/cgroup/app/memory.max":     []byte("1048576\n"),
		"/sys/fs/cgroup/app/memory.current": []byte("262144\n"),
		"/sys/fs/cgroup/memory.max":         []byte("max\n"),
	}, Root: "/sys/fs/cgroup"}
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceCgroupV2 || !got.MemoryKnown() {
		t.Fatalf("sample=%+v", got)
	}
	if got.MemoryLimitBytes != 1048576 || got.MemoryCurrentBytes != 262144 {
		t.Fatalf("limit=%d current=%d", got.MemoryLimitBytes, got.MemoryCurrentBytes)
	}
	if !got.HierarchyKnown {
		t.Fatal("hierarchy should be known when ancestors are readable")
	}
}

func TestECO09T13CgroupV1Memory(t *testing.T) {
	s := &CgroupSampler{Reader: mapFiles{
		"/proc/self/cgroup": []byte("2:memory:/app\n3:cpu,cpuacct:/app\n"),
		"/sys/fs/cgroup/memory/app/memory.limit_in_bytes": []byte("1048576\n"),
		"/sys/fs/cgroup/memory/app/memory.usage_in_bytes": []byte("131072\n"),
		"/sys/fs/cgroup/memory/memory.limit_in_bytes":     []byte("2097152\n"),
	}, Root: "/sys/fs/cgroup"}
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceCgroupV1 || got.MemoryLimitBytes != 1048576 || got.MemoryCurrentBytes != 131072 {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T14NestedParentIsTighter(t *testing.T) {
	s := &CgroupSampler{Reader: mapFiles{
		"/proc/self/cgroup":                       []byte("0::/app/child\n"),
		"/sys/fs/cgroup/app/child/memory.max":     []byte("2097152\n"),
		"/sys/fs/cgroup/app/child/memory.current": []byte("100\n"),
		"/sys/fs/cgroup/app/memory.max":           []byte("1048576\n"),
		"/sys/fs/cgroup/app/memory.current":       []byte("800000\n"),
		"/sys/fs/cgroup/memory.max":               []byte("max\n"),
	}, Root: "/sys/fs/cgroup"}
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitBytes != 1048576 {
		t.Fatalf("limit=%d, parent must win", got.MemoryLimitBytes)
	}
	if got.MemoryCurrentBytes != 800000 {
		t.Fatalf("current=%d, want the parent scope that owns the tighter limit", got.MemoryCurrentBytes)
	}
}

func TestECO09T14UnreadableParentDoesNotClaimLeafLimit(t *testing.T) {
	s := &CgroupSampler{Reader: mapFiles{
		"/proc/self/cgroup":                   []byte("0::/app/child\n"),
		"/sys/fs/cgroup/app/child/memory.max": []byte("1048576\n"),
		"/sys/fs/cgroup/app/memory.max":       []byte("PERM"),
	}, Root: "/sys/fs/cgroup"}
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimitValid == ValidityKnown {
		t.Fatalf("unreadable parent was treated as a known limit: %+v", got)
	}
}

func TestECO09T15PermissionIsNotZeroUsage(t *testing.T) {
	s := &CgroupSampler{Reader: mapFiles{
		"/proc/self/cgroup":                 []byte("0::/app\n"),
		"/sys/fs/cgroup/app/memory.max":     []byte("1048576\n"),
		"/sys/fs/cgroup/app/memory.current": []byte("PERM"),
		"/sys/fs/cgroup/memory.max":         []byte("max\n"),
	}, Root: "/sys/fs/cgroup"}
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryCurrentValid == ValidityKnown {
		t.Fatal("permission denied was published as a known current value")
	}
	if got.MemoryKnown() {
		t.Fatal("partial reading must not look like a usable zero-usage sample")
	}
}

func TestECO09T11CPUQuotaChangesWithoutRestart(t *testing.T) {
	files := mapFiles{
		"/proc/self/cgroup":             []byte("0::/app\n"),
		"/sys/fs/cgroup/cpu.max":        []byte("max 100000\n"),
		"/sys/fs/cgroup/app/cpu.max":    []byte("200000 100000\n"),
		"/sys/fs/cgroup/app/memory.max": []byte("max\n"),
		"/sys/fs/cgroup/memory.max":     []byte("max\n"),
	}
	s := &CgroupSampler{Reader: files, Root: "/sys/fs/cgroup"}
	first, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUCapacity != 2 || first.CPUCapacityValid != ValidityKnown {
		t.Fatalf("first=%+v", first)
	}
	files["/sys/fs/cgroup/app/cpu.max"] = []byte("100000 100000\n")
	second, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUCapacity != 1 {
		t.Fatalf("second capacity=%v, want 1 without restarting the sampler", second.CPUCapacity)
	}
}

func TestCollectorDoesNotSampleOnCurrent(t *testing.T) {
	calls := 0
	c := &Collector{Sampler: SamplerFunc(func(context.Context) (Sample, error) {
		calls++
		return Sample{
			At:                 time.Unix(10, 0),
			Source:             SourceCgroupV2,
			MemoryLimitBytes:   10,
			MemoryLimitValid:   ValidityKnown,
			MemoryCurrentBytes: 1,
			MemoryCurrentValid: ValidityKnown,
			Scope:              "cgroup",
		}, nil
	}), MaxAge: time.Second, Clock: FuncClock(func() time.Time { return time.Unix(10, 0) })}
	if _, err := c.CollectOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Current(time.Unix(10, 0))
	_ = c.Current(time.Unix(10, 0))
	if calls != 1 {
		t.Fatalf("sampler calls=%d, want 1", calls)
	}
	stale := c.Current(time.Unix(12, 0))
	if stale.MemoryLimitValid != ValidityStale {
		t.Fatalf("validity=%s", stale.MemoryLimitValid)
	}
}

func TestHybridCgroup2MountFeedsCPUWithoutQuota(t *testing.T) {
	files := mapFiles{
		"/proc/self/cgroup":                          []byte("0::/init.scope\n"),
		"/proc/self/mountinfo":                       []byte("115 114 0:21 / /sys/fs/cgroup/unified rw,nosuid - cgroup2 cgroup2 rw\n"),
		"/sys/fs/cgroup/unified/init.scope/cpu.stat": []byte("usage_usec 1000\nuser_usec 1000\nsystem_usec 0\n"),
	}
	now := time.Unix(100, 0)
	s := &CgroupSampler{Reader: files, Root: "/sys/fs/cgroup", Clock: FuncClock(func() time.Time { return now })}
	if _, err := s.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	files["/sys/fs/cgroup/unified/init.scope/cpu.stat"] = []byte("usage_usec 1100000\nuser_usec 1100000\nsystem_usec 0\n")
	now = now.Add(time.Second)
	s.Clock = FuncClock(func() time.Time { return now })
	got, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUUtilizationValid != ValidityKnown || got.CPUUtilization <= 0 {
		t.Fatalf("utilization=%s %g", got.CPUUtilizationValid, got.CPUUtilization)
	}
	if got.CPUThrottlingValid != ValidityKnown || got.CPUThrottledRatio != 0 {
		t.Fatalf("throttle=%s %g", got.CPUThrottlingValid, got.CPUThrottledRatio)
	}
}
