// Package robot provides machine-readable output for AI agents.
// rano_stats.go implements the --robot-rano-stats command.
package robot

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/integrations/rano"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// RanoStatsOptions configures the --robot-rano-stats command.
type RanoStatsOptions struct {
	Panes  []int  // pane indices to include (empty = all non-control panes)
	Window string // time window (e.g., 5m, 1h)
}

// RanoStatsOutput represents the response from --robot-rano-stats.
type RanoStatsOutput struct {
	RobotResponse
	Window string                   `json:"window"`
	Query  RanoStatsQuery           `json:"query"`
	Panes  map[string]RanoPaneStats `json:"panes"`
	Total  RanoTotals               `json:"total"`
}

// RanoStatsQuery captures request parameters.
type RanoStatsQuery struct {
	PanesRequested []int `json:"panes_requested,omitempty"`
}

// RanoPaneStats aggregates network stats for a pane.
type RanoPaneStats struct {
	Session      string                     `json:"session"`
	PaneIndex    int                        `json:"pane_index"`
	PaneTitle    string                     `json:"pane_title"`
	AgentType    string                     `json:"agent_type,omitempty"`
	NTMIndex     int                        `json:"ntm_index,omitempty"`
	PIDs         []int                      `json:"pids,omitempty"`
	RequestCount int                        `json:"request_count"`
	BytesIn      int64                      `json:"bytes_in"`
	BytesOut     int64                      `json:"bytes_out"`
	LastRequest  string                     `json:"last_request,omitempty"`
	Providers    map[string]RanoProviderAgg `json:"providers,omitempty"`
}

// RanoProviderAgg is a placeholder for provider breakdowns (if available).
type RanoProviderAgg struct {
	Connections int   `json:"connections,omitempty"`
	BytesSent   int64 `json:"bytes_sent,omitempty"`
	BytesRecv   int64 `json:"bytes_received,omitempty"`
}

// RanoTotals aggregates totals across all panes.
type RanoTotals struct {
	RequestCount int   `json:"request_count"`
	BytesIn      int64 `json:"bytes_in"`
	BytesOut     int64 `json:"bytes_out"`
}

// GetRanoStats returns per-pane network stats from rano.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRanoStats(opts RanoStatsOptions) (*RanoStatsOutput, error) {
	window, err := normalizeRanoWindow(opts.Window)
	if err != nil {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInvalidFlag, "Invalid --rano-window (use 5m, 1h, etc.)"),
			Window:        opts.Window,
			Query:         RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:         map[string]RanoPaneStats{},
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adapter := tools.NewRanoAdapter()
	availability, err := adapter.GetAvailability(ctx)
	if err != nil {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to check rano availability"),
			Window:        window,
			Query:         RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:         map[string]RanoPaneStats{},
		}, nil
	}

	if availability == nil || !availability.Available {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rano not installed"),
				ErrCodeDependencyMissing,
				"Install rano and ensure it is on PATH",
			),
			Window: window,
			Query:  RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:  map[string]RanoPaneStats{},
		}, nil
	}
	if !availability.Compatible {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rano version incompatible"),
				ErrCodeDependencyMissing,
				"Update rano to a compatible version",
			),
			Window: window,
			Query:  RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:  map[string]RanoPaneStats{},
		}, nil
	}
	if !availability.HasCapability {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rano lacks required capabilities"),
				ErrCodePermissionDenied,
				"Grant CAP_NET_ADMIN or run with appropriate privileges",
			),
			Window: window,
			Query:  RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:  map[string]RanoPaneStats{},
		}, nil
	}
	if !availability.CanReadProc {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("rano cannot read /proc"),
				ErrCodePermissionDenied,
				"Ensure /proc is accessible for PID mapping",
			),
			Window: window,
			Query:  RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:  map[string]RanoPaneStats{},
		}, nil
	}

	// Build pane filter map (keys are pane identity strings).
	targets, err := collectRanoTargetPanes(ctx, opts.Panes)
	if err != nil {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to list tmux panes"),
			Window:        window,
			Query:         RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:         map[string]RanoPaneStats{},
		}, nil
	}

	// Build PID mapping across sessions for attribution.
	pidMap := rano.NewPIDMap("")
	if err := pidMap.RefreshContext(ctx); err != nil {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to refresh PID map"),
			Window:        window,
			Query:         RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:         map[string]RanoPaneStats{},
		}, nil
	}

	stats, err := adapter.GetAllProcessStatsWithWindow(ctx, window)
	if err != nil {
		return &RanoStatsOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to query rano stats"),
			Window:        window,
			Query:         RanoStatsQuery{PanesRequested: opts.Panes},
			Panes:         map[string]RanoPaneStats{},
		}, nil
	}

	panes, total := aggregateRanoStats(stats, func(pid int) *rano.PaneIdentity {
		return pidMap.GetPaneForPID(pid)
	}, func(identity *rano.PaneIdentity) bool {
		if identity == nil {
			return false
		}
		_, ok := targets[identity.String()]
		return ok
	})

	return &RanoStatsOutput{
		RobotResponse: NewRobotResponse(true),
		Window:        window,
		Query:         RanoStatsQuery{PanesRequested: opts.Panes},
		Panes:         panes,
		Total:         total,
	}, nil
}

