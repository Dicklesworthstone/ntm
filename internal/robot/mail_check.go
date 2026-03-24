package robot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
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
	Thread     *string `json:"thread,omitempty"`
	Since      *string `json:"since,omitempty"`
	Until      *string `json:"until,omitempty"`
}

// MailCheckMessage represents a message in the mail check output
type MailCheckMessage struct {
	ID                int                          `json:"id"`
	From              string                       `json:"from"`
	To                string                       `json:"to"`
	Subject           string                       `json:"subject"`
	SubjectDisclosure *adapters.DisclosureMetadata `json:"subject_disclosure,omitempty"`
	Preview           string                       `json:"preview,omitempty"`
	PreviewDisclosure *adapters.DisclosureMetadata `json:"preview_disclosure,omitempty"`
	Body              *string                      `json:"body,omitempty"` // present only with --include-bodies when a body preview is available
	BodyDisclosure    *adapters.DisclosureMetadata `json:"body_disclosure,omitempty"`
	ThreadID          *string                      `json:"thread_id,omitempty"`
	Importance        string                       `json:"importance"`
	AckRequired       bool                         `json:"ack_required"`
	Read              bool                         `json:"read"`
	Timestamp         string                       `json:"timestamp"`
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
	Project       string
	Agent         string
	Thread        string
	Status        string // all, read, unread
	IncludeBodies bool
	UrgentOnly    bool
	Verbose       bool
	Limit         int
	Offset        int
	Since         string // YYYY-MM-DD
	Until         string // YYYY-MM-DD
}

type mailCheckInboxEntry struct {
	Message    agentmail.InboxMessage
	Recipients []string
	AllRead    bool
}

const mailCheckBackfillLimit = 1000

func buildMailCheckFilters(opts MailCheckOptions) MailCheckFilters {
	filters := MailCheckFilters{
		Status:     opts.Status,
		UrgentOnly: opts.UrgentOnly,
	}
	if filters.Status == "" {
		filters.Status = "all"
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
	return filters
}

func fetchMailCheckEntries(ctx context.Context, client *agentmail.Client, opts MailCheckOptions, fetchLimit int, sinceTS *time.Time) ([]mailCheckInboxEntry, error) {
	if opts.Agent != "" {
		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    opts.Project,
			AgentName:     opts.Agent,
			UrgentOnly:    opts.UrgentOnly,
			IncludeBodies: opts.IncludeBodies,
			Limit:         fetchLimit,
			SinceTS:       sinceTS,
		})
		if err != nil {
			return nil, err
		}
		entries := make([]mailCheckInboxEntry, 0, len(msgs))
		for _, msg := range msgs {
			entries = append(entries, mailCheckInboxEntry{
				Message:    msg,
				Recipients: []string{opts.Agent},
				AllRead:    msg.ReadAt != nil,
			})
		}
		return entries, nil
	}

	agents, err := client.ListProjectAgents(ctx, opts.Project)
	if err != nil {
		return nil, fmt.Errorf("list_agents failed: %w", err)
	}

	byID := make(map[int]*mailCheckInboxEntry)
	for _, agent := range agents {
		agentName := strings.TrimSpace(agent.Name)
		if agentName == "" || agentName == "HumanOverseer" {
			continue
		}

		msgs, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    opts.Project,
			AgentName:     agentName,
			UrgentOnly:    opts.UrgentOnly,
			IncludeBodies: opts.IncludeBodies,
			Limit:         fetchLimit,
			SinceTS:       sinceTS,
		})
		if err != nil {
			return nil, fmt.Errorf("fetch_inbox for %s: %w", agentName, err)
		}

		for _, msg := range msgs {
			entry, ok := byID[msg.ID]
			if !ok {
				msgCopy := msg
				byID[msg.ID] = &mailCheckInboxEntry{
					Message:    msgCopy,
					Recipients: []string{agentName},
					AllRead:    msg.ReadAt != nil,
				}
				continue
			}
			entry.Recipients = append(entry.Recipients, agentName)
			entry.AllRead = entry.AllRead && msg.ReadAt != nil
		}
	}

	entries := make([]mailCheckInboxEntry, 0, len(byID))
	for _, entry := range byID {
		sort.Strings(entry.Recipients)
		entries = append(entries, *entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		ti := entries[i].Message.CreatedTS.Time
		tj := entries[j].Message.CreatedTS.Time
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return entries[i].Message.ID > entries[j].Message.ID
	})
	return entries, nil
}

