package agent

import (
	"strings"
	"testing"
)

// The omp_*.txt fixtures are verbatim `tmux capture-pane -p` frames of omp
// v18.2.3 (launched as `omp --auto-approve`) with trailing whitespace trimmed:
// nerd, unicode and ascii symbol presets at 120 columns, plus the provider
// error frame and the /model selector at 220 columns.

func TestOmpAgentType(t *testing.T) {
	if AgentTypeOmp != "omp" {
		t.Fatalf("AgentTypeOmp = %q, want \"omp\"", AgentTypeOmp)
	}
	for _, alias := range []string{"omp", "OMP", " omp ", "oh-my-pi", "oh_my_pi", "ohmypi", "Oh-My-Pi"} {
		if got := AgentType(alias).Canonical(); got != AgentTypeOmp {
			t.Errorf("Canonical(%q) = %q, want omp", alias, got)
		}
	}
	if !AgentTypeOmp.IsValid() {
		t.Error("omp must be a valid built-in agent type")
	}
	if got := AgentTypeOmp.DisplayName(); got != "Oh My Pi" {
		t.Errorf("DisplayName = %q, want \"Oh My Pi\"", got)
	}
	if got := AgentTypeOmp.ProfileName(); got != "OMP" {
		t.Errorf("ProfileName = %q, want \"OMP\"", got)
	}
	// omp submits on the first Enter (a second Enter into an empty or busy
	// composer is a verified no-op), so it does not require double-Enter.
	if AgentTypeOmp.NeedsDoubleEnter() {
		t.Error("omp must not require double-Enter")
	}
	// "om"/"pi" are not omp aliases: pi_agent is a distinct harness.
	for _, other := range []string{"pi", "pi-agent", "om", "oc"} {
		if AgentType(other).Canonical() == AgentTypeOmp {
			t.Errorf("Canonical(%q) must not resolve to omp", other)
		}
	}
}

func TestParseOmpComposer_RealCaptures(t *testing.T) {
	tests := []struct {
		file       string
		found      bool
		busy       bool
		escHint    bool
		draft      string
		pasteToken bool
		rowsBelow  int
		steering   int
		subagents  int
	}{
		{file: "omp_nerd_idle_fresh.txt", found: true},
		{file: "omp_nerd_draft.txt", found: true, draft: "reply with the word ok"},
		{file: "omp_nerd_working.txt", found: true, busy: true, escHint: true},
		{file: "omp_nerd_idle_done.txt", found: true},
		{file: "omp_nerd_tool_working.txt", found: true, busy: true, escHint: true},
		{file: "omp_nerd_interrupted.txt", found: true},
		{file: "omp_nerd_multiline_draft.txt", found: true, draft: "first line of a two line prompt\nsecond line of it"},
		// U+F15C is the nerd-font file glyph omp renders before the token.
		{file: "omp_nerd_paste_token.txt", found: true, draft: "first line of a two line prompt #1", pasteToken: true},
		{file: "omp_nerd_autocomplete.txt", found: true, draft: "/mod", rowsBelow: 10},
		{file: "omp_nerd_exited_to_shell.txt", found: true, rowsBelow: 1},
		{file: "omp_nerd_api_error.txt", found: true},
		{file: "omp_nerd_model_selector.txt", found: false},
		{file: "omp_unicode_working.txt", found: true, busy: true, escHint: true},
		{file: "omp_unicode_idle_done.txt", found: true},
		// A message submitted mid-turn waits in a "Steering · 1" block drawn
		// above the activity line.
		{file: "omp_unicode_steering.txt", found: true, busy: true, escHint: true, steering: 1},
		{file: "omp_ascii_working.txt", found: true, busy: true, escHint: true},
		{file: "omp_ascii_idle_done.txt", found: true},
		// A long turn in a real swarm pane: background-job box, TODO tree and
		// an intent label ("Waiting for …") in place of "Working…"; the tree's
		// "└─────" edge must not be mistaken for the composer.
		{file: "omp_nerd_working_todo.txt", found: true, busy: true, escHint: true},
		{file: "omp_nerd_working_subagents.txt", found: true, busy: true, escHint: true, subagents: 1},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			got := ParseOmpComposer(loadTestData(t, tc.file))
			if got.Found != tc.found || got.Busy != tc.busy || got.EscHint != tc.escHint ||
				got.Draft != tc.draft || got.PasteToken != tc.pasteToken || got.RowsBelow != tc.rowsBelow ||
				got.Steering != tc.steering || got.Subagents != tc.subagents {
				t.Fatalf("ParseOmpComposer = %+v\nwant found=%v busy=%v escHint=%v draft=%q pasteToken=%v rowsBelow=%d steering=%d subagents=%d",
					got, tc.found, tc.busy, tc.escHint, tc.draft, tc.pasteToken, tc.rowsBelow, tc.steering, tc.subagents)
			}
		})
	}
}

