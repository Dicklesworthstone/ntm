package agentmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/ntm/internal/reservationpath"
)

// attachTokenFromField adds a cached token to an MCP tool arg map.
// Different Agent Mail tools use different token parameter names:
// send_message/reply_message use sender_token, most agent-scoped read
// and reservation tools use registration_token, and handshake macros
// use requester_registration_token / target_registration_token.
func (c *Client) attachTokenFromField(args map[string]interface{}, tokenField, agentField string) {
	if c == nil || args == nil {
		return
	}
	if _, ok := args[tokenField]; ok {
		return // caller supplied it explicitly; do not clobber
	}
	projectKey, _ := args["project_key"].(string)
	if projectKey == "" {
		projectKey, _ = args["human_key"].(string)
	}
	if projectKey == "" {
		return
	}
	agentName, _ := args[agentField].(string)
	if agentName == "" {
		return
	}
	if token := c.RegistrationToken(projectKey, agentName); token != "" {
		args[tokenField] = token
	}
}

func (c *Client) attachRegistrationToken(args map[string]interface{}) {
	c.attachTokenFromField(args, "registration_token", "agent_name")
}

func (c *Client) attachSenderToken(args map[string]interface{}) {
	c.attachTokenFromField(args, "sender_token", "sender_name")
}

// EnsureProject ensures a project exists for the given path.
func (c *Client) EnsureProject(ctx context.Context, projectKey string) (*Project, error) {
	args := map[string]interface{}{
		"human_key": projectKey,
	}

	// Use retry logic for project creation as it's often the first call made
	// by multiple agents starting simultaneously.
	result, err := c.callToolWithBusyRetry(ctx, "ensure_project", args, 3*time.Second, busyMaxRetries())
	if err != nil {
		return nil, err
	}

	var project Project
	if err := json.Unmarshal(result, &project); err != nil {
		return nil, NewAPIError("ensure_project", 0, err)
	}

	return &project, nil
}

// attachPaneBinding adds the caller's tmux pane id to a registration call's
// arguments when it is a bare %N address, which is the only shape the server's
// pane-binding contract accepts. Registration works without a pane binding, so
// an empty or non-bare id (a composite session:window:pane label, say) is
// simply omitted rather than failing the call.
func attachPaneBinding(args map[string]interface{}, paneID string) {
	if bare, ok := BareTmuxPaneID(paneID); ok {
		args["pane_id"] = bare
	}
}

// callRegistrationTool runs an identity registration tool call. Servers
// without the pane-binding contract — the Python Agent Mail server, whose
// strict argument validation answers "pane_id: Unexpected keyword argument" —
// reject the whole registration over the optional pane_id, leaving every
// spawned pane without an identity (GH #333). Registration works without a
// pane binding, so such a rejection is retried once without it.
func (c *Client) callRegistrationTool(ctx context.Context, toolName string, args map[string]interface{}) (json.RawMessage, error) {
	result, err := c.callToolWithBusyRetry(ctx, toolName, args, 3*time.Second, busyMaxRetries())
	if err == nil || !paneBindingUnsupported(err) {
		return result, err
	}
	if _, bound := args["pane_id"]; !bound {
		return result, err
	}
	delete(args, "pane_id")
	return c.callToolWithBusyRetry(ctx, toolName, args, 3*time.Second, busyMaxRetries())
}

// paneBindingUnsupported reports whether a registration failed because the
// server does not accept the pane_id argument at all.
func paneBindingUnsupported(err error) bool {
	var rejection *ToolRejectionError
	if !errors.As(err, &rejection) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "pane_id") &&
		(strings.Contains(msg, "unexpected keyword") || strings.Contains(msg, "unexpected argument") ||
			strings.Contains(msg, "unknown argument") || strings.Contains(msg, "unknown field"))
}

// RegisterAgent registers an agent in a project.
func (c *Client) RegisterAgent(ctx context.Context, opts RegisterAgentOptions) (*Agent, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"program":     opts.Program,
		"model":       opts.Model,
	}
	if opts.Name != "" {
		args["name"] = opts.Name
	}
	if opts.TaskDescription != "" {
		args["task_description"] = opts.TaskDescription
	}
	attachPaneBinding(args, opts.PaneID)

	// Re-claim path: include the agent's registration token when known so
	// the server can authenticate us as the existing identity instead of
	// rejecting the call. New registrations leave it empty and the
	// server returns a fresh token in the response.
	c.attachTokenFromField(args, "registration_token", "name")

	result, err := c.callRegistrationTool(ctx, "register_agent", args)
	if err != nil {
		return nil, err
	}

	var agent Agent
	if err := json.Unmarshal(result, &agent); err != nil {
		return nil, NewAPIError("register_agent", 0, err)
	}
	c.rememberRegistrationToken(opts.ProjectKey, &agent)

	return &agent, nil
}

