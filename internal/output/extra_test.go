package output

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
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

	if isStdoutTerminal() {
		t.Error("isStdoutTerminal() should be false for pipe stdout")
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
