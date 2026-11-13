// Package vttest provides a deterministic in-memory VT100/ANSI virtual terminal
// screen oracle, raw PTY byte-stream corpus across all 7 interactive agent
// harnesses, key-transition simulation, and structured hindsight failure
// bundles for metamorphic verification.
package vttest

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// HarnessID identifies one of the 7 interactive agent TUI harnesses.
type HarnessID string

const (
	HarnessClaude      HarnessID = "cc"
	HarnessCodex       HarnessID = "cod"
	HarnessAntigravity HarnessID = "agy"
	HarnessOmp         HarnessID = "omp"
	HarnessGrok        HarnessID = "grok"
	HarnessOpencode    HarnessID = "oc"
	HarnessPi          HarnessID = "pi"
)

// AllHarnesses lists all 7 supported interactive agent TUI harnesses in canonical order.
var AllHarnesses = []HarnessID{
	HarnessClaude,
	HarnessCodex,
	HarnessAntigravity,
	HarnessOmp,
	HarnessGrok,
	HarnessOpencode,
	HarnessPi,
}

// LifecycleState enumerates the 7 canonical TUI lifecycle states in the
// Graveyard §3.10 hindsight corpus.
type LifecycleState string

const (
	StateBooting          LifecycleState = "S1_BOOTING"
	StateIdleEmpty        LifecycleState = "S2_IDLE_EMPTY_COMPOSER"
	StateIdleDraft        LifecycleState = "S3_IDLE_DRAFT_IN_COMPOSER"
	StateModalTrap        LifecycleState = "S4_MODAL_TRAP"
	StateWorkingStreaming LifecycleState = "S5_WORKING_STREAMING"
	StateRateLimitedError LifecycleState = "S6_RATE_LIMITED_OR_ERROR"
	StateExitedToShell    LifecycleState = "S7_EXITED_TO_SHELL"
)

// AllLifecycleStates lists all 7 lifecycle states in canonical order.
var AllLifecycleStates = []LifecycleState{
	StateBooting,
	StateIdleEmpty,
	StateIdleDraft,
	StateModalTrap,
	StateWorkingStreaming,
	StateRateLimitedError,
	StateExitedToShell,
}

type cell struct {
	r            rune
	width        int
	continuation bool
}

type screenBuf struct {
	cols             int
	rows             int
	cells            [][]cell
	curRow           int
	curCol           int
	pendingWrap      bool
	savedRow         int
	savedCol         int
	savedPendingWrap bool
}

func newScreenBuf(cols, rows int) screenBuf {
	b := screenBuf{
		cols:  cols,
		rows:  rows,
		cells: make([][]cell, rows),
	}
	for r := range rows {
		b.cells[r] = makeBlankRow(cols)
	}
	return b
}

func makeBlankRow(cols int) []cell {
	return make([]cell, cols)
}

func (b *screenBuf) clearCell(row, col int) {
	if row < 0 || row >= b.rows || col < 0 || col >= b.cols {
		return
	}
	cur := b.cells[row][col]
	if cur.continuation && col > 0 {
		b.cells[row][col-1] = cell{}
	}
	if cur.width == 2 && col+1 < b.cols {
		b.cells[row][col+1] = cell{}
	}
	b.cells[row][col] = cell{}
}

func (b *screenBuf) clearRange(row, startCol, endCol int) {
	if row < 0 || row >= b.rows {
		return
	}
	startCol = max(0, startCol)
	endCol = min(b.cols-1, endCol)
	for c := startCol; c <= endCol; c++ {
		b.clearCell(row, c)
	}
}

func (b *screenBuf) clearAll() {
	for r := range b.rows {
		clear(b.cells[r])
	}
	b.pendingWrap = false
}

func (b *screenBuf) scrollUp(n int) {
	if n <= 0 {
		return
	}
	if n >= b.rows {
		b.clearAll()
		return
	}
	copy(b.cells, b.cells[n:])
	for r := b.rows - n; r < b.rows; r++ {
		b.cells[r] = makeBlankRow(b.cols)
	}
}

