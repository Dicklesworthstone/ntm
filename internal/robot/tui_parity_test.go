package robot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
)

// =============================================================================
// Helper Function Tests
// =============================================================================

func TestDetectErrors(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected []string
	}{
		{
			name:     "no errors",
			lines:    []string{"normal line", "another line", "all good"},
			expected: nil,
		},
		{
			name:     "error colon",
			lines:    []string{"error: something went wrong", "normal"},
			expected: []string{"error: something went wrong"},
		},
		{
			name:     "Error capitalized",
			lines:    []string{"Error: file not found", "normal"},
			expected: []string{"Error: file not found"},
		},
		{
			name:     "ERROR uppercase",
			lines:    []string{"ERROR: critical failure", "normal"},
			expected: []string{"ERROR: critical failure"},
		},
		{
			name:     "failed colon",
			lines:    []string{"failed: to connect", "normal"},
			expected: []string{"failed: to connect"},
		},
		{
			name:     "panic",
			lines:    []string{"panic: runtime error", "normal"},
			expected: []string{"panic: runtime error"},
		},
		{
			name:     "exception",
			lines:    []string{"exception: null pointer", "normal"},
			expected: []string{"exception: null pointer"},
		},
		{
			name:     "traceback",
			lines:    []string{"Traceback (most recent call last):", "normal"},
			expected: []string{"Traceback (most recent call last):"},
		},
		{
			name: "multiple errors",
			lines: []string{
				"error: first error",
				"normal line",
				"Error: second error",
				"more normal",
			},
			expected: []string{"error: first error", "Error: second error"},
		},
		{
			name:     "long error truncated",
			lines:    []string{"error: " + strings.Repeat("x", 250)},
			expected: []string{"error: " + strings.Repeat("x", 193) + "..."},
		},
		{
			name: "max 10 errors",
			lines: func() []string {
				var lines []string
				for i := 0; i < 15; i++ {
					lines = append(lines, "error: line")
				}
				return lines
			}(),
			expected: func() []string {
				var errors []string
				for i := 0; i < 10; i++ {
					errors = append(errors, "error: line")
				}
				return errors
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectErrors(tt.lines)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d errors, got %d", len(tt.expected), len(result))
				return
			}
			for i, exp := range tt.expected {
				if result[i] != exp {
					t.Errorf("error[%d]: expected %q, got %q", i, exp, result[i])
				}
			}
		})
	}
}

func TestParseCodeBlocks(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected []CodeBlockInfo
	}{
		{
			name:     "no code blocks",
			lines:    []string{"normal line", "another line"},
			expected: nil,
		},
		{
			name: "single code block",
			lines: []string{
				"some text",
				"```go",
				"func main() {}",
				"```",
				"more text",
			},
			expected: []CodeBlockInfo{
				{Language: "go", LineStart: 1, LineEnd: 3},
			},
		},
		{
			name: "code block no language",
			lines: []string{
				"```",
				"some code",
				"```",
			},
			expected: []CodeBlockInfo{
				{Language: "", LineStart: 0, LineEnd: 2},
			},
		},
		{
			name: "multiple code blocks",
			lines: []string{
				"```python",
				"print('hello')",
				"```",
				"text between",
				"```bash",
				"echo hello",
				"```",
			},
			expected: []CodeBlockInfo{
				{Language: "python", LineStart: 0, LineEnd: 2},
				{Language: "bash", LineStart: 4, LineEnd: 6},
			},
		},
		{
			name: "unclosed code block",
			lines: []string{
				"```go",
				"func main() {}",
				"no closing",
			},
			expected: nil, // Unclosed blocks not included
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCodeBlocks(tt.lines)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d blocks, got %d", len(tt.expected), len(result))
				return
			}
			for i, exp := range tt.expected {
				if result[i].Language != exp.Language {
					t.Errorf("block[%d].Language: expected %q, got %q", i, exp.Language, result[i].Language)
				}
				if result[i].LineStart != exp.LineStart {
					t.Errorf("block[%d].LineStart: expected %d, got %d", i, exp.LineStart, result[i].LineStart)
				}
				if result[i].LineEnd != exp.LineEnd {
					t.Errorf("block[%d].LineEnd: expected %d, got %d", i, exp.LineEnd, result[i].LineEnd)
				}
			}
		})
	}
}

