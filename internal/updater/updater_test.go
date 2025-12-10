package updater

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0", "v1.0.0", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0", "1.0.0-alpha", 1}, // Release > Pre-release
		{"1.0.0-beta", "1.0.0-alpha", 1}, // beta > alpha
		{"1.0.0-alpha", "1.0.0", -1},
		// Lexical fallback cases
		{"invalid", "1.0.0", 1}, 
		{"1.0.0", "invalid", -1},
	}

	for _, tt := range tests {
		got := compareVersions(tt.v1, tt.v2)
		if got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

// MockRoundTripper for intercepting HTTP requests
type MockRoundTripper struct {
	Response *http.Response
	Err      error
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.Response, m.Err
}

func TestCheckForUpdates_NewVersion(t *testing.T) {
	t.Parallel()
	// Mock response with a newer version
	release := Release{
		TagName: "v2.0.0",
		HTMLURL: "https://github.com/.../v2.0.0",
		Name:    "v2.0.0",
	}
	body, _ := json.Marshal(release)

	client := &http.Client{
		Transport: &MockRoundTripper{
			Response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBuffer(body)),
			},
		},
	}

	info, err := checkForUpdates(client, "http://example.com", "v1.0.0")
	if err != nil {
		t.Fatalf("checkForUpdates failed: %v", err)
	}

	if !info.Available {
		t.Error("Expected update to be available")
	}
	if info.NewVersion != "v2.0.0" {
		t.Errorf("Expected new version v2.0.0, got %s", info.NewVersion)
	}
}

func TestCheckForUpdates_NoUpdate(t *testing.T) {
	t.Parallel()
	// Mock response with same version
	release := Release{
		TagName: "v1.0.0",
	}
	body, _ := json.Marshal(release)

	client := &http.Client{
		Transport: &MockRoundTripper{
			Response: &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBuffer(body)),
			},
		},
	}

	info, err := checkForUpdates(client, "http://example.com", "v1.0.0")
	if err != nil {
		t.Fatalf("checkForUpdates failed: %v", err)
	}

	if info.Available {
		t.Error("Expected no update available")
	}
}

func TestCheckForUpdates_Error(t *testing.T) {
	t.Parallel()
	client := &http.Client{
		Transport: &MockRoundTripper{
			Response: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewBufferString("error")),
			},
		},
	}

	info, err := checkForUpdates(client, "http://example.com", "v1.0.0")
	if err != nil {
		// Implementation returns (info, nil) on non-200 status unless client.Do fails
		t.Logf("Got error: %v", err)
	}

	// Should handle non-200 gracefully
	if info == nil {
		t.Fatal("Expected info struct even on error")
	}
	if info.Available {
		t.Error("Expected no update available on error")
	}
}
