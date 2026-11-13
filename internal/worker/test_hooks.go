package worker

import (
	"os/exec"

	"github.com/EreborCodeForge/Eregion/internal/protocol"
)

// BindForTest attaches the connection and process a pool test worker uses.
// Production code does not call this.
func BindForTest(w *Worker, conn *protocol.Conn, cmd *exec.Cmd) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.conn = conn
	w.cmd = cmd
	w.mu.Unlock()
}
