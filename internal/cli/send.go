package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/codex"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	dispatchsvc "github.com/Dicklesworthstone/ntm/internal/dispatch"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/prompt"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/templates"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

// SendResult is the JSON output for the send command.
type SendResult struct {
	Success              bool                                `json:"success"`
	Session              string                              `json:"session"`
	PromptPreview        string                              `json:"prompt_preview,omitempty"`
	NonInteractiveForced bool                                `json:"non_interactive_forced,omitempty"`
	Redaction            *RedactionSummary                   `json:"redaction,omitempty"`
	Warnings             []string                            `json:"warnings,omitempty"`
	Blocked              bool                                `json:"blocked,omitempty"`
	ErrorCode            string                              `json:"error_code,omitempty"`
	Randomized           bool                                `json:"randomized,omitempty"`
	SeedUsed             int64                               `json:"seed_used,omitempty"`
	Targets              []string                            `json:"targets"`
	Delivered            int                                 `json:"delivered"`
	Failed               int                                 `json:"failed"`
	RoutedTo             *SendRoutingResult                  `json:"routed_to,omitempty"`
	DispatchPacing       *coordinator.DispatchPacingDecision `json:"dispatch_pacing,omitempty"`
	Error                string                              `json:"error,omitempty"`
}

const (
	sendErrorCodeFailed          = "SEND_FAILED"
	sendErrorCodeNoMatchingPanes = "NO_MATCHING_PANES"
)

// sendProjectSessionResult is the per-session receipt in a project broadcast.
// It intentionally contains only stable, machine-actionable delivery data.
type sendProjectSessionResult struct {
	Session   string   `json:"session"`
	Success   bool     `json:"success"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	DryRun    bool     `json:"dry_run,omitempty"`
	WouldSend int      `json:"would_send,omitempty"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// sendProjectResult is the sole terminal JSON document for `send --project`.
type sendProjectResult struct {
	output.TimestampedResponse
	Success           bool                       `json:"success"`
	Project           string                     `json:"project"`
	Sessions          []sendProjectSessionResult `json:"sessions"`
	MatchedSessions   int                        `json:"matched_sessions"`
	SucceededSessions int                        `json:"succeeded_sessions"`
	FailedSessions    int                        `json:"failed_sessions"`
	Delivered         int                        `json:"delivered"`
	FailedDeliveries  int                        `json:"failed_deliveries"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	Error             string                     `json:"error,omitempty"`
}

type SendDryRunEntry struct {
	Pane          string `json:"pane"`
	PaneID        string `json:"pane_id"`
	Agent         string `json:"agent,omitempty"`
	Prompt        string `json:"prompt"`
	PromptPreview string `json:"prompt_preview,omitempty"`
	Source        string `json:"source,omitempty"`
	Priority      int    `json:"priority,omitempty"` // -1 omitted; 0..4 = P0..P4
}

type SendDryRunResult struct {
	Success              bool                                `json:"success"`
	DryRun               bool                                `json:"dry_run"`
	Session              string                              `json:"session"`
	NonInteractiveForced bool                                `json:"non_interactive_forced,omitempty"`
	Redaction            *RedactionSummary                   `json:"redaction,omitempty"`
	Warnings             []string                            `json:"warnings,omitempty"`
	Blocked              bool                                `json:"blocked,omitempty"`
	ErrorCode            string                              `json:"error_code,omitempty"`
	Total                int                                 `json:"total"`
	WouldSend            []SendDryRunEntry                   `json:"would_send"`
	RoutedTo             *SendRoutingResult                  `json:"routed_to,omitempty"`
	DispatchPacing       *coordinator.DispatchPacingDecision `json:"dispatch_pacing,omitempty"`
	Message              string                              `json:"message,omitempty"`
	Error                string                              `json:"error,omitempty"`
}

// outputSendCommandError preserves the command's machine-readable contract for
// validation failures that occur before the normal send/distribute runners can
// construct their result. A failure envelope always carries an empty targets
// array and propagates errJSONFailure so the process exits non-zero silently.
func outputSendCommandError(session string, err error) error {
	if err == nil {
		return nil
	}
	if !jsonOutput || errors.Is(err, errJSONFailure) {
		return err
	}
	result := SendResult{
		Success: false,
		Session: strings.TrimSpace(session),
		Targets: []string{},
		Error:   err.Error(),
	}
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		result.ErrorCode = robot.ErrCodeTimeout
	case errors.Is(err, errCLIInvalidInput):
		result.ErrorCode = robot.ErrCodeInvalidFlag
	default:
		result.ErrorCode = robot.ErrCodeInternalError
	}
	emitErr := emitJSONFailureEnvelope(result)
	if !errors.Is(emitErr, errJSONFailure) {
		return emitErr
	}
	return errors.Join(emitErr, err)
}

// SendRoutingResult contains routing decision info for smart routing.
type SendRoutingResult struct {
	PaneIndex int     `json:"pane_index"`
	Pane      string  `json:"pane,omitempty"`
	PaneID    string  `json:"pane_id,omitempty"`
	AgentType string  `json:"agent_type"`
	Strategy  string  `json:"strategy"`
	Reason    string  `json:"reason"`
	Score     float64 `json:"score"`
}

// SessionInterruptInput is the kernel input for sessions.interrupt.
type SessionInterruptInput struct {
	Session string   `json:"session"`
	Tags    []string `json:"tags,omitempty"`
}

// SessionKillInput is the kernel input for sessions.kill.
type SessionKillInput struct {
	Session   string   `json:"session"`
	Force     bool     `json:"force,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	NoHooks   bool     `json:"no_hooks,omitempty"`
	Summarize bool     `json:"summarize,omitempty"` // Generate summary before killing
}

func init() {
	// Register sessions.interrupt command
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.interrupt",
		Description: "Send Ctrl+C to all agent panes in a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionInterruptInput",
			Ref:  "cli.SessionInterruptInput",
		},
		Output: &kernel.SchemaRef{
			Name: "InterruptResponse",
			Ref:  "output.InterruptResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/sessions/{session}/interrupt",
		},
		Examples: []kernel.Example{
			{
				Name:        "interrupt",
				Description: "Send Ctrl+C to all agents",
				Command:     "ntm interrupt myproject",
			},
			{
				Name:        "interrupt-tags",
				Description: "Interrupt only panes with specific tag",
				Command:     "ntm interrupt myproject --tag=frontend",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.interrupt", func(ctx context.Context, input any) (any, error) {
		opts := SessionInterruptInput{}
		switch value := input.(type) {
		case SessionInterruptInput:
			opts = value
		case *SessionInterruptInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		return buildInterruptResponse(ctx, opts.Session, opts.Tags)
	})

	// Register sessions.kill command
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.kill",
		Description: "Kill a tmux session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionKillInput",
			Ref:  "cli.SessionKillInput",
		},
		Output: &kernel.SchemaRef{
			Name: "KillResponse",
			Ref:  "output.KillResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "DELETE",
			Path:   "/sessions/{session}",
		},
		Examples: []kernel.Example{
			{
				Name:        "kill",
				Description: "Kill a session (prompts confirmation)",
				Command:     "ntm kill myproject",
			},
			{
				Name:        "kill-force",
				Description: "Kill without confirmation",
				Command:     "ntm kill myproject --force",
			},
		},
		SafetyLevel: kernel.SafetyDanger,
		Idempotent:  true,
	})
	kernel.MustRegisterHandler("sessions.kill", func(ctx context.Context, input any) (any, error) {
		opts := SessionKillInput{}
		switch value := input.(type) {
		case SessionKillInput:
			opts = value
		case *SessionKillInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		return buildKillResponse(ctx, opts.Session, opts.Force, opts.Tags, opts.NoHooks, opts.Summarize)
	})
}

type sendExecutionPolicy uint8

const (
	sendExecutionEmit sendExecutionPolicy = iota
	sendExecutionCollect
)

// sendExecutionResult lets a composing command collect the terminal send
// receipt without changing the process-wide JSON mode or writing to stdout.
type sendExecutionResult struct {
	recorded  bool
	result    SendResult
	dryRun    bool
	wouldSend int
}

// SendOptions configures the send operation
type SendOptions struct {
	// Context is populated by command entry points. runSendWithTargets supplies
	// Background only for legacy in-process callers with no context surface.
	Context      context.Context
	Session      string
	Prompt       string
	PromptSource string
	BasePrompt   string // Prepended to all prompts (bd-3ejl)
	Targets      SendTargets
	TargetAll    bool
	// IncludeUser opts the user/control pane into a --all broadcast
	// (ntm-hykz). --all alone no longer touches the user pane: prompt text
	// typed into the operator's shell was one of the most-documented swarm
	// footguns (every broadcast recipe carried --skip-first to dodge it).
	IncludeUser bool
	// ClearInput performs the per-agent composer clear (Escape ritual + C-u)
	// with emptiness verification before typing each prompt (ntm-5p0b).
	ClearInput     bool
	SkipFirst      bool
	PaneSelector   string   // Explicit N, W.P, or %N selector from --pane
	PaneSelectors  []string // Explicit N, W.P, or %N selectors from --panes
	PanesSpecified bool     // True if --panes was explicitly set
	TemplateName   string
	Tags           []string
	DryRun         bool
	Randomize      bool  // Randomize send order for individualized prompts
	Seed           int64 // Deterministic seed (only used when Randomize=true)
	PriorityOrder  bool  // Sort batch prompts by priority (P0 first)
	PaceDispatch   bool  // Include advisory dispatch pacing in JSON/dry-run output

	// Runtime/test injection for advisory dispatch pacing.
	DispatchPacingInput *coordinator.DispatchPacingInput

	// Smart routing options
	SmartRoute    bool   // Use smart routing to select best agent
	RouteStrategy string // Routing strategy (least-loaded, round-robin, etc.)

	// CASS check options
	CassCheck      bool
	CassSimilarity float64
	CassCheckDays  int
	// LoopMode permits periodic orchestration nudges without using timestamp
	// suffixes to evade the advisory CASS duplicate-work prompt.
	LoopMode bool

	// ForceNonInteractive bypasses safe confirmation gates (currently the CASS
	// duplicate-work prompt) so a recovery/status wrapper can drive `ntm send`
	// without piping `y` through stdin. Destructive or ambiguous confirmation
	// classes are NOT bypassed by this flag — they fail closed.
	ForceNonInteractive bool

	// Hooks
	NoHooks bool

	// Batch processing options
	BatchFile       string        // Path to batch file
	BatchDelay      time.Duration // Delay between prompts
	BatchConfirm    bool          // Confirm each prompt before sending
	BatchStopOnErr  bool          // Stop on first error
	BatchBroadcast  bool          // Send same prompt to all agents simultaneously
	BatchAgentIndex int           // Send to specific agent index (-1 = round-robin)

	// Runtime: filled by smart routing
	routingResult *SendRoutingResult

	// Runtime: composing commands use collect mode to own terminal output.
	executionPolicy sendExecutionPolicy
	executionResult *sendExecutionResult
}

// SendTarget represents a send target with optional variant filter.
// Used for --cc:opus style flags where variant filters to specific model/persona.
type SendTarget struct {
	Type    AgentType
	Variant string // Empty = all agents of type; non-empty = filter by variant
}

// SendTargets is a slice of SendTarget that implements pflag.Value for accumulating
type SendTargets []SendTarget

func (s *SendTargets) String() string {
	if s == nil || len(*s) == 0 {
		return ""
	}
	var parts []string
	for _, t := range *s {
		if t.Variant != "" {
			parts = append(parts, fmt.Sprintf("%s:%s", t.Type, t.Variant))
		} else {
			parts = append(parts, string(t.Type))
		}
	}
	return strings.Join(parts, ",")
}

func (s *SendTargets) Set(value string) error {
	// Parse value as optional variant: "cc" or "cc:opus"
	parts := strings.SplitN(value, ":", 2)
	target := SendTarget{}
	if len(parts) > 1 && parts[1] != "" {
		target.Variant = parts[1]
	}
	// Type is set by the flag registration, value is just the variant
	*s = append(*s, target)
	return nil
}

func (s *SendTargets) Type() string {
	return "[variant]"
}

// sendTargetValue wraps SendTargets with a specific agent type for flag parsing
type sendTargetValue struct {
	agentType AgentType
	targets   *SendTargets
}

func newSendTargetValue(agentType AgentType, targets *SendTargets) *sendTargetValue {
	return &sendTargetValue{
		agentType: agentType,
		targets:   targets,
	}
}

func (v *sendTargetValue) String() string {
	return v.targets.String()
}

func (v *sendTargetValue) Set(value string) error {
	// When IsBoolFlag() is true, pflag passes "true" when the flag is present
	// without an explicit value (e.g. --cc). Treat that as "all variants".
	// If the user explicitly sets --cc=false, treat it as a no-op.
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "true":
		value = ""
	case "false":
		return nil
	}

	// Value is the variant (after the equals), or empty for all.
	target := SendTarget{
		Type:    v.agentType,
		Variant: value,
	}
	*v.targets = append(*v.targets, target)
	return nil
}

func (v *sendTargetValue) Type() string {
	return "[variant]"
}

// IsBoolFlag allows the flag to work with or without a value
// --cc sends to all Claude, --cc=opus sends to Claude with opus variant
func (v *sendTargetValue) IsBoolFlag() bool {
	return true
}

// HasTargetsForType checks if any targets match the given agent type
func (s SendTargets) HasTargetsForType(t AgentType) bool {
	for _, target := range s {
		if target.Type == t {
			return true
		}
	}
	return false
}

// MatchesPane checks if any target matches the given pane
func (s SendTargets) MatchesPane(pane tmux.Pane) bool {
	for _, target := range s {
		if matchesSendTarget(pane, target) {
			return true
		}
	}
	return false
}

// matchesSendTarget checks if a pane matches a send target.
func matchesSendTarget(pane tmux.Pane, target SendTarget) bool {
	if sendValueNotEqual(normalizeAgentType(string(pane.Type)), normalizeAgentType(string(target.Type))) {
		return false
	}
	if sendValueNotEqual(target.Variant, "") && sendValueNotEqual(pane.Variant, target.Variant) {
		return false
	}
	return true
}

func sendValueEqual[T comparable](left, right T) bool {
	return left == right
}

func sendValueNotEqual[T comparable](left, right T) bool {
	return !sendValueEqual(left, right)
}

func sendErrorIsNil(err error) bool {
	return err == nil
}

func matchesLegacySendTypeFilter(pane tmux.Pane, targetCC, targetCod, targetGmi bool) bool {
	switch tmux.AgentType(pane.Type).Canonical() {
	case tmux.AgentClaude:
		return targetCC
	case tmux.AgentCodex:
		return targetCod
	case tmux.AgentGemini:
		return targetGmi
	default:
		return false
	}
}

func isInterruptibleAgentPane(pane tmux.Pane) bool {
	switch tmux.AgentType(pane.Type).Canonical() {
	case tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini, tmux.AgentGrok, tmux.AgentCursor, tmux.AgentWindsurf, tmux.AgentAider, tmux.AgentOpencode, tmux.AgentOllama:
		return true
	default:
		return false
	}
}

func intsToStrings(ints []int) []string {
	out := make([]string, 0, len(ints))
	for _, v := range ints {
		out = append(out, fmt.Sprintf("%d", v))
	}
	return out
}

// shuffledPermutation returns a Fisher-Yates permutation of [0..n) using a deterministic PRNG.
// If seed is 0, it uses a time-based seed and returns the chosen seed via seedUsed.
func shuffledPermutation(n int, seed int64) (seedUsed int64, perm []int) {
	perm = make([]int, n)
	for i := 0; i < n; i++ {
		perm[i] = i
	}
	if n <= 1 {
		if seed == 0 {
			return time.Now().UnixNano(), perm
		}
		return seed, perm
	}

	seedUsed = seed
	if seedUsed == 0 {
		seedUsed = time.Now().UnixNano()
	}

	// xorshift64 (deterministic, stable across Go versions)
	x := uint64(seedUsed)
	if x == 0 {
		x = 0x9e3779b97f4a7c15
	}
	next := func() uint64 {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		return x
	}

	for i := n - 1; i > 0; i-- {
		j := int(next() % uint64(i+1))
		perm[i], perm[j] = perm[j], perm[i]
	}

	return seedUsed, perm
}

func permutePanes(panes []tmux.Pane, perm []int) []tmux.Pane {
	if len(panes) != len(perm) {
		return panes
	}
	out := make([]tmux.Pane, 0, len(panes))
	for _, idx := range perm {
		if idx < 0 || idx >= len(panes) {
			continue
		}
		out = append(out, panes[idx])
	}
	// If perm was malformed, fall back to original ordering.
	if len(out) != len(panes) {
		return panes
	}
	return out
}

func permuteBatchPrompts(prompts []BatchPrompt, perm []int) []BatchPrompt {
	if len(prompts) != len(perm) {
		return prompts
	}
	out := make([]BatchPrompt, 0, len(prompts))
	for _, idx := range perm {
		if idx < 0 || idx >= len(prompts) {
			continue
		}
		out = append(out, prompts[idx])
	}
	if len(out) != len(prompts) {
		return prompts
	}
	return out
}

