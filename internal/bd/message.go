package bd

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
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
	cmd := exec.CommandContext(ctx, "bd", "message", "send", to, body, "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = c.projectPath
	return cmd.Run()
}

func (c *MessageClient) Inbox(ctx context.Context, unreadOnly, urgentOnly bool) ([]Message, error) {
	args := []string{"message", "inbox", "--json"}
	if unreadOnly {
		args = append(args, "--unread")
	}
	if urgentOnly {
		args = append(args, "--urgent")
	}

	cmd := exec.CommandContext(ctx, "bd", args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = c.projectPath

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Limit output to 10MB to prevent OOM
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, 10*1024*1024))
	if err != nil {
		_ = cmd.Wait()
		return nil, err
	}

	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal(output, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (c *MessageClient) Read(ctx context.Context, id string) (*Message, error) {
	cmd := exec.CommandContext(ctx, "bd", "message", "read", id, "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = c.projectPath

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Limit output to 1MB to prevent OOM
	output, err := io.ReadAll(io.LimitReader(stdoutPipe, 1024*1024))
	if err != nil {
		_ = cmd.Wait()
		return nil, err
	}

	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(output, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *MessageClient) Ack(ctx context.Context, id string) error {
	cmd := exec.CommandContext(ctx, "bd", "message", "ack", id, "--json")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = c.projectPath
	return cmd.Run()
}
