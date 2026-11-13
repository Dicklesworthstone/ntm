package tmux

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"testing/quick"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/tests/testutil/vttest"
)

func tmuxAgentForHarness(h vttest.HarnessID) AgentType {
	switch h {
	case vttest.HarnessClaude:
		return AgentClaude
	case vttest.HarnessCodex:
		return AgentCodex
	case vttest.HarnessAntigravity:
		return AgentAntigravity
	case vttest.HarnessOmp:
		return AgentOmp
	case vttest.HarnessGrok:
		return AgentGrok
	case vttest.HarnessOpencode:
		return AgentOpencode
	case vttest.HarnessPi:
		return AgentType("pi")
	default:
		return AgentUnknown
	}
}

func checkTmuxComposerInvariants(
	spec vttest.FrameSpec,
	stream []byte,
	plain string,
	seed uint64,
	testName string,
) string {
	agentType := tmuxAgentForHarness(spec.Harness)
	visible := ComposerVisibleForDelivery(plain, agentType)
	inspected := InspectComposer(plain, agentType)

	fail := func(expected string) string {
		b := vttest.HindsightBundle{
			Invariant:        "M1_COMPOSER_GEOMETRY_AND_MODAL_VETO",
			Harness:          spec.Harness,
			LifecycleState:   spec.State,
			Seed:             seed,
			Cols:             spec.Cols,
			Rows:             spec.Rows,
			Variant:          spec.Variant,
			ANSIHexSlice:     vttest.HexPreview(stream, 128),
			RenderedViewport: plain,
			ExpectedState:    expected,
			ActualState:      fmt.Sprintf("visible=%v marker=%v holdsText=%v queued=%v", visible, inspected.MarkerVisible, inspected.HoldsText, inspected.QueuedMessages),
			ReproCmd:         vttest.ReproCommand("./internal/tmux", testName, seed),
		}
		return "tmux composer metamorphic violation:\n" + b.FormatJSON()
	}

	switch spec.State {
	case vttest.StateBooting:
		if visible || inspected.MarkerVisible || inspected.HoldsText {
			return fail("visible=false marker=false holdsText=false")
		}
	case vttest.StateIdleEmpty:
		if !visible || !inspected.MarkerVisible || inspected.HoldsText {
			return fail("visible=true marker=true holdsText=false")
		}
	case vttest.StateIdleDraft:
		if !visible || !inspected.MarkerVisible || !inspected.HoldsText {
			return fail("visible=true marker=true holdsText=true")
		}
	case vttest.StateModalTrap:
		if visible {
			return fail("visible=false (modal trap MUST veto delivery)")
		}
	case vttest.StateExitedToShell:
		if visible || inspected.MarkerVisible || inspected.HoldsText {
			return fail("visible=false marker=false holdsText=false (shell exit MUST veto composer)")
		}
	}
	return ""
}

