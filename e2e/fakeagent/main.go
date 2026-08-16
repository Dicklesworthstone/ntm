// Command fakeagent is a controllable agent-TUI test fixture (bd-kur07).
//
// It runs as a tmux pane's foreground process and mimics the surface
// contracts NTM's detectors key on, so E2E tests can exercise the delivery
// and observability stack against a REAL non-shell process:
//
//   - a bottom-pinned composer marker ("❯ " for --persona=claude, "› " for
//     --persona=codex) plus realistic footer chrome, so RefuseDeadAgentPane,
//     tmux.InspectComposer, and ComposerReadyForDelivery see an agent;
//   - typed text accumulates in the composer; Enter submits (echoes the
//     message into the transcript with the persona's echo prefix and clears
//     the composer); C-u clears; Escape dismisses a simulated picker;
//     bracketed paste is honored (newlines inside a paste do NOT submit —
//     the same property that makes NTM's double-Enter protocol necessary);
//   - scriptable behaviors via a polled control file (--control): "strand"
//     swallows the next submit Enter (reproducing ntm-8ubn), "work N"
//     renders live working chrome for N seconds then a completion line,
//     "gate trust|login|theme" renders the corresponding interactive gate
//     screen, "ratelimit codex|claude" prints the provider usage-limit
//     banner, "ack TEXT" emits TEXT to the transcript after a short delay,
//     "clear" empties the transcript, "quit" exits;
//   - a structured JSONL event log (--log) records every keystroke, state
//     transition, and render with timestamps so E2E assertions run against
//     fixture ground truth rather than robot envelopes alone;
//   - painted lines are hard-wrapped at the real terminal width, so
//     narrow-pane tests (bd-eeifh) exercise genuine wrapping.
//
// The binary is a test fixture only: it is built on demand by the E2E
// harness and never shipped in releases.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

type persona struct {
	name        string
	marker      string // live composer marker (bottom-pinned)
	echoPrefix  string // transcript prefix for submitted user turns
	footer      string
	workingLine string // live working chrome (must satisfy the busy detectors)
	doneLine    string // turn-ended line (claude completion shape)
	limitBanner string // provider usage-limit banner (must satisfy rate-limit detectors)
}

var personas = map[string]persona{
	"claude": {
		name:        "claude",
		marker:      "❯",
		echoPrefix:  "> ",
		footer:      "? for shortcuts",
		workingLine: "✻ Cooking… (esc to interrupt)",
		doneLine:    "✻ Cooked for %ds",
		limitBanner: "You've hit your limit · resets 5pm",
	},
	"codex": {
		name:        "codex",
		marker:      "›",
		echoPrefix:  "› ",
		footer:      "47% context left · ? for shortcuts",
		workingLine: "• Working (%ds • esc to interrupt)",
		doneLine:    "",
		limitBanner: "You've hit your usage limit. Please try again at 7:00 PM.",
	},
}

// gateScreens are full-screen modal renders WITHOUT a composer marker, so
// ComposerReadyForDelivery reports not-ready and DetectInteractiveGate
// matches. Any keypress dismisses the gate.
var gateScreens = map[string][]string{
	"trust": {
		"",
		"  Do you trust the contents of this project?",
		"",
		"  Antigravity needs your confirmation before reading files here.",
		"",
		"  [Enter] Yes, I trust this folder    [Esc] No",
	},
	"login": {
		"",
		"  Select login method",
		"",
		"    1. Sign in with browser",
		"    2. Paste an API key",
		"",
		"  Press enter to open browser",
	},
	"theme": {
		"",
		"  Welcome! Choose the text style that looks best with your terminal:",
		"",
		"    1. Dark mode",
		"    2. Light mode",
		"",
		"  Use arrows and press Enter",
	},
}

type event struct {
	TS    string `json:"ts"`
	Event string `json:"event"`
	Data  string `json:"data,omitempty"`
	N     int    `json:"n,omitempty"`
}

