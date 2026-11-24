package signals_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/scaling/signals"
)

func TestECO11T03CounterResetDoesNotUnderflow(t *testing.T) {
	e := signals.NewEngine(nil, nil)
	base := time.Unix(1_700_000_000, 0)
	snap := &signals.HTTPSnap{Duration: make([]uint64, len(signals.HistogramBounds)+1), QueueWait: make([]uint64, len(signals.HistogramBounds)+1)}
	for i := 0; i < 6; i++ {
		snap.Offered = uint64(100 + i*10)
		snap.Completed = snap.Offered
		snap.Success = snap.Offered
		e.Observe(base.Add(time.Duration(i)*time.Second), freshGlobal(base), []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second, HTTP: snap,
		}})
	}
	snap.Offered = 1
	snap.Completed = 1
	snap.Success = 1
	e.Observe(base.Add(6*time.Second), freshGlobal(base), []signals.TickWorkload{{
		Name: "http", Window: 10 * time.Second, HTTP: snap,
	}})
	if _, ok := e.Closed("http"); ok {
		t.Fatal("reset closed an epoch")
	}
}

func TestECO11T04PartialAndRepeatedEpochDoesNotConfirm(t *testing.T) {
	e := signals.NewEngine(nil, nil)
	base := time.Unix(1_700_000_000, 0)
	feedHTTP(e, base, 4, 10)
	e.Observe(base.Add(8*time.Second), freshGlobal(base), []signals.TickWorkload{{
		Name: "http", Window: 10 * time.Second, HTTP: httpSnap(40),
	}})
	if _, ok := e.Closed("http"); ok {
		t.Fatal("gapped epoch closed")
	}
}

func TestECO11T05MeasuredZeroIsNotUnknown(t *testing.T) {
	e := signals.NewEngine(nil, nil)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 11; i++ {
		e.Observe(base.Add(time.Duration(i)*time.Second), freshGlobal(base), []signals.TickWorkload{{
			Name: "jobs", Window: 10 * time.Second, MaxAge: 20 * time.Second,
			Backlog: signals.BacklogSnap{Depth: 0, Valid: true, At: base.Add(time.Duration(i) * time.Second)},
		}})
	}
	got, ok := e.Closed("jobs")
	if !ok || !got.BacklogDepth.Valid || got.BacklogDepth.Value != 0 {
		t.Fatalf("zero backlog = %+v ok=%v", got.BacklogDepth, ok)
	}
	e2 := signals.NewEngine(nil, nil)
	for i := 0; i < 11; i++ {
		e2.Observe(base.Add(time.Duration(i)*time.Second), freshGlobal(base), []signals.TickWorkload{{
			Name: "jobs", Window: 10 * time.Second,
			Backlog: signals.BacklogSnap{Valid: false},
		}})
	}
	unknown, ok := e2.Closed("jobs")
	if !ok || unknown.BacklogDepth.Valid {
		t.Fatalf("missing backlog looked measured: %+v", unknown.BacklogDepth)
	}
}

func TestECO11T07SmallHistogramIsNotAP95(t *testing.T) {
	counts := make([]uint64, len(signals.HistogramBounds)+1)
	counts[8] = 10
	got := signals.Quantile(signals.HistogramBounds, counts, 0.95, 100)
	if got.Valid {
		t.Fatalf("10 samples produced %+v", got)
	}
	counts[8] = 100
	got = signals.Quantile(signals.HistogramBounds, counts, 0.95, 100)
	if !got.Valid {
		t.Fatal("100 samples should be valid")
	}
}

func TestECO11T08PIDReuseInvalidatesCPU(t *testing.T) {
	reader := signals.MapReader{7: {User: 100, Sys: 0, Start: 50, OK: true}}
	e := signals.NewEngine(nil, reader)
	base := time.Unix(1_700_000_000, 0)
	load := func(start uint64, ticks uint64) {
		reader[7] = signals.ProcTimes{User: ticks, Sys: 0, Start: start, OK: true}
		e.Observe(base, freshGlobal(base), []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second,
			Procs: []signals.ProcID{{PID: 7, Generation: 1}},
		}})
	}
	load(50, 100)
	base = base.Add(time.Second)
	reader[7] = signals.ProcTimes{User: 200, Sys: 0, Start: 99, OK: true}
	e.Observe(base, freshGlobal(base), []signals.TickWorkload{{
		Name: "http", Window: 10 * time.Second,
		Procs: []signals.ProcID{{PID: 7, Generation: 1}},
	}})
	for i := 2; i <= 10; i++ {
		base = base.Add(time.Second)
		e.Observe(base, freshGlobal(base), []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second,
			Procs: []signals.ProcID{{PID: 7, Generation: 1}},
		}})
	}
	got, ok := e.Closed("http")
	if ok && got.WorkerCPURatio.Valid {
		t.Fatalf("reused pid produced cpu %+v", got.WorkerCPURatio)
	}
}

