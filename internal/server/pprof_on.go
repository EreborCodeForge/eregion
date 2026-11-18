//go:build pprof

package server

import (
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strings"
)

func startPprof(logger *slog.Logger) {
	raw := os.Getenv("EREGION_PPROF")
	addr, ok := pprofListenAddr(raw)
	if !ok {
		if strings.TrimSpace(raw) != "" {
			logger.Error("EREGION_PPROF ignored; bind must be 127.0.0.1:port")
		}
		return
	}
	runtime.SetMutexProfileFraction(5)
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			logger.Error("pprof listener stopped", "error", err.Error())
		}
	}()
}
