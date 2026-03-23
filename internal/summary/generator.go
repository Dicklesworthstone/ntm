package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// SummaryFormat controls the output format for session summaries.
type SummaryFormat string

const (
	FormatBrief    SummaryFormat = "brief"
	FormatDetailed SummaryFormat = "detailed"
	FormatHandoff  SummaryFormat = "handoff"
)

// FileAction constants for file changes.
const (
	FileActionCreated  = "created"
	FileActionModified = "modified"
	FileActionDeleted  = "deleted"
	FileActionRead     = "read"
	FileActionUnknown  = "unknown"
)

// AgentOutput contains per-agent output for summarization.
type AgentOutput struct {
	AgentID   string
	AgentType string
	Output    string
}

// FileChange describes a file touched during the session.
type FileChange struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Context string `json:"context,omitempty"`
}

// SessionSummary holds the structured summary output.
type SessionSummary struct {
	Session         string                    `json:"session"`
	GeneratedAt     time.Time                 `json:"generated_at"`
	Format          SummaryFormat             `json:"format"`
	Accomplishments []string                  `json:"accomplishments,omitempty"`
	Changes         []string                  `json:"changes,omitempty"`
	Files           []FileChange              `json:"files,omitempty"`
	Pending         []string                  `json:"pending,omitempty"`
	Errors          []string                  `json:"errors,omitempty"`
	Decisions       []string                  `json:"decisions,omitempty"`
	ThreadSummaries []agentmail.ThreadSummary `json:"thread_summaries,omitempty"`
	TokenEstimate   int                       `json:"token_estimate"`
	Text            string                    `json:"text"`
	Handoff         *handoff.Handoff          `json:"handoff,omitempty"`
}

