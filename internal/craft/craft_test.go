package craft_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/config"
	"github.com/EreborCodeForge/Eregion/internal/craft"
)

func TestWriteDefaultToCWDWhenNoProject(t *testing.T) {
	dir := t.TempDir()
	res, err := craft.WriteDefault(craft.Options{StartDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Directory != dir {
		t.Fatalf("dir = %s want %s", res.Directory, dir)
	}
	if filepath.Base(res.Path) != "eregion.yaml" {
		t.Fatalf("path = %s", res.Path)
	}
	cfg, err := config.Load(res.Path)
	if err != nil {
		t.Fatalf("load crafted yaml: %v", err)
	}
	if cfg.Protocol.Version != 1 {
		t.Fatalf("version = %d", cfg.Protocol.Version)
	}
	if cfg.Workers.HandshakeTimeout <= 0 {
		t.Fatal("handshake timeout missing")
	}
}

func TestWriteDefaultPrefersProjectRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "app", "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := craft.WriteDefault(craft.Options{StartDir: nested})
	if err != nil {
		t.Fatal(err)
	}
	if res.Directory != root {
		t.Fatalf("dir = %s want project root %s", res.Directory, root)
	}
	if _, err := os.Stat(filepath.Join(root, "eregion.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteDefaultExplicitDir(t *testing.T) {
	start := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	// Mark start as a project so we can prove --dir wins.
	if err := os.WriteFile(filepath.Join(start, "composer.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := craft.WriteDefault(craft.Options{StartDir: start, Dir: out})
	if err != nil {
		t.Fatal(err)
	}
	if res.Directory != out {
		// ResolveDir Abs may normalize; compare abs.
		want, _ := filepath.Abs(out)
		if res.Directory != want {
			t.Fatalf("dir = %s want %s", res.Directory, want)
		}
	}
}

func TestRefuseOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := craft.WriteDefault(craft.Options{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	_, err := craft.WriteDefault(craft.Options{Dir: dir})
	if err == nil {
		t.Fatal("expected exists error")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatalf("error = %v", err)
	}
}

func TestForceOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, craft.DefaultFileName)
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := craft.WriteDefault(craft.Options{Dir: dir, Force: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "stale" || !strings.Contains(string(data), `version: "1"`) {
		t.Fatalf("unexpected content: %q", data)
	}
}

func TestTemplateNotEmpty(t *testing.T) {
	if len(craft.DefaultTemplate()) < 100 {
		t.Fatal("template too small")
	}
}
