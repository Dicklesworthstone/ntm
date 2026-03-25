package bd

import (
	"context"
	"fmt"
	"time"
)

type MessageClient struct {
	projectPath string
	agentName   string
}

func NewMessageClient(projectPath, agentName string) *MessageClient {
	return &MessageClient{
		projectPath: projectPath,
		agentName:   agentName,
	}
}

func legacyMessageUnsupportedError() error {
	return fmt.Errorf("legacy beads message commands are not supported by the installed br version")
}

type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Body      string    `json:"body"`
	Timestamp time.Time `json:"timestamp"`
	Read      bool      `json:"read"`
	Urgent    bool      `json:"urgent"`
}

func (c *MessageClient) Send(ctx context.Context, to, body string) error {
	return legacyMessageUnsupportedError()
}

func (c *MessageClient) Inbox(ctx context.Context, unreadOnly, urgentOnly bool) ([]Message, error) {
	return nil, legacyMessageUnsupportedError()
}

func (c *MessageClient) Read(ctx context.Context, id string) (*Message, error) {
	return nil, legacyMessageUnsupportedError()
}

func (c *MessageClient) Ack(ctx context.Context, id string) error {
	return legacyMessageUnsupportedError()
}
