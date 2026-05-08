package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// MailSendStep describes a first-class MCP Agent Mail send operation.
type MailSendStep struct {
	ProjectKey  string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName   string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	To          StringOrList `yaml:"to,omitempty" toml:"to,omitempty" json:"to,omitempty"`
	Subject     string       `yaml:"subject,omitempty" toml:"subject,omitempty" json:"subject,omitempty"`
	Body        string       `yaml:"body,omitempty" toml:"body,omitempty" json:"body,omitempty"`
	ThreadID    string       `yaml:"thread_id,omitempty" toml:"thread_id,omitempty" json:"thread_id,omitempty"`
	AckRequired bool         `yaml:"ack_required,omitempty" toml:"ack_required,omitempty" json:"ack_required,omitempty"`
}

// FileReservationPathsStep describes an MCP Agent Mail file reservation
// acquisition operation.
type FileReservationPathsStep struct {
	ProjectKey string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName  string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	Paths      StringOrList `yaml:"paths,omitempty" toml:"paths,omitempty" json:"paths,omitempty"`
	TTLSeconds int          `yaml:"ttl_seconds,omitempty" toml:"ttl_seconds,omitempty" json:"ttl_seconds,omitempty"`
	Exclusive  bool         `yaml:"exclusive,omitempty" toml:"exclusive,omitempty" json:"exclusive,omitempty"`
	Reason     string       `yaml:"reason,omitempty" toml:"reason,omitempty" json:"reason,omitempty"`
}

// MailInboxCheckStep describes a first-class MCP Agent Mail inbox polling
// operation.
type MailInboxCheckStep struct {
	ProjectKey    string `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName     string `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	UntilAckCount int    `yaml:"until_ack_count,omitempty" toml:"until_ack_count,omitempty" json:"until_ack_count,omitempty"`
}

// FileReservationReleaseStep describes an MCP Agent Mail file reservation
// release operation.
type FileReservationReleaseStep struct {
	ProjectKey string       `yaml:"project_key,omitempty" toml:"project_key,omitempty" json:"project_key,omitempty"`
	AgentName  string       `yaml:"agent_name,omitempty" toml:"agent_name,omitempty" json:"agent_name,omitempty"`
	Paths      StringOrList `yaml:"paths,omitempty" toml:"paths,omitempty" json:"paths,omitempty"`
}

// hasMailStep reports whether the step is configured as any of the Agent Mail
// dispatch kinds. The runtime executor uses this to short-circuit the prompt
// dispatch path so authors get a structured "not implemented" skip instead of
// a misleading "step has no prompt or prompt_file" failure (bd-hz1tl).
func (s *Step) hasMailStep() bool {
	if s == nil {
		return false
	}
	return s.MailSend != nil ||
		s.FileReservationPaths != nil ||
		s.MailInboxCheck != nil ||
		s.FileReservationRelease != nil
}

type agentMailClientCache struct {
	mu        sync.Mutex
	byProject map[string]*agentmail.Client
}

func (c *agentMailClientCache) get(projectKey string) *agentmail.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.byProject == nil {
		c.byProject = make(map[string]*agentmail.Client)
	}
	if client := c.byProject[projectKey]; client != nil {
		return client
	}

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	c.byProject[projectKey] = client
	return client
}

func (e *Executor) mailClient(projectKey string) *agentmail.Client {
	return e.mailClients.get(projectKey)
}

// executeMailStep is the executor dispatch branch for Agent Mail step kinds.
// These steps execute through MCP Agent Mail rather than tmux pane dispatch.
func (e *Executor) executeMailStep(ctx context.Context, step *Step) StepResult {
	now := time.Now()
	result := StepResult{
		StepID:    step.ID,
		Status:    StatusRunning,
		StartedAt: now,
	}

	if err := ctx.Err(); err != nil {
		result.Status = StatusCancelled
		result.FinishedAt = time.Now()
		result.SkipKind = SkipKindCancelled
		result.SkipReason = err.Error()
		return result
	}

	if e.config.DryRun {
		return e.completeMailStep(step, result, "dry_run", map[string]interface{}{
			"action": "dry_run",
			"kinds":  step.mailStepKindNames(),
		})
	}

	switch {
	case step.MailSend != nil:
		return e.executeMailSendStep(ctx, step, result)
	case step.FileReservationPaths != nil:
		return e.executeFileReservationPathsStep(ctx, step, result)
	case step.MailInboxCheck != nil:
		return e.executeMailInboxCheckStep(ctx, step, result)
	case step.FileReservationRelease != nil:
		return e.executeFileReservationReleaseStep(ctx, step, result)
	default:
		result.Status = StatusFailed
		result.FinishedAt = time.Now()
		result.Error = &StepError{
			Type:      "agent_mail",
			Message:   "Agent Mail step has no configured kind",
			Timestamp: time.Now(),
		}
		return result
	}
}

