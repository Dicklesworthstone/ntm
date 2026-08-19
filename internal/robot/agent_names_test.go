package robot

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNATOAlphabetLength(t *testing.T) {
	if len(NATOAlphabet) != 26 {
		t.Errorf("NATOAlphabet should have 26 entries, got %d", len(NATOAlphabet))
	}
}

func TestAssignNew(t *testing.T) {
	m := NewAgentNameMap("test-session")

	name1 := m.AssignNew("claude", "0.1")
	name2 := m.AssignNew("codex", "0.2")
	name3 := m.AssignNew("gemini", "0.3")

	if name1 != "claude-alpha" {
		t.Errorf("first AssignNew = %q, want claude-alpha", name1)
	}
	if name2 != "codex-bravo" {
		t.Errorf("second AssignNew = %q, want codex-bravo", name2)
	}
	if name3 != "gemini-charlie" {
		t.Errorf("third AssignNew = %q, want gemini-charlie", name3)
	}

	// Verify all mappings are correct
	entries := m.AllNames()
	if len(entries) != 3 {
		t.Errorf("AllNames() returned %d entries, want 3", len(entries))
	}
	if entries[0].Name != "claude-alpha" || entries[0].Pane != "0.1" {
		t.Errorf("first entry = %+v, want claude-alpha at 0.1", entries[0])
	}
}

func TestAllNames(t *testing.T) {
	m := NewAgentNameMap("test-session")

	m.AssignNew("claude", "0.1")
	m.AssignNew("codex", "0.2")
	m.AssignNew("user", "0.0")

	entries := m.AllNames()

	if len(entries) != 3 {
		t.Fatalf("AllNames() returned %d entries, want 3", len(entries))
	}

	// Should be sorted by pane reference
	if entries[0].Pane != "0.0" {
		t.Errorf("first entry pane = %q, want 0.0", entries[0].Pane)
	}
	if entries[1].Pane != "0.1" {
		t.Errorf("second entry pane = %q, want 0.1", entries[1].Pane)
	}
	if entries[2].Pane != "0.2" {
		t.Errorf("third entry pane = %q, want 0.2", entries[2].Pane)
	}
}

