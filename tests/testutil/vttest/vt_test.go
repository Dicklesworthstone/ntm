package vttest

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"
)

func TestVT_CarriageReturnSpinnerOverwriteAndControls(t *testing.T) {
	vt := New(40, 5)
	vt.WriteString("⠋ Thinking deeply about auth…\r\x1b[K⠙ Pondering…\r⠹ Done")
	lines := strings.Split(vt.CapturePlain(), "\n")
	if got, want := lines[0], "⠹ Doneering…"; got != want {
		t.Fatalf("CR overwrite without EL: got %q, want %q", got, want)
	}

	vt = New(40, 5)
	vt.WriteString("⠋ Thinking deeply…\r\x1b[K⠹ Ready\n\tcol8\bX")
	lines = strings.Split(vt.CapturePlain(), "\n")
	if lines[0] != "⠹ Ready" {
		t.Fatalf("row 0 = %q, want %q", lines[0], "⠹ Ready")
	}
	if lines[1] != "        colX" {
		t.Fatalf("row 1 (tab + backspace) = %q, want %q", lines[1], "        colX")
	}
}

func TestVT_DelayedAutoWrapAndScroll(t *testing.T) {
	// Exact cols-wide border followed by \r\n must NOT double-advance a blank row.
	vt := New(10, 4)
	vt.WriteString("╭────────╮\r\n│ ❯      │\r\n╰────────╯")
	lines := strings.Split(vt.CapturePlain(), "\n")
	want := []string{
		"╭────────╮",
		"│ ❯      │",
		"╰────────╯",
		"",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("delayed autowrap rows = %#v, want %#v", lines, want)
	}

	// Writing past cols wraps; writing past rows-1 scrolls up cleanly.
	vt = New(8, 3)
	vt.WriteString("12345678AB\nLINE3\nLINE4")
	gotScroll := strings.Split(vt.CapturePlain(), "\n")
	wantScroll := []string{"AB", "LINE3", "LINE4"}
	if !reflect.DeepEqual(gotScroll, wantScroll) {
		t.Fatalf("scroll rows = %#v, want %#v", gotScroll, wantScroll)
	}
}

func TestVT_CSICursorMotionAndEraseOperations(t *testing.T) {
	vt := New(20, 4)
	vt.WriteString("1111111111\n2222222222\n3333333333\n4444444444")
	// Move to row 2, col 5 (1-based) and erase to end of line (EL 0).
	vt.WriteString("\x1b[2;5H\x1b[0K")
	// Move down 1 (row 3), col 3 via CHA, erase to start of line (EL 1).
	vt.WriteString("\x1b[B\x1b[3G\x1b[1K")
	// Move to row 4 and erase whole line (EL 2).
	vt.WriteString("\x1b[4;1f\x1b[2K")

	lines := strings.Split(vt.CapturePlain(), "\n")
	want := []string{
		"1111111111",
		"2222",
		"   3333333",
		"",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("CSI motion + EL = %#v, want %#v", lines, want)
	}

	// Test ED 0 (from cursor to end of screen) and ED 1 (start to cursor).
	vt.WriteString("\x1b[2;3H\x1b[0J")
	lines = strings.Split(vt.CapturePlain(), "\n")
	if lines[0] != "1111111111" || lines[1] != "22" || lines[2] != "" || lines[3] != "" {
		t.Fatalf("ED 0 rows = %#v", lines)
	}
}

func TestVT_WideUTF8CellsAndHalfOverwriteCleanup(t *testing.T) {
	vt := New(12, 3)
	vt.WriteString("漢字test")
	lines := strings.Split(vt.CapturePlain(), "\n")
	if lines[0] != "漢字test" {
		t.Fatalf("wide CJK render = %q, want %q", lines[0], "漢字test")
	}

	// Overwriting the trailing half (col 2, 1-based) of '漢' must clear its leading half.
	vt.WriteString("\x1b[1;2HX")
	lines = strings.Split(vt.CapturePlain(), "\n")
	if lines[0] != " X字test" {
		t.Fatalf("overwrite trailing half of wide rune = %q, want %q", lines[0], " X字test")
	}

	// Overwriting the leading half (col 3, 1-based) of '字' must clear its trailing half.
	vt.WriteString("\x1b[1;3HY")
	lines = strings.Split(vt.CapturePlain(), "\n")
	if lines[0] != " XY test" {
		t.Fatalf("overwrite leading half of wide rune = %q, want %q", lines[0], " XY test")
	}
}

func TestVT_AltScreenSwapAndSynchronizedOutput(t *testing.T) {
	vt := New(40, 6)
	vt.WriteString("user@host:~/proj$ opencode\r\n")
	if vt.InAltScreen() {
		t.Fatal("expected primary screen before ?1049h")
	}

	vt.WriteString("\x1b[?2026h\x1b[?1049h\x1b[2J\x1b[3;3H\x1b[1;38;2;120;200;255m┃  Ask anything…\x1b[0m")
	if !vt.InAltScreen() || !vt.SyncOutput() {
		t.Fatalf("expected alt-screen and sync-output active, got alt=%v sync=%v", vt.InAltScreen(), vt.SyncOutput())
	}
	if !strings.Contains(vt.CapturePlain(), "┃  Ask anything…") {
		t.Fatalf("alt screen missing TUI frame: %q", vt.CapturePlain())
	}
	if strings.Contains(vt.CapturePlain(), "user@host:~/proj$") {
		t.Fatalf("alt screen leaked primary shell prompt: %q", vt.CapturePlain())
	}

	vt.WriteString("\x1b[?1049l\x1b[?2026luser@host:~/proj$ ")
	if vt.InAltScreen() || vt.SyncOutput() {
		t.Fatalf("expected primary screen and sync-output off after ?1049l/?2026l")
	}
	plain := vt.CapturePlain()
	if strings.Contains(plain, "Ask anything") {
		t.Fatalf("rmcup leaked alt-screen TUI frame into primary screen: %q", plain)
	}
	if !strings.Contains(plain, "user@host:~/proj$") {
		t.Fatalf("rmcup failed to restore primary shell prompt: %q", plain)
	}
}

