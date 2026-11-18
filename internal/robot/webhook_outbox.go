package robot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

const (
	webhookOutboxSchemaID      = "ntm:robot:webhook-outbox:v1"
	webhookOutboxSchemaVersion = "1.0.0"
)

// WebhookOutboxOptions configures ORI durable webhook outbox inspection, retry, and archive actions.
type WebhookOutboxOptions struct {
	Action     string
	ProjectDir string
	Record     string
	Revision   string
	RedactCfg  *redaction.Config
}

// WebhookOutboxOutput represents the structured JSON envelope for --robot-webhook-outbox.
type WebhookOutboxOutput struct {
	RobotResponse
	SchemaID      string                  `json:"schema_id"`
	SchemaVersion string                  `json:"schema_version"`
	Action        string                  `json:"action"`
	ProjectDir    string                  `json:"project_dir"`
	Snapshot      *webhook.OutboxSnapshot `json:"snapshot,omitempty"`
	Authorized    bool                    `json:"authorized,omitempty"`
	Entry         *webhook.OutboxEntry    `json:"entry,omitempty"`
	Archive       *webhook.OutboxArchive  `json:"archive,omitempty"`
	AgentHints    *AgentHints             `json:"_agent_hints,omitempty"`
}

func newWebhookOutboxErrorOutput(action, projectDir string, err error, code, hint string) *WebhookOutboxOutput {
	return &WebhookOutboxOutput{
		RobotResponse: NewErrorResponse(err, code, hint),
		SchemaID:      webhookOutboxSchemaID,
		SchemaVersion: webhookOutboxSchemaVersion,
		Action:        action,
		ProjectDir:    projectDir,
	}
}

func classifyWebhookOutboxMutationError(err error) (string, string) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "requires a record name"), strings.Contains(msg, "invalid webhook outbox revision"):
		return ErrCodeInvalidFlag, "Pass exact --outbox-record and 64-hex --outbox-revision from --robot-webhook-outbox --outbox-action=list"
	case strings.Contains(msg, "changed"):
		return ErrCodeInvalidFlag, "Re-run --robot-webhook-outbox --outbox-action=list to obtain the current revision"
	default:
		return ErrCodeInternalError, "Inspect --robot-webhook-outbox --outbox-action=list for record state and blocked_by"
	}
}

