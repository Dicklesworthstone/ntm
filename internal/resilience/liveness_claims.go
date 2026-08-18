package resilience

import (
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/notify"
)

// WS0-G2 config-key liveness claims for keys consumed by internal/resilience
// (bd-g2-claims-backlog-o787y). See internal/config/liveness.go.
//
// The notifications.* claims also live here: internal/config imports
// internal/notify (Config embeds notify.Config), so notify cannot claim its
// own keys without an import cycle. This package constructs the Notifier from
// cfg.Notifications (monitor.go → notify.NewWithRedaction), so the claims
// reference the notify functions that consume each field.
func init() {
	// Crash monitor thresholds (monitor.go).
	for _, key := range []string{
		"resilience.max_restarts",
		"resilience.restart_delay_seconds",
		"resilience.health_check_seconds",
		"resilience.crash_threshold",
		"resilience.notify_on_crash",
		"resilience.notify_on_max_restarts",
		"resilience.rate_limit.detect",
		"resilience.rate_limit.notify",
	} {
		config.RegisterReader(key, NewMonitor)
	}
	// The rate_limit.auto_rotate alias is folded into rotation.enabled +
	// rotation.auto_trigger at config load (see internal/config loadWithCWD,
	// ntm#113); this monitor is the runtime consumer of the folded values.
	config.RegisterReader("resilience.rate_limit.auto_rotate", NewMonitor)

	// Rotation assistance gates (monitor.go triggerRotationAssistance path).
	config.RegisterReader("rotation.enabled", NewMonitor)
	config.RegisterReader("rotation.auto_trigger", NewMonitor)
	config.RegisterReader("rotation.auto_initiate", NewMonitor)

	// Plugin agent launch commands (monitor.go ScanAndRegisterAgents).
	config.RegisterReader("agents.plugins", (*Monitor).ScanAndRegisterAgents)

	// notifications.* — consumed by internal/notify (notify.go).
	for _, key := range []string{
		"notifications.enabled",
		"notifications.events",
		"notifications.desktop.enabled",
		"notifications.filebox.enabled",
		"notifications.log.enabled",
		"notifications.shell.enabled",
		"notifications.webhook.enabled",
	} {
		config.RegisterReader(key, notify.New)
	}
	for _, key := range []string{
		"notifications.primary",
		"notifications.fallback",
		"notifications.routing",
		"notifications.desktop.title",
		"notifications.filebox.path",
		"notifications.log.path",
		"notifications.shell.command",
		"notifications.shell.pass_json",
		"notifications.webhook.url",
		"notifications.webhook.template",
		"notifications.webhook.method",
		"notifications.webhook.headers",
	} {
		config.RegisterReader(key, (*notify.Notifier).Notify)
	}
}
