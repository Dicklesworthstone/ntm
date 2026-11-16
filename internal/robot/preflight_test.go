package robot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func TestPreflight_CleanPrompt(t *testing.T) {
	prompt := "Implement unit tests for session health reporting in internal/robot."
	out, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
		Strict: false,
	})
	if err != nil {
		t.Fatalf("GetPreflight(clean): %v", err)
	}
	if !out.Success {
		t.Fatalf("Success = false, want true; error=%q code=%q", out.Error, out.ErrorCode)
	}
	if out.SchemaID != preflightSchemaID || out.SchemaVersion != preflightSchemaVersion {
		t.Fatalf("schema = (%q, %q), want (%q, %q)", out.SchemaID, out.SchemaVersion, preflightSchemaID, preflightSchemaVersion)
	}
	if len(out.PreviewHash) != 16 {
		t.Fatalf("PreviewHash = %q (len %d), want 16 hex chars", out.PreviewHash, len(out.PreviewHash))
	}
	if out.PreviewLen != len(prompt) {
		t.Fatalf("PreviewLen = %d, want %d", out.PreviewLen, len(prompt))
	}
	if out.EstimatedTokens <= 0 {
		t.Fatalf("EstimatedTokens = %d, want > 0", out.EstimatedTokens)
	}
	if out.Findings == nil || len(out.Findings) != 0 {
		t.Fatalf("Findings = %#v, want non-nil empty slice", out.Findings)
	}
	if out.ErrorCount != 0 || out.WarningCount != 0 || out.InfoCount != 0 {
		t.Fatalf("counts = (%d, %d, %d), want (0, 0, 0)", out.ErrorCount, out.WarningCount, out.InfoCount)
	}
	if out.Preview != prompt {
		t.Fatalf("Preview = %q, want %q", out.Preview, prompt)
	}
	if out.Strict {
		t.Fatalf("Strict = true, want false")
	}
	if out.AgentHints == nil || !strings.Contains(out.AgentHints.Summary, "Preflight passed") {
		t.Fatalf("AgentHints = %+v, want non-nil passed summary", out.AgentHints)
	}
}

func TestPreflight_SecretModesAndRedaction(t *testing.T) {
	fakeOpenAIKey := "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	prompt := "Use API token " + fakeOpenAIKey + " to authenticate."

	// 1. Default lint rules (nil Config): secrets block by default.
	defaultOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
	})
	if err != nil {
		t.Fatalf("GetPreflight(nil config): %v", err)
	}
	if defaultOut.Success || defaultOut.ErrorCode != "PREFLIGHT_BLOCKED" || defaultOut.ErrorCount == 0 {
		t.Fatalf("nil config secret preflight = %+v, want blocked PREFLIGHT_BLOCKED", defaultOut)
	}

	// 2. Redaction mode "warn": warns without blocking, raw preview preserved.
	warnCfg := config.Default()
	warnCfg.Redaction.Mode = "warn"
	warnOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
		Config: warnCfg,
	})
	if err != nil {
		t.Fatalf("GetPreflight(warn): %v", err)
	}
	if !warnOut.Success || warnOut.WarningCount == 0 {
		t.Fatalf("warn mode preflight = %+v, want success=true with warning", warnOut)
	}
	if !strings.Contains(warnOut.Preview, fakeOpenAIKey) {
		t.Fatalf("warn mode preview = %q, expected raw key in warn mode", warnOut.Preview)
	}

	// 3. Redaction mode "redact": warns without blocking, preview is redacted.
	redactCfg := config.Default()
	redactCfg.Redaction.Mode = "redact"
	redactOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
		Config: redactCfg,
	})
	if err != nil {
		t.Fatalf("GetPreflight(redact): %v", err)
	}
	if !redactOut.Success || redactOut.WarningCount == 0 {
		t.Fatalf("redact mode preflight = %+v, want success=true with warning", redactOut)
	}
	if !strings.Contains(redactOut.Preview, "[REDACTED:") || strings.Contains(redactOut.Preview, fakeOpenAIKey) {
		t.Fatalf("redact mode preview = %q, want redacted token without raw secret", redactOut.Preview)
	}

	// 4. Redaction mode "block": blocks AND redacts preview so raw secret is not leaked.
	blockCfg := config.Default()
	blockCfg.Redaction.Mode = "block"
	blockOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
		Config: blockCfg,
	})
	if err != nil {
		t.Fatalf("GetPreflight(block): %v", err)
	}
	if blockOut.Success || blockOut.ErrorCode != "PREFLIGHT_BLOCKED" || blockOut.ErrorCount == 0 {
		t.Fatalf("block mode preflight = %+v, want success=false PREFLIGHT_BLOCKED", blockOut)
	}
	if !strings.Contains(blockOut.Preview, "[REDACTED:") || strings.Contains(blockOut.Preview, fakeOpenAIKey) {
		t.Fatalf("block mode preview = %q, want redacted token without raw secret", blockOut.Preview)
	}

	// 5. Redaction mode "off": disables secret_detected completely.
	offCfg := config.Default()
	offCfg.Redaction.Mode = "off"
	offOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: prompt,
		Config: offCfg,
	})
	if err != nil {
		t.Fatalf("GetPreflight(off): %v", err)
	}
	if !offOut.Success {
		t.Fatalf("off mode preflight = %+v, want success=true", offOut)
	}
	for _, f := range offOut.Findings {
		if f.ID == "secret_detected" {
			t.Fatalf("off mode unexpectedly emitted secret_detected finding: %+v", f)
		}
	}
}

