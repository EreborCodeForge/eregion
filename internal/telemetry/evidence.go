package telemetry

import "runtime"

// StatusTally counts HTTP responses. 502, 503 and 504 stay visible as errors.
type StatusTally struct {
	codes map[int]int
}

func (s *StatusTally) Observe(code int) {
	if s.codes == nil {
		s.codes = map[int]int{}
	}
	s.codes[code]++
}

func (s *StatusTally) GatewayErrors() int {
	return s.codes[502] + s.codes[503] + s.codes[504]
}

// FrameworkRouteReport rejects health aliases as proof that a framework pipeline ran.
func FrameworkRouteReport(path string) bool {
	switch path {
	case "/api/health", "/_eregion/health", "/health":
		return false
	default:
		return path != ""
	}
}

// EnvironmentManifest is the comparable header of a profile run.
type EnvironmentManifest struct {
	Go      string
	Binary  string
	Version string
}

func NewEnvironmentManifest(binary, version string) EnvironmentManifest {
	return EnvironmentManifest{Go: runtime.Version(), Binary: binary, Version: version}
}
