package server

import (
	"log/slog"
	"net/http"
	"os"
	"strings"

	_ "net/http/pprof"
)

func pprofListenAddr(raw string) (string, bool) {
	addr := strings.TrimSpace(raw)
	if addr == "" || !strings.HasPrefix(addr, "127.0.0.1:") {
		return "", false
	}
	return addr, true
}

func startPprof(logger *slog.Logger) {
	addr, ok := pprofListenAddr(os.Getenv("EREGION_PPROF"))
	if !ok {
		if strings.TrimSpace(os.Getenv("EREGION_PPROF")) != "" {
			logger.Error("EREGION_PPROF ignored; bind must be 127.0.0.1:port")
		}
		return
	}
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			logger.Error("pprof listener stopped", "error", err.Error())
		}
	}()
}