// TestOmpSteeringAloneIsWorking pins that a pending-steering block keeps the
// pane working even in a frame where the timer and Esc hint are absent (omp
// acts on queued steering as soon as the current step yields).
func TestOmpSteeringAloneIsWorking(t *testing.T) {
	steering := loadTestData(t, "omp_unicode_steering.txt")
	quiet := strings.Replace(steering, "  ⎋ Working…\n", "", 1)
	quiet = strings.Replace(quiet, "╭── ⠸ 4s > ", "╭── π > ", 1)
	c := ParseOmpComposer(quiet)
	if c.Busy || c.EscHint || c.Steering != 1 {
		t.Fatalf("fixture edit did not isolate the steering block: %+v", c)
	}
	if !OmpActivelyWorking(quiet, 0) || OmpIdlePromptShowing(quiet) {
		t.Fatal("pending steering must read as working, never idle")
	}
}

// TestOmpProviderError pins the dismissable provider-error block from live
// captures (an OpenRouter server_error in a swarm pane, a bad-key 401) and its
// boundaries: the block must be complete and directly above a quiet composer.
func TestOmpProviderError(t *testing.T) {
	for file, want := range map[string]string{
		"omp_nerd_provider_error.txt": "server_error: ERROR",
		"omp_nerd_api_error.txt":      "401 Invalid API Key",
		// Live swarm frame: "F5 to Retry" between the closing rule and the
		// composer (was a false negative that read as idle).
		"omp_nerd_provider_error_f5.txt": "server_error: ERROR",
	} {
		got, ok := OmpProviderError(loadTestData(t, file))
		if !ok || got != want {
			t.Errorf("OmpProviderError(%s) = (%q, %v), want %q", file, got, ok, want)
		}
	}
	for _, file := range []string{"omp_nerd_idle_done.txt", "omp_nerd_working.txt", "omp_nerd_interrupted.txt", "omp_nerd_model_selector.txt"} {
		if got, ok := OmpProviderError(loadTestData(t, file)); ok {
			t.Errorf("OmpProviderError(%s) = %q, want none", file, got)
		}
	}

	live := loadTestData(t, "omp_nerd_provider_error.txt")
	// Once a retried turn runs, the lingering block is history, not state.
	retrying := strings.Replace(live, "╭── 󰵗 ", "╭── ⠧ 2s ", 1)
	if c := ParseOmpComposer(retrying); !c.Busy || c.ProviderError == "" {
		t.Fatalf("fixture edit did not make the retry busy: %+v", c)
	}
	if _, ok := OmpProviderError(retrying); ok {
		t.Fatal("a provider-error block under a running turn must not read as an error")
	}
	// Transcript text quoting the footer, not framed above the composer.
	quoted := strings.Replace(loadTestData(t, "omp_nerd_idle_done.txt"), " ok\n\n╭──", " the TUI said: Dismissed when you send your next message.\n\n╭──", 1)
	if _, ok := OmpProviderError(quoted); ok {
		t.Fatal("a quoted footer without the rule frame must not read as a provider error")
	}
	// The block must be the last thing above the composer.
	pushedUp := strings.Replace(live, "\n\n╭── 󰵗 ", "\n\n later transcript line\n\n╭── 󰵗 ", 1)
	if _, ok := OmpProviderError(pushedUp); ok {
		t.Fatal("a provider-error block with transcript below it is no longer current")
	}

	// The retry hint is the only non-blank row allowed between block and
	// composer, with or without its nerd glyph and surrounding blank rows.
	withHint := loadTestData(t, "omp_nerd_provider_error_f5.txt")
	for name, variant := range map[string]string{
		"plain hint":         strings.Replace(withHint, "  F5 to Retry\n", "  F5 to Retry\n", 1),
		"hint between blank": strings.Replace(withHint, "  F5 to Retry\n", "\n  F5 to Retry\n\n", 1),
	} {
		if got, ok := OmpProviderError(variant); !ok || got != "server_error: ERROR" {
			t.Errorf("%s: OmpProviderError = (%q, %v), want server_error", name, got, ok)
		}
	}
	if _, ok := OmpProviderError(strings.Replace(withHint, "  F5 to Retry\n", "  F5 to Retry\n some later line\n", 1)); ok {
		t.Fatal("an arbitrary row after the retry hint means the block is history")
	}
	state, _ := NewParser().ParseWithHint(withHint, AgentTypeOmp)
	if !state.IsInError || state.IsIdle || state.IsWorking {
		t.Fatalf("live F5 frame parsed as error=%v idle=%v working=%v, want error only", state.IsInError, state.IsIdle, state.IsWorking)
	}
	if !strings.Contains(OmpProviderErrorRecovery, "F5") || !strings.Contains(OmpProviderErrorRecovery, "continue prompt") {
		t.Fatalf("recovery guidance must name both F5 and a continue prompt: %q", OmpProviderErrorRecovery)
	}
}

