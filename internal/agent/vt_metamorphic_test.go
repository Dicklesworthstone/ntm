package agent

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"testing/quick"

	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/tests/testutil/vttest"
)

var agentVTPool = sync.Pool{
	New: func() any {
		return vttest.New(100, 24)
	},
}

func registerShippedPiForTest(t *testing.T) {
	t.Helper()
	resetPluginPatternsForTest()
	t.Cleanup(resetPluginPatternsForTest)
	shipped := plugins.ShippedPiPlugin()
	if err := RegisterPlugin(
		shipped.Name,
		shipped.Readiness.IdlePatterns,
		shipped.Readiness.WorkingPatterns,
		shipped.Readiness.ErrorPatterns,
	); err != nil {
		t.Fatalf("RegisterPlugin(pi): %v", err)
	}
}

func agentTypeForHarness(h vttest.HarnessID) AgentType {
	switch h {
	case vttest.HarnessClaude:
		return AgentTypeClaudeCode
	case vttest.HarnessCodex:
		return AgentTypeCodex
	case vttest.HarnessAntigravity:
		return AgentTypeAntigravity
	case vttest.HarnessOmp:
		return AgentTypeOmp
	case vttest.HarnessGrok:
		return AgentTypeGrok
	case vttest.HarnessOpencode:
		return AgentTypeOpencode
	case vttest.HarnessPi:
		return AgentType("pi")
	default:
		return AgentTypeUnknown
	}
}

func activelyWorkingForHarness(h vttest.HarnessID, capture string, cols int) bool {
	switch h {
	case vttest.HarnessClaude:
		return ClaudeActivelyWorking(capture, cols)
	case vttest.HarnessCodex:
		return CodexActivelyWorking(capture, cols)
	case vttest.HarnessAntigravity:
		return AntigravityActivelyWorking(capture, cols)
	case vttest.HarnessOmp:
		return OmpActivelyWorking(capture, cols)
	case vttest.HarnessGrok:
		return GrokActivelyWorking(capture, cols)
	case vttest.HarnessOpencode:
		return OpencodeActivelyWorking(capture, cols)
	case vttest.HarnessPi:
		return PluginActivelyWorking(capture, AgentType("pi"), cols)
	default:
		return false
	}
}

