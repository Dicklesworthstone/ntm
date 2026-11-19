package robot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/hooks"
)

const (
	hooksSchemaID      = "ntm:robot:hooks:v1"
	hooksSchemaVersion = "1.0.0"

	HooksSchemaID      = hooksSchemaID
	HooksSchemaVersion = hooksSchemaVersion

	ErrCodeHookCheckFailed = "HOOK_CHECK_FAILED"
)

// HooksOptions configures GetHooks.
type HooksOptions struct {
	Action        string // "status" (default) or "run"
	HookType      string // "pre-commit" (default for run), "pre-push", "commit-msg", "post-commit", "post-checkout"
	HookArgs      []string
	ProjectDir    string
	Verbose       bool
	FailOnWarning *bool
	TimeoutSec    int
}

// HooksOutput is the canonical robot envelope for --robot-hooks and /api/v1/hooks*.
type HooksOutput struct {
	RobotResponse
	SchemaID        string                 `json:"schema_id"`
	SchemaVersion   string                 `json:"schema_version"`
	Action          string                 `json:"action"`
	RepoRoot        string                 `json:"repo_root,omitempty"`
	HooksDir        string                 `json:"hooks_dir,omitempty"`
	Hooks           []*hooks.HookInfo      `json:"hooks,omitempty"`
	InstalledCount  *int                   `json:"installed_count,omitempty"`
	HookType        string                 `json:"hook_type,omitempty"`
	Passed          *bool                  `json:"passed,omitempty"`
	PreCommitResult *hooks.PreCommitResult `json:"pre_commit_result,omitempty"`
	CheckResult     *hooks.HookCheckResult `json:"check_result,omitempty"`
	AgentHints      *AgentHints            `json:"_agent_hints,omitempty"`
}