func TestVT_StreamingChunkInvariance_AllHarnessesAndQuick(t *testing.T) {
	baseSeed := ResolveSeed()
	rng := NewSeededRNG(baseSeed)

	for _, h := range AllHarnesses {
		for _, st := range AllLifecycleStates {
			stream := GenerateANSIStream(FrameSpec{
				Harness: h,
				State:   st,
				Cols:    120,
				Rows:    40,
				Variant: rng.IntN(4),
			})

			full := New(120, 40)
			_, _ = full.Write(stream)
			want := full.CapturePlain()

			// 1-byte worst-case split (slices every multi-byte UTF-8 rune & CSI token).
			byteByByte := New(120, 40)
			for i := range stream {
				_, _ = byteByByte.Write(stream[i : i+1])
			}
			if got := byteByByte.CapturePlain(); got != want {
				t.Fatalf("1-byte chunk mismatch for %s/%s:\ngot:\n%s\nwant:\n%s", h, st, got, want)
			}

			// Random 1..17 byte splits.
			chunked := New(120, 40)
			chunks, _ := RandomByteChunks(rng, stream, 17)
			for _, ch := range chunks {
				_, _ = chunked.Write(ch)
			}
			if got := chunked.CapturePlain(); got != want {
				t.Fatalf("random 1..17 chunk mismatch for %s/%s", h, st)
			}
		}
	}

	// stdlib testing/quick property check over arbitrary chunking schedules.
	prop := func(harnessIdx uint8, stateIdx uint8, splitSeed int64) bool {
		h := AllHarnesses[int(harnessIdx)%len(AllHarnesses)]
		st := AllLifecycleStates[int(stateIdx)%len(AllLifecycleStates)]
		stream := GenerateANSIStream(FrameSpec{Harness: h, State: st, Cols: 100, Rows: 24})

		full := New(100, 24)
		_, _ = full.Write(stream)

		chunked := New(100, 24)
		r := rand.New(rand.NewPCG(uint64(splitSeed), 0xfeedface))
		chunks, _ := RandomByteChunks(r, stream, 17)
		for _, ch := range chunks {
			_, _ = chunked.Write(ch)
		}
		return full.CapturePlain() == chunked.CapturePlain()
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatalf("quick.Check chunking property failed: %v", err)
	}
}

func TestVT_SimulateKeyAndHindsightBundleJSON(t *testing.T) {
	vt := New(100, 24)
	vt.LoadSpec(FrameSpec{Harness: HarnessCodex, State: StateIdleEmpty, Cols: 100, Rows: 24})
	vt.SimulateKey(HarnessCodex, "Escape")
	if vt.Interactive().State != StateIdleEmpty {
		t.Fatalf("single Escape in Codex should stay idle, got %s", vt.Interactive().State)
	}
	vt.SimulateKey(HarnessCodex, "Escape")
	if vt.Interactive().State != StateModalTrap {
		t.Fatalf("double Escape in Codex must enter StateModalTrap, got %s", vt.Interactive().State)
	}

	// omp double Ctrl+C within 500ms on empty composer exits, spaced >500ms does not.
	vtOmp := New(100, 24)
	vtOmp.LoadSpec(FrameSpec{Harness: HarnessOmp, State: StateIdleEmpty, Cols: 100, Rows: 24})
	vtOmp.SimulateKey(HarnessOmp, "C-c")
	vtOmp.AdvanceSimClock(700 * time.Millisecond)
	vtOmp.SimulateKey(HarnessOmp, "C-c")
	if vtOmp.Interactive().ExitedViaDoubleCC {
		t.Fatal("spaced Ctrl+C (>500ms) must not exit omp")
	}
	vtOmp.AdvanceSimClock(100 * time.Millisecond)
	vtOmp.SimulateKey(HarnessOmp, "C-c")
	if !vtOmp.Interactive().ExitedViaDoubleCC || vtOmp.Interactive().State != StateExitedToShell {
		t.Fatalf("unspaced double Ctrl+C (<=500ms) must exit omp, got %+v", vtOmp.Interactive())
	}

	bundle := HindsightBundle{
		Invariant:        "M2_PTY_CHUNK_INVARIANCE",
		Harness:          HarnessCodex,
		LifecycleState:   StateIdleEmpty,
		Seed:             0x1234,
		Cols:             100,
		Rows:             24,
		ChunkSplits:      []int{3, 7, 1},
		ANSIHexSlice:     HexPreview([]byte("\x1b[2Jhello"), 16),
		RenderedViewport: "› ",
		ExpectedState:    "idle",
		ActualState:      "working",
		ReproCmd:         ReproCommand("./internal/tmux", "TestExample", 0x1234),
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(bundle.FormatJSON()), &decoded); err != nil {
		t.Fatalf("HindsightBundle.FormatJSON produced invalid JSON: %v", err)
	}
	if decoded["harness"] != "cod" || decoded["repro_cmd"] == "" {
		t.Fatalf("unexpected decoded bundle fields: %#v", decoded)
	}
}
