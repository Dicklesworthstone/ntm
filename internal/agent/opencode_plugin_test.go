package agent

import (
	"strings"
	"testing"
)

// Chrome facts come from the OpenCode TUI source
// (packages/tui/src/component/prompt/index.tsx): composer hint text
// `Ask anything... "<example>"` at idle, spinner + `esc interrupt` footer
// while a turn runs.
const (
	ocIdleScreen = "╭─────────────────────────────────────╮\n" +
		"│ Ask anything... \"fix the failing test\" │\n" +
		"╰─────────────────────────────────────╯\n" +
		"  /commands  @context\n"
	ocWorkingScreen = "user: Reply exactly NTM_OPENCODE_OK\n" +
		"⠹ Thinking\n" +
		"╭─────────────────────────────────────╮\n" +
		"│                                       │\n" +
		"╰─────────────────────────────────────╯\n" +
		"  esc interrupt\n"
	ocSecondEscScreen = "⠹ Thinking\n  esc again to interrupt\n"
)

func TestOpencodeActivelyWorking(t *testing.T) {
	if OpencodeActivelyWorking(ocIdleScreen, 0) {
		t.Fatal("idle composer must not read as working")
	}
	if !OpencodeActivelyWorking(ocWorkingScreen, 0) {
		t.Fatal("esc interrupt footer must read as working")
	}
	if !OpencodeActivelyWorking(ocSecondEscScreen, 0) {
		t.Fatal("esc again to interrupt footer must read as working")
	}
	// A hint that scrolled far out of the live tail no longer counts.
	stale := ocWorkingScreen + strings.Repeat("output line\n", 40) + ocIdleScreen
	if OpencodeActivelyWorking(stale, 0) {
		t.Fatal("footer outside the live tail must not read as working")
	}
}

func TestParser_Opencode_IdleAndWorking(t *testing.T) {
	p := NewParser()

	idle, err := p.ParseWithHint(ocIdleScreen, AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if !idle.IsIdle || idle.IsWorking {
		t.Fatalf("idle composer: IsIdle=%v IsWorking=%v, want idle", idle.IsIdle, idle.IsWorking)
	}

	working, err := p.ParseWithHint(ocWorkingScreen, AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if working.IsIdle || !working.IsWorking {
		t.Fatalf("in-flight turn: IsIdle=%v IsWorking=%v, want working", working.IsIdle, working.IsWorking)
	}

	// Hint text drawn together with the footer (race between frames) must
	// still be classified as working: the veto wins.
	both := ocIdleScreen + "  esc interrupt\n"
	state, err := p.ParseWithHint(both, AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if state.IsIdle || !state.IsWorking {
		t.Fatalf("footer must veto hint text: IsIdle=%v IsWorking=%v", state.IsIdle, state.IsWorking)
	}

	errored, err := p.ParseWithHint("error: provider returned 500\n"+ocIdleScreen, AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if !errored.IsInError {
		t.Fatal("error: line must flag IsInError")
	}
	limited, err := p.ParseWithHint("rate limit exceeded, retry later\n", AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if !limited.IsRateLimited || len(limited.LimitIndicators) == 0 {
		t.Fatalf("rate limit not detected: %+v", limited)
	}
}

func resetPluginPatternsForTest() {
	pluginPatternsMu.Lock()
	pluginPatterns = map[AgentType]PluginPatterns{}
	pluginPatternsMu.Unlock()
}

func TestRegisterPlugin(t *testing.T) {
	resetPluginPatternsForTest()
	t.Cleanup(resetPluginPatternsForTest)

	if err := RegisterPlugin("", nil, nil, nil); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if err := RegisterPlugin("bad", []string{"("}, nil, nil); err == nil || !strings.Contains(err.Error(), "idle pattern") {
		t.Fatalf("invalid regexp must be rejected naming the kind, got %v", err)
	}
	if IsPluginType("bad") {
		t.Fatal("a rejected registration must not leave the type registered")
	}

	if err := RegisterPlugin(" OMP ", []string{`^\s*╰─.*─╯\s*$`}, []string{`⟨esc⟩`}, []string{`(?i)^error:`}); err != nil {
		t.Fatal(err)
	}
	if !IsPluginType("omp") || !IsPluginType("OMP") {
		t.Fatal("plugin type lookup must be case-insensitive and trimmed")
	}
	pp, ok := LookupPluginPatterns("omp")
	if !ok || len(pp.Idle) != 1 || len(pp.Working) != 1 || len(pp.Error) != 1 || !pp.Declared() {
		t.Fatalf("patterns = %+v", pp)
	}
	// Re-registration replaces.
	if err := RegisterPlugin("omp", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if pp, _ := LookupPluginPatterns("omp"); pp.Declared() {
		t.Fatal("re-registration must replace the previous patterns")
	}
}

// Chrome captured live from omp v18 in tmux: idle shows the composer box;
// a turn in flight adds "⠙ Working… ⟨esc⟩" above it.
const (
	ompIdle = "Connected to MCP servers: node_repl.\n" +
		"╭──  Ox Alpha ·  max  …/proj ─3%──1M───╮\n" +
		"╰─                                    ─╯\n"
	ompWorking = "Reply with exactly the single word PONG and nothing else.\n" +
		" ⠙ Working… ⟨esc⟩\n" +
		"╭──  Ox Alpha ·  max  …/proj ─3%──1M───╮\n" +
		"╰─                                    ─╯\n"
)

func TestParser_PluginPatternsDriveClassification(t *testing.T) {
	resetPluginPatternsForTest()
	t.Cleanup(resetPluginPatternsForTest)
	if err := RegisterPlugin("omp", []string{`^\s*╰─.*─╯\s*$`}, []string{`⟨esc⟩`}, nil); err != nil {
		t.Fatal(err)
	}
	p := NewParser()

	idle, err := p.ParseWithHint(ompIdle, "omp")
	if err != nil {
		t.Fatal(err)
	}
	if !idle.IsIdle || idle.IsWorking {
		t.Fatalf("omp idle composer: IsIdle=%v IsWorking=%v", idle.IsIdle, idle.IsWorking)
	}
	working, err := p.ParseWithHint(ompWorking, "omp")
	if err != nil {
		t.Fatal(err)
	}
	if working.IsIdle || !working.IsWorking {
		t.Fatalf("omp working: IsIdle=%v IsWorking=%v", working.IsIdle, working.IsWorking)
	}
	if !PluginActivelyWorking(ompWorking, "omp", 0) || PluginActivelyWorking(ompIdle, "omp", 0) {
		t.Fatal("PluginActivelyWorking disagrees with the declared working pattern")
	}

	// Unregistered type: the same screens fall back to the generic union,
	// which knows nothing about the omp box — proving the plugin patterns
	// are what produced the verdicts above.
	resetPluginPatternsForTest()
	fallback, err := p.ParseWithHint(ompIdle, "omp")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.IsIdle {
		t.Fatal("without registration the omp composer must not be recognised as idle")
	}
}
