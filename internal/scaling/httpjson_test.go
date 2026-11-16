package scaling_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/resources"
	"github.com/EreborCodeForge/Eregion/internal/scaling"
	"github.com/EreborCodeForge/Eregion/internal/workload"
)

func TestHTTPJSONBacklogScenarios(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		if r.URL.Path == "/deny" && r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/zero":
			_, _ = w.Write([]byte(`{"backlog":0,"ignored":true}`))
		case "/eighty":
			_, _ = w.Write([]byte(`{"backlog":80}`))
		case "/fivehundred":
			_, _ = w.Write([]byte(`{"backlog":500}`))
		case "/bad":
			_, _ = w.Write([]byte(`{"backlog":-1}`))
		case "/deny":
			_, _ = w.Write([]byte(`{"backlog":3}`))
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("ECO03_ZERO_URL", srv.URL+"/zero")
	t.Setenv("ECO03_EIGHTY_URL", srv.URL+"/eighty")
	t.Setenv("ECO03_BIG_URL", srv.URL+"/fivehundred")
	t.Setenv("ECO03_BAD_URL", srv.URL+"/bad")
	t.Setenv("ECO03_MISSING_URL", srv.URL+"/missing")
	t.Setenv("ECO03_SLOW_URL", srv.URL+"/slow")
	t.Setenv("ECO03_TOKEN_URL", srv.URL+"/deny")
	t.Setenv("ECO03_TOKEN", "secret")
	t.Setenv("ECO03_PUBLIC_URL", "http://example.com/backlog")

	provider := scaling.NewHTTPJSONBacklogProvider([]workload.ResolvedWorkloadSpec{
		backlogSpec("zero", "ECO03_ZERO_URL", "", 250*time.Millisecond),
		backlogSpec("eighty", "ECO03_EIGHTY_URL", "", 250*time.Millisecond),
		backlogSpec("big", "ECO03_BIG_URL", "", 250*time.Millisecond),
		backlogSpec("bad", "ECO03_BAD_URL", "", 250*time.Millisecond),
		backlogSpec("missing", "ECO03_MISSING_URL", "", 250*time.Millisecond),
		backlogSpec("slow", "ECO03_SLOW_URL", "", 50*time.Millisecond),
		backlogSpec("token", "ECO03_TOKEN_URL", "ECO03_TOKEN", 250*time.Millisecond),
		backlogSpec("public", "ECO03_PUBLIC_URL", "", 250*time.Millisecond),
	})
	if provider == nil {
		t.Fatal("expected provider")
	}
	t.Cleanup(provider.Close)

	if n, ok := provider.Backlog("zero"); !ok || n != 0 {
		t.Fatalf("T01 zero: n=%d ok=%v", n, ok)
	}
	spec := consumerSpec()
	if got := (scaling.Backlog{Provider: provider}).DesiredWorkers(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{}); got != 1 {
		t.Fatalf("T01 desired=%d", got)
	}

	spec.Name = "eighty"
	if got := (scaling.Backlog{Provider: provider}).DesiredWorkers(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{}); got != 4 {
		t.Fatalf("T02 desired=%d", got)
	}

	spec.Name = "big"
	if got := (scaling.Backlog{Provider: provider}).DesiredWorkers(spec, workload.WorkloadMetrics{}, resources.RuntimeResources{}); got != 8 {
		t.Fatalf("T03 desired=%d", got)
	}

	if _, ok := provider.Backlog("missing"); ok {
		t.Fatal("T04 missing endpoint must be unavailable")
	}
	if _, ok := provider.Backlog("bad"); ok {
		t.Fatal("T04 negative backlog must be unavailable")
	}
	if _, ok := provider.Backlog("public"); ok {
		t.Fatal("T04 public http must be refused")
	}
	t.Setenv("ECO03_TOKEN", "wrong")
	if _, ok := provider.Backlog("token"); ok {
		t.Fatal("T04 wrong token must be unavailable")
	}
	t.Setenv("ECO03_TOKEN", "secret")
	if n, ok := provider.Backlog("token"); !ok || n != 3 {
		t.Fatalf("token ok n=%d ok=%v", n, ok)
	}

	started := time.Now()
	if _, ok := provider.Backlog("slow"); ok {
		t.Fatal("T10 slow source must time out")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("T10 took %s", time.Since(started))
	}

	messages, measured, _, configured := provider.Stats("zero")
	if !configured || !measured || messages != 0 {
		t.Fatalf("zero sample messages=%d measured=%v configured=%v", messages, measured, configured)
	}
	_, measured, _, configured = provider.Stats("missing")
	if !configured || measured {
		t.Fatal("failed collection must not look like backlog 0")
	}

	other := backlogSpec("other", "ECO03_EIGHTY_URL", "", 250*time.Millisecond)
	other.Name = "other"
	isolated := scaling.NewHTTPJSONBacklogProvider([]workload.ResolvedWorkloadSpec{other, backlogSpec("zero", "ECO03_ZERO_URL", "", 250*time.Millisecond)})
	if _, ok := isolated.Backlog("other"); !ok {
		t.Fatal("T07 other workload")
	}
	if n, ok := isolated.Backlog("zero"); !ok || n != 0 {
		t.Fatalf("T07 zero isolated n=%d ok=%v", n, ok)
	}
}

func TestFixedIgnoresBacklogSource(t *testing.T) {
	spec := workload.ResolvedWorkloadSpec{
		Name:      "fixed",
		Workers:   workload.WorkerPolicy{Min: 2, Max: 2},
		Resources: workload.DefaultResources(),
		Scaling:   workload.DefaultScaling(),
	}
	n := scaling.Fixed{}.DesiredWorkers(spec, workload.WorkloadMetrics{Backlog: 100}, resources.RuntimeResources{})
	if n != 2 {
		t.Fatalf("T08 fixed=%d", n)
	}
}

func backlogSpec(name, urlEnv, tokenEnv string, timeout time.Duration) workload.ResolvedWorkloadSpec {
	spec := consumerSpec()
	spec.Name = name
	spec.Scaling.BacklogSource = workload.BacklogSource{
		Type:         "http_json",
		URLEnv:       urlEnv,
		TokenEnv:     tokenEnv,
		Timeout:      timeout,
		MaxStaleness: 5 * time.Second,
	}
	return spec
}

func consumerSpec() workload.ResolvedWorkloadSpec {
	return workload.ResolvedWorkloadSpec{
		Name:      "email",
		Mode:      workload.ModeConsumer,
		Workers:   workload.WorkerPolicy{Min: 1, Max: 8},
		Resources: workload.ResourcePolicy{Class: workload.ClassIO, MemoryMB: 128},
		Scaling: workload.ScalingPolicy{
			Strategy:         workload.StrategyBacklog,
			AvgJobDuration:   500 * time.Millisecond,
			TargetDrainTime:  10 * time.Second,
			ScaleUpCooldown:  time.Second,
			ScaleDownIdleFor: 30 * time.Second,
		},
	}
}
