package agent

import (
	"os"
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

func TestOpencodeComposerRequiresLiveFooter(t *testing.T) {
	t.Parallel()
	composer := "┃ Ask anything… \"fix the failing test\"\n┃ Build · GPT\n╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n  tab agents  ctrl+p commands\n"
	for _, suffix := range []string{
		"new unframed transcript output\n",
		strings.Repeat("output line\n", 40),
		"Press Enter to continue\n",
		"a log line mentioning tab agents and ctrl+p commands\n",
	} {
		t.Run(strings.TrimSpace(strings.SplitN(suffix, "\n", 2)[0]), func(t *testing.T) {
			capture := composer + suffix
			if draft, found := ParseOpencodeComposer(capture); found {
				t.Fatalf("stale composer accepted with draft %q", draft)
			}
			if OpencodeIdlePromptShowing(capture) {
				t.Fatal("legacy idle fallback resurrected the stale composer")
			}
		})
	}
	if draft, found := ParseOpencodeComposer(composer + "output from previous turn\n" + composer); !found || draft != "" {
		t.Fatalf("latest live composer = (%q, %v), want empty and found", draft, found)
	}
	if OpencodeIdlePromptShowing(ocIdleScreen + "new output after old boxed composer\n") {
		t.Fatal("legacy boxed composer followed by output must not count as live")
	}
}

func TestOpencodeComposerPreservesNativeWelcomeAndSessionChrome(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"opencode_idle.txt", "opencode_draft.txt", "opencode_paste_token.txt", "opencode_working.txt"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, found := ParseOpencodeComposer(string(data)); !found {
				t.Fatal("native welcome/session composer not recognized")
			}
			if _, found := ParseOpencodeComposer(string(data) + "new transcript output\n"); found {
				t.Fatal("stale native composer survived a later transcript row")
			}
		})
	}
}

func TestOpencodeComposerPreservesMultilineDraftBeginningWithPlaceholder(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"Ask anything…\nand then explain the result",
		"Ask anything about the deployment",
		"Ask anything... but keep this draft",
		"Ask anything… about the failing tests",
	} {
		capture := "┃ " + strings.ReplaceAll(want, "\n", "\n┃ ") + "\n┃ Build · GPT\n╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n  tab agents  ctrl+p commands\n"
		if draft, found := ParseOpencodeComposer(capture); !found || draft != want {
			t.Fatalf("draft = (%q, %v), want (%q, true)", draft, found, want)
		}
	}
}