// Summarizer is an optional LLM-backed summarization hook.
type Summarizer interface {
	Summarize(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// Options controls session summarization.
type Options struct {
	Session         string
	Outputs         []AgentOutput
	Format          SummaryFormat
	MaxTokens       int
	ProjectKey      string
	ProjectDir      string // Working directory for git operations
	IncludeGitDiff  bool   // Include git diff in summary
	ThreadIDs       []string
	AgentMailClient *agentmail.Client
	Summarizer      Summarizer
}

// SummarizeSession generates a session summary from agent outputs.
func SummarizeSession(ctx context.Context, opts Options) (*SessionSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(opts.Outputs) == 0 {
		return nil, errors.New("no outputs provided")
	}
	if opts.Format == "" {
		opts.Format = FormatBrief
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 1500
	}

	data := aggregateOutputs(opts.Outputs)

	// Enrich with git state if requested
	if opts.IncludeGitDiff && opts.ProjectDir != "" {
		gitFiles := getGitFileChanges(opts.ProjectDir)
		data.files = mergeFileChanges(data.files, gitFiles)
	}

	// Agent Mail thread summaries
	var threadSummaries []agentmail.ThreadSummary
	if opts.AgentMailClient != nil && len(opts.ThreadIDs) > 0 {
		if opts.ProjectKey == "" {
			data.errors = appendUnique(data.errors, "agent mail project key missing")
		} else {
			for _, tid := range opts.ThreadIDs {
				includeExamples := false
				thread, err := opts.AgentMailClient.SummarizeThread(ctx, agentmail.SummarizeThreadOptions{
					ProjectKey:      opts.ProjectKey,
					ThreadID:        tid,
					IncludeExamples: &includeExamples,
				})
				if err != nil {
					data.errors = appendUnique(data.errors, fmt.Sprintf("agent mail summarize %s: %v", tid, err))
					continue
				}
				threadSummaries = append(threadSummaries, thread.Summary)
				// Fold action items into pending list
				data.pending = appendUniqueList(data.pending, thread.Summary.ActionItems)
			}
		}
	}

	summary := &SessionSummary{
		Session:         opts.Session,
		GeneratedAt:     time.Now(),
		Format:          opts.Format,
		Accomplishments: data.accomplishments,
		Changes:         data.changes,
		Files:           data.files,
		Pending:         data.pending,
		Errors:          data.errors,
		Decisions:       data.decisions,
		ThreadSummaries: threadSummaries,
	}

	// Optional LLM summarization for brief/detailed formats
	if opts.Summarizer != nil && opts.Format != FormatHandoff {
		prompt := buildLLMPrompt(opts.Format, summary)
		if text, err := opts.Summarizer.Summarize(ctx, prompt, opts.MaxTokens); err == nil && strings.TrimSpace(text) != "" {
			summary.Text = strings.TrimSpace(text)
		}
	}

	if summary.Text == "" {
		// Deterministic formatting fallback
		switch opts.Format {
		case FormatDetailed:
			summary.Text = formatDetailed(summary)
		case FormatHandoff:
			fallback := buildHandoffSummary(summary)
			summary.Handoff = fallback
			summary.Text = formatHandoff(fallback)
		default:
			summary.Text = formatBrief(summary)
		}
	}

	summary.TokenEstimate = len(summary.Text) / 4
	if summary.TokenEstimate > opts.MaxTokens {
		summary.Text = truncateToTokens(summary.Text, opts.MaxTokens)
		summary.TokenEstimate = opts.MaxTokens
	}

	return summary, nil
}

// summaryData aggregates extracted data.
type summaryData struct {
	accomplishments []string
	changes         []string
	files           []FileChange
	pending         []string
	errors          []string
	decisions       []string
}

func aggregateOutputs(outputs []AgentOutput) summaryData {
	var data summaryData
	for _, out := range outputs {
		text := out.Output
		if strings.TrimSpace(text) == "" {
			continue
		}

		structured := parseStructuredJSON(text)
		data.accomplishments = appendUniqueList(data.accomplishments, structured.accomplishments)
		data.changes = appendUniqueList(data.changes, structured.changes)
		data.pending = appendUniqueList(data.pending, structured.pending)
		data.errors = appendUniqueList(data.errors, structured.errors)
		data.decisions = appendUniqueList(data.decisions, structured.decisions)
		data.files = mergeFileChanges(data.files, structured.files)

		sanitized := stripJSONBlocks(text)

		// Section-based parsing
		data.accomplishments = appendUniqueList(data.accomplishments, extractSectionItems(sanitized, accomplishmentHeaders))
		data.changes = appendUniqueList(data.changes, extractSectionItems(sanitized, changeHeaders))
		data.pending = appendUniqueList(data.pending, extractSectionItems(sanitized, pendingHeaders))
		data.errors = appendUniqueList(data.errors, extractSectionItems(sanitized, errorHeaders))
		data.decisions = appendUniqueList(data.decisions, extractSectionItems(sanitized, decisionHeaders))

		// Inline heuristics
		lines := strings.Split(sanitized, "\n")
		data.accomplishments = appendUniqueList(data.accomplishments, extractKeyActions(lines))
		data.changes = appendUniqueList(data.changes, extractChangeHighlights(lines))
		data.pending = appendUniqueList(data.pending, extractPendingInline(lines))
		data.errors = appendUniqueList(data.errors, extractErrorLines(lines))
		data.files = mergeFileChanges(data.files, extractFileChanges(lines))
	}
	return data
}

// Structured parsing

var (
	accomplishmentHeaders = []string{"accomplishments", "completed", "done", "what i did", "summary"}
	changeHeaders         = []string{"changes", "changes made", "updates", "modifications"}
	pendingHeaders        = []string{"pending", "next", "next steps", "todo", "remaining", "follow up", "follow-ups"}
	errorHeaders          = []string{"errors", "issues", "failures", "problems", "blockers"}
	decisionHeaders       = []string{"decisions", "key decisions"}
	pathLineRegexes       = []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[\s'"(,])((src|internal|pkg|cmd|lib|test|tests|spec|app|api|web|frontend|backend|client|server|utils|util|common|shared|core|modules|components|services|models|views|controllers|middleware|config|configs|scripts|tools|build|dist|bin|docs|examples|assets|resources|public|private|vendor|third_party|node_modules)\/[\w\-./]+\.[A-Za-z0-9]+)`),
		regexp.MustCompile(`(?:^|[\s'"(,])(\./[\w\-./]+\.[A-Za-z0-9]+)`),
		regexp.MustCompile(`(?:^|[\s'"(,])([\w\-./]+\.(?:go|py|js|ts|jsx|tsx|rs|rb|java|c|cpp|h|hpp|cs|php|swift|kt|scala|vue|svelte|md|txt|json|yaml|yml|toml|xml|html|css|scss|sass|less))(?:[\s'"\])\}:,]|$)`),
	}
)

func parseStructuredJSON(text string) summaryData {
	var data summaryData
	for _, raw := range extractJSONBlocks(text) {
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			continue
		}

		// Allow nesting under "summary"
		if inner, ok := obj["summary"].(map[string]interface{}); ok {
			obj = inner
		}

		data.accomplishments = appendUniqueList(data.accomplishments, extractStringSlice(obj, "accomplishments", "done", "completed"))
		data.changes = appendUniqueList(data.changes, extractStringSlice(obj, "changes", "updates"))
		data.pending = appendUniqueList(data.pending, extractStringSlice(obj, "pending", "next", "next_steps", "todo"))
		data.errors = appendUniqueList(data.errors, extractStringSlice(obj, "errors", "issues", "failures"))
		data.decisions = appendUniqueList(data.decisions, extractStringSlice(obj, "decisions"))

		// File changes from structured data
		data.files = mergeFileChanges(data.files, extractFileChangesFromJSON(obj))
	}
	return data
}

func extractStringSlice(obj map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		if val, ok := obj[key]; ok {
			return toStringSlice(val)
		}
	}
	return nil
}