func (b *screenBuf) scrollDown(n int) {
	if n <= 0 {
		return
	}
	if n >= b.rows {
		b.clearAll()
		return
	}
	copy(b.cells[n:], b.cells[:b.rows-n])
	for r := range n {
		b.cells[r] = makeBlankRow(b.cols)
	}
}

func (b *screenBuf) lineFeed() {
	if b.curRow+1 >= b.rows {
		b.scrollUp(1)
		b.curRow = b.rows - 1
	} else {
		b.curRow++
	}
}

func (b *screenBuf) saveCursor() {
	b.savedRow = b.curRow
	b.savedCol = b.curCol
	b.savedPendingWrap = b.pendingWrap
}

func (b *screenBuf) restoreCursor() {
	b.curRow = clamp(b.savedRow, 0, b.rows-1)
	b.curCol = clamp(b.savedCol, 0, b.cols-1)
	b.savedPendingWrap = false
	b.pendingWrap = false
}

// InteractiveModel tracks harness-level interactive modal/draft state alongside
// the raw VT100 screen so SimulateKey can deterministically model keystroke
// policies (Escape pairs in Codex, Ctrl+C double-press quit in omp, C-u line
// kill vs C-e+C-u multi-row drain, etc.) and re-render the screen authentically.
type InteractiveModel struct {
	Harness           HarnessID
	State             LifecycleState
	DraftLines        []string
	CursorLine        int
	CursorCol         int
	EscapeStreak      int
	EmptyCtrlCCount   int
	LastEmptyCtrlCAt  time.Time
	SimulatedClock    time.Time
	ModalReason       string
	ExitedViaDoubleCC bool
}

// Terminal is a deterministic VT100/ANSI virtual terminal screen state machine
// backed by github.com/charmbracelet/x/ansi's byte-by-byte DEC parser.
type Terminal struct {
	cols       int
	rows       int
	primary    screenBuf
	alt        screenBuf
	useAlt     bool
	autoWrap   bool
	syncOutput bool
	sgrActive  bool
	parser     *ansi.Parser
	sim        InteractiveModel
}

// New creates a new deterministic virtual terminal of dimensions (cols, rows).
func New(cols, rows int) *Terminal {
	if cols < 4 {
		cols = 4
	}
	if rows < 2 {
		rows = 2
	}
	t := &Terminal{
		cols:     cols,
		rows:     rows,
		primary:  newScreenBuf(cols, rows),
		autoWrap: true,
		parser:   ansi.NewParser(),
		sim: InteractiveModel{
			SimulatedClock: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
		},
	}
	t.parser.SetHandler(ansi.Handler{
		Print:     t.handlePrint,
		Execute:   t.handleExecute,
		HandleCsi: t.handleCSI,
		HandleEsc: t.handleESC,
	})
	return t
}

// Reset reinitializes the terminal to (cols, rows) while reusing its DEC parser
// and cell-grid backing arrays when dimensions match.
func (t *Terminal) Reset(cols, rows int) {
	if cols < 4 {
		cols = 4
	}
	if rows < 2 {
		rows = 2
	}
	if t.cols == cols && t.rows == rows && len(t.primary.cells) == rows {
		t.primary.clearAll()
		t.primary.curRow = 0
		t.primary.curCol = 0
		t.primary.savedRow = 0
		t.primary.savedCol = 0
		t.primary.savedPendingWrap = false
	} else {
		t.cols = cols
		t.rows = rows
		t.primary = newScreenBuf(cols, rows)
	}
	t.alt = screenBuf{}
	t.useAlt = false
	t.autoWrap = true
	t.syncOutput = false
	t.sgrActive = false
	t.parser.Reset()
}

// Cols returns the terminal's column width.
func (t *Terminal) Cols() int { return t.cols }

// Rows returns the terminal's row height.
func (t *Terminal) Rows() int { return t.rows }

