package craft

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultFileName is the canonical configuration filename.
const DefaultFileName = "eregion.yaml"

//go:embed eregion.yaml
var defaultYAML []byte

// Options controls where and how the default config is written.
type Options struct {
	// Dir is an explicit output directory. Empty means auto-detect.
	Dir string
	// Force overwrites an existing eregion.yaml.
	Force bool
	// StartDir is the directory to begin project-root detection from.
	// Empty means the process working directory.
	StartDir string
}

// Result describes a successful craft operation.
type Result struct {
	Path      string
	Directory string
	Created   bool
}

// DefaultTemplate returns the embedded default YAML bytes.
func DefaultTemplate() []byte {
	out := make([]byte, len(defaultYAML))
	copy(out, defaultYAML)
	return out
}

// ResolveDir picks the output directory.
// Priority: explicit Dir → detected project root → start/cwd.
func ResolveDir(opts Options) (string, error) {
	if opts.Dir != "" {
		abs, err := filepath.Abs(opts.Dir)
		if err != nil {
			return "", fmt.Errorf("resolve --dir: %w", err)
		}
		return abs, nil
	}

	start := opts.StartDir
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
		start = wd
	}
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}

	if root, ok := FindProjectRoot(start); ok {
		return root, nil
	}
	return start, nil
}

// FindProjectRoot walks upward looking for a Mithril/PHP or Go project marker.
func FindProjectRoot(start string) (string, bool) {
	dir := start
	for {
		for _, marker := range []string{"composer.json", "go.mod"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// WriteDefault writes eregion.yaml into the resolved directory.
func WriteDefault(opts Options) (Result, error) {
	dir, err := ResolveDir(opts)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("stat output dir: %w", err)
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return Result{}, fmt.Errorf("create output dir: %w", mkErr)
		}
	} else if !info.IsDir() {
		return Result{}, fmt.Errorf("output path is not a directory: %s", dir)
	}

	path := filepath.Join(dir, DefaultFileName)
	if _, err := os.Stat(path); err == nil && !opts.Force {
		return Result{}, fmt.Errorf("%s already exists (use --force to overwrite)", path)
	} else if err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, DefaultTemplate(), 0o644); err != nil {
		return Result{}, fmt.Errorf("write temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Result{}, fmt.Errorf("finalize config: %w", err)
	}

	return Result{Path: path, Directory: dir, Created: true}, nil
}
