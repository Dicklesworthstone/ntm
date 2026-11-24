package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
)

func TestScoreAndSelectAssignmentsProducesLocalReservationPaths(t *testing.T) {
	selected := ScoreAndSelectAssignments(
		[]*AgentState{{PaneID: "%3", PaneIndex: 3}},
		[]bv.TriageRecommendation{{
			ID: "ntm-path-intent", Type: "task", Status: "open", Score: 1,
			Title:   "Fix src/main.go:42 using https://example.com/reference.go",
			Reasons: []string{"See [design](docs/design.md#layout); then inspect `src/main.go:43`."},
		}},
		ScoreConfig{}, nil,
	)
	if len(selected) != 1 || selected[0].Assignment == nil {
		t.Fatalf("expected one prepared assignment, got %+v", selected)
	}
	want := []string{"src/main.go", "docs/design.md"}
	if got := selected[0].Assignment.FilesToReserve; !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared reservation paths = %q, want %q", got, want)
	}
}

// Exercise the coordinator's actual admission path and reservation adapter.
// Ranking sees the first bead revision; the new intent must use the second
// authoritative revision, even when all owned files are absent from the title.
func TestAssignWorkResolvesLiveOwnedPathsBeforeClaimAndDispatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		title       string
		description string
		wantPaths   []string
		change      func(*bv.BeadAssignmentDetails)
		lookupErr   bool
		conflict    bool
		reserveErr  bool
		wantError   string
	}{
		{
			name: "description-only paths", title: "Implement durable coordination",
			description: "Update src/work.go and src/work_test.go.",
			wantPaths:   []string{"src/work.go", "src/work_test.go"},
		},
		{
			name: "declared new outputs override title and input paths", title: "Read input.go for durable coordination",
			description: "## Owned outputs\n- new/deep/output.go\n- new/deep/output_test.go\n\n## Inputs\n- input.go\n",
			wantPaths:   []string{"new/deep/output.go", "new/deep/output_test.go"},
		},
		{
			name: "explicit pathless work", title: "Review coordination design",
			description: "Consider the design and report conclusions without editing files.",
		},
		{
			name: "declared empty ownership", title: "Read input.go",
			description: "## Owned outputs\nUndecided.\n\n## Inputs\n- input.go\n",
			wantError:   "owned-files section",
		},
		{
			name: "declared external paths", title: "Read input.go",
			description: "## Owned outputs\n- /tmp/output.go\n- ../outside.go\n",
			wantError:   "owned-files section",
		},
		{
			name: "live lookup failure", title: "Implement coordination", lookupErr: true,
			wantError: "tracker unavailable",
		},
		{
			name: "wrong live bead", title: "Implement coordination",
			change:    func(details *bv.BeadAssignmentDetails) { details.ID = "ntm-other" },
			wantError: "want",
		},
		{
			name: "operator gate added since planning", title: "Implement coordination",
			change:    func(details *bv.BeadAssignmentDetails) { details.Labels = []string{"Maintainer-Approval"} },
			wantError: "operator",
		},
		{
			name: "dependency added since planning", title: "Implement coordination",
			change:    func(details *bv.BeadAssignmentDetails) { details.BlockedBy = []string{"ntm-prerequisite"} },
			wantError: "block",
		},
		{
			name: "container since planning", title: "Implement coordination",
			change:    func(details *bv.BeadAssignmentDetails) { details.IssueType = "epic" },
			wantError: "eligible implementation work",
		},
		{
			name: "claimed since planning", title: "Implement coordination",
			change:    func(details *bv.BeadAssignmentDetails) { details.Assignee = "OtherAgent" },
			wantError: "authorized generation",
		},
		{
			name: "reservation conflict", title: "Implement coordination",
			description: "## Owned outputs\n- new/deep/output.go\n",
			wantPaths:   []string{"new/deep/output.go"}, conflict: true, wantError: "reservation conflicts",
		},
		{
			name: "reservation outcome unknown", title: "Implement coordination",
			description: "## Owned outputs\n- new/deep/output.go\n",
			wantPaths:   []string{"new/deep/output.go"}, reserveErr: true, wantError: "reservation response lost",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			projectDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(projectDir, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, "input.go"), []byte("package input\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, []string{"maintainer-approval"}); err != nil {
				t.Fatal(err)
			}
			c := New("live-owned-scope", projectDir, &agentmail.Client{}, "CoordinatorAgent")
			addEligibleCoordinatorAgent(c, "%9", "BlueLake")
			const beadID = "ntm-live-owned-scope"
			c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
				return []bv.TriageRecommendation{{
					ID: beadID, Title: "Stale ranking input.go", Status: "open", Type: "task", Priority: 2, Score: 1,
					Reasons: []string{"Read input.go"},
				}}, nil
			}
			lookups := 0
			c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
				lookups++
				details := &bv.BeadAssignmentDetails{
					ID: beadID, Title: "First revision input.go", Description: "## Owned outputs\n- old/output.go\n",
					Status: "open", IssueType: "task", Priority: 2,
				}
				if lookups > 1 {
					if tc.lookupErr {
						return nil, errors.New("tracker unavailable")
					}
					details.Title, details.Description, details.Priority = tc.title, tc.description, 1
					if tc.change != nil {
						tc.change(details)
					}
				}
				return details, nil
			}
			var effects []string
			var reserved []string
			var delivered string
			client := &fakeCoordinatorReservationClient{}
			client.reserveFn = func(_ context.Context, request agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
				effects = append(effects, "reserve")
				reserved = append([]string(nil), request.Paths...)
				if tc.reserveErr {
					return nil, errors.New("reservation response lost")
				}
				result := &agentmail.ReservationResult{}
				if tc.conflict {
					result.Conflicts = []agentmail.ReservationConflict{{Path: request.Paths[0]}}
					return result, nil
				}
				for i, path := range request.Paths {
					result.Granted = append(result.Granted, agentmail.FileReservation{
						ID: 91 + i, ProjectID: 9, AgentName: request.AgentName,
						PathPattern: path, Exclusive: true, Reason: request.Reason,
						ExpiresTS: agentmail.FlexTime{Time: time.Now().Add(time.Hour)},
					})
				}
				return result, nil
			}
			c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
				claim := assignmentstore.ClaimFunc(func(_ context.Context, id, actor string) (assignmentstore.ClaimReceipt, error) {
					effects = append(effects, "claim")
					return assignmentstore.ClaimReceipt{BeadID: id, Actor: actor, Status: "in_progress", ClaimedAt: time.Now()}, nil
				})
				reservation := &coordinatorAgentMailReservationPort{client: client, projectKey: projectDir}
				dispatch := assignmentstore.DispatchFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
					effects = append(effects, "dispatch")
					delivered = request.Prompt
					return assignmentstore.DispatchReceipt{DeliveryID: "mail-scope"}, nil
				})
				preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
					return assignmentstore.PromptPreflightResult{DispatchPrompt: request.Prompt, DurablePrompt: request.Prompt, DurableTitle: request.BeadTitle}, nil
				})
				return assignmentstore.NewAtomicCoordinator(store, claim, reservation, dispatch, preflight)
			}

			results, err := c.AssignWork(t.Context())
			if err != nil || len(results) != 1 || lookups != 2 {
				t.Fatalf("AssignWork results=%+v err=%v live lookups=%d", results, err, lookups)
			}
			sort.Strings(reserved)
			if !reflect.DeepEqual(reserved, tc.wantPaths) {
				t.Fatalf("reserved %v, want live write set %v", reserved, tc.wantPaths)
			}
			if tc.wantError != "" {
				if results[0].Success || results[0].MessageSent || !strings.Contains(results[0].Error, tc.wantError) {
					t.Fatalf("result=%+v, want no dispatch and error containing %q", results[0], tc.wantError)
				}
				wantEffects := ""
				if tc.conflict || tc.reserveErr {
					wantEffects = "claim,reserve"
				}
				if strings.Join(effects, ",") != wantEffects || delivered != "" {
					t.Fatalf("effects=%v delivered=%q, want %q and no dispatch", effects, delivered, wantEffects)
				}
				return
			}
			wantEffects := "claim,dispatch"
			if len(tc.wantPaths) > 0 {
				wantEffects = "claim,reserve,dispatch"
			}
			if !results[0].Success || results[0].Error != "" || strings.Join(effects, ",") != wantEffects {
				t.Fatalf("result=%+v effects=%v, want successful %s", results[0], effects, wantEffects)
			}
			if !strings.Contains(delivered, "**Title:** "+tc.title) || strings.Contains(delivered, "old/output.go") {
				t.Fatalf("delivered stale assignment: %s", delivered)
			}
			for _, path := range tc.wantPaths {
				if !strings.Contains(delivered, path) {
					t.Fatalf("delivered prompt omits reserved output %q: %s", path, delivered)
				}
			}
			store, err := assignmentstore.LoadStoreStrict(c.session)
			if err != nil {
				t.Fatal(err)
			}
			recorded := store.Get(beadID)
			if recorded == nil || recorded.ReservationRequired != (len(tc.wantPaths) > 0) ||
				!reflect.DeepEqual(recorded.ReservationRequested, tc.wantPaths) || recorded.BeadTitle != tc.title {
				t.Fatalf("durable assignment=%+v, want exact live title and write set %v", recorded, tc.wantPaths)
			}
		})
	}
}
