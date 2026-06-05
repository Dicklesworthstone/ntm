package ideation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultOptionalAdapterTimeout  = 1500 * time.Millisecond
	defaultOptionalOutputLimit     = 1 << 19
	defaultPerSignalSnippetBudget  = 256
	defaultPerSignalSummaryBudget  = 320
	defaultMaxSignalsPerSource     = 5
	defaultMaxOptionalSignals      = 25
	defaultHandoffScanLimit        = 24
	defaultAssuranceScanLimit      = 16
	defaultClosedHistoryLimit      = 8
	defaultCASSDays                = 30
	defaultMaxRedactedSegmentRunes = 64
)

const (
	// OptionalSourceCASS identifies cass-derived signals.
	OptionalSourceCASS = "cass"
	// OptionalSourceCM identifies cass-memory derived signals.
	OptionalSourceCM = "cm"
	// OptionalSourceHandoff identifies pane handoff summary signals.
	OptionalSourceHandoff = "handoff"
	// OptionalSourceAssurance identifies assurance/closeout digest signals.
	OptionalSourceAssurance = "assurance"
	// OptionalSourceClosedWorkHistory identifies compact br closed-history signals
	// derived from already-collected ExistingWork entries.
	OptionalSourceClosedWorkHistory = "closed_work_history"
)

// OptionalAdapterOptions tunes the optional signal adapters. Zero values fall
// back to conservative, privacy-preserving defaults.
type OptionalAdapterOptions struct {
	ProjectDir            string
	CommandTimeout        time.Duration
	OutputLimitBytes      int
	PerSignalSnippetBytes int
	PerSignalSummaryBytes int
	MaxSignalsPerSource   int
	MaxTotalSignals       int
	HandoffRoots          []string
	AssuranceRoots        []string
	CASSQueries           []string
	CASSDays              int
	CMQuery               string
	Now                   func() time.Time
}

// CollectOptional runs every optional adapter against snapshot. Each adapter
// degrades gracefully: missing tools and timeouts are recorded as warnings,
// never as failures.
func (collector Collector) CollectOptional(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	collector.CollectCASSSignals(ctx, snapshot, opts)
	collector.CollectCMSignals(ctx, snapshot, opts)
	collector.CollectHandoffSummaries(snapshot, opts)
	collector.CollectAssuranceDigests(snapshot, opts)
	collector.CollectClosedWorkHistorySignals(snapshot, opts)
	enforceOptionalSignalBudget(snapshot, opts.MaxTotalSignals)
	sortOptionalSignals(snapshot.OptionalSignals)
}

// CollectCASSSignals shells out to `cass search`. When cass is missing or the
// runner errors, a degraded source note is recorded — never a fatal error.
func (collector Collector) CollectCASSSignals(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	if !collector.lookPath("cass") {
		recordMissingOptionalTool(snapshot, "cass:search", SourceCASS, "cass not installed in PATH")
		return
	}
	for _, query := range opts.CASSQueries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		args := []string{"search", query, "--robot", "--limit", "5", "--fields", "minimal"}
		if opts.CASSDays > 0 {
			args = append(args, "--days", fmt.Sprintf("%d", opts.CASSDays))
		}
		collector.runCASSCommand(ctx, snapshot, opts, "cass:search", "cass_search", args, query)
	}
}

// CollectCMSignals shells out to `cm context "<query>" --json`. Missing cm is
// recorded as a degraded source rather than failing the run.
func (collector Collector) CollectCMSignals(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	if !collector.lookPath("cm") {
		recordMissingOptionalTool(snapshot, "cm:context", SourceCM, "cm not installed in PATH")
		return
	}
	query := strings.TrimSpace(opts.CMQuery)
	if query == "" {
		query = "queue-dry ideation"
	}
	output, err := collector.runOptionalCommand(ctx, opts, "cm", []string{"context", query, "--json", "--limit", "5"})
	source := CandidateSource{
		ID:        "cm:context",
		Kind:      SourceCM,
		Available: err == nil,
		Required:  false,
		Evidence:  []string{"cm context " + truncateForEvidence(query, 60)},
	}
	if err != nil {
		source.Error = err.Error()
		snapshot.RecordSource(source)
		return
	}
	signals := parseCMContextOutput(output, "cm:context", opts)
	if len(signals) == 0 {
		source.Evidence = append(source.Evidence, "cm context returned no compact bullets")
	}
	snapshot.RecordSource(source)
	snapshot.OptionalSignals = append(snapshot.OptionalSignals, signals...)
}

