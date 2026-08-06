//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/history"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// This suite intentionally crosses the process boundary. It runs the binary
// built by TestMain against an isolated, real tmux server and verifies the
// canonical N, W.P, and %N pane-address contract on every robot surface that
// accepts it. The private tmux server prevents ambient sessions and tmux
// options from changing selector meaning during parallel CI runs.

type canonicalPaneEndpoint struct {
	Address string
	ID      string
	Title   string
	Type    tmux.AgentType
}

type canonicalPaneFixture struct {
	t           *testing.T
	ntmPath     string
	tmuxPath    string
	session     string
	runtimeRoot string
	env         []string
	panes       map[string]canonicalPaneEndpoint
}

type robotProcessResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type processEnvelope struct {
	Success      bool   `json:"success"`
	Timestamp    string `json:"timestamp"`
	Error        string `json:"error,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Hint         string `json:"hint,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
}

type sendProcessOutput struct {
	processEnvelope
	Session    string   `json:"session"`
	Targets    []string `json:"targets"`
	Successful []string `json:"successful"`
	Failed     []struct {
		Pane  string `json:"pane"`
		Error string `json:"error"`
	} `json:"failed"`
}

type cliSendProcessOutput struct {
	Success   bool     `json:"success"`
	Session   string   `json:"session"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type replayProcessOutput struct {
	Success   bool     `json:"success"`
	Timestamp string   `json:"timestamp"`
	Session   string   `json:"session"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	DryRun    bool     `json:"dry_run"`
	Warnings  []string `json:"warnings"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type projectSendSessionOutput struct {
	Session   string   `json:"session"`
	Success   bool     `json:"success"`
	Targets   []string `json:"targets"`
	Delivered int      `json:"delivered"`
	Failed    int      `json:"failed"`
	ErrorCode string   `json:"error_code,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type projectSendProcessOutput struct {
	Success           bool                       `json:"success"`
	GeneratedAt       string                     `json:"generated_at"`
	Project           string                     `json:"project"`
	Sessions          []projectSendSessionOutput `json:"sessions"`
	MatchedSessions   int                        `json:"matched_sessions"`
	SucceededSessions int                        `json:"succeeded_sessions"`
	FailedSessions    int                        `json:"failed_sessions"`
	Delivered         int                        `json:"delivered"`
	FailedDeliveries  int                        `json:"failed_deliveries"`
	ErrorCode         string                     `json:"error_code,omitempty"`
	Error             string                     `json:"error,omitempty"`
}

type scaleProcessOutput struct {
	Success bool           `json:"success"`
	Session string         `json:"session"`
	Before  map[string]int `json:"before"`
	After   map[string]int `json:"after"`
	Actions []struct {
		ActionType string   `json:"type"`
		AgentType  string   `json:"agent_type"`
		Count      int      `json:"count"`
		Agents     []string `json:"agents"`
	} `json:"actions"`
	Errors    []string `json:"errors,omitempty"`
	ErrorCode string   `json:"error_code,omitempty"`
}

type tailPaneOutput struct {
	State                 string   `json:"state"`
	Lines                 []string `json:"lines"`
	CaptureProvenance     string   `json:"capture_provenance"`
	ObservationState      string   `json:"observation_state"`
	ObservationFreshness  string   `json:"observation_freshness"`
	LastKnownState        string   `json:"last_known_state,omitempty"`
	LastKnownObservedAt   string   `json:"last_known_observed_at,omitempty"`
	ObservationSafeToSend bool     `json:"safe_to_dispatch"`
}

type tailProcessOutput struct {
	processEnvelope
	Session string                    `json:"session"`
	Panes   map[string]tailPaneOutput `json:"panes"`
}

type isWorkingPaneOutput struct {
	ObservationState      string `json:"observation_state"`
	ObservationFreshness  string `json:"observation_freshness"`
	ObservationObservedAt string `json:"observation_observed_at"`
	ObservationError      string `json:"observation_error,omitempty"`
	LastKnownState        string `json:"last_known_state,omitempty"`
	LastKnownObservedAt   string `json:"last_known_observed_at,omitempty"`
	SafeToDispatch        bool   `json:"safe_to_dispatch"`
}

type isWorkingProcessOutput struct {
	processEnvelope
	Session string                         `json:"session"`
	Panes   map[string]isWorkingPaneOutput `json:"panes"`
}

type activityAgentOutput struct {
	Pane                  string `json:"pane"`
	CaptureProvenance     string `json:"capture_provenance"`
	ObservationState      string `json:"observation_state"`
	ObservationFreshness  string `json:"observation_freshness"`
	LastKnownState        string `json:"last_known_state,omitempty"`
	LastKnownObservedAt   string `json:"last_known_observed_at,omitempty"`
	ObservationSafeToSend bool   `json:"safe_to_dispatch"`
}

type activityProcessOutput struct {
	processEnvelope
	Session string                `json:"session"`
	Agents  []activityAgentOutput `json:"agents"`
	Summary struct {
		TotalAgents int `json:"total_agents"`
	} `json:"summary"`
	SourceHealth map[string]struct {
		Status     string `json:"status"`
		Provenance string `json:"provenance"`
	} `json:"source_health"`
}

type agentHealthPaneOutput struct {
	LocalState isWorkingPaneOutput `json:"local_state"`
}

type agentHealthProcessOutput struct {
	processEnvelope
	Session string                           `json:"session"`
	Panes   map[string]agentHealthPaneOutput `json:"panes"`
}

type smartRestartProcessOutput struct {
	processEnvelope
	Session string `json:"session"`
	DryRun  bool   `json:"dry_run"`
	Actions map[string]struct {
		Action          string `json:"action"`
		Error           string `json:"error,omitempty"`
		StructuredError *struct {
			Code  string `json:"code"`
			Phase string `json:"phase"`
		} `json:"structured_error,omitempty"`
		PromptError *struct {
			Code  string `json:"code"`
			Phase string `json:"phase"`
		} `json:"prompt_error,omitempty"`
		RestartSequence *struct {
			AgentLaunched bool `json:"agent_launched"`
			PromptSent    bool `json:"prompt_sent"`
			PromptOutcome *struct {
				Status        string `json:"status"`
				Target        string `json:"target"`
				Delivered     int    `json:"delivered"`
				Failed        int    `json:"failed"`
				Blocked       int    `json:"blocked"`
				Skipped       int    `json:"skipped"`
				ReceiptStatus string `json:"receipt_status"`
				DispatchCode  string `json:"dispatch_code"`
			} `json:"prompt_outcome,omitempty"`
		} `json:"restart_sequence,omitempty"`
	} `json:"actions"`
	Summary struct {
		Restarted              int                 `json:"restarted"`
		Skipped                int                 `json:"skipped"`
		Waiting                int                 `json:"waiting"`
		Failed                 int                 `json:"failed"`
		PromptDelivered        int                 `json:"prompt_delivered"`
		PromptFailed           int                 `json:"prompt_failed"`
		PanesWithPromptFailure []string            `json:"panes_with_prompt_failure"`
		PanesByAction          map[string][]string `json:"panes_by_action"`
	} `json:"summary"`
}

type waitProcessOutput struct {
	processEnvelope
	Session           string   `json:"session"`
	Condition         string   `json:"condition"`
	AgentsPending     []string `json:"agents_pending"`
	ConvergenceStreak int      `json:"convergence_streak"`
	Productivity      *struct {
		Decision         string `json:"decision"`
		EvidenceComplete bool   `json:"evidence_complete"`
		ReadyBeadCount   int    `json:"ready_bead_count"`
	} `json:"productivity,omitempty"`
}

type ackProcessOutput struct {
	processEnvelope
	Session       string `json:"session"`
	Confirmations []struct {
		Pane    string `json:"pane"`
		AckType string `json:"ack_type"`
	} `json:"confirmations"`
	Pending  []string `json:"pending"`
	TimedOut bool     `json:"timed_out"`
}

type sendAndAckProcessOutput struct {
	processEnvelope
	Send sendProcessOutput `json:"send"`
	Ack  ackProcessOutput  `json:"ack"`
}

type historyProcessOutput struct {
	processEnvelope
	Session  string                 `json:"session"`
	Entries  []history.HistoryEntry `json:"entries"`
	Total    int                    `json:"total"`
	Filtered int                    `json:"filtered"`
}

type interruptProcessOutput struct {
	processEnvelope
	Session        string               `json:"session"`
	Interrupted    []string             `json:"interrupted"`
	PreviousStates map[string]paneState `json:"previous_states"`
	MessageSent    bool                 `json:"message_sent"`
	ReadyForInput  []string             `json:"ready_for_input"`
	Failed         []struct {
		Pane   string `json:"pane"`
		Reason string `json:"reason"`
	} `json:"failed"`
}

type paneState struct {
	State                 string  `json:"state"`
	ObservationFreshness  string  `json:"observation_freshness"`
	ObservationConfidence float64 `json:"observation_confidence"`
	ObservedAt            string  `json:"observed_at"`
	ObservationError      string  `json:"observation_error,omitempty"`
	LastKnownState        string  `json:"last_known_state,omitempty"`
	LastKnownObservedAt   string  `json:"last_known_observed_at,omitempty"`
}

type selectorCommand struct {
	name string
	args func(selector string) []string
}

func canonicalSelectorCommands(fixture *canonicalPaneFixture) []selectorCommand {
	return []selectorCommand{
		{
			name: "activity",
			args: func(selector string) []string {
				return []string{"--robot-activity=" + fixture.session, "--panes=" + selector}
			},
		},
		{
			name: "send",
			args: func(selector string) []string {
				return []string{"--robot-send=" + fixture.session, "--panes=" + selector, "--msg=echo unreachable"}
			},
		},
		{
			name: "tail",
			args: func(selector string) []string {
				return []string{"--robot-tail=" + fixture.session, "--panes=" + selector, "--lines=20"}
			},
		},
		{
			name: "wait",
			args: func(selector string) []string {
				return []string{"--robot-wait=" + fixture.session, "--panes=" + selector, "--wait-until=rate_limited", "--timeout=200ms", "--poll=25ms"}
			},
		},
		{
			name: "is_working",
			args: func(selector string) []string {
				return []string{"--robot-is-working=" + fixture.session, "--panes=" + selector}
			},
		},
		{
			name: "agent_health",
			args: func(selector string) []string {
				return []string{"--robot-agent-health=" + fixture.session, "--panes=" + selector, "--no-caut"}
			},
		},
		{
			name: "ack",
			args: func(selector string) []string {
				return []string{"--robot-ack=" + fixture.session, "--panes=" + selector, "--timeout=200ms", "--poll=25ms"}
			},
		},
		{
			name: "history",
			args: func(selector string) []string {
				return []string{"--robot-history=" + fixture.session, "--pane=" + selector}
			},
		},
		{
			name: "smart_restart",
			args: func(selector string) []string {
				return []string{"--robot-smart-restart=" + fixture.session, "--panes=" + selector, "--force", "--dry-run"}
			},
		},
		{
			name: "interrupt",
			args: func(selector string) []string {
				return []string{"--robot-interrupt=" + fixture.session, "--panes=" + selector, "--dry-run"}
			},
		},
	}
}

