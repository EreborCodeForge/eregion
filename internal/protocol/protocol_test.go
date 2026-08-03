package protocol_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/EreborCodeForge/Eregion/internal/protocol"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello-eregion")
	if err := protocol.WriteFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := protocol.ReadFrame(&buf, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q", got)
	}
}

func TestPartialWrites(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 100)
	w := &slowWriter{n: 3}
	if err := protocol.WriteFrame(w, payload); err != nil {
		t.Fatal(err)
	}
	got, err := protocol.ReadFrame(bytes.NewReader(w.buf.Bytes()), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("mismatch")
	}
}

func TestOversizedFrame(t *testing.T) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 100)
	r := io.MultiReader(bytes.NewReader(hdr[:]), bytes.NewReader(make([]byte, 100)))
	if _, err := protocol.ReadFrame(r, 10); err == nil {
		t.Fatal("expected oversized error")
	}
}

func TestEmptyFrame(t *testing.T) {
	var hdr [4]byte
	if _, err := protocol.ReadFrame(bytes.NewReader(hdr[:]), 10); err == nil {
		t.Fatal("expected empty frame error")
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	req := protocol.RequestEnvelope{
		Type:    protocol.TypeRequest,
		Version: 1,
		ID:      "req-1",
		Method:  "POST",
		URI:     "/x?y=1",
		Path:    "/x",
		Query:   "y=1",
		Headers: map[string][]string{"X-A": {"1", "2"}},
		Body:    []byte{0, 1, 2, 255},
	}
	raw, err := protocol.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.RequestEnvelope
	if err := protocol.Decode(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != req.ID || !bytes.Equal(got.Body, req.Body) {
		t.Fatalf("got %+v", got)
	}
	if len(got.Headers["X-A"]) != 2 {
		t.Fatalf("headers = %#v", got.Headers)
	}
}

func TestValidateResponseMismatch(t *testing.T) {
	resp := protocol.ResponseEnvelope{
		Type: protocol.TypeResponse, Version: 1, ID: "b", Status: 200,
	}
	if err := protocol.ValidateResponse(resp, "a"); err == nil {
		t.Fatal("expected id mismatch")
	}
}

func TestValidateReady(t *testing.T) {
	r := protocol.Ready{
		Type: protocol.TypeReady, Protocol: protocol.ProtocolName,
		ProtocolVersion: 1, WorkerID: "worker-1", Generation: 2, PID: 9,
	}
	if err := protocol.ValidateReady(r, "worker-1", 2); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateReady(r, "worker-2", 2); err == nil {
		t.Fatal("expected worker mismatch")
	}
}

type slowWriter struct {
	n   int
	buf bytes.Buffer
}

func (w *slowWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := w.n
	if n > len(p) {
		n = len(p)
	}
	return w.buf.Write(p[:n])
}
