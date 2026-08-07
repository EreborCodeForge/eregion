package dispatcher_test

import (
	"net/http"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/dispatcher"
)

func TestRedactHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer secret")
	h.Set("X-Request-Id", "abc")
	h.Set("Cookie", "a=b")
	out := dispatcher.RedactHeadersForTest(h)
	if out["Authorization"][0] != "[REDACTED]" {
		t.Fatalf("auth = %#v", out["Authorization"])
	}
	if out["Cookie"][0] != "[REDACTED]" {
		t.Fatalf("cookie = %#v", out["Cookie"])
	}
	if out["X-Request-Id"][0] != "abc" {
		t.Fatalf("id = %#v", out["X-Request-Id"])
	}
}

func TestTruncateBody(t *testing.T) {
	s, omitted, truncated := dispatcher.TruncateBodyForTest([]byte("hello"))
	if omitted || truncated || s != "hello" {
		t.Fatalf("got %q omit=%v trunc=%v", s, omitted, truncated)
	}
	big := make([]byte, 9*1024)
	for i := range big {
		big[i] = 'a'
	}
	s, omitted, truncated = dispatcher.TruncateBodyForTest(big)
	if omitted || !truncated || len(s) != 8*1024 {
		t.Fatalf("len=%d omit=%v trunc=%v", len(s), omitted, truncated)
	}
	_, omitted, _ = dispatcher.TruncateBodyForTest([]byte{0, 1, 2})
	if !omitted {
		t.Fatal("expected binary omit")
	}
}