func TestE2ECanonicalPaneContract(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newCanonicalPaneFixture(t)

	t.Run("status_json_preserves_multi_window_physical_identity", func(t *testing.T) {
		result := fixture.runNTM(t, nil, "--json", "status", fixture.session)
		var response struct {
			Exists bool `json:"exists"`
			Panes  []struct {
				PaneID      string `json:"pane_id"`
				PaneTarget  string `json:"pane_target"`
				WindowIndex int    `json:"window_index"`
				Index       int    `json:"index"`
			} `json:"panes"`
		}
		decodeCLIJSONSuccess(t, result, &response)
		if !response.Exists || len(response.Panes) != len(fixture.panes) {
			t.Fatalf("status topology=%+v, want %d panes", response, len(fixture.panes))
		}
		seen := make(map[string]string, len(response.Panes))
		for _, pane := range response.Panes {
			wantTarget := fmt.Sprintf("%d.%d", pane.WindowIndex, pane.Index)
			if pane.PaneTarget != wantTarget || !strings.HasPrefix(pane.PaneID, "%") {
				t.Fatalf("status pane identity=%+v, want target %s and %%N ID", pane, wantTarget)
			}
			seen[pane.PaneTarget] = pane.PaneID
		}
		for target, endpoint := range fixture.panes {
			if seen[target] != endpoint.ID {
				t.Fatalf("status target %s pane_id=%q, want %q; all=%v", target, seen[target], endpoint.ID, seen)
			}
		}
		if seen["0.0"] == seen["1.0"] {
			t.Fatalf("duplicate local index panes collapsed to one identity: %v", seen)
		}
	})

	t.Run("startup_errors_are_single_json_documents", func(t *testing.T) {
		cases := []struct {
			name     string
			args     []string
			extraEnv map[string]string
		}{
			{name: "format_flag", args: []string{"--robot-status", "--robot-format=bogus"}},
			{name: "format_environment", args: []string{"--robot-status"}, extraEnv: map[string]string{"NTM_ROBOT_FORMAT": "bogus"}},
			{name: "verbosity_flag", args: []string{"--robot-status", "--robot-verbosity=bogus"}},
			{name: "verbosity_environment", args: []string{"--robot-status"}, extraEnv: map[string]string{"NTM_ROBOT_VERBOSITY": "bogus"}},
			{name: "redaction_flag", args: []string{"--robot-status", "--redact=bogus"}},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				result := fixture.runNTM(t, test.extraEnv, test.args...)
				assertRobotFailure(t, result, "INVALID_FLAG")
			})
		}
	})

	t.Run("distribute_preflight_errors_are_single_json_documents", func(t *testing.T) {
		cases := []struct {
			name    string
			args    []string
			wantErr string
		}{
			{name: "pane", args: []string{"--pane=0"}, wantErr: "cannot combine --distribute with --pane"},
			{name: "panes", args: []string{"--panes=0,1"}, wantErr: "cannot combine --distribute with --pane"},
			{name: "skip_first", args: []string{"--skip-first"}, wantErr: "cannot combine --distribute with --skip-first"},
			{name: "dry_run_auto", args: []string{"--dry-run", "--dist-auto"}, wantErr: "cannot use --dry-run with --dist-auto"},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				args := []string{"--json", "send", fixture.session, "--distribute"}
				args = append(args, test.args...)
				result := fixture.runNTM(t, nil, args...)
				if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
					t.Fatalf("distribute preflight exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
				}
				var envelope struct {
					Success bool     `json:"success"`
					Session string   `json:"session"`
					Targets []string `json:"targets"`
					Error   string   `json:"error"`
				}
				if err := json.Unmarshal(result.stdout, &envelope); err != nil {
					t.Fatalf("distribute preflight must emit exactly one JSON document: %v; output=%s", err, result.stdout)
				}
				if envelope.Success || envelope.Session != fixture.session || envelope.Targets == nil || len(envelope.Targets) != 0 || !strings.Contains(envelope.Error, test.wantErr) {
					t.Fatalf("distribute preflight envelope=%+v, want error containing %q", envelope, test.wantErr)
				}
			})
		}
	})

	t.Run("send_resolves_aliases_once_and_never_spills", func(t *testing.T) {
		tests := []struct {
			name        string
			selectors   string
			marker      string
			wantTargets []string
		}{
			{
				name:        "window_pane_and_id_are_one_physical_target",
				selectors:   "0.1," + fixture.panes["0.1"].ID,
				marker:      fixture.uniqueMarker("SEND_ALIAS"),
				wantTargets: []string{"0.1"},
			},
			{
				name:        "pane_id_is_exact",
				selectors:   fixture.panes["1.0"].ID,
				marker:      fixture.uniqueMarker("SEND_ID"),
				wantTargets: []string{"1.0"},
			},
			{
				name:        "bare_number_selects_whole_window",
				selectors:   "1",
				marker:      fixture.uniqueMarker("SEND_WINDOW"),
				wantTargets: []string{"1.0", "1.1"},
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				message := shellMarkerCommand(test.marker)
				result := fixture.runRobot(t, nil,
					"--robot-send="+fixture.session,
					"--panes="+test.selectors,
					"--msg="+message,
				)
				var output sendProcessOutput
				decodeRobotSuccess(t, result, &output)
				assertStringSlice(t, "send targets", output.Targets, test.wantTargets)
				assertStringSlice(t, "send successful", output.Successful, test.wantTargets)
				if len(output.Failed) != 0 {
					t.Fatalf("unexpected send failures: %+v", output.Failed)
				}

				fixture.waitForMarkers(t, test.marker, test.wantTargets)
				fixture.assertMarkerOnlyIn(t, test.marker, test.wantTargets)
			})
		}

		t.Run("empty_selector_components_are_rejected_without_actuation", func(t *testing.T) {
			selectors := []struct {
				name  string
				value string
			}{
				{name: "only_comma", value: ","},
				{name: "whitespace_only", value: "   "},
				{name: "leading_comma", value: ",0.1"},
				{name: "trailing_comma", value: "0.1,"},
				{name: "double_comma", value: "0.1,,1.0"},
			}
			for _, selector := range selectors {
				t.Run(selector.name, func(t *testing.T) {
					marker := fixture.uniqueMarker("SEND_EMPTY_SELECTOR")
					result := fixture.runRobot(t, nil,
						"--robot-send="+fixture.session,
						"--panes="+selector.value,
						"--msg="+shellMarkerCommand(marker),
					)
					assertRobotFailure(t, result, "INVALID_FLAG")
					fixture.assertMarkerOnlyIn(t, marker, nil)
				})
			}
		})

		t.Run("empty_target_sets_fail_loudly_without_actuation", func(t *testing.T) {
			cases := []struct {
				name string
				args []string
			}{
				{name: "unmatched_type", args: []string{"--type=aider"}},
				{name: "selected_pane_excluded", args: []string{"--panes=0.1", "--exclude=0.1"}},
			}
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					marker := fixture.uniqueMarker("SEND_NO_TARGETS")
					args := []string{
						"--robot-send=" + fixture.session,
						"--msg=" + shellMarkerCommand(marker),
					}
					args = append(args, test.args...)
					result := fixture.runRobot(t, nil, args...)
					assertTypedRobotFailure(t, result)

					var output sendProcessOutput
					if err := json.Unmarshal(result.stdout, &output); err != nil {
						t.Fatalf("decode no-target send failure: %v; output=%s", err, result.stdout)
					}
					if output.Success || len(output.Targets) != 0 || len(output.Successful) != 0 {
						t.Fatalf("no-target send output = success:%v targets:%v successful:%v, want false and empty target sets", output.Success, output.Targets, output.Successful)
					}
					fixture.assertMarkerOnlyIn(t, marker, nil)
				})
			}
		})

		t.Run("singular_bare_window_is_rejected_as_ambiguous", func(t *testing.T) {
			result := fixture.runRobot(t, nil,
				"--robot-send="+fixture.session,
				"--pane=1",
				"--msg="+shellMarkerCommand(fixture.uniqueMarker("AMBIGUOUS")),
			)
			assertRobotFailure(t, result, "INVALID_FLAG")
		})

		t.Run("track_uses_the_same_exact_targets_and_acknowledges_delivery", func(t *testing.T) {
			marker := fixture.uniqueMarker("SEND_TRACK")
			result := fixture.runRobot(t, nil,
				"--robot-send="+fixture.session,
				"--panes=0.1,"+fixture.panes["0.1"].ID,
				"--msg="+shellMarkerCommand(marker),
				"--track",
				"--timeout=3s",
				"--poll=50ms",
			)
			var output sendAndAckProcessOutput
			decodeRobotSuccess(t, result, &output)
			assertStringSlice(t, "tracked send targets", output.Send.Targets, []string{"0.1"})
			assertStringSlice(t, "tracked send successful", output.Send.Successful, []string{"0.1"})
			if len(output.Ack.Confirmations) != 1 || output.Ack.Confirmations[0].Pane != "0.1" {
				t.Fatalf("tracked ack confirmations = %+v, want one confirmation for 0.1", output.Ack.Confirmations)
			}
			if output.Ack.TimedOut || len(output.Ack.Pending) != 0 {
				t.Fatalf("tracked ack unexpectedly pending/timed out: pending=%v timed_out=%v", output.Ack.Pending, output.Ack.TimedOut)
			}
			fixture.waitForMarkers(t, marker, []string{"0.1"})
			fixture.assertMarkerOnlyIn(t, marker, []string{"0.1"})
		})
	})

	t.Run("send_json_emits_one_truthful_terminal_document", func(t *testing.T) {
		t.Run("direct_no_matching_panes", func(t *testing.T) {
			result := fixture.runNTM(t, nil,
				"--json",
				"send",
				fixture.session,
				"--tag=ntm-e2e-never-matches",
				"--no-cass-check",
				"--no-hooks",
				"no matching pane contract",
			)
			var output cliSendProcessOutput
			decodeCLIJSONFailure(t, result, &output)
			if output.Success || output.Session != fixture.session || output.ErrorCode != "NO_MATCHING_PANES" || output.Error == "" {
				t.Fatalf("no-target send output = %+v", output)
			}
			if output.Targets == nil || len(output.Targets) != 0 || output.Delivered != 0 || output.Failed != 0 {
				t.Fatalf("no-target delivery summary = %+v, want non-nil empty targets and zero counts", output)
			}
		})

		failingTmux := fixture.writeFailingTmuxWrapper(t)

		t.Run("direct_partial_delivery_failure", func(t *testing.T) {
			result := fixture.runNTM(t, map[string]string{
				"NTM_TMUX_BINARY":      failingTmux,
				"NTM_E2E_REAL_TMUX":    fixture.tmuxPath,
				"NTM_E2E_FAIL_PANE_ID": fixture.panes["0.1"].ID,
			},
				"--json",
				"send",
				fixture.session,
				"--all",
				"--no-cass-check",
				"--no-hooks",
				shellMarkerCommand(fixture.uniqueMarker("CLI_SEND_PARTIAL")),
			)
			var output cliSendProcessOutput
			decodeCLIJSONFailure(t, result, &output)
			if output.Success || output.Session != fixture.session || output.ErrorCode != "SEND_FAILED" || output.Error == "" {
				t.Fatalf("partial send output = %+v", output)
			}
			if output.Targets == nil || len(output.Targets) != len(fixture.panes) || output.Delivered == 0 || output.Failed == 0 {
				t.Fatalf("partial send delivery summary = %+v, want both delivered and failed panes", output)
			}
		})

		t.Run("project_aggregate_is_sorted_and_failure_aware", func(t *testing.T) {
			project := fixture.session + "-project"
			sessionA := project + "--a"
			sessionB := project + "--b"
			// Create sessions out of order, then make the lexicographically second fail.
			failedPaneID := fixture.createSinglePaneAgentSession(t, sessionB)
			fixture.createSinglePaneAgentSession(t, sessionA)

			successResult := fixture.runNTM(t, nil,
				"--json",
				"send",
				"--project="+project,
				"--all",
				"--no-cass-check",
				"--no-hooks",
				shellMarkerCommand(fixture.uniqueMarker("PROJECT_SEND_SUCCESS")),
			)
			var successOutput projectSendProcessOutput
			decodeCLIJSONSuccess(t, successResult, &successOutput)
			if !successOutput.Success || successOutput.GeneratedAt == "" || successOutput.Project != project || successOutput.ErrorCode != "" || successOutput.Error != "" {
				t.Fatalf("successful project send aggregate = %+v", successOutput)
			}
			if successOutput.MatchedSessions != 2 || successOutput.SucceededSessions != 2 || successOutput.FailedSessions != 0 || successOutput.Delivered != 2 || successOutput.FailedDeliveries != 0 {
				t.Fatalf("successful project send counts = %+v", successOutput)
			}
			if len(successOutput.Sessions) != 2 || successOutput.Sessions[0].Session != sessionA || successOutput.Sessions[1].Session != sessionB || !successOutput.Sessions[0].Success || !successOutput.Sessions[1].Success {
				t.Fatalf("successful project session receipts = %+v", successOutput.Sessions)
			}

			result := fixture.runNTM(t, map[string]string{
				"NTM_TMUX_BINARY":      failingTmux,
				"NTM_E2E_REAL_TMUX":    fixture.tmuxPath,
				"NTM_E2E_FAIL_PANE_ID": failedPaneID,
			},
				"--json",
				"send",
				"--project="+project,
				"--all",
				"--no-cass-check",
				"--no-hooks",
				shellMarkerCommand(fixture.uniqueMarker("PROJECT_SEND")),
			)
			var output projectSendProcessOutput
			decodeCLIJSONFailure(t, result, &output)
			if output.Success || output.GeneratedAt == "" || output.Project != project || output.ErrorCode != "SEND_FAILED" || output.Error == "" {
				t.Fatalf("project send aggregate = %+v", output)
			}
			if output.MatchedSessions != 2 || output.SucceededSessions != 1 || output.FailedSessions != 1 || output.Delivered != 1 || output.FailedDeliveries != 1 {
				t.Fatalf("project send counts = %+v", output)
			}
			if len(output.Sessions) != 2 || output.Sessions[0].Session != sessionA || output.Sessions[1].Session != sessionB {
				t.Fatalf("project session order = %+v, want [%s %s]", output.Sessions, sessionA, sessionB)
			}
			if !output.Sessions[0].Success || output.Sessions[0].Delivered != 1 || output.Sessions[0].Failed != 0 {
				t.Fatalf("successful project receipt = %+v", output.Sessions[0])
			}
			if output.Sessions[1].Success || output.Sessions[1].Delivered != 0 || output.Sessions[1].Failed != 1 || output.Sessions[1].ErrorCode != "SEND_FAILED" || output.Sessions[1].Error == "" {
				t.Fatalf("failed project receipt = %+v", output.Sessions[1])
			}
			for _, session := range output.Sessions {
				if session.Targets == nil {
					t.Fatalf("project receipt %s omitted targets", session.Session)
				}
			}
		})
	})

	t.Run("scale_json_owns_nested_add_failure", func(t *testing.T) {
		session := fmt.Sprintf("ntm-e2e-scale-json-%d-%d", os.Getpid(), time.Now().UnixNano())
		fixture.createSinglePaneAgentSession(t, session)

		wrapperPath := filepath.Join(fixture.runtimeRoot, "bin", "tmux-fail-scale-split")
		wrapper := `#!/bin/sh
if [ "${1:-}" = "split-window" ]; then
    printf 'injected scale split failure\n' >&2
    exit 92
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
		if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
			t.Fatalf("write scale tmux wrapper: %v", err)
		}

		result := fixture.runNTM(t, map[string]string{
			"NTM_TMUX_BINARY":   wrapperPath,
			"NTM_E2E_REAL_TMUX": fixture.tmuxPath,
		}, "--json", "scale", session, "--cc=2")
		if result.exitCode != 1 || len(bytes.TrimSpace(result.stderr)) != 0 {
			t.Fatalf("scale nested-add failure exit=%d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
		}

		var output scaleProcessOutput
		if err := json.Unmarshal(result.stdout, &output); err != nil {
			t.Fatalf("scale failure must be one JSON document: %v; output=%s", err, result.stdout)
		}
		if output.Success || output.Session != session || output.ErrorCode != "INTERNAL_ERROR" || len(output.Errors) == 0 {
			t.Fatalf("scale failure output=%+v", output)
		}
		if output.Before["cc"] != 1 || output.After["cc"] != 1 || len(output.Actions) != 1 ||
			output.Actions[0].ActionType != "spawn" || output.Actions[0].AgentType != "cc" || output.Actions[0].Count != 1 {
			t.Fatalf("scale failure state/action=%+v", output)
		}
		if output.Actions[0].Agents == nil || len(output.Actions[0].Agents) != 0 {
			t.Fatalf("scale spawn action agents=%v, want checked-empty []", output.Actions[0].Agents)
		}
		if bytes.Contains(result.stdout, []byte(`"added_claude"`)) || bytes.Contains(result.stdout, []byte(`"total_added"`)) {
			t.Fatalf("scale leaked nested AddResponse: %s", result.stdout)
		}
	})

	t.Run("scale_final_layout_failure_reports_partial_state_without_late_mutation", func(t *testing.T) {
		session := fmt.Sprintf("ntm-e2e-scale-layout-fail-%d-%d", os.Getpid(), time.Now().UnixNano())
		fixture.createSinglePaneAgentSession(t, session)
		wrapperPath, stateRoot := fixture.writeScaleLayoutWrapper(t, "fail")

		result := fixture.runNTM(t, map[string]string{
			"NTM_TMUX_BINARY":            wrapperPath,
			"NTM_E2E_REAL_TMUX":          fixture.tmuxPath,
			"NTM_E2E_SCALE_LAYOUT_MODE":  "fail",
			"NTM_E2E_SCALE_LAYOUT_STATE": stateRoot,
		}, "--json", "scale", session, "--cc=2")

		var output scaleProcessOutput
		decodeCLIJSONFailure(t, result, &output)
		if output.Success || output.Session != session || output.ErrorCode != "INTERNAL_ERROR" || len(output.Errors) != 1 ||
			!strings.Contains(output.Errors[0], "apply tiled layout") || !strings.Contains(output.Errors[0], "injected final layout failure") {
			t.Fatalf("final-layout failure output=%+v", output)
		}
		assertScaleUpPartialResponse(t, output)

		if _, err := os.Stat(filepath.Join(stateRoot, "final-layout-started")); err != nil {
			t.Fatalf("final layout failure was not reached: %v", err)
		}
		beforeWait := fixture.sessionPaneSnapshot(t, session)
		assertScaleUpPaneSnapshot(t, session, beforeWait)
		logBeforeWait, err := os.ReadFile(filepath.Join(stateRoot, "commands.log"))
		if err != nil {
			t.Fatalf("read final-layout failure command log: %v", err)
		}
		if got := strings.Count(string(logBeforeWait), "select-layout"); got != 2 {
			t.Fatalf("final-layout failure select-layout calls=%d, want split plus final; log=%s", got, logBeforeWait)
		}

		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		afterWait := fixture.sessionPaneSnapshot(t, session)
		if !reflect.DeepEqual(afterWait, beforeWait) {
			t.Fatalf("scale mutated panes after terminal failure: before=%v after=%v", beforeWait, afterWait)
		}
		logAfterWait, err := os.ReadFile(filepath.Join(stateRoot, "commands.log"))
		if err != nil {
			t.Fatalf("reread final-layout failure command log: %v", err)
		}
		if !bytes.Equal(logAfterWait, logBeforeWait) {
			t.Fatalf("scale issued tmux commands after terminal failure: before=%q after=%q", logBeforeWait, logAfterWait)
		}
	})

	t.Run("scale_sigint_during_final_layout_reports_timeout_without_late_side_effects", func(t *testing.T) {
		session := fmt.Sprintf("ntm-e2e-scale-layout-cancel-%d-%d", os.Getpid(), time.Now().UnixNano())
		fixture.createSinglePaneAgentSession(t, session)
		wrapperPath, stateRoot := fixture.writeScaleLayoutWrapper(t, "block")

		cmd := exec.Command(fixture.ntmPath, "--json", "scale", session, "--cc=2")
		cmd.Env = mergeProcessEnv(fixture.env, map[string]string{
			"NTM_TMUX_BINARY":            wrapperPath,
			"NTM_E2E_REAL_TMUX":          fixture.tmuxPath,
			"NTM_E2E_SCALE_LAYOUT_MODE":  "block",
			"NTM_E2E_SCALE_LAYOUT_STATE": stateRoot,
		})
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatalf("start scale cancellation process: %v", err)
		}
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		finished := false
		defer func() {
			if !finished {
				_ = cmd.Process.Kill()
				<-waited
			}
		}()

		startedPath := filepath.Join(stateRoot, "final-layout-started")
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(startedPath); err == nil {
				break
			}
			select {
			case waitErr := <-waited:
				finished = true
				t.Fatalf("scale exited before final layout could be interrupted: %v; stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("scale did not reach final layout; stdout=%s stderr=%s", stdout.String(), stderr.String())
			}
			time.Sleep(25 * time.Millisecond)
		}

		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("interrupt scale during final layout: %v", err)
		}
		var waitErr error
		select {
		case waitErr = <-waited:
			finished = true
		case <-time.After(10 * time.Second):
			t.Fatal("scale did not return after final-layout cancellation")
		}
		if waitErr == nil {
			t.Fatalf("canceled scale exited successfully: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("canceled scale returned no process status: %v", waitErr)
		}

		var output scaleProcessOutput
		decodeCLIJSONFailure(t, robotProcessResult{
			stdout:   stdout.Bytes(),
			stderr:   stderr.Bytes(),
			exitCode: exitErr.ExitCode(),
		}, &output)
		if output.Success || output.Session != session || output.ErrorCode != "TIMEOUT" || len(output.Errors) != 1 ||
			!strings.Contains(output.Errors[0], "apply tiled layout") || !strings.Contains(output.Errors[0], "context canceled") {
			t.Fatalf("final-layout cancellation output=%+v", output)
		}
		assertScaleUpPartialResponse(t, output)

		beforeWait := fixture.sessionPaneSnapshot(t, session)
		assertScaleUpPaneSnapshot(t, session, beforeWait)
		logBeforeWait, err := os.ReadFile(filepath.Join(stateRoot, "commands.log"))
		if err != nil {
			t.Fatalf("read final-layout cancellation command log: %v", err)
		}
		if got := strings.Count(string(logBeforeWait), "select-layout"); got != 2 {
			t.Fatalf("final-layout cancellation select-layout calls=%d, want split plus final; log=%s", got, logBeforeWait)
		}

		time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
		afterWait := fixture.sessionPaneSnapshot(t, session)
		if !reflect.DeepEqual(afterWait, beforeWait) {
			t.Fatalf("scale mutated panes after cancellation response: before=%v after=%v", beforeWait, afterWait)
		}
		logAfterWait, err := os.ReadFile(filepath.Join(stateRoot, "commands.log"))
		if err != nil {
			t.Fatalf("reread final-layout cancellation command log: %v", err)
		}
		if !bytes.Equal(logAfterWait, logBeforeWait) {
			t.Fatalf("scale issued tmux commands after cancellation response: before=%q after=%q", logBeforeWait, logAfterWait)
		}
	})

	t.Run("tail_uses_canonical_keys_and_deduplicates_aliases", func(t *testing.T) {
		marker := fixture.uniqueMarker("TAIL")
		fixture.sendPaneCommand(t, fixture.panes["0.1"].ID, shellMarkerCommand(marker))
		fixture.waitForMarkers(t, marker, []string{"0.1"})

		tests := []struct {
			name      string
			selectors string
			wantKeys  []string
		}{
			{name: "alias_dedup", selectors: "0.1," + fixture.panes["0.1"].ID, wantKeys: []string{"0.1"}},
			{name: "exact_id", selectors: fixture.panes["1.0"].ID, wantKeys: []string{"1.0"}},
			{name: "whole_window", selectors: "1", wantKeys: []string{"1.0", "1.1"}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result := fixture.runRobot(t, nil,
					"--robot-tail="+fixture.session,
					"--panes="+test.selectors,
					"--lines=100",
				)
				var output tailProcessOutput
				decodeRobotSuccess(t, result, &output)
				assertStringSlice(t, "tail pane keys", sortedMapKeys(output.Panes), test.wantKeys)
				for key, pane := range output.Panes {
					if pane.CaptureProvenance != "live" {
						t.Fatalf("pane %s capture provenance = %q, want live", key, pane.CaptureProvenance)
					}
					if pane.ObservationFreshness != "fresh" || pane.ObservationState == "" {
						t.Fatalf("pane %s tail observation = %+v, want fresh classified observation", key, pane)
					}
					if pane.ObservationState != "unknown" && (pane.LastKnownState != "" || pane.LastKnownObservedAt != "") {
						t.Fatalf("pane %s tail duplicated fresh known state into last-known fields: %+v", key, pane)
					}
				}
			})
		}
		if !tailLinesContainExact(outputForTail(t, fixture, "0.1,"+fixture.panes["0.1"].ID).Panes["0.1"].Lines, marker) {
			t.Fatalf("tail for 0.1 did not contain exact marker %q", marker)
		}
	})

	t.Run("observation_consumers_share_exact_topology", func(t *testing.T) {
		selectors := "0.1," + fixture.panes["0.1"].ID

		t.Run("activity", func(t *testing.T) {
			tests := []struct {
				name      string
				selectors string
				wantPanes []string
			}{
				{name: "alias_dedup", selectors: selectors, wantPanes: []string{"0.1"}},
				{name: "bare_window", selectors: "1", wantPanes: []string{"1.0", "1.1"}},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					result := fixture.runRobot(t, nil,
						"--robot-activity="+fixture.session,
						"--panes="+test.selectors,
					)
					var output activityProcessOutput
					decodeRobotSuccess(t, result, &output)
					if output.Summary.TotalAgents != len(test.wantPanes) || len(output.Agents) != len(test.wantPanes) {
						t.Fatalf("activity total/agents = %d/%d, want %d", output.Summary.TotalAgents, len(output.Agents), len(test.wantPanes))
					}
					gotPanes := make([]string, 0, len(output.Agents))
					for _, pane := range output.Agents {
						gotPanes = append(gotPanes, pane.Pane)
						if pane.CaptureProvenance != "live" || pane.ObservationFreshness != "fresh" || pane.ObservationState == "" {
							t.Fatalf("activity pane %s observation = %+v, want fresh live observation", pane.Pane, pane)
						}
						if pane.ObservationState != "unknown" && (pane.LastKnownState != "" || pane.LastKnownObservedAt != "") {
							t.Fatalf("activity pane %s duplicated fresh known state into last-known fields: %+v", pane.Pane, pane)
						}
					}
					sort.Strings(gotPanes)
					assertStringSlice(t, "activity panes", gotPanes, test.wantPanes)
					if source := output.SourceHealth["tmux"]; source.Status != "fresh" || source.Provenance != "live" {
						t.Fatalf("activity tmux source health = %+v, want fresh/live", source)
					}
				})
			}
		})

		t.Run("is_working", func(t *testing.T) {
			result := fixture.runRobot(t, nil,
				"--robot-is-working="+fixture.session,
				"--panes="+selectors,
				"--lines=100",
			)
			var output isWorkingProcessOutput
			decodeRobotSuccess(t, result, &output)
			assertStringSlice(t, "is-working pane keys", sortedMapKeys(output.Panes), []string{"0.1"})
			assertFreshObservation(t, "is-working 0.1", output.Panes["0.1"])
		})

		t.Run("is_working_bare_window", func(t *testing.T) {
			result := fixture.runRobot(t, nil,
				"--robot-is-working="+fixture.session,
				"--panes=1",
				"--lines=100",
			)
			var output isWorkingProcessOutput
			decodeRobotSuccess(t, result, &output)
			assertStringSlice(t, "is-working window pane keys", sortedMapKeys(output.Panes), []string{"1.0", "1.1"})
			for key, pane := range output.Panes {
				assertFreshObservation(t, "is-working "+key, pane)
			}
		})

		t.Run("agent_health", func(t *testing.T) {
			result := fixture.runRobot(t, nil,
				"--robot-agent-health="+fixture.session,
				"--panes="+selectors,
				"--no-caut",
				"--lines=100",
			)
			var output agentHealthProcessOutput
			decodeRobotSuccess(t, result, &output)
			assertStringSlice(t, "agent-health pane keys", sortedMapKeys(output.Panes), []string{"0.1"})
			assertFreshObservation(t, "agent-health 0.1", output.Panes["0.1"].LocalState)
		})

		t.Run("smart_restart_dry_run", func(t *testing.T) {
			marker := fixture.uniqueMarker("SMART_RESTART_DRY_RUN")
			result := fixture.runRobot(t, nil,
				"--robot-smart-restart="+fixture.session,
				"--panes="+selectors,
				"--force",
				"--dry-run",
				"--prompt="+marker,
			)
			var output smartRestartProcessOutput
			decodeRobotSuccess(t, result, &output)
			if !output.DryRun {
				t.Fatal("smart-restart did not report dry_run=true")
			}
			assertStringSlice(t, "smart-restart action keys", sortedMapKeys(output.Actions), []string{"0.1"})
			if output.Actions["0.1"].Action != "WOULD_RESTART" {
				t.Fatalf("smart-restart action = %q, want WOULD_RESTART", output.Actions["0.1"].Action)
			}
			assertStringSlice(t, "smart-restart panes_by_action", output.Summary.PanesByAction["WOULD_RESTART"], []string{"0.1"})
			fixture.assertMarkerOnlyIn(t, marker, nil)
		})

		t.Run("smart_restart_dispatches_prompt_once_after_real_ready_gate", func(t *testing.T) {
			targetAddress := "0.1"
			fixture.sendPaneCommand(t, fixture.panes[targetAddress].ID, "stty -echo")
			readyMarker := fixture.uniqueMarker("READY_SMART_RESTART")
			fixture.sendPaneCommand(t, fixture.panes[targetAddress].ID, shellMarkerCommand(readyMarker))
			fixture.waitForMarkers(t, readyMarker, []string{targetAddress})

			promptMarker := fixture.uniqueMarker("SMART_RESTART_PROMPT")
			result := fixture.runRobot(t, nil,
				"--robot-smart-restart="+fixture.session,
				"--panes="+targetAddress+","+fixture.panes[targetAddress].ID,
				"--force",
				"--prompt="+shellMarkerCommand(promptMarker),
			)
			var output smartRestartProcessOutput
			decodeRobotSuccess(t, result, &output)
			assertStringSlice(t, "real smart-restart action keys", sortedMapKeys(output.Actions), []string{targetAddress})
			action := output.Actions[targetAddress]
			if action.Action != "RESTARTED" || action.RestartSequence == nil {
				t.Fatalf("real smart-restart action = %+v, want RESTARTED with sequence", action)
			}
			if !action.RestartSequence.AgentLaunched || !action.RestartSequence.PromptSent {
				t.Fatalf("real smart-restart sequence = %+v, want launched and prompt sent", action.RestartSequence)
			}
			if action.RestartSequence.PromptOutcome == nil || action.RestartSequence.PromptOutcome.Status != "delivered" {
				t.Fatalf("real smart-restart prompt outcome = %+v, want delivered", action.RestartSequence.PromptOutcome)
			}
			assertStringSlice(t, "real smart-restart panes_by_action", output.Summary.PanesByAction["RESTARTED"], []string{targetAddress})
			fixture.waitForMarkers(t, promptMarker, []string{targetAddress})
			fixture.assertMarkerOnlyIn(t, promptMarker, []string{targetAddress})
		})

		t.Run("smart_restart_redaction_block_preserves_restart_and_never_types_prompt", func(t *testing.T) {
			targetAddress := "0.1"
			blockedPrompt := "password=NTM_E2E_BLOCKED_SECRET_123456789"
			result := fixture.runRobot(t, nil,
				"--robot-smart-restart="+fixture.session,
				"--panes="+targetAddress+","+fixture.panes[targetAddress].ID,
				"--force",
				"--prompt="+blockedPrompt,
				"--redact=block",
			)
			var output smartRestartProcessOutput
			envelope := assertTypedRobotFailure(t, result)
			if err := json.Unmarshal(result.stdout, &output); err != nil {
				t.Fatalf("decode blocked smart-restart failure: %v; output=%s", err, result.stdout)
			}
			action := output.Actions[targetAddress]
			if envelope.ErrorCode != "PROMPT_SEND_FAILED" {
				t.Fatalf("blocked smart-restart error_code=%q want PROMPT_SEND_FAILED; action=%+v summary=%+v", envelope.ErrorCode, action, output.Summary)
			}
			if action.Action != "RESTARTED" || action.RestartSequence == nil || !action.RestartSequence.AgentLaunched {
				t.Fatalf("blocked smart-restart action = %+v, want completed restart", action)
			}
			if action.RestartSequence.PromptSent || action.RestartSequence.PromptOutcome == nil || action.RestartSequence.PromptOutcome.Status != "blocked" {
				t.Fatalf("blocked smart-restart prompt outcome = %+v", action.RestartSequence)
			}
			if action.PromptError == nil || action.PromptError.Code != "PROMPT_SEND_FAILED" {
				t.Fatalf("blocked smart-restart prompt error = %+v", action.PromptError)
			}
			assertStringSlice(t, "blocked smart-restart panes_by_action", output.Summary.PanesByAction["RESTARTED"], []string{targetAddress})
			fixture.assertMarkerOnlyIn(t, blockedPrompt, nil)
		})

		t.Run("smart_restart_failed_ready_gate_never_types_prompt", func(t *testing.T) {
			targetAddress := "1.1"
			promptMarker := fixture.uniqueMarker("SMART_RESTART_NOT_READY")
			result := fixture.runRobot(t, nil,
				"--robot-smart-restart="+fixture.session,
				"--panes="+targetAddress+","+fixture.panes[targetAddress].ID,
				"--force",
				"--prompt="+promptMarker,
			)
			var output smartRestartProcessOutput
			decodeRobotFailure(t, result, "INTERNAL_ERROR", &output)
			assertStringSlice(t, "failed smart-restart action keys", sortedMapKeys(output.Actions), []string{targetAddress})
			action := output.Actions[targetAddress]
			if action.Action != "FAILED" || !strings.Contains(action.Error, "did not become ready") {
				t.Fatalf("failed smart-restart action = %+v, want ready-gate failure", action)
			}
			fixture.assertMarkerOnlyIn(t, promptMarker, nil)
		})
	})

	t.Run("wait_filters_exact_panes_before_timeout", func(t *testing.T) {
		tests := []struct {
			name      string
			selectors string
			wantIDs   []string
		}{
			{
				name:      "alias_dedup",
				selectors: "0.1," + fixture.panes["0.1"].ID,
				wantIDs:   []string{fixture.panes["0.1"].ID},
			},
			{
				name:      "bare_window",
				selectors: "1",
				wantIDs:   []string{fixture.panes["1.0"].ID, fixture.panes["1.1"].ID},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result := fixture.runRobot(t, nil,
					"--robot-wait="+fixture.session,
					"--panes="+test.selectors,
					"--wait-until=rate_limited",
					"--timeout=300ms",
					"--poll=25ms",
				)
				var output waitProcessOutput
				decodeRobotFailure(t, result, "TIMEOUT", &output)
				sort.Strings(output.AgentsPending)
				sort.Strings(test.wantIDs)
				assertStringSlice(t, "wait pending pane IDs", output.AgentsPending, test.wantIDs)
			})
		}
	})

	t.Run("convergence_wait_returns_productivity_evidence", func(t *testing.T) {
		result := fixture.runRobot(t, nil,
			"--robot-wait="+fixture.session,
			"--wait-until=converged",
			"--productivity-window=1ns",
			"--converged-streak=2",
			"--timeout=300ms",
			"--poll=25ms",
		)
		var output waitProcessOutput
		if err := json.Unmarshal(result.stdout, &output); err != nil {
			t.Fatalf("decode convergence wait response: %v\nstdout=%s\nstderr=%s", err, result.stdout, result.stderr)
		}
		if result.exitCode != 0 && result.exitCode != 1 {
			t.Fatalf("convergence wait exit code = %d, want 0 or 1\nstdout=%s\nstderr=%s", result.exitCode, result.stdout, result.stderr)
		}
		if output.Productivity == nil {
			t.Fatal("convergence wait omitted productivity evidence")
		}
		if output.Success {
			if output.Productivity.Decision != "converged" || output.ConvergenceStreak < 2 {
				t.Fatalf("successful convergence response = %+v, want converged evidence and a two-poll streak", output)
			}
			return
		}
		if output.ErrorCode != "TIMEOUT" || output.Productivity.Decision == "converged" || output.ConvergenceStreak != 0 {
			t.Fatalf("non-converged response = %+v, want timeout with zero streak", output)
		}
	})

	t.Run("standalone_ack_deduplicates_aliases_before_timeout", func(t *testing.T) {
		result := fixture.runRobot(t, nil,
			"--robot-ack="+fixture.session,
			"--panes=0.1,"+fixture.panes["0.1"].ID,
			"--timeout=300ms",
			"--poll=25ms",
		)
		var output ackProcessOutput
		decodeRobotFailure(t, result, "TIMEOUT", &output)
		if !output.TimedOut {
			t.Fatal("standalone ack did not report timed_out=true")
		}
		if len(output.Confirmations) != 0 {
			t.Fatalf("standalone ack confirmations = %+v, want none", output.Confirmations)
		}
		assertStringSlice(t, "standalone ack pending", output.Pending, []string{"0.1"})
	})

	t.Run("history_filters_all_selector_forms_without_alias_leaks", func(t *testing.T) {
		fixture.seedHistory(t)
		tests := []struct {
			name         string
			selector     string
			wantFiltered int
			wantPrompts  []string
		}{
			{
				name:         "window_pane",
				selector:     "0.1",
				wantFiltered: 1,
				wantPrompts:  []string{"history-0.1"},
			},
			{
				name:         "pane_id_alias",
				selector:     fixture.panes["0.1"].ID,
				wantFiltered: 1,
				wantPrompts:  []string{"history-0.1"},
			},
			{
				name:         "bare_window",
				selector:     "1",
				wantFiltered: 2,
				wantPrompts:  []string{"history-1.0", "history-1.1"},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				result := fixture.runRobot(t, nil,
					"--robot-history="+fixture.session,
					"--pane="+test.selector,
				)
				var output historyProcessOutput
				decodeRobotSuccess(t, result, &output)
				if output.Filtered != test.wantFiltered || len(output.Entries) != test.wantFiltered {
					t.Fatalf("history filtered/entries = %d/%d, want %d", output.Filtered, len(output.Entries), test.wantFiltered)
				}
				prompts := make([]string, 0, len(output.Entries))
				for _, entry := range output.Entries {
					prompts = append(prompts, entry.Prompt)
				}
				sort.Strings(prompts)
				sort.Strings(test.wantPrompts)
				assertStringSlice(t, "history prompts", prompts, test.wantPrompts)
			})
		}
	})

	t.Run("interrupt_reuses_observation_and_unified_dispatch", func(t *testing.T) {
		target := fixture.panes["1.1"]
		fixture.sendPaneCommand(t, target.ID, "sleep 30")
		time.Sleep(200 * time.Millisecond)

		marker := fixture.uniqueMarker("INTERRUPT_FOLLOWUP")
		result := fixture.runRobot(t, nil,
			"--robot-interrupt="+fixture.session,
			"--panes=1.1,"+target.ID,
			"--force",
			"--no-wait",
			"--msg="+shellMarkerCommand(marker),
			"--timeout=3s",
		)
		var output interruptProcessOutput
		decodeRobotSuccess(t, result, &output)
		assertStringSlice(t, "interrupted panes", output.Interrupted, []string{"1.1"})
		assertStringSlice(t, "ready panes", output.ReadyForInput, []string{"1.1"})
		if !output.MessageSent {
			t.Fatalf("interrupt did not dispatch the follow-up message: %+v", output)
		}
		if len(output.Failed) != 0 {
			t.Fatalf("interrupt failures = %+v", output.Failed)
		}
		previous, ok := output.PreviousStates["1.1"]
		if !ok {
			t.Fatalf("interrupt previous_states keys = %v, want 1.1", sortedMapKeys(output.PreviousStates))
		}
		if previous.ObservationFreshness != "fresh" || previous.ObservedAt == "" || previous.ObservationError != "" {
			t.Fatalf("interrupt observation = %+v, want fresh observation", previous)
		}
		if previous.State != "unknown" && (previous.LastKnownState != "" || previous.LastKnownObservedAt != "") {
			t.Fatalf("fresh known interrupt observation duplicated last-known state: %+v", previous)
		}
		fixture.waitForMarkers(t, marker, []string{"1.1"})
		fixture.assertMarkerOnlyIn(t, marker, []string{"1.1"})
	})

	t.Run("every_selector_surface_fails_loud_at_process_boundary", func(t *testing.T) {
		for _, command := range canonicalSelectorCommands(fixture) {
			t.Run(command.name+"_malformed", func(t *testing.T) {
				result := fixture.runRobot(t, nil, command.args("1.x")...)
				assertRobotFailure(t, result, "INVALID_FLAG")
			})
			t.Run(command.name+"_missing", func(t *testing.T) {
				result := fixture.runRobot(t, nil, command.args("99.99")...)
				assertRobotFailure(t, result, "PANE_NOT_FOUND")
			})
		}
	})

	t.Run("toon_terminal_failures_fall_back_to_canonical_json", func(t *testing.T) {
		for _, command := range canonicalSelectorCommands(fixture) {
			t.Run(command.name, func(t *testing.T) {
				args := append([]string{"--robot-format=toon"}, command.args("99.99")...)
				result := fixture.runRobot(t, nil, args...)
				envelope := assertRobotFailure(t, result, "PANE_NOT_FOUND")
				if envelope.OutputFormat != "json" {
					t.Fatalf("TOON terminal failure output_format = %q, want json", envelope.OutputFormat)
				}
			})
		}
	})

	t.Run("session_observer_retains_last_known_after_real_capture_failure", func(t *testing.T) {
		var failCaptures atomic.Bool
		detector := statuspkg.NewDetector()
		config := statuspkg.DefaultSessionObserverConfig(detector.Config())
		config.CaptureLines = 100
		config.MaxConcurrentCaptures = 4
		observer := statuspkg.NewSessionObserverWithDependencies(
			detector,
			config,
			statuspkg.SessionObserverDependencies{
				ListPanes: fixture.realPaneActivities,
				CapturePane: func(ctx context.Context, paneID string, lines int) (string, error) {
					if failCaptures.Load() {
						return "", errors.New("injected capture failure after real snapshot")
					}
					return fixture.capturePaneContext(ctx, paneID, lines)
				},
				Now: time.Now,
			},
		)

		fresh, err := observer.Observe(context.Background(), fixture.session)
		if err != nil {
			t.Fatalf("fresh real-tmux observation: %v", err)
		}
		if !fresh.Complete || len(fresh.Panes) != 4 || len(fresh.Failures) != 0 {
			t.Fatalf("fresh observation = complete:%v panes:%d failures:%+v", fresh.Complete, len(fresh.Panes), fresh.Failures)
		}
		freshByID := make(map[string]statuspkg.PaneObservation, len(fresh.Panes))
		for _, pane := range fresh.Panes {
			freshByID[pane.Pane.ID] = pane
			if pane.Current.Freshness != statuspkg.FreshnessFresh || pane.Current.Error != "" {
				t.Fatalf("fresh pane %s observation = %+v", pane.Pane.ID, pane.Current)
			}
			if pane.Current.Status.State == statuspkg.StateUnknown {
				t.Fatalf("fixture pane %s did not produce a cacheable known state: %+v", pane.Pane.ID, pane.Current.Status)
			}
			if pane.LastKnown != nil {
				t.Fatalf("fresh known pane %s duplicated last-known: %+v", pane.Pane.ID, pane.LastKnown)
			}
		}

		failCaptures.Store(true)
		degraded, err := observer.Observe(context.Background(), fixture.session)
		if err != nil {
			t.Fatalf("pane-local capture failures should not fail observation: %v", err)
		}
		if degraded.Complete || len(degraded.Panes) != 4 || len(degraded.Failures) != 4 {
			t.Fatalf("degraded observation = complete:%v panes:%d failures:%+v", degraded.Complete, len(degraded.Panes), degraded.Failures)
		}
		for _, pane := range degraded.Panes {
			prior := freshByID[pane.Pane.ID]
			if pane.Current.Freshness != statuspkg.FreshnessUnavailable || pane.Current.Error == "" || pane.SafeToDispatch() {
				t.Fatalf("degraded pane %s current state = %+v safe=%v", pane.Pane.ID, pane.Current, pane.SafeToDispatch())
			}
			if pane.LastKnown == nil {
				t.Fatalf("degraded pane %s lost last-known state", pane.Pane.ID)
			}
			if pane.LastKnown.Freshness != statuspkg.FreshnessStale || pane.LastKnown.Status.State != prior.Current.Status.State {
				t.Fatalf("degraded pane %s last-known = %+v, prior=%+v", pane.Pane.ID, pane.LastKnown, prior.Current)
			}
			if !pane.LastKnown.ObservedAt.Equal(prior.Current.ObservedAt) {
				t.Fatalf("degraded pane %s refreshed last-known timestamp: got %s want %s", pane.Pane.ID, pane.LastKnown.ObservedAt, prior.Current.ObservedAt)
			}
		}
	})

	t.Run("distribute_targets_duplicate_local_indexes_by_pane_id", func(t *testing.T) {
		fixture.respawnUserShells(t, "")
		targets := []struct {
			address string
			title   string
			beadID  string
		}{
			{address: "0.1", title: fixture.session + "__cc_2", beadID: "ntm-e2e-distribute-w0"},
			{address: "1.1", title: fixture.session + "__cc_4", beadID: "ntm-e2e-distribute-w1"},
		}

		for _, target := range targets {
			endpoint := fixture.panes[target.address]
			fixture.mustTMUX(t, "select-pane", "-t", endpoint.ID, "-T", target.title)
			endpoint.Title = target.title
			endpoint.Type = tmux.AgentClaude
			fixture.panes[target.address] = endpoint
			fixture.sendPaneCommand(t, endpoint.ID, "cc")
			fixture.waitForPaneContains(t, target.address, "claude>")
		}

		fakeBin := filepath.Join(fixture.runtimeRoot, "bin")
		triagePath := filepath.Join(fixture.runtimeRoot, "canonical-distribute-triage.json")
		planPath := filepath.Join(fixture.runtimeRoot, "canonical-distribute-plan.json")
		if err := os.WriteFile(triagePath, []byte(`{"triage":{"recommendations":[]}}`), 0o600); err != nil {
			t.Fatalf("write deterministic distribute triage payload: %v", err)
		}
		planPayload, err := json.Marshal(map[string]any{
			"plan": map[string]any{
				"tracks": []map[string]any{{
					"track_id": "canonical-distribute-track",
					"items": []map[string]any{
						{"id": targets[0].beadID, "title": "Window zero assignment", "status": "open", "priority": 1},
						{"id": targets[1].beadID, "title": "Window one assignment", "status": "open", "priority": 1},
					},
				}},
			},
		})
		if err != nil {
			t.Fatalf("marshal deterministic distribute plan payload: %v", err)
		}
		if err := os.WriteFile(planPath, planPayload, 0o600); err != nil {
			t.Fatalf("write deterministic distribute plan payload: %v", err)
		}
		bvScript := strings.Join([]string{
			"#!/bin/sh",
			`case "$1" in`,
			`  --robot-triage) cat "$NTM_E2E_TRIAGE_PAYLOAD" ;;`,
			`  --robot-plan) cat "$NTM_E2E_PLAN_PAYLOAD" ;;`,
			`  *) printf 'unexpected bv args: %s\n' "$*" >&2; exit 64 ;;`,
			"esac",
			"",
		}, "\n")
		if err := os.WriteFile(filepath.Join(fakeBin, "bv"), []byte(bvScript), 0o700); err != nil {
			t.Fatalf("write deterministic distribute bv: %v", err)
		}
		readyJSON := fmt.Sprintf(
			`[{"id":%q,"title":%q,"priority":1},{"id":%q,"title":%q,"priority":1}]`,
			targets[0].beadID, "Window zero assignment", targets[1].beadID, "Window one assignment",
		)
		fakeBR := fmt.Sprintf("#!/bin/sh\nif [ \"${1:-}\" = --lock-timeout ]; then shift 2; fi\ncase \"${1:-}\" in\n  ready) printf '%%s\\n' '%s' ;;\n  list|blocked) printf '[]\\n' ;;\n  *) echo \"unexpected br args: $*\" >&2; exit 64 ;;\nesac\n", readyJSON)
		if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(fakeBR), 0o700); err != nil {
			t.Fatalf("write deterministic distribute br: %v", err)
		}
		extraEnv := map[string]string{
			"PATH":                   fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"NTM_E2E_TRIAGE_PAYLOAD": triagePath,
			"NTM_E2E_PLAN_PAYLOAD":   planPath,
		}
		robotAssign := fixture.runRobot(t, extraEnv, "--robot-assign="+fixture.session, "--strategy=balanced")
		if robotAssign.exitCode != 0 {
			t.Fatalf("robot assign inspection exit=%d stdout=%s stderr=%s", robotAssign.exitCode, robotAssign.stdout, robotAssign.stderr)
		}
		var assignInspection struct {
			IdleAgents      []string `json:"idle_agents"`
			Recommendations []struct {
				PaneID     string `json:"pane_id"`
				PaneTarget string `json:"pane_target"`
			} `json:"recommendations"`
		}
		if err := json.Unmarshal(robotAssign.stdout, &assignInspection); err != nil {
			t.Fatalf("decode robot assign inspection: %v; output=%s", err, robotAssign.stdout)
		}
		if len(assignInspection.IdleAgents) != len(targets) || len(assignInspection.Recommendations) != len(targets) {
			t.Fatalf("robot assign inspection idle=%v recommendations=%+v; output=%s", assignInspection.IdleAgents, assignInspection.Recommendations, robotAssign.stdout)
		}

		preview := fixture.runNTM(t, extraEnv,
			"--json", "send", fixture.session, "--distribute", "--dist-limit=2",
		)
		if preview.exitCode != 0 {
			t.Fatalf("distribute preview exit=%d stdout=%s stderr=%s", preview.exitCode, preview.stdout, preview.stderr)
		}
		var previewOutput struct {
			Success         bool             `json:"success"`
			Recommendations []map[string]any `json:"recommendations"`
		}
		if err := json.Unmarshal(preview.stdout, &previewOutput); err != nil {
			t.Fatalf("decode distribute preview: %v; output=%s", err, preview.stdout)
		}
		if !previewOutput.Success || len(previewOutput.Recommendations) != len(targets) {
			t.Fatalf("distribute preview = success:%v recommendations:%+v", previewOutput.Success, previewOutput.Recommendations)
		}
		for i, target := range targets {
			recommendation := previewOutput.Recommendations[i]
			endpoint := fixture.panes[target.address]
			if recommendation["bead_id"] != target.beadID || recommendation["pane_id"] != endpoint.ID || recommendation["pane_target"] != target.address {
				t.Fatalf("recommendation %d = %+v, want bead %s on %s (%s)", i, recommendation, target.beadID, target.address, endpoint.ID)
			}
			if _, legacy := recommendation["pane_index"]; legacy {
				t.Fatalf("recommendation %d leaked ambiguous pane_index: %+v", i, recommendation)
			}
		}

	})

	t.Run("robot_and_distribute_use_session_project_and_fail_loud", func(t *testing.T) {
		targetProject := filepath.Join(fixture.runtimeRoot, "session-project")
		decoyProject := filepath.Join(fixture.runtimeRoot, "caller-project")
		for _, projectDir := range []string{targetProject, decoyProject} {
			if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o700); err != nil {
				t.Fatalf("create project marker in %s: %v", projectDir, err)
			}
		}

		fixture.respawnUserShells(t, targetProject)
		for i, address := range []string{"0.1", "1.1"} {
			endpoint := fixture.panes[address]
			endpoint.Title = fmt.Sprintf("%s__cc_%d", fixture.session, 2*(i+1))
			endpoint.Type = tmux.AgentClaude
			fixture.mustTMUX(t, "select-pane", "-t", endpoint.ID, "-T", endpoint.Title)
			fixture.panes[address] = endpoint
			fixture.sendPaneCommand(t, endpoint.ID, "cc")
			fixture.waitForPaneContains(t, address, "claude>")
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			allScoped := true
			for _, endpoint := range fixture.panes {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				output, err := fixture.tmuxOutput(ctx, "display-message", "-p", "-t", endpoint.ID, "#{pane_current_path}")
				cancel()
				if err != nil || filepath.Clean(strings.TrimSpace(string(output))) != targetProject {
					allScoped = false
					break
				}
			}
			if allScoped {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("tmux panes did not enter the target session project")
			}
			time.Sleep(50 * time.Millisecond)
		}

		targetIDs := []string{"ntm-e2e-session-project-a", "ntm-e2e-session-project-b"}
		decoyIDs := []string{"ntm-e2e-caller-project-a", "ntm-e2e-caller-project-b"}
		readyRows := func(ids []string) string {
			return fmt.Sprintf(`[{"id":%q,"title":%q,"priority":1},{"id":%q,"title":%q,"priority":1}]`,
				ids[0], "Session project A", ids[1], "Session project B")
		}
		fakeBin := filepath.Join(fixture.runtimeRoot, "bin")
		planPayloadFor := func(ids []string, prefix string) []byte {
			t.Helper()
			payload, err := json.Marshal(map[string]any{
				"plan": map[string]any{
					"tracks": []map[string]any{{
						"track_id": prefix + "-track",
						"items": []map[string]any{
							{"id": ids[0], "title": prefix + " A", "status": "open", "priority": 1},
							{"id": ids[1], "title": prefix + " B", "status": "open", "priority": 1},
						},
					}},
				},
			})
			if err != nil {
				t.Fatalf("marshal %s project plan: %v", prefix, err)
			}
			return payload
		}
		targetPlanPath := filepath.Join(fixture.runtimeRoot, "session-project-plan.json")
		decoyPlanPath := filepath.Join(fixture.runtimeRoot, "caller-project-plan.json")
		if err := os.WriteFile(targetPlanPath, planPayloadFor(targetIDs, "Session project"), 0o600); err != nil {
			t.Fatalf("write session-project plan: %v", err)
		}
		if err := os.WriteFile(decoyPlanPath, planPayloadFor(decoyIDs, "Caller project"), 0o600); err != nil {
			t.Fatalf("write caller-project plan: %v", err)
		}
		projectBV := fmt.Sprintf(`#!/bin/sh
