package ideation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCollectorTimeout     = 2 * time.Second
	defaultCollectorOutputLimit = 1 << 20
	defaultRecentClosedLimit    = 80
	defaultGitCommitLimit       = 8
	defaultSupportBundleLimit   = 20
)

var ErrCommandOutputTooLarge = errors.New("command output exceeded collector limit")

type CommandRunner interface {
	Run(ctx context.Context, workdir string, name string, args []string) ([]byte, error)
}

type ExecCommandRunner struct {
	OutputLimitBytes int
}

type CollectorOptions struct {
	ProjectDir         string
	CommandTimeout     time.Duration
	OutputLimitBytes   int
	RecentClosedLimit  int
	GitCommitLimit     int
	SupportBundleLimit int
}

type Collector struct {
	Runner CommandRunner
}

func CollectLocalEvidence(ctx context.Context, opts CollectorOptions) IdeaEvidenceSnapshot {
	return Collector{}.Collect(ctx, opts)
}

func (collector Collector) Collect(ctx context.Context, opts CollectorOptions) IdeaEvidenceSnapshot {
	opts = normalizeCollectorOptions(opts)
	snapshot := NewIdeaEvidenceSnapshot(opts.ProjectDir)
	collector.CollectBR(ctx, &snapshot, opts)
	collector.CollectBV(ctx, &snapshot, opts)
	collector.CollectProjectDocs(&snapshot, opts)
	collector.CollectGit(ctx, &snapshot, opts)
	collector.CollectSupportBundles(&snapshot, opts)
	return snapshot
}

func (collector Collector) CollectBR(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts CollectorOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeCollectorOptions(opts)
	ready := collector.collectBRIssues(ctx, opts, "br:ready", "ready", []string{"ready", "--json", "--no-auto-flush", "--no-auto-import"})
	open := collector.collectBRIssues(ctx, opts, "br:open", "open", []string{"list", "--status", "open", "--limit", "0", "--json", "--no-auto-flush", "--no-auto-import"})
	inProgress := collector.collectBRIssues(ctx, opts, "br:in_progress", "in_progress", []string{"list", "--status", "in_progress", "--limit", "0", "--json", "--no-auto-flush", "--no-auto-import"})
	closed := collector.collectBRIssues(ctx, opts, "br:closed", "closed", []string{"list", "--status", "closed", "--all", "--limit", strconv.Itoa(opts.RecentClosedLimit), "--sort", "updated_at", "--reverse", "--json", "--no-auto-flush", "--no-auto-import"})

	for _, group := range []struct {
		sourceID string
		issues   []brIssue
		err      error
	}{
		{"br:ready", ready.issues, ready.err},
		{"br:open", open.issues, open.err},
		{"br:in_progress", inProgress.issues, inProgress.err},
		{"br:closed", closed.issues, closed.err},
	} {
		source := CandidateSource{
			ID:        group.sourceID,
			Kind:      SourceBR,
			Available: group.err == nil,
			Required:  group.sourceID != "br:closed",
			Evidence:  []string{"br " + strings.TrimPrefix(group.sourceID, "br:") + " collector"},
		}
		if group.err != nil {
			source.Error = group.err.Error()
		}
		snapshot.RecordSource(source)
	}

	if ready.err == nil {
		snapshot.Queue.ReadyCount = len(ready.issues)
		snapshot.Queue.ActionableCount = len(ready.issues)
	}
	if open.err == nil {
		snapshot.Queue.OpenCount = len(open.issues)
		snapshot.ExistingWork = append(snapshot.ExistingWork, brIssuesToWork(open.issues, WorkStatusOpen, "br:open")...)
	}
	if inProgress.err == nil {
		snapshot.Queue.InProgressCount = len(inProgress.issues)
		snapshot.ExistingWork = append(snapshot.ExistingWork, brIssuesToWork(inProgress.issues, WorkStatusInProgress, "br:in_progress")...)
	}
	if closed.err == nil {
		snapshot.ExistingWork = append(snapshot.ExistingWork, brIssuesToWork(closed.issues, WorkStatusClosed, "br:closed")...)
	}
	if ready.err == nil || open.err == nil || inProgress.err == nil {
		snapshot.Queue.CountsVerified = true
	}
	if snapshot.Queue.BlockedCount == 0 && snapshot.Queue.OpenCount > snapshot.Queue.ReadyCount+snapshot.Queue.InProgressCount {
		snapshot.Queue.BlockedCount = snapshot.Queue.OpenCount - snapshot.Queue.ReadyCount - snapshot.Queue.InProgressCount
	}
	snapshot.Queue.SourceIDs = stableStrings(append(snapshot.Queue.SourceIDs, "br:ready", "br:open", "br:in_progress", "br:closed"))
}

