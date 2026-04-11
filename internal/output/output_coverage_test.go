package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// mockResult implements the Result interface for testing Formatter.Output.
type mockResult struct {
	textOut string
	textErr error
	jsonOut interface{}
}

func (m *mockResult) Text(w io.Writer) error {
	if m.textErr != nil {
		return m.textErr
	}
	_, err := fmt.Fprint(w, m.textOut)
	return err
}
func (m *mockResult) JSON() interface{} { return m.jsonOut }

// ---------------------------------------------------------------------------
// Formatter.Output
// ---------------------------------------------------------------------------

func TestFormatterOutput_JSONMode(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithJSON(true), WithWriter(&buf))

	r := &mockResult{jsonOut: map[string]string{"status": "ok"}}
	if err := f.Output(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	if decoded["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", decoded["status"])
	}
}

func TestFormatterOutput_TextMode(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithWriter(&buf))

	r := &mockResult{textOut: "hello world"}
	if err := f.Output(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if buf.String() != "hello world" {
		t.Errorf("expected 'hello world', got %q", buf.String())
	}
}

func TestFormatterOutput_TextError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithWriter(&buf))

	r := &mockResult{textErr: fmt.Errorf("render failed")}
	err := f.Output(r)
	if err == nil || err.Error() != "render failed" {
		t.Errorf("expected 'render failed' error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Formatter.ErrorWithHint (JSON branch only; text branch writes to stderr)
// ---------------------------------------------------------------------------

func TestFormatterErrorWithHint_JSONMode(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithJSON(true), WithWriter(&buf))

	err := f.ErrorWithHint("something broke", "try again later")
	// JSON mode returns the result of f.JSON(), which is nil on success
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]interface{}
	if jsonErr := json.Unmarshal(buf.Bytes(), &decoded); jsonErr != nil {
		t.Fatalf("invalid JSON: %v", jsonErr)
	}

	if decoded["error"] != "something broke" {
		t.Errorf("expected error='something broke', got %v", decoded["error"])
	}
	if decoded["hint"] != "try again later" {
		t.Errorf("expected hint='try again later', got %v", decoded["hint"])
	}
}

// ---------------------------------------------------------------------------
// Formatter.Print (text.go:31)
// ---------------------------------------------------------------------------

func TestFormatterPrint(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithWriter(&buf))

	f.Print("alpha", " ", "beta")

	if buf.String() != "alpha beta" {
		t.Errorf("expected 'alpha beta', got %q", buf.String())
	}
}