case "$PWD" in
  %q) plan=%q ;;
  %q) plan=%q ;;
  *) echo "unexpected bv cwd: $PWD" >&2; exit 65 ;;
esac
case "$1" in
  --robot-triage) printf '{"triage":{"recommendations":[]}}\n' ;;
  --robot-plan) cat "$plan" ;;
  *) echo "unexpected bv args: $*" >&2; exit 64 ;;
esac
`, targetProject, targetPlanPath, decoyProject, decoyPlanPath)
		if err := os.WriteFile(filepath.Join(fakeBin, "bv"), []byte(projectBV), 0o700); err != nil {
			t.Fatalf("write project-scoped bv: %v", err)
		}
		goodBR := fmt.Sprintf(`#!/bin/sh
if [ "${1:-}" = --lock-timeout ]; then shift 2; fi
case "$PWD" in
  %q) rows='%s' ;;
  %q) rows='%s' ;;
  *) echo "unexpected br cwd: $PWD" >&2; exit 65 ;;
esac
case "$1" in
  ready) printf '%%s\n' "$rows" ;;
  list|blocked) printf '[]\n' ;;
  *) echo "unexpected br args: $*" >&2; exit 64 ;;
esac
`, targetProject, readyRows(targetIDs), decoyProject, readyRows(decoyIDs))
		if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(goodBR), 0o700); err != nil {
			t.Fatalf("write project-scoped br: %v", err)
		}
		extraEnv := map[string]string{"PATH": fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")}

		robotAssign := fixture.runNTMInDir(t, decoyProject, extraEnv,
			"--robot-format=json", "--robot-assign="+fixture.session, "--strategy=balanced",
		)
		if robotAssign.exitCode != 0 {
			t.Fatalf("project-scoped robot assign exit=%d stdout=%s stderr=%s", robotAssign.exitCode, robotAssign.stdout, robotAssign.stderr)
		}
		for _, beadID := range targetIDs {
			if !bytes.Contains(robotAssign.stdout, []byte(beadID)) {
				t.Fatalf("robot assign omitted session-project bead %s: %s", beadID, robotAssign.stdout)
			}
		}
		for _, beadID := range decoyIDs {
			if bytes.Contains(robotAssign.stdout, []byte(beadID)) {
				t.Fatalf("robot assign leaked caller-CWD bead %s: %s", beadID, robotAssign.stdout)
			}
		}

		preview := fixture.runNTMInDir(t, decoyProject, extraEnv,
			"--json", "send", fixture.session, "--distribute", "--dist-limit=2",
		)
		if preview.exitCode != 0 {
			t.Fatalf("project-scoped distribute preview exit=%d stdout=%s stderr=%s", preview.exitCode, preview.stdout, preview.stderr)
		}
		for _, beadID := range targetIDs {
			if !bytes.Contains(preview.stdout, []byte(beadID)) {
				t.Fatalf("distribute preview omitted session-project bead %s: %s", beadID, preview.stdout)
			}
		}
		for _, beadID := range decoyIDs {
			if bytes.Contains(preview.stdout, []byte(beadID)) {
				t.Fatalf("distribute preview leaked caller-CWD bead %s: %s", beadID, preview.stdout)
			}
		}

		malformedBR := "#!/bin/sh\nif [ \"${1:-}\" = --lock-timeout ]; then shift 2; fi\ncase \"${1:-}\" in\n  ready) printf '{malformed\\n' ;;\n  list|blocked) printf '[]\\n' ;;\n  *) exit 64 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(fakeBin, "br"), []byte(malformedBR), 0o700); err != nil {
			t.Fatalf("write malformed br: %v", err)
		}
		malformedRobot := fixture.runNTMInDir(t, decoyProject, extraEnv,
			"--robot-format=json", "--robot-assign="+fixture.session, "--strategy=balanced",
		)
		if malformedRobot.exitCode != 1 || len(bytes.TrimSpace(malformedRobot.stderr)) != 0 {
			t.Fatalf("malformed robot assign exit=%d, want 1: stdout=%s stderr=%s", malformedRobot.exitCode, malformedRobot.stdout, malformedRobot.stderr)
		}
		var robotFailure struct {
			Success         bool             `json:"success"`
			Error           string           `json:"error"`
			Recommendations []map[string]any `json:"recommendations"`
			BlockedBeads    []map[string]any `json:"blocked_beads"`
		}
		if err := json.Unmarshal(malformedRobot.stdout, &robotFailure); err != nil {
			t.Fatalf("malformed robot assign must emit one JSON document: %v; output=%s", err, malformedRobot.stdout)
		}
		if robotFailure.Success || robotFailure.Error == "" || robotFailure.Recommendations == nil || robotFailure.BlockedBeads == nil {
			t.Fatalf("malformed robot assignment output=%+v", robotFailure)
		}

		malformedDistribute := fixture.runNTMInDir(t, decoyProject, extraEnv,
			"--json", "send", fixture.session, "--distribute", "--dist-limit=2",
		)
		if malformedDistribute.exitCode != 1 || len(bytes.TrimSpace(malformedDistribute.stderr)) != 0 {
			t.Fatalf("malformed distribute exit=%d, want 1: stdout=%s stderr=%s", malformedDistribute.exitCode, malformedDistribute.stdout, malformedDistribute.stderr)
		}
		var distributeFailure struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(malformedDistribute.stdout, &distributeFailure); err != nil {
			t.Fatalf("malformed distribute must emit one JSON document: %v; output=%s", err, malformedDistribute.stdout)
		}
		if distributeFailure.Success || distributeFailure.Error == "" {
			t.Fatalf("malformed distribute output=%+v", distributeFailure)
		}
	})
}

// TestE2EProjectSendCancellationAggregation interrupts a built project
// broadcast after its first real tmux target has been staged. The terminal
// response must preserve the failed in-flight receipt, synthesize a typed
// pending receipt for the untouched session, and join the delivery protocol so
// neither a delayed Enter nor a second-session stage can occur after return.
func TestE2EProjectSendCancellationAggregation(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newCanonicalPaneFixture(t)
	project := fixture.session + "-cancel-project"
	sessionA := project + "--a"
	sessionB := project + "--b"
	paneA := fixture.createSinglePaneAgentSession(t, sessionA)
	paneB := fixture.createSinglePaneAgentSession(t, sessionB)
	marker := fixture.uniqueMarker("PROJECT_SEND_CANCEL")

	stateRoot := filepath.Join(fixture.runtimeRoot, fmt.Sprintf("project-send-cancel-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatalf("create project send cancellation state: %v", err)
	}
	stagePath := filepath.Join(stateRoot, "staged")
	enterLog := filepath.Join(stateRoot, "enter.log")
	commandLog := filepath.Join(stateRoot, "commands.log")
	wrapper := filepath.Join(fixture.runtimeRoot, "bin", fmt.Sprintf("tmux-project-send-cancel-%d", time.Now().UnixNano()))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_PROJECT_COMMANDS"
command_name=${1:-}
target=
literal=0
enter=0
previous=
for argument in "$@"; do
    if [ "$previous" = "-t" ]; then target=$argument; fi
    if [ "$argument" = "-l" ]; then literal=1; fi
    if [ "$argument" = "Enter" ]; then enter=1; fi
    previous=$argument
done
if [ "$command_name" = "paste-buffer" ] || { [ "$command_name" = "send-keys" ] && [ "$literal" -eq 1 ]; }; then
    "$NTM_E2E_REAL_TMUX" "$@"
    status=$?
    printf '%s\n' "$target" > "$NTM_E2E_PROJECT_STAGED"
    exit "$status"
fi
if [ "$command_name" = "send-keys" ] && [ "$enter" -eq 1 ]; then
    printf '%s\n' "$target" >> "$NTM_E2E_PROJECT_ENTER_LOG"
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatalf("write project send cancellation wrapper: %v", err)
	}

	cmd := exec.Command(fixture.ntmPath,
		"--json", "send", "--project="+project, "--all",
		"--no-cass-check", "--no-hooks", shellMarkerCommand(marker),
	)
	cmd.Env = mergeProcessEnv(fixture.env, map[string]string{
		"NTM_TMUX_BINARY":           wrapper,
		"NTM_E2E_REAL_TMUX":         fixture.tmuxPath,
		"NTM_E2E_PROJECT_STAGED":    stagePath,
		"NTM_E2E_PROJECT_ENTER_LOG": enterLog,
		"NTM_E2E_PROJECT_COMMANDS":  commandLog,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start project send cancellation process: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			<-waited
		}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(stagePath); err == nil {
			break
		}
		select {
		case waitErr := <-waited:
			finished = true
			t.Fatalf("project send exited before first stage: %v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Signal(syscall.SIGQUIT)
			t.Fatalf("project send did not stage before timeout: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	stagedTarget, err := os.ReadFile(stagePath)
	if err != nil {
		t.Fatalf("read project send staged target: %v", err)
	}
	if strings.TrimSpace(string(stagedTarget)) != paneA {
		t.Fatalf("project send staged target=%q, want first sorted session pane %s", stagedTarget, paneA)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt staged project send: %v", err)
	}

	var waitErr error
	select {
	case waitErr = <-waited:
		finished = true
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Signal(syscall.SIGQUIT)
		t.Fatalf("project send did not join after cancellation: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if waitErr == nil {
		t.Fatalf("canceled project send exited successfully: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("canceled project send returned no process status: %v", waitErr)
	}
	var output projectSendProcessOutput
	decodeCLIJSONFailure(t, robotProcessResult{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: exitErr.ExitCode(),
	}, &output)
	if output.Success || output.GeneratedAt == "" || output.Project != project || output.ErrorCode != "TIMEOUT" || output.Error == "" {
		t.Fatalf("canceled project aggregate=%+v", output)
	}
	if output.MatchedSessions != 2 || output.SucceededSessions != 0 || output.FailedSessions != 2 || output.Delivered != 0 || output.FailedDeliveries != 1 {
		t.Fatalf("canceled project aggregate counts=%+v", output)
	}
	if len(output.Sessions) != 2 || output.Sessions[0].Session != sessionA || output.Sessions[1].Session != sessionB {
		t.Fatalf("canceled project session order=%+v, want [%s %s]", output.Sessions, sessionA, sessionB)
	}
	inFlight := output.Sessions[0]
	if inFlight.Success || inFlight.ErrorCode != "TIMEOUT" || inFlight.Error == "" || inFlight.Delivered != 0 || inFlight.Failed != 1 || len(inFlight.Targets) != 1 {
		t.Fatalf("canceled in-flight project receipt=%+v", inFlight)
	}
	pending := output.Sessions[1]
	if pending.Success || pending.ErrorCode != "TIMEOUT" || pending.Error == "" || pending.Delivered != 0 || pending.Failed != 0 || pending.Targets == nil || len(pending.Targets) != 0 {
		t.Fatalf("canceled pending project receipt=%+v", pending)
	}

	commandsBefore, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("read project send command log: %v", err)
	}
	time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
	enterData, err := os.ReadFile(enterLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read project send Enter log: %v", err)
	}
	if strings.TrimSpace(string(enterData)) != "" {
		t.Fatalf("project send submitted Enter after cancellation: %q", enterData)
	}
	commandsAfter, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("reread project send command log: %v", err)
	}
	if !bytes.Equal(commandsAfter, commandsBefore) {
		t.Fatalf("project send issued tmux commands after cancellation: before=%q after=%q", commandsBefore, commandsAfter)
	}
	if strings.Contains(string(commandsAfter), paneB) {
		t.Fatalf("project send touched pending session pane %s: commands=%q", paneB, commandsAfter)
	}
	for session, paneID := range map[string]string{sessionA: paneA, sessionB: paneB} {
		captureCtx, captureCancel := context.WithTimeout(context.Background(), 5*time.Second)
		captured, captureErr := fixture.capturePaneContext(captureCtx, paneID, 100)
		captureCancel()
		if captureErr != nil {
			t.Fatalf("capture canceled project session %s: %v", session, captureErr)
		}
		if exactLineCount(captured, marker) != 0 {
			t.Fatalf("project send executed marker after cancellation in %s: %s", session, captured)
		}
	}
}

func TestE2ECanonicalReplayJSONContract(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newCanonicalPaneFixture(t)
	assertStableResponse := func(t *testing.T, response replayProcessOutput) {
		t.Helper()
		if response.Timestamp == "" || response.Targets == nil || response.Warnings == nil {
			t.Fatalf("replay response lost required fields: %+v", response)
		}
		if _, err := time.Parse(time.RFC3339Nano, response.Timestamp); err != nil {
			t.Fatalf("replay timestamp %q is not RFC3339: %v", response.Timestamp, err)
		}
	}

	marker := fixture.uniqueMarker("REPLAY_JSON")
	fixture.writeReplayHistory(t, shellMarkerCommand(marker))

	t.Run("dry run is noninteractive and does not actuate", func(t *testing.T) {
		result := fixture.runNTM(t, nil,
			"--json", "replay", "--last", "--session="+fixture.session, "--cc", "--dry-run", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONSuccess(t, result, &response)
		assertStableResponse(t, response)
		if !response.Success || !response.DryRun || response.Session != fixture.session || response.Delivered != 0 || response.Failed != 0 {
			t.Fatalf("dry-run response=%+v", response)
		}
		if !reflect.DeepEqual(response.Targets, []string{"0.1"}) {
			t.Fatalf("dry-run targets=%v, want [0.1]", response.Targets)
		}
		fixture.assertMarkerOnlyIn(t, marker, nil)
	})

	t.Run("execution requires explicit yes", func(t *testing.T) {
		result := fixture.runNTM(t, nil,
			"--json", "replay", "--last", "--session="+fixture.session, "--cc", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.ErrorCode != "CONFIRMATION_REQUIRED" || response.Error == "" || response.Delivered != 0 || response.Failed != 0 {
			t.Fatalf("confirmation-required response=%+v", response)
		}
		fixture.assertMarkerOnlyIn(t, marker, nil)
	})

	t.Run("missing history selector is invalid flag", func(t *testing.T) {
		result := fixture.runNTM(t, nil,
			"--json", "replay", "missing-history-prefix", "--session="+fixture.session, "--yes", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.ErrorCode != "INVALID_FLAG" || response.Error == "" || len(response.Targets) != 0 {
			t.Fatalf("missing-history replay response=%+v", response)
		}
	})

	t.Run("missing session is typed", func(t *testing.T) {
		missingSession := fmt.Sprintf("ntm-e2e-replay-missing-%d", time.Now().UnixNano())
		result := fixture.runNTM(t, nil,
			"--json", "replay", "--last", "--session="+missingSession, "--yes", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.Session != missingSession || response.ErrorCode != "SESSION_NOT_FOUND" || response.Error == "" || len(response.Targets) != 0 {
			t.Fatalf("missing-session replay response=%+v", response)
		}
	})

	t.Run("yes delivers to canonical target", func(t *testing.T) {
		result := fixture.runNTM(t, nil,
			"--json", "replay", "--last", "--session="+fixture.session, "--cc", "--yes", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONSuccess(t, result, &response)
		assertStableResponse(t, response)
		if !response.Success || response.DryRun || response.Delivered != 1 || response.Failed != 0 || response.Error != "" || response.ErrorCode != "" {
			t.Fatalf("successful replay response=%+v", response)
		}
		if !reflect.DeepEqual(response.Targets, []string{"0.1"}) {
			t.Fatalf("successful replay targets=%v, want [0.1]", response.Targets)
		}
		fixture.waitForMarkers(t, marker, []string{"0.1"})
		fixture.assertMarkerOnlyIn(t, marker, []string{"0.1"})
	})

	t.Run("zero matching targets is typed", func(t *testing.T) {
		result := fixture.runNTM(t, nil,
			"--json", "replay", "--last", "--session="+fixture.session, "--agy", "--yes", "--no-history",
		)
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.ErrorCode != "PANE_NOT_FOUND" || response.Error == "" || len(response.Targets) != 0 || response.Delivered != 0 || response.Failed != 0 {
			t.Fatalf("zero-target replay response=%+v", response)
		}
	})

	t.Run("partial delivery reports delivered and failed counts", func(t *testing.T) {
		partialMarker := fixture.uniqueMarker("REPLAY_PARTIAL")
		fixture.writeReplayHistory(t, shellMarkerCommand(partialMarker))
		wrapper := fixture.writeFailingTmuxWrapper(t)
		result := fixture.runNTM(t, map[string]string{
			"NTM_TMUX_BINARY":      wrapper,
			"NTM_E2E_REAL_TMUX":    fixture.tmuxPath,
			"NTM_E2E_FAIL_PANE_ID": fixture.panes["1.1"].ID,
		}, "--json", "replay", "--last", "--session="+fixture.session, "--cod", "--yes", "--no-history")
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.ErrorCode != "PROMPT_SEND_FAILED" || response.Error == "" || response.Delivered != 1 || response.Failed != 1 {
			t.Fatalf("partial replay response=%+v", response)
		}
		if !reflect.DeepEqual(response.Targets, []string{"0.0", "1.1"}) {
			t.Fatalf("partial replay targets=%v, want [0.0 1.1]", response.Targets)
		}
		fixture.waitForMarkers(t, partialMarker, []string{"0.0"})
		fixture.assertMarkerOnlyIn(t, partialMarker, []string{"0.0"})
	})

	t.Run("all targets undelivered reports prompt send failure", func(t *testing.T) {
		failedMarker := fixture.uniqueMarker("REPLAY_ALL_FAILED")
		fixture.writeReplayHistory(t, shellMarkerCommand(failedMarker))
		wrapper := fixture.writeFailingTmuxWrapper(t)
		result := fixture.runNTM(t, map[string]string{
			"NTM_TMUX_BINARY":      wrapper,
			"NTM_E2E_REAL_TMUX":    fixture.tmuxPath,
			"NTM_E2E_FAIL_PANE_ID": fixture.panes["0.0"].ID,
		}, "--json", "replay", "--last", "--session="+fixture.session, "--cod", "--yes", "--no-history")
		var response replayProcessOutput
		decodeCLIJSONFailure(t, result, &response)
		assertStableResponse(t, response)
		if response.Success || response.ErrorCode != "PROMPT_SEND_FAILED" || response.Error == "" || response.Delivered != 0 || response.Failed != 2 {
			t.Fatalf("all-undelivered replay response=%+v", response)
		}
		if !reflect.DeepEqual(response.Targets, []string{"0.0", "1.1"}) {
			t.Fatalf("all-undelivered replay targets=%v, want [0.0 1.1]", response.Targets)
		}
		fixture.assertMarkerOnlyIn(t, failedMarker, nil)
	})
}

func TestE2ECanonicalReplayCancellationNoPostReturnEnter(t *testing.T) {
	CommonE2EPrerequisites(t)
	testutil.RequireTmuxThrottled(t)

	fixture := newCanonicalPaneFixture(t)
	prompt := fmt.Sprintf("NTM_REPLAY_CANCEL_%d", time.Now().UnixNano())
	entry := history.NewEntry(fixture.session, nil, prompt, history.SourceCLI)
	entry.SetSuccess()
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode replay history entry: %v", err)
	}
	historyDir := filepath.Join(fixture.runtimeRoot, "data", "ntm")
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		t.Fatalf("create replay history directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(historyDir, "history.jsonl"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write replay history: %v", err)
	}

	stageMarker := filepath.Join(fixture.runtimeRoot, "replay-stage")
	enterLog := filepath.Join(fixture.runtimeRoot, "replay-enter.log")
	tmuxWrapper := filepath.Join(fixture.runtimeRoot, "bin", "tmux-replay-cancel")
	wrapper := `#!/bin/sh