func TestVTMetamorphic_M1_ComposerGeometryAndModalVeto(t *testing.T) {
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()
	states := []vttest.LifecycleState{
		vttest.StateBooting,
		vttest.StateIdleEmpty,
		vttest.StateIdleDraft,
		vttest.StateModalTrap,
		vttest.StateExitedToShell,
	}

	// 1. Exhaustive canonical geometry matrix: 7 harnesses × 6 widths × 4 heights × all modal/state variants.
	var matrix []vttest.FrameSpec
	for _, h := range vttest.AllHarnesses {
		for _, cols := range vttest.StandardCols {
			for _, rows := range vttest.StandardRows {
				for _, st := range states {
					maxVar := 2
					if st == vttest.StateModalTrap && h == vttest.HarnessCodex {
						maxVar = 3
					}
					for v := range maxVar {
						matrix = append(matrix, vttest.FrameSpec{
							Harness: h,
							State:   st,
							Cols:    cols,
							Rows:    rows,
							Variant: v,
						})
					}
				}
			}
		}
	}
	if msg := vttest.RunParallelSeeds(len(matrix), func(idx int) string {
		vt := vttest.New(80, 24)
		spec := matrix[idx]
		stream := vt.LoadSpec(spec)
		return checkTmuxComposerInvariants(spec, stream, vt.CapturePlain(), baseSeed, testName)
	}); msg != "" {
		t.Fatal(msg)
	}

	// 2. 1,024 randomized seeded scenarios across geometries, wrapped multi-line drafts, and modal variants.
	const totalSeeds = 1024
	if msg := vttest.RunParallelSeeds(totalSeeds, func(i int) string {
		seed := vttest.DeriveSeed(baseSeed, i)
		rng := vttest.NewSeededRNG(seed)
		cols := vttest.StandardCols[rng.IntN(len(vttest.StandardCols))]
		rows := vttest.StandardRows[rng.IntN(len(vttest.StandardRows))]
		if i%8 != 0 && (cols > 160 || rows > 40) {
			cols = 100
			rows = 24
		}
		spec := vttest.FrameSpec{
			Harness:    vttest.AllHarnesses[rng.IntN(len(vttest.AllHarnesses))],
			State:      states[rng.IntN(len(states))],
			Cols:       cols,
			Rows:       rows,
			Variant:    rng.IntN(6),
			DraftLines: vttest.RandomDraftLines(rng, 4),
		}
		vt := vttest.New(cols, rows)
		stream := vt.LoadSpec(spec)
		return checkTmuxComposerInvariants(spec, stream, vt.CapturePlain(), seed, testName)
	}); msg != "" {
		t.Fatal(msg)
	}
}

func TestVTMetamorphic_M2_ComposerPTYChunkInvariance(t *testing.T) {
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()

	const totalSeeds = 1024
	if msg := vttest.RunParallelSeeds(totalSeeds, func(i int) string {
		seed := vttest.DeriveSeed(baseSeed, i)
		rng := vttest.NewSeededRNG(seed)
		cols := vttest.StandardCols[rng.IntN(len(vttest.StandardCols))]
		rows := vttest.StandardRows[rng.IntN(len(vttest.StandardRows))]
		if i%8 != 0 && (cols > 160 || rows > 40) {
			cols = 100
			rows = 24
		}
		spec := vttest.FrameSpec{
			Harness:    vttest.AllHarnesses[i%len(vttest.AllHarnesses)],
			State:      vttest.AllLifecycleStates[(i/len(vttest.AllHarnesses))%len(vttest.AllLifecycleStates)],
			Cols:       cols,
			Rows:       rows,
			Variant:    rng.IntN(6),
			DraftLines: vttest.RandomDraftLines(rng, 3),
		}

		stream := vttest.GenerateANSIStream(spec)
		fullVT := vttest.New(cols, rows)
		_, _ = fullVT.Write(stream)
		wantPlain := fullVT.CapturePlain()

		chunkedVT := vttest.New(cols, rows)
		chunks, splits := vttest.RandomByteChunks(rng, stream, 17)
		for _, ch := range chunks {
			_, _ = chunkedVT.Write(ch)
		}
		gotPlain := chunkedVT.CapturePlain()

		if gotPlain != wantPlain || i%4 == 0 {
			agentType := tmuxAgentForHarness(spec.Harness)
			wantVis := ComposerVisibleForDelivery(wantPlain, agentType)
			gotVis := ComposerVisibleForDelivery(gotPlain, agentType)
			wantInsp := InspectComposer(wantPlain, agentType)
			gotInsp := InspectComposer(gotPlain, agentType)

			if gotPlain != wantPlain || gotVis != wantVis || gotInsp != wantInsp {
				b := vttest.HindsightBundle{
					Invariant:        "M2_COMPOSER_PTY_CHUNK_INVARIANCE",
					Harness:          spec.Harness,
					LifecycleState:   spec.State,
					Seed:             seed,
					Cols:             spec.Cols,
					Rows:             spec.Rows,
					Variant:          spec.Variant,
					ChunkSplits:      splits,
					ANSIHexSlice:     vttest.HexPreview(stream, 128),
					RenderedViewport: gotPlain,
					ExpectedState:    fmt.Sprintf("vis=%v insp=%+v", wantVis, wantInsp),
					ActualState:      fmt.Sprintf("vis=%v insp=%+v", gotVis, gotInsp),
					ReproCmd:         vttest.ReproCommand("./internal/tmux", testName, seed),
				}
				return "M2 composer chunk invariance violation:\n" + b.FormatJSON()
			}
		}
		return ""
	}); msg != "" {
		t.Fatal(msg)
	}

	fullVT := vttest.New(100, 24)
	chunkedVT := vttest.New(100, 24)
	prop := func(hIdx, sIdx uint8, splitSeed int64) bool {
		h := vttest.AllHarnesses[int(hIdx)%len(vttest.AllHarnesses)]
		st := vttest.AllLifecycleStates[int(sIdx)%len(vttest.AllLifecycleStates)]
		stream := vttest.GenerateANSIStream(vttest.FrameSpec{Harness: h, State: st, Cols: 100, Rows: 24})
		fullVT.Reset(100, 24)
		_, _ = fullVT.Write(stream)
		chunkedVT.Reset(100, 24)
		r := rand.New(rand.NewPCG(uint64(splitSeed), 0xabcdef0123456789))
		chunks, _ := vttest.RandomByteChunks(r, stream, 17)
		for _, ch := range chunks {
			_, _ = chunkedVT.Write(ch)
		}
		agentType := tmuxAgentForHarness(h)
		return ComposerVisibleForDelivery(fullVT.CapturePlain(), agentType) ==
			ComposerVisibleForDelivery(chunkedVT.CapturePlain(), agentType)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatalf("quick.Check M2 composer chunking failed: %v", err)
	}
}