// PrintRanoStats handles the --robot-rano-stats command.
// This is a thin wrapper around GetRanoStats() for CLI output.
func PrintRanoStats(opts RanoStatsOptions) error {
	output, err := GetRanoStats(opts)
	if err != nil {
		return err
	}
	return outputJSON(output)
}

func normalizeRanoWindow(window string) (string, error) {
	if window == "" {
		return "5m", nil
	}
	if _, err := util.ParseDuration(window); err != nil {
		return "", err
	}
	return window, nil
}

func collectRanoTargetPanes(ctx context.Context, panesFilter []int) (map[string]tmux.Pane, error) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return nil, err
	}

	filter := make(map[int]bool)
	for _, idx := range panesFilter {
		filter[idx] = true
	}

	targets := make(map[string]tmux.Pane)
	for _, sess := range sessions {
		panes, err := tmux.GetPanesContext(ctx, sess.Name)
		if err != nil {
			return nil, err
		}
		for _, pane := range panes {
			if pane.Index <= 0 {
				continue
			}
			if len(filter) > 0 && !filter[pane.Index] {
				continue
			}
			key := paneIdentityKey(pane, sess.Name)
			targets[key] = pane
		}
	}
	return targets, nil
}

func paneIdentityKey(pane tmux.Pane, session string) string {
	if pane.Title != "" {
		return pane.Title
	}
	return pane.ID
}

func aggregateRanoStats(
	stats []tools.RanoProcessStats,
	pidLookup func(int) *rano.PaneIdentity,
	allowPane func(*rano.PaneIdentity) bool,
) (map[string]RanoPaneStats, RanoTotals) {
	panes := make(map[string]RanoPaneStats)
	lastRequest := make(map[string]time.Time)
	total := RanoTotals{}

	for _, stat := range stats {
		identity := pidLookup(stat.PID)
		if identity == nil || !allowPane(identity) {
			continue
		}

		key := identity.String()
		pane := panes[key]
		if pane.PaneTitle == "" {
			pane = RanoPaneStats{
				Session:   identity.Session,
				PaneIndex: identity.PaneIndex,
				PaneTitle: identity.PaneTitle,
				AgentType: string(identity.AgentType),
				NTMIndex:  identity.NTMIndex,
				Providers: map[string]RanoProviderAgg{},
			}
		}

		pane.PIDs = append(pane.PIDs, stat.PID)
		pane.RequestCount += stat.RequestCount
		pane.BytesIn += stat.BytesIn
		pane.BytesOut += stat.BytesOut

		if stat.LastRequest != "" {
			if t, err := time.Parse(time.RFC3339, stat.LastRequest); err == nil {
				if prev, ok := lastRequest[key]; !ok || t.After(prev) {
					lastRequest[key] = t
					pane.LastRequest = stat.LastRequest
				}
			} else if pane.LastRequest == "" {
				pane.LastRequest = stat.LastRequest
			}
		}

		panes[key] = pane
	}

	// Normalize PID ordering for stable output and compute totals.
	keys := make([]string, 0, len(panes))
	for key := range panes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pane := panes[key]
		sort.Ints(pane.PIDs)
		panes[key] = pane
		total.RequestCount += pane.RequestCount
		total.BytesIn += pane.BytesIn
		total.BytesOut += pane.BytesOut
	}

	return panes, total
}
