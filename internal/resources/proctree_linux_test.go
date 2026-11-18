//go:build linux

package resources

import "testing"

func TestProcessTreeRSSIsObservable(t *testing.T) {
	_, known := ProcessTreeRSS()
	if !known {
		t.Fatal("linux process tree was not readable")
	}
}
