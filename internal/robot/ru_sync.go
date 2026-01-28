// Package robot provides machine-readable output for AI agents.
// ru_sync.go implements the --robot-ru-sync command.
package robot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	DryRun bool
}

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
		output.RobotResponse.Meta = meta.WithExitCode(1)
		output.ExitCode = 1
		return output, nil
	}

	args := []string{"sync", "--non-interactive"}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	useJSON := adapter.HasCapability(ctx, tools.CapRobotMode)
	if useJSON {
		args = append(args, "--json")
	}

	run := runRUSyncCommand(ctx, path, args)
	if run.err != nil && useJSON && isUnknownJSONFlag(run.stderr) {
		args = removeFlag(args, "--json")
		run = runRUSyncCommand(ctx, path, args)
	}

	output.DurationMs = time.Since(start).Milliseconds()
	output.ExitCode = run.exitCode
	meta.DurationMs = output.DurationMs
	meta.WithExitCode(run.exitCode)

	stdoutBytes := []byte(run.stdout)
	stderrStr := strings.TrimSpace(run.stderr)

	parsedRepos, parsedConflicts := parseRUSyncPayload(stdoutBytes)
	if len(parsedRepos.Synced) > 0 || len(parsedRepos.Skipped) > 0 {
		output.Repos = parsedRepos
	}
	if len(parsedConflicts) > 0 {
		output.Conflicts = parsedConflicts
	}

	if run.err != nil {
		errCode := ErrCodeInternalError
		errHint := "Check ru configuration or rerun with --dry-run"
		if ctx.Err() == context.DeadlineExceeded {
			errCode = ErrCodeTimeout
			errHint = "Try again later or reduce repo scope"
		}
		output.RobotResponse = NewErrorResponse(run.err, errCode, errHint)
		output.RobotResponse.Meta = meta
		output.Stdout = strings.TrimSpace(string(stdoutBytes))
		output.Stderr = stderrStr
		return output, nil
	}

	output.RobotResponse = NewRobotResponseWithMeta(true, meta)

	if (len(output.Repos.Synced) == 0 && len(output.Repos.Skipped) == 0) || stderrStr != "" {
		output.Stdout = strings.TrimSpace(string(stdoutBytes))
		output.Stderr = stderrStr
	}

	return output, nil
}

// PrintRUSync handles the --robot-ru-sync command.
// This is a thin wrapper around GetRUSync() for CLI output.
func PrintRUSync(opts RUSyncOptions) error {
	output, err := GetRUSync(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

func parseRUSyncPayload(data []byte) (RUSyncRepos, []string) {
	repos := RUSyncRepos{Synced: []string{}, Skipped: []string{}}
	conflicts := []string{}

	if len(data) == 0 || !json.Valid(data) {
		return repos, conflicts
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		var list []interface{}
		if err := json.Unmarshal(data, &list); err != nil {
			return repos, conflicts
		}
		mergeRepoItems(list, &repos, &conflicts)
		return repos, conflicts
	}

	repos.Synced = appendUniqueStrings(repos.Synced, parseStringSlice(payload["synced"])...)
	repos.Skipped = appendUniqueStrings(repos.Skipped, parseStringSlice(payload["skipped"])...)
	conflicts = appendUniqueStrings(conflicts, parseStringSlice(payload["conflicts"])...)

	if rawRepos, ok := payload["repos"]; ok {
		switch v := rawRepos.(type) {
		case map[string]interface{}:
			repos.Synced = appendUniqueStrings(repos.Synced, parseStringSlice(v["synced"])...)
			repos.Skipped = appendUniqueStrings(repos.Skipped, parseStringSlice(v["skipped"])...)
			conflicts = appendUniqueStrings(conflicts, parseStringSlice(v["conflicts"])...)
			if items, ok := v["items"]; ok {
				mergeRepoItems(items, &repos, &conflicts)
			}
		case []interface{}:
			mergeRepoItems(v, &repos, &conflicts)
		}
	}

	return repos, conflicts
}

func mergeRepoItems(items interface{}, repos *RUSyncRepos, conflicts *[]string) {
	list, ok := items.([]interface{})
	if !ok {
		return
	}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name := firstNonEmpty(
			stringValue(m["name"]),
			stringValue(m["repo"]),
			stringValue(m["path"]),
		)
		if name == "" {
			continue
		}
		status := strings.ToLower(stringValue(m["status"]))
		switch status {
		case "synced", "updated", "ok", "success":
			repos.Synced = appendUniqueStrings(repos.Synced, name)
		case "skipped", "noop", "unchanged":
			repos.Skipped = appendUniqueStrings(repos.Skipped, name)
		case "conflict", "conflicts", "merge-conflict", "merge_conflict":
			*conflicts = appendUniqueStrings(*conflicts, name)
		}
	}
}

type ruSyncRun struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func runRUSyncCommand(ctx context.Context, path string, args []string) ruSyncRun {
	cmd := exec.CommandContext(ctx, path, args...)
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

func isUnknownJSONFlag(stderr string) bool {
	lower := strings.ToLower(stderr)
	if !strings.Contains(lower, "json") {
		return false
	}
	return strings.Contains(lower, "unknown flag") || strings.Contains(lower, "flag provided but not defined")
}

func removeFlag(args []string, flag string) []string {
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == flag {
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func parseStringSlice(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return []string{}
	case string:
		if v == "" {
			return []string{}
		}
		return []string{v}
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			str := stringValue(item)
			if str != "" {
				out = append(out, str)
			}
		}
		return out
	default:
		return []string{}
	}
}

func appendUniqueStrings(dst []string, src ...string) []string {
	seen := make(map[string]struct{}, len(dst))
	for _, existing := range dst {
		if existing == "" {
			continue
		}
		seen[existing] = struct{}{}
	}
	for _, item := range src {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		dst = append(dst, item)
	}
	return dst
}

func stringValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
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