func mailCheckNeedsBackfill(opts MailCheckOptions) bool {
	return opts.Thread != "" || opts.Status == "read" || opts.Status == "unread" || opts.Until != ""
}

func filterMailCheckEntries(entries []mailCheckInboxEntry, opts MailCheckOptions) ([]mailCheckInboxEntry, error) {
	filtered := entries

	if opts.Thread != "" {
		threadFiltered := make([]mailCheckInboxEntry, 0, len(filtered))
		for _, entry := range filtered {
			if entry.Message.ThreadID != nil && *entry.Message.ThreadID == opts.Thread {
				threadFiltered = append(threadFiltered, entry)
			}
		}
		filtered = threadFiltered
	}

	switch opts.Status {
	case "read":
		statusFiltered := make([]mailCheckInboxEntry, 0, len(filtered))
		for _, entry := range filtered {
			if entry.AllRead {
				statusFiltered = append(statusFiltered, entry)
			}
		}
		filtered = statusFiltered
	case "unread":
		statusFiltered := make([]mailCheckInboxEntry, 0, len(filtered))
		for _, entry := range filtered {
			if !entry.AllRead {
				statusFiltered = append(statusFiltered, entry)
			}
		}
		filtered = statusFiltered
	}

	if opts.Until != "" {
		untilDate, err := parseMailCheckDate(opts.Until)
		if err != nil {
			return nil, err
		}
		untilDate = untilDate.Add(24 * time.Hour)
		dateFiltered := make([]mailCheckInboxEntry, 0, len(filtered))
		for _, entry := range filtered {
			if entry.Message.CreatedTS.Before(untilDate) {
				dateFiltered = append(dateFiltered, entry)
			}
		}
		filtered = dateFiltered
	}

	return filtered, nil
}

// Validate checks that options are valid
func (o *MailCheckOptions) Validate() error {
	if o.Project == "" {
		return fmt.Errorf("--mail-project is required")
	}
	if o.Limit < 0 {
		return fmt.Errorf("--limit cannot be negative")
	}
	if o.Offset < 0 {
		return fmt.Errorf("--mail-offset cannot be negative")
	}

	// Validate status value
	if o.Status != "" && o.Status != "all" && o.Status != "read" && o.Status != "unread" {
		return fmt.Errorf("invalid --mail-status value %q: must be read, unread, or all", o.Status)
	}

	var sinceDate *time.Time
	if o.Since != "" {
		parsed, err := parseMailCheckDate(o.Since)
		if err != nil {
			return fmt.Errorf("invalid --since date format: expected YYYY-MM-DD")
		}
		sinceDate = &parsed
	}
	if o.Until != "" {
		untilDate, err := parseMailCheckDate(o.Until)
		if err != nil {
			return fmt.Errorf("invalid --mail-until date format: expected YYYY-MM-DD")
		}
		if sinceDate != nil && untilDate.Before(*sinceDate) {
			return fmt.Errorf("--mail-until date cannot be before --since date")
		}
	}

	return nil
}

func parseMailCheckDate(raw string) (time.Time, error) {
	return time.Parse("2006-01-02", raw)
}

