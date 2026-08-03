package protocol_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/EreborCodeForge/Eregion/internal/protocol"
)

func TestHandshakeRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	errCh := make(chan error, 1)
	go func() {
		raw, err := protocol.ReadFrame(c2, 1<<20)
		if err != nil {
			errCh <- err
			return
		}
		var hello protocol.Hello
		if err := protocol.Decode(raw, &hello); err != nil {
			errCh <- err
			return
		}
		ready := protocol.Ready{
			Type: protocol.TypeReady, Protocol: protocol.ProtocolName,
			ProtocolVersion: 1, WorkerID: hello.WorkerID, Generation: hello.Generation,
			PID: 42, PHPVersion: "8.3", MithrilVersion: "0.1",
		}
		payload, err := protocol.Encode(ready)
		if err != nil {
			errCh <- err
			return
		}
		errCh <- protocol.WriteFrame(c2, payload)
	}()

	ctx := context.Background()
	hello := protocol.Hello{
		Type: protocol.TypeHello, Protocol: protocol.ProtocolName,
		ProtocolVersion: 1, RuntimeVersion: "test",
		WorkerID: "worker-1", Generation: 3,
	}
	ready, err := protocol.PerformHandshake(ctx, c1, 1<<20, hello, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ready.PID != 42 {
		t.Fatalf("pid %d", ready.PID)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}