type agentState struct {
	p          persona
	transcript []string
	composer   string
	inPaste    bool
	// strandRemaining: number of submit-Enters to swallow. The double-Enter
	// delivery protocol sends TWO Enters after the paste; reproducing the
	// stranded-composer state (ntm-8ubn) requires swallowing both so the
	// verification's rescue Enter is the one that finally submits.
	strandRemaining int
	pickerOpen      bool
	gate            string // active gate screen name, "" when none
	workUntil       time.Time
	workStart       time.Time
	width           int
	height          int

	logFile *os.File
}

func (s *agentState) log(ev string, data string, n int) {
	if s.logFile == nil {
		return
	}
	rec, err := json.Marshal(event{
		TS:    time.Now().UTC().Format(time.RFC3339Nano),
		Event: ev,
		Data:  data,
		N:     n,
	})
	if err != nil {
		return
	}
	rec = append(rec, '\n')
	_, _ = s.logFile.Write(rec)
}

func (s *agentState) working() bool { return time.Now().Before(s.workUntil) }

// wrap hard-wraps a line at the pane width, mirroring what the terminal
// itself does to overlong lines, so captures of narrow panes look real.
func (s *agentState) wrap(line string) []string {
	width := s.width
	if width <= 0 {
		width = 80
	}
	runes := []rune(line)
	if len(runes) <= width {
		return []string{line}
	}
	var out []string
	for len(runes) > width {
		out = append(out, string(runes[:width]))
		runes = runes[width:]
	}
	out = append(out, string(runes))
	return out
}

// render repaints the whole screen: transcript tail, then chrome. The
// composer is always the bottom-most marker line, matching the real TUIs'
// bottom-pinned input boxes.
func (s *agentState) render() {
	var b bytes.Buffer
	b.WriteString("\x1b[2J\x1b[H") // clear + home

	var lines []string
	if s.gate != "" {
		for _, l := range gateScreens[s.gate] {
			lines = append(lines, s.wrap(l)...)
		}
	} else {
		height := s.height
		if height <= 0 {
			height = 24
		}
		// Reserve rows for chrome: working line, composer, footer, picker.
		budget := height - 4
		if budget < 3 {
			budget = 3
		}
		var body []string
		for _, l := range s.transcript {
			body = append(body, s.wrap(l)...)
		}
		if len(body) > budget {
			body = body[len(body)-budget:]
		}
		lines = append(lines, body...)
		if s.working() {
			working := s.p.workingLine
			if strings.Contains(working, "%d") {
				working = fmt.Sprintf(working, int(time.Since(s.workStart).Seconds()))
			}
			lines = append(lines, s.wrap(working)...)
		}
		if s.pickerOpen {
			lines = append(lines, s.wrap("  ▸ @file-suggestion   (autocomplete picker)")...)
		}
		lines = append(lines, s.wrap(s.p.marker+" "+s.composer)...)
		lines = append(lines, s.wrap(s.p.footer)...)
	}

	for i, l := range lines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
	}
	_, _ = os.Stdout.Write(b.Bytes())
	s.log("render", s.gate, len(lines))
}

func (s *agentState) submit() {
	msg := s.composer
	if s.strandRemaining > 0 {
		s.strandRemaining--
		s.log("swallow_strand", msg, s.strandRemaining)
		return // composer keeps its text: the stranded state (ntm-8ubn)
	}
	if strings.TrimSpace(msg) == "" {
		s.log("submit_empty", "", 0)
		return
	}
	s.composer = ""
	first := true
	for _, line := range strings.Split(msg, "\n") {
		if first {
			s.transcript = append(s.transcript, s.p.echoPrefix+line)
			first = false
		} else {
			s.transcript = append(s.transcript, "  "+line)
		}
	}
	s.log("submit", msg, 0)
}

