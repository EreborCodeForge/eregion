//go:build windows

package worker

import "syscall"

// Windows has no SIGTERM; callers fall back to Kill when needed.
func syscallSIGTERM() syscall.Signal {
	return syscall.SIGTERM
}
