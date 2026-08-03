package config

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// Config is the fully resolved Eregion configuration.
type Config struct {
	Version    string
	Server     ServerConfig
	PHP        PHPConfig
	Workers    WorkersConfig
	Socket     SocketConfig
	Protocol   ProtocolConfig
	Queue      QueueConfig
	Logging    LoggingConfig
	Operations OperationsConfig
	Metrics    EndpointConfig
	Health     EndpointConfig
	Readiness  EndpointConfig
	Liveness   EndpointConfig

	// Manifest is set from the CLI and forwarded to PHP workers.
	Manifest string
}

type ServerConfig struct {
	Host              string
	Port              int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
}

type PHPConfig struct {
	Binary           string
	WorkerScript     string
	WorkingDirectory string
	Environment      map[string]string
}

type WorkersConfig struct {
	Count            int
	MinReady         int
	MaxRequests      int
	StartupTimeout   time.Duration
	HandshakeTimeout time.Duration
	RequestTimeout   time.Duration
	AcquireTimeout   time.Duration
	ShutdownTimeout  time.Duration
	MemoryLimitMB    int
	RestartLimit     int
	RestartWindow    time.Duration
	RestartBackoff   BackoffConfig
}

type BackoffConfig struct {
	Initial    time.Duration
	Maximum    time.Duration
	Multiplier float64
	Jitter     float64
}

type SocketConfig struct {
	Directory            string
	DirectoryPermissions os.FileMode
	SocketPermissions    os.FileMode
}

type ProtocolConfig struct {
	Version       uint8
	MaxFrameBytes uint32
}

type QueueConfig struct {
	Capacity   int
	RetryAfter time.Duration
}

type LoggingConfig struct {
	Level                 string
	Format                string
	AccessLog             bool
	IncludeRequestHeaders bool
	IncludeRequestBody    bool
	IncludeResponseBody   bool
}

type OperationsConfig struct {
	Prefix string
}

type EndpointConfig struct {
	Enabled bool
	Path    string
}

// Default returns safe local-development defaults (spec §7.3).
func Default() Config {
	workerCount := runtime.NumCPU()
	if workerCount > 8 {
		workerCount = 8
	}
	if workerCount < 1 {
		workerCount = 1
	}

	return Config{
		Version: "1",
		Server: ServerConfig{
			Host:              "127.0.0.1",
			Port:              8080,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   20 * time.Second,
			MaxHeaderBytes:    1 << 20,
			MaxBodyBytes:      10 << 20,
		},
		PHP: PHPConfig{
			Binary:           "php",
			WorkerScript:     "bin/eregion-worker",
			WorkingDirectory: ".",
			Environment:      map[string]string{},
		},
		Workers: WorkersConfig{
			Count:            workerCount,
			MinReady:         1,
			MaxRequests:      1000,
			StartupTimeout:   10 * time.Second,
			HandshakeTimeout: 5 * time.Second,
			RequestTimeout:   30 * time.Second,
			AcquireTimeout:   2 * time.Second,
			ShutdownTimeout:  5 * time.Second,
			MemoryLimitMB:    256,
			RestartLimit:     5,
			RestartWindow:    30 * time.Second,
			RestartBackoff: BackoffConfig{
				Initial:    100 * time.Millisecond,
				Maximum:    5 * time.Second,
				Multiplier: 2.0,
				Jitter:     0.2,
			},
		},
		Socket: SocketConfig{
			Directory:            "/tmp/eregion",
			DirectoryPermissions: 0o700,
			SocketPermissions:    0o600,
		},
		Protocol: ProtocolConfig{
			Version:       1,
			MaxFrameBytes: 16 << 20,
		},
		Queue: QueueConfig{
			Capacity:   workerCount * 8,
			RetryAfter: time.Second,
		},
		Logging: LoggingConfig{
			Level:                 "info",
			Format:                "text",
			AccessLog:             true,
			IncludeRequestHeaders: false,
			IncludeRequestBody:    false,
			IncludeResponseBody:   false,
		},
		Operations: OperationsConfig{Prefix: "/_eregion"},
		Metrics:    EndpointConfig{Enabled: true, Path: "/_eregion/metrics"},
		Health:     EndpointConfig{Enabled: true, Path: "/_eregion/health"},
		Readiness:  EndpointConfig{Enabled: true, Path: "/_eregion/ready"},
		Liveness:   EndpointConfig{Enabled: true, Path: "/_eregion/live"},
	}
}

// Addr returns host:port for the HTTP server.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
