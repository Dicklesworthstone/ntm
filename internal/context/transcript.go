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
//
// Oh My Pi (omp) writes pi-family JSONL under
// ~/.omp/agent/sessions/<safe-path>/<timestamp>_<uuid>.jsonl (profile, XDG,
// and PI_CODING_AGENT_* overrides relocate the store). A padded "title" line
// precedes the "session" header, whose cwd identifies the working directory;
// assistant "message" entries carry message.usage (input, cacheRead,
// cacheWrite, output, totalTokens) with message.provider and message.model.
// Sub-agent transcripts live one directory deeper
// (<timestamp>_<uuid>/<AgentName>.jsonl) and are never attributed to a pane.
// omp also renders the same occupancy live in its composer border, which
// OmpStatusBarUsage reads without any cwd correlation.
package context

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
)

// TranscriptUsage is the last usage record extracted from an agent's own
// session transcript. Tokens is the effective context occupancy after the
// last turn (input + cache_read + cache_creation + output for Claude;
// last_token_usage.total_tokens for Codex; native total for Gemini).
type TranscriptUsage struct {
	Path                string    // transcript file the record came from
	ProviderSessionID   string    // native Gemini identity; empty for other formats
	Model               string    // model reported by the transcript, verbatim
	Tokens              int       // effective context occupancy (tokens)
	InputTokens         int       // uncached input tokens of the last turn
	CacheReadTokens     int       // cache read tokens of the last turn
	CacheCreationTokens int       // cache creation tokens of the last turn
	OutputTokens        int       // output tokens of the last turn
	ContextWindow       int       // model context window if the transcript reports one (Codex), else 0
	UpdatedAt           time.Time // native accounting timestamp, capped by the file snapshot; zero when unknown
}

// Start with a cheap tail, but allow large assistant/tool records between the
// latest accounting and EOF. Expansion reads only additional bytes and is
// capped independently of total transcript size. One extra byte establishes
// whether the first record crosses the scan boundary.
const (
	transcriptTailWindow = 256 * 1024
	transcriptScanLimit  = 8 * 1024 * 1024
)

// TranscriptFreshness is the maximum age of a transcript for its usage to be
// considered current (confidence "high").
const TranscriptFreshness = 10 * time.Minute

