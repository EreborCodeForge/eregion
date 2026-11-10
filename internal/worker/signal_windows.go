//go:build windows

package worker

import (
	"os"
	"syscall"
)

// Windows has no SIGTERM; callers fall back to Kill when needed.
func syscallSIGTERM() syscall.Signal {
	return syscall.SIGTERM
}

func processStillAlive(p *os.Process) bool {
	return p != nil
}