func toStringSlice(val interface{}) []string {
	var out []string
	switch v := val.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range v {
			out = append(out, strings.TrimSpace(s))
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func extractFileChangesFromJSON(obj map[string]interface{}) []FileChange {
	var changes []FileChange

	// changes: [{path, action, context}]
	if rawChanges, ok := obj["changes"]; ok {
		switch v := rawChanges.(type) {
		case []interface{}:
			for _, item := range v {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				path, _ := m["path"].(string)
				action, _ := m["action"].(string)
				context, _ := m["context"].(string)
				if path == "" {
					continue
				}
				if action == "" {
					action = FileActionUnknown
				}
				changes = append(changes, FileChange{Path: path, Action: action, Context: strings.TrimSpace(context)})
			}
		}
	}

	// files: {created:[], modified:[], deleted:[]}
	if rawFiles, ok := obj["files"]; ok {
		if fm, ok := rawFiles.(map[string]interface{}); ok {
			changes = append(changes, fileChangesFromMap(fm)...)
		}
	}
	if rawFiles, ok := obj["file_changes"]; ok {
		if fm, ok := rawFiles.(map[string]interface{}); ok {
			changes = append(changes, fileChangesFromMap(fm)...)
		}
	}

	return changes
}

func fileChangesFromMap(fm map[string]interface{}) []FileChange {
	var changes []FileChange
	ordered := []struct {
		key    string
		action string
	}{
		{"created", FileActionCreated},
		{"modified", FileActionModified},
		{"deleted", FileActionDeleted},
		{"read", FileActionRead},
	}
	for _, entry := range ordered {
		if val, ok := fm[entry.key]; ok {
			for _, path := range toStringSlice(val) {
				if path == "" {
					continue
				}
				changes = append(changes, FileChange{Path: path, Action: entry.action})
			}
		}
	}
	return changes
}

// Section extraction

func extractSectionItems(text string, headers []string) []string {
	lines := strings.Split(text, "\n")
	var items []string
	capturing := false

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" && capturing {
			continue
		}

		if isHeader(line, headers) {
			capturing = true
			continue
		}

		if capturing && isAnyHeader(line) {
			capturing = false
		}
		if !capturing {
			continue
		}

		if item := parseBulletItem(line); item != "" {
			items = append(items, item)
		}
	}

	return items
}

func isHeader(line string, headers []string) bool {
	if line == "" {
		return false
	}
	clean := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
	clean = strings.TrimSpace(strings.TrimSuffix(clean, ":"))
	for _, h := range headers {
		if clean == h {
			return true
		}
	}
	return false
}

func isAnyHeader(line string) bool {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return true
	}
	return headerLineRegex.MatchString(line)
}

var headerLineRegex = regexp.MustCompile(`(?i)^(accomplishments|completed|done|summary|changes|changes made|updates|pending|next steps|next|todo|errors|issues|failures|blockers|decisions):?\s*$`)

