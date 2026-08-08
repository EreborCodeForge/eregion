package sizing

// Classification labels for workers-per-CPU ratio (diagnostic only).
const (
	ClassCPUConservative         = "cpu_conservative"
	ClassBalanced                = "balanced"
	ClassIOOptimized             = "io_optimized"
	ClassHighOversubscription    = "high_oversubscription"
	ClassExtremeOversubscription = "extreme_oversubscription"
)

// Classify maps workers_per_cpu to a diagnostic classification.
func Classify(workersPerCPU float64) string {
	switch {
	case workersPerCPU <= 1.0:
		return ClassCPUConservative
	case workersPerCPU <= 2.0:
		return ClassBalanced
	case workersPerCPU <= 4.0:
		return ClassIOOptimized
	case workersPerCPU <= 8.0:
		return ClassHighOversubscription
	default:
		return ClassExtremeOversubscription
	}
}
