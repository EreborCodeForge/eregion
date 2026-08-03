package protocol

import (
	"fmt"
	"io"
	"net"
	"time"
)

// Conn is a framed MessagePack connection to a worker.
type Conn struct {
	raw           net.Conn
	maxFrameBytes uint32
}

// NewConn wraps a net.Conn for framed MessagePack IO.
func NewConn(c net.Conn, maxFrameBytes uint32) *Conn {
	return &Conn{raw: c, maxFrameBytes: maxFrameBytes}
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	if c.raw == nil {
		return nil
	}
	return c.raw.Close()
}

// SendRequest encodes and sends a request envelope, then reads the response.
func (c *Conn) SendRequest(req RequestEnvelope, timeout time.Duration) (ResponseEnvelope, error) {
	if req.Type == "" {
		req.Type = TypeRequest
	}
	if req.Version == 0 {
		req.Version = ProtocolVersion
	}
	if req.Headers == nil {
		req.Headers = map[string][]string{}
	}
	if req.Body == nil {
		req.Body = []byte{}
	}

	deadline := time.Now().Add(timeout)
	if err := c.raw.SetDeadline(deadline); err != nil {
		return ResponseEnvelope{}, err
	}
	defer func() { _ = c.raw.SetDeadline(time.Time{}) }()

	payload, err := Encode(req)
	if err != nil {
		return ResponseEnvelope{}, fmt.Errorf("encode request: %w", err)
	}
	if uint32(len(payload)) > c.maxFrameBytes {
		return ResponseEnvelope{}, fmt.Errorf("%w: request payload %d", ErrFrameTooLarge, len(payload))
	}
	if err := WriteFrame(c.raw, payload); err != nil {
		return ResponseEnvelope{}, fmt.Errorf("write request: %w", err)
	}

	raw, err := ReadFrame(c.raw, c.maxFrameBytes)
	if err != nil {
		return ResponseEnvelope{}, fmt.Errorf("read response: %w", err)
	}
	var resp ResponseEnvelope
	if err := Decode(raw, &resp); err != nil {
		return ResponseEnvelope{}, fmt.Errorf("decode response: %w", err)
	}
	if err := ValidateResponse(resp, req.ID); err != nil {
		return ResponseEnvelope{}, err
	}
	return resp, nil
}

// Raw returns the underlying net.Conn.
func (c *Conn) Raw() net.Conn { return c.raw }

// Writer exposes the connection as an io.Writer for tests.
func (c *Conn) Writer() io.Writer { return c.raw }
