// Package context: transcript.go reads ground-truth token usage from an
// agent's own session transcript instead of estimating from scrollback.
//
// Claude Code writes JSONL transcripts under
// ~/.claude/projects/<munged-project-path>/<session-uuid>.jsonl where each
// assistant entry carries message.usage (input_tokens,
// cache_read_input_tokens, cache_creation_input_tokens, output_tokens) and
// message.model. The last assistant entry's usage is the current context
// occupancy.
//
// Codex writes rollout JSONL under ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
// where event_msg entries with payload.type=="token_count" carry
// payload.info.last_token_usage and payload.info.model_context_window, and
// turn_context entries carry payload.model. The first line is a session_meta
// entry whose payload.cwd identifies the working directory.
package context

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TranscriptUsage is the last usage record extracted from an agent's own
// session transcript. Tokens is the effective context occupancy after the
// last turn (input + cache_read + cache_creation + output for Claude;
// last_token_usage.total_tokens for Codex).
type TranscriptUsage struct {
	Path                string    // transcript file the record came from
	Model               string    // model reported by the transcript, verbatim
	Tokens              int       // effective context occupancy (tokens)
	InputTokens         int       // uncached input tokens of the last turn
	CacheReadTokens     int       // cache read tokens of the last turn
	CacheCreationTokens int       // cache creation tokens of the last turn
	OutputTokens        int       // output tokens of the last turn
	ContextWindow       int       // model context window if the transcript reports one (Codex), else 0
	UpdatedAt           time.Time // transcript file mtime
}

// transcriptTailWindow bounds how much of a transcript is read. Transcripts
// can exceed 100MB; the last usage-bearing line is always near the end.
const transcriptTailWindow = 256 * 1024

// TranscriptFreshness is the maximum age of a transcript for its usage to be
// considered current (confidence "high").
const TranscriptFreshness = 10 * time.Minute

