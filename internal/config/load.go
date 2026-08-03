package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// fileConfig mirrors the YAML schema with KnownFields-friendly decoding.
type fileConfig struct {
	Version    string         `yaml:"version"`
	Server     fileServer     `yaml:"server"`
	PHP        filePHP        `yaml:"php"`
	Workers    fileWorkers    `yaml:"workers"`
	Socket     fileSocket     `yaml:"socket"`
	Protocol   fileProtocol   `yaml:"protocol"`
	Queue      fileQueue      `yaml:"queue"`
	Logging    fileLogging    `yaml:"logging"`
	Operations fileOperations `yaml:"operations"`
	Metrics    fileEndpoint   `yaml:"metrics"`
	Health     fileEndpoint   `yaml:"health"`
	Readiness  fileEndpoint   `yaml:"readiness"`
	Liveness   fileEndpoint   `yaml:"liveness"`
}

type fileServer struct {
	Host              *string `yaml:"host"`
	Port              *int    `yaml:"port"`
	ReadHeaderTimeout *string `yaml:"read_header_timeout"`
	ReadTimeout       *string `yaml:"read_timeout"`
	WriteTimeout      *string `yaml:"write_timeout"`
	IdleTimeout       *string `yaml:"idle_timeout"`
	ShutdownTimeout   *string `yaml:"shutdown_timeout"`
	MaxHeaderBytes    *int    `yaml:"max_header_bytes"`
	MaxBodyBytes      *int64  `yaml:"max_body_bytes"`
}

type filePHP struct {
	Binary           *string           `yaml:"binary"`
	WorkerScript     *string           `yaml:"worker_script"`
	WorkingDirectory *string           `yaml:"working_directory"`
	Environment      map[string]string `yaml:"environment"`
}

type fileWorkers struct {
	Count            *int        `yaml:"count"`
	MinReady         *int        `yaml:"min_ready"`
	MaxRequests      *int        `yaml:"max_requests"`
	StartupTimeout   *string     `yaml:"startup_timeout"`
	HandshakeTimeout *string     `yaml:"handshake_timeout"`
	RequestTimeout   *string     `yaml:"request_timeout"`
	AcquireTimeout   *string     `yaml:"acquire_timeout"`
	ShutdownTimeout  *string     `yaml:"shutdown_timeout"`
	MemoryLimitMB    *int        `yaml:"memory_limit_mb"`
	RestartLimit     *int        `yaml:"restart_limit"`
	RestartWindow    *string     `yaml:"restart_window"`
	RestartBackoff   fileBackoff `yaml:"restart_backoff"`
}

type fileBackoff struct {
	Initial    *string  `yaml:"initial"`
	Maximum    *string  `yaml:"maximum"`
	Multiplier *float64 `yaml:"multiplier"`
	Jitter     *float64 `yaml:"jitter"`
}

type fileSocket struct {
	Directory            *string `yaml:"directory"`
	DirectoryPermissions *string `yaml:"directory_permissions"`
	SocketPermissions    *string `yaml:"socket_permissions"`
}

type fileProtocol struct {
	Version       *uint8  `yaml:"version"`
	MaxFrameBytes *uint32 `yaml:"max_frame_bytes"`
	// Rejected if present — fixed in v1 / moved under workers.
	Transport        *string `yaml:"transport"`
	Codec            *string `yaml:"codec"`
	HandshakeTimeout *string `yaml:"handshake_timeout"`
}

type fileQueue struct {
	Capacity   *int    `yaml:"capacity"`
	RetryAfter *string `yaml:"retry_after"`
}

type fileLogging struct {
	Level                 *string `yaml:"level"`
	Format                *string `yaml:"format"`
	AccessLog             *bool   `yaml:"access_log"`
	IncludeRequestHeaders *bool   `yaml:"include_request_headers"`
	IncludeRequestBody    *bool   `yaml:"include_request_body"`
	IncludeResponseBody   *bool   `yaml:"include_response_body"`
}

type fileOperations struct {
	Prefix *string `yaml:"prefix"`
}

type fileEndpoint struct {
	Enabled *bool   `yaml:"enabled"`
	Path    *string `yaml:"path"`
}

