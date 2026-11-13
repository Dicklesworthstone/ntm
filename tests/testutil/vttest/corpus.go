package vttest

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// StandardGeometries enumerates the 6 widths × 4 heights = 24 canonical
// terminal geometries required by Metamorphic Invariant M1.
var (
	StandardCols = []int{80, 100, 120, 160, 240, 320}
	StandardRows = []int{24, 40, 80, 100}
)

// FrameSpec parameterizes a deterministic raw ANSI PTY byte-stream capture
// for one of the 7 interactive agent harnesses at a specific geometry.
type FrameSpec struct {
	Harness         HarnessID
	State           LifecycleState
	Cols            int
	Rows            int
	Variant         int
	DraftLines      []string
	TranscriptLines []string
}

// HindsightBundle is the structured failure diagnostic emitted on any
// metamorphic invariant violation (Graveyard §3.10).
type HindsightBundle struct {
	Invariant        string         `json:"invariant"`
	Harness          HarnessID      `json:"harness"`
	LifecycleState   LifecycleState `json:"lifecycle_state"`
	Seed             uint64         `json:"seed"`
	Cols             int            `json:"cols"`
	Rows             int            `json:"rows"`
	Variant          int            `json:"variant,omitempty"`
	ChunkSplits      []int          `json:"chunk_splits,omitempty"`
	ANSIHexSlice     string         `json:"ansi_hex_slice"`
	RenderedViewport string         `json:"rendered_viewport"`
	ExpectedState    string         `json:"expected_state"`
	ActualState      string         `json:"actual_state"`
	ReproCmd         string         `json:"repro_cmd"`
}

// FormatJSON serializes the hindsight bundle as indented JSON for t.Fatalf.
func (b HindsightBundle) FormatJSON() string {
	const maxHexBytes = 256
	if len(b.ANSIHexSlice) > maxHexBytes*2 {
		b.ANSIHexSlice = b.ANSIHexSlice[:maxHexBytes*2] + "...(truncated)"
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", b)
	}
	return string(raw)
}

// HexPreview encodes up to maxBytes of raw ANSI stream as hex for failure bundles.
func HexPreview(stream []byte, maxBytes int) string {
	if maxBytes <= 0 || len(stream) <= maxBytes {
		return hex.EncodeToString(stream)
	}
	return hex.EncodeToString(stream[:maxBytes]) + "...(truncated)"
}

// LoadSpec resets the virtual terminal to (spec.Cols, spec.Rows), initializes
// its interactive keyboard model, writes the generated raw ANSI byte stream,
// and returns the raw bytes written.
func (t *Terminal) LoadSpec(spec FrameSpec) []byte {
	t.Reset(spec.Cols, spec.Rows)
	t.sim = InteractiveModel{
		Harness:        spec.Harness,
		State:          spec.State,
		SimulatedClock: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
	}
	if spec.State == StateIdleDraft {
		lines := spec.DraftLines
		if len(lines) == 0 {
			lines = []string{"audit retry backoff and add deterministic tests"}
		}
		t.sim.DraftLines = append([]string(nil), lines...)
		t.sim.CursorLine = len(t.sim.DraftLines) - 1
		t.sim.CursorCol = utf8.RuneCountInString(t.sim.DraftLines[t.sim.CursorLine])
	}
	stream := GenerateANSIStream(spec)
	_, _ = t.Write(stream)
	return stream
}

// AdvanceSimClock advances the simulated wall clock for key-timing windows
// (such as omp's 500ms double-Ctrl+C empty-draft quit window).
func (t *Terminal) AdvanceSimClock(d time.Duration) {
	t.sim.SimulatedClock = t.sim.SimulatedClock.Add(d)
}

// SimulateKey models the effect of sending a tmux key name ("Escape", "C-u",
// "C-e", "C-c", "C-d", "BSpace", "Enter", "Up") to the harness in its current
// lifecycle state and writes the resulting ANSI redraw to the terminal buffer.
func (t *Terminal) SimulateKey(harness HarnessID, key string) {
	if harness == "" {
		harness = t.sim.Harness
	}
	t.sim.Harness = harness

	switch t.sim.State {
	case StateExitedToShell:
		if key == "C-d" || key == "exit" {
			t.sim.ModalReason = "destructive_shell_eof"
		}
		return

	case StateModalTrap:
		if key == "Escape" {
			// Single Escape dismisses Codex transcript browsing or autocomplete.
			t.sim.State = StateIdleEmpty
			t.sim.ModalReason = ""
			t.sim.EscapeStreak = 0
			t.rerenderFromSim()
		}
		return

	case StateWorkingStreaming:
		switch harness {
		case HarnessOpencode:
			if key == "Escape" {
				t.sim.State = StateIdleEmpty
				t.sim.DraftLines = nil
				t.rerenderFromSim()
			} else if key == "C-c" {
				// Ctrl+C in OpenCode exits / triggers quit rather than cancelling generation.
				t.sim.State = StateExitedToShell
				t.rerenderFromSim()
			}
		case HarnessOmp:
			if key == "Escape" {
				t.sim.State = StateIdleEmpty
				t.sim.DraftLines = nil
				t.rerenderFromSim()
			} else if key == "C-c" {
				t.handleOmpCtrlC()
			}
		default:
			if key == "C-c" {
				t.sim.State = StateIdleEmpty
				t.sim.DraftLines = nil
				t.rerenderFromSim()
			}
		}
		return

	case StateIdleEmpty:
		switch key {
		case "Escape":
			t.sim.EscapeStreak++
			// Codex opens transcript browsing on Escape pairs (GH #345);
			// Claude Code opens history jump on double-Escape.
			if (harness == HarnessCodex || harness == HarnessClaude) && t.sim.EscapeStreak >= 2 {
				t.sim.State = StateModalTrap
				t.sim.ModalReason = "double_escape_history_trap"
				t.rerenderFromSim()
			}
		case "C-c":
			t.sim.EscapeStreak = 0
			if harness == HarnessOmp {
				t.handleOmpCtrlC()
				return
			}
			if harness == HarnessOpencode {
				t.sim.State = StateExitedToShell
				t.rerenderFromSim()
				return
			}
		case "C-d":
			t.sim.EscapeStreak = 0
			t.sim.State = StateExitedToShell
			t.rerenderFromSim()
		default:
			t.sim.EscapeStreak = 0
		}
		return

	case StateIdleDraft:
		if key != "Escape" {
			t.sim.EscapeStreak = 0
		}
		switch key {
		case "Escape":
			t.sim.EscapeStreak++
			if (harness == HarnessCodex || harness == HarnessClaude) && t.sim.EscapeStreak >= 2 {
				t.sim.State = StateModalTrap
				t.sim.ModalReason = "double_escape_history_trap"
				t.rerenderFromSim()
			}
		case "C-c":
			if harness == HarnessOmp {
				// One Ctrl+C in omp drains the entire draft of any shape without quitting.
				t.sim.DraftLines = nil
				t.sim.CursorLine = 0
				t.sim.CursorCol = 0
				t.sim.State = StateIdleEmpty
				t.rerenderFromSim()
				return
			}
			if harness == HarnessOpencode {
				t.sim.State = StateExitedToShell
				t.rerenderFromSim()
				return
			}
			t.sim.DraftLines = nil
			t.sim.CursorLine = 0
			t.sim.CursorCol = 0
			t.sim.State = StateIdleEmpty
			t.rerenderFromSim()
		case "C-e":
			if len(t.sim.DraftLines) > 0 {
				t.sim.CursorLine = len(t.sim.DraftLines) - 1
				t.sim.CursorCol = utf8.RuneCountInString(t.sim.DraftLines[t.sim.CursorLine])
			}
		case "C-u":
			t.killLineBackward()
		case "BSpace":
			t.backspaceOne()
		}
	}
}