// CollectHandoffSummaries reads compact summary text from .ntm/handoffs and
// any caller-provided roots. Files are scanned non-recursively except for
// known nested layouts; no file content is exfiltrated raw.
func (collector Collector) CollectHandoffSummaries(snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	roots := append([]string{}, opts.HandoffRoots...)
	roots = append(roots, filepath.Join(opts.ProjectDir, ".ntm", "handoffs"))
	collected := scanFilesystemSignals(roots, opts.ProjectDir, opts.MaxSignalsPerSource, defaultHandoffScanLimit, "handoff_summary", "handoff:local", []string{".md", ".txt", ".json"}, opts)
	source := CandidateSource{
		ID:        "handoff:local",
		Kind:      SourceAgentMail,
		Available: true,
		Required:  false,
		Evidence:  []string{"local handoff summary scan"},
	}
	if len(collected) == 0 {
		source.Evidence = []string{"no local handoff summaries found"}
	}
	snapshot.RecordSource(source)
	snapshot.OptionalSignals = append(snapshot.OptionalSignals, collected...)
}

// CollectAssuranceDigests reads compact digest summaries from assurance and
// proof-bundle locations. Sensitive content is redacted before storage.
func (collector Collector) CollectAssuranceDigests(snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	roots := append([]string{}, opts.AssuranceRoots...)
	roots = append(roots,
		filepath.Join(opts.ProjectDir, "tests", "artifacts", "assurance"),
		filepath.Join(opts.ProjectDir, "internal", "robot", "assurance"),
		filepath.Join(opts.ProjectDir, "tests", "artifacts", "supportbundle"),
	)
	collected := scanFilesystemSignals(roots, opts.ProjectDir, opts.MaxSignalsPerSource, defaultAssuranceScanLimit, "assurance_digest", "assurance:local", []string{".md", ".json"}, opts)
	source := CandidateSource{
		ID:        "assurance:local",
		Kind:      SourceSupportBundle,
		Available: true,
		Required:  false,
		Evidence:  []string{"local assurance/closeout digest scan"},
	}
	if len(collected) == 0 {
		source.Evidence = []string{"no local assurance digests found"}
	}
	snapshot.RecordSource(source)
	snapshot.OptionalSignals = append(snapshot.OptionalSignals, collected...)
}

// CollectClosedWorkHistorySignals turns the recently-closed ExistingWork
// fingerprints already on the snapshot into compact, redacted history hints.
// It does not call any external tool — collectors that fed ExistingWork run
// their own degraded-source bookkeeping, so a degraded br:closed already
// surfaces upstream.
func (collector Collector) CollectClosedWorkHistorySignals(snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeOptionalAdapterOptions(opts)
	closed := closedExistingWork(snapshot.ExistingWork)
	limit := opts.MaxSignalsPerSource
	if limit > defaultClosedHistoryLimit {
		limit = defaultClosedHistoryLimit
	}
	if len(closed) > limit {
		closed = closed[:limit]
	}
	signals := make([]OptionalSignal, 0, len(closed))
	for _, work := range closed {
		signals = append(signals, OptionalSignal{
			ID:        "closed-history-" + normalizeIDPart(work.ID),
			SourceID:  "br:closed",
			Kind:      OptionalSourceClosedWorkHistory,
			Title:     truncateForEvidence(work.Title, 80),
			Summary:   redactAndTruncate(work.Summary, opts.PerSignalSummaryBytes),
			Tags:      stableStrings(append([]string{}, work.Labels...)),
			Timestamp: work.UpdatedAt,
			Evidence:  stableStrings([]string{"recently closed " + work.ID, "family " + work.FamilyID}),
		})
	}
	snapshot.OptionalSignals = append(snapshot.OptionalSignals, signals...)
}

