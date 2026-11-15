package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/scanner"
)

// PreCommitConfig holds configuration for the pre-commit hook.
type PreCommitConfig struct {
	// MaxCritical is the maximum allowed critical issues (default 0).
	MaxCritical int
	// MaxWarning is the maximum allowed warning issues (default 0).
	MaxWarning int
	// FailOnWarning treats warnings as errors.
	FailOnWarning bool
	// Timeout for the scan operation.
	Timeout time.Duration
	// Verbose enables detailed output.
	Verbose bool
	// SkipEmpty skips the scan if no staged files.
	SkipEmpty bool
}

// DefaultPreCommitConfig returns sensible defaults.
func DefaultPreCommitConfig() PreCommitConfig {
	return PreCommitConfig{
		MaxCritical:   0,
		MaxWarning:    0,
		FailOnWarning: true,
		Timeout:       120 * time.Second,
		Verbose:       false,
		SkipEmpty:     true,
	}
}

// PreCommitResult contains the result of running the pre-commit hook.
type PreCommitResult struct {
	Passed       bool                `json:"passed"`
	StagedFiles  []string            `json:"staged_files"`
	ScanResult   *scanner.ScanResult `json:"scan_result,omitempty"`
	BlockReason  string              `json:"block_reason,omitempty"`
	Duration     time.Duration       `json:"duration"`
	UBSAvailable bool                `json:"ubs_available"`
}

// RunPreCommit executes the pre-commit hook logic.
func RunPreCommit(ctx context.Context, repoPath string, config PreCommitConfig) (*PreCommitResult, error) {
	startTime := time.Now()
	result := &PreCommitResult{
		Passed:       true,
		UBSAvailable: scanner.IsAvailable(),
	}

	// Get staged files
	stagedFiles, err := getStagedFiles(repoPath)
	if err != nil {
		return nil, fmt.Errorf("getting staged files: %w", err)
	}
	result.StagedFiles = stagedFiles

	// Skip if no staged files
	if len(stagedFiles) == 0 && config.SkipEmpty {
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Check if UBS is available
	if !scanner.IsAvailable() {
		// Graceful degradation - pass but note UBS is not available
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Create scanner
	s, err := scanner.New()
	if err != nil {
		return nil, fmt.Errorf("creating scanner: %w", err)
	}

	// Apply timeout
	if config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, config.Timeout)
		defer cancel()
	}

	// Run scan on staged files
	opts := scanner.ScanOptions{
		StagedOnly:    true,
		FailOnWarning: config.FailOnWarning,
		Verbose:       config.Verbose,
		Timeout:       config.Timeout,
	}

	scanResult, err := s.Scan(ctx, repoPath, opts)
	if err != nil {
		return nil, fmt.Errorf("running scan: %w", err)
	}
	result.ScanResult = scanResult
	result.Duration = time.Since(startTime)

	// Check thresholds
	if scanResult.Totals.Critical > config.MaxCritical {
		result.Passed = false
		result.BlockReason = fmt.Sprintf(
			"critical issues exceeded threshold: %d > %d",
			scanResult.Totals.Critical, config.MaxCritical,
		)
	} else if config.FailOnWarning && scanResult.Totals.Warning > config.MaxWarning {
		result.Passed = false
		result.BlockReason = fmt.Sprintf(
			"warning issues exceeded threshold: %d > %d",
			scanResult.Totals.Warning, config.MaxWarning,
		)
	}

	return result, nil
}

// getStagedFiles returns a list of staged file paths.
func getStagedFiles(repoPath string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--name-only", "--cached", "--diff-filter=ACMR")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.TrimSpace(string(output))
	if lines == "" {
		return nil, nil
	}

	return strings.Split(lines, "\n"), nil
}

