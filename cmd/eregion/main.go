package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/EreborCodeForge/Eregion/internal/config"
	applog "github.com/EreborCodeForge/Eregion/internal/logging"
	"github.com/EreborCodeForge/Eregion/internal/server"
)

// Version is set via -ldflags or read from VERSION at build time.
var Version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	case "version":
		fmt.Printf("eregion %s\n", Version)
		fmt.Printf("go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Eregion — application server for MithrilPHP

Usage:
  eregion serve [--config=PATH] [--manifest=PATH] [--host=ADDR] [--port=N] [--workers=N]
  eregion check [--config=PATH]
  eregion status [--url=URL]
  eregion version
`)
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "eregion.yaml", "path to eregion.yaml")
	manifest := fs.String("manifest", "", "runtime manifest path forwarded to PHP workers")
	host := fs.String("host", "", "override server.host")
	port := fs.Int("port", 0, "override server.port")
	workers := fs.Int("workers", 0, "override workers.count")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		return 1
	}
	cfg.Manifest = *manifest
	if *host != "" {
		cfg.Server.Host = *host
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}
	if *workers > 0 {
		cfg.Workers.Count = *workers
		if err := cfg.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "config error: %v\n", err)
			return 1
		}
	}

	logger, err := applog.New(cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logging error: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv, err := server.New(cfg, logger, Version)
	if err != nil {
		logger.Error("server init failed", "error", err)
		return 1
	}
	if err := srv.Run(ctx); err != nil {
		logger.Error("server stopped with error", "error", err)
		return 1
	}
	return 0
}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := fs.String("config", "eregion.yaml", "path to eregion.yaml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		return 1
	}
	fmt.Println("OK: configuration is valid")
	fmt.Printf("  host=%s port=%d workers=%d queue=%d\n",
		cfg.Server.Host, cfg.Server.Port, cfg.Workers.Count, cfg.Queue.Capacity)
	fmt.Printf("  php=%s script=%s\n", cfg.PHP.Binary, cfg.PHP.WorkerScript)
	return 0
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:8080/_eregion/health", "health endpoint URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return server.PrintStatus(*url)
}

func loadConfig(path string) (config.Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := config.Default()
		return cfg, cfg.Validate()
	}
	return config.Load(path)
}
