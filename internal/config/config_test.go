package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

func TestDefaultValidate(t *testing.T) {
	cfg := config.Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default invalid: %v", err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("host = %q", cfg.Server.Host)
	}
	if cfg.Workers.Count < 1 {
		t.Fatalf("workers.count = %d", cfg.Workers.Count)
	}
	if cfg.Queue.Capacity != cfg.Workers.Count*8 {
		t.Fatalf("queue capacity = %d", cfg.Queue.Capacity)
	}
}

func TestLoadEregionYAML(t *testing.T) {
	root := findRepoRoot(t)
	cfg, err := config.Load(filepath.Join(root, "eregion.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("host = %q", cfg.Server.Host)
	}
	if cfg.Workers.Count != 4 {
		t.Fatalf("workers = %d", cfg.Workers.Count)
	}
	if cfg.Workers.HandshakeTimeout != 5*time.Second {
		t.Fatalf("handshake = %v", cfg.Workers.HandshakeTimeout)
	}
	if cfg.Socket.DirectoryPermissions != 0o700 {
		t.Fatalf("dir perms = %o", cfg.Socket.DirectoryPermissions)
	}
	if cfg.Logging.Format != "json" {
		t.Fatalf("format = %q", cfg.Logging.Format)
	}
	if cfg.PHP.Environment["APP_ENV"] != "production" {
		t.Fatalf("env = %#v", cfg.PHP.Environment)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("version: \"1\"\nserver:\n  host: 127.0.0.1\n  nope: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestProtocolHandshakeRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	content := `
version: "1"
protocol:
  version: 1
  max_frame_bytes: 16777216
  handshake_timeout: 5s
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected protocol.handshake_timeout error")
	}
}

func TestInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("version: \"1\"\nserver:\n  read_timeout: notaduration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected duration error")
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
