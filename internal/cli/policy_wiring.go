package cli

// WS6-wire (bd-ws6-config-truth-ienmd.1): after config load, thread the
// central [retry] policy and [rotation.thresholds] into the packages that
// own the corresponding retry loops / rotation classification. Each consuming
// package registers the G2 liveness claim for the keys it reads; this file is
// only the startup plumbing that hands the loaded values over.

import (
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/quota"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func init() {
	// G2 config-key liveness claims for [retry.agent_mail]. The reader is
	// agentmail.ApplyRetryPolicy; the claim is registered here because
	// internal/agentmail cannot import internal/config (import cycle via
	// internal/watcher → agentmail).
	config.RegisterReader("retry.agent_mail.max_attempts", agentmail.ApplyRetryPolicy)
	config.RegisterReader("retry.agent_mail.initial_delay_ms", agentmail.ApplyRetryPolicy)
}

// applyConfiguredPolicies pushes reader-owned config policies into their
// consuming packages. Called once per process after the config is loaded
// (root.go); safe to call with nil (keeps compiled-in defaults, which match
// the historical hardcoded behavior).
func applyConfiguredPolicies(cfg *config.Config) {
	if cfg == nil {
		return
	}
	// [retry] → the three real retry loops (WS6-wire).
	agentmail.ApplyRetryPolicy(cfg.Retry.RetryPolicyFor("agent_mail"))
	robot.ApplyAlertRetryPolicy(cfg.Retry.RetryPolicyFor("alerts"))
	webhookAttempts, webhookDelay := cfg.Retry.RetryPolicyFor("webhook")
	webhook.ApplyRetryPolicy(webhookAttempts, webhookDelay,
		cfg.Retry.MaxDelayMs, cfg.Retry.BackoffFactor, cfg.Retry.Jitter)
	// [rotation.thresholds] → rotation engine quota classification.
	quota.ApplyRotationThresholds(
		cfg.Rotation.Thresholds.WarningPercent,
		cfg.Rotation.Thresholds.CriticalPercent)
}