// InAltScreen reports whether DEC private mode ?1049h / ?1047h / ?47h is active.
func (t *Terminal) InAltScreen() bool { return t.useAlt }

// SyncOutput reports whether DEC synchronized output mode (?2026h) is active.
func (t *Terminal) SyncOutput() bool { return t.syncOutput }

// Interactive returns a snapshot of the simulated interactive key model.
func (t *Terminal) Interactive() InteractiveModel {
	out := t.sim
	if len(t.sim.DraftLines) > 0 {
		out.DraftLines = append([]string(nil), t.sim.DraftLines...)
	}
	return out
}

func (t *Terminal) activeBuf() *screenBuf {
	if t.useAlt {
		return &t.alt
	}
	return &t.primary
}

// Write feeds an arbitrary byte slice (including mid-UTF-8 rune and mid-CSI
// escape sequence chunks) into the DEC ANSI state machine.
func (t *Terminal) Write(p []byte) (int, error) {
	for i := range p {
		t.parser.Advance(p[i])
	}
	return len(p), nil
}

// WriteString writes s into the terminal state machine.
func (t *Terminal) WriteString(s string) {
	for i := 0; i < len(s); i++ {
		t.parser.Advance(s[i])
	}
}

func runeCellWidth(r rune) int {
	if r >= 0x20 && r <= 0x7e {
		return 1
	}
	if r == utf8.RuneError || r < 0x20 {
		return 0
	}
	// Box-drawing, block elements, geometric shapes, Braille spinners, arrows,
	// dingbats, and Private Use Area (Nerd Font) icons render in 1 terminal
	// cell in modern monospace TUI configurations (tmux/xterm/Ghostty/WezTerm).
	switch {
	case r >= 0x2500 && r <= 0x259F: // Box Drawing + Block Elements
		return 1
	case r >= 0x2800 && r <= 0x28FF: // Braille Patterns
		return 1
	case r >= 0xE000 && r <= 0xF8FF, r >= 0xF0000 && r <= 0xFFFFD: // Nerd Font PUA
		return 1
	}
	w := ansi.StringWidth(string(r))
	if w <= 0 {
		return 0
	}
	if w > 2 {
		return 2
	}
	return w
}

func (t *Terminal) handlePrint(r rune) {
	w := runeCellWidth(r)
	if w == 0 {
		return
	}
	b := t.activeBuf()
	if b.pendingWrap && t.autoWrap {
		b.pendingWrap = false
		b.curCol = 0
		b.lineFeed()
	}
	if w == 2 && b.curCol == b.cols-1 {
		if !t.autoWrap {
			b.clearCell(b.curRow, b.curCol)
			return
		}
		b.clearCell(b.curRow, b.curCol)
		b.curCol = 0
		b.lineFeed()
	}
	if w == 2 && b.cols < 2 {
		return
	}

	b.clearCell(b.curRow, b.curCol)
	if w == 2 && b.curCol+1 < b.cols {
		b.clearCell(b.curRow, b.curCol+1)
		b.cells[b.curRow][b.curCol] = cell{r: r, width: 2}
		b.cells[b.curRow][b.curCol+1] = cell{r: 0, width: 0, continuation: true}
	} else {
		b.cells[b.curRow][b.curCol] = cell{r: r, width: 1}
	}

	if b.curCol+w >= b.cols {
		b.curCol = b.cols - 1
		if t.autoWrap {
			b.pendingWrap = true
		}
	} else {
		b.curCol += w
	}
}

func (t *Terminal) handleExecute(ctrl byte) {
	b := t.activeBuf()
	switch ctrl {
	case '\r':
		b.curCol = 0
		b.pendingWrap = false
	case '\n', '\v', '\f':
		// Match default POSIX PTY ONLCR behavior where newline resets column 0
		// and advances/scrolls one line; explicit CSI B (CUD) moves straight
		// down without changing curCol.
		b.curCol = 0
		b.pendingWrap = false
		b.lineFeed()
	case '\b':
		b.pendingWrap = false
		if b.curCol > 0 {
			b.curCol--
		}
	case '\t':
		b.pendingWrap = false
		next := ((b.curCol / 8) + 1) * 8
		if next >= b.cols {
			b.curCol = b.cols - 1
		} else {
			b.curCol = next
		}
	}
}

