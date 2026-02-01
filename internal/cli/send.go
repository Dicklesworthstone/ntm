package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/checkpoint"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/prompt"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	sessionPkg "github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/templates"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// SendResult is the JSON output for the send command.
type SendResult struct {
	Success       bool               `json:"success"`
	Session       string             `json:"session"`
	PromptPreview string             `json:"prompt_preview,omitempty"`
	Targets       []int              `json:"targets"`
	Delivered     int                `json:"delivered"`
	Failed        int                `json:"failed"`
	RoutedTo      *SendRoutingResult `json:"routed_to,omitempty"`
	Error         string             `json:"error,omitempty"`
}

type SendDryRunEntry struct {
	Pane          int    `json:"pane"`
	Agent         string `json:"agent,omitempty"`
	Prompt        string `json:"prompt"`
	PromptPreview string `json:"prompt_preview,omitempty"`
	Source        string `json:"source,omitempty"`
}

type SendDryRunResult struct {
	Success   bool               `json:"success"`
	DryRun    bool               `json:"dry_run"`
	Session   string             `json:"session"`
	Total     int                `json:"total"`
	WouldSend []SendDryRunEntry  `json:"would_send"`
	RoutedTo  *SendRoutingResult `json:"routed_to,omitempty"`
	Message   string             `json:"message,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// SendRoutingResult contains routing decision info for smart routing.
type SendRoutingResult struct {
	PaneIndex int     `json:"pane_index"`
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
		return buildInterruptResponse(opts.Session, opts.Tags)
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
		return buildKillResponse(opts.Session, opts.Force, opts.Tags, opts.NoHooks, opts.Summarize)
	})
}

// SendOptions configures the send operation
type SendOptions struct {
	Session        string
	Prompt         string
	PromptSource   string
	Targets        SendTargets
	TargetAll      bool
	SkipFirst      bool
	PaneIndex      int
	Panes          []int // Specific pane indices to target
	PanesSpecified bool  // True if --panes was explicitly set
	TemplateName   string
	Tags           []string
	DryRun         bool

	// Smart routing options
	SmartRoute    bool   // Use smart routing to select best agent
	RouteStrategy string // Routing strategy (least-loaded, round-robin, etc.)

	// CASS check options
	CassCheck      bool
	CassSimilarity float64
	CassCheckDays  int

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

// matchesSendTarget checks if a pane matches a send target
func matchesSendTarget(pane tmux.Pane, target SendTarget) bool {
	// Type must match
	if string(pane.Type) != string(target.Type) {
		return false
	}
	// If variant is specified, it must also match
	if target.Variant != "" && pane.Variant != target.Variant {
		return false
	}
	return true
}

func intsToStrings(ints []int) []string {
	out := make([]string, 0, len(ints))
	for _, v := range ints {
		out = append(out, fmt.Sprintf("%d", v))
	}
	return out
}

func newSendCmd() *cobra.Command {
	var targets SendTargets
	var targetAll, skipFirst bool
	var paneIndex int
	var panesArg string
	var promptFile, prefix, suffix string
	var contextFiles []string
	var templateName string
	var templateVars []string
	var tags []string
	var dryRun bool
	var cassCheck bool
	var noCassCheck bool
	var cassSimilarity float64
	var cassCheckDays int
	var noHooks bool
	var smartRoute bool
	var routeStrategy string
	var distribute bool
	var distributeStrategy string
	var distributeLimit int
	var distributeAuto bool

	// Batch mode variables
	var batchFile string
	var batchDelay string
	var batchConfirm bool
	var batchStopOnErr bool
	var batchBroadcast bool
	var batchAgentIndex int

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
		  ntm send myproject --pane=2 "specific pane"           # Specific pane
		  ntm send myproject --skip-first "restart"             # Skip user pane
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
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			// Handle --distribute mode: auto-distribute work from bv triage
			if distribute {
				if dryRun && distributeAuto {
					return fmt.Errorf("cannot use --dry-run with --dist-auto")
				}
				return runDistributeMode(session, distributeStrategy, distributeLimit, distributeAuto, dryRun)
			}

			// Handle --batch mode: send multiple prompts from file
			if batchFile != "" {
				var delay time.Duration
				if batchDelay != "" {
					var err error
					delay, err = time.ParseDuration(batchDelay)
					if err != nil {
						return fmt.Errorf("invalid --delay value %q: %w", batchDelay, err)
					}
				}
				batchOpts := SendOptions{
					Session:         session,
					Targets:         targets,
					TargetAll:       targetAll,
					SkipFirst:       skipFirst,
					PaneIndex:       paneIndex,
					Tags:            tags,
					SmartRoute:      smartRoute,
					RouteStrategy:   routeStrategy,
					CassCheck:       cassCheck && !noCassCheck,
					CassSimilarity:  cassSimilarity,
					CassCheckDays:   cassCheckDays,
					NoHooks:         noHooks,
					DryRun:          dryRun,
					BatchFile:       batchFile,
					BatchDelay:      delay,
					BatchConfirm:    batchConfirm,
					BatchStopOnErr:  batchStopOnErr,
					BatchBroadcast:  batchBroadcast,
					BatchAgentIndex: batchAgentIndex,
				}
				return runSendBatch(batchOpts)
			}

			var panes []int
			panesSpecified := panesArg != ""
			if panesSpecified {
				var err error
				panes, err = robot.ParsePanesArg(panesArg)
				if err != nil {
					return err
				}
			}
			if panesSpecified && paneIndex >= 0 {
				return fmt.Errorf("cannot use --pane and --panes together")
			}

			opts := SendOptions{
				Session:        session,
				Targets:        targets,
				TargetAll:      targetAll,
				SkipFirst:      skipFirst,
				PaneIndex:      paneIndex,
				Panes:          panes,
				PanesSpecified: panesSpecified,
				Tags:           tags,
				SmartRoute:     smartRoute,
				RouteStrategy:  routeStrategy,
				CassCheck:      cassCheck && !noCassCheck,
				CassSimilarity: cassSimilarity,
				CassCheckDays:  cassCheckDays,
				NoHooks:        noHooks,
				DryRun:         dryRun,
			}

			// Handle template-based prompts
			if templateName != "" {
				opts.TemplateName = templateName
				opts.PromptSource = fmt.Sprintf("template:%s", templateName)
				return runSendWithTemplate(templateVars, promptFile, contextFiles, opts)
			}

			promptText, promptSource, err := getPromptContent(args[1:], promptFile, prefix, suffix)
			if err != nil {
				return err
			}

			// Inject file context if specified
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
			opts.PromptSource = promptSource
			return runSendWithTargets(opts)
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
	cmd.Flags().BoolVar(&targetAll, "all", false, "send to all panes (including user pane)")
	cmd.Flags().BoolVarP(&skipFirst, "skip-first", "s", false, "skip the first (user) pane")
	cmd.Flags().IntVarP(&paneIndex, "pane", "p", -1, "send to specific pane index")
	cmd.Flags().StringVar(&panesArg, "panes", "", "send to specific pane indices (comma-separated). Example: --panes=1,2")
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
	cmd.Flags().Float64Var(&cassSimilarity, "cass-similarity", 0.7, "Similarity threshold for duplicate detection")
	cmd.Flags().IntVar(&cassCheckDays, "cass-check-days", 7, "Look back N days for duplicates")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview what would be sent without sending")

	// Batch mode flags - send multiple prompts from file
	cmd.Flags().StringVar(&batchFile, "batch", "", "Read prompts from file (one per line or --- separated)")
	cmd.Flags().StringVar(&batchDelay, "delay", "", "Delay between prompts (e.g., 5s, 100ms)")
	cmd.Flags().BoolVar(&batchConfirm, "confirm-each", false, "Confirm each prompt before sending")
	cmd.Flags().BoolVar(&batchStopOnErr, "stop-on-error", false, "Stop batch on first send failure")
	cmd.Flags().BoolVar(&batchBroadcast, "broadcast", false, "Send same prompt to all agents simultaneously")
	cmd.Flags().IntVar(&batchAgentIndex, "agent", -1, "Send to specific agent index only (-1 = round-robin)")

	cmd.ValidArgsFunction = completeSessionArgs
	_ = cmd.RegisterFlagCompletionFunc("pane", completePaneIndexes)
	_ = cmd.RegisterFlagCompletionFunc("panes", completePaneIndexes)

	return cmd
}

// getPromptContent resolves the prompt content from various sources:
// 1. If --file is specified, read from that file
// 2. If stdin has data (piped/redirected), read from stdin
// 3. Otherwise, use positional arguments
// The prefix and suffix are applied when reading from file or stdin.
func getPromptContent(args []string, promptFile, prefix, suffix string) (string, string, error) {
	var content string

	// Priority 1: Read from file if specified
	if promptFile != "" {
		data, err := os.ReadFile(promptFile)
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

	// Read file content if --file specified (used as {{file}} variable)
	if promptFile != "" {
		content, err := os.ReadFile(promptFile)
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
	return runSendInternal(opts)
}

func runSendInternal(opts SendOptions) error {
	session := opts.Session
	prompt := opts.Prompt
	promptSource := opts.PromptSource
	templateName := opts.TemplateName
	targets := opts.Targets
	targetAll := opts.TargetAll
	skipFirst := opts.SkipFirst
	paneIndex := opts.PaneIndex
	tags := opts.Tags
	dryRun := opts.DryRun

	// Convert to the old signature for backwards compatibility if needed locally
	targetCC := targets.HasTargetsForType(AgentTypeClaude)
	targetCod := targets.HasTargetsForType(AgentTypeCodex)
	targetGmi := targets.HasTargetsForType(AgentTypeGemini)

	// Helper for JSON error output
	var (
		histTargets []int
		histErr     error
		histSuccess bool
	)

	// Start time tracking for history
	start := time.Now()

	// Defer history logic
	defer func() {
		if dryRun {
			return
		}
		entry := history.NewEntry(session, intsToStrings(histTargets), prompt, history.SourceCLI)
		entry.Template = templateName
		entry.DurationMs = int(time.Since(start) / time.Millisecond)
		if histSuccess {
			entry.SetSuccess()
		} else {
			entry.SetError(histErr)
		}
		_ = history.Append(entry)

		// Also persist to session-specific storage for restart resilience
		promptEntry := sessionPkg.PromptEntry{
			Session:  session,
			Content:  prompt,
			Targets:  intsToStrings(histTargets),
			Source:   "cli",
			Template: templateName,
		}
		_ = sessionPkg.SavePrompt(promptEntry)
	}()

	outputError := func(err error) error {
		histErr = err
		if jsonOutput {
			result := SendResult{
				Success: false,
				Session: session,
				Error:   err.Error(),
			}
			_ = json.NewEncoder(os.Stdout).Encode(result)
			// Return error to ensure non-zero exit code
			// Since SilenceErrors is true, Cobra won't print the error message again
			return err
		}
		return err
	}

	// Smart routing: select best agent automatically.
	// Explicit pane selection (--pane/--panes) wins over automatic routing.
	if opts.SmartRoute && (opts.PanesSpecified || paneIndex >= 0) {
		if !jsonOutput {
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
		}

		// Filter by agent type if specified
		if targetCC && !targetCod && !targetGmi {
			routeOpts.AgentType = "claude"
		} else if targetCod && !targetCC && !targetGmi {
			routeOpts.AgentType = "codex"
		} else if targetGmi && !targetCC && !targetCod {
			routeOpts.AgentType = "gemini"
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
		opts.routingResult = &SendRoutingResult{
			PaneIndex: recommendation.PaneIndex,
			AgentType: recommendation.AgentType,
			Strategy:  string(strategy),
			Reason:    recommendation.Reason,
			Score:     recommendation.Score,
		}

		if !jsonOutput {
			fmt.Printf("Smart routing: selected %s (pane %d) - %s\n",
				recommendation.AgentType, recommendation.PaneIndex, recommendation.Reason)
		}
	}

	// CASS Duplicate Detection
	if opts.CassCheck {
		if err := checkCassDuplicates(session, prompt, opts.CassSimilarity, opts.CassCheckDays); err != nil {
			if err.Error() == "aborted by user" {
				fmt.Println("Aborted.")
				return nil
			}
			if strings.Contains(err.Error(), "cass not installed") || strings.Contains(err.Error(), "connection refused") {
				if !jsonOutput {
					fmt.Printf("Warning: CASS duplicate check failed: %v\n", err)
				}
			} else {
				return outputError(err)
			}
		}
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return outputError(err)
	}

	if !tmux.SessionExists(session) {
		return outputError(fmt.Errorf("session '%s' not found", session))
	}

	// Initialize hook executor
	var hookExec *hooks.Executor
	if !opts.NoHooks {
		var err error
		hookExec, err = hooks.NewExecutorFromConfig()
		if err != nil {
			// Log warning but continue - hooks are optional
			if !jsonOutput {
				fmt.Printf("⚠ Could not load hooks config: %v\n", err)
			}
			hookExec = hooks.NewExecutor(nil) // Use empty config
		}
	}

	// Build target description for hook environment
	targetDesc := buildTargetDescription(targetCC, targetCod, targetGmi, targetAll, skipFirst, paneIndex, tags)

	// Build execution context for hooks
	hookCtx := hooks.ExecutionContext{
		SessionName: session,
		ProjectDir:  getSessionWorkingDir(session),
		Message:     prompt,
		AdditionalEnv: map[string]string{
			"NTM_SEND_TARGETS": targetDesc,
			"NTM_TARGET_CC":    boolToStr(targetCC),
			"NTM_TARGET_COD":   boolToStr(targetCod),
			"NTM_TARGET_GMI":   boolToStr(targetGmi),
			"NTM_TARGET_ALL":   boolToStr(targetAll),
			"NTM_PANE_INDEX":   fmt.Sprintf("%d", paneIndex),
		},
	}

	// Run pre-send hooks
	if !dryRun && hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPreSend) {
		if !jsonOutput {
			fmt.Println("Running pre-send hooks...")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, err := hookExec.RunHooksForEvent(ctx, hooks.EventPreSend, hookCtx)
		cancel()
		if err != nil {
			return outputError(fmt.Errorf("pre-send hook failed: %w", err))
		}
		if hooks.AnyFailed(results) {
			return outputError(fmt.Errorf("pre-send hook failed: %w", hooks.AllErrors(results)))
		}
		if !jsonOutput {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d pre-send hook(s) completed\n", success)
		}
	}

	// Auto-checkpoint before broadcast sends
	isBroadcast := !opts.PanesSpecified && paneIndex < 0 && (targetAll || (!targetCC && !targetCod && !targetGmi && len(tags) == 0))
	if !dryRun && isBroadcast && cfg != nil && cfg.Checkpoints.Enabled && cfg.Checkpoints.BeforeBroadcast {
		if !jsonOutput {
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
			if !jsonOutput {
				fmt.Printf("⚠ Auto-checkpoint failed: %v\n", err)
			}
		} else if !jsonOutput {
			fmt.Printf("✓ Auto-checkpoint created: %s\n", cp.ID)
		}
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return outputError(err)
	}

	if len(panes) == 0 {
		return outputError(fmt.Errorf("no panes found in session '%s'", session))
	}

	// Determine which panes to target
	var selectedPanes []tmux.Pane
	if paneIndex >= 0 {
		for _, p := range panes {
			if p.Index == paneIndex {
				selectedPanes = append(selectedPanes, p)
				break
			}
		}
		if len(selectedPanes) == 0 {
			return outputError(fmt.Errorf("pane %d not found", paneIndex))
		}
	} else if opts.PanesSpecified {
		// --panes was specified: select only the specified pane indices
		paneSet := make(map[int]bool)
		for _, idx := range opts.Panes {
			paneSet[idx] = true
		}
		for _, p := range panes {
			if paneSet[p.Index] {
				selectedPanes = append(selectedPanes, p)
			}
		}
		// Check for missing panes
		if len(selectedPanes) != len(opts.Panes) {
			foundSet := make(map[int]bool)
			for _, p := range selectedPanes {
				foundSet[p.Index] = true
			}
			var missing []int
			for _, idx := range opts.Panes {
				if !foundSet[idx] {
					missing = append(missing, idx)
				}
			}
			if len(missing) > 0 {
				return outputError(fmt.Errorf("pane(s) not found: %v", missing))
			}
		}
	} else {
		noFilter := !targetCC && !targetCod && !targetGmi && !targetAll && len(tags) == 0
		hasVariantFilter := len(targets) > 0
		if noFilter {
			// Default: send to all agent panes (skip user panes)
			skipFirst = true
		}

		for i, p := range panes {
			// Skip first pane if requested
			if skipFirst && i == 0 {
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
				hasTypeFilter := hasVariantFilter || targetCC || targetCod || targetGmi

				if hasTypeFilter {
					if hasVariantFilter {
						if !targets.MatchesPane(p) {
							continue
						}
					} else {
						match := false
						if targetCC && p.Type == tmux.AgentClaude {
							match = true
						}
						if targetCod && p.Type == tmux.AgentCodex {
							match = true
						}
						if targetGmi && p.Type == tmux.AgentGemini {
							match = true
						}
						if !match {
							continue
						}
					}
				}
			} else if noFilter {
				// Default mode: skip non-agent panes
				if p.Type == tmux.AgentUser {
					continue
				}
			}

			selectedPanes = append(selectedPanes, p)
		}
	}

	// Track results for JSON output
	targetPanes := make([]int, 0, len(selectedPanes))
	for _, p := range selectedPanes {
		targetPanes = append(targetPanes, p.Index)
	}
	histTargets = targetPanes

	// Apply DCG safety check for non-Claude agents
	if err := maybeBlockSendWithDCG(prompt, session, selectedPanes); err != nil {
		return outputError(err)
	}

	if dryRun {
		entries := buildSendDryRunEntries(selectedPanes, prompt, promptSource)
		return printSendDryRunResult(SendDryRunResult{
			Success:   true,
			DryRun:    true,
			Session:   session,
			Total:     len(entries),
			WouldSend: entries,
			RoutedTo:  opts.routingResult,
			Message:   "use without --dry-run to execute",
		})
	}

	delivered := 0
	failed := 0

	// If specific pane requested
	if paneIndex >= 0 {
		p := selectedPanes[0]
		if err := sendPromptToPane(session, p, prompt); err != nil {
			failed++
			histErr = err
			if jsonOutput {
				result := SendResult{
					Success:       false,
					Session:       session,
					PromptPreview: truncatePrompt(prompt, 50),
					Targets:       targetPanes,
					Delivered:     delivered,
					Failed:        failed,
					RoutedTo:      opts.routingResult,
					Error:         err.Error(),
				}
				return json.NewEncoder(os.Stdout).Encode(result)
			}
			return err
		}
		delivered++
		histSuccess = true

		if jsonOutput {
			result := SendResult{
				Success:       true,
				Session:       session,
				PromptPreview: truncatePrompt(prompt, 50),
				Targets:       targetPanes,
				Delivered:     delivered,
				Failed:        failed,
				RoutedTo:      opts.routingResult,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}
		fmt.Printf("Sent to pane %d\n", paneIndex)
		return nil
	}

	if len(selectedPanes) == 0 {
		histErr = errors.New("no matching panes found")
		fmt.Println("No matching panes found")
		return nil
	}

	for _, p := range selectedPanes {
		if err := sendPromptToPane(session, p, prompt); err != nil {
			failed++
			histErr = err
			if !jsonOutput {
				return fmt.Errorf("sending to pane %d: %w", p.Index, err)
			}
		} else {
			delivered++
		}
	}

	// Update hook context with delivery results
	hookCtx.AdditionalEnv["NTM_DELIVERED_COUNT"] = fmt.Sprintf("%d", delivered)
	hookCtx.AdditionalEnv["NTM_FAILED_COUNT"] = fmt.Sprintf("%d", failed)
	hookCtx.AdditionalEnv["NTM_TARGET_PANES"] = fmt.Sprintf("%v", targetPanes)
	histTargets = targetPanes

	// Run post-send hooks
	if hookExec != nil && !dryRun && hookExec.HasHooksForEvent(hooks.EventPostSend) {
		if !jsonOutput {
			fmt.Println("Running post-send hooks...")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		results, postErr := hookExec.RunHooksForEvent(ctx, hooks.EventPostSend, hookCtx)
		cancel()
		if postErr != nil {
			// Log error but don't fail (send already succeeded)
			if !jsonOutput {
				fmt.Printf("⚠ Post-send hook error: %v\n", postErr)
			}
		} else if hooks.AnyFailed(results) {
			// Log failures but don't fail (send already succeeded)
			if !jsonOutput {
				fmt.Printf("⚠ Post-send hook failed: %v\n", hooks.AllErrors(results))
			}
		} else if !jsonOutput {
			success, _, _ := hooks.CountResults(results)
			fmt.Printf("✓ %d post-send hook(s) completed\n", success)
		}
	}

	// Emit prompt_send event
	if delivered > 0 {
		events.EmitPromptSend(session, delivered, len(prompt), "", buildTargetDescription(targetCC, targetCod, targetGmi, targetAll, skipFirst, paneIndex, tags), len(hookCtx.AdditionalEnv) > 0)
	}

	// JSON output mode
	if jsonOutput {
		result := SendResult{
			Success:       failed == 0,
			Session:       session,
			PromptPreview: truncatePrompt(prompt, 50),
			Targets:       targetPanes,
			Delivered:     delivered,
			Failed:        failed,
			RoutedTo:      opts.routingResult,
		}
		if failed > 0 {
			result.Error = fmt.Sprintf("%d pane(s) failed", failed)
			if histErr == nil {
				histErr = errors.New(result.Error)
			}
		} else {
			histSuccess = true
		}
		return json.NewEncoder(os.Stdout).Encode(result)
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

func paneAgentLabel(p tmux.Pane) string {
	if p.Type != tmux.AgentUnknown && p.Type != tmux.AgentUser && p.NTMIndex > 0 {
		return fmt.Sprintf("%s_%d", p.Type, p.NTMIndex)
	}
	if p.Type == tmux.AgentUser {
		return "user"
	}
	if p.Title != "" {
		if parts := strings.SplitN(p.Title, "__", 2); len(parts) == 2 && parts[1] != "" {
			return parts[1]
		}
		return p.Title
	}
	return fmt.Sprintf("pane_%d", p.Index)
}

func buildSendDryRunEntries(panes []tmux.Pane, prompt string, source string) []SendDryRunEntry {
	entries := make([]SendDryRunEntry, 0, len(panes))
	for _, p := range panes {
		entries = append(entries, SendDryRunEntry{
			Pane:          p.Index,
			Agent:         paneAgentLabel(p),
			Prompt:        prompt,
			PromptPreview: truncateForPreview(prompt, 80),
			Source:        source,
		})
	}
	return entries
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
		fmt.Printf("  %d. %s (pane %d): %q (%s)\n", i+1, w.Agent, w.Pane, w.PromptPreview, source)
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
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return output.PrintJSON(result)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

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
		if p.Type == tmux.AgentClaude || p.Type == tmux.AgentCodex || p.Type == tmux.AgentGemini {
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
func buildInterruptResponse(session string, tags []string) (*output.InterruptResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return nil, err
	}

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
		if p.Type == tmux.AgentClaude || p.Type == tmux.AgentCodex || p.Type == tmux.AgentGemini {
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

	cmd := &cobra.Command{
		Use:   "kill <session>",
		Short: "Kill a tmux session",
		Long: `Kill a tmux session and all its panes.

Examples:
  ntm kill myproject           # Prompts for confirmation
  ntm kill myproject --force   # No confirmation
  ntm kill myproject --tag=ui  # Kill only panes with 'ui' tag
  ntm kill myproject --summarize # Generate summary before killing`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKill(args[0], force, tags, noHooks, summarize)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter panes to kill by tag (if used, only matching panes are killed)")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Disable command hooks")
	cmd.Flags().BoolVar(&summarize, "summarize", false, "Generate session summary before killing")

	return cmd
}