func (t *Terminal) handleOmpCtrlC() {
	if len(t.sim.DraftLines) > 0 {
		t.sim.DraftLines = nil
		t.sim.State = StateIdleEmpty
		t.rerenderFromSim()
		return
	}
	if t.sim.EmptyCtrlCCount > 0 && t.sim.SimulatedClock.Sub(t.sim.LastEmptyCtrlCAt) <= 500*time.Millisecond {
		t.sim.EmptyCtrlCCount++
		t.sim.ExitedViaDoubleCC = true
		t.sim.State = StateExitedToShell
		t.rerenderFromSim()
		return
	}
	t.sim.EmptyCtrlCCount = 1
	t.sim.LastEmptyCtrlCAt = t.sim.SimulatedClock
}

func (t *Terminal) killLineBackward() {
	if len(t.sim.DraftLines) == 0 {
		t.sim.State = StateIdleEmpty
		t.rerenderFromSim()
		return
	}
	// Single-buffer TUIs without marker escalation clear the line buffer on C-u;
	// multi-row drafts drop the active tail line per C-e/C-u round.
	switch t.sim.Harness {
	case HarnessAntigravity, HarnessOpencode, HarnessPi:
		t.sim.DraftLines = nil
	default:
		if len(t.sim.DraftLines) <= 1 {
			t.sim.DraftLines = nil
		} else {
			t.sim.DraftLines = t.sim.DraftLines[:len(t.sim.DraftLines)-1]
		}
	}
	if len(t.sim.DraftLines) == 0 {
		t.sim.CursorLine = 0
		t.sim.CursorCol = 0
		t.sim.State = StateIdleEmpty
	} else {
		t.sim.CursorLine = len(t.sim.DraftLines) - 1
		t.sim.CursorCol = utf8.RuneCountInString(t.sim.DraftLines[t.sim.CursorLine])
	}
	t.rerenderFromSim()
}

func (t *Terminal) backspaceOne() {
	if len(t.sim.DraftLines) == 0 {
		t.sim.State = StateIdleEmpty
		return
	}
	last := len(t.sim.DraftLines) - 1
	runes := []rune(t.sim.DraftLines[last])
	if len(runes) > 1 {
		t.sim.DraftLines[last] = string(runes[:len(runes)-1])
	} else {
		t.sim.DraftLines = t.sim.DraftLines[:last]
	}
	if len(t.sim.DraftLines) == 0 {
		t.sim.State = StateIdleEmpty
	}
	t.rerenderFromSim()
}

func (t *Terminal) rerenderFromSim() {
	stream := GenerateANSIStream(FrameSpec{
		Harness:    t.sim.Harness,
		State:      t.sim.State,
		Cols:       t.cols,
		Rows:       t.rows,
		DraftLines: t.sim.DraftLines,
	})
	t.primary = newScreenBuf(t.cols, t.rows)
	t.alt = newScreenBuf(t.cols, t.rows)
	t.useAlt = false
	t.parser.Reset()
	_, _ = t.Write(stream)
}

// GenerateANSIStream synthesizes a realistic width-parameterized raw ANSI PTY
// byte stream (including DEC synchronized output ?2026h/l, SGR 24-bit colors,
// CSI CUP/CHA/EL/ED positioning, \r spinner overwrites, and ?1049h/l alt-screen
// transitions) for any (Harness, LifecycleState, Cols, Rows) tuple.
func GenerateANSIStream(spec FrameSpec) []byte {
	cols := max(40, spec.Cols)
	rows := max(16, spec.Rows)
	spec.Cols = cols
	spec.Rows = rows

	var b strings.Builder
	b.WriteString("\x1b[?2026h") // Begin DEC synchronized update

	switch spec.Harness {
	case HarnessClaude:
		emitClaudeStream(&b, spec)
	case HarnessCodex:
		emitCodexStream(&b, spec)
	case HarnessAntigravity:
		emitAntigravityStream(&b, spec)
	case HarnessOmp:
		emitOmpStream(&b, spec)
	case HarnessGrok:
		emitGrokStream(&b, spec)
	case HarnessOpencode:
		emitOpencodeStream(&b, spec)
	case HarnessPi:
		emitPiStream(&b, spec)
	}

	b.WriteString("\x1b[?2026l") // End DEC synchronized update
	return []byte(b.String())
}

