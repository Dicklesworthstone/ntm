package agentmail

import (
	"errors"
	"testing"
)

// TestMapJSONRPCErrorClassifiesTimeout pins that a daemon timeout surfaced as a
// JSON-RPC error (application code -32000 with a timeout-shaped message) maps to
// ErrTimeout, so callers can classify it via IsTimeout instead of string-matching.
func TestMapJSONRPCErrorClassifiesTimeout(t *testing.T) {
	cases := []struct {
		name string
		rpc  *JSONRPCError
	}{
		{"timed out", &JSONRPCError{Code: -32000, Message: "lock list request timed out after 10s"}},
		{"timeout keyword", &JSONRPCError{Code: -32000, Message: "Timeout waiting for storage lock"}},
		{"deadline exceeded", &JSONRPCError{Code: -32000, Message: "context deadline exceeded"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapJSONRPCError(tc.rpc)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("expected ErrTimeout for %q, got %v", tc.rpc.Message, err)
			}
			if !IsTimeout(err) {
				t.Fatalf("IsTimeout should be true for %q", tc.rpc.Message)
			}
		})
	}
}

// TestMapJSONRPCErrorTimeoutDoesNotShadowOtherCases guards against the timeout
// heuristic over-matching: a non-timeout message must not be classified as one.
func TestMapJSONRPCErrorTimeoutDoesNotShadowOtherCases(t *testing.T) {
	for _, msg := range []string{"agent not registered", "message not found", "resource temporarily busy"} {
		if IsTimeout(mapJSONRPCError(&JSONRPCError{Code: -32000, Message: msg})) {
			t.Fatalf("non-timeout message %q must not map to ErrTimeout", msg)
		}
	}
}
