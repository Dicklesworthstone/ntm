package status

import (
	"fmt"
	"sync"
	"testing"
	"testing/quick"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/tests/testutil/vttest"
)

var statusVTPool = sync.Pool{
	New: func() any {
		return vttest.New(100, 24)
	},
}

func registerShippedPiStatusForTest(t *testing.T) {
	t.Helper()
	shipped := plugins.ShippedPiPlugin()
	if err := agent.RegisterPlugin(
		shipped.Name,
		shipped.Readiness.IdlePatterns,
		shipped.Readiness.WorkingPatterns,
		shipped.Readiness.ErrorPatterns,
	); err != nil {
		t.Fatalf("RegisterPlugin(pi): %v", err)
	}
	t.Cleanup(func() {
		_ = agent.RegisterPlugin("pi", nil, nil, nil)
	})
}

func TestVTMetamorphic_M3_SiblingPaneIsolationAndStatusGeometry(t *testing.T) {
	registerShippedPiStatusForTest(t)
	detector := NewDetector()
	baseSeed := vttest.ResolveSeed()
	testName := t.Name()
	observedAt := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	checkFrame := func(spec vttest.FrameSpec, stream []byte, plain string, lastActivity time.Time, seed uint64) string {
		agentName := string(spec.Harness)
		got := detector.AnalyzeAt("%42", "pane-1", agentName, plain, lastActivity, observedAt)
		idleDirect := DetectIdleFromOutput(plain, agentName)

		fail := func(inv, expected, actual string) string {
			b := vttest.HindsightBundle{
				Invariant:        inv,
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
				ReproCmd:         vttest.ReproCommand("./internal/status", testName, seed),
			}
			return "status metamorphic violation:\n" + b.FormatJSON()
		}

		switch spec.State {
		case vttest.StateIdleEmpty, vttest.StateIdleDraft:
			if got.State != StateIdle || !idleDirect {
				return fail(
					"M3_SIBLING_PANE_ISOLATION",
					fmt.Sprintf("State=%s DetectIdleFromOutput=true (lastActivity delta=%s)", StateIdle, observedAt.Sub(lastActivity)),
					fmt.Sprintf("State=%s ErrorType=%s DetectIdleFromOutput=%v", got.State, got.ErrorType, idleDirect),
				)
			}
		case vttest.StateWorkingStreaming:
			if got.State != StateWorking || idleDirect {
				return fail(
					"M1_STATUS_WORKING_INVARIANCE",
					fmt.Sprintf("State=%s DetectIdleFromOutput=false", StateWorking),
					fmt.Sprintf("State=%s DetectIdleFromOutput=%v", got.State, idleDirect),
				)
			}
		case vttest.StateRateLimitedError:
			if spec.Harness == vttest.HarnessOmp {
				// omp renders its dismissable provider-error block directly above its
				// live interactive composer ("Dismissed when you send your next message.");
				// OmpProviderError must fire and UnifiedDetector must classify StateError.
				if _, failed := agent.OmpProviderError(plain); got.State != StateError || !failed {
					return fail(
						"M1_STATUS_ERROR_INVARIANCE",
						fmt.Sprintf("State=%s OmpProviderError=true", StateError),
						fmt.Sprintf("State=%s ErrorType=%s OmpProviderError=%v", got.State, got.ErrorType, failed),
					)
				}
			} else if got.State != StateError || idleDirect {
				return fail(
					"M1_STATUS_ERROR_INVARIANCE",
					fmt.Sprintf("State=%s DetectIdleFromOutput=false", StateError),
					fmt.Sprintf("State=%s ErrorType=%s DetectIdleFromOutput=%v", got.State, got.ErrorType, idleDirect),
				)
			}
		case vttest.StateExitedToShell:
			if idleDirect {
				return fail(
					"M1_STATUS_SHELL_EXIT_VETO",
					"DetectIdleFromOutput=false",
					fmt.Sprintf("State=%s DetectIdleFromOutput=%v", got.State, idleDirect),
				)
			}
		}
		return ""
	}

	// 1. Exhaustive 7 harnesses × 24 canonical geometries under frantic sibling activity (lastActivity == observedAt, GH #344).
	states := []vttest.LifecycleState{
		vttest.StateIdleEmpty,
		vttest.StateIdleDraft,
		vttest.StateWorkingStreaming,
		vttest.StateRateLimitedError,
		vttest.StateExitedToShell,
	}
	var matrix []vttest.FrameSpec
	for _, h := range vttest.AllHarnesses {
		for _, cols := range vttest.StandardCols {
			for _, rows := range vttest.StandardRows {
				for _, st := range states {
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
	if msg := vttest.RunParallelSeeds(len(matrix), func(idx int) string {
		vt := statusVTPool.Get().(*vttest.Terminal)
		defer statusVTPool.Put(vt)
		spec := matrix[idx]
		stream := vt.LoadSpec(spec)
		act := observedAt // Frantic sibling pane refreshed #{window_activity} 0ms ago!
		if spec.State == vttest.StateWorkingStreaming {
			act = observedAt.Add(-45 * time.Minute)
		} else if spec.State == vttest.StateRateLimitedError {
			act = observedAt.Add(-10 * time.Minute)
		}
		return checkFrame(spec, stream, vt.CapturePlain(), act, baseSeed)
	}); msg != "" {
		t.Fatal(msg)
	}

	// 2. 1,024 randomized seeded scenarios across sibling activity jitter [0ms..45m], geometries, and drafts.
	siblingDeltas := []time.Duration{
		0,
		5 * time.Millisecond,
		50 * time.Millisecond,
		250 * time.Millisecond,
		900 * time.Millisecond,
		2 * time.Second,
		30 * time.Second,
		45 * time.Minute,
	}
	const totalSeeds = 1024
	if msg := vttest.RunParallelSeeds(totalSeeds, func(i int) string {
		seed := vttest.DeriveSeed(baseSeed, i)
		rng := vttest.NewSeededRNG(seed)
		st := states[rng.IntN(len(states))]
		cols := vttest.StandardCols[rng.IntN(len(vttest.StandardCols))]
		rows := vttest.StandardRows[rng.IntN(len(vttest.StandardRows))]
		if i%8 != 0 && (cols > 160 || rows > 40) {
			cols = 100
			rows = 24
		}
		spec := vttest.FrameSpec{
			Harness:    vttest.AllHarnesses[rng.IntN(len(vttest.AllHarnesses))],
			State:      st,
			Cols:       cols,
			Rows:       rows,
			Variant:    rng.IntN(6),
			DraftLines: vttest.RandomDraftLines(rng, 3),
		}
		delta := siblingDeltas[rng.IntN(len(siblingDeltas))]
		if st == vttest.StateRateLimitedError && delta < 10*time.Second {
			delta = 10 * time.Minute
		}
		vt := statusVTPool.Get().(*vttest.Terminal)
		defer statusVTPool.Put(vt)
		stream := vt.LoadSpec(spec)
		return checkFrame(spec, stream, vt.CapturePlain(), observedAt.Add(-delta), seed)
	}); msg != "" {
		t.Fatal(msg)
	}

	// 3. stdlib testing/quick property: frantic sibling activity never flips S2_IDLE_EMPTY_COMPOSER into WORKING.
	vtQuick := vttest.New(100, 24)
	prop := func(hIdx uint8, colIdx uint8, rowIdx uint8, jitterMillis uint16) bool {
		h := vttest.AllHarnesses[int(hIdx)%len(vttest.AllHarnesses)]
		cols := vttest.StandardCols[int(colIdx)%len(vttest.StandardCols)]
		rows := vttest.StandardRows[int(rowIdx)%len(vttest.StandardRows)]
		spec := vttest.FrameSpec{Harness: h, State: vttest.StateIdleEmpty, Cols: cols, Rows: rows}
		vtQuick.LoadSpec(spec)
		act := observedAt.Add(-time.Duration(jitterMillis) * time.Millisecond)
		st := detector.AnalyzeAt("%9", "pane", string(h), vtQuick.CapturePlain(), act, observedAt)
		return st.State == StateIdle
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatalf("quick.Check M3 sibling-pane isolation failed: %v", err)
	}
}

func TestVTRegression_StatusBorderedCodexAndBannerErrorGuard(t *testing.T) {
	detector := NewDetector()
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)

	// 1. Bordered Codex draft composer must classify as idle even under 0ms sibling activity.
	vtCod := vttest.New(80, 24)
	vtCod.LoadSpec(vttest.FrameSpec{Harness: vttest.HarnessCodex, State: vttest.StateIdleDraft, Cols: 80, Rows: 24})
	if st := detector.AnalyzeAt("%1", "cod-1", "cod", vtCod.CapturePlain(), now, now); st.State != StateIdle {
		t.Fatalf("bordered Codex draft misclassified under sibling activity: %+v", st)
	}

	// 2. Version banner ("Claude Code v2.1.12" / "Grok Build 1.0.5") above an unrecovered
	// rate-limit banner must NOT trigger false idle via upward prompt scan.
	for _, h := range []vttest.HarnessID{vttest.HarnessClaude, vttest.HarnessGrok} {
		vtErr := vttest.New(80, 24)
		vtErr.LoadSpec(vttest.FrameSpec{Harness: h, State: vttest.StateRateLimitedError, Cols: 80, Rows: 24})
		plain := vtErr.CapturePlain()
		if DetectIdleFromOutput(plain, string(h)) {
			t.Fatalf("%s version banner above active rate-limit error falsely returned idle", h)
		}
		if st := detector.AnalyzeAt("%2", "err-1", string(h), plain, now.Add(-10*time.Minute), now); st.State != StateError {
			t.Fatalf("%s rate-limit banner did not classify as StateError: %+v", h, st)
		}
	}
}