func parseBulletItem(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}

	prefixes := []string{"- ", "* ", "• ", "1. ", "2. ", "3. ", "4. ", "5. ", "[x] ", "[ ] "}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, p))
			break
		}
	}

	// Skip plain headers or overly short noise
	if len(trimmed) < 3 {
		return ""
	}
	return trimmed
}

// Inline extraction

func extractPendingInline(lines []string) []string {
	var items []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		cleaned := cleanContextLine(trimmed)
		if cleaned == "" {
			continue
		}
		lower := strings.ToLower(cleaned)
		if strings.Contains(lower, "todo") || strings.HasPrefix(lower, "next:") || strings.HasPrefix(lower, "pending:") || strings.HasPrefix(lower, "remaining:") {
			item := strings.TrimSpace(strings.TrimPrefix(cleaned, "TODO"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "todo"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "Next:"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "next:"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "Pending:"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "pending:"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "Remaining:"))
			item = strings.TrimSpace(strings.TrimPrefix(item, "remaining:"))
			if item != "" {
				items = append(items, item)
			}
		}
	}
	return items
}

func extractErrorLines(lines []string) []string {
	var items []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") || strings.Contains(lower, "exception") {
			items = append(items, trimmed)
		}
	}
	return items
}

func extractKeyActions(lines []string) []string {
	var actions []string
	seen := make(map[string]bool)

	patterns := []string{"implemented", "fixed", "added", "created", "completed", "resolved"}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || len(trimmed) > 200 {
			continue
		}
		if isFileOnlyActionLine(trimmed) {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, p := range patterns {
			if strings.Contains(lower, p) {
				clean := cleanContextLine(trimmed)
				if clean != "" && !seen[clean] {
					seen[clean] = true
					actions = append(actions, clean)
				}
				break
			}
		}
	}
	return actions
}

func isFileOnlyActionLine(line string) bool {
	cleaned := cleanContextLine(line)
	if cleaned == "" {
		return false
	}
	paths := extractPathsFromLine(cleaned)
	if len(paths) == 0 {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(cleaned))
	lower = strings.TrimSuffix(lower, ".")
	lower = strings.TrimSuffix(lower, ",")

	verbs := []string{"created ", "modified ", "updated ", "deleted ", "added ", "renamed ", "removed "}
	for _, path := range paths {
		pathLower := strings.ToLower(path)
		for _, verb := range verbs {
			if lower == verb+pathLower {
				return true
			}
		}
	}
	return false
}

func extractChangeHighlights(lines []string) []string {
	var actions []string
	seen := make(map[string]bool)
	patterns := []string{"updated", "modified", "refactored", "changed", "rewrote", "renamed"}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || len(trimmed) > 200 {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, p := range patterns {
			if strings.Contains(lower, p) {
				clean := cleanContextLine(trimmed)
				if clean != "" && !seen[clean] {
					seen[clean] = true
					actions = append(actions, clean)
				}
				break
			}
		}
	}
	return actions
}

// File extraction helpers

func extractFileChanges(lines []string) []FileChange {
	var changes []FileChange
	seen := make(map[string]bool)

	for _, line := range lines {
		paths := extractPathsFromLine(line)
		if len(paths) == 0 {
			continue
		}
		for _, path := range paths {
			action := inferFileAction(line, path)
			key := action + ":" + path
			if seen[key] {
				continue
			}
			seen[key] = true
			changes = append(changes, FileChange{
				Path:    path,
				Action:  action,
				Context: cleanContextLine(line),
			})
		}
	}

	return changes
}

func extractPathsFromLine(line string) []string {
	var paths []string
	for _, re := range pathLineRegexes {
		matches := re.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) > 1 {
				path := strings.Trim(match[1], `"'`)
				if strings.Contains(path, "://") || strings.HasPrefix(path, "http") {
					continue
				}
				if len(path) < 3 {
					continue
				}
				paths = append(paths, path)
			}
		}
	}

	return paths
}