func (collector Collector) runCASSCommand(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts OptionalAdapterOptions, sourceID, kind string, args []string, query string) {
	output, err := collector.runOptionalCommand(ctx, opts, "cass", args)
	source := CandidateSource{
		ID:        sourceID,
		Kind:      SourceCASS,
		Available: err == nil,
		Required:  false,
		Evidence:  []string{"cass " + args[0] + " " + truncateForEvidence(query, 60)},
	}
	if err != nil {
		source.Error = err.Error()
		snapshot.RecordSource(source)
		return
	}
	signals := parseCASSOutput(output, sourceID, kind, query, opts)
	if len(signals) == 0 {
		source.Evidence = append(source.Evidence, "cass returned no compact results")
	}
	snapshot.RecordSource(source)
	snapshot.OptionalSignals = append(snapshot.OptionalSignals, signals...)
}

func (collector Collector) runOptionalCommand(ctx context.Context, opts OptionalAdapterOptions, name string, args []string) ([]byte, error) {
	runner := collector.Runner
	if runner == nil {
		runner = ExecCommandRunner{OutputLimitBytes: opts.OutputLimitBytes}
	}
	commandCtx, cancel := context.WithTimeout(ctx, opts.CommandTimeout)
	defer cancel()
	output, err := runner.Run(commandCtx, opts.ProjectDir, name, args)
	if err != nil {
		return nil, err
	}
	if len(output) > opts.OutputLimitBytes {
		return nil, fmt.Errorf("%w: %s %s", ErrCommandOutputTooLarge, name, strings.Join(args, " "))
	}
	return output, nil
}

