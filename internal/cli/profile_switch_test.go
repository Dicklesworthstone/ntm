package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestProfileSwitchJSONFailuresUseSingleTerminalSentinel(t *testing.T) {
	previousJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = previousJSON })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, runErr := captureStdout(t, func() error {
		return runProfileSwitch(ctx, "cod_1", "reviewer", "", "", false, false)
	})
	if !errors.Is(runErr, errJSONFailure) || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("profile switch error = %v, want JSON sentinel and context cancellation", runErr)
	}
	var result ProfileSwitchResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode profile switch failure: %v\noutput=%s", err, output)
	}
	if result.Success || result.AgentID != "cod_1" || result.NewProfile != "reviewer" || result.Error == "" {
		t.Fatalf("profile switch failure = %+v", result)
	}
}

func TestParseAgentID(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantType  string
		wantIndex int
		wantErr   bool
	}{
		{
			name:      "claude agent 1",
			input:     "cc_1",
			wantType:  "cc",
			wantIndex: 1,
			wantErr:   false,
		},
		{
			name:      "claude agent 2",
			input:     "cc_2",
			wantType:  "cc",
			wantIndex: 2,
			wantErr:   false,
		},
		{
			name:      "codex agent 1",
			input:     "cod_1",
			wantType:  "cod",
			wantIndex: 1,
			wantErr:   false,
		},
		{
			name:      "gemini agent 3",
			input:     "gmi_3",
			wantType:  "gmi",
			wantIndex: 3,
			wantErr:   false,
		},
		{
			name:      "large index",
			input:     "cc_99",
			wantType:  "cc",
			wantIndex: 99,
			wantErr:   false,
		},
		{
			name:      "cursor agent",
			input:     "cursor_1",
			wantType:  "cursor",
			wantIndex: 1,
			wantErr:   false,
		},
		{
			name:      "windsurf agent",
			input:     "windsurf_2",
			wantType:  "windsurf",
			wantIndex: 2,
			wantErr:   false,
		},
		{
			name:      "windsurf short alias",
			input:     "ws_3",
			wantType:  "windsurf",
			wantIndex: 3,
			wantErr:   false,
		},
		{
			name:      "aider agent",
			input:     "aider_4",
			wantType:  "aider",
			wantIndex: 4,
			wantErr:   false,
		},
		{
			name:      "ollama agent",
			input:     "ollama_5",
			wantType:  "ollama",
			wantIndex: 5,
			wantErr:   false,
		},
		{
			name:      "claude alias canonicalizes",
			input:     "claude_code_6",
			wantType:  "cc",
			wantIndex: 6,
			wantErr:   false,
		},
		{
			name:    "invalid type",
			input:   "xxx_1",
			wantErr: true,
		},
		{
			name:    "missing underscore",
			input:   "cc1",
			wantErr: true,
		},
		{
			name:    "missing index",
			input:   "cc_",
			wantErr: true,
		},
		{
			name:    "invalid index",
			input:   "cc_abc",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "extra underscore",
			input:   "cc_1_extra",
			wantErr: true,
		},
		{
			name:    "user unsupported",
			input:   "user_1",
			wantErr: true,
		},
		{
			name:    "zero index unsupported",
			input:   "cursor_0",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agentType, index, err := parseAgentID(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("parseAgentID(%q) expected error, got none", tt.input)
				}
				return
			}

			if err != nil {
				t.Errorf("parseAgentID(%q) unexpected error: %v", tt.input, err)
				return
			}

			if agentType != tt.wantType {
				t.Errorf("parseAgentID(%q) type = %q, want %q", tt.input, agentType, tt.wantType)
			}
			if index != tt.wantIndex {
				t.Errorf("parseAgentID(%q) index = %d, want %d", tt.input, index, tt.wantIndex)
			}
		})
	}
}

