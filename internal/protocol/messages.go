package protocol

import (
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	ProtocolName    = "eregion"
	ProtocolVersion = 1

	TypeHello    = "hello"
	TypeReady    = "ready"
	TypeRequest  = "request"
	TypeResponse = "response"
)

// Hello is sent from Go to PHP during handshake.
type Hello struct {
	Type            string `msgpack:"type"`
	Protocol        string `msgpack:"protocol"`
	ProtocolVersion uint8  `msgpack:"protocol_version"`
	RuntimeVersion  string `msgpack:"runtime_version"`
	WorkerID        string `msgpack:"worker_id"`
	Generation      uint64 `msgpack:"generation"`
}

// Ready is sent from PHP to Go after a successful hello.
type Ready struct {
	Type            string `msgpack:"type"`
	Protocol        string `msgpack:"protocol"`
	ProtocolVersion uint8  `msgpack:"protocol_version"`
	WorkerID        string `msgpack:"worker_id"`
	Generation      uint64 `msgpack:"generation"`
	PID             int    `msgpack:"pid"`
	PHPVersion      string `msgpack:"php_version"`
	MithrilVersion  string `msgpack:"mithril_version"`
}

// RequestEnvelope carries an HTTP request to PHP.
type RequestEnvelope struct {
	Type          string              `msgpack:"type"`
	Version       uint8               `msgpack:"version"`
	ID            string              `msgpack:"id"`
	Method        string              `msgpack:"method"`
	URI           string              `msgpack:"uri"`
	Path          string              `msgpack:"path"`
	Query         string              `msgpack:"query"`
	Protocol      string              `msgpack:"protocol"`
	Headers       map[string][]string `msgpack:"headers"`
	Body          []byte              `msgpack:"body"`
	RemoteAddress string              `msgpack:"remote_address"`
	Host          string              `msgpack:"host"`
	Scheme        string              `msgpack:"scheme"`
	TimeoutMs     uint32              `msgpack:"timeout_ms"`
}

// ResponseEnvelope carries an HTTP response from PHP.
type ResponseEnvelope struct {
	Type    string              `msgpack:"type"`
	Version uint8               `msgpack:"version"`
	ID      string              `msgpack:"id"`
	Status  uint16              `msgpack:"status"`
	Headers map[string][]string `msgpack:"headers"`
	Body    []byte              `msgpack:"body"`
	Error   *ProtocolError      `msgpack:"error,omitempty"`
	Meta    ResponseMeta        `msgpack:"meta"`
}

// ProtocolError is an optional protocol-level error in a response.
type ProtocolError struct {
	Code    string `msgpack:"code"`
	Message string `msgpack:"message"`
}

// ResponseMeta carries worker-side metrics and recycle intent.
type ResponseMeta struct {
	RequestsHandled uint64 `msgpack:"requests_handled"`
	MemoryUsage     uint64 `msgpack:"memory_usage"`
	MemoryPeak      uint64 `msgpack:"memory_peak"`
	Recycle         bool   `msgpack:"recycle"`
	RecycleReason   string `msgpack:"recycle_reason,omitempty"`
}

// Encode marshals v to MessagePack.
func Encode(v any) ([]byte, error) {
	return msgpack.Marshal(v)
}

// Decode unmarshals MessagePack into v.
func Decode(data []byte, v any) error {
	return msgpack.Unmarshal(data, v)
}

// ValidateHello checks a hello message before sending (local sanity).
func ValidateHello(h Hello) error {
	if h.Type != TypeHello {
		return fmt.Errorf("unexpected hello type %q", h.Type)
	}
	if h.Protocol != ProtocolName {
		return fmt.Errorf("unexpected protocol %q", h.Protocol)
	}
	if h.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", h.ProtocolVersion)
	}
	if h.WorkerID == "" {
		return fmt.Errorf("worker_id is required")
	}
	return nil
}

// ValidateReady checks the ready message from PHP.
func ValidateReady(r Ready, expectWorkerID string, expectGeneration uint64) error {
	if r.Type != TypeReady {
		return fmt.Errorf("unexpected ready type %q", r.Type)
	}
	if r.Protocol != ProtocolName {
		return fmt.Errorf("unexpected protocol %q", r.Protocol)
	}
	if r.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", r.ProtocolVersion)
	}
	if r.WorkerID != expectWorkerID {
		return fmt.Errorf("worker_id mismatch: got %q want %q", r.WorkerID, expectWorkerID)
	}
	if r.Generation != expectGeneration {
		return fmt.Errorf("generation mismatch: got %d want %d", r.Generation, expectGeneration)
	}
	if r.PID <= 0 {
		return fmt.Errorf("invalid pid %d", r.PID)
	}
	return nil
}

// ValidateResponse checks a response envelope against the request ID.
func ValidateResponse(resp ResponseEnvelope, expectID string) error {
	if resp.Type != TypeResponse {
		return fmt.Errorf("unexpected response type %q", resp.Type)
	}
	if resp.Version != ProtocolVersion {
		return fmt.Errorf("unsupported response version %d", resp.Version)
	}
	if resp.ID != expectID {
		return fmt.Errorf("request id mismatch: got %q want %q", resp.ID, expectID)
	}
	if resp.Status < 100 || resp.Status > 599 {
		return fmt.Errorf("invalid status %d", resp.Status)
	}
	return nil
}