func newSendCmd() *cobra.Command {
	var targets SendTargets
	var targetAll, includeUser, skipFirst, clearInput bool
	var paneSelector string
	var panesArg string
	var promptFile, prefix, suffix string
	var contextFiles []string
	var templateName string
	var templateVars []string
	var tags []string
	var dryRun bool
	var cassCheck bool
	var noCassCheck bool
	var loopMode bool
	var forceNonInteractive bool
	var cassSimilarity float64
	var cassCheckDays int
	var noHooks bool
	var smartRoute bool
	var routeStrategy string
	var distribute bool
	var distributeStrategy string
	var distributeLimit int
	var distributeAuto bool
	var randomize bool
	var seed int64
	var priorityOrder bool
	var paceDispatch bool
	var basePrompt string
	var basePromptFile string

	// Batch mode variables
	var batchFile string
	var batchDelay string
	var batchConfirm bool
	var batchStopOnErr bool
	var batchBroadcast bool
	var batchAgentIndex int

	// Project filter (bd-3cu02.14)
	var projectFilter string

	// Codex goal-send mode (#165)
	var codexGoal bool

	cmd := &cobra.Command{
		Use:   "send <session> [prompt]",
		Short: "Send a prompt to agent panes",
		Long: `Send a prompt or command to agent panes in a session.

		By default, sends to all agent panes. Use flags to target specific types.
		Use --cc=variant to filter by model or persona (e.g., --cc=opus, --cc=architect).
		Use --tag to filter by user-defined tags.

		Prompt can be provided as:
		  - Command line argument (traditional)
		  - From a file using --file
		  - From stdin when piped/redirected
		  - From a template using --template

		Template Usage:
		Use --template (-t) to use a named prompt template with variable substitution.
		Templates support {{variable}} placeholders and {{#var}}...{{/var}} conditionals.
		See 'ntm template list' for available templates.

		File Context Injection:
		Use --context (-c) to include file contents in the prompt. Files are prepended
		with headers and code fences. Supports line ranges: path:10-50, path:10-, path:-50

		When using --file or stdin, use --prefix and --suffix to wrap the content.

		Duplicate Detection:
		By default, checks CASS for similar past sessions to avoid duplicate work.
		Use --no-cass-check to skip.

		Non-interactive automation:
		Use --force-non-interactive to bypass safe confirmation gates (currently
		the CASS duplicate prompt) so recovery/status wrappers can drive 'ntm
		send' without piping 'y' through stdin. Destructive or ambiguous
		confirmation classes are NOT bypassed by this flag — they fail closed.
		When set, JSON output includes "non_interactive_forced": true.

		Smart Routing:
		Use --smart to automatically select the best agent based on routing strategies.
		Use --route to specify the strategy (default: least-loaded).
		Strategies: least-loaded, round-robin, affinity, sticky, random.

		Examples:
		  ntm send myproject "fix the linting errors"           # All agents
		  ntm send myproject --cc "review the changes"          # All Claude agents
		  ntm send myproject --cc=opus "review the changes"     # Only Claude Opus agents
		  ntm send myproject --tag=frontend "update ui"         # Agents with 'frontend' tag
		  ntm send myproject --cod --gmi "run the tests"        # Codex and Gemini
		  ntm send myproject --all "git status"                 # All panes
		  ntm send myproject --pane=2 "specific pane"           # Single-window pane index
		  ntm send myproject --pane=1.0 "specific pane"         # Exact window.pane
		  ntm send myproject --panes=%7,2.0 "two panes"         # Exact tmux ID + window.pane
		  ntm send myproject --skip-first "restart"             # Skip first topology-ordered pane
		  ntm send myproject --json "run tests"                 # JSON output
		  ntm send myproject --file prompts/review.md           # From file
		  cat error.log | ntm send myproject --cc               # From stdin
		  git diff | ntm send myproject --all --prefix "Review these changes:"  # Stdin with prefix
		  ntm send myproject -c src/auth.py "Refactor this"     # With file context
		  ntm send myproject -c src/api.go:10-50 "Review lines" # With line range
		  ntm send myproject -c a.go -c b.go "Compare these"    # Multiple files
		  ntm send myproject -t code_review --file src/main.go  # Template with file
		  ntm send myproject -t fix --var issue="null pointer" --file src/app.go  # Template with vars
		  ntm send myproject --smart "fix auth bug"             # Auto-select best agent
		  ntm send myproject --smart --route=affinity "auth"    # Use affinity strategy`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			failureSession := ""
			if projectFilter == "" && len(args) > 0 {
				failureSession = args[0]
			}
			earlyError := func(err error) error {
				return outputSendCommandError(failureSession, err)
			}
			paneSelector = strings.TrimSpace(paneSelector)
			panesSpecified := strings.TrimSpace(panesArg) != ""
			paneSelectors, err := parseShellPaneSelectors(panesArg)
			if err != nil {
				return earlyError(err)
			}
			if paneSelector != "" {
				if err := validateShellPaneSelector(paneSelector); err != nil {
					return earlyError(err)
				}
			}
			if paneSelector != "" && panesSpecified {
				return earlyError(fmt.Errorf("cannot use --pane and --panes together"))
			}
			if skipFirst && (paneSelector != "" || panesSpecified) {
				return earlyError(fmt.Errorf("cannot combine --skip-first with explicit --pane/--panes selectors"))
			}
			if skipFirst && smartRoute {
				return earlyError(fmt.Errorf("cannot combine --skip-first with --smart"))
			}

			// Handle --project mode: broadcast to all matching sessions (bd-3cu02.14)
			if projectFilter != "" {
				if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
					// Check if first arg looks like a session name (no spaces, no special chars)
					// If it could be a session name, error out
					if !strings.Contains(args[0], " ") {
						return earlyError(fmt.Errorf("cannot use --project with a specific session name; use just --project or just a session name"))
					}
				}
				return earlyError(runSendProject(cmd, projectFilter, args, targets, targetAll, includeUser, skipFirst, paneSelector, paneSelectors, panesSpecified, tags, noHooks, dryRun, forceNonInteractive))
			}

			if len(args) == 0 {
				return earlyError(fmt.Errorf("session name required (or use --project)"))
			}
			session := args[0]

			// Codex goal-send mode (#165): drive the Codex /goal slash command
			// flow instead of the generic prompt-paste path.
			if codexGoal {
				if panesSpecified {
					return earlyError(fmt.Errorf("--codex-goal requires exactly one --pane selector; --panes is not supported"))
				}
				body, _, err := getPromptContent(args[1:], promptFile, prefix, suffix)
				if err != nil {
					return earlyError(err)
				}
				return runCodexGoalSend(cmd.Context(), session, paneSelector, body)
			}

			// Resolve base prompt: flag > file > config (bd-3ejl)
			var cfgBasePrompt, cfgBasePromptFile string
			if cfg != nil {
				cfgBasePrompt = cfg.Send.BasePrompt
				cfgBasePromptFile = cfg.Send.BasePromptFile
			}
			resolvedBasePrompt, err := resolveBasePrompt(basePrompt, basePromptFile, cfgBasePrompt, cfgBasePromptFile)
			if err != nil {
				return earlyError(err)
			}

			// Handle --distribute mode: auto-distribute work from bv triage
			if distribute {
				if paneSelector != "" || panesSpecified {
					return earlyError(fmt.Errorf("cannot combine --distribute with --pane or --panes"))
				}
				if skipFirst {
					return earlyError(fmt.Errorf("cannot combine --distribute with --skip-first"))
				}
				if dryRun && distributeAuto {
					return earlyError(fmt.Errorf("cannot use --dry-run with --dist-auto"))
				}
				return runDistributeMode(cmd.Context(), session, distributeStrategy, distributeLimit, distributeAuto, dryRun, randomize, seed)
			}

			// Handle --batch mode: send multiple prompts from file
			if batchFile != "" {
				if paneSelector != "" || panesSpecified {
					return earlyError(fmt.Errorf("cannot combine --batch with --pane or --panes; use --agent for a specific batch target"))
				}
				var delay time.Duration
				if batchDelay != "" {
					var err error
					delay, err = time.ParseDuration(batchDelay)
					if err != nil {
						return earlyError(fmt.Errorf("invalid --delay value %q: %w", batchDelay, err))
					}
				}
				batchOpts := SendOptions{
					Context:             cmd.Context(),
					Session:             session,
					BasePrompt:          resolvedBasePrompt,
					Targets:             targets,
					TargetAll:           targetAll,
					IncludeUser:         includeUser,
					ClearInput:          clearInput,
					SkipFirst:           skipFirst,
					Tags:                tags,
					SmartRoute:          smartRoute,
					RouteStrategy:       routeStrategy,
					CassCheck:           cassCheck && !noCassCheck,
					LoopMode:            loopMode,
					CassSimilarity:      cassSimilarity,
					CassCheckDays:       cassCheckDays,
					ForceNonInteractive: forceNonInteractive,
					NoHooks:             noHooks,
					DryRun:              dryRun,
					BatchFile:           batchFile,
					BatchDelay:          delay,
					BatchConfirm:        batchConfirm,
					BatchStopOnErr:      batchStopOnErr,
					BatchBroadcast:      batchBroadcast,
					BatchAgentIndex:     batchAgentIndex,
					Randomize:           randomize,
					Seed:                seed,
					PriorityOrder:       priorityOrder,
					PaceDispatch:        paceDispatch,
				}
				return earlyError(runSendBatch(batchOpts))
			}

			opts := SendOptions{
				Context:             cmd.Context(),
				Session:             session,
				BasePrompt:          resolvedBasePrompt,
				Targets:             targets,
				TargetAll:           targetAll,
				IncludeUser:         includeUser,
				ClearInput:          clearInput,
				SkipFirst:           skipFirst,
				PaneSelector:        paneSelector,
				PaneSelectors:       paneSelectors,
				PanesSpecified:      panesSpecified,
				Tags:                tags,
				SmartRoute:          smartRoute,
				RouteStrategy:       routeStrategy,
				CassCheck:           cassCheck && !noCassCheck,
				LoopMode:            loopMode,
				CassSimilarity:      cassSimilarity,
				CassCheckDays:       cassCheckDays,
				ForceNonInteractive: forceNonInteractive,
				NoHooks:             noHooks,
				DryRun:              dryRun,
				Randomize:           randomize,
				Seed:                seed,
				PaceDispatch:        paceDispatch,
			}

			// Handle template-based prompts
			if templateName != "" {
				opts.TemplateName = templateName
				opts.PromptSource = fmt.Sprintf("template:%s", templateName)
				return earlyError(runSendWithTemplate(templateVars, promptFile, contextFiles, opts))
			}

			promptText, promptSource, err := getPromptContent(args[1:], promptFile, prefix, suffix)
			if err != nil {
				return earlyError(err)
			}

			// Inject file context if specified
			if len(contextFiles) > 0 {
				var specs []prompt.FileSpec
				for _, cf := range contextFiles {
					spec, err := prompt.ParseFileSpec(cf)
					if err != nil {
						return earlyError(fmt.Errorf("invalid --context spec '%s': %w", cf, err))
					}
					specs = append(specs, spec)
				}

				promptText, err = prompt.InjectFiles(specs, promptText)
				if err != nil {
					return earlyError(err)
				}
			}

			opts.Prompt = promptText
			opts.PromptSource = promptSource
			return earlyError(runSendWithTargets(opts))
		},
	}

	// Use custom flag values that support --cc or --cc=variant syntax
	// NoOptDefVal must be set explicitly for pflag to honor IsBoolFlag() on custom Var types
	cmd.Flags().Var(newSendTargetValue(AgentTypeClaude, &targets), "cc", "send to Claude agents (optional :variant filter)")
	cmd.Flags().Lookup("cc").NoOptDefVal = "true"
	cmd.Flags().Var(newSendTargetValue(AgentTypeCodex, &targets), "cod", "send to Codex agents (optional :variant filter)")
	cmd.Flags().Lookup("cod").NoOptDefVal = "true"
	cmd.Flags().Var(newSendTargetValue(AgentTypeGemini, &targets), "gmi", "send to Gemini agents (optional :variant filter)")
	cmd.Flags().Lookup("gmi").NoOptDefVal = "true"
	cmd.Flags().Var(newSendTargetValue(AgentTypeAntigravity, &targets), "agy", "send to Antigravity (agy) agents (optional :variant filter)")
	cmd.Flags().Lookup("agy").NoOptDefVal = "true"
	cmd.Flags().BoolVar(&targetAll, "all", false, "send to all agent panes, overriding type/tag filters (the user pane is excluded unless --include-user)")
	cmd.Flags().BoolVar(&includeUser, "include-user", false, "opt the user/control pane into a --all broadcast (deliberate shell input only)")
	cmd.Flags().BoolVar(&clearInput, "clear-input", false, "clear residual composer text (per-agent Escape ritual + C-u, verified) before typing each prompt; recommended after interrupts on codex panes")
	cmd.Flags().BoolVarP(&skipFirst, "skip-first", "s", false, "skip the first pane in deterministic topology order")
	cmd.Flags().StringVarP(&paneSelector, "pane", "p", "", "send to one pane (N, W.P, or %N)")
	cmd.Flags().StringVarP(&panesArg, "panes", "", "", "send to panes (comma-separated N, W.P, or %N selectors)")
	cmd.Flags().StringVarP(&promptFile, "file", "f", "", "read prompt from file (also used as {{file}} in templates)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "text to prepend to file/stdin content")
	cmd.Flags().StringVar(&suffix, "suffix", "", "text to append to file/stdin content")
	cmd.Flags().StringArrayVarP(&contextFiles, "context", "c", nil, "file to include as context (repeatable, supports path:start-end)")
	cmd.Flags().StringVarP(&templateName, "template", "t", "", "use a named prompt template (see 'ntm template list')")
	cmd.Flags().StringArrayVar(&templateVars, "var", nil, "template variable in key=value format (repeatable)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter by tag (OR logic)")

	// Smart routing flags
	cmd.Flags().BoolVar(&smartRoute, "smart", false, "Use smart routing to select best agent")
	cmd.Flags().StringVar(&routeStrategy, "route", "", "Routing strategy: least-loaded, round-robin, affinity, sticky, random")

	// Distribute mode flags - auto-distribute work from bv triage to agents
	cmd.Flags().BoolVar(&distribute, "distribute", false, "Auto-distribute prioritized work from bv triage to idle agents")
	cmd.Flags().StringVar(&distributeStrategy, "dist-strategy", "balanced", "Distribution strategy: balanced, speed, quality, dependency")
	cmd.Flags().IntVar(&distributeLimit, "dist-limit", 0, "Max tasks to distribute (0 = one per idle agent)")
	cmd.Flags().BoolVar(&distributeAuto, "dist-auto", false, "Execute distribution without confirmation")

	// CASS check flags
	cmd.Flags().BoolVar(&cassCheck, "cass-check", true, "Check for duplicate work in CASS")
	cmd.Flags().BoolVar(&noCassCheck, "no-cass-check", false, "Skip CASS duplicate check")
	cmd.Flags().BoolVar(&loopMode, "loop-mode", false, "Allow repeated orchestration nudges without a CASS duplicate prompt")
	cmd.Flags().BoolVar(&forceNonInteractive, "force-non-interactive", false,
		"Bypass safe confirmation gates (currently the CASS duplicate prompt) for "+
			"recovery/status automation. Destructive or ambiguous classes are NOT "+
			"bypassed — they fail closed. Sets non_interactive_forced=true in JSON output.")
	cmd.Flags().Float64Var(&cassSimilarity, "cass-similarity", 0.7, "Similarity threshold for duplicate detection")
	cmd.Flags().IntVar(&cassCheckDays, "cass-check-days", 7, "Look back N days for duplicates")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be sent without sending")

	// Randomization flags
	cmd.Flags().BoolVar(&randomize, "randomize", false, "Randomize send order for individualized prompts (reduces thundering herd)")
	cmd.Flags().Int64Var(&seed, "seed", 0, "Deterministic seed for --randomize (0 = time-based)")
	cmd.Flags().BoolVar(&paceDispatch, "pace-dispatch", false, "Include advisory dispatch pacing in JSON and dry-run output without changing send behavior")

	// Priority ordering flag (bd-2wzs)
	cmd.Flags().BoolVar(&priorityOrder, "priority-order", false, "Sort batch prompts by priority (P0 first, annotate with '# priority: N')")

	// Base prompt flags (bd-3ejl) - prepend common instructions to all prompts
	cmd.Flags().StringVar(&basePrompt, "base-prompt", "", "Text to prepend to all prompts")
	cmd.Flags().StringVar(&basePromptFile, "base-prompt-file", "", "File whose contents are prepended to all prompts")

	// Batch mode flags - send multiple prompts from file
	cmd.Flags().StringVar(&batchFile, "batch", "", "Read prompts from file (one per line or --- separated)")
	cmd.Flags().StringVar(&batchDelay, "delay", "", "Delay between prompts (e.g., 5s, 100ms)")
	cmd.Flags().BoolVar(&batchConfirm, "confirm-each", false, "Confirm each prompt before sending")
	cmd.Flags().BoolVar(&batchStopOnErr, "stop-on-error", false, "Stop batch on first send failure")
	cmd.Flags().BoolVar(&batchBroadcast, "broadcast", false, "Send same prompt to all agents simultaneously")
	cmd.Flags().IntVar(&batchAgentIndex, "agent", -1, "Send to specific agent index only (-1 = round-robin)")

	// Project filter (bd-3cu02.14)
	cmd.Flags().StringVar(&projectFilter, "project", "", "broadcast to all sessions for a base project name")

	// Codex goal-send mode (#165): drive the Codex /goal slash command flow.
	cmd.Flags().BoolVar(&codexGoal, "codex-goal", false,
		"Drive the Codex /goal slash-command flow on --pane: type /goal, wait for the "+
			"goal palette to engage, inject the packet body, submit, and emit a JSON "+
			"receipt. Requires --pane and a codex-live pane. Use with --file for the packet.")

	cmd.ValidArgsFunction = completeSessionArgs
	_ = cmd.RegisterFlagCompletionFunc("pane", completeSendPaneSelector)
	_ = cmd.RegisterFlagCompletionFunc("panes", completeSendPaneSelectors)

	return cmd
}

// runSendProject broadcasts a prompt to all sessions matching a base project (bd-3cu02.14).
func runSendProject(cmd *cobra.Command, project string, args []string, targets SendTargets, targetAll, includeUser, skipFirst bool, paneSelector string, paneSelectors []string, panesSpecified bool, tags []string, noHooks, dryRun, forceNonInteractive bool) error {
	outputError := func(err error) error {
		if !jsonOutput {
			return err
		}
		result := buildSendProjectResult(project, []sendProjectSessionResult{})
		result.Success = false
		result.ErrorCode = sendErrorCodeFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.ErrorCode = robot.ErrCodeTimeout
		}
		result.Error = err.Error()
		return emitSendProjectResult(result, err)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	sessions, err := tmux.ListSessionsContext(cmd.Context())
	if err != nil {
		return outputError(err)
	}

	var matching []tmux.Session
	for _, s := range sessions {
		if sendValueEqual(config.SessionBase(s.Name), project) {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Name < matching[j].Name })

	if len(matching) == 0 {
		return outputError(fmt.Errorf("no sessions found for project %q", project))
	}

	// Build prompt from remaining args
	promptText := strings.Join(args, " ")
	if strings.TrimSpace(promptText) == "" {
		return outputError(fmt.Errorf("prompt text required"))
	}

	if jsonOutput {
		return runSendProjectJSON(cmd.Context(), project, promptText, matching, targets, targetAll, includeUser, skipFirst, paneSelector, paneSelectors, panesSpecified, tags, noHooks, dryRun, forceNonInteractive)
	}

	var names []string
	for _, s := range matching {
		names = append(names, s.Name)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Sending to %d session(s): %s\n", len(matching), strings.Join(names, ", "))

	var sendErrors []string
	delivered := 0
	for _, s := range matching {
		if err := cmd.Context().Err(); err != nil {
			return fmt.Errorf("project send canceled: %w", err)
		}
		opts := SendOptions{
			Context:             cmd.Context(),
			Session:             s.Name,
			Prompt:              promptText,
			Targets:             targets,
			TargetAll:           targetAll,
			IncludeUser:         includeUser,
			SkipFirst:           skipFirst,
			PaneSelector:        paneSelector,
			PaneSelectors:       paneSelectors,
			PanesSpecified:      panesSpecified,
			Tags:                tags,
			NoHooks:             noHooks,
			DryRun:              dryRun,
			ForceNonInteractive: forceNonInteractive,
		}
		if err := runSendWithTargets(opts); err != nil {
			sendErrors = append(sendErrors, fmt.Sprintf("%s: %v", s.Name, err))
		} else {
			delivered++
		}
	}

	if len(sendErrors) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Delivered to %d/%d sessions. Errors: %s\n", delivered, len(matching), strings.Join(sendErrors, "; "))
		return fmt.Errorf("project send failed for %d of %d sessions", len(sendErrors), len(matching))
	}

	return nil
}

func runSendProjectJSON(ctx context.Context, project, prompt string, sessions []tmux.Session, targets SendTargets, targetAll, includeUser, skipFirst bool, paneSelector string, paneSelectors []string, panesSpecified bool, tags []string, noHooks, dryRun, forceNonInteractive bool) error {
	results := make([]sendProjectSessionResult, 0, len(sessions))
	var firstErr error

	appendCanceled := func(pending []tmux.Session, cancelErr error) {
		for _, session := range pending {
			results = append(results, sendProjectSessionResult{
				Session:   session.Name,
				Success:   false,
				Targets:   []string{},
				ErrorCode: robot.ErrCodeTimeout,
				Error:     cancelErr.Error(),
			})
		}
	}

	for index, session := range sessions {
		if ctxErr := ctx.Err(); ctxErr != nil {
			cancelErr := fmt.Errorf("project send canceled: %w", ctxErr)
			if firstErr == nil {
				firstErr = cancelErr
			}
			appendCanceled(sessions[index:], cancelErr)
			break
		}

		collected := &sendExecutionResult{}
		opts := SendOptions{
			Context:             ctx,
			Session:             session.Name,
			Prompt:              prompt,
			Targets:             targets,
			TargetAll:           targetAll,
			IncludeUser:         includeUser,
			SkipFirst:           skipFirst,
			PaneSelector:        paneSelector,
			PaneSelectors:       paneSelectors,
			PanesSpecified:      panesSpecified,
			Tags:                tags,
			NoHooks:             noHooks,
			DryRun:              dryRun,
			ForceNonInteractive: forceNonInteractive,
			executionPolicy:     sendExecutionCollect,
			executionResult:     collected,
		}
		sendErr := runSendWithTargets(opts)
		result, resultErr := sendProjectSessionFromExecution(session.Name, collected, sendErr)
		results = append(results, result)
		if firstErr == nil && resultErr != nil {
			firstErr = resultErr
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			cancelErr := fmt.Errorf("project send canceled: %w", ctxErr)
			if firstErr == nil {
				firstErr = cancelErr
			}
			appendCanceled(sessions[index+1:], cancelErr)
			break
		}
	}

	return emitSendProjectResult(buildSendProjectResult(project, results), firstErr)
}

func sendProjectSessionFromExecution(session string, collected *sendExecutionResult, sendErr error) (sendProjectSessionResult, error) {
	result := sendProjectSessionResult{
		Session: session,
		Targets: []string{},
	}
	if collected != nil && collected.recorded {
		result.Success = collected.result.Success
		result.Targets = append([]string(nil), collected.result.Targets...)
		result.Delivered = collected.result.Delivered
		result.Failed = collected.result.Failed
		result.DryRun = collected.dryRun
		result.WouldSend = collected.wouldSend
		result.ErrorCode = collected.result.ErrorCode
		result.Error = collected.result.Error
	} else if sendErr == nil {
		sendErr = errors.New("send completed without a terminal result")
	}

	if sendErr != nil {
		result.Success = false
		if result.Error == "" {
			result.Error = sendErr.Error()
		}
		if errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded) {
			result.ErrorCode = robot.ErrCodeTimeout
		} else if result.ErrorCode == "" {
			result.ErrorCode = sendErrorCodeFailed
		}
	}
	if result.Targets == nil {
		result.Targets = []string{}
	}
	return result, sendErr
}

func buildSendProjectResult(project string, sessions []sendProjectSessionResult) sendProjectResult {
	if sessions == nil {
		sessions = []sendProjectSessionResult{}
	} else {
		sessions = append([]sendProjectSessionResult(nil), sessions...)
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Session < sessions[j].Session })
	result := sendProjectResult{
		TimestampedResponse: output.NewTimestamped(),
		Success:             true,
		Project:             project,
		Sessions:            sessions,
		MatchedSessions:     len(sessions),
	}
	for index := range result.Sessions {
		session := &result.Sessions[index]
		if session.Targets == nil {
			session.Targets = []string{}
		}
		result.Delivered += session.Delivered
		result.FailedDeliveries += session.Failed
		if session.Success {
			result.SucceededSessions++
			continue
		}
		result.Success = false
		result.FailedSessions++
		if session.ErrorCode == robot.ErrCodeTimeout {
			result.ErrorCode = robot.ErrCodeTimeout
		}
	}
	if !result.Success {
		if result.ErrorCode == "" {
			result.ErrorCode = sendErrorCodeFailed
		}
		result.Error = fmt.Sprintf("%d of %d session sends failed", result.FailedSessions, result.MatchedSessions)
	}
	return result
}

func emitSendProjectResult(result sendProjectResult, cause error) error {
	if result.Success {
		return output.PrintJSON(result)
	}
	emitErr := emitJSONFailureEnvelope(result)
	if !errors.Is(emitErr, errJSONFailure) {
		return emitErr
	}
	if cause == nil {
		cause = errors.New(result.Error)
	}
	return errors.Join(emitErr, cause)
}

// getPromptContent resolves the prompt content from various sources:
// 1. If --file is specified, read from that file
// 2. If stdin has data (piped/redirected), read from stdin
// 3. Otherwise, use positional arguments
// The prefix and suffix are applied when reading from file or stdin.
// readPromptFileOrStdin reads a prompt payload from a path, with "-" meaning
// stdin (ntm-kd2s / AP-52: file- and stdin-delivered payloads stay out of the
// command line that dcg-style safety filters scan).
func readPromptFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		// Read one byte past the limit so oversize input is a loud error
		// instead of a silent mid-prompt truncation (mirrors
		// loadRobotSendMessage's overflow detection).
		const limit = 10 * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(os.Stdin, limit+1))
		if err != nil {
			return nil, err
		}
		if len(data) > limit {
			return nil, fmt.Errorf("stdin prompt too large (max 10MB)")
		}
		return data, nil
	}
	return os.ReadFile(path)
}