func inferFileAction(line, path string) string {
	lower := strings.ToLower(line)
	pathLower := strings.ToLower(path)

	if strings.Contains(lower, "created "+pathLower) || strings.Contains(lower, "creating "+pathLower) || strings.Contains(lower, "new file") {
		return FileActionCreated
	}
	if strings.Contains(lower, "modified "+pathLower) || strings.Contains(lower, "modifying "+pathLower) || strings.Contains(lower, "updated "+pathLower) || strings.Contains(lower, "editing "+pathLower) || strings.Contains(lower, "changed "+pathLower) {
		return FileActionModified
	}
	if strings.Contains(lower, "deleted "+pathLower) || strings.Contains(lower, "deleting "+pathLower) || strings.Contains(lower, "removed "+pathLower) {
		return FileActionDeleted
	}
	if strings.Contains(lower, "read "+pathLower) || strings.Contains(lower, "reading "+pathLower) || strings.Contains(lower, "opened "+pathLower) {
		return FileActionRead
	}

	if strings.Contains(lower, "creat") || strings.Contains(lower, "new") {
		return FileActionCreated
	}
	if strings.Contains(lower, "modif") || strings.Contains(lower, "edit") || strings.Contains(lower, "updat") || strings.Contains(lower, "chang") {
		return FileActionModified
	}
	if strings.Contains(lower, "delet") || strings.Contains(lower, "remov") {
		return FileActionDeleted
	}
	if strings.Contains(lower, "read") || strings.Contains(lower, "open") {
		return FileActionRead
	}

	return FileActionUnknown
}

func cleanContextLine(line string) string {
	trimmed := strings.TrimSpace(line)
	prefixes := []string{"- ", "* ", "• ", "1. ", "2. ", "3. ", "4. ", "5. ", "[x] ", "[ ] "}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, p))
			break
		}
	}
	if len(trimmed) > 120 {
		trimmed = util.SafeSlice(trimmed, 120) + "..."
	}
	return trimmed
}

func mergeFileChanges(existing, incoming []FileChange) []FileChange {
	seen := make(map[string]int)
	for i, fc := range existing {
		seen[fc.Action+":"+fc.Path] = i
	}
	for _, fc := range incoming {
		key := fc.Action + ":" + fc.Path
		if idx, ok := seen[key]; ok {
			if existing[idx].Context == "" && fc.Context != "" {
				existing[idx].Context = fc.Context
			}
			continue
		}
		seen[key] = len(existing)
		existing = append(existing, fc)
	}
	return existing
}

// JSON extraction helpers

func extractJSONBlocks(text string) []string {
	lines := strings.Split(text, "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if trimmed[0] == '{' || trimmed[0] == '[' {
			block, end := extractCompleteJSON(lines, i)
			if block != "" {
				blocks = append(blocks, block)
				i = end - 1
			}
		}
	}
	return blocks
}

func stripJSONBlocks(text string) string {
	blocks := extractJSONBlocks(text)
	if len(blocks) == 0 {
		return text
	}
	sanitized := text
	for _, block := range blocks {
		sanitized = strings.ReplaceAll(sanitized, block, "")
	}
	return sanitized
}

func extractCompleteJSON(lines []string, startIdx int) (string, int) {
	var builder strings.Builder
	depth := 0
	inString := false
	escaped := false

	for i := startIdx; i < len(lines) && i < startIdx+100; i++ {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(lines[i])

		for _, ch := range lines[i] {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' && inString {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			switch ch {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}

		if depth == 0 && builder.Len() > 0 {
			jsonStr := strings.TrimSpace(builder.String())
			if isValidJSON(jsonStr) {
				return jsonStr, i + 1
			}
			return "", 0
		}
		if depth < 0 {
			return "", 0
		}
	}
	return "", 0
}

func isValidJSON(s string) bool {
	var js interface{}
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(s)))
	dec.UseNumber()
	return dec.Decode(&js) == nil
}

// Formatting

func formatBrief(summary *SessionSummary) string {
	var sb strings.Builder
	name := summary.Session
	if name == "" {
		name = "(unknown)"
	}
	fmt.Fprintf(&sb, "Session %s summary\n", name)

	writeInlineList(&sb, "Accomplishments", summary.Accomplishments, 3)
	writeInlineList(&sb, "Changes", summary.Changes, 3)
	writeInlineFileList(&sb, summary.Files, 3)
	writeInlineList(&sb, "Pending", summary.Pending, 3)
	writeInlineList(&sb, "Errors", summary.Errors, 2)

	if len(summary.ThreadSummaries) > 0 {
		fmt.Fprintf(&sb, "Threads summarized: %d\n", len(summary.ThreadSummaries))
	}

	return strings.TrimSpace(sb.String())
}

