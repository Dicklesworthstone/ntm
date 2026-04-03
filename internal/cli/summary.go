package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/archive"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

func newSummaryCmd() *cobra.Command {
	var (
		since      string
		format     string
		listAll    bool
		recent     bool
		regenerate bool
	)

	cmd := &cobra.Command{
		Use:   "summary [session]",
		Short: "Show activity summary for agents in a session",
		Long: `Display a summary of what each agent accomplished in a session.

Shows per-agent:
  - Active time and output volume
  - Files modified
  - Key actions (created, fixed, added, etc.)
  - Error counts

The summary is useful after parallel agent work to understand
what each agent did and identify potential conflicts.

Examples:
  ntm summary                      # Auto-detect session
  ntm summary myproject            # Specific session
  ntm summary --recent             # Show most recent summary
  ntm summary --since 1h           # Look back 1 hour
  ntm summary --format markdown    # Output as markdown
  ntm summary --json               # Output as JSON
  ntm summary --all                # List all available summaries
  ntm summary --regenerate         # Regenerate from archived output`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listAll {
				return runSummaryList(format)
			}
			if recent && regenerate {
				return fmt.Errorf("--recent and --regenerate cannot be used together")
			}
			return runSummary(args, since, format, recent, regenerate)
		},
	}

	cmd.Flags().StringVar(&since, "since", "30m", "Duration to look back (e.g., 30m, 1h)")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, markdown, detailed, or handoff")
	cmd.Flags().BoolVar(&listAll, "all", false, "List all available summaries")
	cmd.Flags().BoolVar(&recent, "recent", false, "Show most recent summary (optionally filtered by session)")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "Regenerate summary from archived output (if available)")

	return cmd
}

type summaryFileInfo struct {
	Session   string    `json:"session"`
	Timestamp time.Time `json:"timestamp"`
	Path      string    `json:"path"`
}

var summaryFilenameRegex = regexp.MustCompile(`^(?P<session>.+)-(?P<ts>\d{8}-\d{6})\.json$`)
var archiveFilenameRegex = regexp.MustCompile(`^(?P<session>.+)_(?P<date>\d{4}-\d{2}-\d{2})\.jsonl$`)

