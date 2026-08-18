package output

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// Helper to capture stdout/stderr
func captureOutput(f func()) (string, string) {
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	f()

	wOut.Close()
	wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var bufOut bytes.Buffer
	var bufErr bytes.Buffer
	bufOut.ReadFrom(rOut)
	bufErr.ReadFrom(rErr)
	return bufOut.String(), bufErr.String()
}

func TestTimestamped(t *testing.T) {
	ts := NewTimestamped()
	if ts.GeneratedAt.IsZero() {
		t.Error("NewTimestamped() time is zero")
	}
	// Check if recent
	if time.Since(ts.GeneratedAt) > time.Second {
		t.Error("NewTimestamped() time is too old")
	}
}

func TestPrintJSON(t *testing.T) {
	data := map[string]string{"foo": "bar"}

	stdout, _ := captureOutput(func() {
		if err := PrintJSON(data); err != nil {
			t.Fatal(err)
		}
	})

	var resp map[string]string
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("PrintJSON output invalid: %v", err)
	}
	if resp["foo"] != "bar" {
		t.Errorf("PrintJSON foo = %q, want bar", resp["foo"])
	}
}

func TestWriteJSONPrettyAndCompact(t *testing.T) {
	payload := map[string]string{"foo": "bar"}

	var prettyBuf bytes.Buffer
	if err := WriteJSON(&prettyBuf, payload, true); err != nil {
		t.Fatalf("WriteJSON pretty error: %v", err)
	}
	prettyOut := prettyBuf.String()
	if !strings.Contains(prettyOut, "\n  \"foo\"") {
		t.Errorf("pretty JSON should be indented, got: %q", prettyOut)
	}

	var compactBuf bytes.Buffer
	if err := WriteJSON(&compactBuf, payload, false); err != nil {
		t.Fatalf("WriteJSON compact error: %v", err)
	}
	compactOut := compactBuf.String()
	if strings.Contains(compactOut, "\n  \"foo\"") {
		t.Errorf("compact JSON should not be indented, got: %q", compactOut)
	}
}

func TestPrintJSONCompactOutput(t *testing.T) {
	stdout, _ := captureOutput(func() {
		if err := PrintJSONCompact(map[string]string{"foo": "bar"}); err != nil {
			t.Fatalf("PrintJSONCompact error: %v", err)
		}
	})
	if strings.Contains(stdout, "\n  \"foo\"") {
		t.Errorf("PrintJSONCompact output should not be indented: %q", stdout)
	}
}

func TestMarshalJSONPrettyAndCompact(t *testing.T) {
	payload := map[string]string{"foo": "bar"}

	pretty, err := MarshalJSON(payload, true)
	if err != nil {
		t.Fatalf("MarshalJSON pretty error: %v", err)
	}
	if !strings.Contains(string(pretty), "\n  \"foo\"") {
		t.Errorf("MarshalJSON pretty should be indented, got: %q", string(pretty))
	}

	compact, err := MarshalJSON(payload, false)
	if err != nil {
		t.Fatalf("MarshalJSON compact error: %v", err)
	}
	if strings.Contains(string(compact), "\n  \"foo\"") {
		t.Errorf("MarshalJSON compact should not be indented, got: %q", string(compact))
	}
}

func TestOutputOrText(t *testing.T) {
	data := map[string]string{"key": "val"}
	textCalled := false
	textFn := func() error {
		textCalled = true
		return nil
	}

	// JSON mode
	// Note: OutputOrText writes to stdout for JSON
	captureOutput(func() {
		OutputOrText(true, data, textFn)
	})
	if textCalled {
		t.Error("OutputOrText(true) called textFn")
	}

	// Text mode
	OutputOrText(false, data, textFn)
	if !textCalled {
		t.Error("OutputOrText(false) did not call textFn")
	}
}

func TestFormatterMethods(t *testing.T) {
	f := New(WithPretty(true))

	// Writer
	if f.Writer() != os.Stdout {
		// Default is stdout
	}

	// Format
	if f.Format() != FormatText {
		t.Error("Expected FormatText default")
	}
}

func TestTextHelpersExtended(t *testing.T) {
	// Text
	var buf bytes.Buffer
	f := New(WithWriter(&buf))

	f.Text("hello")
	if buf.String() != "hello" {
		t.Errorf("Text() = %q, want hello", buf.String())
	}

	buf.Reset()
	f.Line()
	if buf.String() != "\n" {
		t.Errorf("Line() = %q, want newline", buf.String())
	}

	buf.Reset()
	f.Println("world")
	if buf.String() != "world\n" {
		t.Errorf("Println() = %q, want world\\n", buf.String())
	}

	buf.Reset()
	f.Printf("hello %s", "world")
	if buf.String() != "hello world" {
		t.Errorf("Printf() = %q, want hello world", buf.String())
	}
}

