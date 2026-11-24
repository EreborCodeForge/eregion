package socket_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EreborCodeForge/Eregion/internal/socket"
)

func TestSunPathBytesMatchesPlatform(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "windows", "android":
		if socket.SunPathBytes() != 108 {
			t.Fatalf("sun_path=%d", socket.SunPathBytes())
		}
	case "darwin", "freebsd", "openbsd", "netbsd":
		if socket.SunPathBytes() != 104 {
			t.Fatalf("sun_path=%d", socket.SunPathBytes())
		}
	}
	if socket.MaxBindPathBytes() != socket.SunPathBytes()-1 {
		t.Fatalf("max=%d sun=%d", socket.MaxBindPathBytes(), socket.SunPathBytes())
	}
}

func TestValidateBindPathBounds(t *testing.T) {
	valid := strings.Repeat("a", socket.MaxBindPathBytes())
	if err := socket.ValidateBindPath(valid); err != nil {
		t.Fatalf("path of %d bytes: %v", len(valid), err)
	}

	invalid := strings.Repeat("b", socket.SunPathBytes())
	err := socket.ValidateBindPath(invalid)
	var lengthErr *socket.PathLengthError
	if !errors.As(err, &lengthErr) {
		t.Fatalf("got %v", err)
	}
	if lengthErr.Bytes != len(invalid) || lengthErr.Limit != socket.SunPathBytes() {
		t.Fatalf("length error=%+v", lengthErr)
	}

	emptyErr := socket.ValidateBindPath("")
	if emptyErr == nil {
		t.Fatal("empty path accepted")
	}
	if errors.As(emptyErr, &lengthErr) {
		t.Fatal("empty path reported as a length error")
	}
}

func TestValidateBindPathUsesBytesNotRunes(t *testing.T) {
	// U+00E1 encodes as two bytes. A rune count under the limit must still
	// fail once the UTF-8 byte length reaches sizeof(sun_path).
	unit := "á"
	if len(unit) != 2 {
		t.Fatalf("unit bytes=%d", len(unit))
	}
	count := socket.SunPathBytes() / len(unit)
	path := strings.Repeat(unit, count)
	if len(path) < socket.SunPathBytes() {
		t.Fatalf("constructed %d bytes", len(path))
	}
	if utf8.RuneCountInString(path) >= socket.SunPathBytes() {
		t.Fatalf("rune count %d is not below the byte limit", utf8.RuneCountInString(path))
	}
	if err := socket.ValidateBindPath(path); err == nil {
		t.Fatal("multi-byte path at the platform limit was accepted")
	}

	shorter := strings.Repeat(unit, socket.MaxBindPathBytes()/len(unit))
	if len(shorter) > socket.MaxBindPathBytes() {
		t.Fatalf("shorter path is %d bytes", len(shorter))
	}
	if err := socket.ValidateBindPath(shorter); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPathLengthFollowsFinalBytes(t *testing.T) {
	name := "worker-2-1.sock"
	limit := socket.SunPathBytes()
	sep := string(filepath.Separator)
	dirLen := limit - len(sep) - len(name)
	prefix := "/"
	if runtime.GOOS == "windows" {
		prefix = `C:\`
	}
	if dirLen <= len(prefix) {
		t.Fatal("platform prefix leaves no room")
	}
	dir := prefix + strings.Repeat("a", dirLen-len(prefix))
	m := socket.NewManager(dir, 0o700, 0o600)
	path := m.Path(2, 1)
	if len(path) != limit {
		t.Fatalf("final path len=%d want %d (%s)", len(path), limit, path)
	}
	if err := socket.ValidateBindPath(path); err == nil {
		t.Fatal("final worker path at the platform limit was accepted")
	}

	short := socket.NewManager(filepath.Join(prefix+"sock"), 0o700, 0o600)
	okPath := short.Path(2, 1)
	if err := socket.ValidateBindPath(okPath); err != nil {
		t.Fatalf("short final path %q: %v", okPath, err)
	}
}
