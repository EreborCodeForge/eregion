package resources

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sampler reads one resource observation. Implementations must be safe for a single collector.
type Sampler interface {
	Sample(ctx context.Context) (Sample, error)
}

// CgroupSampler reads cgroup v1 or v2 files through FileReader.
// It never falls back to GOMAXPROCS or to a fake zero-usage reading.
type CgroupSampler struct {
	Reader     FileReader
	Root       string
	ProcCgroup string
	Clock      Clock

	mu   sync.Mutex
	prev *cpuCursor
}

type cpuCursor struct {
	at        time.Time
	usageUsec float64
	periods   uint64
	throttled uint64
	hasUsage  bool
	hasPeriod bool
}

// Sample implements Sampler.
func (s *CgroupSampler) Sample(ctx context.Context) (Sample, error) {
	if err := ctx.Err(); err != nil {
		return Sample{}, err
	}
	now := time.Now()
	if s.Clock != nil {
		now = s.Clock.Now()
	}
	out := UnknownSample(now)
	rd := s.Reader
	if rd == nil {
		rd = osFileReader{}
	}
	root := s.Root
	if root == "" {
		root = "/sys/fs/cgroup"
	}
	root = path.Clean(root)
	procPath := s.ProcCgroup
	if procPath == "" {
		procPath = procSelfCgroup
	}

	data, err := rd.ReadFile(procPath)
	if err != nil {
		return out, nil
	}
	membership := ParseSelfCgroup(data)
	if membership.Version == 0 {
		return out, nil
	}

	if membership.Version == 2 {
		return s.sampleV2(rd, root, membership, now), nil
	}
	return s.sampleV1(rd, root, membership, now), nil
}

func (s *CgroupSampler) sampleV2(rd FileReader, root string, m CgroupMembership, now time.Time) Sample {
	out := UnknownSample(now)
	out.Source = SourceCgroupV2
	leaf, ok := ResolveCgroupPath(root, m.Unified)
	if !ok {
		out.MemoryLimitValid = ValidityError
		out.MemoryCurrentValid = ValidityUnknown
		out.HierarchyKnown = false
		return out
	}
	dirs := ancestorDirs(leaf, root)
	limit, limitValid, currentDir, hierarchy := tightestMemory(rd, dirs, true)
	out.HierarchyKnown = hierarchy
	out.MemoryLimitValid = limitValid
	out.Scope = "cgroup"
	if limitValid == ValidityKnown {
		out.MemoryLimitBytes = limit
		cur, valid := readMemoryCurrent(rd, currentDir, true)
		out.MemoryCurrentValid = valid
		if valid == ValidityKnown {
			out.MemoryCurrentBytes = cur
		}
	}
	oom, oomValid := readMemoryEvents(rd, leaf, true)
	out.MemoryOOMValid = oomValid
	if oomValid == ValidityKnown {
		out.MemoryEventsOOM = oom
	}
	s.readCPU(rd, dirs, leaf, true, now, &out)
	return out
}

func (s *CgroupSampler) sampleV1(rd FileReader, root string, m CgroupMembership, now time.Time) Sample {
	out := UnknownSample(now)
	out.Source = SourceCgroupV1
	out.Scope = "cgroup"
	memRoot := joinRoot(root, "memory")
	rel := m.Memory
	if rel == "" {
		rel = "/"
	}
	leaf, ok := ResolveCgroupPath(memRoot, rel)
	if !ok {
		out.MemoryLimitValid = ValidityError
		out.HierarchyKnown = false
		return out
	}
	dirs := ancestorDirs(leaf, memRoot)
	limit, limitValid, currentDir, hierarchy := tightestMemory(rd, dirs, false)
	out.HierarchyKnown = hierarchy
	out.MemoryLimitValid = limitValid
	if limitValid == ValidityKnown {
		out.MemoryLimitBytes = limit
		cur, valid := readMemoryCurrent(rd, currentDir, false)
		out.MemoryCurrentValid = valid
		if valid == ValidityKnown {
			out.MemoryCurrentBytes = cur
		}
	}
	oom, oomValid := readMemoryEvents(rd, leaf, false)
	out.MemoryOOMValid = oomValid
	if oomValid == ValidityKnown {
		out.MemoryEventsOOM = oom
	}

	cpuRoot := joinRoot(root, "cpu")
	cpuRel := m.CPU
	if cpuRel == "" {
		cpuRel = "/"
	}
	cpuLeaf, ok := ResolveCgroupPath(cpuRoot, cpuRel)
	if ok {
		s.readCPU(rd, ancestorDirs(cpuLeaf, cpuRoot), cpuLeaf, false, now, &out)
	}
	return out
}

