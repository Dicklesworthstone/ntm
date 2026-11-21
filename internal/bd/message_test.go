package bd

import (
	"context"
	"strings"
	"testing"
)

func TestMessageClientReturnsUnsupportedErrors(t *testing.T) {
	t.Parallel()

	client := &MessageClient{projectPath: "/tmp/project", agentName: "tester"}
	ctx := context.Background()

	if err := client.Send(ctx, "peer", "hello"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Send() error = %v, want unsupported error", err)
	}
	if msgs, err := client.Inbox(ctx, true, false); err == nil || len(msgs) != 0 || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Inbox() = (%v, %v), want (nil, unsupported error)", msgs, err)
	}
	if msg, err := client.Read(ctx, "msg-1"); err == nil || msg != nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Read() = (%v, %v), want (nil, unsupported error)", msg, err)
	}
	if err := client.Ack(ctx, "msg-1"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Ack() error = %v, want unsupported error", err)
	}
}
