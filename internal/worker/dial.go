package worker

import (
	"context"
	"net"
	"time"
)

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// WaitSocketAvailable polls until the path exists or ctx ends.
func WaitSocketAvailable(ctx context.Context, path string, interval time.Duration) error {
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := net.DialTimeout("unix", path, 50*time.Millisecond); err == nil {
			return nil
		}
		// Also accept when the file exists even if accept isn't ready yet.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := osStat(path); err == nil {
				if c, err := net.DialTimeout("unix", path, 50*time.Millisecond); err == nil {
					_ = c.Close()
					return nil
				}
			}
		}
	}
}