func getPromptContent(args []string, promptFile, prefix, suffix string) (string, string, error) {
	var content string

	// Priority 1: Read from file if specified. "-" reads stdin (ntm-kd2s):
	// keeps destructive-looking prompt TEXT out of the scanned command line
	// so dcg-style command filters cannot mistake message content for an
	// executable command.
	if promptFile != "" {
		data, err := readPromptFileOrStdin(promptFile)
		if err != nil {
			return "", "", fmt.Errorf("reading prompt file: %w", err)
		}
		content = string(data)
		if strings.TrimSpace(content) == "" {
			return "", "", errors.New("prompt file is empty")
		}
		// Apply prefix/suffix for file content
		return buildPrompt(content, prefix, suffix), "file:" + promptFile, nil
	}

	// Priority 2: Read from stdin if piped/redirected AND we have no args
	// (If args are provided, they take priority over stdin)
	if len(args) == 0 && stdinHasData() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", fmt.Errorf("reading from stdin: %w", err)
		}
		content = string(data)
		// Allow empty stdin if we have a prefix (e.g., just sending a command)
		if strings.TrimSpace(content) == "" && prefix == "" {
			return "", "", errors.New("stdin is empty and no prefix provided")
		}
		// Apply prefix/suffix for stdin content
		return buildPrompt(content, prefix, suffix), "stdin", nil
	}

	// Priority 3: Use positional arguments
	if len(args) == 0 {
		return "", "", errors.New("no prompt provided (use argument, --file, or pipe to stdin)")
	}
	content = strings.Join(args, " ")
	// For positional args, prefix/suffix are ignored (they're for file/stdin)
	return content, "args", nil
}

// stdinHasData checks if stdin has data available (is piped/redirected)
func stdinHasData() bool {
	// Check if stdin is a terminal - if it is, there's no piped data
	if isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return false
	}
	// Check if stdin has actual data using Stat
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	// Check if it's a named pipe (FIFO) or has data waiting
	// ModeCharDevice is 0 when stdin is redirected/piped
	return (stat.Mode() & os.ModeCharDevice) == 0
}

// resolveBasePrompt determines the base prompt from flags and config.
// Priority: flag > file flag > config string > config file > empty.
func resolveBasePrompt(flagValue, flagFile, cfgValue, cfgFile string) (string, error) {
	// Explicit --base-prompt flag takes highest priority
	if flagValue != "" {
		return flagValue, nil
	}
	// --base-prompt-file flag
	if flagFile != "" {
		data, err := os.ReadFile(flagFile)
		if err != nil {
			return "", fmt.Errorf("reading --base-prompt-file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	// Config string
	if cfgValue != "" {
		return cfgValue, nil
	}
	// Config file
	if cfgFile != "" {
		data, err := os.ReadFile(cfgFile)
		if err != nil {
			return "", fmt.Errorf("reading send.base_prompt_file from config: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return "", nil
}

// applyBasePrompt prepends a base prompt to a user prompt with a blank-line separator.
// Returns userPrompt unchanged if basePrompt is empty.
func applyBasePrompt(basePrompt, userPrompt string) string {
	if basePrompt == "" {
		return userPrompt
	}
	if userPrompt == "" {
		return basePrompt
	}
	return basePrompt + "\n\n" + userPrompt
}

// buildPrompt combines prefix, content, and suffix into a single prompt string.
func buildPrompt(content, prefix, suffix string) string {
	var parts []string
	if prefix != "" {
		parts = append(parts, prefix)
	}
	parts = append(parts, strings.TrimSpace(content))
	if suffix != "" {
		parts = append(parts, suffix)
	}
	return strings.Join(parts, "\n")
}

// runSendWithTemplate handles template-based prompt generation and sending.
func runSendWithTemplate(templateVars []string, promptFile string, contextFiles []string, opts SendOptions) error {
	// Load the template
	loader := templates.NewLoader()
	tmpl, err := loader.Load(opts.TemplateName)
	if err != nil {
		return fmt.Errorf("loading template '%s': %w", opts.TemplateName, err)
	}

	// Parse template variables from --var flags
	vars := make(map[string]string)
	for _, v := range templateVars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid --var format '%s' (expected key=value)", v)
		}
		vars[parts[0]] = parts[1]
	}

	// Build execution context
	ctx := templates.ExecutionContext{
		Variables: vars,
		Session:   opts.Session,
	}

	// Read file content if --file specified (used as {{file}} variable).
	// "-" reads stdin (ntm-kd2s).
	if promptFile != "" {
		content, err := readPromptFileOrStdin(promptFile)
		if err != nil {
			return fmt.Errorf("reading file '%s': %w", promptFile, err)
		}
		ctx.FileContent = string(content)
	}

	// Execute the template
	promptText, err := tmpl.Execute(ctx)
	if err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	// Inject additional file context if specified (via --context)
	if len(contextFiles) > 0 {
		var specs []prompt.FileSpec
		for _, cf := range contextFiles {
			spec, err := prompt.ParseFileSpec(cf)
			if err != nil {
				return fmt.Errorf("invalid --context spec '%s': %w", cf, err)
			}
			specs = append(specs, spec)
		}

		promptText, err = prompt.InjectFiles(specs, promptText)
		if err != nil {
			return err
		}
	}

	opts.Prompt = promptText
	return runSendWithTargets(opts)
}

// runSendWithTargets sends prompts using the new SendTargets filtering
func runSendWithTargets(opts SendOptions) error {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	return runSendInternal(opts)
}

func finishSendResult(opts SendOptions, result SendResult, cause error) error {
	if result.Targets == nil {
		result.Targets = []string{}
	}
	if opts.executionPolicy == sendExecutionCollect {
		if opts.executionResult != nil {
			opts.executionResult.recorded = true
			opts.executionResult.result = result
			opts.executionResult.dryRun = false
			opts.executionResult.wouldSend = 0
		}
		return cause
	}
	if !jsonOutput {
		return cause
	}
	if result.Success {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if cause == nil {
		cause = errors.New(result.Error)
	}
	emitErr := emitJSONFailureEnvelope(result)
	if !errors.Is(emitErr, errJSONFailure) {
		return emitErr
	}
	return errors.Join(emitErr, cause)
}

func finishSendDryRunResult(opts SendOptions, result SendDryRunResult) error {
	if result.WouldSend == nil {
		result.WouldSend = []SendDryRunEntry{}
	}
	if opts.executionPolicy != sendExecutionCollect {
		return printSendDryRunResult(result)
	}

	targets := make([]string, 0, len(result.WouldSend))
	for _, entry := range result.WouldSend {
		targets = append(targets, entry.Pane)
	}
	if opts.executionResult != nil {
		opts.executionResult.recorded = true
		opts.executionResult.result = SendResult{
			Success:              result.Success,
			Session:              result.Session,
			NonInteractiveForced: result.NonInteractiveForced,
			Redaction:            result.Redaction,
			Warnings:             result.Warnings,
			Blocked:              result.Blocked,
			ErrorCode:            result.ErrorCode,
			Targets:              targets,
			RoutedTo:             result.RoutedTo,
			DispatchPacing:       result.DispatchPacing,
			Error:                result.Error,
		}
		opts.executionResult.dryRun = true
		opts.executionResult.wouldSend = result.Total
	}
	return nil
}

func parseShellPaneSelectors(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	selectors := make([]string, 0, len(parts))
	for _, part := range parts {
		selector := strings.TrimSpace(part)
		if selector == "" {
			return nil, fmt.Errorf("invalid empty pane selector in %q", raw)
		}
		if err := validateShellPaneSelector(selector); err != nil {
			return nil, err
		}
		selectors = append(selectors, selector)
	}
	return selectors, nil
}

func validateShellPaneSelector(selector string) error {
	_, err := tmux.ParsePaneSelector(selector)
	return err
}

func sortPanesByTopology(panes []tmux.Pane) []tmux.Pane {
	return tmux.SortPanesByTopology(panes)
}

func resolveShellSendSelectors(panes []tmux.Pane, selectors []string, singular bool) ([]tmux.Pane, error) {
	return tmux.ResolvePaneSelectors(panes, selectors, singular)
}

func runSendInternal(opts SendOptions) (err error) {
	ctx := opts.Context
	session := opts.Session
	prompt := applyBasePrompt(opts.BasePrompt, opts.Prompt)
	opts.Prompt = prompt // update opts so downstream sees combined prompt
	promptSource := opts.PromptSource
	templateName := opts.TemplateName
	targets := opts.Targets
	targetAll := opts.TargetAll
	skipFirst := opts.SkipFirst
	paneIndex := -1
	paneSelector := strings.TrimSpace(opts.PaneSelector)
	tags := opts.Tags
	dryRun := opts.DryRun
	silent := opts.executionPolicy == sendExecutionCollect

	// Convert to the old signature for backwards compatibility if needed locally
	targetCC := targets.HasTargetsForType(AgentTypeClaude)
	targetCod := targets.HasTargetsForType(AgentTypeCodex)
	targetGmi := targets.HasTargetsForType(AgentTypeGemini)
	targetAgy := targets.HasTargetsForType(AgentTypeAntigravity)

	// Helper for JSON error output
	var (
		histTargets    []string
		histAgentTypes []string
		histErr        error
		histSuccess    bool
	)

	// Redaction preflight for outbound prompts
	var (
		redactionSummary  *RedactionSummary
		redactionWarnings []string
		redactionBlocked  bool
	)

	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		if redactCfg.Mode != redaction.ModeOff {
			result := redaction.ScanAndRedact(prompt, redactCfg)
			if len(result.Findings) > 0 {
				summary := summarizeRedactionResult(result)
				redactionSummary = &summary

				switch result.Mode {
				case redaction.ModeWarn:
					msg := "Warning: potential secrets detected in prompt"
					if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
						msg = fmt.Sprintf("%s (%s)", msg, parts)
					}
					redactionWarnings = append(redactionWarnings, msg)
					if !jsonOutput && !silent {
						fmt.Fprintln(os.Stderr, msg)
					}
				case redaction.ModeRedact:
					prompt = result.Output
					opts.Prompt = prompt
					msg := "Warning: redacted potential secrets in prompt"
					if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
						msg = fmt.Sprintf("%s (%s)", msg, parts)
					}
					redactionWarnings = append(redactionWarnings, msg)
					if !jsonOutput && !silent {
						fmt.Fprintln(os.Stderr, msg)
					}
				case redaction.ModeBlock:
					// Avoid persisting raw secrets in history/session prompt store by replacing
					// the in-memory prompt with a redacted preview before returning the error.
					previewCfg := redactCfg
					previewCfg.Mode = redaction.ModeRedact
					previewRes := redaction.ScanAndRedact(prompt, previewCfg)
					prompt = previewRes.Output
					opts.Prompt = prompt

					redactionBlocked = true
					msg := "Blocked: potential secrets detected in prompt (redaction mode: block)"
					if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
						msg = fmt.Sprintf("%s (%s)", msg, parts)
					}
					redactionWarnings = append(redactionWarnings, msg)
				}
			}
		}
	}

	delivered := 0
	failed := 0
	seedUsed := int64(0)

	outputError := func(err error) error {
		histErr = err
		if jsonOutput || opts.executionPolicy == sendExecutionCollect {
			code := ""
			if redactionBlocked {
				code = "SENSITIVE_DATA_BLOCKED"
			} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				code = robot.ErrCodeTimeout
			}
			result := SendResult{
				Success:              false,
				Session:              session,
				NonInteractiveForced: opts.ForceNonInteractive,
				Redaction: func() *RedactionSummary {
					if redactionSummary == nil {
						return nil
					}
					cp := *redactionSummary
					return &cp
				}(),
				Warnings:  redactionWarnings,
				Blocked:   redactionBlocked,
				ErrorCode: code,
				Targets:   []string{},
				Error:     err.Error(),
			}
			return finishSendResult(opts, result, err)
		}
		return err
	}
	if paneSelector != "" {
		if selectorErr := validateShellPaneSelector(paneSelector); selectorErr != nil {
			return outputError(selectorErr)
		}
	}
	if opts.PanesSpecified && len(opts.PaneSelectors) == 0 {
		return outputError(fmt.Errorf("--panes requires at least one pane selector"))
	}
	if skipFirst && (paneSelector != "" || opts.PanesSpecified) {
		return outputError(fmt.Errorf("cannot combine --skip-first with explicit --pane/--panes selectors"))
	}

	if redactionBlocked {
		return outputError(redactionBlockedError{summary: *redactionSummary})
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("send canceled: %w", err))
	}

	sessionInferred, err := false, error(nil)
	session, sessionInferred, err = resolveSendSessionForCommandContext(ctx, session)
	if err != nil {
		return outputError(err)
	}
	opts.Session = session

	// Audit: send command start (redacted preview only)
	_ = audit.LogEvent(session, audit.EventTypeSend, audit.ActorUser, "send", map[string]interface{}{
		"phase":          "start",
		"prompt_preview": truncateForPreview(prompt, 80),
		"prompt_length":  len(prompt),
		"prompt_source":  promptSource,
		"template":       templateName,
		"targets":        buildSendTargetDescription(targetCC, targetCod, targetGmi, targetAgy, targetAll, skipFirst, paneIndex, paneSelector, opts.PaneSelectors, opts.PanesSpecified, tags),
		"dry_run":        dryRun,
		"randomize":      opts.Randomize,
		"seed":           opts.Seed,
		"correlation_id": auditCorrelationID,
	}, nil)

	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"prompt_preview": truncateForPreview(prompt, 80),
			"prompt_length":  len(prompt),
			"delivered":      delivered,
			"failed":         failed,
			"dry_run":        dryRun,
			"success":        sendErrorIsNil(err),
			"correlation_id": auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(session, audit.EventTypeSend, audit.ActorUser, "send", payload, nil)
	}()

	// Start time tracking for history
	start := time.Now()

	// Defer history logic
	defer func() {
		if dryRun {
			return
		}
		entry := history.NewEntry(session, histTargets, prompt, history.SourceCLI)
		entry.SetAgentTypes(histAgentTypes)
		entry.Template = templateName
		entry.DurationMs = int(time.Since(start) / time.Millisecond)
		if histSuccess {
			entry.SetSuccess()
		} else {
			entry.SetError(histErr)
		}
		_ = history.Append(entry)

		// Session prompt history is replayable restart state, so only record
		// prompts that reached at least one pane. Global history above retains
		// failed attempts with their error metadata.
		promptEntry := sessionPkg.PromptEntry{
			Session:  session,
			Content:  prompt,
			Targets:  histTargets,
			Source:   "cli",
			Template: templateName,
		}
		_ = saveDeliveredPrompt(delivered, promptEntry)
	}()

	// Smart routing: select best agent automatically.
	// Explicit pane selection (--pane/--panes) wins over automatic routing.
	if opts.SmartRoute && (opts.PanesSpecified || paneSelector != "") {
		if !jsonOutput && !silent {
			if opts.PanesSpecified {
				fmt.Println("Note: --panes specified, skipping smart routing")
			} else {
				fmt.Println("Note: --pane specified, skipping smart routing")
			}
		}
		opts.SmartRoute = false
	}
	if opts.SmartRoute {
		strategy := robot.StrategyLeastLoaded
		if opts.RouteStrategy != "" {
			strategy = robot.StrategyName(opts.RouteStrategy)
			if !robot.IsValidStrategy(strategy) {
				validNames := robot.GetStrategyNames()
				validStrs := make([]string, len(validNames))
				for i, n := range validNames {
					validStrs[i] = string(n)
				}
				return outputError(fmt.Errorf("invalid routing strategy: %s (valid: %s)",
					opts.RouteStrategy, strings.Join(validStrs, ", ")))
			}
		}

		routeOpts := robot.RouteOptions{
			Session:  session,
			Strategy: strategy,
			Prompt:   prompt,
			Config:   loadSelectedConfigOrDefault(),
		}

		// Filter by agent type if specified (only when exactly one type is set)
		if targetCC && !targetCod && !targetGmi && !targetAgy {
			routeOpts.AgentType = "claude"
		} else if targetCod && !targetCC && !targetGmi && !targetAgy {
			routeOpts.AgentType = "codex"
		} else if targetGmi && !targetCC && !targetCod && !targetAgy {
			routeOpts.AgentType = "gemini"
		} else if targetAgy && !targetCC && !targetCod && !targetGmi {
			routeOpts.AgentType = "antigravity"
		}

		recommendation, err := robot.GetRouteRecommendation(routeOpts)
		if err != nil {
			return outputError(fmt.Errorf("smart routing failed: %w", err))
		}
		if recommendation == nil {
			return outputError(fmt.Errorf("smart routing: no available agent found"))
		}

		// Set target to the recommended pane
		paneIndex = recommendation.PaneIndex
		paneSelector = recommendation.PaneID
		opts.routingResult = &SendRoutingResult{
			PaneIndex: recommendation.PaneIndex,
			PaneID:    recommendation.PaneID,
			AgentType: recommendation.AgentType,
			Strategy:  string(strategy),
			Reason:    recommendation.Reason,
			Score:     recommendation.Score,
		}

		if !jsonOutput && !silent {
			fmt.Printf("Smart routing: selected %s (pane %d) - %s\n",
				recommendation.AgentType, recommendation.PaneIndex, recommendation.Reason)
		}
	}

	// Resolve explicit selectors before duplicate checks, hooks, checkpoints, or
	// any pane actuation. Ambiguous or missing selectors must fail closed without
	// triggering side effects.
	var panes []tmux.Pane
	var selectedPanes []tmux.Pane
	multiWindow := false
	explicitSingle := paneSelector != ""
	if explicitSingle || opts.PanesSpecified {
		panes, err = tmux.GetPanesContext(ctx, session)
		if err != nil {
			return outputError(err)
		}
		if len(panes) == 0 {
			return outputError(fmt.Errorf("no panes found in session '%s'", session))
		}
		panes = sortPanesByTopology(panes)
		multiWindow = tmux.PanesSpanMultipleWindows(panes)
		if explicitSingle {
			selectedPanes, err = resolveShellSendSelectors(panes, []string{paneSelector}, true)
		} else {
			selectedPanes, err = resolveShellSendSelectors(panes, opts.PaneSelectors, false)
		}
		if err != nil {
			return outputError(err)
		}
	}

	// CASS Duplicate Detection
	if opts.CassCheck && !opts.LoopMode {
		if err := checkCassDuplicates(ctx, session, sessionInferred, prompt, opts.CassSimilarity, opts.CassCheckDays, opts.ForceNonInteractive, silent); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return outputError(fmt.Errorf("send canceled during duplicate check: %w", ctxErr))
			}
			if strings.Compare(err.Error(), "aborted by user") == 0 {
				if jsonOutput || silent {
					return outputError(err)
				}
				fmt.Println("Aborted.")
				return nil
			}
			// CASS is advisory — never block prompt delivery on cass failures.
			// Common transient errors: WAL corruption, signal killed, timeouts.
			if !jsonOutput && !silent {
				fmt.Printf("Warning: CASS duplicate check failed: %v\n", err)
			}
		}
	}

	// Initialize hook executor
	var hookExec *hooks.Executor
	if !opts.NoHooks {
		var err error
		hookExec, err = hooks.NewExecutorFromConfig()
		if err != nil {
			// Log warning but continue - hooks are optional
			if !jsonOutput && !silent {
				fmt.Printf("⚠ Could not load hooks config: %v\n", err)
			}
			hookExec = hooks.NewExecutor(nil) // Use empty config
		}
	}

	// Build target description for hook environment
	targetDesc := buildSendTargetDescription(targetCC, targetCod, targetGmi, targetAgy, targetAll, skipFirst, paneIndex, paneSelector, opts.PaneSelectors, opts.PanesSpecified, tags)

	// Build execution context for hooks
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  getSessionWorkingDir(ctx, session, sessionInferred),
		Message:     prompt,
		AdditionalEnv: map[string]string{
			"NTM_SEND_TARGETS":   targetDesc,
			"NTM_TARGET_CC":      boolToStr(targetCC),
			"NTM_TARGET_COD":     boolToStr(targetCod),
			"NTM_TARGET_GMI":     boolToStr(targetGmi),
			"NTM_TARGET_AGY":     boolToStr(targetAgy),
			"NTM_TARGET_ALL":     boolToStr(targetAll),
			"NTM_PANE_INDEX":     fmt.Sprintf("%d", paneIndex),
			"NTM_PANE_SELECTOR":  paneSelector,
			"NTM_PANE_SELECTORS": strings.Join(opts.PaneSelectors, ","),
		},
	}

	// Run pre-send hooks
	if !dryRun && hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreSend) {
		if !jsonOutput && !silent {
			fmt.Println("Running pre-send hooks...")
		}
		hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(hookRunCtx, hooks.EventPreSend, hookCtx)
		cancel()
		if err != nil {
			return outputError(fmt.Errorf("pre-send hook failed: %w", err))
		}
		if hooks.AnyFailed(results) {
			return outputError(fmt.Errorf("pre-send hook failed: %w", hooks.AllErrors(results)))
		}
		if !jsonOutput && !silent {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d pre-send hook(s) completed\n", success)
		}
	}

	// Auto-checkpoint before broadcast sends
	isBroadcast := !opts.PanesSpecified && paneSelector == "" && (targetAll || (!targetCC && !targetCod && !targetGmi && !targetAgy && len(tags) == 0))
	// Checkpoint capture emits human-facing advisory logs from lower layers, so
	// keep it on the interactive path and preserve a clean machine-output channel.
	if !dryRun && !jsonOutput && !silent && isBroadcast && cfg != nil && cfg.Checkpoints.Enabled && cfg.Checkpoints.BeforeBroadcast {
		if !jsonOutput && !silent {
			fmt.Println("Creating auto-checkpoint before broadcast...")
		}
		autoCP := checkpoint.NewAutoCheckpointer()
		cp, err := autoCP.Create(checkpoint.AutoCheckpointOptions{
			SessionName:     session,
			Reason:          checkpoint.ReasonBroadcast,
			Description:     fmt.Sprintf("before sending to %s", targetDesc),
			ScrollbackLines: cfg.Checkpoints.ScrollbackLines,
			IncludeGit:      cfg.Checkpoints.IncludeGit,
			MaxCheckpoints:  cfg.Checkpoints.MaxAutoCheckpoints,
		})
		if err != nil {
			// Log warning but continue - auto-checkpoint is best-effort
			if !jsonOutput && !silent {
				fmt.Printf("⚠ Auto-checkpoint failed: %v\n", err)
			}
		} else if !jsonOutput && !silent {
			fmt.Printf("✓ Auto-checkpoint created: %s\n", cp.ID)
		}
	}

	if panes == nil {
		panes, err = tmux.GetPanesContext(ctx, session)
		if err != nil {
			return outputError(err)
		}
		if len(panes) == 0 {
			return outputError(fmt.Errorf("no panes found in session '%s'", session))
		}
		panes = sortPanesByTopology(panes)
		multiWindow = tmux.PanesSpanMultipleWindows(panes)
	}

	// Broad sends apply type/tag filters after deterministic topology ordering.
	if selectedPanes == nil {
		noFilter := !targetCC && !targetCod && !targetGmi && !targetAgy && !targetAll && len(tags) == 0
		hasVariantFilter := len(targets) > 0

		for i, p := range panes {
			// Skip first pane if requested
			if skipFirst && i == 0 {
				continue
			}

			// --all is an agent broadcast: the user/control pane joins only
			// with an explicit --include-user (ntm-hykz). Broadcast prompt
			// text typed into the operator's zsh was the trap every skill
			// recipe carried --skip-first to dodge.
			if targetAll && !opts.IncludeUser && sendValueEqual(p.Type, tmux.AgentUser) {
				continue
			}

			// Apply filters
			if !targetAll && !noFilter {
				// Check tags
				if len(tags) > 0 {
					if !HasAnyTag(p.Tags, tags) {
						continue
					}
				}

				// Check type filters (only if specified)
				hasTypeFilter := hasVariantFilter || targetCC || targetCod || targetGmi || targetAgy

				if hasTypeFilter {
					if hasVariantFilter {
						if !targets.MatchesPane(p) {
							continue
						}
					} else {
						match := matchesLegacySendTypeFilter(p, targetCC, targetCod, targetGmi)
						if !match {
							continue
						}
					}
				}
			} else if noFilter {
				// Default mode: skip non-agent panes
				if sendValueEqual(p.Type, tmux.AgentUser) {
					continue
				}
			}

			selectedPanes = append(selectedPanes, p)
		}
	}

	// Track results for JSON output
	if opts.Randomize && len(selectedPanes) > 1 && !explicitSingle {
		var perm []int
		seedUsed, perm = shuffledPermutation(len(selectedPanes), opts.Seed)
		selectedPanes = permutePanes(selectedPanes, perm)
	}

	targetPanes := make([]string, 0, len(selectedPanes))
	targetAgentTypes := make([]string, 0, len(selectedPanes))
	for _, p := range selectedPanes {
		targetPanes = append(targetPanes, tmux.PaneTargetKey(p, multiWindow))
		targetAgentTypes = append(targetAgentTypes, p.Type.String())
	}
	if opts.routingResult != nil && len(selectedPanes) == 1 {
		opts.routingResult.Pane = targetPanes[0]
		opts.routingResult.PaneID = selectedPanes[0].ID
	}
	histTargets = targetPanes
	histAgentTypes = targetAgentTypes
	dispatchPacing := buildDispatchPacingDecision(opts, session, selectedPanes, multiWindow)

	if opts.Randomize && len(targetPanes) > 1 && !jsonOutput && !silent {
		fmt.Fprintf(os.Stderr, "Randomized send order (seed=%d): %v\n", seedUsed, targetPanes)
	}

	// Apply DCG safety check for non-Claude agents
	if err := maybeBlockSendWithDCG(prompt, session, selectedPanes); err != nil {
		return outputError(err)
	}

	if len(selectedPanes) == 0 {
		histErr = errors.New("no matching panes found")
		result := SendResult{
			Success:              false,
			Session:              session,
			PromptPreview:        truncatePrompt(prompt, 50),
			NonInteractiveForced: opts.ForceNonInteractive,
			Redaction:            redactionSummary,
			Warnings:             redactionWarnings,
			ErrorCode:            sendErrorCodeNoMatchingPanes,
			Targets:              []string{},
			Delivered:            0,
			Failed:               0,
			RoutedTo:             opts.routingResult,
			DispatchPacing:       dispatchPacing,
			Error:                histErr.Error(),
		}
		if jsonOutput || opts.executionPolicy == sendExecutionCollect {
			return finishSendResult(opts, result, histErr)
		}
		fmt.Println("No matching panes found")
		return histErr
	}

	dispatchRedactCfg := activeShellDispatchRedactionConfig()
	dispatchService, err := newShellDispatchService(session, selectedPanes, dispatchRedactCfg)
	if err != nil {
		return outputError(err)
	}
	dispatchRequest := shellDispatchRequest(session, panes, selectedPanes, prompt, (!jsonOutput && !silent) || explicitSingle)
	dispatchRequest.DryRun = dryRun
	dispatchRequest.ClearInput = opts.ClearInput
	preparedDispatch, err := dispatchService.Prepare(
		ctx,
		dispatchRequest,
	)
	if err != nil {
		return outputError(err)
	}
	dispatchResult, dispatchErr := dispatchService.Dispatch(ctx, preparedDispatch)
	if dryRun {
		if dispatchErr != nil || !dispatchResult.Success {
			if dispatchErr == nil {
				dispatchErr = errors.New("dispatch preflight did not produce a successful preview")
			}
			return outputError(dispatchErr)
		}
		entries := buildSendDryRunEntries(selectedPanes, prompt, promptSource, multiWindow)
		return finishSendDryRunResult(opts, SendDryRunResult{
			Success:              true,
			DryRun:               true,
			Session:              session,
			NonInteractiveForced: opts.ForceNonInteractive,
			Redaction:            redactionSummary,
			Warnings:             redactionWarnings,
			Blocked:              false,
			Total:                len(entries),
			WouldSend:            entries,
			RoutedTo:             opts.routingResult,
			DispatchPacing:       dispatchPacing,
			Message:              "use without --dry-run to execute",
		})
	}
	delivered = dispatchResult.Delivered
	failed = dispatchResult.Failed
	var firstDeliveryErr error
	var firstFailedPane string
	for _, receipt := range dispatchResult.Receipts {
		if receipt.Status != dispatchsvc.ReceiptFailed {
			continue
		}
		failure := errors.New(receipt.Error)
		if firstDeliveryErr == nil {
			firstDeliveryErr = failure
			firstFailedPane = receipt.Target.Address
		}
		histErr = failure
	}
	if dispatchErr != nil && (firstDeliveryErr == nil || errors.Is(dispatchErr, context.Canceled) || errors.Is(dispatchErr, context.DeadlineExceeded)) {
		firstDeliveryErr = dispatchErr
		histErr = dispatchErr
	}

	// Preserve the explicit single-pane command's receipt and lifecycle: it has
	// historically returned before broadcast post-hooks and prompt-send events.
	if explicitSingle {
		if firstDeliveryErr != nil {
			errorCode := sendErrorCodeFailed
			if errors.Is(firstDeliveryErr, context.Canceled) || errors.Is(firstDeliveryErr, context.DeadlineExceeded) {
				errorCode = robot.ErrCodeTimeout
			}
			result := SendResult{
				Success:              false,
				Session:              session,
				PromptPreview:        truncatePrompt(prompt, 50),
				NonInteractiveForced: opts.ForceNonInteractive,
				Redaction:            redactionSummary,
				Warnings:             redactionWarnings,
				Randomized:           opts.Randomize,
				SeedUsed:             seedUsed,
				Targets:              targetPanes,
				Delivered:            delivered,
				Failed:               failed,
				RoutedTo:             opts.routingResult,
				DispatchPacing:       dispatchPacing,
				ErrorCode:            errorCode,
				Error:                firstDeliveryErr.Error(),
			}
			if jsonOutput || opts.executionPolicy == sendExecutionCollect {
				return finishSendResult(opts, result, firstDeliveryErr)
			}
			return firstDeliveryErr
		}
		histSuccess = true
		result := SendResult{
			Success:              true,
			Session:              session,
			PromptPreview:        truncatePrompt(prompt, 50),
			NonInteractiveForced: opts.ForceNonInteractive,
			Redaction:            redactionSummary,
			Warnings:             redactionWarnings,
			Randomized:           opts.Randomize,
			SeedUsed:             seedUsed,
			Targets:              targetPanes,
			Delivered:            delivered,
			Failed:               failed,
			RoutedTo:             opts.routingResult,
			DispatchPacing:       dispatchPacing,
		}
		if jsonOutput || opts.executionPolicy == sendExecutionCollect {
			return finishSendResult(opts, result, nil)
		}
		fmt.Printf("Sent to pane %s\n", targetPanes[0])
		return nil
	}
	if firstDeliveryErr != nil && !jsonOutput && !silent {
		return fmt.Errorf("sending to pane %s: %w", firstFailedPane, firstDeliveryErr)
	}

	// Update hook context with delivery results
	hookCtx.AdditionalEnv["NTM_DELIVERED_COUNT"] = fmt.Sprintf("%d", delivered)
	hookCtx.AdditionalEnv["NTM_FAILED_COUNT"] = fmt.Sprintf("%d", failed)
	hookCtx.AdditionalEnv["NTM_TARGET_PANES"] = fmt.Sprintf("%v", targetPanes)
	histTargets = targetPanes

	// Run post-send hooks
	if hookExec != nil && !dryRun && hookExec.HasHooksForEvent(hooks.EventPostSend) {
		if !jsonOutput && !silent {
			fmt.Println("Running post-send hooks...")
		}
		hookRunCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		results, postErr := hookExec.RunHooksForEvent(hookRunCtx, hooks.EventPostSend, hookCtx)
		cancel()
		if postErr != nil {
			// Log error but don't fail (send already succeeded)
			if !jsonOutput && !silent {
				fmt.Printf("⚠ Post-send hook error: %v\n", postErr)
			}
		} else if hooks.AnyFailed(results) {
			// Log failures but don't fail (send already succeeded)
			if !jsonOutput && !silent {
				fmt.Printf("⚠ Post-send hook failed: %v\n", hooks.AllErrors(results))
			}
		} else if !jsonOutput && !silent {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d post-send hook(s) completed\n", success)
		}
	}

	// Emit prompt_send event
	if delivered > 0 {
		events.EmitPromptSend(session, delivered, len(prompt), "", buildObservedTargetTypes(selectedPanes), len(hookCtx.AdditionalEnv) > 0)
	}

	result := SendResult{
		Success:              failed == 0 && firstDeliveryErr == nil,
		Session:              session,
		PromptPreview:        truncatePrompt(prompt, 50),
		NonInteractiveForced: opts.ForceNonInteractive,
		Redaction:            redactionSummary,
		Warnings:             redactionWarnings,
		Randomized:           opts.Randomize,
		SeedUsed:             seedUsed,
		Targets:              targetPanes,
		Delivered:            delivered,
		Failed:               failed,
		RoutedTo:             opts.routingResult,
		DispatchPacing:       dispatchPacing,
	}
	if !result.Success {
		result.ErrorCode = sendErrorCodeFailed
		result.Error = fmt.Sprintf("%d pane(s) failed", failed)
		if firstDeliveryErr != nil {
			result.Error = firstDeliveryErr.Error()
			if errors.Is(firstDeliveryErr, context.Canceled) || errors.Is(firstDeliveryErr, context.DeadlineExceeded) {
				result.ErrorCode = robot.ErrCodeTimeout
			}
		}
		if histErr == nil {
			histErr = errors.New(result.Error)
		}
	} else {
		histSuccess = true
	}
	if jsonOutput || opts.executionPolicy == sendExecutionCollect {
		return finishSendResult(opts, result, histErr)
	}

	if len(targetPanes) == 0 {
		histErr = errors.New("no matching panes found")
		fmt.Println("No matching panes found")
	} else {
		fmt.Printf("Sent to %d pane(s)\n", delivered)
		histSuccess = failed == 0 && delivered > 0
		if failed > 0 && histErr == nil {
			histErr = fmt.Errorf("%d pane(s) failed", failed)
		}
		// Show "What's next?" suggestions only on complete success
		if failed == 0 {
			output.SuccessFooter(output.SendSuggestions(session)...)
		}
	}

	return nil
}

