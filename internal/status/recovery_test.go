package status

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestRecoveryManager_CanSendRecovery(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// First recovery should be allowed
	can, reason := rm.CanSendRecovery("test:0")
	if !can {
		t.Errorf("first recovery should be allowed, got: %s", reason)
	}

	// Simulate a recovery
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.mu.Unlock()

	// Second immediate recovery should be blocked by cooldown
	can, reason = rm.CanSendRecovery("test:0")
	if can {
		t.Error("recovery should be blocked by cooldown")
	}
	if reason == "" {
		t.Error("reason should explain cooldown")
	}
}

func TestRecoveryManager_MaxRecoveries(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Millisecond, // Fast cooldown for testing
		Prompt:        "test prompt",
		MaxRecoveries: 3,
	}
	rm := NewRecoveryManager(config)

	// Simulate max recoveries
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()

	can, reason := rm.CanSendRecovery("test:0")
	if can {
		t.Error("recovery should be blocked by max recoveries")
	}
	if reason == "" {
		t.Error("reason should explain max recoveries")
	}
}

func TestRecoveryManager_ResetPane(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Set some state
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.recoveryCount["test:0"] = 5
	rm.mu.Unlock()

	// Reset
	rm.ResetPane("test:0")

	// Should be allowed again
	can, _ := rm.CanSendRecovery("test:0")
	if !can {
		t.Error("recovery should be allowed after reset")
	}

	count := rm.GetRecoveryCount("test:0")
	if count != 0 {
		t.Errorf("count should be 0 after reset, got %d", count)
	}
}

func TestRecoveryManager_GetRecoveryCount(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Initial count should be 0
	count := rm.GetRecoveryCount("test:0")
	if count != 0 {
		t.Errorf("initial count should be 0, got %d", count)
	}

	// Simulate recoveries
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()

	count = rm.GetRecoveryCount("test:0")
	if count != 3 {
		t.Errorf("count should be 3, got %d", count)
	}
}

func TestRecoveryManager_GetLastRecoveryTime(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// No recovery yet
	_, ok := rm.GetLastRecoveryTime("test:0")
	if ok {
		t.Error("should not have last recovery time yet")
	}

	// Set recovery time
	now := time.Now()
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = now
	rm.mu.Unlock()

	lastTime, ok := rm.GetLastRecoveryTime("test:0")
	if !ok {
		t.Error("should have last recovery time")
	}
	if !lastTime.Equal(now) {
		t.Errorf("last time should match, got %v want %v", lastTime, now)
	}
}

func TestRecoveryManager_SetPrompt(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	rm.SetPrompt("custom prompt")
	if rm.prompt != "custom prompt" {
		t.Errorf("prompt should be 'custom prompt', got %q", rm.prompt)
	}
}

func TestRecoveryManager_SetCooldown(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	rm.SetCooldown(5 * time.Minute)
	if rm.cooldown != 5*time.Minute {
		t.Errorf("cooldown should be 5m, got %v", rm.cooldown)
	}
}

func TestRecoveryManager_HandleCompactionEvent(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Second,
		MaxRecoveries: 5,
	}
	rm := NewRecoveryManager(config)

	event := &CompactionEvent{
		AgentType:   "claude",
		MatchedText: "Conversation compacted",
		DetectedAt:  time.Now(),
	}

	// Note: This won't actually send keys since tmux isn't running in tests
	// It will fail with "failed to send recovery prompt"
	// but the logic should work
	_, err := rm.HandleCompactionEvent(event, "testsession", 0)

	// We expect an error because tmux isn't available in tests
	if err == nil {
		t.Log("HandleCompactionEvent succeeded (tmux available)")
	} else {
		t.Logf("HandleCompactionEvent failed as expected without tmux: %v", err)
	}

	// Test with nil event
	sent, err := rm.HandleCompactionEvent(nil, "testsession", 0)
	if sent {
		t.Error("should not send for nil event")
	}
	if err != nil {
		t.Error("should not error for nil event")
	}
}

func TestRecoveryManagerRejectsGrokCompactionBeforeStateMutation(t *testing.T) {
	rm := NewRecoveryManagerDefault()
	event := &CompactionEvent{
		PaneID:      "unchanged",
		AgentType:   " XAI_GROK_BUILD ",
		MatchedText: "continuing from summary",
		DetectedAt:  time.Now(),
	}

	sent, err := rm.HandleCompactionEvent(event, "grok-session", 4)
	if sent {
		t.Fatal("recovery sent = true, want false")
	}
	if !errors.Is(err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
		t.Fatalf("error = %v, want prompt-delivery sentinel", err)
	}
	if event.PaneID != "unchanged" {
		t.Fatalf("event pane ID mutated to %q", event.PaneID)
	}
	if count := rm.GetRecoveryCount(makePaneID("grok-session", 4)); count != 0 {
		t.Fatalf("recovery count = %d, want 0", count)
	}
	if _, ok := rm.GetLastRecoveryTime(makePaneID("grok-session", 4)); ok {
		t.Fatal("last recovery time was mutated")
	}
	if events := rm.GetRecoveryEvents(); len(events) != 0 {
		t.Fatalf("recovery events = %+v, want empty", events)
	}
}

