package robot

// Hermetic E2E tests for send-time CASS injection (C10,
// bd-ws2-wire-or-delete-ykmcz.11). The cass binary is a stub shell script
// printing a checked-in fixture JSON, so these run unconditionally in CI —
// no live cass installation, no index (the E3 lesson: a test that skips when
// cass is absent is not proof). The delivery-path tests use a real tmux pane
// via the same throttled harness as the CM (--with-memory) precedent.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const cassFixtureMarker = "NTM_CASS_FIX_MARKER"

// cassFixtureJSON is the stub `cass search --json` response.
const cassFixtureJSON = `{"query":"rate limiting middleware","total_matches":2,"hits":[` +
	`{"source_path":"/home/user/.cass/sessions/api-work/2026-08-10-session.jsonl","line_number":12,"agent":"claude","content":"` +
	cassFixtureMarker + ` use a token bucket with per-key state","score":0.91},` +
	`{"source_path":"/home/user/.cass/sessions/api-work/2026-08-12-session.jsonl","line_number":40,"agent":"codex","content":"` +
	cassFixtureMarker + ` middleware ordering matters: rate limit before auth logging","score":0.84}]}`

// writeStubCass writes an executable stub cass binary printing payload.
func writeStubCass(t *testing.T, payload string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// CI runs Linux+macOS only (ntm requires tmux); the sh stub is the
		// same pattern the repo's other stub-binary tests use.
		t.Skip("stub cass script requires a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cass")
	script := "#!/bin/sh\ncat <<'NTM_FIXTURE_EOF'\n" + payload + "\nNTM_FIXTURE_EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}
	return path
}

// stubCassConfigs returns engine configs pointed at the stub binary with
// filtering opened up (relevance thresholds are not what these tests prove).
func stubCassConfigs(binPath string) (CASSConfig, FilterConfig, InjectConfig) {
	query := DefaultCASSConfig()
	query.BinaryPath = binPath
	query.Timeout = 10 * time.Second

	filter := DefaultFilterConfig()
	filter.MinRelevance = 0
	filter.MaxAgeDays = 0

	inject := DefaultInjectConfig()
	return query, filter, inject
}

// TestInjectContextFromQuery_StubCassE2E proves the wired engine end to end:
// stub cass hits are queried, filtered, formatted, and PREPENDED to the
// prompt, and the cass_injection envelope block reports the injection.
func TestInjectContextFromQuery_StubCassE2E(t *testing.T) {
	stub := writeStubCass(t, cassFixtureJSON)
	query, filter, inject := stubCassConfigs(stub)

	prompt := "Implement rate limiting middleware for the API gateway"
	injectRes, queryRes, filterRes := InjectContextFromQuery(prompt, query, filter, inject)

	if !queryRes.Success {
		t.Fatalf("query failed: %s", queryRes.Error)
	}
	if len(queryRes.Hits) != 2 {
		t.Fatalf("hits = %d, want 2 from fixture", len(queryRes.Hits))
	}
	if !injectRes.Success {
		t.Fatalf("injection failed: %s", injectRes.Error)
	}
	if injectRes.Metadata.ItemsInjected == 0 {
		t.Fatal("items_injected = 0, want > 0")
	}
	if !strings.Contains(injectRes.ModifiedPrompt, cassFixtureMarker) {
		t.Fatalf("modified prompt missing injected fixture content: %q", injectRes.ModifiedPrompt)
	}
	if !strings.Contains(injectRes.ModifiedPrompt, prompt) {
		t.Fatal("modified prompt lost the original message")
	}
	if strings.Index(injectRes.ModifiedPrompt, cassFixtureMarker) > strings.Index(injectRes.ModifiedPrompt, prompt) {
		t.Fatal("injected context should be PREPENDED above the original message")
	}

	// Envelope block: populated, marshaled under cass_injection.
	info := NewCASSInjectionInfo(injectRes, queryRes.Query, filterRes.Hits)
	if !info.Enabled || info.ItemsInjected == 0 || info.SkippedReason != "" {
		t.Fatalf("envelope block wrong: %+v", info)
	}
	raw, err := json.Marshal(SendOutput{CASSInjection: info})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if _, ok := envelope["cass_injection"]; !ok {
		t.Fatalf("envelope missing cass_injection block: %s", raw)
	}
}