// TestOmpSubagentsPanelIsWorking pins the running-subagents panel as in-flight
// chrome on its own, and that the TODO tree alone is not: a plan can outlive
// the turn that wrote it, and reading it as busy would wedge delivery.
func TestOmpSubagentsPanelIsWorking(t *testing.T) {
	live := loadTestData(t, "omp_nerd_working_subagents.txt")
	quiet := strings.Replace(live, "  󱊷 Reading executable restrictor facade fixture\n", "", 1)
	quiet = strings.Replace(quiet, "╭── ⠧ 3m ", "╭── 󰵗 ", 1)
	c := ParseOmpComposer(quiet)
	if c.Busy || c.EscHint || c.Subagents != 1 {
		t.Fatalf("fixture edit did not isolate the subagents panel: %+v", c)
	}
	if !OmpActivelyWorking(quiet, 0) || OmpIdlePromptShowing(quiet) {
		t.Fatal("a listed running subagent must read as working, never idle")
	}

	todoOnly := strings.Replace(quiet, " Subagents\n  └─ • RestrictorContracts ⟨scout⟩ Complete assignment thoroughly: ↵ # Tar…\n", "", 1)
	if c := ParseOmpComposer(todoOnly); c.Subagents != 0 || c.Working() {
		t.Fatalf("the TODO tree alone must not read as working: %+v", c)
	}
	// A transcript line that merely says "Subagents" with no rows is not a panel.
	prose := strings.Replace(todoOnly, " TODO\n", " Subagents\n\n TODO\n", 1)
	if c := ParseOmpComposer(prose); c.Subagents != 0 {
		t.Fatalf("a bare Subagents line without rows must not count: %+v", c)
	}
}

// TestOmpWorkingSignalsAreIndependent proves each in-flight signal alone keeps
// a pane working, so a frame where one of them is momentarily absent (the
// intent label replacing "Working…", or a status line without the timer) can
// never read as idle.
func TestOmpWorkingSignalsAreIndependent(t *testing.T) {
	working := loadTestData(t, "omp_nerd_working.txt")

	timerOnly := strings.Replace(working, "  󱊷 Working…", "", 1)
	if c := ParseOmpComposer(timerOnly); !c.Busy || c.EscHint || !OmpActivelyWorking(timerOnly, 0) {
		t.Fatalf("spinner+timer alone must read as working: %+v", c)
	}

	hintOnly := strings.Replace(working, "╭── ⠧ 1s ", "╭── 󰵗 ", 1)
	if c := ParseOmpComposer(hintOnly); c.Busy || !c.EscHint || !OmpActivelyWorking(hintOnly, 0) {
		t.Fatalf("Esc-hint activity line alone must read as working: %+v", c)
	}

	neither := strings.Replace(timerOnly, "╭── ⠧ 1s ", "╭── 󰵗 ", 1)
	if OmpActivelyWorking(neither, 0) || !OmpIdlePromptShowing(neither) {
		t.Fatal("with both in-flight signals removed the working frame must read as idle")
	}

	// The trailing "⟨esc⟩" form earlier v18 builds rendered.
	legacy := strings.Replace(neither, "\n╭── 󰵗 ", "\n ⠙ Working… ⟨esc⟩\n╭── 󰵗 ", 1)
	if !OmpActivelyWorking(legacy, 0) {
		t.Fatal("legacy ' ⠙ Working… ⟨esc⟩' activity line must read as working")
	}
}

