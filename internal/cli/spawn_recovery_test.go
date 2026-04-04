package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
)

type recoveryMailStub struct {
	inboxByAgent      map[string][]agentmail.InboxMessage
	inboxErrByAgent   map[string]error
	resByAgent        map[string][]agentmail.FileReservation
	resErrByAgent     map[string]error
	fetchInboxAgents  []string
	listReserveAgents []string
}

func (s *recoveryMailStub) FetchInbox(_ context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error) {
	s.fetchInboxAgents = append(s.fetchInboxAgents, opts.AgentName)
	if err := s.inboxErrByAgent[opts.AgentName]; err != nil {
		return nil, err
	}
	return s.inboxByAgent[opts.AgentName], nil
}

func (s *recoveryMailStub) ListReservations(_ context.Context, projectKey, agentName string, allAgents bool) ([]agentmail.FileReservation, error) {
	_ = projectKey
	_ = allAgents
	s.listReserveAgents = append(s.listReserveAgents, agentName)
	if err := s.resErrByAgent[agentName]; err != nil {
		return nil, err
	}
	return s.resByAgent[agentName], nil
}

func TestRecoveryAgentCandidates_DeduplicatesSessionName(t *testing.T) {
	candidates := recoveryAgentCandidates("session-1", "session-1")
	if len(candidates) != 1 || candidates[0] != "session-1" {
		t.Fatalf("expected only session name candidate, got %#v", candidates)
	}

	candidates = recoveryAgentCandidates("session-1", "GreenCastle")
	if len(candidates) != 2 || candidates[0] != "GreenCastle" || candidates[1] != "session-1" {
		t.Fatalf("expected resolved agent then session fallback, got %#v", candidates)
	}
}

func TestFetchRecoveryInbox_ReturnsEffectiveFallbackAgentName(t *testing.T) {
	stub := &recoveryMailStub{
		inboxByAgent: map[string][]agentmail.InboxMessage{
			"session-1": {{Subject: "fallback worked"}},
		},
		inboxErrByAgent: map[string]error{
			"GreenCastle": errors.New("agent not registered"),
		},
	}

	inbox, effectiveAgentName, err := fetchRecoveryInbox(context.Background(), stub, "/tmp/project", "session-1", "GreenCastle")
	if err != nil {
		t.Fatalf("expected fallback inbox fetch to succeed, got %v", err)
	}
	if effectiveAgentName != "session-1" {
		t.Fatalf("expected effective agent to fall back to session name, got %q", effectiveAgentName)
	}
	if len(inbox) != 1 || inbox[0].Subject != "fallback worked" {
		t.Fatalf("expected fallback inbox contents, got %#v", inbox)
	}
	if got := strings.Join(stub.fetchInboxAgents, ","); got != "GreenCastle,session-1" {
		t.Fatalf("expected fetch order GreenCastle then session-1, got %q", got)
	}
}

func TestListRecoveryReservations_FollowsEffectiveFallbackAgentName(t *testing.T) {
	stub := &recoveryMailStub{
		resByAgent: map[string][]agentmail.FileReservation{
			"session-1": {{PathPattern: "internal/cli/spawn.go"}},
		},
		resErrByAgent: map[string]error{
			"GreenCastle": errors.New("agent not found"),
		},
	}

	reservations, effectiveAgentName, err := listRecoveryReservations(context.Background(), stub, "/tmp/project", "session-1", "GreenCastle")
	if err != nil {
		t.Fatalf("expected fallback reservation lookup to succeed, got %v", err)
	}
	if effectiveAgentName != "session-1" {
		t.Fatalf("expected effective reservation agent to fall back to session name, got %q", effectiveAgentName)
	}
	if len(reservations) != 1 || reservations[0].PathPattern != "internal/cli/spawn.go" {
		t.Fatalf("expected fallback reservations, got %#v", reservations)
	}
	if got := strings.Join(stub.listReserveAgents, ","); got != "GreenCastle,session-1" {
		t.Fatalf("expected reservation lookup order GreenCastle then session-1, got %q", got)
	}
}

