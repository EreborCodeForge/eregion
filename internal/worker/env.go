package worker

import (
	"os"
	"strconv"
	"strings"
)

const (
	EnvWorkload   = "EREGION_WORKLOAD"
	EnvWorkerID   = "EREGION_WORKER_ID"
	EnvGeneration = "EREGION_GENERATION"
)

// InjectConsumerMetadata copies env and sets Eregion consumer process metadata.
// Eregion values always override conflicting keys already present in env.
// Does not add broker metadata.
func InjectConsumerMetadata(env map[string]string, workload, workerID string, generation uint64) map[string]string {
	out := make(map[string]string, len(env)+3)
	for k, v := range env {
		out[k] = v
	}
	out[EnvWorkload] = workload
	out[EnvWorkerID] = workerID
	out[EnvGeneration] = strconv.FormatUint(generation, 10)
	return out
}

// MergeEnvironment merges process environment with overrides without duplicate keys.
// Override values win. Base entries whose keys appear in overrides are dropped.
func MergeEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		out := make([]string, len(base))
		copy(out, base)
		return out
	}
	skip := make(map[string]struct{}, len(overrides))
	for k := range overrides {
		skip[k] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, e := range base {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		if _, drop := skip[key]; drop {
			continue
		}
		out = append(out, e)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// EnvironMerged returns os.Environ merged with overrides.
func EnvironMerged(overrides map[string]string) []string {
	return MergeEnvironment(os.Environ(), overrides)
}