if [ "${1:-}" = "send-keys" ]; then
  literal=0
  enter=0
  for arg in "$@"; do
    if [ "$arg" = "-l" ]; then literal=1; fi
    if [ "$arg" = "Enter" ]; then enter=1; fi
  done
  if [ "$literal" = "1" ]; then
    "$NTM_E2E_REAL_TMUX" "$@"
    rc=$?
    : > "$NTM_E2E_STAGE_MARKER"
    exit "$rc"
  fi
  if [ "$enter" = "1" ]; then
    printf 'Enter\n' >> "$NTM_E2E_ENTER_LOG"
  fi
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(tmuxWrapper, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write replay tmux wrapper: %v", err)
	}

	cmd := exec.Command(fixture.ntmPath, "--json", "replay", "--last", "--session="+fixture.session, "--cc", "--yes", "--no-history")
	cmd.Env = mergeProcessEnv(fixture.env, map[string]string{
		"NTM_TMUX_BINARY":      tmuxWrapper,
		"NTM_E2E_REAL_TMUX":    fixture.tmuxPath,
		"NTM_E2E_STAGE_MARKER": stageMarker,
		"NTM_E2E_ENTER_LOG":    enterLog,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start replay process: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(stageMarker); err == nil {
			break
		}
		select {
		case waitErr := <-waited:
			t.Fatalf("replay exited before staging prompt: %v stdout=%s stderr=%s", waitErr, stdout.String(), stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Signal(os.Interrupt)
			t.Fatalf("replay never staged prompt: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt replay after staging: %v", err)
	}
	replayExitCode := 0
	select {
	case waitErr := <-waited:
		if waitErr == nil {
			t.Fatalf("canceled replay exited successfully: stdout=%s stderr=%s", stdout.String(), stderr.String())
		}
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Fatalf("canceled replay returned no process status: %v", waitErr)
		}
		replayExitCode = exitErr.ExitCode()
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Signal(os.Interrupt)
		t.Fatal("replay did not return after cancellation")
	}

	// The old background delivery path returned on cancellation while its
	// delayed Enter could still fire later. Wait beyond both protocol delays
	// and prove the instrumented real-tmux boundary observed no Enter.
	time.Sleep(tmux.DoubleEnterFirstDelay + tmux.DoubleEnterSecondDelay + 250*time.Millisecond)
	enterData, err := os.ReadFile(enterLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read replay Enter log: %v", err)
	}
	if strings.TrimSpace(string(enterData)) != "" {
		t.Fatalf("replay submitted Enter after cancellation returned: %q", enterData)
	}

	var response replayProcessOutput
	decodeCLIJSONFailure(t, robotProcessResult{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: replayExitCode,
	}, &response)
	if response.Success || response.ErrorCode != "TIMEOUT" || response.Error == "" || response.Targets == nil || response.Warnings == nil {
		t.Fatalf("canceled replay response=%+v", response)
	}
	if !reflect.DeepEqual(response.Targets, []string{"0.1"}) || response.Delivered != 0 || response.Failed != 1 {
		t.Fatalf("canceled replay counts/targets=%+v", response)
	}
}

func newCanonicalPaneFixture(t *testing.T) *canonicalPaneFixture {
	t.Helper()

	ntmPath, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("resolve E2E ntm binary: %v", err)
	}
	tmuxPath, err := exec.LookPath(tmux.BinaryPath())
	if err != nil {
		t.Fatalf("resolve tmux: %v", err)
	}

	runtimeRoot := t.TempDir()
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	for _, path := range []string{
		filepath.Join(runtimeRoot, "home"),
		filepath.Join(runtimeRoot, "config"),
		filepath.Join(runtimeRoot, "data"),
		filepath.Join(runtimeRoot, "bin"),
		tmuxRoot,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create fixture path %s: %v", path, err)
		}
	}

	fakeBin := filepath.Join(runtimeRoot, "bin")
	fakeClaude := strings.Join([]string{
		"#!/bin/sh",
		"trap 'exit 0' INT TERM HUP",
		"printf 'Claude Code v0.0.0\\nclaude>\\n'",
		"while IFS= read -r line; do",
		"  if [ \"$line\" = /exit ]; then exit 0; fi",
		"  if [ -n \"$line\" ]; then printf 'RECEIVED:%s\\n' \"$line\"; eval \"$line\"; fi",
		"  printf 'claude>\\n'",
		"done",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(fakeBin, "cc"), []byte(fakeClaude), 0o700); err != nil {
		t.Fatalf("write deterministic fake Claude agent: %v", err)
	}
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("resolve non-ready Codex fake: %v", err)
	}
	if err := os.Symlink(truePath, filepath.Join(fakeBin, "cod")); err != nil {
		t.Fatalf("create non-ready Codex fake: %v", err)
	}

	fixture := &canonicalPaneFixture{
		t:           t,
		ntmPath:     ntmPath,
		tmuxPath:    tmuxPath,
		session:     fmt.Sprintf("ntm-e2e-panes-%d-%d", os.Getpid(), time.Now().UnixNano()),
		runtimeRoot: runtimeRoot,
		panes:       make(map[string]canonicalPaneEndpoint),
	}
	fixture.env = isolatedProcessEnv(map[string]string{
		"HOME":            filepath.Join(runtimeRoot, "home"),
		"XDG_CONFIG_HOME": filepath.Join(runtimeRoot, "config"),
		"XDG_DATA_HOME":   filepath.Join(runtimeRoot, "data"),
		"TMUX_TMPDIR":     tmuxRoot,
		"NO_COLOR":        "1",
		"TERM":            "xterm-256color",
	})

	configPath := filepath.Join(runtimeRoot, "tmux.conf")
	config := strings.Join([]string{
		"set -g base-index 0",
		"setw -g pane-base-index 0",
		"set -g renumber-windows off",
		"set -g status off",
		"setw -g allow-rename off",
		"setw -g automatic-rename off",
		"",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write isolated tmux config: %v", err)
	}

	fakeAgentPath := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	shell := fmt.Sprintf("env PATH=%q PS1='NTM_E2E> ' bash --noprofile --norc", fakeAgentPath)
	fixture.mustTMUX(t, "-f", configPath, "new-session", "-d", "-s", fixture.session, "-x", "160", "-y", "48", "-n", "w0", shell)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		panePIDs := fixture.listPanePIDs(shutdownCtx)
		if err := fixture.runTMUX(shutdownCtx, "kill-server"); err != nil && shutdownCtx.Err() == nil {
			t.Errorf("stop canonical tmux server: %v", err)
		}
		if err := waitForProcessExit(shutdownCtx, panePIDs); err != nil {
			t.Errorf("canonical tmux pane shutdown: %v", err)
		}
	})
	fixture.mustTMUX(t, "split-window", "-d", "-t", fixture.session+":0", "-v", shell)
	fixture.mustTMUX(t, "new-window", "-d", "-t", fixture.session+":1", "-n", "w1", shell)
	fixture.mustTMUX(t, "split-window", "-d", "-t", fixture.session+":1", "-v", shell)

	titles := map[string]struct {
		title string
		type_ tmux.AgentType
	}{
		"0.0": {title: fixture.session + "__cod_1", type_: tmux.AgentCodex},
		"0.1": {title: fixture.session + "__cc_2", type_: tmux.AgentClaude},
		"1.0": {title: fixture.session + "__gmi_3", type_: tmux.AgentGemini},
		"1.1": {title: fixture.session + "__cod_4", type_: tmux.AgentCodex},
	}
	for address, title := range titles {
		fixture.mustTMUX(t, "select-pane", "-t", fixture.session+":"+address, "-T", title.title)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		panes, listErr := fixture.listPhysicalPanes(context.Background())
		if listErr == nil && len(panes) == len(titles) {
			ready := true
			for address, pane := range panes {
				title := titles[address]
				pane.Title = title.title
				pane.Type = title.type_
				fixture.panes[address] = pane
				captured, captureErr := fixture.capturePaneContext(context.Background(), pane.ID, 20)
				if captureErr != nil || !strings.Contains(captured, "NTM_E2E>") {
					ready = false
				}
			}
			if ready {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("tmux fixture did not reach four ready panes: panes=%+v err=%v", panes, listErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	for address, endpoint := range fixture.panes {
		wantTitle := titles[address].title
		fixture.mustTMUX(t, "select-pane", "-t", endpoint.ID, "-T", wantTitle)
		endpoint.Title = wantTitle
		fixture.panes[address] = endpoint
	}

	return fixture
}

func (f *canonicalPaneFixture) runRobot(t *testing.T, extraEnv map[string]string, args ...string) robotProcessResult {
	t.Helper()
	args = append([]string{"--robot-format=json"}, args...)
	return f.runNTM(t, extraEnv, args...)
}

func (f *canonicalPaneFixture) runNTM(t *testing.T, extraEnv map[string]string, args ...string) robotProcessResult {
	return f.runNTMInDir(t, "", extraEnv, args...)
}

func (f *canonicalPaneFixture) runNTMInDir(t *testing.T, dir string, extraEnv map[string]string, args ...string) robotProcessResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, f.ntmPath, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.Env = mergeProcessEnv(f.env, extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("ntm command timed out: %s", strings.Join(args, " "))
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run ntm %s: %v", strings.Join(args, " "), err)
		}
	}
	t.Logf("[E2E-CANONICAL] exit=%d args=%q stdout=%s stderr=%s", exitCode, args, truncateString(stdout.String(), 500), truncateString(stderr.String(), 500))
	return robotProcessResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode}
}