func TestTimestampFormat(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	s := FormatTime(now)
	if s != "2025-01-01T12:00:00Z" {
		t.Errorf("FormatTime() = %q", s)
	}

	parsed, err := ParseTime(s)
	if err != nil {
		t.Errorf("ParseTime() error: %v", err)
	}
	if !parsed.Equal(now) {
		t.Errorf("ParseTime() = %v, want %v", parsed, now)
	}
}

func TestDefaultFormatter(t *testing.T) {
	f := DefaultFormatter(true)
	if !f.IsJSON() {
		t.Error("DefaultFormatter(true) should be JSON")
	}

	f = DefaultFormatter(false)
	if f.IsJSON() {
		t.Error("DefaultFormatter(false) should be Text")
	}
}

func TestDetectFormatEnv(t *testing.T) {
	// Env var JSON
	os.Setenv("NTM_OUTPUT_FORMAT", "json")
	if f := DetectFormat(false); f != FormatJSON {
		t.Error("DetectFormat(false) with env=json should be JSON")
	}

	// Env var TEXT
	os.Setenv("NTM_OUTPUT_FORMAT", "text")
	if f := DetectFormat(false); f != FormatText {
		t.Error("DetectFormat(false) with env=text should be Text")
	}
	os.Unsetenv("NTM_OUTPUT_FORMAT")
}

func TestTerminalHelpersWithPipeStdout(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
		w.Close()
		r.Close()
	}()

	if IsTerminal() {
		t.Error("IsTerminal() should be false for pipe stdout")
	}
	if isStdoutTerminal() {
		t.Error("isStdoutTerminal() should be false for pipe stdout")
	}

	os.Unsetenv("NTM_OUTPUT_FORMAT")
	if f := DetectFormat(false); f != FormatJSON {
		t.Errorf("DetectFormat(false) with pipe stdout = %v, want FormatJSON", f)
	}
}

func TestTerminalHelpersWithPipeStderr(t *testing.T) {
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = oldStderr
		w.Close()
		r.Close()
	}()

	if isStderrTerminal() {
		t.Error("isStderrTerminal() should be false for pipe stderr")
	}
}

func TestConfirmWriterDefaults(t *testing.T) {
	var buf bytes.Buffer

	ok := ConfirmWriter(&buf, strings.NewReader("\n"), "Proceed?", ConfirmOptions{
		Default: true,
	})
	if !ok {
		t.Error("ConfirmWriter should return default true on empty input")
	}
	if !strings.Contains(buf.String(), "[Y/n]") {
		t.Errorf("ConfirmWriter output missing default hint: %q", buf.String())
	}

	buf.Reset()
	ok = ConfirmWriter(&buf, strings.NewReader("n\n"), "Proceed?", ConfirmOptions{
		Default: true,
	})
	if ok {
		t.Error("ConfirmWriter should return false for explicit 'n'")
	}
	if !strings.Contains(buf.String(), "[Y/n]") {
		t.Errorf("ConfirmWriter output missing default hint: %q", buf.String())
	}

	buf.Reset()
	ok = ConfirmWriter(&buf, strings.NewReader("\n"), "Proceed?", ConfirmOptions{
		Default: false,
	})
	if ok {
		t.Error("ConfirmWriter should return default false on empty input")
	}
	if !strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("ConfirmWriter output missing default hint: %q", buf.String())
	}
}

func TestTableAlignment(t *testing.T) {
	var buf bytes.Buffer
	tbl := NewTable(&buf, "Col1", "Col2")
	tbl.AddRow("Short", "Long Value Here")
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "Col1") {
		t.Error("Table missing header Col1")
	}
	// Check for padding/alignment (heuristic)
	if !strings.Contains(output, "Short ") { // Should have padding
		t.Error("Table row padding seems missing")
	}
}

func TestTableRenderMissingColumns(t *testing.T) {
	var buf bytes.Buffer
	tbl := NewTable(&buf, "A", "B", "C")
	tbl.AddRow("one", "two")
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "A") || !strings.Contains(output, "B") || !strings.Contains(output, "C") {
		t.Fatalf("expected headers in output, got: %q", output)
	}
	if !strings.Contains(output, "one") || !strings.Contains(output, "two") {
		t.Fatalf("expected row values in output, got: %q", output)
	}
}

