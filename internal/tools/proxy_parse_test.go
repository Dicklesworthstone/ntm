package tools

import (
	"encoding/json"
	"testing"
)

// --- proxyCompatible ---

func TestProxyCompatible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version Version
		want    bool
	}{
		{"exact minimum", Version{Major: 0, Minor: 1, Patch: 0}, true},
		{"above minimum", Version{Major: 0, Minor: 2, Patch: 0}, true},
		{"major above", Version{Major: 1, Minor: 0, Patch: 0}, true},
		{"below minimum", Version{Major: 0, Minor: 0, Patch: 9}, false},
		{"zero version", Version{Major: 0, Minor: 0, Patch: 0}, false},
		{"patch above minimum", Version{Major: 0, Minor: 1, Patch: 5}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := proxyCompatible(tc.version)
			if got != tc.want {
				t.Errorf("proxyCompatible(%v) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

// --- jsonBoolField ---

func TestJsonBoolField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]interface{}
		key     string
		wantVal bool
		wantOK  bool
	}{
		{"true value", map[string]interface{}{"active": true}, "active", true, true},
		{"false value", map[string]interface{}{"active": false}, "active", false, true},
		{"missing key", map[string]interface{}{"other": true}, "active", false, false},
		{"string value", map[string]interface{}{"active": "true"}, "active", false, false},
		{"int value", map[string]interface{}{"active": 1}, "active", false, false},
		{"nil value", map[string]interface{}{"active": nil}, "active", false, false},
		{"empty payload", map[string]interface{}{}, "active", false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			val, ok := jsonBoolField(tc.payload, tc.key)
			if ok != tc.wantOK {
				t.Errorf("jsonBoolField ok = %v, want %v", ok, tc.wantOK)
			}
			if val != tc.wantVal {
				t.Errorf("jsonBoolField val = %v, want %v", val, tc.wantVal)
			}
		})
	}
}

// --- jsonIntField ---

func TestJsonIntField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]interface{}
		key     string
		wantVal int64
		wantOK  bool
	}{
		{"float64 value", map[string]interface{}{"port": float64(8080)}, "port", 8080, true},
		{"int value", map[string]interface{}{"port": int(3000)}, "port", 3000, true},
		{"int64 value", map[string]interface{}{"uptime": int64(86400)}, "uptime", 86400, true},
		{"json.Number valid", map[string]interface{}{"count": json.Number("42")}, "count", 42, true},
		{"json.Number invalid", map[string]interface{}{"count": json.Number("not-a-number")}, "count", 0, false},
		{"missing key", map[string]interface{}{"other": float64(1)}, "port", 0, false},
		{"string value", map[string]interface{}{"port": "8080"}, "port", 0, false},
		{"bool value", map[string]interface{}{"port": true}, "port", 0, false},
		{"nil value", map[string]interface{}{"port": nil}, "port", 0, false},
		{"negative float64", map[string]interface{}{"offset": float64(-10)}, "offset", -10, true},
		{"zero float64", map[string]interface{}{"count": float64(0)}, "count", 0, true},
		{"large int64", map[string]interface{}{"bytes": int64(1 << 40)}, "bytes", 1 << 40, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			val, ok := jsonIntField(tc.payload, tc.key)
			if ok != tc.wantOK {
				t.Errorf("jsonIntField ok = %v, want %v", ok, tc.wantOK)
			}
			if val != tc.wantVal {
				t.Errorf("jsonIntField val = %d, want %d", val, tc.wantVal)
			}
		})
	}
}

// --- jsonStringField ---

func TestJsonStringField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload map[string]interface{}
		key     string
		wantVal string
		wantOK  bool
	}{
		{"simple string", map[string]interface{}{"version": "1.0.0"}, "version", "1.0.0", true},
		{"string with spaces", map[string]interface{}{"version": "  1.0.0  "}, "version", "1.0.0", true},
		{"empty string", map[string]interface{}{"version": ""}, "version", "", true},
		{"missing key", map[string]interface{}{"other": "val"}, "version", "", false},
		{"int value", map[string]interface{}{"version": 123}, "version", "", false},
		{"bool value", map[string]interface{}{"version": true}, "version", "", false},
		{"nil value", map[string]interface{}{"version": nil}, "version", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			val, ok := jsonStringField(tc.payload, tc.key)
			if ok != tc.wantOK {
				t.Errorf("jsonStringField ok = %v, want %v", ok, tc.wantOK)
			}
			if val != tc.wantVal {
				t.Errorf("jsonStringField val = %q, want %q", val, tc.wantVal)
			}
		})
	}
}

// --- parseProxyRoutes ---

