package robot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// MailCheckOutput represents the response from --robot-mail-check
type MailCheckOutput struct {
	RobotResponse
	Project       string               `json:"project"`
	Agent         string               `json:"agent,omitempty"`
	Filters       MailCheckFilters     `json:"filters"`
	Unread        int                  `json:"unread"`
	Urgent        int                  `json:"urgent"`
	TotalMessages int                  `json:"total_messages"`
	Offset        int                  `json:"offset"`
	Count         int                  `json:"count"`
	Messages      []MailCheckMessage   `json:"messages"`
	HasMore       bool                 `json:"has_more"`
	AgentHints    *MailCheckAgentHints `json:"_agent_hints,omitempty"`
}

// MailCheckFilters shows active filters in the response
type MailCheckFilters struct {
	Status     string  `json:"status"` // all, read, unread
	UrgentOnly bool    `json:"urgent_only"`
	Thread     *string `json:"thread"`
	Since      *string `json:"since"`
	Until      *string `json:"until"`
}

// MailCheckMessage represents a message in the mail check output
type MailCheckMessage struct {
	ID         int     `json:"id"`
	From       string  `json:"from"`
	To         string  `json:"to"`
	Subject    string  `json:"subject"`
	Preview    string  `json:"preview,omitempty"`
	Body       *string `json:"body"` // null unless --include-bodies
	ThreadID   *string `json:"thread_id,omitempty"`
	Importance string  `json:"importance"`
	Read       bool    `json:"read"`
	Timestamp  string  `json:"timestamp"`
}

// MailCheckAgentHints provides actionable suggestions for AI agents
type MailCheckAgentHints struct {
	SuggestedAction string  `json:"suggested_action,omitempty"`
	UnreadSummary   string  `json:"unread_summary,omitempty"`
	NextOffset      *int    `json:"next_offset,omitempty"`
	PagesRemaining  *int    `json:"pages_remaining,omitempty"`
	OldestUnread    *string `json:"oldest_unread,omitempty"`
}

// MailCheckOptions configures the GetMailCheck operation
type MailCheckOptions struct {
	Project      string
	Agent        string
	Thread       string
	Status       string // all, read, unread
	IncludeBodies bool
	UrgentOnly   bool
	Verbose      bool
	Limit        int
	Offset       int
	Since        string // YYYY-MM-DD
	Until        string // YYYY-MM-DD
}

// Validate checks that options are valid
func (o *MailCheckOptions) Validate() error {
	if o.Project == "" {
		return fmt.Errorf("--project is required")
	}

	// Validate status value
	if o.Status != "" && o.Status != "all" && o.Status != "read" && o.Status != "unread" {
		return fmt.Errorf("invalid --status value %q: must be read, unread, or all", o.Status)
	}

	// Validate date range
	if o.Since != "" && o.Until != "" {
		sinceDate, err := time.Parse("2006-01-02", o.Since)
		if err != nil {
			return fmt.Errorf("invalid --since date format: expected YYYY-MM-DD")
		}
		untilDate, err := time.Parse("2006-01-02", o.Until)
		if err != nil {
			return fmt.Errorf("invalid --until date format: expected YYYY-MM-DD")
		}
		if untilDate.Before(sinceDate) {
			return fmt.Errorf("--until date cannot be before --since date")
		}
	}

	return nil
}