func checkAgentLifecycleInvariants(
	parser Parser,
	spec vttest.FrameSpec,
	stream []byte,
	plain string,
	seed uint64,
	testName string,
) string {
	agentType := agentTypeForHarness(spec.Harness)
	parsed, err := parser.ParseWithHint(plain, agentType)
	if err != nil {
		return fmt.Sprintf("ParseWithHint(%s): %v", spec.Harness, err)
	}
	liveWork := activelyWorkingForHarness(spec.Harness, plain, spec.Cols)
	gateKind, gated := DetectInteractiveGate(plain, spec.Cols)

	fail := func(expected, actual string) string {
		b := vttest.HindsightBundle{
			Invariant:        "M1_GEOMETRY_INVARIANCE",
			Harness:          spec.Harness,
			LifecycleState:   spec.State,
			Seed:             seed,
			Cols:             spec.Cols,
			Rows:             spec.Rows,
			Variant:          spec.Variant,
			ANSIHexSlice:     vttest.HexPreview(stream, 128),
			RenderedViewport: plain,
			ExpectedState:    expected,
			ActualState:      actual,
			ReproCmd:         vttest.ReproCommand("./internal/agent", testName, seed),
		}
		return "agent metamorphic violation:\n" + b.FormatJSON()
	}

	switch spec.State {
	case vttest.StateBooting:
		if liveWork || parsed.IsWorking || parsed.IsRateLimited || gated {
			return fail(
				"booting(liveWork=false, working=false, rateLimited=false, gated=false)",
				fmt.Sprintf("liveWork=%v working=%v idle=%v rateLimited=%v gated=%v(%s)",
					liveWork, parsed.IsWorking, parsed.IsIdle, parsed.IsRateLimited, gated, gateKind),
			)
		}

	case vttest.StateIdleEmpty:
		if !parsed.IsIdle || parsed.IsWorking || liveWork || parsed.IsRateLimited || gated {
			return fail(
				"idle_empty(idle=true, working=false, liveWork=false, rateLimited=false, gated=false)",
				fmt.Sprintf("idle=%v working=%v liveWork=%v rateLimited=%v gated=%v(%s)",
					parsed.IsIdle, parsed.IsWorking, liveWork, parsed.IsRateLimited, gated, gateKind),
			)
		}
		switch spec.Harness {
		case vttest.HarnessOmp:
			if pct, win, ok := OmpContextUsage(plain); !ok || pct != 7 || win != 262000 {
				return fail("omp_context_usage(7%, 262000, ok=true)", fmt.Sprintf("pct=%v win=%d ok=%v", pct, win, ok))
			}
		case vttest.HarnessOpencode:
			if spec.Variant%2 == 1 && spec.Cols >= 120 {
				if pct, tok, ok := OpencodeContextUsage(plain); !ok || pct != 11 || tok != 14200 {
					return fail("oc_context_usage(11%, 14200, ok=true)", fmt.Sprintf("pct=%v tok=%d ok=%v", pct, tok, ok))
				}
			}
		case vttest.HarnessGrok:
			if got := GrokStatusLineModel(plain); got != "grok-4.6" {
				return fail("grok_model=grok-4.6", fmt.Sprintf("grok_model=%q", got))
			}
		case vttest.HarnessCodex:
			if parsed.ContextRemaining == nil || *parsed.ContextRemaining != 78 {
				return fail("cod_context_remaining=78", fmt.Sprintf("context_remaining=%v", parsed.ContextRemaining))
			}
		case vttest.HarnessAntigravity:
			if parsed.MemoryMB == nil || *parsed.MemoryMB != 342.5 {
				return fail("agy_memory_mb=342.5", fmt.Sprintf("memory_mb=%v", parsed.MemoryMB))
			}
		}

	case vttest.StateIdleDraft:
		if !parsed.IsIdle || parsed.IsWorking || liveWork || parsed.IsRateLimited || gated {
			return fail(
				"idle_draft(idle=true, working=false, liveWork=false, rateLimited=false, gated=false)",
				fmt.Sprintf("idle=%v working=%v liveWork=%v rateLimited=%v gated=%v(%s)",
					parsed.IsIdle, parsed.IsWorking, liveWork, parsed.IsRateLimited, gated, gateKind),
			)
		}

	case vttest.StateWorkingStreaming:
		if !liveWork || !parsed.IsWorking || parsed.IsIdle || parsed.IsRateLimited {
			return fail(
				"working_streaming(liveWork=true, working=true, idle=false, rateLimited=false)",
				fmt.Sprintf("liveWork=%v working=%v idle=%v rateLimited=%v",
					liveWork, parsed.IsWorking, parsed.IsIdle, parsed.IsRateLimited),
			)
		}

	case vttest.StateRateLimitedError:
		if (!parsed.IsRateLimited && !parsed.IsInError) || parsed.IsIdle || parsed.IsWorking || liveWork {
			return fail(
				"rate_limited_or_error(rateLimited||inError=true, idle=false, working=false)",
				fmt.Sprintf("rateLimited=%v inError=%v idle=%v working=%v liveWork=%v",
					parsed.IsRateLimited, parsed.IsInError, parsed.IsIdle, parsed.IsWorking, liveWork),
			)
		}

	case vttest.StateExitedToShell:
		if parsed.IsIdle || parsed.IsWorking || liveWork {
			return fail(
				"exited_to_shell(idle=false, working=false, liveWork=false)",
				fmt.Sprintf("idle=%v working=%v liveWork=%v", parsed.IsIdle, parsed.IsWorking, liveWork),
			)
		}
	}
	return ""
}