func (s *CgroupSampler) readCPU(rd FileReader, dirs []string, leaf string, v2 bool, now time.Time, out *Sample) {
	cap, capValid := tightestCPU(rd, dirs, v2)
	out.CPUCapacityValid = capValid
	if capValid == ValidityKnown {
		out.CPUCapacity = cap
	}
	usage, usageOK, periods, throttled, periodOK := readCPUCounters(rd, leaf, v2)
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.prev
	s.prev = &cpuCursor{at: now, usageUsec: usage, periods: periods, throttled: throttled, hasUsage: usageOK, hasPeriod: periodOK}
	if prev == nil || !usageOK || !prev.hasUsage || !capValid.known() || cap <= 0 {
		out.CPUUtilizationValid = ValidityUnknown
	} else {
		dt := now.Sub(prev.at).Seconds()
		if dt <= 0 {
			out.CPUUtilizationValid = ValidityError
		} else {
			deltaUsec := usage - prev.usageUsec
			if deltaUsec < 0 {
				out.CPUUtilizationValid = ValidityError
			} else {
				out.CPUUtilization = (deltaUsec / 1e6) / (dt * cap)
				out.CPUUtilizationValid = ValidityKnown
			}
		}
	}
	if prev == nil || !periodOK || !prev.hasPeriod {
		out.CPUThrottlingValid = ValidityUnknown
		return
	}
	dPeriod := periods - prev.periods
	dThrottled := throttled - prev.throttled
	if dPeriod == 0 || dThrottled > dPeriod && periods < prev.periods {
		out.CPUThrottlingValid = ValidityUnknown
		return
	}
	if periods < prev.periods {
		out.CPUThrottlingValid = ValidityError
		return
	}
	if dPeriod == 0 {
		out.CPUThrottlingValid = ValidityUnknown
		return
	}
	out.CPUThrottledRatio = float64(dThrottled) / float64(dPeriod)
	out.CPUThrottlingValid = ValidityKnown
}

func (v FieldValidity) known() bool { return v == ValidityKnown }

func ancestorDirs(leaf, root string) []string {
	leaf = path.Clean(leaf)
	root = path.Clean(root)
	var dirs []string
	dir := leaf
	for {
		dirs = append(dirs, dir)
		if dir == root {
			break
		}
		parent := path.Dir(dir)
		if parent == dir {
			break
		}
		if root != "/" && parent != root && !strings.HasPrefix(parent, root+"/") {
			break
		}
		dir = parent
	}
	return dirs
}

func tightestMemory(rd FileReader, dirs []string, v2 bool) (limit uint64, valid FieldValidity, currentDir string, hierarchy bool) {
	found := false
	var best uint64
	bestDir := ""
	for _, dir := range dirs {
		value, known, v, err := readMemoryLimitFile(rd, dir, v2)
		if err != nil {
			return 0, v, "", false
		}
		if !known {
			continue
		}
		if !found || value < best || value == best {
			best = value
			bestDir = dir
			found = true
		}
	}
	if !found {
		return 0, ValidityUnknown, "", true
	}
	return best, ValidityKnown, bestDir, true
}

func readMemoryLimitFile(rd FileReader, dir string, v2 bool) (uint64, bool, FieldValidity, error) {
	name := "memory.max"
	if !v2 {
		name = "memory.limit_in_bytes"
	}
	data, err := rd.ReadFile(joinRoot(dir, name))
	if err != nil {
		if isAbsent(err) {
			return 0, false, ValidityUnknown, nil
		}
		return 0, false, ValidityError, err
	}
	var (
		limit uint64
		known bool
		perr  error
	)
	if v2 {
		limit, known, perr = ParseMemoryMax(string(data))
	} else {
		limit, known, perr = ParseMemoryLimitBytes(string(data))
	}
	if perr != nil {
		return 0, false, ValidityError, perr
	}
	return limit, known, ValidityKnown, nil
}

func readMemoryCurrent(rd FileReader, dir string, v2 bool) (uint64, FieldValidity) {
	name := "memory.current"
	if !v2 {
		name = "memory.usage_in_bytes"
	}
	data, err := rd.ReadFile(joinRoot(dir, name))
	if err != nil {
		if isAbsent(err) {
			return 0, ValidityUnknown
		}
		return 0, ValidityError
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, ValidityError
	}
	return v, ValidityKnown
}

func readMemoryEvents(rd FileReader, dir string, v2 bool) (uint64, FieldValidity) {
	name := "memory.events"
	if !v2 {
		name = "memory.oom_control"
	}
	data, err := rd.ReadFile(joinRoot(dir, name))
	if err != nil {
		if isAbsent(err) {
			return 0, ValidityUnknown
		}
		return 0, ValidityError
	}
	oom, ok := parseOOMCount(string(data))
	if !ok {
		return 0, ValidityUnknown
	}
	return oom, ValidityKnown
}

func parseOOMCount(content string) (uint64, bool) {
	var oom, kill uint64
	var sawOOM, sawKill bool
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "oom", "oom_kill", "under_oom":
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}
			if fields[0] == "oom_kill" {
				kill = v
				sawKill = true
			} else if fields[0] == "oom" || !sawOOM {
				oom = v
				sawOOM = true
			}
		}
	}
	if sawKill {
		return kill, true
	}
	if sawOOM {
		return oom, true
	}
	return 0, false
}

