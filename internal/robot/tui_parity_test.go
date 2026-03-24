package robot

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
)

func ptrInt64(v int64) *int64 { return &v }

func diagnosticSectionByKind(sections []InspectDiagnosticSection, kind string) *InspectDiagnosticSection {
	for i := range sections {
		if sections[i].Kind == kind {
			return &sections[i]
		}
	}
	return nil
}

func TestFormatTerseLine_FeedUnavailableMarker(t *testing.T) {
	state := TerseState{
		Session:           "proj",
		ActiveAgents:      2,
		TotalAgents:       2,
		AttentionAction:   0,
		AttentionInterest: 0,
	}

	line := formatTerseLine(state, "feed:unavail")
	if !strings.Contains(line, "|T:feed:unavail") {
		t.Fatalf("formatTerseLine() = %q, want feed unavailable marker", line)
	}

	clearLine := formatTerseLine(state, "clear")
	if strings.Contains(clearLine, "|T:") {
		t.Fatalf("formatTerseLine() with clear hint = %q, want no extra marker", clearLine)
	}
}

func TestPrintDashboardMarkdown_IncludesAttentionSection(t *testing.T) {
	output := DashboardOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Unix(1700000000, 0).UTC(),
		Fleet:         "ntm",
		System: SystemInfo{
			Version:   "test-version",
			GoVersion: "go1.test",
			OS:        "linux",
			Arch:      "amd64",
		},
		Summary: StatusSummary{},
		Agents:  []SnapshotSession{},
		Attention: &SnapshotAttentionSummary{
			TotalEvents:         4,
			ActionRequiredCount: 2,
			InterestingCount:    1,
			TopItems: []SnapshotAttentionItem{
				{Cursor: 12, Severity: string(SeverityWarning), Category: string(EventCategoryAlert), Summary: "agent stalled in proj"},
			},
			ByCategoryCount: map[string]int{
				string(EventCategoryAlert): 2,
				string(EventCategoryAgent): 2,
			},
			UnsupportedSignals: []string{"bead_orphaned"},
			NextSteps: []NextAction{
				{Action: "robot-attention", Args: "--robot-attention --attention-cursor=12", Reason: "Continue the operator loop"},
			},
		},
	}

	rendered, err := captureStdout(t, func() error { return printDashboardMarkdown(output) })
	if err != nil {
		t.Fatalf("printDashboardMarkdown() error = %v", err)
	}
	if !strings.Contains(rendered, "## Attention") {
		t.Fatalf("dashboard markdown missing attention section:\n%s", rendered)
	}
	if !strings.Contains(rendered, "| Attention | 2 action-required, 1 interesting |") {
		t.Fatalf("dashboard markdown missing attention headline:\n%s", rendered)
	}
	if !strings.Contains(rendered, "agent stalled in proj") {
		t.Fatalf("dashboard markdown missing frontier item:\n%s", rendered)
	}
	if !strings.Contains(rendered, "robot-attention") {
		t.Fatalf("dashboard markdown missing next-step command:\n%s", rendered)
	}
	if !strings.Contains(rendered, "bead_orphaned") {
		t.Fatalf("dashboard markdown missing unsupported signal:\n%s", rendered)
	}
}

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
	skipSlowRobotShortIntegrationTest(t, "PrintAlertsTUI walks live alert collection and is too expensive for go test -short")

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
	skipSlowRobotShortIntegrationTest(t, "PrintAlertsTUIWithFilters walks live alert collection and is too expensive for go test -short")

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
	skipSlowRobotShortIntegrationTest(t, "PrintAlertsTUINilConfig walks live alert collection and is too expensive for go test -short")

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

	output, err := captureStdout(t, func() error {
		return PrintDismissAlert(opts)
	})
	if err != nil {
		t.Fatalf("PrintDismissAlert returned error: %v", err)
	}

	var result DismissAlertOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Fatal("expected success=false when no alert ID provided")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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
	now := time.Now()
	alertCreatedAt := now.Add(-2 * time.Minute)
	alert := alerts.Alert{
		ID:         "alert-demo",
		Type:       alerts.AlertAgentError,
		Severity:   alerts.SeverityCritical,
		Session:    "alpha",
		Pane:       "%1",
		Message:    "agent crashed repeatedly",
		CreatedAt:  alertCreatedAt,
		LastSeenAt: now,
		Count:      3,
	}

	info := TUIAlertInfo{
		ID:          alert.ID,
		Type:        string(alert.Type),
		Severity:    string(alert.Severity),
		Session:     alert.Session,
		Pane:        alert.Pane,
		Message:     alert.Message,
		CreatedAt:   FormatTimestamp(alert.CreatedAt),
		AgeSeconds:  int(now.Sub(alert.CreatedAt).Seconds()),
		Dismissible: true,
	}

	// Verify the info is valid
	if info.ID == "" {
		t.Error("alert ID should not be empty")
	}
	if info.Type == "" {
		t.Error("alert type should not be empty")
	}
	if info.Severity != string(alerts.SeverityCritical) {
		t.Errorf("severity = %q, want %q", info.Severity, alerts.SeverityCritical)
	}
	if info.Session != "alpha" {
		t.Errorf("session = %q, want %q", info.Session, "alpha")
	}
	if info.Pane != "%1" {
		t.Errorf("pane = %q, want %q", info.Pane, "%1")
	}
	if info.Message != alert.Message {
		t.Errorf("message = %q, want %q", info.Message, alert.Message)
	}
	if info.AgeSeconds < 119 || info.AgeSeconds > 121 {
		t.Errorf("age_seconds = %d, want about 120", info.AgeSeconds)
	}
	if !info.Dismissible {
		t.Error("dismissible = false, want true")
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

func TestInspectSessionOutputStructure(t *testing.T) {
	output := InspectSessionOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-session"),
		SchemaVersion: inspectSessionSchemaVersion,
		Session:       "alpha",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "projection",
				Summary: "Session projection fresh",
				Entries: []InspectDiagnosticEntry{{Code: "projection_freshness", Summary: "Session projection is fresh"}},
			},
		},
		Detail: InspectSessionDetail{
			Name:       "alpha",
			Attached:   true,
			AgentCount: 1,
			Health:     InspectHealth{Status: "healthy"},
			Projection: InspectProjectionInfo{Fresh: true},
			Agents: []InspectAgentDetail{
				{
					ID:         "alpha:%1",
					Session:    "alpha",
					Pane:       "%1",
					Type:       "claude",
					State:      "idle",
					Health:     InspectHealth{Status: "healthy"},
					Projection: InspectProjectionInfo{Fresh: true},
				},
			},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectSessionOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectSessionOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "session", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestInspectAgentOutputStructure(t *testing.T) {
	output := InspectAgentOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-agent"),
		SchemaVersion: inspectAgentSchemaVersion,
		AgentID:       "alpha:%1",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "agent_state",
				Summary: "Agent projected state",
				Entries: []InspectDiagnosticEntry{{Code: "agent_state", Summary: "Agent alpha:%1 is idle"}},
			},
		},
		Detail: InspectAgentDetail{
			ID:         "alpha:%1",
			Session:    "alpha",
			Pane:       "%1",
			Type:       "claude",
			State:      "idle",
			Health:     InspectHealth{Status: "healthy"},
			Projection: InspectProjectionInfo{Fresh: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectAgentOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectAgentOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "agent_id", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestGetInspectSessionUsesProjectionStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	if err := store.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         "alpha",
		Label:        "frontend",
		ProjectPath:  "/data/projects/ntm",
		Attached:     true,
		WindowCount:  1,
		PaneCount:    2,
		AgentCount:   1,
		ActiveAgents: 1,
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now,
		StaleAfter:   staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeSession: %v", err)
	}
	if err := store.UpsertRuntimeAgent(&state.RuntimeAgent{
		ID:               "alpha:%1",
		SessionName:      "alpha",
		Pane:             "%1",
		AgentType:        "claude",
		Variant:          "opus",
		TypeConfidence:   0.9,
		TypeMethod:       "process",
		State:            state.AgentStateBusy,
		StateReason:      "working",
		LastOutputAgeSec: 4,
		OutputTailLines:  12,
		CurrentBead:      "bd-j9jo3.6.4",
		PendingMail:      2,
		AgentMailName:    "BlueLake",
		HealthStatus:     state.HealthStatusHealthy,
		CollectedAt:      now,
		StaleAfter:       staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeAgent: %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "tmux",
		Available:   true,
		Healthy:     false,
		Reason:      "socket refresh lagging",
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(tmux): %v", err)
	}

	output, err := GetInspectSession(InspectSessionOptions{Session: "alpha"})
	if err != nil {
		t.Fatalf("GetInspectSession: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectSession failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-session") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-session"))
	}
	if output.Detail.Name != "alpha" || output.Detail.Label != "frontend" {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	if len(output.Detail.Agents) != 1 {
		t.Fatalf("Detail.Agents = %+v", output.Detail.Agents)
	}
	if output.Detail.Agents[0].ID != "alpha:%1" || output.Detail.Agents[0].CurrentBead != "bd-j9jo3.6.4" {
		t.Fatalf("Agent detail = %+v", output.Detail.Agents[0])
	}
	if diagnosticSectionByKind(output.Diagnostics, "projection") == nil {
		t.Fatalf("Diagnostics = %+v, want projection section", output.Diagnostics)
	}
	sourceSection := diagnosticSectionByKind(output.Diagnostics, "source_health")
	if sourceSection == nil || len(sourceSection.Entries) != 1 || sourceSection.Entries[0].Source != "tmux" {
		t.Fatalf("Diagnostics source health = %+v", output.Diagnostics)
	}
}

func TestGetInspectAgentUsesProjectionStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	if err := store.UpsertRuntimeSession(&state.RuntimeSession{
		Name:         "alpha",
		Attached:     true,
		WindowCount:  1,
		PaneCount:    2,
		AgentCount:   1,
		IdleAgents:   1,
		HealthStatus: state.HealthStatusHealthy,
		CollectedAt:  now,
		StaleAfter:   staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeSession: %v", err)
	}
	if err := store.UpsertRuntimeAgent(&state.RuntimeAgent{
		ID:              "alpha:%2",
		SessionName:     "alpha",
		Pane:            "%2",
		AgentType:       "codex",
		TypeConfidence:  0.8,
		TypeMethod:      "title",
		State:           state.AgentStateIdle,
		OutputTailLines: 24,
		PendingMail:     1,
		HealthStatus:    state.HealthStatusWarning,
		HealthReason:    "waiting for prompt",
		CollectedAt:     now,
		StaleAfter:      staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeAgent: %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "tmux",
		Available:   true,
		Healthy:     true,
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(tmux): %v", err)
	}

	output, err := GetInspectAgent(InspectAgentOptions{AgentID: "alpha:%2"})
	if err != nil {
		t.Fatalf("GetInspectAgent: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectAgent failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-agent") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-agent"))
	}
	if output.Detail.ID != "alpha:%2" || output.Detail.Type != "codex" || output.Detail.Health.Status != "warning" {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	agentSection := diagnosticSectionByKind(output.Diagnostics, "agent_state")
	if agentSection == nil || len(agentSection.Entries) < 2 {
		t.Fatalf("Diagnostics = %+v, want agent_state section with bounded output entry", output.Diagnostics)
	}
}

func TestInspectWorkOutputStructure(t *testing.T) {
	output := InspectWorkOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-work"),
		SchemaVersion: inspectWorkSchemaVersion,
		BeadID:        "bd-j9jo3.6.6",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "work_state",
				Summary: "Work queue evidence",
				Entries: []InspectDiagnosticEntry{{Code: "queue_state", Summary: "Work is ready"}},
			},
		},
		Detail: InspectWorkDetail{
			ID:         "bd-j9jo3.6.6",
			Title:      "Targeted inspect surfaces",
			Status:     "open",
			Queue:      "ready",
			Priority:   1,
			Labels:     []string{"robot-redesign"},
			Health:     InspectHealth{Status: "healthy"},
			Projection: InspectProjectionInfo{Fresh: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectWorkOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectWorkOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "bead_id", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestInspectCoordinationOutputStructure(t *testing.T) {
	output := InspectCoordinationOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-coordination"),
		SchemaVersion: inspectCoordinationSchemaVersion,
		AgentName:     "BlueLake",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "coordination_problems",
				Summary: "Coordination evidence",
				Entries: []InspectDiagnosticEntry{{Code: "urgent_mail", Summary: "1 urgent message"}},
			},
		},
		Detail: InspectCoordinationDetail{
			AgentName:        "BlueLake",
			Mail:             adapters.AgentMailStats{Unread: 2, Pending: 1, Urgent: 1},
			RelatedIncidents: []string{"inc-demo"},
			Problems:         []adapters.CoordinationProblem{{Kind: "urgent_mail", Severity: "error", Summary: "1 urgent message"}},
			Health:           InspectHealth{Status: "critical"},
			Projection:       InspectProjectionInfo{Fresh: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectCoordinationOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectCoordinationOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "agent_name", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestInspectQuotaOutputStructure(t *testing.T) {
	output := InspectQuotaOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-quota"),
		SchemaVersion: inspectQuotaSchemaVersion,
		QuotaID:       "anthropic/default",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "quota_state",
				Summary: "Quota evidence",
				Entries: []InspectDiagnosticEntry{{Code: "quota_status", Summary: "Quota warning"}},
			},
		},
		Detail: InspectQuotaDetail{
			ID:         "anthropic/default",
			Provider:   "anthropic",
			Account:    "default",
			Status:     "warning",
			ReasonCode: adapters.ReasonQuotaWarningTokens,
			Health:     InspectHealth{Status: "warning"},
			Projection: InspectProjectionInfo{Fresh: true},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectQuotaOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectQuotaOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "quota_id", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestInspectIncidentOutputStructure(t *testing.T) {
	output := InspectIncidentOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      defaultRobotSchemaID("inspect-incident"),
		SchemaVersion: inspectIncidentSchemaVersion,
		IncidentID:    "inc-demo",
		Diagnostics: []InspectDiagnosticSection{
			{
				Kind:    "incident_evidence",
				Summary: "Incident evidence",
				Entries: []InspectDiagnosticEntry{{Code: "incident_state", Summary: "Incident open"}},
			},
		},
		Detail: InspectIncidentDetail{
			ID:           "inc-demo",
			Fingerprint:  "incident:fingerprint",
			Title:        "Reservation conflict",
			Status:       "open",
			Severity:     "error",
			SessionNames: []string{"alpha"},
			AgentIDs:     []string{"BlueLake"},
			StartedAt:    time.Now().UTC().Format(time.RFC3339),
			LastEventAt:  time.Now().UTC().Format(time.RFC3339),
			Health:       InspectHealth{Status: "critical"},
		},
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal InspectIncidentOutput: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal InspectIncidentOutput: %v", err)
	}

	requiredFields := []string{"success", "timestamp", "schema_id", "schema_version", "incident_id", "detail", "diagnostics"}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}
}

func TestGetInspectWorkUsesProjectionStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	score := 0.91
	if err := store.UpsertRuntimeWork(&state.RuntimeWork{
		BeadID:         "bd-j9jo3.6.6",
		Title:          "Inspect surfaces",
		Status:         "open",
		Priority:       1,
		BeadType:       "task",
		BlockedByCount: 2,
		UnblocksCount:  4,
		Labels:         `["robot-redesign","inspect"]`,
		Score:          &score,
		ScoreReason:    "high impact",
		CollectedAt:    now,
		StaleAfter:     staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeWork: %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "beads",
		Available:   true,
		Healthy:     false,
		Reason:      "cache lagging",
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(beads): %v", err)
	}

	output, err := GetInspectWork(InspectWorkOptions{BeadID: "bd-j9jo3.6.6"})
	if err != nil {
		t.Fatalf("GetInspectWork: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectWork failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-work") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-work"))
	}
	if output.Detail.Queue != "blocked" || output.Detail.BlockedByCount != 2 || len(output.Detail.Labels) != 2 {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	if diagnosticSectionByKind(output.Diagnostics, "work_state") == nil {
		t.Fatalf("Diagnostics = %+v, want work_state section", output.Diagnostics)
	}
	sourceSection := diagnosticSectionByKind(output.Diagnostics, "source_health")
	if sourceSection == nil || len(sourceSection.Entries) == 0 || sourceSection.Entries[0].Source != "beads" {
		t.Fatalf("Diagnostics source health = %+v", output.Diagnostics)
	}
}

func TestGetInspectCoordinationUsesProjectionStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	if err := store.UpsertRuntimeCoordination(&state.RuntimeCoordination{
		AgentName:                    "BlueLake",
		SessionName:                  "alpha",
		Pane:                         "%1",
		UnreadCount:                  6,
		PendingAckCount:              2,
		UrgentCount:                  1,
		LastMessageSubject:           "Need ack",
		LastMessageSubjectDisclosure: `{"disclosure_state":"visible"}`,
		LastMessagePreview:           "Please ack the handoff",
		LastMessagePreviewDisclosure: `{"disclosure_state":"preview_only","preview":"Please ack the handoff"}`,
		CollectedAt:                  now,
		StaleAfter:                   staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeCoordination: %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "mail",
		Available:   true,
		Healthy:     false,
		Reason:      "mail poll delayed",
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(mail): %v", err)
	}
	if err := store.UpsertRuntimeHandoff(&state.RuntimeHandoff{
		SessionName:      "alpha",
		Status:           "waiting",
		Blockers:         `["mail ack"]`,
		AgentMailThreads: `["br-123"]`,
		CollectedAt:      now,
		StaleAfter:       staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeHandoff: %v", err)
	}
	if err := store.CreateIncident(&state.Incident{
		ID:           "inc-demo",
		Title:        "Coordination incident",
		Fingerprint:  "incident:fingerprint",
		Family:       "coordination",
		Category:     "reservation_conflict",
		Status:       state.IncidentStatusOpen,
		Severity:     state.SeverityError,
		SessionNames: `["alpha"]`,
		AgentIDs:     `["BlueLake"]`,
		StartedAt:    now,
		LastEventAt:  now,
	}); err != nil {
		t.Fatalf("CreateIncident: %v", err)
	}

	output, err := GetInspectCoordination(InspectCoordinationOptions{AgentName: "BlueLake"})
	if err != nil {
		t.Fatalf("GetInspectCoordination: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectCoordination failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-coordination") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-coordination"))
	}
	if output.Detail.Mail.Unread != 6 || output.Detail.Handoff == nil || len(output.Detail.RelatedIncidents) != 1 {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	if diagnosticSectionByKind(output.Diagnostics, "coordination_problems") == nil {
		t.Fatalf("Diagnostics = %+v, want coordination_problems section", output.Diagnostics)
	}
}

func TestGetInspectQuotaUsesProjectionStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	staleAfter := now.Add(time.Hour)
	if err := store.UpsertRuntimeQuota(&state.RuntimeQuota{
		Provider:      "anthropic",
		Account:       "default",
		UsedPct:       87,
		UsedPctKnown:  true,
		UsedPctSource: state.RuntimeQuotaUsedPctSourceTokens,
		IsActive:      true,
		Healthy:       true,
		CollectedAt:   now,
		StaleAfter:    staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeQuota: %v", err)
	}
	if err := store.UpsertSourceHealth(&state.SourceHealth{
		SourceName:  "anthropic",
		Available:   true,
		Healthy:     true,
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(anthropic): %v", err)
	}

	output, err := GetInspectQuota(InspectQuotaOptions{QuotaID: "anthropic/default"})
	if err != nil {
		t.Fatalf("GetInspectQuota: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectQuota failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-quota") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-quota"))
	}
	if output.Detail.ID != "anthropic/default" || output.Detail.Status != "warning" {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	if diagnosticSectionByKind(output.Diagnostics, "quota_state") == nil {
		t.Fatalf("Diagnostics = %+v, want quota_state section", output.Diagnostics)
	}
}

func TestGetInspectIncidentUsesStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := state.Open(filepath.Join(tmpDir, "state.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}

	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
		_ = store.Close()
	})

	now := time.Now().UTC()
	cursor, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            now,
		SessionName:   "alpha",
		Category:      "incident",
		EventType:     "opened",
		Source:        "attention_feed",
		Actionability: state.ActionabilityActionRequired,
		Severity:      state.SeverityCritical,
		Summary:       "Repeated crash incident opened",
		DedupKey:      "incident:inc-demo",
	})
	if err != nil {
		t.Fatalf("AppendAttentionEvent: %v", err)
	}
	itemKey, err := store.ResolveAttentionItemKey(cursor)
	if err != nil {
		t.Fatalf("ResolveAttentionItemKey: %v", err)
	}
	snoozedUntil := now.Add(15 * time.Minute)
	if err := store.UpsertAttentionItemState(&state.AttentionItemState{
		ItemKey:      itemKey,
		DedupKey:     "incident:inc-demo",
		State:        state.AttentionStateSnoozed,
		SnoozedUntil: &snoozedUntil,
		Pinned:       true,
		PinnedAt:     &now,
		PinnedBy:     "operator",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("UpsertAttentionItemState: %v", err)
	}
	if err := store.CreateIncident(&state.Incident{
		ID:               "inc-demo",
		Title:            "Repeated crash",
		Fingerprint:      "incident:fingerprint",
		Family:           "agent",
		Category:         "crash_loop",
		Status:           state.IncidentStatusInvestigating,
		Severity:         state.SeverityCritical,
		SessionNames:     `["alpha"]`,
		AgentIDs:         `["alpha:%1"]`,
		AlertCount:       3,
		EventCount:       5,
		FirstEventCursor: ptrInt64(cursor),
		LastEventCursor:  ptrInt64(cursor),
		StartedAt:        now.Add(-time.Hour),
		LastEventAt:      now,
		RootCause:        "quota exhaustion",
	}); err != nil {
		t.Fatalf("CreateIncident: %v", err)
	}

	output, err := GetInspectIncident(InspectIncidentOptions{IncidentID: "inc-demo"})
	if err != nil {
		t.Fatalf("GetInspectIncident: %v", err)
	}
	if !output.Success {
		t.Fatalf("GetInspectIncident failed: %+v", output.RobotResponse)
	}
	if output.SchemaID != defaultRobotSchemaID("inspect-incident") {
		t.Fatalf("SchemaID = %q, want %q", output.SchemaID, defaultRobotSchemaID("inspect-incident"))
	}
	if output.Detail.ID != "inc-demo" || output.Detail.Fingerprint != "incident:fingerprint" || output.Detail.Health.Status != "warning" {
		t.Fatalf("Detail = %+v", output.Detail)
	}
	if diagnosticSectionByKind(output.Diagnostics, "incident_evidence") == nil {
		t.Fatalf("Diagnostics = %+v, want incident_evidence section", output.Diagnostics)
	}
	attentionSection := diagnosticSectionByKind(output.Diagnostics, "attention_state")
	if attentionSection == nil || len(attentionSection.Entries) < 2 {
		t.Fatalf("Diagnostics = %+v, want attention_state section with cursor and state entries", output.Diagnostics)
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

	output, err := captureStdout(t, func() error {
		return PrintReplay(opts)
	})
	if err != nil {
		t.Fatalf("PrintReplay returned error: %v", err)
	}

	var result ReplayOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Error("expected success=false when history ID is missing")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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

	output, err := captureStdout(t, func() error {
		return PrintBeadClaim(opts)
	})
	if err != nil {
		t.Fatalf("PrintBeadClaim returned error: %v", err)
	}

	var result BeadClaimOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Error("expected success=false when bead ID is missing")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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

	output, err := captureStdout(t, func() error {
		return PrintBeadCreate(opts)
	})
	if err != nil {
		t.Fatalf("PrintBeadCreate returned error: %v", err)
	}

	var result BeadCreateOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Error("expected success=false when title is missing")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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

	output, err := captureStdout(t, func() error {
		return PrintBeadShow(opts)
	})
	if err != nil {
		t.Fatalf("PrintBeadShow returned error: %v", err)
	}

	var result BeadShowOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Error("expected success=false when bead ID is missing")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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

	output, err := captureStdout(t, func() error {
		return PrintBeadClose(opts)
	})
	if err != nil {
		t.Fatalf("PrintBeadClose returned error: %v", err)
	}

	var result BeadCloseOutput
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("failed to parse output as JSON: %v", err)
	}
	if result.Success {
		t.Error("expected success=false when bead ID is missing")
	}
	if result.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error_code %s, got %s", ErrCodeInvalidFlag, result.ErrorCode)
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