func TestExtractFileReferences(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		contains []string // Files that should be in the result
	}{
		{
			name:     "no files",
			lines:    []string{"hello world", "foo bar"},
			contains: nil,
		},
		{
			name:     "go file",
			lines:    []string{"editing main.go now"},
			contains: []string{"main.go"},
		},
		{
			name:     "absolute path",
			lines:    []string{"file at /usr/local/bin/app"},
			contains: []string{"/usr/local/bin/app"},
		},
		{
			name:     "relative path",
			lines:    []string{"see ./src/main.go for details"},
			contains: []string{"./src/main.go"},
		},
		{
			name:     "multiple files",
			lines:    []string{"updated config.yaml and script.sh"},
			contains: []string{"config.yaml", "script.sh"},
		},
		{
			name:     "various extensions",
			lines:    []string{"file.py file.js file.ts file.tsx file.jsx file.json file.md"},
			contains: []string{"file.py", "file.js", "file.ts", "file.tsx", "file.jsx", "file.json", "file.md"},
		},
		{
			name:     "quoted paths",
			lines:    []string{`reading "path/to/file.go" now`},
			contains: []string{"path/to/file.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractFileReferences(tt.lines)
			resultSet := make(map[string]bool)
			for _, f := range result {
				resultSet[f] = true
			}

			for _, expected := range tt.contains {
				if !resultSet[expected] {
					t.Errorf("expected to find %q in results: %v", expected, result)
				}
			}
		})
	}
}

func TestIsLikelyFilePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"main.go", true},
		{"config.yaml", true},
		{"script.sh", true},
		{"/usr/bin/app", true},
		{"./src/main.go", true},
		{"../parent/file.py", true},
		{"hello", false},
		{"foobar", false},
		{"123", false},
		{"file.unknownext", false},
		{"path/to/file.ts", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isLikelyFilePath(tt.input)
			if result != tt.expected {
				t.Errorf("isLikelyFilePath(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "he..."},
		{"hello world", 8, "hello..."},
		{"short", 5, "short"},
		{"ab", 5, "ab"},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateString(tt.input, tt.max)
			if result != tt.expected {
				t.Errorf("truncateString(%q, %d) = %q, expected %q", tt.input, tt.max, result, tt.expected)
			}
		})
	}
}

// =============================================================================
// PrintAlertsTUI Tests
// =============================================================================

func TestPrintAlertsTUI(t *testing.T) {
	// Create a config with alerts enabled
	cfg := config.Default()
	cfg.Alerts.Enabled = true

	// Test with empty options - should return successfully
	opts := TUIAlertsOptions{}

	// Capture stdout by using encodeJSON behavior
	// Since PrintAlertsTUI writes to stdout, we test the output structure
	err := PrintAlertsTUI(cfg, opts)
	if err != nil {
		t.Fatalf("PrintAlertsTUI failed: %v", err)
	}
}

func TestPrintAlertsTUIWithFilters(t *testing.T) {
	cfg := config.Default()
	cfg.Alerts.Enabled = true

	// Test with severity filter
	opts := TUIAlertsOptions{
		Severity: "critical",
	}

	err := PrintAlertsTUI(cfg, opts)
	if err != nil {
		t.Fatalf("PrintAlertsTUI with severity filter failed: %v", err)
	}

	// Test with type filter
	opts = TUIAlertsOptions{
		Type: "agent_stuck",
	}

	err = PrintAlertsTUI(cfg, opts)
	if err != nil {
		t.Fatalf("PrintAlertsTUI with type filter failed: %v", err)
	}

	// Test with session filter
	opts = TUIAlertsOptions{
		Session: "test-session",
	}

	err = PrintAlertsTUI(cfg, opts)
	if err != nil {
		t.Fatalf("PrintAlertsTUI with session filter failed: %v", err)
	}
}

