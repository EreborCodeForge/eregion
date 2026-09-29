package worker_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestMergeEnvironmentNoDuplicates(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=old", "HOME=/tmp"}
	out := worker.MergeEnvironment(base, map[string]string{"FOO": "new", "BAR": "1"})
	sort.Strings(out)
	got := map[string]string{}
	for _, e := range out {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("bad entry %q", e)
		}
		if _, exists := got[k]; exists {
			t.Fatalf("duplicate key %q", k)
		}
		got[k] = v
	}
	want := map[string]string{"PATH": "/usr/bin", "FOO": "new", "HOME": "/tmp", "BAR": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestInjectConsumerMetadataOverrides(t *testing.T) {
	env := map[string]string{
		worker.EnvWorkload:   "stale",
		worker.EnvWorkerID:   "stale-id",
		worker.EnvGeneration: "99",
		"KEEP":               "yes",
	}
	out := worker.InjectConsumerMetadata(env, "telemetry", "consumer-2", 4)
	if out[worker.EnvWorkload] != "telemetry" {
		t.Fatalf("workload=%q", out[worker.EnvWorkload])
	}
	if out[worker.EnvWorkerID] != "consumer-2" {
		t.Fatalf("worker_id=%q", out[worker.EnvWorkerID])
	}
	if out[worker.EnvGeneration] != "4" {
		t.Fatalf("generation=%q", out[worker.EnvGeneration])
	}
	if out["KEEP"] != "yes" {
		t.Fatalf("KEEP lost")
	}
	if env[worker.EnvWorkload] != "stale" {
		t.Fatal("mutated input map")
	}
}