func runKill(session string, force bool, tags []string, noHooks bool, summarize bool) error {
	// Use kernel for JSON output mode
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.kill", SessionKillInput{
			Session:   session,
			Force:     force,
			Tags:      tags,
			NoHooks:   noHooks,
			Summarize: summarize,
		})
		if err != nil {
			return output.PrintJSON(output.NewError(err.Error()))
		}
		return output.PrintJSON(result)
	}

	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	dir := cfg.GetProjectDir(session)

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
		summaryResult, err := generateKillSummary(session)
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
			return nil
		}

		if !force {
			if !confirm(fmt.Sprintf("Kill %d pane(s) matching tags %v?", len(toKill), tags)) {
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
		fmt.Printf("Killed %d pane(s)\n", len(toKill))
		return nil
	}

	if !force {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return err
		}

		if !confirm(fmt.Sprintf("Kill session '%s' with %d pane(s)?", session, len(panes))) {
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

	if err := tmux.KillSession(session); err != nil {
		return err
	}

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

// buildKillResponse constructs the response for session kill.
// Used by both kernel handler and direct CLI calls.
// In JSON/robot mode, force is effectively always true (no interactive confirmation).
func buildKillResponse(session string, force bool, tags []string, noHooks bool, summarize bool) (*output.KillResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return nil, err
	}

	if !tmux.SessionExists(session) {
		return nil, fmt.Errorf("session '%s' not found", session)
	}

	dir := cfg.GetProjectDir(session)

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
		summaryResult, err = generateKillSummary(session)
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
			return &output.KillResponse{
				TimestampedResponse: output.NewTimestamped(),
				Session:             session,
				Killed:              false,
				Message:             "No panes found matching tags",
			}, nil
		}

		for _, p := range toKill {
			if err := tmux.KillPane(p.ID); err != nil {
				return nil, fmt.Errorf("killing pane %s: %w", p.ID, err)
			}
		}
		addTimelineStopMarkers(session, toKill)
		message = fmt.Sprintf("Killed %d pane(s) matching tags", len(toKill))
	} else {
		panesForStop, err := tmux.GetPanes(session)
		if err == nil {
			addTimelineStopMarkers(session, panesForStop)
		}

		// Finalize timeline persistence before killing the session
		_ = state.EndSessionTimeline(session) // Ignore error - not critical

		if err := tmux.KillSession(session); err != nil {
			return nil, err
		}
		message = fmt.Sprintf("Killed session '%s'", session)
	}

	// Post-kill hooks
	if hookExec != nil && hookExec.HasHooksForEvent(hooks.EventPostKill) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_, _ = hookExec.RunHooksForEvent(ctx, hooks.EventPostKill, hookCtx)
		cancel()
		// Post-kill hook errors are logged but don't fail the response
	}

	return &output.KillResponse{
		TimestampedResponse: output.NewTimestamped(),
		Session:             session,
		Killed:              true,
		Message:             message,
		Summary:             summaryResult,
	}, nil
}