func paramPositive(params ansi.Params, idx, def int) int {
	v, _, ok := params.Param(idx, def)
	if !ok || v <= 0 {
		return def
	}
	return v
}

func paramNonNeg(params ansi.Params, idx, def int) int {
	v, _, ok := params.Param(idx, def)
	if !ok || v < 0 {
		return def
	}
	return v
}

func (t *Terminal) handleCSI(cmd ansi.Cmd, params ansi.Params) {
	if cmd.Prefix() == '?' {
		t.handleDECPrivateCSI(cmd.Final(), params)
		return
	}
	if cmd.Prefix() != 0 || cmd.Intermediate() != 0 {
		return
	}
	b := t.activeBuf()
	switch cmd.Final() {
	case 'A': // CUU - Cursor Up
		b.pendingWrap = false
		b.curRow = max(0, b.curRow-paramPositive(params, 0, 1))
	case 'B': // CUD - Cursor Down
		b.pendingWrap = false
		b.curRow = min(b.rows-1, b.curRow+paramPositive(params, 0, 1))
	case 'C': // CUF - Cursor Forward
		b.pendingWrap = false
		b.curCol = min(b.cols-1, b.curCol+paramPositive(params, 0, 1))
	case 'D': // CUB - Cursor Back
		b.pendingWrap = false
		b.curCol = max(0, b.curCol-paramPositive(params, 0, 1))
	case 'E': // CNL - Cursor Next Line
		b.pendingWrap = false
		b.curRow = min(b.rows-1, b.curRow+paramPositive(params, 0, 1))
		b.curCol = 0
	case 'F': // CPL - Cursor Previous Line
		b.pendingWrap = false
		b.curRow = max(0, b.curRow-paramPositive(params, 0, 1))
		b.curCol = 0
	case 'G', '`': // CHA / HPA - Cursor Horizontal Absolute (1-based)
		b.pendingWrap = false
		b.curCol = clamp(paramPositive(params, 0, 1)-1, 0, b.cols-1)
	case 'd': // VPA - Vertical Position Absolute (1-based)
		b.pendingWrap = false
		b.curRow = clamp(paramPositive(params, 0, 1)-1, 0, b.rows-1)
	case 'H', 'f': // CUP / HVP - Cursor Position (1-based row;col)
		b.pendingWrap = false
		b.curRow = clamp(paramPositive(params, 0, 1)-1, 0, b.rows-1)
		b.curCol = clamp(paramPositive(params, 1, 1)-1, 0, b.cols-1)
	case 'J': // ED - Erase in Display
		b.pendingWrap = false
		switch paramNonNeg(params, 0, 0) {
		case 0:
			b.clearRange(b.curRow, b.curCol, b.cols-1)
			for r := b.curRow + 1; r < b.rows; r++ {
				b.cells[r] = makeBlankRow(b.cols)
			}
		case 1:
			for r := 0; r < b.curRow; r++ {
				b.cells[r] = makeBlankRow(b.cols)
			}
			b.clearRange(b.curRow, 0, b.curCol)
		case 2, 3:
			b.clearAll()
		}
	case 'K': // EL - Erase in Line
		b.pendingWrap = false
		switch paramNonNeg(params, 0, 0) {
		case 0:
			b.clearRange(b.curRow, b.curCol, b.cols-1)
		case 1:
			b.clearRange(b.curRow, 0, b.curCol)
		case 2:
			b.cells[b.curRow] = makeBlankRow(b.cols)
		}
	case 'X': // ECH - Erase Characters
		b.pendingWrap = false
		n := paramPositive(params, 0, 1)
		b.clearRange(b.curRow, b.curCol, b.curCol+n-1)
	case 'S': // SU - Scroll Up
		b.pendingWrap = false
		b.scrollUp(paramPositive(params, 0, 1))
	case 'T': // SD - Scroll Down
		b.pendingWrap = false
		b.scrollDown(paramPositive(params, 0, 1))
	case 's': // SCOSC - Save Cursor
		b.saveCursor()
	case 'u': // SCORC - Restore Cursor
		b.restoreCursor()
	case 'm': // SGR - Select Graphic Rendition (stripped in CapturePlain)
		if len(params) == 0 {
			t.sgrActive = false
			return
		}
		params.ForEach(0, func(_ int, param int, _ bool) {
			if param == 0 {
				t.sgrActive = false
			} else {
				t.sgrActive = true
			}
		})
	}
}

