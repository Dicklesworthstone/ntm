package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNATOAlphabetLength(t *testing.T) {
	if len(NATOAlphabet) != 26 {
		t.Errorf("NATOAlphabet should have 26 entries, got %d", len(NATOAlphabet))
	}
}

func TestGenerateNameDefault(t *testing.T) {
	m := NewAgentNameMap("test-session")

	tests := []struct {
		agentType string
		want      string
	}{
		{"claude", "claude-alpha"},
		{"codex", "codex-bravo"},
		{"gemini", "gemini-charlie"},
		{"claude", "claude-delta"},
	}

	for _, tt := range tests {
		got := m.GenerateName(tt.agentType)
		if got != tt.want {
			t.Errorf("GenerateName(%q) = %q, want %q", tt.agentType, got, tt.want)
		}
	}
}

func TestGenerateNameWraparound(t *testing.T) {
	m := NewAgentNameMap("test-session")

	// Exhaust all 26 NATO letters
	for i := 0; i < 26; i++ {
		m.GenerateName("claude")
	}

	// The 27th name should wrap around with a suffix
	got := m.GenerateName("claude")
	want := "claude-alpha-2"
	if got != want {
		t.Errorf("GenerateName after wraparound = %q, want %q", got, want)
	}
}

func TestGenerateNameCustomNames(t *testing.T) {
	customNames := []string{"worker-1", "worker-2", "worker-3"}
	m := NewAgentNameMapWithCustomNames("test-session", customNames)

	// First three should use custom names
	got1 := m.GenerateName("claude")
	got2 := m.GenerateName("codex")
	got3 := m.GenerateName("gemini")

	if got1 != "worker-1" {
		t.Errorf("first custom name = %q, want %q", got1, "worker-1")
	}
	if got2 != "worker-2" {
		t.Errorf("second custom name = %q, want %q", got2, "worker-2")
	}
	if got3 != "worker-3" {
		t.Errorf("third custom name = %q, want %q", got3, "worker-3")
	}

	// Fourth should fall back to NATO alphabet
	got4 := m.GenerateName("claude")
	want4 := "claude-alpha"
	if got4 != want4 {
		t.Errorf("fourth name after custom exhausted = %q, want %q", got4, want4)
	}
}

func TestAssignAndLookup(t *testing.T) {
	m := NewAgentNameMap("test-session")

	m.Assign("claude-alpha", "0.1", "claude")
	m.Assign("codex-bravo", "0.2", "codex")

	// Lookup by pane
	name, ok := m.NameForPane("0.1")
	if !ok || name != "claude-alpha" {
		t.Errorf("NameForPane(0.1) = %q, %v; want %q, true", name, ok, "claude-alpha")
	}

	// Lookup by name
	pane, ok := m.PaneForName("codex-bravo")
	if !ok || pane != "0.2" {
		t.Errorf("PaneForName(codex-bravo) = %q, %v; want %q, true", pane, ok, "0.2")
	}

	// Lookup type by name
	agentType, ok := m.TypeForName("claude-alpha")
	if !ok || agentType != "claude" {
		t.Errorf("TypeForName(claude-alpha) = %q, %v; want %q, true", agentType, ok, "claude")
	}

	// Non-existent lookup
	_, ok = m.PaneForName("nonexistent")
	if ok {
		t.Error("PaneForName(nonexistent) should return false")
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
	if m.Count() != 3 {
		t.Errorf("Count() = %d, want 3", m.Count())
	}

	pane, ok := m.PaneForName("claude-alpha")
	if !ok || pane != "0.1" {
		t.Errorf("PaneForName(claude-alpha) = %q, %v; want 0.1, true", pane, ok)
	}
}

func TestAllNames(t *testing.T) {
	m := NewAgentNameMap("test-session")

	m.Assign("claude-alpha", "0.1", "claude")
	m.Assign("codex-bravo", "0.2", "codex")
	m.Assign("user-charlie", "0.0", "user")

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
		m.Count()
		m.AllNames()
		m.NameForPane("0.1")
	}
	<-done
}

func TestUnknownAgentType(t *testing.T) {
	m := NewAgentNameMap("test-session")

	// Unknown agent type should use the type as prefix
	name := m.GenerateName("aider")
	if name != "aider-alpha" {
		t.Errorf("GenerateName(aider) = %q, want %q", name, "aider-alpha")
	}
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

	if nameMap.Count() != 5 {
		t.Fatalf("expected 5 agents, got %d", nameMap.Count())
	}

	// Verify name patterns
	name0, ok := nameMap.NameForPane("0.0")
	if !ok || name0 != "user-alpha" {
		t.Errorf("pane 0.0 name = %q, want user-alpha", name0)
	}

	name1, ok := nameMap.NameForPane("0.1")
	if !ok || name1 != "claude-bravo" {
		t.Errorf("pane 0.1 name = %q, want claude-bravo", name1)
	}

	name4, ok := nameMap.NameForPane("0.4")
	if !ok || name4 != "gemini-echo" {
		t.Errorf("pane 0.4 name = %q, want gemini-echo", name4)
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

	name1, ok := nameMap.NameForPane("0.1")
	if !ok || name1 != "claude-bravo" {
		t.Fatalf("pane 0.1 name = %q, want claude-bravo", name1)
	}

	name2, ok := nameMap.NameForPane("0.2")
	if !ok || name2 != "codex-charlie" {
		t.Fatalf("pane 0.2 name = %q, want codex-charlie", name2)
	}
}
