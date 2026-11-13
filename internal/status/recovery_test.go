package status

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestRecoveryManager_CanSendRecovery(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	can, reason := rm.CanSendRecovery("test:0")
	if !can {
		t.Errorf("first recovery should be allowed, got: %s", reason)
	}
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.mu.Unlock()
	can, reason = rm.CanSendRecovery("test:0")
	if can {
		t.Error("recovery should be blocked by cooldown")
	}
	if reason == "" {
		t.Error("reason should explain cooldown")
	}
}

func TestRecoveryManager_MaxRecoveries(t *testing.T) {
	rm := NewRecoveryManager(RecoveryConfig{Cooldown: time.Millisecond, Prompt: "test prompt", MaxRecoveries: 3})
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
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.recoveryCount["test:0"] = 5
	rm.mu.Unlock()
	rm.ResetPane("test:0")
	if can, _ := rm.CanSendRecovery("test:0"); !can {
		t.Error("recovery should be allowed after reset")
	}
	if count := rm.GetRecoveryCount("test:0"); count != 0 {
		t.Errorf("count should be 0 after reset, got %d", count)
	}
}

func TestRecoveryManager_GetRecoveryCount(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	if count := rm.GetRecoveryCount("test:0"); count != 0 {
		t.Errorf("initial count should be 0, got %d", count)
	}
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()
	if count := rm.GetRecoveryCount("test:0"); count != 3 {
		t.Errorf("count should be 3, got %d", count)
	}
}

func TestRecoveryManager_GetLastRecoveryTime(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	if _, ok := rm.GetLastRecoveryTime("test:0"); ok {
		t.Error("should not have last recovery time yet")
	}
	now := time.Now()
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = now
	rm.mu.Unlock()
	last, ok := rm.GetLastRecoveryTime("test:0")
	if !ok {
		t.Error("should have last recovery time")
	}
	if !last.Equal(now) {
		t.Errorf("last time should match, got %v want %v", last, now)
	}
}

func TestRecoveryManager_SetPrompt(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.SetPrompt("custom prompt")
	if rm.prompt != "custom prompt" {
		t.Errorf("prompt should be 'custom prompt', got %q", rm.prompt)
	}
}
func TestRecoveryManager_SetCooldown(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.SetCooldown(5 * time.Minute)
	if rm.cooldown != 5*time.Minute {
		t.Errorf("cooldown should be 5m, got %v", rm.cooldown)
	}
}

// HandleCompactionEvent dispatches an explicit compaction event in unit tests.
func (rm *RecoveryManager) HandleCompactionEvent(event *CompactionEvent, session string, paneIndex int) (bool, error) {
	if event == nil {
		return false, nil
	}
	paneID := event.PaneID
	if !strings.HasPrefix(paneID, "%") {
		paneID = makePaneID(session, paneIndex)
	}
	return rm.sendRecoveryPromptByIDForAgent(session, paneIndex, paneID, event.MatchedText, agent.AgentType(event.AgentType))
}

// ResetAll resets all tracked recovery state (test-only helper).
func (rm *RecoveryManager) ResetAll() {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.lastRecovery = make(map[string]time.Time)
	rm.recoveryCount = make(map[string]int)
	rm.recoveryEvents = make([]RecoveryEvent, 0)
}

func TestRecoveryManager_HandleCompactionEvent(t *testing.T) {
	rm := NewRecoveryManager(RecoveryConfig{Cooldown: time.Second, MaxRecoveries: 5})
	event := &CompactionEvent{AgentType: "claude", MatchedText: "Conversation compacted", DetectedAt: time.Now()}
	_, err := rm.HandleCompactionEvent(event, "testsession", 0)
	if err == nil {
		t.Log("HandleCompactionEvent succeeded (tmux available)")
	} else {
		t.Logf("HandleCompactionEvent failed as expected without tmux: %v", err)
	}
	sent, err := rm.HandleCompactionEvent(nil, "testsession", 0)
	if sent {
		t.Error("should not send for nil event")
	}
	if err != nil {
		t.Error("should not error for nil event")
	}
}