// CreateAgentIdentity creates a new unique agent identity.
func (c *Client) CreateAgentIdentity(ctx context.Context, opts RegisterAgentOptions) (*Agent, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"program":     opts.Program,
		"model":       opts.Model,
	}
	if opts.Name != "" {
		args["name_hint"] = opts.Name
	}
	if opts.TaskDescription != "" {
		args["task_description"] = opts.TaskDescription
	}
	attachPaneBinding(args, opts.PaneID)

	result, err := c.callRegistrationTool(ctx, "create_agent_identity", args)
	if err != nil {
		return nil, err
	}

	var agent Agent
	if err := json.Unmarshal(result, &agent); err != nil {
		return nil, NewAPIError("create_agent_identity", 0, err)
	}
	c.rememberRegistrationToken(opts.ProjectKey, &agent)

	return &agent, nil
}

// Whois retrieves agent profile details.
func (c *Client) Whois(ctx context.Context, projectKey, agentName string, includeRecentCommits bool) (*Agent, error) {
	args := map[string]interface{}{
		"project_key":            projectKey,
		"agent_name":             agentName,
		"include_recent_commits": includeRecentCommits,
	}

	result, err := c.callTool(ctx, "whois", args)
	if err != nil {
		return nil, err
	}

	var agent Agent
	if err := json.Unmarshal(result, &agent); err != nil {
		return nil, NewAPIError("whois", 0, err)
	}

	return &agent, nil
}

// SendMessage sends a message to one or more agents.
func (c *Client) SendMessage(ctx context.Context, opts SendMessageOptions) (*SendResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"sender_name": opts.SenderName,
		"to":          opts.To,
		"subject":     opts.Subject,
		"body_md":     opts.BodyMD,
	}
	if len(opts.CC) > 0 {
		args["cc"] = opts.CC
	}
	if len(opts.BCC) > 0 {
		args["bcc"] = opts.BCC
	}
	if opts.Importance != "" {
		args["importance"] = opts.Importance
	}
	if opts.AckRequired {
		args["ack_required"] = true
	}
	if opts.ThreadID != "" {
		args["thread_id"] = opts.ThreadID
	}
	if opts.ConvertImages != nil {
		args["convert_images"] = *opts.ConvertImages
	}

	c.attachSenderToken(args)
	result, err := c.callTool(ctx, "send_message", args)
	if err != nil {
		return nil, err
	}

	var sendResult SendResult
	if err := json.Unmarshal(result, &sendResult); err != nil {
		return nil, NewAPIError("send_message", 0, err)
	}

	return &sendResult, nil
}

// ReplyMessage replies to an existing message.
func (c *Client) ReplyMessage(ctx context.Context, opts ReplyMessageOptions) (*Message, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"message_id":  opts.MessageID,
		"sender_name": opts.SenderName,
		"body_md":     opts.BodyMD,
	}
	if len(opts.To) > 0 {
		args["to"] = opts.To
	}
	if len(opts.CC) > 0 {
		args["cc"] = opts.CC
	}
	if len(opts.BCC) > 0 {
		args["bcc"] = opts.BCC
	}
	if opts.SubjectPrefix != "" {
		args["subject_prefix"] = opts.SubjectPrefix
	}

	c.attachSenderToken(args)
	result, err := c.callTool(ctx, "reply_message", args)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(result, &msg); err != nil {
		return nil, NewAPIError("reply_message", 0, err)
	}

	return &msg, nil
}

// FetchInbox retrieves inbox messages for an agent.
func (c *Client) FetchInbox(ctx context.Context, opts FetchInboxOptions) ([]InboxMessage, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"agent_name":  opts.AgentName,
	}
	if opts.UrgentOnly {
		args["urgent_only"] = true
	}
	if opts.SinceTS != nil {
		args["since_ts"] = opts.SinceTS.Format("2006-01-02T15:04:05Z07:00")
	}
	if opts.Limit > 0 {
		args["limit"] = opts.Limit
	}
	if opts.IncludeBodies {
		args["include_bodies"] = true
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "fetch_inbox", args)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(result)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, NewAPIError("fetch_inbox", 0, fmt.Errorf("unexpected null response"))
	}

	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(result, &wrapper); err == nil && len(bytes.TrimSpace(wrapper.Result)) > 0 {
		rawMessages := bytes.TrimSpace(wrapper.Result)
		if bytes.Equal(rawMessages, []byte("null")) {
			return nil, NewAPIError("fetch_inbox", 0, fmt.Errorf("unexpected null result field"))
		}

		var messages []InboxMessage
		if err := json.Unmarshal(rawMessages, &messages); err != nil {
			return nil, NewAPIError("fetch_inbox", 0, fmt.Errorf("parsing result field: %w", err))
		}
		return messages, nil
	}

	var messages []InboxMessage
	if err := json.Unmarshal(result, &messages); err != nil {
		return nil, NewAPIError("fetch_inbox", 0, fmt.Errorf("unexpected response shape: %w", err))
	}
	return messages, nil
}