// transcriptEntry is a permissive union of the Claude and Codex line shapes.
type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Payload struct {
		Type  string `json:"type"`
		Model string `json:"model"`
		Cwd   string `json:"cwd"`
		Info  struct {
			LastTokenUsage struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				CacheWriteInputTokens int `json:"cache_write_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				TotalTokens           int `json:"total_tokens"`
			} `json:"last_token_usage"`
			ModelContextWindow int `json:"model_context_window"`
		} `json:"info"`
	} `json:"payload"`
}

// ReadLatestTranscriptUsage reads the tail of a JSONL transcript and returns
// the last usage-bearing record. It reads at most transcriptTailWindow bytes
// from the end of the file and tolerates a truncated first line in that
// window. Returns (nil, nil) when the tail contains no usage record.
func ReadLatestTranscriptUsage(path string) (*TranscriptUsage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	offset := int64(0)
	if info.Size() > transcriptTailWindow {
		offset = info.Size() - transcriptTailWindow
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, transcriptTailWindow))
	if err != nil {
		return nil, err
	}

	// If we started mid-file the first line is almost certainly partial:
	// drop everything through the first newline.
	if offset > 0 {
		if nl := bytes.IndexByte(buf, '\n'); nl >= 0 {
			buf = buf[nl+1:]
		} else {
			return nil, nil // window is a single partial line
		}
	}

	usage := parseLastUsage(buf)
	if usage == nil {
		return nil, nil
	}
	usage.Path = path
	usage.UpdatedAt = info.ModTime()
	return usage, nil
}

// parseLastUsage scans JSONL content and returns the last usage-bearing
// record, with the last seen model filled in for records that do not carry
// one themselves (Codex token_count events).
func parseLastUsage(buf []byte) *TranscriptUsage {
	var last *TranscriptUsage
	lastModel := ""

	for _, line := range bytes.Split(buf, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// Cheap pre-filter before full JSON decode.
		hasUsage := bytes.Contains(line, []byte(`"usage"`))
		hasTokenCount := bytes.Contains(line, []byte(`"token_count"`))
		hasModel := bytes.Contains(line, []byte(`"model"`))
		if !hasUsage && !hasTokenCount && !hasModel {
			continue
		}

		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // partial/corrupt line
		}

		if e.Message.Model != "" {
			lastModel = e.Message.Model
		} else if e.Payload.Model != "" {
			lastModel = e.Payload.Model
		}

		switch {
		case e.Type == "assistant" && hasUsage:
			// Claude Code shape.
			u := e.Message.Usage
			total := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
			if total <= 0 {
				continue
			}
			last = &TranscriptUsage{
				Model:               e.Message.Model,
				Tokens:              total,
				InputTokens:         u.InputTokens,
				CacheReadTokens:     u.CacheReadInputTokens,
				CacheCreationTokens: u.CacheCreationInputTokens,
				OutputTokens:        u.OutputTokens,
			}
		case e.Payload.Type == "token_count":
			// Codex shape: last_token_usage.input_tokens already includes
			// cached tokens; total_tokens = input + output.
			u := e.Payload.Info.LastTokenUsage
			total := u.TotalTokens
			if total <= 0 {
				total = u.InputTokens + u.OutputTokens
			}
			if total <= 0 {
				continue
			}
			last = &TranscriptUsage{
				Tokens:          total,
				InputTokens:     u.InputTokens - u.CachedInputTokens,
				CacheReadTokens: u.CachedInputTokens,
				OutputTokens:    u.OutputTokens,
				ContextWindow:   e.Payload.Info.ModelContextWindow,
			}
		default:
			continue
		}
		if last.Model == "" {
			last.Model = lastModel
		}
	}

	if last != nil && last.Model == "" {
		last.Model = lastModel
	}
	return last
}

// MungeProjectPath converts a working directory into the directory name
// Claude Code uses under ~/.claude/projects: every character that is not an
// ASCII letter or digit becomes '-' (e.g. /Users/x/proj -> -Users-x-proj).
func MungeProjectPath(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// DefaultClaudeProjectsDir returns ~/.claude/projects, or "" if the home
// directory cannot be determined.
func DefaultClaudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// DefaultCodexSessionsDir returns ~/.codex/sessions, or "" if the home
// directory cannot be determined.
func DefaultCodexSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// FindClaudeTranscript locates the most likely transcript for a pane working
// in cwd: the most recently modified *.jsonl in the munged project directory
// whose mtime is after newerThan, falling back to the newest overall. This is
// a heuristic: multiple sessions in the same project directory cannot be told
// apart without deeper correlation.
func FindClaudeTranscript(projectsDir, cwd string, newerThan time.Time) (string, bool) {
	if projectsDir == "" || cwd == "" {
		return "", false
	}
	dir := filepath.Join(projectsDir, MungeProjectPath(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}

	var newest, newestFresh string
	var newestT, newestFreshT time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		mt := fi.ModTime()
		if mt.After(newestT) {
			newest, newestT = filepath.Join(dir, e.Name()), mt
		}
		if mt.After(newerThan) && mt.After(newestFreshT) {
			newestFresh, newestFreshT = filepath.Join(dir, e.Name()), mt
		}
	}
	if newestFresh != "" {
		return newestFresh, true
	}
	if newest != "" {
		return newest, true
	}
	return "", false
}

// codexCwdProbeLimit bounds how many candidate rollout files are opened to
// check their session_meta cwd.
const codexCwdProbeLimit = 40

// FindCodexTranscript locates the most likely Codex rollout transcript for a
// pane working in cwd. Rollout files are grouped by date, not project, so the
// newest files (bounded probe) are opened and their session_meta cwd compared.
func FindCodexTranscript(sessionsDir, cwd string, newerThan time.Time) (string, bool) {
	if sessionsDir == "" || cwd == "" {
		return "", false
	}
	type cand struct {
		path string
		mt   time.Time
	}
	var cands []cand
	// sessions/YYYY/MM/DD/*.jsonl
	_ = filepath.WalkDir(sessionsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		cands = append(cands, cand{path: path, mt: fi.ModTime()})
		return nil
	})
	sort.Slice(cands, func(i, j int) bool { return cands[i].mt.After(cands[j].mt) })
	if len(cands) > codexCwdProbeLimit {
		cands = cands[:codexCwdProbeLimit]
	}

	var fallback string
	for _, c := range cands {
		if !codexSessionMatchesCwd(c.path, cwd) {
			continue
		}
		if c.mt.After(newerThan) {
			return c.path, true // cands sorted newest-first
		}
		if fallback == "" {
			fallback = c.path
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// codexSessionMatchesCwd reads the head of a rollout file and reports whether
// its session_meta cwd equals cwd.
func codexSessionMatchesCwd(path, cwd string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// session_meta is the first line but carries large base_instructions;
	// read a bounded head and parse the first line only.
	head := make([]byte, 128*1024)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if nl := bytes.IndexByte(head, '\n'); nl >= 0 {
		head = head[:nl]
	}
	var e transcriptEntry
	if err := json.Unmarshal(head, &e); err != nil {
		// The session_meta line can exceed the probe window (it embeds the
		// full base instructions). Fall back to a substring check for the
		// exact cwd JSON pair.
		needle, merr := json.Marshal(cwd)
		if merr != nil {
			return false
		}
		return bytes.Contains(head, append([]byte(`"cwd":`), needle...))
	}
	return e.Type == "session_meta" && e.Payload.Cwd == cwd
}

// LatestAgentTranscriptUsage finds the transcript for an agent pane by agent
// type and working directory and returns its last usage record. agentType is
// NTM's normalized agent type string ("claude", "codex", ...). newerThan
// filters transcripts by mtime (zero time accepts any). Returns (nil, false)
// when no transcript is found or it has no usage records.
func LatestAgentTranscriptUsage(agentType, cwd string, newerThan time.Time) (*TranscriptUsage, bool) {
	var path string
	var ok bool
	switch agentType {
	case "claude", "cc":
		path, ok = FindClaudeTranscript(DefaultClaudeProjectsDir(), cwd, newerThan)
	case "codex", "cod":
		path, ok = FindCodexTranscript(DefaultCodexSessionsDir(), cwd, newerThan)
	default:
		// Seam: other agent CLIs (gemini, cursor, ...) do not yet have known
		// transcript locations; fall back to scrollback estimation.
		return nil, false
	}
	if !ok {
		return nil, false
	}
	usage, err := ReadLatestTranscriptUsage(path)
	if err != nil || usage == nil {
		return nil, false
	}
	return usage, true
}

// TranscriptConfidence maps transcript freshness to a confidence label:
// "high" when the transcript was updated within TranscriptFreshness of now,
// "medium" otherwise (the session may have moved on or ended).
func TranscriptConfidence(updatedAt, now time.Time) string {
	if now.Sub(updatedAt) < TranscriptFreshness {
		return "high"
	}
	return "medium"
}
