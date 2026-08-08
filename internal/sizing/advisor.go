// Package sizing recommends PHP worker pool sizes from detected runtime resources.
// Recommendations are advisory only; configured worker count is never mutated.
package sizing

import (
	"fmt"
	"math"

	"github.com/EreborCodeForge/Eregion/internal/resources"
)

// WorkerSizing is the result of analyzing configured workers against available CPU.
type WorkerSizing struct {
	ConfiguredWorkers  int
	AvailableCPUs      float64
	WorkersPerCPU      float64
	RecommendedWorkers int
	RecommendedMin     int
	RecommendedMax     int
	Classification     string
	Warning            bool
	Reason             string
}

// Advisor computes worker sizing recommendations.
type Advisor struct{}

// Analyze returns a pure, deterministic recommendation for configuredWorkers.
func (Advisor) Analyze(res resources.RuntimeResources, configuredWorkers int) WorkerSizing {
	available := res.AvailableCPUs
	if available <= 0 {
		available = float64(res.GOMAXPROCS)
	}
	if available <= 0 {
		available = float64(res.LogicalCPUs)
	}
	if available <= 0 {
		available = 1
	}

	if configuredWorkers < 1 {
		configuredWorkers = 1
	}

	min := ceilWorkers(available * 1)
	rec := ceilWorkers(available * 2)
	max := ceilWorkers(available * 4)

	ratio := float64(configuredWorkers) / available
	class := Classify(ratio)
	warn := ratio > 8

	reason := fmt.Sprintf(
		"configured=%d available_cpu=%g workers_per_cpu=%.2f classification=%s",
		configuredWorkers, available, ratio, class,
	)
	if warn {
		reason = "worker pool may be oversized for available CPU; " + reason
	}

	return WorkerSizing{
		ConfiguredWorkers:  configuredWorkers,
		AvailableCPUs:      available,
		WorkersPerCPU:      ratio,
		RecommendedWorkers: rec,
		RecommendedMin:     min,
		RecommendedMax:     max,
		Classification:     class,
		Warning:            warn,
		Reason:             reason,
	}
}

func ceilWorkers(v float64) int {
	n := int(math.Ceil(v))
	if n < 1 {
		return 1
	}
	return n
}