// Explicit operator recovery remains available for Grok. It does not make an
// unverified generic prose pattern safe for automatic recovery detection.
func TestRecoveryManagerRecoversGrokCompaction(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.includeBeadContext = false
	sendCh := make(chan string, 1)
	rm.sendPrompt = func(target, _ string, _ bool) error { sendCh <- target; return nil }
	event := &CompactionEvent{PaneID: "%17", AgentType: " XAI_GROK_BUILD ", MatchedText: "continuing from summary", DetectedAt: time.Now()}
	sent, err := rm.HandleCompactionEvent(event, "grok-session", 4)
	if err != nil || !sent {
		t.Fatalf("explicit Grok recovery = %t, %v", sent, err)
	}
	if event.PaneID != "%17" {
		t.Fatal("recovery mutated the caller's durable pane identity")
	}
	if count := rm.GetRecoveryCount("%17"); count != 1 {
		t.Fatalf("recovery count = %d, want 1", count)
	}
	if _, ok := rm.GetLastRecoveryTime("%17"); !ok {
		t.Fatal("last recovery time was not recorded")
	}
	select {
	case target := <-sendCh:
		if target != "%17" {
			t.Fatalf("target = %q, want stable %%17 rather than an active-window index", target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery prompt never reached the sender")
	}
}

func TestRecoveryEvent(t *testing.T) {
	event := RecoveryEvent{PaneID: "test:0", Session: "test", PaneIndex: 0, SentAt: time.Now(), Prompt: "test prompt", TriggerText: "Conversation compacted"}
	if event.PaneID != "test:0" {
		t.Errorf("PaneID should be test:0, got %s", event.PaneID)
	}
	if event.TriggerText != "Conversation compacted" {
		t.Error("TriggerText should be set")
	}
}
func TestDefaultRecoveryConfig(t *testing.T) {
	config := DefaultRecoveryConfig()
	if config.Cooldown != DefaultCooldown {
		t.Errorf("Cooldown should be %v, got %v", DefaultCooldown, config.Cooldown)
	}
	if config.Prompt != DefaultRecoveryPrompt {
		t.Error("Prompt should be default")
	}
	if config.MaxRecoveries != DefaultMaxRecoveriesPerPane {
		t.Errorf("MaxRecoveries should be %d, got %d", DefaultMaxRecoveriesPerPane, config.MaxRecoveries)
	}
}
func TestCompactionRecoveryIntegration(t *testing.T) {
	cri := NewCompactionRecoveryIntegration(DefaultRecoveryConfig())
	if cri.Detector() == nil {
		t.Error("detector should not be nil")
	}
	if cri.Recovery() == nil {
		t.Error("recovery should not be nil")
	}
}
func TestMakePaneID(t *testing.T) {
	for _, tc := range []struct {
		session string
		index   int
		want    string
	}{{"mysession", 5, "mysession:.5"}, {"test", 0, "test:.0"}} {
		t.Run(fmt.Sprintf("%s_%d", tc.session, tc.index), func(t *testing.T) {
			if got := makePaneID(tc.session, tc.index); got != tc.want {
				t.Errorf("makePaneID = %q, want %q", got, tc.want)
			}
		})
	}
}
func TestRecoveryManager_GetRecoveryEvents(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	if events := rm.GetRecoveryEvents(); len(events) != 0 {
		t.Errorf("initial events should be empty, got %d", len(events))
	}
	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{{PaneID: "test:0", SentAt: time.Now()}, {PaneID: "test:1", SentAt: time.Now()}}
	rm.mu.Unlock()
	if events := rm.GetRecoveryEvents(); len(events) != 2 {
		t.Errorf("should have 2 events, got %d", len(events))
	}
}
func TestRecoveryManager_ResetAll(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.mu.Lock()
	rm.lastRecovery["test:0"], rm.lastRecovery["test:1"] = time.Now(), time.Now()
	rm.recoveryCount["test:0"], rm.recoveryCount["test:1"] = 5, 3
	rm.recoveryEvents = []RecoveryEvent{{PaneID: "test:0", SentAt: time.Now()}}
	rm.mu.Unlock()
	rm.ResetAll()
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
	if can, _ := rm.CanSendRecovery("test:0"); !can {
		t.Error("recovery should be allowed after ResetAll")
	}
}
func TestRecoveryManager_pruneEvents(t *testing.T) {
	rm := NewRecoveryManager(RecoveryConfig{Cooldown: 30 * time.Second, MaxRecoveries: 10, MaxEventAge: time.Minute})
	rm.mu.Lock()
	rm.recoveryEvents = []RecoveryEvent{{PaneID: "test:0", SentAt: time.Now().Add(-2 * time.Minute)}, {PaneID: "test:1", SentAt: time.Now()}}
	rm.mu.Unlock()
	events := rm.GetRecoveryEvents()
	if len(events) != 1 {
		t.Errorf("should have 1 event after pruning, got %d", len(events))
	}
	if len(events) > 0 && events[0].PaneID != "test:1" {
		t.Errorf("remaining event should be test:1, got %s", events[0].PaneID)
	}
}
func TestRecoveryManager_SendRecoveryPrompt_NoTmux(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	sent, err := rm.SendRecoveryPrompt("fake_session", 999)
	if err == nil && sent {
		t.Log("SendRecoveryPrompt succeeded (tmux available)")
	} else if err != nil {
		t.Logf("SendRecoveryPrompt failed as expected: %v", err)
	} else {
		t.Log("SendRecoveryPrompt returned false (skipped)")
	}
}
func TestBuildContextAwarePrompt_NoContext(t *testing.T) {
	base := "Reread AGENTS.md"
	if BuildContextAwarePrompt(base, false) != base {
		t.Error("without bead context, should return base prompt unchanged")
	}
}
func TestBuildContextAwarePrompt_WithContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping slow integration test in short mode")
	}
	base := "Reread AGENTS.md"
	result := BuildContextAwarePrompt(base, true)
	if len(result) < len(base) {
		t.Error("result should contain at least the base prompt")
	}
	t.Logf("Context-aware prompt length: %d (base: %d)", len(result), len(base))
}
func TestGetBeadContext(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping slow integration test in short mode")
	}
	ctx := GetBeadContext()
	if ctx == nil {
		t.Log("GetBeadContext returned nil (bv not available)")
	} else {
		t.Logf("GetBeadContext: bottlenecks=%d, actions=%d, health=%s, drift=%v", len(ctx.TopBottlenecks), len(ctx.NextActions), ctx.HealthStatus, ctx.HasDrift)
	}
}
func TestDefaultRecoveryConfig_IncludesBeadContext(t *testing.T) {
	if !DefaultRecoveryConfig().IncludeBeadContext {
		t.Error("default config should include bead context")
	}
}
func TestRecoveryManager_IncludeBeadContext(t *testing.T) {
	rm1 := NewRecoveryManager(RecoveryConfig{Cooldown: 30 * time.Second, Prompt: "test prompt", IncludeBeadContext: true})
	if !rm1.includeBeadContext {
		t.Error("should have includeBeadContext true")
	}
	rm2 := NewRecoveryManager(RecoveryConfig{Cooldown: 30 * time.Second, Prompt: "test prompt", IncludeBeadContext: false})
	if rm2.includeBeadContext {
		t.Error("should have includeBeadContext false")
	}
}
func TestRecoveryManager_SendRecoveryPromptByID_Cooldown(t *testing.T) {
	rm := NewRecoveryManager(RecoveryConfig{Cooldown: time.Hour, Prompt: "test", MaxRecoveries: 5})
	rm.mu.Lock()
	rm.lastRecovery["test:0"] = time.Now()
	rm.mu.Unlock()
	sent, err := rm.sendRecoveryPromptByIDForAgent("test", 0, "test:0", "trigger", agent.AgentTypeClaudeCode)
	if sent {
		t.Error("should not send when in cooldown")
	}
	if err != nil {
		t.Errorf("should not error when blocked by cooldown: %v", err)
	}
}
func TestRecoveryManager_SendRecoveryPromptByID_MaxRecoveries(t *testing.T) {
	rm := NewRecoveryManager(RecoveryConfig{Cooldown: time.Millisecond, Prompt: "test", MaxRecoveries: 3})
	rm.mu.Lock()
	rm.recoveryCount["test:0"] = 3
	rm.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	sent, err := rm.sendRecoveryPromptByIDForAgent("test", 0, "test:0", "trigger", agent.AgentTypeClaudeCode)
	if sent {
		t.Error("should not send when max recoveries reached")
	}
	if err != nil {
		t.Errorf("should not error when blocked by max recoveries: %v", err)
	}
}
func TestRecoveryManagerExportedPromptDeliversToGrok(t *testing.T) {
	rm := NewRecoveryManager(DefaultRecoveryConfig())
	rm.includeBeadContext = false
	rm.resolvePaneType = func(string, int, string) (agent.AgentType, error) { return agent.AgentType("xai-grok-build"), nil }
	sendCh := make(chan struct{}, 1)
	rm.sendPrompt = func(string, string, bool) error { sendCh <- struct{}{}; return nil }
	paneID := "%grok"
	sent, err := rm.SendRecoveryPromptByID("grok-session", 4, paneID, "compacted")
	if err != nil || !sent {
		t.Fatalf("Grok recovery = %t, %v", sent, err)
	}
	if count := rm.GetRecoveryCount(paneID); count != 1 {
		t.Fatalf("Grok recovery count = %d, want 1", count)
	}
	if _, ok := rm.GetLastRecoveryTime(paneID); !ok {
		t.Fatal("Grok recovery did not record last-recovery state")
	}
	select {
	case <-sendCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Grok recovery prompt never reached the sender")
	}
}

