package worker

import "testing"

func TestNormalizeRecycleReason(t *testing.T) {
	allowed := []string{
		"max_requests",
		"memory_limit",
		"max_uptime",
		"max_jobs",
		"scope_cleanup_failure",
		"planned",
		"worker_requested",
		"guard_state_violation",
		"guard_resource_violation",
	}
	for _, reason := range allowed {
		if got := NormalizeRecycleReason(reason); got != reason {
			t.Fatalf("%q normalized to %q", reason, got)
		}
	}

	for _, reason := range []string{"", "App\\Security\\UserContext", "guard_state_violation_user-42", "a very long reason that must not become a label"} {
		if got := NormalizeRecycleReason(reason); got != "worker_requested" {
			t.Fatalf("%q normalized to %q", reason, got)
		}
	}
}