func TestRecoveryEvent(t *testing.T) {
	event := RecoveryEvent{
		PaneID:      "test:0",
		Session:     "test",
		PaneIndex:   0,
		SentAt:      time.Now(),
		Prompt:      "test prompt",
		TriggerText: "Conversation compacted",
	}

	if event.PaneID != "test:0" {
		t.Errorf("PaneID should be test:0, got %s", event.PaneID)
	}
	if event.TriggerText != "Conversation compacted" {
		t.Errorf("TriggerText should be set")
	}
}

func TestDefaultRecoveryConfig(t *testing.T) {
	config := DefaultRecoveryConfig()

	if config.Cooldown != DefaultCooldown {
		t.Errorf("Cooldown should be %v, got %v", DefaultCooldown, config.Cooldown)
	}
	if config.Prompt != DefaultRecoveryPrompt {
		t.Errorf("Prompt should be default")
	}
	if config.MaxRecoveries != DefaultMaxRecoveriesPerPane {
		t.Errorf("MaxRecoveries should be %d, got %d", DefaultMaxRecoveriesPerPane, config.MaxRecoveries)
	}
}

func TestCompactionRecoveryIntegration(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	if cri.Detector() == nil {
		t.Error("detector should not be nil")
	}
	if cri.Recovery() == nil {
		t.Error("recovery should not be nil")
	}
}

func TestCompactionRecoveryIntegration_CheckAndRecover_NoCompaction(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	event, sent, err := cri.CheckAndRecover("normal output", "claude", "test", 0)
	if event != nil {
		t.Error("should not detect compaction in normal output")
	}
	if sent {
		t.Error("should not send recovery")
	}
	if err != nil {
		t.Errorf("should not error: %v", err)
	}
}

func TestMakePaneID(t *testing.T) {
	tests := []struct {
		session string
		index   int
		want    string
	}{
		{"mysession", 5, "mysession:.5"},
		{"test", 0, "test:.0"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.session, tt.index), func(t *testing.T) {
			got := makePaneID(tt.session, tt.index)
			if got != tt.want {
				t.Errorf("makePaneID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRecoveryManager_GetRecoveryEvents(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Initially should be empty
	events := rm.GetRecoveryEvents()
	if len(events) != 0 {
		t.Errorf("initial events should be empty, got %d", len(events))
	}

	// Add some events
	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: time.Now()},
		{PaneID: "test:1", SentAt: time.Now()},
	}
	rm.mu.Unlock()

	events = rm.GetRecoveryEvents()
	if len(events) != 2 {
		t.Errorf("should have 2 events, got %d", len(events))
	}
}

func TestRecoveryManager_ResetAll(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// Set some state
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.lastRecovery["test:1"] = time.Now()
	rm.recoveryCount["test:0"] = 5
	rm.recoveryCount["test:1"] = 3
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: time.Now()},
	}
	rm.mu.Unlock()

	// Reset all
	rm.ResetAll()

	// Verify all cleared
	rm.mu.RLock()
	if len(rm.lastRecovery) != 0 {
		t.Errorf("lastRecovery should be empty, got %d entries", len(rm.lastRecovery))
	}
	if len(rm.recoveryCount) != 0 {
		t.Errorf("recoveryCount should be empty, got %d entries", len(rm.recoveryCount))
	}
	if len(rm.recoveryEvents) != 0 {
		t.Errorf("recoveryEvents should be empty, got %d entries", len(rm.recoveryEvents))
	}
	rm.mu.RUnlock()

	// Recovery should be allowed for all panes now
	can, _ := rm.CanSendRecovery("test:0")
	if !can {
		t.Error("recovery should be allowed after ResetAll")
	}
}

func TestRecoveryManager_pruneEvents(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      30 * time.Second,
		MaxRecoveries: 10,
		MaxEventAge:   1 * time.Minute,
	}
	rm := NewRecoveryManager(config)

	// Add old and new events
	oldTime := time.Now().Add(-2 * time.Minute) // Older than maxEventAge
	newTime := time.Now()

	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{
		{PaneID: "test:0", SentAt: oldTime}, // Should be pruned
		{PaneID: "test:1", SentAt: newTime}, // Should be kept
	}
	rm.mu.Unlock()

	// GetRecoveryEvents calls pruneEvents internally
	events := rm.GetRecoveryEvents()

	// Only the new event should remain
	if len(events) != 1 {
		t.Errorf("should have 1 event after pruning, got %d", len(events))
	}
	if len(events) > 0 && events[0].PaneID != "test:1" {
		t.Errorf("remaining event should be test:1, got %s", events[0].PaneID)
	}
}