// PrintPreCommitResult prints the result in a human-readable format.
func PrintPreCommitResult(result *PreCommitResult) {
	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		red    = "\033[31m"
		green  = "\033[32m"
		yellow = "\033[33m"
		dim    = "\033[2m"
	)

	fmt.Println()
	fmt.Printf("%s%sNTM Pre-commit Check%s\n", bold, "", reset)
	fmt.Printf("%s════════════════════════════════════════%s\n\n", dim, reset)

	// Staged files info
	fmt.Printf("  Staged files: %d\n", len(result.StagedFiles))
	fmt.Printf("  Duration:     %s\n\n", result.Duration.Round(time.Millisecond))

	// UBS availability
	if !result.UBSAvailable {
		fmt.Printf("  %s⚠%s UBS not installed - skipping scan\n", yellow, reset)
		fmt.Printf("    Install: %shttps://github.com/nightowlai/ubs%s\n\n", dim, reset)
		return
	}

	// No staged files
	if len(result.StagedFiles) == 0 {
		fmt.Printf("  %s•%s No staged files to check\n\n", dim, reset)
		return
	}

	// Scan results
	if result.ScanResult != nil {
		sr := result.ScanResult

		critColor := reset
		warnColor := reset
		if sr.Totals.Critical > 0 {
			critColor = red
		}
		if sr.Totals.Warning > 0 {
			warnColor = yellow
		}

		fmt.Printf("  %sCritical:%s  %s%d%s\n", bold, reset, critColor, sr.Totals.Critical, reset)
		fmt.Printf("  %sWarning:%s   %s%d%s\n", bold, reset, warnColor, sr.Totals.Warning, reset)
		fmt.Printf("  %sInfo:%s      %d\n\n", bold, reset, sr.Totals.Info)

		// Show findings summary
		if len(sr.Findings) > 0 {
			fmt.Printf("%sFindings:%s\n\n", bold, reset)
			for i, f := range sr.Findings {
				if i >= 5 {
					remaining := len(sr.Findings) - 5
					fmt.Printf("  %s... and %d more%s\n", dim, remaining, reset)
					break
				}
				severityColor := reset
				switch f.Severity {
				case scanner.SeverityCritical:
					severityColor = red
				case scanner.SeverityWarning:
					severityColor = yellow
				}
				fmt.Printf("  %s%s%s %s:%d - %s\n",
					severityColor, string(f.Severity), reset,
					f.File, f.Line, f.Message)
			}
			fmt.Println()
		}
	}

	// Result
	fmt.Printf("%s────────────────────────────────────────%s\n", dim, reset)
	if result.Passed {
		fmt.Printf("%s✓%s Pre-commit check passed\n\n", green, reset)
	} else {
		fmt.Printf("%s✗%s Pre-commit check failed\n", red, reset)
		fmt.Printf("  %s%s%s\n\n", red, result.BlockReason, reset)
		fmt.Printf("  Fix the issues above and try again.\n")
		fmt.Printf("  Run '%subs $(git diff --name-only --cached)%s' to see details.\n\n", dim, reset)
	}
}

// ExitCode returns the appropriate exit code for the result.
func (r *PreCommitResult) ExitCode() int {
	if r.Passed {
		return 0
	}
	return 1
}

