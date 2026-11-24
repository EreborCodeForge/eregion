package socket

import (
	"fmt"
	"runtime"
)

// SunPathBytes is sizeof(sockaddr_un.sun_path) on this OS.
// PHP's socket_bind rejects an address whose byte length is greater than or
// equal to this value (the NUL terminator occupies the last byte of the array).
func SunPathBytes() int {
	switch runtime.GOOS {
	case "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return 104
	default:
		// Linux, Android and Windows AF_UNIX (UNIX_PATH_MAX).
		return 108
	}
}

// MaxBindPathBytes is the longest socket path PHP will bind on this OS.
func MaxBindPathBytes() int {
	n := SunPathBytes() - 1
	if n < 1 {
		return 0
	}
	return n
}

// PathLengthError means the final socket path cannot be passed to socket_bind.
type PathLengthError struct {
	Path  string
	Bytes int
	Limit int
}

func (e *PathLengthError) Error() string {
	return fmt.Sprintf("socket path is %d bytes; platform limit is %d", e.Bytes, e.Limit)
}

// ValidateBindPath checks the exact path string that will be given to the worker.
// Length is measured in bytes, not runes, because that is what PHP compares
// against sockaddr_un.sun_path.
func ValidateBindPath(path string) error {
	if path == "" {
		return fmt.Errorf("socket path is empty")
	}
	limit := SunPathBytes()
	n := len(path)
	if n >= limit {
		return &PathLengthError{Path: path, Bytes: n, Limit: limit}
	}
	return nil
}
