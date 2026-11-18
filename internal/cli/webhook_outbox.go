package cli

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func init() {
	rootCmd.AddCommand(newWebhookOutboxCmd())
}

func newWebhookOutboxCmd() *cobra.Command {
	var project string
	redactConfig := func() *redaction.Config {
		if cfg == nil {
			return nil
		}
		value := cfg.Redaction.ToRedactionLibConfig()
		return &value
	}
	write := func(cmd *cobra.Command, value any) error {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	list := func(cmd *cobra.Command, args []string) error {
		out, err := robot.GetWebhookOutbox(cmd.Context(), robot.WebhookOutboxOptions{
			Action:     "list",
			ProjectDir: project,
			RedactCfg:  redactConfig(),
		})
		if err != nil {
			return err
		}
		if !out.Success {
			return errors.New(out.Error)
		}
		return write(cmd, out.Snapshot)
	}
	cmd := &cobra.Command{
		Use: "webhook-outbox", Short: "Inspect and recover durable project webhook deliveries (JSON)",
		Long: `Inspect retained webhook receipts without sending HTTP or creating storage.
NTM_WEBHOOK_OUTBOX_DIR selects the same project-namespaced outbox as normal delivery.
Output excludes request bodies, messages and endpoint credentials.

Retry requires the exact record and revision returned by list. It authorizes a
new configured retry cycle only for a failed receipt with unchanged configuration
and allowed persistence. Receiver cooldowns, delivery IDs and wire bytes remain
unchanged. A live or later project manager sends the authorized work; this command
does not start a sender. Receivers must deduplicate by X-NTM-Delivery-ID.`,
		Args: cobra.NoArgs, RunE: list,
	}
	cmd.PersistentFlags().StringVar(&project, "project", ".", "Project directory containing .ntm.yaml or .ntm.yml")
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List retained receipts without dispatching them", Args: cobra.NoArgs, RunE: list})
	var revision string
	retry := &cobra.Command{
		Use: "retry RECORD", Short: "Authorize another retry cycle for one inspected failed receipt",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if revision == "" {
				return errors.New("--revision from webhook-outbox list is required")
			}
			out, err := robot.GetWebhookOutbox(cmd.Context(), robot.WebhookOutboxOptions{
				Action:     "retry",
				ProjectDir: project,
				Record:     args[0],
				Revision:   revision,
				RedactCfg:  redactConfig(),
			})
			if err != nil {
				return err
			}
			if !out.Success {
				return errors.New(out.Error)
			}
			return write(cmd, struct {
				Authorized bool                `json:"authorized"`
				Entry      webhook.OutboxEntry `json:"entry"`
			}{out.Authorized, *out.Entry})
		},
	}
	retry.Flags().StringVar(&revision, "revision", "", "Revision of the inspected receipt; stale revisions are refused")
	cmd.AddCommand(retry)
	var archiveRevision string
	archive := &cobra.Command{
		Use: "archive RECORD", Short: "Preserve one inspected failed receipt outside active outbox capacity",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if archiveRevision == "" {
				return errors.New("--revision from webhook-outbox list is required")
			}
			out, err := robot.GetWebhookOutbox(cmd.Context(), robot.WebhookOutboxOptions{
				Action:     "archive",
				ProjectDir: project,
				Record:     args[0],
				Revision:   archiveRevision,
				RedactCfg:  redactConfig(),
			})
			if err != nil {
				return err
			}
			if !out.Success {
				return errors.New(out.Error)
			}
			return write(cmd, out.Archive)
		},
	}
	archive.Flags().StringVar(&archiveRevision, "revision", "", "Revision of the inspected failed receipt; pending work cannot be archived")
	cmd.AddCommand(archive)
	return cmd
}
