package server

import "strings"

func pprofListenAddr(raw string) (string, bool) {
	addr := strings.TrimSpace(raw)
	if addr == "" || !strings.HasPrefix(addr, "127.0.0.1:") {
		return "", false
	}
	return addr, true
}
