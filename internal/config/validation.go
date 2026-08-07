package config

import (
	"fmt"
	"strings"
)

// Validate checks the resolved configuration.
func (c *Config) Validate() error {
	if c.Version != "" && c.Version != "1" {
		return fmt.Errorf("unsupported config version %q", c.Version)
	}
	if c.Server.Host == "" {
		return fmt.Errorf("server.host is required")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port must be between 1 and 65535")
	}
	if c.Server.MaxHeaderBytes <= 0 {
		return fmt.Errorf("server.max_header_bytes must be positive")
	}
	if c.Server.MaxBodyBytes <= 0 {
		return fmt.Errorf("server.max_body_bytes must be positive")
	}
	if c.Server.ReadHeaderTimeout <= 0 || c.Server.ReadTimeout <= 0 ||
		c.Server.WriteTimeout <= 0 || c.Server.IdleTimeout <= 0 ||
		c.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server timeouts must be positive")
	}

	if strings.TrimSpace(c.PHP.Binary) == "" {
		return fmt.Errorf("php.binary is required")
	}
	if strings.TrimSpace(c.PHP.WorkerScript) == "" {
		return fmt.Errorf("php.worker_script is required")
	}
	if strings.TrimSpace(c.PHP.WorkingDirectory) == "" {
		return fmt.Errorf("php.working_directory is required")
	}

	if c.Workers.Count < 1 {
		return fmt.Errorf("workers.count must be at least 1")
	}
	if c.Workers.MinReady < 0 || c.Workers.MinReady > c.Workers.Count {
		return fmt.Errorf("workers.min_ready must be between 0 and workers.count")
	}
	if c.Workers.MaxRequests < 0 {
		return fmt.Errorf("workers.max_requests must be >= 0")
	}
	if c.Workers.MemoryLimitMB < 0 {
		return fmt.Errorf("workers.memory_limit_mb must be >= 0")
	}
	if c.Workers.StartupTimeout <= 0 || c.Workers.HandshakeTimeout <= 0 ||
		c.Workers.RequestTimeout <= 0 || c.Workers.AcquireTimeout <= 0 ||
		c.Workers.ShutdownTimeout <= 0 {
		return fmt.Errorf("worker timeouts must be positive")
	}
	if c.Workers.RestartLimit < 0 {
		return fmt.Errorf("workers.restart_limit must be >= 0")
	}
	if c.Workers.RestartWindow <= 0 {
		return fmt.Errorf("workers.restart_window must be positive")
	}
	b := c.Workers.RestartBackoff
	if b.Initial <= 0 || b.Maximum <= 0 {
		return fmt.Errorf("workers.restart_backoff initial/maximum must be positive")
	}
	if b.Multiplier < 1 {
		return fmt.Errorf("workers.restart_backoff.multiplier must be >= 1")
	}
	if b.Jitter < 0 || b.Jitter > 1 {
		return fmt.Errorf("workers.restart_backoff.jitter must be between 0 and 1")
	}

	if strings.TrimSpace(c.Socket.Directory) == "" {
		return fmt.Errorf("socket.directory is required")
	}

	if c.Protocol.Version != 1 {
		return fmt.Errorf("protocol.version must be 1")
	}
	if c.Protocol.MaxFrameBytes == 0 {
		return fmt.Errorf("protocol.max_frame_bytes must be positive")
	}
	if int64(c.Protocol.MaxFrameBytes) <= c.Server.MaxBodyBytes {
		return fmt.Errorf("protocol.max_frame_bytes must be greater than server.max_body_bytes")
	}

	if c.Queue.Capacity < 0 {
		return fmt.Errorf("queue.capacity must be >= 0")
	}
	if c.Queue.RetryAfter < 0 {
		return fmt.Errorf("queue.retry_after must be >= 0")
	}

	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	switch strings.ToLower(c.Logging.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("logging.format must be text or json")
	}

	if c.Operations.Prefix == "" {
		return fmt.Errorf("operations.prefix is required")
	}
	if !strings.HasPrefix(c.Operations.Prefix, "/") {
		return fmt.Errorf("operations.prefix must start with /")
	}

	if err := c.ResolveEndpoints(); err != nil {
		return err
	}
	if err := validateEndpointCollisions(*c); err != nil {
		return err
	}

	return nil
}
