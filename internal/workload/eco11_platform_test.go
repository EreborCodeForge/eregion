package workload_test

import (
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestECO11T11CPURejectedWithoutWorkerCPU(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:    "http",
		Mode:    workload.ModeHTTP,
		Workers: workload.WorkerPolicy{Min: 1, Max: 2},
		Scaling: workload.ApplyAdvancedDefaults(workload.ScalingPolicy{Strategy: workload.StrategyCPUUtilization}),
	}
	err := workload.ValidateAdvanced(spec, "observe", workload.PlatformCaps{WorkerCPU: false})
	if err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("err=%v", err)
	}
}
