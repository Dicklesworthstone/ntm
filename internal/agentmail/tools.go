package agentmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// EnsureProject ensures a project exists for the given path.
func (c *Client) EnsureProject(ctx context.Context, projectKey string) (*Project, error) {
	args := map[string]interface{}{
		"human_key": projectKey,
	}

	result, err := c.callTool(ctx, "ensure_project", args)
	if err != nil {
		return nil, err
	}

	var project Project
	if err := json.Unmarshal(result, &project); err != nil {
		return nil, NewAPIError("ensure_project", 0, err)
	}

	return &project, nil
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

	result, err := c.callTool(ctx, "register_agent", args)
	if err != nil {
		return nil, err
	}

	var agent Agent
	if err := json.Unmarshal(result, &agent); err != nil {
		return nil, NewAPIError("register_agent", 0, err)
	}

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

	result, err := c.callTool(ctx, "create_agent_identity", args)
	if err != nil {
		return nil, err
	}

	var agent Agent
	if err := json.Unmarshal(result, &agent); err != nil {
		return nil, NewAPIError("create_agent_identity", 0, err)
	}

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

	result, err := c.callTool(ctx, "fetch_inbox", args)
	if err != nil {
		return nil, err
	}

	// The result is wrapped in a "result" field
	var wrapper struct {
		Result []InboxMessage `json:"result"`
	}
	if err := json.Unmarshal(result, &wrapper); err != nil {
		// Try direct unmarshal
		var messages []InboxMessage
		if err := json.Unmarshal(result, &messages); err != nil {
			return nil, NewAPIError("fetch_inbox", 0, err)
		}
		return messages, nil
	}

	return wrapper.Result, nil
}

// MarkMessageRead marks a message as read for an agent.
func (c *Client) MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) error {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"message_id":  messageID,
	}

	_, err := c.callTool(ctx, "mark_message_read", args)
	return err
}

// AcknowledgeMessage acknowledges a message for an agent.
func (c *Client) AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) error {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"message_id":  messageID,
	}

	_, err := c.callTool(ctx, "acknowledge_message", args)
	return err
}

// ContactRequestResult contains the result of a contact request.
type ContactRequestResult struct {
	Status    string       `json:"status"` // "pending", "approved", etc.
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
func (c *Client) RespondContact(ctx context.Context, opts RespondContactOptions) error {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"to_agent":    opts.ToAgent,
		"from_agent":  opts.FromAgent,
		"accept":      opts.Accept,
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}

	_, err := c.callTool(ctx, "respond_contact", args)
	return err
}

// ListContacts lists contact links for an agent.
func (c *Client) ListContacts(ctx context.Context, projectKey, agentName string) ([]ContactLink, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
	}

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

	result, err := c.callToolWithTimeout(ctx, "search_messages", args, LongTimeout)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal(result, &results); err != nil {
		return nil, NewAPIError("search_messages", 0, err)
	}

	return results, nil
}

// SummarizeThread summarizes a message thread using options struct.
func (c *Client) SummarizeThread(ctx context.Context, opts SummarizeThreadOptions) (*ThreadSummary, error) {
	args := map[string]interface{}{
		"project_key":      opts.ProjectKey,
		"thread_id":        opts.ThreadID,
		"include_examples": opts.IncludeExamples,
	}
	if opts.LLMMode {
		args["llm_mode"] = opts.LLMMode
	}
	if opts.LLMModel != "" {
		args["llm_model"] = opts.LLMModel
	}

	result, err := c.callToolWithTimeout(ctx, "summarize_thread", args, LongTimeout)
	if err != nil {
		return nil, err
	}

	var summary ThreadSummary
	if err := json.Unmarshal(result, &summary); err != nil {
		return nil, NewAPIError("summarize_thread", 0, err)
	}

	return &summary, nil
}

// ReservePaths requests file path reservations.
func (c *Client) ReservePaths(ctx context.Context, opts FileReservationOptions) (*ReservationResult, error) {
	args := map[string]interface{}{
		"project_key": opts.ProjectKey,
		"agent_name":  opts.AgentName,
		"paths":       opts.Paths,
	}
	if opts.TTLSeconds > 0 {
		args["ttl_seconds"] = opts.TTLSeconds
	}
	if opts.Exclusive {
		args["exclusive"] = true
	}
	if opts.Reason != "" {
		args["reason"] = opts.Reason
	}

	result, err := c.callTool(ctx, "file_reservation_paths", args)
	if err != nil {
		return nil, err
	}

	var reservationResult ReservationResult
	if err := json.Unmarshal(result, &reservationResult); err != nil {
		return nil, NewAPIError("file_reservation_paths", 0, err)
	}

	// Check for conflicts
	if len(reservationResult.Conflicts) > 0 {
		return &reservationResult, fmt.Errorf("%w: %d conflicts", ErrReservationConflict, len(reservationResult.Conflicts))
	}

	return &reservationResult, nil
}