func TestOpencodeComposerWelcomeHomeDirectory(t *testing.T) {
	t.Parallel()
	capture := "┃ Ask anything…\n┃ Build · GPT\n╹▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀\n  tab agents  ctrl+p commands\n\n  ● Tip Run /connect to add an AI provider\n\n  ~                       1.18.32\n"
	if draft, found := ParseOpencodeComposer(capture); !found || draft != "" {
		t.Fatalf("home welcome composer = (%q, %v), want empty and found", draft, found)
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

	if err := RegisterPlugin(" PIX ", []string{`^\s*┃ pix ready ┃\s*$`}, []string{`\[stop\]`}, []string{`(?i)^error:`}); err != nil {
		t.Fatal(err)
	}
	if !IsPluginType("pix") || !IsPluginType("PIX") {
		t.Fatal("plugin type lookup must be case-insensitive and trimmed")
	}
	pp, ok := LookupPluginPatterns("pix")
	if !ok || len(pp.Idle) != 1 || len(pp.Working) != 1 || len(pp.Error) != 1 || !pp.Declared() {
		t.Fatalf("patterns = %+v", pp)
	}
	// Re-registration replaces.
	if err := RegisterPlugin("pix", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if pp, _ := LookupPluginPatterns("pix"); pp.Declared() {
		t.Fatal("re-registration must replace the previous patterns")
	}
}

// Chrome of a fictional plugin agent ("pix"): idle shows a ready bar; a turn
// in flight adds a "[stop]" hint above it. The generic heuristics know
// neither line, so any verdict below comes from the declared patterns.
// (omp, whose chrome these tests used to borrow, is a built-in type now.)
const (
	pixIdle = "Connected to MCP servers: node_repl.\n" +
		"┃ pix ready ┃\n"
	pixWorking = "Reply with exactly the single word PONG and nothing else.\n" +
		" ⠙ thinking [stop]\n" +
		"┃ pix ready ┃\n"
)

func TestParser_PluginPatternsDriveClassification(t *testing.T) {
	resetPluginPatternsForTest()
	t.Cleanup(resetPluginPatternsForTest)
	if err := RegisterPlugin("pix", []string{`^\s*┃ pix ready ┃\s*$`}, []string{`\[stop\]`}, nil); err != nil {
		t.Fatal(err)
	}
	p := NewParser()

	idle, err := p.ParseWithHint(pixIdle, "pix")
	if err != nil {
		t.Fatal(err)
	}
	if !idle.IsIdle || idle.IsWorking {
		t.Fatalf("pix idle bar: IsIdle=%v IsWorking=%v", idle.IsIdle, idle.IsWorking)
	}
	working, err := p.ParseWithHint(pixWorking, "pix")
	if err != nil {
		t.Fatal(err)
	}
	if working.IsIdle || !working.IsWorking {
		t.Fatalf("pix working: IsIdle=%v IsWorking=%v", working.IsIdle, working.IsWorking)
	}
	if !PluginActivelyWorking(pixWorking, "pix", 0) || PluginActivelyWorking(pixIdle, "pix", 0) {
		t.Fatal("PluginActivelyWorking disagrees with the declared working pattern")
	}

	// Unregistered type: the same screens fall back to the generic union,
	// which knows nothing about the pix bar — proving the plugin patterns
	// are what produced the verdicts above.
	resetPluginPatternsForTest()
	fallback, err := p.ParseWithHint(pixIdle, "pix")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.IsIdle {
		t.Fatal("without registration the pix ready bar must not be recognised as idle")
	}
}

// TestParser_OmpBuiltinClassifiesEarlierV18Chrome keeps the earlier omp v18
// frames the plugin preset was verified against (captured live in tmux:
// "⠙ Working… ⟨esc⟩" above a status-line composer without the timer) green
// under the built-in omp arm, with no plugin registered.
func TestParser_OmpBuiltinClassifiesEarlierV18Chrome(t *testing.T) {
	resetPluginPatternsForTest()
	t.Cleanup(resetPluginPatternsForTest)
	const (
		idleFrame = "Connected to MCP servers: node_repl.\n" +
			"╭──  Ox Alpha ·  max  …/proj ─3%──1M───╮\n" +
			"╰─                                    ─╯\n"
		workingFrame = "Reply with exactly the single word PONG and nothing else.\n" +
			" ⠙ Working… ⟨esc⟩\n" +
			"╭──  Ox Alpha ·  max  …/proj ─3%──1M───╮\n" +
			"╰─                                    ─╯\n"
	)
	p := NewParser()
	idle, err := p.ParseWithHint(idleFrame, "omp")
	if err != nil {
		t.Fatal(err)
	}
	if idle.Type != AgentTypeOmp || !idle.IsIdle || idle.IsWorking {
		t.Fatalf("omp idle composer: type=%q IsIdle=%v IsWorking=%v", idle.Type, idle.IsIdle, idle.IsWorking)
	}
	if idle.ContextRemaining == nil || *idle.ContextRemaining != 97 {
		t.Fatalf("ContextRemaining = %v, want 97", idle.ContextRemaining)
	}
	working, err := p.ParseWithHint(workingFrame, "omp")
	if err != nil {
		t.Fatal(err)
	}
	if working.IsIdle || !working.IsWorking {
		t.Fatalf("omp working: IsIdle=%v IsWorking=%v", working.IsIdle, working.IsWorking)
	}
	if _, window, ok := OmpContextUsage(idleFrame); !ok || window != 1000000 {
		t.Fatalf("context window = %d (ok=%v), want 1M", window, ok)
	}
}

func TestOpencodeContextUsageAndGrokStatusLineModel(t *testing.T) {
	sidebar := strings.NewReplacer("0 tokens", "14,200 tokens", "0% used", "11% used").Replace(loadTestData(t, "opencode_working.txt"))
	pct, tok, ok := OpencodeContextUsage(sidebar)
	if !ok || pct != 11 || tok != 14200 {
		t.Fatalf("OpencodeContextUsage = (pct=%v, tok=%d, ok=%v), want (11, 14200, true)", pct, tok, ok)
	}
	state, err := NewParser().ParseWithHint(sidebar, AgentTypeOpencode)
	if err != nil {
		t.Fatal(err)
	}
	if state.ContextRemaining == nil || *state.ContextRemaining != 89 || state.TokensUsed == nil || *state.TokensUsed != 14200 {
		t.Fatalf("ParseWithHint(opencode) = remaining=%v tokens=%v, want 89 and 14200", state.ContextRemaining, state.TokensUsed)
	}

	grokFrame := "│ ❯                                      │\n╰─ Grok 4.6 (high) · always-approve ─╯\n"
	if got := GrokStatusLineModel(grokFrame); got != "grok-4.6" {
		t.Fatalf("GrokStatusLineModel = %q, want grok-4.6", got)
	}
}

func TestOpencodeContextUsageRequiresNativeSidebarGeometry(t *testing.T) {
	t.Parallel()
	native := loadTestData(t, "opencode_working.txt")
	idle := strings.Replace(native, "⬝⬝⬝⬝■■■■  esc interrupt", "", 1)
	accounted := strings.NewReplacer("0 tokens", "14,200 tokens", "0% used", "11% used").Replace(idle)
	for _, tc := range []struct {
		name    string
		capture string
		pct     float64
		tokens  int64
	}{
		{name: "verbatim native working viewport", capture: native},
		{name: "completed turn", capture: accounted, pct: 11, tokens: 14200},
		{name: "ANSI colors", capture: "\x1b[36m" + accounted + "\x1b[0m", pct: 11, tokens: 14200},
		{name: "wide transcript characters", capture: strings.Replace(accounted, "step 073", "审查 073", 1), pct: 11, tokens: 14200},
		{name: "abbreviated thousands", capture: strings.Replace(accounted, "14,200 tokens", "14.2k tokens", 1), pct: 11, tokens: 14200},
		{name: "abbreviated millions", capture: strings.Replace(accounted, "14,200 tokens", "1.4M tokens", 1), pct: 11, tokens: 1400000},
		{name: "display metrics while holding a draft", capture: strings.Replace(accounted, "  ┃\n  ┃\n  ┃  Build", "  ┃  keep this draft\n  ┃\n  ┃  Build", 1), pct: 11, tokens: 14200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pct, tokens, ok := OpencodeContextUsage(tc.capture)
			if !ok || pct != tc.pct || tokens != tc.tokens {
				t.Fatalf("native context usage = (%v, %d, %t), want (%v, %d, true)", pct, tokens, ok, tc.pct, tc.tokens)
			}
		})
	}

	// Keep the actual composer and viewport geometry, but remove the native
	// accounting. Any remaining count must come from the counterfeit text.
	noGauge := strings.Replace(idle, "Context", "Telemetry unavailable", 1)
	plainGauge := "Context\n180,000 tokens\n90% used\n"
	nativeRows := strings.Split(idle, "\n")
	var nativeGauge string
	for i, row := range nativeRows {
		if strings.HasSuffix(row, "Context") {
			nativeGauge = strings.Join(nativeRows[i:i+3], "\n") + "\n"
			break
		}
	}
	if nativeGauge == "" {
		t.Fatal("native capture does not contain its expected sidebar")
	}
	for _, tc := range []struct {
		name    string
		capture string
	}{
		{name: "standalone accounting prose", capture: plainGauge},
		{name: "unindented transcript with live composer", capture: plainGauge + noGauge},
		{name: "indented transcript with live composer", capture: strings.ReplaceAll("  "+plainGauge, "\n", "\n  ") + noGauge},
		{name: "right aligned inside transcript", capture: strings.Repeat(" ", 120) + strings.ReplaceAll(plainGauge, "\n", "\n"+strings.Repeat(" ", 120)) + noGauge},
		{name: "draft quoting usage", capture: strings.Replace(noGauge, "  ┃\n  ┃\n  ┃  Build", "  ┃  Context\n  ┃  180,000 tokens\n  ┃  90% used\n  ┃\n  ┃  Build", 1)},
		{name: "draft extending into sidebar", capture: strings.Replace(noGauge, "  ┃\n  ┃\n  ┃  Build", "  ┃"+strings.Repeat(" ", 157)+"Context\n  ┃"+strings.Repeat(" ", 157)+"180,000 tokens\n  ┃"+strings.Repeat(" ", 157)+"90% used\n  ┃\n  ┃  Build", 1)},
		{name: "token row at wrong column", capture: strings.Replace(idle, "0 tokens", " 0 tokens", 1)},
		{name: "percentage row at wrong column", capture: strings.Replace(idle, "0% used", " 0% used", 1)},
		{name: "text intruding into sidebar gap", capture: strings.Replace(idle, "    Context", " x  Context", 1)},
		{name: "stale frame followed by output", capture: idle + "new output after the old composer\n"},
		{name: "previous frame cannot lend its gauge", capture: idle + noGauge},
		{name: "missing current composer", capture: nativeGauge},
		{name: "ambiguous native gauges", capture: nativeGauge + idle},
		{name: "prose in place of composer rule", capture: strings.Replace(idle, "╹▀", "╹text", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if pct, tokens, ok := OpencodeContextUsage(tc.capture); ok {
				t.Fatalf("unproven accounting accepted: percentage=%v tokens=%d", pct, tokens)
			}
		})
	}
}
