package synthesizer

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

func TestNewModeVisualization(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	if !mv.Focused {
		t.Error("expected Focused to be true")
	}
	if mv.cursor != 0 {
		t.Errorf("cursor = %d, want 0", mv.cursor)
	}
	if mv.session != nil {
		t.Error("expected nil session")
	}
	if mv.catalog != nil {
		t.Error("expected nil catalog")
	}
}

func TestModeVisualization_SetSize(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	mv.SetSize(120, 40)

	if mv.Width != 120 {
		t.Errorf("Width = %d, want 120", mv.Width)
	}
	if mv.Height != 40 {
		t.Errorf("Height = %d, want 40", mv.Height)
	}
}

func TestModeVisualization_SetData_NilSession(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	mv.SetData("test", nil, nil, nil, nil)

	if mv.Session != "test" {
		t.Errorf("Session = %q, want %q", mv.Session, "test")
	}
	if mv.session != nil {
		t.Error("expected nil session")
	}
	if mv.Strategy != "" {
		t.Errorf("Strategy = %q, want empty", mv.Strategy)
	}
}

func TestModeVisualization_SetData_WithSession(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		SessionName:       "my-session",
		SynthesisStrategy: ensemble.StrategyConsensus,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "proj__cc_1", Status: ensemble.AssignmentActive},
			{ModeID: "bayesian", PaneName: "proj__cod_1", Status: ensemble.AssignmentPending},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("session-name", sess, nil, nil, nil)

	if mv.Strategy != "consensus" {
		t.Errorf("Strategy = %q, want %q", mv.Strategy, "consensus")
	}
}

func TestModeVisualization_SetData_PaneIndexMapping(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "proj__cc_1"},
		},
	}
	panes := []tmux.Pane{
		{Title: "proj__cc_1", Index: 0},
		{Title: "proj__cod_1", Index: 1},
		{Title: "", Index: 2}, // empty title should be skipped
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, panes, nil)

	if len(mv.paneIndex) != 2 {
		t.Errorf("paneIndex has %d entries, want 2 (empty title skipped)", len(mv.paneIndex))
	}
	if idx, ok := mv.paneIndex["proj__cc_1"]; !ok || idx != 0 {
		t.Errorf("paneIndex[proj__cc_1] = %d, %v; want 0, true", idx, ok)
	}
}

func TestModeVisualization_SetData_CursorClamp(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", Status: ensemble.AssignmentActive},
		},
	}

	mv := NewModeVisualization()
	mv.cursor = 10 // beyond range
	mv.SetData("test", sess, nil, nil, nil)

	if mv.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (clamped to max)", mv.cursor)
	}
}

func TestModeVisualization_Init(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	cmd := mv.Init()
	if cmd != nil {
		t.Error("Init should return nil cmd")
	}
}

func TestModeVisualization_Update_Close(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	updated, cmd := mv.Update(tea.KeyMsg{Type: tea.KeyEsc})

	_ = updated
	if cmd == nil {
		t.Fatal("expected non-nil cmd for close")
	}
	msg := cmd()
	if _, ok := msg.(CloseMsg); !ok {
		t.Errorf("expected CloseMsg, got %T", msg)
	}
}

func TestModeVisualization_Update_Refresh(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	_, cmd := mv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	if cmd == nil {
		t.Fatal("expected non-nil cmd for refresh")
	}
	msg := cmd()
	if _, ok := msg.(RefreshMsg); !ok {
		t.Errorf("expected RefreshMsg, got %T", msg)
	}
}

func TestModeVisualization_Update_NavigateDown(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentActive},
			{ModeID: "mode-b", Status: ensemble.AssignmentPending},
			{ModeID: "mode-c", Status: ensemble.AssignmentDone},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, nil, nil)

	updated, _ := mv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if updated.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", updated.cursor)
	}

	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if updated.cursor != 2 {
		t.Errorf("cursor after second down = %d, want 2", updated.cursor)
	}

	// Should not exceed max
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if updated.cursor != 2 {
		t.Errorf("cursor after third down = %d, want 2 (clamped)", updated.cursor)
	}
}

func TestModeVisualization_Update_NavigateUp(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentActive},
			{ModeID: "mode-b", Status: ensemble.AssignmentPending},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, nil, nil)
	mv.cursor = 1

	updated, _ := mv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if updated.cursor != 0 {
		t.Errorf("cursor after up = %d, want 0", updated.cursor)
	}

	// Should not go below 0
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if updated.cursor != 0 {
		t.Errorf("cursor after second up = %d, want 0 (clamped)", updated.cursor)
	}
}

