package resources

import "time"

// FieldValidity is the measurement state of one sample field.
// A numeric zero is a real reading only when the matching validity is known.
type FieldValidity string

const (
	ValidityKnown   FieldValidity = "known"
	ValidityUnknown FieldValidity = "unknown"
	ValidityStale   FieldValidity = "stale"
	ValidityError   FieldValidity = "error"
)

// SampleSource is the closed set of resource origins.
type SampleSource string

const (
	SourceCgroupV2 SampleSource = "cgroup_v2"
	SourceCgroupV1 SampleSource = "cgroup_v1"
	SourceHost     SampleSource = "host"
	SourceUnknown  SampleSource = "unknown"
)

// Sample is one observation of CPU and memory. Fields are independent.
type Sample struct {
	At                 time.Time
	CPUCapacity        float64
	CPUUtilization     float64
	CPUThrottledRatio  float64
	MemoryLimitBytes   uint64
	MemoryCurrentBytes uint64
	MemoryEventsOOM    uint64

	CPUCapacityValid    FieldValidity
	CPUUtilizationValid FieldValidity
	CPUThrottlingValid  FieldValidity
	MemoryLimitValid    FieldValidity
	MemoryCurrentValid  FieldValidity
	MemoryOOMValid      FieldValidity

	Source               SampleSource
	HierarchyKnown       bool
	Scope                string
	PredictionBytes      uint64
	PredictionValid      bool
	PredictionConfidence string
}

// Known reports whether a validity is a usable fresh reading.
func Known(v FieldValidity) bool { return v == ValidityKnown }

// MemoryKnown is true when limit and current describe the same fresh scope.
func (s Sample) MemoryKnown() bool {
	return Known(s.MemoryLimitValid) && Known(s.MemoryCurrentValid) && s.MemoryLimitBytes > 0
}

// MarkStale keeps the last numbers but withdraws trust from fresh readings.
func (s Sample) MarkStale() Sample {
	if s.CPUCapacityValid == ValidityKnown {
		s.CPUCapacityValid = ValidityStale
	}
	if s.CPUUtilizationValid == ValidityKnown {
		s.CPUUtilizationValid = ValidityStale
	}
	if s.CPUThrottlingValid == ValidityKnown {
		s.CPUThrottlingValid = ValidityStale
	}
	if s.MemoryLimitValid == ValidityKnown {
		s.MemoryLimitValid = ValidityStale
	}
	if s.MemoryCurrentValid == ValidityKnown {
		s.MemoryCurrentValid = ValidityStale
	}
	if s.MemoryOOMValid == ValidityKnown {
		s.MemoryOOMValid = ValidityStale
	}
	s.PredictionValid = false
	s.PredictionConfidence = ""
	return s
}

// UnknownSample is an explicit lack of data, not a zero-usage host.
func UnknownSample(at time.Time) Sample {
	return Sample{
		At:                  at,
		Source:              SourceUnknown,
		CPUCapacityValid:    ValidityUnknown,
		CPUUtilizationValid: ValidityUnknown,
		CPUThrottlingValid:  ValidityUnknown,
		MemoryLimitValid:    ValidityUnknown,
		MemoryCurrentValid:  ValidityUnknown,
		MemoryOOMValid:      ValidityUnknown,
	}
}