// MarkMessageRead marks a message as read for an agent.
func (c *Client) MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) (*MessageReadResult, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"message_id":  messageID,
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "mark_message_read", args)
	if err != nil {
		return nil, err
	}

	var readResult MessageReadResult
	if err := json.Unmarshal(result, &readResult); err != nil {
		return nil, NewAPIError("mark_message_read", 0, err)
	}

	return &readResult, nil
}

// AcknowledgeMessage acknowledges a message for an agent.
func (c *Client) AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) (*MessageAckResult, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"message_id":  messageID,
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "acknowledge_message", args)
	if err != nil {
		return nil, err
	}

	var ackResult MessageAckResult
	if err := json.Unmarshal(result, &ackResult); err != nil {
		return nil, NewAPIError("acknowledge_message", 0, err)
	}

	return &ackResult, nil
}

// ContactRequestResult contains the result of a contact request.
type ContactRequestResult struct {
	Status    string       `json:"status"` // "pending", "approved", etc.
	Link      *ContactLink `json:"link,omitempty"`
	ExpiresTS *string      `json:"expires_ts,omitempty"`
}

// ContactRespondResult contains the result of responding to a contact request.
type ContactRespondResult struct {
	Status    string       `json:"status,omitempty"`
	Link      *ContactLink `json:"link,omitempty"`
	ExpiresTS *string      `json:"expires_ts,omitempty"`
}

// RequestContact requests contact approval from another agent.
func (c *Client) RequestContact(ctx context.Context, opts RequestContactOptions) (*ContactRequestResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"from_agent":  opts.FromAgent,
		"to_agent":    opts.ToAgent,
	}
	if opts.ToProject != "" {
		args["to_project"] = opts.ToProject
	}
	if opts.Reason != "" {
		args["reason"] = opts.Reason
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}

	c.attachTokenFromField(args, "registration_token", "from_agent")
	result, err := c.callTool(ctx, "request_contact", args)
	if err != nil {
		return nil, err
	}

	var contactResult ContactRequestResult
	if err := json.Unmarshal(result, &contactResult); err != nil {
		return nil, NewAPIError("request_contact", 0, err)
	}

	return &contactResult, nil
}

// RespondContact approves or denies a contact request.
func (c *Client) RespondContact(ctx context.Context, opts RespondContactOptions) (*ContactRespondResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"to_agent":    opts.ToAgent,
		"from_agent":  opts.FromAgent,
		"accept":      opts.Accept,
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}

	c.attachTokenFromField(args, "registration_token", "to_agent")
	result, err := c.callTool(ctx, "respond_contact", args)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return &ContactRespondResult{}, nil
	}

	var respondResult ContactRespondResult
	if err := json.Unmarshal(result, &respondResult); err != nil {
		return nil, NewAPIError("respond_contact", 0, err)
	}

	return &respondResult, nil
}

// ListContacts lists contact links for an agent.
func (c *Client) ListContacts(ctx context.Context, projectKey, agentName string) ([]ContactLink, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "list_contacts", args)
	if err != nil {
		return nil, err
	}

	var contacts []ContactLink
	if err := json.Unmarshal(result, &contacts); err != nil {
		return nil, NewAPIError("list_contacts", 0, err)
	}

	return contacts, nil
}

// SearchMessages searches messages by query.
func (c *Client) SearchMessages(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"query":       opts.Query,
	}
	if opts.Limit > 0 {
		args["limit"] = opts.Limit
	}

	c.attachRegistrationToken(args)
	result, err := c.callToolWithTimeout(ctx, "search_messages", args, LongTimeout)
	if err != nil {
		return nil, err
	}

	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(result, &wrapper); err == nil && len(bytes.TrimSpace(wrapper.Result)) > 0 {
		var results []SearchResult
		if err := json.Unmarshal(wrapper.Result, &results); err != nil {
			return nil, NewAPIError("search_messages", 0, fmt.Errorf("parsing result field: %w", err))
		}
		return results, nil
	}

	var results []SearchResult
	if err := json.Unmarshal(result, &results); err != nil {
		return nil, NewAPIError("search_messages", 0, err)
	}

	return results, nil
}