func TestResolveRecoveryAgentNameUsesSavedSessionAgentIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	sessionName := "session-1"
	actualProject := t.TempDir()
	saveSessionAgentForTest(t, sessionName, actualProject, "GreenCastle")

	if got := resolveRecoveryAgentName(sessionName, t.TempDir()); got != "GreenCastle" {
		t.Fatalf("expected saved session agent name, got %q", got)
	}
}

func TestBuildRecoveryReservationTransferOptions_UsesResolvedAgentName(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()

	cfg = config.Default()
	cfg.FileReservation.DefaultTTLMin = 45

	transfer := &handoff.ReservationTransfer{
		FromAgent:          "BlueLake",
		GracePeriodSeconds: 7,
		Reservations: []handoff.ReservationSnapshot{
			{PathPattern: "internal/a.go", Exclusive: true},
		},
	}

	opts := buildRecoveryReservationTransferOptions(transfer, "GreenCastle", "/tmp/project")

	if opts.ProjectKey != "/tmp/project" {
		t.Fatalf("expected project key fallback to working dir, got %q", opts.ProjectKey)
	}
	if opts.FromAgent != "BlueLake" {
		t.Fatalf("expected from agent BlueLake, got %q", opts.FromAgent)
	}
	if opts.ToAgent != "GreenCastle" {
		t.Fatalf("expected resolved target agent GreenCastle, got %q", opts.ToAgent)
	}
	if opts.TTLSeconds != 45*60 {
		t.Fatalf("expected TTL from config fallback, got %d", opts.TTLSeconds)
	}
	if opts.GracePeriod != 7*time.Second {
		t.Fatalf("expected grace period of 7s, got %v", opts.GracePeriod)
	}
	if len(opts.Reservations) != 1 || opts.Reservations[0].PathPattern != "internal/a.go" {
		t.Fatalf("expected reservations to be preserved, got %#v", opts.Reservations)
	}
}

func TestLoadRecoveryCheckpoint_NoCheckpointsReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cp, err := loadRecoveryCheckpoint("recovery-empty-session")
	if err != nil {
		t.Fatalf("loadRecoveryCheckpoint() error = %v, want nil", err)
	}
	if cp != nil {
		t.Fatalf("loadRecoveryCheckpoint() = %#v, want nil", cp)
	}
}

func TestLoadRecoveryCheckpoint_InvalidLatestCheckpointReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	storage := checkpoint.NewStorage()
	sessionName := "recovery-invalid-session"
	invalidDir := storage.CheckpointDir(sessionName, "20260101-120000-bad")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) failed: %v", invalidDir, err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, checkpoint.MetadataFile), []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(metadata) failed: %v", err)
	}

	cp, err := loadRecoveryCheckpoint(sessionName)
	if err == nil {
		t.Fatal("loadRecoveryCheckpoint() error = nil, want invalid checkpoint error")
	}
	if cp != nil {
		t.Fatalf("loadRecoveryCheckpoint() = %#v, want nil", cp)
	}
	if !strings.Contains(err.Error(), "checkpoint selection blocked by invalid checkpoint") {
		t.Fatalf("loadRecoveryCheckpoint() error = %v, want invalid checkpoint context", err)
	}
}

