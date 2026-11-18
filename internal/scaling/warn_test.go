package scaling_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
)

func TestECO09T28WarningDedupeAndHysteresis(t *testing.T) {
	now := time.Unix(1_000, 0)
	clock := resources.FuncClock(func() time.Time { return now })
	warner := &scaling.Warner{Window: time.Minute, Clock: clock}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	sample := resources.Sample{
		At:                 now,
		MemoryLimitBytes:   100,
		MemoryLimitValid:   resources.ValidityKnown,
		MemoryCurrentBytes: 91,
		MemoryCurrentValid: resources.ValidityKnown,
	}
	decision := scaling.CapacityDecision{
		Workload: "http", Strategy: "fixed", Requested: 4, Admitted: 1,
		ConstrainedBy: scaling.ReasonMemoryPressure, Severity: scaling.SeverityCritical,
		Recommendation: "inspect_memory_growth", SampleValid: true,
	}
	if !warner.Log(logger, "enforce", decision, sample) {
		t.Fatal("first warning was suppressed")
	}
	if warner.Log(logger, "enforce", decision, sample) {
		t.Fatal("duplicate warning inside the window")
	}
	now = now.Add(time.Minute)
	if !warner.Log(logger, "enforce", decision, sample) {
		t.Fatal("warning did not return after the window")
	}
	level := scaling.SeverityOK
	for i := 0; i < 3; i++ {
		level = warner.ObserveSample(sample, 0.80, 0.90, 0.85, 0.20)
	}
	if level != scaling.SeverityCritical {
		t.Fatalf("level=%s", level)
	}
	cooled := sample
	cooled.MemoryCurrentBytes = 70
	for i := 0; i < 2; i++ {
		level = warner.ObserveSample(cooled, 0.80, 0.90, 0.85, 0.20)
	}
	if level == scaling.SeverityOK {
		t.Fatal("critical cleared before 3 cool samples")
	}
	level = warner.ObserveSample(cooled, 0.80, 0.90, 0.85, 0.20)
	if level != scaling.SeverityOK {
		t.Fatalf("after 3 cool samples level=%s", level)
	}
}