// SummarizeThread summarizes a message thread using options struct.
func (c *Client) SummarizeThread(ctx context.Context, opts SummarizeThreadOptions) (*ThreadSummaryResponse, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"thread_id":   opts.ThreadID,
	}
	if opts.IncludeExamples != nil {
		args["include_examples"] = *opts.IncludeExamples
	}
	if opts.LLMMode != nil {
		args["llm_mode"] = *opts.LLMMode
	}
	if opts.LLMModel != "" {
		args["llm_model"] = opts.LLMModel
	}

	result, err := c.callToolWithTimeout(ctx, "summarize_thread", args, LongTimeout)
	if err != nil {
		return nil, err
	}

	var wrapped ThreadSummaryResponse
	if err := json.Unmarshal(result, &wrapped); err == nil && (wrapped.ThreadID != "" || len(wrapped.Summary.Participants) > 0 || len(wrapped.Summary.KeyPoints) > 0 || len(wrapped.Summary.ActionItems) > 0 || len(wrapped.Examples) > 0) {
		if wrapped.ThreadID == "" {
			wrapped.ThreadID = wrapped.Summary.ThreadID
		}
		if wrapped.ThreadID == "" {
			wrapped.ThreadID = opts.ThreadID
		}
		if wrapped.Summary.ThreadID == "" {
			wrapped.Summary.ThreadID = wrapped.ThreadID
		}
		return &wrapped, nil
	}

	var summary ThreadSummary
	if err := json.Unmarshal(result, &summary); err != nil {
		return nil, NewAPIError("summarize_thread", 0, err)
	}

	if summary.ThreadID == "" {
		summary.ThreadID = opts.ThreadID
	}

	return &ThreadSummaryResponse{
		ThreadID: summary.ThreadID,
		Summary:  summary,
	}, nil
}

// ReleaseReservations releases file path reservations.
func (c *Client) ReleaseReservations(ctx context.Context, projectKey, agentName string, paths []string, ids []int) (*ReleaseReservationsResult, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
	}
	if len(paths) > 0 {
		args["paths"] = paths
	}
	if len(ids) > 0 {
		args["file_reservation_ids"] = ids
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "release_file_reservations", args)
	if err != nil {
		return nil, err
	}

	var releaseResult ReleaseReservationsResult
	if err := json.Unmarshal(result, &releaseResult); err != nil {
		return nil, NewAPIError("release_file_reservations", 0, err)
	}

	return &releaseResult, nil
}

// CleanupPaneIdentities invokes the cleanup_pane_identities MCP tool, which
// removes stale per-pane identity files for tmux panes that no longer exist
// (the server checks live tmux panes itself and no-ops when tmux is down).
// Pass an empty projectKey to clean up across all projects.
func (c *Client) CleanupPaneIdentities(ctx context.Context, projectKey string) (*CleanupPaneIdentitiesResult, error) {
	args := map[string]interface{}{}
	if projectKey != "" {
		args["project_key"] = projectKey
	}

	result, err := c.callTool(ctx, "cleanup_pane_identities", args)
	if err != nil {
		return nil, err
	}

	var cleanupResult CleanupPaneIdentitiesResult
	if err := json.Unmarshal(result, &cleanupResult); err != nil {
		return nil, NewAPIError("cleanup_pane_identities", 0, err)
	}

	return &cleanupResult, nil
}

// RenewReservations extends the TTL of existing reservations using options struct.
func (c *Client) RenewReservations(ctx context.Context, opts RenewReservationsOptions) (*RenewReservationsResult, error) {
	args := map[string]interface{}{
		"project_key":    opts.ProjectKey,
		"agent_name":     opts.AgentName,
		"extend_seconds": opts.ExtendSeconds,
	}
	if len(opts.ReservationIDs) > 0 {
		args["file_reservation_ids"] = opts.ReservationIDs
	}
	if len(opts.Paths) > 0 {
		args["paths"] = opts.Paths
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "renew_file_reservations", args)
	if err != nil {
		return nil, err
	}

	var renewResult RenewReservationsResult
	if err := json.Unmarshal(result, &renewResult); err != nil {
		return nil, NewAPIError("renew_file_reservations", 0, err)
	}

	return &renewResult, nil
}