// GetMailCheck checks agent mail inbox and returns the results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMailCheck(opts MailCheckOptions) (*MailCheckOutput, error) {
	// Validate options first
	if err := opts.Validate(); err != nil {
		return &MailCheckOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Check --mail-project, --since, --mail-until, and related mail filter flags"),
			Project:       opts.Project,
			Messages:      []MailCheckMessage{},
			Filters:       buildMailCheckFilters(opts),
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
			Filters:  buildMailCheckFilters(opts),
		}, nil
	}

	effectiveLimit := opts.Limit
	if effectiveLimit <= 0 {
		effectiveLimit = 20
	}

	fetchLimit := effectiveLimit + opts.Offset + 1

	var sinceTS *time.Time
	if opts.Since != "" {
		parsedSince, err := parseMailCheckDate(opts.Since)
		if err != nil {
			return &MailCheckOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Check --since"),
				Project:       opts.Project,
				Agent:         opts.Agent,
				Messages:      []MailCheckMessage{},
				Filters:       buildMailCheckFilters(opts),
			}, nil
		}
		sinceTS = &parsedSince
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	needCount := opts.Offset + effectiveLimit
	if needCount < effectiveLimit {
		needCount = effectiveLimit
	}

	var entries []mailCheckInboxEntry
	var filtered []mailCheckInboxEntry
	var err error
	for {
		entries, err = fetchMailCheckEntries(ctx, client, opts, fetchLimit, sinceTS)
		if err != nil {
			return &MailCheckOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to fetch inbox"),
				Project:       opts.Project,
				Agent:         opts.Agent,
				Messages:      []MailCheckMessage{},
				Filters:       buildMailCheckFilters(opts),
			}, nil
		}

		filtered, err = filterMailCheckEntries(entries, opts)
		if err != nil {
			return &MailCheckOutput{
				RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Check --mail-until"),
				Project:       opts.Project,
				Agent:         opts.Agent,
				Messages:      []MailCheckMessage{},
				Filters:       buildMailCheckFilters(opts),
			}, nil
		}

		if !mailCheckNeedsBackfill(opts) || len(filtered) > needCount || len(entries) < fetchLimit || fetchLimit >= mailCheckBackfillLimit {
			break
		}

		nextFetchLimit := fetchLimit * 2
		if nextFetchLimit <= fetchLimit {
			nextFetchLimit = fetchLimit + effectiveLimit
		}
		if nextFetchLimit > mailCheckBackfillLimit {
			nextFetchLimit = mailCheckBackfillLimit
		}
		fetchLimit = nextFetchLimit
	}

	// Calculate counts before pagination
	totalMessages := len(filtered)
	unreadCount := 0
	urgentCount := 0
	var oldestUnread *time.Time

	for _, entry := range filtered {
		msg := entry.Message
		if !entry.AllRead {
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
	if len(filtered) > effectiveLimit {
		hasMore = true
		filtered = filtered[:effectiveLimit]
	}

	// Convert to output format
	outputMsgs := make([]MailCheckMessage, len(filtered))
	for i, entry := range filtered {
		outputMsgs[i] = mailCheckMessageFromEntry(entry, opts.IncludeBodies)
	}

	filters := buildMailCheckFilters(opts)

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
			nextOffset := opts.Offset + len(outputMsgs)
			hints.NextOffset = &nextOffset
			remaining := totalMessages - nextOffset
			pagesRemaining := 0
			if remaining > 0 {
				pagesRemaining = (remaining + effectiveLimit - 1) / effectiveLimit
			}
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

func mailCheckMessageFromEntry(entry mailCheckInboxEntry, includeBodies bool) MailCheckMessage {
	msg := entry.Message
	subject, subjectDisclosure := adapters.NormalizeDisclosureText(msg.Subject)
	bodyText, bodyDisclosure := adapters.NormalizeDisclosureText(msg.BodyMD)

	preview := bodyText
	previewDisclosure := bodyDisclosure
	if preview == "" {
		preview = subject
		previewDisclosure = subjectDisclosure
	}

	var body *string
	if includeBodies && bodyText != "" {
		b := bodyText
		body = &b
	}

	recipient := strings.Join(entry.Recipients, ", ")
	return MailCheckMessage{
		ID:                msg.ID,
		From:              msg.From,
		To:                recipient,
		Subject:           subject,
		SubjectDisclosure: subjectDisclosure,
		Preview:           preview,
		PreviewDisclosure: previewDisclosure,
		Body:              body,
		BodyDisclosure:    bodyDisclosure,
		ThreadID:          msg.ThreadID,
		Importance:        msg.Importance,
		AckRequired:       msg.AckRequired,
		Read:              entry.AllRead,
		Timestamp:         msg.CreatedTS.Format(time.RFC3339),
	}
}

func mailCheckMessageFromInbox(msg agentmail.InboxMessage, recipient string, includeBodies bool) MailCheckMessage {
	return mailCheckMessageFromEntry(mailCheckInboxEntry{
		Message:    msg,
		Recipients: []string{recipient},
		AllRead:    msg.ReadAt != nil,
	}, includeBodies)
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
