package protocol

import (
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestResponseMetaWithoutRecycleReason(t *testing.T) {
	raw, err := msgpack.Marshal(map[string]any{
		"type":    TypeResponse,
		"version": ProtocolVersion,
		"id":      "req-old",
		"status":  200,
		"headers": map[string][]string{},
		"body":    []byte("ok"),
		"meta": map[string]any{
			"requests_handled": 1,
			"memory_usage":     2,
			"memory_peak":      3,
			"recycle":          false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var env ResponseEnvelope
	if err := msgpack.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.Meta.Recycle || env.Meta.RecycleReason != "" {
		t.Fatalf("meta = %+v", env.Meta)
	}
	if string(env.Body) != "ok" || env.Version != ProtocolVersion {
		t.Fatalf("envelope = %+v", env)
	}
}