func saveDeliveredPrompt(delivered int, entry sessionPkg.PromptEntry) error {
	if delivered <= 0 {
		return nil
	}
	return sessionPkg.SavePrompt(entry)
}

func paneAgentLabel(p tmux.Pane) string {
	if sendValueNotEqual(p.Type, tmux.AgentUnknown) && sendValueNotEqual(p.Type, tmux.AgentUser) && p.NTMIndex > 0 {
		return fmt.Sprintf("%s_%d", p.Type, p.NTMIndex)
	}
	if sendValueEqual(p.Type, tmux.AgentUser) {
		return "user"
	}
	if p.Title != "" {
		if suffix := tmux.PaneTitleSuffix(p.Title); suffix != "" {
			return suffix
		}
		return p.Title
	}
	return fmt.Sprintf("pane_%d", p.Index)
}

func buildSendDryRunEntries(panes []tmux.Pane, prompt string, source string, topology ...bool) []SendDryRunEntry {
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	if len(topology) > 0 {
		multiWindow = topology[0]
	}
	entries := make([]SendDryRunEntry, 0, len(panes))
	for _, p := range panes {
		entries = append(entries, SendDryRunEntry{
			Pane:          tmux.PaneTargetKey(p, multiWindow),
			PaneID:        p.ID,
			Agent:         paneAgentLabel(p),
			Prompt:        prompt,
			PromptPreview: truncateForPreview(prompt, 80),
			Source:        source,
		})
	}
	return entries
}

func buildDispatchPacingDecision(opts SendOptions, session string, panes []tmux.Pane, topology ...bool) *coordinator.DispatchPacingDecision {
	if !opts.PaceDispatch && opts.DispatchPacingInput == nil {
		return nil
	}

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	if len(topology) > 0 {
		multiWindow = topology[0]
	}
	input := coordinator.DispatchPacingInput{
		Session:          session,
		RequestedTargets: len(panes),
		PaneHealth:       dispatchPacingPaneHealth(panes, multiWindow),
	}
	if opts.DispatchPacingInput != nil {
		input = *opts.DispatchPacingInput
		if strings.Compare(strings.TrimSpace(input.Session), "") == 0 {
			input.Session = session
		}
		if input.RequestedTargets <= 0 {
			input.RequestedTargets = len(panes)
		}
		if len(input.PaneHealth) == 0 {
			input.PaneHealth = dispatchPacingPaneHealth(panes, multiWindow)
		}
	}

	decision := coordinator.EvaluateDispatchPacing(input)
	return &decision
}

func dispatchPacingPaneHealth(panes []tmux.Pane, topology ...bool) []coordinator.DispatchPaneHealth {
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	if len(topology) > 0 {
		multiWindow = topology[0]
	}
	health := make([]coordinator.DispatchPaneHealth, 0, len(panes))
	for _, pane := range panes {
		health = append(health, coordinator.DispatchPaneHealth{
			PaneIndex: pane.Index,
			Pane:      tmux.PaneTargetKey(pane, multiWindow),
			PaneID:    pane.ID,
			AgentType: pane.Type.Canonical().String(),
			Healthy:   true,
		})
	}
	return health
}

func printSendDryRunResult(result SendDryRunResult) error {
	if IsJSONOutput() {
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Printf("Dry Run: ntm send %s\n\n", result.Session)

	if result.RoutedTo != nil {
		fmt.Printf("Routing: pane %d (%s) via %s\n\n", result.RoutedTo.PaneIndex, result.RoutedTo.AgentType, result.RoutedTo.Strategy)
	}

	fmt.Printf("Would send %d prompt(s):\n", result.Total)
	for i, w := range result.WouldSend {
		source := w.Source
		if source == "" {
			source = "unknown"
		}
		fmt.Printf("  %d. %s (pane %s): %q (%s)\n", i+1, w.Agent, w.Pane, w.PromptPreview, source)
	}
	fmt.Println()
	if result.Message != "" {
		fmt.Println(result.Message)
	}
	return nil
}

func newInterruptCmd() *cobra.Command {
	var tags []string

	cmd := &cobra.Command{
		Use:   "interrupt <session>",
		Short: "Send Ctrl+C to all agent panes",
		Long: `Send an interrupt signal (Ctrl+C) to all agent panes in a session.
User panes are not affected.

Examples:
  ntm interrupt myproject
  ntm interrupt myproject --tag=frontend`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInterrupt(args[0], tags)
		},
	}

	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter panes by tag (OR logic)")
	cmd.ValidArgsFunction = completeSessionArgs

	return cmd
}

func runInterrupt(session string, tags []string) error {
	// Use kernel for JSON output mode
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.interrupt", SessionInterruptInput{
			Session: session,
			Tags:    tags,
		})
		if err != nil {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return output.PrintJSON(result)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, os.Stdout)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return err
	}

	count := 0
	for _, p := range panes {
		// Only interrupt agent panes
		if isInterruptibleAgentPane(p) {
			// Check tags
			if len(tags) > 0 {
				if !HasAnyTag(p.Tags, tags) {
					continue
				}
			}

			if err := tmux.SendInterrupt(p.ID); err != nil {
				return fmt.Errorf("interrupting pane %d: %w", p.Index, err)
			}
			count++
		}
	}

	fmt.Printf("Sent Ctrl+C to %d agent pane(s)\n", count)
	return nil
}

// buildInterruptResponse constructs the response for session interrupt.
// Used by both kernel handler and direct CLI calls.
func buildInterruptResponse(ctx context.Context, session string, tags []string) (*output.InterruptResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return nil, err
	}
	resolvedSession, err := normalizeExplicitLiveSessionName(ctx, session, true)
	if err != nil {
		return nil, err
	}
	session = resolvedSession

	if !tmux.SessionExists(session) {
		return nil, fmt.Errorf("session '%s' not found", session)
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, err
	}

	var targetedPanes []int
	interrupted := 0
	skipped := 0

	for _, p := range panes {
		// Only interrupt agent panes
		if isInterruptibleAgentPane(p) {
			// Check tags
			if len(tags) > 0 {
				if !HasAnyTag(p.Tags, tags) {
					skipped++
					continue
				}
			}

			targetedPanes = append(targetedPanes, p.Index)
			if err := tmux.SendInterrupt(p.ID); err != nil {
				return nil, fmt.Errorf("interrupting pane %d: %w", p.Index, err)
			}
			interrupted++
		}
	}

	return &output.InterruptResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Interrupted:         interrupted,
		Skipped:             skipped,
		TargetedPanes:       targetedPanes,
	}, nil
}

func newKillCmd() *cobra.Command {
	var force bool
	var tags []string
	var noHooks bool
	var summarize bool
	var project string
	var pane string

	cmd := &cobra.Command{
		Use:   "kill <session>",
		Short: "Kill a tmux session",
		Long: `Kill a tmux session and all its panes.

Use --project to kill all sessions for a base project (requires confirmation).

Examples:
  ntm kill myproject              # Prompts for confirmation
  ntm kill myproject --force      # No confirmation
  ntm kill myproject --tag=ui     # Kill only panes with 'ui' tag
  ntm kill myproject --summarize  # Generate summary before killing
  ntm kill myproject --pane=2     # Remove only pane 2, session survives
  ntm kill --project myproject    # Kill all sessions for the project`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project != "" && len(args) > 0 {
				return fmt.Errorf("cannot use --project with a specific session name")
			}
			if pane != "" {
				if project != "" || len(tags) > 0 || summarize {
					return fmt.Errorf("--pane cannot be combined with --project, --tag, or --summarize")
				}
				if len(args) == 0 {
					return fmt.Errorf("session name required with --pane")
				}
				return runKillPane(cmd.Context(), cmd.OutOrStdout(), args[0], pane, force)
			}
			if project != "" {
				return runKillProject(cmd.Context(), cmd.OutOrStdout(), project, force, tags, noHooks, summarize)
			}
			if len(args) == 0 {
				return fmt.Errorf("session name or --project required")
			}
			return runKill(cmd.Context(), cmd.OutOrStdout(), args[0], force, tags, noHooks, summarize)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter panes to kill by tag (if used, only matching panes are killed)")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&summarize, "summarize", false, "Generate session summary before killing")
	cmd.Flags().StringVarP(&project, "project", "p", "", "kill all sessions for a base project name")
	cmd.Flags().StringVar(&pane, "pane", "", "remove only the selected pane(s) (N, W.P, or %N; comma-separated); the session and sibling panes survive")

	return cmd
}

// runKillPane removes specific panes from a session, leaving the session and
// its sibling panes untouched (ntm-34jt: recovery-ladder Rung 6 prescribed
// `ntm kill <session> --pane=N` before this flag existed, forcing operators
// back to raw `tmux kill-pane`).
func runKillPane(ctx context.Context, w io.Writer, session, selector string, force bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	session = res.Session
	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}
	opts := robot.KillPaneOptions{
		Session: session,
		Panes:   strings.Split(selector, ","),
		Force:   force,
	}
	if IsJSONOutput() {
		return robot.PrintKillPane(ctx, opts)
	}
	if !force {
		title := fmt.Sprintf("Remove pane(s) %s from session '%s'?", selector, session)
		desc := "The selected pane(s) and their processes will be terminated; the session survives."
		if !confirmHuhDestructive(title, desc) {
			fmt.Fprintln(w, "Aborted.")
			return nil
		}
	}
	result, err := robot.GetKillPane(ctx, opts)
	if err != nil {
		return err
	}
	if !result.Success {
		if result.Error != "" {
			return fmt.Errorf("%s", result.Error)
		}
		return fmt.Errorf("pane removal failed")
	}
	for _, removed := range result.Removed {
		fmt.Fprintf(w, "Removed pane %s (%s, %s)\n", removed.Pane, removed.Target, removed.AgentType)
	}
	fmt.Fprintf(w, "%d pane(s) remain in session '%s'\n", result.RemainingPanes, session)
	return nil
}

