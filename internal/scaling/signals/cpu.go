package signals

import "time"

// ProcID is one managed worker. CreateToken is the OS process start identity.
type ProcID struct {
	PID        int
	Generation uint64
}

// ProcTimes is one reading of a process CPU clock.
type ProcTimes struct {
	User  uint64
	Sys   uint64
	Start uint64
	OK    bool
}

// CPUReader reads process CPU without retaining the sample itself.
type CPUReader interface {
	Read(pid int) ProcTimes
}

// MapReader is a test double.
type MapReader map[int]ProcTimes

func (m MapReader) Read(pid int) ProcTimes {
	if m == nil {
		return ProcTimes{}
	}
	return m[pid]
}

type procPoint struct {
	pid   int
	gen   uint64
	start uint64
	ticks uint64
}

// AttributeCPU converts two stable sets into a per-worker core ratio.
// 1 means one core per worker. The ratio is not truncated when a process
// uses more than one core. A changed set invalidates the interval.
func AttributeCPU(prev, cur []procPoint, dt time.Duration, workers int, ticksPerSecond float64) (float64, InvalidReason) {
	if workers < 1 || dt <= 0 || ticksPerSecond <= 0 {
		return 0, ReasonInsufficient
	}
	if len(prev) == 0 || len(cur) == 0 || len(prev) != len(cur) {
		return 0, ReasonMixedGeneration
	}
	prevBy := map[int]procPoint{}
	for _, p := range prev {
		if p.pid <= 0 {
			return 0, ReasonMissing
		}
		if _, dup := prevBy[p.pid]; dup {
			return 0, ReasonMixedGeneration
		}
		prevBy[p.pid] = p
	}
	var delta uint64
	for _, c := range cur {
		p, ok := prevBy[c.pid]
		if !ok || p.gen != c.gen || p.start != c.start || p.start == 0 || c.start == 0 {
			return 0, ReasonMixedGeneration
		}
		if c.ticks < p.ticks {
			return 0, ReasonReset
		}
		delta += c.ticks - p.ticks
		delete(prevBy, c.pid)
	}
	if len(prevBy) != 0 {
		return 0, ReasonMixedGeneration
	}
	seconds := dt.Seconds()
	ratio := (float64(delta) / ticksPerSecond) / seconds / float64(workers)
	if ratio < 0 {
		return 0, ReasonReset
	}
	return ratio, ""
}
