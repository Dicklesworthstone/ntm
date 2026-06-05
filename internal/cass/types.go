package cass

import (
	"encoding/json"
	"fmt"
	"time"
)

// FlexTime wraps time.Time to support unmarshaling from RFC3339 strings or Unix timestamps
type FlexTime struct {
	time.Time
}

// UnmarshalJSON implements custom unmarshaling logic
func (ft *FlexTime) UnmarshalJSON(data []byte) error {
	// Handle null
	if string(data) == "null" {
		ft.Time = time.Time{}
		return nil
	}

	// 1. Try as string (RFC3339)
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if s == "" {
			ft.Time = time.Time{}
			return nil
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return err
		}
		ft.Time = t
		return nil
	}

	// 2. Try as number (Unix seconds or milliseconds)
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		if i == 0 {
			ft.Time = time.Time{}
			return nil
		}
		// Heuristic: If > 1e11 (year 5138), assume milliseconds.
		// Current time ~1.7e9. 1e11 ms = ~1973.
		if i > 100000000000 {
			ft.Time = time.UnixMilli(i)
		} else {
			ft.Time = time.Unix(i, 0)
		}
		return nil
	}

	// 3. Try float (for fractional seconds)
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * 1e9)
		ft.Time = time.Unix(sec, nsec)
		return nil
	}

	return fmt.Errorf("unknown time format: %s", string(data))
}

