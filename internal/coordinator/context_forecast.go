package coordinator

import (
	"fmt"
	"strings"
	"time"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// reportContextForecasts runs within the existing rotation observation pass.
// Its ONLY effect outside bounded sampling state is an advisory attention
// event. In particular, auto_confirm does not turn a forecast into actuation.
func (rc *rotationChecker) reportContextForecasts(panes []tmux.Pane, usages map[string]*ntmctx.TranscriptUsage) {
	if rc.publishForecast == nil {
		return
	}
	if rc.forecaster == nil {
		rc.forecaster = ntmctx.NewExhaustionForecaster()
	}
	now := rc.now()
	observations := make([]ntmctx.ForecastObservation, 0, len(panes))
	byID := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		usage := usages[pane.ID]
		if usage == nil || pane.ID == "" || pane.PID <= 0 || pane.Dead || pane.Service != "" ||
			strings.TrimSpace(pane.Command) == "" || tmux.PaneCommandIsStarting(pane.Command) || pane.IdleShell() {
			continue
		}
		limit, known := rc.contextWindow(usage)
		if !known {
			continue
		}
		observations = append(observations, ntmctx.ForecastObservation{
			Key: pane.ID,
			Generation: fmt.Sprintf("%d/%q/%q/%q/%q", pane.PID, pane.Title,
				string(pane.Type.Canonical()), usage.Model, usage.Path),
			Tokens: int64(usage.Tokens), ContextLimit: int64(limit), ObservedAt: usage.UpdatedAt,
		})
		byID[pane.ID] = pane
	}
	for _, forecast := range rc.forecaster.Observe(observations, now) {
		pane := byID[forecast.Key]
		usage := usages[forecast.Key]
		prediction := forecast.Prediction
		limitSource := "transcript"
		if usage.ContextWindow <= 0 {
			limitSource = "model_registry"
		} else if usage.Path == "" {
			limitSource = "status_bar"
		}
		rc.publishForecast(robot.AttentionEvent{
			Ts: now.UTC().Format(time.RFC3339Nano), Session: rc.session, Pane: pane.Index,
			Category: robot.EventCategoryAlert, Type: robot.EventTypeAlertWarning,
			Source: "coordinator.context_forecast", ReasonCode: "context_exhaustion_forecast",
			Actionability: robot.ActionabilityInteresting, Severity: robot.SeverityWarning,
			Summary: fmt.Sprintf("Pane %s may exhaust context in %.1fm if observed growth continues (%.0f tokens/min); inspect context and prepare a handoff",
				pane.ID, prediction.MinutesToExhaustion, prediction.TokenVelocity),
			Details: map[string]any{
				"pane_id": pane.ID, "window_index": pane.WindowIndex, "pane_pid": pane.PID,
				"agent_id": pane.Title, "agent_type": string(pane.Type.Canonical()),
				"model": usage.Model, "usage_source": rotationUsageSource(usage),
				"context_limit_source": limitSource, "advisory": true,
				"forecast_level": forecast.Level, "current_tokens": prediction.CurrentTokens,
				"context_limit": prediction.ContextLimit, "usage_percent": prediction.CurrentUsage * 100,
				"tokens_per_minute": prediction.TokenVelocity, "minutes_to_exhaustion": prediction.MinutesToExhaustion,
				"sample_count": prediction.SampleCount, "window_minutes": prediction.WindowDuration,
				"observed_at": forecast.ObservedAt.UTC().Format(time.RFC3339Nano),
			},
		})
	}
}