// ListReservations lists active file reservations for a project (optionally filtered by agent).
// If the Agent Mail server does not support this tool, callers will receive an error rather
// than an empty slice so the CLI can surface the limitation instead of misreporting "no locks".
func (c *Client) ListReservations(ctx context.Context, projectKey, agentName string, allAgents bool) ([]FileReservation, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reservation listing requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, LongTimeout)
	defer cancel()
	// Preferred: use the MCP resource view.
	// Resource URI: resource://file_reservations/{slug}?active_only=true
	//
	// The server accepts either project slug or human_key in {slug}; we pass projectKey
	// (usually an absolute path) URL-escaped for compatibility.
	uri := activeReservationsURI(projectKey)

	result, err := c.ReadResource(ctx, uri)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Fallback for older Agent Mail deployments: try legacy tools.
		args := map[string]interface{}{
			"project_key": projectKey,
		}
		if agentName != "" {
			args["agent_name"] = agentName
		}
		if allAgents {
			args["all_agents"] = true
		}

		c.attachRegistrationToken(args)
		toolResult, toolErr := c.callTool(ctx, "list_file_reservations", args)
		if toolErr != nil {
			fallbackResult, fallbackErr := c.callTool(ctx, "list_reservations", args)
			if fallbackErr != nil {
				return nil, err // return original resource error to make diagnosis clear
			}
			toolResult = fallbackResult
		}

		var reservations []FileReservation
		if unmarshalErr := json.Unmarshal(toolResult, &reservations); unmarshalErr != nil {
			return nil, NewAPIError("list_file_reservations", 0, unmarshalErr)
		}

		// Some legacy tool implementations may ignore agent_name filtering. Mirror the
		// resource path behavior and defensively filter client-side when requested.
		if agentName != "" && !allAgents {
			filtered := make([]FileReservation, 0, len(reservations))
			for _, r := range reservations {
				if r.AgentName == agentName {
					filtered = append(filtered, r)
				}
			}
			reservations = filtered
		}
		return reservations, nil
	}

	return c.readReservationPages(ctx, projectKey, agentName, allAgents, uri, result)
}

// StartSession is a macro that starts a project session (ensure project, register agent, fetch inbox).
func (c *Client) StartSession(ctx context.Context, projectKey, program, model, taskDescription string) (*SessionStartResult, error) {
	args := map[string]interface{}{
		"human_key": projectKey,
		"program":   program,
		"model":     model,
	}
	if taskDescription != "" {
		args["task_description"] = taskDescription
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "macro_start_session", args)
	if err != nil {
		return nil, err
	}

	var sessionResult SessionStartResult
	if err := json.Unmarshal(result, &sessionResult); err != nil {
		return nil, NewAPIError("macro_start_session", 0, err)
	}

	return &sessionResult, nil
}

// PrepareThread aligns an agent with an existing thread, optionally summarizing the thread.
// This is a macro that ensures registration, summarizes the thread, and fetches recent inbox context.
func (c *Client) PrepareThread(ctx context.Context, opts PrepareThreadOptions) (*PrepareThreadResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"thread_id":   opts.ThreadID,
		"program":     opts.Program,
		"model":       opts.Model,
	}

	if opts.AgentName != "" {
		args["agent_name"] = opts.AgentName
	}
	if opts.TaskDescription != "" {
		args["task_description"] = opts.TaskDescription
	}
	if opts.LLMModel != "" {
		args["llm_model"] = opts.LLMModel
	}
	if opts.InboxLimit > 0 {
		args["inbox_limit"] = opts.InboxLimit
	}

	// Only send boolean options when explicitly set (non-nil).
	// Server defaults: include_examples=true, include_inbox_bodies=false, llm_mode=true, register_if_missing=true
	if opts.IncludeExamples != nil {
		args["include_examples"] = *opts.IncludeExamples
	}
	if opts.IncludeInboxBodies != nil {
		args["include_inbox_bodies"] = *opts.IncludeInboxBodies
	}
	if opts.LLMMode != nil {
		args["llm_mode"] = *opts.LLMMode
	}
	if opts.RegisterIfMissing != nil {
		args["register_if_missing"] = *opts.RegisterIfMissing
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "macro_prepare_thread", args)
	if err != nil {
		return nil, err
	}

	var threadResult PrepareThreadResult
	if err := json.Unmarshal(result, &threadResult); err != nil {
		return nil, NewAPIError("macro_prepare_thread", 0, err)
	}

	return &threadResult, nil
}

// ContactHandshake requests contact permissions and optionally auto-approves and sends a welcome message.
func (c *Client) ContactHandshake(ctx context.Context, opts ContactHandshakeOptions) (*ContactHandshakeResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
	}

	if opts.AgentName != "" {
		args["agent_name"] = opts.AgentName
	}
	if opts.ToAgent != "" {
		args["to_agent"] = opts.ToAgent
	}
	if opts.ToProject != "" {
		args["to_project"] = opts.ToProject
	}
	if opts.Reason != "" {
		args["reason"] = opts.Reason
	}
	if opts.Program != "" {
		args["program"] = opts.Program
	}
	if opts.Model != "" {
		args["model"] = opts.Model
	}
	if opts.TaskDescription != "" {
		args["task_description"] = opts.TaskDescription
	}
	if opts.WelcomeSubject != "" {
		args["welcome_subject"] = opts.WelcomeSubject
	}
	if opts.WelcomeBody != "" {
		args["welcome_body"] = opts.WelcomeBody
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}

	args["auto_accept"] = opts.AutoAccept
	args["register_if_missing"] = true // Always try to register

	c.attachTokenFromField(args, "requester_registration_token", "agent_name")
	c.attachTokenFromField(args, "target_registration_token", "to_agent")
	result, err := c.callTool(ctx, "macro_contact_handshake", args)
	if err != nil {
		return nil, err
	}

	var handshakeResult ContactHandshakeResult
	if err := json.Unmarshal(result, &handshakeResult); err != nil {
		return nil, NewAPIError("macro_contact_handshake", 0, err)
	}

	return &handshakeResult, nil
}

