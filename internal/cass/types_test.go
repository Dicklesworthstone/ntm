package cass

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSearchHitCreatedAtTime(t *testing.T) {
	ts := int64(1702200000)
	tm := time.Unix(ts, 0)
	hit := SearchHit{CreatedAt: &FlexTime{Time: tm}}
	got := hit.CreatedAtTime()
	if !got.Equal(tm) {
		t.Errorf("CreatedAtTime() = %v, want %v", got, tm)
	}

	// Test nil case
	hitNil := SearchHit{}
	if !hitNil.CreatedAtTime().IsZero() {
		t.Error("CreatedAtTime() should return zero time for nil")
	}
}

func TestMetaHasMore(t *testing.T) {
	tests := []struct {
		name string
		meta *Meta
		want bool
	}{
		{"nil meta", nil, false},
		{"empty cursor", &Meta{NextCursor: ""}, false},
		{"with cursor", &Meta{NextCursor: "abc123"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meta.HasMore(); got != tt.want {
				t.Errorf("HasMore() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSearchResponseHasResults(t *testing.T) {
	empty := SearchResponse{Hits: []SearchHit{}}
	if empty.HasResults() {
		t.Error("HasResults() should return false for empty hits")
	}

	withHits := SearchResponse{Hits: []SearchHit{{Title: "test"}}}
	if !withHits.HasResults() {
		t.Error("HasResults() should return true with hits")
	}
}

func TestSearchResponseHasMore(t *testing.T) {
	tests := []struct {
		name string
		resp SearchResponse
		want bool
	}{
		{
			"no more via count",
			SearchResponse{Offset: 0, Count: 10, TotalMatches: 10, Meta: &Meta{}},
			false,
		},
		{
			"more via count",
			SearchResponse{Offset: 0, Count: 10, TotalMatches: 20, Meta: &Meta{}},
			true,
		},
		{
			"more via cursor",
			SearchResponse{Offset: 0, Count: 10, TotalMatches: 10, Meta: &Meta{NextCursor: "next"}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.resp.HasMore(); got != tt.want {
				t.Errorf("HasMore() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIndexInfoSizeMB(t *testing.T) {
	info := IndexInfo{SizeBytes: 10 * 1024 * 1024} // 10 MB
	got := info.SizeMB()
	if got != 10.0 {
		t.Errorf("SizeMB() = %v, want 10.0", got)
	}
}

func TestDBInfoSizeMB(t *testing.T) {
	info := DBInfo{SizeBytes: 5 * 1024 * 1024} // 5 MB
	got := info.SizeMB()
	if got != 5.0 {
		t.Errorf("SizeMB() = %v, want 5.0", got)
	}
}

func TestPendingHasPending(t *testing.T) {
	tests := []struct {
		name    string
		pending Pending
		want    bool
	}{
		{"no pending", Pending{Sessions: 0, Files: 0}, false},
		{"pending sessions", Pending{Sessions: 1, Files: 0}, true},
		{"pending files", Pending{Sessions: 0, Files: 1}, true},
		{"both pending", Pending{Sessions: 1, Files: 1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pending.HasPending(); got != tt.want {
				t.Errorf("HasPending() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStatusResponseIsHealthy(t *testing.T) {
	healthy := StatusResponse{
		Healthy:  true,
		Index:    IndexInfo{Healthy: true},
		Database: DBInfo{Healthy: true},
	}
	if !healthy.IsHealthy() {
		t.Error("IsHealthy() should return true when all healthy")
	}

	unhealthy := StatusResponse{
		Healthy:  true,
		Index:    IndexInfo{Healthy: false},
		Database: DBInfo{Healthy: true},
	}
	if unhealthy.IsHealthy() {
		t.Error("IsHealthy() should return false when index unhealthy")
	}

	pathOnlyDB := StatusResponse{
		Healthy:  true,
		Index:    IndexInfo{Healthy: true},
		Database: DBInfo{Path: "/tmp/cass.db"},
	}
	if pathOnlyDB.IsHealthy() {
		t.Error("IsHealthy() should return false when database is path-only but not opened/healthy")
	}
}

func TestDBInfoIsUsable(t *testing.T) {
	tests := []struct {
		name string
		db   DBInfo
		want bool
	}{
		{name: "legacy healthy", db: DBInfo{Healthy: true}, want: true},
		{name: "opened database", db: DBInfo{Opened: true, Path: "/tmp/cass.db"}, want: true},
		{name: "existing database path", db: DBInfo{Exists: true, Path: "/tmp/cass.db"}, want: true},
		{name: "path only is not usable", db: DBInfo{Path: "/tmp/cass.db"}, want: false},
		{name: "empty database info", db: DBInfo{}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.db.IsUsable()
			if got != tt.want {
				t.Errorf("IsUsable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCapabilitiesHasFeature(t *testing.T) {
	caps := Capabilities{Features: []string{"search", "timeline", "expand"}}

	if !caps.HasFeature("search") {
		t.Error("HasFeature() should return true for existing feature")
	}
	if caps.HasFeature("nonexistent") {
		t.Error("HasFeature() should return false for nonexistent feature")
	}
}

func TestCapabilitiesHasConnector(t *testing.T) {
	caps := Capabilities{Connectors: []string{"claude-code", "codex", "cursor"}}

	if !caps.HasConnector("claude-code") {
		t.Error("HasConnector() should return true for existing connector")
	}
	if caps.HasConnector("nonexistent") {
		t.Error("HasConnector() should return false for nonexistent connector")
	}
}

func TestCASSErrorError(t *testing.T) {
	errNoHint := CASSError{Message: "something failed"}
	if errNoHint.Error() != "something failed" {
		t.Errorf("Error() = %q, want %q", errNoHint.Error(), "something failed")
	}

	errWithHint := CASSError{Message: "query failed", Hint: "try a simpler query"}
	want := "query failed (hint: try a simpler query)"
	if errWithHint.Error() != want {
		t.Errorf("Error() = %q, want %q", errWithHint.Error(), want)
	}
}

func TestSearchResponseUnmarshal(t *testing.T) {
	jsonData := `{
		"query": "authentication error",
		"limit": 20,
		"offset": 0,
		"count": 2,
		"total_matches": 42,
		"hits": [
			{
				"source_path": "/path/to/session.jsonl",
				"agent": "claude-code",
				"workspace": "myproject",
				"title": "Debug auth flow",
				"score": 0.95,
				"snippet": "The authentication error occurs when...",
				"match_type": "semantic"
			},
			{
				"source_path": "/path/to/another.jsonl",
				"line_number": 42,
				"agent": "codex",
				"workspace": "backend",
				"title": "Fix login bug",
				"score": 0.85,
				"snippet": "Found authentication issue...",
				"created_at": 1702200000,
				"match_type": "keyword"
			}
		],
		"_meta": {
			"elapsed_ms": 15,
			"wildcard_fallback": false,
			"next_cursor": "cursor123"
		}
	}`

	var resp SearchResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if resp.Query != "authentication error" {
		t.Errorf("Query = %q, want %q", resp.Query, "authentication error")
	}
	if resp.TotalMatches != 42 {
		t.Errorf("TotalMatches = %d, want 42", resp.TotalMatches)
	}
	if len(resp.Hits) != 2 {
		t.Errorf("len(Hits) = %d, want 2", len(resp.Hits))
	}
	if resp.Hits[0].Agent != "claude-code" {
		t.Errorf("Hits[0].Agent = %q, want %q", resp.Hits[0].Agent, "claude-code")
	}
	if resp.Hits[1].LineNumber == nil || *resp.Hits[1].LineNumber != 42 {
		t.Error("Hits[1].LineNumber should be 42")
	}
	if !resp.HasMore() {
		t.Error("HasMore() should be true with next_cursor")
	}
}

func TestStatusResponseUnmarshal(t *testing.T) {
	jsonData := `{
		"healthy": true,
		"recommended_action": "",
		"index": {
			"doc_count": 1000,
			"size_bytes": 10485760,
			"last_updated": "2024-12-10T08:00:00Z",
			"healthy": true
		},
		"database": {
			"path": "/home/user/.cass/db.sqlite",
			"size_bytes": 5242880,
			"healthy": true,
			"session_count": 150
		},
		"pending": {
			"sessions": 0,
			"files": 2
		}
	}`

	var resp StatusResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !resp.IsHealthy() {
		t.Error("IsHealthy() should return true")
	}
	if resp.Index.DocCount != 1000 {
		t.Errorf("Index.DocCount = %d, want 1000", resp.Index.DocCount)
	}
	if resp.Index.SizeMB() != 10.0 {
		t.Errorf("Index.SizeMB() = %v, want 10.0", resp.Index.SizeMB())
	}
	if !resp.Pending.HasPending() {
		t.Error("Pending.HasPending() should return true")
	}
}

func TestStatusResponseUnmarshalCurrentStatusSchema(t *testing.T) {
	jsonData := `{
		"status": "healthy",
		"healthy": true,
		"recommended_action": "Lexical search is ready",
		"index": {
			"exists": true,
			"status": "ready",
			"fresh": true,
			"last_indexed_at": "2026-05-09T00:02:48.355+00:00",
			"rebuilding": false
		},
		"database": {
			"exists": true,
			"opened": true,
			"path": "/home/user/.local/share/coding-agent-search/agent_search.db",
			"counts_skipped": true,
			"open_skipped": true
		},
		"pending": {
			"sessions": 0
		}
	}`

	var resp StatusResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !resp.IsHealthy() {
		t.Error("IsHealthy() should accept current cass status schema")
	}
	if !resp.Index.IsReady() {
		t.Error("Index.IsReady() should use fresh ready current status fields")
	}
	if resp.Index.EffectiveLastIndexedAt(resp.LastIndexedAt).IsZero() {
		t.Error("EffectiveLastIndexedAt() should use index.last_indexed_at")
	}
	if !resp.Database.CountsSkipped {
		t.Error("Database.CountsSkipped should parse current cass status counts_skipped field")
	}
}

func TestStatusResponseUnmarshalCurrentStatusSchema_OpenSkippedImpliesCountsSkipped(t *testing.T) {
	jsonData := `{
		"status": "healthy",
		"healthy": true,
		"index": {
			"exists": true,
			"status": "ready",
			"fresh": true
		},
		"database": {
			"exists": true,
			"opened": false,
			"open_skipped": true,
			"path": "/home/user/.local/share/coding-agent-search/agent_search.db"
		}
	}`

	var resp StatusResponse
	if err := json.Unmarshal([]byte(jsonData), &resp); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !resp.Database.OpenSkipped {
		t.Fatal("Database.OpenSkipped should parse from current cass status schema")
	}
	if !resp.Database.CountsSkipped {
		t.Error("Database.CountsSkipped should be true when open_skipped=true and counts_skipped is absent")
	}
}

func TestCapabilitiesUnmarshal(t *testing.T) {
	jsonData := `{
		"crate_version": "0.5.0",
		"api_version": 2,
		"contract_version": "2024-12-01",
		"features": ["search", "timeline", "expand", "aggregations"],
		"connectors": ["claude-code", "codex", "cursor", "gemini"],
		"limits": {
			"max_query_length": 1000,
			"max_results": 100,
			"max_concurrent_queries": 10,
			"rate_limit_per_minute": 60
		}
	}`

	var caps Capabilities
	if err := json.Unmarshal([]byte(jsonData), &caps); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if caps.CrateVersion != "0.5.0" {
		t.Errorf("CrateVersion = %q, want %q", caps.CrateVersion, "0.5.0")
	}
	if !caps.HasFeature("timeline") {
		t.Error("HasFeature(timeline) should return true")
	}
	if !caps.HasConnector("claude-code") {
		t.Error("HasConnector(claude-code) should return true")
	}
	if caps.Limits.MaxResults != 100 {
		t.Errorf("Limits.MaxResults = %d, want 100", caps.Limits.MaxResults)
	}
}

func TestCapabilitiesUnmarshalCurrentLimitsSchema(t *testing.T) {
	jsonData := `{
		"crate_version": "0.2.5",
		"api_version": 1,
		"contract_version": "1",
		"features": ["json_output"],
		"connectors": ["claude_code", "codex"],
		"limits": {
			"max_limit": 0,
			"max_content_length": 0,
			"max_fields": 50,
			"max_agg_buckets": 10
		}
	}`

	var caps Capabilities
	if err := json.Unmarshal([]byte(jsonData), &caps); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	limits := caps.Limits.ToMap()
	if got := limits["max_fields"]; got != 50 {
		t.Fatalf("limits[max_fields] = %v, want 50", got)
	}
	if got := limits["max_agg_buckets"]; got != 10 {
		t.Fatalf("limits[max_agg_buckets] = %v, want 10", got)
	}
}
func TestMessageTimestampTime(t *testing.T) {
	ts := int64(1702200000)
	tm := time.Unix(ts, 0)
	msg := Message{Timestamp: &FlexTime{Time: tm}}
	got := msg.TimestampTime()
	if !got.Equal(tm) {
		t.Errorf("TimestampTime() = %v, want %v", got, tm)
	}

	msgNil := Message{}
	if !msgNil.TimestampTime().IsZero() {
		t.Error("TimestampTime() should return zero time for nil")
	}
}

func TestTimelineEntryTimestampTime(t *testing.T) {
	tm := time.Unix(1702200000, 0)
	entry := TimelineEntry{Timestamp: FlexTime{Time: tm}}
	got := entry.TimestampTime()
	if !got.Equal(tm) {
		t.Errorf("TimestampTime() = %v, want %v", got, tm)
	}
}

// =============================================================================
// FlexTime.UnmarshalJSON tests
// =============================================================================

func TestFlexTimeUnmarshalJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		wantZero bool
		wantErr  bool
		checkFn  func(t *testing.T, ft FlexTime) // optional extra checks
	}{
		{
			name:     "null",
			input:    `null`,
			wantZero: true,
		},
		{
			name:  "RFC3339 string",
			input: `"2026-01-15T10:30:00Z"`,
		},
		{
			name:     "empty string",
			input:    `""`,
			wantZero: true,
		},
		{
			name:  "Unix seconds integer",
			input: `1702200000`,
			checkFn: func(t *testing.T, ft FlexTime) {
				t.Helper()
				want := time.Unix(1702200000, 0)
				if !ft.Time.Equal(want) {
					t.Errorf("got %v, want %v", ft.Time, want)
				}
			},
		},
		{
			name:     "zero integer",
			input:    `0`,
			wantZero: true,
		},
		{
			name:  "Unix milliseconds (large int)",
			input: `1702200000000`,
			checkFn: func(t *testing.T, ft FlexTime) {
				t.Helper()
				want := time.UnixMilli(1702200000000)
				if !ft.Time.Equal(want) {
					t.Errorf("got %v, want %v", ft.Time, want)
				}
			},
		},
		{
			name:    "invalid string format",
			input:   `"not-a-date"`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `{invalid}`,
			wantErr: true,
		},
		{
			name:    "boolean",
			input:   `true`,
			wantErr: true,
		},
		{
			name:    "array",
			input:   `[1,2,3]`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var ft FlexTime
			err := json.Unmarshal([]byte(tc.input), &ft)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for input %s", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantZero && !ft.Time.IsZero() {
				t.Errorf("expected zero time for %s, got %v", tc.input, ft.Time)
			}
			if !tc.wantZero && ft.Time.IsZero() {
				t.Errorf("expected non-zero time for %s", tc.input)
			}
			if tc.checkFn != nil {
				tc.checkFn(t, ft)
			}
		})
	}
}

func TestFlexTimeUnmarshalJSON_MillisecondHeuristic(t *testing.T) {
	t.Parallel()

	// Value at the boundary: 100000000001 should be treated as milliseconds
	var ft FlexTime
	err := json.Unmarshal([]byte(`100000000001`), &ft)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := time.UnixMilli(100000000001)
	if !ft.Time.Equal(want) {
		t.Errorf("boundary value: got %v, want %v", ft.Time, want)
	}

	// Value at boundary: 100000000000 should be treated as seconds
	var ft2 FlexTime
	err = json.Unmarshal([]byte(`100000000000`), &ft2)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want2 := time.Unix(100000000000, 0)
	if !ft2.Time.Equal(want2) {
		t.Errorf("boundary value: got %v, want %v", ft2.Time, want2)
	}
}
