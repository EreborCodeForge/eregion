//go:build !pprof

package server

import "log/slog"

// startPprof is a no-op unless the binary is built with -tags pprof.
// The gate measurement process must not import net/http/pprof.
func startPprof(*slog.Logger) {}