func TestParseProxyRoutes(t *testing.T) {
	t.Parallel()

	t.Run("empty slice", func(t *testing.T) {
		t.Parallel()
		result := parseProxyRoutes([]interface{}{})
		if len(result) != 0 {
			t.Errorf("expected empty, got %d routes", len(result))
		}
	})

	t.Run("nil entries skipped", func(t *testing.T) {
		t.Parallel()
		result := parseProxyRoutes([]interface{}{nil, "not-a-map", 42})
		if len(result) != 0 {
			t.Errorf("expected empty (non-map entries skipped), got %d", len(result))
		}
	})

	t.Run("full route", func(t *testing.T) {
		t.Parallel()
		routes := []interface{}{
			map[string]interface{}{
				"domain":    "api.openai.com",
				"upstream":  "proxy-us:443",
				"active":    true,
				"bytes_in":  float64(1024),
				"bytes_out": float64(2048),
				"requests":  float64(100),
				"errors":    float64(3),
			},
		}
		result := parseProxyRoutes(routes)
		if len(result) != 1 {
			t.Fatalf("expected 1 route, got %d", len(result))
		}
		r := result[0]
		if r.Domain != "api.openai.com" {
			t.Errorf("Domain = %q, want %q", r.Domain, "api.openai.com")
		}
		if r.Upstream != "proxy-us:443" {
			t.Errorf("Upstream = %q, want %q", r.Upstream, "proxy-us:443")
		}
		if !r.Active {
			t.Error("Active = false, want true")
		}
		if r.BytesIn != 1024 {
			t.Errorf("BytesIn = %d, want 1024", r.BytesIn)
		}
		if r.BytesOut != 2048 {
			t.Errorf("BytesOut = %d, want 2048", r.BytesOut)
		}
		if r.Requests != 100 {
			t.Errorf("Requests = %d, want 100", r.Requests)
		}
		if r.Errors != 3 {
			t.Errorf("Errors = %d, want 3", r.Errors)
		}
	})

	t.Run("partial route with missing fields", func(t *testing.T) {
		t.Parallel()
		routes := []interface{}{
			map[string]interface{}{
				"domain": "api.anthropic.com",
			},
		}
		result := parseProxyRoutes(routes)
		if len(result) != 1 {
			t.Fatalf("expected 1 route, got %d", len(result))
		}
		r := result[0]
		if r.Domain != "api.anthropic.com" {
			t.Errorf("Domain = %q, want %q", r.Domain, "api.anthropic.com")
		}
		if r.Active {
			t.Error("Active should be false by default")
		}
		if r.Requests != 0 {
			t.Errorf("Requests = %d, want 0", r.Requests)
		}
	})

	t.Run("multiple routes with mixed types", func(t *testing.T) {
		t.Parallel()
		routes := []interface{}{
			map[string]interface{}{"domain": "a.com"},
			"not-a-map",
			map[string]interface{}{"domain": "b.com"},
		}
		result := parseProxyRoutes(routes)
		if len(result) != 2 {
			t.Errorf("expected 2 routes (1 skipped), got %d", len(result))
		}
	})
}

// --- parseProxyFailoverEvents ---

func TestParseProxyFailoverEvents(t *testing.T) {
	t.Parallel()

	t.Run("empty slice", func(t *testing.T) {
		t.Parallel()
		result := parseProxyFailoverEvents([]interface{}{})
		if len(result) != 0 {
			t.Errorf("expected empty, got %d events", len(result))
		}
	})

	t.Run("nil entries skipped", func(t *testing.T) {
		t.Parallel()
		result := parseProxyFailoverEvents([]interface{}{nil, 42})
		if len(result) != 0 {
			t.Errorf("expected empty (non-map entries skipped), got %d", len(result))
		}
	})

	t.Run("full event", func(t *testing.T) {
		t.Parallel()
		events := []interface{}{
			map[string]interface{}{
				"timestamp": "2026-02-06T17:00:00Z",
				"domain":    "api.anthropic.com",
				"from":      "proxy-eu",
				"to":        "proxy-us",
				"reason":    "healthcheck failed",
			},
		}
		result := parseProxyFailoverEvents(events)
		if len(result) != 1 {
			t.Fatalf("expected 1 event, got %d", len(result))
		}
		e := result[0]
		if e.Timestamp != "2026-02-06T17:00:00Z" {
			t.Errorf("Timestamp = %q, want %q", e.Timestamp, "2026-02-06T17:00:00Z")
		}
		if e.Domain != "api.anthropic.com" {
			t.Errorf("Domain = %q, want %q", e.Domain, "api.anthropic.com")
		}
		if e.From != "proxy-eu" {
			t.Errorf("From = %q, want %q", e.From, "proxy-eu")
		}
		if e.To != "proxy-us" {
			t.Errorf("To = %q, want %q", e.To, "proxy-us")
		}
		if e.Reason != "healthcheck failed" {
			t.Errorf("Reason = %q, want %q", e.Reason, "healthcheck failed")
		}
	})

	t.Run("partial event", func(t *testing.T) {
		t.Parallel()
		events := []interface{}{
			map[string]interface{}{
				"domain": "api.openai.com",
				"reason": "timeout",
			},
		}
		result := parseProxyFailoverEvents(events)
		if len(result) != 1 {
			t.Fatalf("expected 1 event, got %d", len(result))
		}
		if result[0].Timestamp != "" {
			t.Errorf("Timestamp = %q, want empty", result[0].Timestamp)
		}
		if result[0].From != "" {
			t.Errorf("From = %q, want empty", result[0].From)
		}
	})
}

// --- parseProxyStatusOutput edge cases ---

