package resources

import (
	"errors"
	"testing"
)

func TestParseCPUMax(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		want    float64
		limited bool
		wantErr bool
	}{
		{name: "two_cpus", in: "200000 100000", want: 2, limited: true},
		{name: "half_cpu", in: "50000 100000", want: 0.5, limited: true},
		{name: "unlimited", in: "max 100000", limited: false},
		{name: "with_newline", in: "200000 100000\n", want: 2, limited: true},
		{name: "bad", in: "onlyone", wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q, err := ParseCPUMax(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if q.Limited != tc.limited {
				t.Fatalf("Limited=%v want %v", q.Limited, tc.limited)
			}
			if tc.limited && q.Available != tc.want {
				t.Fatalf("Available=%g want %g", q.Available, tc.want)
			}
		})
	}
}

func TestParseCFSQuota(t *testing.T) {
	t.Parallel()
	q, err := ParseCFSQuota("200000", "100000")
	if err != nil {
		t.Fatal(err)
	}
	if !q.Limited || q.Available != 2 {
		t.Fatalf("got %+v want Available=2 Limited=true", q)
	}

	q, err = ParseCFSQuota("-1", "100000")
	if err != nil {
		t.Fatal(err)
	}
	if q.Limited {
		t.Fatalf("quota=-1 should be unlimited, got %+v", q)
	}
}

func TestParseMemoryMax(t *testing.T) {
	t.Parallel()
	limit, known, err := ParseMemoryMax("max")
	if err != nil || known || limit != 0 {
		t.Fatalf("max: limit=%d known=%v err=%v", limit, known, err)
	}
	limit, known, err = ParseMemoryMax("1073741824")
	if err != nil || !known || limit != 1073741824 {
		t.Fatalf("1GiB: limit=%d known=%v err=%v", limit, known, err)
	}
}

func TestParseMemoryLimitBytes(t *testing.T) {
	t.Parallel()
	limit, known, err := ParseMemoryLimitBytes("536870912")
	if err != nil || !known || limit != 536870912 {
		t.Fatalf("got limit=%d known=%v err=%v", limit, known, err)
	}
	_, known, err = ParseMemoryLimitBytes("9223372036854771712")
	if err != nil || known {
		t.Fatalf("unlimited sentinel should be unknown, known=%v err=%v", known, err)
	}
}

func TestParseCPUSet(t *testing.T) {
	t.Parallel()
	n, err := ParseCPUSet("0-3")
	if err != nil || n != 4 {
		t.Fatalf("0-3 => %d err=%v want 4", n, err)
	}
	n, err = ParseCPUSet("0,2,4-6")
	if err != nil || n != 5 {
		t.Fatalf("0,2,4-6 => %d err=%v want 5", n, err)
	}
	n, err = ParseCPUSet("1")
	if err != nil || n != 1 {
		t.Fatalf("1 => %d err=%v want 1", n, err)
	}
}

func TestParseSelfCgroup(t *testing.T) {
	t.Parallel()
	m := ParseSelfCgroup([]byte("0::/kubepods/pod1/container1\n"))
	if m.Version != 2 || m.Unified != "/kubepods/pod1/container1" {
		t.Fatalf("v2: %+v", m)
	}

	m = ParseSelfCgroup([]byte("2:cpu,cpuacct:/docker/abc\n3:memory:/docker/abc\n4:cpuset:/docker/abc\n"))
	if m.Version != 1 || m.CPU != "/docker/abc" || m.Memory != "/docker/abc" || m.CPUSet != "/docker/abc" {
		t.Fatalf("v1: %+v", m)
	}

	m = ParseSelfCgroup([]byte(""))
	if m.Version != 0 {
		t.Fatalf("empty: %+v", m)
	}

	m = ParseSelfCgroup([]byte("not-a-cgroup-line\n:::broken\n"))
	if m.Version != 0 {
		t.Fatalf("malformed: %+v", m)
	}
}

