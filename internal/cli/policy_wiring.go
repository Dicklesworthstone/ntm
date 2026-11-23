package cli

// WS6-wire (bd-ws6-config-truth-ienmd.1): after config load, thread the
// central [retry] policy and [rotation.thresholds] into the packages that
// own the corresponding retry loops / rotation classification. Each consuming
// package registers the G2 liveness claim for the keys it reads (in its
// liveness_claims.go); this file is only the startup plumbing that hands the
// loaded values over.

import (
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/notify"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

// applyConfiguredPolicies pushes reader-owned config policies into their
// consuming packages. Called once per process after the config is loaded
// (root.go); safe to call with nil (keeps compiled-in defaults, which match
// the historical hardcoded behavior).
func applyConfiguredPolicies(cfg *config.Config) {
	if cfg == nil {
		return
	}
	// [agent_mail] url/token → every Agent Mail client this process builds,
	// including the ones constructed without options (the pre-commit
	// reservation guard, robot reservations, pipeline mail steps).
	agentmail.UseConfiguredEndpoint(cfg.AgentMail.URL, cfg.AgentMail.Token)
	// [retry] → the three real retry loops (WS6-wire).
	agentmail.ApplyRetryPolicy(cfg.Retry.RetryPolicyFor("agent_mail"))
	notify.ApplyAlertRetryPolicy(cfg.Retry.RetryPolicyFor("alerts"))
	webhookAttempts, webhookDelay := cfg.Retry.RetryPolicyFor("webhook")
	webhook.ApplyRetryPolicy(webhookAttempts, webhookDelay,
		cfg.Retry.MaxDelayMs, cfg.Retry.BackoffFactor, cfg.Retry.Jitter)
	// [rotation.thresholds] → rotation engine quota classification.
	quota.ApplyRotationThresholds(
		cfg.Rotation.Thresholds.WarningPercent,
		cfg.Rotation.Thresholds.CriticalPercent)
}
