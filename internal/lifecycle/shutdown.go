package lifecycle

import (
	"os"
	"path/filepath"
)

// Cleanup removes socket files from the managed directory.
func Cleanup(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