func formatDetailed(summary *SessionSummary) string {
	var sb strings.Builder
	name := summary.Session
	if name == "" {
		name = "(unknown)"
	}
	fmt.Fprintf(&sb, "## Session Summary: %s\n\n", name)

	writeSectionList(&sb, "Accomplishments", summary.Accomplishments)
	writeSectionList(&sb, "Changes", summary.Changes)
	writeSectionFiles(&sb, "Files", summary.Files)
	writeSectionList(&sb, "Pending", summary.Pending)
	writeSectionList(&sb, "Errors", summary.Errors)
	writeSectionList(&sb, "Decisions", summary.Decisions)

	if len(summary.ThreadSummaries) > 0 {
		sb.WriteString("## Thread Summaries\n")
		for _, t := range summary.ThreadSummaries {
			fmt.Fprintf(&sb, "- %s\n", t.ThreadID)
			for _, kp := range t.KeyPoints {
				fmt.Fprintf(&sb, "  - %s\n", kp)
			}
			for _, ai := range t.ActionItems {
				fmt.Fprintf(&sb, "  - TODO: %s\n", ai)
			}
		}
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String())
}

func formatHandoff(h *handoff.Handoff) string {
	if h == nil {
		return ""
	}
	data, err := yamlMarshal(h)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// RenderSummary formats an existing summary using the requested format.
// It regenerates text from structured fields when possible.
func RenderSummary(summary *SessionSummary, format SummaryFormat) string {
	if summary == nil {
		return ""
	}
	if format == "" {
		format = summary.Format
	}
	switch format {
	case FormatDetailed:
		return strings.TrimSpace(formatDetailed(summary))
	case FormatHandoff:
		h := summary.Handoff
		if h == nil {
			h = buildHandoffSummary(summary)
		}
		return strings.TrimSpace(formatHandoff(h))
	case FormatBrief:
		return strings.TrimSpace(formatBrief(summary))
	default:
		if summary.Text != "" {
			return strings.TrimSpace(summary.Text)
		}
		return strings.TrimSpace(formatBrief(summary))
	}
}

func buildHandoffSummary(summary *SessionSummary) *handoff.Handoff {
	h := handoff.New(summary.Session)

	goal := firstNonEmpty(summary.Accomplishments)
	if goal == "" {
		goal = "Session summary"
	}
	now := firstNonEmpty(summary.Pending)
	if now == "" {
		now = "Review recent changes"
	}

	h.WithGoalAndNow(goal, now)
	for _, a := range summary.Accomplishments {
		h.AddTask(a)
	}
	for _, p := range summary.Pending {
		h.Next = appendUnique(h.Next, p)
	}
	for _, e := range summary.Errors {
		h.Blockers = appendUnique(h.Blockers, e)
	}

	for _, fc := range summary.Files {
		switch fc.Action {
		case FileActionCreated:
			h.MarkCreated(fc.Path)
		case FileActionDeleted:
			h.MarkDeleted(fc.Path)
		case FileActionRead:
			// Skip read-only for handoff
		default:
			h.MarkModified(fc.Path)
		}
	}

	if len(summary.Errors) > 0 {
		h.WithStatus(handoff.StatusBlocked, handoff.OutcomePartialMinus)
	} else if len(summary.Accomplishments) > 0 {
		h.WithStatus(handoff.StatusComplete, handoff.OutcomeSucceeded)
	} else {
		h.WithStatus(handoff.StatusPartial, handoff.OutcomePartialPlus)
	}

	return h
}

// LLM prompt builder

func buildLLMPrompt(format SummaryFormat, summary *SessionSummary) string {
	var sb strings.Builder
	sb.WriteString("Generate a concise session summary in format: ")
	sb.WriteString(string(format))
	sb.WriteString(". Use the provided data; do not invent details.\n\n")

	appendPromptList(&sb, "Accomplishments", summary.Accomplishments)
	appendPromptList(&sb, "Changes", summary.Changes)
	appendPromptFiles(&sb, "Files", summary.Files)
	appendPromptList(&sb, "Pending", summary.Pending)
	appendPromptList(&sb, "Errors", summary.Errors)
	appendPromptList(&sb, "Decisions", summary.Decisions)

	return sb.String()
}

// Formatting helpers

func writeInlineList(sb *strings.Builder, label string, items []string, limit int) {
	if len(items) == 0 {
		return
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	fmt.Fprintf(sb, "%s: %s\n", label, strings.Join(items, "; "))
}

func writeInlineFileList(sb *strings.Builder, files []FileChange, limit int) {
	if len(files) == 0 {
		return
	}
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	var parts []string
	for _, f := range files {
		parts = append(parts, fmt.Sprintf("%s %s", f.Action, f.Path))
	}
	fmt.Fprintf(sb, "Files: %s\n", strings.Join(parts, "; "))
}

func writeSectionList(sb *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(sb, "## %s\n", title)
	for _, item := range items {
		fmt.Fprintf(sb, "- %s\n", item)
	}
	sb.WriteString("\n")
}

func writeSectionFiles(sb *strings.Builder, title string, files []FileChange) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(sb, "## %s\n", title)
	for _, f := range files {
		line := fmt.Sprintf("- %s %s", f.Action, f.Path)
		if f.Context != "" {
			line = fmt.Sprintf("%s — %s", line, f.Context)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n")
}

func appendPromptList(sb *strings.Builder, label string, items []string) {
	if len(items) == 0 {
		return
	}
	sb.WriteString(label + ":\n")
	for _, item := range items {
		sb.WriteString("- " + item + "\n")
	}
	sb.WriteString("\n")
}

func appendPromptFiles(sb *strings.Builder, label string, files []FileChange) {
	if len(files) == 0 {
		return
	}
	sb.WriteString(label + ":\n")
	for _, f := range files {
		fmt.Fprintf(sb, "- %s %s\n", f.Action, f.Path)
	}
	sb.WriteString("\n")
}

func appendUnique(list []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return list
	}
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}

func appendUniqueList(list []string, items []string) []string {
	for _, item := range items {
		list = appendUnique(list, item)
	}
	return list
}

func firstNonEmpty(items []string) string {
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			return item
		}
	}
	return ""
}