func (collector Collector) lookPath(name string) bool {
	if lookup, ok := collector.Runner.(interface {
		LookPath(string) bool
	}); ok {
		return lookup.LookPath(name)
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func parseCASSOutput(output []byte, sourceID, kind, query string, opts OptionalAdapterOptions) []OptionalSignal {
	if len(output) == 0 {
		return nil
	}
	var direct []cassResult
	if err := json.Unmarshal(output, &direct); err == nil {
		return cassResultsToSignals(direct, sourceID, kind, query, opts)
	}
	var wrapped struct {
		Results          []cassResult `json:"results"`
		HistorySnippets  []cassResult `json:"history_snippets"`
		HistorySnippets2 []cassResult `json:"historySnippets"`
		Hits             []cassResult `json:"hits"`
	}
	if err := json.Unmarshal(output, &wrapped); err != nil {
		return nil
	}
	all := wrapped.Results
	all = append(all, wrapped.HistorySnippets...)
	all = append(all, wrapped.HistorySnippets2...)
	all = append(all, wrapped.Hits...)
	return cassResultsToSignals(all, sourceID, kind, query, opts)
}

type cassResult struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Summary    string  `json:"summary"`
	Snippet    string  `json:"snippet"`
	Agent      string  `json:"agent"`
	Score      float64 `json:"score"`
	ModifiedAt string  `json:"modified_at"`
	UpdatedAt  string  `json:"updated_at"`
	Path       string  `json:"path"`
	SourcePath string  `json:"source_path"`
	LineNumber int     `json:"line_number"`
}

func cassResultsToSignals(results []cassResult, sourceID, kind, query string, opts OptionalAdapterOptions) []OptionalSignal {
	if len(results) == 0 {
		return nil
	}
	limit := opts.MaxSignalsPerSource
	if limit <= 0 {
		limit = defaultMaxSignalsPerSource
	}
	out := make([]OptionalSignal, 0, limit)
	for _, item := range results {
		if len(out) >= limit {
			break
		}
		id := strings.TrimSpace(item.ID)
		path := firstNonEmpty(item.Path, item.SourcePath)
		if id == "" && path != "" {
			id = path
			if item.LineNumber > 0 {
				id = fmt.Sprintf("%s:%d", path, item.LineNumber)
			}
		}
		if id == "" {
			id = fmt.Sprintf("%s-%d", kind, len(out))
		}
		title := truncateForEvidence(firstNonEmpty(item.Title, filepath.Base(path)), 80)
		summary := redactAndTruncate(firstNonEmpty(item.Summary, item.Snippet, path), opts.PerSignalSummaryBytes)
		snippet := redactAndTruncate(item.Snippet, opts.PerSignalSnippetBytes)
		evidence := []string{"cass " + kind + " for query " + truncateForEvidence(query, 40)}
		if item.Agent != "" {
			evidence = append(evidence, "agent="+truncateForEvidence(item.Agent, 24))
		}
		if item.Score != 0 {
			evidence = append(evidence, fmt.Sprintf("score=%.3f", item.Score))
		}
		out = append(out, OptionalSignal{
			ID:        "cass-" + normalizeIDPart(id),
			SourceID:  sourceID,
			Kind:      kind,
			Title:     title,
			Summary:   summary,
			Snippet:   snippet,
			Tags:      stableStrings([]string{kind}),
			Timestamp: firstNonEmpty(item.ModifiedAt, item.UpdatedAt),
			Evidence:  stableStrings(evidence),
		})
	}
	return out
}

func parseCMContextOutput(output []byte, sourceID string, opts OptionalAdapterOptions) []OptionalSignal {
	if len(output) == 0 {
		return nil
	}
	var wrapped struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(output, &wrapped); err == nil && len(wrapped.Data) > 0 {
		output = wrapped.Data
	}
	var parsed struct {
		RelevantBullets []struct {
			ID       string   `json:"id"`
			Category string   `json:"category"`
			Summary  string   `json:"summary"`
			Tags     []string `json:"tags"`
		} `json:"relevantBullets"`
		AntiPatterns []struct {
			ID       string `json:"id"`
			Category string `json:"category"`
			Summary  string `json:"summary"`
		} `json:"antiPatterns"`
		SuggestedQueries []string `json:"suggestedCassQueries"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return nil
	}
	limit := opts.MaxSignalsPerSource
	if limit <= 0 {
		limit = defaultMaxSignalsPerSource
	}
	out := make([]OptionalSignal, 0, limit)
	for _, bullet := range parsed.RelevantBullets {
		if len(out) >= limit {
			break
		}
		out = append(out, OptionalSignal{
			ID:       "cm-rule-" + normalizeIDPart(bullet.ID),
			SourceID: sourceID,
			Kind:     "cm_rule",
			Title:    truncateForEvidence(bullet.Category, 64),
			Summary:  redactAndTruncate(bullet.Summary, opts.PerSignalSummaryBytes),
			Tags:     stableStrings(append([]string{"cm_rule"}, bullet.Tags...)),
			Evidence: stableStrings([]string{"cm rule " + bullet.ID}),
		})
	}
	for _, anti := range parsed.AntiPatterns {
		if len(out) >= limit {
			break
		}
		out = append(out, OptionalSignal{
			ID:       "cm-anti-" + normalizeIDPart(anti.ID),
			SourceID: sourceID,
			Kind:     "cm_anti_pattern",
			Title:    truncateForEvidence(anti.Category, 64),
			Summary:  redactAndTruncate(anti.Summary, opts.PerSignalSummaryBytes),
			Tags:     stableStrings([]string{"cm_anti_pattern"}),
			Evidence: stableStrings([]string{"cm anti-pattern " + anti.ID}),
		})
	}
	for i, q := range parsed.SuggestedQueries {
		if len(out) >= limit {
			break
		}
		clean := truncateForEvidence(q, 80)
		if clean == "" {
			continue
		}
		out = append(out, OptionalSignal{
			ID:       fmt.Sprintf("cm-query-%d", i),
			SourceID: sourceID,
			Kind:     "cm_suggested_query",
			Title:    clean,
			Summary:  "",
			Tags:     stableStrings([]string{"cm_suggested_query"}),
			Evidence: stableStrings([]string{"cm suggested cass query"}),
		})
	}
	return out
}

func scanFilesystemSignals(roots []string, projectDir string, perSourceLimit, walkLimit int, kind, sourceID string, suffixes []string, opts OptionalAdapterOptions) []OptionalSignal {
	if perSourceLimit <= 0 {
		perSourceLimit = defaultMaxSignalsPerSource
	}
	out := make([]OptionalSignal, 0, perSourceLimit)
	scanned := 0
	for _, root := range roots {
		if root == "" || len(out) >= perSourceLimit {
			continue
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || len(out) >= perSourceLimit || scanned >= walkLimit {
				if scanned >= walkLimit {
					return filepath.SkipDir
				}
				return nil
			}
			scanned++
			if d.IsDir() {
				name := strings.ToLower(d.Name())
				switch name {
				case ".git", "node_modules", ".gomodcache":
					return filepath.SkipDir
				}
				return nil
			}
			lower := strings.ToLower(d.Name())
			if !hasSuffixAny(lower, suffixes) {
				return nil
			}
			rel, relErr := filepath.Rel(projectDir, path)
			if relErr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			data, readErr := readBoundedFile(path, opts.OutputLimitBytes)
			if readErr != nil {
				return nil
			}
			summary := redactAndTruncate(extractFirstParagraph(string(data)), opts.PerSignalSummaryBytes)
			snippet := redactAndTruncate(string(data), opts.PerSignalSnippetBytes)
			info, _ := d.Info()
			updated := ""
			if info != nil {
				updated = info.ModTime().UTC().Format(time.RFC3339)
			}
			out = append(out, OptionalSignal{
				ID:        kind + "-" + normalizeIDPart(rel),
				SourceID:  sourceID,
				Kind:      kind,
				Title:     truncateForEvidence(filepath.Base(rel), 80),
				Summary:   summary,
				Snippet:   snippet,
				Tags:      stableStrings([]string{kind}),
				Timestamp: updated,
				Evidence:  stableStrings([]string{rel}),
			})
			return nil
		})
	}
	return out
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = defaultOptionalOutputLimit
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, limit+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%w: %s", ErrCommandOutputTooLarge, path)
	}
	return buf[:n], nil
}

func extractFirstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if idx := strings.Index(text, "\n\n"); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	if idx := strings.Index(text, "\n"); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	return text
}

func hasSuffixAny(name string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func recordMissingOptionalTool(snapshot *IdeaEvidenceSnapshot, sourceID string, kind SourceKind, message string) {
	source := CandidateSource{
		ID:        sourceID,
		Kind:      kind,
		Available: false,
		Required:  false,
		Error:     message,
		Evidence:  []string{message},
	}
	snapshot.RecordSource(source)
}

func closedExistingWork(items []ExistingWorkFingerprint) []ExistingWorkFingerprint {
	out := make([]ExistingWorkFingerprint, 0, len(items))
	for _, item := range items {
		if item.Status == WorkStatusClosed {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sortOptionalSignals(items []OptionalSignal) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SourceID != items[j].SourceID {
			return items[i].SourceID < items[j].SourceID
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
}

func enforceOptionalSignalBudget(snapshot *IdeaEvidenceSnapshot, max int) {
	if snapshot == nil || max <= 0 {
		return
	}
	if len(snapshot.OptionalSignals) <= max {
		return
	}
	snapshot.OptionalSignals = snapshot.OptionalSignals[:max]
	snapshot.ValidationNotes = append(snapshot.ValidationNotes, ValidationNote{
		Code:     "optional_signals_truncated",
		Severity: ValidationWarning,
		Message:  fmt.Sprintf("optional signal output truncated at %d entries", max),
		Evidence: []string{fmt.Sprintf("max_total_signals=%d", max)},
	})
}

func normalizeOptionalAdapterOptions(opts OptionalAdapterOptions) OptionalAdapterOptions {
	if opts.ProjectDir == "" {
		wd, err := os.Getwd()
		if err == nil {
			opts.ProjectDir = wd
		}
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = defaultOptionalAdapterTimeout
	}
	if opts.OutputLimitBytes <= 0 {
		opts.OutputLimitBytes = defaultOptionalOutputLimit
	}
	if opts.PerSignalSnippetBytes <= 0 {
		opts.PerSignalSnippetBytes = defaultPerSignalSnippetBudget
	}
	if opts.PerSignalSummaryBytes <= 0 {
		opts.PerSignalSummaryBytes = defaultPerSignalSummaryBudget
	}
	if opts.MaxSignalsPerSource <= 0 {
		opts.MaxSignalsPerSource = defaultMaxSignalsPerSource
	}
	if opts.MaxTotalSignals <= 0 {
		opts.MaxTotalSignals = defaultMaxOptionalSignals
	}
	if opts.CASSDays <= 0 {
		opts.CASSDays = defaultCASSDays
	}
	return opts
}

var (
	redactEmailRegex      = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	redactBearerRegex     = regexp.MustCompile(`(?i)(?:bearer|authorization|token|api[_-]?key|secret)[\s:=]+[A-Za-z0-9+/=_\-]{8,}`)
	redactHexSecretRegex  = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
	redactAWSKeyRegex     = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	redactGenericKeyRegex = regexp.MustCompile(`\b[A-Za-z0-9+/=_\-]{40,}\b`)
	redactHomePathRegex   = regexp.MustCompile(`/(?:home|Users)/[^\s/"']+`)
	redactIPv4Regex       = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
)

// redactSensitive removes high-risk substrings (emails, secrets, keys,
// absolute home-directory paths, IPv4 addresses) from text before storage.
// It is intentionally conservative: when in doubt, redact.
func redactSensitive(text string) string {
	if text == "" {
		return ""
	}
	text = redactBearerRegex.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = redactAWSKeyRegex.ReplaceAllString(text, "[REDACTED_AWS_KEY]")
	text = redactEmailRegex.ReplaceAllString(text, "[REDACTED_EMAIL]")
	text = redactHomePathRegex.ReplaceAllString(text, "[REDACTED_PATH]")
	text = redactIPv4Regex.ReplaceAllString(text, "[REDACTED_IP]")
	text = redactHexSecretRegex.ReplaceAllString(text, "[REDACTED_HEX]")
	text = redactGenericKeyRegex.ReplaceAllString(text, "[REDACTED_KEY]")
	return text
}

func redactAndTruncate(text string, byteLimit int) string {
	text = redactSensitive(text)
	text = collapseWhitespace(text)
	if byteLimit <= 0 {
		byteLimit = defaultPerSignalSnippetBudget
	}
	if len(text) <= byteLimit {
		return text
	}
	if byteLimit < 4 {
		return text[:byteLimit]
	}
	return text[:byteLimit-1] + "…"
}

func truncateForEvidence(text string, runeLimit int) string {
	text = strings.TrimSpace(redactSensitive(text))
	if runeLimit <= 0 {
		runeLimit = defaultMaxRedactedSegmentRunes
	}
	runes := []rune(text)
	if len(runes) <= runeLimit {
		return text
	}
	if runeLimit < 2 {
		return string(runes[:runeLimit])
	}
	return string(runes[:runeLimit-1]) + "…"
}

func collapseWhitespace(text string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range text {
		switch r {
		case '\n', '\r', '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case ' ':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