// generateKillSummary generates a session summary for use before killing.
// It captures pane outputs and runs them through the summary generator.
func generateKillSummary(session string) (*summary.SessionSummary, error) {
	// Get panes from session
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("failed to get panes: %w", err)
	}

	// Build agent outputs by capturing pane content
	var outputs []summary.AgentOutput
	for _, pane := range panes {
		agentType := string(pane.Type)
		if agentType == "" || agentType == "unknown" {
			continue // Skip non-agent panes
		}

		// Capture output (500 lines)
		out, _ := tmux.CapturePaneOutput(pane.ID, 500)

		outputs = append(outputs, summary.AgentOutput{
			AgentID:   pane.ID,
			AgentType: agentType,
			Output:    out,
		})
	}

	if len(outputs) == 0 {
		return nil, fmt.Errorf("no agent outputs to summarize")
	}

	wd, _ := os.Getwd()
	projectDir := cfg.GetProjectDir(session)
	if projectDir == "" {
		projectDir = wd
	}

	opts := summary.Options{
		Session:        session,
		Outputs:        outputs,
		Format:         summary.FormatBrief,
		ProjectKey:     wd,
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
func buildTargetDescription(targetCC, targetCod, targetGmi, targetAll, skipFirst bool, paneIndex int, tags []string) string {
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

// getSessionWorkingDir returns the working directory for a session
func getSessionWorkingDir(session string) string {
	if cfg != nil {
		return cfg.GetProjectDir(session)
	}
	return ""
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
	command, ok := extractLikelyCommand(prompt)
	if !ok {
		return nil
	}

	adapter := tools.NewDCGAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !adapter.IsAvailable(ctx) {
		return nil
	}

	blocked, err := adapter.CheckCommand(ctx, command)
	if err != nil {
		return err
	}
	if blocked == nil {
		return nil
	}

	logDCGBlocked(command, session, panes, blocked)
	reason := strings.TrimSpace(blocked.Reason)
	if reason == "" {
		reason = "blocked by dcg"
	}
	return fmt.Errorf("blocked by dcg: %s", reason)
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
	if p.Type == tmux.AgentUser {
		return false
	}
	return p.Type != tmux.AgentClaude
}

func extractLikelyCommand(prompt string) (string, bool) {
	for _, line := range strings.Split(prompt, "\n") {
		candidate := normalizeCommandLine(line)
		if candidate == "" {
			continue
		}
		if looksLikeShellCommand(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func normalizeCommandLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "$ ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "$ "))
	}
	if strings.HasPrefix(trimmed, "> ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "> "))
	}
	if strings.HasPrefix(trimmed, "# ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
	}
	return strings.TrimSpace(trimmed)
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

const (
	agentPromptFirstEnterDelay  = 1 * time.Second
	agentPromptSecondEnterDelay = 500 * time.Millisecond
)

func sendPromptToPane(session string, p tmux.Pane, prompt string) error {
	if p.Type == tmux.AgentUser {
		if err := tmux.PasteKeys(p.ID, prompt, true); err != nil {
			return err
		}
		return nil
	}
	if err := sendPromptWithDoubleEnterForAgent(p.ID, prompt, p.Type); err != nil {
		return err
	}
	addTimelinePromptMarker(session, p, prompt)
	return nil
}

func sendPromptWithDoubleEnter(paneID, prompt string) error {
	// Default to AgentUnknown for backward compatibility
	return sendPromptWithDoubleEnterForAgent(paneID, prompt, tmux.AgentUnknown)
}

func sendPromptWithDoubleEnterForAgent(paneID, prompt string, agentType tmux.AgentType) error {
	// Use agent-aware send method which handles Gemini's multi-line quirks
	// by using buffer-based paste instead of send-keys when content has newlines
	if err := tmux.SendKeysForAgent(paneID, prompt, false, agentType); err != nil {
		return err
	}
	time.Sleep(agentPromptFirstEnterDelay)
	if err := tmux.SendKeys(paneID, "", true); err != nil {
		return err
	}
	time.Sleep(agentPromptSecondEnterDelay)
	if err := tmux.SendKeys(paneID, "", true); err != nil {
		return err
	}
	return nil
}

func addTimelinePromptMarker(session string, p tmux.Pane, prompt string) {
	if session == "" {
		return
	}
	if p.Type == tmux.AgentUser || p.Type == tmux.AgentUnknown {
		return
	}
	agentID := timelineAgentIDFromPane(p)
	if agentID == "" {
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
		if p.Type == tmux.AgentUser || p.Type == tmux.AgentUnknown {
			continue
		}
		agentID := timelineAgentIDFromPane(p)
		if agentID == "" {
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
	if p.NTMIndex > 0 && p.Type != tmux.AgentUnknown && p.Type != tmux.AgentUser {
		return fmt.Sprintf("%s_%d", p.Type, p.NTMIndex)
	}
	if p.Title != "" {
		if parts := strings.SplitN(p.Title, "__", 2); len(parts) == 2 && parts[1] != "" {
			return parts[1]
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

func checkCassDuplicates(session, prompt string, threshold float64, days int) error {
	var opts []cass.ClientOption
	if cfg != nil && cfg.CASS.BinaryPath != "" {
		opts = append(opts, cass.WithBinaryPath(cfg.CASS.BinaryPath))
	}
	client := cass.NewClient(opts...)
	if !client.IsInstalled() {
		return fmt.Errorf("cass not installed")
	}

	// Get workspace from session
	dir := cfg.GetProjectDir(session)

	since := fmt.Sprintf("%dd", days)

	res, err := client.CheckDuplicates(context.Background(), cass.DuplicateCheckOptions{
		Query:     prompt,
		Workspace: dir,
		Since:     since,
		Threshold: threshold,
	})
	if err != nil {
		return err
	}

	if res.DuplicatesFound {
		if jsonOutput {
			return fmt.Errorf("duplicates found in CASS: %d similar sessions", len(res.SimilarSessions))
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
func runDistributeMode(session, strategy string, limit int, autoExecute bool, dryRun bool) error {
	th := theme.Current()

	// Check if bv is installed
	if !bv.IsInstalled() {
		return fmt.Errorf("bv (beads graph triage) is not installed; cannot use --distribute")
	}

	// Verify session exists
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}
	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Get assignment recommendations using robot module
	opts := robot.AssignOptions{
		Session:  session,
		Strategy: strategy,
	}

	recs, err := robot.GetAssignRecommendations(opts)
	if err != nil {
		return fmt.Errorf("getting assignment recommendations: %w", err)
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
				agentStyle.Render(fmt.Sprintf("Pane %d (%s)", rec.PaneIndex, rec.AgentType)))
			if rec.Reason != "" {
				fmt.Printf("     Reason: %s\n", rec.Reason)
			}
		}
		fmt.Println()
	}

	// JSON output mode - just return the plan
	if jsonOutput {
		result := map[string]interface{}{
			"success":         true,
			"session":         session,
			"strategy":        strategy,
			"recommendations": recs,
			"count":           len(recs),
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

	// Execute distribution - send each task to its assigned agent
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}
	paneIDByIndex := make(map[int]string, len(panes))
	for _, p := range panes {
		paneIDByIndex[p.Index] = p.ID
	}

	var delivered, failed int
	for _, rec := range recs {
		// Build the prompt for this task
		taskPrompt := fmt.Sprintf("Please work on this task:\n\n**[%s] %s**\n\nClaim it with: br update %s --status in_progress",
			rec.BeadID, rec.Title, rec.BeadID)

		// Send to the specific pane
		paneID, ok := paneIDByIndex[rec.PaneIndex]
		if !ok {
			if !jsonOutput {
				fmt.Printf("  ✗ Failed to send to pane %d: pane not found\n", rec.PaneIndex)
			}
			failed++
			continue
		}
		if err := sendPromptWithDoubleEnter(paneID, taskPrompt); err != nil {
			if !jsonOutput {
				fmt.Printf("  ✗ Failed to send to pane %d: %v\n", rec.PaneIndex, err)
			}
			failed++
			continue
		}

		if !jsonOutput {
			fmt.Printf("  ✓ Sent [%s] to pane %d (%s)\n", rec.BeadID, rec.PaneIndex, rec.AgentType)
		}
		delivered++
	}

	// Summary
	if jsonOutput {
		result := map[string]interface{}{
			"success":   failed == 0,
			"session":   session,
			"delivered": delivered,
			"failed":    failed,
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	fmt.Println()
	if failed == 0 {
		fmt.Printf("✓ Successfully distributed %d tasks\n", delivered)
	} else {
		fmt.Printf("Distributed %d tasks (%d failed)\n", delivered, failed)
	}

	return nil
}

// BatchResult represents the JSON output for batch send operations
type BatchResult struct {
	Success   bool                `json:"success"`
	Session   string              `json:"session"`
	Total     int                 `json:"batch_total"`
	Delivered int                 `json:"batch_delivered"`
	Failed    int                 `json:"batch_failed"`
	Skipped   int                 `json:"batch_skipped"`
	Results   []BatchPromptResult `json:"results"`
	Error     string              `json:"error,omitempty"`
}

// BatchPromptResult represents the result of sending a single prompt in a batch
type BatchPromptResult struct {
	Index         int    `json:"index"`
	PromptPreview string `json:"prompt_preview"`
	Success       bool   `json:"success"`
	Targets       []int  `json:"targets,omitempty"`
	Delivered     int    `json:"delivered"`
	Error         string `json:"error,omitempty"`
	Skipped       bool   `json:"skipped,omitempty"`
}

type BatchPrompt struct {
	Text   string
	Source string
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
			cleaned := removeComments(strings.Join(blockLines, "\n"))
			if cleaned == "" || blockStartLine == 0 {
				return
			}
			prompts = append(prompts, BatchPrompt{
				Text:   cleaned,
				Source: fmt.Sprintf("line:%d", blockStartLine),
			})
		}

		for i, line := range lines {
			lineNo := i + 1
			trimmed := strings.TrimSpace(line)
			if trimmed == "---" {
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
		// Simple one-prompt-per-line format
		for i, line := range lines {
			lineNo := i + 1
			trimmed := strings.TrimSpace(line)
			// Skip empty lines and comments
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			prompts = append(prompts, BatchPrompt{
				Text:   trimmed,
				Source: fmt.Sprintf("line:%d", lineNo),
			})
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

	for _, p := range panes {
		// If --all, include everything
		if opts.TargetAll {
			filtered = append(filtered, p)
			continue
		}

		// If no filters specified, include all non-user panes
		if noFilter {
			if p.Type != tmux.AgentUser {
				filtered = append(filtered, p)
			}
			continue
		}

		// Skip user panes unless --all was specified
		if p.Type == tmux.AgentUser {
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
	// Parse the batch file
	prompts, err := parseBatchFile(opts.BatchFile)
	if err != nil {
		return err
	}

	jsonOutput := IsJSONOutput()
	total := len(prompts)

	// Get available panes for round-robin targeting
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return fmt.Errorf("getting session panes: %w", err)
	}

	// Apply agent type and tag filters
	agentPanes := filterPanesForBatch(panes, opts)

	if len(agentPanes) == 0 {
		return errors.New("no matching agent panes found in session (check --cc/--cod/--gmi/--tag filters)")
	}

	paneByIndex := make(map[int]tmux.Pane, len(panes))
	for _, p := range panes {
		paneByIndex[p.Index] = p
	}

	if opts.DryRun {
		entries := make([]SendDryRunEntry, 0, total)
		currentAgent := 0

		for _, bp := range prompts {
			var targetPanes []int
			if opts.BatchBroadcast {
				for _, p := range agentPanes {
					targetPanes = append(targetPanes, p.Index)
				}
			} else if opts.BatchAgentIndex >= 0 {
				targetPanes = []int{opts.BatchAgentIndex}
			} else {
				targetPanes = []int{agentPanes[currentAgent%len(agentPanes)].Index}
				currentAgent++
			}

			for _, paneIdx := range targetPanes {
				p, ok := paneByIndex[paneIdx]
				if !ok {
					continue
				}
				entries = append(entries, SendDryRunEntry{
					Pane:          paneIdx,
					Agent:         paneAgentLabel(p),
					Prompt:        bp.Text,
					PromptPreview: truncateForPreview(bp.Text, 80),
					Source:        bp.Source,
				})
			}
		}

		return printSendDryRunResult(SendDryRunResult{
			Success:   true,
			DryRun:    true,
			Session:   opts.Session,
			Total:     len(entries),
			WouldSend: entries,
			Message:   "use without --dry-run to execute",
		})
	}

	// Set up signal handling for graceful Ctrl+C
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()
	defer signal.Stop(sigCh)

	// Show batch info
	if !jsonOutput {
		fmt.Printf("Batch contains %d prompts\n", total)
		fmt.Printf("Target agents: %d panes\n", len(agentPanes))
		if opts.BatchDelay > 0 {
			fmt.Printf("Delay between prompts: %v\n", opts.BatchDelay)
		}
		if opts.BatchBroadcast {
			fmt.Println("Mode: broadcast (same prompt to all agents)")
		} else if opts.BatchAgentIndex >= 0 {
			fmt.Printf("Mode: single agent (pane %d)\n", opts.BatchAgentIndex)
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

	// Process each prompt
	for i, bp := range prompts {
		promptText := bp.Text
		// Check for interrupt
		select {
		case <-ctx.Done():
			interrupted = true
			if !jsonOutput {
				fmt.Printf("\n\nInterrupted at prompt %d/%d\n", i+1, total)
			}
			// Skip remaining prompts
			for j := i; j < total; j++ {
				results = append(results, BatchPromptResult{
					Index:         j,
					PromptPreview: truncateForPreview(prompts[j].Text, 60),
					Skipped:       true,
				})
				skipped++
			}
			goto summary
		default:
		}

		preview := truncateForPreview(promptText, 60)
		result := BatchPromptResult{
			Index:         i,
			PromptPreview: preview,
		}

		// Handle --confirm-each
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
		var targetPanes []int
		if opts.BatchBroadcast {
			// Send to all agent panes
			for _, p := range agentPanes {
				targetPanes = append(targetPanes, p.Index)
			}
		} else if opts.BatchAgentIndex >= 0 {
			// Send to specific pane
			targetPanes = []int{opts.BatchAgentIndex}
		} else {
			// Round-robin: cycle through agents
			targetPanes = []int{agentPanes[currentAgent%len(agentPanes)].Index}
			currentAgent++
		}

		// Send to each target pane
		var paneDelivered, paneFailed int
		var sendErr error
		for _, paneIdx := range targetPanes {
			p, ok := paneByIndex[paneIdx]
			if !ok {
				paneFailed++
				sendErr = fmt.Errorf("pane %d not found", paneIdx)
				continue
			}
			if err := sendPromptToPane(opts.Session, p, promptText); err != nil {
				paneFailed++
				sendErr = err
			} else {
				paneDelivered++
			}
		}

		result.Targets = targetPanes
		result.Delivered = paneDelivered

		if paneFailed > 0 {
			result.Success = false
			result.Error = sendErr.Error()
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
				if !jsonOutput {
					fmt.Printf("\n\nInterrupted during delay after prompt %d/%d\n", i+1, total)
				}
				// Skip remaining prompts
				for j := i + 1; j < total; j++ {
					results = append(results, BatchPromptResult{
						Index:         j,
						PromptPreview: truncateForPreview(prompts[j].Text, 60),
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
			Success:   failed == 0 && !interrupted,
			Session:   opts.Session,
			Total:     total,
			Delivered: delivered,
			Failed:    failed,
			Skipped:   skipped,
			Results:   results,
		}
		if interrupted {
			batchResult.Error = "interrupted by user"
		}
		return json.NewEncoder(os.Stdout).Encode(batchResult)
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
