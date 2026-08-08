package sizing

import (
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/resources"
)

func TestAdvisorClassifications(t *testing.T) {
	t.Parallel()
	adv := Advisor{}
	cases := []struct {
		name        string
		cpu         float64
		workers     int
		class       string
		warning     bool
		recommended int
		recMax      int
	}{
		{name: "conservative", cpu: 1, workers: 1, class: ClassCPUConservative, warning: false, recommended: 2, recMax: 4},
		{name: "balanced_1", cpu: 1, workers: 2, class: ClassBalanced, warning: false, recommended: 2, recMax: 4},
		{name: "balanced_2", cpu: 2, workers: 4, class: ClassBalanced, warning: false, recommended: 4, recMax: 8},
		{name: "io_optimized", cpu: 2, workers: 8, class: ClassIOOptimized, warning: false, recommended: 4, recMax: 8},
		{name: "extreme", cpu: 1, workers: 16, class: ClassExtremeOversubscription, warning: true, recommended: 2, recMax: 4},
		// Acceptance examples §65
		{name: "example_a", cpu: 1, workers: 2, class: ClassBalanced, warning: false, recommended: 2, recMax: 4},
		{name: "example_b", cpu: 2, workers: 8, class: ClassIOOptimized, warning: false, recommended: 4, recMax: 8},
		{name: "example_c", cpu: 1, workers: 16, class: ClassExtremeOversubscription, warning: true, recommended: 2, recMax: 4},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sz := adv.Analyze(resources.RuntimeResources{AvailableCPUs: tc.cpu}, tc.workers)
			if sz.Classification != tc.class {
				t.Fatalf("class=%q want %q", sz.Classification, tc.class)
			}
			if sz.Warning != tc.warning {
				t.Fatalf("warning=%v want %v", sz.Warning, tc.warning)
			}
			if sz.RecommendedWorkers != tc.recommended {
				t.Fatalf("recommended=%d want %d", sz.RecommendedWorkers, tc.recommended)
			}
			if sz.RecommendedMax != tc.recMax {
				t.Fatalf("recommended_max=%d want %d", sz.RecommendedMax, tc.recMax)
			}
			if sz.ConfiguredWorkers != tc.workers {
				t.Fatalf("configured must remain %d, got %d", tc.workers, sz.ConfiguredWorkers)
			}
		})
	}
}

func TestAdvisorFractionalCPU(t *testing.T) {
	t.Parallel()
	sz := Advisor{}.Analyze(resources.RuntimeResources{AvailableCPUs: 0.5}, 1)
	if sz.RecommendedMin < 1 || sz.RecommendedWorkers < 1 || sz.RecommendedMax < 1 {
		t.Fatalf("recommendations must be >= 1: %+v", sz)
	}
	if sz.RecommendedMin != 1 || sz.RecommendedWorkers != 1 || sz.RecommendedMax != 2 {
		t.Fatalf("0.5 CPU: want min=1 rec=1 max=2, got min=%d rec=%d max=%d",
			sz.RecommendedMin, sz.RecommendedWorkers, sz.RecommendedMax)
	}
	if sz.WorkersPerCPU != 2 {
		t.Fatalf("workers_per_cpu=%g want 2", sz.WorkersPerCPU)
	}
}

func TestAdvisorFractionalCPU025(t *testing.T) {
	t.Parallel()
	sz := Advisor{}.Analyze(resources.RuntimeResources{AvailableCPUs: 0.25}, 1)
	if sz.RecommendedMin < 1 || sz.RecommendedWorkers < 1 || sz.RecommendedMax < 1 {
		t.Fatalf("recommendations must be >= 1: %+v", sz)
	}
	if sz.WorkersPerCPU != 4 {
		t.Fatalf("workers_per_cpu=%g want 4", sz.WorkersPerCPU)
	}
	// ceil(0.25)=1, ceil(0.5)=1, ceil(1.0)=1
	if sz.RecommendedMin != 1 || sz.RecommendedWorkers != 1 || sz.RecommendedMax != 1 {
		t.Fatalf("0.25 CPU: want min=1 rec=1 max=1, got min=%d rec=%d max=%d",
			sz.RecommendedMin, sz.RecommendedWorkers, sz.RecommendedMax)
	}
}

func TestWorkersCountRemainsSovereign(t *testing.T) {
	t.Parallel()
	configured := 16
	sz := Advisor{}.Analyze(resources.RuntimeResources{AvailableCPUs: 1}, configured)
	if configured != 16 {
		t.Fatalf("Analyze mutated configured variable: %d", configured)
	}
	if sz.ConfiguredWorkers != 16 {
		t.Fatalf("ConfiguredWorkers=%d want 16", sz.ConfiguredWorkers)
	}
	if !sz.Warning || sz.RecommendedWorkers != 2 || sz.RecommendedMax != 4 {
		t.Fatalf("sizing: %+v", sz)
	}
}

func TestAdvisorZeroCPUFallback(t *testing.T) {
	t.Parallel()
	sz := Advisor{}.Analyze(resources.RuntimeResources{
		AvailableCPUs: 0,
		GOMAXPROCS:    0,
		LogicalCPUs:   0,
	}, 4)
	if sz.AvailableCPUs != 1 {
		t.Fatalf("AvailableCPUs=%g want 1", sz.AvailableCPUs)
	}
	if sz.RecommendedWorkers < 1 {
		t.Fatalf("recommended=%d", sz.RecommendedWorkers)
	}
}

func TestClassifyBuckets(t *testing.T) {
	t.Parallel()
	if Classify(1.0) != ClassCPUConservative {
		t.Fatal("<=1 should be conservative")
	}
	if Classify(1.01) != ClassBalanced {
		t.Fatal(">1 <=2 balanced")
	}
	if Classify(4.0) != ClassIOOptimized {
		t.Fatal("<=4 io_optimized")
	}
	if Classify(8.0) != ClassHighOversubscription {
		t.Fatal("<=8 high")
	}
	if Classify(8.01) != ClassExtremeOversubscription {
		t.Fatal(">8 extreme")
	}
}