func (collector Collector) CollectBV(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts CollectorOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeCollectorOptions(opts)
	output, err := collector.runCommand(ctx, opts, "bv", []string{"--robot-triage"})
	source := CandidateSource{
		ID:        "bv:triage",
		Kind:      SourceBV,
		Available: err == nil,
		Required:  true,
		Evidence:  []string{"bv --robot-triage"},
	}
	if err != nil {
		source.Error = err.Error()
		snapshot.RecordSource(source)
		snapshot.Triage.Warnings = stableStrings(append(snapshot.Triage.Warnings, err.Error()))
		return
	}
	snapshot.RecordSource(source)

	var triage bvTriageRaw
	if err := json.Unmarshal(output, &triage); err != nil {
		recordCollectorError(snapshot, CandidateSource{ID: "bv:triage", Kind: SourceBV, Required: true, Evidence: []string{"bv --robot-triage"}}, fmt.Errorf("parse bv triage: %w", err))
		return
	}
	qr := triage.Triage.QuickRef
	snapshot.Queue.OpenCount = qr.OpenCount
	snapshot.Queue.ActionableCount = qr.ActionableCount
	snapshot.Queue.ReadyCount = qr.ActionableCount
	snapshot.Queue.BlockedCount = qr.BlockedCount
	snapshot.Queue.InProgressCount = qr.InProgressCount
	snapshot.Queue.CountsVerified = true
	for _, pick := range qr.TopPicks {
		snapshot.Triage.TopIDs = append(snapshot.Triage.TopIDs, pick.ID)
	}
	if len(snapshot.Triage.TopIDs) == 0 {
		for _, rec := range triage.Triage.Recommendations {
			snapshot.Triage.TopIDs = append(snapshot.Triage.TopIDs, rec.ID)
			if len(snapshot.Triage.TopIDs) >= 3 {
				break
			}
		}
	}
	snapshot.Triage.TopIDs = stableStrings(snapshot.Triage.TopIDs)
	snapshot.Triage.GraphHealth = graphHealthFromRaw(triage.Triage.ProjectHealth)
	snapshot.Triage.SourceIDs = stableStrings(append(snapshot.Triage.SourceIDs, "bv:triage"))
}

func (collector Collector) CollectProjectDocs(snapshot *IdeaEvidenceSnapshot, opts CollectorOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeCollectorOptions(opts)
	for _, name := range []string{"AGENTS.md", "README.md"} {
		marker := collectDocMarker(opts.ProjectDir, name)
		snapshot.Documents = append(snapshot.Documents, marker)
		source := CandidateSource{
			ID:        "doc:" + name,
			Kind:      SourceProjectDoc,
			Available: marker.Exists,
			Required:  true,
			Evidence:  marker.Evidence,
		}
		if !marker.Exists {
			source.Error = name + " missing"
		}
		snapshot.RecordSource(source)
	}
}

