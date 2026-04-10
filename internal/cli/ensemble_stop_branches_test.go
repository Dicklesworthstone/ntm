package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

// ---------------------------------------------------------------------------
// renderEnsembleStopOutput — missing branches (JSON, YAML, captured, invalid)
// ---------------------------------------------------------------------------

func TestRenderEnsembleStopOutput_JSON(t *testing.T) {

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "json-session",
		Success:     true,
		FinalStatus: "stopped",
		Stopped:     2,
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "json", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "json-session") {
		t.Error("JSON output should contain session name")
	}
}

func TestRenderEnsembleStopOutput_YAML(t *testing.T) {

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "yaml-session",
		Success:     true,
		FinalStatus: "stopped",
		Stopped:     1,
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "yaml", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "yaml-session") {
		t.Error("YAML output should contain session name")
	}
}

func TestRenderEnsembleStopOutput_YML(t *testing.T) {

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "yml-session",
		Success:     true,
		FinalStatus: "stopped",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "yml", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "yml-session") {
		t.Error("YML output should contain session name")
	}
}

func TestRenderEnsembleStopOutput_TextWithCaptured(t *testing.T) {

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "captured-session",
		Success:     true,
		FinalStatus: "stopped",
		Stopped:     3,
		Captured:    5,
		Message:     "All outputs saved",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "text", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "5 outputs") {
		t.Errorf("expected captured count in output, got:\n%s", out)
	}
	if !strings.Contains(out, "All outputs saved") {
		t.Errorf("expected message in output, got:\n%s", out)
	}
}

func TestRenderEnsembleStopOutput_TextWithError(t *testing.T) {

	payload := ensembleStopOutput{
		GeneratedAt: output.Timestamp(),
		Session:     "error-session",
		Success:     false,
		Error:       "session not found",
		FinalStatus: "unknown",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "text", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Error: session not found") {
		t.Errorf("expected error message in output, got:\n%s", buf.String())
	}
}

func TestRenderEnsembleStopOutput_InvalidFormat(t *testing.T) {

	payload := ensembleStopOutput{
		Session: "test",
	}

	var buf bytes.Buffer
	err := renderEnsembleStopOutput(&buf, payload, "csv", false)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
	if !strings.Contains(err.Error(), "invalid format") {
		t.Errorf("error should mention invalid format, got: %v", err)
	}
}
