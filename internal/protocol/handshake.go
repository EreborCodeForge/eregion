package protocol

import (
	"context"
	"fmt"
	"net"
	"time"
)

// PerformHandshake sends hello and waits for a validated ready message.
func PerformHandshake(
	ctx context.Context,
	conn net.Conn,
	maxFrameBytes uint32,
	hello Hello,
	timeout time.Duration,
) (Ready, error) {
	if err := ValidateHello(hello); err != nil {
		return Ready{}, err
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Ready{}, err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	payload, err := Encode(hello)
	if err != nil {
		return Ready{}, fmt.Errorf("encode hello: %w", err)
	}
	if err := WriteFrame(conn, payload); err != nil {
		return Ready{}, fmt.Errorf("write hello: %w", err)
	}

	raw, err := ReadFrame(conn, maxFrameBytes)
	if err != nil {
		return Ready{}, fmt.Errorf("read ready: %w", err)
	}
	var ready Ready
	if err := Decode(raw, &ready); err != nil {
		return Ready{}, fmt.Errorf("decode ready: %w", err)
	}
	if err := ValidateReady(ready, hello.WorkerID, hello.Generation); err != nil {
		return Ready{}, err
	}
	return ready, nil
}