func TestParseProxyStatusOutput_Empty(t *testing.T) {
	t.Parallel()

	_, err := parseProxyStatusOutput([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParseProxyStatusOutput_WhitespaceOnly(t *testing.T) {
	t.Parallel()

	_, err := parseProxyStatusOutput([]byte("   \n  \t  "))
	if err == nil {
		t.Fatal("expected error for whitespace-only output")
	}
}

func TestParseProxyStatusOutput_StatusField(t *testing.T) {
	t.Parallel()

	// Test "status" string field (running)
	output := []byte(`{"status":"running","version":"2.0.0"}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Running {
		t.Error("Running = false, want true (status=running)")
	}
	if status.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", status.Version, "2.0.0")
	}
}

func TestParseProxyStatusOutput_StatusFieldActive(t *testing.T) {
	t.Parallel()

	output := []byte(`{"status":"active"}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Running {
		t.Error("Running = false, want true (status=active)")
	}
}

func TestParseProxyStatusOutput_StatusFieldStopped(t *testing.T) {
	t.Parallel()

	output := []byte(`{"status":"stopped"}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Running {
		t.Error("Running = true, want false (status=stopped)")
	}
}

func TestParseProxyStatusOutput_RouteCount(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"route_count":5}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Routes != 5 {
		t.Errorf("Routes = %d, want 5 (from route_count)", status.Routes)
	}
}

func TestParseProxyStatusOutput_ErrorCount(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"error_count":7}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Errors != 7 {
		t.Errorf("Errors = %d, want 7 (from error_count)", status.Errors)
	}
}

func TestParseProxyStatusOutput_ErrorsAsArray(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"errors":["err1","err2","err3"]}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Errors != 3 {
		t.Errorf("Errors = %d, want 3 (from errors array length)", status.Errors)
	}
}

func TestParseProxyStatusOutput_UptimeAsDuration(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"uptime":"30m"}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.UptimeSeconds != 1800 {
		t.Errorf("UptimeSeconds = %d, want 1800 (30m)", status.UptimeSeconds)
	}
}

func TestParseProxyStatusOutput_PortField(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"port":9090}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.ListenPort != 9090 {
		t.Errorf("ListenPort = %d, want 9090 (from port field)", status.ListenPort)
	}
}

func TestParseProxyStatusOutput_RouteStatsField(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"route_stats":[{"domain":"a.com","active":true}]}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Routes != 1 {
		t.Errorf("Routes = %d, want 1 (from route_stats)", status.Routes)
	}
	if len(status.RouteStats) != 1 {
		t.Fatalf("RouteStats len = %d, want 1", len(status.RouteStats))
	}
	if status.RouteStats[0].Domain != "a.com" {
		t.Errorf("RouteStats[0].Domain = %q, want %q", status.RouteStats[0].Domain, "a.com")
	}
}

func TestParseProxyStatusOutput_DaemonRunning(t *testing.T) {
	t.Parallel()

	output := []byte(`{"daemon_running":true}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Running {
		t.Error("Running = false, want true (daemon_running=true)")
	}
}

func TestParseProxyStatusOutput_NoRunningFields(t *testing.T) {
	t.Parallel()

	output := []byte(`{"version":"1.0.0","routes":2}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Running {
		t.Error("Running = true, want false (no running indicator)")
	}
	if status.Routes != 2 {
		t.Errorf("Routes = %d, want 2", status.Routes)
	}
}

func TestParseProxyStatusOutput_UptimeAsInt(t *testing.T) {
	t.Parallel()

	output := []byte(`{"running":true,"uptime":3600}`)
	status, err := parseProxyStatusOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.UptimeSeconds != 3600 {
		t.Errorf("UptimeSeconds = %d, want 3600", status.UptimeSeconds)
	}
}

// --- parseProxyStatusText edge cases ---

func TestParseProxyStatusText_Stopped(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("Proxy daemon stopped")
	if got == nil {
		t.Fatal("expected status, got nil")
	}
	if got.Running {
		t.Error("Running = true, want false")
	}
}

func TestParseProxyStatusText_ConnectionRefused(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("Error: connection refused on port 8080")
	if got == nil {
		t.Fatal("expected status, got nil")
	}
	if got.Running {
		t.Error("Running = true, want false")
	}
}

func TestParseProxyStatusText_NoDaemon(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("no daemon process found")
	if got == nil {
		t.Fatal("expected status, got nil")
	}
	if got.Running {
		t.Error("Running = true, want false")
	}
}

func TestParseProxyStatusText_Active(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("proxy: active (pid 12345)")
	if got == nil {
		t.Fatal("expected status, got nil")
	}
	if !got.Running {
		t.Error("Running = false, want true")
	}
}

func TestParseProxyStatusText_EmptyString(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("")
	if got != nil {
		t.Errorf("expected nil for empty string, got %+v", *got)
	}
}

func TestParseProxyStatusText_WhitespaceOnly(t *testing.T) {
	t.Parallel()

	got := parseProxyStatusText("   \n\t  ")
	if got != nil {
		t.Errorf("expected nil for whitespace-only, got %+v", *got)
	}
}