func runKill(ctx context.Context, w io.Writer, session string, force bool, tags []string, noHooks bool, summarize bool) (err error) {
	// Use kernel for JSON output mode
	if IsJSONOutput() {
		result, err := kernel.Run(ctx, "sessions.kill", SessionKillInput{
			Session:   session,
			Force:     force,
			Tags:      tags,
			NoHooks:   noHooks,
			Summarize: summarize,
		})
		if err != nil {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return output.PrintJSON(result)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session
	sessionInferred := res.Inferred

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	dir := getSessionWorkingDir(ctx, session, sessionInferred)
	auditStart := time.Now()
	auditAborted := false
	auditKilled := false
	auditNoTargets := false
	auditScope := "session"
	auditKilledPanes := 0
	if len(tags) > 0 {
		auditScope = "tags"
	}
	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.kill", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"force":          force,
		"tags":           tags,
		"summarize":      summarize,
		"scope":          auditScope,
		"working_dir":    dir,
		"correlation_id": auditCorrelationID,
	}, nil)
	defer func() {
		success := err == nil && !auditAborted
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        session,
			"force":          force,
			"tags":           tags,
			"summarize":      summarize,
			"scope":          auditScope,
			"killed":         auditKilled,
			"killed_panes":   auditKilledPanes,
			"no_targets":     auditNoTargets,
			"aborted":        auditAborted,
			"success":        success,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"working_dir":    dir,
			"correlation_id": auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.kill", payload, nil)
	}()

	// Initialize hook executor
	var hookExec *hooks.Executor
	if !noHooks {
		var err error
		hookExec, err = hooks.NewExecutorFromConfig()
		if err != nil {
			if !jsonOutput {
				fmt.Printf("⚠ Could not load hooks config: %v\n", err)
			}
			hookExec = hooks.NewExecutor(nil)
		}
	}

	// Build hook context
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
		AdditionalEnv: map[string]string{
			"NTM_FORCE_KILL": boolToStr(force),
			"NTM_KILL_TAGS":  strings.Join(tags, ","),
		},
	}

	// Run pre-kill hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreKill) {
		if !jsonOutput {
			fmt.Println("Running pre-kill hooks...")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreKill, hookCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("pre-kill hook failed: %w", err)
		}
		if hooks.AnyFailed(results) {
			return fmt.Errorf("pre-kill hook failed: %w", hooks.AllErrors(results))
		}
	}

	// Generate summary before killing if requested
	if summarize {
		fmt.Println("Generating session summary...")
		summaryResult, err := generateKillSummary(ctx, session, sessionInferred)
		if err != nil {
			fmt.Printf("⚠ Summary generation failed: %v\n", err)
		} else {
			fmt.Println("\n" + summaryResult.Text + "\n")
		}
	}

	// If tags are provided, kill specific panes
	if len(tags) > 0 {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return err
		}

		var toKill []tmux.Pane
		for _, p := range panes {
			if HasAnyTag(p.Tags, tags) {
				toKill = append(toKill, p)
			}
		}

		if len(toKill) == 0 {
			fmt.Println("No panes found matching tags.")
			auditNoTargets = true
			return nil
		}

		if !force {
			title := fmt.Sprintf("Kill %d pane(s)?", len(toKill))
			desc := fmt.Sprintf("This will terminate panes matching tags %v in session '%s'.", tags, session)
			if !confirmHuhDestructive(title, desc) {
				auditAborted = true
				fmt.Println("Aborted.")
				return nil
			}
		}

		for _, p := range toKill {
			if err := tmux.KillPane(p.ID); err != nil {
				return fmt.Errorf("killing pane %s: %w", p.ID, err)
			}
		}
		addTimelineStopMarkers(session, toKill)
		auditKilled = true
		auditKilledPanes = len(toKill)
		fmt.Printf("Killed %d pane(s)\n", len(toKill))
		return nil
	}

	if !force {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return err
		}

		title := fmt.Sprintf("Kill session '%s'?", session)
		desc := fmt.Sprintf("This will terminate %d running agent(s).", len(panes))
		if !confirmHuhDestructive(title, desc) {
			auditAborted = true
			fmt.Println("Aborted.")
			return nil
		}
	}

	panesForStop, err := tmux.GetPanes(session)
	if err == nil {
		addTimelineStopMarkers(session, panesForStop)
	}

	// Finalize timeline persistence before killing the session
	if err := state.EndSessionTimeline(session); err != nil {
		// Log but don't fail - timeline finalization is not critical
		if !jsonOutput {
			fmt.Printf("⚠ Timeline finalization failed: %v\n", err)
		}
	}

	// Collect the agent process subtree of every pane BEFORE killing the
	// session. Agents (often node/bun launched with --dangerously-skip-
	// permissions) run as descendants of the pane shell PID; on a tmux
	// kill-session SIGHUP they can survive, reparent to init, and leak —
	// holding agent-mail registrations and file locks. We snapshot the subtree
	// now while the shells are still alive, then reap any survivors after the
	// session is gone.
	var panePIDs []int
	for _, p := range panesForStop {
		panePIDs = append(panePIDs, p.PID)
	}
	orphanCandidates := collectPaneDescendants(panePIDs)

	// Kill the monitor process before destroying the session
	if output, err := exec.Command("pkill", "-f", monitorProcessPattern(session)).CombinedOutput(); err != nil {
		// Monitor may not be running — that's fine
		_ = output
	}

	if err := tmux.KillSession(session); err != nil {
		return err
	}
	auditKilled = true

	// Reap any agent process subtrees that survived kill-session.
	reapOrphanProcesses(orphanCandidates)

	fmt.Printf("Killed session '%s'\n", session)

	// Post-kill hooks?
	// The session is gone, but we can still run hooks in context of what was killed.
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPostKill) {
		if !jsonOutput {
			fmt.Println("Running post-kill hooks...")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPostKill, hookCtx)
		cancel()
		if err != nil {
			if !jsonOutput {
				fmt.Printf("⚠ Post-kill hook error: %v\n", err)
			}
		} else if hooks.AnyFailed(results) {
			if !jsonOutput {
				fmt.Printf("⚠ Post-kill hook failed: %v\n", hooks.AllErrors(results))
			}
		}
	}

	return nil
}

// orphanReapGrace is how long we wait between SIGTERM and SIGKILL when reaping
// agent process subtrees that survived a tmux kill-session.
const orphanReapGrace = 750 * time.Millisecond

// orphanReapMaxDepth bounds the recursive descendant walk so a pathological or
// fork-bombing subtree cannot stall the kill path.
const orphanReapMaxDepth = 4

// orphanReapFanout caps the number of children expanded per node during the
// recursive walk, a defensive bound against runaway process trees.
const orphanReapFanout = 64

// orphanReapExcluded reports whether a PID must never be targeted by the orphan
// reap: init (pid <= 1), the running ntm process itself, and the ntm parent.
// Targeting any of these would be catastrophic, so the reap walk and the signal
// loop both gate on this predicate.
func orphanReapExcluded(pid int) bool {
	return pid <= 1 || pid == os.Getpid() || pid == os.Getppid()
}

// collectPaneDescendants gathers the descendant PID subtree of each pane shell
// PID in `panePIDs`. process.GetChildPIDs returns only ONE level of children,
// so we walk recursively (depth-bounded by orphanReapMaxDepth, fanout-capped by
// orphanReapFanout) to capture the whole subtree.
//
// The pane shell PIDs themselves are intentionally EXCLUDED from the result:
// tmux kill-session reaps the pane shells directly; we only need to mop up the
// agent processes spawned beneath them that can reparent to init and leak.
//
// Self (os.Getpid()), the ntm parent (os.Getppid()), and any pid <= 1 are
// excluded so the reap can never target the running process, its launcher, or
// init. The returned slice is deduplicated.
func collectPaneDescendants(panePIDs []int) []int {
	excluded := orphanReapExcluded

	seen := make(map[int]struct{})
	var ordered []int

	var walk func(pid, depth int)
	walk = func(pid, depth int) {
		if depth > orphanReapMaxDepth {
			return
		}
		children := process.GetChildPIDs(pid, orphanReapFanout)
		for _, child := range children {
			if excluded(child) {
				continue
			}
			if _, ok := seen[child]; ok {
				continue
			}
			seen[child] = struct{}{}
			ordered = append(ordered, child)
			walk(child, depth+1)
		}
	}

	for _, paneShell := range panePIDs {
		if paneShell <= 1 {
			continue
		}
		// Start the walk at the pane shell so its descendants (the agents) are
		// collected, but the shell PID itself is never added to `ordered`.
		walk(paneShell, 1)
	}

	return ordered
}

// reapOrphanProcesses sends SIGTERM to each still-alive PID in `pids`, waits a
// short grace period, then SIGKILLs any that remain. PIDs that have already
// exited (the common case — most agents die with their pane shell's SIGHUP) are
// skipped. Errors from kill are ignored: a process may exit between the
// liveness check and the signal, which is exactly the outcome we want.
func reapOrphanProcesses(pids []int) {
	if len(pids) == 0 {
		return
	}

	var termed []int
	for _, pid := range pids {
		if pid <= 1 || !process.IsAlive(pid) {
			continue
		}
		termProcess(pid)
		termed = append(termed, pid)
	}

	if len(termed) == 0 {
		return
	}

	time.Sleep(orphanReapGrace)

	for _, pid := range termed {
		if process.IsAlive(pid) {
			killProcess(pid)
		}
	}
}

// runKillProject kills all sessions matching a base project name (bd-3cu02.14).
func runKillProject(ctx context.Context, w io.Writer, project string, force bool, tags []string, noHooks bool, summarize bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	sessions, err := tmux.ListSessions()
	if err != nil {
		return err
	}

	var targets []tmux.Session
	for _, s := range sessions {
		if sendValueEqual(config.SessionBase(s.Name), project) {
			targets = append(targets, s)
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no sessions found for project %q", project)
	}

	// Show what will be killed and confirm
	var names []string
	for _, s := range targets {
		names = append(names, s.Name)
	}

	if !force {
		title := fmt.Sprintf("Kill %d session(s)?", len(targets))
		desc := fmt.Sprintf("Sessions: %s", strings.Join(names, ", "))
		if !confirmHuhDestructive(title, desc) {
			fmt.Fprintln(w, "Aborted.")
			return nil
		}
	}

	var killErrors []string
	for _, s := range targets {
		if err := runKill(ctx, w, s.Name, true, tags, noHooks, summarize); err != nil {
			killErrors = append(killErrors, fmt.Sprintf("%s: %v", s.Name, err))
		}
	}

	if len(killErrors) > 0 {
		return fmt.Errorf("some sessions failed to kill: %s", strings.Join(killErrors, "; "))
	}
	return nil
}

// buildKillResponse constructs the response for session kill.
// Used by both kernel handler and direct CLI calls.
// In JSON/robot mode, force is effectively always true (no interactive confirmation).
func buildKillResponse(ctx context.Context, session string, force bool, tags []string, noHooks bool, summarize bool) (resp *output.KillResponse, err error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return nil, err
	}
	resolvedSession, err := normalizeExplicitLiveSessionName(ctx, session, true)
	if err != nil {
		return nil, err
	}
	session = resolvedSession

	if !tmux.SessionExists(session) {
		return nil, fmt.Errorf("session '%s' not found", session)
	}

	dir := getSessionWorkingDir(ctx, session, false)
	auditStart := time.Now()
	auditScope := "session"
	if len(tags) > 0 {
		auditScope = "tags"
	}
	auditKilled := false
	auditNoTargets := false
	auditKilledPanes := 0
	_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.kill", map[string]interface{}{
		"phase":          "start",
		"session":        session,
		"force":          force,
		"tags":           tags,
		"summarize":      summarize,
		"scope":          auditScope,
		"working_dir":    dir,
		"correlation_id": auditCorrelationID,
	}, nil)
	defer func() {
		success := err == nil
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        session,
			"force":          force,
			"tags":           tags,
			"summarize":      summarize,
			"scope":          auditScope,
			"killed":         auditKilled,
			"killed_panes":   auditKilledPanes,
			"no_targets":     auditNoTargets,
			"success":        success,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"working_dir":    dir,
			"correlation_id": auditCorrelationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(session, audit.EventTypeCommand, audit.ActorUser, "session.kill", payload, nil)
	}()

	// Enable project webhooks (if configured) for this session so kill events can fan out.
	// Best-effort: failures should not block the kill operation.
	var (
		bridge    *webhook.BusBridge
		bridgeErr error
	)
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, bridgeErr = webhook.StartBridgeFromProjectConfig(dir, session, events.DefaultBus, &redactCfg)
	} else {
		bridge, bridgeErr = webhook.StartBridgeFromProjectConfig(dir, session, events.DefaultBus, nil)
	}
	if bridgeErr != nil {
		slog.Default().Debug("webhook bridge init failed", "session", session, "error", bridgeErr)
	} else if bridge != nil {
		defer bridge.Close()
	}

	// Initialize hook executor
	var hookExec *hooks.Executor
	if !noHooks {
		var err error
		hookExec, err = hooks.NewExecutorFromConfig()
		if err != nil {
			// In kernel mode, we don't have interactive output
			hookExec = hooks.NewExecutor(nil)
		}
	}

	// Build hook context
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  dir,
		AdditionalEnv: map[string]string{
			"NTM_FORCE_KILL": boolToStr(force),
			"NTM_KILL_TAGS":  strings.Join(tags, ","),
		},
	}

	// Run pre-kill hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreKill) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreKill, hookCtx)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("pre-kill hook failed: %w", err)
		}
		if hooks.AnyFailed(results) {
			return nil, fmt.Errorf("pre-kill hook failed: %w", hooks.AllErrors(results))
		}
	}

	// Generate summary before killing if requested
	var summaryResult *summary.SessionSummary
	if summarize {
		var err error
		summaryResult, err = generateKillSummary(ctx, session, false)
		if err != nil {
			// Non-fatal - continue with kill but note the error
			summaryResult = nil
		}
	}

	var message string

	// If tags are provided, kill specific panes
	if len(tags) > 0 {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return nil, err
		}

		var toKill []tmux.Pane
		for _, p := range panes {
			if HasAnyTag(p.Tags, tags) {
				toKill = append(toKill, p)
			}
		}

		if len(toKill) == 0 {
			auditNoTargets = true
			resp = &output.KillResponse{
				TimestampedResponse: output.NewTimestamped(),
				Session:             session,
				Killed:              false,
				Message:             "No panes found matching tags",
			}
			return resp, nil
		}

		for _, p := range toKill {
			if err := tmux.KillPane(p.ID); err != nil {
				return nil, fmt.Errorf("killing pane %s: %w", p.ID, err)
			}
			events.DefaultEmitter().Emit(events.NewWebhookEvent(
				events.WebhookAgentStopped,
				session,
				p.ID,
				agentTypeToString(p.Type),
				"Agent stopped",
				map[string]string{
					"project_dir": dir,
					"pane_index":  fmt.Sprintf("%d", p.Index),
					"pane_title":  p.Title,
					"kill_tags":   strings.Join(tags, ","),
				},
			))
		}
		addTimelineStopMarkers(session, toKill)
		auditKilled = true
		auditKilledPanes = len(toKill)
		message = fmt.Sprintf("Killed %d pane(s) matching tags", len(toKill))
	} else {
		panesForStop, err := tmux.GetPanes(session)
		if err == nil {
			addTimelineStopMarkers(session, panesForStop)
		}

		// Finalize timeline persistence before killing the session
		_ = state.EndSessionTimeline(session) // Ignore error - not critical

		// Kill the monitor process before destroying the session (same as runKill)
		if output, err := exec.Command("pkill", "-f", monitorProcessPattern(session)).CombinedOutput(); err != nil {
			_ = output // Monitor may not be running — that's fine
		}

		if err := tmux.KillSession(session); err != nil {
			return nil, err
		}
		auditKilled = true
		message = fmt.Sprintf("Killed session '%s'", session)

		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookSessionKilled,
			session,
			"",
			"",
			message,
			map[string]string{
				"project_dir": dir,
				"force":       boolToStr(force),
			},
		))
		// Alternate/legacy naming used by some configs/docs.
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookSessionEnded,
			session,
			"",
			"",
			message,
			map[string]string{
				"project_dir": dir,
				"force":       boolToStr(force),
			},
		))
	}

	// Post-kill hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPostKill) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_, _ = hookExec.RunHooksForEvent(ctx, hooks.EventPostKill, hookCtx)
		cancel()
		// Post-kill hook errors are logged but don't fail the response
	}

	resp = &output.KillResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Killed:              true,
		Message:             message,
		Summary:             summaryResult,
	}
	return resp, nil
}

// generateKillSummary generates a session summary for use before killing.
// It captures pane outputs and runs them through the summary generator.
func generateKillSummary(ctx context.Context, session string, inferred bool) (*summary.SessionSummary, error) {
	// Get panes from session
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	outputs := collectSummaryAgentOutputs(panes, tmux.CapturePaneOutput, func(pane tmux.Pane, err error) {
		slog.Default().Debug("failed to capture pane output for summary", "pane_id", pane.ID, "error", err)
	})

	if len(outputs) == 0 {
		return nil, fmt.Errorf("no agent outputs to summarize")
	}

	projectDir := getSessionWorkingDir(ctx, session, inferred)
	if projectDir == "" {
		return nil, fmt.Errorf("getting project root failed")
	}

	opts := summary.Options{
		Session:        session,
		Outputs:        outputs,
		Format:         summary.FormatBrief,
		ProjectKey:     projectDir,
		ProjectDir:     projectDir,
		IncludeGitDiff: true, // Include git changes in summary
	}

	return summary.SummarizeSession(context.Background(), opts)
}

// truncatePrompt truncates a prompt to the specified length for display, respecting UTF-8 boundaries.
func truncatePrompt(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	// String needs truncation - if maxLen too small for content + "...", just return "..."
	if maxLen <= 3 {
		return "..."[:maxLen]
	}
	// Find the last rune boundary that allows for "..." suffix within maxLen bytes.
	targetLen := maxLen - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	// All rune starts are <= targetLen, but string is > maxLen bytes.
	// Return up to last rune start + "..."
	return s[:prevI] + "..."
}

// buildTargetDescription creates a human-readable description of send targets
func buildObservedTargetTypes(panes []tmux.Pane) string {
	seen := make(map[string]struct{})
	targets := make([]string, 0, len(panes))
	for _, p := range panes {
		normalized := normalizeAgentType(string(p.Type))
		if !isAnalyticsAgentType(normalized) {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		targets = append(targets, normalized)
	}
	return strings.Join(targets, ",")
}

func buildTargetDescription(targetCC, targetCod, targetGmi, targetAgy, targetAll, skipFirst bool, paneIndex int, tags []string) string {
	if paneIndex >= 0 {
		return fmt.Sprintf("pane:%d", paneIndex)
	}
	if targetAll {
		return "all"
	}

	var targets []string
	if targetCC {
		targets = append(targets, "cc")
	}
	if targetCod {
		targets = append(targets, "cod")
	}
	if targetGmi {
		targets = append(targets, "gmi")
	}
	if targetAgy {
		targets = append(targets, "agy")
	}
	if len(tags) > 0 {
		targets = append(targets, fmt.Sprintf("tags:[%s]", strings.Join(tags, ",")))
	}

	if len(targets) == 0 {
		if skipFirst {
			return "agents"
		}
		return "all-agents"
	}
	return strings.Join(targets, ",")
}

func buildSendTargetDescription(targetCC, targetCod, targetGmi, targetAgy, targetAll, skipFirst bool, paneIndex int, paneSelector string, paneSelectors []string, panesSpecified bool, tags []string) string {
	if paneSelector != "" {
		return "pane:" + paneSelector
	}
	if panesSpecified {
		return "panes:" + strings.Join(paneSelectors, ",")
	}
	return buildTargetDescription(targetCC, targetCod, targetGmi, targetAgy, targetAll, skipFirst, paneIndex, tags)
}

// getSessionWorkingDir returns the working directory for a resolved session,
// preserving workspace fallback only for inferred-session commands.
func getSessionWorkingDir(ctx context.Context, session string, inferred bool) string {
	return resolveCommandProjectDirForSession(ctx, session, inferred)
}

// boolToStr converts a boolean to "true" or "false" string
func boolToStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

var dcgCommandPrefixes = map[string]struct{}{
	"git":       {},
	"rm":        {},
	"mv":        {},
	"cp":        {},
	"chmod":     {},
	"chown":     {},
	"kubectl":   {},
	"terraform": {},
	"rch":       {},
}

func maybeBlockSendWithDCG(prompt, session string, panes []tmux.Pane) error {
	if cfg == nil || !cfg.Integrations.DCG.Enabled {
		return nil
	}
	if len(panes) == 0 {
		return nil
	}
	if !hasNonClaudeTargets(panes) {
		return nil
	}
	commands := extractLikelyCommands(prompt)
	if len(commands) == 0 {
		return nil
	}

	adapter := tools.NewDCGAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !adapter.IsAvailable(ctx) {
		return nil
	}

	for _, command := range commands {
		blocked, err := adapter.CheckCommand(ctx, command)
		if err != nil {
			return err
		}
		if blocked != nil {
			logDCGBlocked(command, session, panes, blocked)
			reason := strings.TrimSpace(blocked.Reason)
			if reason == "" {
				reason = "blocked by dcg"
			}
			return fmt.Errorf("blocked by dcg: %s", reason)
		}
	}
	return nil
}

func hasNonClaudeTargets(panes []tmux.Pane) bool {
	for _, p := range panes {
		if isNonClaudeAgent(p) {
			return true
		}
	}
	return false
}

func isNonClaudeAgent(p tmux.Pane) bool {
	if sendValueEqual(p.Type, tmux.AgentUser) {
		return false
	}
	return sendValueNotEqual(p.Type, tmux.AgentClaude)
}

func extractLikelyCommands(prompt string) []string {
	var commands []string
	for _, line := range strings.Split(prompt, "\n") {
		candidate := normalizeCommandLine(line)
		if candidate == "" {
			continue
		}
		if looksLikeShellCommand(candidate) {
			commands = append(commands, candidate)
		}
	}
	return commands
}

func normalizeCommandLine(line string) string {
	trimmed := strings.TrimSpace(line)
	// Prompts are markdown-heavy: strip list/quote/prompt decoration until the
	// line is stable so dcg evaluates the command text, not the bullet (#228).
	// Decoration handled: "-", "*", "+" bullets (also nested), "[ ]"/"[x]"
	// checkboxes, "1." / "1)" ordered lists, "$ "/"> "/"# " prompt markers.
	for {
		next := stripCommandLineDecoration(trimmed)
		if next == trimmed {
			return trimmed
		}
		trimmed = next
	}
}

// stripCommandLineDecoration removes one layer of leading markdown or shell
// prompt decoration from an already-whitespace-trimmed line. It returns the
// input unchanged when no known decoration is present.
func stripCommandLineDecoration(trimmed string) string {
	for _, prefix := range []string{"- ", "* ", "+ ", "$ ", "> ", "# "} {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	// Checkbox once its bullet is gone: "[ ] cmd", "[x] cmd", "[X] cmd".
	if len(trimmed) > 4 && trimmed[0] == '[' && trimmed[2] == ']' && trimmed[3] == ' ' {
		if box := trimmed[1]; box == ' ' || box == 'x' || box == 'X' {
			return strings.TrimSpace(trimmed[4:])
		}
	}
	// Ordered list markers: "1. cmd", "12) cmd" (bounded to 3 digits so real
	// commands like "2>err cmd" or version strings are left alone).
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits <= 3 && digits+1 < len(trimmed) &&
		(trimmed[digits] == '.' || trimmed[digits] == ')') && trimmed[digits+1] == ' ' {
		return strings.TrimSpace(trimmed[digits+2:])
	}
	return trimmed
}

func looksLikeShellCommand(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	if strings.HasPrefix(lower, "```") {
		return false
	}
	if strings.HasPrefix(lower, "sudo ") {
		lower = strings.TrimSpace(strings.TrimPrefix(lower, "sudo "))
	}
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return false
	}
	if _, ok := dcgCommandPrefixes[fields[0]]; ok {
		return true
	}
	if strings.Contains(lower, "&&") || strings.Contains(lower, "||") || strings.Contains(lower, ";") || strings.Contains(lower, "|") {
		return true
	}
	if strings.Contains(lower, "--force") || strings.Contains(lower, "--hard") || strings.Contains(lower, " -rf") || strings.Contains(lower, " -fr") {
		return true
	}
	return false
}

type shellDispatchProtocolPlanner struct{}

func (shellDispatchProtocolPlanner) PlanDelivery(_ context.Context, target dispatchsvc.Target, submit bool) (dispatchsvc.ProtocolPlan, error) {
	if !submit {
		return dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolStageOnly}, nil
	}
	if target.Pane.Type == tmux.AgentUser {
		return dispatchsvc.ProtocolPlan{Protocol: dispatchsvc.ProtocolSingleEnter, EnterDelay: tmux.DefaultEnterDelay}, nil
	}
	return dispatchsvc.ProtocolPlan{
		Protocol:         dispatchsvc.ProtocolDoubleEnter,
		EnterDelay:       tmux.DoubleEnterFirstDelay,
		SecondEnterDelay: tmux.DoubleEnterSecondDelay,
	}, nil
}

