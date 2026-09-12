package agentmail

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestConfigOptionsEnvironmentOverridesConfig pins the precedence rule every
// Agent Mail call site now shares: NewClient reads AGENT_MAIL_URL /
// AGENT_MAIL_TOKEN before applying options, so a configured value is yielded
// only when the matching variable is unset. Copies of this rule had drifted
// across call sites before ntm#316 consolidated them here.
func TestConfigOptionsEnvironmentOverridesConfig(t *testing.T) {
	const (
		cfgURL   = "http://config.test:9000/mcp/"
		cfgToken = "config-token"
		envURL   = "http://env.test:9100/mcp/"
		envToken = "env-token"
	)

	cases := []struct {
		name      string
		envURL    string
		envToken  string
		wantURL   string
		wantToken string
	}{
		{
			name:      "no environment: config wins",
			wantURL:   cfgURL,
			wantToken: cfgToken,
		},
		{
			name:      "AGENT_MAIL_URL set: environment URL wins, config token still applies",
			envURL:    envURL,
			wantURL:   envURL,
			wantToken: cfgToken,
		},
		{
			name:      "AGENT_MAIL_TOKEN set: environment token wins, config URL still applies",
			envToken:  envToken,
			wantURL:   cfgURL,
			wantToken: envToken,
		},
		{
			name:      "both set: environment wins outright",
			envURL:    envURL,
			envToken:  envToken,
			wantURL:   envURL,
			wantToken: envToken,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AGENT_MAIL_URL", tc.envURL)
			t.Setenv("AGENT_MAIL_TOKEN", tc.envToken)

			c := NewClient(ConfigOptions(cfgURL, cfgToken)...)

			if got := strings.TrimSuffix(c.BaseURL(), "/"); got != strings.TrimSuffix(tc.wantURL, "/") {
				t.Errorf("BaseURL() = %q, want %q", got, tc.wantURL)
			}
			if c.bearerToken != tc.wantToken {
				t.Errorf("bearer token = %q, want %q", c.bearerToken, tc.wantToken)
			}
		})
	}
}

// TestConfigOptionsEmptyConfigKeepsDefaults verifies that an unconfigured
// endpoint yields no options at all, leaving the client on its default base
// URL with no bearer.
func TestConfigOptionsEmptyConfigKeepsDefaults(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	if opts := ConfigOptions("", ""); len(opts) != 0 {
		t.Fatalf("ConfigOptions(\"\", \"\") returned %d options, want 0", len(opts))
	}

	c := NewClient(ConfigOptions("", "")...)
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("BaseURL() = %q, want the default %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.bearerToken != "" {
		t.Errorf("bearer token = %q, want empty", c.bearerToken)
	}
}

// TestQuickAvailableDecidesWithoutRetrying pins the contract that separates
// the inventory probe from the dispatch gate: one request, a clear verdict,
// and "cannot decide" reserved for a liveness endpoint that is missing or
// auth-walled. IsAvailableContext retries with backoff by design, which is
// right for gating a send and ~1.25s too slow for filling a tools row
// (ntm#316).
func TestQuickAvailableDecidesWithoutRetrying(t *testing.T) {
	t.Setenv("AGENT_MAIL_URL", "")
	t.Setenv("AGENT_MAIL_TOKEN", "")

	newServer := func(t *testing.T, status int, requireToken string) (*httptest.Server, *atomic.Int64) {
		t.Helper()
		var requests atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path != "/"+HealthCheckPath {
				http.NotFound(w, r)
				return
			}
			if requireToken != "" && r.Header.Get("Authorization") != "Bearer "+requireToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		return srv, &requests
	}

	t.Run("2xx is available and decided in one request", func(t *testing.T) {
		srv, requests := newServer(t, http.StatusOK, "")
		c := NewClient(WithBaseURL(srv.URL))

		available, decided := c.QuickAvailable(context.Background())
		if !available || !decided {
			t.Errorf("available=%v decided=%v, want true/true", available, decided)
		}
		if got := requests.Load(); got != 1 {
			t.Errorf("made %d requests, want exactly 1 (no retries)", got)
		}
	})

	t.Run("5xx is a decided failure", func(t *testing.T) {
		srv, _ := newServer(t, http.StatusServiceUnavailable, "")
		c := NewClient(WithBaseURL(srv.URL))

		available, decided := c.QuickAvailable(context.Background())
		if available || !decided {
			t.Errorf("available=%v decided=%v, want false/true", available, decided)
		}
	})

	t.Run("auth-walled liveness cannot decide, so the caller may escalate", func(t *testing.T) {
		srv, _ := newServer(t, http.StatusOK, "needed-token")
		c := NewClient(WithBaseURL(srv.URL)) // no token

		available, decided := c.QuickAvailable(context.Background())
		if decided {
			t.Errorf("a 401 decided the question (available=%v); it must defer to the MCP probe", available)
		}
	})

	t.Run("a bearer-accepting server is available", func(t *testing.T) {
		srv, _ := newServer(t, http.StatusOK, "needed-token")
		c := NewClient(WithBaseURL(srv.URL), WithToken("needed-token"))

		if available, decided := c.QuickAvailable(context.Background()); !available || !decided {
			t.Errorf("available=%v decided=%v, want true/true with the right bearer", available, decided)
		}
	})

	t.Run("a dead port is decided immediately", func(t *testing.T) {
		c := NewClient(WithBaseURL("http://127.0.0.1:1"))

		start := time.Now()
		available, decided := c.QuickAvailable(context.Background())
		elapsed := time.Since(start)

		if available || !decided {
			t.Errorf("available=%v decided=%v, want false/true", available, decided)
		}
		if elapsed > 300*time.Millisecond {
			t.Errorf("a refused connection took %s; QuickAvailable must not retry", elapsed)
		}
	})
}