// HookCheckResult contains the structured result of running a managed git hook
// (pre-push, commit-msg, post-commit, post-checkout).
type HookCheckResult struct {
	HookType    string        `json:"hook_type"`
	Passed      bool          `json:"passed"`
	Warnings    []string      `json:"warnings,omitempty"`
	BlockReason string        `json:"block_reason,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// ExitCode returns 0 when Passed is true and 1 otherwise.
func (r *HookCheckResult) ExitCode() int {
	if r != nil && r.Passed {
		return 0
	}
	return 1
}

// PrintHookResult prints the hook execution result in a human-readable format.
func PrintHookResult(result *HookCheckResult) {
	if result == nil {
		return
	}
	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		red    = "\033[31m"
		green  = "\033[32m"
		yellow = "\033[33m"
		dim    = "\033[2m"
	)

	fmt.Println()
	fmt.Printf("%sNTM %s Check%s\n", bold, result.HookType, reset)
	fmt.Printf("%s════════════════════════════════════════%s\n\n", dim, reset)
	fmt.Printf("  Duration: %s\n", result.Duration.Round(time.Millisecond))

	for _, w := range result.Warnings {
		fmt.Printf("  %s⚠%s Warning: %s\n", yellow, reset, w)
	}
	fmt.Println()

	fmt.Printf("%s────────────────────────────────────────%s\n", dim, reset)
	if result.Passed {
		fmt.Printf("%s✓%s %s check passed\n\n", green, reset, result.HookType)
	} else {
		fmt.Printf("%s✗%s %s check failed\n", red, reset, result.HookType)
		if result.BlockReason != "" {
			fmt.Printf("  %s%s%s\n", red, result.BlockReason, reset)
		}
		fmt.Println()
	}
}

var conventionalCommitSubjectRe = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert|security|deps|ops)(\([^)\r\n]+\))?!?: \S`)

// RunPrePush executes the pre-push hook checks.
func RunPrePush(ctx context.Context, repoPath string, verbose bool, timeout time.Duration) (*HookCheckResult, error) {
	startTime := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := resolveHookRepoRoot(repoPath)
	if err != nil {
		return nil, err
	}

	result := &HookCheckResult{
		HookType: string(HookPrePush),
		Passed:   true,
	}
	var blockReasons []string

	if os.Getenv("NTM_MAIN_ONLY") == "1" {
		branch, branchErr := currentGitBranch(ctx, root)
		if branchErr != nil {
			return nil, branchErr
		}
		if branch != "main" && branch != "HEAD" {
			blockReasons = append(blockReasons, fmt.Sprintf("push blocked by NTM_MAIN_ONLY=1: current branch is %q (expected \"main\")", branch))
		}
	}

	beadsDir := filepath.Join(root, ".beads")
	if info, statErr := os.Stat(beadsDir); statErr == nil && info.IsDir() {
		dirty, dirtyErr := hasDirtyBeads(ctx, root)
		if dirtyErr != nil {
			return nil, dirtyErr
		}
		if dirty {
			result.Warnings = append(result.Warnings, "uncommitted .beads changes detected before push; run 'br sync --flush-only' and commit .beads/")
		}

		if brPath, lookErr := exec.LookPath("br"); lookErr == nil {
			cmd := exec.CommandContext(ctx, brPath, "dep", "cycles")
			cmd.Dir = root
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				detail := strings.TrimSpace(string(out))
				reason := "beads dependency cycles detected by 'br dep cycles'"
				if detail != "" {
					if !verbose && len(detail) > 200 {
						detail = detail[:200] + "..."
					}
					reason = fmt.Sprintf("%s: %s", reason, detail)
				}
				blockReasons = append(blockReasons, reason)
			}
		}
	}

	if len(blockReasons) > 0 {
		result.Passed = false
		result.BlockReason = strings.Join(blockReasons, "; ")
	}
	result.Duration = time.Since(startTime)
	return result, nil
}