// ReleaseReservations releases file path reservations.
func (c *Client) ReleaseReservations(ctx context.Context, projectKey, agentName string, paths []string, ids []int) error {
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

	_, err := c.callTool(ctx, "release_file_reservations", args)
	return err
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
	args := map[string]interface{}{
		"project_key": projectKey,
	}
	if agentName != "" {
		args["agent_name"] = agentName
	}
	if allAgents {
		args["all_agents"] = true
	}

	// Primary tool name
	result, err := c.callTool(ctx, "list_file_reservations", args)
	if err != nil {
		// Some deployments may expose legacy name; try a fallback once.
		fallbackResult, fallbackErr := c.callTool(ctx, "list_reservations", args)
		if fallbackErr != nil {
			return nil, err // return original error to make diagnosis clear
		}
		result = fallbackResult
	}

	var reservations []FileReservation
	if err := json.Unmarshal(result, &reservations); err != nil {
		return nil, NewAPIError("list_file_reservations", 0, err)
	}
	return reservations, nil
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

// SendOverseerMessage sends a Human Overseer message via the HTTP REST API.
// This bypasses contact policies and auto-injects a preamble telling agents
// to prioritize the human's instructions. Messages are automatically marked
// as high importance.
//
// Note: This uses the HTTP REST API, not the MCP tools API, because the
// overseer functionality is specifically designed for human operators.
func (c *Client) SendOverseerMessage(ctx context.Context, opts OverseerMessageOptions) (*OverseerSendResult, error) {
	// Build request body
	reqBody := map[string]interface{}{
		"recipients": opts.Recipients,
		"subject":    opts.Subject,
		"body_md":    opts.BodyMD,
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

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
	}

	// Check HTTP status
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, NewAPIError("overseer_send", resp.StatusCode, ErrUnauthorized)
	}
	if resp.StatusCode == http.StatusBadRequest {
		// Try to extract error message from response
		var errResp struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Detail != "" {
			return nil, NewAPIError("overseer_send", resp.StatusCode, fmt.Errorf("%s", errResp.Detail))
		}
		return nil, NewAPIError("overseer_send", resp.StatusCode, fmt.Errorf("bad request"))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, NewAPIError("overseer_send", resp.StatusCode, fmt.Errorf("unexpected status: %s", resp.Status))
	}

	// Parse response
	var result OverseerSendResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, NewAPIError("overseer_send", 0, err)
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

	// The server may return either:
	// 1. A wrapped response: {"project": {...}, "agents": [...]}
	// 2. A raw array: [...]
	// Try wrapped format first, fall back to raw array.
	var wrappedResp struct {
		Agents []Agent `json:"agents"`
	}
	text := resourceResp.Contents[0].Text
	if err := json.Unmarshal([]byte(text), &wrappedResp); err == nil && wrappedResp.Agents != nil {
		return wrappedResp.Agents, nil
	}

	// Fall back to raw array format
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
func (c *Client) SetContactPolicy(ctx context.Context, projectKey, agentName, policy string) error {
	args := map[string]interface{}{
		"project_key": projectKey,
		"agent_name":  agentName,
		"policy":      policy,
	}

	_, err := c.callTool(ctx, "set_contact_policy", args)
	return err
}

// CheckConflicts checks for file reservation conflicts on the given paths.
func (c *Client) CheckConflicts(ctx context.Context, projectKey string, paths []string) ([]ReservationConflict, error) {
	args := map[string]interface{}{
		"project_key": projectKey,
		"paths":       paths,
	}

	result, err := c.callTool(ctx, "file_reservation_paths", args)
	if err != nil {
		return nil, err
	}

	var reservationResult ReservationResult
	if err := json.Unmarshal(result, &reservationResult); err != nil {
		return nil, NewAPIError("file_reservation_paths", 0, err)
	}

	return reservationResult.Conflicts, nil
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

	return nil, fmt.Errorf("reservation %d not found", reservationID)
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
