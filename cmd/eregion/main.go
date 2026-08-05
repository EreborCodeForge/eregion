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
	"github.com/EreborCodeForge/Eregion/internal/craft"
	applog "github.com/EreborCodeForge/Eregion/internal/logging"
	"github.com/EreborCodeForge/Eregion/internal/protocol"
	"github.com/EreborCodeForge/Eregion/internal/server"
)

// Version is set via -ldflags at release build time (no leading "v").
var Version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "craft":
		os.Exit(cmdCraft(os.Args[2:]))
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	case "version", "--version", "-version":
		printVersion()
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

func printVersion() {
	// Stable, Forge-parseable contract (see eregion-binary-distribution.md §5).
	fmt.Printf("eregion %s\n", Version)
	fmt.Printf("protocol %s/%d\n", protocol.ProtocolName, protocol.ProtocolVersion)
	fmt.Printf("go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Eregion — application server for MithrilPHP

Usage:
  eregion craft [--dir=PATH] [--force]
  eregion serve [--config=PATH] [--manifest=PATH] [--host=ADDR] [--port=N] [--workers=N]
  eregion check [--config=PATH]
  eregion status [--url=URL]
  eregion version
  eregion --version

craft writes a default eregion.yaml (the forge blueprint):
  - default directory: project root (composer.json/go.mod) or the current working directory
  - override with --dir
  - refuse overwrite unless --force

Binary releases: GitHub Releases with eregion-<os>-<arch> assets and checksums.txt.
Protocol: eregion/1 (EREGION/1).
`)
}

func cmdCraft(args []string) int {
	fs := flag.NewFlagSet("craft", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: eregion craft [--dir=PATH] [--force]

Craft a default eregion.yaml (the forge blueprint).

Without --dir, writes to the project root when composer.json or go.mod
is found above the current directory; otherwise uses the current working directory.

`)
		fs.PrintDefaults()
	}
	dir := fs.String("dir", "", "output directory (default: project root or cwd)")
	force := fs.Bool("force", false, "overwrite eregion.yaml if it already exists")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	res, err := craft.WriteDefault(craft.Options{
		Dir:   *dir,
		Force: *force,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "craft error: %v\n", err)
		return 1
	}

	fmt.Printf("Crafted %s\n", res.Path)
	fmt.Println("Next: adjust php.worker_script and run `eregion check`, then `eregion serve`.")
	return 0
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
