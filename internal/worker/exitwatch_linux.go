//go:build linux

package worker

import (
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// watchExit blocks in the kernel on a pidfd. It does not reap the child:
// exec.Cmd.Wait stays owned by the pool monitor. stop wakes the watcher so
// the goroutine ends when startup no longer needs it.
func watchExit(proc *os.Process) (<-chan struct{}, func()) {
	if proc == nil {
		exited := make(chan struct{})
		close(exited)
		return exited, func() {}
	}
	if exited, stop, ok := startPidfdWatch(proc.Pid); ok {
		return exited, stop
	}
	return startPollWatch(proc)
}

func startPidfdWatch(pid int) (<-chan struct{}, func(), bool) {
	pidfd, err := openPidfd(pid)
	if err != nil {
		return nil, nil, false
	}
	wakeRead, wakeWrite, err := os.Pipe()
	if err != nil {
		_ = syscall.Close(pidfd)
		return nil, nil, false
	}
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		_ = wakeRead.Close()
		_ = wakeWrite.Close()
		_ = syscall.Close(pidfd)
		return nil, nil, false
	}
	if err := epollAdd(epfd, pidfd); err != nil || epollAdd(epfd, int(wakeRead.Fd())) != nil {
		_ = wakeRead.Close()
		_ = wakeWrite.Close()
		_ = syscall.Close(epfd)
		_ = syscall.Close(pidfd)
		return nil, nil, false
	}

	exited := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { _ = wakeWrite.Close() }) }
	go func() {
		defer wakeRead.Close()
		defer syscall.Close(epfd)
		defer syscall.Close(pidfd)
		var events [2]syscall.EpollEvent
		for {
			n, err := syscall.EpollWait(epfd, events[:], -1)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				if events[i].Fd == int32(pidfd) {
					close(exited)
					return
				}
			}
			return
		}
	}()
	return exited, stop, true
}

func startPollWatch(proc *os.Process) (<-chan struct{}, func()) {
	exited := make(chan struct{})
	stopCh := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(stopCh) }) }
	go pollExit(proc, exited, stopCh)
	return exited, stop
}

func openPidfd(pid int) (int, error) {
	number, ok := pidfdSyscallNumber()
	if !ok {
		return -1, syscall.ENOSYS
	}
	r1, _, errno := syscall.Syscall(number, uintptr(pid), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	fd := int(r1)
	syscall.CloseOnExec(fd)
	return fd, nil
}

func pidfdSyscallNumber() (uintptr, bool) {
	// pidfd_open is 434 on the architectures Eregion is exercised on.
	// Other architectures fall back to a cancellable poll.
	switch runtime.GOARCH {
	case "386", "amd64", "arm", "arm64", "riscv64", "loong64":
		return 434, true
	default:
		return 0, false
	}
}

func epollAdd(epfd, fd int) error {
	event := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(fd)}
	return syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, fd, &event)
}

func pollExit(proc *os.Process, exited chan struct{}, stop <-chan struct{}) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if proc == nil || !processStillAlive(proc) {
			close(exited)
			return
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
