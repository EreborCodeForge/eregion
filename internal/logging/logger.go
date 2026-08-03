package logging

import (
	"log/slog"
	"os"
	"strings"

	"github.com/EreborCodeForge/Eregion/internal/config"
)

// New builds a slog.Logger from logging configuration.
func New(cfg config.LoggingConfig) (*slog.Logger, error) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler), nil
}
