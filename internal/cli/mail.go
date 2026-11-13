package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newMailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Agent Mail messaging, session bootstrap, and thread coordination",
		Long: `Send Human Overseer messages and coordinate agents via Agent Mail.

The send subcommand provides the Human Overseer interface. Messages sent with
mail send:
  - Come from the special "HumanOverseer" identity
  - Bypass contact policies (humans can always reach agents)
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Are marked as high importance

This is the CLI equivalent of the Agent Mail web UI at /mail/{project}/overseer/compose

Examples:
  ntm mail send myproject --to GreenCastle "Please review the API changes"
  ntm mail send myproject --all "Checkpoint: sync and report status"
  ntm mail session-start myproject --program codex --model gpt-5 --json
  ntm mail prepare-thread myproject --agent GreenCastle --thread FEAT-123
  ntm mail handshake myproject --agent GreenCastle --to BlueLake`,
	}

	cmd.AddCommand(newMailSendCmd())
	cmd.AddCommand(newMailInboxCmdReal())
	cmd.AddCommand(newMailReadCmd())
	cmd.AddCommand(newMailAckCmd())
	cmd.AddCommand(newMailMacroCmd("session-start"))
	cmd.AddCommand(newMailMacroCmd("prepare-thread"))
	cmd.AddCommand(newMailMacroCmd("handshake"))

	return cmd
}

type mailMacroOptions struct {
	Program           string
	Model             string
	Task              string
	Agent             string
	Thread            string
	Limit             int
	IncludeExamples   bool
	IncludeBodies     bool
	LLM               bool
	LLMModel          string
	RegisterIfMissing bool
	To                string
	ToProject         string
	Reason            string
	AutoAccept        bool
	WelcomeSubject    string
	WelcomeBody       string
	TTLSeconds        int
}

