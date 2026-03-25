package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newMessageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "message",
		Short: "Agent Mail messaging",
	}

	cmd.AddCommand(
		newMessageInboxCmd(),
		newMessageSendCmd(),
		newMessageReadCmd(),
		newMessageAckCmd(),
	)

	return cmd
}

func newMessageInboxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inbox",
		Short: "View unified inbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, agentName, err := resolveMessageScope(tmux.GetCurrentSession())
			if err != nil {
				return err
			}

			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			msgs, err := unified.Inbox(context.Background())
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(msgs)
			}

			t := output.NewTable(cmd.OutOrStdout(), "ID", "Channel", "From", "Subject", "Time")
			for _, m := range msgs {
				t.AddRow(m.ID, m.Channel, m.From, m.Subject, m.Timestamp.Format(time.Kitchen))
			}
			t.Render()
			return nil
		},
	}
}

func newMessageSendCmd() *cobra.Command {
	var subject string
	cmd := &cobra.Command{
		Use:   "send <to> <body>",
		Short: "Send message",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			to := args[0]
			body := args[1]

			projectDir, agentName, err := resolveMessageScope(tmux.GetCurrentSession())
			if err != nil {
				return err
			}

			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			return unified.Send(context.Background(), to, subject, body)
		},
	}
	cmd.Flags().StringVar(&subject, "subject", "(No Subject)", "Message subject")
	return cmd
}

func newMessageReadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "read <msg-id>",
		Short: "Read a message by ID",
		Long: `Read a message by its unified ID (e.g., "am-123").
This marks the message as read.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			projectDir, agentName, err := resolveMessageScope(tmux.GetCurrentSession())
			if err != nil {
				return err
			}

			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			msg, err := unified.Read(context.Background(), id)
			if err != nil {
				return err
			}

			if IsJSONOutput() {
				return output.PrintJSON(msg)
			}

			fmt.Printf("ID:      %s\n", msg.ID)
			fmt.Printf("Channel: %s\n", msg.Channel)
			if msg.From != "" {
				fmt.Printf("From:    %s\n", msg.From)
			}
			if msg.Subject != "" {
				fmt.Printf("Subject: %s\n", msg.Subject)
			}
			if !msg.Timestamp.IsZero() {
				fmt.Printf("Time:    %s\n", msg.Timestamp.Format(time.RFC3339))
			}
			if msg.Body != "" {
				fmt.Printf("\n%s\n", msg.Body)
			}
			return nil
		},
	}
}

func newMessageAckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ack <msg-id>",
		Short: "Acknowledge a message by ID",
		Long: `Acknowledge a message by its unified ID (e.g., "am-123").
This marks the message as both read and acknowledged.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			projectDir, agentName, err := resolveMessageScope(tmux.GetCurrentSession())
			if err != nil {
				return err
			}

			amClient := newAgentMailClient(projectDir)
			unified := agentmail.NewUnifiedMessenger(amClient, nil, projectDir, agentName)

			if err := unified.Ack(context.Background(), id); err != nil {
				return err
			}

			fmt.Printf("Message %s acknowledged.\n", id)
			return nil
		},
	}
}

func resolveMessageScope(session string) (string, string, error) {
	session = strings.TrimSpace(session)
	if session != "" {
		resolved, err := normalizeProjectScopedSessionName(session, !IsJSONOutput())
		if err != nil {
			return "", "", err
		}
		session = resolved
	}

	projectDir := ""
	if session != "" {
		projectDir = resolveProjectDirForSession(session, true)
	} else {
		projectDir = GetProjectRoot()
	}
	if projectDir == "" {
		return "", "", fmt.Errorf("getting project root failed")
	}

	if session == "" {
		session = defaultProjectScopedSession(projectDir)
	}
	return projectDir, fmt.Sprintf("ntm_%s", session), nil
}