// GetWebhookOutbox lists, retries, or archives retained durable project webhook receipts.
func GetWebhookOutbox(ctx context.Context, opts WebhookOutboxOptions) (*WebhookOutboxOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newWebhookOutboxErrorOutput(opts.Action, opts.ProjectDir, err, ErrCodeTimeout, "Retry --robot-webhook-outbox with an active context"), nil
	}

	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "retry", "archive":
	default:
		return newWebhookOutboxErrorOutput(
			action,
			opts.ProjectDir,
			fmt.Errorf("invalid webhook outbox action %q: must be list, retry, or archive", opts.Action),
			ErrCodeInvalidFlag,
			"Use --outbox-action=list, --outbox-action=retry, or --outbox-action=archive",
		), nil
	}

	projectRaw := strings.TrimSpace(opts.ProjectDir)
	if projectRaw == "" {
		projectRaw = "."
	}
	absDir, err := filepath.Abs(projectRaw)
	if err != nil {
		return newWebhookOutboxErrorOutput(action, projectRaw, fmt.Errorf("invalid project directory %q: %w", projectRaw, err), ErrCodeInvalidFlag, "Pass a valid project directory via --project"), nil
	}

	record := strings.TrimSpace(opts.Record)
	revision := strings.TrimSpace(opts.Revision)
	if action == "retry" || action == "archive" {
		if record == "" {
			return newWebhookOutboxErrorOutput(action, absDir, fmt.Errorf("--outbox-record is required for %s", action), ErrCodeInvalidFlag, "Specify --outbox-record and --outbox-revision from --robot-webhook-outbox --outbox-action=list"), nil
		}
		if revision == "" {
			return newWebhookOutboxErrorOutput(action, absDir, errors.New("--revision from webhook-outbox list is required"), ErrCodeInvalidFlag, "Specify --outbox-revision from --robot-webhook-outbox --outbox-action=list"), nil
		}
	}

	manager, err := webhook.NewProjectWebhookManager(absDir, opts.RedactCfg)
	if err != nil {
		return newWebhookOutboxErrorOutput(action, absDir, err, ErrCodeInternalError, "Verify .ntm.yaml/.ntm.yml webhook configuration and outbox directory permissions"), nil
	}

	out := &WebhookOutboxOutput{
		RobotResponse: NewRobotResponse(true),
		SchemaID:      webhookOutboxSchemaID,
		SchemaVersion: webhookOutboxSchemaVersion,
		Action:        action,
		ProjectDir:    absDir,
	}

	switch action {
	case "list":
		snapshot, err := manager.ListOutbox()
		if err != nil {
			return newWebhookOutboxErrorOutput(action, absDir, err, ErrCodeInternalError, "Check durable webhook outbox directory permissions (mode 0700)"), nil
		}
		if snapshot.Entries == nil {
			snapshot.Entries = []webhook.OutboxEntry{}
		}
		out.Snapshot = &snapshot
		failedCount := 0
		retryableCount := 0
		for _, entry := range snapshot.Entries {
			if strings.EqualFold(entry.State, "failed") {
				failedCount++
			}
			if entry.RetryAllowed {
				retryableCount++
			}
		}
		var actions []RobotAction
		if retryableCount > 0 {
			actions = append(actions, RobotAction{
				Action:   "retry_failed_receipt",
				Target:   absDir,
				Reason:   fmt.Sprintf("%d failed receipt(s) eligible for retry authorization", retryableCount),
				Priority: 1,
				Details:  "Pass --outbox-action=retry --outbox-record=<record> --outbox-revision=<revision>",
			})
		}
		out.AgentHints = &AgentHints{
			Summary:          fmt.Sprintf("Outbox enabled=%t: %d retained receipt(s) (%d failed, %d retryable)", snapshot.Enabled, len(snapshot.Entries), failedCount, retryableCount),
			SuggestedActions: actions,
		}
	case "retry":
		entry, err := manager.RetryOutbox(record, revision)
		if err != nil {
			code, hint := classifyWebhookOutboxMutationError(err)
			return newWebhookOutboxErrorOutput(action, absDir, err, code, hint), nil
		}
		out.Authorized = true
		out.Entry = &entry
		out.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Authorized retry cycle for record %s (delivery %s)", entry.Record, entry.DeliveryID),
		}
	case "archive":
		archiveRes, err := manager.ArchiveOutbox(record, revision)
		if err != nil {
			code, hint := classifyWebhookOutboxMutationError(err)
			return newWebhookOutboxErrorOutput(action, absDir, err, code, hint), nil
		}
		out.Archive = &archiveRes
		out.AgentHints = &AgentHints{
			Summary: fmt.Sprintf("Archived terminal receipt %s to %s", archiveRes.Record, archiveRes.Path),
		}
	}

	return out, nil
}

// PrintWebhookOutbox executes GetWebhookOutbox and writes the terminal JSON/TOON response.
func PrintWebhookOutbox(ctx context.Context, opts WebhookOutboxOptions) error {
	out, err := GetWebhookOutbox(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot webhook-outbox failed")
}

func webhookOutboxCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "webhook-outbox",
		Flag:        "--robot-webhook-outbox",
		Category:    "utility",
		Description: "Inspect, retry, or archive durable project webhook outbox receipts without starting background HTTP senders.",
		Parameters: []RobotParameter{
			{Name: "outbox-action", Flag: "--outbox-action", Type: "string", Required: false, Default: "list", Description: "Outbox action: list, retry, archive"},
			{Name: "outbox-record", Flag: "--outbox-record", Type: "string", Required: false, Description: "Outbox record filename (required for retry and archive)"},
			{Name: "outbox-revision", Flag: "--outbox-revision", Type: "string", Required: false, Description: "64-hex optimistic concurrency revision from outbox list (required for retry and archive)"},
			{Name: "project", Flag: "--project", Type: "string", Required: false, Default: ".", Description: "Project directory containing .ntm/webhooks/outbox"},
		},
		Examples: []string{
			"ntm --robot-webhook-outbox",
			"ntm --robot-webhook-outbox --outbox-action=retry --outbox-record=0001.json --outbox-revision=deadbeef",
			"ntm --robot-webhook-outbox --outbox-action=archive --outbox-record=0001.json --outbox-revision=deadbeef",
		},
	}
}
