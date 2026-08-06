package cass

import (
	"encoding/json"
	"testing"
)

func TestStatusResponseRobustness(t *testing.T) {
	// Scenario: cass returns Unix timestamp (int) instead of RFC3339 string
	jsonData := `{
		"healthy": true,
		"index": {
			"doc_count": 1000,
			"size_bytes": 10485760,
			"last_updated": 1702200000,
			"healthy": true
		},
		"database": {
			"path": "/db",
			"size_bytes": 0,
			"healthy": true,
			"session_count": 0
		},
		"pending": {"sessions": 0, "files": 0}
	}`

	var resp StatusResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("StatusResponse must decode Unix timestamp: %v", err)
	}
	if got, want := resp.Index.LastUpdated.Unix(), int64(1702200000); got != want {
		t.Errorf("Index.LastUpdated.Unix() = %d, want %d", got, want)
	}
	if !resp.IsHealthy() {
		t.Fatalf("StatusResponse = %+v, want healthy current-schema status", resp)
	}
}

func TestSearchHitRobustness(t *testing.T) {
	// Scenario: cass returns RFC3339 string instead of Unix timestamp (int)
	jsonData := `{
		"source_path": "path",
		"agent": "cc",
		"workspace": "ws",
		"title": "title",
		"score": 1.0,
		"snippet": "snippet",
		"match_type": "kw",
		"created_at": "2023-12-10T09:20:00Z"
	}`

	var hit SearchHit
	if err := json.Unmarshal([]byte(jsonData), &hit); err != nil {
		t.Fatalf("SearchHit must decode RFC3339 created_at: %v", err)
	}
	if got, want := hit.CreatedAtTime().Format("2006-01-02T15:04:05Z"), "2023-12-10T09:20:00Z"; got != want {
		t.Errorf("CreatedAtTime() = %q, want %q", got, want)
	}
}