func shellDispatchOrderer(selected []tmux.Pane) dispatchsvc.TargetOrderer {
	keys := make([]string, len(selected))
	for i := range selected {
		keys[i] = selected[i].Ref().StableKey()
	}
	return dispatchsvc.TargetOrdererFunc(func(_ context.Context, input dispatchsvc.OrderInput) ([]dispatchsvc.Target, error) {
		byKey := make(map[string]dispatchsvc.Target, len(input.Targets))
		for _, target := range input.Targets {
			byKey[target.Ref.StableKey()] = target
		}
		ordered := make([]dispatchsvc.Target, 0, len(keys))
		for _, key := range keys {
			target, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("selected shell pane %q is absent from dispatch plan", key)
			}
			ordered = append(ordered, target)
		}
		return ordered, nil
	})
}

func shellDispatchSelectors(selected []tmux.Pane) []string {
	selectors := make([]string, 0, len(selected))
	for _, pane := range selected {
		if pane.ID != "" {
			selectors = append(selectors, pane.ID)
		} else {
			selectors = append(selectors, pane.Ref().Physical())
		}
	}
	return selectors
}

func shellFinalMessageRedactor(redactCfg redaction.Config) dispatchsvc.FinalMessageRedactor {
	return dispatchsvc.FinalMessageRedactorFunc(func(_ context.Context, _ dispatchsvc.Target, message string) (dispatchsvc.RedactionResult, error) {
		result := redaction.ScanAndRedact(message, redactCfg)
		categories := make(map[string]int, len(result.Findings))
		for _, finding := range result.Findings {
			categories[string(finding.Category)]++
		}
		if len(categories) == 0 {
			categories = nil
		}
		return dispatchsvc.RedactionResult{
			Message:    result.Output,
			Mode:       string(result.Mode),
			Findings:   len(result.Findings),
			Categories: categories,
			Blocked:    result.Blocked,
		}, nil
	})
}

func newShellDispatchService(session string, selected []tmux.Pane, redactCfg redaction.Config) (*dispatchsvc.Service, error) {
	return newShellDispatchServiceWithGate(session, selected, redactCfg, nil)
}

func newShellDispatchServiceWithGate(
	session string,
	selected []tmux.Pane,
	redactCfg redaction.Config,
	beforeDispatch func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error,
) (*dispatchsvc.Service, error) {
	return dispatchsvc.NewService(dispatchsvc.Ports{
		Builder: dispatchsvc.FinalMessageBuilderFunc(func(_ context.Context, input dispatchsvc.BuildInput) (string, error) {
			return stampMarchingOrders(input.BaseMessage, session, input.Target.Pane.WindowIndex, input.Target.Pane.Index), nil
		}),
		Redactor:  shellFinalMessageRedactor(redactCfg),
		Orderer:   shellDispatchOrderer(selected),
		Protocols: shellDispatchProtocolPlanner{},
		Deliverer: dispatchsvc.DelivererFunc(func(ctx context.Context, delivery dispatchsvc.Delivery) error {
			target := delivery.Target.Pane
			if err := dispatchsvc.RefuseDeadAgentPane(target); err != nil {
				return err
			}
			if target.ID == "" {
				target.ID = fmt.Sprintf("%s:%s", session, target.Ref().Physical())
			}
			if err := dispatchsvc.ClearComposerForDelivery(ctx, target.ID, delivery); err != nil {
				return err
			}
			switch delivery.Protocol {
			case dispatchsvc.ProtocolSingleEnter:
				return tmux.PasteKeysWithDelayContext(ctx, target.ID, delivery.Message, true, delivery.EnterDelay)
			case dispatchsvc.ProtocolDoubleEnter:
				if err := tmux.SendKeysForAgentDoubleEnterContext(ctx, target.ID, delivery.Message, target.Type); err != nil {
					return err
				}
				return dispatchsvc.VerifyAgentSubmission(ctx, target.ID, delivery.Message, target.Type)
			default:
				return fmt.Errorf("unsupported shell send protocol %q", delivery.Protocol)
			}
		}),
		Lifecycle: dispatchsvc.LifecycleHooks{
			BeforeDispatch: beforeDispatch,
			AfterReceipt: func(_ context.Context, delivery dispatchsvc.Delivery, receipt dispatchsvc.Receipt) {
				if receipt.Status == dispatchsvc.ReceiptDelivered {
					addTimelinePromptMarker(session, delivery.Target.Pane, delivery.Message)
				}
			},
		},
	})
}

func distributeDispatchGate(session string) func(context.Context, dispatchsvc.Request, []dispatchsvc.Delivery) error {
	return func(ctx context.Context, _ dispatchsvc.Request, deliveries []dispatchsvc.Delivery) error {
		observation, err := observeAssignSession(ctx, session)
		if err != nil {
			return fmt.Errorf("observe distribute targets: %w", err)
		}
		activePanes, err := loadActiveAssignmentPanes(session)
		if err != nil {
			return fmt.Errorf("load distribute assignment occupancy: %w", err)
		}
		now := time.Now()
		for _, delivery := range deliveries {
			paneID := strings.TrimSpace(delivery.Target.Ref.ID)
			if paneID == "" {
				return fmt.Errorf("distribute target %s has no stable pane ID", delivery.Target.Address)
			}
			paneObservation, err := currentAssignPaneObservation(observation, paneID, now)
			if err != nil {
				return fmt.Errorf("observe distribute target %s (%s): %w", delivery.Target.Address, paneID, err)
			}
			if !paneObservation.SafeToDispatch() {
				return fmt.Errorf("distribute target %s (%s) is not freshly and confidently idle (state: %s)",
					delivery.Target.Address, paneID, paneObservation.Current.Status.State)
			}
			if _, active := activePanes[paneID]; active {
				return fmt.Errorf("distribute target %s (%s) already has a durable active assignment", delivery.Target.Address, paneID)
			}
		}
		return nil
	}
}

func activeShellDispatchRedactionConfig() redaction.Config {
	if cfg == nil {
		return redaction.Config{Mode: redaction.ModeOff}
	}
	return cfg.Redaction.ToRedactionLibConfig()
}

func shellPromptForOutput(prompt string) string {
	redactCfg := activeShellDispatchRedactionConfig()
	result := redaction.ScanAndRedact(prompt, redactCfg)
	if !result.Blocked {
		return result.Output
	}
	redactCfg.Mode = redaction.ModeRedact
	return redaction.ScanAndRedact(prompt, redactCfg).Output
}

func executeShellDispatch(
	ctx context.Context,
	session string,
	allPanes, selected []tmux.Pane,
	prompt string,
	dryRun bool,
) (dispatchsvc.Result, error) {
	service, err := newShellDispatchService(session, selected, activeShellDispatchRedactionConfig())
	if err != nil {
		return dispatchsvc.Result{}, err
	}
	request := shellDispatchRequest(session, allPanes, selected, prompt, false)
	request.DryRun = dryRun
	return service.Execute(ctx, request)
}

func shellDispatchRequest(session string, panes, selected []tmux.Pane, prompt string, stopOnFailure bool) dispatchsvc.Request {
	return dispatchsvc.Request{
		Session:       session,
		Panes:         panes,
		Selectors:     shellDispatchSelectors(selected),
		IncludeUser:   true,
		Message:       prompt,
		Submit:        true,
		StopOnFailure: stopOnFailure,
	}
}

// CodexGoalSendResult is the JSON receipt for `ntm send --codex-goal` (#165).
type CodexGoalSendResult struct {
	robot.RobotResponse

	Session string `json:"session"`
	Pane    string `json:"pane"`
	PaneID  string `json:"pane_id"`

	// TypedGoal is true once the "/goal" slash command was typed and the goal
	// palette engaged (Codex showed the /goal command, not literal chat text).
	TypedGoal bool `json:"typed_goal"`
	// BodyInjected is true once the packet body was injected after engagement.
	BodyInjected bool `json:"body_injected"`
	// Submitted is true once the goal was submitted (Enter sent).
	Submitted bool `json:"submitted"`
	// SubmitAttempts counts how many submit (Enter) attempts were made.
	SubmitAttempts int `json:"submit_attempts"`

	// State is the terminal goal-send state: engaged / submitted / failed.
	State string `json:"state"`
	// PaletteEngaged records the palette state observed after typing /goal.
	PaletteEngaged string `json:"palette_engaged"`
	// PreflightBefore is the preflight state captured before driving the pane.
	PreflightBefore string `json:"preflight_before"`
	// ProvenanceHash is the sha256 of the pre-drive capture (audit trail).
	ProvenanceHash string `json:"provenance_hash"`
	// BodyPreview is a short preview of the injected packet body.
	BodyPreview string `json:"body_preview"`
	// Reason explains the outcome (especially on refusal/failure).
	Reason string `json:"reason"`
}

// codexGoalEngageTimeout bounds the wait for the /goal palette to engage.
var (
	codexGoalEngageTimeout = 6 * time.Second
	codexGoalPollInterval  = 400 * time.Millisecond
)

type codexGoalCommandError struct {
	code string
	hint string
	err  error
}

func (e *codexGoalCommandError) Error() string { return e.err.Error() }
func (e *codexGoalCommandError) Unwrap() error { return e.err }

func newCodexGoalCommandError(err error, code, hint string) error {
	return &codexGoalCommandError{code: code, hint: hint, err: err}
}

func emitCodexGoalSendResult(res CodexGoalSendResult, failErr error, code, hint string, emitJSON bool) error {
	if failErr != nil {
		res.Success = false
		res.Error = failErr.Error()
		res.ErrorCode = code
		res.Hint = hint
	}
	if emitJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return fmt.Errorf("encode Codex goal-send response: %w", err)
		}
		if failErr != nil {
			return errors.Join(errJSONFailure, failErr)
		}
		return nil
	}
	fmt.Printf("Codex Goal Send\n===============\n\n")
	fmt.Printf("Session: %s  Pane: %s\n", res.Session, res.Pane)
	fmt.Printf("State:   %s\n", res.State)
	fmt.Printf("Typed /goal: %v  Body injected: %v  Submitted: %v (attempts %d)\n",
		res.TypedGoal, res.BodyInjected, res.Submitted, res.SubmitAttempts)
	fmt.Printf("Reason:  %s\n", res.Reason)
	return failErr
}

func waitCodexGoalContext(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return errors.New("codex goal wait context is required")
	}
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resolveCodexGoalPane(ctx context.Context, session, selector string) (*tmux.Pane, string, error) {
	if ctx == nil {
		return nil, "", newCodexGoalCommandError(errors.New("codex goal resolver context is required"), robot.ErrCodeInternalError, "Retry the command")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(session) == "" {
		return nil, "", newCodexGoalCommandError(errors.New("session is required"), robot.ErrCodeInvalidFlag, "Pass a session name")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, "", newCodexGoalCommandError(fmt.Errorf("invalid session name: %w", err), robot.ErrCodeInvalidFlag, "Pass a valid session name")
	}
	if strings.TrimSpace(selector) == "" {
		return nil, "", newCodexGoalCommandError(errors.New("pane selector is required"), robot.ErrCodeInvalidFlag, "Pass one pane as N, W.P, or %N")
	}
	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		return nil, "", newCodexGoalCommandError(fmt.Errorf("check Codex goal session: %w", err), robot.ErrCodeInternalError, "Check tmux availability")
	}
	if !exists {
		return nil, "", newCodexGoalCommandError(fmt.Errorf("session %q not found", session), robot.ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions")
	}
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		return nil, "", newCodexGoalCommandError(fmt.Errorf("list Codex goal panes: %w", err), robot.ErrCodeInternalError, "Check tmux availability")
	}
	selected, err := resolveShellSendSelectors(panes, []string{selector}, true)
	if err != nil {
		return nil, "", newCodexGoalCommandError(err, robot.ErrCodePaneNotFound, "Use 'ntm status <session>' to see canonical pane addresses")
	}
	target := &selected[0]
	content, err := tmux.CapturePaneVisibleContext(ctx, target.ID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, "", err
		}
		return nil, "", newCodexGoalCommandError(fmt.Errorf("capture Codex goal pane: %w", err), robot.ErrCodeInternalError, "Check the target pane and retry")
	}
	return target, content, nil
}

// runCodexGoalSend drives the Codex /goal slash-command flow on a single pane and
// emits a deterministic JSON receipt (#165). It refuses unless the pane is
// codex-live, types "/goal " as a real slash command, waits (via the palette/
// preflight classifier) for the goal palette to engage, injects the packet body,
// submits, and reports engaged / submitted / failed with provenance.

func runCodexGoalSend(ctx context.Context, session, paneSelector, body string) error {
	res := CodexGoalSendResult{
		RobotResponse:  robot.NewRobotResponse(false),
		Session:        session,
		Pane:           strings.TrimSpace(paneSelector),
		State:          "failed",
		PaletteEngaged: "none",
	}
	emit := func(failErr error, code, hint string) error {
		if errors.Is(failErr, context.Canceled) || errors.Is(failErr, context.DeadlineExceeded) {
			code = robot.ErrCodeTimeout
			hint = "Retry the goal send after cancellation"
		}
		return emitCodexGoalSendResult(res, failErr, code, hint, IsJSONOutput())
	}
	if ctx == nil {
		return emit(errors.New("codex goal send context is required"), robot.ErrCodeInternalError, "Retry the command")
	}
	if err := ctx.Err(); err != nil {
		return emit(err, robot.ErrCodeTimeout, "Retry the goal send after cancellation")
	}
	if strings.TrimSpace(paneSelector) == "" {
		return emit(
			fmt.Errorf("--codex-goal requires an explicit --pane"),
			robot.ErrCodeInvalidFlag,
			"Pass --pane <N|W.P|%N> identifying the Codex pane to drive",
		)
	}
	if strings.TrimSpace(body) == "" {
		return emit(
			fmt.Errorf("goal packet body is empty"),
			robot.ErrCodeInvalidFlag,
			"Provide the goal objective via --file <packet> or as the trailing argument",
		)
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return emit(err, robot.ErrCodeInternalError, "Install tmux and retry")
	}

	target, content, err := resolveCodexGoalPane(ctx, session, paneSelector)
	if err != nil {
		var commandErr *codexGoalCommandError
		if errors.As(err, &commandErr) {
			return emit(err, commandErr.code, commandErr.hint)
		}
		return emit(err, robot.ErrCodeInternalError, "Inspect the target Codex pane and retry")
	}

	sum := sha256.Sum256([]byte(content))
	res.Pane = fmt.Sprintf("%d.%d", target.WindowIndex, target.Index)
	res.PaneID = target.ID
	res.ProvenanceHash = hex.EncodeToString(sum[:])
	res.BodyPreview = truncatePrompt(strings.TrimSpace(body), 80)

	// Gate: refuse unless the pane is codex-live (proceed-class).
	pf := codex.Preflight(content)
	res.PreflightBefore = pf.State.String()
	if pf.Action != codex.ActionProceed {
		res.Reason = fmt.Sprintf("refused: pane preflight=%s action=%s; not safe to drive a goal here. %s",
			pf.State, pf.Action, pf.Reason)
		return emit(
			fmt.Errorf("pane not codex-live (preflight=%s)", pf.State),
			robot.ErrCodeResourceBusy,
			"Run 'ntm codex preflight' first; only drive a goal when state is codex-live/goal-completed",
		)
	}

	// (1) Type "/goal " as a slash command (literal, no Enter) so Codex opens the
	// slash palette and selects the /goal command rather than treating it as chat.
	if err := tmux.SendKeysContext(ctx, target.ID, "/goal ", false); err != nil {
		res.Reason = fmt.Sprintf("failed to type /goal: %v", err)
		return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys for /goal failed")
	}
	res.TypedGoal = true

	// (2) Wait for the goal palette to engage: Codex shows the /goal command
	// entry / goal palette. Poll the palette+preflight classifiers until engaged
	// or the bounded timeout elapses.
	engageCtx, cancelEngage := context.WithTimeout(ctx, codexGoalEngageTimeout)
	defer cancelEngage()
	engaged := false
	for {
		cap2, capErr := tmux.CapturePaneVisibleContext(engageCtx, target.ID)
		if capErr == nil {
			cls := codex.Classify(cap2)
			lower := strings.ToLower(cap2)
			// Engagement is asserted only by the classifier states or Codex's own
			// descriptive palette text ("set or view the goal"). Do NOT match a
			// bare "/goal" substring: that string is the literal we just typed and
			// is echoed on the visible screen regardless of whether Codex actually
			// opened the slash palette, which would falsely report engagement.
			if cls.State == codex.StateGoalPalettePrimed ||
				cls.State == codex.StateSlashPaletteOpen ||
				strings.Contains(lower, "set or view the goal") {
				res.PaletteEngaged = cls.State.String()
				engaged = true
				break
			}
		} else if errors.Is(capErr, context.Canceled) || errors.Is(capErr, context.DeadlineExceeded) {
			res.Reason = "goal palette observation was canceled"
			return emit(fmt.Errorf("observe goal palette: %w", capErr), robot.ErrCodeTimeout, "Retry after the Codex pane is responsive")
		}
		if err := waitCodexGoalContext(engageCtx, codexGoalPollInterval); err != nil {
			break
		}
	}
	if !engaged {
		res.State = "failed"
		res.Reason = "the /goal palette did not engage within the timeout; Codex may have changed its slash-command UI"
		return emit(
			fmt.Errorf("goal palette did not engage: %w", engageCtx.Err()),
			robot.ErrCodeTimeout,
			"Verify Codex is at a quiescent prompt and that /goal is still a slash command in this Codex version",
		)
	}
	res.State = "engaged"

	// (3) Inject the packet body (single-line objective; literal, no Enter). If
	// the body has newlines, flatten to spaces — /goal takes a one-line objective.
	objective := strings.Join(strings.Fields(body), " ")
	if err := tmux.SendKeysContext(ctx, target.ID, objective, false); err != nil {
		res.Reason = fmt.Sprintf("failed to inject goal body: %v", err)
		return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys for goal body failed")
	}
	res.BodyInjected = true

	// (4) Submit (Enter). One attempt; /goal is a single-line command.
	if err := waitCodexGoalContext(ctx, tmux.DefaultEnterDelay); err != nil {
		res.Reason = "goal submission canceled before Enter"
		return emit(fmt.Errorf("wait before goal submission: %w", err), robot.ErrCodeTimeout, "Retry the goal send")
	}
	res.SubmitAttempts = 1
	if err := tmux.SendKeysContext(ctx, target.ID, "", true); err != nil {
		res.Reason = fmt.Sprintf("failed to submit goal: %v", err)
		return emit(err, robot.ErrCodePromptSendFailed, "tmux send-keys Enter (submit) failed")
	}
	res.Submitted = true
	res.State = "submitted"
	res.Success = true
	res.Reason = "typed /goal, palette engaged, injected objective, and submitted. Use 'ntm codex wait-goal-engaged' to confirm engagement."

	addTimelinePromptMarker(session, *target, "/goal "+objective)
	return emit(nil, "", "")
}

func addTimelinePromptMarker(session string, p tmux.Pane, prompt string) {
	if strings.Compare(session, "") == 0 {
		return
	}
	if sendValueEqual(p.Type, tmux.AgentUser) || sendValueEqual(p.Type, tmux.AgentUnknown) {
		return
	}
	agentID := timelineAgentIDFromPane(p)
	if strings.Compare(agentID, "") == 0 {
		return
	}
	tracker := state.GetGlobalTimelineTracker()
	tracker.AddMarker(state.TimelineMarker{
		AgentID:   agentID,
		SessionID: session,
		Type:      state.MarkerPrompt,
		Timestamp: time.Now(),
		Message:   truncatePrompt(prompt, 80),
	})
}