func tightestCPU(rd FileReader, dirs []string, v2 bool) (float64, FieldValidity) {
	found := false
	best := math.MaxFloat64
	for _, dir := range dirs {
		value, known, err := readCPUQuota(rd, dir, v2)
		if err != nil {
			if isAbsent(err) {
				continue
			}
			return 0, ValidityError
		}
		if !known || value <= 0 {
			continue
		}
		if value < best {
			best = value
			found = true
		}
	}
	if found {
		return best, ValidityKnown
	}
	return 0, ValidityUnknown
}

func readCPUQuota(rd FileReader, dir string, v2 bool) (float64, bool, error) {
	if v2 {
		data, err := rd.ReadFile(joinRoot(dir, "cpu.max"))
		if err != nil {
			return 0, false, err
		}
		q, err := ParseCPUMax(string(data))
		if err != nil {
			return 0, false, err
		}
		return q.Available, q.Limited, nil
	}
	quota, err := rd.ReadFile(joinRoot(dir, "cpu.cfs_quota_us"))
	if err != nil {
		return 0, false, err
	}
	period, err := rd.ReadFile(joinRoot(dir, "cpu.cfs_period_us"))
	if err != nil {
		return 0, false, err
	}
	q, err := ParseCFSQuota(string(quota), string(period))
	if err != nil {
		return 0, false, err
	}
	return q.Available, q.Limited, nil
}

func readCPUCounters(rd FileReader, dir string, v2 bool) (usageUsec float64, usageOK bool, periods, throttled uint64, periodOK bool) {
	statName := "cpu.stat"
	data, err := rd.ReadFile(joinRoot(dir, statName))
	if err == nil {
		usageUsec, usageOK, periods, throttled, periodOK = parseCPUStat(string(data))
	}
	if !v2 && !usageOK {
		raw, err := rd.ReadFile(joinRoot(dir, "cpuacct.usage"))
		if err == nil {
			ns, perr := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
			if perr == nil && ns >= 0 {
				usageUsec = ns / 1000
				usageOK = true
			}
		}
	}
	return usageUsec, usageOK, periods, throttled, periodOK
}

func parseCPUStat(content string) (usageUsec float64, usageOK bool, periods, throttled uint64, periodOK bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "usage_usec":
			v, err := strconv.ParseFloat(fields[1], 64)
			if err == nil && v >= 0 {
				usageUsec = v
				usageOK = true
			}
		case "nr_periods":
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				periods = v
				periodOK = true
			}
		case "nr_throttled":
			v, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				throttled = v
			}
		}
	}
	if !periodOK {
		return usageUsec, usageOK, 0, 0, false
	}
	return usageUsec, usageOK, periods, throttled, true
}

func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist)
}

// SamplerFunc adapts a function to Sampler.
type SamplerFunc func(context.Context) (Sample, error)

// Sample implements Sampler.
func (f SamplerFunc) Sample(ctx context.Context) (Sample, error) { return f(ctx) }

// Collector caches one sampler and never reads files on the metrics scrape path.
type Collector struct {
	Sampler  Sampler
	Interval time.Duration
	MaxAge   time.Duration
	Clock    Clock
	Predict  *Predictor

	mu   sync.RWMutex
	last Sample
	have bool
}

// CollectOnce samples immediately and stores the result.
func (c *Collector) CollectOnce(ctx context.Context) (Sample, error) {
	if c == nil || c.Sampler == nil {
		now := time.Now()
		if c != nil && c.Clock != nil {
			now = c.Clock.Now()
		}
		sample := UnknownSample(now)
		c.store(sample)
		return sample, nil
	}
	sample, err := c.Sampler.Sample(ctx)
	if err != nil {
		now := time.Now()
		if c.Clock != nil {
			now = c.Clock.Now()
		}
		sample = UnknownSample(now)
		sample.MemoryLimitValid = ValidityError
		sample.MemoryCurrentValid = ValidityError
	}
	if c.Predict != nil && sample.MemoryKnown() {
		pred := c.Predict.Observe(sample.At, sample.MemoryCurrentBytes, sample.MemoryLimitBytes, sample.Source)
		sample.PredictionBytes = pred.Bytes
		sample.PredictionValid = pred.Valid
		sample.PredictionConfidence = pred.Confidence
	}
	c.store(sample)
	return sample, nil
}

func (c *Collector) store(sample Sample) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.last = sample
	c.have = true
	c.mu.Unlock()
}

// Have reports whether CollectOnce has stored a sample.
func (c *Collector) Have() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.have
}
func (c *Collector) Current(now time.Time) Sample {
	if c == nil {
		return UnknownSample(now)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.have {
		return UnknownSample(now)
	}
	sample := c.last
	if c.MaxAge > 0 && (sample.At.IsZero() || now.Before(sample.At) || now.Sub(sample.At) > c.MaxAge) {
		return sample.MarkStale()
	}
	return sample
}

// Run samples once and then on each interval until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	if c == nil {
		return
	}
	interval := c.Interval
	if interval <= 0 {
		interval = time.Second
	}
	_, _ = c.CollectOnce(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = c.CollectOnce(ctx)
		}
	}
}