// TestInjectContextFromQuery_CassUnavailable proves the degraded path at the
// engine+envelope level: cass missing entirely leaves a recorded skip and no
// prompt modification — enrichment, never a gate.
func TestInjectContextFromQuery_CassUnavailable(t *testing.T) {
	query, filter, inject := stubCassConfigs(filepath.Join(t.TempDir(), "no-such-cass"))

	injectRes, queryRes, _ := InjectContextFromQuery("Implement rate limiting middleware", query, filter, inject)
	if queryRes.Success {
		t.Fatal("query should fail when the cass binary is absent")
	}
	if injectRes.Success {
		t.Fatal("injection should not report success when cass is absent")
	}

	info := NewCASSInjectionInfo(injectRes, queryRes.Query, nil)
	if info.SkippedReason == "" {
		t.Fatalf("degraded envelope must record a skip reason: %+v", info)
	}
	if !strings.Contains(info.SkippedReason, "not found") {
		t.Fatalf("skip reason %q should name the missing cass binary", info.SkippedReason)
	}
}

// TestGetSendWithCASSDeliversInjectedContextRealTmux proves the H1 doc
// example true end to end: a --with-cass send delivers the injected context
// block ABOVE the message to a real pane and the send envelope carries the
// populated cass_injection block. Hermetic against cass via the stub binary.
func TestGetSendWithCASSDeliversInjectedContextRealTmux(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	stub := writeStubCass(t, cassFixtureJSON)
	query, filter, inject := stubCassConfigs(stub)

	const baseMarker = "NTM_CASS_BASE_MSG"
	session := "ntm-send-cass-inject"
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		if err := tmux.KillSession(session); err != nil {
			t.Errorf("kill tmux session: %v", err)
		}
	})
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) != 1 {
		t.Fatalf("get tmux panes: %v (n=%d)", err, len(panes))
	}

	output, err := GetSend(SendOptions{
		Session:      session,
		Pane:         panes[0].ID,
		Message:      "printf '" + baseMarker + " rate limiting middleware\\n'",
		WithCASS:     true,
		CASSConfig:   &query,
		FilterConfig: &filter,
		InjectConfig: &inject,
	})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if !output.Success {
		t.Fatalf("with-cass send failed: %+v", output)
	}
	if output.CASSInjection == nil {
		t.Fatal("cass_injection missing from send envelope")
	}
	if output.CASSInjection.SkippedReason != "" {
		t.Fatalf("injection skipped: %q", output.CASSInjection.SkippedReason)
	}
	if output.CASSInjection.ItemsInjected == 0 {
		t.Fatalf("items_injected = 0 in envelope: %+v", output.CASSInjection)
	}

	deadline := time.Now().Add(testutil.ScaleTimeout(5 * time.Second))
	var captured string
	for {
		captured, err = tmux.CapturePaneVisible(panes[0].ID)
		if err == nil && strings.Contains(captured, cassFixtureMarker) && strings.Contains(captured, baseMarker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivered payload missing injected context or message (err=%v): %q", err, captured)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Index(captured, cassFixtureMarker) > strings.Index(captured, baseMarker) {
		t.Errorf("injected context should precede the original message: %q", captured)
	}
}

// TestGetSendWithCASSDegradedStillSendsRealTmux proves the send-level
// degraded posture: cass down at send time -> the send still SUCCEEDS with a
// recorded skip in the envelope and the unmodified message delivered.
func TestGetSendWithCASSDegradedStillSendsRealTmux(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	query, filter, inject := stubCassConfigs(filepath.Join(t.TempDir(), "no-such-cass"))

	const baseMarker = "NTM_CASS_DEGRADED_MSG"
	session := "ntm-send-cass-degraded"
	if err := tmux.CreateSession(session, ""); err != nil {
		t.Fatalf("create tmux session: %v", err)
	}
	t.Cleanup(func() {
		if err := tmux.KillSession(session); err != nil {
			t.Errorf("kill tmux session: %v", err)
		}
	})
	panes, err := tmux.GetPanes(session)
	if err != nil || len(panes) != 1 {
		t.Fatalf("get tmux panes: %v (n=%d)", err, len(panes))
	}

	output, err := GetSend(SendOptions{
		Session:      session,
		Pane:         panes[0].ID,
		Message:      "printf '" + baseMarker + " still delivered\\n'",
		WithCASS:     true,
		CASSConfig:   &query,
		FilterConfig: &filter,
		InjectConfig: &inject,
	})
	if err != nil {
		t.Fatalf("GetSend: %v", err)
	}
	if !output.Success {
		t.Fatalf("degraded with-cass send must still succeed: %+v", output)
	}
	if output.CASSInjection == nil || output.CASSInjection.SkippedReason == "" {
		t.Fatalf("degraded envelope must record the skip: %+v", output.CASSInjection)
	}

	deadline := time.Now().Add(testutil.ScaleTimeout(5 * time.Second))
	for {
		captured, capErr := tmux.CapturePaneVisible(panes[0].ID)
		if capErr == nil && strings.Contains(captured, baseMarker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("original message was not delivered on the degraded path (err=%v): %q", capErr, captured)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