func TestModeVisualization_Update_ZoomWithPaneIndex(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "proj__cc_1", Status: ensemble.AssignmentActive},
		},
	}
	panes := []tmux.Pane{
		{Title: "proj__cc_1", Index: 42},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, panes, nil)

	_, cmd := mv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for zoom")
	}
	msg := cmd()
	zm, ok := msg.(ZoomMsg)
	if !ok {
		t.Fatalf("expected ZoomMsg, got %T", msg)
	}
	if zm.PaneIndex != 42 {
		t.Errorf("ZoomMsg.PaneIndex = %d, want 42", zm.PaneIndex)
	}
}

func TestModeVisualization_Update_ZoomNoPaneIndex(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "unknown_pane", Status: ensemble.AssignmentActive},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, nil, nil)

	_, cmd := mv.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected nil cmd when pane not found in index")
	}
}

func TestModeVisualization_View_NoSession(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	mv.SetSize(80, 24)

	view := mv.View()
	if !strings.Contains(view, "No ensemble session") {
		t.Errorf("expected 'No ensemble session' message, got %q", view)
	}
}

func TestModeVisualization_View_EmptyAssignments(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{},
	}

	mv := NewModeVisualization()
	mv.SetSize(80, 24)
	mv.SetData("test", sess, nil, nil, nil)

	view := mv.View()
	if !strings.Contains(view, "No ensemble assignments") {
		t.Errorf("expected empty assignments message, got %q", view)
	}
}

func TestModeVisualization_View_WithError(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	mv.SetSize(80, 24)
	mv.SetData("test", nil, nil, nil, errForTest("connection failed"))

	view := mv.View()
	if !strings.Contains(view, "connection failed") {
		t.Errorf("expected error message in view, got %q", view)
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }

func TestModeVisualization_View_WithAssignments(t *testing.T) {
	t.Parallel()

	modes := []ensemble.ReasoningMode{
		{ID: "deductive", Code: "A1", Name: "Deductive Logic", Category: ensemble.CategoryFormal, Tier: ensemble.TierCore, ShortDesc: "Formal deductive logic"},
		{ID: "bayesian", Code: "C2", Name: "Bayesian Analysis", Category: ensemble.CategoryUncertainty, Tier: ensemble.TierAdvanced, ShortDesc: "Bayes"},
	}
	catalog, err := ensemble.NewModeCatalog(modes, "test")
	if err != nil {
		t.Fatalf("NewModeCatalog: %v", err)
	}

	sess := &ensemble.EnsembleSession{
		SessionName:       "my-ensemble",
		SynthesisStrategy: ensemble.StrategyConsensus,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "deductive", PaneName: "proj__cc_1", AgentType: "cc", Status: ensemble.AssignmentActive},
			{ModeID: "bayesian", PaneName: "proj__cod_1", AgentType: "cod", Status: ensemble.AssignmentDone},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(100, 30)
	mv.SetData("my-ensemble", sess, catalog, nil, nil)

	view := mv.View()

	if !strings.Contains(view, "my-ensemble") {
		t.Errorf("expected session name in view, got %q", view)
	}
	if !strings.Contains(view, "Reasoning Modes") {
		t.Errorf("expected title in view, got %q", view)
	}
	if !strings.Contains(view, "A1") {
		t.Errorf("expected mode code A1 in view, got %q", view)
	}
	if !strings.Contains(view, "C2") {
		t.Errorf("expected mode code C2 in view, got %q", view)
	}
	if !strings.Contains(view, "consensus") {
		t.Errorf("expected strategy in footer, got %q", view)
	}
}

func TestModeVisualization_View_CursorIndicator(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentActive},
			{ModeID: "mode-b", Status: ensemble.AssignmentPending},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(80, 24)
	mv.SetData("test", sess, nil, nil, nil)

	view := mv.View()
	if !strings.Contains(view, "›") {
		t.Errorf("expected cursor indicator in view, got %q", view)
	}
}

func TestModeVisualization_View_Footer(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		SynthesisStrategy: ensemble.StrategyAnalytical,
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentDone},
			{ModeID: "mode-b", Status: ensemble.AssignmentActive},
			{ModeID: "mode-c", Status: ensemble.AssignmentDone},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(80, 30)
	mv.SetData("test", sess, nil, nil, nil)

	view := mv.View()
	if !strings.Contains(view, "2/3 complete") {
		t.Errorf("expected completion count, got %q", view)
	}
	if !strings.Contains(view, "analytical") {
		t.Errorf("expected strategy in footer, got %q", view)
	}
}

func TestModeVisualization_View_DefaultDimensions(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	// Width/Height = 0 should use defaults (80x24)

	view := mv.View()
	if view == "" {
		t.Error("expected non-empty view with default dimensions")
	}
}

