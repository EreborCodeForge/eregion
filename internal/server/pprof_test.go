package server

import "testing"

func TestPprofListenAddrIsOptInLoopback(t *testing.T) {
	if addr, ok := pprofListenAddr(""); ok || addr != "" {
		t.Fatalf("empty addr was accepted: %q", addr)
	}
	for _, raw := range []string{"0.0.0.0:6060", "10.0.0.8:6060", ":6060", "localhost:6060"} {
		if _, ok := pprofListenAddr(raw); ok {
			t.Fatalf("%s was accepted", raw)
		}
	}
	addr, ok := pprofListenAddr("127.0.0.1:6060")
	if !ok || addr != "127.0.0.1:6060" {
		t.Fatalf("loopback addr=%q ok=%v", addr, ok)
	}
}
