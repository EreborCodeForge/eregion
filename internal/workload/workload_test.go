package workload_test

import (
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestAdaptLegacyHTTP(t *testing.T) {
	spec, err := workload.AdaptLegacyHTTP(workload.LegacyHTTPInput{
		PHPBinary:     "php",
		WorkerScript:  "bin/worker",
		WorkerCount:   4,
		MemoryLimitMB: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "http" || spec.Mode != workload.ModeHTTP {
		t.Fatalf("got %+v", spec)
	}
	if spec.Workers.Min != 4 || spec.Workers.Max != 4 {
		t.Fatalf("workers %+v", spec.Workers)
	}
	if len(spec.Command) != 2 || spec.Command[0] != "php" {
		t.Fatalf("command %#v", spec.Command)
	}
}

func TestTemplateOverrides(t *testing.T) {
	templates := map[string]workload.WorkloadTemplate{
		"io-consumer": {
			Name:    "io-consumer",
			Mode:    workload.ModeConsumer,
			Workers: workload.WorkerPolicy{Min: 1, Max: 8},
			Resources: workload.ResourcePolicy{
				Class:    workload.ClassIO,
				MemoryMB: 128,
			},
			Scaling: workload.ScalingPolicy{
				Strategy:         workload.StrategyBacklog,
				ScaleUpCooldown:  time.Second,
				ScaleDownIdleFor: 30 * time.Second,
			},
		},
	}
	resolved, err := workload.Resolve(workload.WorkloadSpec{
		Name:     "telemetry",
		Template: "io-consumer",
		Command:  []string{"php", "vendor/bin/job-worker", "--kernel=App\\TelemetryKernel"},
		Queue:    workload.QueueMetadata{Transport: "mqtt", Name: "devices/+/temperature"},
		Workers:  &workload.WorkerPolicyPatch{Max: intPtr(6)},
	}, templates)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Mode != workload.ModeConsumer {
		t.Fatalf("mode=%s", resolved.Mode)
	}
	if resolved.Workers.Min != 1 || resolved.Workers.Max != 6 {
		t.Fatalf("workers %+v (override max)", resolved.Workers)
	}
	if resolved.Resources.Class != workload.ClassIO || resolved.Resources.MemoryMB != 128 {
		t.Fatalf("resources %+v", resolved.Resources)
	}
	if resolved.Scaling.Strategy != workload.StrategyBacklog {
		t.Fatalf("strategy=%s", resolved.Scaling.Strategy)
	}
}

func intPtr(v int) *int { return &v }

func TestConsumerMinZero(t *testing.T) {
	min, max := 0, 4
	resolved, err := workload.Resolve(workload.WorkloadSpec{
		Name:    "batch",
		Mode:    workload.ModeConsumer,
		Command: []string{"php", "vendor/bin/job-worker"},
		Workers: &workload.WorkerPolicyPatch{Min: &min, Max: &max},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Workers.Min != 0 || resolved.Workers.Max != 4 {
		t.Fatalf("%+v", resolved.Workers)
	}
}

func TestConsumerRequiresCommand(t *testing.T) {
	min, max := 1, 1
	_, err := workload.Resolve(workload.WorkloadSpec{
		Name:    "bad",
		Mode:    workload.ModeConsumer,
		Workers: &workload.WorkerPolicyPatch{Min: &min, Max: &max},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildLegacyOnly(t *testing.T) {
	specs, err := workload.BuildFromConfig(nil, nil, workload.LegacyHTTPInput{
		PHPBinary: "php", WorkerScript: "w.php", WorkerCount: 2,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "http" {
		t.Fatalf("%+v", specs)
	}
}

func TestBuildExplicitNoAutoHTTP(t *testing.T) {
	min, max := 0, 2
	specs, err := workload.BuildFromConfig(nil, []workload.WorkloadSpec{{
		Name:    "telemetry",
		Mode:    workload.ModeConsumer,
		Command: []string{"php", "job"},
		Workers: &workload.WorkerPolicyPatch{Min: &min, Max: &max},
	}}, workload.LegacyHTTPInput{
		PHPBinary: "php", WorkerScript: "w.php", WorkerCount: 4,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "telemetry" {
		t.Fatalf("expected only telemetry, got %+v", specs)
	}
}

func TestRegistryCRUD(t *testing.T) {
	r := workload.NewRegistry()
	spec := workload.ResolvedWorkloadSpec{Name: "a", Mode: workload.ModeConsumer, Workers: workload.WorkerPolicy{Min: 0, Max: 1}, Command: []string{"true"}, Scaling: workload.DefaultScaling(), Resources: workload.DefaultResources()}
	if err := r.Upsert(spec); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("a")
	if !ok || got.Name != "a" {
		t.Fatal("get failed")
	}
	if len(r.List()) != 1 {
		t.Fatal("list")
	}
	if err := r.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 0 {
		t.Fatal("len")
	}
}