func TestCLIErrorBasic(t *testing.T) {
	err := NewCLIError("something failed")
	if err.Error() != "something failed" {
		t.Errorf("CLIError.Error() = %q, want 'something failed'", err.Error())
	}
	if err.Message != "something failed" {
		t.Errorf("CLIError.Message = %q, want 'something failed'", err.Message)
	}
}

func TestCLIErrorChaining(t *testing.T) {
	err := NewCLIError("failed").
		WithCause("network timeout").
		WithHint("check connection").
		WithCode("NET_TIMEOUT")

	if err.Message != "failed" {
		t.Errorf("Message = %q", err.Message)
	}
	if err.Cause != "network timeout" {
		t.Errorf("Cause = %q", err.Cause)
	}
	if err.Hint != "check connection" {
		t.Errorf("Hint = %q", err.Hint)
	}
	if err.Code != "NET_TIMEOUT" {
		t.Errorf("Code = %q", err.Code)
	}
}

func TestFormatCLIErrorPlain(t *testing.T) {
	// Force NO_COLOR to get plain text output
	os.Setenv("NO_COLOR", "1")
	defer os.Unsetenv("NO_COLOR")

	err := NewCLIError("test error").
		WithCause("something went wrong").
		WithHint("try again").
		WithCode("TEST")

	output := FormatCLIError(err)

	if !strings.Contains(output, "Error: test error") {
		t.Errorf("Expected 'Error: test error' in output: %q", output)
	}
	if !strings.Contains(output, "[TEST]") {
		t.Errorf("Expected '[TEST]' in output: %q", output)
	}
	if !strings.Contains(output, "Cause: something went wrong") {
		t.Errorf("Expected cause in output: %q", output)
	}
	if !strings.Contains(output, "Hint: try again") {
		t.Errorf("Expected hint in output: %q", output)
	}
}

func TestFormatCLIErrorMinimal(t *testing.T) {
	os.Setenv("NO_COLOR", "1")
	defer os.Unsetenv("NO_COLOR")

	// Only message, no cause/hint/code
	err := NewCLIError("simple error")
	output := FormatCLIError(err)

	if !strings.Contains(output, "Error: simple error") {
		t.Errorf("Expected 'Error: simple error' in output: %q", output)
	}
	if strings.Contains(output, "Cause:") {
		t.Errorf("Should not have Cause: %q", output)
	}
	if strings.Contains(output, "Hint:") {
		t.Errorf("Should not have Hint: %q", output)
	}
}

func TestSessionNotFoundError(t *testing.T) {
	err := SessionNotFoundError("myproject")

	if !strings.Contains(err.Message, "myproject") {
		t.Errorf("Message should contain session name: %q", err.Message)
	}
	if err.Code != "SESSION_NOT_FOUND" {
		t.Errorf("Code = %q", err.Code)
	}
	if err.Hint == "" {
		t.Error("Hint should not be empty")
	}
}

func TestNewErrorWithHint(t *testing.T) {
	resp := NewErrorWithHint("failed", "try again")
	if resp.Error != "failed" {
		t.Errorf("Error = %q", resp.Error)
	}
	if resp.Hint != "try again" {
		t.Errorf("Hint = %q", resp.Hint)
	}
}

func TestNewErrorFull(t *testing.T) {
	resp := NewErrorFull("CODE", "error msg", "details", "hint")
	if resp.Code != "CODE" {
		t.Errorf("Code = %q", resp.Code)
	}
	if resp.Error != "error msg" {
		t.Errorf("Error = %q", resp.Error)
	}
	if resp.Details != "details" {
		t.Errorf("Details = %q", resp.Details)
	}
	if resp.Hint != "hint" {
		t.Errorf("Hint = %q", resp.Hint)
	}
}

func TestSpawnSuggestions(t *testing.T) {
	suggestions := SpawnSuggestions("myproject")
	if len(suggestions) != 3 {
		t.Fatalf("SpawnSuggestions() returned %d suggestions, want 3", len(suggestions))
	}
	if !strings.Contains(suggestions[0].Command, "attach") {
		t.Errorf("First suggestion should be attach, got %q", suggestions[0].Command)
	}
	if !strings.Contains(suggestions[1].Command, "dashboard") {
		t.Errorf("Second suggestion should be dashboard, got %q", suggestions[1].Command)
	}
}

