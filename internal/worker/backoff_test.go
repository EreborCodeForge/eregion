package worker_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestBackoffIncreases(t *testing.T) {
	b := worker.NewBackoff(config.BackoffConfig{
		Initial:    100 * time.Millisecond,
		Maximum:    time.Second,
		Multiplier: 2,
		Jitter:     0,
	})
	d1 := b.Next()
	d2 := b.Next()
	if d1 != 100*time.Millisecond {
		t.Fatalf("d1=%v", d1)
	}
	if d2 != 200*time.Millisecond {
		t.Fatalf("d2=%v", d2)
	}
	b.Reset()
	if b.Next() != 100*time.Millisecond {
		t.Fatal("reset failed")
	}
}