// transcriptEntry is a permissive union of the Claude, Codex, and omp line
// shapes.
type transcriptEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// Model is top-level on omp "model_change" lines.
	Model   string `json:"model"`
	Message struct {
		Role     string `json:"role"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Usage    *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			// omp (pi-family) spelling.
			Input       int `json:"input"`
			CacheRead   int `json:"cacheRead"`
			CacheWrite  int `json:"cacheWrite"`
			Output      int `json:"output"`
			TotalTokens int `json:"totalTokens"`
		} `json:"usage"`
	} `json:"message"`
	Payload struct {
		Type         string `json:"type"`
		Model        string `json:"model"`
		Cwd          string `json:"cwd"`
		ThreadSource string `json:"thread_source"`
		Info         *struct {
			LastTokenUsage *struct {
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

// ReadLatestTranscriptUsage returns the current provider-native usage record.
// Claude/Codex/omp use a bounded JSONL tail. Gemini JSON conversations and
// JSONL journals require bounded history replay to honor rewinds/replacements;
// unchanged snapshots are cached without retaining conversation content.
// Returns (nil, nil) when there is no attributable usage record.
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
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("transcript must be a regular file")
	}
	if usage, handled, err := readGeminiTranscriptUsage(f, info, path); handled || err != nil {
		return usage, err
	}

	usage, err := readNativeTranscriptTail(f, info.Size())
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("transcript changed during accounting read")
	}
	if usage == nil {
		return nil, nil
	}
	usage.Path = path
	// Progress and tool-output writes do not refresh an earlier token count.
	// Missing/invalid native timestamps stay unknown rather than borrowing
	// the mtime of unrelated data. Never claim evidence newer than this file.
	if usage.UpdatedAt.After(info.ModTime()) {
		usage.UpdatedAt = info.ModTime()
	}
	return usage, nil
}

// readNativeTranscriptTail searches complete JSONL records within a fixed
// snapshot. It neither chases appends nor rereads overlapping windows. A model
// declaration can precede a large tool result, so expand until both the latest
// usage and its preceding model are found, or the bounded history is exhausted.
func readNativeTranscriptTail(src io.ReaderAt, size int64) (*TranscriptUsage, error) {
	if size < 0 {
		return nil, fmt.Errorf("invalid transcript snapshot size")
	}
	var buf []byte
	for window := int64(transcriptTailWindow); ; window = min(window*2, transcriptScanLimit) {
		length := min(size, window+1)
		offset := size - length
		expanded := make([]byte, int(length))
		added := len(expanded) - len(buf)
		copy(expanded[added:], buf)
		if _, err := io.ReadFull(io.NewSectionReader(src, offset, int64(added)), expanded[:added]); err != nil {
			return nil, fmt.Errorf("read transcript snapshot: %w", err)
		}
		buf = expanded
		complete := buf
		if offset > 0 {
			// buf includes the byte immediately before the nominal window.
			// If it is a newline, the first complete record is preserved.
			if newline := bytes.IndexByte(complete, '\n'); newline >= 0 {
				complete = complete[newline+1:]
			} else {
				complete = nil
			}
		}
		usage, mayLookBack := parseLastUsage(complete)
		if !mayLookBack || (usage != nil && usage.Model != "") || offset == 0 || window == transcriptScanLimit {
			return usage, nil
		}
	}
}

// parseLastUsage scans JSONL content and returns the last usage-bearing
// record, with its preceding model. The second result permits looking farther
// back only when no corruption or history-rewrite barrier was encountered.
// Native compaction/branch records invalidate old accounting; their pre-counts
// and projected after-counts are not a new provider measurement.
func parseLastUsage(buf []byte) (*TranscriptUsage, bool) {
	var last *TranscriptUsage
	lastModel := ""
	mayLookBack := true
	invalidate := func() {
		last = nil
		lastModel = ""
		mayLookBack = false
	}

	for _, line := range bytes.Split(buf, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// Decode a minimal discriminator first. Valid unknown records may
		// use message/payload fields with other shapes and must not be
		// confused with accounting. String mentions of reset markers are
		// not barriers, while escaped JSON discriminator spellings still are.
		var header struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if line[0] != '{' || json.Unmarshal(line, &header) != nil {
			invalidate()
			continue
		}
		if (header.Type == "system" && header.Subtype == "compact_boundary") ||
			header.Type == "compacted" || header.Type == "compaction" || header.Type == "branch_summary" {
			invalidate()
			continue
		}
		switch header.Type {
		case "assistant", "message", "model_change", "turn_context":
		case "event_msg":
			var event struct {
				Payload struct {
					Type string `json:"type"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &event) != nil || event.Payload.Type == "thread_rolled_back" {
				invalidate()
				continue
			}
			if event.Payload.Type != "token_count" {
				continue
			}
		default:
			continue
		}

		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			invalidate()
			continue
		}

		if (e.Type == "assistant" || (e.Type == "message" && e.Message.Role == "assistant")) && e.Message.Model != "" {
			lastModel = ompQualifiedModel(e.Message.Provider, e.Message.Model)
		} else if e.Type == "turn_context" && e.Payload.Model != "" {
			lastModel = e.Payload.Model
		} else if e.Type == "model_change" && e.Model != "" {
			lastModel = e.Model // omp: already provider-qualified
		}

		switch {
		case e.Type == "message" && e.Message.Role == "assistant" && e.Message.Usage != nil:
			// omp shape: input excludes cache reads/writes; totalTokens is
			// their sum with output. Aborted turns record all-zero usage and
			// must not mask the previous real reading.
			u := e.Message.Usage
			total, valid := transcriptTokenTotal(u.TotalTokens, u.Input, u.CacheRead, u.CacheWrite, u.Output)
			if !valid {
				invalidate()
				continue
			}
			if total == 0 {
				continue
			}
			last = &TranscriptUsage{
				Model:               ompQualifiedModel(e.Message.Provider, e.Message.Model),
				Tokens:              total,
				InputTokens:         u.Input,
				CacheReadTokens:     u.CacheRead,
				CacheCreationTokens: u.CacheWrite,
				OutputTokens:        u.Output,
			}
		case e.Type == "assistant" && e.Message.Usage != nil:
			// Claude Code shape.
			u := e.Message.Usage
			total, valid := transcriptTokenTotal(0, u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens, u.OutputTokens)
			if !valid || (e.Message.Role != "" && e.Message.Role != "assistant") {
				invalidate()
				continue
			}
			if total == 0 {
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
		case e.Type == "event_msg" && e.Payload.Type == "token_count":
			// Codex shape: last_token_usage.input_tokens already includes
			// cached tokens; total_tokens = input + output.
			if e.Payload.Info == nil || e.Payload.Info.LastTokenUsage == nil {
				continue // Rate-limit-only token_count events carry no usage.
			}
			u := e.Payload.Info.LastTokenUsage
			total, valid := transcriptTokenTotal(u.TotalTokens, u.InputTokens, u.OutputTokens)
			if !valid || u.CachedInputTokens < 0 || u.CachedInputTokens > u.InputTokens ||
				u.CacheWriteInputTokens < 0 || e.Payload.Info.ModelContextWindow < 0 {
				invalidate()
				continue
			}
			if total == 0 {
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
		last.UpdatedAt, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
	}

	return last, mayLookBack
}

// transcriptTokenTotal validates native counters before using them for
// context pressure. Cache counters are passed only when they are additive for
// that provider. A native total may include extra provider-accounted tokens,
// but cannot be negative or smaller than the components we already know.
func transcriptTokenTotal(reported int, components ...int) (int, bool) {
	if reported < 0 {
		return 0, false
	}
	maxInt := int(^uint(0) >> 1)
	total := 0
	for _, component := range components {
		if component < 0 || component > maxInt-total {
			return 0, false
		}
		total += component
	}
	if reported > 0 {
		if reported < total {
			return 0, false
		}
		return reported, true
	}
	return total, true
}

// ompQualifiedModel joins omp's per-message provider and model into the
// "provider/model" spelling omp's --model flag and model_change entries use.
// Claude and Codex entries carry no provider and pass through unchanged.
func ompQualifiedModel(provider, model string) string {
	if provider == "" || model == "" || strings.HasPrefix(model, provider+"/") {
		return model
	}
	return provider + "/" + model
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
	// sessions/YYYY/MM/DD/*.jsonl. The tree is date-organized, so walk the
	// date directories NEWEST-FIRST and stop once the probe budget is full —
	// a full WalkDir over every historical rollout (thousands of stats) on a
	// polled snapshot path would saturate the disk for identical answers.
	var cands []cand
	descDirs := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		return names
	}
collect:
	for _, year := range descDirs(sessionsDir) {
		for _, month := range descDirs(filepath.Join(sessionsDir, year)) {
			for _, day := range descDirs(filepath.Join(sessionsDir, year, month)) {
				dayDir := filepath.Join(sessionsDir, year, month, day)
				entries, err := os.ReadDir(dayDir)
				if err != nil {
					continue
				}
				dayStart := len(cands)
				for _, e := range entries {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
						continue
					}
					fi, err := e.Info()
					if err != nil {
						continue
					}
					cands = append(cands, cand{path: filepath.Join(dayDir, e.Name()), mt: fi.ModTime()})
				}
				sort.Slice(cands[dayStart:], func(i, j int) bool {
					return cands[dayStart+i].mt.After(cands[dayStart+j].mt)
				})
				if len(cands) >= codexCwdProbeLimit {
					cands = cands[:codexCwdProbeLimit]
					break collect
				}
			}
		}
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
		// full base instructions). Fall back to substring checks for the
		// exact cwd JSON pair — still excluding subagent rollouts, which
		// share the main session's cwd but describe a different (usually
		// tiny) context.
		if bytes.Contains(head, []byte(`"thread_source":"subagent"`)) {
			return false
		}
		needle, merr := json.Marshal(cwd)
		if merr != nil {
			return false
		}
		return bytes.Contains(head, append([]byte(`"cwd":`), needle...))
	}
	if e.Type != "session_meta" {
		return false
	}
	// Subagent rollouts live in the same date tree with a matching cwd and
	// are often the newest files by mtime; attributing one to the main pane
	// reports the subagent's token count instead of the pane's.
	if e.Payload.ThreadSource == "subagent" {
		return false
	}
	return filepath.Clean(e.Payload.Cwd) == filepath.Clean(cwd)
}