func runSummary(args []string, sinceStr, format string, recent, regenerate bool) error {
	sessionArg := ""
	if len(args) > 0 {
		sessionArg = args[0]
	}

	sumFormat, forceJSON, err := parseSummaryFormat(format)
	if err != nil {
		return err
	}

	wd, _ := os.Getwd()
	projectDir, err := resolveProjectDir(sessionArg, wd, strings.TrimSpace(sessionArg) != "")
	if err != nil {
		return err
	}
	summaryFiles, err := listSummaryFiles(projectDir)
	if err != nil {
		return err
	}

	if regenerate {
		return regenerateSummaryFromArchive(sessionArg, sumFormat, forceJSON || IsJSONOutput(), projectDir, wd)
	}

	if recent {
		latest, ok := latestSummary(summaryFiles, sessionArg)
		if !ok {
			return fmt.Errorf("no summaries found")
		}
		return outputSummaryFromFile(latest.Path, sumFormat, forceJSON || IsJSONOutput())
	}

	if sessionArg != "" {
		resolved, ok, err := resolveSummarySessionName(sessionArg, summaryFiles)
		if err != nil {
			return err
		}
		if ok {
			if latest, found := latestSummaryForSession(summaryFiles, resolved); found {
				return outputSummaryFromFile(latest.Path, sumFormat, forceJSON || IsJSONOutput())
			}
		}
	}

	if err := tmux.EnsureInstalled(); err != nil {
		if sessionArg == "" && len(summaryFiles) > 0 {
			if latest, ok := latestSummaryForSession(summaryFiles, ""); ok {
				return outputSummaryFromFile(latest.Path, sumFormat, forceJSON || IsJSONOutput())
			}
		}
		return err
	}

	res, err := ResolveSession(sessionArg, os.Stdout)
	if err != nil {
		if sessionArg == "" && len(summaryFiles) > 0 {
			if latest, ok := latestSummaryForSession(summaryFiles, ""); ok {
				return outputSummaryFromFile(latest.Path, sumFormat, forceJSON || IsJSONOutput())
			}
		}
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	session := res.Session
	projectDir, err = resolveProjectDir(session, wd, !res.Inferred)
	if err != nil {
		return err
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// We'll use the since duration to potentially filter logs (if we enhanced capture)
	// For now, it's just validated.
	_, err = util.ParseDurationWithDefault(sinceStr, 30*time.Minute, "since")
	if err != nil {
		return fmt.Errorf("invalid --since: %w", err)
	}

	// Get panes
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Build agent outputs
	outputs := collectSummaryAgentOutputs(panes, tmux.CapturePaneOutput, nil)

	opts := summary.Options{
		Session:        session,
		Outputs:        outputs,
		Format:         sumFormat,
		ProjectKey:     projectDir,
		ProjectDir:     projectDir,
		IncludeGitDiff: true,
	}

	s, err := summary.SummarizeSession(context.Background(), opts)
	if err != nil {
		return err
	}

	// Output
	if IsJSONOutput() || forceJSON {
		return output.PrintJSON(s)
	}

	// Human-readable output
	fmt.Println(s.Text)
	return nil
}

func runSummaryList(format string) error {
	sumFormat, forceJSON, err := parseSummaryFormat(format)
	if err != nil {
		return err
	}

	wd, _ := os.Getwd()
	projectDir, err := resolveProjectDir("", wd, false)
	if err != nil {
		return err
	}
	files, err := listSummaryFiles(projectDir)
	if err != nil {
		return fmt.Errorf("failed to list summaries: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("No summaries found.")
		return nil
	}

	// Output as JSON if requested
	if IsJSONOutput() || forceJSON {
		return output.PrintJSON(files)
	}

	fmt.Printf("Available summaries (%s):\n", sumFormat)
	for _, f := range files {
		fmt.Printf("  %s - %s (%s)\n", f.Session, f.Timestamp.Format(time.RFC3339), f.Path)
	}
	return nil
}

func parseSummaryFormat(format string) (summary.SummaryFormat, bool, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text", "brief":
		return summary.FormatBrief, false, nil
	case "json":
		return summary.FormatBrief, true, nil
	case "markdown", "md", "detailed":
		return summary.FormatDetailed, false, nil
	case "handoff":
		return summary.FormatHandoff, false, nil
	default:
		return "", false, fmt.Errorf("invalid --format %q (expected text, json, markdown, detailed, or handoff)", format)
	}
}

func resolveProjectDir(session, wd string, explicit bool) (string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
		if err != nil {
			return "", err
		}
		if explicit {
			dir, err := resolveExplicitProjectDirForSession(resolved)
			if err != nil {
				return "", err
			}
			return dir, nil
		}
		if dir := resolveCommandProjectDirForSession(resolved, true); dir != "" {
			return dir, nil
		}
	}
	if dir := util.ResolveProjectDir(wd); dir != "" {
		return dir, nil
	}
	return wd, nil
}

func listSummaryFiles(projectDir string) ([]summaryFileInfo, error) {
	summaryDir := filepath.Join(projectDir, ".ntm", "summaries")
	entries, err := os.ReadDir(summaryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []summaryFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		session, ts, ok := parseSummaryFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if ts.IsZero() {
			ts = info.ModTime()
		}
		files = append(files, summaryFileInfo{
			Session:   session,
			Timestamp: ts,
			Path:      filepath.Join(summaryDir, entry.Name()),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Timestamp.After(files[j].Timestamp)
	})

	return files, nil
}

func parseSummaryFilename(name string) (string, time.Time, bool) {
	matches := summaryFilenameRegex.FindStringSubmatch(name)
	if len(matches) != 3 {
		return "", time.Time{}, false
	}
	session := matches[1]
	ts, err := time.Parse("20060102-150405", matches[2])
	if err != nil {
		return session, time.Time{}, true
	}
	return session, ts, true
}

func resolveSummarySessionName(input string, files []summaryFileInfo) (string, bool, error) {
	if input == "" {
		return "", false, nil
	}
	sessions := uniqueSessions(files)
	for _, s := range sessions {
		if s == input {
			return s, true, nil
		}
	}
	var matches []string
	for _, s := range sessions {
		if strings.HasPrefix(s, input) {
			matches = append(matches, s)
		}
	}
	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	if len(matches) > 1 {
		return "", false, fmt.Errorf("session %q matches multiple summaries: %s (please be more specific)", input, strings.Join(matches, ", "))
	}
	return "", false, nil
}

func uniqueSessions(files []summaryFileInfo) []string {
	seen := make(map[string]struct{})
	var sessions []string
	for _, f := range files {
		if _, ok := seen[f.Session]; ok {
			continue
		}
		seen[f.Session] = struct{}{}
		sessions = append(sessions, f.Session)
	}
	sort.Strings(sessions)
	return sessions
}

func latestSummary(files []summaryFileInfo, session string) (summaryFileInfo, bool) {
	if session == "" {
		if len(files) == 0 {
			return summaryFileInfo{}, false
		}
		return files[0], true
	}
	return latestSummaryForSession(files, session)
}

func latestSummaryForSession(files []summaryFileInfo, session string) (summaryFileInfo, bool) {
	for _, f := range files {
		if session == "" || f.Session == session {
			return f, true
		}
	}
	return summaryFileInfo{}, false
}

func outputSummaryFromFile(path string, format summary.SummaryFormat, jsonOut bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var sum summary.SessionSummary
	if err := json.NewDecoder(file).Decode(&sum); err != nil {
		return fmt.Errorf("failed to parse summary %s: %w", path, err)
	}

	if jsonOut {
		return output.PrintJSON(sum)
	}

	text := summary.RenderSummary(&sum, format)
	fmt.Println(text)
	return nil
}

type archiveFileInfo struct {
	Session   string
	Timestamp time.Time
	Path      string
}

func regenerateSummaryFromArchive(sessionArg string, format summary.SummaryFormat, jsonOut bool, projectDir, wd string) error {
	archiveFile, sessionName, err := findArchiveFile(sessionArg)
	if err != nil {
		return err
	}
	if archiveFile == "" {
		return fmt.Errorf("no archived output found")
	}

	if sessionName == "" {
		sessionName = sessionArg
	}
	if projectDir == "" {
		projectDir, err = resolveProjectDir(sessionName, wd, strings.TrimSpace(sessionArg) != "")
		if err != nil {
			return err
		}
	}

	outputs, err := loadArchiveOutputs(archiveFile)
	if err != nil {
		return err
	}
	if len(outputs) == 0 {
		return fmt.Errorf("no archived output found for session %q", sessionName)
	}

	opts := summary.Options{
		Session:        sessionName,
		Outputs:        outputs,
		Format:         format,
		ProjectKey:     projectDir,
		ProjectDir:     projectDir,
		IncludeGitDiff: true,
	}

	sum, err := summary.SummarizeSession(context.Background(), opts)
	if err != nil {
		return err
	}

	if err := writeSummaryFile(projectDir, sessionName, sum); err != nil {
		return err
	}

	if jsonOut {
		return output.PrintJSON(sum)
	}

	fmt.Println(summary.RenderSummary(sum, format))
	return nil
}

func writeSummaryFile(projectDir, session string, sum *summary.SessionSummary) error {
	if projectDir == "" {
		return fmt.Errorf("project directory required to write summary")
	}
	summaryDir := filepath.Join(projectDir, ".ntm", "summaries")
	if err := os.MkdirAll(summaryDir, 0755); err != nil {
		return fmt.Errorf("failed to create summary dir: %w", err)
	}
	timestamp := time.Now().Format("20060102-150405")
	filename := filepath.Join(summaryDir, fmt.Sprintf("%s-%s.json", session, timestamp))
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create summary file: %w", err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(sum); err != nil {
		return fmt.Errorf("failed to write summary file: %w", err)
	}
	return nil
}

func findArchiveFile(session string) (string, string, error) {
	files, err := listArchiveFiles()
	if err != nil {
		return "", "", err
	}
	if len(files) == 0 {
		return "", "", nil
	}

	if session == "" {
		return files[0].Path, files[0].Session, nil
	}

	for _, f := range files {
		if f.Session == session {
			return f.Path, f.Session, nil
		}
	}
	return "", "", nil
}

func listArchiveFiles() ([]archiveFileInfo, error) {
	ntmDir, err := util.NTMDir()
	if err != nil {
		return nil, err
	}
	archiveDir := filepath.Join(ntmDir, "archive")
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []archiveFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		session, ts, ok := parseArchiveFilename(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if ts.IsZero() {
			ts = info.ModTime()
		}
		files = append(files, archiveFileInfo{
			Session:   session,
			Timestamp: ts,
			Path:      filepath.Join(archiveDir, entry.Name()),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Timestamp.After(files[j].Timestamp)
	})

	return files, nil
}

func parseArchiveFilename(name string) (string, time.Time, bool) {
	matches := archiveFilenameRegex.FindStringSubmatch(name)
	if len(matches) != 3 {
		return "", time.Time{}, false
	}
	session := matches[1]
	ts, err := time.Parse("2006-01-02", matches[2])
	if err != nil {
		return session, time.Time{}, true
	}
	return session, ts, true
}

func loadArchiveOutputs(path string) ([]summary.AgentOutput, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	paneBuilders := make(map[string]*strings.Builder)
	paneTypes := make(map[string]string)
	for {
		var record archive.ArchiveRecord
		if err := dec.Decode(&record); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if record.Content == "" {
			continue
		}
		builder := paneBuilders[record.Pane]
		if builder == nil {
			builder = &strings.Builder{}
			paneBuilders[record.Pane] = builder
		}
		builder.WriteString(record.Content)
		if record.Agent != "" {
			paneTypes[record.Pane] = record.Agent
		}
	}

	var panes []string
	for pane := range paneBuilders {
		panes = append(panes, pane)
	}
	sort.Strings(panes)

	outputs := make([]summary.AgentOutput, 0, len(panes))
	for _, pane := range panes {
		agentType := paneTypes[pane]
		if agentType == "" {
			agentType = "unknown"
		}
		outputs = append(outputs, summary.AgentOutput{
			AgentID:   pane,
			AgentType: agentType,
			Output:    paneBuilders[pane].String(),
		})
	}
	return outputs, nil
}

func collectSummaryAgentOutputs(
	panes []tmux.Pane,
	capture func(string, int) (string, error),
	onCaptureError func(tmux.Pane, error),
) []summary.AgentOutput {
	outputs := make([]summary.AgentOutput, 0, len(panes))
	for _, pane := range panes {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "" || agentType == "unknown" || agentType == "user" {
			continue
		}

		out, err := capture(pane.ID, 500)
		if err != nil && onCaptureError != nil {
			onCaptureError(pane, err)
		}

		outputs = append(outputs, summary.AgentOutput{
			AgentID:   pane.ID,
			AgentType: agentType,
			Output:    out,
		})
	}

	return outputs
}