func (collector Collector) CollectGit(ctx context.Context, snapshot *IdeaEvidenceSnapshot, opts CollectorOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeCollectorOptions(opts)
	output, err := collector.runCommand(ctx, opts, "git", []string{"log", "-n", strconv.Itoa(opts.GitCommitLimit), "--name-only", "--pretty=format:__NTM_COMMIT__%x00%H%x00%s"})
	source := CandidateSource{
		ID:        "git:recent",
		Kind:      SourceGit,
		Available: err == nil,
		Required:  false,
		Evidence:  []string{"git recent commit/path collector"},
	}
	if err != nil {
		source.Error = err.Error()
		snapshot.RecordSource(source)
		return
	}
	snapshot.RecordSource(source)
	snapshot.Git = parseGitLog(output, "git:recent")
}

func (collector Collector) CollectSupportBundles(snapshot *IdeaEvidenceSnapshot, opts CollectorOptions) {
	if snapshot == nil {
		return
	}
	opts = normalizeCollectorOptions(opts)
	proofs := collectSupportBundleEvidence(opts.ProjectDir, opts.SupportBundleLimit)
	source := CandidateSource{
		ID:        "supportbundle:local",
		Kind:      SourceSupportBundle,
		Available: true,
		Required:  false,
		Evidence:  []string{"local supportbundle/closeout artifact scan"},
	}
	if len(proofs) == 0 {
		source.Evidence = []string{"no local supportbundle/closeout artifacts found"}
	}
	snapshot.RecordSource(source)
	snapshot.CloseoutProof = append(snapshot.CloseoutProof, proofs...)
}

func (collector Collector) collectBRIssues(ctx context.Context, opts CollectorOptions, sourceID, label string, args []string) brIssuesResult {
	output, err := collector.runCommand(ctx, opts, "br", args)
	if err != nil {
		return brIssuesResult{err: fmt.Errorf("%s: %w", sourceID, err)}
	}
	issues, err := parseBRIssues(output)
	if err != nil {
		return brIssuesResult{err: fmt.Errorf("%s parse: %w", sourceID, err)}
	}
	for i := range issues {
		issues[i].collectorLabel = label
	}
	return brIssuesResult{issues: issues}
}