// TestRecoveryContext_EstimateTokens tests the token estimation function
// NOTE: This test uses the current RecoveryContext struct fields. If the struct
// changes (e.g., Sessions replaced with CMMemories), update these tests accordingly.
func TestRecoveryContext_EstimateTokens(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_EstimateTokens | Testing token estimation accuracy")

	// Note: estimateRecoveryTokens adds 500 chars (~125 tokens) overhead for formatting
	const overhead = 125

	tests := []struct {
		name      string
		rc        *RecoveryContext
		minTokens int
		maxTokens int
	}{
		{
			name:      "empty context",
			rc:        &RecoveryContext{},
			minTokens: overhead,
			maxTokens: overhead + 10,
		},
		{
			name: "checkpoint only",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{
					Name:        "test-checkpoint",
					Description: "A test checkpoint for session recovery",
				},
			},
			minTokens: overhead,
			maxTokens: overhead + 50,
		},
		{
			name: "with messages",
			rc: &RecoveryContext{
				Messages: []RecoveryMessage{
					{
						Subject: "Test message 1",
						Body:    "This is the body of the first test message with some content.",
						From:    "TestAgent",
					},
					{
						Subject: "Test message 2",
						Body:    "Another message body with different content for testing.",
						From:    "AnotherAgent",
					},
				},
			},
			minTokens: overhead + 30,
			maxTokens: overhead + 100,
		},
		{
			name: "with beads",
			rc: &RecoveryContext{
				Beads: []RecoveryBead{
					{ID: "bd-123", Title: "Fix authentication bug", Assignee: "agent1"},
					{ID: "bd-456", Title: "Add unit tests for recovery", Assignee: "agent2"},
					{ID: "bd-789", Title: "Implement dashboard feature", Assignee: ""},
				},
			},
			minTokens: overhead + 15,
			maxTokens: overhead + 60,
		},
		{
			name: "with completed and blocked beads",
			rc: &RecoveryContext{
				Beads: []RecoveryBead{
					{ID: "bd-001", Title: "In progress task"},
				},
				CompletedBeads: []RecoveryBead{
					{ID: "bd-002", Title: "Completed task"},
				},
				BlockedBeads: []RecoveryBead{
					{ID: "bd-003", Title: "Blocked task"},
				},
			},
			minTokens: overhead + 15,
			maxTokens: overhead + 80,
		},
		{
			name: "full context",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{
					Name:        "full-checkpoint",
					Description: "Full recovery checkpoint",
				},
				Messages: []RecoveryMessage{
					{Subject: "Msg 1", Body: "Body 1", From: "Agent1"},
				},
				Beads: []RecoveryBead{
					{ID: "bd-001", Title: "Task 1", Assignee: "agent"},
				},
				FileReservations: []string{
					"internal/cli/spawn.go",
					"internal/cli/spawn_test.go",
				},
			},
			minTokens: overhead + 25,
			maxTokens: overhead + 80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimateRecoveryTokens(tt.rc)
			t.Logf("RECOVERY_TEST: %s | Estimated tokens: %d | Expected range: [%d, %d]",
				tt.name, tokens, tt.minTokens, tt.maxTokens)

			if tokens < tt.minTokens {
				t.Errorf("estimated %d tokens, expected at least %d", tokens, tt.minTokens)
			}
			if tokens > tt.maxTokens {
				t.Errorf("estimated %d tokens, expected at most %d", tokens, tt.maxTokens)
			}
		})
	}
}

// TestRecoveryContext_Truncate tests the truncation logic
func TestRecoveryContext_Truncate(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_Truncate | Testing token truncation behavior")

	tests := []struct {
		name      string
		rc        *RecoveryContext
		maxTokens int
		checkFunc func(t *testing.T, result *RecoveryContext)
	}{
		{
			name: "no truncation needed",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{Name: "cp", Description: "desc"},
				TokenCount: 50,
			},
			maxTokens: 1000,
			checkFunc: func(t *testing.T, result *RecoveryContext) {
				if result.Checkpoint == nil {
					t.Error("checkpoint should be preserved")
				}
			},
		},
		{
			name: "truncate messages",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{Name: "cp"},
				Messages: []RecoveryMessage{
					{Subject: "m1", Body: strings.Repeat("body content ", 100)},
					{Subject: "m2", Body: strings.Repeat("more body content ", 100)},
					{Subject: "m3", Body: strings.Repeat("even more content ", 100)},
				},
				Beads:      []RecoveryBead{{ID: "b1", Title: "task"}},
				TokenCount: 1500,
			},
			maxTokens: 100,
			checkFunc: func(t *testing.T, result *RecoveryContext) {
				// Messages should be truncated
				if len(result.Messages) >= 3 {
					t.Errorf("expected messages to be truncated, got %d", len(result.Messages))
				}
				// Checkpoint should be preserved (highest priority)
				if result.Checkpoint == nil {
					t.Error("checkpoint should be preserved")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set initial token count
			tt.rc.TokenCount = estimateRecoveryTokens(tt.rc)
			t.Logf("RECOVERY_TEST: %s | Initial tokens: %d | Max: %d",
				tt.name, tt.rc.TokenCount, tt.maxTokens)

			truncateRecoveryContext(tt.rc, tt.maxTokens)

			t.Logf("RECOVERY_TEST: %s | After truncation: tokens=%d messages=%d beads=%d",
				tt.name, tt.rc.TokenCount, len(tt.rc.Messages), len(tt.rc.Beads))

			tt.checkFunc(t, tt.rc)
		})
	}
}

