package robot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/session"
)

func TestWave15SavedSessionsORILifecycleAnd11HarnessResume(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	now := time.Now().UTC().Truncate(time.Second)
	snapshot := &session.SessionState{
		Name:      "wave15_proj",
		SavedAt:   now,
		WorkDir:   "/tmp/wave15_proj",
		GitBranch: "main",
		Layout:    "tiled",
		Version:   session.StateVersion,
		Agents: session.AgentConfig{
			Claude:      2,
			Codex:       1,
			Gemini:      1,
			Antigravity: 1,
			Grok:        1,
			Cursor:      1,
			Windsurf:    1,
			Aider:       1,
			Opencode:    1,
			Omp:         1,
			Ollama:      1,
			User:        1,
		},
		Panes: []session.PaneState{
			{
				Title:             "wave15_proj__cc_1",
				Index:             0,
				AgentType:         "cc",
				SessionID:         "claude-sess-001",
				SessionProvider:   "claude",
				SessionFreshness:  agentsession.BindingFresh,
				SessionConfidence: 0.95,
			},
			{
				Title:             "wave15_proj__agy_1",
				Index:             1,
				AgentType:         "agy",
				SessionID:         "agy-sess-002",
				SessionProvider:   "antigravity",
				SessionFreshness:  agentsession.BindingFresh,
				SessionConfidence: 0.88,
			},
			{
				Title:             "wave15_proj__cod_1",
				Index:             2,
				AgentType:         "cod",
				SessionID:         "codex-stale-003",
				SessionProvider:   "codex",
				SessionFreshness:  agentsession.BindingStale,
				SessionConfidence: 0.40,
			},
		},
	}

	summaryLines := FormatAgentConfigSummary(snapshot.Agents)
	joinedSummary := strings.Join(summaryLines, "\n")
	for _, want := range []string{"Claude: 2", "Antigravity: 1", "Grok: 1", "Cursor: 1", "Windsurf: 1", "Aider: 1", "OpenCode: 1", "Oh My Pi: 1", "Ollama: 1", "User: 1"} {
		if !strings.Contains(joinedSummary, want) {
			t.Fatalf("FormatAgentConfigSummary missing %q in:\n%s", want, joinedSummary)
		}
	}

	if _, err := session.Save(snapshot, session.SaveOptions{Name: "wave15-snap", Overwrite: true}); err != nil {
		t.Fatalf("session.Save: %v", err)
	}

	// 1. List active snapshots
	listOut, err := GetSavedSessions(SavedSessionsOptions{Action: "list"})
	if err != nil {
		t.Fatalf("GetSavedSessions(list): %v", err)
	}
	if !listOut.Success || listOut.Count == nil || *listOut.Count != 1 || strings.Compare(listOut.Sessions[0].Name, "wave15-snap") != 0 {
		t.Fatalf("unexpected list output: %+v", listOut)
	}
	if listOut.Sessions[0].Agents != 13 {
		t.Fatalf("expected total 13 agents across all harnesses + user, got %d", listOut.Sessions[0].Agents)
	}

	// 2. Show snapshot with fresh casr/native resume binding count
	showOut, err := GetSavedSessions(SavedSessionsOptions{Action: "show", Name: "wave15-snap"})
	if err != nil {
		t.Fatalf("GetSavedSessions(show): %v", err)
	}
	if !showOut.Success || showOut.State == nil || showOut.ResumablePanes == nil || *showOut.ResumablePanes != 2 {
		t.Fatalf("expected 2 resumable panes on show, got %+v", showOut)
	}

	// 3. Archive -> list archived -> unarchive
	archOut, err := GetSavedSessions(SavedSessionsOptions{Action: "archive", Name: "wave15-snap"})
	if err != nil || !archOut.Success {
		t.Fatalf("GetSavedSessions(archive): %+v (%v)", archOut, err)
	}
	archivedList, err := GetSavedSessions(SavedSessionsOptions{Action: "list", Archived: true})
	if err != nil || !archivedList.Success || archivedList.Count == nil || *archivedList.Count != 1 {
		t.Fatalf("expected 1 archived snapshot, got %+v (%v)", archivedList, err)
	}
	unarchOut, err := GetSavedSessions(SavedSessionsOptions{Action: "unarchive", Name: "wave15-snap"})
	if err != nil || !unarchOut.Success {
		t.Fatalf("GetSavedSessions(unarchive): %+v (%v)", unarchOut, err)
	}

	// 4. Planted negatives: missing snapshot -> SESSION_NOT_FOUND; unsupported action -> INVALID_FLAG
	missingShow, err := GetSavedSessions(SavedSessionsOptions{Action: "show", Name: "does-not-exist"})
	if err != nil {
		t.Fatalf("GetSavedSessions(missing show): %v", err)
	}
	if missingShow.Success || strings.Compare(missingShow.ErrorCode, ErrCodeSessionNotFound) != 0 {
		t.Fatalf("expected SESSION_NOT_FOUND for missing saved session, got %+v", missingShow)
	}

	badAction, err := GetSavedSessions(SavedSessionsOptions{Action: "delete", Name: "wave15-snap"})
	if err != nil {
		t.Fatalf("GetSavedSessions(delete): %v", err)
	}
	if badAction.Success || strings.Compare(badAction.ErrorCode, ErrCodeInvalidFlag) != 0 {
		t.Fatalf("expected INVALID_FLAG for unsupported delete action, got %+v", badAction)
	}
}

func TestWave15AgentMappingORIValidationAndNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	emptyOut, err := GetAgentMapping(context.Background(), AgentMappingOptions{})
	if err != nil {
		t.Fatalf("GetAgentMapping(empty): %v", err)
	}
	if emptyOut.Success || strings.Compare(emptyOut.ErrorCode, ErrCodeInvalidFlag) != 0 {
		t.Fatalf("expected INVALID_FLAG for empty session, got %+v", emptyOut)
	}

	missingOut, err := GetAgentMapping(context.Background(), AgentMappingOptions{
		Session:        "wave15_nonexistent_mapping_session_99999",
		RequireSession: true,
	})
	if err != nil {
		t.Fatalf("GetAgentMapping(missing): %v", err)
	}
	if missingOut.Success || strings.Compare(missingOut.ErrorCode, ErrCodeSessionNotFound) != 0 {
		t.Fatalf("expected SESSION_NOT_FOUND for missing tmux session, got %+v", missingOut)
	}
}

func TestWave15WorkflowsSequenceAndTemplatesORIEngines(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	projectDir := t.TempDir()

	// 1. Workflows list + show with trigger validation
	wfList, err := GetWorkflows(WorkflowsOptions{ProjectDir: projectDir})
	if err != nil {
		t.Fatalf("GetWorkflows(list): %v", err)
	}
	if !wfList.Success || wfList.Total == nil || *wfList.Total == 0 {
		t.Fatalf("expected built-in workflows in list, got %+v", wfList)
	}

	wfShow, err := GetWorkflows(WorkflowsOptions{ProjectDir: projectDir, Name: "red-green"})
	if err != nil {
		t.Fatalf("GetWorkflows(show red-green): %v", err)
	}
	if !wfShow.Success || wfShow.Workflow == nil || strings.Compare(wfShow.Workflow.Name, "red-green") != 0 {
		t.Fatalf("unexpected workflow show output: %+v", wfShow)
	}

	wfMissing, err := GetWorkflows(WorkflowsOptions{ProjectDir: projectDir, Name: "missing-workflow-xyz"})
	if err != nil {
		t.Fatalf("GetWorkflows(missing): %v", err)
	}
	if wfMissing.Success || strings.Compare(wfMissing.ErrorCode, ErrCodeWorkflowNotFound) != 0 {
		t.Fatalf("expected WORKFLOW_NOT_FOUND, got %+v", wfMissing)
	}

	// 2. Durable per-pane prompt sequence lifecycle (create -> next -> idempotent advance -> out-of-order rejection)
	created, err := ExecuteSequence(SequenceOptions{
		ProjectDir: projectDir,
		Name:       "wave15-audit",
		Action:     "create",
		Steps:      []string{"inspect", "verify", "summarize"},
	})
	if err != nil || !created.Success || created.Sequence == nil || len(created.Sequence.Steps) != 3 {
		t.Fatalf("ExecuteSequence(create): %+v (%v)", created, err)
	}
	nextPos, err := ExecuteSequence(SequenceOptions{
		ProjectDir: projectDir,
		Name:       "wave15-audit",
		Action:     "next",
		Pane:       "%1",
	})
	if err != nil || nextPos.Pane == nil || nextPos.Pane.Position != 0 || strings.Compare(nextPos.Pane.Prompt, "inspect") != 0 {
		t.Fatalf("ExecuteSequence(next): %+v (%v)", nextPos, err)
	}
	zero := 0
	adv, err := ExecuteSequence(SequenceOptions{
		ProjectDir:       projectDir,
		Name:             "wave15-audit",
		Action:           "advance",
		Pane:             "%1",
		ExpectedPosition: &zero,
	})
	if err != nil || adv.Pane == nil || !adv.Pane.Advanced || adv.Pane.Position != 1 {
		t.Fatalf("ExecuteSequence(advance): %+v (%v)", adv, err)
	}
	retry, err := ExecuteSequence(SequenceOptions{
		ProjectDir:       projectDir,
		Name:             "wave15-audit",
		Action:           "advance",
		Pane:             "%1",
		ExpectedPosition: &zero,
	})
	if err != nil || retry.Pane == nil || retry.Pane.Advanced || retry.Pane.Position != 1 {
		t.Fatalf("ExecuteSequence(idempotent retry) skipped step: %+v (%v)", retry, err)
	}
	neg := -1
	if _, err := ExecuteSequence(SequenceOptions{ProjectDir: projectDir, Name: "wave15-audit", Action: "advance", Pane: "%1", ExpectedPosition: &neg}); err == nil {
		t.Fatal("expected error for negative ExpectedPosition")
	}

	// 3. Session templates list + project override + show + missing suggestions
	tmplDir := filepath.Join(projectDir, ".ntm", "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm/templates): %v", err)
	}
	customYAML := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: wave15-squad
  description: Wave 15 custom squad
  tags: [wave15, agy]