func (collector Collector) runCommand(ctx context.Context, opts CollectorOptions, name string, args []string) ([]byte, error) {
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

func (runner ExecCommandRunner) Run(ctx context.Context, workdir string, name string, args []string) ([]byte, error) {
	limit := runner.OutputLimitBytes
	if limit <= 0 {
		limit = defaultCollectorOutputLimit
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = limit
	stderr.limit = limit / 4
	if stderr.limit < 1024 {
		stderr.limit = 1024
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.truncated {
		return stdout.buf.Bytes(), fmt.Errorf("%w: %s", ErrCommandOutputTooLarge, name)
	}
	if err != nil {
		stderrText := strings.TrimSpace(stderr.buf.String())
		if stderrText == "" {
			return stdout.buf.Bytes(), err
		}
		return stdout.buf.Bytes(), fmt.Errorf("%w: %s", err, stderrText)
	}
	return stdout.buf.Bytes(), nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

type brIssuesResult struct {
	issues []brIssue
	err    error
}

type brIssue struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Status         string   `json:"status"`
	Priority       int      `json:"priority"`
	IssueType      string   `json:"issue_type"`
	Labels         []string `json:"labels"`
	UpdatedAt      string   `json:"updated_at"`
	ClosedAt       string   `json:"closed_at"`
	collectorLabel string
}

func parseBRIssues(output []byte) ([]brIssue, error) {
	var direct []brIssue
	if err := json.Unmarshal(output, &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Issues []brIssue `json:"issues"`
	}
	if err := json.Unmarshal(output, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Issues, nil
}

func brIssuesToWork(issues []brIssue, fallbackStatus WorkStatus, sourceID string) []ExistingWorkFingerprint {
	out := make([]ExistingWorkFingerprint, 0, len(issues))
	for _, issue := range issues {
		status := WorkStatus(strings.TrimSpace(issue.Status))
		if status == "" {
			status = fallbackStatus
		}
		work := ExistingWorkFingerprint{
			ID:        issue.ID,
			FamilyID:  beadFamily(issue.ID),
			Title:     issue.Title,
			Summary:   issue.Description,
			Status:    status,
			Labels:    stableStrings(issue.Labels),
			Keywords:  tokenSlice(issue.Title + " " + issue.Description + " " + strings.Join(issue.Labels, " ")),
			SourceIDs: []string{sourceID},
			Evidence:  []string{"br " + issue.collectorLabel + " issue " + issue.ID},
			UpdatedAt: issue.UpdatedAt,
		}
		if IsKnownClosedIdeaWizardFamily(work.FamilyID) {
			work.Evidence = append(work.Evidence, "known closed idea-wizard family "+work.FamilyID)
		}
		work.Evidence = stableStrings(work.Evidence)
		out = append(out, work)
	}
	return out
}

type bvTriageRaw struct {
	Triage struct {
		QuickRef struct {
			OpenCount       int `json:"open_count"`
			ActionableCount int `json:"actionable_count"`
			BlockedCount    int `json:"blocked_count"`
			InProgressCount int `json:"in_progress_count"`
			TopPicks        []struct {
				ID string `json:"id"`
			} `json:"top_picks"`
		} `json:"quick_ref"`
		Recommendations []struct {
			ID string `json:"id"`
		} `json:"recommendations"`
		ProjectHealth json.RawMessage `json:"project_health"`
	} `json:"triage"`
}

func graphHealthFromRaw(raw json.RawMessage) GraphHealth {
	health := GraphHealth{Metrics: map[string]string{}, Evidence: []string{}}
	if len(raw) == 0 || string(raw) == "null" {
		return health
	}
	var parsed struct {
		Graph struct {
			NodeCount   int     `json:"node_count"`
			EdgeCount   int     `json:"edge_count"`
			Density     float64 `json:"density"`
			HasCycles   bool    `json:"has_cycles"`
			Phase2Ready bool    `json:"phase2_ready"`
		} `json:"graph"`
		GraphMetrics struct {
			TotalNodes int     `json:"total_nodes"`
			TotalEdges int     `json:"total_edges"`
			Density    float64 `json:"density"`
			CycleCount int     `json:"cycle_count"`
		} `json:"graph_metrics"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		health.Evidence = []string{"project_health parse failed: " + err.Error()}
		return health
	}
	if parsed.Graph.NodeCount != 0 || parsed.Graph.EdgeCount != 0 {
		health.TotalNodes = parsed.Graph.NodeCount
		health.TotalEdges = parsed.Graph.EdgeCount
		health.Density = parsed.Graph.Density
		health.Metrics["has_cycles"] = strconv.FormatBool(parsed.Graph.HasCycles)
		health.Metrics["phase2_ready"] = strconv.FormatBool(parsed.Graph.Phase2Ready)
	}
	if parsed.GraphMetrics.TotalNodes != 0 || parsed.GraphMetrics.TotalEdges != 0 {
		health.TotalNodes = parsed.GraphMetrics.TotalNodes
		health.TotalEdges = parsed.GraphMetrics.TotalEdges
		health.Density = parsed.GraphMetrics.Density
		health.Metrics["cycle_count"] = strconv.Itoa(parsed.GraphMetrics.CycleCount)
	}
	if health.TotalNodes > 0 || health.TotalEdges > 0 {
		health.Evidence = append(health.Evidence, fmt.Sprintf("graph nodes=%d edges=%d density=%.6f", health.TotalNodes, health.TotalEdges, health.Density))
	}
	health.Evidence = stableStrings(health.Evidence)
	return health
}

func collectDocMarker(projectDir, name string) ProjectDocumentMarker {
	path := filepath.Join(projectDir, name)
	marker := ProjectDocumentMarker{
		Path:     name,
		Exists:   false,
		Evidence: []string{},
		SourceID: "doc:" + name,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		marker.Evidence = []string{name + " missing or unreadable"}
		return marker
	}
	info, err := os.Stat(path)
	if err == nil {
		marker.UpdatedAt = info.ModTime().UTC().Format(time.RFC3339)
	}
	sum := sha256.Sum256(data)
	marker.Exists = true
	marker.Digest = hex.EncodeToString(sum[:8])
	marker.Evidence = []string{fmt.Sprintf("%s bytes=%d sha256_8=%s", name, len(data), marker.Digest)}
	return marker
}

func parseGitLog(output []byte, sourceID string) []GitTouchSummary {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return []GitTouchSummary{}
	}
	parts := strings.Split(text, "__NTM_COMMIT__\x00")
	out := make([]GitTouchSummary, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.SplitN(part, "\x00", 3)
		if len(fields) < 2 {
			continue
		}
		commit := strings.TrimSpace(fields[0])
		rest := fields[1]
		lines := strings.Split(rest, "\n")
		subject := strings.TrimSpace(lines[0])
		paths := []string{}
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line != "" {
				paths = append(paths, filepath.ToSlash(line))
			}
		}
		out = append(out, GitTouchSummary{
			Commit:   commit,
			Subject:  subject,
			Paths:    stableStrings(paths),
			SourceID: sourceID,
		})
	}
	return out
}

func collectSupportBundleEvidence(projectDir string, limit int) []CloseoutProofEvidence {
	if limit <= 0 {
		return []CloseoutProofEvidence{}
	}
	roots := []string{"tests/artifacts", "docs", "internal/supportbundle"}
	out := make([]CloseoutProofEvidence, 0, limit)
	for _, root := range roots {
		base := filepath.Join(projectDir, root)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || len(out) >= limit {
				return nil
			}
			name := strings.ToLower(d.Name())
			if d.IsDir() {
				switch name {
				case ".git", ".gomodcache", "node_modules":
					return filepath.SkipDir
				default:
					return nil
				}
			}
			rel, relErr := filepath.Rel(projectDir, path)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			lower := strings.ToLower(rel)
			if !strings.Contains(lower, "supportbundle") && !strings.Contains(lower, "closeout") && !strings.Contains(lower, "proof") && !strings.Contains(lower, "manifest") {
				return nil
			}
			out = append(out, CloseoutProofEvidence{
				ID:       normalizeIDPart(rel),
				Path:     rel,
				Summary:  "local supportbundle/closeout artifact",
				Signals:  []string{"local artifact path matched supportbundle/closeout/proof/manifest"},
				SourceID: "supportbundle:local",
			})
			return nil
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func recordCollectorError(snapshot *IdeaEvidenceSnapshot, source CandidateSource, err error) {
	source.Available = false
	source.Error = err.Error()
	snapshot.RecordSource(source)
}

func normalizeCollectorOptions(opts CollectorOptions) CollectorOptions {
	if opts.ProjectDir == "" {
		wd, err := os.Getwd()
		if err == nil {
			opts.ProjectDir = wd
		}
	}
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = defaultCollectorTimeout
	}
	if opts.OutputLimitBytes <= 0 {
		opts.OutputLimitBytes = defaultCollectorOutputLimit
	}
	if opts.RecentClosedLimit <= 0 {
		opts.RecentClosedLimit = defaultRecentClosedLimit
	}
	if opts.GitCommitLimit < 0 {
		opts.GitCommitLimit = 0
	}
	if opts.GitCommitLimit == 0 {
		opts.GitCommitLimit = defaultGitCommitLimit
	}
	if opts.SupportBundleLimit < 0 {
		opts.SupportBundleLimit = 0
	}
	if opts.SupportBundleLimit == 0 {
		opts.SupportBundleLimit = defaultSupportBundleLimit
	}
	return opts
}

func beadFamily(id string) string {
	parts := strings.Split(id, ".")
	if len(parts) == 0 {
		return id
	}
	return parts[0]
}