// GetMailCheck checks agent mail inbox and returns the results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMailCheck(opts MailCheckOptions) (*MailCheckOutput, error) {
	// Validate options first
	if err := opts.Validate(); err != nil {
		return &MailCheckOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Check --project and filter flags"),
			Project:       opts.Project,
			Messages:      []MailCheckMessage{},
			Filters: MailCheckFilters{
				Status:     opts.Status,
				UrgentOnly: opts.UrgentOnly,
			},
		}, nil
	}

	// Create Agent Mail client
	client := agentmail.NewClient()

	// Check availability
	if !client.IsAvailable() {
		return &MailCheckOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("Agent Mail not available"),
				ErrCodeDependencyMissing,
				"Start Agent Mail server: mcp-agent-mail or ensure it's running",
			),
			Project:  opts.Project,
			Messages: []MailCheckMessage{},
			Filters: MailCheckFilters{
				Status:     opts.Status,
				UrgentOnly: opts.UrgentOnly,
			},
		}, nil
	}

	// Build fetch options
	fetchOpts := agentmail.FetchInboxOptions{
		ProjectKey:    opts.Project,
		AgentName:     opts.Agent,
		UrgentOnly:    opts.UrgentOnly,
		IncludeBodies: opts.IncludeBodies,
		Limit:         opts.Limit,
	}

	// Set default limit if not specified
	if fetchOpts.Limit <= 0 {
		fetchOpts.Limit = 20
	}

	// Parse since date
	if opts.Since != "" {
		sinceDate, _ := time.Parse("2006-01-02", opts.Since)
		fetchOpts.SinceTS = &sinceDate
	}

	// Fetch messages
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages, err := client.FetchInbox(ctx, fetchOpts)
	if err != nil {
		return &MailCheckOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to fetch inbox"),
			Project:       opts.Project,
			Agent:         opts.Agent,
			Messages:      []MailCheckMessage{},
			Filters: MailCheckFilters{
				Status:     opts.Status,
				UrgentOnly: opts.UrgentOnly,
			},
		}, nil
	}

	// Apply additional filtering not supported by agentmail client
	filtered := messages

	// Filter by thread
	if opts.Thread != "" {
		var threadFiltered []agentmail.InboxMessage
		for _, msg := range filtered {
			if msg.ThreadID != nil && *msg.ThreadID == opts.Thread {
				threadFiltered = append(threadFiltered, msg)
			}
		}
		filtered = threadFiltered
	}

	// Filter by read status
	if opts.Status == "read" {
		var statusFiltered []agentmail.InboxMessage
		for _, msg := range filtered {
			if msg.ReadAt != nil {
				statusFiltered = append(statusFiltered, msg)
			}
		}
		filtered = statusFiltered
	} else if opts.Status == "unread" {
		var statusFiltered []agentmail.InboxMessage
		for _, msg := range filtered {
			if msg.ReadAt == nil {
				statusFiltered = append(statusFiltered, msg)
			}
		}
		filtered = statusFiltered
	}

	// Filter by until date
	if opts.Until != "" {
		untilDate, _ := time.Parse("2006-01-02", opts.Until)
		untilDate = untilDate.Add(24 * time.Hour) // Include entire day
		var dateFiltered []agentmail.InboxMessage
		for _, msg := range filtered {
			if msg.CreatedTS.Before(untilDate) {
				dateFiltered = append(dateFiltered, msg)
			}
		}
		filtered = dateFiltered
	}

	// Calculate counts before pagination
	totalMessages := len(filtered)
	unreadCount := 0
	urgentCount := 0
	var oldestUnread *time.Time

	for _, msg := range filtered {
		if msg.ReadAt == nil {
			unreadCount++
			if oldestUnread == nil || msg.CreatedTS.Before(*oldestUnread) {
				t := msg.CreatedTS.Time
				oldestUnread = &t
			}
		}
		if msg.Importance == "high" || msg.Importance == "urgent" {
			urgentCount++
		}
	}

	// Apply offset
	if opts.Offset > 0 && opts.Offset < len(filtered) {
		filtered = filtered[opts.Offset:]
	} else if opts.Offset >= len(filtered) {
		filtered = nil
	}

	// Apply limit
	hasMore := false
	if len(filtered) > opts.Limit {
		hasMore = true
		filtered = filtered[:opts.Limit]
	}

	// Convert to output format
	outputMsgs := make([]MailCheckMessage, len(filtered))
	for i, msg := range filtered {
		var body *string
		if opts.IncludeBodies && msg.BodyMD != "" {
			b := msg.BodyMD
			body = &b
		}

		preview := ""
		if msg.BodyMD != "" {
			preview = truncateStringMail(msg.BodyMD, 100)
		}

		outputMsgs[i] = MailCheckMessage{
			ID:         msg.ID,
			From:       msg.From,
			To:         opts.Agent, // The recipient
			Subject:    msg.Subject,
			Preview:    preview,
			Body:       body,
			ThreadID:   msg.ThreadID,
			Importance: msg.Importance,
			Read:       msg.ReadAt != nil,
			Timestamp:  msg.CreatedTS.Format(time.RFC3339),
		}
	}

	// Build filters object
	filters := MailCheckFilters{
		Status:     opts.Status,
		UrgentOnly: opts.UrgentOnly,
	}
	if opts.Thread != "" {
		filters.Thread = &opts.Thread
	}
	if opts.Since != "" {
		filters.Since = &opts.Since
	}
	if opts.Until != "" {
		filters.Until = &opts.Until
	}
	if filters.Status == "" {
		filters.Status = "all"
	}

	// Build agent hints
	var hints *MailCheckAgentHints
	if opts.Verbose || unreadCount > 0 || hasMore {
		hints = &MailCheckAgentHints{}

		if unreadCount > 0 {
			hints.UnreadSummary = fmt.Sprintf("%d unread messages", unreadCount)
			if urgentCount > 0 {
				hints.UnreadSummary = fmt.Sprintf("%d unread messages, %d urgent", unreadCount, urgentCount)
			}
		}

		if hasMore {
			nextOffset := opts.Offset + opts.Limit
			hints.NextOffset = &nextOffset
			pagesRemaining := (totalMessages - opts.Offset - opts.Limit + opts.Limit - 1) / opts.Limit
			if pagesRemaining < 0 {
				pagesRemaining = 0
			}
			hints.PagesRemaining = &pagesRemaining
		}

		if oldestUnread != nil {
			ts := oldestUnread.Format(time.RFC3339)
			hints.OldestUnread = &ts
		}

		// Generate suggested action
		if unreadCount > 0 && len(outputMsgs) > 0 {
			// Find first unread message
			for _, msg := range outputMsgs {
				if !msg.Read {
					hints.SuggestedAction = fmt.Sprintf("Reply to %s about: %s", msg.From, msg.Subject)
					break
				}
			}
		}
	}

	return &MailCheckOutput{
		RobotResponse: NewRobotResponse(true),
		Project:       opts.Project,
		Agent:         opts.Agent,
		Filters:       filters,
		Unread:        unreadCount,
		Urgent:        urgentCount,
		TotalMessages: totalMessages,
		Offset:        opts.Offset,
		Count:         len(outputMsgs),
		Messages:      outputMsgs,
		HasMore:       hasMore,
		AgentHints:    hints,
	}, nil
}

// PrintMailCheck outputs mail check results as JSON.
// This is a thin wrapper around GetMailCheck() for CLI output.
func PrintMailCheck(opts MailCheckOptions) error {
	output, err := GetMailCheck(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

// truncateStringMail truncates a string to the specified length, adding "..." if truncated
// Named differently to avoid redeclaration with tui_parity.go's truncateString
func truncateStringMail(s string, maxLen int) string {
	if len(s) <= maxLen {
		return strings.TrimSpace(s)
	}
	// Find a good break point
	truncated := s[:maxLen]
	// Try to break at last space
	if lastSpace := strings.LastIndex(truncated, " "); lastSpace > maxLen/2 {
		truncated = truncated[:lastSpace]
	}
	return strings.TrimSpace(truncated) + "..."
}
