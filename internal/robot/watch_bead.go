// Package robot provides machine-readable output for AI agents.
// watch_bead.go implements the --robot-watch-bead command.
package robot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// WatchBeadOptions configures the --robot-watch-bead operation.
type WatchBeadOptions struct {
	Session string
	BeadID  string
	// PaneSelectors holds N, W.P, or %N selectors (the grammar shared by every
	// robot --panes flag). It takes precedence over PaneIndices.
	PaneSelectors []string
	// PaneIndices is the legacy bare-index form; each entry is selector N.
	PaneIndices []int
	Lines       int
	Interval    time.Duration
}

// BeadMention describes a bead mention found in pane output.
type BeadMention struct {
	Pane int `json:"pane"`
	// PaneRef is the unambiguous address of the pane: "window.pane" on a
	// multi-window session, the bare pane index otherwise.
	PaneRef   string    `json:"pane_ref,omitempty"`
	PaneID    string    `json:"pane_id,omitempty"`
	AgentType string    `json:"agent_type"`
	Line      string    `json:"line"`
	LineNum   int       `json:"line_num"`
	Timestamp time.Time `json:"timestamp"`
}

// WatchBeadOutput is the response for --robot-watch-bead=SESSION.
type WatchBeadOutput struct {
	RobotResponse
	Session      string        `json:"session"`
	BeadID       string        `json:"bead_id"`
	CheckedAt    time.Time     `json:"checked_at"`
	Interval     string        `json:"interval"`
	PanesScanned int           `json:"panes_scanned"`
	Mentions     []BeadMention `json:"mentions"`
	BeadStatus   string        `json:"bead_status"`
	StatusError  string        `json:"status_error,omitempty"`
}

type beadMentionMatch struct {
	Line    string
	LineNum int
}

// PrintWatchBead prints a bead mention snapshot and current bead status.
// This is a thin wrapper around GetWatchBead() for CLI output.
func PrintWatchBead(opts WatchBeadOptions) error {
	output, err := GetWatchBead(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot watch-bead failed")
}

// GetWatchBead captures recent pane output and returns bead mention matches.
func GetWatchBead(opts WatchBeadOptions) (*WatchBeadOutput, error) {
	if opts.Lines <= 0 {
		opts.Lines = 200
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}

	output := &WatchBeadOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		BeadID:        strings.TrimSpace(opts.BeadID),
		CheckedAt:     time.Now().UTC(),
		Interval:      opts.Interval.String(),
		Mentions:      []BeadMention{},
		BeadStatus:    "unknown",
	}

	if output.BeadID == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("bead ID is required"),
			ErrCodeInvalidFlag,
			"Provide --bead=<id> with --robot-watch-bead",
		)
		return output, nil
	}
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	mentionRE, err := compileBeadMentionPattern(output.BeadID)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInvalidFlag,
			"Use a non-empty bead identifier",
		)
		return output, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to list panes: %w", err),
			ErrCodeInternalError,
			"Check tmux session state",
		)
		return output, nil
	}

	selectors := opts.PaneSelectors
	if len(selectors) == 0 && len(opts.PaneIndices) > 0 {
		selectors = make([]string, 0, len(opts.PaneIndices))
		for _, idx := range opts.PaneIndices {
			selectors = append(selectors, strconv.Itoa(idx))
		}
	}

	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	scanned := tmux.SortPanesByTopology(panes)
	if len(selectors) > 0 {
		// Shared N / W.P / %N resolution: a malformed or unknown selector fails
		// the scan instead of silently watching fewer panes than requested.
		scanned, err = tmux.ResolvePaneSelectors(panes, selectors, false)
		if err != nil {
			output.RobotResponse = NewErrorResponse(
				err,
				paneSelectorRobotErrorCode(err),
				"Use comma-separated N, W.P, or %N pane selectors, e.g. --panes=1,2.0,%7",
			)
			return output, nil
		}
	}

	for _, pane := range scanned {
		agentType := detectAgentTypeFromPane(pane)
		if agentType == "user" {
			continue
		}

		captured, captureErr := tmux.CapturePaneOutput(pane.ID, opts.Lines)
		if captureErr != nil {
			continue
		}

		lines := splitLines(stripANSI(captured))
		matches := findBeadMentionMatches(lines, mentionRE)
		for _, match := range matches {
			output.Mentions = append(output.Mentions, BeadMention{
				Pane:      pane.Index,
				PaneRef:   tmux.PaneTargetKey(pane, multiWindow),
				PaneID:    pane.ID,
				AgentType: agentType,
				Line:      match.Line,
				LineNum:   match.LineNum,
				Timestamp: output.CheckedAt,
			})
		}
		output.PanesScanned++
	}

	// Mentions are appended in topology order (window, pane, then line), so
	// no re-sort is needed; sorting on the window-local pane index alone would
	// interleave panes from different windows.

	status, statusErr := bv.GetBeadStatus("", output.BeadID)
	if statusErr != nil {
		output.StatusError = statusErr.Error()
	} else {
		output.BeadStatus = status
	}

	return output, nil
}

func compileBeadMentionPattern(beadID string) (*regexp.Regexp, error) {
	id := strings.TrimSpace(beadID)
	if id == "" {
		return nil, fmt.Errorf("bead ID is required")
	}
	return regexp.Compile(`(?i)\b` + regexp.QuoteMeta(id) + `\b`)
}

func findBeadMentionMatches(lines []string, mentionRE *regexp.Regexp) []beadMentionMatch {
	matches := make([]beadMentionMatch, 0)
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if mentionRE.MatchString(trimmed) {
			matches = append(matches, beadMentionMatch{
				Line:    trimmed,
				LineNum: idx + 1,
			})
		}
	}
	return matches
}
