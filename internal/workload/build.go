package workload

import "fmt"

// BuildFromConfig resolves templates/workloads and ensures a legacy HTTP workload
// when the YAML did not declare an explicit workloads section.
func BuildFromConfig(
	templates map[string]WorkloadTemplate,
	specs []WorkloadSpec,
	legacy LegacyHTTPInput,
	hasExplicitWorkloads bool,
) ([]ResolvedWorkloadSpec, error) {
	resolved := make([]ResolvedWorkloadSpec, 0, len(specs)+1)
	seen := make(map[string]struct{})

	for _, spec := range specs {
		r, err := Resolve(spec, templates)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[r.Name]; dup {
			return nil, fmt.Errorf("duplicate workload name %q", r.Name)
		}
		seen[r.Name] = struct{}{}
		resolved = append(resolved, r)
	}

	if !hasExplicitWorkloads {
		httpSpec, err := AdaptLegacyHTTP(legacy)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, httpSpec)
	}

	if len(resolved) == 0 {
		return nil, fmt.Errorf("no workloads configured")
	}
	return resolved, nil
}
