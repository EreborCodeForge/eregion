package workload

import (
	"fmt"
	"path/filepath"
)

// LegacyHTTPInput carries the existing php/workers/queue settings.
type LegacyHTTPInput struct {
	Name             string
	PHPBinary        string
	WorkerScript     string
	WorkingDirectory string
	WorkerCount      int
	MinReady         int
	MemoryLimitMB    int
}

// AdaptLegacyHTTP builds a ResolvedWorkloadSpec named "http" from legacy config.
// Command is the argv base (php + script); runtime flags are still added by the HTTP starter.
func AdaptLegacyHTTP(in LegacyHTTPInput) (ResolvedWorkloadSpec, error) {
	name := in.Name
	if name == "" {
		name = "http"
	}
	if in.WorkerCount < 1 {
		return ResolvedWorkloadSpec{}, fmt.Errorf("legacy http: workers.count must be >= 1")
	}
	if in.PHPBinary == "" || in.WorkerScript == "" {
		return ResolvedWorkloadSpec{}, fmt.Errorf("legacy http: php.binary and php.worker_script are required")
	}
	script := in.WorkerScript
	if !filepath.IsAbs(script) && in.WorkingDirectory != "" && in.WorkingDirectory != "." {
		// Keep relative; starter uses WorkingDirectory as cmd.Dir.
	}
	return ResolvedWorkloadSpec{
		Name: name,
		Mode: ModeHTTP,
		Command: []string{
			in.PHPBinary,
			script,
		},
		Workers: WorkerPolicy{
			Min: in.WorkerCount,
			Max: in.WorkerCount,
		},
		Resources: ResourcePolicy{
			Class:    ClassBalanced,
			MemoryMB: in.MemoryLimitMB,
		},
		Scaling: DefaultScaling(),
	}, nil
}
