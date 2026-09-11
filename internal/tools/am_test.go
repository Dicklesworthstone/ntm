package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

func TestAMAdapter_SetServerURL_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()

	a := NewAMAdapter()
	a.SetServerURL("http://example.test/")
	if a.ServerURL() != "http://example.test" {
		t.Fatalf("ServerURL() = %q, want %q", a.ServerURL(), "http://example.test")
	}
}

// livenessServer serves Agent Mail's liveness endpoint, optionally requiring a
// bearer token the way an authenticated deployment does.
func livenessServer(t *testing.T, requiredToken string, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var unauthorized atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+agentmail.HealthCheckPath {
			http.NotFound(w, r)
			return
		}
		if requiredToken != "" && r.Header.Get("Authorization") != "Bearer "+requiredToken {
			unauthorized.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(ts.Close)

	return ts, &unauthorized
}

func TestAMAdapter_Capabilities_ServerAvailable(t *testing.T) {
	ts, _ := livenessServer(t, "", http.StatusOK)

	a := NewAMAdapter()
	a.SetServerURL(ts.URL + "/")

	caps, err := a.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error: %v", err)
	}
	if !containsCapability(caps, CapMacros) {
		t.Fatalf("expected %q in capabilities", CapMacros)
	}
	if !containsCapability(caps, Capability("server_available")) {
		t.Fatalf("expected %q in capabilities", "server_available")
	}
}

func TestAMAdapter_Capabilities_ServerUnavailableOnNonOK(t *testing.T) {
	// 5xx is a genuine failure verdict, unlike 4xx which only means the
	// endpoint cannot answer and sends the client to the MCP fallback.
	ts, _ := livenessServer(t, "", http.StatusServiceUnavailable)

	a := NewAMAdapter()
	a.SetServerURL(ts.URL)

	caps, err := a.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities() error: %v", err)
	}
	if containsCapability(caps, Capability("server_available")) {
		t.Fatalf("did not expect %q in capabilities", "server_available")
	}
}

// TestAMAdapter_Health_AuthenticatedServer is the ntm#316 regression. An
// Agent Mail endpoint that requires a bearer token is healthy when the
// adapter presents the configured token, and the adapter must actually
// present it — the old probe sent an unauthenticated GET to a hard-coded
// loopback URL, so `ntm doctor` reported a working authenticated server as
// unhealthy while robot Mail talked to it fine.
func TestAMAdapter_Health_AuthenticatedServer(t *testing.T) {
	const token = "s3cret-token"

	t.Run("with the configured token the server is healthy", func(t *testing.T) {
		ts, unauthorized := livenessServer(t, token, http.StatusOK)

		a := NewAMAdapter()
		a.SetServerURL(ts.URL)
		a.SetToken(token)

		if !a.isServerHealthy(context.Background()) {
			t.Error("authenticated server reported unhealthy despite a valid bearer token")
		}
		if got := unauthorized.Load(); got != 0 {
			t.Errorf("probe was rejected %d times; the bearer token was not attached", got)
		}
	})

	t.Run("without the token the server is not healthy", func(t *testing.T) {
		ts, unauthorized := livenessServer(t, token, http.StatusOK)

		a := NewAMAdapter()
		a.SetServerURL(ts.URL)

		if a.isServerHealthy(context.Background()) {
			t.Error("server reported healthy without presenting the required token")
		}
		if got := unauthorized.Load(); got == 0 {
			t.Error("expected the unauthenticated probe to be rejected at least once")
		}
	})
}

// TestAMAdapter_ProbesConfiguredEndpoint verifies the adapter no longer pins
// itself to 127.0.0.1:8765: the endpoint it probes is the one it was given
// (ntm#316).
func TestAMAdapter_ProbesConfiguredEndpoint(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(ts.Close)

	a := NewAMAdapter()
	a.SetServerURL(ts.URL)

	if got := a.ServerURL(); !strings.HasPrefix(got, ts.URL) {
		t.Fatalf("ServerURL() = %q, want the configured endpoint %q", got, ts.URL)
	}
	if !a.isServerHealthy(context.Background()) {
		t.Fatal("configured endpoint reported unhealthy")
	}
	if hits.Load() == 0 {
		t.Error("the configured endpoint was never contacted; the probe used a different URL")
	}
}

func containsCapability(caps []Capability, want Capability) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