// simulateProductionClearComposer executes the exact keystroke-selection and
// verification state machine of ClearComposerContext / clearOmpComposerContext
// against the deterministic VT100 oracle without wall-clock sleeps.
func simulateProductionClearComposer(vt *vttest.Terminal, h vttest.HarnessID) {
	agentType := tmuxAgentForHarness(h)
	canonical := agentType.Canonical()

	sendBurst := func(keys []string) {
		for i, k := range keys {
			if i > 0 {
				vt.AdvanceSimClock(composerClearKeyGap)
			}
			vt.SimulateKey(h, k)
		}
	}

	if canonical == AgentOmp {
		for range composerClearMaxRounds {
			comp := agent.ParseOmpComposer(vt.CapturePlain())
			if !comp.Found || comp.Draft == "" {
				return
			}
			sendBurst(ComposerClearKeys(AgentOmp))
			vt.AdvanceSimClock(600 * time.Millisecond)
		}
		return
	}

	markers := composerMarkersForAgent(agentType)
	if len(markers) == 0 {
		sendBurst(ComposerClearKeys(agentType))
		return
	}

	transcriptDismissed := false
	for round := 1; round <= composerClearMaxRounds; round++ {
		capture := vt.CapturePlain()
		if canonical == AgentCodex && codexTranscriptBrowsing(capture) {
			if transcriptDismissed {
				return
			}
			sendBurst([]string{"Escape"})
			transcriptDismissed = true
			vt.AdvanceSimClock(composerClearVerifyWait)
			continue
		}
		markerVisible, empty, _ := composerBlockEmpty(capture, markers, composerPlaceholderPrefixes(agentType))
		if !markerVisible {
			if canonical != AgentCodex {
				sendBurst(ComposerClearKeys(agentType))
			}
			return
		}
		queued := InspectComposer(capture, agentType).QueuedMessages
		if canonical == AgentCodex && empty && !queued {
			return
		}
		var keys []string
		drain := 0
		switch {
		case round == 1:
			keys = ComposerClearKeys(agentType)
		case round < composerClearMaxRounds:
			keys = composerClearEscalationKeys
		default:
			_, _, before := composerBlockEmpty(capture, markers, composerPlaceholderPrefixes(agentType))
			drain = backspaceCount(before)
		}
		sendBurst(keys)
		for range drain {
			vt.SimulateKey(h, "BSpace")
		}
		vt.AdvanceSimClock(composerClearVerifyWait)
		after := vt.CapturePlain()
		if canonical == AgentCodex && codexTranscriptBrowsing(after) {
			continue
		}
		found, afterEmpty, _ := composerBlockEmpty(after, markers, composerPlaceholderPrefixes(agentType))
		stillQueued := InspectComposer(after, agentType).QueuedMessages
		if !found || (afterEmpty && !stillQueued) {
			return
		}
	}
}

