package tools

import (
	"context"
	"testing"
	"time"
)

// InvalidateAvailabilityCache forces the next GetAvailability call to re-check.
func (a *ProxyAdapter) InvalidateAvailabilityCache() {
	proxyAvailabilityMutex.Lock()
	proxyAvailabilityExpiry = time.Time{}
	proxyAvailabilityMutex.Unlock()
}

func TestNewProxyAdapter(t *testing.T) {
	adapter := NewProxyAdapter()
	if adapter == nil {
		t.Fatal("NewProxyAdapter returned nil")
	}

	if adapter.Name() != ToolProxy {
		t.Errorf("Expected name %s, got %s", ToolProxy, adapter.Name())
	}

	if adapter.BinaryName() != "rust_proxy" {
		t.Errorf("Expected binary name 'rust_proxy', got %s", adapter.BinaryName())
	}
}

func TestProxyAdapterImplementsInterface(t *testing.T) {
	var _ Adapter = (*ProxyAdapter)(nil)
}

func TestProxyAdapterDetect(t *testing.T) {
	adapter := NewProxyAdapter()

	path, installed := adapter.Detect()

	if installed && path == "" {
		t.Error("rust_proxy detected but path is empty")
	}
	if !installed && path != "" {
		t.Errorf("rust_proxy not detected but path is %s", path)
	}
}

func TestParseProxyStatusOutput_SampleSchema(t *testing.T) {
	output := []byte(`{"running":true,"version":"1.0.0","uptime_seconds":86400,"routes":2,"errors":0}`)

	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("parseProxyStatusOutput error: %v", err)
	}
	if !status.Running {
		t.Error("Running = false, want true")
	}
	if status.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", status.Version, "1.0.0")
	}
	if status.UptimeSeconds != 86400 {
		t.Errorf("UptimeSeconds = %d, want 86400", status.UptimeSeconds)
	}
	if status.Routes != 2 {
		t.Errorf("Routes = %d, want 2", status.Routes)
	}
	if status.Errors != 0 {
		t.Errorf("Errors = %d, want 0", status.Errors)
	}
}

func TestParseProxyStatusOutput_NestedProxyKey(t *testing.T) {
	output := []byte(`{"proxy":{"daemon_running":true,"version":"1.2.3","listen_port":8080,"uptime":"2h","routes":[{"domain":"api.openai.com","upstream":"proxy-us:443","active":true,"bytes_in":100,"bytes_out":200,"requests":4,"errors":1},{"domain":"api.anthropic.com","upstream":"proxy-eu:443","active":false,"bytes_in":10,"bytes_out":20,"requests":1,"errors":0}],"errors":[{"msg":"x"}],"failover_events":[{"timestamp":"2026-02-06T17:00:00Z","domain":"api.anthropic.com","from":"proxy-eu","to":"proxy-us","reason":"healthcheck failed"}]}}`)

	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("parseProxyStatusOutput error: %v", err)
	}
	if !status.Running {
		t.Error("Running = false, want true")
	}
	if status.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q", status.Version, "1.2.3")
	}
	if status.ListenPort != 8080 {
		t.Errorf("ListenPort = %d, want 8080", status.ListenPort)
	}
	if status.UptimeSeconds != int64(2*time.Hour.Seconds()) {
		t.Errorf("UptimeSeconds = %d, want %d", status.UptimeSeconds, int64(2*time.Hour.Seconds()))
	}
	if status.Routes != 2 {
		t.Errorf("Routes = %d, want 2", status.Routes)
	}
	if len(status.RouteStats) != 2 {
		t.Fatalf("RouteStats len = %d, want 2", len(status.RouteStats))
	}
	if status.RouteStats[0].Domain != "api.openai.com" {
		t.Errorf("RouteStats[0].Domain = %q, want api.openai.com", status.RouteStats[0].Domain)
	}
	if status.RouteStats[0].Requests != 4 {
		t.Errorf("RouteStats[0].Requests = %d, want 4", status.RouteStats[0].Requests)
	}
	if status.Errors != 1 {
		t.Errorf("Errors = %d, want 1", status.Errors)
	}
	if len(status.FailoverEvents) != 1 {
		t.Fatalf("FailoverEvents len = %d, want 1", len(status.FailoverEvents))
	}
	if status.FailoverEvents[0].Domain != "api.anthropic.com" {
		t.Errorf("FailoverEvents[0].Domain = %q, want api.anthropic.com", status.FailoverEvents[0].Domain)
	}
}

func TestParseProxyStatusOutput_InvalidJSON(t *testing.T) {
	_, err := parseProxyStatusOutput([]byte("not-json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseProxyStatusText(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		expectNil  bool
		expectLive bool
	}{
		{
			name:       "running text",
			input:      "rust_proxy is running",
			expectLive: true,
		},
		{
			name:      "not running text",
			input:     "daemon not running",
			expectNil: false,
		},
		{
			name:      "irrelevant text",
			input:     "hello world",
			expectNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProxyStatusText(tc.input)
			if tc.expectNil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected status, got nil")
			}
			if got.Running != tc.expectLive {
				t.Errorf("Running = %v, want %v", got.Running, tc.expectLive)
			}
		})
	}
}

func TestProxyAdapterGetAvailability(t *testing.T) {
	adapter := NewProxyAdapter()
	adapter.InvalidateAvailabilityCache()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		t.Fatalf("GetAvailability returned error: %v", err)
	}
	if availability == nil {
		t.Fatal("GetAvailability returned nil")
	}
	if availability.LastChecked.IsZero() {
		t.Error("LastChecked should be set")
	}
}

// TestProxyHealthWhenInstalledIdle is an end-to-end regression for issue #202:
// rust_proxy is a proxy *selector* that sits idle (active_proxy: null, status
// exit 0) when no proxy is activated — the normal state on a host not routing
// through a proxy. The old probe required a running daemon and reported a
// permanent "daemon not running" warning. An installed, version-compatible
// rust_proxy must now report healthy regardless of daemon state. Runs only
// where rust_proxy is installed.
func TestProxyHealthWhenInstalledIdle(t *testing.T) {
	adapter := NewProxyAdapter()
	adapter.InvalidateAvailabilityCache()
	if _, installed := adapter.Detect(); !installed {
		t.Skip("rust_proxy not installed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, err := adapter.Health(ctx)
	if err != nil {
		t.Fatalf("Health() returned error: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("installed rust_proxy reported unhealthy (idle-daemon over-strict regression): %s", health.Message)
	}
}

func TestToolProxyInAllTools(t *testing.T) {
	tools := AllTools()
	found := false
	for _, tool := range tools {
		if tool == ToolProxy {
			found = true
			break
		}
	}
	if !found {
		t.Error("ToolProxy not found in AllTools()")
	}
}