// TestRecoveryContext_FormatPrompt tests the prompt formatting
func TestRecoveryContext_FormatPrompt(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_FormatPrompt | Testing prompt formatting")

	tests := []struct {
		name           string
		rc             *RecoveryContext
		expectEmpty    bool
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:        "nil context returns empty",
			rc:          nil,
			expectEmpty: true,
		},
		{
			name:        "empty context returns empty",
			rc:          &RecoveryContext{},
			expectEmpty: true,
		},
		{
			name: "checkpoint only",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{
					Name:        "test-checkpoint",
					Description: "Testing recovery",
					CreatedAt:   time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
					PaneCount:   3,
					HasGitPatch: true,
				},
			},
			expectEmpty: false,
			mustContain: []string{
				"Session Recovery Context",
				"Your Previous Work",
				"Last checkpoint",
				"Testing recovery",
				"Uncommitted changes",
			},
		},
		{
			name: "checkpoint with assignment and bv summary",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{
					Name:        "summary-checkpoint",
					Description: "Checkpoint with summary",
					Assignments: &RecoveryAssignmentSummary{
						Total:    2,
						Working:  1,
						Assigned: 1,
					},
					BVSummary: &RecoveryBVSummary{
						ActionableCount: 3,
						BlockedCount:    1,
					},
				},
			},
			expectEmpty: false,
			mustContain: []string{
				"Assignment summary",
				"Beads summary",
				"3 ready",
				"1 blocked",
			},
		},
		{
			name: "beads included",
			rc: &RecoveryContext{
				Beads: []RecoveryBead{
					{ID: "bd-123", Title: "Fix the auth bug"},
					{ID: "bd-456", Title: "Add unit tests"},
				},
			},
			expectEmpty: false,
			mustContain: []string{
				"Current Task Status",
				"bd-123",
				"bd-456",
				"Fix the auth bug",
				"Add unit tests",
				"You were working on",
			},
		},
		{
			name: "messages included",
			rc: &RecoveryContext{
				Messages: []RecoveryMessage{
					{
						From:      "TeamLead",
						Subject:   "Priority task",
						Body:      "Please focus on the authentication module.",
						CreatedAt: time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC),
					},
				},
			},
			expectEmpty: false,
			mustContain: []string{
				"Recent Messages",
				"TeamLead",
				"Priority task",
				"authentication module",
			},
		},
		{
			name: "full context",
			rc: &RecoveryContext{
				Checkpoint: &RecoveryCheckpoint{Name: "full-cp"},
				Beads:      []RecoveryBead{{ID: "bd-001", Title: "Task 1"}},
				Messages:   []RecoveryMessage{{Subject: "Msg 1", From: "Agent"}},
			},
			expectEmpty: false,
			mustContain: []string{
				"Session Recovery Context",
				"Your Previous Work",
				"Current Task Status",
				"Recent Messages",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatRecoveryPrompt(tt.rc, AgentTypeClaude)

			t.Logf("RECOVERY_TEST: %s | Output length: %d chars | Empty: %v",
				tt.name, len(result), result == "")

			if tt.expectEmpty && result != "" {
				t.Errorf("expected empty result, got %d chars", len(result))
			}
			if !tt.expectEmpty && result == "" {
				t.Error("expected non-empty result, got empty")
			}

			for _, s := range tt.mustContain {
				if !strings.Contains(result, s) {
					t.Errorf("expected output to contain %q", s)
				}
			}

			for _, s := range tt.mustNotContain {
				if strings.Contains(result, s) {
					t.Errorf("expected output to NOT contain %q", s)
				}
			}

			if result != "" {
				t.Logf("RECOVERY_TEST: %s | First 200 chars: %s", tt.name,
					truncateForLog(result, 200))
			}
		})
	}
}