func TestModeVisualization_View_AgentTypes(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", AgentType: "cc", Status: ensemble.AssignmentActive},
			{ModeID: "mode-b", AgentType: "cod", Status: ensemble.AssignmentPending},
			{ModeID: "mode-c", AgentType: "gmi", Status: ensemble.AssignmentDone},
			{ModeID: "mode-d", AgentType: "openai-codex", Status: ensemble.AssignmentDone},
			{ModeID: "mode-e", AgentType: "ws", Status: ensemble.AssignmentDone},
			{ModeID: "mode-f", AgentType: "user", Status: ensemble.AssignmentDone},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(100, 30)
	mv.SetData("test", sess, nil, nil, nil)

	view := status.StripANSI(mv.View())
	if !strings.Contains(view, "cc") {
		t.Errorf("expected 'cc' agent type in view, got %q", view)
	}
	if !strings.Contains(view, "cod") {
		t.Errorf("expected 'cod' agent type in view, got %q", view)
	}
	if !strings.Contains(view, "gmi") {
		t.Errorf("expected 'gmi' agent type in view, got %q", view)
	}
	if strings.Contains(view, "openai-codex") {
		t.Errorf("expected canonicalized agent label, got raw alias in %q", view)
	}
	if !strings.Contains(view, "ws") {
		t.Errorf("expected 'ws' agent type in view, got %q", view)
	}
	if !strings.Contains(view, "usr") {
		t.Errorf("expected 'usr' agent type in view, got %q", view)
	}
}

func TestModeAssignmentAgentLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude alias", "claude-code", "cc"},
		{"codex alias", "openai-codex", "cod"},
		{"gemini alias", "google-gemini", "gmi"},
		{"cursor", "cursor", "cur"},
		{"windsurf alias", "ws", "ws"},
		{"aider", "aider", "aid"},
		{"ollama", "ollama", "oll"},
		{"user", "user", "usr"},
		{"unknown", "mystery", "mys"},
		{"empty", "", "unk"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := modeAssignmentAgentLabel(tc.agentType); got != tc.want {
				t.Fatalf("modeAssignmentAgentLabel(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestModeAssignmentAgentColor(t *testing.T) {
	t.Parallel()

	current := theme.Current()
	tests := []struct {
		name      string
		agentType string
		want      string
	}{
		{"claude", "claude", string(current.Claude)},
		{"codex alias", "openai-codex", string(current.Codex)},
		{"gemini alias", "google-gemini", string(current.Gemini)},
		{"cursor", "cursor", string(current.Cursor)},
		{"windsurf alias", "ws", string(current.Windsurf)},
		{"aider", "aider", string(current.Aider)},
		{"ollama", "ollama", string(current.Ollama)},
		{"user", "user", string(current.User)},
		{"unknown", "mystery", string(current.Subtext)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := string(modeAssignmentAgentColor(tc.agentType, current)); got != tc.want {
				t.Fatalf("modeAssignmentAgentColor(%q) = %q, want %q", tc.agentType, got, tc.want)
			}
		})
	}
}

func TestSelectedPaneIndex_NilSession(t *testing.T) {
	t.Parallel()

	mv := NewModeVisualization()
	_, ok := mv.selectedPaneIndex()
	if ok {
		t.Error("expected ok=false for nil session")
	}
}

func TestSelectedPaneIndex_EmptyPaneName(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "", Status: ensemble.AssignmentActive},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, nil, nil)

	_, ok := mv.selectedPaneIndex()
	if ok {
		t.Error("expected ok=false for empty pane name")
	}
}

func TestSelectedPaneIndex_CursorOutOfRange(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", PaneName: "proj__cc_1", Status: ensemble.AssignmentActive},
		},
	}

	mv := NewModeVisualization()
	mv.SetData("test", sess, nil, nil, nil)
	mv.cursor = -1

	_, ok := mv.selectedPaneIndex()
	if ok {
		t.Error("expected ok=false for negative cursor")
	}
}

func TestSummarizeCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		assign    []ensemble.ModeAssignment
		wantDone  int
		wantTotal int
	}{
		{"empty", nil, 0, 0},
		{"all done", []ensemble.ModeAssignment{
			{Status: ensemble.AssignmentDone},
			{Status: ensemble.AssignmentDone},
		}, 2, 2},
		{"mixed", []ensemble.ModeAssignment{
			{Status: ensemble.AssignmentDone},
			{Status: ensemble.AssignmentActive},
			{Status: ensemble.AssignmentPending},
		}, 1, 3},
		{"none done", []ensemble.ModeAssignment{
			{Status: ensemble.AssignmentActive},
			{Status: ensemble.AssignmentPending},
		}, 0, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done, total := summarizeCompletion(tc.assign)
			if done != tc.wantDone {
				t.Errorf("done = %d, want %d", done, tc.wantDone)
			}
			if total != tc.wantTotal {
				t.Errorf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

func TestAssignmentCount(t *testing.T) {
	t.Parallel()

	if got := assignmentCount(nil); got != 0 {
		t.Errorf("assignmentCount(nil) = %d, want 0", got)
	}

	sess := &ensemble.EnsembleSession{
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "a"}, {ModeID: "b"}, {ModeID: "c"},
		},
	}
	if got := assignmentCount(sess); got != 3 {
		t.Errorf("assignmentCount = %d, want 3", got)
	}
}

func TestAssignmentProgress_Synthesizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ensemble.AssignmentStatus
		want   float64
	}{
		{ensemble.AssignmentPending, 0.05},
		{ensemble.AssignmentInjecting, 0.25},
		{ensemble.AssignmentActive, 0.6},
		{ensemble.AssignmentDone, 1.0},
		{ensemble.AssignmentError, 1.0},
		{ensemble.AssignmentStatus("unknown"), 0},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := assignmentProgress(tc.status)
			if got != tc.want {
				t.Errorf("assignmentProgress(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestAssignmentStatusIcon_Synthesizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ensemble.AssignmentStatus
		want   string
	}{
		{ensemble.AssignmentActive, "●"},
		{ensemble.AssignmentInjecting, "◐"},
		{ensemble.AssignmentPending, "○"},
		{ensemble.AssignmentDone, "✓"},
		{ensemble.AssignmentError, "✗"},
		{ensemble.AssignmentStatus("unknown"), "•"},
	}

	for _, tc := range tests {
		t.Run(string(tc.status), func(t *testing.T) {
			got := assignmentStatusIcon(tc.status)
			if got != tc.want {
				t.Errorf("assignmentStatusIcon(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestFitToHeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		height  int
		want    int // expected number of lines
	}{
		{"exact fit", "line1\nline2\nline3", 3, 3},
		{"pad short", "line1", 3, 3},
		{"truncate long", "a\nb\nc\nd\ne", 3, 3},
		{"zero height", "line1\nline2", 0, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fitToHeight(tc.content, tc.height)
			lines := strings.Split(got, "\n")
			if tc.height <= 0 {
				// Should return content as-is
				if got != tc.content {
					t.Errorf("fitToHeight with height=0 modified content")
				}
				return
			}
			if len(lines) != tc.want {
				t.Errorf("got %d lines, want %d", len(lines), tc.want)
			}
		})
	}
}

func TestClampInt_Synthesizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		v, lo, hi int
		want      int
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{15, 0, 10, 10},
	}

	for _, tc := range tests {
		got := clampInt(tc.v, tc.lo, tc.hi)
		if got != tc.want {
			t.Errorf("clampInt(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestMinInt_Synthesizer(t *testing.T) {
	t.Parallel()

	if got := minInt(3, 5); got != 3 {
		t.Errorf("minInt(3, 5) = %d, want 3", got)
	}
	if got := minInt(7, 2); got != 2 {
		t.Errorf("minInt(7, 2) = %d, want 2", got)
	}
}

func TestMaxInt_Synthesizer(t *testing.T) {
	t.Parallel()

	if got := maxInt(3, 5); got != 5 {
		t.Errorf("maxInt(3, 5) = %d, want 5", got)
	}
	if got := maxInt(7, 2); got != 7 {
		t.Errorf("maxInt(7, 2) = %d, want 7", got)
	}
}

func TestDefaultKeys(t *testing.T) {
	t.Parallel()

	checks := map[string]key.Binding{
		"Up":      defaultKeys.Up,
		"Down":    defaultKeys.Down,
		"Refresh": defaultKeys.Refresh,
		"Zoom":    defaultKeys.Zoom,
		"Close":   defaultKeys.Close,
	}

	for name, binding := range checks {
		keys := binding.Keys()
		if len(keys) == 0 {
			t.Errorf("defaultKeys.%s has no keys", name)
		}
	}
}

func TestModeVisualization_View_EmptySessionName(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		SessionName: "",
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentActive},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(80, 24)
	mv.SetData("", sess, nil, nil, nil)

	view := mv.View()
	// Empty session name should show dash
	if !strings.Contains(view, "—") {
		t.Errorf("expected dash for empty session name, got %q", view)
	}
}

func TestModeVisualization_View_EmptyStrategy(t *testing.T) {
	t.Parallel()

	sess := &ensemble.EnsembleSession{
		SynthesisStrategy: "",
		Assignments: []ensemble.ModeAssignment{
			{ModeID: "mode-a", Status: ensemble.AssignmentDone},
		},
	}

	mv := NewModeVisualization()
	mv.SetSize(80, 30)
	mv.SetData("test", sess, nil, nil, nil)

	view := mv.View()
	// Empty strategy should show dash in footer
	if !strings.Contains(view, "—") {
		t.Errorf("expected dash for empty strategy, got %q", view)
	}
}
