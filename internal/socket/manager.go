package socket

import (
	"fmt"
	"os"
	"path/filepath"
)

// Manager owns the private socket directory for worker UDS paths.
type Manager struct {
	dir      string
	dirPerm  os.FileMode
	sockPerm os.FileMode
}

// NewManager creates a socket manager for the given directory.
func NewManager(dir string, dirPerm, sockPerm os.FileMode) *Manager {
	return &Manager{dir: dir, dirPerm: dirPerm, sockPerm: sockPerm}
}

// Prepare ensures the socket directory exists with the configured permissions.
func (m *Manager) Prepare() error {
	if err := os.MkdirAll(m.dir, m.dirPerm); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Chmod(m.dir, m.dirPerm); err != nil {
		return fmt.Errorf("chmod socket directory: %w", err)
	}
	return nil
}

// Path returns the socket path for a worker slot and generation.
func (m *Manager) Path(slot int, generation uint64) string {
	name := fmt.Sprintf("worker-%d-%d.sock", slot, generation)
	return filepath.Join(m.dir, name)
}

// Remove deletes a socket file if present.
func (m *Manager) Remove(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// CleanupDir removes known socket files under the managed directory.
func (m *Manager) CleanupDir() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(m.dir, e.Name()))
	}
	return nil
}

// Dir returns the managed directory path.
func (m *Manager) Dir() string { return m.dir }

// SocketPerm returns configured socket file permissions.
func (m *Manager) SocketPerm() os.FileMode { return m.sockPerm }