// GetHooks inspects installed repository git hooks or executes a hook check in-place.
func GetHooks(ctx context.Context, opts HooksOptions) (*HooksOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if strings.Compare(action, "") == 0 {
		action = "status"
	}
	if !strings.EqualFold(action, "status") && !strings.EqualFold(action, "run") {
		return &HooksOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("invalid hooks action %q (expected status or run)", opts.Action),
				ErrCodeInvalidFlag,
				"Use --hooks-action=status or --hooks-action=run",
			),
			SchemaID:      hooksSchemaID,
			SchemaVersion: hooksSchemaVersion,
			Action:        action,
		}, nil
	}

	mgr, err := hooks.NewManager(strings.TrimSpace(opts.ProjectDir))
	if err != nil {
		code := ErrCodeInternalError
		if errors.Is(err, hooks.ErrNotGitRepo) {
			code = ErrCodeInvalidFlag
		}
		return &HooksOutput{
			RobotResponse: NewErrorResponse(
				err,
				code,
				"Run inside a git repository or pass --project=<git-repo-dir>",
			),
			SchemaID:      hooksSchemaID,
			SchemaVersion: hooksSchemaVersion,
			Action:        action,
		}, nil
	}

	if strings.EqualFold(action, "status") {
		infos, listErr := mgr.ListAll()
		if listErr != nil {
			return &HooksOutput{
				RobotResponse: NewErrorResponse(listErr, ErrCodeInternalError, "Inspect .git/hooks permissions and git repository configuration"),
				SchemaID:      hooksSchemaID,
				SchemaVersion: hooksSchemaVersion,
				Action:        action,
				RepoRoot:      mgr.RepoRoot(),
				HooksDir:      mgr.HooksDir(),
			}, nil
		}
		if infos == nil {
			infos = []*hooks.HookInfo{}
		}
		installedCount := 0
		for _, info := range infos {
			if info.Installed {
				installedCount++
			}
		}
		return &HooksOutput{
			RobotResponse:  NewRobotResponse(true),
			SchemaID:       hooksSchemaID,
			SchemaVersion:  hooksSchemaVersion,
			Action:         "status",
			RepoRoot:       mgr.RepoRoot(),
			HooksDir:       mgr.HooksDir(),
			Hooks:          infos,
			InstalledCount: &installedCount,
			AgentHints: &AgentHints{
				Summary: fmt.Sprintf("%d/%d git hooks installed in %s.", installedCount, len(infos), mgr.RepoRoot()),
			},
		}, nil
	}

	hookType := strings.TrimSpace(opts.HookType)
	if strings.Compare(hookType, "") == 0 {
		hookType = string(hooks.HookPreCommit)
	}

	switch hooks.HookType(hookType) {
	case hooks.HookPreCommit:
		pcCfg := hooks.DefaultPreCommitConfig()
		pcCfg.Verbose = opts.Verbose
		if opts.FailOnWarning != nil {
			pcCfg.FailOnWarning = *opts.FailOnWarning
		}
		if opts.TimeoutSec > 0 {
			pcCfg.Timeout = time.Duration(opts.TimeoutSec) * time.Second
		} else if opts.TimeoutSec < 0 {
			pcCfg.Timeout = 0
		}
		res, runErr := hooks.RunPreCommit(ctx, mgr.RepoRoot(), pcCfg)
		if runErr != nil {
			return &HooksOutput{
				RobotResponse: NewErrorResponse(runErr, ErrCodeInternalError, "Verify git and ubs availability in PATH"),
				SchemaID:      hooksSchemaID,
				SchemaVersion: hooksSchemaVersion,
				Action:        "run",
				RepoRoot:      mgr.RepoRoot(),
				HooksDir:      mgr.HooksDir(),
				HookType:      hookType,
			}, nil
		}
		passed := res.Passed
		resp := NewRobotResponse(true)
		summary := fmt.Sprintf("pre-commit check passed (%d staged file(s)).", len(res.StagedFiles))
		if !passed {
			reason := strings.TrimSpace(res.BlockReason)
			if strings.Compare(reason, "") == 0 {
				reason = "pre-commit hook failed"
			}
			resp = NewErrorResponse(errors.New(reason), ErrCodeHookCheckFailed, "Resolve reported staged-file findings and re-run")
			summary = fmt.Sprintf("pre-commit check failed: %s", reason)
		}
		return &HooksOutput{
			RobotResponse:   resp,
			SchemaID:        hooksSchemaID,
			SchemaVersion:   hooksSchemaVersion,
			Action:          "run",
			RepoRoot:        mgr.RepoRoot(),
			HooksDir:        mgr.HooksDir(),
			HookType:        hookType,
			Passed:          &passed,
			PreCommitResult: res,
			AgentHints: &AgentHints{
				Summary: summary,
			},
		}, nil

	case hooks.HookPrePush, hooks.HookCommitMsg, hooks.HookPostCommit, hooks.HookPostCheckout:
		var dur time.Duration
		if opts.TimeoutSec > 0 {
			dur = time.Duration(opts.TimeoutSec) * time.Second
		}
		var (
			res    *hooks.HookCheckResult
			runErr error
		)
		switch hooks.HookType(hookType) {
		case hooks.HookPrePush:
			res, runErr = hooks.RunPrePush(ctx, mgr.RepoRoot(), opts.Verbose, dur)
		case hooks.HookCommitMsg:
			msgFile := ""
			if len(opts.HookArgs) > 0 {
				msgFile = opts.HookArgs[0]
			}
			res, runErr = hooks.RunCommitMsg(ctx, mgr.RepoRoot(), msgFile)
		case hooks.HookPostCommit:
			res, runErr = hooks.RunPostCommit(ctx, mgr.RepoRoot())
		case hooks.HookPostCheckout:
			res, runErr = hooks.RunPostCheckout(ctx, mgr.RepoRoot())
		}
		if runErr != nil {
			return &HooksOutput{
				RobotResponse: NewErrorResponse(runErr, ErrCodeInternalError, "Inspect git repository state and hook arguments"),
				SchemaID:      hooksSchemaID,
				SchemaVersion: hooksSchemaVersion,
				Action:        "run",
				RepoRoot:      mgr.RepoRoot(),
				HooksDir:      mgr.HooksDir(),
				HookType:      hookType,
			}, nil
		}
		passed := res.Passed
		resp := NewRobotResponse(true)
		summary := fmt.Sprintf("%s check passed.", hookType)
		if !passed {
			reason := strings.TrimSpace(res.BlockReason)
			if strings.Compare(reason, "") == 0 {
				reason = fmt.Sprintf("%s hook failed", hookType)
			}
			resp = NewErrorResponse(errors.New(reason), ErrCodeHookCheckFailed, "Resolve the reported hook check blocker and re-run")
			summary = fmt.Sprintf("%s check failed: %s", hookType, reason)
		}
		return &HooksOutput{
			RobotResponse: resp,
			SchemaID:      hooksSchemaID,
			SchemaVersion: hooksSchemaVersion,
			Action:        "run",
			RepoRoot:      mgr.RepoRoot(),
			HooksDir:      mgr.HooksDir(),
			HookType:      hookType,
			Passed:        &passed,
			CheckResult:   res,
			AgentHints: &AgentHints{
				Summary: summary,
			},
		}, nil

	default:
		return &HooksOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("unknown hook type %q (supported: pre-commit, pre-push, commit-msg, post-commit, post-checkout)", hookType),
				ErrCodeInvalidFlag,
				"Specify one of: pre-commit, pre-push, commit-msg, post-commit, post-checkout",
			),
			SchemaID:      hooksSchemaID,
			SchemaVersion: hooksSchemaVersion,
			Action:        "run",
			RepoRoot:      mgr.RepoRoot(),
			HooksDir:      mgr.HooksDir(),
			HookType:      hookType,
		}, nil
	}
}

// PrintHooks writes the terminal JSON/TOON response for --robot-hooks.
func PrintHooks(ctx context.Context, opts HooksOptions) error {
	out, err := GetHooks(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot hooks failed")
}

func hooksCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "hooks",
		Flag:        "--robot-hooks",
		Category:    "utility",
		Summary:     "Inspect or run repository git hooks.",
		Description: "Inspect installed repository git hooks or execute pre-commit, pre-push, commit-msg, post-commit, or post-checkout checks.",
		Parameters: []RobotParameter{
			{Name: "hooks-action", Flag: "--hooks-action", Type: "string", Required: false, Default: "status", Description: "Hooks action: status (default) or run"},
			{Name: "hooks-type", Flag: "--hooks-type", Type: "string", Required: false, Default: "pre-commit", Description: "Hook type for --hooks-action=run: pre-commit, pre-push, commit-msg, post-commit, post-checkout"},
			{Name: "hooks-fail-on-warning", Flag: "--hooks-fail-on-warning", Type: "bool", Required: false, Default: "true", Description: "Treat UBS warnings as errors when running pre-commit"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Description: "Repository directory to inspect or run hooks against (defaults to CWD)"},
		},
		Examples: []string{
			"ntm --robot-hooks",
			"ntm --robot-hooks --hooks-action=run --hooks-type=pre-commit",
			"ntm --robot-hooks --hooks-action=run --hooks-type=pre-push --project=/data/projects/ntm",
		},
	}
}