func (f *canonicalPaneFixture) writeFailingTmuxWrapper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(f.runtimeRoot, "bin", "tmux-fail-send")
	script := strings.Join([]string{
		"#!/bin/sh",
		"if [ \"${1:-}\" = \"send-keys\" ]; then",
		"  previous=",
		"  for argument in \"$@\"; do",
		"    if [ \"$previous\" = \"-t\" ] && [ \"$argument\" = \"$NTM_E2E_FAIL_PANE_ID\" ]; then",
		"      printf 'injected send-keys failure for %s\\n' \"$argument\" >&2",
		"      exit 91",
		"    fi",
		"    previous=$argument",
		"  done",
		"fi",
		"exec \"$NTM_E2E_REAL_TMUX\" \"$@\"",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write failing tmux wrapper: %v", err)
	}
	return path
}

func (f *canonicalPaneFixture) writeScaleLayoutWrapper(t *testing.T, mode string) (string, string) {
	t.Helper()
	if mode != "fail" && mode != "block" {
		t.Fatalf("unsupported scale layout wrapper mode %q", mode)
	}

	stateRoot := filepath.Join(f.runtimeRoot, fmt.Sprintf("scale-layout-%s-%d", mode, time.Now().UnixNano()))
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatalf("create scale layout state directory: %v", err)
	}
	path := filepath.Join(f.runtimeRoot, "bin", fmt.Sprintf("tmux-scale-layout-%s-%d", mode, time.Now().UnixNano()))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$NTM_E2E_SCALE_LAYOUT_STATE/commands.log"