func (e *Executor) executeMailSendStep(ctx context.Context, step *Step, result StepResult) StepResult {
	send := step.MailSend
	projectKey, err := e.resolveMailStepString(ctx, send.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, send.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send agent_name: %v", err))
	}
	to, err := e.resolveMailStepList(ctx, send.To)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send to: %v", err))
	}
	subject, err := e.resolveMailStepString(ctx, send.Subject)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send subject: %v", err))
	}
	body, err := e.resolveMailStepString(ctx, send.Body)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send body: %v", err))
	}
	threadID, err := e.resolveMailStepString(ctx, send.ThreadID)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_send thread_id: %v", err))
	}

	client := e.mailClient(projectKey)
	sent, err := client.SendMessage(ctx, agentmail.SendMessageOptions{
		ProjectKey:  projectKey,
		SenderName:  agentName,
		To:          to,
		Subject:     subject,
		BodyMD:      body,
		ThreadID:    threadID,
		AckRequired: send.AckRequired,
	})
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("mail_send failed: %v", err))
	}

	return e.completeMailStep(step, result, "mail_send", map[string]interface{}{
		"action":    "mail_send",
		"result":    sent,
		"sent":      sent.Count,
		"thread_id": threadID,
	})
}

func (e *Executor) executeFileReservationPathsStep(ctx context.Context, step *Step, result StepResult) StepResult {
	reserve := step.FileReservationPaths
	projectKey, err := e.resolveMailStepString(ctx, reserve.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, reserve.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths agent_name: %v", err))
	}
	paths, err := e.resolveMailStepList(ctx, reserve.Paths)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths paths: %v", err))
	}
	reason, err := e.resolveMailStepString(ctx, reserve.Reason)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_paths reason: %v", err))
	}

	client := e.mailClient(projectKey)
	reservation, err := client.ReservePaths(ctx, agentmail.FileReservationOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
		Paths:      paths,
		TTLSeconds: reserve.TTLSeconds,
		Exclusive:  reserve.Exclusive,
		Reason:     reason,
	})
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("file_reservation_paths failed: %v", err))
	}

	return e.completeMailStep(step, result, "file_reservation_paths", map[string]interface{}{
		"action":         "file_reservation_paths",
		"result":         reservation,
		"granted_count":  len(reservation.Granted),
		"conflict_count": len(reservation.Conflicts),
	})
}

func (e *Executor) executeMailInboxCheckStep(ctx context.Context, step *Step, result StepResult) StepResult {
	inbox := step.MailInboxCheck
	projectKey, err := e.resolveMailStepString(ctx, inbox.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_inbox_check project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, inbox.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("mail_inbox_check agent_name: %v", err))
	}

	client := e.mailClient(projectKey)
	messages, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
		ProjectKey: projectKey,
		AgentName:  agentName,
	})
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("mail_inbox_check failed: %v", err))
	}

	ackRequired := 0
	for _, msg := range messages {
		if msg.AckRequired {
			ackRequired++
		}
	}

	return e.completeMailStep(step, result, "mail_inbox_check", map[string]interface{}{
		"action":              "mail_inbox_check",
		"messages":            messages,
		"message_count":       len(messages),
		"ack_required_count":  ackRequired,
		"until_ack_count":     inbox.UntilAckCount,
		"until_ack_count_met": inbox.UntilAckCount == 0 || ackRequired >= inbox.UntilAckCount,
	})
}

func (e *Executor) executeFileReservationReleaseStep(ctx context.Context, step *Step, result StepResult) StepResult {
	release := step.FileReservationRelease
	projectKey, err := e.resolveMailStepString(ctx, release.ProjectKey)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release project_key: %v", err))
	}
	agentName, err := e.resolveMailStepString(ctx, release.AgentName)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release agent_name: %v", err))
	}
	paths, err := e.resolveMailStepList(ctx, release.Paths)
	if err != nil {
		return failMailStep(result, "substitution", fmt.Sprintf("file_reservation_release paths: %v", err))
	}

	client := e.mailClient(projectKey)
	released, err := client.ReleaseReservations(ctx, projectKey, agentName, paths, nil)
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("file_reservation_release failed: %v", err))
	}

	return e.completeMailStep(step, result, "file_reservation_release", map[string]interface{}{
		"action": "file_reservation_release",
		"result": released,
	})
}

func (e *Executor) resolveMailStepString(ctx context.Context, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return e.substituteVariablesStrictCtx(ctx, value)
}

func (e *Executor) resolveMailStepList(ctx context.Context, values StringOrList) ([]string, error) {
	resolved := make([]string, len(values))
	for i, value := range values {
		item, err := e.resolveMailStepString(ctx, value)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		resolved[i] = item
	}
	return resolved, nil
}

func (e *Executor) completeMailStep(step *Step, result StepResult, action string, payload interface{}) StepResult {
	output, err := json.Marshal(payload)
	if err != nil {
		return failMailStep(result, "agent_mail", fmt.Sprintf("%s output marshal failed: %v", action, err))
	}

	result.Status = StatusCompleted
	result.Output = string(output)
	result.FinishedAt = time.Now()

	if step.OutputParse.Type != "" && step.OutputParse.Type != "none" {
		parsed, err := e.parseOutput(result.Output, step.OutputParse)
		if err != nil {
			return failMailStep(result, "output_parse", fmt.Sprintf("failed to parse mail step output: %v", err))
		}
		result.ParsedData = parsed
	}

	return result
}