// OmpStatusBarUsage converts the context gauge omp renders in its composer
// border ("6%" … "262K", or annotated "6.0%/262K") into a usage reading. It
// is the agent's own live accounting, attributed to the captured pane itself,
// so unlike transcript correlation it stays unambiguous when several omp
// panes share one working directory. Path and Model are empty; Tokens is
// derived from the percentage and window. It reports false when no gauge
// with a window is visible (for example while a selector overlay covers the
// composer).
func OmpStatusBarUsage(captured string, capturedAt time.Time) (*TranscriptUsage, bool) {
	usedPct, window, ok := agent.OmpContextUsage(captured)
	if !ok || window <= 0 {
		return nil, false
	}
	return &TranscriptUsage{
		Tokens:        int(float64(window)*usedPct/100 + 0.5),
		ContextWindow: int(window),
		UpdatedAt:     capturedAt,
	}, true
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
	case "gemini", "gmi":
		home, _ := os.UserHomeDir()
		ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 2*time.Second)
		defer cancel()
		path, ok = agentsession.FindGeminiTranscriptForCwd(ctx, home, cwd, newerThan)
	case "omp":
		// The omp store layout is owned by agentsession (shared with
		// session capture/resume discovery).
		home, _ := os.UserHomeDir()
		path, _ = agentsession.NewestOmpTranscriptForCwd(agentsession.OmpSessionRoots(home, os.Getenv), cwd, newerThan, nil)
		ok = path != ""
	default:
		// Other agent CLIs (cursor, ...) do not yet have known
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
	if (agentType == "gemini" || agentType == "gmi") && usage.ProviderSessionID == "" {
		return nil, false // A native Gemini path must contain Gemini accounting.
	}
	return usage, true
}