// handleControl consumes one control-file verb.
func (s *agentState) handleControl(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	s.log("control", line, 0)
	fields := strings.SplitN(line, " ", 2)
	arg := ""
	if len(fields) > 1 {
		arg = strings.TrimSpace(fields[1])
	}
	switch fields[0] {
	case "strand":
		count, err := strconv.Atoi(arg)
		if err != nil || count <= 0 {
			count = 2 // both Enters of the double-Enter protocol
		}
		s.strandRemaining = count
	case "work":
		secs, err := strconv.Atoi(arg)
		if err != nil || secs <= 0 {
			secs = 5
		}
		s.workStart = time.Now()
		s.workUntil = s.workStart.Add(time.Duration(secs) * time.Second)
	case "gate":
		if _, ok := gateScreens[arg]; ok {
			s.gate = arg
		}
	case "ratelimit":
		p, ok := personas[arg]
		if !ok {
			p = s.p
		}
		s.transcript = append(s.transcript, p.limitBanner)
	case "ack":
		msg := arg
		time.AfterFunc(300*time.Millisecond, func() {
			// Delivered through the control channel's owning goroutine via
			// the ackCh to keep state single-threaded.
			select {
			case ackCh <- msg:
			default:
			}
		})
	case "picker":
		s.pickerOpen = true
	case "clear":
		s.transcript = nil
	case "quit":
		s.log("quit", "", 0)
		restoreAndExit(0)
	}
}

var ackCh = make(chan string, 8)

var restoreTerm func()

func restoreAndExit(code int) {
	if restoreTerm != nil {
		restoreTerm()
	}
	os.Exit(code)
}

func main() {
	personaFlag := flag.String("persona", "claude", "claude|codex")
	controlPath := flag.String("control", "", "control file polled for verbs")
	logPath := flag.String("log", "", "JSONL event log path")
	flag.Parse()

	p, ok := personas[*personaFlag]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown persona %q\n", *personaFlag)
		os.Exit(2)
	}

	s := &agentState{p: p, width: 80, height: 24}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open log: %v\n", err)
			os.Exit(2)
		}
		s.logFile = f
		defer f.Close()
	}

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if w, h, err := term.GetSize(fd); err == nil {
			s.width, s.height = w, h
		}
		old, err := term.MakeRaw(fd)
		if err == nil {
			restoreTerm = func() { _ = term.Restore(fd, old) }
			defer restoreTerm()
		}
	}

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, resizeSignal, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	inCh := make(chan []byte, 64)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				inCh <- chunk
			}
			if err != nil {
				close(inCh)
				return
			}
		}
	}()

	var controlOffset int64
	controlTick := time.NewTicker(100 * time.Millisecond)
	defer controlTick.Stop()
	renderTick := time.NewTicker(500 * time.Millisecond) // refresh working timer
	defer renderTick.Stop()

	s.log("start", p.name, os.Getpid())
	// Advertise bracketed-paste mode: tmux's paste-buffer -p only wraps the
	// paste in 200~/201~ markers when the pane's application enabled it —
	// exactly what the real agent TUIs do, and what makes newlines-in-paste
	// literal instead of submits.
	_, _ = os.Stdout.WriteString("[?2004h")
	defer func() { _, _ = os.Stdout.WriteString("[?2004l") }()
	s.render()

	var pending []byte // holds a trailing lone ESC across chunks
	for {
		select {
		case chunk, okCh := <-inCh:
			if !okCh {
				restoreAndExit(0)
			}
			pending = append(pending, chunk...)
			pending = s.consume(pending)
			s.render()
		case <-renderTick.C:
			s.render()
		case msg := <-ackCh:
			s.transcript = append(s.transcript, msg)
			s.log("ack_emit", msg, 0)
			s.render()
		case sig := <-sigCh:
			switch sig {
			case resizeSignal:
				if w, h, err := term.GetSize(fd); err == nil {
					s.width, s.height = w, h
				}
				s.render()
			case syscall.SIGINT:
				// Ctrl+C while working ends the turn (like a real agent);
				// otherwise ignored so the fixture survives stray signals.
				s.log("sigint", "", 0)
				if s.working() {
					s.workUntil = time.Time{}
					if s.p.doneLine != "" {
						s.transcript = append(s.transcript, fmt.Sprintf(s.p.doneLine, int(time.Since(s.workStart).Seconds())))
					}
					s.render()
				}
			case syscall.SIGTERM, syscall.SIGHUP:
				s.log("terminate", sig.String(), 0)
				restoreAndExit(0)
			}
		case <-controlTick.C:
			if *controlPath == "" {
				continue
			}
			data, err := os.ReadFile(*controlPath)
			if err != nil || int64(len(data)) <= controlOffset {
				continue
			}
			fresh := data[controlOffset:]
			controlOffset = int64(len(data))
			for _, line := range strings.Split(string(fresh), "\n") {
				s.handleControl(line)
			}
			// A finished work window appends the completion line exactly once.
			s.render()
		}

		// Work window expiry: append the completion line once.
		if !s.workUntil.IsZero() && !s.working() {
			if s.p.doneLine != "" {
				s.transcript = append(s.transcript, fmt.Sprintf(s.p.doneLine, int(s.workUntil.Sub(s.workStart).Seconds())))
			}
			s.log("work_done", "", 0)
			s.workUntil = time.Time{}
			s.render()
		}
	}
}

