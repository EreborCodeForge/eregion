package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
)

const (
	stderrHeadBytes = 6144
	stderrTailBytes = 2048
)

// streamCapture keeps a bounded head and tail of a child stream.
// Write always consumes the input so a noisy child cannot block on a full pipe.
type streamCapture struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	dropped int
}

func (c *streamCapture) Write(p []byte) (int, error) {
	if len(p) == 0 || c == nil {
		return len(p), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rest := p
	if len(c.head) < stderrHeadBytes {
		n := stderrHeadBytes - len(c.head)
		if n > len(rest) {
			n = len(rest)
		}
		c.head = append(c.head, rest[:n]...)
		rest = rest[n:]
	}
	if len(rest) == 0 {
		return len(p), nil
	}
	c.tail = append(c.tail, rest...)
	if len(c.tail) > stderrTailBytes {
		overflow := len(c.tail) - stderrTailBytes
		c.dropped += overflow
		tail := make([]byte, stderrTailBytes)
		copy(tail, c.tail[overflow:])
		c.tail = tail
	}
	return len(p), nil
}

// Text returns the captured prefix and, when the stream was truncated, the tail.
func (c *streamCapture) Text() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	head := append([]byte(nil), c.head...)
	tail := append([]byte(nil), c.tail...)
	dropped := c.dropped
	c.mu.Unlock()
	var raw []byte
	if dropped == 0 {
		raw = append(head, tail...)
	} else {
		raw = make([]byte, 0, len(head)+len(tail)+24)
		raw = append(raw, head...)
		raw = append(raw, []byte("\n[stderr truncated]\n")...)
		raw = append(raw, tail...)
	}
	raw = bytes.ReplaceAll(raw, []byte{0}, nil)
	return string(bytes.ToValidUTF8(raw, []byte("?")))
}

// drainCapture reads r until EOF. The returned channel is closed when the
// read ends. A nil reader returns a nil channel so callers do not treat a
// missing pipe as process exit.
func drainCapture(logger *slog.Logger, workerID, stream string, r io.ReadCloser, cap *streamCapture) <-chan struct{} {
	if r == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer r.Close()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				_, _ = cap.Write(buf[:n])
				logStreamChunk(logger, workerID, stream, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return done
}

func logStreamChunk(logger *slog.Logger, workerID, stream string, chunk []byte) {
	if logger == nil || !logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	view := chunk
	if len(view) > 512 {
		view = view[:512]
	}
	logger.Debug("worker output", "worker_id", workerID, "stream", stream, "data", string(view))
}
