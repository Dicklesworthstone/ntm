package agentmail_test

// WS6-wire behavior proof (bd-ws6-config-truth-ienmd.1): flipping the
// [retry.agent_mail] knobs changes the observed number of Agent Mail tool
// calls. G2's blind spot (a registered reader that ignores the value) is
// covered by asserting call COUNTS against a real HTTP server, not by
// inspecting configuration state.
//
// External test package: internal/agentmail itself cannot import
// internal/config (cycle via internal/watcher), but its external tests can.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

func newAlwaysBusyServer(t *testing.T, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req agentmail.JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := agentmail.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &agentmail.JSONRPCError{
				Code:    -32000,
				Message: "database is busy",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestBusyRetryPolicyGovernsCallCounts proves the [retry.agent_mail] policy
// governs the busy-retry loop: the number of HTTP calls observed by a real
// server flips with the configured max_attempts.
func TestBusyRetryPolicyGovernsCallCounts(t *testing.T) {
	defer agentmail.ResetBusyRetryPolicyForTest()

	cases := []struct {
		name      string
		retryCfg  *config.RetryConfig // nil = the shipping default [retry] config
		wantCalls int64
	}{
		{
			// Default config: RetryPolicyFor("agent_mail") yields the
			// historical 3 retries after the first call = 4 calls total.
			name:      "default_policy_four_calls",
			retryCfg:  nil,
			wantCalls: 4,
		},
		{
			name: "custom_policy_one_retry_two_calls",
			retryCfg: &config.RetryConfig{
				AgentMail: config.RetryOverride{MaxAttempts: 1, InitialDelayMs: 1},
			},
			wantCalls: 2,
		},
		{
			name: "custom_policy_five_retries_six_calls",
			retryCfg: &config.RetryConfig{
				AgentMail: config.RetryOverride{MaxAttempts: 5, InitialDelayMs: 1},
			},
			wantCalls: 6,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentmail.ResetBusyRetryPolicyForTest()
			if tc.retryCfg == nil {
				// Prove the shipping default path resolves to the historical
				// retry count; shrink only the delay for test speed.
				def := config.DefaultRetryConfig()
				maxAttempts, _ := def.RetryPolicyFor("agent_mail")
				agentmail.ApplyRetryPolicy(maxAttempts, 1)
			} else {
				agentmail.ApplyRetryPolicy(tc.retryCfg.RetryPolicyFor("agent_mail"))
			}

			var calls atomic.Int64
			server := newAlwaysBusyServer(t, &calls)
			defer server.Close()

			c := agentmail.NewClient(agentmail.WithBaseURL(server.URL + "/"))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := c.EnsureProject(ctx, "/tmp/ws6-test"); err == nil {
				t.Fatal("expected busy error from always-busy server")
			}
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("observed %d calls, want %d (policy must govern retry count)", got, tc.wantCalls)
			}
		})
	}
}

// TestApplyRetryPolicyDefaultsMatchHistoricalBehavior pins the invariant that
// the default config produces the pre-wiring behavior: 3 busy retries and a
// 500ms initial backoff.
func TestApplyRetryPolicyDefaultsMatchHistoricalBehavior(t *testing.T) {
	defer agentmail.ResetBusyRetryPolicyForTest()
	agentmail.ResetBusyRetryPolicyForTest()

	def := config.DefaultRetryConfig()
	agentmail.ApplyRetryPolicy(def.RetryPolicyFor("agent_mail"))
	maxRetries, backoff := agentmail.BusyRetryPolicyForTest()
	if maxRetries != 3 {
		t.Errorf("default agent_mail max retries = %d, want historical 3", maxRetries)
	}
	if backoff != 500*time.Millisecond {
		t.Errorf("default agent_mail initial backoff = %v, want historical 500ms", backoff)
	}
}