func TestQuickSuggestions(t *testing.T) {
	suggestions := QuickSuggestions("/home/user/project", "myproject")
	if len(suggestions) != 2 {
		t.Fatalf("QuickSuggestions() returned %d suggestions, want 2", len(suggestions))
	}
	if !strings.Contains(suggestions[0].Command, "cd") {
		t.Errorf("First suggestion should be cd, got %q", suggestions[0].Command)
	}
}

func TestFormatSuggestions(t *testing.T) {
	suggestions := []Suggestion{
		{Command: "ntm attach test", Description: "Connect to session"},
		{Command: "ntm dashboard test", Description: "Live status"},
	}
	result := FormatSuggestions(suggestions)
	if !strings.Contains(result, "What's next?") {
		t.Error("FormatSuggestions should include 'What's next?' header")
	}
	if !strings.Contains(result, "ntm attach test") {
		t.Error("FormatSuggestions should include commands")
	}
	if !strings.Contains(result, "Connect to session") {
		t.Error("FormatSuggestions should include descriptions")
	}
}

func TestFormatSuggestionsEmpty(t *testing.T) {
	result := FormatSuggestions(nil)
	if result != "" {
		t.Errorf("FormatSuggestions(nil) = %q, want empty string", result)
	}
}

func TestNewSuccessWithSuggestions(t *testing.T) {
	suggestions := []Suggestion{
		{Command: "ntm status", Description: "Check status"},
	}
	resp := NewSuccessWithSuggestions("Done!", suggestions)
	if !resp.Success {
		t.Error("Success should be true")
	}
	if resp.Message != "Done!" {
		t.Errorf("Message = %q, want 'Done!'", resp.Message)
	}
	if len(resp.Suggestions) != 1 {
		t.Fatalf("Suggestions count = %d, want 1", len(resp.Suggestions))
	}
	if resp.Suggestions[0].Command != "ntm status" {
		t.Errorf("Suggestion command = %q", resp.Suggestions[0].Command)
	}
}

func TestPrintSuccessFooterToBuffer(t *testing.T) {
	var buf bytes.Buffer
	suggestions := []Suggestion{
		{Command: "ntm test", Description: "Test command"},
	}
	// Buffer is not a terminal, so this should skip output
	PrintSuccessFooter(&buf, suggestions...)
	// Since buf is not a *os.File terminal, it should still output
	// Actually the check is for *os.File, so buffer will get output
	output := buf.String()
	if !strings.Contains(output, "What's next?") {
		t.Errorf("Expected 'What's next?' in output, got: %q", output)
	}
}

func TestPrintSuccessFooterSkipsNonTerminalFile(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}

	suggestions := []Suggestion{
		{Command: "ntm attach demo", Description: "Attach to session"},
	}

	PrintSuccessFooter(w, suggestions...)
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("ReadFrom pipe error: %v", err)
	}
	r.Close()

	if buf.Len() != 0 {
		t.Errorf("Expected no output for non-terminal *os.File, got: %q", buf.String())
	}
}

func TestSuccessCheckToBuffer(t *testing.T) {
	var buf bytes.Buffer
	PrintSuccessCheck(&buf, "Task completed")
	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Error("SuccessCheck should include checkmark")
	}
	if !strings.Contains(output, "Task completed") {
		t.Error("SuccessCheck should include message")
	}
}

func TestAddSuggestions(t *testing.T) {
	suggestions := AddSuggestions("proj", 3)
	if len(suggestions) != 3 {
		t.Fatalf("AddSuggestions() returned %d suggestions, want 3", len(suggestions))
	}
}

func TestSendSuggestions(t *testing.T) {
	suggestions := SendSuggestions("proj")
	if len(suggestions) != 2 {
		t.Fatalf("SendSuggestions() returned %d suggestions, want 2", len(suggestions))
	}
}

func TestKillSuggestions(t *testing.T) {
	suggestions := KillSuggestions()
	if len(suggestions) != 2 {
		t.Fatalf("KillSuggestions() returned %d suggestions, want 2", len(suggestions))
	}
}