if [ "${1:-}" = "select-layout" ]; then
    if [ ! -e "$NTM_E2E_SCALE_LAYOUT_STATE/initial-layout-finished" ]; then
        : > "$NTM_E2E_SCALE_LAYOUT_STATE/initial-layout-finished"
        exec "$NTM_E2E_REAL_TMUX" "$@"
    fi
    : > "$NTM_E2E_SCALE_LAYOUT_STATE/final-layout-started"
    case "$NTM_E2E_SCALE_LAYOUT_MODE" in
        fail)
            printf 'injected final layout failure\n' >&2
            exit 93
            ;;
        block)
            exec /bin/sleep 30
            ;;
        *)
            printf 'unknown scale layout mode: %s\n' "$NTM_E2E_SCALE_LAYOUT_MODE" >&2
            exit 94
            ;;
    esac
fi
exec "$NTM_E2E_REAL_TMUX" "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write scale layout tmux wrapper: %v", err)
	}
	return path, stateRoot
}

func (f *canonicalPaneFixture) createSinglePaneAgentSession(t *testing.T, session string) string {
	t.Helper()
	fakeAgentPath := filepath.Join(f.runtimeRoot, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	shell := fmt.Sprintf("env PATH=%q PS1='NTM_E2E> ' bash --noprofile --norc", fakeAgentPath)
	f.mustTMUX(t, "new-session", "-d", "-s", session, "-x", "120", "-y", "32", shell)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.runTMUX(ctx, "kill-session", "-t", session); err != nil && ctx.Err() == nil {
			t.Errorf("stop project fixture session %s: %v", session, err)
		}
	})
	f.mustTMUX(t, "select-pane", "-t", session+":0.0", "-T", session+"__cc_1")
	paneID := f.paneIDForSession(t, session)

	deadline := time.Now().Add(5 * time.Second)
	for {
		captured, err := f.capturePaneContext(context.Background(), paneID, 20)
		if err == nil && strings.Contains(captured, "NTM_E2E>") {
			return paneID
		}
		if time.Now().After(deadline) {
			t.Fatalf("project fixture session %s did not become ready: output=%q err=%v", session, captured, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *canonicalPaneFixture) paneIDForSession(t *testing.T, session string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := f.tmuxOutput(ctx, "display-message", "-p", "-t", session+":0.0", "#{pane_id}")
	if err != nil {
		t.Fatalf("resolve pane ID for session %s: %v", session, err)
	}
	paneID := strings.TrimSpace(string(output))
	if paneID == "" {
		t.Fatalf("session %s returned an empty pane ID", session)
	}
	return paneID
}

func (f *canonicalPaneFixture) sessionPaneSnapshot(t *testing.T, session string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := f.tmuxOutput(ctx, "list-panes", "-s", "-t", session, "-F", "#{pane_id}|#{pane_title}")
	if err != nil {
		t.Fatalf("snapshot panes for session %s: %v", session, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return []string{}
	}
	sort.Strings(lines)
	return lines
}

func (f *canonicalPaneFixture) mustTMUX(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.runTMUX(ctx, args...); err != nil {
		t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
}

func (f *canonicalPaneFixture) runTMUX(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (f *canonicalPaneFixture) tmuxOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, f.tmuxPath, args...)
	cmd.Env = append([]string(nil), f.env...)
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return nil, err
}

func (f *canonicalPaneFixture) listPanePIDs(ctx context.Context) []int {
	output, err := f.tmuxOutput(ctx, "list-panes", "-s", "-t", f.session, "-F", "#{pane_pid}")
	if err != nil {
		return nil
	}
	var result []int
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(line))
		if parseErr == nil && pid > 0 {
			result = append(result, pid)
		}
	}
	return result
}

func waitForProcessExit(ctx context.Context, pids []int) error {
	remaining := append([]int(nil), pids...)
	for len(remaining) > 0 {
		alive := remaining[:0]
		for _, pid := range remaining {
			process, err := os.FindProcess(pid)
			if err == nil && process.Signal(syscall.Signal(0)) == nil {
				alive = append(alive, pid)
			}
		}
		if len(alive) == 0 {
			return nil
		}
		remaining = alive
		select {
		case <-ctx.Done():
			return fmt.Errorf("pane processes %v remained after tmux shutdown: %w", remaining, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil
}

func (f *canonicalPaneFixture) listPhysicalPanes(ctx context.Context) (map[string]canonicalPaneEndpoint, error) {
	output, err := f.tmuxOutput(ctx, "list-panes", "-s", "-t", f.session, "-F", "#{window_index}.#{pane_index}|#{pane_id}|#{pane_title}")
	if err != nil {
		return nil, err
	}
	result := make(map[string]canonicalPaneEndpoint)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected list-panes line %q", line)
		}
		result[parts[0]] = canonicalPaneEndpoint{Address: parts[0], ID: parts[1], Title: parts[2]}
	}
	return result, nil
}

func (f *canonicalPaneFixture) realPaneActivities(ctx context.Context, session string) ([]tmux.PaneActivity, error) {
	if session != f.session {
		return nil, fmt.Errorf("unexpected session %q", session)
	}
	physical, err := f.listPhysicalPanes(ctx)
	if err != nil {
		return nil, err
	}
	addresses := sortedMapKeys(physical)
	result := make([]tmux.PaneActivity, 0, len(addresses))
	for _, address := range addresses {
		endpoint := physical[address]
		known, ok := f.panes[address]
		if !ok {
			return nil, fmt.Errorf("unexpected physical pane %s", address)
		}
		window, pane, ok := strings.Cut(address, ".")
		if !ok {
			return nil, fmt.Errorf("invalid physical address %q", address)
		}
		windowIndex, err := strconv.Atoi(window)
		if err != nil {
			return nil, err
		}
		paneIndex, err := strconv.Atoi(pane)
		if err != nil {
			return nil, err
		}
		result = append(result, tmux.PaneActivity{
			Pane: tmux.Pane{
				ID:          endpoint.ID,
				Index:       paneIndex,
				WindowIndex: windowIndex,
				Title:       known.Title,
				Type:        known.Type,
				Command:     "bash",
			},
			LastActivity: time.Now().Add(-time.Hour),
		})
	}
	return result, nil
}

func (f *canonicalPaneFixture) capturePaneContext(ctx context.Context, paneID string, lines int) (string, error) {
	output, err := f.tmuxOutput(ctx, "capture-pane", "-p", "-t", paneID, "-S", fmt.Sprintf("-%d", lines))
	return string(output), err
}

func (f *canonicalPaneFixture) capturePane(t *testing.T, address string) string {
	t.Helper()
	endpoint, ok := f.panes[address]
	if !ok {
		t.Fatalf("unknown pane address %q", address)
	}
	output, err := f.capturePaneContext(context.Background(), endpoint.ID, 300)
	if err != nil {
		t.Fatalf("capture pane %s: %v", address, err)
	}
	return output
}

func (f *canonicalPaneFixture) sendPaneCommand(t *testing.T, paneID, command string) {
	t.Helper()
	if err := f.sendPaneCommandErr(paneID, command); err != nil {
		t.Fatalf("send command to pane %s: %v", paneID, err)
	}
}

func (f *canonicalPaneFixture) respawnUserShells(t *testing.T, workingDir string) {
	t.Helper()
	fakeAgentPath := filepath.Join(f.runtimeRoot, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	shell := fmt.Sprintf("env PATH=%q PS1='NTM_E2E> ' bash --noprofile --norc", fakeAgentPath)
	for address, endpoint := range f.panes {
		args := []string{"respawn-pane", "-k"}
		if workingDir != "" {
			args = append(args, "-c", workingDir)
		}
		args = append(args, "-t", endpoint.ID, shell)
		f.mustTMUX(t, args...)
		endpoint.Title = "shell"
		endpoint.Type = tmux.AgentUser
		f.mustTMUX(t, "select-pane", "-t", endpoint.ID, "-T", endpoint.Title)
		f.panes[address] = endpoint
		f.waitForPaneContains(t, address, "NTM_E2E>")
	}
}

func (f *canonicalPaneFixture) sendPaneCommandErr(paneID, command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := f.runTMUX(ctx, "send-keys", "-t", paneID, "-l", command); err != nil {
		return err
	}
	return f.runTMUX(ctx, "send-keys", "-t", paneID, "Enter")
}

func (f *canonicalPaneFixture) waitForMarkers(t *testing.T, marker string, addresses []string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		allFound := true
		for _, address := range addresses {
			if exactLineCount(f.capturePane(t, address), marker) != 1 {
				allFound = false
				break
			}
		}
		if allFound {
			return
		}
		if time.Now().After(deadline) {
			captures := make(map[string]string, len(f.panes))
			for address := range f.panes {
				captures[address] = f.capturePane(t, address)
			}
			t.Fatalf("marker %q not delivered exactly once to %v; captures=%v", marker, addresses, captures)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *canonicalPaneFixture) waitForPaneContains(t *testing.T, address, value string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if strings.Contains(f.capturePane(t, address), value) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s did not contain %q; capture=%s", address, value, f.capturePane(t, address))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *canonicalPaneFixture) assertMarkerOnlyIn(t *testing.T, marker string, wantAddresses []string) {
	t.Helper()
	want := make(map[string]struct{}, len(wantAddresses))
	for _, address := range wantAddresses {
		want[address] = struct{}{}
	}
	for address := range f.panes {
		count := exactLineCount(f.capturePane(t, address), marker)
		_, expected := want[address]
		if expected && count != 1 {
			t.Errorf("marker %q count in target %s = %d, want 1", marker, address, count)
		}
		if !expected && count != 0 {
			t.Errorf("marker %q leaked to pane %s (%d exact lines)", marker, address, count)
		}
	}
}

func (f *canonicalPaneFixture) seedHistory(t *testing.T) {
	t.Helper()
	addresses := sortedMapKeys(f.panes)
	var data bytes.Buffer
	for _, address := range addresses {
		entry := history.NewEntry(f.session, []string{f.panes[address].ID}, "history-"+address, history.SourceCLI)
		entry.SetSuccess()
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal history entry: %v", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	path := filepath.Join(f.runtimeRoot, "data", "ntm", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create history dir: %v", err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write fixture history: %v", err)
	}
}

func (f *canonicalPaneFixture) writeReplayHistory(t *testing.T, prompt string) {
	t.Helper()
	entry := history.NewEntry(f.session, nil, prompt, history.SourceCLI)
	entry.SetSuccess()
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal replay history entry: %v", err)
	}
	path := filepath.Join(f.runtimeRoot, "data", "ntm", "history.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create replay history dir: %v", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write replay history: %v", err)
	}
}

func (f *canonicalPaneFixture) uniqueMarker(prefix string) string {
	return fmt.Sprintf("NTM_E2E_%s_%d", prefix, time.Now().UnixNano())
}

func shellMarkerCommand(marker string) string {
	return fmt.Sprintf("printf '%%s\\n' '%s'", marker)
}

func exactLineCount(output, line string) int {
	count := 0
	for _, candidate := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
		if strings.TrimSpace(candidate) == line {
			count++
		}
	}
	return count
}

func tailLinesContainExact(lines []string, marker string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) == marker {
			return true
		}
	}
	return false
}