// SendOverseerMessage uses the server's audited human intervention route.
// Only a missing route permits a legacy MCP send, using an already provisioned
// identity. Never register the reserved name, downgrade a rejected intervention,
// or retry an ambiguous send (which could duplicate delivery).
func (c *Client) SendOverseerMessage(ctx context.Context, opts OverseerMessageOptions) (*OverseerSendResult, error) {
	if opts.Reason == "" {
		opts.Reason = DefaultOverseerReason
	}
	opts.Reason = strings.TrimSpace(opts.Reason)
	if len(opts.Recipients) == 0 || len(opts.Recipients) > 100 {
		return nil, NewAPIError("overseer_send", 0, fmt.Errorf("%w: choose 1 to 100 explicit recipients", ErrInvalidRequest))
	}
	for _, recipient := range opts.Recipients {
		if strings.TrimSpace(recipient) == "" {
			return nil, NewAPIError("overseer_send", 0, fmt.Errorf("%w: recipient cannot be empty", ErrInvalidRequest))
		}
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"subject", opts.Subject, 200},
		{"message body", opts.BodyMD, 49_600},
		{"intervention reason", opts.Reason, 500},
	} {
		if strings.TrimSpace(field.value) == "" || !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.limit {
			return nil, NewAPIError("overseer_send", 0, fmt.Errorf("%w: %s must contain 1 to %d characters", ErrInvalidRequest, field.name, field.limit))
		}
	}
	if opts.ProjectSlug == "" && opts.ProjectKey != "" {
		project, err := c.EnsureProject(ctx, opts.ProjectKey)
		if err != nil {
			return nil, err
		}
		opts.ProjectSlug = project.Slug
		if opts.ProjectSlug == "" {
			opts.ProjectSlug = ProjectSlugFromPath(opts.ProjectKey)
		}
	}
	result, err := c.sendOverseerMessageHTTP(ctx, opts)
	if err == nil || !errors.Is(err, ErrNotImplemented) || opts.ProjectKey == "" {
		return result, err
	}

	body := HumanOverseerPreamble + "Human intervention reason: " + opts.Reason + "\n\n" + opts.BodyMD
	send, err := c.SendMessage(ctx, SendMessageOptions{
		ProjectKey: opts.ProjectKey,
		SenderName: HumanOverseerAgentName,
		To:         opts.Recipients,
		Subject:    opts.Subject,
		BodyMD:     body,
		Importance: "high",
		ThreadID:   opts.ThreadID,
	})
	if err != nil {
		return nil, fmt.Errorf("legacy overseer send requires an existing authorized HumanOverseer identity; use a server with the overseer intervention route: %w", err)
	}

	// Map send_message's SendResult (per-recipient deliveries) onto
	// OverseerSendResult so the CLI's output shape is unchanged.
	result = &OverseerSendResult{
		Success:    true,
		Recipients: opts.Recipients,
	}
	for _, d := range send.Deliveries {
		if d.Payload == nil {
			continue
		}
		if result.MessageID == 0 {
			result.MessageID = d.Payload.ID
		}
		if !d.Payload.CreatedTS.IsZero() && result.SentAt.IsZero() {
			result.SentAt = d.Payload.CreatedTS
		}
	}
	return result, nil
}

