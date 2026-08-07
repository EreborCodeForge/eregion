package worker

import (
	"os"
	"strings"
)

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