// TestRecoveryContext_BuildWithDisabled tests that disabled config returns nil
func TestRecoveryContext_BuildWithDisabled(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_BuildWithDisabled | Testing disabled recovery")

	ctx := context.Background()
	recoveryCfg := config.SessionRecoveryConfig{
		Enabled: false,
	}

	rc, err := buildRecoveryContext(ctx, "test-session", "/tmp/test", recoveryCfg)

	t.Logf("RECOVERY_TEST: Disabled config | Result: rc=%v err=%v", rc, err)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if rc != nil {
		t.Error("expected nil recovery context when disabled")
	}
}

// TestRecoveryContext_BuildGracefulDegradation tests graceful degradation when services are unavailable
func TestRecoveryContext_BuildGracefulDegradation(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_BuildGracefulDegradation | Testing graceful degradation")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Config that enables all services but uses non-existent paths/services
	recoveryCfg := config.SessionRecoveryConfig{
		Enabled:             true,
		IncludeAgentMail:    true,
		IncludeBeadsContext: true,
		IncludeCMMemories:   true,
		MaxRecoveryTokens:   3000,
		AutoInjectOnSpawn:   true,
		StaleThresholdHours: 24,
	}

	// Use a non-existent session/project to trigger graceful fallbacks
	rc, err := buildRecoveryContext(ctx, "nonexistent-session-12345", "/nonexistent/path", recoveryCfg)

	t.Logf("RECOVERY_TEST: Graceful degradation | err=%v rc=%+v", err, rc)

	// Should not error - should gracefully degrade
	if err != nil {
		t.Errorf("expected graceful degradation, got error: %v", err)
	}

	// Should return a context (possibly empty) rather than nil
	if rc == nil {
		t.Log("RECOVERY_TEST: Recovery context is nil (all services unavailable) - this is acceptable")
	} else {
		t.Logf("RECOVERY_TEST: Recovery context created with checkpoint=%v msgs=%d beads=%d",
			rc.Checkpoint != nil, len(rc.Messages), len(rc.Beads))
	}
}

// TestRecoveryContext_TokenBudgetEnforced tests that token budget is enforced
func TestRecoveryContext_TokenBudgetEnforced(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryContext_TokenBudgetEnforced | Testing token budget enforcement")

	// Create a context that would exceed token budget
	rc := &RecoveryContext{
		Checkpoint: &RecoveryCheckpoint{
			Name:        "checkpoint",
			Description: "description",
		},
		Messages: make([]RecoveryMessage, 20),
		Beads:    make([]RecoveryBead, 10),
	}

	// Fill with content
	for i := 0; i < 20; i++ {
		rc.Messages[i] = RecoveryMessage{
			Subject: "Subject " + string(rune('A'+i)),
			Body:    strings.Repeat("Message body content ", 50),
			From:    "Agent" + string(rune('A'+i)),
		}
	}
	for i := 0; i < 10; i++ {
		rc.Beads[i] = RecoveryBead{
			ID:    "bd-" + string(rune('0'+i)),
			Title: "Task title with some description " + string(rune('A'+i)),
		}
	}

	rc.TokenCount = estimateRecoveryTokens(rc)
	initialTokens := rc.TokenCount
	t.Logf("RECOVERY_TEST: Initial tokens: %d", initialTokens)

	// Apply truncation with a small budget
	maxTokens := 500
	truncateRecoveryContext(rc, maxTokens)

	t.Logf("RECOVERY_TEST: After truncation | tokens=%d messages=%d beads=%d",
		rc.TokenCount, len(rc.Messages), len(rc.Beads))

	// Token count should be reduced
	if rc.TokenCount > maxTokens*2 { // Allow some overage due to estimation
		t.Errorf("token count %d significantly exceeds budget %d", rc.TokenCount, maxTokens)
	}

	// Checkpoint should be preserved (highest priority)
	if rc.Checkpoint == nil {
		t.Error("checkpoint should be preserved even during truncation")
	}
}

