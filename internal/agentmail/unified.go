package agentmail

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bd"
)

type UnifiedMessage struct {
	ID        string    `json:"id"`
	Channel   string    `json:"channel"` // "agentmail" or "bd"
	From      string    `json:"from"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	Timestamp time.Time `json:"timestamp"`
}

type agentMailClient interface {
	IsAvailable() bool
	FetchInbox(ctx context.Context, opts FetchInboxOptions) ([]InboxMessage, error)
	SendMessage(ctx context.Context, opts SendMessageOptions) (*SendResult, error)
	MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) error
	AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) error
}

type bdMessageClient interface {
	Send(ctx context.Context, to, body string) error
	Inbox(ctx context.Context, unreadOnly, urgentOnly bool) ([]bd.Message, error)
	Read(ctx context.Context, id string) (*bd.Message, error)
	Ack(ctx context.Context, id string) error
}

type UnifiedMessenger struct {
	amClient   agentMailClient
	bdClient   bdMessageClient
	projectKey string
	agentName  string
}

func NewUnifiedMessenger(am *Client, bd *bd.MessageClient, projectKey, agentName string) *UnifiedMessenger {
	var amClient agentMailClient
	if am != nil {
		amClient = am
	}
	var bdClient bdMessageClient
	if bd != nil {
		bdClient = bd
	}
	return &UnifiedMessenger{
		amClient:   amClient,
		bdClient:   bdClient,
		projectKey: projectKey,
		agentName:  agentName,
	}
}

// Inbox fetches messages from both channels and merges them sorted by timestamp descending
func (m *UnifiedMessenger) Inbox(ctx context.Context) ([]UnifiedMessage, error) {
	var unified []UnifiedMessage

	// Fetch from Agent Mail
	if m.amClient != nil && m.amClient.IsAvailable() {
		opts := FetchInboxOptions{
			ProjectKey:    m.projectKey,
			AgentName:     m.agentName,
			Limit:         50,
			IncludeBodies: true,
		}
		inbox, err := m.amClient.FetchInbox(ctx, opts)
		if err == nil {
			for _, msg := range inbox {
				unified = append(unified, UnifiedMessage{
					ID:        fmt.Sprintf("am-%d", msg.ID),
					Channel:   "agentmail",
					From:      msg.From,
					Subject:   msg.Subject,
					Body:      msg.BodyMD,
					Timestamp: msg.CreatedTS.Time,
				})
			}
		}
	}

	// Fetch from BD
	if m.bdClient != nil {
		bdInbox, err := m.bdClient.Inbox(ctx, false, false)
		if err == nil {
			for _, msg := range bdInbox {
				unified = append(unified, UnifiedMessage{
					ID:        fmt.Sprintf("bd-%s", msg.ID),
					Channel:   "bd",
					From:      msg.From,
					Subject:   "(No Subject)",
					Body:      msg.Body,
					Timestamp: msg.Timestamp,
				})
			}
		}
	}

	// Sort by timestamp desc
	sort.Slice(unified, func(i, j int) bool {
		return unified[i].Timestamp.After(unified[j].Timestamp)
	})

	return unified, nil
}

// Send sends a message via the preferred channel (defaulting to Agent Mail if available, else BD)
// For now, it tries Agent Mail first.
func (m *UnifiedMessenger) Send(ctx context.Context, to, subject, body string) error {
	// Try Agent Mail first
	if m.amClient != nil && m.amClient.IsAvailable() {
		_, err := m.amClient.SendMessage(ctx, SendMessageOptions{
			ProjectKey: m.projectKey,
			SenderName: m.agentName,
			To:         []string{to},
			Subject:    subject,
			BodyMD:     body,
		})
		if err == nil {
			return nil
		}
		// If failed, try BD? Or maybe user specifies channel preference?
		// Fallthrough only on error might be confusing.
		// For now, just return error if AM configured but failed.
		// If AM not configured/available, try BD.
	}

	if m.bdClient != nil {
		return m.bdClient.Send(ctx, to, body)
	}

	return fmt.Errorf("no message channels available")
}

// Read retrieves a specific message by its unified ID (e.g., "am-123" or "bd-456")
func (m *UnifiedMessenger) Read(ctx context.Context, id string) (*UnifiedMessage, error) {
	if len(id) < 4 {
		return nil, fmt.Errorf("invalid message ID format: %s", id)
	}

	channel := id[:2]
	rawID := id[3:] // Skip "am-" or "bd-"

	switch channel {
	case "am":
		if m.amClient != nil && m.amClient.IsAvailable() {
			msgID, err := strconv.Atoi(rawID)
			if err != nil {
				return nil, fmt.Errorf("invalid agent mail message ID: %w", err)
			}

			// Fetch inbox to get message content (Agent Mail MCP doesn't have get-single-message)
			opts := FetchInboxOptions{
				ProjectKey:    m.projectKey,
				AgentName:     m.agentName,
				Limit:         100, // Reasonable limit to find the message
				IncludeBodies: true,
			}
			inbox, err := m.amClient.FetchInbox(ctx, opts)
			if err != nil {
				return nil, fmt.Errorf("fetch inbox: %w", err)
			}

			// Helper to find message in inbox
			findMsg := func(list []InboxMessage) *InboxMessage {
				for _, msg := range list {
					if msg.ID == msgID {
						return &msg
					}
				}
				return nil
			}

			found := findMsg(inbox)

			// If not found, try fetching deeper history (up to 1000)
			if found == nil {
				opts.Limit = 1000
				inbox, err = m.amClient.FetchInbox(ctx, opts)
				if err == nil {
					found = findMsg(inbox)
				}
			}

			if found != nil {
				// Mark as read
				_ = m.amClient.MarkMessageRead(ctx, m.projectKey, m.agentName, msgID)
				return &UnifiedMessage{
					ID:        id,
					Channel:   "agentmail",
					From:      found.From,
					Subject:   found.Subject,
					Body:      found.BodyMD,
					Timestamp: found.CreatedTS.Time,
				}, nil
			}
			return nil, fmt.Errorf("message not found: %s", id)
		}
		return nil, fmt.Errorf("agent mail not available or not configured")

	case "bd":
		if m.bdClient != nil {
			msg, err := m.bdClient.Read(ctx, rawID)
			if err != nil {
				return nil, fmt.Errorf("read bd message: %w", err)
			}
			return &UnifiedMessage{
				ID:        id,
				Channel:   "bd",
				From:      msg.From,
				Subject:   "(No Subject)",
				Body:      msg.Body,
				Timestamp: msg.Timestamp,
			}, nil
		}
		return nil, fmt.Errorf("bd messaging not available")

	default:
		return nil, fmt.Errorf("unknown message channel: %s", channel)
	}
}

// Ack acknowledges a message by its unified ID
func (m *UnifiedMessenger) Ack(ctx context.Context, id string) error {
	if len(id) < 4 {
		return fmt.Errorf("invalid message ID format: %s", id)
	}

	channel := id[:2]
	rawID := id[3:]

	switch channel {
	case "am":
		if m.amClient != nil && m.amClient.IsAvailable() {
			msgID, err := strconv.Atoi(rawID)
			if err != nil {
				return fmt.Errorf("invalid agent mail message ID: %w", err)
			}
			return m.amClient.AcknowledgeMessage(ctx, m.projectKey, m.agentName, msgID)
		}
		return fmt.Errorf("agent mail not available")

	case "bd":
		if m.bdClient != nil {
			return m.bdClient.Ack(ctx, rawID)
		}
		return fmt.Errorf("bd messaging not available")

	default:
		return fmt.Errorf("unknown message channel: %s", channel)
	}
}