// Token truncation

func truncateToTokens(text string, maxTokens int) string {
	maxChars := maxTokens * 4
	if len(text) <= maxChars {
		return text
	}
	truncated := util.SafeSlice(text, maxChars)
	lastPeriod := strings.LastIndex(truncated, ".")
	lastNewline := strings.LastIndex(truncated, "\n")

	cutPoint := len(truncated)
	if lastPeriod > cutPoint*3/4 {
		cutPoint = lastPeriod + 1
	} else if lastNewline > cutPoint*3/4 {
		cutPoint = lastNewline + 1
	}

	return text[:cutPoint] + "\n\n[Summary truncated due to token limit]"
}

// truncateAtRuneBoundary truncates a string to at most maxBytes bytes,
// ensuring the cut is at a valid UTF-8 rune boundary.
func truncateAtRuneBoundary(s string, maxBytes int) string {
	return util.SafeSlice(s, maxBytes)
}

// yamlMarshal is a small wrapper to avoid leaking yaml dependency in callers.
func yamlMarshal(h *handoff.Handoff) ([]byte, error) {
	return yaml.Marshal(h)
}

// getGitFileChanges retrieves file changes from git working directory.
// Returns modified, untracked, and deleted files.
func getGitFileChanges(projectDir string) []FileChange {
	var changes []FileChange

	// Get modified files from git diff
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = projectDir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, path := range parseLines(out.String()) {
			if path != "" {
				changes = append(changes, FileChange{
					Path:   path,
					Action: FileActionModified,
				})
			}
		}
	}

	// Get untracked files
	cmd = exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd.Dir = projectDir
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, path := range parseLines(out.String()) {
			if path != "" {
				changes = append(changes, FileChange{
					Path:   path,
					Action: FileActionCreated,
				})
			}
		}
	}

	// Get deleted files from git status
	cmd = exec.Command("git", "diff", "--name-only", "--diff-filter=D", "HEAD")
	cmd.Dir = projectDir
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, path := range parseLines(out.String()) {
			if path != "" {
				changes = append(changes, FileChange{
					Path:   path,
					Action: FileActionDeleted,
				})
			}
		}
	}

	return changes
}

// parseLines splits output by newlines and filters empty lines.
func parseLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