func TestOmpIdleAndWorking_NeverBothAndNeverFalseIdle(t *testing.T) {
	cases := map[string]struct{ working, idle bool }{
		"omp_nerd_idle_fresh.txt":      {idle: true},
		"omp_nerd_draft.txt":           {idle: true},
		"omp_nerd_working.txt":         {working: true},
		"omp_nerd_idle_done.txt":       {idle: true},
		"omp_nerd_tool_working.txt":    {working: true},
		"omp_nerd_interrupted.txt":     {idle: true},
		"omp_nerd_multiline_draft.txt": {idle: true},
		"omp_nerd_paste_token.txt":     {idle: true},
		// A popup (or a shell prompt after omp exited) under the box is
		// neither working nor a quiet composer.
		"omp_nerd_autocomplete.txt":    {},
		"omp_nerd_exited_to_shell.txt": {},
		"omp_nerd_api_error.txt":       {idle: true},
		"omp_nerd_model_selector.txt":  {},
		"omp_unicode_working.txt":      {working: true},
		"omp_unicode_steering.txt":     {working: true},
		"omp_unicode_idle_done.txt":    {idle: true},
		"omp_ascii_working.txt":        {working: true},
		"omp_ascii_idle_done.txt":      {idle: true},
	}
	for file, want := range cases {
		t.Run(file, func(t *testing.T) {
			out := loadTestData(t, file)
			gotWorking := OmpActivelyWorking(out, 0)
			gotIdle := OmpIdlePromptShowing(out)
			if gotWorking != want.working || gotIdle != want.idle {
				t.Fatalf("working=%v idle=%v, want working=%v idle=%v", gotWorking, gotIdle, want.working, want.idle)
			}
		})
	}
}

// TestOmpComposerRejectsLookalikeBoxes pins that the tool-output box, the
// welcome banner, and the paste preview box — all bordered with the same
// glyphs — are never mistaken for the composer.
func TestOmpComposerRejectsLookalikeBoxes(t *testing.T) {
	for name, capture := range map[string]string{
		"tool box": "╭────────────────────╮\n│ $ sleep 25         │\n╰────────────────────╯",
		"banner":   "╭─── omp v18.2.3 ─────────╮\n│      Welcome back!      │\n╰─────────────────────────╯",
		"paste":    "╭───  #1 ───╮\n│paste line 0│\n╰ +15 lines ─╯",
		"ascii":    "+--------------------+\n| $ sleep 25         |\n+--------------------+",
		"claude":   "╭────────────────────╮\n│ > try something    │\n╰────────────────────╯",
		"plain":    "just some transcript text\n$ ",
	} {
		if c := ParseOmpComposer(capture); c.Found {
			t.Errorf("%s: parsed as an omp composer: %+v", name, c)
		}
	}
}

func TestParser_Omp_RealCaptures(t *testing.T) {
	p := NewParser()
	tests := []struct {
		file          string
		working, idle bool
		inError       bool
		contextLeft   float64
	}{
		{file: "omp_nerd_idle_fresh.txt", idle: true, contextLeft: 94},
		{file: "omp_nerd_working.txt", working: true, contextLeft: 94},
		{file: "omp_nerd_tool_working.txt", working: true, contextLeft: 93},
		{file: "omp_nerd_idle_done.txt", idle: true, contextLeft: 93},
		{file: "omp_nerd_interrupted.txt", idle: true, contextLeft: 93},
		// A failed turn is an error, never idle-after-completion.
		{file: "omp_nerd_api_error.txt", inError: true, contextLeft: 88},
		{file: "omp_nerd_provider_error.txt", inError: true, contextLeft: 59},
		{file: "omp_unicode_working.txt", working: true, contextLeft: 94},
		{file: "omp_ascii_working.txt", working: true, contextLeft: 94},
		{file: "omp_ascii_idle_done.txt", idle: true, contextLeft: 93},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			out := loadTestData(t, tc.file)
			state, err := p.ParseWithHint(out, AgentTypeOmp)
			if err != nil {
				t.Fatalf("ParseWithHint: %v", err)
			}
			if state.Type != AgentTypeOmp {
				t.Fatalf("Type = %q, want omp", state.Type)
			}
			if state.IsWorking != tc.working || state.IsIdle != tc.idle || state.IsInError != tc.inError {
				t.Fatalf("working=%v idle=%v error=%v, want working=%v idle=%v error=%v",
					state.IsWorking, state.IsIdle, state.IsInError, tc.working, tc.idle, tc.inError)
			}
			if state.ContextRemaining == nil || *state.ContextRemaining != tc.contextLeft {
				t.Fatalf("ContextRemaining = %v, want %v", state.ContextRemaining, tc.contextLeft)
			}
			if tc.working && len(state.WorkIndicators) == 0 {
				t.Fatal("a working omp pane must carry work indicators")
			}
		})
	}
}

