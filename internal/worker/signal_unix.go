//go:build !windows

package worker

import "syscall"

func syscallSIGTERM() syscall.Signal {
	return syscall.SIGTERM
}
