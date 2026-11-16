package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

func TestMetricsLoopbackIgnoresForwardedHeader(t *testing.T) {
	next := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics"))
	}
	handler := guardMetrics(next, "loopback")

	remote := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	remote.RemoteAddr = "10.1.1.1:40000"
	remote.Header.Set("X-Forwarded-For", "127.0.0.1")
	rec := httptest.NewRecorder()
	handler(rec, remote)
	if rec.Code != http.StatusForbidden || rec.Body.Len() == 0 {
		t.Fatalf("remote code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "metrics" {
		t.Fatal("forbidden response included the metrics payload")
	}

	for _, addr := range []string{"127.0.0.1:10", "[::1]:10"} {
		local := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		local.RemoteAddr = addr
		got := httptest.NewRecorder()
		handler(got, local)
		if got.Code != http.StatusOK || got.Body.String() != "metrics" {
			t.Fatalf("%s code=%d body=%q", addr, got.Code, got.Body.String())
		}
	}

	public := guardMetrics(next, "public")
	again := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	again.RemoteAddr = "10.1.1.1:40000"
	pub := httptest.NewRecorder()
	public(pub, again)
	if pub.Code != http.StatusOK {
		t.Fatalf("public code=%d", pub.Code)
	}
}

func TestDisabledMetricsStayNotFound(t *testing.T) {
	cfg := config.Default()
	cfg.Socket.Directory = t.TempDir()
	cfg.Metrics.Enabled = false
	srv, err := New(cfg, slog.Default(), "test")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, srv.cfg.Metrics.Path, nil)
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled metrics code=%d body=%q", rec.Code, rec.Body.String())
	}
	live := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, srv.cfg.Liveness.Path, nil))
	if live.Code == http.StatusNotFound {
		t.Fatalf("liveness disappeared: %d", live.Code)
	}
}
