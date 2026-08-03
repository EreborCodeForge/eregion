package socket_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/socket"
)

func TestManagerPathAndCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "eregion")
	m := socket.NewManager(dir, 0o700, 0o600)
	if err := m.Prepare(); err != nil {
		t.Fatal(err)
	}
	path := m.Path(2, 8)
	if filepath.Base(path) != "worker-2-8.sock" {
		t.Fatalf("path %s", path)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Path(1, 1), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CleanupDir(); err != nil {
		t.Fatal(err)
	}
}