func cup(row1, col1 int) string {
	return fmt.Sprintf("\x1b[%d;%dH", max(1, row1), max(1, col1))
}

func fitRunes(s string, maxCols int) string {
	if maxCols <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxCols {
		return s
	}
	return string(runes[:maxCols])
}

func padRightRunes(s string, width int) string {
	runes := []rune(s)
	if len(runes) >= width {
		return string(runes[:width])
	}
	return s + strings.Repeat(" ", width-len(runes))
}

func wrapDraftToWidth(lines []string, innerWidth int) []string {
	if innerWidth < 8 {
		innerWidth = 8
	}
	if len(lines) == 0 {
		return []string{"audit retry backoff and add deterministic tests"}
	}
	var out []string
	for _, line := range lines {
		runes := []rune(line)
		if len(runes) == 0 {
			out = append(out, "")
			continue
		}
		for len(runes) > innerWidth {
			split := innerWidth
			for k := innerWidth; k > innerWidth/2; k-- {
				if runes[k-1] == ' ' {
					split = k
					break
				}
			}
			out = append(out, strings.TrimRight(string(runes[:split]), " "))
			runes = []rune(strings.TrimLeft(string(runes[split:]), " "))
		}
		if len(runes) > 0 {
			out = append(out, string(runes))
		}
	}
	if len(out) == 0 {
		return []string{"draft"}
	}
	return out
}

func writeTranscriptPrefix(b *strings.Builder, lines []string, cols, maxRows int) int {
	if len(lines) == 0 {
		lines = []string{
			"Verified state transitions across edge cases.",
			"All unit checks green.",
		}
	}
	if len(lines) > maxRows {
		lines = lines[len(lines)-maxRows:]
	}
	row := 1
	for _, l := range lines {
		b.WriteString(cup(row, 1))
		b.WriteString("\x1b[38;2;210;215;225m")
		b.WriteString(fitRunes(l, cols-1))
		b.WriteString("\x1b[0m\x1b[K")
		row++
	}
	return row
}

// ---------------- 1. Claude Code (cc) ----------------

func emitClaudeStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	boxW := min(cols, max(40, cols-2))
	innerW := boxW - 4 // "│ " + content + " │"

	drawComposerBox := func(startRow int, draft []string, placeholder string) int {
		b.WriteString(cup(startRow, 1))
		b.WriteString("\x1b[38;5;244m╭" + strings.Repeat("─", boxW-2) + "╮\x1b[0m")
		r := startRow + 1
		if len(draft) == 0 {
			content := "❯ "
			if placeholder != "" {
				content += placeholder
			}
			b.WriteString(cup(r, 1))
			b.WriteString("\x1b[38;5;244m│\x1b[0m " + padRightRunes(content, innerW) + " \x1b[38;5;244m│\x1b[0m")
			r++
		} else {
			wrapped := wrapDraftToWidth(draft, innerW-2)
			for i, line := range wrapped {
				prefix := "  "
				if i == 0 {
					prefix = "❯ "
				}
				b.WriteString(cup(r, 1))
				b.WriteString("\x1b[38;5;244m│\x1b[0m " + padRightRunes(prefix+line, innerW) + " \x1b[38;5;244m│\x1b[0m")
				r++
			}
		}
		b.WriteString(cup(r, 1))
		b.WriteString("\x1b[38;5;244m╰" + strings.Repeat("─", boxW-2) + "╯\x1b[0m")
		r++
		b.WriteString(cup(r, 1))
		b.WriteString("\x1b[2m  ⏵⏵ bypass permissions on (shift+tab to cycle)\x1b[0m\x1b[K")
		return r
	}

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[2J" + cup(1, 1))
		b.WriteString("\x1b[38;5;208m✻\x1b[0m Initializing session environment…\r\n")
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		r := writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-10)
		b.WriteString(cup(r+1, 1) + "\x1b[38;5;208m✻\x1b[0m Cooked for 14s")
		placeholder := ""
		if spec.Variant%2 == 1 {
			placeholder = `Try "refactor parser.go"`
		}
		drawComposerBox(rows-4, nil, placeholder)
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-14)
		draft := spec.DraftLines
		if len(draft) == 0 {
			draft = []string{"audit retry backoff and add deterministic tests"}
		}
		wrapped := wrapDraftToWidth(draft, innerW-2)
		topRow := max(3, rows-3-len(wrapped))
		drawComposerBox(topRow, draft, "")
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		if spec.Variant%2 == 0 {
			b.WriteString(cup(2, 1) + "╭" + strings.Repeat("─", boxW-2) + "╮")
			b.WriteString(cup(3, 1) + "│ " + padRightRunes("Do you trust the files in this folder?", innerW) + " │")
			b.WriteString(cup(4, 1) + "│ " + padRightRunes("/workspace/project", innerW) + " │")
			b.WriteString(cup(5, 1) + "│ " + padRightRunes("❯ 1. Yes, proceed", innerW) + " │")
			b.WriteString(cup(6, 1) + "│ " + padRightRunes("  2. No, exit", innerW) + " │")
			b.WriteString(cup(7, 1) + "│ " + padRightRunes("Enter to confirm · Esc to cancel", innerW) + " │")
			b.WriteString(cup(8, 1) + "╰" + strings.Repeat("─", boxW-2) + "╯")
		} else {
			b.WriteString(cup(rows-5, 1) + "Select model:")
			b.WriteString(cup(rows-4, 1) + "❯ 1. Claude Opus 4.6 (recommended)")
			b.WriteString(cup(rows-3, 1) + "  2. Claude Sonnet 4.6")
			b.WriteString(cup(rows-2, 1) + "  Press enter to continue")
		}
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-10)
		spinRow := rows - 6
		b.WriteString(cup(spinRow, 1))
		b.WriteString("⠋ Thinking…\r\x1b[K⠙ Pondering…\r\x1b[K")
		b.WriteString("\x1b[38;5;208m✻\x1b[0m " + fitRunes("Whirlpooling… (ctrl+c to interrupt · 12s · ↓ 1.4k tokens)", cols-4))
		drawComposerBox(rows-4, nil, "")
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(2, 1) + "Claude Code v2.1.12")
		b.WriteString(cup(4, 1) + "\x1b[1;31mYou've hit your limit · resets 5pm (UTC)\x1b[0m")
	case StateExitedToShell:
		if spec.Variant%2 == 0 {
			b.WriteString(cup(1, 1) + "user@host:~/proj$ claude\r\n")
			b.WriteString("\x1b[?1049h\x1b[2J")
			drawComposerBox(rows-4, nil, "")
			b.WriteString("\x1b[?1049l")
			b.WriteString("Session ended.\r\nuser@host:~/proj$ ")
		} else {
			b.WriteString("\x1b[2J")
			endRow := drawComposerBox(max(2, rows-6), nil, "")
			b.WriteString(cup(min(rows, endRow+1), 1) + "user@host:~/proj$ ")
		}
	}
}

