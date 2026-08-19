// Package pipeline provides workflow execution for AI agent orchestration.
// notify.go implements notifications for pipeline events.
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// NotificationEvent represents a pipeline event to notify about.
type NotificationEvent string

const (
	NotifyStarted   NotificationEvent = "started"
	NotifyCompleted NotificationEvent = "completed"
	NotifyFailed    NotificationEvent = "failed"
	NotifyCancelled NotificationEvent = "cancelled"
	NotifyStepError NotificationEvent = "step_error"
)

// NotificationPayload contains data for a notification.
type NotificationPayload struct {
	Event        NotificationEvent `json:"event"`
	WorkflowName string            `json:"workflow_name"`
	RunID        string            `json:"run_id"`
	Session      string            `json:"session,omitempty"`
	Status       ExecutionStatus   `json:"status"`
	Duration     time.Duration     `json:"duration,omitempty"`
	StepsTotal   int               `json:"steps_total"`
	StepsDone    int               `json:"steps_done"`
	StepsFailed  int               `json:"steps_failed"`
	Error        string            `json:"error,omitempty"`
	FailedStep   string            `json:"failed_step,omitempty"`
	Timestamp    time.Time         `json:"timestamp"`
}

// NotificationChannel represents a channel for sending notifications.
type NotificationChannel string

const (
	ChannelDesktop NotificationChannel = "desktop"
	ChannelWebhook NotificationChannel = "webhook"
	ChannelMail    NotificationChannel = "mail"
)

// Notifier sends notifications for pipeline events.
type Notifier struct {
	channels      []NotificationChannel
	webhookURL    string
	mailRecipient string
	mailClient    *agentmail.Client
	projectKey    string
	agentName     string
}

// NotifierConfig configures the notifier.
type NotifierConfig struct {
	Channels      []string
	WebhookURL    string
	MailRecipient string
	MailClient    *agentmail.Client
	ProjectKey    string
	AgentName     string
}