func TestFormatterPrint_Empty(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithWriter(&buf))

	f.Print()

	if buf.Len() != 0 {
		t.Errorf("expected empty output, got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// ProgressMsg format helpers: Warningf, Errorf, Infof, Printf
// ---------------------------------------------------------------------------

func TestProgressMsg_Warningf(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf)

	p.Warningf("found %d issues in %s", 3, "main.go")

	out := buf.String()
	if !strings.Contains(out, "⚠") {
		t.Error("expected warning icon ⚠")
	}
	if !strings.Contains(out, "found 3 issues in main.go") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

func TestProgressMsg_Errorf(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf)

	p.Errorf("failed after %d retries", 5)

	out := buf.String()
	if !strings.Contains(out, "✗") {
		t.Error("expected error icon ✗")
	}
	if !strings.Contains(out, "failed after 5 retries") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

func TestProgressMsg_Infof(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf)

	p.Infof("processing %s (%d/%d)", "item", 2, 10)

	out := buf.String()
	if !strings.Contains(out, "ℹ") {
		t.Error("expected info icon ℹ")
	}
	if !strings.Contains(out, "processing item (2/10)") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

func TestProgressMsg_Printf(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf)

	p.Printf("step %d of %d", 3, 7)

	out := buf.String()
	if !strings.Contains(out, "step 3 of 7") {
		t.Errorf("expected formatted message, got %q", out)
	}
	// Printf should NOT have an icon
	if strings.Contains(out, "✓") || strings.Contains(out, "✗") || strings.Contains(out, "⚠") || strings.Contains(out, "ℹ") {
		t.Errorf("Printf should not include an icon, got %q", out)
	}
}

func TestProgressMsg_Printf_NoArgs(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf)

	p.Printf("plain message")

	if !strings.Contains(buf.String(), "plain message") {
		t.Errorf("expected 'plain message', got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// ProgressMsg format helpers with indent
// ---------------------------------------------------------------------------

func TestProgressMsg_WarningfWithIndent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf).SetIndent("  ")

	p.Warningf("warn %d", 1)

	if !strings.HasPrefix(buf.String(), "  ") {
		t.Errorf("expected indented output, got %q", buf.String())
	}
}

func TestProgressMsg_ErrorfWithIndent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	p := ProgressWriter(&buf).SetIndent(">> ")

	p.Errorf("err %s", "timeout")

	if !strings.HasPrefix(buf.String(), ">> ") {
		t.Errorf("expected indented output, got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Operation.Start and Operation.Summary (captures stdout)
// ---------------------------------------------------------------------------

func TestOperationStart(t *testing.T) {
	t.Parallel()

	op := NewOperation("Test")
	steps := op.Start("step-1")

	if steps == nil {
		t.Fatal("expected non-nil Steps from Operation.Start")
	}
}

func TestOperationStart_MultipleSteps(t *testing.T) {
	t.Parallel()

	op := NewOperation("Build")

	s1 := op.Start("compile")
	s1.Done()

	s2 := op.Start("link")
	s2.Done()

	// Verify no panics and operation state is clean
	if op.HasErrors() {
		t.Error("expected no errors")
	}
}

// ---------------------------------------------------------------------------
// PrintSuccessCheck with non-terminal writer (covers non-color branch)
// ---------------------------------------------------------------------------

func TestPrintSuccessCheck_NonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	PrintSuccessCheck(&buf, "All tests passed")

	out := buf.String()
	if !strings.Contains(out, "✓") {
		t.Error("expected checkmark ✓")
	}
	if !strings.Contains(out, "All tests passed") {
		t.Errorf("expected message text, got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("expected trailing newline")
	}
}

func TestPrintSuccessCheck_EmptyMessage(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	PrintSuccessCheck(&buf, "")

	out := buf.String()
	if !strings.Contains(out, "✓") {
		t.Error("expected checkmark even for empty message")
	}
}

// ---------------------------------------------------------------------------
// PrintSuccessFooter edge cases
// ---------------------------------------------------------------------------

func TestPrintSuccessFooter_EmptySuggestions(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	PrintSuccessFooter(&buf)

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty suggestions, got %q", buf.String())
	}
}

func TestPrintSuccessFooter_NonTerminalBuffer(t *testing.T) {
	t.Parallel()

	// A bytes.Buffer is not an *os.File, so the terminal check is skipped
	// and output is written (non-color path)
	var buf bytes.Buffer
	PrintSuccessFooter(&buf, Suggestion{
		Command:     "ntm status myproj",
		Description: "Check status",
	})

	out := buf.String()
	if !strings.Contains(out, "What's next?") {
		t.Errorf("expected 'What's next?' header, got %q", out)
	}
	if !strings.Contains(out, "ntm status myproj") {
		t.Errorf("expected command in output, got %q", out)
	}
	if !strings.Contains(out, "# Check status") {
		t.Errorf("expected description in output, got %q", out)
	}
}

func TestPrintSuccessFooter_MultipleSuggestions(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	PrintSuccessFooter(&buf,
		Suggestion{Command: "cmd1", Description: "desc1"},
		Suggestion{Command: "cmd2", Description: "desc2"},
		Suggestion{Command: "cmd3", Description: "desc3"},
	)

	out := buf.String()
	if strings.Count(out, "# desc") != 3 {
		t.Errorf("expected 3 suggestions, got output: %q", out)
	}
}

// ---------------------------------------------------------------------------
// FormatCLIError plain-text branch coverage (non-terminal always hits this)
// ---------------------------------------------------------------------------

func TestFormatCLIError_MessageOnly(t *testing.T) {
	t.Parallel()

	e := NewCLIError("connection refused")
	out := FormatCLIError(e)

	if !strings.Contains(out, "Error: connection refused") {
		t.Errorf("expected error message, got %q", out)
	}
	if strings.Contains(out, "Cause:") {
		t.Error("did not expect Cause line")
	}
	if strings.Contains(out, "Hint:") {
		t.Error("did not expect Hint line")
	}
}

func TestFormatCLIError_WithCode(t *testing.T) {
	t.Parallel()

	e := NewCLIError("bad request").WithCode("E400")
	out := FormatCLIError(e)

	if !strings.Contains(out, "[E400]") {
		t.Errorf("expected error code, got %q", out)
	}
}

func TestFormatCLIError_WithCauseAndHint(t *testing.T) {
	t.Parallel()

	e := NewCLIError("deploy failed").
		WithCause("network timeout").
		WithHint("check VPN connection")
	out := FormatCLIError(e)

	if !strings.Contains(out, "Cause: network timeout") {
		t.Errorf("expected cause line, got %q", out)
	}
	if !strings.Contains(out, "Hint: check VPN connection") {
		t.Errorf("expected hint line, got %q", out)
	}
}

func TestFormatCLIError_AllFields(t *testing.T) {
	t.Parallel()

	e := NewCLIError("auth failed").
		WithCode("E401").
		WithCause("token expired").
		WithHint("refresh token with 'ntm auth refresh'")
	out := FormatCLIError(e)

	if !strings.Contains(out, "Error: auth failed") {
		t.Errorf("missing error message, got %q", out)
	}
	if !strings.Contains(out, "[E401]") {
		t.Errorf("missing error code, got %q", out)
	}
	if !strings.Contains(out, "Cause: token expired") {
		t.Errorf("missing cause, got %q", out)
	}
	if !strings.Contains(out, "Hint: refresh token") {
		t.Errorf("missing hint, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Formatter.Output with complex JSON data
// ---------------------------------------------------------------------------

func TestFormatterOutput_JSONComplex(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	f := New(WithJSON(true), WithWriter(&buf))

	r := &mockResult{jsonOut: map[string]interface{}{
		"items": []string{"a", "b"},
		"count": 2,
	}}
	if err := f.Output(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	items, ok := decoded["items"].([]interface{})
	if !ok || len(items) != 2 {
		t.Errorf("expected 2 items, got %v", decoded["items"])
	}
}

// ---------------------------------------------------------------------------
// Convenience function: Progress() returns non-nil
// ---------------------------------------------------------------------------

func TestProgress_ReturnsNonNil(t *testing.T) {
	t.Parallel()

	p := Progress()
	if p == nil {
		t.Fatal("Progress() returned nil")
	}
}

// ---------------------------------------------------------------------------
// SuccessCheck convenience (just verify no panic)
// ---------------------------------------------------------------------------

func TestSuccessCheck_NoPanic(t *testing.T) {
	// SuccessCheck writes to os.Stdout; keep this test serial so it does not
	// race with stdout-capturing tests in this package.
	SuccessCheck("test message")
}

// ---------------------------------------------------------------------------
// captureStdout helper for testing stdout-writing functions
// ---------------------------------------------------------------------------

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	orig := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// ---------------------------------------------------------------------------
// Convenience functions: PrintSuccess, PrintSuccessf, etc.
// ---------------------------------------------------------------------------

func TestPrintSuccess_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintSuccess("deployment complete")
	})

	if !strings.Contains(out, "✓") {
		t.Error("expected success icon ✓")
	}
	if !strings.Contains(out, "deployment complete") {
		t.Errorf("expected message text, got %q", out)
	}
}

func TestPrintSuccessf_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintSuccessf("created %d files", 42)
	})

	if !strings.Contains(out, "created 42 files") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

func TestPrintWarning_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintWarning("disk almost full")
	})

	if !strings.Contains(out, "⚠") {
		t.Error("expected warning icon ⚠")
	}
	if !strings.Contains(out, "disk almost full") {
		t.Errorf("expected message text, got %q", out)
	}
}