// SearchHit represents a single search result from CASS
type SearchHit struct {
	SourcePath string    `json:"source_path"`
	LineNumber *int      `json:"line_number,omitempty"`
	Agent      string    `json:"agent"`
	Workspace  string    `json:"workspace"`
	Title      string    `json:"title"`
	Score      float64   `json:"score"`
	Snippet    string    `json:"snippet"`
	CreatedAt  *FlexTime `json:"created_at,omitempty"`
	MatchType  string    `json:"match_type"`
	Content    string    `json:"content,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
}

// CreatedAtTime returns the CreatedAt timestamp as a time.Time
func (h SearchHit) CreatedAtTime() time.Time {
	if h.CreatedAt == nil {
		return time.Time{}
	}
	return h.CreatedAt.Time
}

// Meta contains metadata about the search request
type Meta struct {
	TookMs           int64  `json:"took_ms"`
	IndexSize        int64  `json:"index_size"`
	Version          string `json:"version"`
	ElapsedMs        int64  `json:"elapsed_ms"`
	WildcardFallback bool   `json:"wildcard_fallback"`
	NextCursor       string `json:"next_cursor,omitempty"`
}

// HasMore returns true if there are more results available
func (m *Meta) HasMore() bool {
	if m == nil {
		return false
	}
	return m.NextCursor != ""
}

// Aggregations contains aggregated stats from search results
type Aggregations struct {
	Agents     map[string]int `json:"agents,omitempty"`
	Workspaces map[string]int `json:"workspaces,omitempty"`
	Tags       map[string]int `json:"tags,omitempty"`
}

// SearchResponse represents the full response from a CASS search
type SearchResponse struct {
	Query        string        `json:"query"`
	Limit        int           `json:"limit"`
	Offset       int           `json:"offset"`
	Count        int           `json:"count"`
	TotalMatches int           `json:"total_matches"`
	Hits         []SearchHit   `json:"hits"`
	Meta         *Meta         `json:"_meta,omitempty"`
	Aggregations *Aggregations `json:"aggregations,omitempty"`
}

// HasResults returns true if the response contains any hits
func (r SearchResponse) HasResults() bool {
	return len(r.Hits) > 0
}

// HasMore returns true if there are more results available (via cursor or count)
func (r SearchResponse) HasMore() bool {
	if r.Meta != nil && r.Meta.HasMore() {
		return true
	}
	return r.Offset+r.Count < r.TotalMatches
}

// IndexInfo provides details about the search index
type IndexInfo struct {
	Exists        bool     `json:"exists"`
	Status        string   `json:"status"`
	Fresh         bool     `json:"fresh"`
	Rebuilding    bool     `json:"rebuilding"`
	DocCount      int64    `json:"doc_count"`
	Documents     int64    `json:"documents"`
	SizeBytes     int64    `json:"size_bytes"`
	LastUpdated   FlexTime `json:"last_updated"`
	LastIndexedAt FlexTime `json:"last_indexed_at"`
	Healthy       bool     `json:"healthy"`
}

// SizeMB returns the index size in megabytes
func (i IndexInfo) SizeMB() float64 {
	return float64(i.SizeBytes) / (1024 * 1024)
}

// IsReady returns true when either the legacy or current CASS status schema
// reports that the lexical index is usable.
func (i IndexInfo) IsReady() bool {
	if i.Healthy {
		return true
	}
	if i.Fresh && !i.Rebuilding {
		return i.Status == "" || i.Status == "ready"
	}
	return false
}

// EffectiveLastIndexedAt returns the newest populated index timestamp.
func (i IndexInfo) EffectiveLastIndexedAt(fallback FlexTime) time.Time {
	if !i.LastIndexedAt.IsZero() {
		return i.LastIndexedAt.Time
	}
	if !i.LastUpdated.IsZero() {
		return i.LastUpdated.Time
	}
	return fallback.Time
}

// DBInfo provides details about the SQLite database
type DBInfo struct {
	Path          string `json:"path"`
	Exists        bool   `json:"exists"`
	Opened        bool   `json:"opened"`
	OpenSkipped   bool   `json:"open_skipped"`
	CountsSkipped bool   `json:"counts_skipped"`
	SizeBytes     int64  `json:"size_bytes"`
	Healthy       bool   `json:"healthy"`
	SessionCount  int64  `json:"session_count"`
	Conversations int64  `json:"conversations"`
	Messages      int64  `json:"messages"`
}

// SizeMB returns the DB size in megabytes
func (d DBInfo) SizeMB() float64 {
	return float64(d.SizeBytes) / (1024 * 1024)
}

// IsUsable returns true when either the legacy or current CASS status schema
// reports that the canonical database can be opened.
func (d DBInfo) IsUsable() bool {
	return d.Healthy || d.Opened || (d.Exists && d.Path != "")
}

// Pending tracks items waiting to be indexed
type Pending struct {
	Sessions int `json:"sessions"`
	Files    int `json:"files"`
}

// HasPending returns true if there are items waiting to be indexed
func (p Pending) HasPending() bool {
	return p.Sessions > 0 || p.Files > 0
}

// StatusResponse represents the response from CASS health check
type StatusResponse struct {
	Healthy           bool      `json:"healthy"`
	Version           string    `json:"version,omitempty"`
	RecommendedAction string    `json:"recommended_action,omitempty"`
	Index             IndexInfo `json:"index"`
	Database          DBInfo    `json:"database"`
	Pending           Pending   `json:"pending"`

	// Flattened fields for backwards compatibility/simpler parsing if needed
	IndexSize     int64    `json:"index_size,omitempty"`
	Conversations int64    `json:"conversations,omitempty"`
	Messages      int64    `json:"messages,omitempty"`
	LastIndexedAt FlexTime `json:"last_indexed_at,omitempty"`
	StoragePath   string   `json:"storage_path,omitempty"`
}

// UnmarshalJSON accepts both the legacy CASS status schema and the current
// status/health schema used by recent cass builds.
func (s *StatusResponse) UnmarshalJSON(data []byte) error {
	type statusResponse StatusResponse
	var response statusResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}

	*s = StatusResponse(response)
	if s.Index.IsReady() {
		s.Index.Healthy = true
	}
	if s.Database.IsUsable() {
		s.Database.Healthy = true
	}
	// Older/current CASS schemas can report open_skipped without the newer
	// counts_skipped flag. In that case, counts are unknown and callers should
	// omit conversations/messages rather than surfacing misleading zeros.
	if s.Database.OpenSkipped {
		s.Database.CountsSkipped = true
	}
	if s.LastIndexedAt.IsZero() {
		s.LastIndexedAt = FlexTime{Time: s.Index.EffectiveLastIndexedAt(FlexTime{})}
	}
	s.Conversations = firstNonZero(s.Conversations, s.Database.Conversations, s.Database.SessionCount)
	s.Messages = firstNonZero(s.Messages, s.Database.Messages, s.Index.Documents, s.Index.DocCount)

	return nil
}

// IsHealthy returns true if the overall status is healthy
func (s StatusResponse) IsHealthy() bool {
	return s.Healthy && s.Index.IsReady() && s.Database.IsUsable()
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

// Limits defines API limits.
//
// CASS has emitted more than one capabilities schema over time. Keep both the
// legacy fields and the current robot-format fields so callers can parse either
// response shape without guessing which CLI version they are talking to.
type Limits struct {
	MaxQueryLength       int `json:"max_query_length"`
	MaxResults           int `json:"max_results"`
	MaxConcurrentQueries int `json:"max_concurrent_queries"`
	RateLimitPerMinute   int `json:"rate_limit_per_minute"`
	MaxLimit             int `json:"max_limit"`
	MaxContentLength     int `json:"max_content_length"`
	MaxFields            int `json:"max_fields"`
	MaxAggBuckets        int `json:"max_agg_buckets"`
}

// ToMap returns the populated limits in a JSON-ready shape.
func (l Limits) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"max_query_length":       l.MaxQueryLength,
		"max_results":            l.MaxResults,
		"max_concurrent_queries": l.MaxConcurrentQueries,
		"rate_limit_per_minute":  l.RateLimitPerMinute,
		"max_limit":              l.MaxLimit,
		"max_content_length":     l.MaxContentLength,
		"max_fields":             l.MaxFields,
		"max_agg_buckets":        l.MaxAggBuckets,
	}
}

// Capabilities describes the features supported by the CASS instance
type Capabilities struct {
	CrateVersion    string   `json:"crate_version"`
	APIVersion      int      `json:"api_version"`
	ContractVersion string   `json:"contract_version"`
	Features        []string `json:"features"`
	Connectors      []string `json:"connectors"`
	Limits          Limits   `json:"limits"`
}

// HasFeature checks if a feature is supported
func (c Capabilities) HasFeature(feature string) bool {
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// HasConnector checks if a connector is supported
func (c Capabilities) HasConnector(connector string) bool {
	for _, conn := range c.Connectors {
		if conn == connector {
			return true
		}
	}
	return false
}

// CASSError represents an error returned by the CASS API
type CASSError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e CASSError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s (hint: %s)", e.Message, e.Hint)
	}
	return e.Message
}

// Message represents a chat message in a timeline
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp *FlexTime `json:"timestamp,omitempty"`
}

// TimestampTime returns the message timestamp as time.Time
func (m Message) TimestampTime() time.Time {
	if m.Timestamp == nil {
		return time.Time{}
	}
	return m.Timestamp.Time
}

// TimelineEntry represents an event in the timeline
type TimelineEntry struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Timestamp FlexTime `json:"timestamp"`
	Data      any      `json:"data"`
}

// TimestampTime returns the entry timestamp as time.Time
func (e TimelineEntry) TimestampTime() time.Time {
	return e.Timestamp.Time
}

// SearchOptions configures a search request
type SearchOptions struct {
	Query     string
	Limit     int
	Offset    int
	Agent     string
	Workspace string
	Since     string // e.g., "7d", "24h"
	Until     string // e.g., "now", "2025-01-01"
	Cursor    string
	Fields    string
	MaxTokens int
	Aggregate string
	Explain   bool
	Highlight bool
	Json      bool // Always true for robot mode interactions
}
