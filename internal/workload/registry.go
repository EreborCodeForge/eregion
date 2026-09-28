package workload

import (
	"fmt"
	"sync"
)

// Registry is the desired-state store for workloads inside Eregion.
type Registry struct {
	mu   sync.RWMutex
	byName map[string]ResolvedWorkloadSpec
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]ResolvedWorkloadSpec)}
}

// Upsert creates or replaces a workload.
func (r *Registry) Upsert(spec ResolvedWorkloadSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("workload name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[spec.Name] = spec
	return nil
}

// Update replaces an existing workload; errors if missing.
func (r *Registry) Update(spec ResolvedWorkloadSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("workload name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[spec.Name]; !ok {
		return fmt.Errorf("workload %q not found", spec.Name)
	}
	r.byName[spec.Name] = spec
	return nil
}

// Remove deletes a workload.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byName[name]; !ok {
		return fmt.Errorf("workload %q not found", name)
	}
	delete(r.byName, name)
	return nil
}

// Get returns a workload by name.
func (r *Registry) Get(name string) (ResolvedWorkloadSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[name]
	return s, ok
}

// List returns all workloads (order not guaranteed).
func (r *Registry) List() []ResolvedWorkloadSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ResolvedWorkloadSpec, 0, len(r.byName))
	for _, s := range r.byName {
		out = append(out, s)
	}
	return out
}

// Len returns the number of registered workloads.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}