// RunCommitMsg validates a commit message file for the commit-msg hook.
func RunCommitMsg(ctx context.Context, repoPath string, msgFile string) (*HookCheckResult, error) {
	startTime := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := resolveHookRepoRoot(repoPath)
	if err != nil {
		return nil, err
	}

	msgPath := strings.TrimSpace(msgFile)
	if msgPath == "" {
		msgPath, err = findGitPath(root, "COMMIT_EDITMSG")
		if err != nil {
			return nil, fmt.Errorf("resolving COMMIT_EDITMSG: %w", err)
		}
	} else if !filepath.IsAbs(msgPath) {
		if _, statErr := os.Stat(msgPath); os.IsNotExist(statErr) {
			candidate := filepath.Join(root, msgPath)
			if _, candErr := os.Stat(candidate); candErr == nil {
				msgPath = candidate
			}
		}
	}

	raw, err := os.ReadFile(msgPath)
	if err != nil {
		return nil, fmt.Errorf("reading commit message %s: %w", msgPath, err)
	}

	result := &HookCheckResult{
		HookType: string(HookCommitMsg),
		Passed:   true,
	}

	normalized := strings.ReplaceAll(string(raw), "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")
	keptLines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		if strings.HasPrefix(line, "# ------------------------ >8 ------------------------") {
			break
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		keptLines = append(keptLines, line)
	}

	cleaned := strings.Join(keptLines, "\n")
	if strings.TrimSpace(cleaned) == "" {
		result.Passed = false
		result.BlockReason = "commit message is empty"
		result.Duration = time.Since(startTime)
		return result, nil
	}

	for _, line := range keptLines {
		if hasConflictMarkerLine(line) {
			result.Passed = false
			result.BlockReason = "commit message contains unresolved merge-conflict markers"
			result.Duration = time.Since(startTime)
			return result, nil
		}
	}

	var subject string
	for _, line := range keptLines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			subject = trimmed
			break
		}
	}

	conventional := os.Getenv("NTM_CONVENTIONAL_COMMITS") == "1"
	var blockReasons []string

	subjectLen := utf8.RuneCountInString(subject)
	if subjectLen > 120 {
		lengthMsg := fmt.Sprintf("commit subject line is %d characters (exceeds 120)", subjectLen)
		if conventional {
			blockReasons = append(blockReasons, lengthMsg)
		} else {
			result.Warnings = append(result.Warnings, lengthMsg)
		}
	}

	if conventional && !conventionalCommitSubjectRe.MatchString(subject) {
		blockReasons = append(blockReasons, "commit subject must use Conventional Commits format (e.g. feat(scope): summary)")
	}

	if len(blockReasons) > 0 {
		result.Passed = false
		result.BlockReason = strings.Join(blockReasons, "; ")
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// RunPostCommit executes non-blocking post-commit checks.
func RunPostCommit(ctx context.Context, repoPath string) (*HookCheckResult, error) {
	startTime := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := resolveHookRepoRoot(repoPath)
	if err != nil {
		return nil, err
	}

	result := &HookCheckResult{
		HookType: string(HookPostCommit),
		Passed:   true,
	}

	dirty, err := hasDirtyBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	if dirty {
		result.Warnings = append(result.Warnings, "uncommitted .beads changes remain after commit; run 'br sync --flush-only' if beads changed")
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// RunPostCheckout executes non-blocking post-checkout checks.
func RunPostCheckout(ctx context.Context, repoPath string) (*HookCheckResult, error) {
	startTime := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	root, err := resolveHookRepoRoot(repoPath)
	if err != nil {
		return nil, err
	}

	result := &HookCheckResult{
		HookType: string(HookPostCheckout),
		Passed:   true,
	}

	dirty, err := hasDirtyBeads(ctx, root)
	if err != nil {
		return nil, err
	}
	if dirty {
		result.Warnings = append(result.Warnings, ".beads has uncommitted changes. Run: br sync --flush-only and commit .beads/")
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

func resolveHookRepoRoot(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		repoPath = cwd
	}
	return findGitRoot(repoPath)
}

func hasDirtyBeads(ctx context.Context, repoRoot string) (bool, error) {
	beadsDir := filepath.Join(repoRoot, ".beads")
	info, err := os.Stat(beadsDir)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "-C", repoRoot, "status", "--porcelain", ".beads")
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("checking .beads git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func currentGitBranch(ctx context.Context, repoRoot string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "-C", repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	symCmd := exec.CommandContext(gitCtx, "git", "-C", repoRoot, "symbolic-ref", "--short", "HEAD")
	out, err := symCmd.Output()
	if err != nil {
		return "", fmt.Errorf("determining current git branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func hasConflictMarkerLine(line string) bool {
	return strings.HasPrefix(line, "<<<<<<< ") ||
		line == "=======" ||
		strings.HasPrefix(line, ">>>>>>> ")
}
