package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCMAdapterConnectSetsDiscoveredServerPort(t *testing.T) {
	tmpDir := t.TempDir()
	writeCMPIDFile(t, tmpDir, "sess-a", 12345)

	adapter := NewCMAdapter()
	if err := adapter.Connect(tmpDir, "sess-a"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if adapter.serverPort != 12345 {
		t.Fatalf("serverPort = %d, want 12345", adapter.serverPort)
	}
}

// newMCPHealthServer fakes a `cm serve` daemon: MCP JSON-RPC at the root
// path, answering tools/list (the health probe) with an empty tool set.
func newMCPHealthServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Errorf("path = %s, want /", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"tools": []any{}},
		})
	}))
}

func TestCMAdapterIsDaemonRunningUsesConnectedClientEndpoint(t *testing.T) {
	ts := newMCPHealthServer(t, 0)
	defer ts.Close()

	port := mustServerPort(t, ts.URL)
	tmpDir := t.TempDir()
	writeCMPIDFile(t, tmpDir, "sess-b", port)

	adapter := NewCMAdapter()
	if err := adapter.Connect(tmpDir, "sess-b"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	// Force fallback port to a known-bad value; health must still pass via connected client.
	adapter.SetServerPort(0)

	if !adapter.isDaemonRunning(context.Background()) {
		t.Fatal("isDaemonRunning() = false, want true via connected client health endpoint")
	}
}

func TestCMAdapterIsDaemonRunningFallsBackToConfiguredPort(t *testing.T) {
	ts := newMCPHealthServer(t, 0)
	defer ts.Close()

	adapter := NewCMAdapter()
	adapter.SetServerPort(mustServerPort(t, ts.URL))

	if !adapter.isDaemonRunning(context.Background()) {
		t.Fatal("isDaemonRunning() = false, want true via configured port fallback")
	}
}

func TestCMAdapterIsDaemonRunningFallbackAfterClientTimeout(t *testing.T) {
	slowServer := newMCPHealthServer(t, 3*time.Second)
	defer slowServer.Close()

	fastServer := newMCPHealthServer(t, 0)
	defer fastServer.Close()

	tmpDir := t.TempDir()
	writeCMPIDFile(t, tmpDir, "sess-c", mustServerPort(t, slowServer.URL))

	adapter := NewCMAdapter()
	if err := adapter.Connect(tmpDir, "sess-c"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	adapter.SetServerPort(mustServerPort(t, fastServer.URL))

	if !adapter.isDaemonRunning(context.Background()) {
		t.Fatal("isDaemonRunning() = false, want true via configured fallback after client timeout")
	}
}

func writeCMPIDFile(t *testing.T, projectDir, sessionID string, port int) {
	t.Helper()

	pidsDir := filepath.Join(projectDir, ".ntm", "pids")
	if err := os.MkdirAll(pidsDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data, err := json.Marshal(map[string]int{"port": port})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	pidPath := filepath.Join(pidsDir, "cm-"+sessionID+".pid")
	if err := os.WriteFile(pidPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func mustServerPort(t *testing.T, rawURL string) int {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("Atoi(port=%q) error = %v", u.Port(), err)
	}
	return port
}