func (t *Terminal) handleDECPrivateCSI(final byte, params ansi.Params) {
	enable := final == 'h'
	if !enable && final != 'l' {
		return
	}
	params.ForEach(0, func(_ int, mode int, _ bool) {
		switch mode {
		case 7: // DECAWM - Autowrap Mode
			t.autoWrap = enable
		case 47, 1047: // Alternate Screen Buffer (without cursor save)
			if enable && !t.useAlt {
				t.alt = newScreenBuf(t.cols, t.rows)
				t.useAlt = true
			} else if !enable && t.useAlt {
				t.useAlt = false
			}
		case 1048: // Save/Restore Cursor
			if enable {
				t.activeBuf().saveCursor()
			} else {
				t.activeBuf().restoreCursor()
			}
		case 1049: // smcup / rmcup (Save cursor + Alternate Screen Buffer)
			if enable {
				t.primary.saveCursor()
				t.alt = newScreenBuf(t.cols, t.rows)
				t.useAlt = true
			} else if t.useAlt {
				t.useAlt = false
				t.primary.restoreCursor()
			}
		case 2026: // Synchronized Output
			t.syncOutput = enable
		}
	})
}

func (t *Terminal) handleESC(cmd ansi.Cmd) {
	if cmd.Intermediate() != 0 {
		return
	}
	b := t.activeBuf()
	switch cmd.Final() {
	case '7': // DECSC
		b.saveCursor()
	case '8': // DECRC
		b.restoreCursor()
	case 'D': // IND - Index
		b.pendingWrap = false
		b.lineFeed()
	case 'E': // NEL - Next Line
		b.pendingWrap = false
		b.curCol = 0
		b.lineFeed()
	case 'M': // RI - Reverse Index
		b.pendingWrap = false
		if b.curRow == 0 {
			b.scrollDown(1)
		} else {
			b.curRow--
		}
	case 'c': // RIS - Full Reset
		t.primary = newScreenBuf(t.cols, t.rows)
		t.alt = newScreenBuf(t.cols, t.rows)
		t.useAlt = false
		t.autoWrap = true
		t.syncOutput = false
		t.sgrActive = false
		t.parser.Reset()
	}
}

// CapturePlain renders the visible viewport identically to `tmux capture-pane -p -S 0`:
// SGR attributes are stripped, wide-character continuation cells are collapsed,
// trailing spaces on each viewport row are trimmed, and all rows are joined by "\n".
func (t *Terminal) CapturePlain() string {
	b := t.activeBuf()
	var sb strings.Builder
	sb.Grow(b.rows * min(80, b.cols))
	for r := range b.rows {
		if r > 0 {
			sb.WriteByte('\n')
		}
		row := b.cells[r]
		end := b.cols
		for end > 0 {
			c := row[end-1]
			if c.continuation || (c.r != 0 && c.r != ' ') {
				break
			}
			end--
		}
		for c := 0; c < end; c++ {
			cell := row[c]
			if cell.continuation {
				continue
			}
			ch := cell.r
			if ch == 0 {
				sb.WriteByte(' ')
			} else {
				sb.WriteRune(ch)
			}
		}
	}
	return sb.String()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
