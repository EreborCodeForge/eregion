package worker

import "strings"

// ClassifyExit maps a process exit onto planned|crash|timeout|protocol|other.
// The returned value is the only label published on the exit counter.
func ClassifyExit(planned bool, exitCode int, raw string) string {
	switch raw {
	case "planned", "crash", "timeout", "protocol", "other":
		return raw
	}
	if planned {
		return "planned"
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "protocol"):
		return "protocol"
	case exitCode != 0:
		return "crash"
	default:
		return "other"
	}
}