func TestVTMetamorphic_M4_IdempotentClearComposerModalTrapSafety(t *testing.T) {
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()
	vt := vttest.New(100, 24)

	// Oracle self-check (mutation proof): confirm SimulateKey catches buggy Escape pairs
	// in Codex/Claude and unguarded empty-draft double-Ctrl+C in omp.
	for _, h := range []vttest.HarnessID{vttest.HarnessCodex, vttest.HarnessClaude} {
		vt.LoadSpec(vttest.FrameSpec{Harness: h, State: vttest.StateIdleEmpty, Cols: 100, Rows: 24})
		vt.SimulateKey(h, "Escape")
		vt.SimulateKey(h, "Escape")
		if vt.Interactive().State != vttest.StateModalTrap || ComposerVisibleForDelivery(vt.CapturePlain(), tmuxAgentForHarness(h)) {
			t.Fatalf("mutation self-check failed: double-Escape in %s did not trigger modal trap", h)
		}
	}
	vt.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessOmp, State: vttest.StateIdleEmpty, Cols: 100, Rows: 24})
	vt.SimulateKey(vttest.HarnessOmp, "C-c")
	vt.AdvanceSimClock(100 * time.Millisecond)
	vt.SimulateKey(vttest.HarnessOmp, "C-c")
	if !vt.Interactive().ExitedViaDoubleCC {
		t.Fatal("mutation self-check failed: unspaced double Ctrl+C on empty omp composer did not exit")
	}

	// Codex recovery from Browsing transcript (Variant 0) via single Escape in ClearComposerContext.
	vt.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessCodex, State: vttest.StateModalTrap, Cols: 100, Rows: 24, Variant: 0})
	simulateProductionClearComposer(vt, vttest.HarnessCodex)
	if !ComposerVisibleForDelivery(vt.CapturePlain(), AgentCodex) || codexTranscriptBrowsing(vt.CapturePlain()) {
		t.Fatalf("ClearComposer failed to recover Codex from transcript browsing: %q", vt.CapturePlain())
	}

	const totalSeeds = 1024
	if msg := vttest.RunParallelSeeds(totalSeeds, func(i int) string {
		seed := vttest.DeriveSeed(baseSeed, i)
		rng := vttest.NewSeededRNG(seed)
		h := vttest.AllHarnesses[i%len(vttest.AllHarnesses)]
		agentType := tmuxAgentForHarness(h)
		cols := vttest.StandardCols[rng.IntN(len(vttest.StandardCols))]
		rows := vttest.StandardRows[rng.IntN(len(vttest.StandardRows))]
		if i%8 != 0 && (cols > 160 || rows > 40) {
			cols = 100
			rows = 24
		}
		localVT := vttest.New(cols, rows)

		if i%2 == 0 {
			spec := vttest.FrameSpec{Harness: h, State: vttest.StateIdleEmpty, Cols: cols, Rows: rows, Variant: rng.IntN(4)}
			stream := localVT.LoadSpec(spec)
			repeats := 1 + rng.IntN(3)
			for range repeats {
				simulateProductionClearComposer(localVT, h)
				localVT.AdvanceSimClock(700 * time.Millisecond)
			}
			plain := localVT.CapturePlain()
			sim := localVT.Interactive()
			insp := InspectComposer(plain, agentType)
			if sim.State != vttest.StateIdleEmpty || sim.ExitedViaDoubleCC || !ComposerVisibleForDelivery(plain, agentType) || insp.HoldsText {
				b := vttest.HindsightBundle{
					Invariant:        "M4_EMPTY_CLEAR_MODAL_TRAP_SAFETY",
					Harness:          h,
					LifecycleState:   vttest.StateIdleEmpty,
					Seed:             seed,
					Cols:             cols,
					Rows:             rows,
					ANSIHexSlice:     vttest.HexPreview(stream, 128),
					RenderedViewport: plain,
					ExpectedState:    "State=S2_IDLE_EMPTY_COMPOSER visible=true holdsText=false exitedDoubleCC=false",
					ActualState:      fmt.Sprintf("State=%s visible=%v holdsText=%v exitedDoubleCC=%v", sim.State, ComposerVisibleForDelivery(plain, agentType), insp.HoldsText, sim.ExitedViaDoubleCC),
					ReproCmd:         vttest.ReproCommand("./internal/tmux", testName, seed),
				}
				return "M4 empty-composer clear violation:\n" + b.FormatJSON()
			}
			return ""
		}

		spec := vttest.FrameSpec{
			Harness:    h,
			State:      vttest.StateIdleDraft,
			Cols:       cols,
			Rows:       rows,
			Variant:    rng.IntN(4),
			DraftLines: vttest.RandomDraftLines(rng, 4),
		}
		stream := localVT.LoadSpec(spec)
		simulateProductionClearComposer(localVT, h)
		plain := localVT.CapturePlain()
		sim := localVT.Interactive()
		insp := InspectComposer(plain, agentType)
		if sim.State != vttest.StateIdleEmpty || !ComposerVisibleForDelivery(plain, agentType) || !insp.MarkerVisible || insp.HoldsText {
			b := vttest.HindsightBundle{
				Invariant:        "M4_DRAFT_CLEAR_TO_EMPTY_COMPOSER",
				Harness:          h,
				LifecycleState:   vttest.StateIdleDraft,
				Seed:             seed,
				Cols:             cols,
				Rows:             rows,
				ANSIHexSlice:     vttest.HexPreview(stream, 128),
				RenderedViewport: plain,
				ExpectedState:    "State=S2_IDLE_EMPTY_COMPOSER visible=true marker=true holdsText=false",
				ActualState:      fmt.Sprintf("State=%s visible=%v marker=%v holdsText=%v", sim.State, ComposerVisibleForDelivery(plain, agentType), insp.MarkerVisible, insp.HoldsText),
				ReproCmd:         vttest.ReproCommand("./internal/tmux", testName, seed),
			}
			return "M4 draft-clear violation:\n" + b.FormatJSON()
		}
		return ""
	}); msg != "" {
		t.Fatal(msg)
	}
}

