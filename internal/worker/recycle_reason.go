package worker

// NormalizeRecycleReason collapses recycle reasons before they become metric labels.
// Unknown, empty, or application-specific strings use worker_requested so labels stay bounded.
func NormalizeRecycleReason(reason string) string {
	switch reason {
	case "max_requests", "memory_limit", "max_uptime", "max_jobs",
		"scope_cleanup_failure", "planned", "worker_requested",
		"guard_state_violation", "guard_resource_violation":
		return reason
	default:
		return "worker_requested"
	}
}
