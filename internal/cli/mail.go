package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newMailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail",
		Short: "Human Overseer messaging to agents",
		Long: `Send Human Overseer messages to AI agents via Agent Mail.

This command provides the CLI interface for the Human Overseer functionality
in Agent Mail. Messages sent this way:
  - Come from the special "HumanOverseer" identity
  - Bypass contact policies (humans can always reach agents)
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Are marked as high importance

This is the CLI equivalent of the Agent Mail web UI at /mail/{project}/overseer/compose

Examples:
  ntm mail send myproject --to GreenCastle "Please review the API changes"
  ntm mail send myproject --all "Checkpoint: sync and report status"`,
	}

	cmd.AddCommand(newMailSendCmd())
	cmd.AddCommand(newMailInboxCmdReal())
	cmd.AddCommand(newMailReadCmd())
	cmd.AddCommand(newMailAckCmd())

	return cmd
}

func newMailSendCmd() *cobra.Command {
	var (
		to       []string
		subject  string
		threadID string
		all      bool
		fromFile string
	)

	cmd := &cobra.Command{
		Use:   "send <session> [message]",
		Short: "Send Human Overseer message to agents",
		Long: `Send a Human Overseer message to agents via Agent Mail.

This uses the Agent Mail Human Overseer functionality, which:
  - Sends from the special "HumanOverseer" identity
  - Bypasses contact policies (humans can always reach any agent)
  - Auto-inject a preamble telling agents to prioritize your instructions
  - Marks all messages as high importance

Recipients must be Agent Mail agent names (e.g., "GreenCastle", "BlueLake").
Use --all to send to all registered agents in the project.

If no message body is provided, opens $EDITOR for composition.

Examples:
  ntm mail send myproject --to GreenCastle "Please review the API changes"
  ntm mail send myproject --all "Stop current work and checkpoint"
  ntm mail send myproject --to BlueLake --to RedStone --thread FEAT-123 "Status update"
  ntm mail send myproject --all --file ./instructions.md`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]

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

			return runMailSendOverseer(cmd, session, to, subject, body, threadID, all)
		},
	}

	cmd.Flags().StringArrayVar(&to, "to", nil, "recipient agent name (e.g., GreenCastle)")
	cmd.Flags().StringVarP(&subject, "subject", "s", "", "message subject (auto-derived if not provided)")
	cmd.Flags().StringVar(&threadID, "thread", "", "thread ID for conversation continuity")
	cmd.Flags().BoolVar(&all, "all", false, "send to all registered agents in project")
	cmd.Flags().StringVarP(&fromFile, "file", "f", "", "read message body from file")

	return cmd
}

// mailInboxClient is the minimal interface we need for inbox operations (mockable in tests).
type mailInboxClient interface {
	IsAvailable() bool
	ListProjectAgents(ctx context.Context, projectKey string) ([]agentmail.Agent, error)
	FetchInbox(ctx context.Context, opts agentmail.FetchInboxOptions) ([]agentmail.InboxMessage, error)
}

