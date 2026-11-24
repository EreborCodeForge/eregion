package integration_test

import (
	"os"
	"runtime"
	"testing"
)

// TestECO11PerformanceGate keeps the 20-pair ECO-11 bench open.
// A skip is not a pass. Windows does not prove the Linux cgroup gate.
func TestECO11PerformanceGate(t *testing.T) {
	if os.Getenv("EREGION_ECO11_BENCH") == "" || runtime.GOOS != "linux" {
		t.Skip("ECO-11 performance gate was not executed. Skip is not PASS. It needs EREGION_ECO11_BENCH=1 on Linux.")
	}
	runECO11OfficialGate(t)
}
