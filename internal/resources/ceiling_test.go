package resources

import (
	"testing"
	"time"
)

func TestECO09T38LocalCeilingUsesTheSmallerCgroupLimit(t *testing.T) {
	sample := Sample{
		Source:             SourceCgroupV2,
		Scope:              "cgroup",
		MemoryLimitBytes:   2 << 30,
		MemoryLimitValid:   ValidityKnown,
		MemoryCurrentBytes: 100,
		MemoryCurrentValid: ValidityKnown,
	}
	got := ApplyLocalCeiling(sample, 1024, 0, false)
	if got.MemoryLimitBytes != 1<<30 || got.MemoryCurrentBytes != 100 || got.Scope != "cgroup" {
		t.Fatalf("%+v", got)
	}
}

func TestECO09T37HostCeilingRequiresManagedConsumption(t *testing.T) {
	unknown := UnknownSample(time.Unix(1, 0))
	got := ApplyLocalCeiling(unknown, 2048, 0, false)
	if got.MemoryKnown() {
		t.Fatalf("hard limit without managed memory was treated as known: %+v", got)
	}
	got = ApplyLocalCeiling(unknown, 2048, 4096, true)
	if !got.MemoryKnown() || got.MemoryLimitBytes != 2048<<20 || got.MemoryCurrentBytes != 4096 || got.Scope != "process_tree" {
		t.Fatalf("%+v", got)
	}
}
