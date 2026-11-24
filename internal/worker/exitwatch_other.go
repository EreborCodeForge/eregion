//go:build !windows && !linux

package worker

import (
	"os"
	"sync"
	"time"
)

// watchExit polls with signal 0 on platforms that have no pidfd.
// stop ends the goroutine when startup no longer needs it.
func watchExit(proc *os.Process) (<-chan struct{}, func()) {
	exited := make(chan struct{})
	stopCh := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(stopCh) }) }
	if proc == nil {
		close(exited)
		return exited, stop
	}
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			if !processStillAlive(proc) {
				close(exited)
				return
			}
			select {
			case <-stopCh:
				return
			case <-ticker.C:
			}
		}
	}()
	return exited, stop
}