func TestPrintWarningf_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintWarningf("usage at %d%%", 95)
	})

	if !strings.Contains(out, "usage at 95%") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

func TestPrintInfo_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintInfo("checking status")
	})

	if !strings.Contains(out, "ℹ") {
		t.Error("expected info icon ℹ")
	}
	if !strings.Contains(out, "checking status") {
		t.Errorf("expected message text, got %q", out)
	}
}

func TestPrintInfof_Stdout(t *testing.T) {
	out := captureStdout(t, func() {
		PrintInfof("found %d sessions", 3)
	})

	if !strings.Contains(out, "found 3 sessions") {
		t.Errorf("expected formatted message, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Operation.Summary (captures stdout)
// ---------------------------------------------------------------------------

func TestOperationSummary_Success(t *testing.T) {
	op := NewOperation("Build")

	out := captureStdout(t, func() {
		op.Summary()
	})

	if !strings.Contains(out, "✓") {
		t.Error("expected success icon for clean operation")
	}
	if !strings.Contains(out, "Build completed successfully") {
		t.Errorf("expected success message, got %q", out)
	}
}

func TestOperationSummary_WithErrors(t *testing.T) {
	op := NewOperation("Deploy")
	op.AddError("connection timeout")
	op.AddError("auth failed")

	out := captureStdout(t, func() {
		op.Summary()
	})

	if !strings.Contains(out, "✗") {
		t.Error("expected error icon")
	}
	if !strings.Contains(out, "Deploy completed with 2 error(s)") {
		t.Errorf("expected error summary, got %q", out)
	}
	if !strings.Contains(out, "connection timeout") {
		t.Errorf("expected error detail, got %q", out)
	}
}

func TestOperationSummary_WithWarnings(t *testing.T) {
	op := NewOperation("Setup")
	op.AddWarning("deprecated config format")

	out := captureStdout(t, func() {
		op.Summary()
	})

	if !strings.Contains(out, "⚠") {
		t.Error("expected warning icon")
	}
	if !strings.Contains(out, "Setup completed with 1 warning(s)") {
		t.Errorf("expected warning summary, got %q", out)
	}
	if !strings.Contains(out, "deprecated config format") {
		t.Errorf("expected warning detail, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// SuccessFooter convenience (stdout wrapper)
// ---------------------------------------------------------------------------

func TestSuccessFooter_Stdout(t *testing.T) {
	// SuccessFooter calls PrintSuccessFooter(os.Stdout, ...) which checks
	// if stdout is a terminal. In test, stdout is a pipe so this goes
	// through the non-terminal code path. With our pipe capture, the
	// writer is *os.File so the terminal check runs and skips output.
	// We just verify no panic.
	out := captureStdout(t, func() {
		SuccessFooter(Suggestion{Command: "test", Description: "test"})
	})
	// Output may be empty (pipe detected as non-terminal) or contain text
	_ = out
}
