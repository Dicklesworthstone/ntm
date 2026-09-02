//go:build integration

package grok

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveACPReadOnlyRoundTrip is an explicitly opted-in, no-write provider
// check. It uses only an existing cached Grok login and never emits the
// prompt, nonce, provider text, or credential material. Normal unit and
// integration runs skip it unless NTM_LIVE_GROK_ACP=1 is set. Exporting
// XAI_API_KEY is deliberately not a substitute for a cached login.
func TestLiveACPReadOnlyRoundTrip(t *testing.T) {
	if os.Getenv("NTM_LIVE_GROK_ACP") != "1" {
		t.Skip("set NTM_LIVE_GROK_ACP=1 for the authenticated no-write ACP check")
	}
	model := strings.TrimSpace(os.Getenv("NTM_LIVE_GROK_MODEL"))
	if model == "" {
		model = "grok-4.6"
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	nonce := "NTM_ACK_" + hex.EncodeToString(random)
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := Run(ctx, OSRunner{}, Request{
		Prompt:        "Do not call any tools. Reply with this exact token and nothing else on one line: " + nonce,
		ExpectedNonce: nonce,
		CWD:           cwd,
		Binary:        "grok",
		Model:         model,
		AutomationPolicyArgs: []string{
			"--sandbox=read-only",
			"--permission-mode=dontAsk",
			"--allow=Read",
			"--deny=Bash(*)",
			"--deny=Edit(*)",
			"--deny=MCPTool(*)",
		},
		PostResponseQuietWindow: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("live ACP check failed with code %q and state %q: %v", result.FailureCode, result.State, err)
	}
	if !result.Success || !result.CompletionConfirmed || !result.AcknowledgementVerified || strings.TrimSpace(result.StopReason) == "" {
		t.Fatalf("live ACP receipt was not authoritative: success=%v completion=%v acknowledgement=%v stop_reason_present=%v", result.Success, result.CompletionConfirmed, result.AcknowledgementVerified, result.StopReason != "")
	}
	// This check does not require an exact model: a global catalog is not a
	// session identity observation. If this CLI emits stronger evidence, it must
	// be one of the two session-scoped forms; catalog-only promotion is invalid.
	if result.ModelEvidence == "provider_catalog_plus_exact_launch" {
		t.Fatalf("catalog-only live transport promoted model identity: model %q", result.Model)
	}
	if result.ModelEvidence != "" && result.ModelEvidence != "completion_metadata" && result.ModelEvidence != "provider_session_notification_plus_exact_launch" {
		t.Fatalf("live ACP returned unknown model evidence %q", result.ModelEvidence)
	}
	if result.ModelEvidence != "" && strings.TrimSpace(result.Model) != model {
		t.Fatalf("session-scoped live model evidence named %q, want exact selected model %q", result.Model, model)
	}
	if entries, err := os.ReadDir(cwd); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		t.Fatalf("no-write ACP check left %d filesystem entries", len(entries))
	}
}
