package worker_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/worker"
)

func TestMergeEnvironmentNoDuplicates(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=old", "HOME=/tmp"}
	out := worker.MergeEnvironment(base, map[string]string{"FOO": "new", "BAR": "1"})
	sort.Strings(out)
	got := map[string]string{}
	for _, e := range out {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("bad entry %q", e)
		}
		if _, exists := got[k]; exists {
			t.Fatalf("duplicate key %q", k)
		}
		got[k] = v
	}
	want := map[string]string{"PATH": "/usr/bin", "FOO": "new", "HOME": "/tmp", "BAR": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}