type mailMacroOutput struct {
	Success       bool              `json:"success"`
	Timestamp     string            `json:"timestamp"`
	Action        string            `json:"action"`
	Session       string            `json:"session,omitempty"`
	ProjectKey    string            `json:"project_key,omitempty"`
	Agent         string            `json:"agent,omitempty"`
	IdentitySaved bool              `json:"identity_saved,omitempty"`
	Redaction     *RedactionSummary `json:"redaction,omitempty"`
	Result        interface{}       `json:"result,omitempty"`
	Error         string            `json:"error,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
}

// These explicit macros have their own identity lifecycle. They must not be
// substituted into spawn's per-pane bootstrap, which already owns pane bindings.
func newMailMacroCmd(action string) *cobra.Command {
	opts := mailMacroOptions{}
	cmd := &cobra.Command{
		Use: action + " <session>",
		RunE: func(cmd *cobra.Command, args []string) error {
			defer resetMailMacroFlags(cmd)
			result := mailMacroOutput{Timestamp: time.Now().UTC().Format(time.RFC3339), Action: action}
			if len(args) != 1 {
				return renderMailMacroFailure(cmd, result, "INVALID_ARGUMENT", fmt.Errorf("%s requires exactly one session", cmd.CommandPath()))
			}
			result.Session = strings.TrimSpace(args[0])
			if err := validateMailMacroOptions(action, result.Session, opts); err != nil {
				return renderMailMacroFailure(cmd, result, "INVALID_FLAG", err)
			}
			return runMailMacro(cmd, result, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Program, "program", "ntm", "Program registering or continuing the agent identity")
	cmd.Flags().StringVar(&opts.Model, "model", "coordinator", "Model name recorded for the agent identity")
	cmd.Flags().StringVar(&opts.Task, "task", "", "Current task description")

	switch action {
	case "session-start":
		cmd.Short = "Bootstrap an Agent Mail project and new agent in one call"
		cmd.Long = `Ensure the session's project, register a new Agent Mail identity, and fetch
its inbox through macro_start_session. The returned identity and registration
credential are saved locally for later mail commands; credentials are never
printed. Existing tmux pane bindings and the session's existing default identity
are preserved. Use the returned agent name with --agent to select this identity.`
	case "prepare-thread":
		cmd.Short = "Load an Agent Mail thread summary and inbox for an agent"
		cmd.Long = `Prepare an existing thread through macro_prepare_thread. Reuses --agent,
the session identity, or AGENT_NAME. Creating an identity requires the explicit
--register-if-missing flag. LLM summarization is opt-in with --llm; the default
uses a deterministic summary. Returned credentials are saved locally.`
		cmd.Flags().StringVar(&opts.Agent, "agent", "", "Agent identity (defaults to the session identity or AGENT_NAME)")
		cmd.Flags().StringVar(&opts.Thread, "thread", "", "Thread ID to continue (required)")
		cmd.Flags().IntVar(&opts.Limit, "limit", 10, "Maximum inbox messages (1-1000)")
		cmd.Flags().BoolVar(&opts.IncludeExamples, "examples", true, "Include example messages from the thread")
		cmd.Flags().BoolVar(&opts.IncludeBodies, "include-bodies", false, "Include full inbox message bodies")
		cmd.Flags().BoolVar(&opts.LLM, "llm", false, "Use the server's LLM summarizer")
		cmd.Flags().StringVar(&opts.LLMModel, "llm-model", "", "Summarizer model override (requires --llm)")
		cmd.Flags().BoolVar(&opts.RegisterIfMissing, "register-if-missing", false, "Allow the macro to register an identity if missing")
	case "handshake":
		cmd.Short = "Request contact with another Agent Mail agent"
		cmd.Long = `Request contact through macro_contact_handshake using an existing agent
identity. --to names the target agent; --to-project selects another project.
Automatic approval and a welcome message happen only when explicitly requested
with --auto-accept and the --welcome-subject/--welcome-body pair. The server
enforces contact authorization; a rejected or unavailable macro is an error.
Create and save an identity with session-start or prepare-thread before using
handshake, whose server response does not include registration credentials.`
		cmd.Flags().StringVar(&opts.Agent, "agent", "", "Requesting identity (defaults to the session identity or AGENT_NAME)")
		cmd.Flags().StringVar(&opts.To, "to", "", "Target agent name (required)")
		cmd.Flags().StringVar(&opts.ToProject, "to-project", "", "Target project's absolute path (defaults to the current project)")
		cmd.Flags().StringVar(&opts.Reason, "reason", "", "Reason for requesting contact")
		cmd.Flags().BoolVar(&opts.AutoAccept, "auto-accept", false, "Request approval on the target's behalf (requires server authorization)")
		cmd.Flags().StringVar(&opts.WelcomeSubject, "welcome-subject", "", "Optional welcome message subject (requires --welcome-body)")
		cmd.Flags().StringVar(&opts.WelcomeBody, "welcome-body", "", "Optional welcome message body (requires --welcome-subject)")
		cmd.Flags().IntVar(&opts.TTLSeconds, "ttl", 3600, "Contact approval lifetime in seconds (must be positive)")
	}
	return cmd
}

func resetMailMacroFlags(cmd *cobra.Command) {
	// Cobra commands can be reused. In particular, a prior auto-accept or welcome
	// message must never leak into a later invocation that omitted those flags.
	for _, name := range []string{"program", "model", "task", "agent", "thread", "limit", "examples", "include-bodies", "llm", "llm-model", "register-if-missing", "to", "to-project", "reason", "auto-accept", "welcome-subject", "welcome-body", "ttl"} {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			_ = flag.Value.Set(flag.DefValue)
			flag.Changed = false
		}
	}
}

func validateMailMacroOptions(action, session string, opts mailMacroOptions) error {
	if err := tmux.ValidateSessionName(session); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"program", opts.Program}, {"model", opts.Model},
	} {
		if strings.TrimSpace(field.value) == "" || !utf8.ValidString(field.value) || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("--%s must be non-empty UTF-8 text without control characters", field.name)
		}
	}
	for _, field := range []struct{ name, value string }{
		{"agent", opts.Agent}, {"thread", opts.Thread}, {"to", opts.To}, {"to-project", opts.ToProject},
		{"llm-model", opts.LLMModel}, {"reason", opts.Reason}, {"welcome-subject", opts.WelcomeSubject},
	} {
		if !utf8.ValidString(field.value) || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("--%s must be UTF-8 text without control characters", field.name)
		}
	}
	if !utf8.ValidString(opts.Task) || !utf8.ValidString(opts.WelcomeBody) {
		return errors.New("--task and --welcome-body must be valid UTF-8")
	}
	if strings.EqualFold(strings.TrimSpace(opts.Agent), agentmail.HumanOverseerAgentName) {
		return errors.New("HumanOverseer is server-managed; use ntm mail send for human interventions")
	}
	if opts.Agent != "" && strings.TrimSpace(opts.Agent) == "" {
		return errors.New("--agent cannot be blank")
	}
	switch action {
	case "prepare-thread":
		if strings.TrimSpace(opts.Thread) == "" {
			return errors.New("--thread is required")
		}
		if opts.Limit < 1 || opts.Limit > 1000 {
			return errors.New("--limit must be between 1 and 1000")
		}
		if opts.LLMModel != "" && !opts.LLM {
			return errors.New("--llm-model requires --llm")
		}
	case "handshake":
		if strings.TrimSpace(opts.To) == "" {
			return errors.New("--to is required")
		}
		if opts.ToProject != "" && !filepath.IsAbs(opts.ToProject) {
			return errors.New("--to-project must be an absolute project path")
		}
		if opts.TTLSeconds <= 0 {
			return errors.New("--ttl must be positive")
		}
		if (strings.TrimSpace(opts.WelcomeSubject) == "") != (strings.TrimSpace(opts.WelcomeBody) == "") {
			return errors.New("--welcome-subject and --welcome-body must be provided together")
		}
	}
	return nil
}

func runMailMacro(cmd *cobra.Command, out mailMacroOutput, opts mailMacroOptions) error {
	parent, err := requireMailCommandContext(cmd, "mail "+out.Action)
	if err != nil {
		return renderMailMacroFailure(cmd, out, "INVALID_REQUEST", err)
	}
	// Scan all free-form content before invoking a macro: contact creation and
	// welcome delivery are one request, so a blocked message cannot be checked
	// after the contact mutation has already happened.
	redactCfg := redaction.DefaultConfig()
	if cfg != nil {
		redactCfg = cfg.Redaction.ToRedactionLibConfig()
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"task", &opts.Task}, {"reason", &opts.Reason},
		{"welcome-subject", &opts.WelcomeSubject}, {"welcome-body", &opts.WelcomeBody},
	} {
		value, summary, redactErr := applyOutputRedaction(*field.value, redactCfg)
		if summary != nil {
			if out.Redaction == nil {
				out.Redaction = &RedactionSummary{Mode: summary.Mode, Action: summary.Action, Categories: make(map[string]int)}
			}
			out.Redaction.Findings += summary.Findings
			for category, count := range summary.Categories {
				out.Redaction.Categories[category] += count
			}
		}
		if redactErr != nil {
			return renderMailMacroFailure(cmd, out, "SENSITIVE_DATA_BLOCKED", fmt.Errorf("--%s: %w", field.name, redactErr))
		}
		*field.value = value
	}
	if out.Redaction != nil && !IsJSONOutput() {
		fmt.Fprintf(cmd.ErrOrStderr(), "Agent Mail redaction: %d potential secret(s), action=%s (mode=%s)\n", out.Redaction.Findings, out.Redaction.Action, out.Redaction.Mode)
	}
	session, projectKey, err := resolveAgentMailCommandScope(parent, out.Session)
	if err != nil {
		return renderMailMacroFailure(cmd, out, "SCOPE_FAILED", err)
	}
	out.Session, out.ProjectKey = session, projectKey
	client := newAgentMailClient(projectKey)
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	if out.Action != "session-start" {
		opts.Agent = strings.TrimSpace(opts.Agent)
		if opts.Agent == "" {
			opts.Agent, err = resolveMailAgentIdentity(ctx, session, "")
			if err != nil && !(out.Action == "prepare-thread" && opts.RegisterIfMissing && ctx.Err() == nil) {
				return renderMailMacroFailure(cmd, out, "IDENTITY_REQUIRED", err)
			}
		}
		if strings.EqualFold(opts.Agent, agentmail.HumanOverseerAgentName) {
			return renderMailMacroFailure(cmd, out, "INVALID_FLAG", errors.New("HumanOverseer is server-managed; use ntm mail send for human interventions"))
		}
		if !utf8.ValidString(opts.Agent) || strings.IndexFunc(opts.Agent, unicode.IsControl) >= 0 {
			return renderMailMacroFailure(cmd, out, "INVALID_FLAG", errors.New("agent identity must be UTF-8 text without control characters"))
		}
	}

	var agent *agentmail.Agent
	switch out.Action {
	case "session-start":
		var result *agentmail.SessionStartResult
		result, err = client.StartSession(ctx, projectKey, strings.TrimSpace(opts.Program), strings.TrimSpace(opts.Model), opts.Task)
		if result != nil {
			agent = result.Agent
			if result.Inbox == nil {
				result.Inbox = make([]agentmail.InboxMessage, 0)
			}
			if result.FileReservations != nil {
				if result.FileReservations.Granted == nil {
					result.FileReservations.Granted = make([]agentmail.FileReservation, 0)
				}
				if result.FileReservations.Conflicts == nil {
					result.FileReservations.Conflicts = make([]agentmail.ReservationConflict, 0)
				}
			}
			out.Result = result
			if err == nil && (result.Project == nil || strings.TrimSpace(result.Project.HumanKey) == "" || !agentmail.ProjectKeysEquivalent(result.Project.HumanKey, projectKey)) {
				err = errors.New("Agent Mail returned a missing or mismatched project for session bootstrap")
			}
		}
	case "prepare-thread":
		var result *agentmail.PrepareThreadResult
		result, err = client.PrepareThread(ctx, agentmail.PrepareThreadOptions{
			ProjectKey: projectKey, ThreadID: strings.TrimSpace(opts.Thread),
			Program: strings.TrimSpace(opts.Program), Model: strings.TrimSpace(opts.Model),
			AgentName: opts.Agent, TaskDescription: opts.Task, InboxLimit: opts.Limit,
			IncludeExamples: &opts.IncludeExamples, IncludeInboxBodies: &opts.IncludeBodies,
			LLMMode: &opts.LLM, LLMModel: opts.LLMModel, RegisterIfMissing: &opts.RegisterIfMissing,
		})
		if result != nil {
			agent = result.Agent
			if result.Inbox == nil {
				result.Inbox = make([]agentmail.InboxMessage, 0)
			}
			out.Result = result
			if err == nil && (result.Thread == nil || result.Thread.ThreadID != strings.TrimSpace(opts.Thread)) {
				err = errors.New("Agent Mail returned a missing or mismatched thread summary")
			}
			if err == nil && (result.Project == nil || strings.TrimSpace(result.Project.HumanKey) == "" || !agentmail.ProjectKeysEquivalent(result.Project.HumanKey, projectKey)) {
				err = errors.New("Agent Mail returned a missing or mismatched project for thread preparation")
			}
			if result.Thread != nil {
				if result.Thread.Summary.Participants == nil {
					result.Thread.Summary.Participants = make([]string, 0)
				}
				if result.Thread.Summary.KeyPoints == nil {
					result.Thread.Summary.KeyPoints = make([]string, 0)
				}
				if result.Thread.Summary.ActionItems == nil {
					result.Thread.Summary.ActionItems = make([]string, 0)
				}
			}
		}
	case "handshake":
		var result *agentmail.ContactHandshakeResult
		result, err = client.ContactHandshake(ctx, agentmail.ContactHandshakeOptions{
			ProjectKey: projectKey, AgentName: opts.Agent, ToAgent: strings.TrimSpace(opts.To),
			ToProject: opts.ToProject, Reason: opts.Reason,
			Program: strings.TrimSpace(opts.Program), Model: strings.TrimSpace(opts.Model),
			TaskDescription: opts.Task, RegisterIfMissing: false, AutoAccept: opts.AutoAccept,
			WelcomeSubject: opts.WelcomeSubject, WelcomeBody: opts.WelcomeBody, TTLSeconds: opts.TTLSeconds,
		})
		if result != nil {
			out.Agent = opts.Agent
			out.Result = result
			if err == nil {
				if result.Request == nil || result.Request.FromAgent != opts.Agent || result.Request.ToAgent != strings.TrimSpace(opts.To) {
					err = errors.New("Agent Mail returned a missing or mismatched contact request")
				} else {
					switch result.Request.Status {
					case "approved", "pending":
					case "denied", "blocked":
						err = fmt.Errorf("%w: contact status %s", agentmail.ErrContactBlocked, result.Request.Status)
					default:
						err = errors.New("Agent Mail returned no recognized contact status")
					}
				}
				if err == nil && opts.AutoAccept && (result.Response == nil || !result.Response.Approved || result.Response.From != opts.Agent || result.Response.To != strings.TrimSpace(opts.To)) {
					err = errors.New("contact request recorded but requested approval was not confirmed for these agents")
				}
				if err == nil && opts.WelcomeBody != "" && (result.WelcomeMsg == nil || result.WelcomeMsg.Count == 0) {
					err = fmt.Errorf("contact request recorded but welcome message was not sent: %s", result.WelcomeSkippedReason)
				}
			}
		}
	}

	// Credentials are retained only for the private registry, never the output
	// envelope (including a partial result when validation or persistence fails).
	var token string
	if agent != nil {
		token, agent.RegistrationToken = agent.RegistrationToken, ""
		out.Agent = agent.Name
	}
	if err != nil {
		return renderMailMacroFailure(cmd, out, mailMacroErrorCode(err), err)
	}
	if out.Action != "handshake" {
		if agent == nil || strings.TrimSpace(agent.Name) == "" || !utf8.ValidString(agent.Name) || strings.IndexFunc(agent.Name, unicode.IsControl) >= 0 {
			return renderMailMacroFailure(cmd, out, "INVALID_RESPONSE", errors.New("Agent Mail returned no valid agent identity"))
		}
		if strings.EqualFold(strings.TrimSpace(agent.Name), agentmail.HumanOverseerAgentName) {
			return renderMailMacroFailure(cmd, out, "INVALID_RESPONSE", errors.New("Agent Mail returned the reserved HumanOverseer identity"))
		}
		if opts.Agent != "" && agent.Name != opts.Agent {
			return renderMailMacroFailure(cmd, out, "INVALID_RESPONSE", errors.New("Agent Mail returned a different agent identity than requested"))
		}
		if err := persistMailMacroIdentity(session, projectKey, client, agent.Name, token); err != nil {
			return renderMailMacroFailure(cmd, out, "IDENTITY_SAVE_FAILED", fmt.Errorf("macro completed for %s but saving its identity failed: %w", agent.Name, err))
		}
		out.IdentitySaved = true
	} else if out.Result == nil {
		return renderMailMacroFailure(cmd, out, "INVALID_RESPONSE", errors.New("Agent Mail returned no contact request result"))
	}
	out.Success = true
	if IsJSONOutput() {
		return encodeJSONResult(mailJSONWriter(cmd), out)
	}
	w := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(w, "Agent Mail %s completed for %s\nProject: %s\n", out.Action, sanitizeMailDisplayField(out.Agent), sanitizeMailDisplayField(projectKey)); err != nil {
		return err
	}
	// The structured result includes the thread summary and inbox, so text mode
	// does not discard the very context these macros retrieve.
	return encodeJSONResult(w, out.Result)
}

func persistMailMacroIdentity(session, projectKey string, client *agentmail.Client, name, token string) error {
	if token == "" {
		token = client.RegistrationToken(projectKey, name)
	}
	registry, err := agentmail.LoadSessionAgentRegistry(session, projectKey)
	if err != nil {
		return err
	}
	if registry == nil {
		registry = agentmail.NewSessionAgentRegistry(session, projectKey)
	}
	if token != "" {
		registry.SetRegistrationToken(name, token)
		if err := agentmail.SaveSessionAgentRegistry(registry); err != nil {
			return err
		}
	}

	info, err := agentmail.LoadSessionAgent(session, projectKey)
	if err != nil {
		return err
	}
	if info != nil && info.AgentName != "" && info.AgentName != name {
		return nil // A macro must not steal an existing session coordinator's identity.
	}
	now := time.Now().UTC()
	if info == nil {
		info = &agentmail.SessionAgentInfo{AgentName: name, ProjectKey: projectKey, RegisteredAt: now}
	}
	info.AgentName, info.ProjectKey, info.LastActiveAt = name, projectKey, now
	if token != "" {
		info.RegistrationToken = token
	}
	return agentmail.SaveSessionAgent(session, projectKey, info)
}

func mailMacroErrorCode(err error) string {
	switch {
	case errors.Is(err, agentmail.ErrNotImplemented):
		return "NOT_IMPLEMENTED"
	case errors.Is(err, agentmail.ErrUnauthorized):
		return "UNAUTHORIZED"
	case errors.Is(err, agentmail.ErrContactBlocked):
		return "CONTACT_BLOCKED"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, agentmail.ErrTimeout):
		return "TIMEOUT"
	default:
		return "MAIL_MACRO_FAILED"
	}
}

func renderMailMacroFailure(cmd *cobra.Command, out mailMacroOutput, code string, cause error) error {
	out.Success, out.ErrorCode, out.Error = false, code, cause.Error()
	if IsJSONOutput() {
		return emitJSONFailureEnvelopeToWithCause(mailJSONWriter(cmd), out, cause)
	}
	return cause
}

func newMailSendCmd() *cobra.Command {
	var (
		to                []string
		subject           string
		threadID          string
		reason            string
		all               bool
		fromFile          string
		preparedRedaction string
	)

	cmd := &cobra.Command{
		Use:   "send <session> [message]",
		Short: "Send Human Overseer message to agents",
		Long: `Send a Human Overseer message to agents via Agent Mail.

This uses the Agent Mail Human Overseer functionality, which:
  - Sends from the special "HumanOverseer" identity
  - Uses the server's authorized intervention route and audit log
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Marks all messages as high importance

Recipients must be Agent Mail agent names (e.g., "GreenCastle", "BlueLake").
Use --all to send to all registered agents in the project, excluding HumanOverseer.
Specifying multiple --to flags or --all confirms delivery to those recipients.
Use --reason to record why you are intervening in the server's audit log.

If no message body is provided, opens $EDITOR for composition.

For token-bearing payloads, use the prepare/send handle pattern:
  handle=$(SENDER_TOKEN=secret ntm redact prepare-mail --sender-token-env=SENDER_TOKEN --json | jq -r .handle)
  ntm mail send <session> --to <agent> --prepared-redaction "$handle" --subject "deploy token"
The raw token never enters the command line, env, or logs. An
explicit --subject is required so the auto-derivation path can never
truncate the raw token into the subject line. See ntm#126.

Examples:
  ntm mail send myproject --to GreenCastle "Please review the API changes"
  ntm mail send myproject --all --reason "Checkpoint before maintenance" "Stop current work and checkpoint"
  ntm mail send myproject --to BlueLake --to RedStone --thread FEAT-123 "Status update"
  ntm mail send myproject --all --file ./instructions.md`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			// Cobra commands are reused by tests and by embedded
			// callers in the same process. Local flag variables live
			// in this closure, so reset them after each execution or
			// omitted flags can leak into the next Execute() call.
			currentPrepared := preparedRedaction
			defer resetMailSendLocalFlags(cmd, &to, &subject, &threadID, &reason, &all, &fromFile, &preparedRedaction)

			// Reject invalid selections and audit fields before opening the
			// editor, consuming a one-use prepared body, or contacting the server.
			if all && len(to) > 0 {
				return fmt.Errorf("--all and --to are mutually exclusive")
			}
			if !all && len(to) == 0 {
				return fmt.Errorf("no recipients specified (use --to <agent-name> or --all)")
			}
			for i, recipient := range to {
				to[i] = strings.TrimSpace(recipient)
				if to[i] == "" {
					return fmt.Errorf("--to recipient cannot be empty")
				}
				if !utf8.ValidString(to[i]) {
					return fmt.Errorf("--to recipient must be valid UTF-8")
				}
			}
			if !utf8.ValidString(threadID) {
				return fmt.Errorf("--thread must be valid UTF-8")
			}
			reason = strings.TrimSpace(reason)
			if reason == "" {
				return fmt.Errorf("--reason cannot be empty")
			}
			if !utf8.ValidString(reason) {
				return fmt.Errorf("--reason must be valid UTF-8")
			}
			if utf8.RuneCountInString(reason) > agentmail.MaxOverseerReasonLength {
				return fmt.Errorf("--reason cannot exceed %d characters", agentmail.MaxOverseerReasonLength)
			}
			if cmd.Flags().Changed("subject") && strings.TrimSpace(subject) == "" {
				return fmt.Errorf("message subject cannot be empty")
			}
			if !utf8.ValidString(subject) {
				return fmt.Errorf("message subject must be valid UTF-8")
			}
			if subject != "" {
				// Validate the value that will be sent, since redaction can
				// change its length. Preserve the original for the send's
				// redaction summary and never consume a handle on a flag error.
				redactCfg := redaction.DefaultConfig()
				if cfg != nil {
					redactCfg = cfg.Redaction.ToRedactionLibConfig()
				}
				checkedSubject, _, err := applyOutputRedaction(subject, redactCfg)
				if err != nil {
					return err
				}
				if utf8.RuneCountInString(strings.TrimSpace(checkedSubject)) > 200 {
					return fmt.Errorf("message subject cannot exceed 200 characters")
				}
			}

			// --prepared-redaction is mutually exclusive with positional
			// body, --file, and the editor flow: the handle IS the body
			// source.
			if currentPrepared != "" {
				if fromFile != "" || len(args) > 1 {
					return fmt.Errorf("--prepared-redaction is mutually exclusive with --file and positional body")
				}
				// SECURITY: never auto-derive a subject from a
				// prepared-redaction body. truncateSubject() takes
				// the first 60 chars of the body — for a raw
				// token-bearing payload that's a leak path into
				// the audit log, JSON envelope, server-side logs,
				// and email indices whenever the configured
				// redaction patterns fail to match the user's
				// specific token shape. Require an explicit
				// subject before consuming the handle so a user who
				// forgot --subject can retry without regenerating
				// the handle.
				if strings.TrimSpace(subject) == "" {
					return fmt.Errorf("--prepared-redaction requires an explicit --subject to avoid leaking the raw token prefix as the auto-derived subject")
				}
				raw, _, _, err := consumePreparedRedaction(currentPrepared)
				if err != nil {
					return err
				}
				if strings.TrimSpace(raw) == "" {
					return fmt.Errorf("prepared redaction handle %q produced empty body", currentPrepared)
				}
				return runMailSendOverseer(cmd, session, to, subject, raw, threadID, reason, all)
			}
			if fromFile != "" && len(args) > 1 {
				return fmt.Errorf("--file and positional body are mutually exclusive")
			}

			// Get message body
			var body string
			if fromFile != "" {
				content, err := os.ReadFile(fromFile)
				if err != nil {
					return fmt.Errorf("reading file: %w", err)
				}
				body = string(content)
			} else if len(args) > 1 {
				body = strings.Join(args[1:], " ")
			} else {
				// Open editor for message composition
				composedBody, composedSubject, err := openEditorForMessage(subject)
				if err != nil {
					return fmt.Errorf("composing message: %w", err)
				}
				body = composedBody
				if subject == "" && composedSubject != "" {
					subject = composedSubject
				}
			}

			// Validate body is not empty
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("message body cannot be empty")
			}

			return runMailSendOverseer(cmd, session, to, subject, body, threadID, reason, all)
		},
	}

	cmd.Flags().StringArrayVar(&to, "to", nil, "recipient agent name (e.g., GreenCastle)")
	cmd.Flags().StringVarP(&subject, "subject", "s", "", "message subject (auto-derived if not provided)")
	cmd.Flags().StringVar(&threadID, "thread", "", "thread ID for conversation continuity")
	cmd.Flags().StringVar(&reason, "reason", agentmail.DefaultOverseerReason, "audit reason for this human intervention (1-500 characters)")
	cmd.Flags().BoolVar(&all, "all", false, "send to all registered agents in project")
	cmd.Flags().StringVarP(&fromFile, "file", "f", "", "read message body from file")
	cmd.Flags().StringVar(&preparedRedaction, "prepared-redaction", "", "Consume a token-handle from `ntm redact prepare-mail` and use its stashed body as the message (raw token never enters this command's args/env/logs; see ntm#126)")

	return cmd
}

func resetMailSendLocalFlags(cmd *cobra.Command, to *[]string, subject, threadID, reason *string, all *bool, fromFile, preparedRedaction *string) {
	*to = nil
	*subject = ""
	*threadID = ""
	*reason = agentmail.DefaultOverseerReason
	*all = false
	*fromFile = ""
	*preparedRedaction = ""
	for _, name := range []string{"to", "subject", "thread", "reason", "all", "file", "prepared-redaction"} {
		if flag := cmd.Flags().Lookup(name); flag != nil {
			flag.Changed = false
		}
	}
}

// mailInboxClient is the minimal interface we need for inbox operations (mockable in tests).
type mailInboxClient interface {
	IsAvailableContext(ctx context.Context) bool
	ListProjectAgents(ctx context.Context, projectKey string) ([]agentmail.Agent, error)
	FetchInbox(ctx context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error)
}

// mailAvailabilityErrorProvider is implemented by the concrete Agent Mail
// client, but remains optional so lightweight command mocks do not need to
// model diagnostic state.
type mailAvailabilityErrorProvider interface {
	LastAvailabilityError() error
}

// agentMailUnavailableMessage is the operator-facing text for a failed
// availability probe, with the probe's terminal error appended when one is
// known. Reservation verbs (lock/unlock/locks/renew/force-release) used to
// emit the bare string, which pointed operators at a healthy server and
// discarded the only evidence of what actually failed (issue #295).
func agentMailUnavailableMessage(client any) string {
	const base = "Agent Mail server unavailable"
	if reason := agentMailUnavailableReason(client); reason != nil {
		return base + ": " + reason.Error()
	}
	return base
}

// agentMailUnavailableReason returns the terminal error of the most recent
// availability probe, or nil when the client cannot report one.
func agentMailUnavailableReason(client any) error {
	if provider, ok := client.(mailAvailabilityErrorProvider); ok {
		return provider.LastAvailabilityError()
	}
	return nil
}

// agentMailUnavailableCLIError is the human-output counterpart of
// agentMailUnavailableMessage: a lowercase, wrapped error carrying the probe's
// terminal cause so `errors.Is` still works on it.
func agentMailUnavailableCLIError(client any) error {
	if reason := agentMailUnavailableReason(client); reason != nil {
		return fmt.Errorf("agent mail server unavailable: %w", reason)
	}
	return errors.New("agent mail server unavailable")
}

func agentMailUnavailableError(ctx context.Context, client any, fallback string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("checking agent mail availability: %w", ctxErr)
	}
	if provider, ok := client.(mailAvailabilityErrorProvider); ok {
		if reason := provider.LastAvailabilityError(); reason != nil {
			return fmt.Errorf("%s\nreason: %w", fallback, reason)
		}
	}
	return errors.New(fallback)
}

// newMailInboxCmd shows aggregate inbox for project agents.
func newMailInboxCmdReal() *cobra.Command {
	var (
		agent         string
		urgent        bool
		sessionAgents bool
		limit         int
	)

	cmd := &cobra.Command{
		Use:   "inbox [session]",
		Short: "Show aggregate project inbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runMailInbox(cmd, nil, session, sessionAgents, agent, urgent, limit, IsJSONOutput())
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Filter by specific agent name")
	cmd.Flags().BoolVar(&urgent, "urgent", false, "Show only urgent messages")
	cmd.Flags().BoolVar(&sessionAgents, "session-agents", false, "Filter to agents currently in session")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max messages to show")

	return cmd
}

func newAgentMailClient(projectKey string) *agentmail.Client {
	var opts []agentmail.Option
	opts = append(opts, agentmail.WithProjectKey(projectKey))
	if cfg != nil {
		opts = append(opts, agentmail.ConfigOptions(cfg.AgentMail.URL, cfg.AgentMail.Token)...)
	}
	client := agentmail.NewClient(opts...)
	// Pull every cached registration_token for this project out of any
	// on-disk session registries (mcp-agent-mail >=2.13 requires the
	// token on identity-scoped calls — ntm#146). Failure is silent:
	// when no registry exists yet, this is a no-op, and the first
	// register_agent / create_agent_identity call will populate
	// the cache the regular way.
	if projectKey != "" {
		agentmail.HydrateClientTokensForProject(client, projectKey)
	}
	return client
}

func resolveAgentMailScope(ctx context.Context, session string) (string, string, error) {
	return resolveAgentMailScopeWithPreference(ctx, session, true)
}

func resolveAgentMailCommandScope(ctx context.Context, session string) (string, string, error) {
	if ctx == nil {
		return "", "", fmt.Errorf("agent mail scope context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	session = strings.TrimSpace(session)
	if session == "" {
		return resolveAgentMailScope(ctx, "")
	}

	resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		return "", "", err
	}

	if projectKey, err := resolveExplicitProjectDirForSessionContext(ctx, resolved); err == nil {
		projectKey = refineAgentMailProjectKey(resolved, projectKey)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		if projectKey != "" {
			return resolved, projectKey, nil
		}
	} else if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}

	projectKey := refineAgentMailProjectKey(resolved, GetProjectRoot())
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}
	if projectKey == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}
	return resolved, projectKey, nil
}

func resolveAgentMailScopeWithPreference(ctx context.Context, session string, preferSession bool) (string, string, error) {
	if ctx == nil {
		return "", "", fmt.Errorf("agent mail scope context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(ctx, session, !IsJSONOutput())
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", "", ctxErr
			}
			return "", "", err
		}
		session = resolved
		projectKey := resolveCommandProjectDirForSession(ctx, session, !preferSession)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		projectKey = refineAgentMailProjectKey(session, projectKey)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", ctxErr
		}
		if projectKey == "" {
			return "", "", fmt.Errorf("getting project root failed")
		}
		return session, projectKey, nil
	}

	projectKey := GetProjectRoot()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", "", ctxErr
	}
	if projectKey == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}
	return "", projectKey, nil
}

func refineAgentMailProjectKey(sessionName, projectKey string) string {
	if strings.TrimSpace(sessionName) == "" {
		return projectKey
	}
	if registry, err := agentmail.LoadBestSessionAgentRegistry(sessionName, projectKey); err == nil && registry != nil {
		projectKey = bestUsableProjectDir(registry.ProjectKey, projectKey)
	}
	if info, err := agentmail.LoadBestSessionAgent(sessionName, projectKey); err == nil && info != nil {
		projectKey = bestUsableProjectDir(info.ProjectKey, projectKey)
	}
	return projectKey
}

// runMailInbox aggregates messages across agents and writes to cmd output.
func runMailInbox(cmd *cobra.Command, client mailInboxClient, session string, sessionAgents bool, agentFilter string, urgent bool, limit int, jsonFmt bool) error {
	parent, err := requireMailCommandContext(cmd, "mail inbox")
	if err != nil {
		return err
	}

	preferSession := strings.TrimSpace(session) != ""
	var (
		projectKey string
		scopeErr   error
	)

	// Filter to session agents if requested
	if sessionAgents {
		if session == "" {
			res, err := ResolveSessionWithOptionsContext(parent, "", cmd.OutOrStdout(), SessionResolveOptions{TreatAsJSON: jsonFmt})
			if err != nil {
				return err
			}
			if res.Session == "" {
				return nil
			}
			if !jsonFmt {
				res.ExplainIfInferred(cmd.ErrOrStderr())
			}
			session = res.Session
		}
	}

	if strings.TrimSpace(session) == "" {
		session, projectKey, scopeErr = resolveAgentMailScopeWithPreference(parent, session, preferSession)
	} else {
		session, projectKey, scopeErr = resolveAgentMailCommandScope(parent, session)
	}
	if scopeErr != nil {
		return scopeErr
	}

	if client == nil {
		client = newAgentMailClient(projectKey)
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	if !client.IsAvailableContext(ctx) {
		return agentMailUnavailableError(ctx, client, "agent mail server not available")
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("listing agents: %w", ctxErr)
		}
		return fmt.Errorf("listing agents: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("listing agents: %w", ctxErr)
	}

	targetAgents := make([]string, 0)
	if agentFilter != "" {
		targetAgents = append(targetAgents, agentFilter)
	} else {
		for _, a := range agents {
			if a.Name != "HumanOverseer" {
				targetAgents = append(targetAgents, a.Name)
			}
		}
	}

	if sessionAgents {
		panes, err := tmux.GetPanesContext(ctx, session)
		if err != nil {
			return fmt.Errorf("getting session panes: %w", err)
		}
		registry, _ := agentmail.LoadBestSessionAgentRegistry(session, projectKey)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("resolving session agents: %w", ctxErr)
		}
		sessionSet := make(map[string]bool)
		for _, p := range panes {
			name := resolvePaneAgentName(p, registry)
			if name != "" {
				sessionSet[name] = true
			}
		}
		filtered := targetAgents[:0]
		for _, name := range targetAgents {
			if sessionSet[name] {
				filtered = append(filtered, name)
			}
		}
		targetAgents = filtered
		if len(targetAgents) == 0 {
			return fmt.Errorf("no agents found in session '%s'", session)
		}
	}

	// Aggregated messages by ID (to dedupe across multiple agents)
	agg := make(map[int]*aggregatedMessage)

	for _, name := range targetAgents {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("fetching inbox: %w", ctxErr)
		}
		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey: projectKey,
			AgentName:  name,
			UrgentOnly: urgent,
			Limit:      limit,
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("fetching inbox for %s: %w", name, ctxErr)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("fetching inbox for %s: %w", name, err)
			}
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("fetching inbox for %s: %w", name, ctxErr)
		}
		for _, msg := range msgs {
			entry, ok := agg[msg.ID]
			if !ok {
				entry = &aggregatedMessage{
					ID:          msg.ID,
					Subject:     msg.Subject,
					From:        msg.From,
					CreatedTS:   msg.CreatedTS.Time,
					Importance:  msg.Importance,
					AckRequired: msg.AckRequired,
					Kind:        msg.Kind,
					BodyMD:      msg.BodyMD,
				}
				agg[msg.ID] = entry
			}
			entry.Recipients = append(entry.Recipients, name)
		}
	}

	if len(agg) == 0 {
		if jsonFmt {
			return json.NewEncoder(cmd.OutOrStdout()).Encode([]aggregatedMessage{})
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Inbox empty")
		return nil
	}

	var msgs []aggregatedMessage
	for _, m := range agg {
		msgs = append(msgs, *m)
	}

	// Simple deterministic order: newest ID last not available; just sort by ID.
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })

	if jsonFmt {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(msgs)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Project Inbox: %s\n", sanitizeMailDisplayField(filepath.Base(projectKey)))
	for _, m := range msgs {
		prefix := ""
		if strings.EqualFold(m.Importance, "urgent") || strings.EqualFold(m.Importance, "high") {
			prefix = "[URGENT] "
		}
		subject := sanitizeMailDisplayField(m.Subject)
		from := sanitizeMailDisplayField(m.From)
		var recipients []string
		for _, r := range m.Recipients {
			recipients = append(recipients, sanitizeMailDisplayField(r))
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", prefix, subject)
		if from != "" || len(recipients) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n", from, strings.Join(recipients, ", "))
		}
	}
	return nil
}

func requireMailCommandContext(cmd *cobra.Command, operation string) (context.Context, error) {
	if cmd == nil || cmd.Context() == nil {
		return nil, fmt.Errorf("%s requires a command context", operation)
	}
	ctx := cmd.Context()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%s canceled: %w", operation, err)
	}
	return ctx, nil
}

func sanitizeMailDisplayField(value string) string {
	clean := status.StripANSI(value)
	clean = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, clean)
	return strings.Join(strings.Fields(clean), " ")
}

// mailAction represents the kind of mailbox mutation to apply.
type mailAction string

const (
	mailActionRead mailAction = "read"
	mailActionAck  mailAction = "ack"
)

// newMailReadCmd marks messages as read.
func newMailReadCmd() *cobra.Command {
	return newMailMarkCmd(mailActionRead)
}

// newMailAckCmd marks messages as acknowledged.
func newMailAckCmd() *cobra.Command {
	return newMailMarkCmd(mailActionAck)
}

func newMailMarkCmd(action mailAction) *cobra.Command {
	var (
		agent     string
		urgent    bool
		fromAgent string
		all       bool
		limit     int
	)

	cmd := &cobra.Command{
		Use:   fmt.Sprintf("%s <session> [message-id...]", action),
		Short: fmt.Sprintf("Mark Agent Mail messages as %s", action),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

			resolvedAgent, err := resolveMailAgentIdentity(cmd.Context(), session, agent)
			if err != nil {
				return err
			}
			if limit <= 0 {
				return fmt.Errorf("--limit must be greater than 0")
			}

			ids, err := parseMessageIDs(args[1:])
			if err != nil {
				return err
			}

			return runMailMark(cmd, session, resolvedAgent, action, ids, urgent, fromAgent, all, limit)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "agent name to mark messages for (defaults to $AGENT_NAME)")
	cmd.Flags().BoolVar(&urgent, "urgent", false, "only mark urgent messages")
	cmd.Flags().StringVar(&fromAgent, "from", "", "only mark messages from this sender")
	cmd.Flags().BoolVar(&all, "all", false, "mark all matching messages (ignores positional IDs)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max messages to consider when using --all/filters")

	return cmd
}

func resolveMailAgentIdentity(ctx context.Context, session, agent string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("agent mail identity context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	agent = strings.TrimSpace(agent)
	if agent != "" {
		return agent, nil
	}

	resolvedSession, projectKey, err := resolveAgentMailCommandScope(ctx, session)
	if err != nil {
		return "", err
	}
	if agentName := resolveSessionPaneAgentName(ctx, resolvedSession, projectKey); agentName != "" {
		return agentName, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if info, err := loadResolvedSessionAgent(resolvedSession, projectKey); err == nil && info != nil && strings.TrimSpace(info.AgentName) != "" {
		return info.AgentName, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if envAgent := strings.TrimSpace(os.Getenv("AGENT_NAME")); envAgent != "" {
		return envAgent, nil
	}

	return "", fmt.Errorf("--agent is required (or set AGENT_NAME)")
}

func loadResolvedSessionAgent(session, projectKey string) (*agentmail.SessionAgentInfo, error) {
	if strings.TrimSpace(session) == "" {
		return nil, nil
	}
	return agentmail.LoadBestSessionAgent(session, projectKey)
}

func resolveSessionPaneAgentName(ctx context.Context, session, projectKey string) string {
	if ctx == nil || ctx.Err() != nil {
		return ""
	}
	if strings.TrimSpace(session) == "" {
		return ""
	}
	paneID := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if paneID == "" {
		return ""
	}

	registry, err := agentmail.LoadBestSessionAgentRegistry(session, projectKey)
	if err != nil || registry == nil {
		return ""
	}
	if name, ok := registry.GetAgent("", paneID); ok && strings.TrimSpace(name) != "" {
		return name
	}

	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return ""
	}
	for _, pane := range panes {
		if pane.ID != paneID {
			continue
		}
		return resolvePaneAgentName(pane, registry)
	}
	return ""
}

func resolvePaneAgentName(p tmux.Pane, registry *agentmail.SessionAgentRegistry) string {
	if registry != nil {
		if name, ok := registry.GetAgent(p.Title, p.ID); ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	return resolveAgentName(p)
}

// parseMessageIDs converts a slice of strings to ints.
func parseMessageIDs(raw []string) ([]int, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(raw))
	for _, s := range raw {
		id, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid message id %q", s)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type markSummary struct {
	Action    string `json:"action"`
	Agent     string `json:"agent"`
	Processed int    `json:"processed"`
	Skipped   int    `json:"skipped"`
	Errors    int    `json:"errors"`
	IDs       []int  `json:"ids"`
}

func runMailMark(cmd *cobra.Command, session, agent string, action mailAction, ids []int, urgent bool, fromAgent string, all bool, limit int) error {
	parent, err := requireMailCommandContext(cmd, "mail mark")
	if err != nil {
		return err
	}
	_, projectKey, err := resolveAgentMailCommandScope(parent, session)
	if err != nil {
		return err
	}

	client := newAgentMailClient(projectKey)
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	if !client.IsAvailableContext(ctx) {
		return agentMailUnavailableError(ctx, client, fmt.Sprintf(
			"agent mail server not available at %s\nstart the server with: am serve-http --host 127.0.0.1 --no-tui --no-auth",
			client.BaseURL(),
		))
	}

	// Ensure project exists (and agent registered)
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		return fmt.Errorf("ensuring project: %w", err)
	}

	// If no IDs provided, fetch inbox with filters.
	if len(ids) == 0 && !all && !urgent && fromAgent == "" {
		return fmt.Errorf("provide message IDs or use --all/--urgent/--from to select messages")
	}

	jsonEnabled := IsJSONOutput()
	if !jsonEnabled {
		if f := cmd.Root().PersistentFlags().Lookup("json"); f != nil && f.Changed {
			jsonEnabled = true
		}
	}

	if len(ids) == 0 {
		inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     agent,
			UrgentOnly:    urgent,
			IncludeBodies: false,
			Limit:         limit,
		})
		if err != nil {
			return fmt.Errorf("fetching inbox: %w", err)
		}
		for _, msg := range inbox {
			if fromAgent != "" && !strings.EqualFold(msg.From, fromAgent) {
				continue
			}
			if !mailMessageMatchesAction(msg, action) {
				continue
			}
			if !all && !urgent && fromAgent == "" {
				// No filters and no IDs: avoid marking everything accidentally
				break
			}
			ids = append(ids, msg.ID)
		}
		if len(ids) == 0 {
			if jsonEnabled {
				return encodeJSONResult(mailJSONWriter(cmd), markSummary{Action: string(action), Agent: agent})
			}
			fmt.Println("No matching messages found.")
			return nil
		}
	}

	var processed, errs int
	for _, id := range ids {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("marking messages: %w", ctxErr)
		}
		var markErr error
		switch action {
		case mailActionRead:
			_, markErr = client.MarkMessageRead(ctx, projectKey, agent, id)
		case mailActionAck:
			_, markErr = client.AcknowledgeMessage(ctx, projectKey, agent, id)
		}
		if markErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("marking message %d: %w", id, ctxErr)
			}
			if errors.Is(markErr, context.Canceled) || errors.Is(markErr, context.DeadlineExceeded) {
				return fmt.Errorf("marking message %d: %w", id, markErr)
			}
			errs++
			if !jsonEnabled {
				fmt.Fprintf(os.Stderr, "⚠ message %d: %v\n", id, markErr)
			}
			continue
		}
		processed++
		if !jsonEnabled {
			switch action {
			case mailActionRead:
				fmt.Printf("✓ Message %d marked as read\n", id)
			case mailActionAck:
				fmt.Printf("✓ Message %d acknowledged\n", id)
			}
		}
	}

	if jsonEnabled {
		return encodeJSONResult(mailJSONWriter(cmd), markSummary{
			Action:    string(action),
			Agent:     agent,
			Processed: processed,
			Skipped:   len(ids) - processed,
			Errors:    errs,
			IDs:       ids,
		})
	}

	return nil
}

func mailMessageMatchesAction(msg agentmail.InboxMessage, action mailAction) bool {
	switch action {
	case mailActionRead:
		return msg.ReadAt == nil
	case mailActionAck:
		return msg.AckRequired
	default:
		return true
	}
}

// runMailSendOverseer sends a Human Overseer message via the Agent Mail HTTP API.
func runMailSendOverseer(cmd *cobra.Command, session string, to []string, subject, body, threadID, reason string, all bool) error {
	parent, err := requireMailCommandContext(cmd, "mail overseer send")
	if err != nil {
		return err
	}
	resolvedSession, projectKey, err := resolveAgentMailCommandScope(parent, session)
	if err != nil {
		return err
	}
	session = resolvedSession

	// Session check is optional - we primarily care about the project
	// But it's useful to verify the user is in the right context
	if session != "" {
		if err := tmux.EnsureInstalled(); err == nil {
			if exists, probeErr := tmux.SessionExistsContext(parent, session); probeErr == nil && !exists {
				fmt.Fprintf(os.Stderr, "Warning: tmux session '%s' not found (continuing anyway)\n", session)
			}
		}
	}

	// Create Agent Mail client
	client := newAgentMailClient(projectKey)
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	// Check if Agent Mail is available
	if !client.IsAvailableContext(ctx) {
		return agentMailUnavailableError(ctx, client, fmt.Sprintf(
			"agent mail server not available at %s\nstart the server with: am serve-http --host 127.0.0.1 --no-tui --no-auth",
			client.BaseURL(),
		))
	}

	// Ensure project exists before proceeding
	project, err := client.EnsureProject(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("ensuring project: %w", err)
	}
	if strings.TrimSpace(project.Slug) == "" {
		return fmt.Errorf("server returned no project slug for overseer delivery")
	}

	// Resolve recipients
	var recipients []string
	if all {
		// Get all registered agents in the project
		agents, err := client.ListProjectAgents(ctx, projectKey)
		if err != nil {
			return fmt.Errorf("listing agents: %w", err)
		}
		for _, agent := range agents {
			// Skip the HumanOverseer itself
			name := strings.TrimSpace(agent.Name)
			if !strings.EqualFold(name, "HumanOverseer") {
				recipients = append(recipients, name)
			}
		}
		if len(recipients) == 0 {
			return fmt.Errorf("no agents registered in project (agents must register with Agent Mail first)")
		}
	} else {
		recipients = to
	}

	if len(recipients) == 0 {
		return fmt.Errorf("no recipients specified (use --to <agent-name> or --all)")
	}

	// Auto-generate subject if not provided
	if subject == "" {
		subject = truncateSubject(body, 60)
	}

	redactCfg := redaction.DefaultConfig()
	if cfg != nil {
		redactCfg = cfg.Redaction.ToRedactionLibConfig()
	}

	redactedBody, bodySummary, err := applyOutputRedaction(body, redactCfg)
	if err != nil {
		return err
	}
	redactedSubject, subjectSummary, err := applyOutputRedaction(subject, redactCfg)
	if err != nil {
		return err
	}
	redactedReason, reasonSummary, err := applyOutputRedaction(reason, redactCfg)
	if err != nil {
		return err
	}

	var redactionSummary *RedactionSummary
	var warnings []string
	if bodySummary != nil || subjectSummary != nil || reasonSummary != nil {
		totalFindings := 0
		totalCategories := map[string]int{}
		action := ""
		for _, s := range []*RedactionSummary{bodySummary, subjectSummary, reasonSummary} {
			if s == nil {
				continue
			}
			totalFindings += s.Findings
			for cat, count := range s.Categories {
				totalCategories[cat] += count
			}
			if action == "" {
				action = s.Action
			}
		}
		redactionSummary = &RedactionSummary{
			Mode:       string(redactCfg.Mode),
			Findings:   totalFindings,
			Categories: totalCategories,
			Action:     action,
		}

		cats := formatRedactionCategoryCounts(totalCategories)
		switch action {
		case "warn":
			msg := fmt.Sprintf("Warning: detected %d potential secret(s) (%s); message will be sent unmodified (mode=warn)", totalFindings, cats)
			fmt.Fprintln(cmd.ErrOrStderr(), msg)
			warnings = append(warnings, msg)
		case "redact":
			fmt.Fprintf(cmd.ErrOrStderr(), "Redacted %d potential secret(s) (%s)\n", totalFindings, cats)
		}
	}

	subject = redactedSubject
	body = redactedBody

	// The designated intervention route owns the reserved HumanOverseer
	// identity, authorization, audit trail, and priority/preamble semantics.
	result, err := client.SendOverseerMessage(ctx, agentmail.OverseerMessageOptions{
		ProjectKey:  projectKey,
		ProjectSlug: project.Slug,
		Recipients:  recipients,
		Subject:     subject,
		BodyMD:      body,
		ThreadID:    threadID,
		Reason:      redactedReason,
	})
	if err != nil {
		return fmt.Errorf("sending overseer message: %w", err)
	}

	// Output result
	jsonEnabled := IsJSONOutput()
	if !jsonEnabled {
		if f := cmd.Root().PersistentFlags().Lookup("json"); f != nil && f.Changed {
			jsonEnabled = true
		}
	}
	if jsonEnabled {
		payload := map[string]interface{}{
			"success":    result.Success,
			"recipients": result.Recipients,
			"subject":    subject,
			"message_id": result.MessageID,
			"sent_at":    result.SentAt,
			"overseer":   true,
		}
		if redactionSummary != nil {
			payload["redaction"] = redactionSummary
		}
		if len(warnings) > 0 {
			payload["warnings"] = warnings
		}
		return encodeJSONResult(mailJSONWriter(cmd), payload)
	}

	fmt.Printf("🚨 Human Overseer message sent to %d agent(s)\n", len(result.Recipients))
	for _, r := range result.Recipients {
		fmt.Printf("  → %s\n", r)
	}
	fmt.Printf("\nAgents will receive this with high priority and a directive preamble.\n")

	return nil
}

// resolveAgentName tries to get the agent name from a pane.
func resolveAgentName(p tmux.Pane) string {
	// Try pane title first (may contain agent name)
	if p.Title != "" && !strings.HasPrefix(p.Title, "pane") {
		if looksLikeAgentName(p.Title) {
			return p.Title
		}
	}

	// Fall back to generated name based on canonical pane type and index.
	var prefix string
	switch p.Type.Canonical() {
	case tmux.AgentClaude:
		prefix = "Claude"
	case tmux.AgentCodex:
		prefix = "Codex"
	case tmux.AgentGemini:
		prefix = "Gemini"
	case tmux.AgentAntigravity:
		prefix = "Antigravity"
	case tmux.AgentCursor:
		prefix = "Cursor"
	case tmux.AgentWindsurf:
		prefix = "Windsurf"
	case tmux.AgentAider:
		prefix = "Aider"
	case tmux.AgentOpencode:
		prefix = "Opencode"
	case tmux.AgentOmp:
		prefix = "Omp"
	case tmux.AgentOllama:
		prefix = "Ollama"
	default:
		return ""
	}
	return fmt.Sprintf("%sAgent%d", prefix, p.Index)
}

// looksLikeAgentName checks if a string looks like an AdjectiveNoun agent name.
func looksLikeAgentName(s string) bool {
	if strings.Contains(s, " ") || strings.Contains(s, "_") || strings.Contains(s, "-") {
		return false
	}
	if len(s) == 0 || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return true
		}
	}
	return false
}

// truncateSubject creates a subject from message body.
func truncateSubject(body string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	lines := strings.SplitN(strings.TrimSpace(body), "\n", 2)
	subject := strings.TrimSpace(lines[0])

	// Remove markdown heading prefix
	subject = strings.TrimPrefix(subject, "# ")
	subject = strings.TrimPrefix(subject, "## ")
	subject = strings.TrimPrefix(subject, "### ")

	runes := []rune(subject)
	if len(runes) > maxLen {
		if maxLen <= 3 {
			return string(runes[:maxLen])
		}
		return string(runes[:maxLen-3]) + "..."
	}
	return subject
}

func mailJSONWriter(cmd *cobra.Command) io.Writer {
	return cmd.Root().OutOrStdout()
}

// encodeJSONResult is a helper to output JSON.
func encodeJSONResult(w io.Writer, v interface{}) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

func resolveMailMacroProjectKey(ctx context.Context, session, explicitProjectKey string) (string, error) {
	if trimmed := strings.TrimSpace(explicitProjectKey); trimmed != "" {
		absPath, err := filepath.Abs(trimmed)
		if err != nil {
			return "", fmt.Errorf("resolving --project-key: %w", err)
		}
		return absPath, nil
	}
	if strings.TrimSpace(session) == "" {
		_, projectKey, err := resolveAgentMailScopeWithPreference(ctx, "", false)
		return projectKey, err
	}
	_, projectKey, err := resolveAgentMailCommandScope(ctx, session)
	return projectKey, err
}

func newMailSessionStartCmd() *cobra.Command {
	var (
		program         string
		model           string
		taskDescription string
		projectKeyFlag  string
	)

	cmd := &cobra.Command{
		Use:   "session-start [session]",
		Short: "Bootstrap an Agent Mail session (ensure project, register agent, fetch inbox)",
		Long: `Invoke the Agent Mail macro_start_session workflow to ensure the project
exists, register an agent identity, and fetch initial inbox messages in one call.

Examples:
  ntm mail session-start --program claude-code --model opus-4.6 --task "Audit scanner"
  ntm mail session-start myproject --program codex-cli --model gpt-5 --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() {
				program = "ntm"
				model = "default"
				taskDescription = ""
				projectKeyFlag = ""
			}()

			parent, err := requireMailCommandContext(cmd, "mail session-start")
			if err != nil {
				return err
			}

			var session string
			if len(args) > 0 {
				session = args[0]
			}
			prog := strings.TrimSpace(program)
			if prog == "" {
				return fmt.Errorf("--program cannot be empty")
			}
			mod := strings.TrimSpace(model)
			if mod == "" {
				return fmt.Errorf("--model cannot be empty")
			}

			projectKey, err := resolveMailMacroProjectKey(parent, session, projectKeyFlag)
			if err != nil {
				return err
			}

			client := newAgentMailClient(projectKey)
			ctx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()

			if !client.IsAvailableContext(ctx) {
				return agentMailUnavailableError(ctx, client, "agent mail server not available")
			}

			result, err := client.StartSession(ctx, projectKey, prog, mod, strings.TrimSpace(taskDescription))
			if err != nil {
				return fmt.Errorf("starting agent mail session: %w", err)
			}

			if result.Inbox == nil {
				result.Inbox = make([]agentmail.InboxMessage, 0)
			}
			if result.FileReservations == nil {
				result.FileReservations = &agentmail.ReservationResult{
					Granted:   make([]agentmail.FileReservation, 0),
					Conflicts: make([]agentmail.ReservationConflict, 0),
				}
			} else {
				if result.FileReservations.Granted == nil {
					result.FileReservations.Granted = make([]agentmail.FileReservation, 0)
				}
				if result.FileReservations.Conflicts == nil {
					result.FileReservations.Conflicts = make([]agentmail.ReservationConflict, 0)
				}
			}

			if IsJSONOutput() {
				return encodeJSONResult(mailJSONWriter(cmd), map[string]interface{}{
					"success":           true,
					"project_key":       projectKey,
					"project":           result.Project,
					"agent":             result.Agent,
					"file_reservations": result.FileReservations,
					"inbox":             result.Inbox,
				})
			}

			out := cmd.OutOrStdout()
			agentName := "(unknown)"
			if result.Agent != nil && result.Agent.Name != "" {
				agentName = result.Agent.Name
			}
			fmt.Fprintf(out, "Agent Mail session started for %s\n", agentName)
			fmt.Fprintf(out, "  Project: %s\n", projectKey)
			fmt.Fprintf(out, "  Inbox messages: %d\n", len(result.Inbox))
			if result.FileReservations != nil {
				fmt.Fprintf(out, "  Reservations granted: %d (conflicts: %d)\n",
					len(result.FileReservations.Granted), len(result.FileReservations.Conflicts))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&program, "program", "ntm", "Agent program identifier (e.g. claude-code, codex-cli)")
	cmd.Flags().StringVar(&model, "model", "default", "Model identifier (e.g. opus-4.6, gpt-5)")
	cmd.Flags().StringVar(&taskDescription, "task", "", "Current task description")
	cmd.Flags().StringVar(&projectKeyFlag, "project-key", "", "Explicit project root path override")

	return cmd
}