// consume processes input bytes, returning any trailing bytes that must
// wait for the next chunk (a lone ESC that may begin a sequence).
func (s *agentState) consume(buf []byte) []byte {
	i := 0
	for i < len(buf) {
		b := buf[i]
		switch {
		case b == 0x1b: // ESC: sequence, paste marker, or bare Escape
			if i == len(buf)-1 {
				return buf[i:] // maybe a sequence split across reads
			}
			if buf[i+1] == '[' {
				// CSI: consume through the final byte (0x40-0x7e).
				j := i + 2
				for j < len(buf) && (buf[j] < 0x40 || buf[j] > 0x7e) {
					j++
				}
				if j >= len(buf) {
					return buf[i:] // incomplete sequence
				}
				seq := string(buf[i : j+1])
				switch seq {
				case "\x1b[200~":
					s.inPaste = true
					s.log("paste_begin", "", 0)
				case "\x1b[201~":
					s.inPaste = false
					s.log("paste_end", s.composer, 0)
				default:
					s.log("csi", seq[2:], 0)
				}
				i = j + 1
				continue
			}
			// Bare Escape (next byte is not '['): dismiss picker/gate.
			s.log("key", "escape", 0)
			if s.gate != "" {
				s.gate = ""
			} else if s.pickerOpen {
				s.pickerOpen = false
			}
			i++
		case b == '\r' || b == '\n':
			s.log("key", "enter", 0)
			if s.gate != "" {
				s.gate = "" // any confirm dismisses the gate
				i++
				continue
			}
			if s.inPaste {
				s.composer += "\n" // literal newline inside a paste
			} else if s.pickerOpen {
				// The picker swallows Enter as a selection (GH#241 shape).
				s.pickerOpen = false
				s.log("picker_swallow_enter", "", 0)
			} else {
				s.submit()
			}
			i++
		case b == 0x15: // C-u clears the composer
			s.log("key", "c-u", 0)
			s.composer = ""
			i++
		case b == 0x7f || b == 0x08: // backspace
			if r := []rune(s.composer); len(r) > 0 {
				s.composer = string(r[:len(r)-1])
			}
			i++
		case b == 0x03: // C-c arrives as a byte in raw mode
			s.log("key", "c-c", 0)
			if s.working() {
				s.workUntil = time.Time{}
				if s.p.doneLine != "" {
					s.transcript = append(s.transcript, fmt.Sprintf(s.p.doneLine, int(time.Since(s.workStart).Seconds())))
				}
			}
			i++
		case b >= 0x20 || b == 0x09:
			// Printable (and UTF-8 continuation bytes fall in >=0x20 range
			// for the bytes we care about); accumulate raw and let strings
			// handle UTF-8 naturally by appending bytes.
			start := i
			for i < len(buf) && (buf[i] >= 0x20 || buf[i] == 0x09) && buf[i] != 0x7f {
				i++
			}
			s.composer += string(buf[start:i])
			s.log("composer_change", s.composer, 0)
			if s.gate != "" {
				s.gate = "" // typing answers/dismisses the gate too
			}
		default:
			i++ // ignore other control bytes
		}
	}
	return nil
}
