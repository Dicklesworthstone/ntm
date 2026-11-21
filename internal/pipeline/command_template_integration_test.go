package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestIntegrationCommandAndTemplatePipeline(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	templatePath := filepath.Join(projectDir, "fixture-template.md")
	if err := os.WriteFile(templatePath, []byte("Template dispatch says <PARAM>"), 0o644); err != nil {
		t.Fatalf("WriteFile(template) returned error: %v", err)
	}

	mock := NewMockTmuxClient(tmux.Pane{
		ID:    "%1",
		Index: 1,
		Type:  tmux.AgentCodex,
	})
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("command-template-session")
	cfg.ProjectDir = projectDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "command-template-integration",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID:      "write_file",
				Command: `printf '%s' hello > test-out.txt`,
			},
			{
				ID:        "render_template",
				Template:  "fixture-template.md",
				Params:    map[string]interface{}{"PARAM": "world"},
				Pane:      PaneSpec{Index: 1},
				Wait:      WaitNone,
				DependsOn: []string{"write_file"},
			},
			{
				ID:        "read_file",
				Command:   `cat test-out.txt`,
				OutputVar: "file_contents",
				DependsOn: []string{"render_template"},
			},
			{
				ID:        "cleanup_file",
				Command:   `rm test-out.txt`,
				DependsOn: []string{"read_file"},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}

	assertCompletedStepOutput(t, state, "write_file", "")
	assertCompletedStepOutput(t, state, "render_template", "")
	assertCompletedStepOutput(t, state, "read_file", "hello")
	assertCompletedStepOutput(t, state, "cleanup_file", "")
	if got := state.Variables["file_contents"]; got != "hello" {
		t.Fatalf("file_contents output var = %#v, want hello", got)
	}

	history, err := mock.PasteHistory("%1")
	if err != nil {
		t.Fatalf("PasteHistory returned error: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory length = %d, want one template dispatch", len(history))
	}
	if !history[0].Enter {
		t.Fatal("template dispatch did not send Enter")
	}
	for _, want := range []string{"Template dispatch says world"} {
		if !strings.Contains(history[0].Content, want) {
			t.Fatalf("template dispatch missing %q:\n%s", want, history[0].Content)
		}
	}

	if _, err := os.Stat(filepath.Join(projectDir, "test-out.txt")); !os.IsNotExist(err) {
		t.Fatalf("cleanup_file left test-out.txt stat error = %v, want not exist", err)
	}
}

func assertCompletedStepOutput(t *testing.T, state *ExecutionState, stepID string, wantOutput string) {
	t.Helper()
	result, ok := state.Steps[stepID]
	if !ok {
		t.Fatalf("missing result for step %s", stepID)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("%s status = %q, want %q; error = %#v", stepID, result.Status, StatusCompleted, result.Error)
	}
	if result.Output != wantOutput {
		t.Fatalf("%s output = %q, want %q", stepID, result.Output, wantOutput)
	}
}

// bd-6lkqr.6: end-to-end coverage for the substitution layer driving a
// foreach_pane → template dispatch (the brennerbot MO-02-onboarding shape).
// Each pane is bound to its own role/model/domain via tags; the body step
// renders a fixture template whose params reference ${pane.X}; we verify
// every pane received a distinct rendered dispatch carrying its own values.
func TestIntegrationForeachPaneTemplateRendersPerPaneSubstitution(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	templatePath := filepath.Join(projectDir, "mo-onboarding.md")
	body := "Onboarding <ROLE> on <MODEL> covering <DOMAIN>\n" +
		"**Parameters:** <ROLE> <MODEL> <DOMAIN>\n"
	if err := os.WriteFile(templatePath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(template) returned error: %v", err)
	}

	mock := NewMockTmuxClient(
		tmux.Pane{ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude,
			Tags: []string{"role=investigator", "model=claude-opus-4-7", "domain=runtime"}},
		tmux.Pane{ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentCodex,
			Tags: []string{"role=adjudicator", "model=gpt-5-codex", "domain=audit"}},
		tmux.Pane{ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini,
			Tags: []string{"role=investigator", "model=gemini-3-pro", "domain=docs"}},
	)
	t.Cleanup(mock.Reset)

	cfg := DefaultExecutorConfig("mo-onboarding-session")
	cfg.ProjectDir = projectDir
	cfg.DefaultTimeout = 10 * time.Second
	executor := NewExecutor(cfg)
	executor.SetTmuxClient(mock)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "mo-onboarding-substitution",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{
				ID: "fanout",
				ForeachPane: &ForeachConfig{
					Steps: []Step{
						{
							ID:       "send_mo",
							Template: "mo-onboarding.md",
							Params: map[string]interface{}{
								"ROLE":   "${pane.role}",
								"MODEL":  "${pane.model}",
								"DOMAIN": "${pane.domain}",
							},
							Wait: WaitNone,
						},
					},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("workflow status = %q, want %q", state.Status, StatusCompleted)
	}

	wantPerPane := map[string]string{
		"%1": "Onboarding investigator on claude-opus-4-7 covering runtime",
		"%2": "Onboarding adjudicator on gpt-5-codex covering audit",
		"%3": "Onboarding investigator on gemini-3-pro covering docs",
	}
	for paneID, want := range wantPerPane {
		history, err := mock.PasteHistory(paneID)
		if err != nil {
			t.Fatalf("PasteHistory(%s) returned error: %v", paneID, err)
		}
		if len(history) != 1 {
			t.Fatalf("PasteHistory(%s) length = %d, want 1", paneID, len(history))
		}
		if !history[0].Enter {
			t.Fatalf("PasteHistory(%s) did not send Enter", paneID)
		}
		if !strings.Contains(history[0].Content, want) {
			t.Fatalf("PasteHistory(%s) missing %q:\n%s", paneID, want, history[0].Content)
		}
		for otherPane, otherWant := range wantPerPane {
			if otherPane == paneID {
				continue
			}
			if strings.Contains(history[0].Content, otherWant) {
				t.Fatalf("PasteHistory(%s) leaked another pane's substitution %q:\n%s",
					paneID, otherWant, history[0].Content)
			}
		}
	}
}