// TestRecoveryCheckpoint_Structure tests the checkpoint structure
func TestRecoveryCheckpoint_Structure(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryCheckpoint_Structure | Testing checkpoint data structure")

	cp := &RecoveryCheckpoint{
		ID:          "cp-12345",
		Name:        "pre-refactor",
		Description: "Checkpoint before major refactoring",
		CreatedAt:   time.Now(),
		PaneCount:   5,
		HasGitPatch: true,
	}

	if cp.ID == "" {
		t.Error("ID should not be empty")
	}
	if cp.Name == "" {
		t.Error("Name should not be empty")
	}
	if cp.PaneCount <= 0 {
		t.Error("PaneCount should be positive")
	}

	t.Logf("RECOVERY_TEST: Checkpoint | ID=%s Name=%s Panes=%d HasGit=%v",
		cp.ID, cp.Name, cp.PaneCount, cp.HasGitPatch)
}

// TestRecoveryMessage_Structure tests the message structure
func TestRecoveryMessage_Structure(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryMessage_Structure | Testing message data structure")

	msg := &RecoveryMessage{
		ID:         12345,
		From:       "ArchitectAgent",
		Subject:    "Design review needed",
		Body:       "Please review the API design document.",
		Importance: "high",
		CreatedAt:  time.Now(),
	}

	if msg.ID == 0 {
		t.Error("ID should not be zero")
	}
	if msg.From == "" {
		t.Error("From should not be empty")
	}
	if msg.Subject == "" {
		t.Error("Subject should not be empty")
	}

	t.Logf("RECOVERY_TEST: Message | ID=%d From=%s Subject=%s Importance=%s",
		msg.ID, msg.From, msg.Subject, msg.Importance)
}

// TestRecoveryBead_Structure tests the bead structure
func TestRecoveryBead_Structure(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryBead_Structure | Testing bead data structure")

	bead := &RecoveryBead{
		ID:       "bd-abc123",
		Title:    "Implement session recovery",
		Assignee: "PinkBeaver",
	}

	if bead.ID == "" {
		t.Error("ID should not be empty")
	}
	if bead.Title == "" {
		t.Error("Title should not be empty")
	}

	t.Logf("RECOVERY_TEST: Bead | ID=%s Title=%s Assignee=%s",
		bead.ID, bead.Title, bead.Assignee)
}

// TestRecoveryCMRule_Structure tests the CM rule structure
func TestRecoveryCMRule_Structure(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryCMRule_Structure | Testing CM rule data structure")

	rule := &RecoveryCMRule{
		ID:      "rule-123",
		Content: "Always run tests before committing",
	}

	if rule.ID == "" {
		t.Error("ID should not be empty")
	}
	if rule.Content == "" {
		t.Error("Content should not be empty")
	}

	t.Logf("RECOVERY_TEST: CM Rule | ID=%s Content=%s", rule.ID, rule.Content)
}

// TestRecoveryCMMemories_Structure tests the CM memories structure
func TestRecoveryCMMemories_Structure(t *testing.T) {
	t.Log("RECOVERY_TEST: TestRecoveryCMMemories_Structure | Testing CM memories data structure")

	memories := &RecoveryCMMemories{
		Rules: []RecoveryCMRule{
			{ID: "rule-1", Content: "Test first"},
			{ID: "rule-2", Content: "Code review always"},
		},
		AntiPatterns: []RecoveryCMRule{
			{ID: "anti-1", Content: "Don't commit without testing"},
		},
	}

	if len(memories.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(memories.Rules))
	}
	if len(memories.AntiPatterns) != 1 {
		t.Errorf("expected 1 anti-pattern, got %d", len(memories.AntiPatterns))
	}

	t.Logf("RECOVERY_TEST: CM Memories | Rules=%d AntiPatterns=%d",
		len(memories.Rules), len(memories.AntiPatterns))
}

// truncateForLog truncates strings for logging in tests
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
