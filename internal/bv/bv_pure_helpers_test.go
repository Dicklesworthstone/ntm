package bv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// bv.go: extractStatusField
// =============================================================================

func TestExtractStatusField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    map[string]interface{}
		wantStatus string
		wantOK     bool
	}{
		{"valid status", map[string]interface{}{"status": "open"}, "open", true},
		{"missing key", map[string]interface{}{"other": "val"}, "", false},
		{"not a string", map[string]interface{}{"status": 42}, "", false},
		{"empty string", map[string]interface{}{"status": ""}, "", false},
		{"whitespace only", map[string]interface{}{"status": "   "}, "", false},
		{"trimmed", map[string]interface{}{"status": "  closed  "}, "closed", true},
		{"nil payload", nil, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := extractStatusField(tc.payload)
			if ok != tc.wantOK {
				t.Errorf("extractStatusField(%v) ok = %v, want %v", tc.payload, ok, tc.wantOK)
			}
			if got != tc.wantStatus {
				t.Errorf("extractStatusField(%v) = %q, want %q", tc.payload, got, tc.wantStatus)
			}
		})
	}
}

// =============================================================================
// triage.go: normalizeTriageDir
// =============================================================================

func TestNormalizeTriageDir(t *testing.T) {
	t.Parallel()

	t.Run("absolute path returned as-is", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := normalizeTriageDir(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		abs, _ := filepath.Abs(dir)
		if got != abs {
			t.Errorf("normalizeTriageDir(%q) = %q, want %q", dir, got, abs)
		}
	})

	t.Run("empty uses cwd", func(t *testing.T) {
		t.Parallel()
		got, err := normalizeTriageDir("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == "" {
			t.Error("expected non-empty result for empty input")
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expected absolute path, got %q", got)
		}
	})

	t.Run("subdir resolves to beads root", func(t *testing.T) {
		t.Parallel()
		projectDir := t.TempDir()
		subDir := filepath.Join(projectDir, "internal", "robot")
		if err := os.MkdirAll(filepath.Join(projectDir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}

		got, err := normalizeTriageDir(subDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != projectDir {
			t.Errorf("normalizeTriageDir(%q) = %q, want %q", subDir, got, projectDir)
		}
	})
}

// =============================================================================
// triage.go: InvalidateTriageCache
// =============================================================================

func TestInvalidateTriageCachePure(t *testing.T) {
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	// Cache should be valid after priming
	if !IsCacheValid() {
		t.Fatal("cache should be valid after priming")
	}

	InvalidateTriageCache()

	if IsCacheValid() {
		t.Error("cache should be invalid after invalidation")
	}
}

// =============================================================================
// triage.go: SetTriageCacheTTL
// =============================================================================

func TestSetTriageCacheTTLPure(t *testing.T) {
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	// Set a very short TTL
	SetTriageCacheTTL(1 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	// Cache should now be expired
	if IsCacheValid() {
		t.Error("cache should be expired after very short TTL")
	}
}

func TestGetTriageWithTimeoutIncludesRunLockWait(t *testing.T) {
	InvalidateTriageCache()
	triageRunMu.Lock()
	defer triageRunMu.Unlock()

	start := time.Now()
	_, err := GetTriageWithTimeout(t.TempDir(), 20*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetTriageWithTimeout returned nil error while triage run lock was held")
	}
	if !strings.Contains(err.Error(), "bv timed out after 20ms") {
		t.Fatalf("GetTriageWithTimeout error = %q, want timeout", err.Error())
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("GetTriageWithTimeout waited %s for held run lock, want bounded wait", elapsed)
	}
}

// =============================================================================
// triage.go: IsCacheValid
// =============================================================================

func TestIsCacheValid(t *testing.T) {
	// Start with no cache
	InvalidateTriageCache()
	if IsCacheValid() {
		t.Error("empty cache should not be valid")
	}

	// Prime cache
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	if !IsCacheValid() {
		t.Error("primed cache should be valid")
	}
}

// =============================================================================
// triage.go: GetCacheAge
// =============================================================================

func TestGetCacheAge(t *testing.T) {
	// No cache => age is 0
	InvalidateTriageCache()
	if age := GetCacheAge(); age != 0 {
		t.Errorf("empty cache age = %v, want 0", age)
	}

	// Prime cache
	dir := t.TempDir()
	cleanup := primeTriageCache(t, dir)
	defer cleanup()

	time.Sleep(10 * time.Millisecond)
	age := GetCacheAge()
	if age < 10*time.Millisecond {
		t.Errorf("cache age = %v, expected >= 10ms", age)
	}
}

// =============================================================================
// bv.go: getNoDBState / setNoDBState
// =============================================================================

func TestGetSetNoDBState(t *testing.T) {
	// Unknown dir returns false
	if getNoDBState("/nonexistent/path/for/test") {
		t.Error("unknown dir should return false")
	}

	// Set and retrieve
	setNoDBState("/test/dir/for/nodb", true)
	if !getNoDBState("/test/dir/for/nodb") {
		t.Error("expected true after set")
	}

	// Overwrite with false
	setNoDBState("/test/dir/for/nodb", false)
	if getNoDBState("/test/dir/for/nodb") {
		t.Error("expected false after overwrite")
	}
}

// =============================================================================
// markdown.go: renderRecommendation (direct test)
// =============================================================================

func TestRenderRecommendation(t *testing.T) {
	t.Parallel()

	t.Run("with all fields", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		rec := &TriageRecommendation{
			ID:       "bd-123",
			Title:    "Fix auth bug",
			Type:     "bug",
			Priority: 1,
			Score:    0.95,
			Action:   "Fix the auth module",
			Reasons:  []string{"blocks 5 items", "critical path"},
			Breakdown: &ScoreBreakdown{
				Pagerank:      0.3,
				BlockerRatio:  0.4,
				PriorityBoost: 0.25,
			},
		}
		opts := MarkdownOptions{IncludeScores: true}
		renderRecommendation(&sb, rec, 1, opts)
		out := sb.String()

		checks := []string{"bd-123", "Fix auth bug", "bug", "P1", "blocks 5 items", "critical path", "Fix the auth module", "Score: 0.950"}
		for _, want := range checks {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("no reasons no action no scores", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		rec := &TriageRecommendation{
			ID:       "bd-456",
			Title:    "Simple task",
			Type:     "task",
			Priority: 3,
		}
		opts := MarkdownOptions{IncludeScores: false}
		renderRecommendation(&sb, rec, 2, opts)
		out := sb.String()

		if !strings.Contains(out, "bd-456") {
			t.Errorf("output missing bd-456:\n%s", out)
		}
		if strings.Contains(out, "Score:") {
			t.Errorf("output should not contain Score when IncludeScores=false:\n%s", out)
		}
		if strings.Contains(out, "Action:") {
			t.Errorf("output should not contain Action when empty:\n%s", out)
		}
	})
}

// =============================================================================
// markdown.go: renderHealthSummary (direct test)
// =============================================================================

func TestRenderHealthSummary(t *testing.T) {
	t.Parallel()

	t.Run("with metrics and cycles", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		health := &ProjectHealth{
			GraphMetrics: &GraphMetrics{
				TotalNodes: 50,
				TotalEdges: 45,
				Density:    0.036,
				CycleCount: 2,
			},
		}
		renderHealthSummary(&sb, health)
		out := sb.String()

		if !strings.Contains(out, "Nodes: 50") {
			t.Errorf("missing nodes count:\n%s", out)
		}
		if !strings.Contains(out, "Cycles: 2") {
			t.Errorf("missing cycle warning:\n%s", out)
		}
	})

	t.Run("with metrics no cycles", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		health := &ProjectHealth{
			GraphMetrics: &GraphMetrics{
				TotalNodes: 20,
				TotalEdges: 15,
				Density:    0.079,
				CycleCount: 0,
			},
		}
		renderHealthSummary(&sb, health)
		out := sb.String()

		if !strings.Contains(out, "Nodes: 20") {
			t.Errorf("missing nodes count:\n%s", out)
		}
		if strings.Contains(out, "Cycles") {
			t.Errorf("should not mention cycles when count is 0:\n%s", out)
		}
	})

	t.Run("nil graph metrics", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		health := &ProjectHealth{
			GraphMetrics: nil,
		}
		renderHealthSummary(&sb, health)
		out := sb.String()

		if !strings.Contains(out, "Project Health") {
			t.Errorf("should still have header:\n%s", out)
		}
		if strings.Contains(out, "Nodes") {
			t.Errorf("should not contain nodes when metrics nil:\n%s", out)
		}
	})
}

// =============================================================================
// markdown.go: PreferredFormat
// (already tested in markdown_test.go but adding edge case)
// =============================================================================

func TestPreferredFormatUnknownAgent(t *testing.T) {
	t.Parallel()

	// Unknown agent type should default to JSON
	format := PreferredFormat(AgentType("unknown-agent"))
	if format != FormatJSON {
		t.Errorf("PreferredFormat(unknown) = %q, want %q", format, FormatJSON)
	}
}

// =============================================================================
// client.go: NewBVClientWithOptions edge cases
// =============================================================================

func TestNewBVClientWithOptionsDefaults(t *testing.T) {
	t.Parallel()

	// Zero values should get defaults
	client := NewBVClientWithOptions("/some/path", 0, 0)
	if client.CacheTTL != DefaultClientCacheTTL {
		t.Errorf("CacheTTL = %v, want %v", client.CacheTTL, DefaultClientCacheTTL)
	}
	if client.Timeout != DefaultClientTimeout {
		t.Errorf("Timeout = %v, want %v", client.Timeout, DefaultClientTimeout)
	}
	if client.WorkspacePath != "/some/path" {
		t.Errorf("WorkspacePath = %q, want /some/path", client.WorkspacePath)
	}

	// Custom values should be preserved
	client2 := NewBVClientWithOptions("/other", 5*time.Minute, 10*time.Second)
	if client2.CacheTTL != 5*time.Minute {
		t.Errorf("CacheTTL = %v, want 5m", client2.CacheTTL)
	}
	if client2.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", client2.Timeout)
	}
}
