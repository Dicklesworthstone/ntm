package agentmail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	IsAvailableContext(ctx context.Context) bool
	FetchInbox(ctx context.Context, opts FetchInboxOptions) ([]InboxMessage, error)
	SendMessage(ctx context.Context, opts SendMessageOptions) (*SendResult, error)
	MarkMessageRead(ctx context.Context, projectKey, agentName string, messageID int) (*MessageReadResult, error)
	AcknowledgeMessage(ctx context.Context, projectKey, agentName string, messageID int) (*MessageAckResult, error)
	GetMessage(ctx context.Context, projectKey string, messageID int) (*Message, error)
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

func requireUnifiedContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return fmt.Errorf("%s: context is required", operation)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func unifiedCancellationError(ctx context.Context, err error, operation string) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", operation, ctxErr)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return nil
}

func (m *UnifiedMessenger) isAgentMailAvailable(ctx context.Context, operation string) (bool, error) {
	if m.amClient == nil {
		return false, nil
	}
	available := m.amClient.IsAvailableContext(ctx)
	if err := unifiedCancellationError(ctx, nil, operation); err != nil {
		return false, err
	}
	return available, nil
}

// Inbox fetches messages from both channels and merges them sorted by timestamp descending
func (m *UnifiedMessenger) Inbox(ctx context.Context) ([]UnifiedMessage, error) {
	if err := requireUnifiedContext(ctx, "read unified inbox"); err != nil {
		return nil, err
	}

	var unified []UnifiedMessage

	// Fetch from Agent Mail
	amAvailable, err := m.isAgentMailAvailable(ctx, "check agent mail availability")
	if err != nil {
		return nil, err
	}
	if amAvailable {
		opts := FetchInboxOptions{
			ProjectKey:    m.projectKey,
			AgentName:     m.agentName,
			Limit:         50,
			IncludeBodies: true,
		}
		inbox, err := m.amClient.FetchInbox(ctx, opts)
		if cancellationErr := unifiedCancellationError(ctx, err, "fetch agent mail inbox"); cancellationErr != nil {
			return nil, cancellationErr
		}
		if err != nil {
			slog.Warn("agent mail inbox fetch failed", "error", err)
		} else {
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
		if cancellationErr := unifiedCancellationError(ctx, err, "fetch bd inbox"); cancellationErr != nil {
			return nil, cancellationErr
		}
		if err != nil {
			slog.Warn("bd inbox fetch failed", "error", err)
		} else {
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
	if err := requireUnifiedContext(ctx, "send unified message"); err != nil {
		return err
	}

	// Try Agent Mail first
	amAvailable, err := m.isAgentMailAvailable(ctx, "check agent mail availability")
	if err != nil {
		return err
	}
	if amAvailable {
		_, err := m.amClient.SendMessage(ctx, SendMessageOptions{
			ProjectKey: m.projectKey,
			SenderName: m.agentName,
			To:         []string{to},
			Subject:    subject,
			BodyMD:     body,
		})
		if cancellationErr := unifiedCancellationError(ctx, err, "send agent mail message"); cancellationErr != nil {
			return cancellationErr
		}
		if err == nil {
			return nil
		}
		// If failed, try BD? Or maybe user specifies channel preference?
		// Fallthrough only on error might be confusing.
		// For now, just return error if AM configured but failed.
		// If AM not configured/available, try BD.
	}

	if m.bdClient != nil {
		err := m.bdClient.Send(ctx, to, body)
		if cancellationErr := unifiedCancellationError(ctx, err, "send bd message"); cancellationErr != nil {
			return cancellationErr
		}
		return err
	}

	return fmt.Errorf("no message channels available")
}

// Read retrieves a specific message by its unified ID (e.g., "am-123" or "bd-456")
func (m *UnifiedMessenger) Read(ctx context.Context, id string) (*UnifiedMessage, error) {
	if err := requireUnifiedContext(ctx, "read unified message"); err != nil {
		return nil, err
	}
	if len(id) < 4 || id[2] != '-' {
		return nil, fmt.Errorf("invalid message ID format: %s", id)
	}

	channel := id[:2]
	rawID := id[3:] // Skip "am-" or "bd-"

	switch channel {
	case "am":
		amAvailable, err := m.isAgentMailAvailable(ctx, "check agent mail availability")
		if err != nil {
			return nil, err
		}
		if amAvailable {
			msgID, err := strconv.Atoi(rawID)
			if err != nil {
				return nil, fmt.Errorf("invalid agent mail message ID: %w", err)
			}

			// Use the direct tool to fetch the message (much more efficient than scanning inbox)
			found, err := m.amClient.GetMessage(ctx, m.projectKey, msgID)
			if cancellationErr := unifiedCancellationError(ctx, err, "get agent mail message"); cancellationErr != nil {
				return nil, cancellationErr
			}
			if err != nil {
				return nil, fmt.Errorf("get agent mail message: %w", err)
			}

			if found != nil {
				// Mark as read
				_, markErr := m.amClient.MarkMessageRead(ctx, m.projectKey, m.agentName, msgID)
				if cancellationErr := unifiedCancellationError(ctx, markErr, "mark agent mail message read"); cancellationErr != nil {
					return nil, cancellationErr
				}
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
			if cancellationErr := unifiedCancellationError(ctx, err, "read bd message"); cancellationErr != nil {
				return nil, cancellationErr
			}
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
	if err := requireUnifiedContext(ctx, "acknowledge unified message"); err != nil {
		return err
	}
	if len(id) < 4 || id[2] != '-' {
		return fmt.Errorf("invalid message ID format: %s", id)
	}

	channel := id[:2]
	rawID := id[3:]

	switch channel {
	case "am":
		amAvailable, err := m.isAgentMailAvailable(ctx, "check agent mail availability")
		if err != nil {
			return err
		}
		if amAvailable {
			msgID, err := strconv.Atoi(rawID)
			if err != nil {
				return fmt.Errorf("invalid agent mail message ID: %w", err)
			}
			_, err = m.amClient.AcknowledgeMessage(ctx, m.projectKey, m.agentName, msgID)
			if cancellationErr := unifiedCancellationError(ctx, err, "acknowledge agent mail message"); cancellationErr != nil {
				return cancellationErr
			}
			return err
		}
		return fmt.Errorf("agent mail not available")

	case "bd":
		if m.bdClient != nil {
			err := m.bdClient.Ack(ctx, rawID)
			if cancellationErr := unifiedCancellationError(ctx, err, "acknowledge bd message"); cancellationErr != nil {
				return cancellationErr
			}
			return err
		}
		return fmt.Errorf("bd messaging not available")

	default:
		return fmt.Errorf("unknown message channel: %s", channel)
	}
}