// Notify sends a notification to all configured channels.
func (n *Notifier) Notify(ctx context.Context, payload NotificationPayload) error {
	if len(n.channels) == 0 {
		return nil
	}

	var errs []string
	for _, channel := range n.channels {
		var err error
		switch channel {
		case ChannelDesktop:
			err = n.notifyDesktop(payload)
		case ChannelWebhook:
			err = n.notifyWebhook(ctx, payload)
		case ChannelMail:
			err = n.notifyMail(ctx, payload)
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", channel, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ShouldNotify checks if a notification should be sent for the given event.
func ShouldNotify(settings WorkflowSettings, event NotificationEvent) bool {
	switch event {
	case NotifyCompleted:
		return settings.NotifyOnComplete
	case NotifyFailed, NotifyStepError:
		return settings.NotifyOnError
	case NotifyStarted, NotifyCancelled:
		// Could add settings.NotifyOnStart, settings.NotifyOnCancel
		return false
	}
	return false
}

// notifyDesktop sends a desktop notification using OS-specific tools.
func (n *Notifier) notifyDesktop(payload NotificationPayload) error {
	title := formatDesktopTitle(payload)
	body := formatDesktopBody(payload)

	switch runtime.GOOS {
	case "darwin":
		return notifyMacOS(title, body)
	case "linux":
		return notifyLinux(title, body)
	default:
		// Windows or unsupported - skip silently
		return nil
	}
}

func notifyMacOS(title, body string) error {
	// bd-coq43: bound osascript with the same 2 s ceiling notifyLinux uses
	// (bd-7ramj.1) so a modal Apple permission dialog, wedged
	// NotificationCenter, or TCC authorization stall can't freeze the
	// pipeline thread.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	script := fmt.Sprintf(`display notification %q with title %q`, body, title)
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	return cmd.Run()
}

func notifyLinux(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "notify-send", title, body)
	return cmd.Run()
}

func formatDesktopTitle(p NotificationPayload) string {
	switch p.Event {
	case NotifyCompleted:
		return fmt.Sprintf("Pipeline '%s' completed", p.WorkflowName)
	case NotifyFailed:
		return fmt.Sprintf("Pipeline '%s' failed", p.WorkflowName)
	case NotifyCancelled:
		return fmt.Sprintf("Pipeline '%s' cancelled", p.WorkflowName)
	case NotifyStarted:
		return fmt.Sprintf("Pipeline '%s' started", p.WorkflowName)
	case NotifyStepError:
		return fmt.Sprintf("Pipeline '%s' step error", p.WorkflowName)
	}
	return fmt.Sprintf("Pipeline '%s'", p.WorkflowName)
}

func formatDesktopBody(p NotificationPayload) string {
	switch p.Event {
	case NotifyCompleted:
		return fmt.Sprintf("Duration: %s | Steps: %d/%d", formatDuration(p.Duration), p.StepsDone, p.StepsTotal)
	case NotifyFailed:
		if p.FailedStep != "" {
			return fmt.Sprintf("Failed at step '%s': %s", p.FailedStep, truncateMessage(p.Error, 100))
		}
		return truncateMessage(p.Error, 150)
	case NotifyCancelled:
		return fmt.Sprintf("Cancelled after %s", formatDuration(p.Duration))
	case NotifyStarted:
		return fmt.Sprintf("Starting with %d steps", p.StepsTotal)
	case NotifyStepError:
		return fmt.Sprintf("Step '%s' failed: %s", p.FailedStep, truncateMessage(p.Error, 100))
	}
	return ""
}

// notifyWebhook sends a notification via HTTP webhook.
func (n *Notifier) notifyWebhook(ctx context.Context, payload NotificationPayload) error {
	if n.webhookURL == "" {
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ntm-pipeline/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

// notifyMail sends a notification via Agent Mail.
func (n *Notifier) notifyMail(ctx context.Context, payload NotificationPayload) error {
	if n.mailClient == nil || n.mailRecipient == "" {
		return nil
	}

	subject := formatMailSubject(payload)
	body := formatMailBody(payload)
	importance := "normal"
	if payload.Event == NotifyFailed || payload.Event == NotifyStepError {
		importance = "high"
	}

	_, err := n.mailClient.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  n.projectKey,
		SenderName:  n.agentName,
		To:          []string{n.mailRecipient},
		Subject:     subject,
		BodyMD:      body,
		Importance:  importance,
		AckRequired: false,
	})
	if err != nil {
		return fmt.Errorf("sending mail: %w", err)
	}

	return nil
}

func formatMailSubject(p NotificationPayload) string {
	switch p.Event {
	case NotifyCompleted:
		return fmt.Sprintf("Pipeline '%s' completed successfully", p.WorkflowName)
	case NotifyFailed:
		return fmt.Sprintf("Pipeline '%s' failed", p.WorkflowName)
	case NotifyCancelled:
		return fmt.Sprintf("Pipeline '%s' was cancelled", p.WorkflowName)
	case NotifyStarted:
		return fmt.Sprintf("Pipeline '%s' started", p.WorkflowName)
	case NotifyStepError:
		return fmt.Sprintf("Pipeline '%s' step failed: %s", p.WorkflowName, p.FailedStep)
	}
	return fmt.Sprintf("Pipeline '%s' notification", p.WorkflowName)
}

func formatMailBody(p NotificationPayload) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Pipeline: %s\n\n", p.WorkflowName))
	sb.WriteString(fmt.Sprintf("**Run ID:** %s\n", p.RunID))
	if p.Session != "" {
		sb.WriteString(fmt.Sprintf("**Session:** %s\n", p.Session))
	}
	sb.WriteString(fmt.Sprintf("**Status:** %s\n", p.Status))
	sb.WriteString(fmt.Sprintf("**Time:** %s\n\n", p.Timestamp.Format(time.RFC3339)))

	switch p.Event {
	case NotifyCompleted:
		sb.WriteString("## Summary\n\n")
		sb.WriteString(fmt.Sprintf("- **Duration:** %s\n", formatDuration(p.Duration)))
		sb.WriteString(fmt.Sprintf("- **Steps completed:** %d/%d\n", p.StepsDone, p.StepsTotal))
		if p.StepsFailed > 0 {
			sb.WriteString(fmt.Sprintf("- **Steps failed:** %d (continued)\n", p.StepsFailed))
		}

	case NotifyFailed:
		sb.WriteString("## Error\n\n")
		if p.FailedStep != "" {
			sb.WriteString(fmt.Sprintf("**Failed step:** `%s`\n\n", p.FailedStep))
		}
		sb.WriteString(fmt.Sprintf("```\n%s\n```\n\n", p.Error))
		sb.WriteString("## Progress\n\n")
		sb.WriteString(fmt.Sprintf("- **Duration before failure:** %s\n", formatDuration(p.Duration)))
		sb.WriteString(fmt.Sprintf("- **Steps completed:** %d/%d\n", p.StepsDone, p.StepsTotal))

	case NotifyCancelled:
		sb.WriteString("## Cancellation\n\n")
		sb.WriteString(fmt.Sprintf("Pipeline was cancelled after %s.\n\n", formatDuration(p.Duration)))
		sb.WriteString(fmt.Sprintf("- **Steps completed:** %d/%d\n", p.StepsDone, p.StepsTotal))

	case NotifyStepError:
		sb.WriteString("## Step Error\n\n")
		sb.WriteString(fmt.Sprintf("**Step:** `%s`\n\n", p.FailedStep))
		sb.WriteString(fmt.Sprintf("```\n%s\n```\n\n", p.Error))
		sb.WriteString("Pipeline is continuing with remaining steps.\n")
	}

	sb.WriteString("\n---\n*Sent by NTM Pipeline*\n")

	return sb.String()
}

// formatDuration formats a duration in human-readable form.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// truncateMessage truncates a message to the specified length.
// Respects UTF-8 rune boundaries.
func truncateMessage(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return "..."[:n]
	}
	// Find last rune boundary at or before n-3 bytes
	targetLen := n - 3
	prevI := 0
	for i := range s {
		if i > targetLen {
			return s[:prevI] + "..."
		}
		prevI = i
	}
	return s[:prevI] + "..."
}