func TestFindPaneByAgentID(t *testing.T) {
	session := "myproject"
	panes := []tmux.Pane{
		{ID: "%0", Title: "myproject__cc_1[frontend]", Index: 0},
		{ID: "%1", Title: "myproject__cc_2_architect", Index: 1},
		{ID: "%2", Title: "myproject__cod_1_reviewer", Index: 2},
		{ID: "%3", Title: "myproject__gmi_1", Index: 3},
		{ID: "%4", Title: "myproject__cursor_1_pairing[ux]", Index: 4},
		{ID: "%5", Title: "myproject__openai-codex_2_ops[backend]", Index: 5},
	}

	tests := []struct {
		name       string
		agentType  string
		agentIndex int
		wantPaneID string
		wantOld    string
		wantErr    bool
	}{
		{
			name:       "find cc_1 no profile",
			agentType:  "cc",
			agentIndex: 1,
			wantPaneID: "%0",
			wantOld:    "",
			wantErr:    false,
		},
		{
			name:       "find cc_2 with architect profile",
			agentType:  "cc",
			agentIndex: 2,
			wantPaneID: "%1",
			wantOld:    "architect",
			wantErr:    false,
		},
		{
			name:       "find cod_1 with reviewer profile",
			agentType:  "cod",
			agentIndex: 1,
			wantPaneID: "%2",
			wantOld:    "reviewer",
			wantErr:    false,
		},
		{
			name:       "find gmi_1 no profile",
			agentType:  "gmi",
			agentIndex: 1,
			wantPaneID: "%3",
			wantOld:    "",
			wantErr:    false,
		},
		{
			name:       "find cursor_1 with tagged profile",
			agentType:  "cursor",
			agentIndex: 1,
			wantPaneID: "%4",
			wantOld:    "pairing",
			wantErr:    false,
		},
		{
			name:       "find cod_2 with legacy aliased title and tags",
			agentType:  "cod",
			agentIndex: 2,
			wantPaneID: "%5",
			wantOld:    "ops",
			wantErr:    false,
		},
		{
			name:       "not found cc_3",
			agentType:  "cc",
			agentIndex: 3,
			wantErr:    true,
		},
		{
			name:       "not found ollama_1",
			agentType:  "ollama",
			agentIndex: 1,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane, oldProfile, err := findPaneByAgentID(panes, session, tt.agentType, tt.agentIndex)

			if tt.wantErr {
				if err == nil {
					t.Errorf("findPaneByAgentID expected error, got none")
				}
				return
			}

			if err != nil {
				t.Errorf("findPaneByAgentID unexpected error: %v", err)
				return
			}

			if pane.ID != tt.wantPaneID {
				t.Errorf("findPaneByAgentID pane ID = %q, want %q", pane.ID, tt.wantPaneID)
			}
			if oldProfile != tt.wantOld {
				t.Errorf("findPaneByAgentID old profile = %q, want %q", oldProfile, tt.wantOld)
			}
		})
	}
}

func TestFormatProfileSwitchPaneTitlePreservesTags(t *testing.T) {

	got := formatProfileSwitchPaneTitle("myproject", "cursor", 2, "reviewer", []string{"frontend", "ux"})
	want := "myproject__cursor_2_reviewer[frontend,ux]"
	if got != want {
		t.Fatalf("formatProfileSwitchPaneTitle() = %q, want %q", got, want)
	}
}