func outputForTail(t *testing.T, fixture *canonicalPaneFixture, selectors string) tailProcessOutput {
	t.Helper()
	result := fixture.runRobot(t, nil,
		"--robot-tail="+fixture.session,
		"--panes="+selectors,
		"--lines=100",
	)
	var output tailProcessOutput
	decodeRobotSuccess(t, result, &output)
	return output
}

func decodeCLIJSONSuccess(t *testing.T, result robotProcessResult, target any) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("CLI JSON success exit = %d, want 0; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decodeSingleCLIJSON(t, result, target)
}

func decodeCLIJSONFailure(t *testing.T, result robotProcessResult, target any) {
	t.Helper()
	if result.exitCode != 1 {
		t.Fatalf("CLI JSON failure exit = %d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	decodeSingleCLIJSON(t, result, target)
}

func decodeSingleCLIJSON(t *testing.T, result robotProcessResult, target any) {
	t.Helper()
	if strings.TrimSpace(string(result.stderr)) != "" {
		t.Fatalf("CLI JSON command wrote diagnostics to stderr: %q", result.stderr)
	}
	if !json.Valid(result.stdout) {
		t.Fatalf("CLI JSON stdout is not exactly one JSON document: %q", result.stdout)
	}
	if err := json.Unmarshal(result.stdout, target); err != nil {
		t.Fatalf("decode CLI JSON: %v; output=%s", err, result.stdout)
	}
}

func decodeRobotSuccess(t *testing.T, result robotProcessResult, target any) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("robot command exit = %d, want 0; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	assertEmptyRobotStderr(t, result.stderr)
	if !json.Valid(result.stdout) {
		t.Fatalf("robot stdout is not one JSON document: %q", result.stdout)
	}
	if err := json.Unmarshal(result.stdout, target); err != nil {
		t.Fatalf("decode robot success: %v; output=%s", err, result.stdout)
	}
	var envelope processEnvelope
	if err := json.Unmarshal(result.stdout, &envelope); err != nil {
		t.Fatalf("decode robot envelope: %v", err)
	}
	if !envelope.Success || envelope.Timestamp == "" || envelope.Error != "" || envelope.ErrorCode != "" {
		t.Fatalf("unexpected success envelope: %+v", envelope)
	}
}

func decodeRobotFailure(t *testing.T, result robotProcessResult, errorCode string, target any) {
	t.Helper()
	assertRobotFailure(t, result, errorCode)
	if err := json.Unmarshal(result.stdout, target); err != nil {
		t.Fatalf("decode robot failure: %v; output=%s", err, result.stdout)
	}
}

func assertRobotFailure(t *testing.T, result robotProcessResult, errorCode string) processEnvelope {
	t.Helper()
	envelope := assertTypedRobotFailure(t, result)
	if envelope.ErrorCode != errorCode {
		t.Fatalf("robot failure envelope = %+v, want error_code=%s", envelope, errorCode)
	}
	return envelope
}

func assertTypedRobotFailure(t *testing.T, result robotProcessResult) processEnvelope {
	t.Helper()
	if result.exitCode != 1 {
		t.Fatalf("robot failure exit = %d, want 1; stdout=%s stderr=%s", result.exitCode, result.stdout, result.stderr)
	}
	assertEmptyRobotStderr(t, result.stderr)
	if !json.Valid(result.stdout) {
		t.Fatalf("robot failure stdout is not one JSON document: %q", result.stdout)
	}
	var envelope processEnvelope
	if err := json.Unmarshal(result.stdout, &envelope); err != nil {
		t.Fatalf("decode robot failure envelope: %v; output=%s", err, result.stdout)
	}
	if envelope.Success || envelope.Timestamp == "" || envelope.Error == "" || envelope.ErrorCode == "" || envelope.OutputFormat != "json" {
		t.Fatalf("robot failure envelope = %+v, want typed canonical JSON failure", envelope)
	}
	return envelope
}

func assertEmptyRobotStderr(t *testing.T, stderr []byte) {
	t.Helper()
	if strings.TrimSpace(string(stderr)) != "" {
		t.Fatalf("robot process wrote non-JSON diagnostics to stderr: %q", stderr)
	}
}

func assertFreshObservation(t *testing.T, label string, observation isWorkingPaneOutput) {
	t.Helper()
	if observation.ObservationFreshness != "fresh" || observation.ObservationObservedAt == "" || observation.ObservationError != "" {
		t.Fatalf("%s observation = %+v, want fresh observation", label, observation)
	}
	if observation.ObservationState == "" {
		t.Fatalf("%s omitted observation_state", label)
	}
	if observation.ObservationState != "unknown" && (observation.LastKnownState != "" || observation.LastKnownObservedAt != "") {
		t.Fatalf("%s duplicated fresh known state into last-known fields: %+v", label, observation)
	}
}

func assertStringSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func assertScaleUpPartialResponse(t *testing.T, output scaleProcessOutput) {
	t.Helper()
	if output.Before == nil || output.After == nil || output.Before["cc"] != 1 || output.After["cc"] != 2 || len(output.Actions) != 1 {
		t.Fatalf("scale partial response state=%+v", output)
	}
	action := output.Actions[0]
	if action.ActionType != "spawn" || action.AgentType != "cc" || action.Count != 1 || action.Agents == nil || len(action.Agents) != 0 {
		t.Fatalf("scale partial response action=%+v", action)
	}
}

func assertScaleUpPaneSnapshot(t *testing.T, session string, snapshot []string) {
	t.Helper()
	if len(snapshot) != 2 {
		t.Fatalf("scale partial pane snapshot=%v, want two panes", snapshot)
	}
	for _, title := range []string{session + "__cc_1", session + "__cc_2"} {
		found := false
		for _, pane := range snapshot {
			if strings.HasSuffix(pane, "|"+title) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("scale partial pane snapshot=%v, missing title %q", snapshot, title)
		}
	}
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isolatedProcessEnv(overrides map[string]string) []string {
	keys := map[string]struct{}{
		"HOME":                {},
		"XDG_CONFIG_HOME":     {},
		"XDG_DATA_HOME":       {},
		"TMUX":                {},
		"TMUX_PANE":           {},
		"TMUX_TMPDIR":         {},
		"NTM_CONFIG":          {},
		"NTM_ROBOT_FORMAT":    {},
		"NTM_OUTPUT_FORMAT":   {},
		"TOON_DEFAULT_FORMAT": {},
		"NTM_ROBOT_VERBOSITY": {},
	}
	for key := range overrides {
		keys[key] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := keys[key]; replace {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

func mergeProcessEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for key, value := range overrides {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}