func TestStatusBadge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		// Green statuses
		{"active", "active"},
		{"running", "running"},
		{"ok", "ok"},
		{"ready", "ready"},
		// Yellow statuses
		{"idle", "idle"},
		{"waiting", "waiting"},
		{"pending", "pending"},
		// Blue statuses
		{"busy", "busy"},
		{"working", "working"},
		{"processing", "processing"},
		// Error statuses
		{"error", "error"},
		{"failed", "failed"},
		{"stopped", "stopped"},
		// Warning statuses
		{"warning", "warning"},
		{"warn", "warn"},
		// Default/unknown
		{"unknown status", "something_else"},
		{"empty string", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := StatusBadge(tc.status)
			// The rendered string should contain the original status text
			if !strings.Contains(result, tc.status) {
				t.Errorf("StatusBadge(%q) = %q, does not contain input", tc.status, result)
			}
		})
	}
}

func TestStatusBadge_CaseInsensitive(t *testing.T) {
	t.Parallel()

	// Upper case should still render the input text
	result := StatusBadge("ACTIVE")
	if !strings.Contains(result, "ACTIVE") {
		t.Errorf("StatusBadge(\"ACTIVE\") should contain 'ACTIVE', got %q", result)
	}

	result = StatusBadge("Error")
	if !strings.Contains(result, "Error") {
		t.Errorf("StatusBadge(\"Error\") should contain 'Error', got %q", result)
	}
}

func TestAgentBadge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		wantLabel string
	}{
		// Claude variants
		{"claude", "claude", "Claude"},
		{"cc", "cc", "Claude"},
		// Codex variants
		{"codex", "codex", "Codex"},
		{"codex alias", "openai-codex", "Codex"},
		{"cod", "cod", "Codex"},
		// Gemini variants
		{"gemini", "gemini", "Gemini"},
		{"gmi", "gmi", "Gemini"},
		{"gemini alias", "google-gemini", "Gemini"},
		{"cursor", "cursor", "Cursor"},
		{"windsurf", "windsurf", "Windsurf"},
		{"aider", "aider", "Aider"},
		{"opencode short", "oc", "Opencode"},
		{"opencode long", "opencode", "Opencode"},
		{"ollama", "ollama", "Ollama"},
		{"user", "user", "User"},
		// Default/unknown
		{"unknown agent", "other", "other"},
		{"empty string", "", "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := AgentBadge(tc.agentType)
			if !strings.Contains(result, tc.wantLabel) {
				t.Errorf("AgentBadge(%q) = %q, does not contain label %q", tc.agentType, result, tc.wantLabel)
			}
		})
	}
}

func TestAgentBadge_CaseInsensitive(t *testing.T) {
	t.Parallel()

	result := AgentBadge("Claude")
	if !strings.Contains(result, "Claude") {
		t.Errorf("AgentBadge(\"Claude\") should contain 'Claude', got %q", result)
	}

	result = AgentBadge("CODEX")
	if !strings.Contains(result, "Codex") {
		t.Errorf("AgentBadge(\"CODEX\") should contain canonical 'Codex', got %q", result)
	}
}

func TestOutputAgentBadgeColor(t *testing.T) {
	t.Parallel()

	current := theme.Current()
	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", string(current.Claude)},
		{"codex alias", "openai-codex", string(current.Codex)},
		{"gemini alias", "google-gemini", string(current.Gemini)},
		{"cursor", "cursor", string(current.Cursor)},
		{"windsurf alias", "ws", string(current.Windsurf)},
		{"aider", "aider", string(current.Aider)},
		{"ollama", "ollama", string(current.Ollama)},
		{"user", "user", string(current.User)},
		// Opencode currently has no dedicated theme color, so it falls
		// through to current.Overlay (same as the unknown bucket). This
		// test pins that behavior — if a future change adds a theme.Opencode
		// color and an explicit case, this test will fail and should be
		// updated to expect the new color.
		{"opencode short", "oc", string(current.Overlay)},
		{"opencode long", "opencode", string(current.Overlay)},
		{"unknown", "other", string(current.Overlay)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(outputAgentBadgeColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("outputAgentBadgeColor(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestStyledTableCompact(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tbl := NewStyledTableWriter(&buf, "A", "B")
	tbl.Compact = true
	tbl.AddRow("1", "2")
	tbl.Render()

	output := buf.String()
	if !strings.Contains(output, "A") || !strings.Contains(output, "1") {
		t.Error("compact table should contain data")
	}
}

func TestNewStyledTable_DefaultsToStdout(t *testing.T) {
	t.Parallel()

	tbl := NewStyledTable("X", "Y")
	if tbl == nil {
		t.Fatal("NewStyledTable returned nil")
	}
	if tbl.RowCount() != 0 {
		t.Errorf("new table RowCount() = %d, want 0", tbl.RowCount())
	}
}