func addTimelineStopMarkers(session string, panes []tmux.Pane) {
	if session == "" {
		return
	}
	tracker := state.GetGlobalTimelineTracker()
	now := time.Now()

	if len(panes) == 0 {
		events := tracker.GetEventsForSession(session, time.Time{})
		seen := make(map[string]struct{})
		for _, e := range events {
			if e.AgentID == "" {
				continue
			}
			if _, ok := seen[e.AgentID]; ok {
				continue
			}
			seen[e.AgentID] = struct{}{}
			tracker.AddMarker(state.TimelineMarker{
				AgentID:   e.AgentID,
				SessionID: session,
				Type:      state.MarkerStop,
				Timestamp: now,
			})
		}
		return
	}

	for _, p := range panes {
		if sendValueEqual(p.Type, tmux.AgentUser) || sendValueEqual(p.Type, tmux.AgentUnknown) {
			continue
		}
		agentID := timelineAgentIDFromPane(p)
		if strings.Compare(agentID, "") == 0 {
			continue
		}
		tracker.AddMarker(state.TimelineMarker{
			AgentID:   agentID,
			SessionID: session,
			Type:      state.MarkerStop,
			Timestamp: now,
		})
	}
}

func timelineAgentIDFromPane(p tmux.Pane) string {
	if p.NTMIndex > 0 && sendValueNotEqual(p.Type, tmux.AgentUnknown) && sendValueNotEqual(p.Type, tmux.AgentUser) {
		return fmt.Sprintf("%s_%d", p.Type, p.NTMIndex)
	}
	if strings.Compare(p.Title, "") != 0 {
		if suffix := tmux.PaneTitleSuffix(p.Title); suffix != "" {
			return suffix
		}
		return p.Title
	}
	if p.ID != "" {
		return p.ID
	}
	return ""
}

func logDCGBlocked(command, session string, panes []tmux.Pane, blocked *tools.BlockedCommand) {
	config := dcg.DefaultAuditLoggerConfig()
	if cfg != nil && cfg.Integrations.DCG.AuditLog != "" {
		config.Path = cfg.Integrations.DCG.AuditLog
	}
	logger, err := dcg.NewAuditLogger(config)
	if err != nil {
		if !jsonOutput {
			fmt.Printf("⚠ DCG audit log unavailable: %v\n", err)
		}
		return
	}
	defer func() {
		_ = logger.Close()
	}()

	rule := strings.TrimSpace(blocked.Reason)
	if rule == "" {
		rule = "blocked"
	}
	output := strings.TrimSpace(blocked.Reason)
	if output == "" {
		output = "blocked"
	}

	for _, p := range panes {
		if !isNonClaudeAgent(p) {
			continue
		}
		paneLabel := p.Title
		if paneLabel == "" {
			if p.ID != "" {
				paneLabel = p.ID
			} else {
				paneLabel = fmt.Sprintf("pane_%d", p.Index)
			}
		}
		_ = logger.LogBlocked(command, paneLabel, session, rule, output)
	}
}

func resolveSendSessionForCommand(session string) (string, bool, error) {
	return resolveSendSessionForCommandContext(context.Background(), session)
}

func resolveSendSessionForCommandContext(ctx context.Context, session string) (string, bool, error) {
	if ctx == nil {
		return "", false, errors.New("send session resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return "", false, err
	}

	var res SessionResolution
	if session != "" {
		if err := tmux.ValidateSessionName(session); err != nil {
			return "", false, fmt.Errorf("invalid session name: %w", err)
		}
		sessions, err := tmux.ListSessionsContext(ctx)
		if err != nil {
			return "", false, err
		}
		resolved, reason, err := resolveExplicitSessionName(session, sessions, !IsJSONOutput())
		if err != nil {
			return "", false, err
		}
		res = SessionResolution{Session: resolved, Reason: reason}
	} else {
		var err error
		res, err = ResolveSession(session, os.Stdout)
		if err != nil {
			return "", false, err
		}
	}
	if res.Session == "" {
		return "", false, fmt.Errorf("session is required")
	}
	exists, err := tmux.SessionExistsContext(ctx, res.Session)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "", false, fmt.Errorf("session '%s' not found", res.Session)
	}
	return res.Session, res.Inferred, nil
}

func checkCassDuplicates(ctx context.Context, session string, inferred bool, prompt string, threshold float64, days int, forceNonInteractive, silent bool) error {
	var opts []cass.ClientOption
	if cfg != nil && cfg.CASS.BinaryPath != "" {
		opts = append(opts, cass.WithBinaryPath(cfg.CASS.BinaryPath))
	}
	client := cass.NewClient(opts...)
	if !client.IsInstalled() {
		return fmt.Errorf("cass not installed")
	}

	// Get workspace from session
	dir := getSessionWorkingDir(ctx, session, inferred)
	if dir == "" {
		return fmt.Errorf("getting project root failed")
	}

	since := fmt.Sprintf("%dd", days)

	res, err := client.CheckDuplicates(ctx, cass.DuplicateCheckOptions{
		Query:     prompt,
		Workspace: dir,
		Since:     since,
		Threshold: threshold,
	})
	if err != nil {
		// CASS is installed but the user hasn't run `cass index --full`
		// yet (issue acfs#266). Don't fail the send — the dedup check
		// is best-effort, and a fresh install can't have any history
		// to dedup against anyway. Warn the user once and proceed as
		// if no duplicates were found.
		if errors.Is(err, cass.ErrNotInitialized) {
			if !jsonOutput && !silent {
				fmt.Fprintf(os.Stderr,
					"\033[33mwarning\033[0m: cass is installed but not initialized; "+
						"skipping dedup check.\n"+
						"        run \033[1mcass index --full\033[0m once to enable session "+
						"deduplication on subsequent sends.\n")
			}
			return nil
		}
		return err
	}

	if res.DuplicatesFound {
		// --force-non-interactive: continue without confirmation, log to stderr so
		// the warning doesn't leak into machine-readable stdout (JSON pipelines).
		if forceNonInteractive {
			if !jsonOutput && !silent {
				fmt.Fprintf(os.Stderr,
					"warning: CASS duplicate check found %d similar session(s); "+
						"continuing because --force-non-interactive was used.\n",
					len(res.SimilarSessions))
			}
			return nil
		}
		if jsonOutput || silent {
			return fmt.Errorf("duplicates found in CASS: %d similar sessions", len(res.SimilarSessions))
		}

		// Never block a non-interactive caller on the [y/N] confirm: an
		// orchestrator loop with a quiet stdin pipe hung here forever, and
		// EOF aborted the send with a misleading "aborted by user" (AP-2 /
		// ntm-dv50). Non-TTY behaves like --force-non-interactive:
		// proceed with a stderr warning.
		if !isTTY() || os.Getenv("NTM_NONINTERACTIVE") != "" {
			fmt.Fprintf(os.Stderr,
				"warning: CASS duplicate check found %d similar session(s); "+
					"continuing because stdin/stdout is not a TTY.\n",
				len(res.SimilarSessions))
			return nil
		}

		// Interactive mode
		fmt.Printf("\n%s⚠ Similar work found in past sessions:%s\n", "\033[33m", "\033[0m")
		for i, hit := range res.SimilarSessions {
			fmt.Printf("  %d. \"%s\" (%s, %s)\n", i+1, hit.Title, hit.Agent, hit.SourcePath)
			if hit.Snippet != "" {
				fmt.Printf("     Preview: %s\n", strings.TrimSpace(hit.Snippet))
			}
			fmt.Println()
		}

		if !confirm("Continue anyway?") {
			return fmt.Errorf("aborted by user")
		}
	}

	return nil
}

// runDistributeMode implements the --distribute flag behavior.
// It gets prioritized work from bv triage and distributes tasks to idle agents.
func runDistributeMode(ctx context.Context, session, strategy string, limit int, autoExecute bool, dryRun bool, randomize bool, seed int64) error {
	th := theme.Current()

	outputError := func(err error) error {
		return outputSendCommandError(session, err)
	}
	if ctx == nil {
		return outputError(errors.New("distribute context is required"))
	}
	if err := ctx.Err(); err != nil {
		return outputError(fmt.Errorf("distribute canceled: %w", err))
	}

	// Check if bv is installed
	if !bv.IsInstalled() {
		return outputError(fmt.Errorf("bv (beads graph triage) is not installed; cannot use --distribute"))
	}

	resolvedSession, _, err := resolveSendSessionForCommandContext(ctx, session)
	if err != nil {
		return outputError(fmt.Errorf("resolve distribute session: %w", err))
	}
	session = resolvedSession
	projectDir, err := resolveExplicitProjectDirForSessionContext(ctx, session)
	if err != nil {
		return outputError(fmt.Errorf("resolve distribute project for session %s: %w", session, err))
	}
	if err := configureAuthoritativeAssignmentPolicy(projectDir); err != nil {
		return outputError(err)
	}

	// Get assignment recommendations using robot module
	opts := robot.AssignOptions{
		Session:    session,
		ProjectDir: projectDir,
		Strategy:   strategy,
	}

	recs, err := robot.GetAssignRecommendations(ctx, opts)
	if err != nil {
		return outputError(fmt.Errorf("getting assignment recommendations: %w", err))
	}

	if len(recs) == 0 {
		if jsonOutput {
			result := map[string]interface{}{
				"success":     true,
				"session":     session,
				"distributed": 0,
				"message":     "no work to distribute or no idle agents available",
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Println("No work to distribute or no idle agents available.")
		return nil
	}

	// Apply limit if specified
	if limit > 0 && len(recs) > limit {
		recs = recs[:limit]
	}

	seedUsed := int64(0)
	if randomize && len(recs) > 1 {
		var perm []int
		seedUsed, perm = shuffledPermutation(len(recs), seed)
		shuffled := make([]robot.DistributeRecommendation, 0, len(recs))
		for _, idx := range perm {
			if idx < 0 || idx >= len(recs) {
				continue
			}
			shuffled = append(shuffled, recs[idx])
		}
		if len(shuffled) == len(recs) {
			recs = shuffled
		}
		if !jsonOutput {
			order := make([]string, 0, len(recs))
			for _, r := range recs {
				order = append(order, r.PaneTarget)
			}
			fmt.Fprintf(os.Stderr, "Randomized distribute order (seed=%d): %v\n", seedUsed, order)
		}
	}

	// Style helpers
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(th.Primary))
	beadStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Secondary))
	agentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Success))

	// Show preview
	if !jsonOutput {
		fmt.Println()
		fmt.Println(titleStyle.Render("📤 Work Distribution Plan"))
		fmt.Println()
		fmt.Printf("Session: %s | Strategy: %s | Tasks: %d\n\n", session, strategy, len(recs))

		for i, rec := range recs {
			fmt.Printf("  %d. %s → %s\n",
				i+1,
				beadStyle.Render(fmt.Sprintf("[%s] %s", rec.BeadID, rec.Title)),
				agentStyle.Render(fmt.Sprintf("Pane %s (%s, %s)", rec.PaneTarget, rec.PaneID, rec.AgentType)))
			if rec.Reason != "" {
				fmt.Printf("     Reason: %s\n", rec.Reason)
			}
		}
		fmt.Println()
	}

	// JSON preview mode returns the plan. --dist-auto continues through the same
	// unified dispatch path as human output and returns per-task receipts below.
	if jsonOutput && (dryRun || !autoExecute) {
		result := map[string]interface{}{
			"success":         true,
			"session":         session,
			"strategy":        strategy,
			"recommendations": recs,
			"count":           len(recs),
		}
		if randomize {
			result["randomized"] = true
			result["seed_used"] = seedUsed
		}
		if dryRun {
			result["dry_run"] = true
			result["preview"] = true
			result["message"] = "use --dist-auto to execute"
		} else if !autoExecute {
			result["preview"] = true
			result["message"] = "use --dist-auto to execute"
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	// If not auto mode, ask for confirmation
	if dryRun {
		fmt.Println("Dry run - no prompts sent.")
		return nil
	}
	if !autoExecute {
		if !confirm("Distribute these tasks?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Execute distribution against the stable pane identity captured by the
	// recommendation. Each task has a distinct prompt, so it receives its own
	// unified dispatch request and safe receipt.
	var delivered, failed int
	receipts := make([]DistributeDispatchReceipt, 0, len(recs))
	for i := range recs {
		rec := recs[i]
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("distribute canceled before %s: %w", rec.BeadID, err))
		}
		validated, err := revalidateDistributeRecommendation(ctx, projectDir, rec)
		if err != nil {
			return outputError(fmt.Errorf("revalidate distribute bead %s immediately before dispatch: %w", rec.BeadID, err))
		}
		rec = validated
		recs[i] = validated
		// Build the prompt for this task
		taskPrompt := fmt.Sprintf("Please work on this task:\n\n**[%s] %s**\n\nNTM has atomically claimed this task for this pane.",
			rec.BeadID, rec.Title)

		dispatchResult, dispatchErr := executeDistributeDispatch(ctx, projectDir, session, rec, taskPrompt)
		if err := ctx.Err(); err != nil {
			return outputError(fmt.Errorf("distribute canceled while dispatching %s: %w", rec.BeadID, err))
		}
		receipt := buildDistributeDispatchReceipt(rec, dispatchResult, dispatchErr)
		receipts = append(receipts, receipt)
		if dispatchErr != nil || !dispatchResult.Atomic.Sent {
			if !jsonOutput {
				failure := dispatchErr
				if failure == nil {
					failure = errors.New("atomic dispatch did not produce a durable sent receipt")
				}
				fmt.Printf("  ✗ Failed to send to pane %s (%s): %v\n", rec.PaneTarget, rec.PaneID, failure)
			}
			failed++
			continue
		}
		if !dispatchResult.Atomic.Replayed {
			bestEffortStampBeadLabel(
				projectDir,
				rec.BeadID,
				session,
				dispatchResult.Target.WindowIndex,
				dispatchResult.Target.Index,
			)
		}

		if !jsonOutput {
			fmt.Printf("  ✓ Sent [%s] to pane %s (%s, %s)\n", rec.BeadID, rec.PaneTarget, rec.PaneID, rec.AgentType)
		}
		delivered++
	}

	// Summary
	if jsonOutput {
		result := map[string]interface{}{
			"success":         failed == 0,
			"session":         session,
			"strategy":        strategy,
			"recommendations": recs,
			"delivered":       delivered,
			"failed":          failed,
			"receipts":        receipts,
		}
		if failed > 0 {
			cause := &DistributeDispatchError{Delivered: delivered, Failed: failed}
			return emitJSONFailureEnvelopeWithCause(result, cause)
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Println()
	if failed == 0 {
		fmt.Printf("✓ Successfully distributed %d tasks\n", delivered)
	} else {
		fmt.Printf("Distributed %d tasks (%d failed)\n", delivered, failed)
	}
	if failed > 0 {
		return &DistributeDispatchError{Delivered: delivered, Failed: failed}
	}

	return nil
}

// DistributeDispatchError reports an executed plan with one or more failed
// deliveries. All per-task receipts are emitted before this error is returned.
type DistributeDispatchError struct {
	Delivered int
	Failed    int
}

func (e *DistributeDispatchError) Error() string {
	if e == nil {
		return "distribute dispatch failed"
	}
	return fmt.Sprintf("distributed %d tasks with %d failed deliveries", e.Delivered, e.Failed)
}

// DistributeDispatchReceipt maps one task to the safe unified-dispatch receipt
// without retaining the outbound prompt.
type DistributeDispatchReceipt struct {
	BeadID         string                       `json:"bead_id"`
	PaneID         string                       `json:"pane_id"`
	PaneTarget     string                       `json:"pane_target"`
	Status         dispatchsvc.ReceiptStatus    `json:"status"`
	Protocol       dispatchsvc.DeliveryProtocol `json:"protocol,omitempty"`
	Redaction      dispatchsvc.RedactionReceipt `json:"redaction"`
	DeliveryID     string                       `json:"delivery_id,omitempty"`
	IdempotencyKey string                       `json:"idempotency_key,omitempty"`
	ClaimActor     string                       `json:"claim_actor,omitempty"`
	Replayed       bool                         `json:"replayed,omitempty"`
	Recovered      bool                         `json:"recovered,omitempty"`
	Error          string                       `json:"error,omitempty"`
}

func revalidateDistributeRecommendation(ctx context.Context, projectDir string, rec robot.DistributeRecommendation) (robot.DistributeRecommendation, error) {
	if ctx == nil {
		return rec, errors.New("distribute revalidation context is required")
	}
	if err := ctx.Err(); err != nil {
		return rec, err
	}
	details, err := bv.GetBeadAssignmentDetailsContext(ctx, projectDir, rec.BeadID)
	if err != nil {
		return rec, fmt.Errorf("read live bead details: %w", err)
	}
	if err := validateDistributeBeadDetailsForProject(projectDir, rec, details, time.Now()); err != nil {
		return rec, err
	}
	if title := strings.TrimSpace(details.Title); title != "" {
		rec.Title = title
	}
	return rec, nil
}

func validateDistributeBeadDetails(rec robot.DistributeRecommendation, details *bv.BeadAssignmentDetails, now time.Time) error {
	return validateDistributeBeadDetailsWithGate(rec, details, now, bv.IsOperatorGatedLabel)
}

func validateDistributeBeadDetailsForProject(projectDir string, rec robot.DistributeRecommendation, details *bv.BeadAssignmentDetails, now time.Time) error {
	return validateDistributeBeadDetailsWithGate(rec, details, now, func(label string) bool {
		return bv.IsOperatorGatedLabelForProject(projectDir, label)
	})
}

func validateDistributeBeadDetailsWithGate(rec robot.DistributeRecommendation, details *bv.BeadAssignmentDetails, now time.Time, operatorGated func(string) bool) error {
	beadID := strings.TrimSpace(rec.BeadID)
	if beadID == "" {
		return errors.New("distribute recommendation has no bead ID")
	}
	if details == nil {
		return fmt.Errorf("bead %s has no live assignment details", beadID)
	}
	liveID := strings.TrimSpace(details.ID)
	if liveID != beadID {
		return fmt.Errorf("live bead ID %q does not match recommendation %q", liveID, beadID)
	}
	if len(details.BlockedBy) > 0 {
		return fmt.Errorf("bead %s is blocked by dependencies: %s", beadID, strings.Join(details.BlockedBy, ", "))
	}
	for _, label := range details.Labels {
		if operatorGated(label) {
			return fmt.Errorf("bead %s is operator-gated by label %q", beadID, strings.TrimSpace(label))
		}
	}
	if status := normalizeBeadStatus(details.Status); status != "open" {
		return fmt.Errorf("bead %s has status %q, want open", beadID, strings.TrimSpace(details.Status))
	}
	if assignee := strings.TrimSpace(details.Assignee); assignee != "" {
		return fmt.Errorf("bead %s is already assigned to %q", beadID, assignee)
	}
	if details.DeferUntil != nil && details.DeferUntil.After(now) {
		return fmt.Errorf("bead %s is deferred until %s", beadID, details.DeferUntil.UTC().Format(time.RFC3339))
	}
	if details.Pinned {
		return fmt.Errorf("bead %s is pinned", beadID)
	}
	if details.Ephemeral {
		return fmt.Errorf("bead %s is ephemeral", beadID)
	}
	if details.Template {
		return fmt.Errorf("bead %s is a template", beadID)
	}
	if details.Wisp || strings.Contains(strings.ToLower(liveID), "-wisp-") {
		return fmt.Errorf("bead %s is a wisp", beadID)
	}
	return nil
}

type distributeAtomicDispatchResult struct {
	Atomic         assignment.AtomicResult
	Target         tmux.Pane
	Redaction      dispatchsvc.RedactionReceipt
	Protocol       dispatchsvc.DeliveryProtocol
	IdempotencyKey string
}

type distributeAtomicExecutor interface {
	Execute(context.Context, assignment.AtomicRequest) (assignment.AtomicResult, error)
}

var (
	getDistributePanes          = tmux.GetPanesContext
	loadDistributeStore         = assignment.LoadStoreStrict
	newDistributeAtomicExecutor = func(store *assignment.AssignmentStore, projectDir string) distributeAtomicExecutor {
		return newCLIAtomicAssignmentCoordinator(store, projectDir, nil)
	}
)

func executeDistributeDispatch(ctx context.Context, projectDir, session string, rec robot.DistributeRecommendation, prompt string) (distributeAtomicDispatchResult, error) {
	if ctx == nil {
		return distributeAtomicDispatchResult{}, errors.New("distribute dispatch context is required")
	}
	if err := ctx.Err(); err != nil {
		return distributeAtomicDispatchResult{}, err
	}
	panes, err := getDistributePanes(ctx, session)
	if err != nil {
		return distributeAtomicDispatchResult{}, fmt.Errorf("refresh distribute topology: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return distributeAtomicDispatchResult{}, err
	}
	panes = sortPanesByTopology(panes)
	target, err := resolveDistributeRecommendationPane(rec, panes)
	if err != nil {
		return distributeAtomicDispatchResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return distributeAtomicDispatchResult{}, err
	}
	store, err := loadDistributeStore(session)
	if err != nil {
		return distributeAtomicDispatchResult{}, fmt.Errorf("load distribute assignment ledger: %w", err)
	}
	stableTarget := assignmentPaneStableKey(target)
	agentType := string(target.Type.Canonical())
	agentName := assignmentAgentIdentityForPane(projectDir, session, agentType, target, tmux.PanesSpanMultipleWindows(panes))
	stampedPrompt := stampMarchingOrders(prompt, session, target.WindowIndex, target.Index)
	idempotencyKey := distributeAssignmentIdempotencyKey(
		session,
		rec.BeadID,
		stableTarget,
		stampedPrompt,
		store.ClearedGeneration(rec.BeadID),
	)
	result := distributeAtomicDispatchResult{
		Target:         target,
		Redaction:      distributeRedactionReceipt(stampedPrompt),
		IdempotencyKey: idempotencyKey,
	}
	coordinator := newDistributeAtomicExecutor(store, projectDir)
	if coordinator == nil {
		return result, errors.New("distribute atomic assignment coordinator is not configured")
	}
	result.Atomic, err = coordinator.Execute(ctx, assignment.AtomicRequest{
		BeadID:         strings.TrimSpace(rec.BeadID),
		BeadTitle:      strings.TrimSpace(rec.Title),
		Target:         stableTarget,
		OccupancyKey:   stableTarget,
		Pane:           target.Index,
		AgentType:      agentType,
		AgentName:      agentName,
		Actor:          fmt.Sprintf("%s-distribute-%s", session, agentName),
		Prompt:         stampedPrompt,
		IdempotencyKey: idempotencyKey,
	})
	result.Protocol = distributeProtocolFromDeliveryID(result.Atomic.Dispatch.DeliveryID)
	return result, err
}

func distributeAssignmentIdempotencyKey(session, beadID, paneID, prompt string, clearedGeneration uint64) string {
	parts := []string{
		"ntm/distribute/v1",
		strings.TrimSpace(session),
		strings.TrimSpace(beadID),
		strings.TrimSpace(paneID),
		assignment.PromptSHA256(prompt),
	}
	if clearedGeneration > 0 {
		parts = append(parts, fmt.Sprintf("cleared-generation=%d", clearedGeneration))
	}
	return assignment.AssignmentIdempotencyKey(parts...)
}

func distributeProtocolFromDeliveryID(deliveryID string) dispatchsvc.DeliveryProtocol {
	parts := strings.Split(strings.TrimSpace(deliveryID), "/")
	if len(parts) != 3 {
		return ""
	}
	protocol := dispatchsvc.DeliveryProtocol(parts[1])
	switch protocol {
	case dispatchsvc.ProtocolStageOnly, dispatchsvc.ProtocolSingleEnter, dispatchsvc.ProtocolDoubleEnter:
		return protocol
	default:
		return ""
	}
}

func distributeRedactionReceipt(prompt string) dispatchsvc.RedactionReceipt {
	result := redaction.ScanAndRedact(prompt, activeShellDispatchRedactionConfig())
	categories := make(map[string]int, len(result.Findings))
	for _, finding := range result.Findings {
		categories[string(finding.Category)]++
	}
	if len(categories) == 0 {
		categories = nil
	}
	return dispatchsvc.RedactionReceipt{
		Mode:       string(result.Mode),
		Findings:   len(result.Findings),
		Categories: categories,
		Blocked:    result.Blocked,
	}
}

func resolveDistributeRecommendationPane(rec robot.DistributeRecommendation, panes []tmux.Pane) (tmux.Pane, error) {
	paneID := strings.TrimSpace(rec.PaneID)
	if paneID == "" {
		return tmux.Pane{}, fmt.Errorf("distribute recommendation for %s has no stable pane ID", rec.BeadID)
	}
	var matches []tmux.Pane
	for _, pane := range panes {
		if pane.ID == paneID {
			matches = append(matches, pane)
		}
	}
	if len(matches) != 1 {
		return tmux.Pane{}, fmt.Errorf("distribute recommendation for %s resolved pane ID %s to %d live panes", rec.BeadID, paneID, len(matches))
	}
	target := matches[0]
	expectedType := tmux.AgentType(strings.TrimSpace(rec.AgentType)).Canonical()
	if expectedType.IsValid() && target.Type.Canonical() != expectedType {
		return tmux.Pane{}, fmt.Errorf("distribute recommendation for %s expected %s at pane ID %s, found %s",
			rec.BeadID, expectedType, paneID, target.Type.Canonical())
	}
	return target, nil
}

func buildDistributeDispatchReceipt(rec robot.DistributeRecommendation, result distributeAtomicDispatchResult, dispatchErr error) DistributeDispatchReceipt {
	receipt := DistributeDispatchReceipt{
		BeadID: rec.BeadID, PaneID: rec.PaneID, PaneTarget: rec.PaneTarget,
		Status: dispatchsvc.ReceiptFailed, Protocol: result.Protocol, Redaction: result.Redaction,
		DeliveryID: result.Atomic.Dispatch.DeliveryID, IdempotencyKey: result.IdempotencyKey,
		Replayed: result.Atomic.Replayed, Recovered: result.Atomic.Recovered,
	}
	if result.Atomic.Assignment != nil {
		receipt.ClaimActor = result.Atomic.Assignment.ClaimActor
		if receipt.DeliveryID == "" {
			receipt.DeliveryID = result.Atomic.Assignment.DispatchReceiptID
		}
	}
	if result.Atomic.Sent {
		receipt.Status = dispatchsvc.ReceiptDelivered
		if receipt.Protocol == "" {
			receipt.Protocol = distributeProtocolFromDeliveryID(receipt.DeliveryID)
		}
	}
	if dispatchErr != nil {
		receipt.Error = dispatchErr.Error()
	}
	return receipt
}

// BatchResult represents the JSON output for batch send operations
type BatchResult struct {
	Success              bool                `json:"success"`
	Session              string              `json:"session"`
	NonInteractiveForced bool                `json:"non_interactive_forced,omitempty"`
	Randomized           bool                `json:"randomized,omitempty"`
	SeedUsed             int64               `json:"seed_used,omitempty"`
	PriorityOrdered      bool                `json:"priority_ordered,omitempty"`
	Order                []string            `json:"order,omitempty"` // BatchPrompt.Source in execution order (for debugging/tests)
	Total                int                 `json:"batch_total"`
	Delivered            int                 `json:"batch_delivered"`
	Failed               int                 `json:"batch_failed"`
	Skipped              int                 `json:"batch_skipped"`
	Results              []BatchPromptResult `json:"results"`
	Error                string              `json:"error,omitempty"`
}

func emitBatchResult(result BatchResult, cause error) error {
	if result.Success {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	if cause == nil {
		cause = fmt.Errorf("batch send failed: %d prompt(s) failed", result.Failed)
	}
	return emitJSONFailureEnvelopeWithCause(result, cause)
}

// BatchPromptResult represents the result of sending a single prompt in a batch
type BatchPromptResult struct {
	Index         int      `json:"index"`
	PromptPreview string   `json:"prompt_preview"`
	Priority      int      `json:"priority,omitempty"` // -1 omitted; 0..4 = P0..P4
	Success       bool     `json:"success"`
	Targets       []string `json:"targets,omitempty"`
	Delivered     int      `json:"delivered"`
	Error         string   `json:"error,omitempty"`
	Skipped       bool     `json:"skipped,omitempty"`
}

type BatchPrompt struct {
	Text     string
	Source   string
	Priority int // -1 = unset; 0..4 = P0..P4 (lower = higher priority)
}

// parseBatchFile reads and parses a batch file into individual prompts.
// Supports two formats:
// 1. One prompt per line (simple)
// 2. Multi-line prompts separated by "---" on its own line
// Lines starting with # are treated as comments and ignored.
func parseBatchFile(path string) ([]BatchPrompt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading batch file: %w", err)
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("batch file is empty")
	}

	var prompts []BatchPrompt

	// Check if file uses --- separators
	lines := strings.Split(content, "\n")
	if strings.Contains(content, "\n---\n") || strings.HasPrefix(content, "---\n") {
		// Multi-line format with --- separators (track source as first non-comment line of each block).
		var blockLines []string
		blockStartLine := 0

		flushBlock := func() {
			raw := strings.Join(blockLines, "\n")
			cleaned := removeComments(raw)
			if cleaned == "" || blockStartLine == 0 {
				return
			}
			prompts = append(prompts, BatchPrompt{
				Text:     cleaned,
				Source:   fmt.Sprintf("line:%d", blockStartLine),
				Priority: parsePriorityAnnotation(raw),
			})
		}

		for i, line := range lines {
			lineNo := i + 1
			trimmed := strings.TrimSpace(line)
			if strings.Compare(trimmed, "---") == 0 {
				flushBlock()
				blockLines = nil
				blockStartLine = 0
				continue
			}
			blockLines = append(blockLines, line)
			if blockStartLine == 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				blockStartLine = lineNo
			}
		}
		flushBlock()
	} else {
		// Simple one-prompt-per-line format.
		// Track last priority annotation so "# priority: N" applies to the next prompt.
		pendingPriority := -1
		for i, line := range lines {
			lineNo := i + 1
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "#") {
				if p := parsePriorityAnnotation(trimmed); p >= 0 {
					pendingPriority = p
				}
				continue
			}
			prompts = append(prompts, BatchPrompt{
				Text:     trimmed,
				Source:   fmt.Sprintf("line:%d", lineNo),
				Priority: pendingPriority,
			})
			pendingPriority = -1
		}
	}

	if len(prompts) == 0 {
		return nil, errors.New("batch file contains no prompts (all lines are comments or empty)")
	}

	return prompts, nil
}

// removeComments removes comment lines (starting with #) from text
func removeComments(text string) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			lines = append(lines, line)
		}
	}
	result := strings.Join(lines, "\n")
	return strings.TrimSpace(result)
}

