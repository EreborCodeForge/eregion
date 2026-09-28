package worker

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/socket"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

// Manager owns one Pool per workload.
type Manager struct {
	cfg     config.Config
	logger  *slog.Logger
	version string

	mu    sync.RWMutex
	pools map[string]*Pool
}

// NewManager creates an empty pool manager.
func NewManager(cfg config.Config, logger *slog.Logger, version string) *Manager {
	return &Manager{
		cfg:     cfg,
		logger:  logger,
		version: version,
		pools:   make(map[string]*Pool),
	}
}

// Ensure creates a pool for spec if missing.
func (m *Manager) Ensure(spec workload.ResolvedWorkloadSpec) (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[spec.Name]; ok {
		return p, nil
	}

	desired := spec.Workers.Min
	if desired == 0 && spec.Scaling.Strategy == workload.StrategyFixed && spec.Workers.Max > 0 && spec.Workers.Min == spec.Workers.Max {
		desired = spec.Workers.Max
	}
	if spec.Scaling.Strategy == workload.StrategyFixed && spec.Workers.Min == spec.Workers.Max {
		desired = spec.Workers.Max
	}
	if desired < spec.Workers.Min {
		desired = spec.Workers.Min
	}

	minReady := 0
	if spec.Mode == workload.ModeHTTP {
		minReady = m.cfg.Workers.MinReady
		if minReady > desired {
			minReady = desired
		}
	}

	var socks *socket.Manager
	if spec.Mode == workload.ModeHTTP {
		dir := filepath.Join(m.cfg.Socket.Directory, spec.Name)
		socks = socket.NewManager(dir, m.cfg.Socket.DirectoryPermissions, m.cfg.Socket.SocketPermissions)
	}

	cfg := m.cfg
	if spec.Resources.MemoryMB > 0 {
		cfg.Workers.MemoryLimitMB = spec.Resources.MemoryMB
	}

	p := NewWorkloadPool(PoolOptions{
		Name:     spec.Name,
		Mode:     spec.Mode,
		Command:  spec.Command,
		Cfg:      cfg,
		Sockets:  socks,
		Logger:   m.logger,
		Version:  m.version,
		Desired:  desired,
		Min:      spec.Workers.Min,
		Max:      spec.Workers.Max,
		MinReady: minReady,
	})
	m.pools[spec.Name] = p
	return p, nil
}

// Get returns a pool by workload name.
func (m *Manager) Get(name string) (*Pool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pools[name]
	return p, ok
}

// HTTP returns the http workload pool if present.
func (m *Manager) HTTP() (*Pool, bool) {
	return m.Get("http")
}

// List returns all pools.
func (m *Manager) List() []*Pool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Pool, 0, len(m.pools))
	for _, p := range m.pools {
		out = append(out, p)
	}
	return out
}

// StartAll starts every managed pool.
func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.RLock()
	pools := make([]*Pool, 0, len(m.pools))
	for _, p := range m.pools {
		pools = append(pools, p)
	}
	m.mu.RUnlock()

	for _, p := range pools {
		if err := p.Start(ctx); err != nil {
			return fmt.Errorf("start workload %q: %w", p.Name(), err)
		}
	}
	return nil
}

// ShutdownAll drains every pool.
func (m *Manager) ShutdownAll(ctx context.Context) error {
	m.mu.RLock()
	pools := make([]*Pool, 0, len(m.pools))
	for _, p := range m.pools {
		pools = append(pools, p)
	}
	m.mu.RUnlock()

	var first error
	for _, p := range pools {
		if err := p.Shutdown(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Remove shuts down and forgets a pool.
func (m *Manager) Remove(ctx context.Context, name string) error {
	m.mu.Lock()
	p, ok := m.pools[name]
	if ok {
		delete(m.pools, name)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return p.Shutdown(ctx)
}