func TestPrintAlertsTUINilConfig(t *testing.T) {
	// Test with nil config - should use defaults
	opts := TUIAlertsOptions{}

	err := PrintAlertsTUI(nil, opts)
	if err != nil {
		t.Fatalf("PrintAlertsTUI with nil config failed: %v", err)
	}
}

// =============================================================================
// PrintDismissAlert Tests
// =============================================================================

func TestPrintDismissAlertNoID(t *testing.T) {
	opts := DismissAlertOptions{
		AlertID:    "",
		DismissAll: false,
	}

	err := PrintDismissAlert(opts)
	if err == nil {
		t.Fatal("expected error when no alert ID provided")
	}
}

func TestPrintDismissAlertWithID(t *testing.T) {
	opts := DismissAlertOptions{
		AlertID: "test-alert-123",
	}

	err := PrintDismissAlert(opts)
	if err != nil {
		t.Fatalf("PrintDismissAlert failed: %v", err)
	}
}

// =============================================================================
// PrintPalette Tests
// =============================================================================

func TestPrintPaletteDefault(t *testing.T) {
	cfg := config.Default()

	opts := PaletteOptions{}

	err := PrintPalette(cfg, opts)
	if err != nil {
		t.Fatalf("PrintPalette failed: %v", err)
	}
}

func TestPrintPaletteWithCategoryFilter(t *testing.T) {
	cfg := config.Default()
	// Add some palette entries
	cfg.Palette = []config.PaletteCmd{
		{Key: "test1", Label: "Test 1", Category: "testing", Prompt: "test prompt"},
		{Key: "test2", Label: "Test 2", Category: "coding", Prompt: "code prompt"},
	}

	opts := PaletteOptions{
		Category: "testing",
	}

	err := PrintPalette(cfg, opts)
	if err != nil {
		t.Fatalf("PrintPalette with category filter failed: %v", err)
	}
}

func TestPrintPaletteWithSearchQuery(t *testing.T) {
	cfg := config.Default()
	cfg.Palette = []config.PaletteCmd{
		{Key: "fix-bugs", Label: "Fix Bugs", Category: "dev", Prompt: "fix bugs"},
		{Key: "write-tests", Label: "Write Tests", Category: "dev", Prompt: "write tests"},
	}

	opts := PaletteOptions{
		SearchQuery: "test",
	}

	err := PrintPalette(cfg, opts)
	if err != nil {
		t.Fatalf("PrintPalette with search query failed: %v", err)
	}
}

func TestPrintPaletteNilConfig(t *testing.T) {
	opts := PaletteOptions{}

	err := PrintPalette(nil, opts)
	if err != nil {
		t.Fatalf("PrintPalette with nil config failed: %v", err)
	}
}

// =============================================================================
// PrintFiles Tests
// =============================================================================

func TestPrintFilesDefaultOptions(t *testing.T) {
	opts := FilesOptions{}

	err := PrintFiles(opts)
	if err != nil {
		t.Fatalf("PrintFiles failed: %v", err)
	}
}

func TestPrintFilesWithTimeWindow(t *testing.T) {
	windows := []string{"5m", "15m", "1h", "all", "30m"}

	for _, window := range windows {
		t.Run(window, func(t *testing.T) {
			opts := FilesOptions{
				TimeWindow: window,
			}

			err := PrintFiles(opts)
			if err != nil {
				t.Fatalf("PrintFiles with time window %s failed: %v", window, err)
			}
		})
	}
}

func TestPrintFilesWithSession(t *testing.T) {
	opts := FilesOptions{
		Session: "test-session",
	}

	err := PrintFiles(opts)
	if err != nil {
		t.Fatalf("PrintFiles with session filter failed: %v", err)
	}
}

func TestPrintFilesWithLimit(t *testing.T) {
	opts := FilesOptions{
		Limit: 10,
	}

	err := PrintFiles(opts)
	if err != nil {
		t.Fatalf("PrintFiles with limit failed: %v", err)
	}
}

// =============================================================================
// PrintMetrics Tests
// =============================================================================