func failMailStep(result StepResult, errType, message string) StepResult {
	result.Status = StatusFailed
	result.FinishedAt = time.Now()
	result.Error = &StepError{
		Type:      errType,
		Message:   message,
		Timestamp: time.Now(),
	}
	return result
}

func (s *Step) mailStepKindNames() []string {
	if s == nil {
		return nil
	}

	var names []string
	if s.MailSend != nil {
		names = append(names, "mail_send")
	}
	if s.FileReservationPaths != nil {
		names = append(names, "file_reservation_paths")
	}
	if s.MailInboxCheck != nil {
		names = append(names, "mail_inbox_check")
	}
	if s.FileReservationRelease != nil {
		names = append(names, "file_reservation_release")
	}
	return names
}

// validateMailStepPayload reports per-kind required-field errors for the
// Agent Mail step kinds (bd-vv7ij). Mutual-exclusion checks live in
// Validate; this only fires when exactly one mail step kind is set.
func validateMailStepPayload(step *Step, stepField string, result *ValidationResult) {
	if step == nil {
		return
	}

	if send := step.MailSend; send != nil {
		field := stepField + ".mail_send"
		if strings.TrimSpace(send.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "mail_send requires project_key",
				Hint:    "Set the absolute project root (e.g. /data/projects/ntm) so MCP Agent Mail can scope the send.",
			})
		}
		if strings.TrimSpace(send.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "mail_send requires agent_name",
				Hint:    "Set agent_name to the sender's roster name (e.g. TealCrane).",
			})
		}
		if len(send.To) == 0 {
			result.addError(ParseError{
				Field:   field + ".to",
				Message: "mail_send requires at least one recipient in to",
				Hint:    "Use a string for a single recipient or a list for multiple recipients.",
			})
		} else {
			for i, recipient := range send.To {
				if strings.TrimSpace(recipient) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.to[%d]", field, i),
						Message: "mail_send recipient cannot be empty",
					})
				}
			}
		}
		if strings.TrimSpace(send.Subject) == "" && strings.TrimSpace(send.Body) == "" {
			result.addError(ParseError{
				Field:   field,
				Message: "mail_send requires subject or body",
				Hint:    "Set subject or body so the recipient has something to read.",
			})
		}
	}

	if reserve := step.FileReservationPaths; reserve != nil {
		field := stepField + ".file_reservation_paths"
		if strings.TrimSpace(reserve.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "file_reservation_paths requires project_key",
			})
		}
		if strings.TrimSpace(reserve.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "file_reservation_paths requires agent_name",
			})
		}
		if len(reserve.Paths) == 0 {
			result.addError(ParseError{
				Field:   field + ".paths",
				Message: "file_reservation_paths requires at least one path",
				Hint:    "Use a string for a single path or a list for multiple paths.",
			})
		} else {
			for i, path := range reserve.Paths {
				if strings.TrimSpace(path) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.paths[%d]", field, i),
						Message: "file_reservation_paths path cannot be empty",
					})
				}
			}
		}
		if reserve.TTLSeconds < 0 {
			result.addError(ParseError{
				Field:   field + ".ttl_seconds",
				Message: fmt.Sprintf("file_reservation_paths.ttl_seconds must be non-negative, got %d", reserve.TTLSeconds),
				Hint:    "Use 0 for the server default or a positive integer for the lock TTL in seconds.",
			})
		}
	}

	if inbox := step.MailInboxCheck; inbox != nil {
		field := stepField + ".mail_inbox_check"
		if strings.TrimSpace(inbox.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "mail_inbox_check requires project_key",
			})
		}
		if strings.TrimSpace(inbox.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "mail_inbox_check requires agent_name",
			})
		}
		if inbox.UntilAckCount < 0 {
			result.addError(ParseError{
				Field:   field + ".until_ack_count",
				Message: fmt.Sprintf("mail_inbox_check.until_ack_count must be non-negative, got %d", inbox.UntilAckCount),
			})
		}
	}

	if release := step.FileReservationRelease; release != nil {
		field := stepField + ".file_reservation_release"
		if strings.TrimSpace(release.ProjectKey) == "" {
			result.addError(ParseError{
				Field:   field + ".project_key",
				Message: "file_reservation_release requires project_key",
			})
		}
		if strings.TrimSpace(release.AgentName) == "" {
			result.addError(ParseError{
				Field:   field + ".agent_name",
				Message: "file_reservation_release requires agent_name",
			})
		}
		if len(release.Paths) == 0 {
			result.addError(ParseError{
				Field:   field + ".paths",
				Message: "file_reservation_release requires at least one path",
				Hint:    "Use a string for a single path or a list for multiple paths.",
			})
		} else {
			for i, path := range release.Paths {
				if strings.TrimSpace(path) == "" {
					result.addError(ParseError{
						Field:   fmt.Sprintf("%s.paths[%d]", field, i),
						Message: "file_reservation_release path cannot be empty",
					})
				}
			}
		}
	}
}