// parsePriorityAnnotation extracts a priority value from a "# priority: N" comment.
// Returns -1 if no priority annotation is found.
func parsePriorityAnnotation(text string) int {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Strip leading # and whitespace
		comment := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		lower := strings.ToLower(comment)
		if strings.HasPrefix(lower, "priority:") {
			valStr := strings.TrimSpace(strings.TrimPrefix(lower, "priority:"))
			if len(valStr) == 1 && valStr[0] >= '0' && valStr[0] <= '4' {
				return int(valStr[0] - '0')
			}
		}
	}
	return -1
}

// sortBatchByPriority performs a stable sort of batch prompts by priority.
// Lower priority values sort first (P0 before P1). Prompts without a priority
// annotation (Priority == -1) sort last.
func sortBatchByPriority(prompts []BatchPrompt) {
	sort.SliceStable(prompts, func(i, j int) bool {
		pi, pj := prompts[i].Priority, prompts[j].Priority
		// Both unset: preserve order
		if sendValueEqual(pi, -1) && sendValueEqual(pj, -1) {
			return false
		}
		// Unset sorts last
		if sendValueEqual(pi, -1) {
			return false
		}
		if sendValueEqual(pj, -1) {
			return true
		}
		return pi < pj
	})
}

// truncateForPreview shortens a string for display/logging
func truncateForPreview(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// batchAction represents a user choice when an error occurs during batch processing
type batchAction int

const (
	batchContinue batchAction = iota
	batchSkip
	batchAbort
)

// promptBatchAction asks the user what to do when an error occurs during batch processing
func promptBatchAction(prompt string) batchAction {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s (c=continue, s=skip, a=abort) [c]: ", prompt)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	switch answer {
	case "s", "skip":
		return batchSkip
	case "a", "abort":
		return batchAbort
	default:
		return batchContinue
	}
}

// filterPanesForBatch applies target and tag filters to the given panes
func filterPanesForBatch(panes []tmux.Pane, opts SendOptions) []tmux.Pane {
	var filtered []tmux.Pane

	// Determine if we have any filters
	hasTargets := len(opts.Targets) > 0
	hasTags := len(opts.Tags) > 0
	noFilter := !hasTargets && !hasTags && !opts.TargetAll

	for i, p := range panes {
		if opts.SkipFirst && i == 0 {
			continue
		}
		// If --all, include every agent pane; the user pane joins only with
		// an explicit --include-user (ntm-hykz).
		if opts.TargetAll {
			if !opts.IncludeUser && sendValueEqual(p.Type, tmux.AgentUser) {
				continue
			}
			filtered = append(filtered, p)
			continue
		}

		// If no filters specified, include all non-user panes
		if noFilter {
			if sendValueNotEqual(p.Type, tmux.AgentUser) {
				filtered = append(filtered, p)
			}
			continue
		}

		// Skip user panes unless --all was specified
		if sendValueEqual(p.Type, tmux.AgentUser) {
			continue
		}

		// Apply tag filter (OR logic)
		if hasTags {
			if !HasAnyTag(p.Tags, opts.Tags) {
				continue
			}
		}

		// Apply agent type filter
		if hasTargets {
			if !opts.Targets.MatchesPane(p) {
				continue
			}
		}

		filtered = append(filtered, p)
	}

	return filtered
}

// runSendBatch handles --batch mode: send multiple prompts from file
func runSendBatch(opts SendOptions) error {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("batch send canceled: %w", err)
	}
	// Parse the batch file
	prompts, err := parseBatchFile(opts.BatchFile)
	if err != nil {
		return err
	}

	// Prepend base prompt to each batch prompt (bd-3ejl)
	if opts.BasePrompt != "" {
		for i := range prompts {
			prompts[i].Text = applyBasePrompt(opts.BasePrompt, prompts[i].Text)
		}
	}

	// Sort by priority annotation if --priority-order (bd-2wzs).
	// Applied before randomization so priority wins.
	if opts.PriorityOrder {
		sortBatchByPriority(prompts)
	}

	jsonOutput := IsJSONOutput()
	total := len(prompts)

	seedUsed := int64(0)
	if opts.Randomize && total > 1 {
		var perm []int
		seedUsed, perm = shuffledPermutation(total, opts.Seed)
		prompts = permuteBatchPrompts(prompts, perm)
		if !jsonOutput {
			order := make([]string, 0, len(prompts))
			for _, p := range prompts {
				order = append(order, p.Source)
			}
			fmt.Fprintf(os.Stderr, "Randomized batch order (seed=%d): %v\n", seedUsed, order)
		}
	}

	// Get available panes for round-robin targeting
	panes, err := tmux.GetPanesContext(ctx, opts.Session)
	if err != nil {
		return fmt.Errorf("getting session panes: %w", err)
	}
	panes = sortPanesByTopology(panes)
	multiWindow := tmux.PanesSpanMultipleWindows(panes)

	// Apply agent type and tag filters
	agentPanes := filterPanesForBatch(panes, opts)

	if len(agentPanes) == 0 {
		return errors.New("no matching agent panes found in session (check --cc/--cod/--gmi/--tag filters)")
	}

	var batchAgentPane *tmux.Pane
	if opts.BatchAgentIndex >= 0 {
		selected, err := resolveShellSendSelectors(panes, []string{strconv.Itoa(opts.BatchAgentIndex)}, true)
		if err != nil {
			return fmt.Errorf("resolving --agent: %w", err)
		}
		batchAgentPane = &selected[0]
	}

	if opts.DryRun {
		entries := make([]SendDryRunEntry, 0, total)
		currentAgent := 0

		for _, bp := range prompts {
			var targetPanes []tmux.Pane
			if opts.BatchBroadcast {
				targetPanes = append(targetPanes, agentPanes...)
			} else if opts.BatchAgentIndex >= 0 {
				targetPanes = []tmux.Pane{*batchAgentPane}
			} else {
				targetPanes = []tmux.Pane{agentPanes[currentAgent%len(agentPanes)]}
				currentAgent++
			}
			preview, err := executeShellDispatch(ctx, opts.Session, panes, targetPanes, bp.Text, true)
			if err != nil {
				return fmt.Errorf("preflighting batch prompt %q: %w", bp.Source, err)
			}
			if !preview.Success {
				return fmt.Errorf("preflighting batch prompt %q did not produce a successful preview", bp.Source)
			}
			outputPrompt := shellPromptForOutput(bp.Text)

			for _, pane := range targetPanes {
				entries = append(entries, SendDryRunEntry{
					Pane:          tmux.PaneTargetKey(pane, multiWindow),
					PaneID:        pane.ID,
					Agent:         paneAgentLabel(pane),
					Prompt:        outputPrompt,
					PromptPreview: truncateForPreview(outputPrompt, 80),
					Source:        bp.Source,
					Priority:      bp.Priority,
				})
			}
		}

		return printSendDryRunResult(SendDryRunResult{
			Success:              true,
			DryRun:               true,
			Session:              opts.Session,
			NonInteractiveForced: opts.ForceNonInteractive,
			Total:                len(entries),
			WouldSend:            entries,
			Message:              "use without --dry-run to execute",
		})
	}

	// Show batch info
	if !jsonOutput {
		fmt.Printf("Batch contains %d prompts\n", total)
		fmt.Printf("Target agents: %d panes\n", len(agentPanes))
		if opts.PriorityOrder {
			fmt.Println("Order: priority (P0 first)")
		}
		if opts.BatchDelay > 0 {
			fmt.Printf("Delay between prompts: %v\n", opts.BatchDelay)
		}
		if opts.BatchBroadcast {
			fmt.Println("Mode: broadcast (same prompt to all agents)")
		} else if opts.BatchAgentIndex >= 0 {
			fmt.Printf("Mode: single agent (pane %s)\n", tmux.PaneTargetKey(*batchAgentPane, multiWindow))
		} else {
			fmt.Println("Mode: round-robin across agents")
		}
		fmt.Println()
	}

	// Track results
	results := make([]BatchPromptResult, 0, total)
	var delivered, failed, skipped int
	currentAgent := 0
	interrupted := false
	var batchCause error

	// Process each prompt
	for i, bp := range prompts {
		promptText := bp.Text
		// Check for interrupt
		select {
		case <-ctx.Done():
			interrupted = true
			batchCause = fmt.Errorf("batch send canceled: %w", ctx.Err())
			if !jsonOutput {
				fmt.Printf("\n\nInterrupted at prompt %d/%d\n", i+1, total)
			}
			// Skip remaining prompts
			for j := i; j < total; j++ {
				results = append(results, BatchPromptResult{
					Index:         j,
					PromptPreview: truncateForPreview(prompts[j].Text, 60),
					Priority:      prompts[j].Priority,
					Skipped:       true,
				})
				skipped++
			}
			goto summary
		default:
		}

		preview := truncateForPreview(shellPromptForOutput(promptText), 60)
		result := BatchPromptResult{
			Index:         i,
			PromptPreview: preview,
			Priority:      bp.Priority,
		}

		// Handle --confirm-each. A non-TTY caller can never answer the
		// prompt, so --confirm-each there would block on stdin forever
		// (ntm-dv50); requesting per-prompt confirmation without a terminal
		// is a contradiction, surfaced as a hard error before any send.
		if opts.BatchConfirm && !jsonOutput && !isTTY() {
			return fmt.Errorf("--confirm-each requires an interactive terminal; drop the flag or run from a TTY")
		}
		if opts.BatchConfirm && !jsonOutput {
			fmt.Printf("Prompt %d/%d: %s\n", i+1, total, preview)
			if !confirm("Send this prompt?") {
				fmt.Println("Skipped.")
				result.Skipped = true
				skipped++
				results = append(results, result)
				continue
			}
		} else if !jsonOutput {
			fmt.Printf("Sending prompt %d/%d: %s... ", i+1, total, preview)
		}

		// Determine target panes
		var targetPanes []tmux.Pane
		if opts.BatchBroadcast {
			// Send to all agent panes
			targetPanes = append(targetPanes, agentPanes...)
		} else if opts.BatchAgentIndex >= 0 {
			// Send to specific pane
			targetPanes = []tmux.Pane{*batchAgentPane}
		} else {
			// Round-robin: cycle through agents
			targetPanes = []tmux.Pane{agentPanes[currentAgent%len(agentPanes)]}
			currentAgent++
		}

		dispatchResult, sendErr := executeShellDispatch(ctx, opts.Session, panes, targetPanes, promptText, false)
		paneDelivered := dispatchResult.Delivered
		paneFailed := dispatchResult.Failed + dispatchResult.Blocked + dispatchResult.Skipped
		if sendErr != nil && paneFailed == 0 {
			paneFailed = len(targetPanes)
		}
		if sendErr == nil && paneFailed > 0 {
			sendErr = fmt.Errorf("%d pane dispatch(es) did not complete", paneFailed)
		}

		result.Targets = make([]string, 0, len(targetPanes))
		for _, pane := range targetPanes {
			result.Targets = append(result.Targets, tmux.PaneTargetKey(pane, multiWindow))
		}
		result.Delivered = paneDelivered

		if paneFailed > 0 {
			result.Success = false
			result.Error = sendErr.Error()
			if batchCause == nil {
				batchCause = sendErr
			}
			failed++
			if !jsonOutput {
				fmt.Printf("error (%d/%d delivered)\n", paneDelivered, len(targetPanes))
			}

			// Handle error: either stop on error, prompt user, or continue
			if opts.BatchStopOnErr {
				if !jsonOutput {
					fmt.Printf("\nBatch stopped on error at prompt %d/%d\n", i+1, total)
				}
				results = append(results, result)
				break
			} else if !jsonOutput {
				// Interactive error handling: ask user what to do
				action := promptBatchAction("Send failed. Continue?")
				switch action {
				case batchSkip:
					// Already counted as failed, just continue
					fmt.Println("Continuing to next prompt...")
				case batchAbort:
					fmt.Printf("\nBatch aborted at prompt %d/%d\n", i+1, total)
					results = append(results, result)
					goto summary
				default:
					// Continue - just move on
				}
			}
		} else {
			result.Success = true
			delivered++
			if !jsonOutput {
				fmt.Println("done")
			}
		}

		results = append(results, result)

		// Apply delay before next prompt (except after last)
		if opts.BatchDelay > 0 && i < total-1 {
			select {
			case <-ctx.Done():
				interrupted = true
				batchCause = fmt.Errorf("batch send canceled during delay: %w", ctx.Err())
				if !jsonOutput {
					fmt.Printf("\n\nInterrupted during delay after prompt %d/%d\n", i+1, total)
				}
				// Skip remaining prompts
				for j := i + 1; j < total; j++ {
					results = append(results, BatchPromptResult{
						Index:         j,
						PromptPreview: truncateForPreview(prompts[j].Text, 60),
						Priority:      prompts[j].Priority,
						Skipped:       true,
					})
					skipped++
				}
				goto summary
			case <-time.After(opts.BatchDelay):
			}
		}
	}

summary:
	// Output results
	if jsonOutput {
		batchResult := BatchResult{
			Success:              failed == 0 && !interrupted,
			Session:              opts.Session,
			NonInteractiveForced: opts.ForceNonInteractive,
			Randomized:           opts.Randomize,
			SeedUsed:             seedUsed,
			PriorityOrdered:      opts.PriorityOrder,
			Order: func() []string {
				if !opts.Randomize {
					return nil
				}
				out := make([]string, 0, len(prompts))
				for _, p := range prompts {
					out = append(out, p.Source)
				}
				return out
			}(),
			Total:     total,
			Delivered: delivered,
			Failed:    failed,
			Skipped:   skipped,
			Results:   results,
		}
		if interrupted {
			batchResult.Error = "interrupted by user"
		}
		return emitBatchResult(batchResult, batchCause)
	}

	// Summary
	fmt.Println()
	if interrupted {
		fmt.Printf("Batch interrupted: %d delivered, %d failed, %d skipped (of %d total)\n",
			delivered, failed, skipped, total)
	} else if failed == 0 && skipped == 0 {
		fmt.Printf("✓ Successfully sent %d/%d prompts\n", delivered, total)
	} else {
		fmt.Printf("Batch complete: %d delivered, %d failed, %d skipped (of %d total)\n",
			delivered, failed, skipped, total)
	}

	return nil
}