func newMailPrepareThreadCmd() *cobra.Command {
	var (
		threadID           string
		program            string
		model              string
		agentName          string
		taskDescription    string
		inboxLimit         int
		includeExamples    bool
		includeInboxBodies bool
		llmMode            bool
		llmModel           string
		registerIfMissing  bool
		projectKeyFlag     string
	)

	cmd := &cobra.Command{
		Use:   "prepare-thread [session]",
		Short: "Align an agent with an existing Agent Mail thread",
		Long: `Invoke the Agent Mail macro_prepare_thread workflow to ensure agent
registration, summarize an existing thread, and fetch recent inbox context.

Examples:
  ntm mail prepare-thread --thread FEAT-123 --program claude-code --model opus-4.6
  ntm mail prepare-thread myproject --thread br-42 --agent BlueLake --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			changedExamples := cmd.Flags().Changed("include-examples")
			changedBodies := cmd.Flags().Changed("include-inbox-bodies")
			changedLLM := cmd.Flags().Changed("llm-mode")
			changedRegister := cmd.Flags().Changed("register-if-missing")
			defer func() {
				threadID = ""
				program = "ntm"
				model = "default"
				agentName = ""
				taskDescription = ""
				inboxLimit = 0
				includeExamples = true
				includeInboxBodies = false
				llmMode = true
				llmModel = ""
				registerIfMissing = true
				projectKeyFlag = ""
			}()

			parent, err := requireMailCommandContext(cmd, "mail prepare-thread")
			if err != nil {
				return err
			}

			tid := strings.TrimSpace(threadID)
			if tid == "" {
				return fmt.Errorf("--thread is required")
			}
			prog := strings.TrimSpace(program)
			if prog == "" {
				return fmt.Errorf("--program cannot be empty")
			}
			mod := strings.TrimSpace(model)
			if mod == "" {
				return fmt.Errorf("--model cannot be empty")
			}

			var session string
			if len(args) > 0 {
				session = args[0]
			}
			projectKey, err := resolveMailMacroProjectKey(parent, session, projectKeyFlag)
			if err != nil {
				return err
			}

			opts := agentmail.PrepareThreadOptions{
				ProjectKey:      projectKey,
				ThreadID:        tid,
				Program:         prog,
				Model:           mod,
				AgentName:       strings.TrimSpace(agentName),
				InboxLimit:      inboxLimit,
				LLMModel:        strings.TrimSpace(llmModel),
				TaskDescription: strings.TrimSpace(taskDescription),
			}
			if changedExamples {
				v := includeExamples
				opts.IncludeExamples = &v
			}
			if changedBodies {
				v := includeInboxBodies
				opts.IncludeInboxBodies = &v
			}
			if changedLLM {
				v := llmMode
				opts.LLMMode = &v
			}
			if changedRegister {
				v := registerIfMissing
				opts.RegisterIfMissing = &v
			}

			client := newAgentMailClient(projectKey)
			ctx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()

			if !client.IsAvailableContext(ctx) {
				return agentMailUnavailableError(ctx, client, "agent mail server not available")
			}

			result, err := client.PrepareThread(ctx, opts)
			if err != nil {
				return fmt.Errorf("preparing thread %q: %w", tid, err)
			}

			if result.Inbox == nil {
				result.Inbox = make([]agentmail.InboxMessage, 0)
			}
			var examples []agentmail.InboxMessage
			var summary agentmail.ThreadSummary
			if result.Thread != nil {
				examples = result.Thread.Examples
				summary = result.Thread.Summary
				if summary.Participants == nil {
					summary.Participants = make([]string, 0)
					result.Thread.Summary.Participants = summary.Participants
				}
				if summary.KeyPoints == nil {
					summary.KeyPoints = make([]string, 0)
					result.Thread.Summary.KeyPoints = summary.KeyPoints
				}
				if summary.ActionItems == nil {
					summary.ActionItems = make([]string, 0)
					result.Thread.Summary.ActionItems = summary.ActionItems
				}
			}
			if examples == nil {
				examples = make([]agentmail.InboxMessage, 0)
			}

			if IsJSONOutput() {
				return encodeJSONResult(mailJSONWriter(cmd), map[string]interface{}{
					"success":        true,
					"project_key":    projectKey,
					"thread_id":      tid,
					"agent":          result.Agent,
					"thread_summary": summary,
					"examples":       examples,
					"inbox":          result.Inbox,
				})
			}

			out := cmd.OutOrStdout()
			resolvedAgent := "(unknown)"
			if result.Agent != nil && result.Agent.Name != "" {
				resolvedAgent = result.Agent.Name
			}
			fmt.Fprintf(out, "Prepared thread %s for %s\n", tid, resolvedAgent)
			if result.Thread != nil {
				fmt.Fprintf(out, "  Participants: %d | Key points: %d | Action items: %d\n",
					len(summary.Participants),
					len(summary.KeyPoints),
					len(summary.ActionItems))
			}
			fmt.Fprintf(out, "  Examples: %d | Inbox messages: %d\n", len(examples), len(result.Inbox))
			return nil
		},
	}

	cmd.Flags().StringVar(&threadID, "thread", "", "Thread ID to prepare (required)")
	cmd.Flags().StringVar(&program, "program", "ntm", "Agent program identifier")
	cmd.Flags().StringVar(&model, "model", "default", "Model identifier")
	cmd.Flags().StringVar(&agentName, "agent", "", "Agent name (auto-generated if omitted)")
	cmd.Flags().StringVar(&taskDescription, "task", "", "Current task description")
	cmd.Flags().IntVar(&inboxLimit, "inbox-limit", 0, "Maximum inbox messages to fetch")
	cmd.Flags().BoolVar(&includeExamples, "include-examples", true, "Include sample messages from the thread")
	cmd.Flags().BoolVar(&includeInboxBodies, "include-inbox-bodies", false, "Include full message bodies in inbox context")
	cmd.Flags().BoolVar(&llmMode, "llm-mode", true, "Use LLM refinement for thread summary")
	cmd.Flags().StringVar(&llmModel, "llm-model", "", "Override LLM model for thread summary")
	cmd.Flags().BoolVar(&registerIfMissing, "register-if-missing", true, "Register agent if missing")
	cmd.Flags().StringVar(&projectKeyFlag, "project-key", "", "Explicit project root path override")

	return cmd
}

func newMailHandshakeCmd() *cobra.Command {
	var (
		toAgent         string
		toAgentAlias    string
		agentName       string
		toProject       string
		reason          string
		program         string
		model           string
		taskDescription string
		autoAccept      bool
		welcomeSubject  string
		welcomeBody     string
		ttlSeconds      int
		projectKeyFlag  string
	)

	cmd := &cobra.Command{
		Use:   "handshake [session]",
		Short: "Establish an Agent Mail contact handshake with another agent",
		Long: `Invoke the Agent Mail macro_contact_handshake workflow to request contact
permissions, optionally auto-accept mutual links, and send an optional welcome message.

Examples:
  ntm mail handshake --to GreenCastle --agent BlueLake --reason "Coordinate API changes"
  ntm mail handshake myproject --to GreenCastle --auto-accept --welcome-subject "Hello" --welcome-body "Ready to sync" --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defer func() {
				toAgent = ""
				toAgentAlias = ""
				agentName = ""
				toProject = ""
				reason = ""
				program = ""
				model = ""
				taskDescription = ""
				autoAccept = false
				welcomeSubject = ""
				welcomeBody = ""
				ttlSeconds = 0
				projectKeyFlag = ""
			}()

			parent, err := requireMailCommandContext(cmd, "mail handshake")
			if err != nil {
				return err
			}

			target := strings.TrimSpace(toAgent)
			if target == "" {
				target = strings.TrimSpace(toAgentAlias)
			}
			if target == "" {
				return fmt.Errorf("--to is required")
			}

			var session string
			if len(args) > 0 {
				session = args[0]
			}
			projectKey, err := resolveMailMacroProjectKey(parent, session, projectKeyFlag)
			if err != nil {
				return err
			}

			opts := agentmail.ContactHandshakeOptions{
				ProjectKey:      projectKey,
				AgentName:       strings.TrimSpace(agentName),
				ToAgent:         target,
				ToProject:       strings.TrimSpace(toProject),
				Reason:          strings.TrimSpace(reason),
				Program:         strings.TrimSpace(program),
				Model:           strings.TrimSpace(model),
				TaskDescription: strings.TrimSpace(taskDescription),
				AutoAccept:      autoAccept,
				WelcomeSubject:  strings.TrimSpace(welcomeSubject),
				WelcomeBody:     strings.TrimSpace(welcomeBody),
				TTLSeconds:      ttlSeconds,
			}

			client := newAgentMailClient(projectKey)
			ctx, cancel := context.WithTimeout(parent, 30*time.Second)
			defer cancel()

			if !client.IsAvailableContext(ctx) {
				return agentMailUnavailableError(ctx, client, "agent mail server not available")
			}

			result, err := client.ContactHandshake(ctx, opts)
			if err != nil {
				return fmt.Errorf("contact handshake with %q: %w", target, err)
			}

			if IsJSONOutput() {
				status := ""
				if result.Request != nil {
					status = result.Request.Status
				}
				return encodeJSONResult(mailJSONWriter(cmd), map[string]interface{}{
					"success":                true,
					"project_key":            projectKey,
					"to_agent":               target,
					"contact_status":         status,
					"request":                result.Request,
					"response":               result.Response,
					"welcome_message":        result.WelcomeMsg,
					"welcome_skipped_reason": result.WelcomeSkippedReason,
				})
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Contact handshake with %s\n", target)
			if result.Request != nil {
				fmt.Fprintf(out, "  Request recorded\n")
			}
			if result.WelcomeMsg != nil && result.WelcomeMsg.Count > 0 {
				fmt.Fprintf(out, "  Welcome deliveries: %d\n", result.WelcomeMsg.Count)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&toAgent, "to", "", "Target agent name to contact (required)")
	cmd.Flags().StringVar(&toAgentAlias, "to-agent", "", "Alias for --to")
	cmd.Flags().StringVar(&agentName, "agent", "", "Requester agent name (auto-generated if omitted)")
	cmd.Flags().StringVar(&toProject, "to-project", "", "Target project path or slug (same project if omitted)")
	cmd.Flags().StringVar(&reason, "reason", "", "Reason for requesting contact")
	cmd.Flags().StringVar(&program, "program", "", "Agent program identifier for auto-registration")
	cmd.Flags().StringVar(&model, "model", "", "Model identifier for auto-registration")
	cmd.Flags().StringVar(&taskDescription, "task", "", "Task description for auto-registration")
	cmd.Flags().BoolVar(&autoAccept, "auto-accept", false, "Auto-accept mutual contact link")
	cmd.Flags().StringVar(&welcomeSubject, "welcome-subject", "", "Optional welcome message subject")
	cmd.Flags().StringVar(&welcomeBody, "welcome-body", "", "Optional welcome message body")
	cmd.Flags().IntVar(&ttlSeconds, "ttl", 0, "Contact approval TTL in seconds")
	cmd.Flags().StringVar(&projectKeyFlag, "project-key", "", "Explicit project root path override")

	return cmd
}

// openEditorForMessage opens $EDITOR for message composition.
// Returns the body and subject (parsed from first line if it's a heading).
func openEditorForMessage(existingSubject string) (body string, subject string, err error) {
	// Create temp file with template
	tmpFile, err := os.CreateTemp("", "ntm-mail-*.md")
	if err != nil {
		return "", "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Write template
	template := "# Subject\n\n<!-- Write your message below. First line starting with # becomes the subject. -->\n\n"
	if existingSubject != "" {
		template = fmt.Sprintf("# %s\n\n<!-- Edit your message below. -->\n\n", existingSubject)
	}
	if _, err := tmpFile.WriteString(template); err != nil {
		tmpFile.Close()
		return "", "", fmt.Errorf("writing template: %w", err)
	}
	tmpFile.Close()

	// Open editor
	cmd, err := buildEditorCommandWithFallback(tmpPath, "nano")
	if err != nil {
		return "", "", fmt.Errorf("configuring editor: %w", err)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("running editor: %w", err)
	}

	// Read composed message
	content, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", "", fmt.Errorf("reading composed message: %w", err)
	}

	// Parse content
	lines := strings.Split(string(content), "\n")
	var bodyLines []string
	subject = ""

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip HTML comments
		if strings.HasPrefix(trimmed, "<!--") && strings.HasSuffix(trimmed, "-->") {
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") || strings.HasSuffix(trimmed, "-->") {
			continue
		}

		// Extract subject from first heading
		if subject == "" && strings.HasPrefix(trimmed, "#") {
			// Remove heading markers
			subject = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if subject == "Subject" {
				subject = "" // Placeholder wasn't changed
			}
			continue
		}

		// Skip leading empty lines before body
		if len(bodyLines) == 0 && trimmed == "" {
			continue
		}

		bodyLines = append(bodyLines, lines[i])
	}

	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
	return body, subject, nil
}

type aggregatedMessage struct {
	ID          int       `json:"id"`
	Subject     string    `json:"subject"`
	From        string    `json:"from"`
	CreatedTS   time.Time `json:"created_ts"`
	Importance  string    `json:"importance"`
	AckRequired bool      `json:"ack_required"`
	Kind        string    `json:"kind"`
	BodyMD      string    `json:"body_md,omitempty"` // truncated for display
	Recipients  []string  `json:"recipients"`
}