func TestVTMetamorphic_M5_InterruptAndExitKeyDiscipline(t *testing.T) {
	escapeAgents := []AgentType{AgentOpencode, "opencode", "opencode-cli", AgentOmp, "oh-my-pi"}
	for _, a := range escapeAgents {
		if got := InterruptKeyForAgent(a); got != "Escape" {
			t.Fatalf("InterruptKeyForAgent(%q) = %q, want Escape", a, got)
		}
		if lbl := InterruptKeyLabel(a); lbl != "Escape" {
			t.Fatalf("InterruptKeyLabel(%q) = %q, want Escape", a, lbl)
		}
	}

	ctrlCAgents := []AgentType{
		AgentClaude, "claude", "claude_code",
		AgentCodex, "codex", "codex-cli",
		AgentAntigravity, "antigravity",
		AgentGrok,
		"pi",
	}
	for _, a := range ctrlCAgents {
		if got := InterruptKeyForAgent(a); got != "C-c" {
			t.Fatalf("InterruptKeyForAgent(%q) = %q, want C-c", a, got)
		}
		if lbl := InterruptKeyLabel(a); lbl != "Ctrl+C" {
			t.Fatalf("InterruptKeyLabel(%q) = %q, want Ctrl+C", a, lbl)
		}
	}

	for _, h := range vttest.AllHarnesses {
		a := tmuxAgentForHarness(h)
		for _, k := range append([]string{InterruptKeyForAgent(a)}, ComposerClearKeys(a)...) {
			if k == "C-d" || k == "exit" {
				t.Fatalf("harness %s emitted destructive shell EOF key %q", h, k)
			}
		}
	}

	// Negative oracle check: sending C-c (wrong key!) to OpenCode in S5 exits to shell instead of cancelling.
	vtNeg := vttest.New(100, 24)
	vtNeg.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessOpencode, State: vttest.StateWorkingStreaming, Cols: 100, Rows: 24})
	vtNeg.SimulateKey(vttest.HarnessOpencode, "C-c")
	if vtNeg.Interactive().State != vttest.StateExitedToShell {
		t.Fatalf("negative check failed: C-c in OpenCode S5 should exit to shell, got %s", vtNeg.Interactive().State)
	}

	baseSeed := vttest.ResolveSeed()
	testName := t.Name()
	const totalSeeds = 1024
	if msg := vttest.RunParallelSeeds(totalSeeds, func(i int) string {
		seed := vttest.DeriveSeed(baseSeed, i)
		rng := vttest.NewSeededRNG(seed)
		h := vttest.AllHarnesses[i%len(vttest.AllHarnesses)]
		agentType := tmuxAgentForHarness(h)
		cols := vttest.StandardCols[rng.IntN(len(vttest.StandardCols))]
		rows := vttest.StandardRows[rng.IntN(len(vttest.StandardRows))]
		if i%8 != 0 && (cols > 160 || rows > 40) {
			cols = 100
			rows = 24
		}

		vt := vttest.New(cols, rows)
		spec := vttest.FrameSpec{
			Harness: h,
			State:   vttest.StateWorkingStreaming,
			Cols:    cols,
			Rows:    rows,
			Variant: rng.IntN(4),
		}
		stream := vt.LoadSpec(spec)
		intrKey := InterruptKeyForAgent(agentType)
		vt.SimulateKey(h, intrKey)
		plain := vt.CapturePlain()
		sim := vt.Interactive()
		if sim.State != vttest.StateIdleEmpty || !ComposerVisibleForDelivery(plain, agentType) {
			b := vttest.HindsightBundle{
				Invariant:        "M5_INTERRUPT_KEY_DISCIPLINE",
				Harness:          h,
				LifecycleState:   vttest.StateWorkingStreaming,
				Seed:             seed,
				Cols:             cols,
				Rows:             rows,
				ANSIHexSlice:     vttest.HexPreview(stream, 128),
				RenderedViewport: plain,
				ExpectedState:    fmt.Sprintf("key=%s -> State=S2_IDLE_EMPTY_COMPOSER visible=true", intrKey),
				ActualState:      fmt.Sprintf("key=%s -> State=%s visible=%v", intrKey, sim.State, ComposerVisibleForDelivery(plain, agentType)),
				ReproCmd:         vttest.ReproCommand("./internal/tmux", testName, seed),
			}
			return "M5 interrupt transition violation:\n" + b.FormatJSON()
		}

		exitSpec := vttest.FrameSpec{Harness: h, State: vttest.StateExitedToShell, Cols: cols, Rows: rows, Variant: rng.IntN(2)}
		vt.LoadSpec(exitSpec)
		if ComposerVisibleForDelivery(vt.CapturePlain(), agentType) {
			return fmt.Sprintf("M5 shell exit accepted as visible composer for %s", h)
		}
		vt.SimulateKey(h, intrKey)
		if vt.Interactive().ModalReason == "destructive_shell_eof" {
			return fmt.Sprintf("M5 interrupt key %q triggered destructive shell EOF on %s", intrKey, h)
		}
		return ""
	}); msg != "" {
		t.Fatal(msg)
	}
}
