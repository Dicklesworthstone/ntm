package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func init() {
	rootCmd.AddCommand(newWebhookOutboxCmd())
}

func newWebhookOutboxCmd() *cobra.Command {
	var project string
	manager := func(cmd *cobra.Command) (*webhook.WebhookManager, error) {
		if err := cmd.Context().Err(); err != nil {
			return nil, err
		}
		dir, err := filepath.Abs(project)
		if err != nil {
			return nil, err
		}
		var redactCfg *redaction.Config
		if cfg != nil {
			value := cfg.Redaction.ToRedactionLibConfig()
			redactCfg = &value
		}
		return webhook.NewProjectWebhookManager(dir, redactCfg)
	}
	write := func(cmd *cobra.Command, value any) error {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	list := func(cmd *cobra.Command, args []string) error {
		m, err := manager(cmd)
		if err != nil {
			return err
		}
		snapshot, err := m.ListOutbox()
		if err != nil {
			return err
		}
		return write(cmd, snapshot)
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
			m, err := manager(cmd)
			if err != nil {
				return err
			}
			entry, err := m.RetryOutbox(args[0], revision)
			if err != nil {
				return err
			}
			return write(cmd, struct {
				Authorized bool                `json:"authorized"`
				Entry      webhook.OutboxEntry `json:"entry"`
			}{true, entry})
		},
	}
	retry.Flags().StringVar(&revision, "revision", "", "Revision of the inspected receipt; stale revisions are refused")
	cmd.AddCommand(retry)
	return cmd
}