// TestParser_Omp_BlankPaneIsNotIdle pins the deliberate difference from the
// grok/Claude arms: omp draws nothing for ~1s after launch, so a blank pane
// must not read as a ready composer.
func TestParser_Omp_BlankPaneIsNotIdle(t *testing.T) {
	state, err := NewParser().ParseWithHint("\n\n\n", AgentTypeOmp)
	if err != nil {
		t.Fatal(err)
	}
	if state.IsIdle || state.IsWorking {
		t.Fatalf("blank omp pane: idle=%v working=%v, want neither", state.IsIdle, state.IsWorking)
	}
}

func TestParser_Omp_RateLimitAndErrors(t *testing.T) {
	p := NewParser()
	idle := loadTestData(t, "omp_nerd_idle_done.txt")
	const reply = " ok\n\n╭──"
	if !strings.Contains(idle, reply) {
		t.Fatalf("fixture no longer contains %q", reply)
	}
	// No omp rate-limit frame has been captured, so omp infers none: a
	// transcript that discusses rate limits leaves the pane idle, and a framed
	// provider 429 is a (retryable) provider error rather than a wait state.
	chattyLimits := strings.Replace(idle, reply, " Added retry with backoff for the rate limit / too many requests path.\n\n╭──", 1)
	if state, _ := p.ParseWithHint(chattyLimits, AgentTypeOmp); state.IsRateLimited || !state.IsIdle || state.IsInError {
		t.Fatalf("transcript mentioning rate limits: limited=%v idle=%v error=%v", state.IsRateLimited, state.IsIdle, state.IsInError)
	}
	rule := strings.Repeat("─", 40)
	framed429 := strings.Replace(idle, reply, " ok\n\n"+rule+"\n  429 Too Many Requests\n Dismissed when you send your next message.\n"+rule+"\n\n╭──", 1)
	state, _ := p.ParseWithHint(framed429, AgentTypeOmp)
	if state.IsRateLimited || !state.IsInError || state.IsIdle || state.IsWorking {
		t.Fatalf("framed 429: limited=%v error=%v idle=%v working=%v", state.IsRateLimited, state.IsInError, state.IsIdle, state.IsWorking)
	}

	noModel := strings.Replace(idle, reply, " Error: No model selected.\n Then use /model to select a model.\n\n╭──", 1)
	if state, _ := p.ParseWithHint(noModel, AgentTypeOmp); !state.IsInError {
		t.Fatal("\"Error: No model selected.\" must read as an omp error state")
	}

	// Ordinary transcript mentions of errors are not an omp error state.
	chatter := strings.Replace(idle, reply, " fixed the error: nil pointer in parser.go\n\n╭──", 1)
	if state, _ := p.ParseWithHint(chatter, AgentTypeOmp); state.IsInError {
		t.Fatal("transcript text containing \"error:\" must not flag an omp pane as errored")
	}
}

func TestDetectAgentType_Omp(t *testing.T) {
	p := NewParser()
	for _, file := range []string{
		"omp_nerd_idle_fresh.txt", "omp_nerd_working.txt", "omp_nerd_idle_done.txt",
		"omp_unicode_working.txt", "omp_ascii_idle_done.txt",
	} {
		if got := p.DetectAgentType(loadTestData(t, file)); got != AgentTypeOmp {
			t.Errorf("DetectAgentType(%s) = %q, want omp", file, got)
		}
	}
	for _, file := range []string{"cc_idle.txt", "cc_working.txt", "cod_idle.txt", "gmi_idle.txt"} {
		if got := p.DetectAgentType(loadTestData(t, file)); got == AgentTypeOmp {
			t.Errorf("DetectAgentType(%s) = omp, want a non-omp type", file)
		}
	}
}

