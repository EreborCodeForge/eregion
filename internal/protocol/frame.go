package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	ErrFrameTooLarge = errors.New("frame exceeds max_frame_bytes")
	ErrEmptyFrame    = errors.New("empty frame")
)

// ReadFrame reads a length-prefixed frame from r.
func ReadFrame(r io.Reader, maxBytes uint32) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, ErrEmptyFrame
	}
	if n > maxBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, maxBytes)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteFrame writes a length-prefixed frame to w, handling partial writes.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, payload)
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}