func TestResolveCgroupPath(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	got, ok := ResolveCgroupPath(root, "/kubepods/pod1")
	if !ok || got != "/sys/fs/cgroup/kubepods/pod1" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	got, ok = ResolveCgroupPath(root, "/")
	if !ok || got != root {
		t.Fatalf("root membership: %q ok=%v", got, ok)
	}
	_, ok = ResolveCgroupPath(root, "../../etc")
	if ok {
		t.Fatal("escape should be rejected")
	}
}

type mapReader map[string]string

func (m mapReader) ReadFile(path string) ([]byte, error) {
	v, ok := m[path]
	if !ok {
		return nil, errors.New("not found: " + path)
	}
	return []byte(v), nil
}

func TestApplyCgroupDetectionV2(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		root + "/cpu.max":    "200000 100000",
		root + "/memory.max": "1073741824",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if !r.CPUQuotaDetected || r.AvailableCPUs != 2 {
		t.Fatalf("CPU: %+v", r)
	}
	if r.CPUSource != CPUDetectionCgroupQuota {
		t.Fatalf("source=%q", r.CPUSource)
	}
	if r.Environment != EnvCgroupV2 {
		t.Fatalf("Environment=%q", r.Environment)
	}
	if !r.MemoryLimitKnown || r.MemoryLimitBytes != 1073741824 {
		t.Fatalf("Memory: %+v", r)
	}
}

func TestApplyCgroupDetectionV1(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		root + "/cpu/cpu.cfs_quota_us":         "200000",
		root + "/cpu/cpu.cfs_period_us":        "100000",
		root + "/memory/memory.limit_in_bytes": "536870912",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if !r.CPUQuotaDetected || r.AvailableCPUs != 2 {
		t.Fatalf("CPU: %+v", r)
	}
	if r.Environment != EnvCgroupV1 {
		t.Fatalf("Environment=%q", r.Environment)
	}
	if !r.MemoryLimitKnown || r.MemoryLimitBytes != 536870912 {
		t.Fatalf("Memory: %+v", r)
	}
}