func TestOmpContextUsage(t *testing.T) {
	tests := []struct {
		name    string
		capture string
		pct     float64
		window  int64
		ok      bool
	}{
		{name: "embedded gauge", capture: loadTestData(t, "omp_nerd_idle_done.txt"), pct: 7, window: 262000, ok: true},
		{name: "ascii gauge", capture: loadTestData(t, "omp_ascii_working.txt"), pct: 6, window: 262000, ok: true},
		{name: "provider error frame", capture: loadTestData(t, "omp_nerd_api_error.txt"), pct: 12, window: 131000, ok: true},
		// A crowded status line drops the rule fill between the percentage
		// and the window ("──69%󰁨262K─"); the session title follows.
		{name: "crowded status line", capture: loadTestData(t, "omp_nerd_working_todo.txt"), pct: 69, window: 262000, ok: true},
		{
			// A size-like token in the session title is never the window.
			name:    "title token after a window-less gauge",
			capture: "╭── 󰵗   Union Alpha   omp-probe ────41%──────  Load 10K rows ──╮\n╰─                                  ─╯",
			pct:     41, ok: true,
		},
		{
			// statusLine.contextLine=annotated renders "6.0%/262K" (live capture).
			name:    "annotated",
			capture: "╭── 󰵗   Union Alpha   omp-probe   6.0%/262K 󰁨 ──────────────────󰁨──────╮\n╰─                                  ─╯",
			pct:     6, window: 262000, ok: true,
		},
		{
			// An unknown --model renders "16K/?" and no percentage.
			name:    "no model",
			capture: "╭── 󰵗   no-model   omp-probe   16K/? 󰁨 ──────────────────────╮\n╰─                                  ─╯",
		},
		{name: "no composer", capture: loadTestData(t, "omp_nerd_model_selector.txt")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pct, window, ok := OmpContextUsage(tc.capture)
			if ok != tc.ok || pct != tc.pct || window != tc.window {
				t.Fatalf("OmpContextUsage = (%v, %v, %v), want (%v, %v, %v)", pct, window, ok, tc.pct, tc.window, tc.ok)
			}
		})
	}
}

func TestOmpComposerBottomBorderHint(t *testing.T) {
	const top = "╭── 󰵗   K3   project   main *5 ?1  󰙺 ──2%──────────────󰁨────────1M───╮\n"
	const hint = "󰘶 󰌒 to change thinking effort"
	cases := []struct {
		name  string
		box   string
		draft string
	}{
		{"empty with reported hint", top + "╰─                                                               " + hint + " ─╯", ""},
		{"Unicode effort hint", top + "╰─          ⇧⇥ to change thinking effort ─╯", ""},
		{"ASCII effort hint", "+-- omp project ---2%---1M---+\n+-          Shift+Tab to change thinking effort -+", ""},
		{"nerd agent hint", top + "╰─          ← ← to see 2 running agents ─╯", ""},
		{"Unicode agent hint", top + "╰─          ←← to see 1 running agent ─╯", ""},
		{"ASCII agent hint", top + "╰─          Left Left to see 12 running agents ─╯", ""},
		{"draft before hint", top + "╰─ preserve this draft           " + hint + " ─╯", "preserve this draft"},
		{"wrapped draft above hint", top + "│ first line of real input │\n╰─          " + hint + " ─╯", "first line of real input"},
		{"left-aligned hint is a draft", top + "╰─ " + hint + " ─╯", hint},
		{"ordinary hint words", top + "╰─ explain how to change thinking effort ─╯", "explain how to change thinking effort"},
		{"unknown right-aligned text", top + "╰─        unknown footer text ─╯", "unknown footer text"},
		{"hint in continuation row", top + "│ " + hint + " │\n╰─  ─╯", hint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOmpComposer(tc.box)
			if !got.Found || got.Draft != tc.draft {
				t.Fatalf("ParseOmpComposer = %+v, want found with draft %q", got, tc.draft)
			}
		})
	}
}