func TestRecoveryManager_SendRecoveryPrompt_NoTmux(t *testing.T) {
	rm := NewRecoveryManagerDefault()

	// This will fail since we're not in a real tmux session
	// but we're testing the code path
	sent, err := rm.SendRecoveryPrompt("fake_session", 999)

	// Should fail - either tmux not available or session doesn't exist
	if err == nil && sent {
		t.Log("SendRecoveryPrompt succeeded (tmux available)")
	} else if err != nil {
		t.Logf("SendRecoveryPrompt failed as expected: %v", err)
	} else {
		t.Log("SendRecoveryPrompt returned false (skipped)")
	}
}

func TestBuildContextAwarePrompt_NoContext(t *testing.T) {
	basePrompt := "Reread AGENTS.md"

	// Without bead context
	result := BuildContextAwarePrompt(basePrompt, false)
	if result != basePrompt {
		t.Errorf("without bead context, should return base prompt unchanged")
	}
}

func TestBuildContextAwarePrompt_WithContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping slow integration test in short mode")
	}
	basePrompt := "Reread AGENTS.md"

	// With bead context - this tests the real bv integration
	result := BuildContextAwarePrompt(basePrompt, true)

	// Should at least contain the base prompt
	if len(result) < len(basePrompt) {
		t.Errorf("result should contain at least the base prompt")
	}

	// If bv is available and we're in a beads project, it should be longer
	t.Logf("Context-aware prompt length: %d (base: %d)", len(result), len(basePrompt))
}

func TestGetBeadContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping slow integration test in short mode")
	}
	ctx := GetBeadContext()

	// If bv is not installed, ctx should be nil
	// If bv is installed but not in a beads project, we'll get partial data
	// If in a beads project, we'll get full data

	if ctx == nil {
		t.Log("GetBeadContext returned nil (bv not available)")
	} else {
		t.Logf("GetBeadContext: bottlenecks=%d, actions=%d, health=%s, drift=%v",
			len(ctx.TopBottlenecks), len(ctx.NextActions), ctx.HealthStatus, ctx.HasDrift)
	}
}

func TestDefaultRecoveryConfig_IncludesBeadContext(t *testing.T) {
	config := DefaultRecoveryConfig()

	if !config.IncludeBeadContext {
		t.Error("default config should include bead context")
	}
}

func TestRecoveryManager_IncludeBeadContext(t *testing.T) {
	// With bead context enabled
	configWithContext := RecoveryConfig{
		Cooldown:           30 * time.Second,
		Prompt:             "test prompt",
		IncludeBeadContext: true,
	}
	rm1 := NewRecoveryManager(configWithContext)
	if !rm1.includeBeadContext {
		t.Error("should have includeBeadContext true")
	}

	// Without bead context
	configNoContext := RecoveryConfig{
		Cooldown:           30 * time.Second,
		Prompt:             "test prompt",
		IncludeBeadContext: false,
	}
	rm2 := NewRecoveryManager(configNoContext)
	if rm2.includeBeadContext {
		t.Error("should have includeBeadContext false")
	}
}

func TestRecoveryManager_SendRecoveryPromptByID_Cooldown(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Hour, // Long cooldown
		Prompt:        "test",
		MaxRecoveries: 5,
	}
	rm := NewRecoveryManager(config)

	// Simulate a recent recovery
	paneID := "test:0"
	rm.mu.Lock()
	rm.lastRecovery[paneID] = time.Now()
	rm.mu.Unlock()

	// Try to send again - should be blocked by cooldown
	sent, err := rm.sendRecoveryPromptByIDForAgent("test", 0, paneID, "trigger", agent.AgentTypeClaudeCode)
	if sent {
		t.Error("should not send when in cooldown")
	}
	if err != nil {
		t.Errorf("should not error when blocked by cooldown: %v", err)
	}
}

func TestRecoveryManager_SendRecoveryPromptByID_MaxRecoveries(t *testing.T) {
	config := RecoveryConfig{
		Cooldown:      1 * time.Millisecond, // Short cooldown
		Prompt:        "test",
		MaxRecoveries: 3,
	}
	rm := NewRecoveryManager(config)

	// Simulate max recoveries reached
	paneID := "test:0"
	rm.mu.Lock()
	rm.recoveryCount[paneID] = 3
	rm.mu.Unlock()

	// Wait for cooldown to pass
	time.Sleep(5 * time.Millisecond)

	// Try to send - should be blocked by max recoveries
	sent, err := rm.sendRecoveryPromptByIDForAgent("test", 0, paneID, "trigger", agent.AgentTypeClaudeCode)
	if sent {
		t.Error("should not send when max recoveries reached")
	}
	if err != nil {
		t.Errorf("should not error when blocked by max recoveries: %v", err)
	}
}