func TestQuotaAndCPUSetMostRestrictive(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"

	t.Run("quota2_cpuset1", func(t *testing.T) {
		t.Parallel()
		rd := mapReader{
			root + "/cpu.max":               "200000 100000",
			root + "/cpuset.cpus.effective": "0",
		}
		var r RuntimeResources
		ApplyCgroupDetection(rd, root, &r)
		if r.AvailableCPUs != 1 || !r.CPUQuotaDetected || !r.CPUSetDetected || r.CPUSource != CPUDetectionCombined {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("quota1_cpuset4", func(t *testing.T) {
		t.Parallel()
		rd := mapReader{
			root + "/cpu.max":               "100000 100000",
			root + "/cpuset.cpus.effective": "0-3",
		}
		var r RuntimeResources
		ApplyCgroupDetection(rd, root, &r)
		if r.AvailableCPUs != 1 {
			t.Fatalf("AvailableCPUs=%g want 1", r.AvailableCPUs)
		}
	})

	t.Run("quota8_cpuset4", func(t *testing.T) {
		t.Parallel()
		rd := mapReader{
			root + "/cpu.max":               "800000 100000",
			root + "/cpuset.cpus.effective": "0-3",
		}
		var r RuntimeResources
		ApplyCgroupDetection(rd, root, &r)
		if r.AvailableCPUs != 4 {
			t.Fatalf("AvailableCPUs=%g want 4", r.AvailableCPUs)
		}
	})
}

func TestUnlimitedCPUMaxUsesCPUSet(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		root + "/cpu.max":               "max 100000",
		root + "/cpuset.cpus.effective": "0-1",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("AvailableCPUs=%g want 2", r.AvailableCPUs)
	}
	if r.CPUQuotaDetected {
		t.Fatal("CPUQuotaDetected should be false for unlimited cpu.max")
	}
	if !r.CPUSetDetected || r.CPUSource != CPUDetectionCPUSet {
		t.Fatalf("cpuset flags/source: %+v", r)
	}
}

func TestApplyCgroupDetectionUnlimitedFallsThrough(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		root + "/cpu.max": "max 100000",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.CPUQuotaDetected || r.AvailableCPUs != 0 {
		t.Fatalf("unlimited without cpuset should leave AvailableCPUs unset, got %+v", r)
	}
}

func TestGOMAXPROCSFallbackSource(t *testing.T) {
	t.Parallel()
	r := RuntimeResources{AvailableCPUs: 0, GOMAXPROCS: 2, LogicalCPUs: 16}
	applyCPUFallbacks(&r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("AvailableCPUs=%g want 2", r.AvailableCPUs)
	}
	if r.CPUSource != CPUDetectionGOMAXPROCS {
		t.Fatalf("source=%q want gomaxprocs", r.CPUSource)
	}
	if r.CPUQuotaDetected {
		t.Fatal("quota flag must stay false")
	}
}

func TestApplyCPUFallbacksZero(t *testing.T) {
	t.Parallel()
	r := RuntimeResources{AvailableCPUs: 0, GOMAXPROCS: 0, LogicalCPUs: 0}
	applyCPUFallbacks(&r)
	if r.AvailableCPUs != 1 || r.CPUSource != CPUDetectionFallback {
		t.Fatalf("final floor: %+v", r)
	}
}

func TestCgroupV2LeafOverRoot(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	leaf := root + "/kubepods/pod1/container1"
	rd := mapReader{
		procSelfCgroup:       "0::/kubepods/pod1/container1\n",
		root + "/cpu.max":    "max 100000",
		leaf + "/cpu.max":    "200000 100000",
		leaf + "/memory.max": "1073741824",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("AvailableCPUs=%g want 2 from leaf", r.AvailableCPUs)
	}
	if !r.CPUQuotaDetected || r.CPUSource != CPUDetectionCgroupQuota {
		t.Fatalf("quota source: %+v", r)
	}
	if !r.MemoryLimitKnown || r.MemoryLimitBytes != 1073741824 {
		t.Fatalf("memory leaf: %+v", r)
	}
	if r.Environment != EnvCgroupV2 || r.CgroupVersion != 2 {
		t.Fatalf("env/version: %+v", r)
	}
}

func TestCgroupLeafMissingFallsBackToRoot(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		procSelfCgroup:    "0::/kubepods/pod1/missing\n",
		root + "/cpu.max": "200000 100000",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("AvailableCPUs=%g want 2 from root fallback", r.AvailableCPUs)
	}
}

func TestMalformedSelfCgroupFallsBackToRoot(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		procSelfCgroup:    "garbage\n",
		root + "/cpu.max": "200000 100000",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("AvailableCPUs=%g want 2", r.AvailableCPUs)
	}
}

func TestPathEscapeFallsBackSafely(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		procSelfCgroup:    "0::/../../etc\n",
		root + "/cpu.max": "200000 100000",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.AvailableCPUs != 2 {
		t.Fatalf("escape should fall back to root, got %+v", r)
	}
}

func TestEnvironmentNotContainerFromCPUSetAlone(t *testing.T) {
	t.Parallel()
	root := "/sys/fs/cgroup"
	rd := mapReader{
		root + "/cpu.max":               "max 100000",
		root + "/cpuset.cpus.effective": "0-1",
	}
	var r RuntimeResources
	ApplyCgroupDetection(rd, root, &r)
	if r.Environment == "container" || r.Environment == "bare-metal" {
		t.Fatalf("must not infer container/bare-metal, got %q", r.Environment)
	}
	if r.Environment != EnvCgroupV2 && r.Environment != EnvHost {
		t.Fatalf("Environment=%q", r.Environment)
	}
}

func TestSystemDetectorFallbacks(t *testing.T) {
	t.Parallel()
	d := SystemDetector{
		Reader:     mapReader{},
		CgroupRoot: "/sys/fs/cgroup",
	}
	r := d.Detect()
	if r.AvailableCPUs <= 0 {
		t.Fatalf("AvailableCPUs must be > 0, got %g", r.AvailableCPUs)
	}
	if r.LogicalCPUs < 1 {
		t.Fatalf("LogicalCPUs=%d", r.LogicalCPUs)
	}
	if r.GOMAXPROCS < 1 {
		t.Fatalf("GOMAXPROCS=%d", r.GOMAXPROCS)
	}
}