// sendOverseerMessageHTTP leaves identity provisioning, policy bypass, preamble,
// importance, and audit recording to the server's designated intervention route.
func (c *Client) sendOverseerMessageHTTP(ctx context.Context, opts OverseerMessageOptions) (*OverseerSendResult, error) {
	if opts.ProjectSlug == "" {
		return nil, NewAPIError("overseer_send", 0, fmt.Errorf("project_slug required for HTTP fallback path"))
	}

	// Build request body
	reqBody := map[string]interface{}{
		"intent":           "human_overseer_send",
		"reason":           opts.Reason,
		"confirm_multiple": len(opts.Recipients) > 1,
		"recipients":       opts.Recipients,
		"subject":          opts.Subject,
		"body_md":          opts.BodyMD,
	}
	if opts.ThreadID != "" {
		reqBody["thread_id"] = opts.ThreadID
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
	}

	// Build URL: /mail/{project_slug}/overseer/send
	httpBaseURL := c.httpBaseURL()
	// Encode path segments to ensure valid URL
	url := fmt.Sprintf("%s/mail/%s/overseer/send", httpBaseURL, url.PathEscape(opts.ProjectSlug))

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, NewAPIError("overseer_send", 0, ErrTimeout)
		}
		return nil, NewAPIError("overseer_send", 0, ErrServerUnavailable)
	}
	defer resp.Body.Close()

	// Read response body (limit to 10MB to prevent DoS/OOM)
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Detail json.RawMessage `json:"detail"`
			Error  json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errResp)
		raw := errResp.Error
		if len(raw) == 0 {
			raw = errResp.Detail
		}
		var message string
		if len(raw) > 0 && json.Unmarshal(raw, &message) != nil {
			var structured struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(raw, &structured) == nil && structured.Message != "" {
				message = structured.Message
			} else {
				message = string(raw)
			}
		}
		cause := error(nil)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			cause = ErrUnauthorized
		case http.StatusBadRequest:
			cause = ErrInvalidRequest
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			// A structured rejection may mean an unknown project or a policy
			// denial, rather than an absent route. Do not downgrade it.
			if len(errResp.Error) == 0 && len(errResp.Detail) == 0 {
				cause = ErrNotImplemented
			}
		}
		if message == "" {
			message = strings.ToLower(http.StatusText(resp.StatusCode))
		}
		if cause != nil {
			return nil, NewAPIError("overseer_send", resp.StatusCode, fmt.Errorf("%w: %s", cause, message))
		}
		return nil, NewAPIError("overseer_send", resp.StatusCode, errors.New(message))
	}

	// Parse response
	var result OverseerSendResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
	}
	if !result.Success || result.MessageID <= 0 {
		return nil, NewAPIError("overseer_send", 0, fmt.Errorf("server did not confirm overseer delivery"))
	}
	if len(result.Recipients) == 0 {
		result.Recipients = opts.Recipients // Older HTTP receipts omit this field.
	}

	return &result, nil
}

// ListProjectAgents lists all agents registered in a project.
// This is useful for discovering recipients for overseer messages.
func (c *Client) ListProjectAgents(ctx context.Context, projectKey string) ([]Agent, error) {
	// Use the MCP resource to list agents
	// Resource URI: resource://agents/{project_key}
	uri := fmt.Sprintf("resource://agents/%s", url.PathEscape(projectKey))

	result, err := c.ReadResource(ctx, uri)
	if err != nil {
		return nil, err
	}

	// MCP Resources Read result structure:
	// { "contents": [ { "uri": "...", "mimeType": "...", "text": "..." } ] }
	var resourceResp struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}

	if err := json.Unmarshal(result, &resourceResp); err != nil {
		return nil, NewAPIError("list_agents", 0, err)
	}

	if len(resourceResp.Contents) == 0 {
		return []Agent{}, nil
	}

	// Try wrapped format first: {"agents": [...]}
	var wrapped struct {
		Agents []Agent `json:"agents"`
	}
	text := resourceResp.Contents[0].Text
	if err := json.Unmarshal([]byte(text), &wrapped); err == nil && wrapped.Agents != nil {
		return wrapped.Agents, nil
	}

	// Fall back to raw array format: [...]
	var agents []Agent
	if err := json.Unmarshal([]byte(text), &agents); err != nil {
		return nil, NewAPIError("list_agents", 0, err)
	}

	return agents, nil
}

// InstallPrecommitGuard installs the Agent Mail pre-commit guard for a repo.
func (c *Client) InstallPrecommitGuard(ctx context.Context, projectKey, repoPath string) error {
	args := map[string]interface{}{
		"project_key":    projectKey,
		"code_repo_path": repoPath,
	}

	_, err := c.callTool(ctx, "install_precommit_guard", args)
	return err
}

// UninstallPrecommitGuard removes the Agent Mail pre-commit guard from a repo.
func (c *Client) UninstallPrecommitGuard(ctx context.Context, repoPath string) error {
	args := map[string]interface{}{
		"code_repo_path": repoPath,
	}

	_, err := c.callTool(ctx, "uninstall_precommit_guard", args)
	return err
}

// GetMessage retrieves a specific message by ID.
func (c *Client) GetMessage(ctx context.Context, projectKey string, messageID int) (*Message, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"message_id":  messageID,
	}

	result, err := c.callTool(ctx, "get_message", args)
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(result, &msg); err != nil {
		return nil, NewAPIError("get_message", 0, err)
	}

	return &msg, nil
}