func TestECO11T09PoolsDoNotShareCPU(t *testing.T) {
	reader := signals.MapReader{
		1: {User: 0, Sys: 0, Start: 5, OK: true},
		2: {User: 0, Sys: 0, Start: 5, OK: true},
	}
	e := signals.NewEngine(nil, reader)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 11; i++ {
		reader[1] = signals.ProcTimes{User: uint64(i * 50), Sys: 0, Start: 5, OK: true}
		reader[2] = signals.ProcTimes{User: uint64(i * 10), Sys: 0, Start: 5, OK: true}
		at := base.Add(time.Duration(i) * time.Second)
		e.Observe(at, freshGlobal(base), []signals.TickWorkload{
			{Name: "a", Window: 10 * time.Second, Procs: []signals.ProcID{{PID: 1, Generation: 1}}},
			{Name: "b", Window: 10 * time.Second, Procs: []signals.ProcID{{PID: 2, Generation: 1}}},
		})
	}
	a, okA := e.Closed("a")
	b, okB := e.Closed("b")
	if !okA || !okB || !a.WorkerCPURatio.Valid || !b.WorkerCPURatio.Valid {
		t.Fatalf("a=%+v b=%+v", a.WorkerCPURatio, b.WorkerCPURatio)
	}
	if a.WorkerCPURatio.Value <= b.WorkerCPURatio.Value {
		t.Fatalf("pool cpu collapsed a=%v b=%v", a.WorkerCPURatio.Value, b.WorkerCPURatio.Value)
	}
}

func TestECO11T10QuotaOrSourceChangeResetsWindow(t *testing.T) {
	e := signals.NewEngine(nil, nil)
	base := time.Unix(1_700_000_000, 0)
	before := signals.GlobalSnap{CPU: 0.20, CPUKnown: true, Throttle: 0, ThrotKnow: true, At: base, Source: "cgroup_v2", Capacity: 2, CapacityKnown: true}
	for i := 0; i < 6; i++ {
		g := before
		g.At = base.Add(time.Duration(i) * time.Second)
		e.Observe(g.At, g, []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second, HTTP: httpSnap(uint64(i+1) * 10),
		}})
	}
	after := signals.GlobalSnap{CPU: 0.40, CPUKnown: true, Throttle: 0, ThrotKnow: true, Source: "host", Capacity: 1, CapacityKnown: true}
	for i := 0; i < 6; i++ {
		g := after
		g.At = base.Add(time.Duration(6+i) * time.Second)
		e.Observe(g.At, g, []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second, HTTP: httpSnap(uint64(7+i) * 10),
		}})
	}
	if _, ok := e.Closed("http"); ok {
		t.Fatal("mixed quota closed an epoch")
	}
	for i := 6; i < 11; i++ {
		g := after
		g.At = base.Add(time.Duration(6+i) * time.Second)
		e.Observe(g.At, g, []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second, HTTP: httpSnap(uint64(7+i) * 10),
		}})
	}
	got, ok := e.Closed("http")
	if !ok || !got.ResourceCPURatio.Valid || got.ResourceCPURatio.Value != 0.40 || got.ResourceCPURatio.Source != "host" {
		t.Fatalf("window after reset = %+v ok=%v", got.ResourceCPURatio, ok)
	}
}

func TestECO11T12ForgetDropsHistory(t *testing.T) {
	e := signals.NewEngine(nil, nil)
	base := time.Unix(1_700_000_000, 0)
	feedHTTP(e, base, 11, 10)
	e.NoteDecision("http", 1, "demand_up", 2)
	e.Forget("http")
	if _, ok := e.Closed("http"); ok {
		t.Fatal("closed window survived Forget")
	}
	if len(e.Summaries("http")) != 0 {
		t.Fatal("summaries survived Forget")
	}
}

func freshGlobal(at time.Time) signals.GlobalSnap {
	return signals.GlobalSnap{CPU: 0.2, CPUKnown: true, Throttle: 0, ThrotKnow: true, At: at, Source: "cgroup_v2"}
}

func httpSnap(offered uint64) *signals.HTTPSnap {
	return &signals.HTTPSnap{
		Offered: offered, Completed: offered, Success: offered,
		Duration:  make([]uint64, len(signals.HistogramBounds)+1),
		QueueWait: make([]uint64, len(signals.HistogramBounds)+1),
	}
}

func feedHTTP(e *signals.Engine, base time.Time, n int, step uint64) {
	for i := 0; i < n; i++ {
		e.Observe(base.Add(time.Duration(i)*time.Second), freshGlobal(base), []signals.TickWorkload{{
			Name: "http", Window: 10 * time.Second, HTTP: httpSnap(uint64(i+1) * step),
		}})
	}
}