func TestVTMetamorphic_M1_AgentParserGeometryInvariance(t *testing.T) {
	registerShippedPiForTest(t)
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()

	// 1. Exhaustive canonical geometry matrix: 7 harnesses × 6 widths × 4 heights × 7 states = 1,176 frames.
	var matrix []vttest.FrameSpec
	for _, h := range vttest.AllHarnesses {
		for _, cols := range vttest.StandardCols {
			for _, rows := range vttest.StandardRows {
				for _, st := range vttest.AllLifecycleStates {
					matrix = append(matrix, vttest.FrameSpec{
						Harness: h,
						State:   st,
						Cols:    cols,
						Rows:    rows,
						Variant: (cols + rows) % 4,
					})
				}
			}
		}
	}
	parser := NewParser()
	if msg := vttest.RunParallelSeeds(len(matrix), func(idx int) string {
		vt := agentVTPool.Get().(*vttest.Terminal)
		defer agentVTPool.Put(vt)
		spec := matrix[idx]
		stream := vt.LoadSpec(spec)
		return checkAgentLifecycleInvariants(parser, spec, stream, vt.CapturePlain(), baseSeed, testName)
	}); msg != "" {
		t.Fatal(msg)
	}

	// 2. 1,024 randomized seeded scenarios varying geometry, draft wrapping, and transcripts.
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
			State:      vttest.AllLifecycleStates[rng.IntN(len(vttest.AllLifecycleStates))],
			Cols:       cols,
			Rows:       rows,
			Variant:    rng.IntN(6),
			DraftLines: vttest.RandomDraftLines(rng, 3),
		}
		vt := agentVTPool.Get().(*vttest.Terminal)
		defer agentVTPool.Put(vt)
		stream := vt.LoadSpec(spec)
		return checkAgentLifecycleInvariants(parser, spec, stream, vt.CapturePlain(), seed, testName)
	}); msg != "" {
		t.Fatal(msg)
	}
}

func TestVTMetamorphic_M2_AgentParserPTYChunkInvariance(t *testing.T) {
	registerShippedPiForTest(t)
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()
	parser := NewParser()

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
		fullVT := agentVTPool.Get().(*vttest.Terminal)
		fullVT.Reset(spec.Cols, spec.Rows)
		_, _ = fullVT.Write(stream)
		wantPlain := fullVT.CapturePlain()
		agentVTPool.Put(fullVT)

		chunkedVT := agentVTPool.Get().(*vttest.Terminal)
		chunkedVT.Reset(spec.Cols, spec.Rows)
		chunks, splits := vttest.RandomByteChunks(rng, stream, 17)
		for _, ch := range chunks {
			_, _ = chunkedVT.Write(ch)
		}
		gotPlain := chunkedVT.CapturePlain()
		agentVTPool.Put(chunkedVT)

		if gotPlain != wantPlain {
			agentType := agentTypeForHarness(spec.Harness)
			gotParsed, _ := parser.ParseWithHint(gotPlain, agentType)
			gotGate, gotGated := DetectInteractiveGate(gotPlain, spec.Cols)
			wantParsed, _ := parser.ParseWithHint(wantPlain, agentType)
			wantGate, wantGated := DetectInteractiveGate(wantPlain, spec.Cols)
			b := vttest.HindsightBundle{
				Invariant:        "M2_PTY_BYTE_CHUNKING_INVARIANCE",
				Harness:          spec.Harness,
				LifecycleState:   spec.State,
				Seed:             seed,
				Cols:             spec.Cols,
				Rows:             spec.Rows,
				Variant:          spec.Variant,
				ChunkSplits:      splits,
				ANSIHexSlice:     vttest.HexPreview(stream, 128),
				RenderedViewport: gotPlain,
				ExpectedState:    fmt.Sprintf("idle=%v work=%v rl=%v err=%v gate=%v(%s)", wantParsed.IsIdle, wantParsed.IsWorking, wantParsed.IsRateLimited, wantParsed.IsInError, wantGated, wantGate),
				ActualState:      fmt.Sprintf("idle=%v work=%v rl=%v err=%v gate=%v(%s)", gotParsed.IsIdle, gotParsed.IsWorking, gotParsed.IsRateLimited, gotParsed.IsInError, gotGated, gotGate),
				ReproCmd:         vttest.ReproCommand("./internal/agent", testName, seed),
			}
			return "M2 chunk invariance violation:\n" + b.FormatJSON()
		}
		if i%4 == 0 {
			return checkAgentLifecycleInvariants(parser, spec, stream, gotPlain, seed, testName)
		}
		return ""
	}); msg != "" {
		t.Fatal(msg)
	}

	fullVT := vttest.New(100, 24)
	chunkedVT := vttest.New(100, 24)
	prop := func(hIdx, sIdx uint8, splitSeed int64) bool {
		spec := vttest.FrameSpec{
			Harness: vttest.AllHarnesses[int(hIdx)%len(vttest.AllHarnesses)],
			State:   vttest.AllLifecycleStates[int(sIdx)%len(vttest.AllLifecycleStates)],
			Cols:    100,
			Rows:    24,
		}
		stream := vttest.GenerateANSIStream(spec)
		fullVT.Reset(100, 24)
		_, _ = fullVT.Write(stream)

		chunkedVT.Reset(100, 24)
		r := rand.New(rand.NewPCG(uint64(splitSeed), 0x123456789abcdef0))
		chunks, _ := vttest.RandomByteChunks(r, stream, 17)
		for _, ch := range chunks {
			_, _ = chunkedVT.Write(ch)
		}
		return fullVT.CapturePlain() == chunkedVT.CapturePlain()
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatalf("quick.Check M2 agent parser failed: %v", err)
	}
}