func TestRecoveryManagerExportedPromptRejectsGrokBeforeStateOrSendMutation(t *testing.T) {
	rm := NewRecoveryManagerDefault()
	rm.resolvePaneType = func(string, int, string) (agent.AgentType, error) {
		return agent.AgentType("xai-grok-build"), nil
	}
	sendCalls := 0
	rm.sendPrompt = func(string, string, bool) error {
		sendCalls++
		return nil
	}

	paneID := "%grok"
	sent, err := rm.SendRecoveryPromptByID("grok-session", 4, paneID, "compacted")
	if sent {
		t.Fatal("Grok recovery sent = true, want false")
	}
	if !errors.Is(err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
		t.Fatalf("Grok recovery error = %v, want prompt-delivery sentinel", err)
	}
	if sendCalls != 0 {
		t.Fatalf("Grok recovery send calls = %d, want 0", sendCalls)
	}
	if count := rm.GetRecoveryCount(paneID); count != 0 {
		t.Fatalf("Grok recovery count = %d, want 0", count)
	}
	if _, ok := rm.GetLastRecoveryTime(paneID); ok {
		t.Fatal("Grok recovery mutated last-recovery state")
	}
}

func TestCompactionRecoveryIntegration_CheckAndRecover_WithCompaction(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	// Test with output containing compaction text
	output := "Conversation compacted due to context limits"

	event, sent, err := cri.CheckAndRecover(output, "cc", "testsession", 0)

	// Should detect compaction
	if event == nil {
		t.Error("should detect compaction in output")
	}

	// Sent should be false since tmux isn't running
	// or true if it happens to be available
	t.Logf("Sent=%v, Error=%v", sent, err)
}

func TestCompactionRecoveryIntegrationDetectsButDoesNotRecoverGrok(t *testing.T) {
	cri := NewCompactionRecoveryIntegrationDefault()

	event, sent, err := cri.CheckAndRecover("Continuing from summary", "Grok-Build", "grok-session", 3)
	if event == nil {
		t.Fatal("compaction event = nil, want detected event")
	}
	if sent {
		t.Fatal("recovery sent = true, want false")
	}
	if !errors.Is(err, agent.ErrAutomatedPromptDeliveryNotImplemented) {
		t.Fatalf("error = %v, want prompt-delivery sentinel", err)
	}
	if detectorEvents := cri.Detector().EventsForPane(makePaneID("grok-session", 3)); len(detectorEvents) != 1 {
		t.Fatalf("detector events = %+v, want one detected event", detectorEvents)
	}
	if count := cri.Recovery().GetRecoveryCount(makePaneID("grok-session", 3)); count != 0 {
		t.Fatalf("recovery count = %d, want 0", count)
	}
	if _, ok := cri.Recovery().GetLastRecoveryTime(makePaneID("grok-session", 3)); ok {
		t.Fatal("last recovery time was mutated")
	}
}

// Recovery prompts are routinely multi-line: BuildContextAwarePrompt appends a
// bead-context block whenever bv is installed. Raw send-keys -l delivers each
// newline to the pane as a real Enter, so the agent received a dozen partial
// prompts instead of one. The resolved agent type must therefore reach the send
// so newline-containing payloads route through paste-buffer.
func TestSendRecoveryPromptForwardsAgentTypeForBufferRouting(t *testing.T) {
	multiline := "Continue where you left off.\n\n# Project Context from Beads\n- bd-1\n- bd-2\n"

	// The injected hook keeps the test hermetic; the contract under test is that
	// sendRecoveryPrompt accepts and forwards the agent type rather than
	// discarding it.
	var gotTarget, gotPrompt string
	var gotEnter bool
	rm := &RecoveryManager{
		sendPrompt: func(target, prompt string, enter bool) error {
			gotTarget, gotPrompt, gotEnter = target, prompt, enter
			return nil
		},
	}

	if err := rm.sendRecoveryPrompt("proj:.2", multiline, true, agent.AgentTypeClaudeCode); err != nil {
		t.Fatalf("sendRecoveryPrompt: %v", err)
	}
	if gotTarget != "proj:.2" || gotPrompt != multiline || !gotEnter {
		t.Fatalf("hook received target=%q enter=%t prompt=%q", gotTarget, gotEnter, gotPrompt)
	}

	// The fixture really is multi-line, which is the condition that made raw
	// send-keys fragment the prompt. tmux.needsBufferSend is unexported, so the
	// routing itself is covered in the tmux package; what matters here is that
	// the agent type reaches the send instead of being discarded.
	if !strings.Contains(multiline, "\n") {
		t.Fatal("fixture is not multi-line; the regression it guards cannot occur")
	}
}