// SetContactPolicy sets the contact policy for an agent.
func (c *Client) SetContactPolicy(ctx context.Context, projectKey, agentName, policy string) (*ContactPolicyResult, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"policy":      policy,
	}

	c.attachRegistrationToken(args)
	result, err := c.callTool(ctx, "set_contact_policy", args)
	if err != nil {
		return nil, err
	}

	trimmed := bytes.TrimSpace(result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return &ContactPolicyResult{}, nil
	}

	var policyResult ContactPolicyResult
	if err := json.Unmarshal(result, &policyResult); err != nil {
		return nil, NewAPIError("set_contact_policy", 0, err)
	}

	return &policyResult, nil
}

// CheckConflicts checks for file reservation conflicts on the given paths.
func (c *Client) CheckConflicts(ctx context.Context, projectKey string, paths []string) ([]ReservationConflict, error) {
	reservations, err := c.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	conflicts := make([]ReservationConflict, 0, len(paths))
	for _, target := range paths {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}

		matched := make([]FileReservation, 0, len(reservations))
		for _, reservation := range reservations {
			if !reservationActiveAt(reservation, now) {
				continue
			}
			if reservationPatternsOverlap(target, reservation.PathPattern) {
				matched = append(matched, reservation)
			}
		}

		holderSet := make(map[string]struct{})
		for i := 0; i < len(matched); i++ {
			for j := i + 1; j < len(matched); j++ {
				if !reservationsConflict(matched[i], matched[j]) {
					continue
				}
				if matched[i].AgentName != "" {
					holderSet[matched[i].AgentName] = struct{}{}
				}
				if matched[j].AgentName != "" {
					holderSet[matched[j].AgentName] = struct{}{}
				}
			}
		}

		if len(holderSet) == 0 {
			continue
		}

		holders := make([]string, 0, len(holderSet))
		for holder := range holderSet {
			holders = append(holders, holder)
		}
		sort.Strings(holders)

		conflicts = append(conflicts, ReservationConflict{
			Path:    target,
			Holders: holders,
		})
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Path < conflicts[j].Path
	})

	return conflicts, nil
}

func reservationActiveAt(reservation FileReservation, now time.Time) bool {
	if reservation.ReleasedTS != nil {
		return false
	}
	// Missing expiry evidence cannot clear an exclusive lease. This also
	// protects legacy listing responses, which may omit expires_ts.
	if reservation.ExpiresTS.Time.IsZero() {
		return true
	}
	return !now.After(reservation.ExpiresTS.Time)
}

func reservationsConflict(a, b FileReservation) bool {
	if a.AgentName == b.AgentName {
		return false
	}
	if !a.Exclusive && !b.Exclusive {
		return false
	}
	return reservationPatternsOverlap(a.PathPattern, b.PathPattern)
}

func reservationPatternsOverlap(a, b string) bool {
	return reservationpath.MayOverlap(a, b)
}

func matchesReservationPattern(path, pattern string) bool {
	return reservationpath.Matches(pattern, path)
}

// GetReservation retrieves a specific file reservation by ID.
func (c *Client) GetReservation(ctx context.Context, projectKey string, reservationID int) (*FileReservation, error) {
	// The MCP server doesn't have a direct get_reservation tool,
	// so we list all and filter. This is a limitation we can improve later.
	reservations, err := c.ListReservations(ctx, projectKey, "", true)
	if err != nil {
		return nil, err
	}

	for i := range reservations {
		if reservations[i].ID == reservationID {
			return &reservations[i], nil
		}
	}

	return nil, fmt.Errorf("%w: reservation %d not found", ErrNotFound, reservationID)
}

// RenewReservationsWithOptions is an alias for RenewReservations for backward compatibility.
func (c *Client) RenewReservationsWithOptions(ctx context.Context, opts RenewReservationsOptions) (*RenewReservationsResult, error) {
	return c.RenewReservations(ctx, opts)
}

// ListAgents is an alias for ListProjectAgents for convenience.
func (c *Client) ListAgents(ctx context.Context, projectKey string) ([]Agent, error) {
	return c.ListProjectAgents(ctx, projectKey)
}

// ForceReleaseReservation forcibly releases a stale reservation held by another agent.
// The tool validates inactivity heuristics before allowing the release.
// Optionally notifies the previous holder about the forced release.
func (c *Client) ForceReleaseReservation(ctx context.Context, opts ForceReleaseOptions) (*ForceReleaseResult, error) {
	args := map[string]interface{}{
		"project_key":         opts.ProjectKey,
		"agent_name":          opts.AgentName,
		"file_reservation_id": opts.ReservationID,
	}
	if opts.Note != "" {
		args["note"] = opts.Note
	}
	args["notify_previous"] = opts.NotifyPrevious

	result, err := c.callTool(ctx, "force_release_file_reservation", args)
	if err != nil {
		return nil, err
	}

	var releaseResult ForceReleaseResult
	if err := json.Unmarshal(result, &releaseResult); err != nil {
		return nil, NewAPIError("force_release_file_reservation", 0, err)
	}

	return &releaseResult, nil
}
