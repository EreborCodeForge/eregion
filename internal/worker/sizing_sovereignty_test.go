package worker

import (
	"log/slog"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/sizing"
	"github.com/EreborCodeForge/Eregion/internal/socket"
)

// Ensures sizing advice never changes pool Desired (no Start / no PHP required).
func TestPoolDesiredIgnoresSizingAdvice(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	cfg.Workers.Count = 16
	cfg.Socket.Directory = t.TempDir()

	sz := sizing.Advisor{}.Analyze(resources.RuntimeResources{AvailableCPUs: 1}, cfg.Workers.Count)
	if !sz.Warning || sz.RecommendedWorkers != 2 {
		t.Fatalf("expected extreme advice, got %+v", sz)
	}
	if cfg.Workers.Count != 16 {
		t.Fatalf("Analyze mutated cfg.Workers.Count to %d", cfg.Workers.Count)
	}

	socks := socket.NewManager(cfg.Socket.Directory, cfg.Socket.DirectoryPermissions, cfg.Socket.SocketPermissions)
	pool := NewPool(cfg, socks, slog.Default(), "test")
	if pool.Snapshot().Desired != 16 {
		t.Fatalf("Desired=%d want 16", pool.Snapshot().Desired)
	}
}