// Generic summary prose is not a compaction banner, so it never reaches the
// monitor's recovery. Explicit Grok delivery is covered by the recovery-manager
// tests above.
func TestCompactionDetectorIgnoresGenericSummaryProse(t *testing.T) {
	detector := NewCompactionDetector(5 * time.Minute)
	paneID := makePaneID("grok-session", 3)
	detector.Check("progress anchor", "Grok-Build", paneID)
	if event := detector.Check("progress anchor\nContinuing from summary", "Grok-Build", paneID); event != nil {
		t.Fatalf("generic prose detected as compaction: %+v", event)
	}
	if events := detector.EventsForPane(paneID); len(events) != 0 {
		t.Fatalf("generic prose recorded compaction events: %+v", events)
	}
}

func TestSendRecoveryPromptForwardsAgentTypeForBufferRouting(t *testing.T) {
	multiline := "Continue where you left off.\n\n# Project Context from Beads\n- bd-1\n- bd-2\n"
	var gotTarget, gotPrompt string
	var gotEnter bool
	rm := &RecoveryManager{sendPrompt: func(target, prompt string, enter bool) error {
		gotTarget, gotPrompt, gotEnter = target, prompt, enter
		return nil
	}}
	if err := rm.sendRecoveryPrompt(context.Background(), "proj:.2", multiline, true, agent.AgentTypeClaudeCode); err != nil {
		t.Fatalf("sendRecoveryPrompt: %v", err)
	}
	if gotTarget != "proj:.2" || gotPrompt != multiline || !gotEnter {
		t.Fatalf("hook received target=%q enter=%t prompt=%q", gotTarget, gotEnter, gotPrompt)
	}
	if !strings.Contains(multiline, "\n") {
		t.Fatal("fixture is not multi-line")
	}
}