func TestVTRegression_TallBlankPaddedViewportAndShellExit(t *testing.T) {
	registerShippedPiForTest(t)
	parser := NewParser()

	// Regression 1: Inline TUI rendered near top of a tall 100-row viewport (80+ trailing blank rows).
	vt := vttest.New(120, 100)
	vt.WriteString("\x1b[2J\x1b[2;1H⠋ Working (8s • esc to interrupt)\r\n\x1b[4;1H› ")
	plain := vt.CapturePlain()
	if !CodexActivelyWorking(plain, 120) {
		t.Fatal("tall blank-padded viewport hid Codex live working spinner")
	}

	// Regression 2: Trust gate rendered near top of a tall 100-row viewport.
	vtGate := vttest.New(120, 100)
	vtGate.WriteString("\x1b[2J\x1b[2;1HDo you trust the files in this folder?\r\n❯ 1. Yes, proceed\r\n  2. No, exit")
	if kind, ok := DetectInteractiveGate(vtGate.CapturePlain(), 120); !ok || kind == "" {
		t.Fatalf("tall blank-padded viewport hid workspace trust gate: ok=%v kind=%q", ok, kind)
	}

	// Regression 3: Centered OpenCode fresh-spawn composer on 100-row viewport.
	vtOC := vttest.New(160, 100)
	vtOC.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessOpencode, State: vttest.StateIdleEmpty, Cols: 160, Rows: 100, Variant: 0})
	if parsed, err := parser.ParseWithHint(vtOC.CapturePlain(), AgentTypeOpencode); err != nil || !parsed.IsIdle {
		t.Fatalf("centered OpenCode composer on 100-row viewport was not classified idle: %+v, err=%v", parsed, err)
	}

	// Regression 4: Exited Codex shell prompt containing "$ " must NOT classify as working.
	vtExit := vttest.New(80, 24)
	vtExit.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessCodex, State: vttest.StateExitedToShell, Cols: 80, Rows: 24, Variant: 0})
	if parsed, err := parser.ParseWithHint(vtExit.CapturePlain(), AgentTypeCodex); err != nil || parsed.IsWorking || parsed.IsIdle {
		t.Fatalf("exited Codex shell prompt misclassified: %+v, err=%v", parsed, err)
	}
}