// Load reads and validates a YAML configuration file, merging with defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var raw yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	var file fileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	if err := applyFile(&cfg, &file); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, file *fileConfig) error {
	if file.Version != "" {
		cfg.Version = file.Version
	}

	s := &file.Server
	if s.Host != nil {
		cfg.Server.Host = *s.Host
	}
	if s.Port != nil {
		cfg.Server.Port = *s.Port
	}
	if err := applyDuration(&cfg.Server.ReadHeaderTimeout, s.ReadHeaderTimeout, "server.read_header_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Server.ReadTimeout, s.ReadTimeout, "server.read_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Server.WriteTimeout, s.WriteTimeout, "server.write_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Server.IdleTimeout, s.IdleTimeout, "server.idle_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Server.ShutdownTimeout, s.ShutdownTimeout, "server.shutdown_timeout"); err != nil {
		return err
	}
	if s.MaxHeaderBytes != nil {
		cfg.Server.MaxHeaderBytes = *s.MaxHeaderBytes
	}
	if s.MaxBodyBytes != nil {
		cfg.Server.MaxBodyBytes = *s.MaxBodyBytes
	}

	p := &file.PHP
	if p.Binary != nil {
		cfg.PHP.Binary = *p.Binary
	}
	if p.WorkerScript != nil {
		cfg.PHP.WorkerScript = *p.WorkerScript
	}
	if p.WorkingDirectory != nil {
		cfg.PHP.WorkingDirectory = *p.WorkingDirectory
	}
	if p.Environment != nil {
		cfg.PHP.Environment = p.Environment
	}

	w := &file.Workers
	if w.Count != nil {
		cfg.Workers.Count = *w.Count
		// Recompute default queue capacity only if queue.capacity not set later.
	}
	if w.MinReady != nil {
		cfg.Workers.MinReady = *w.MinReady
	}
	if w.MaxRequests != nil {
		cfg.Workers.MaxRequests = *w.MaxRequests
	}
	if err := applyDuration(&cfg.Workers.StartupTimeout, w.StartupTimeout, "workers.startup_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Workers.HandshakeTimeout, w.HandshakeTimeout, "workers.handshake_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Workers.RequestTimeout, w.RequestTimeout, "workers.request_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Workers.AcquireTimeout, w.AcquireTimeout, "workers.acquire_timeout"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Workers.ShutdownTimeout, w.ShutdownTimeout, "workers.shutdown_timeout"); err != nil {
		return err
	}
	if w.MemoryLimitMB != nil {
		cfg.Workers.MemoryLimitMB = *w.MemoryLimitMB
	}
	if w.RestartLimit != nil {
		cfg.Workers.RestartLimit = *w.RestartLimit
	}
	if err := applyDuration(&cfg.Workers.RestartWindow, w.RestartWindow, "workers.restart_window"); err != nil {
		return err
	}
	b := &w.RestartBackoff
	if err := applyDuration(&cfg.Workers.RestartBackoff.Initial, b.Initial, "workers.restart_backoff.initial"); err != nil {
		return err
	}
	if err := applyDuration(&cfg.Workers.RestartBackoff.Maximum, b.Maximum, "workers.restart_backoff.maximum"); err != nil {
		return err
	}
	if b.Multiplier != nil {
		cfg.Workers.RestartBackoff.Multiplier = *b.Multiplier
	}
	if b.Jitter != nil {
		cfg.Workers.RestartBackoff.Jitter = *b.Jitter
	}

	sock := &file.Socket
	if sock.Directory != nil {
		cfg.Socket.Directory = *sock.Directory
	}
	if sock.DirectoryPermissions != nil {
		mode, err := parsePermission(*sock.DirectoryPermissions, "socket.directory_permissions")
		if err != nil {
			return err
		}
		cfg.Socket.DirectoryPermissions = mode
	}
	if sock.SocketPermissions != nil {
		mode, err := parsePermission(*sock.SocketPermissions, "socket.socket_permissions")
		if err != nil {
			return err
		}
		cfg.Socket.SocketPermissions = mode
	}

	proto := &file.Protocol
	if proto.Transport != nil || proto.Codec != nil {
		return fmt.Errorf("protocol.transport and protocol.codec are fixed in v1 (UDS + MessagePack) and must not be set")
	}
	if proto.HandshakeTimeout != nil {
		return fmt.Errorf("protocol.handshake_timeout is invalid; use workers.handshake_timeout")
	}
	if proto.Version != nil {
		cfg.Protocol.Version = *proto.Version
	}
	if proto.MaxFrameBytes != nil {
		cfg.Protocol.MaxFrameBytes = *proto.MaxFrameBytes
	}

	q := &file.Queue
	queueCapacitySet := q.Capacity != nil
	if q.Capacity != nil {
		cfg.Queue.Capacity = *q.Capacity
	} else if w.Count != nil {
		cfg.Queue.Capacity = *w.Count * 8
	}
	_ = queueCapacitySet
	if err := applyDuration(&cfg.Queue.RetryAfter, q.RetryAfter, "queue.retry_after"); err != nil {
		return err
	}

	l := &file.Logging
	if l.Level != nil {
		cfg.Logging.Level = *l.Level
	}
	if l.Format != nil {
		cfg.Logging.Format = *l.Format
	}
	if l.AccessLog != nil {
		cfg.Logging.AccessLog = *l.AccessLog
	}
	if l.IncludeRequestHeaders != nil {
		cfg.Logging.IncludeRequestHeaders = *l.IncludeRequestHeaders
	}
	if l.IncludeRequestBody != nil {
		cfg.Logging.IncludeRequestBody = *l.IncludeRequestBody
	}
	if l.IncludeResponseBody != nil {
		cfg.Logging.IncludeResponseBody = *l.IncludeResponseBody
	}

	if file.Operations.Prefix != nil {
		cfg.Operations.Prefix = *file.Operations.Prefix
	}
	applyEndpoint(&cfg.Metrics, &file.Metrics)
	applyEndpoint(&cfg.Health, &file.Health)
	applyEndpoint(&cfg.Readiness, &file.Readiness)
	applyEndpoint(&cfg.Liveness, &file.Liveness)

	return nil
}

func applyEndpoint(dst *EndpointConfig, src *fileEndpoint) {
	if src.Enabled != nil {
		dst.Enabled = *src.Enabled
	}
	if src.Path != nil {
		dst.Path = *src.Path
	}
}

func applyDuration(dst *time.Duration, raw *string, field string) error {
	if raw == nil {
		return nil
	}
	d, err := time.ParseDuration(*raw)
	if err != nil {
		return fmt.Errorf("invalid duration for %s: %w", field, err)
	}
	if d < 0 {
		return fmt.Errorf("%s must be non-negative", field)
	}
	*dst = d
	return nil
}

func parsePermission(raw, field string) (os.FileMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("%s is empty", field)
	}
	// Accept "0700" or "700".
	v, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid permission for %s: %w", field, err)
	}
	return os.FileMode(v), nil
}
