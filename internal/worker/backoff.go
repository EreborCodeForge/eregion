package worker

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

// Backoff computes restart delays with optional jitter.
type Backoff struct {
	cfg config.BackoffConfig
	mu  sync.Mutex
	n   int
}

// NewBackoff creates a backoff calculator.
func NewBackoff(cfg config.BackoffConfig) *Backoff {
	return &Backoff{cfg: cfg}
}

// Next returns the delay for the next restart attempt and increments the counter.
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := float64(b.cfg.Initial) * math.Pow(b.cfg.Multiplier, float64(b.n))
	if d > float64(b.cfg.Maximum) {
		d = float64(b.cfg.Maximum)
	}
	b.n++
	if b.cfg.Jitter > 0 {
		j := (rand.Float64()*2 - 1) * b.cfg.Jitter
		d = d * (1 + j)
		if d < 0 {
			d = 0
		}
		if d > float64(b.cfg.Maximum) {
			d = float64(b.cfg.Maximum)
		}
	}
	return time.Duration(d)
}

// Reset clears the failure streak after a successful start.
func (b *Backoff) Reset() {
	b.mu.Lock()
	b.n = 0
	b.mu.Unlock()
}