// BuildPayloadFromState creates a NotificationPayload from execution state.
func BuildPayloadFromState(state *ExecutionState, workflow *Workflow, event NotificationEvent) NotificationPayload {
	payload := NotificationPayload{
		Event:        event,
		WorkflowName: workflow.Name,
		RunID:        state.RunID,
		Session:      state.Session,
		Status:       state.Status,
		Timestamp:    time.Now(),
		StepsTotal:   len(workflow.Steps),
	}

	// Calculate duration
	if !state.StartedAt.IsZero() {
		if !state.FinishedAt.IsZero() {
			payload.Duration = state.FinishedAt.Sub(state.StartedAt)
		} else {
			payload.Duration = time.Since(state.StartedAt)
		}
	}

	// Count completed and failed steps
	for _, result := range state.Steps {
		switch result.Status {
		case StatusCompleted:
			payload.StepsDone++
		case StatusFailed:
			payload.StepsFailed++
			if payload.FailedStep == "" {
				payload.FailedStep = result.StepID
				if result.Error != nil {
					payload.Error = result.Error.Message
				}
			}
		case StatusSkipped:
			payload.StepsDone++ // Count skipped as "done" for progress
		}
	}

	// Get error from state if not already set
	if payload.Error == "" && len(state.Errors) > 0 {
		for _, err := range state.Errors {
			if err.Fatal {
				payload.Error = err.Message
				if err.StepID != "" && payload.FailedStep == "" {
					payload.FailedStep = err.StepID
				}
				break
			}
		}
	}

	return payload
}