// ---------------- 2. Codex CLI (cod) ----------------

func emitCodexStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	glyph := "›"
	effort := "xhigh"
	if spec.Variant%2 == 1 {
		glyph = "»"
		effort = "ultra"
	}
	footer := fitRunes(fmt.Sprintf("  gpt-5.4 %s · 78%% context left · ? for shortcuts", effort), cols-1)

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[2J" + cup(1, 1) + "Launching session sandbox…")
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		prompt := glyph + " "
		if spec.Variant >= 2 {
			prompt = glyph + " Ask Codex to do anything"
		}
		b.WriteString(cup(rows-2, 1) + "\x1b[1m" + fitRunes(prompt, cols-1) + "\x1b[0m\x1b[K")
		b.WriteString(cup(rows-1, 1) + "\x1b[2m" + footer + "\x1b[0m\x1b[K")
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-12)
		boxW := min(cols, max(40, cols-2))
		innerW := boxW - 4
		wrapped := wrapDraftToWidth(spec.DraftLines, innerW-2)
		startRow := max(2, rows-3-len(wrapped))
		b.WriteString(cup(startRow, 1) + "╭" + strings.Repeat("─", boxW-2) + "╮")
		for i, l := range wrapped {
			p := "  "
			if i == 0 {
				p = glyph + " "
			}
			b.WriteString(cup(startRow+1+i, 1) + "│ " + padRightRunes(p+l, innerW) + " │")
		}
		b.WriteString(cup(startRow+1+len(wrapped), 1) + "╰" + strings.Repeat("─", boxW-2) + "╯")
		b.WriteString(cup(rows-1, 1) + "\x1b[2m" + footer + "\x1b[0m")
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		switch spec.Variant % 3 {
		case 0:
			b.WriteString(cup(rows-4, 1) + glyph + " previous user prompt in transcript")
			b.WriteString(cup(rows-1, 1) + fitRunes("Browsing transcript · ↑↓/jk scroll · ↵ rewind · esc back", cols-1))
		case 1:
			b.WriteString(cup(rows-6, 1) + "Codex update available!")
			b.WriteString(cup(rows-4, 1) + glyph + " 1. Update now (runs `bun install -g @openai/codex`)")
			b.WriteString(cup(rows-3, 1) + "  2. Skip")
			b.WriteString(cup(rows-1, 1) + "  Press enter to continue")
		default:
			b.WriteString(cup(rows-5, 1) + "Do you trust the contents of this project?")
			b.WriteString(cup(rows-3, 1) + glyph + " 1. Yes, continue")
			b.WriteString(cup(rows-2, 1) + "  2. No, quit")
			b.WriteString(cup(rows-1, 1) + "Enter to confirm · Esc to cancel")
		}
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-4, 1))
		b.WriteString("⠋ Working (1s • esc to interrupt)\r\x1b[K• Working (12s • esc to interrupt)")
		b.WriteString(cup(rows-2, 1) + glyph + " ")
		b.WriteString(cup(rows-1, 1) + "\x1b[2m" + footer + "\x1b[0m")
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(2, 1) + "OpenAI Codex v0.151.0")
		b.WriteString(cup(4, 1) + "\x1b[31mError: 429 Too Many Requests — rate limit exceeded, please try again later.\x1b[0m")
	case StateExitedToShell:
		if spec.Variant%2 == 0 {
			b.WriteString(cup(1, 1) + "user@host:~/proj$ codex\r\n\x1b[?1049h\x1b[2J")
			b.WriteString(cup(rows-2, 1) + glyph + " \r\n" + footer)
			b.WriteString("\x1b[?1049luser@host:~/proj$ ")
		} else {
			b.WriteString("\x1b[2J")
			b.WriteString(cup(rows-4, 1) + glyph + " ")
			b.WriteString(cup(rows-3, 1) + footer)
			b.WriteString(cup(rows-1, 1) + "user@host:~/proj$ ")
		}
	}
}

// ---------------- 3. Antigravity CLI (agy) ----------------

func emitAntigravityStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	rule := strings.Repeat("─", max(20, cols-2))

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[2J" + cup(1, 1) + "Antigravity CLI — warming workspace index…")
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-5, 1) + "\x1b[2m" + rule + "\x1b[0m")
		b.WriteString(cup(rows-4, 1) + "\x1b[1;36m>\x1b[0m")
		b.WriteString(cup(rows-3, 1) + "\x1b[2m" + rule + "\x1b[0m")
		b.WriteString(cup(rows-2, 1) + "? for shortcuts")
		b.WriteString(cup(rows-1, 1) + fitRunes("Gemini 4 Argon (High) /model | 342.5 MB", cols-1))
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-12)
		wrapped := wrapDraftToWidth(spec.DraftLines, cols-6)
		if len(wrapped) > 5 {
			wrapped = wrapped[:5]
		}
		topRow := max(2, rows-4-len(wrapped))
		b.WriteString(cup(topRow, 1) + "\x1b[2m" + rule + "\x1b[0m")
		for i, l := range wrapped {
			p := "  "
			if i == 0 {
				p = "> "
			}
			b.WriteString(cup(topRow+1+i, 1) + fitRunes(p+l, cols-2))
		}
		b.WriteString(cup(topRow+1+len(wrapped), 1) + "\x1b[2m" + rule + "\x1b[0m")
		b.WriteString(cup(rows-2, 1) + "? for shortcuts")
		b.WriteString(cup(rows-1, 1) + fitRunes("Gemini 4 Argon (High) /model | 342.5 MB", cols-1))
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(rows-6, 1) + "Antigravity Workspace Security")
		b.WriteString(cup(rows-5, 1) + "Do you trust the contents of this project?")
		b.WriteString(cup(rows-3, 1) + "> 1. Yes, I trust this folder")
		b.WriteString(cup(rows-2, 1) + "  2. No, exit")
		b.WriteString(cup(rows-1, 1) + "Enter to confirm · Esc to cancel")
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-6, 1) + "⠋ Reading files…\r\x1b[K⠹ Updating harness (esc to cancel)")
		b.WriteString(cup(rows-5, 1) + rule)
		b.WriteString(cup(rows-4, 1) + ">")
		b.WriteString(cup(rows-3, 1) + rule)
		b.WriteString(cup(rows-1, 1) + "esc to cancel")
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(2, 1) + "Antigravity CLI")
		b.WriteString(cup(4, 1) + "\x1b[31mError: 429 quota exceeded — rate limit reached, please wait.\x1b[0m")
	case StateExitedToShell:
		if spec.Variant%2 == 0 {
			b.WriteString(cup(1, 1) + "user@host:~/proj$ agy\r\n\x1b[?1049h\x1b[2J")
			b.WriteString(cup(rows-3, 1) + ">\r\n? for shortcuts")
			b.WriteString("\x1b[?1049luser@host:~/proj$ ")
		} else {
			b.WriteString("\x1b[2J")
			b.WriteString(cup(rows-4, 1) + ">")
			b.WriteString(cup(rows-3, 1) + "? for shortcuts")
			b.WriteString(cup(rows-1, 1) + "user@host:~/proj$ ")
		}
	}
}

// ---------------- 4. Oh My Pi (omp) ----------------

func ompTopBorder(cols int, busy bool) string {
	prefix := "╭── 󰵗   Union Alpha   proj "
	if busy {
		prefix = "╭── ⠧ 3s   Union Alpha   proj "
	}
	suffix := "─7%──󰁨──262K───╮"
	fill := cols - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	if fill < 2 {
		return fitRunes(prefix+"─7%─262K─╮", cols)
	}
	return prefix + strings.Repeat("─", fill) + suffix
}

func ompBottomBorder(cols int, text string) string {
	innerW := cols - 4 // "╰─" + inner + "─╯"
	if innerW < 4 {
		innerW = 4
	}
	return "╰─" + padRightRunes(" "+text, innerW) + "─╯"
}

func emitOmpStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows

	switch spec.State {
	case StateBooting:
		// omp draws a blank screen for ~1s while initializing before painting banner+composer.
		b.WriteString("\x1b[2J" + cup(1, 1))
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		bannerFill := max(4, cols-18)
		b.WriteString(cup(1, 1) + "╭─── omp v18.2.3 " + strings.Repeat("─", bannerFill) + "╮")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-6)
		b.WriteString(cup(rows-2, 1) + "\x1b[38;5;110m" + ompTopBorder(cols, false) + "\x1b[0m")
		hint := ""
		if spec.Variant%2 == 1 && cols >= 96 {
			hint = strings.Repeat(" ", max(2, cols-44)) + "Shift+Tab to change thinking effort"
		}
		b.WriteString(cup(rows-1, 1) + ompBottomBorder(cols, strings.TrimPrefix(hint, " ")))
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-10)
		wrapped := wrapDraftToWidth(spec.DraftLines, cols-6)
		topRow := max(2, rows-1-len(wrapped))
		b.WriteString(cup(topRow, 1) + ompTopBorder(cols, false))
		for i := 0; i < len(wrapped)-1; i++ {
			b.WriteString(cup(topRow+1+i, 1) + "│" + padRightRunes(" "+wrapped[i], cols-2) + "│")
		}
		b.WriteString(cup(topRow+len(wrapped), 1) + ompBottomBorder(cols, wrapped[len(wrapped)-1]))
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		if spec.Variant%2 == 0 {
			// Slash-command autocomplete open below the composer box.
			b.WriteString(cup(rows-5, 1) + ompTopBorder(cols, false))
			b.WriteString(cup(rows-4, 1) + ompBottomBorder(cols, "/mod"))
			b.WriteString(cup(rows-3, 1) + "  /model   Switch active LLM")
			b.WriteString(cup(rows-2, 1) + "  /mode    Configure approval policy")
		} else {
			// Full-screen model selector replaces composer.
			b.WriteString(cup(2, 1) + "╭─ Models " + strings.Repeat("─", max(8, cols-11)) + "╮")
			b.WriteString(cup(3, 1) + "│ ❯ Union Alpha (openrouter) " + strings.Repeat(" ", max(1, cols-30)) + "│")
			b.WriteString(cup(4, 1) + "╰" + strings.Repeat("─", max(8, cols-2)) + "╯")
		}
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-6)
		b.WriteString(cup(rows-3, 1) + "󱊷 Connecting…\r\x1b[K󱊷 Working…")
		b.WriteString(cup(rows-2, 1) + ompTopBorder(cols, true))
		b.WriteString(cup(rows-1, 1) + ompBottomBorder(cols, ""))
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		rule := strings.Repeat("─", max(12, cols-2))
		b.WriteString(cup(rows-7, 1) + rule)
		b.WriteString(cup(rows-6, 1) + " ⚠ 429 server_error: ERROR (rate limit)")
		b.WriteString(cup(rows-5, 1) + " Dismissed when you send your next message.")
		b.WriteString(cup(rows-4, 1) + rule)
		b.WriteString(cup(rows-3, 1) + " ↻ F5 to Retry")
		b.WriteString(cup(rows-2, 1) + ompTopBorder(cols, false))
		b.WriteString(cup(rows-1, 1) + ompBottomBorder(cols, ""))
	case StateExitedToShell:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(rows-4, 1) + ompTopBorder(cols, false))
		b.WriteString(cup(rows-3, 1) + ompBottomBorder(cols, ""))
		b.WriteString(cup(rows-1, 1) + "user@host:~/proj$ ")
	}
}