func TestParseOmpComposer_BandShape_GH342(t *testing.T) {
	// Exact captures reported in GH #342 (Nov 18 comment, omp 18.22.2 default composer.shape: band).
	const freshSingleLine = "   π > ◒ DeepSeek V4.1 Flash 1017 > 📁 /path/to/project > ⑂ main ?3 ▶─1%────────────────────────────────┃──────1M─\n" +
		"  ╰─                                                               ⇧⇥ to change thinking effort\n"
	const freshWrapped = "   π > ◒ DeepSeek V4.1 Flash 1017 > 📁 /path/to/project > ⑂ main ?3\n" +
		"▶─1%────────────────────────────────┃──────1M─\n" +
		"  ╰─                                                                        \n" +
		"⇧⇥ to change thinking effort\n"
	const idleAfterTurn = "Done implementing parser.\n\n" +
		"   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main *10 ?6 > $1.16 ▶──────────────────────────11%──────────────────\n" +
		"  ╰─\n"
	const idleWrapped = "   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main *10 ?6 > $1.16\n" +
		"▶──────────────────────────11%──────────────────\n" +
		"  ╰─\n"
	const workingBand = "   ⠏ 18m > ◒ DeepSeek V4.1 Flash 1017 > 📁 /path/to/project > ⑂ main *10 ?6 > $0.06 ▶─────────14%─────\n" +
		"  ╰─\n"
	const workingWrapped = "   ⠏ 18m > ◒ DeepSeek V4.1 Flash 1017 > 📁 /path/to/project > ⑂ main *10\n" +
		"?6 > $0.06 ▶─────────14%─────\n" +
		"  ╰─\n"
	const bandWithDraftAndHint = "   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main ▶───5%───262K─\n" +
		"  ╰─ fix context rotation           ⇧⇥ to change thinking effort\n"
	const bandWithMultilineDraft = "   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main ▶───5%───262K─\n" +
		"  first line of draft\n" +
		"  ╰─ second line of draft\n"
	const bandWithToolBoxScrollback = "╭─── Service: API ───╮\n" +
		"│ status: ok         │\n" +
		"╰─── Service: API ───╯\n\n" +
		"   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main ▶───12%───1M─\n" +
		"  ╰─\n"
	const bandWithCompletionMenu = "   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main ▶───12%───1M─\n" +
		"  ╰─ /mod\n" +
		"  /model  Switch active model\n" +
		"  /mode   Change reasoning mode\n"
	const bandANSI = "\x1b[38;5;39m   π > ◒ GLM-5.3 > 📁 /path/to/project > ⑂ main ▶───22%───262K─\x1b[0m\n" +
		"\x1b[2m  ╰─\x1b[0m\n"
	const asciiBand = "  omp > model-x > /repo > main ---7%---262K---\n" +
		"  +-          Shift+Tab to change thinking effort\n"

	cases := []struct {
		name      string
		capture   string
		wantFound bool
		wantBusy  bool
		wantIdle  bool
		wantDraft string
		wantBelow int
		wantPct   float64
		wantWin   int64
		wantGauge bool
	}{
		{"fresh single-line band with hint", freshSingleLine, true, false, true, "", 0, 1, 1000000, true},
		{"fresh wrapped band with wrapped hint", freshWrapped, true, false, true, "", 0, 1, 1000000, true},
		{"idle after turn", idleAfterTurn, true, false, true, "", 0, 11, 0, true},
		{"idle wrapped", idleWrapped, true, false, true, "", 0, 11, 0, true},
		{"working band", workingBand, true, true, false, "", 0, 14, 0, true},
		{"working wrapped band", workingWrapped, true, true, false, "", 0, 14, 0, true},
		{"band with draft and right-aligned hint", bandWithDraftAndHint, true, false, true, "fix context rotation", 0, 5, 262000, true},
		{"band with multiline draft", bandWithMultilineDraft, true, false, true, "first line of draft\nsecond line of draft", 0, 5, 262000, true},
		{"band below scrollback tool-output box", bandWithToolBoxScrollback, true, false, true, "", 0, 12, 1000000, true},
		{"band with open completion list blocks ready", bandWithCompletionMenu, true, false, false, "/mod", 2, 12, 1000000, true},
		{"band with ANSI escapes", bandANSI, true, false, true, "", 0, 22, 262000, true},
		{"ASCII band with Shift+Tab hint", asciiBand, true, false, true, "", 0, 7, 262000, true},
	}

	parser := NewParser()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseOmpComposer(tc.capture)
			if got.Found != tc.wantFound || got.Busy != tc.wantBusy || got.Draft != tc.wantDraft || got.RowsBelow != tc.wantBelow {
				t.Fatalf("ParseOmpComposer = %+v, want found=%v busy=%v draft=%q rowsBelow=%d",
					got, tc.wantFound, tc.wantBusy, tc.wantDraft, tc.wantBelow)
			}
			if gotIdle := OmpIdlePromptShowing(tc.capture); gotIdle != tc.wantIdle {
				t.Errorf("OmpIdlePromptShowing = %v, want %v", gotIdle, tc.wantIdle)
			}
			if gotWorking := OmpActivelyWorking(tc.capture, 160); gotWorking != tc.wantBusy {
				t.Errorf("OmpActivelyWorking = %v, want %v", gotWorking, tc.wantBusy)
			}
			if gotType := parser.DetectAgentType(tc.capture); gotType != AgentTypeOmp {
				t.Errorf("DetectAgentType = %q, want %q", gotType, AgentTypeOmp)
			}
			pct, win, ok := OmpContextUsage(tc.capture)
			if ok != tc.wantGauge || pct != tc.wantPct || win != tc.wantWin {
				t.Errorf("OmpContextUsage = (%v, %v, %v), want (%v, %v, %v)", pct, win, ok, tc.wantPct, tc.wantWin, tc.wantGauge)
			}
		})
	}

	// Negative / anti-false-positive checks: scrollback-only tool footers,
	// blank-separated prose, and non-omp dialogs must NOT match as a band composer.
	negatives := []struct {
		name    string
		capture string
	}{
		{"tool output box only", "╭─── Service: API ───╮\n│ ok │\n╰─── Service: API ───╯\n"},
		{"blank line separates scrollback band from stray curved prefix", "   π > ◒ GLM-5.3 > 📁 /repo ▶───10%───\n\n  ╰─ stray tree branch\n"},
		{"ordinary prose with π > and no status markers", "note: π > 3.14\n  ╰─ tree leaf\n"},
		{"setup prompt without composer", "Keep omp up to date?\n  > Yes (recommended)\n    No\n"},
	}
	for _, tc := range negatives {
		t.Run("negative_"+tc.name, func(t *testing.T) {
			if got := ParseOmpComposer(tc.capture); got.Found {
				t.Fatalf("expected Found=false for %s, got %+v", tc.name, got)
			}
		})
	}
}

