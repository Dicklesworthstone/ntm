package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestAMAdapter_Capabilities_ServerAvailable(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/liveness" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(ts.Close)

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

	out, err := a.HealthCheck(context.Background())
	if err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Fatalf("HealthCheck() = %q, want %q", string(out), `{"ok":true}`)
	}
}

func TestAMAdapter_Capabilities_ServerUnavailableOnNonOK(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/liveness" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ts.Close)

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

func TestAssessAgentMailHealthStatus_CorruptRecoveryIsError(t *testing.T) {
	t.Parallel()

	got := AssessAgentMailHealthStatus(&agentmail.HealthStatus{
		Status:      "ok",
		HealthLevel: "green",
		Recovery: &agentmail.RecoveryStatus{
			Mode:       "corrupt",
			NextAction: "Run am doctor repair --yes",
		},
	})

	if got.Healthy {
		t.Fatal("Healthy=true, want false for corrupt recovery")
	}
	if got.DoctorStatus != "error" {
		t.Fatalf("DoctorStatus=%q, want error", got.DoctorStatus)
	}
	for _, want := range []string{"status=ok", "health_level=green", "recovery_mode=corrupt", "doctor repair"} {
		if !strings.Contains(got.Message, want) {
			t.Fatalf("Message=%q, want %q", got.Message, want)
		}
	}
}

func TestCheckAgentMailServerHealth_LivenessButMCPHealthFails(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/liveness":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"status":"alive"}`))
		case "/mcp/":
			http.Error(w, "database may be under heavy contention", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	got := CheckAgentMailServerHealth(context.Background(), ts.URL)
	if got.Healthy {
		t.Fatal("Healthy=true, want false when MCP health_check fails")
	}
	if got.DoctorStatus != "warning" {
		t.Fatalf("DoctorStatus=%q, want warning", got.DoctorStatus)
	}
	if !strings.Contains(got.Message, "listening but MCP health_check failed") {
		t.Fatalf("Message=%q, want MCP failure", got.Message)
	}
}

func TestCheckAgentMailServerHealthWithTokenUsesBearer(t *testing.T) {
	t.Parallel()

	const token = "test-token"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/liveness":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"status":"alive"}`))
		case "/mcp/":
			if got := r.Header.Get("Authorization"); got != "Bearer "+token {
				http.Error(w, "missing bearer", http.StatusUnauthorized)
				return
			}
			var req agentmail.JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode JSON-RPC request: %v", err)
			}
			statusJSON, _ := json.Marshal(&agentmail.HealthStatus{Status: "ok", HealthLevel: "green"})
			resp := agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(statusJSON)}
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	got := CheckAgentMailServerHealthWithToken(context.Background(), ts.URL, token)
	if !got.Healthy {
		t.Fatalf("Healthy=false, want true: %+v", got)
	}
}

func TestAMAdapterHealth_CorruptRecoveryIsUnhealthy(t *testing.T) {
	ts := newAgentMailHealthServer(t, &agentmail.HealthStatus{
		Status:      "ok",
		HealthLevel: "green",
		Recovery: &agentmail.RecoveryStatus{
			Mode:       "corrupt",
			NextAction: "Run am doctor repair --yes",
		},
	})

	restore := withFakeAgentMailBinary(t)
	t.Cleanup(restore)

	a := NewAMAdapter()
	a.SetServerURL(ts.URL)

	got, err := a.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error: %v", err)
	}
	if got.Healthy {
		t.Fatal("Health().Healthy=true, want false")
	}
	if !strings.Contains(got.Message, "recovery_mode=corrupt") {
		t.Fatalf("Health().Message=%q, want corrupt recovery", got.Message)
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

func newAgentMailHealthServer(t *testing.T, health *agentmail.HealthStatus) *httptest.Server {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/liveness":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"status":"alive"}`))
		case "/mcp/":
			var req agentmail.JSONRPCRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode JSON-RPC request: %v", err)
			}
			params, _ := req.Params.(map[string]interface{})
			if params["name"] != "health_check" {
				t.Fatalf("tool name=%v, want health_check", params["name"])
			}
			statusJSON, err := json.Marshal(health)
			if err != nil {
				t.Fatalf("marshal health: %v", err)
			}
			resp := agentmail.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(statusJSON),
			}
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func withFakeAgentMailBinary(t *testing.T) func() {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-agent-mail")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'mcp-agent-mail 0.3.10\\n'\n"), 0o755); err != nil {
		t.Fatalf("write fake mcp-agent-mail: %v", err)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	return func() {
		os.Setenv("PATH", oldPath)
	}
}