// ---------------- 5. Grok Build (grok) ----------------

func grokBottomBorder(cols int) string {
	label := "─ Grok 4.6 (high) · always-approve "
	fill := max(2, cols-2-utf8.RuneCountInString(label))
	return "╰" + label + strings.Repeat("─", fill) + "╯"
}

func emitGrokStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	innerW := cols - 4

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[2J" + cup(1, 1) + "Connecting to xAI build service…")
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-5, 1) + "Worked for 6s")
		b.WriteString(cup(rows-4, 1) + "╭" + strings.Repeat("─", cols-2) + "╮")
		b.WriteString(cup(rows-3, 1) + "│ " + padRightRunes("❯ ", innerW) + " │")
		b.WriteString(cup(rows-2, 1) + grokBottomBorder(cols))
		b.WriteString(cup(rows-1, 1) + fitRunes("Shift+Tab:mode  │  Ctrl+x:shortcuts", cols-1))
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-12)
		wrapped := wrapDraftToWidth(spec.DraftLines, innerW-2)
		topRow := max(2, rows-3-len(wrapped))
		b.WriteString(cup(topRow, 1) + "╭" + strings.Repeat("─", cols-2) + "╮")
		for i, l := range wrapped {
			p := "  "
			if i == 0 {
				p = "❯ "
			}
			b.WriteString(cup(topRow+1+i, 1) + "│ " + padRightRunes(p+l, innerW) + " │")
		}
		b.WriteString(cup(topRow+1+len(wrapped), 1) + grokBottomBorder(cols))
		b.WriteString(cup(rows-1, 1) + fitRunes("Shift+Tab:mode  │  Ctrl+x:shortcuts", cols-1))
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(rows-6, 1) + fitRunes("Grok Build may run or modify contents in /work/proj.", cols-1))
		b.WriteString(cup(rows-5, 1) + "Make sure you understand the security risks before continuing.")
		b.WriteString(cup(rows-3, 1) + "❯ 1. Yes, trust this workspace")
		b.WriteString(cup(rows-2, 1) + "  2. No, exit")
		b.WriteString(cup(rows-1, 1) + "Enter to confirm · Esc to cancel")
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-5, 1) + "⠋ Waiting for response… 0.3s\r\x1b[K⠹ Thinking… 1.9s")
		b.WriteString(cup(rows-4, 1) + "╭" + strings.Repeat("─", cols-2) + "╮")
		b.WriteString(cup(rows-3, 1) + "│ " + padRightRunes("❯ ", innerW) + " │")
		b.WriteString(cup(rows-2, 1) + grokBottomBorder(cols))
		b.WriteString(cup(rows-1, 1) + fitRunes("Shift+Tab:mode  │  Esc:cancel  │  Ctrl+x:shortcuts", cols-1))
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(2, 1) + "Grok Build 1.0.5")
		b.WriteString(cup(4, 1) + "\x1b[31merror: 429 rate limit exceeded — too many requests\x1b[0m")
	case StateExitedToShell:
		if spec.Variant%2 == 0 {
			b.WriteString(cup(1, 1) + "user@host:~/proj$ grok\r\n\x1b[?1049h\x1b[2J")
			b.WriteString(cup(rows-3, 1) + "│ " + padRightRunes("❯ ", innerW) + " │\r\n" + grokBottomBorder(cols))
			b.WriteString("\x1b[?1049luser@host:~/proj$ ")
		} else {
			b.WriteString("\x1b[2J")
			b.WriteString(cup(rows-4, 1) + "│ " + padRightRunes("❯ ", innerW) + " │")
			b.WriteString(cup(rows-3, 1) + grokBottomBorder(cols))
			b.WriteString(cup(rows-1, 1) + "user@host:~/proj$ ")
		}
	}
}

// ---------------- 6. OpenCode (oc) ----------------

func emitOpencodeStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	hasSidebar := cols >= 120
	compW := cols - 6
	if hasSidebar {
		compW = cols - 34
	}
	compW = max(36, compW)

	drawSidebar := func() {
		if !hasSidebar {
			return
		}
		sc := cols - 26
		b.WriteString(cup(3, sc) + "Context")
		b.WriteString(cup(4, sc) + "14,200 tokens")
		b.WriteString(cup(5, sc) + "11% used")
	}

	drawHeavyComposer := func(baseRow int, draft []string, showHint bool) {
		b.WriteString(cup(baseRow, 3) + "┃")
		r := baseRow + 1
		if len(draft) == 0 {
			hint := ""
			if showHint {
				hint = fitRunes(`Ask anything… "Fix a TODO in the codebase"`, compW-4)
			}
			b.WriteString(cup(r, 3) + "┃  " + hint)
			r++
		} else {
			wrapped := wrapDraftToWidth(draft, compW-4)
			if len(wrapped) > 4 {
				wrapped = wrapped[:4]
			}
			for _, l := range wrapped {
				b.WriteString(cup(r, 3) + "┃  " + fitRunes(l, compW-4))
				r++
			}
		}
		b.WriteString(cup(r, 3) + "┃")
		r++
		b.WriteString(cup(r, 3) + "┃  " + fitRunes("Build · Big Pickle OpenCode Zen", compW-4))
		r++
		b.WriteString(cup(r, 3) + "╹" + strings.Repeat("▀", compW-1))
	}

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[?1049h\x1b[2J" + cup(2, 3) + "OpenCode — loading LSP & MCP configuration…")
	case StateIdleEmpty:
		b.WriteString("\x1b[?1049h\x1b[2J")
		if spec.Variant%2 == 0 {
			// Centered fresh-spawn composer with blank viewport gap above bottom status bar.
			mid := max(3, rows/2-3)
			drawHeavyComposer(mid, nil, true)
			b.WriteString(cup(mid+5, 3) + "tab agents  ctrl+p commands")
			b.WriteString(cup(rows, 3) + fitRunes("/home/agent/projects/demo   1.18.32", cols-4))
		} else {
			// Active-session bottom-pinned composer (no "Ask anything" hint) + right sidebar.
			_ = writeTranscriptPrefix(b, spec.TranscriptLines, compW, rows-10)
			drawSidebar()
			drawHeavyComposer(rows-6, nil, false)
			b.WriteString(cup(rows-1, 3) + fitRunes("tab agents  ctrl+p commands   • OpenCode 1.18.32", cols-4))
		}
	case StateIdleDraft:
		b.WriteString("\x1b[?1049h\x1b[2J")
		drawSidebar()
		draft := spec.DraftLines
		if len(draft) == 0 {
			draft = []string{"audit retry backoff and add deterministic tests"}
		}
		wrapped := wrapDraftToWidth(draft, compW-4)
		nRows := min(4, len(wrapped))
		base := max(3, rows-6-nRows)
		drawHeavyComposer(base, draft, false)
		b.WriteString(cup(rows-1, 3) + fitRunes("tab agents  ctrl+p commands   • OpenCode 1.18.32", cols-4))
	case StateModalTrap:
		b.WriteString("\x1b[?1049h\x1b[2J")
		if spec.Variant%2 == 0 {
			b.WriteString(cup(rows-5, 3) + "Do you trust the contents of this project?")
			b.WriteString(cup(rows-3, 3) + "❯ 1. Yes, allow workspace")
			b.WriteString(cup(rows-2, 3) + "  2. No, abort")
			b.WriteString(cup(rows-1, 3) + "Enter to confirm · Esc to cancel")
		} else {
			b.WriteString(cup(rows-4, 3) + "Select login method")
			b.WriteString(cup(rows-2, 3) + "Paste the code from the browser")
		}
	case StateWorkingStreaming:
		b.WriteString("\x1b[?1049h\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, compW, rows-10)
		drawSidebar()
		drawHeavyComposer(rows-6, nil, false)
		hint := "⬝⬝⬝⬝■■■■  esc interrupt"
		if spec.Variant%2 == 1 {
			hint = "⬝⬝■■■■⬝⬝  esc again to interrupt"
		}
		b.WriteString(cup(rows-1, 3) + "⬝⬝⬝⬝⬝⬝⬝⬝  esc interrupt\r" + cup(rows-1, 3) + fitRunes(hint+"    tab agents  ctrl+p commands   • OpenCode 1.18.32", cols-4))
	case StateRateLimitedError:
		b.WriteString("\x1b[?1049h\x1b[2J")
		b.WriteString(cup(3, 3) + "\x1b[31merror: 429 rate limit exceeded — provider usage limit reached\x1b[0m")
	case StateExitedToShell:
		b.WriteString(cup(1, 1) + "user@host:~/proj$ opencode\r\n")
		b.WriteString("\x1b[?1049h\x1b[2J")
		drawHeavyComposer(rows-6, nil, true)
		b.WriteString("\x1b[?1049l")
		b.WriteString("user@host:~/proj$ ")
	}
}

// ---------------- 7. Pi Coding Agent (pi) ----------------

func piStatsLine(cols int) string {
	full := "↑4.4k ↓380 R2.1k $0.013 2.6%/1.0M (auto)  claude-sonnet-4"
	if utf8.RuneCountInString(full) <= cols-1 {
		return full
	}
	return "↑4.4k ↓380 2.6%/1.0M"
}

func emitPiStream(b *strings.Builder, spec FrameSpec) {
	cols, rows := spec.Cols, spec.Rows
	ruleW := max(24, cols-2)
	plainRule := strings.Repeat("─", ruleW)

	switch spec.State {
	case StateBooting:
		b.WriteString("\x1b[2J" + cup(1, 1) + "pi v1.3.0\r\nLoading extensions…")
	case StateIdleEmpty:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		b.WriteString(cup(rows-4, 1) + "\x1b[2m" + plainRule + "\x1b[0m")
		b.WriteString(cup(rows-3, 1) + " ")
		b.WriteString(cup(rows-2, 1) + "\x1b[2m" + plainRule + "\x1b[0m")
		b.WriteString(cup(rows-1, 1) + "~/projects/demo (main)")
		b.WriteString(cup(rows, 1) + piStatsLine(cols))
	case StateIdleDraft:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-12)
		wrapped := wrapDraftToWidth(spec.DraftLines, ruleW-2)
		topRule := plainRule
		botRule := plainRule
		if spec.Variant%2 == 1 && ruleW >= 30 {
			padL := (ruleW - 10) / 2
			padR := ruleW - 10 - padL
			topRule = strings.Repeat("─", padL) + " ↑ 2 more " + strings.Repeat("─", padR)
			botRule = strings.Repeat("─", padL) + " ↓ 1 more " + strings.Repeat("─", padR)
		}
		topRow := max(2, rows-3-len(wrapped))
		b.WriteString(cup(topRow, 1) + topRule)
		for i, l := range wrapped {
			b.WriteString(cup(topRow+1+i, 1) + " " + fitRunes(l, ruleW-2))
		}
		b.WriteString(cup(topRow+1+len(wrapped), 1) + botRule)
		b.WriteString(cup(rows-1, 1) + "~/projects/demo (main)")
		b.WriteString(cup(rows, 1) + piStatsLine(cols))
	case StateModalTrap:
		b.WriteString("\x1b[2J")
		if spec.Variant%2 == 0 {
			// Pi SelectList command autocomplete open between bottom rule and footer.
			b.WriteString(cup(rows-6, 1) + plainRule)
			b.WriteString(cup(rows-5, 1) + " /mod")
			b.WriteString(cup(rows-4, 1) + plainRule)
			b.WriteString(cup(rows-3, 1) + " → /model    Select a model")
			b.WriteString(cup(rows-2, 1) + "   /models   Configure available models")
			b.WriteString(cup(rows-1, 1) + "~/projects/demo (main)")
			b.WriteString(cup(rows, 1) + piStatsLine(cols))
		} else {
			b.WriteString(cup(rows-5, 1) + "Do you trust the files in this folder?")
			b.WriteString(cup(rows-3, 1) + "❯ 1. Yes, trust folder")
			b.WriteString(cup(rows-2, 1) + "  2. No, exit")
			b.WriteString(cup(rows-1, 1) + "Enter to confirm · Esc to cancel")
		}
	case StateWorkingStreaming:
		b.WriteString("\x1b[2J")
		_ = writeTranscriptPrefix(b, spec.TranscriptLines, cols, rows-8)
		workPrefix := "── ⠹ Working "
		workFill := max(4, ruleW-utf8.RuneCountInString(workPrefix))
		workRule := workPrefix + strings.Repeat("─", workFill)
		spinA := "── ⠋ Working " + strings.Repeat("─", workFill)
		b.WriteString(cup(rows-4, 1) + spinA + "\r\x1b[K\x1b[36m" + workRule + "\x1b[0m")
		b.WriteString(cup(rows-3, 1) + " ")
		b.WriteString(cup(rows-2, 1) + plainRule)
		b.WriteString(cup(rows-1, 1) + "~/projects/demo (main)")
		b.WriteString(cup(rows, 1) + piStatsLine(cols))
	case StateRateLimitedError:
		b.WriteString("\x1b[2J")
		b.WriteString(cup(2, 1) + "pi v1.3.0")
		b.WriteString(cup(4, 1) + "\x1b[31mError: 429 Too Many Requests — rate limit exceeded\x1b[0m")
	case StateExitedToShell:
		if spec.Variant%2 == 0 {
			b.WriteString("\x1b[2J")
			b.WriteString(cup(rows-5, 1) + plainRule)
			b.WriteString(cup(rows-4, 1) + " ")
			b.WriteString(cup(rows-3, 1) + plainRule)
			b.WriteString(cup(rows-2, 1) + "~/projects/demo (main)")
			b.WriteString(cup(rows-1, 1) + piStatsLine(cols))
			b.WriteString(cup(rows, 1) + "user@host:~/demo$")
		} else {
			b.WriteString(cup(1, 1) + "user@host:~/demo$ pi\r\n\x1b[?1049h\x1b[2J")
			b.WriteString(cup(rows-4, 1) + plainRule + "\r\n \r\n" + plainRule)
			b.WriteString("\x1b[?1049luser@host:~/demo$ ")
		}
	}
}

