package worker

import (
	"fmt"
	"strings"
)

const (
	StageSocketPath = "socket_path"
	StageSpawn      = "spawn"
	StageWaitSocket = "wait_socket"
	StageDial       = "dial"
	StageHandshake  = "handshake"

	CausePathTooLong     = "unix_path_too_long"
	CausePathInvalid     = "socket_path_invalid"
	CauseSpawnFailed     = "spawn_failed"
	CauseWorkerExited    = "worker_exited"
	CauseBindFailed      = "bind_failed"
	CauseSocketMissing   = "socket_missing"
	CauseDialFailed      = "dial_failed"
	CauseHandshakeFailed = "handshake_failed"
)

// StartupError is a worker boot failure with the workload, worker, stage and
// a classified cause. Stderr is a bounded snapshot of the child process.
type StartupError struct {
	Workload string
	WorkerID string
	Stage    string
	Cause    string
	Path     string
	Stderr   string
	Err      error
}

func (e *StartupError) Error() string {
	if e == nil {
		return "worker startup failed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "worker startup workload=%s worker=%s stage=%s cause=%s", e.Workload, e.WorkerID, e.Stage, e.Cause)
	if e.Path != "" {
		fmt.Fprintf(&b, " path=%s", e.Path)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if msg := strings.TrimSpace(e.Stderr); msg != "" {
		fmt.Fprintf(&b, ": %s", msg)
	}
	return b.String()
}

func (e *StartupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func causeFromStderr(stderr string, exited bool) string {
	msg := strings.ToLower(stderr)
	switch {
	case strings.Contains(msg, "must be less than") && strings.Contains(msg, "byte"):
		return CausePathTooLong
	case strings.Contains(msg, "socket_bind"):
		return CauseBindFailed
	case exited:
		return CauseWorkerExited
	default:
		return CauseSocketMissing
	}
}