// newMailInboxCmd shows aggregate inbox for project agents.
func newMailInboxCmdReal() *cobra.Command {
	var (
		agent         string
		urgent        bool
		sessionAgents bool
		jsonFormat    bool
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
			return runMailInbox(cmd, nil, session, sessionAgents, agent, urgent, limit, jsonFormat)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Filter by specific agent name")
	cmd.Flags().BoolVar(&urgent, "urgent", false, "Show only urgent messages")
	cmd.Flags().BoolVar(&sessionAgents, "session-agents", false, "Filter to agents currently in session")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max messages to show")
	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output in JSON format")

	return cmd
}

// runMailInbox aggregates messages across agents and writes to cmd output.
func runMailInbox(cmd *cobra.Command, client mailInboxClient, session string, sessionAgents bool, agentFilter string, urgent bool, limit int, jsonFmt bool) error {
	projectKey, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	if client == nil {
		client = agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available")
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
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

	// Filter to session agents if requested
	if sessionAgents {
		if session == "" {
			if tmux.InTmux() {
				session = tmux.GetCurrentSession()
			} else {
				return fmt.Errorf("session name required for --session-agents")
			}
		}
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return fmt.Errorf("getting session panes: %w", err)
		}
		sessionSet := make(map[string]bool)
		for _, p := range panes {
			name := resolveAgentName(p)
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

	type aggregatedMessage struct {
		ID         int
		Subject    string
		From       string
		Recipients []string
		Importance string
	}

	agg := make(map[int]*aggregatedMessage)

	for _, name := range targetAgents {
		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey: projectKey,
			AgentName:  name,
			UrgentOnly: urgent,
			Limit:      limit,
		})
		if err != nil {
			return err
		}
		for _, msg := range msgs {
			entry, ok := agg[msg.ID]
			if !ok {
				entry = &aggregatedMessage{
					ID:         msg.ID,
					Subject:    msg.Subject,
					From:       msg.From,
					Importance: msg.Importance,
				}
				agg[msg.ID] = entry
			}
			entry.Recipients = append(entry.Recipients, name)
		}
	}

	if len(agg) == 0 {
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

	fmt.Fprintf(cmd.OutOrStdout(), "Project Inbox: %s\n", filepath.Base(projectKey))
	for _, m := range msgs {
		prefix := ""
		if strings.EqualFold(m.Importance, "urgent") || strings.EqualFold(m.Importance, "high") {
			prefix = "[URGENT] "
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", prefix, m.Subject)
		if m.From != "" || len(m.Recipients) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%s → %s\n", m.From, strings.Join(m.Recipients, ", "))
		}
	}
	return nil
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
			if agent == "" {
				agent = os.Getenv("AGENT_NAME")
			}
			if agent == "" {
				return fmt.Errorf("--agent is required (or set AGENT_NAME)")
			}
			if limit <= 0 {
				return fmt.Errorf("--limit must be greater than 0")
			}

			ids, err := parseMessageIDs(args[1:])
			if err != nil {
				return err
			}

			return runMailMark(cmd, session, agent, action, ids, urgent, fromAgent, all, limit)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "agent name to mark messages for (defaults to $AGENT_NAME)")
	cmd.Flags().BoolVar(&urgent, "urgent", false, "only mark urgent messages")
	cmd.Flags().StringVar(&fromAgent, "from", "", "only mark messages from this sender")
	cmd.Flags().BoolVar(&all, "all", false, "mark all matching messages (ignores positional IDs)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max messages to consider when using --all/filters")

	return cmd
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
	projectKey, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available at %s\nstart the server with: mcp-agent-mail serve", agentmail.DefaultBaseURL)
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
		var markErr error
		switch action {
		case mailActionRead:
			markErr = client.MarkMessageRead(ctx, projectKey, agent, id)
		case mailActionAck:
			markErr = client.AcknowledgeMessage(ctx, projectKey, agent, id)
		}
		if markErr != nil {
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

// runMailSendOverseer sends a Human Overseer message via the Agent Mail HTTP API.
func runMailSendOverseer(cmd *cobra.Command, session string, to []string, subject, body, threadID string, all bool) error {
	// Session check is optional - we primarily care about the project
	// But it's useful to verify the user is in the right context
	if session != "" {
		if err := tmux.EnsureInstalled(); err == nil {
			if !tmux.SessionExists(session) {
				fmt.Fprintf(os.Stderr, "Warning: tmux session '%s' not found (continuing anyway)\n", session)
			}
		}
	}

	// Get project key (current working directory)
	projectKey, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Create Agent Mail client
	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check if Agent Mail is available
	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available at %s\nstart the server with: mcp-agent-mail serve", agentmail.DefaultBaseURL)
	}

	// Ensure project exists before proceeding
	project, err := client.EnsureProject(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("ensuring project: %w", err)
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
			if agent.Name != "HumanOverseer" {
				recipients = append(recipients, agent.Name)
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

	// Derive project slug for the HTTP endpoint
	projectSlug := agentmail.ProjectSlugFromPath(projectKey)
	if project.Slug != "" {
		projectSlug = project.Slug // Use server-provided slug if available
	}

	// Send via Human Overseer endpoint
	result, err := client.SendOverseerMessage(ctx, agentmail.OverseerMessageOptions{
		ProjectSlug: projectSlug,
		Recipients:  recipients,
		Subject:     subject,
		BodyMD:      body,
		ThreadID:    threadID,
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
		return encodeJSONResult(mailJSONWriter(cmd), map[string]interface{}{
			"success":    result.Success,
			"recipients": result.Recipients,
			"subject":    subject,
			"message_id": result.MessageID,
			"sent_at":    result.SentAt,
			"overseer":   true,
		})
	}

	fmt.Printf("🚨 Human Overseer message sent to %d agent(s)\n", len(result.Recipients))
	for _, r := range result.Recipients {
		fmt.Printf("  → %s\n", r)
	}
	fmt.Printf("\nAgents will receive this with high priority and a directive preamble.\n")

	return nil
}

// resolveRecipients resolves recipient specifiers to agent names.
func resolveRecipients(session string, to []string, all, targetCC, targetCod, targetGmi bool) ([]string, error) {
	var recipients []string

	// Get panes for resolution
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("getting panes: %w", err)
	}

	// Build pane map for resolution
	paneMap := make(map[string]tmux.Pane) // e.g., "cc_1" -> pane
	for _, p := range panes {
		// Generate pane identifier based on type
		var prefix string
		switch p.Type {
		case tmux.AgentClaude:
			prefix = "cc"
		case tmux.AgentCodex:
			prefix = "cod"
		case tmux.AgentGemini:
			prefix = "gmi"
		default:
			prefix = "user"
		}
		id := fmt.Sprintf("%s_%d", prefix, p.Index)
		paneMap[id] = p
	}

	// Process --all flag
	if all {
		for _, p := range panes {
			if p.Type != tmux.AgentUser {
				name := resolveAgentName(p)
				if name != "" {
					recipients = append(recipients, name)
				}
			}
		}
	}

	// Process type filters
	if targetCC {
		for _, p := range panes {
			if p.Type == tmux.AgentClaude {
				name := resolveAgentName(p)
				if name != "" {
					recipients = append(recipients, name)
				}
			}
		}
	}
	if targetCod {
		for _, p := range panes {
			if p.Type == tmux.AgentCodex {
				name := resolveAgentName(p)
				if name != "" {
					recipients = append(recipients, name)
				}
			}
		}
	}
	if targetGmi {
		for _, p := range panes {
			if p.Type == tmux.AgentGemini {
				name := resolveAgentName(p)
				if name != "" {
					recipients = append(recipients, name)
				}
			}
		}
	}

	// Process --to recipients
	for _, recipient := range to {
		// Check if it's a pane identifier
		if p, ok := paneMap[recipient]; ok {
			name := resolveAgentName(p)
			if name != "" {
				recipients = append(recipients, name)
			}
		} else {
			// Assume it's an agent name
			recipients = append(recipients, recipient)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	unique := make([]string, 0, len(recipients))
	for _, r := range recipients {
		if !seen[r] {
			seen[r] = true

			unique = append(unique, r)
		}
	}

	return unique, nil
}

// resolveAgentName tries to get the agent name from a pane.
func resolveAgentName(p tmux.Pane) string {
	// Try pane title first (may contain agent name)
	if p.Title != "" && !strings.HasPrefix(p.Title, "pane") {
		if looksLikeAgentName(p.Title) {
			return p.Title
		}
	}

	// Fall back to generated name based on type and index
	var prefix string
	switch p.Type {
	case tmux.AgentClaude:
		prefix = "Claude"
	case tmux.AgentCodex:
		prefix = "Codex"
	case tmux.AgentGemini:
		prefix = "Gemini"
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
	lines := strings.SplitN(body, "\n", 2)
	subject := strings.TrimSpace(lines[0])

	// Remove markdown heading prefix
	subject = strings.TrimPrefix(subject, "# ")
	subject = strings.TrimPrefix(subject, "## ")
	subject = strings.TrimPrefix(subject, "### ")

	if len(subject) > maxLen {
		return subject[:maxLen-3] + "..."
	}
	return subject
}

func mailJSONWriter(cmd *cobra.Command) io.Writer {
	return io.MultiWriter(cmd.Root().OutOrStdout(), cmd.Root().ErrOrStderr())
}

// encodeJSONResult is a helper to output JSON.
func encodeJSONResult(w io.Writer, v interface{}) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

// openEditorForMessage opens $EDITOR for message composition.
// Returns the body and subject (parsed from first line if it's a heading).
func openEditorForMessage(existingSubject string) (body string, subject string, err error) {
	// Get editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "nano" // fallback
	}

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
	editorCmd, editorArgs := parseEditorCommand(editor)
	args := append(editorArgs, tmpPath)
	cmd := exec.Command(editorCmd, args...)
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
func newMailInboxCmd() *cobra.Command {
	var (
		agent         string
		urgent        bool
		sessionAgents bool
		jsonFormat    bool
		limit         int
	)

	cmd := &cobra.Command{
		Use:   "inbox [session]",
		Short: "Show aggregate project inbox",
		Long: `Display an aggregate inbox view showing messages across ALL agents in the project.

		This gives visibility into all agent-to-agent communication.

		Examples:
		  ntm mail inbox myproject
		  ntm mail inbox myproject --urgent
		  ntm mail inbox myproject --agent cc_1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			if len(args) > 0 {
				session = args[0]
			}
			return runMailInbox(cmd, session, sessionAgents, agent, urgent, limit, jsonFormat)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Filter by specific agent name")
	cmd.Flags().BoolVar(&urgent, "urgent", false, "Show only urgent messages")
	cmd.Flags().BoolVar(&sessionAgents, "session-agents", false, "Filter to agents currently in session")
	cmd.Flags().IntVar(&limit, "limit", 50, "Max messages to show")
	cmd.Flags().BoolVar(&jsonFormat, "json", false, "Output in JSON format")

	return cmd
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

func runMailInbox(cmd *cobra.Command, session string, sessionAgents bool, agentFilter string, urgent bool, limit int, jsonFmt bool) error {
	projectKey, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if !client.IsAvailable() {
		return fmt.Errorf("agent mail server not available")
	}

	// List all agents in project
	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}

	var targetAgents []string
	if agentFilter != "" {
		targetAgents = []string{agentFilter}
	} else {
		for _, a := range agents {
			if a.Name != "HumanOverseer" {
				targetAgents = append(targetAgents, a.Name)
			}
		}
	}

	// Filter by session agents if requested
	if sessionAgents {
		if session == "" {
			if tmux.InTmux() {
				session = tmux.GetCurrentSession()
			} else {
				return fmt.Errorf("session name required for --session-agents")
			}
		}
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return fmt.Errorf("getting session panes: %w", err)
		}

		sessionAgentSet := make(map[string]bool)
		for _, p := range panes {
			name := resolveAgentName(p)
			if name != "" {
				sessionAgentSet[name] = true
			}
		}

		var filteredAgents []string
		for _, name := range targetAgents {
			if sessionAgentSet[name] {
				filteredAgents = append(filteredAgents, name)
			}
		}
		targetAgents = filteredAgents

		if len(targetAgents) == 0 {
			return fmt.Errorf("no agents found in session '%s'", session)
		}
	}

	// Collect messages
	var allMessages []*aggregatedMessage
	var mu sync.Mutex
	var wg sync.WaitGroup
	messageMap := make(map[int]*aggregatedMessage)

	for _, agentName := range targetAgents {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey: projectKey,
				AgentName:  name,
				UrgentOnly: urgent,
				Limit:      limit,
			})
			if err == nil {
				mu.Lock()
				for _, msg := range msgs {
					agg, exists := messageMap[msg.ID]
					if !exists {
						agg = &aggregatedMessage{
							ID:          msg.ID,
							Subject:     msg.Subject,
							From:        msg.From,
							CreatedTS:   msg.CreatedTS,
							Importance:  msg.Importance,
							AckRequired: msg.AckRequired,
							Kind:        msg.Kind,
							BodyMD:      msg.BodyMD,
							Recipients:  []string{},
						}
						messageMap[msg.ID] = agg
						allMessages = append(allMessages, agg)
					}
					// Add recipient if not present
					hasRecipient := false
					for _, r := range agg.Recipients {
						if r == name {
							hasRecipient = true
							break
						}
					}
					if !hasRecipient {
						agg.Recipients = append(agg.Recipients, name)
					}
				}
				mu.Unlock()
			}
		}(agentName)
	}
	wg.Wait()

	// Sort by time descending
	sort.Slice(allMessages, func(i, j int) bool {
		return allMessages[i].CreatedTS.After(allMessages[j].CreatedTS)
	})

	if IsJSONOutput() || jsonFmt {
		return encodeJSONResult(mailJSONWriter(cmd), allMessages)
	}

	if len(allMessages) == 0 {
		fmt.Println("No messages found.")
		return nil
	}

	fmt.Printf("Inbox for project %s (%d messages)\n", filepath.Base(projectKey), len(allMessages))
	for _, m := range allMessages {
		prefix := " "
		if m.Importance == "high" || m.Importance == "urgent" {
			prefix = "!"
		}
		recipients := strings.Join(m.Recipients, ", ")
		if len(recipients) > 30 {
			recipients = recipients[:27] + "..."
		}

		fmt.Printf("%s [%s] #%d %s -> %s: %s\n",
			prefix,
			m.CreatedTS.Format("15:04"),
			m.ID,
			m.From,
			recipients,
			m.Subject)
	}

	return nil
}
