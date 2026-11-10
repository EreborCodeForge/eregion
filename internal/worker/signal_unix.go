//go:build !windows

package worker

import (
	"os"
	"syscall"
)

func syscallSIGTERM() syscall.Signal {
	return syscall.SIGTERM
}

func processStillAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