spec:
  agents:
    claude:
      count: 2
    antigravity:
      count: 1
`
	if err := os.WriteFile(filepath.Join(tmplDir, "wave15-squad.yaml"), []byte(customYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(wave15-squad.yaml): %v", err)
	}

	stList, err := GetSessionTemplates(SessionTemplatesOptions{ProjectDir: projectDir})
	if err != nil {
		t.Fatalf("GetSessionTemplates(list): %v", err)
	}
	if !stList.Success || stList.Total == nil || *stList.Total == 0 {
		t.Fatalf("expected templates in list, got %+v", stList)
	}

	stShow, err := GetSessionTemplates(SessionTemplatesOptions{ProjectDir: projectDir, Name: "wave15-squad"})
	if err != nil {
		t.Fatalf("GetSessionTemplates(show wave15-squad): %v", err)
	}
	if !stShow.Success || stShow.Template == nil || stShow.Template.Agents == nil || stShow.Template.Agents.Antigravity == nil || stShow.Template.Agents.Antigravity.Count != 1 {
		t.Fatalf("unexpected custom template show output: %+v", stShow)
	}

	stMissing, err := GetSessionTemplates(SessionTemplatesOptions{ProjectDir: projectDir, Name: "missing-template-xyz"})
	if err != nil {
		t.Fatalf("GetSessionTemplates(missing): %v", err)
	}
	if stMissing.Success || strings.Compare(stMissing.ErrorCode, ErrCodeTemplateNotFound) != 0 || len(stMissing.Suggestions) == 0 {
		t.Fatalf("expected TEMPLATE_NOT_FOUND with suggestions, got %+v", stMissing)
	}
}