func TestPreflight_StrictModeAndPreviewTruncation(t *testing.T) {
	destructive := "Execute rm -rf /tmp/critical_data immediately"

	normalOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: destructive,
		Strict: false,
	})
	if err != nil {
		t.Fatalf("GetPreflight(normal): %v", err)
	}
	if !normalOut.Success || normalOut.WarningCount == 0 {
		t.Fatalf("normal destructive preflight = %+v, want success=true with warning", normalOut)
	}

	strictOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: destructive,
		Strict: true,
	})
	if err != nil {
		t.Fatalf("GetPreflight(strict): %v", err)
	}
	if strictOut.Success || !strictOut.Strict || strictOut.ErrorCode != "PREFLIGHT_BLOCKED" || strictOut.ErrorCount == 0 {
		t.Fatalf("strict destructive preflight = %+v, want blocked strict error", strictOut)
	}

	longPrompt := strings.Repeat("safe token ", 80) // 880 bytes
	truncOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: longPrompt,
	})
	if err != nil {
		t.Fatalf("GetPreflight(long): %v", err)
	}
	if truncOut.PreviewLen != len(longPrompt) {
		t.Fatalf("PreviewLen = %d, want %d", truncOut.PreviewLen, len(longPrompt))
	}
	if len(truncOut.Preview) != maxPreflightPreviewLen+3 || !strings.HasSuffix(truncOut.Preview, "...") {
		t.Fatalf("Preview len = %d, want %d ending in '...'", len(truncOut.Preview), maxPreflightPreviewLen+3)
	}
}

func TestPreflight_EmptyPromptAndCancelledContext(t *testing.T) {
	emptyOut, err := GetPreflight(context.Background(), PreflightOptions{
		Prompt: "   \t\n  ",
		Strict: true,
	})
	if err != nil {
		t.Fatalf("GetPreflight(empty): %v", err)
	}
	if emptyOut.Success || emptyOut.ErrorCode != ErrCodeInvalidFlag {
		t.Fatalf("empty prompt preflight = %+v, want INVALID_FLAG", emptyOut)
	}
	if emptyOut.Findings == nil {
		t.Fatal("Findings must be non-nil on INVALID_FLAG error output")
	}
	rawJSON, err := json.Marshal(emptyOut)
	if err != nil {
		t.Fatalf("marshal emptyOut: %v", err)
	}
	if !strings.Contains(string(rawJSON), `"findings":[]`) {
		t.Fatalf("expected non-null findings array in JSON, got %s", string(rawJSON))
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledOut, err := GetPreflight(cancelledCtx, PreflightOptions{Prompt: "hello"})
	if err != nil {
		t.Fatalf("GetPreflight(cancelled): %v", err)
	}
	if cancelledOut.Success || cancelledOut.ErrorCode != ErrCodeTimeout || cancelledOut.Findings == nil {
		t.Fatalf("cancelled preflight = %+v, want TIMEOUT with non-nil Findings", cancelledOut)
	}
}

func TestPreflight_RegistryAndSchemaContract(t *testing.T) {
	surface, ok := GetRobotRegistry().Surface("preflight")
	if !ok {
		t.Fatal("expected 'preflight' surface in robot registry")
	}
	if surface.SchemaID != preflightSchemaID || surface.SchemaType != "preflight" || surface.Flag != "--robot-preflight" {
		t.Fatalf("unexpected preflight surface metadata: %+v", surface)
	}
	schemaOut, err := GetSchema("preflight")
	if err != nil || schemaOut == nil || !schemaOut.Success {
		t.Fatalf("GetSchema(preflight) = (%+v, %v), want valid schema", schemaOut, err)
	}
}