// ProcessTranscriptUsage reads usage from the transcript of the session the
// agent process under panePID is writing, found through the pane's process
// tree (agentsession.DiscoverProcessSession). That binding is exact per pane,
// so it holds where (agent type, cwd) correlation cannot attribute a
// transcript: several panes of one agent type sharing a directory, the normal
// NTM swarm layout. It reports false when there is no exact binding.
func ProcessTranscriptUsage(agentType string, panePID int) (*TranscriptUsage, bool) {
	info := agentsession.DiscoverProcessSession(agentType, panePID)
	return readBoundTranscriptUsage(info)
}

func readBoundTranscriptUsage(info *agentsession.Info) (*TranscriptUsage, bool) {
	if info == nil || info.SourcePath == "" {
		return nil, false
	}
	usage, err := ReadLatestTranscriptUsage(info.SourcePath)
	if err != nil || usage == nil {
		return nil, false
	}
	if info.Provider == "gemini" || usage.ProviderSessionID != "" {
		// Reopening a discovered pathname must not silently bind a replaced
		// file or another provider's transcript to this pane.
		if info.Provider != "gemini" || usage.ProviderSessionID == "" || usage.ProviderSessionID != info.SessionID {
			return nil, false
		}
	}
	return usage, true
}

// PaneTranscriptRef is one agent pane for AttributePaneTranscripts.
type PaneTranscriptRef struct {
	PaneID    string
	AgentType string // transcript agent type: "claude", "codex", "gemini", "omp"
	PID       int    // the pane's shell PID; 0 when unknown
	Cwd       string // the pane's working directory; "" when unknown
}

// AttributePaneTranscripts attributes session transcript usage to panes,
// keyed by pane ID. It is the one attribution rule every reader of pane
// transcripts shares (robot context and snapshot, the coordinator's rotation
// trigger).
//
// known holds readings already attributed to a pane by other means (omp's own
// context gauge). byProcess binds a pane through its agent process's session
// (exact per pane; may be nil). byCwd finds the newest transcript for an agent
// type in a directory, which is only trustworthy for a pane that is the sole
// pane of its type there: with several, it may belong to any of them, so those
// panes get no reading rather than another pane's. Panes resolved through
// known or byProcess still count toward their directory for that rule.
func AttributePaneTranscripts(panes []PaneTranscriptRef, known map[string]*TranscriptUsage, byProcess func(agentType string, pid int) (*TranscriptUsage, bool), byCwd func(agentType, cwd string) (*TranscriptUsage, bool)) map[string]*TranscriptUsage {
	type group struct{ agentType, cwd string }
	groups := make(map[group][]string)
	result := make(map[string]*TranscriptUsage, len(panes))
	for paneID, usage := range known {
		if usage != nil {
			result[paneID] = usage
		}
	}
	for _, pane := range panes {
		if result[pane.PaneID] == nil && byProcess != nil && pane.PID > 0 {
			if usage, ok := byProcess(pane.AgentType, pane.PID); ok && usage != nil {
				result[pane.PaneID] = usage
			}
		}
		if pane.Cwd != "" {
			key := group{agentType: pane.AgentType, cwd: pane.Cwd}
			groups[key] = append(groups[key], pane.PaneID)
		}
	}
	if byCwd == nil {
		return result
	}
	for key, paneIDs := range groups {
		if len(paneIDs) != 1 || result[paneIDs[0]] != nil {
			continue // ambiguous attribution: no transcript beats a wrong one
		}
		if usage, ok := byCwd(key.agentType, key.cwd); ok && usage != nil {
			result[paneIDs[0]] = usage
		}
	}
	return result
}

// TranscriptConfidence maps transcript freshness to a confidence label:
// "high" when the transcript was updated within TranscriptFreshness of now,
// "medium" otherwise (the session may have moved on or ended).
func TranscriptConfidence(updatedAt, now time.Time) string {
	if !updatedAt.IsZero() && !updatedAt.After(now) && now.Sub(updatedAt) < TranscriptFreshness {
		return "high"
	}
	return "medium"
}
