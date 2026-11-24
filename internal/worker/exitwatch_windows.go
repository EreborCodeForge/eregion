//go:build windows

package worker

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW           = kernel32.NewProc("CreateEventW")
	procSetEvent               = kernel32.NewProc("SetEvent")
	procWaitForMultipleObjects = kernel32.NewProc("WaitForMultipleObjects")
)

// watchExit reports process exit without calling os.Process.Wait or exec.Cmd.Wait.
// The pool monitor is the only owner of Wait. stop unblocks the watcher when the
// socket is ready so the goroutine does not outlive startup.
func watchExit(proc *os.Process) (<-chan struct{}, func()) {
	noop := func() {}
	if proc == nil {
		exited := make(chan struct{})
		close(exited)
		return exited, noop
	}
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(proc.Pid))
	if err != nil {
		return nil, noop
	}
	event, err := createManualEvent()
	if err != nil {
		_ = syscall.CloseHandle(handle)
		return nil, noop
	}

	exited := make(chan struct{})
	state := &winExitWatch{event: event}
	go func() {
		handles := [2]syscall.Handle{handle, event}
		r, _, _ := procWaitForMultipleObjects.Call(
			uintptr(len(handles)),
			uintptr(unsafe.Pointer(&handles[0])),
			0,
			0xFFFFFFFF,
		)
		state.close(handle)
		// WAIT_OBJECT_0 is the process handle. The event means stop.
		if uint32(r) == 0 {
			close(exited)
		}
	}()
	return exited, state.signal
}

type winExitWatch struct {
	mu     sync.Mutex
	event  syscall.Handle
	closed bool
}

func (w *winExitWatch) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.event == 0 {
		return
	}
	_, _, _ = procSetEvent.Call(uintptr(w.event))
}

func (w *winExitWatch) close(process syscall.Handle) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.event != 0 {
		_ = syscall.CloseHandle(w.event)
		w.event = 0
	}
	if process != 0 {
		_ = syscall.CloseHandle(process)
	}
}

func createManualEvent() (syscall.Handle, error) {
	// Manual-reset, initially nonsignaled, unnamed.
	r, _, err := procCreateEventW.Call(0, 1, 0, 0)
	if r == 0 {
		if err == nil {
			err = syscall.EINVAL
		}
		return 0, err
	}
	return syscall.Handle(r), nil
}