func TestGenerateTransitionPrompt(t *testing.T) {
	newProfile := &persona.Persona{
		Name:          "reviewer",
		Description:   "Code reviewer focused on quality",
		SystemPrompt:  "You are a code reviewer.",
		FocusPatterns: []string{"**/*.go", "**/*.ts"},
	}

	tests := []struct {
		name         string
		oldProfile   string
		newProfile   *persona.Persona
		wantContains []string
	}{
		{
			name:       "with old profile",
			oldProfile: "architect",
			newProfile: newProfile,
			wantContains: []string{
				"Profile Switch Notification",
				"transitioning from the 'architect' profile",
				"to the 'reviewer' profile",
				"Code reviewer focused on quality",
				"You are a code reviewer",
				"**/*.go",
				"acknowledge this profile change",
			},
		},
		{
			name:       "without old profile",
			oldProfile: "",
			newProfile: newProfile,
			wantContains: []string{
				"Profile Switch Notification",
				"adopting the 'reviewer' profile",
				"Code reviewer focused on quality",
				"You are a code reviewer",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateTransitionPrompt(tt.oldProfile, tt.newProfile, tt.newProfile.SystemPrompt, "")

			for _, want := range tt.wantContains {
				if !containsString(result, want) {
					t.Errorf("generateTransitionPrompt() missing expected content: %q\nGot:\n%s", want, result)
				}
			}
		})
	}
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestPrepareProfileSwitchPromptUsesRenderedContext(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "CONTEXT.md"), []byte("project context"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &persona.Persona{
		Name:         "reviewer",
		AgentType:    "claude",
		SystemPrompt: "Review {{project_name}}",
		ContextFiles: []string{"CONTEXT.md"},
	}
	path, rendered, err := prepareProfileSwitchPrompt(p, project)
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || !strings.Contains(rendered, "project context") || !strings.Contains(rendered, "Review "+filepath.Base(project)) {
		t.Fatalf("prepared switch prompt path=%q rendered=%q", path, rendered)
	}
	transition := generateTransitionPrompt("architect", p, rendered, path)
	if !strings.Contains(transition, "project context") || !strings.Contains(transition, "Review "+filepath.Base(project)) || !strings.Contains(transition, path) {
		t.Fatalf("transition did not carry rendered persona contract: %q", transition)
	}
}

func TestPrepareProfileSwitchPromptFailsClosedOnContextError(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &persona.Persona{
		Name:         "reviewer",
		AgentType:    "claude",
		SystemPrompt: "must retain context",
		ContextFiles: []string{"docs"},
	}
	if path, rendered, err := prepareProfileSwitchPrompt(p, project); err == nil || path != "" || rendered != "" {
		t.Fatalf("invalid profile context was accepted: path=%q rendered=%q err=%v", path, rendered, err)
	}
}

func TestGenerateTransitionPromptUsesRenderedPromptInsteadOfRawTemplate(t *testing.T) {
	p := &persona.Persona{
		Name:         "reviewer",
		Description:  "review",
		SystemPrompt: "Review {{project_name}}",
	}
	rendered := "# Context Files\n\n## RULES.md\n\nproject rule\n\n---\n\nReview ntm"
	got := generateTransitionPrompt("", p, rendered, "/tmp/reviewer-hash.md")
	if !strings.Contains(got, rendered) || strings.Contains(got, "Review {{project_name}}") || !strings.Contains(got, "/tmp/reviewer-hash.md") {
		t.Fatalf("transition prompt did not use rendered profile: %q", got)
	}
}

func TestSupportsProfileSwitchAgentType_MultiHarness(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		agentType tmux.AgentType
		want      bool
	}{
		{tmux.AgentAntigravity, true},
		{tmux.AgentType("antigravity"), true},
		{tmux.AgentGrok, true},
		{tmux.AgentType("grok-build"), true},
		{tmux.AgentOmp, true},
		{tmux.AgentOpencode, true},
		{tmux.AgentUser, false},
		{tmux.AgentUnknown, false},
	} {
		if got := supportsProfileSwitchAgentType(tc.agentType); got != tc.want {
			t.Errorf("supportsProfileSwitchAgentType(%q) = %v, want %v", tc.agentType, got, tc.want)
		}
	}

	for _, tc := range []struct {
		id       string
		wantType string
		wantIdx  int
	}{
		{"agy_1", "agy", 1},
		{"antigravity_2", "agy", 2},
		{"grok_1", "grok", 1},
		{"grok-build_3", "grok", 3},
		{"omp_1", "omp", 1},
		{"opencode_2", "oc", 2},
	} {
		gotType, gotIdx, err := parseAgentID(tc.id)
		if err != nil {
			t.Fatalf("parseAgentID(%q) unexpected error: %v", tc.id, err)
		}
		if gotType != tc.wantType || gotIdx != tc.wantIdx {
			t.Errorf("parseAgentID(%q) = (%q, %d), want (%q, %d)", tc.id, gotType, gotIdx, tc.wantType, tc.wantIdx)
		}
	}

	panes := []tmux.Pane{
		{ID: "%1", Title: "proj__agy_1_architect", Type: tmux.AgentAntigravity, NTMIndex: 1, Variant: "architect"},
		{ID: "%2", Title: "proj__grok_2_debugger", Type: tmux.AgentGrok, NTMIndex: 2, Variant: "debugger"},
	}
	if p, oldProf, err := findPaneByAgentID(panes, "proj", "antigravity", 1); err != nil || p.ID != "%1" || oldProf != "architect" {
		t.Fatalf("findPaneByAgentID(antigravity_1) = (%+v, %q, %v), want %%1/architect/nil", p, oldProf, err)
	}
	if p, oldProf, err := findPaneByAgentID(panes, "proj", "grok-build", 2); err != nil || p.ID != "%2" || oldProf != "debugger" {
		t.Fatalf("findPaneByAgentID(grok-build_2) = (%+v, %q, %v), want %%2/debugger/nil", p, oldProf, err)
	}
}