// DefaultBaseSeed is the deterministic root seed used when NTM_VT_SEED is unset.
const DefaultBaseSeed uint64 = 0x4e544d5f56543130

// ResolveSeed returns NTM_VT_SEED when set (decimal or 0x hex) or DefaultBaseSeed.
func ResolveSeed() uint64 {
	raw := strings.TrimSpace(os.Getenv("NTM_VT_SEED"))
	if raw == "" {
		return DefaultBaseSeed
	}
	if v, err := strconv.ParseUint(raw, 0, 64); err == nil {
		return v
	}
	return DefaultBaseSeed
}

// DeriveSeed mixes a root seed and iteration index into a deterministic sub-seed.
func DeriveSeed(base uint64, index int) uint64 {
	return base + uint64(index)*0x9e3779b97f4a7c15
}

// NewSeededRNG constructs a deterministic PCG PRNG for the given seed.
func NewSeededRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0xa5a5a5a55a5a5a5a))
}

// ReproCommand returns a one-command shell invocation that replays a failing seed.
func ReproCommand(pkg, testName string, seed uint64) string {
	return fmt.Sprintf("NTM_VT_SEED=0x%x go test -v %s -run '^%s$'", seed, pkg, testName)
}

// RandomByteChunks partitions stream into random 1..maxChunk byte slices,
// returning both the slices and the chunk lengths for hindsight bundles.
func RandomByteChunks(rng *rand.Rand, stream []byte, maxChunk int) ([][]byte, []int) {
	if maxChunk < 1 {
		maxChunk = 17
	}
	var chunks [][]byte
	var splits []int
	for pos := 0; pos < len(stream); {
		sz := 1 + rng.IntN(maxChunk)
		if pos+sz > len(stream) {
			sz = len(stream) - pos
		}
		chunks = append(chunks, stream[pos:pos+sz])
		splits = append(splits, sz)
		pos += sz
	}
	return chunks, splits
}

var sampleDraftTokens = []string{
	"fix", "retry", "backoff", "race", "condition", "in", "dispatcher",
	"verify", "edge-cases", "across", "narrow", "panes", "with", "utf8",
	"check", "database", "timeout", "propagation", "and", "context", "cancel",
}

// RandomDraftLines synthesizes 1..maxLines realistic draft lines using rng.
func RandomDraftLines(rng *rand.Rand, maxLines int) []string {
	if maxLines < 1 {
		maxLines = 1
	}
	nLines := 1 + rng.IntN(maxLines)
	out := make([]string, nLines)
	for i := range nLines {
		words := 3 + rng.IntN(9)
		parts := make([]string, words)
		for w := range words {
			parts[w] = sampleDraftTokens[rng.IntN(len(sampleDraftTokens))]
		}
		out[i] = strings.Join(parts, " ")
	}
	return out
}

// RunParallelSeeds executes total deterministic seed indices [0, total) across
// worker goroutines, exercising concurrent read safety under -race while cutting
// wall-clock runtime. Returns the lowest-index failure string, or "" if all pass.
func RunParallelSeeds(total int, fn func(index int) string) string {
	if total <= 0 {
		return ""
	}
	workers := min(total, max(2, runtime.GOMAXPROCS(0)))
	failures := make([]string, total)
	var cursor atomic.Int64
	var anyFailed atomic.Bool
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				if anyFailed.Load() {
					return
				}
				idx := int(cursor.Add(1) - 1)
				if idx >= total {
					return
				}
				if msg := fn(idx); msg != "" {
					failures[idx] = msg
					anyFailed.Store(true)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, msg := range failures {
		if msg != "" {
			return msg
		}
	}
	return ""
}