func TestDetectAgentTypeFromTitle(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"myproj__cc_1", "claude"},
		{"myproj__cc_2", "claude"},
		{"myproj__cod_1", "codex"},
		{"myproj__gmi_1", "gemini"},
		{"myproj__cursor_1", "cursor"},
		{"myproj__windsurf_1", "windsurf"},
		{"myproj__ws_1", "windsurf"},
		{"myproj__aider_1", "aider"},
		{"myproj__ollama_1", "ollama"},
		{"myproj__user", "user"},
		{"myproj__claude_1", "claude"},
		{"myproj__codex_1", "codex"},
		{"myproj__gemini_1", "gemini"},
		{"my__proj__cc_1", "claude"},
		{"myproj__ccidental_1", ""},
		{"random_title", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := detectAgentTypeFromTitle(tt.title)
		if got != tt.want {
			t.Errorf("detectAgentTypeFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestParseCustomNames(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"alice,bob,charlie", []string{"alice", "bob", "charlie"}},
		{" alice , bob , charlie ", []string{"alice", "bob", "charlie"}},
		{"single", []string{"single"}},
		{"a,,b", []string{"a", "b"}}, // Empty parts are skipped
	}

	for _, tt := range tests {
		got := ParseCustomNames(tt.input)
		if tt.want == nil && got != nil {
			t.Errorf("ParseCustomNames(%q) = %v, want nil", tt.input, got)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("ParseCustomNames(%q) = %v (len=%d), want %v (len=%d)", tt.input, got, len(got), tt.want, len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseCustomNames(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestGetAgentNamesNoTmux(t *testing.T) {
	// Override tmux functions for testing
	origInstalled := tmuxInstalledFn
	defer func() { tmuxInstalledFn = origInstalled }()
	tmuxInstalledFn = func() bool { return false }

	output, err := GetAgentNames("test-session", nil)
	if err != nil {
		t.Fatalf("GetAgentNames returned error: %v", err)
	}
	if output.Success {
		t.Error("expected success=false when tmux not installed")
	}
	if output.ErrorCode != ErrCodeDependencyMissing {
		t.Errorf("expected error code %q, got %q", ErrCodeDependencyMissing, output.ErrorCode)
	}
}

func TestGetAgentNamesSessionNotFound(t *testing.T) {
	// Override tmux functions for testing
	origInstalled := tmuxInstalledFn
	origExists := tmuxSessionExistsFn
	defer func() {
		tmuxInstalledFn = origInstalled
		tmuxSessionExistsFn = origExists
	}()
	tmuxInstalledFn = func() bool { return true }
	tmuxSessionExistsFn = func(_ string) bool { return false }

	output, err := GetAgentNames("nonexistent", nil)
	if err != nil {
		t.Fatalf("GetAgentNames returned error: %v", err)
	}
	if output.Success {
		t.Error("expected success=false when session not found")
	}
	if output.ErrorCode != ErrCodeSessionNotFound {
		t.Errorf("expected error code %q, got %q", ErrCodeSessionNotFound, output.ErrorCode)
	}
}

// TestPrintAgentNamesSessionNotFoundExitCode is the ntm#215 regression guard:
// GetAgentNames reports SESSION_NOT_FOUND as a success:false envelope with a
// nil Go error, so PrintAgentNames must derive the process exit code from the
// envelope (ExitCodeForResponse) — not from the error — or the CLI exits 0 on
// failure and agents branching on the shell status silently proceed.
func TestPrintAgentNamesSessionNotFoundExitCode(t *testing.T) {
	origInstalled := tmuxInstalledFn
	origExists := tmuxSessionExistsFn
	defer func() {
		tmuxInstalledFn = origInstalled
		tmuxSessionExistsFn = origExists
	}()
	tmuxInstalledFn = func() bool { return true }
	tmuxSessionExistsFn = func(_ string) bool { return false }

	// Silence the JSON envelope while capturing the exit code.
	origStdout := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdout = devNull
	exitCode := PrintAgentNames("definitely-no-such-session", nil)
	os.Stdout = origStdout
	devNull.Close()

	if exitCode != 1 {
		t.Errorf("PrintAgentNames() exit code = %d, want 1 for SESSION_NOT_FOUND", exitCode)
	}
}

// TestPrintAgentNamesSuccessExitCode verifies the happy path still exits 0.
func TestPrintAgentNamesSuccessExitCode(t *testing.T) {
	origInstalled := tmuxInstalledFn
	origExists := tmuxSessionExistsFn
	origGetPanes := tmuxGetPanesFn
	defer func() {
		tmuxInstalledFn = origInstalled
		tmuxSessionExistsFn = origExists
		tmuxGetPanesFn = origGetPanes
	}()
	tmuxInstalledFn = func() bool { return true }
	tmuxSessionExistsFn = func(_ string) bool { return true }
	tmuxGetPanesFn = func(_ string) []tmuxPaneInfo {
		return []tmuxPaneInfo{{Index: 1, Title: "proj__cc_1"}}
	}

	origStdout := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdout = devNull
	exitCode := PrintAgentNames("proj", nil)
	os.Stdout = origStdout
	devNull.Close()

	if exitCode != 0 {
		t.Errorf("PrintAgentNames() exit code = %d, want 0 on success", exitCode)
	}
}

func TestGetAgentNamesWithMockSession(t *testing.T) {
	origInstalled := tmuxInstalledFn
	origExists := tmuxSessionExistsFn
	origGetPanes := tmuxGetPanesFn
	defer func() {
		tmuxInstalledFn = origInstalled
		tmuxSessionExistsFn = origExists
		tmuxGetPanesFn = origGetPanes
	}()

	tmuxInstalledFn = func() bool { return true }
	tmuxSessionExistsFn = func(_ string) bool { return true }
	tmuxGetPanesFn = func(_ string) []tmuxPaneInfo {
		return []tmuxPaneInfo{
			{Index: 0, Title: "proj__user"},
			{Index: 1, Title: "proj__cc_1"},
			{Index: 2, Title: "proj__cc_2"},
			{Index: 3, Title: "proj__cod_1"},
		}
	}

	output, err := GetAgentNames("proj", nil)
	if err != nil {
		t.Fatalf("GetAgentNames returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success=true, got error: %s", output.Error)
	}
	if output.Count != 4 {
		t.Errorf("expected 4 agents, got %d", output.Count)
	}

	// Verify the names
	expectedNames := map[string]string{
		"0.0": "user-alpha",
		"0.1": "claude-bravo",
		"0.2": "claude-charlie",
		"0.3": "codex-delta",
	}
	for _, agent := range output.Agents {
		want, ok := expectedNames[agent.Pane]
		if !ok {
			t.Errorf("unexpected pane %q in output", agent.Pane)
			continue
		}
		if agent.Name != want {
			t.Errorf("agent at pane %q: name = %q, want %q", agent.Pane, agent.Name, want)
		}
	}
}

func TestGetAgentNamesWithCustomNames(t *testing.T) {
	origInstalled := tmuxInstalledFn
	origExists := tmuxSessionExistsFn
	origGetPanes := tmuxGetPanesFn
	defer func() {
		tmuxInstalledFn = origInstalled
		tmuxSessionExistsFn = origExists
		tmuxGetPanesFn = origGetPanes
	}()

	tmuxInstalledFn = func() bool { return true }
	tmuxSessionExistsFn = func(_ string) bool { return true }
	tmuxGetPanesFn = func(_ string) []tmuxPaneInfo {
		return []tmuxPaneInfo{
			{Index: 0, Title: "proj__cc_1"},
			{Index: 1, Title: "proj__cod_1"},
		}
	}

	customNames := []string{"alice", "bob"}
	output, err := GetAgentNames("proj", customNames)
	if err != nil {
		t.Fatalf("GetAgentNames returned error: %v", err)
	}
	if !output.Success {
		t.Fatalf("expected success=true, got error: %s", output.Error)
	}
	if output.Count != 2 {
		t.Errorf("expected 2 agents, got %d", output.Count)
	}

	// Custom names should be used in order
	if output.Agents[0].Name != "alice" {
		t.Errorf("first agent name = %q, want %q", output.Agents[0].Name, "alice")
	}
	if output.Agents[1].Name != "bob" {
		t.Errorf("second agent name = %q, want %q", output.Agents[1].Name, "bob")
	}
}

func TestAgentNameMapConcurrency(t *testing.T) {
	m := NewAgentNameMap("test-session")

	// Concurrent reads and writes should not race
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			m.AssignNew("claude", "0."+string(rune('0'+i%10)))
		}
	}()

	for i := 0; i < 100; i++ {
		m.AllNames()
	}
	<-done
}

func TestBuildNameMapFromSession(t *testing.T) {
	origGetPanes := tmuxGetPanesFn
	defer func() { tmuxGetPanesFn = origGetPanes }()

	tmuxGetPanesFn = func(_ string) []tmuxPaneInfo {
		return []tmuxPaneInfo{
			{Index: 0, Title: "proj__user"},
			{Index: 1, Title: "proj__cc_1"},
			{Index: 2, Title: "proj__cc_2"},
			{Index: 3, Title: "proj__cod_1"},
			{Index: 4, Title: "proj__gmi_1"},
		}
	}

	nameMap := BuildNameMapFromSession("proj", nil)

	entries := nameMap.AllNames()
	if len(entries) != 5 {
		t.Fatalf("expected 5 agents, got %d", len(entries))
	}

	// Verify name patterns (entries are sorted by pane reference)
	if entries[0].Pane != "0.0" || entries[0].Name != "user-alpha" {
		t.Errorf("pane 0.0 entry = %+v, want user-alpha", entries[0])
	}
	if entries[1].Pane != "0.1" || entries[1].Name != "claude-bravo" {
		t.Errorf("pane 0.1 entry = %+v, want claude-bravo", entries[1])
	}
	if entries[4].Pane != "0.4" || entries[4].Name != "gemini-echo" {
		t.Errorf("pane 0.4 entry = %+v, want gemini-echo", entries[4])
	}
}

func TestBuildNameMapFromSessionUsesPaneTypeForCustomTitles(t *testing.T) {
	origGetPanes := tmuxGetPanesFn
	defer func() { tmuxGetPanesFn = origGetPanes }()

	tmuxGetPanesFn = func(_ string) []tmuxPaneInfo {
		return []tmuxPaneInfo{
			{Index: 0, Title: "shell", Type: tmux.AgentUser},
			{Index: 1, Title: "notes", Type: tmux.AgentClaude},
			{Index: 2, Title: "logs", Type: tmux.AgentType("openai-codex")},
		}
	}

	nameMap := BuildNameMapFromSession("proj", nil)

	entries := nameMap.AllNames()
	if len(entries) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(entries))
	}
	if entries[1].Pane != "0.1" || entries[1].Name != "claude-bravo" {
		t.Fatalf("pane 0.1 entry = %+v, want claude-bravo", entries[1])
	}
	if entries[2].Pane != "0.2" || entries[2].Name != "codex-charlie" {
		t.Fatalf("pane 0.2 entry = %+v, want codex-charlie", entries[2])
	}
}
