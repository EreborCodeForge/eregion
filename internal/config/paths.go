package config

import (
	"fmt"
	"path"
	"strings"
)

// ResolveOperationPath joins operations.prefix with an endpoint path.
// Legacy absolute paths that already start with the prefix are kept as-is.
func ResolveOperationPath(prefix, endpointPath string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	endpointPath = strings.TrimSpace(endpointPath)
	if prefix == "" {
		return "", fmt.Errorf("operations.prefix is required")
	}
	if !strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("operations.prefix must start with /")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "" {
		prefix = "/"
	}
	if endpointPath == "" {
		return "", fmt.Errorf("endpoint path is required")
	}
	if !strings.HasPrefix(endpointPath, "/") {
		endpointPath = "/" + endpointPath
	}

	if endpointPath == prefix || strings.HasPrefix(endpointPath, prefix+"/") {
		cleaned := path.Clean(endpointPath)
		if !strings.HasPrefix(cleaned, "/") {
			cleaned = "/" + cleaned
		}
		return cleaned, nil
	}

	joined := path.Clean(prefix + endpointPath)
	if !strings.HasPrefix(joined, "/") {
		joined = "/" + joined
	}
	return joined, nil
}

// ResolveEndpoints rewrites Metrics/Health/Readiness/Liveness paths using operations.prefix.
func (c *Config) ResolveEndpoints() error {
	var err error
	if c.Metrics.Path, err = ResolveOperationPath(c.Operations.Prefix, c.Metrics.Path); err != nil {
		return fmt.Errorf("metrics.path: %w", err)
	}
	if c.Health.Path, err = ResolveOperationPath(c.Operations.Prefix, c.Health.Path); err != nil {
		return fmt.Errorf("health.path: %w", err)
	}
	if c.Readiness.Path, err = ResolveOperationPath(c.Operations.Prefix, c.Readiness.Path); err != nil {
		return fmt.Errorf("readiness.path: %w", err)
	}
	if c.Liveness.Path, err = ResolveOperationPath(c.Operations.Prefix, c.Liveness.Path); err != nil {
		return fmt.Errorf("liveness.path: %w", err)
	}
	return nil
}

func validateEndpointCollisions(c Config) error {
	type ep struct {
		name string
		path string
		on   bool
	}
	eps := []ep{
		{"metrics", c.Metrics.Path, c.Metrics.Enabled},
		{"health", c.Health.Path, c.Health.Enabled},
		{"readiness", c.Readiness.Path, c.Readiness.Enabled},
		{"liveness", c.Liveness.Path, c.Liveness.Enabled},
	}
	seen := map[string]string{}
	for _, e := range eps {
		if !e.on {
			continue
		}
		if prev, ok := seen[e.path]; ok {
			return fmt.Errorf("duplicate operation endpoint path %q: %s and %s", e.path, prev, e.name)
		}
		seen[e.path] = e.name
	}
	return nil
}