// TestMR2_ParseOmpComposer_WidthAndANSIInvariance is a metamorphic proof (MR-2)
// that varying terminal rule width (20..220 cols) and wrapping lines in ANSI SGR
// color escapes never breaks omp band-shape composer recognition or alters
// Found/Busy/Draft semantics.
func TestMR2_ParseOmpComposer_WidthAndANSIInvariance(t *testing.T) {
	t.Parallel()

	for _, width := range []int{20, 48, 80, 120, 180, 220} {
		rule := strings.Repeat("─", width)
		for _, tc := range []struct {
			name     string
			prefix   string
			draft    string
			wantBusy bool
		}{
			{"idle_empty", "   π > ◒ GLM-5.3 > 📁 /repo ▶" + rule + "12%───", "", false},
			{"idle_draft", "   π > ◒ GLM-5.3 > 📁 /repo ▶" + rule + "12%───", "ship metamorphic tests", false},
			{"busy_spinner", "   ⠸ 19s > ◒ GLM-5.3 > 📁 /repo ▶" + rule + "18%───", "", true},
		} {
			plain := tc.prefix + "\n" +
				"  ╰─ " + tc.draft + "\n" +
				"      tab cycle thinking · shift+tab plan mode\n"
			ansi := "\x1b[38;5;244m" + tc.prefix + "\x1b[0m\n" +
				"\x1b[1m  ╰─ \x1b[0m" + tc.draft + "\n" +
				"\x1b[2m      tab cycle thinking · shift+tab plan mode\x1b[0m\n"

			gotPlain := ParseOmpComposer(plain)
			gotANSI := ParseOmpComposer(ansi)
			if !gotPlain.Found || gotPlain.Busy != tc.wantBusy || gotPlain.Draft != tc.draft {
				t.Fatalf("width=%d %s plain=%+v, want found=true busy=%v draft=%q",
					width, tc.name, gotPlain, tc.wantBusy, tc.draft)
			}
			if gotPlain != gotANSI {
				t.Fatalf("MR-2 ANSI invariance failed at width=%d %s: plain=%+v ansi=%+v",
					width, tc.name, gotPlain, gotANSI)
			}
		}
	}
}

// FuzzParseOmpComposer fuzzes ParseOmpComposer on arbitrary terminal buffers to
// prove panic-freedom and consistency with OmpIdlePromptShowing/OmpActivelyWorking.
func FuzzParseOmpComposer(f *testing.F) {
	f.Add("   π > ◒ GLM-5.3 > 📁 /repo ▶───1%───\n  ╰─ \n")
	f.Add("╭── π > ◒ Claude ──╮\n│ hello │\n╰──────────────────╯\n")
	f.Add("╭─── Tool Output ───╮\n│ ok │\n╰─── Tool Output ───╯\n")
	f.Add("\x1b[31m   ⠸ 5s > ◒ GPT ▶───20%───\x1b[0m\n  ╰─ draft\n")

	f.Fuzz(func(t *testing.T, capture string) {
		c := ParseOmpComposer(capture)
		idle := OmpIdlePromptShowing(capture)
		working := OmpActivelyWorking(capture, 120)
		// Fundamental invariant: a pane cannot simultaneously be idle-ready and actively working.
		if idle && working {
			t.Fatalf("contradictory omp state: both idle and actively working for capture=%q (composer=%+v)", capture, c)
		}
		if idle && !c.Found {
			t.Fatalf("OmpIdlePromptShowing=true despite composer.Found=false for capture=%q", capture)
		}
	})
}
