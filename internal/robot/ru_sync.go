// Package robot provides machine-readable output for AI agents.
// ru_sync.go implements the --robot-ru-sync command.
package robot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// RUSyncOutput represents the response from --robot-ru-sync.
type RUSyncOutput struct {
	RobotResponse
	DryRun     bool        `json:"dry_run,omitempty"`
	Repos      RUSyncRepos `json:"repos"`
	Conflicts  []string    `json:"conflicts"`
	Failed     []string    `json:"failed"`
	DurationMs int64       `json:"duration_ms"`
	ExitCode   int         `json:"exit_code"`
	Stdout     string      `json:"stdout,omitempty"`
	Stderr     string      `json:"stderr,omitempty"`
}

// RUSyncRepos groups repo results by outcome.
type RUSyncRepos struct {
	Synced  []string `json:"synced"`
	Skipped []string `json:"skipped"`
}

// RUSyncOptions configures the GetRUSync operation.
type RUSyncOptions struct {
	DryRun   bool
	extraEnv []string
}

// ru sync exit codes (ru --help, EXIT CODES).
const (
	ruExitPartialFailure = 1
	ruExitConflicts      = 2
	ruExitSystemError    = 3
	ruExitInterrupted    = 5
)

// GetRUSync runs ru sync and returns a structured robot response.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRUSync(opts RUSyncOptions) (*RUSyncOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	adapter := tools.NewRUAdapter()
	path, installed := adapter.Detect()

	output := &RUSyncOutput{
		RobotResponse: NewRobotResponse(true),
		DryRun:        opts.DryRun,
		Repos: RUSyncRepos{
			Synced:  []string{},
			Skipped: []string{},
		},
		Conflicts: []string{},
		Failed:    []string{},
	}

	meta := NewResponseMeta("robot-ru-sync")
	start := time.Now()

	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ru not installed"),
			ErrCodeDependencyMissing,
			"Install ru to enable repo sync",
		)
		output.DurationMs = time.Since(start).Milliseconds()
		meta.DurationMs = output.DurationMs
		output.Meta = meta.WithExitCode(1)
		output.ExitCode = 1
		return output, nil
	}

	args := []string{"sync", "--non-interactive", "--json"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	run := runRUSyncCommand(ctx, path, args, opts.extraEnv)

	output.DurationMs = time.Since(start).Milliseconds()
	output.ExitCode = run.exitCode
	meta.DurationMs = output.DurationMs
	meta = meta.WithExitCode(run.exitCode)

	parsed, parseErr := parseRUSyncPayload([]byte(run.stdout))
	if parseErr == nil {
		output.Repos = parsed.repos
		output.Conflicts = parsed.conflicts
		output.Failed = parsed.failed
	}

	if run.err != nil || parseErr != nil {
		output.RobotResponse = ruSyncErrorResponse(ctx, run, parseErr, output)
		output.Meta = meta
		output.Stdout = run.stdout
		output.Stderr = run.stderr
		return output, nil
	}

	output.RobotResponse = NewRobotResponseWithMeta(true, meta)
	if run.stderr != "" {
		output.Stderr = run.stderr
	}
	return output, nil
}

// ruSyncErrorResponse maps a failed or unreadable ru sync to a robot error;
// ru's exit code says which repos need attention, not that ntm broke.
func ruSyncErrorResponse(ctx context.Context, run ruSyncRun, parseErr error, output *RUSyncOutput) RobotResponse {
	if ctx.Err() == context.DeadlineExceeded {
		return NewErrorResponse(fmt.Errorf("ru sync timed out: %w", ctx.Err()), ErrCodeTimeout, "Try again later or reduce repo scope")
	}
	switch run.exitCode {
	case ruExitConflicts:
		return NewErrorResponse(
			fmt.Errorf("ru sync left %d repo(s) needing manual resolution", len(output.Conflicts)),
			"RU_CONFLICTS",
			"Resolve the repos in conflicts (diverged, dirty, remote mismatch, ...) and re-run; stderr has ru's per-repo guidance",
		)
	case ruExitPartialFailure:
		return NewErrorResponse(
			fmt.Errorf("ru sync failed for %d repo(s)", len(output.Failed)),
			"RU_PARTIAL_FAILURE",
			"See failed and stderr for the repos that did not sync",
		)
	case ruExitSystemError:
		return NewErrorResponse(fmt.Errorf("ru sync reported a dependency or system error"), ErrCodeDependencyMissing, "Run 'ru doctor' (gh missing or not authenticated is the usual cause)")
	case ruExitInterrupted:
		return NewErrorResponse(fmt.Errorf("a previous ru sync was interrupted"), "RU_SYNC_INTERRUPTED", "Run 'ru sync --resume' or 'ru sync --restart'")
	}
	err := run.err
	if err == nil {
		err = parseErr
	}
	return NewErrorResponse(err, ErrCodeInternalError, "Check ru configuration or rerun with --dry-run")
}

// ruSyncResult is ru's per-repo outcome, bucketed for RUSyncOutput.
type ruSyncResult struct {
	repos     RUSyncRepos
	conflicts []string
	failed    []string
}

// parseRUSyncPayload reads `ru sync --json`, whose repos sit under
// data.repos[] with a status per repo, and buckets the statuses the way ru's
// own summary counts them.
func parseRUSyncPayload(data []byte) (ruSyncResult, error) {
	result := ruSyncResult{
		repos:     RUSyncRepos{Synced: []string{}, Skipped: []string{}},
		conflicts: []string{},
		failed:    []string{},
	}
	var envelope struct {
		Data *struct {
			Repos []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"repos"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return result, fmt.Errorf("decode ru sync output: %w", err)
	}
	if envelope.Data == nil {
		return result, fmt.Errorf("ru sync output has no data object")
	}
	// ru can record one repo's result twice (seen with a remote mismatch), so
	// each bucket lists a repo once.
	listed := make(map[string]bool)
	add := func(bucket *[]string, kind, name string) {
		if key := kind + "\x00" + name; !listed[key] {
			listed[key] = true
			*bucket = append(*bucket, name)
		}
	}
	for _, repo := range envelope.Data.Repos {
		switch repo.Status {
		case "ok", "updated", "current": // ok = cloned
			add(&result.repos.Synced, "synced", repo.Name)
		case "failed", "timeout", "dep_error", "auth_error":
			add(&result.failed, "failed", repo.Name)
		case "diverged", "dirty", "conflict", "mismatch", "not_git", "branch_error", "no_remote", "no_upstream", "invalid":
			add(&result.conflicts, "conflict", repo.Name)
		default: // skipped, dry_run, and anything newer, as ru counts them
			add(&result.repos.Skipped, "skipped", repo.Name)
		}
	}
	return result, nil
}

type ruSyncRun struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func runRUSyncCommand(ctx context.Context, path string, args []string, extraEnv []string) ruSyncRun {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 2 * time.Second
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	stdout := tools.NewLimitedBuffer(10 * 1024 * 1024)
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	return ruSyncRun{
		stdout:   strings.TrimSpace(stdout.String()),
		stderr:   strings.TrimSpace(stderr.String()),
		exitCode: exitCode,
		err:      err,
	}
}

func firstNonEmpty(values ...string) string {
	for _, val := range values {
		if strings.TrimSpace(val) != "" {
			return val
		}
	}
	return ""
}

// PrintRUSync handles the --robot-ru-sync command.
// This is a thin wrapper around GetRUSync() for CLI output.
func PrintRUSync(opts RUSyncOptions) error {
	output, err := GetRUSync(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ru sync failed")
}