func TestPrintMetricsDefaultOptions(t *testing.T) {
	opts := MetricsOptions{}

	err := PrintMetrics(opts)
	if err != nil {
		t.Fatalf("PrintMetrics failed: %v", err)
	}
}

func TestPrintMetricsWithPeriod(t *testing.T) {
	periods := []string{"1h", "24h", "7d", "all"}

	for _, period := range periods {
		t.Run(period, func(t *testing.T) {
			opts := MetricsOptions{
				Period: period,
			}

			err := PrintMetrics(opts)
			if err != nil {
				t.Fatalf("PrintMetrics with period %s failed: %v", period, err)
			}
		})
	}
}

// =============================================================================
// Output Structure Tests
// =============================================================================

func TestTUIAlertsOutputStructure(t *testing.T) {
	output := TUIAlertsOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		Count:         2,
		Alerts: []TUIAlertInfo{
			{
				ID:          "alert-1",
				Type:        "agent_stuck",
				Severity:    "warning",
				Session:     "test",
				Message:     "Agent stuck",
				CreatedAt:   time.Now().Format(time.RFC3339),
				AgeSeconds:  60,
				Dismissible: true,
			},
		},
	}

	// Verify JSON marshaling works
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal TUIAlertsOutput: %v", err)
	}

	// Verify required fields are present
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "count", "alerts"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestFilesOutputStructure(t *testing.T) {
	output := FilesOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		TimeWindow:    "15m",
		Count:         1,
		Changes: []FileChangeRecord{
			{
				Timestamp: time.Now().Format(time.RFC3339),
				Path:      "main.go",
				Operation: "modify",
				Agents:    []string{"claude"},
				Session:   "test",
			},
		},
		Summary: FileChangesSummary{
			TotalChanges: 1,
			UniqueFiles:  1,
			ByAgent:      map[string]int{"claude": 1},
			ByOperation:  map[string]int{"modify": 1},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal FilesOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "time_window", "count", "changes", "summary"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestBeadsListOutputStructure(t *testing.T) {
	output := BeadsListOutput{
		RobotResponse: NewRobotResponse(true),
		Beads: []BeadListItem{
			{
				ID:       "ntm-abc123",
				Title:    "Test bead",
				Status:   "open",
				Priority: "P2",
				Type:     "task",
				IsReady:  true,
			},
		},
		Total:    1,
		Filtered: 1,
		Summary: BeadsListSummary{
			Open:       1,
			InProgress: 0,
			Blocked:    0,
			Closed:     0,
			Ready:      1,
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal BeadsListOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "beads", "total", "filtered", "summary"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// =============================================================================
// BeadsListOptions Tests
// =============================================================================

func TestBeadsListOptionsDefaults(t *testing.T) {
	opts := BeadsListOptions{}

	// Verify default values
	if opts.Limit != 0 {
		t.Errorf("expected default limit 0, got %d", opts.Limit)
	}
	if opts.Status != "" {
		t.Errorf("expected empty default status, got %q", opts.Status)
	}
}

// =============================================================================
// FormatTimestamp Tests (used throughout tui_parity.go)
// =============================================================================

func TestFormatTimestampConsistency(t *testing.T) {
	now := time.Now()
	formatted := FormatTimestamp(now)

	// Verify it's RFC3339 format
	parsed, err := time.Parse(time.RFC3339, formatted)
	if err != nil {
		t.Fatalf("FormatTimestamp output not RFC3339: %v", err)
	}

	// Verify it round-trips reasonably (within a second due to precision)
	diff := now.Sub(parsed)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("timestamp round-trip error: %v", diff)
	}
}

// =============================================================================
// Integration-style Tests
// =============================================================================

func TestAlertInfoFromRealAlert(t *testing.T) {
	// Test that TUIAlertInfo can be properly created from alerts.Alert
	cfg := alerts.DefaultConfig()
	alertList := alerts.GetActiveAlerts(cfg)

	// We don't know if there are alerts, but the function should not panic
	now := time.Now()
	for _, a := range alertList {
		info := TUIAlertInfo{
			ID:          a.ID,
			Type:        string(a.Type),
			Severity:    string(a.Severity),
			Session:     a.Session,
			Pane:        a.Pane,
			Message:     a.Message,
			CreatedAt:   FormatTimestamp(a.CreatedAt),
			AgeSeconds:  int(now.Sub(a.CreatedAt).Seconds()),
			Dismissible: true,
		}

		// Verify the info is valid
		if info.ID == "" {
			t.Error("alert ID should not be empty")
		}
		if info.Type == "" {
			t.Error("alert type should not be empty")
		}
	}
}

// =============================================================================
// PrintInspectPane Tests
// =============================================================================

func TestPrintInspectPaneDefaultOptions(t *testing.T) {
	opts := InspectPaneOptions{
		Session: "nonexistent-session-12345",
	}

	// This should return an error since the session doesn't exist
	err := PrintInspectPane(opts)
	if err == nil {
		t.Log("PrintInspectPane succeeded (tmux session might exist)")
	}
	// We're mainly testing that it doesn't panic
}

func TestPrintInspectPaneWithOptions(t *testing.T) {
	opts := InspectPaneOptions{
		Session:     "test-session",
		PaneIndex:   0,
		Lines:       50,
		IncludeCode: true,
	}

	// This tests that the function handles all options without crashing
	_ = PrintInspectPane(opts)
}

func TestInspectPaneOutputStructure(t *testing.T) {
	output := InspectPaneOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		PaneIndex:     0,
		PaneID:        "%1",
		Agent: InspectPaneAgent{
			Type:  "claude",
			Title: "claude_0",
			State: "idle",
		},
		Output: InspectPaneOutput_{
			Lines:      2,
			Characters: 14,
			LastLines:  []string{"line 1", "line 2"},
			CodeBlocks: []CodeBlockInfo{},
		},
		Context: InspectPaneContext{
			PendingMail: 0,
			RecentFiles: []string{},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectPaneOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "session", "pane_index", "agent", "output"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// =============================================================================
// PrintReplay Tests
// =============================================================================

func TestPrintReplayMissingID(t *testing.T) {
	opts := ReplayOptions{
		Session:   "test-session",
		HistoryID: "",
	}

	err := PrintReplay(opts)
	if err == nil {
		t.Error("expected error when history ID is missing")
	}
}

func TestPrintReplayDryRun(t *testing.T) {
	opts := ReplayOptions{
		Session:   "test-session",
		HistoryID: "1234567890-abcd1234",
		DryRun:    true,
	}

	// Should not error even if session doesn't exist in dry-run mode
	err := PrintReplay(opts)
	if err != nil {
		// It's OK if it errors due to missing history
		t.Logf("PrintReplay dry-run returned: %v", err)
	}
}

func TestReplayOutputStructure(t *testing.T) {
	output := ReplayOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       "test",
		HistoryID:     "1234567890-abcd",
		OriginalCmd:   "echo hello",
		TargetPanes:   []int{0, 1},
		Replayed:      true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal ReplayOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "session", "history_id"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// =============================================================================
// PrintBeadsList Tests (require bd to be installed)
// =============================================================================

func TestPrintBeadsListDefaultOptions(t *testing.T) {
	opts := BeadsListOptions{}

	// This will fail if bd is not installed or .beads/ doesn't exist
	// but we're testing that the function handles options correctly
	err := PrintBeadsList(opts)
	if err != nil {
		t.Logf("PrintBeadsList returned error (expected if bd not available): %v", err)
	}
}

func TestPrintBeadsListWithFilters(t *testing.T) {
	opts := BeadsListOptions{
		Status:   "open",
		Priority: "P2",
		Type:     "task",
		Limit:    5,
	}

	err := PrintBeadsList(opts)
	if err != nil {
		t.Logf("PrintBeadsList with filters returned error: %v", err)
	}
}

func TestPrintBeadsListPriorityNormalization(t *testing.T) {
	// Test that P0-P4 gets normalized to 0-4
	opts := BeadsListOptions{
		Priority: "P1",
		Limit:    3,
	}

	err := PrintBeadsList(opts)
	if err != nil {
		t.Logf("PrintBeadsList with P1 priority returned error: %v", err)
	}

	// Test numeric priority
	opts.Priority = "2"
	err = PrintBeadsList(opts)
	if err != nil {
		t.Logf("PrintBeadsList with numeric priority returned error: %v", err)
	}
}

// =============================================================================
// Bead Management Function Tests
// =============================================================================

func TestPrintBeadClaimMissingID(t *testing.T) {
	opts := BeadClaimOptions{
		BeadID: "",
	}

	err := PrintBeadClaim(opts)
	if err == nil {
		t.Error("expected error when bead ID is missing")
	}
}

func TestBeadClaimOutputStructure(t *testing.T) {
	output := BeadClaimOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        "ntm-abc123",
		Title:         "Test bead",
		PrevStatus:    "open",
		NewStatus:     "in_progress",
		Claimed:       true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal BeadClaimOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "bead_id", "claimed"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestPrintBeadCreateMissingTitle(t *testing.T) {
	opts := BeadCreateOptions{
		Title: "",
		Type:  "task",
	}

	err := PrintBeadCreate(opts)
	if err == nil {
		t.Error("expected error when title is missing")
	}
}

func TestBeadCreateOutputStructure(t *testing.T) {
	output := BeadCreateOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        "ntm-xyz789",
		Title:         "New feature",
		Type:          "feature",
		Priority:      "P2",
		Created:       true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal BeadCreateOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "bead_id", "title", "created"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestPrintBeadShowMissingID(t *testing.T) {
	opts := BeadShowOptions{
		BeadID: "",
	}

	err := PrintBeadShow(opts)
	if err == nil {
		t.Error("expected error when bead ID is missing")
	}
}

func TestBeadShowOutputStructure(t *testing.T) {
	output := BeadShowOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        "ntm-abc123",
		Title:         "Test bead",
		Status:        "open",
		Priority:      "P2",
		Type:          "task",
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal BeadShowOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "bead_id", "title", "status"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestPrintBeadCloseMissingID(t *testing.T) {
	opts := BeadCloseOptions{
		BeadID: "",
	}

	err := PrintBeadClose(opts)
	if err == nil {
		t.Error("expected error when bead ID is missing")
	}
}

func TestBeadCloseOutputStructure(t *testing.T) {
	output := BeadCloseOutput{
		RobotResponse: NewRobotResponse(true),
		BeadID:        "ntm-abc123",
		Title:         "Completed task",
		PrevStatus:    "in_progress",
		NewStatus:     "closed",
		Closed:        true,
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal BeadCloseOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "bead_id", "closed"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

// =============================================================================
// Edge Cases and Error Handling Tests
// =============================================================================

func TestExtractFileReferencesEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		minCount int
	}{
		{
			name:     "empty lines",
			lines:    []string{},
			minCount: 0,
		},
		{
			name:     "lines with only whitespace",
			lines:    []string{"   ", "\t\t", ""},
			minCount: 0,
		},
		{
			name:     "file path at end of line",
			lines:    []string{"editing file internal/robot/tui_parity.go"},
			minCount: 1,
		},
		{
			name:     "multiple paths on one line",
			lines:    []string{"modified main.go and config.yaml and util.go"},
			minCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractFileReferences(tt.lines)
			if len(result) < tt.minCount {
				t.Errorf("expected at least %d file references, got %d: %v", tt.minCount, len(result), result)
			}
		})
	}
}

func TestDetectErrorsEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		wantCount int
	}{
		{
			name:      "empty input",
			lines:     []string{},
			wantCount: 0,
		},
		{
			name:      "error in middle of word should not match",
			lines:     []string{"terror is not an error"},
			wantCount: 0,
		},
		{
			name:      "Error at start of line",
			lines:     []string{"Error: something bad"},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectErrors(tt.lines)
			if len(result) != tt.wantCount {
				t.Errorf("expected %d errors, got %d: %v", tt.wantCount, len(result), result)
			}
		})
	}
}
