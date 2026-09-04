package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	assignmentstore "github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/status"
)

type fakeCoordinatorReservationClient struct {
	project        *agentmail.Project
	ensureErr      error
	reserveResult  *agentmail.ReservationResult
	reserveErr     error
	reservations   []agentmail.FileReservation
	listErr        error
	releaseResult  *agentmail.ReleaseReservationsResult
	releaseErr     error
	releaseIDs     [][]int
	releasePaths   [][]string
	keepOnRelease  bool
	releaseStarted chan struct{}
	allowRelease   chan struct{}
	releaseOnce    sync.Once
}

func (f *fakeCoordinatorReservationClient) EnsureProject(_ context.Context, projectKey string) (*agentmail.Project, error) {
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if f.project != nil {
		copy := *f.project
		return &copy, nil
	}
	return &agentmail.Project{ID: 9, HumanKey: projectKey}, nil
}

func (f *fakeCoordinatorReservationClient) ReservePaths(context.Context, agentmail.FileReservationOptions) (*agentmail.ReservationResult, error) {
	return f.reserveResult, f.reserveErr
}

func (f *fakeCoordinatorReservationClient) ListReservations(context.Context, string, string, bool) ([]agentmail.FileReservation, error) {
	return append([]agentmail.FileReservation(nil), f.reservations...), f.listErr
}

func (f *fakeCoordinatorReservationClient) ReleaseReservations(_ context.Context, _ string, _ string, paths []string, ids []int) (*agentmail.ReleaseReservationsResult, error) {
	if f.releaseStarted != nil {
		f.releaseOnce.Do(func() { close(f.releaseStarted) })
		<-f.allowRelease
	}
	f.releaseIDs = append(f.releaseIDs, append([]int(nil), ids...))
	f.releasePaths = append(f.releasePaths, append([]string(nil), paths...))
	if f.releaseErr != nil {
		return f.releaseResult, f.releaseErr
	}
	wantedIDs := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		wantedIDs[id] = struct{}{}
	}
	wantedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		wantedPaths[path] = struct{}{}
	}
	if !f.keepOnRelease {
		remaining := f.reservations[:0]
		for _, reservation := range f.reservations {
			_, byID := wantedIDs[reservation.ID]
			_, byPath := wantedPaths[reservation.PathPattern]
			if !byID && !byPath {
				remaining = append(remaining, reservation)
			}
		}
		f.reservations = remaining
	}
	if f.releaseResult != nil {
		return f.releaseResult, nil
	}
	return &agentmail.ReleaseReservationsResult{Released: len(ids) + len(paths)}, nil
}

func (f *fakeCoordinatorReservationClient) RenewReservations(context.Context, agentmail.RenewReservationsOptions) (*agentmail.RenewReservationsResult, error) {
	return &agentmail.RenewReservationsResult{}, nil
}

func TestWorkAssignmentStruct(t *testing.T) {
	now := time.Now()
	wa := WorkAssignment{
		BeadID:         "ntm-1234",
		BeadTitle:      "Implement feature X",
		AgentPaneID:    "%0",
		AgentMailName:  "BlueFox",
		AgentType:      "cc",
		AssignedAt:     now,
		Priority:       1,
		Score:          0.85,
		FilesToReserve: []string{"internal/feature/*.go"},
	}

	if wa.BeadID != "ntm-1234" {
		t.Errorf("expected BeadID 'ntm-1234', got %q", wa.BeadID)
	}
	if wa.Score != 0.85 {
		t.Errorf("expected Score 0.85, got %f", wa.Score)
	}
	if len(wa.FilesToReserve) != 1 {
		t.Errorf("expected 1 file to reserve, got %d", len(wa.FilesToReserve))
	}
}

func TestAssignmentResultStruct(t *testing.T) {
	ar := AssignmentResult{
		Success:      true,
		MessageSent:  true,
		Reservations: []string{"internal/*.go"},
	}

	if !ar.Success {
		t.Error("expected Success to be true")
	}
	if !ar.MessageSent {
		t.Error("expected MessageSent to be true")
	}
	if ar.Error != "" {
		t.Error("expected empty error on success")
	}
}

func TestCoordinatorWorkAssignedEventUsesOnlyDurableTitle(t *testing.T) {
	planned := &WorkAssignment{BeadID: "ntm-event", BeadTitle: "token=raw-secret", Score: 0.75}
	agentState := &AgentState{AgentType: "cod"}
	details := coordinatorWorkAssignedEventDetails(AssignmentResult{
		Assignment: &WorkAssignment{BeadID: "ntm-event", BeadTitle: "token=[REDACTED]"},
	}, planned, agentState)
	if details["bead_title"] != "token=[REDACTED]" || details["bead_id"] != "ntm-event" || details["agent_type"] != "cod" || details["score"] != 0.75 {
		t.Fatalf("durable event details=%+v", details)
	}
	if strings.Contains(fmt.Sprint(details), "raw-secret") {
		t.Fatalf("event details leaked planned title: %+v", details)
	}

	withoutDurable := coordinatorWorkAssignedEventDetails(AssignmentResult{}, planned, agentState)
	if withoutDurable["bead_title"] != "" || strings.Contains(fmt.Sprint(withoutDurable), "raw-secret") {
		t.Fatalf("event fallback leaked planned title: %+v", withoutDurable)
	}
}

func TestScoreAndSelectAssignmentsSkipsGatedRecommendations(t *testing.T) {
	agent := &AgentState{
		PaneID:        "%0",
		AgentType:     "cc",
		AgentMailName: "BlueFox",
		Status:        robot.StateWaiting,
		Healthy:       true,
	}

	recs := []bv.TriageRecommendation{
		{ID: "ntm-001", Title: "Blocked Task", Status: "Blocked", Score: 0.9},
		{ID: "ntm-dependency", Title: "Dependency gated", Status: "open", BlockedBy: []string{"ntm-prerequisite"}, Score: 0.9},
		{ID: "ntm-operator", Title: "Operator gated", Status: "open", Labels: []string{"human-input"}, Score: 0.9},
		{ID: "ntm-002", Title: "Ready Task", Status: "open", Priority: 1, Score: 0.8},
		{ID: "ntm-003", Title: "Another Ready", Status: "open", Priority: 2, Score: 0.7},
	}

	selected := ScoreAndSelectAssignments([]*AgentState{agent}, recs, DefaultScoreConfig(), nil)

	if len(selected) != 1 {
		t.Fatalf("expected 1 assignment for 1 agent, got %d", len(selected))
	}
	if selected[0].Assignment.BeadID != "ntm-002" {
		t.Errorf("expected BeadID 'ntm-002' (highest-scoring ungated), got %q", selected[0].Assignment.BeadID)
	}
	if selected[0].Assignment.AgentMailName != "BlueFox" {
		t.Errorf("expected AgentMailName 'BlueFox', got %q", selected[0].Assignment.AgentMailName)
	}
}

func TestScoreAndSelectAssignmentsAllGated(t *testing.T) {
	agent := &AgentState{
		PaneID:    "%0",
		AgentType: "cc",
	}

	recs := []bv.TriageRecommendation{
		{ID: "ntm-001", Title: "Blocked 1", Status: "Blocked", Score: 0.9},
		{ID: "ntm-002", Title: "Dependency", Status: "open", BlockedBy: []string{"ntm-prerequisite"}, Score: 0.9},
		{ID: "ntm-003", Title: "Operator", Status: "open", Labels: []string{"blocked-on-operator"}, Score: 0.9},
	}

	selected := ScoreAndSelectAssignments([]*AgentState{agent}, recs, DefaultScoreConfig(), nil)

	if len(selected) != 0 {
		t.Errorf("expected no assignments when all recommendations are gated, got %d", len(selected))
	}
}

func TestAttemptAssignmentRequiresMailClient(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	assignment := &WorkAssignment{
		BeadID:        "ntm-001",
		BeadTitle:     "Fix bug",
		AgentPaneID:   "%0",
		AgentMailName: "BlueFox",
		AgentType:     "cc",
	}
	rec := &bv.TriageRecommendation{ID: "ntm-001", Title: "Fix bug"}

	result := c.attemptAssignment(t.Context(), assignment, rec)
	if result.Success {
		t.Fatal("expected assignment to fail without mail client")
	}
	if result.MessageSent {
		t.Fatal("expected no message to be sent without mail client")
	}
	if !strings.Contains(result.Error, "mail client") {
		t.Fatalf("expected missing mail client error, got %q", result.Error)
	}
}

func TestAttemptAssignmentRequiresAgentMailIdentity(t *testing.T) {
	c := New("test-session", "/tmp/test", &agentmail.Client{}, "TestAgent")
	assignment := &WorkAssignment{
		BeadID:      "ntm-001",
		BeadTitle:   "Fix bug",
		AgentPaneID: "%0",
		AgentType:   "cc",
	}
	rec := &bv.TriageRecommendation{ID: "ntm-001", Title: "Fix bug"}

	result := c.attemptAssignment(t.Context(), assignment, rec)
	if result.Success {
		t.Fatal("expected assignment to fail without agent-mail identity")
	}
	if result.MessageSent {
		t.Fatal("expected no message to be sent without agent-mail identity")
	}
	if !strings.Contains(result.Error, "agent-mail identity") {
		t.Fatalf("expected missing identity error, got %q", result.Error)
	}
}

func TestAttemptAssignmentAtomicSuccessPersistsReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	// Reservation candidates must resolve inside the repository (ntm#302), so
	// the path this assignment reserves has to actually exist.
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "coordinator"), 0o755); err != nil {
		t.Fatalf("create repository path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "coordinator", "assign.go"), []byte("package coordinator\n"), 0o600); err != nil {
		t.Fatalf("write repository path: %v", err)
	}
	c := New("coordinator-atomic-success", projectDir, &agentmail.Client{}, "CoordinatorAgent")
	var order []string
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(_ context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
			order = append(order, "claim")
			return assignmentstore.ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		})
		reserve := assignmentstore.ReservationFunc(func(_ context.Context, request assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
			order = append(order, "reserve")
			expiresAt := time.Now().UTC().Add(time.Hour)
			return assignmentstore.LeaseReceipt{
				AgentName: request.AgentName, Target: request.Target,
				Requested: append([]string(nil), request.RequestedPaths...),
				Granted:   append([]string(nil), request.RequestedPaths...), ReservationIDs: []int{73}, ExpiresAt: &expiresAt,
			}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			order = append(order, "dispatch")
			if request.Target != "%9" || request.AgentName != "BlueFox" || !strings.Contains(request.Prompt, "ntm-success") {
				t.Fatalf("dispatch request = %+v", request)
			}
			return assignmentstore.DispatchReceipt{DeliveryID: "mail-91"}, nil
		})
		preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
			return assignmentstore.PromptPreflightResult{DispatchPrompt: request.Prompt, DurablePrompt: request.Prompt}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
	}

	work := &WorkAssignment{
		BeadID: "ntm-success", BeadTitle: "Atomic coordinator success", AgentPaneID: "%9", AgentPaneIndex: 4,
		AgentMailName: "BlueFox", AgentType: "cod", Priority: 1, Score: 0.9,
		FilesToReserve: []string{"internal/coordinator/assign.go"},
	}
	result := c.attemptAssignment(t.Context(), work, &bv.TriageRecommendation{ID: work.BeadID, Title: work.BeadTitle})
	if !result.Success || !result.MessageSent || result.Error != "" || result.ClaimActor == "" || result.IdempotencyKey == "" {
		t.Fatalf("attemptAssignment result = %+v", result)
	}
	if strings.Join(order, ",") != "claim,reserve,dispatch" || len(result.Reservations) != 1 || result.Reservations[0] != "internal/coordinator/assign.go" {
		t.Fatalf("assignment order=%v reservations=%v", order, result.Reservations)
	}
	store, err := assignmentstore.LoadStoreStrict(c.session)
	if err != nil {
		t.Fatalf("LoadStoreStrict: %v", err)
	}
	stored := store.Get(work.BeadID)
	if stored == nil || stored.DispatchState != assignmentstore.DispatchSent || stored.DispatchReceiptID != "mail-91" ||
		stored.ClaimActor != result.ClaimActor || stored.IdempotencyKey != result.IdempotencyKey || stored.OccupancyKey != "%9" {
		t.Fatalf("durable coordinator assignment = %+v", stored)
	}
}

func TestExecuteAtomicAssignmentDoesNotProjectForeignTargetOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-foreign-occupant"
	store := assignmentstore.NewStore(session)
	foreign, err := store.Assign("ntm-foreign", "foreign-secret-title", 4, "cod", "OtherAgent", "foreign prompt")
	if err != nil {
		t.Fatalf("seed foreign assignment: %v", err)
	}
	foreign.IdempotencyKey = "foreign-secret-idempotency-key"
	foreign.DispatchTarget = "%9"
	foreign.OccupancyKey = "%9"
	store.Assignments[foreign.BeadID] = foreign
	if err := store.Save(); err != nil {
		t.Fatalf("persist foreign assignment: %v", err)
	}

	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(context.Context, string, string) (assignmentstore.ClaimReceipt, error) {
			t.Fatal("foreign target race reached claim")
			return assignmentstore.ClaimReceipt{}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(context.Context, assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			t.Fatal("foreign target race reached dispatch")
			return assignmentstore.DispatchReceipt{}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, nil, dispatch, nil)
	}
	work := &WorkAssignment{
		BeadID: "ntm-requested", BeadTitle: "requested-raw-secret", AgentPaneID: "%9", AgentPaneIndex: 4,
		AgentMailName: "BlueLake", AgentType: "cod", FilesToReserve: []string{"internal/requested/**"},
	}
	request := assignmentstore.AtomicRequest{
		BeadID: work.BeadID, BeadTitle: work.BeadTitle, Target: work.AgentPaneID, OccupancyKey: work.AgentPaneID,
		Pane: work.AgentPaneIndex, AgentType: work.AgentType, AgentName: work.AgentMailName, Actor: work.AgentMailName,
		Prompt: "requested prompt", IdempotencyKey: "requested-idempotency-key",
	}
	result := c.executeAtomicAssignment(t.Context(), store, work, request)
	if result.Error == "" || !strings.Contains(result.Error, assignmentstore.ErrTargetOccupied.Error()) {
		t.Fatalf("foreign target result=%+v", result)
	}
	if result.Assignment == nil || result.Assignment.BeadID != work.BeadID || result.Assignment.BeadTitle != "" || len(result.Assignment.FilesToReserve) != 0 {
		t.Fatalf("foreign target projected authoritative row: %+v", result.Assignment)
	}
	if strings.Contains(fmt.Sprint(result), "foreign-secret") {
		t.Fatalf("foreign target leaked owner metadata: %+v", result)
	}
}

func TestValidatedCoordinatorAgentMailProjectIDRequiresExactHumanKey(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		project    *agentmail.Project
		projectKey string
		wantOK     bool
	}{
		{name: "exact", project: &agentmail.Project{ID: 9, HumanKey: "/project"}, projectKey: "/project", wantOK: true},
		{name: "blank human key", project: &agentmail.Project{ID: 9}, projectKey: "/project"},
		{name: "wrong human key", project: &agentmail.Project{ID: 9, HumanKey: "/other"}, projectKey: "/project"},
		{name: "blank expected key", project: &agentmail.Project{ID: 9}, projectKey: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			id, err := validatedCoordinatorAgentMailProjectID(test.project, test.projectKey)
			if test.wantOK && (err != nil || id != 9) {
				t.Fatalf("validated project id=%d error=%v", id, err)
			}
			if !test.wantOK && err == nil {
				t.Fatalf("validated malformed project id=%d", id)
			}
		})
	}
}

func TestCoordinatorAtomicPreflightBlocksSensitiveReservationPathBeforeClaim(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	c := New("coordinator-sensitive-path", projectDir, &agentmail.Client{}, "CoordinatorAgent")
	c.reservationClient = &fakeCoordinatorReservationClient{}
	store := assignmentstore.NewStore(c.session)
	coordinator := c.newAtomicAssignmentCoordinator(store)
	_, err := coordinator.Execute(t.Context(), assignmentstore.AtomicRequest{
		BeadID: "ntm-sensitive-path", BeadTitle: "Safe title", Target: "%41", OccupancyKey: "%41",
		Pane: 1, AgentType: "cod", AgentName: "BlueLake", Actor: "BlueLake", Prompt: "safe prompt",
		IdempotencyKey: "sensitive-path-key",
		RequestedPaths: []string{"internal/" + "sk-proj-FAKEtestkey1234567890123456789012345678901234" + ".txt"},
	})
	if err == nil || !strings.Contains(err.Error(), "reservation path") {
		t.Fatalf("sensitive path preflight error=%v", err)
	}
	if stored := store.Get("ntm-sensitive-path"); stored != nil {
		t.Fatalf("sensitive path reached durable ledger: %+v", stored)
	}
}

func TestCoordinatorAtomicPolicySnapshotsRemainProjectIsolatedConcurrently(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := t.Context()
	const (
		gateA = "project-a-approval"
		gateB = "project-b-approval"
	)

	type projectRun struct {
		name        string
		coordinator *SessionCoordinator
		ownBead     string
		crossBead   string
		ownGate     string
		crossGate   string
		claimStop   error
		claimPolicy []string
		claimMu     sync.Mutex
	}
	newProjectRun := func(name, ownGate, crossGate string) *projectRun {
		projectDir := t.TempDir()
		if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, []string{ownGate}); err != nil {
			t.Fatalf("configure %s policy: %v", name, err)
		}
		run := &projectRun{
			name: name, ownBead: "ntm-" + name + "-own-gate", crossBead: "ntm-" + name + "-cross-gate",
			ownGate: ownGate, crossGate: crossGate, claimStop: errors.New(name + " claim policy captured"),
		}
		run.coordinator = New("coordinator-policy-"+name, projectDir, &agentmail.Client{}, "CoordinatorAgent")
		run.coordinator.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
			label := run.crossGate
			if beadID == run.ownBead {
				label = run.ownGate
			}
			return &bv.BeadAssignmentDetails{ID: beadID, Status: "open", Labels: []string{label}}, nil
		}
		run.coordinator.claimBeadForAssignmentFn = func(_ context.Context, _ string, beadID, actor string, gatedLabels []string) (bv.BeadClaimResult, error) {
			run.claimMu.Lock()
			run.claimPolicy = append([]string(nil), gatedLabels...)
			run.claimMu.Unlock()
			return bv.BeadClaimResult{ID: beadID, Actor: actor}, run.claimStop
		}
		return run
	}

	runs := []*projectRun{
		newProjectRun("a", gateA, gateB),
		newProjectRun("b", gateB, gateA),
	}
	type runResult struct {
		run        *projectRun
		ownErr     error
		crossErr   error
		ownMutated bool
		policy     []string
	}
	results := make(chan runResult, len(runs))
	start := make(chan struct{})
	runProject := func(run *projectRun) {
		<-start
		execute := func(beadID, key, target string) (*assignmentstore.AssignmentStore, error) {
			store := assignmentstore.NewStore("coordinator-policy-" + run.name + "-" + key)
			_, err := run.coordinator.newAtomicAssignmentCoordinator(store).Execute(ctx, assignmentstore.AtomicRequest{
				BeadID: beadID, BeadTitle: "Project policy isolation", Target: target, OccupancyKey: target,
				Pane: 1, AgentType: "cod", AgentName: "BlueLake", Actor: "BlueLake",
				Prompt: "verify isolated policy", IdempotencyKey: key,
			})
			return store, err
		}
		ownStore, ownErr := execute(run.ownBead, "own", "%501")
		_, crossErr := execute(run.crossBead, "cross", "%502")
		run.claimMu.Lock()
		policy := append([]string(nil), run.claimPolicy...)
		run.claimMu.Unlock()
		results <- runResult{run: run, ownErr: ownErr, crossErr: crossErr, ownMutated: ownStore.Get(run.ownBead) != nil, policy: policy}
	}
	go runProject(runs[0])
	go runProject(runs[1])
	close(start)

	containsLabel := func(labels []string, wanted string) bool {
		for _, label := range labels {
			if strings.EqualFold(strings.TrimSpace(label), wanted) {
				return true
			}
		}
		return false
	}
	for range runs {
		result := <-results
		if !errors.Is(result.ownErr, assignmentstore.ErrClaimIneligible) || result.ownMutated {
			t.Fatalf("project %s own gate error=%v mutated=%v", result.run.name, result.ownErr, result.ownMutated)
		}
		if !errors.Is(result.crossErr, result.run.claimStop) {
			t.Fatalf("project %s cross gate error=%v, want claim callback", result.run.name, result.crossErr)
		}
		if !containsLabel(result.policy, result.run.ownGate) || containsLabel(result.policy, result.run.crossGate) {
			t.Fatalf("project %s claim policy=%v, want own gate %q without %q", result.run.name, result.policy, result.run.ownGate, result.run.crossGate)
		}
		if !reflect.DeepEqual(result.policy, result.run.coordinator.operatorGatedLabels) {
			t.Fatalf("project %s preauthorization/CAS snapshots differ: claim=%v coordinator=%v", result.run.name, result.policy, result.run.coordinator.operatorGatedLabels)
		}
	}
}

func TestCoordinatorReservationPortPreservesPartialLeaseHandles(t *testing.T) {
	client := &fakeCoordinatorReservationClient{reserveResult: &agentmail.ReservationResult{
		Granted: []agentmail.FileReservation{{
			ID: 71, PathPattern: "internal/coordinator/**", AgentName: "BlueLake", ProjectID: 9, Exclusive: true,
			ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
			Reason:    "bead assignment: ntm-partial",
		}},
		Conflicts: []agentmail.ReservationConflict{{Path: "internal/robot/**", Holders: []string{"OtherAgent"}}},
	}}
	port := &coordinatorAgentMailReservationPort{client: client, projectKey: "/project"}
	lease, err := port.Reserve(t.Context(), assignmentstore.ReservationRequest{
		BeadID: "ntm-partial", AgentName: "BlueLake", Target: "%1",
		RequestedPaths: []string{"internal/coordinator/**", "internal/robot/**"},
	})
	if err == nil || assignmentstore.IsGuaranteedNoReservation(err) {
		t.Fatalf("Reserve() error = %v, want ambiguous partial-lease failure", err)
	}
	if !reflect.DeepEqual(lease.Granted, []string{"internal/coordinator/**"}) || !reflect.DeepEqual(lease.ReservationIDs, []int{71}) {
		t.Fatalf("partial lease = %+v, want retained release handles", lease)
	}
}

func TestCoordinatorReservationPortPreservesMalformedReceiptHandle(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agentmail.FileReservation)
	}{
		{name: "missing project", mutate: func(row *agentmail.FileReservation) { row.ProjectID = 0 }},
		{name: "wrong project", mutate: func(row *agentmail.FileReservation) { row.ProjectID = 10 }},
		{name: "nonexclusive", mutate: func(row *agentmail.FileReservation) { row.Exclusive = false }},
		{name: "wrong agent", mutate: func(row *agentmail.FileReservation) { row.AgentName = "OtherAgent" }},
		{name: "wrong reason", mutate: func(row *agentmail.FileReservation) { row.Reason = "bead assignment: ntm-other" }},
		{name: "unexpected path", mutate: func(row *agentmail.FileReservation) { row.PathPattern = "internal/unexpected/**" }},
		{name: "missing expiry", mutate: func(row *agentmail.FileReservation) { row.ExpiresTS = agentmail.FlexTime{} }},
		{name: "expired", mutate: func(row *agentmail.FileReservation) {
			row.ExpiresTS = agentmail.FlexTime{Time: time.Now().UTC().Add(-time.Second)}
		}},
		{name: "released", mutate: func(row *agentmail.FileReservation) {
			released := agentmail.FlexTime{Time: time.Now().UTC()}
			row.ReleasedTS = &released
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := agentmail.FileReservation{
				ID: 79, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/coordinator/**", Exclusive: true,
				ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
				Reason:    "bead assignment: ntm-malformed",
			}
			test.mutate(&row)
			client := &fakeCoordinatorReservationClient{reserveResult: &agentmail.ReservationResult{Granted: []agentmail.FileReservation{row}}}
			port := &coordinatorAgentMailReservationPort{client: client, projectKey: "/project"}
			lease, err := port.Reserve(t.Context(), assignmentstore.ReservationRequest{
				BeadID: "ntm-malformed", AgentName: "BlueLake", Target: "%1", RequestedPaths: []string{"internal/coordinator/**"},
			})
			if err == nil || !reflect.DeepEqual(lease.ReservationIDs, []int{79}) {
				t.Fatalf("Reserve() lease=%+v error=%v, want retained positive handle", lease, err)
			}
		})
	}
}

func TestCoordinatorReservationPortReconcilesInterruptedLease(t *testing.T) {
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{
		{ID: 81, PathPattern: "internal/coordinator/**", AgentName: "BlueLake", ProjectID: 9, Exclusive: true, Reason: "bead assignment: ntm-reconcile", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}},
		{ID: 82, PathPattern: "internal/robot/**", AgentName: "BlueLake", ProjectID: 9, Exclusive: true, Reason: "bead assignment: ntm-reconcile", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}},
	}}
	port := &coordinatorAgentMailReservationPort{client: client, projectKey: "/project"}
	got, err := port.ReconcileReservation(t.Context(), assignmentstore.ReservationRequest{
		BeadID: "ntm-reconcile", AgentName: "BlueLake", Target: "%1",
		RequestedPaths: []string{"internal/coordinator/**", "internal/robot/**"},
	}, assignmentstore.LeaseReceipt{})
	if err != nil {
		t.Fatalf("ReconcileReservation: %v", err)
	}
	if got.State != assignmentstore.ReservationReconciliationReserved || !reflect.DeepEqual(got.Lease.ReservationIDs, []int{81, 82}) {
		t.Fatalf("reconciliation = %+v, want complete reserved lease", got)
	}
}

func TestCoordinatorReservationPortReconcilesPartialLeaseAsKnownReserved(t *testing.T) {
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 83, PathPattern: "internal/coordinator/**", AgentName: "BlueLake", ProjectID: 9, Exclusive: true,
		Reason: "bead assignment: ntm-partial-reconcile", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	port := &coordinatorAgentMailReservationPort{client: client, projectKey: "/project"}
	got, err := port.ReconcileReservation(t.Context(), assignmentstore.ReservationRequest{
		BeadID: "ntm-partial-reconcile", AgentName: "BlueLake", Target: "%1",
		RequestedPaths: []string{"internal/coordinator/**", "internal/robot/**"},
	}, assignmentstore.LeaseReceipt{})
	if err != nil {
		t.Fatalf("ReconcileReservation: %v", err)
	}
	if got.State != assignmentstore.ReservationReconciliationReserved || !reflect.DeepEqual(got.Lease.ReservationIDs, []int{83}) ||
		!reflect.DeepEqual(got.Lease.Granted, []string{"internal/coordinator/**"}) {
		t.Fatalf("partial reconciliation = %+v, want known reserved handles", got)
	}
}

func TestCoordinatorReservationPortReconcileMalformedReceiptRetainsHandle(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*agentmail.FileReservation)
	}{
		{name: "missing project", mutate: func(row *agentmail.FileReservation) { row.ProjectID = 0 }},
		{name: "wrong project", mutate: func(row *agentmail.FileReservation) { row.ProjectID = 10 }},
		{name: "nonexclusive", mutate: func(row *agentmail.FileReservation) { row.Exclusive = false }},
		{name: "missing expiry", mutate: func(row *agentmail.FileReservation) { row.ExpiresTS = agentmail.FlexTime{} }},
		{name: "expired", mutate: func(row *agentmail.FileReservation) {
			row.ExpiresTS = agentmail.FlexTime{Time: time.Now().UTC().Add(-time.Second)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := agentmail.FileReservation{
				ID: 84, PathPattern: "internal/coordinator/**", AgentName: "BlueLake", ProjectID: 9, Exclusive: true,
				Reason: "bead assignment: ntm-malformed-reconcile", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
			}
			test.mutate(&row)
			client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{row}}
			port := &coordinatorAgentMailReservationPort{client: client, projectKey: "/project"}
			got, err := port.ReconcileReservation(t.Context(), assignmentstore.ReservationRequest{
				BeadID: "ntm-malformed-reconcile", AgentName: "BlueLake", Target: "%1", RequestedPaths: []string{"internal/coordinator/**"},
			}, assignmentstore.LeaseReceipt{})
			if err != nil {
				t.Fatalf("ReconcileReservation: %v", err)
			}
			if got.State != assignmentstore.ReservationReconciliationUnknown || !reflect.DeepEqual(got.Lease.ReservationIDs, []int{84}) {
				t.Fatalf("malformed reconciliation = %+v, want unknown with retained handle", got)
			}
		})
	}
}

func TestCoordinatorReservationPortPreflightFailureGuaranteesNoLease(t *testing.T) {
	port := &coordinatorAgentMailReservationPort{client: &fakeCoordinatorReservationClient{}, projectKey: "/project"}
	_, err := port.Reserve(t.Context(), assignmentstore.ReservationRequest{
		AgentName: "BlueLake", RequestedPaths: []string{"internal/**", "internal/**"},
	})
	if err == nil || !assignmentstore.IsGuaranteedNoReservation(err) {
		t.Fatalf("Reserve() error = %v, want guaranteed no-reservation failure", err)
	}
}

func coordinatorReleaseBarrier(beadID, agentName string) *assignmentstore.Assignment {
	return &assignmentstore.Assignment{
		BeadID: beadID, AgentName: agentName, ClearState: assignmentstore.ClearStateReservationReleasing,
	}
}

func TestReleaseCoordinatorLeaseVerifiesAbsenceDespiteSuccessCount(t *testing.T) {
	t.Parallel()
	reservation := agentmail.FileReservation{
		ID: 91, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/coordinator/**", Exclusive: true,
		Reason: "bead assignment: ntm-release-proof", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}
	client := &fakeCoordinatorReservationClient{
		reservations:  []agentmail.FileReservation{reservation},
		releaseResult: &agentmail.ReleaseReservationsResult{Released: 1},
		keepOnRelease: true,
	}
	c := New("release-proof", "/project", nil, "Coordinator")
	c.reservationClient = client
	lease := assignmentstore.LeaseReceipt{AgentName: "BlueLake", ReservationIDs: []int{91}, Granted: []string{"internal/coordinator/**"}}
	barrier := coordinatorReleaseBarrier("ntm-release-proof", "BlueLake")
	if err := c.releaseCoordinatorLease(t.Context(), barrier, lease); err == nil || !strings.Contains(err.Error(), "remain active") {
		t.Fatalf("unverified success-count error=%v", err)
	}
	client.keepOnRelease = false
	if err := c.releaseCoordinatorLease(t.Context(), barrier, lease); err != nil {
		t.Fatalf("verified retry: %v", err)
	}
}

func TestReleaseCoordinatorLeaseNeverReleasesDifferentBeadOnSamePath(t *testing.T) {
	t.Parallel()
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{
		{ID: 92, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/shared.go", Exclusive: true, Reason: "bead assignment: ntm-old", ExpiresTS: expires},
		{ID: 93, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/shared.go", Exclusive: true, Reason: "bead assignment: ntm-new", ExpiresTS: expires},
	}}
	c := New("release-collateral", "/project", nil, "Coordinator")
	c.reservationClient = client
	if err := c.releaseCoordinatorLease(t.Context(), coordinatorReleaseBarrier("ntm-old", "BlueLake"), assignmentstore.LeaseReceipt{
		AgentName: "BlueLake", Granted: []string{"internal/shared.go"},
	}); err != nil {
		t.Fatalf("release exact old lease: %v", err)
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{92}) {
		t.Fatalf("release IDs=%v, want only old bead ID", client.releaseIDs)
	}
	if len(client.reservations) != 1 || client.reservations[0].ID != 93 {
		t.Fatalf("different-bead reservation was changed: %+v", client.reservations)
	}
}

func TestReleaseCoordinatorLeasePathOnlyRejectsDuplicateExactLeasesBeforeRelease(t *testing.T) {
	t.Parallel()
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{
		{ID: 921, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/shared.go", Exclusive: true, Reason: "bead assignment: ntm-duplicate", ExpiresTS: expires},
		{ID: 922, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/shared.go", Exclusive: true, Reason: "bead assignment: ntm-duplicate", ExpiresTS: expires},
		{ID: 923, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/unique.go", Exclusive: true, Reason: "bead assignment: ntm-duplicate", ExpiresTS: expires},
	}}
	c := New("release-duplicate", "/project", nil, "Coordinator")
	c.reservationClient = client
	err := c.releaseCoordinatorLease(t.Context(), coordinatorReleaseBarrier("ntm-duplicate", "BlueLake"), assignmentstore.LeaseReceipt{
		AgentName: "BlueLake", Granted: []string{"internal/shared.go", "internal/unique.go"},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate path-only cleanup error=%v", err)
	}
	if len(client.releaseIDs) != 0 || len(client.releasePaths) != 0 || len(client.reservations) != 3 {
		t.Fatalf("ambiguous cleanup actuated release: ids=%v paths=%v reservations=%+v", client.releaseIDs, client.releasePaths, client.reservations)
	}
}

func TestReleaseCoordinatorLeaseByIDPreservesNewerGenerationForSameBead(t *testing.T) {
	t.Parallel()
	expires := agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{
		{ID: 94, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/old.go", Exclusive: true, Reason: "bead assignment: ntm-same", ExpiresTS: expires},
		{ID: 95, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/new.go", Exclusive: true, Reason: "bead assignment: ntm-same", ExpiresTS: expires},
	}}
	c := New("release-generation", "/project", nil, "Coordinator")
	c.reservationClient = client
	if err := c.releaseCoordinatorLease(t.Context(), coordinatorReleaseBarrier("ntm-same", "BlueLake"), assignmentstore.LeaseReceipt{
		AgentName: "BlueLake", ReservationIDs: []int{94}, Granted: []string{"internal/old.go"},
	}); err != nil {
		t.Fatalf("release exact prior generation: %v", err)
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{94}) {
		t.Fatalf("release IDs=%v, want only prior generation ID", client.releaseIDs)
	}
	if len(client.reservations) != 1 || client.reservations[0].ID != 95 {
		t.Fatalf("newer same-bead reservation was changed: %+v", client.reservations)
	}
}

func TestReleaseCoordinatorLeaseRecoversUniqueChangedIDForSameBeadAndPath(t *testing.T) {
	t.Parallel()
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 952, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/shared.go", Exclusive: true,
		Reason: "bead assignment: ntm-drift", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	c := New("release-drift", "/project", nil, "Coordinator")
	c.reservationClient = client
	err := c.releaseCoordinatorLease(t.Context(), coordinatorReleaseBarrier("ntm-drift", "BlueLake"), assignmentstore.LeaseReceipt{
		AgentName: "BlueLake", ReservationIDs: []int{951}, Granted: []string{"internal/shared.go"},
	})
	if err != nil {
		t.Fatalf("changed-ID cleanup error=%v", err)
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{952}) || len(client.reservations) != 0 {
		t.Fatalf("changed-ID cleanup calls=%v reservations=%+v", client.releaseIDs, client.reservations)
	}
}

func TestReleaseCoordinatorLeaseRejectsIDPathBindingMismatch(t *testing.T) {
	t.Parallel()
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 96, ProjectID: 9, AgentName: "BlueLake", PathPattern: "internal/new.go", Exclusive: true,
		Reason: "bead assignment: ntm-mismatch", ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	c := New("release-binding", "/project", nil, "Coordinator")
	c.reservationClient = client
	err := c.releaseCoordinatorLease(t.Context(), coordinatorReleaseBarrier("ntm-mismatch", "BlueLake"), assignmentstore.LeaseReceipt{
		AgentName: "BlueLake", ReservationIDs: []int{96}, Granted: []string{"internal/old.go"},
	})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("binding mismatch error=%v", err)
	}
	if len(client.releaseIDs) != 0 || len(client.reservations) != 1 || client.reservations[0].ID != 96 {
		t.Fatalf("mismatched reservation was changed: calls=%v reservations=%+v", client.releaseIDs, client.reservations)
	}
}

func TestAssignWorkSkipsPersistedActiveRecommendation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-active-filter"
	store := assignmentstore.NewStore(session)
	store.Assignments["ntm-active"] = &assignmentstore.Assignment{
		BeadID: "ntm-active", BeadTitle: "Already assigned", Pane: 1,
		DispatchTarget: "%1", OccupancyKey: "%1", AgentType: "cod", AgentName: "BlueLake",
		Status: assignmentstore.StatusAssigned, AssignedAt: time.Now().UTC(), PromptSent: "work",
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save canonical active assignment: %v", err)
	}
	c := New(session, t.TempDir(), nil, "CoordinatorAgent")
	c.config.AutoAssign = true
	c.config.IdleThreshold = 0
	now := time.Now().UTC()
	c.agents["%1"] = &AgentState{
		PaneID: "%1", PaneIndex: 1, AgentType: "cod", AgentMailName: "BlueLake",
		Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{
			ID: "ntm-active", Title: "Already assigned", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	c.workItemStatusFn = func(context.Context, string) (string, error) {
		return "in_progress", nil
	}
	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("active recommendation produced assignments: %+v", results)
	}
}

func TestAssignmentCandidatesHonorAssignOnlyIdle(t *testing.T) {
	c := New("coordinator-candidate-policy", t.TempDir(), nil, "CoordinatorAgent")
	now := time.Now().UTC()
	c.config.IdleThreshold = 0
	c.agents["%1"] = &AgentState{
		PaneID: "%1", Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.agents["%2"] = &AgentState{
		PaneID: "%2", Status: robot.StateGenerating, Healthy: true, SafeToDispatch: false,
		ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}

	c.config.AssignOnlyIdle = true
	idleOnly := c.getAssignmentCandidates()
	if len(idleOnly) != 1 || idleOnly[0].PaneID != "%1" {
		t.Fatalf("idle-only candidates = %+v, want only %%1", idleOnly)
	}

	c.config.AssignOnlyIdle = false
	allHealthy := c.getAssignmentCandidates()
	if len(allHealthy) != 2 {
		t.Fatalf("queue-enabled candidates = %+v, want both healthy current panes", allHealthy)
	}
}

func TestAssignWorkReconcilesClosedAssignmentAndReusesPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-terminal-reconciliation"
	store := assignmentstore.NewStore(session)
	if _, err := store.Assign("ntm-closed", "Already completed", 1, "cod", "BlueLake", "old work"); err != nil {
		t.Fatalf("seed closed assignment: %v", err)
	}

	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.config.AutoAssign = true
	c.config.AssignOnlyIdle = true
	c.config.IdleThreshold = 0
	now := time.Now().UTC()
	c.agents["%1"] = &AgentState{
		PaneID: "%1", PaneIndex: 1, AgentType: "cod", AgentMailName: "BlueLake",
		Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.workItemStatusFn = func(_ context.Context, beadID string) (string, error) {
		if beadID == "ntm-closed" {
			return "closed", nil
		}
		return "open", nil
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{
			ID: "ntm-new", Title: "Fresh work", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(_ context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
			return assignmentstore.ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		})
		reserve := assignmentstore.ReservationFunc(func(_ context.Context, request assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
			return assignmentstore.LeaseReceipt{AgentName: request.AgentName, Target: request.Target}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			if request.Target != "%1" || request.AgentName != "BlueLake" {
				t.Fatalf("dispatch request = %+v", request)
			}
			return assignmentstore.DispatchReceipt{DeliveryID: "mail-reuse"}, nil
		})
		preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
			return assignmentstore.PromptPreflightResult{DispatchPrompt: request.Prompt, DurablePrompt: request.Prompt}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
	}

	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].Assignment == nil || results[0].Assignment.BeadID != "ntm-new" {
		t.Fatalf("assignment results = %+v", results)
	}

	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload assignment ledger: %v", err)
	}
	if closed := reloaded.Get("ntm-closed"); closed == nil || closed.Status != assignmentstore.StatusCompleted {
		t.Fatalf("reconciled assignment = %+v, want completed", closed)
	}
	if fresh := reloaded.Get("ntm-new"); fresh == nil || fresh.Status != assignmentstore.StatusAssigned || fresh.DispatchReceiptID != "mail-reuse" {
		t.Fatalf("fresh assignment = %+v", fresh)
	}
}

func TestAssignWorkReconcilesClosedAssignmentBeforeCandidateGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-terminal-before-candidates"
	store := assignmentstore.NewStore(session)
	if _, err := store.Assign("ntm-closed", "Already completed", 1, "cod", "BlueLake", "old work"); err != nil {
		t.Fatalf("seed closed assignment: %v", err)
	}

	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.config.AutoAssign = true
	c.workItemStatusFn = func(_ context.Context, beadID string) (string, error) {
		if beadID != "ntm-closed" {
			t.Fatalf("unexpected status lookup for %s", beadID)
		}
		return "closed", nil
	}
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		t.Fatal("actionable planning must not run when there are no assignment candidates")
		return nil, nil
	}

	results, err := c.AssignWork(t.Context())
	if err != nil || len(results) != 0 {
		t.Fatalf("AssignWork results=%+v err=%v, want empty success", results, err)
	}
	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload assignment ledger: %v", err)
	}
	if closed := reloaded.Get("ntm-closed"); closed == nil || closed.Status != assignmentstore.StatusCompleted || closed.CompletedAt == nil {
		t.Fatalf("reconciled assignment = %+v, want completed before candidate gate", closed)
	}
}

func addEligibleCoordinatorAgent(c *SessionCoordinator, paneID, agentName string) {
	now := time.Now().UTC()
	c.config.AutoAssign = true
	c.config.AssignOnlyIdle = true
	c.config.IdleThreshold = 0
	c.agents[paneID] = &AgentState{
		PaneID: paneID, PaneIndex: 1, AgentType: "cod", AgentMailName: agentName,
		Status: robot.StateWaiting, Healthy: true, SafeToDispatch: true,
		LastActivity: now.Add(-time.Minute), ObservedAt: now, ObservationFreshness: status.FreshnessFresh,
	}
	c.workItemStatusFn = func(context.Context, string) (string, error) { return "in_progress", nil }
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return nil, nil }
}

func TestAssignWorkFailsClosedBeforePendingRecoveryWhenActionableVerificationFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tests := []struct {
		name string
		err  error
	}{
		{name: "plan", err: bv.ErrActionablePlanUnverified},
		{name: "labels", err: bv.ErrActionableLabelsUnverified},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := fmt.Sprintf("coordinator-actionable-%s-failure", test.name)
			request := assignmentstore.AtomicRequest{
				BeadID:         "ntm-pending-" + test.name,
				BeadTitle:      "Pending recovery must wait for verified " + test.name,
				Target:         fmt.Sprintf("%%%d", index+81),
				OccupancyKey:   fmt.Sprintf("%%%d", index+81),
				Pane:           index + 1,
				AgentType:      "cod",
				AgentName:      "BlueLake" + test.name,
				Actor:          "BlueLake" + test.name,
				Prompt:         "pending verified recovery prompt",
				IdempotencyKey: "coordinator-actionable-" + test.name + "-key",
			}
			store := assignmentstore.NewStore(session)
			actor := assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey)
			if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
				t.Fatalf("RecordAtomicIntent: %v", err)
			}
			if _, err := store.RecordAtomicClaim(request, assignmentstore.ClaimReceipt{
				BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatalf("RecordAtomicClaim: %v", err)
			}

			c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
			addEligibleCoordinatorAgent(c, request.Target, request.AgentName)
			verificationCalls := 0
			c.actionableRecommendationsFn = func(_ context.Context, projectKey string, limit int) ([]bv.TriageRecommendation, error) {
				verificationCalls++
				if projectKey != c.projectKey || limit != 0 {
					t.Fatalf("actionable verification project=%q limit=%d", projectKey, limit)
				}
				return nil, fmt.Errorf("injected %s verification failure: %w", test.name, test.err)
			}
			factoryCalls := 0
			claimCalls := 0
			reserveCalls := 0
			dispatchCalls := 0
			c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
				factoryCalls++
				claim := assignmentstore.ClaimFunc(func(_ context.Context, beadID, gotActor string) (assignmentstore.ClaimReceipt, error) {
					claimCalls++
					return assignmentstore.ClaimReceipt{BeadID: beadID, Actor: gotActor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
				})
				reserve := assignmentstore.ReservationFunc(func(_ context.Context, req assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
					reserveCalls++
					return assignmentstore.LeaseReceipt{AgentName: req.AgentName, Target: req.Target}, nil
				})
				dispatch := assignmentstore.DispatchFunc(func(context.Context, assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
					dispatchCalls++
					return assignmentstore.DispatchReceipt{DeliveryID: "must-not-dispatch"}, nil
				})
				preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
					return assignmentstore.PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: req.Prompt}, nil
				})
				return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
			}

			results, err := c.AssignWork(t.Context())
			if !errors.Is(err, test.err) || results != nil {
				t.Fatalf("AssignWork results=%+v error=%v, want typed fail-closed error", results, err)
			}
			if verificationCalls != 1 || factoryCalls != 0 || claimCalls != 0 || reserveCalls != 0 || dispatchCalls != 0 {
				t.Fatalf("calls verification=%d factory=%d claim=%d reserve=%d dispatch=%d", verificationCalls, factoryCalls, claimCalls, reserveCalls, dispatchCalls)
			}
			reloaded, err := assignmentstore.LoadStoreStrict(session)
			if err != nil {
				t.Fatalf("reload pending assignment: %v", err)
			}
			pending := reloaded.Get(request.BeadID)
			if pending == nil || pending.DispatchState != assignmentstore.DispatchPending || pending.DispatchAttempts != 0 {
				t.Fatalf("pending assignment changed across verification failure: %+v", pending)
			}
		})
	}
}

func TestAssignWorkUsesPlanOnlyVerifiedRecommendationWithoutReintroducingStaleTriageRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		planOnlyID    = "ntm-plan-only"
		staleTriageID = "ntm-stale-triage-only"
	)
	c := New("coordinator-verified-membership", t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	addEligibleCoordinatorAgent(c, "%83", "BlueLake")
	verificationCalls := 0
	c.actionableRecommendationsFn = func(_ context.Context, projectKey string, limit int) ([]bv.TriageRecommendation, error) {
		verificationCalls++
		if projectKey != c.projectKey || limit != 0 {
			t.Fatalf("actionable verification project=%q limit=%d", projectKey, limit)
		}
		return []bv.TriageRecommendation{{
			ID: planOnlyID, Title: "Plan-only actionable work", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	var detailLookups []string
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		detailLookups = append(detailLookups, beadID)
		if beadID == staleTriageID {
			t.Fatal("stale triage-only recommendation was reintroduced")
		}
		return &bv.BeadAssignmentDetails{ID: planOnlyID, Title: "Plan-only actionable work", IssueType: "task", Status: "open"}, nil
	}
	c.atomicCoordinatorFactory = successfulCoordinatorAtomicFactory("mail-plan-only")

	results, err := c.AssignWork(t.Context())
	if err != nil || len(results) != 1 || !results[0].Success || results[0].Assignment == nil || results[0].Assignment.BeadID != planOnlyID {
		t.Fatalf("AssignWork results=%+v error=%v", results, err)
	}
	if verificationCalls != 1 || !reflect.DeepEqual(detailLookups, []string{planOnlyID}) {
		t.Fatalf("verification calls=%d detail lookups=%v", verificationCalls, detailLookups)
	}
}

func TestAssignWorkAppliesLiveConfiguredOperatorGateBeforeClaimAndDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	projectDir := t.TempDir()
	if err := bv.ConfigureProjectOperatorGatedLabels(projectDir, []string{"coordinator-approval-required"}); err != nil {
		t.Fatalf("configure project policy: %v", err)
	}

	c := New("coordinator-live-operator-gate", projectDir, &agentmail.Client{}, "CoordinatorAgent")
	addEligibleCoordinatorAgent(c, "%84", "BlueLake")
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{{
			ID: "ntm-live-operator-gate", Title: "Stale ungated work", Type: "task", Status: "open", Priority: 1, Score: 1,
		}}, nil
	}
	detailCalls := 0
	c.workItemDetailsFn = func(context.Context, string) (*bv.BeadAssignmentDetails, error) {
		detailCalls++
		return &bv.BeadAssignmentDetails{
			ID: "ntm-live-operator-gate", Title: "Live gated work", IssueType: "task", Status: "open", Labels: []string{" Coordinator-Approval-Required "},
		}, nil
	}
	factoryCalls := 0
	c.atomicCoordinatorFactory = func(*assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		factoryCalls++
		return nil
	}

	results, err := c.AssignWork(t.Context())
	if err != nil || len(results) != 0 {
		t.Fatalf("AssignWork results=%+v error=%v, want live operator gate to skip assignment", results, err)
	}
	if detailCalls != 1 || factoryCalls != 0 {
		t.Fatalf("live detail calls=%d atomic factory calls=%d", detailCalls, factoryCalls)
	}
}

func TestAssignWorkRecoversKnownUnsentIntentWithOriginalIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-recover-pending"
	request := assignmentstore.AtomicRequest{
		BeadID: "ntm-recover", BeadTitle: "Recover internal/coordinator/assign.go",
		Target: "%91", OccupancyKey: "%91", Pane: 1, AgentType: "cod", AgentName: "BlueLake",
		Actor: "BlueLake", Prompt: "durable coordinator recovery prompt", IdempotencyKey: "coordinator-recovery-key",
		RequireReservation: true, RequestedPaths: []string{"internal/coordinator/assign.go"}, ReservationTTL: time.Hour,
	}
	store := assignmentstore.NewStore(session)
	actor := assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, assignmentstore.ClaimReceipt{
		BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicReservation(request.BeadID, request.IdempotencyKey, assignmentstore.ReservationFailed,
		assignmentstore.LeaseReceipt{AgentName: request.AgentName, Target: request.Target, Requested: request.RequestedPaths},
		assignmentstore.GuaranteeNoReservation(errors.New("first reservation attempt was not granted"))); err != nil {
		t.Fatalf("RecordAtomicReservation: %v", err)
	}

	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	addEligibleCoordinatorAgent(c, request.Target, request.AgentName)
	claimCalls := 0
	reserveCalls := 0
	dispatchCalls := 0
	c.atomicCoordinatorFactory = func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(context.Context, string, string) (assignmentstore.ClaimReceipt, error) {
			claimCalls++
			return assignmentstore.ClaimReceipt{}, errors.New("claim must not repeat for a claimed row")
		})
		reserve := assignmentstore.ReservationFunc(func(_ context.Context, req assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
			reserveCalls++
			expiresAt := time.Now().UTC().Add(time.Hour)
			return assignmentstore.LeaseReceipt{
				AgentName: req.AgentName, Target: req.Target, Requested: append([]string(nil), req.RequestedPaths...),
				Granted: append([]string(nil), req.RequestedPaths...), ReservationIDs: []int{911}, ExpiresAt: &expiresAt,
			}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			dispatchCalls++
			if req.IdempotencyKey != request.IdempotencyKey || req.Target != request.Target || req.AgentName != request.AgentName || req.Prompt != request.Prompt {
				t.Fatalf("recovery dispatch request = %+v", req)
			}
			return assignmentstore.DispatchReceipt{DeliveryID: "mail-recovered"}, nil
		})
		preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, req assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
			return assignmentstore.PromptPreflightResult{DispatchPrompt: req.Prompt, DurablePrompt: req.Prompt}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
	}

	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork error = %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].IdempotencyKey != request.IdempotencyKey || results[0].ClaimActor != actor {
		t.Fatalf("recovery results = %+v", results)
	}
	if claimCalls != 0 || reserveCalls != 1 || dispatchCalls != 1 {
		t.Fatalf("recovery side effects claim=%d reserve=%d dispatch=%d", claimCalls, reserveCalls, dispatchCalls)
	}
	recovered, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload recovery ledger: %v", err)
	}
	stored := recovered.Get(request.BeadID)
	if stored == nil || stored.DispatchState != assignmentstore.DispatchSent || stored.DispatchReceiptID != "mail-recovered" || stored.IdempotencyKey != request.IdempotencyKey {
		t.Fatalf("recovered ledger = %+v", stored)
	}
}

func TestPendingRecoveryIdentityErrorRejectsReusedPaneIdentity(t *testing.T) {
	t.Parallel()
	recorded := &assignmentstore.Assignment{OccupancyKey: "%91", AgentType: "cod", AgentName: "BlueLake"}
	candidate := &AgentState{PaneID: "%91", AgentType: "codex", AgentMailName: "BlueLake"}
	if err := pendingRecoveryIdentityError(recorded, candidate); err != nil {
		t.Fatalf("matching identity: %v", err)
	}

	typeChanged := *candidate
	typeChanged.AgentType = "claude"
	if err := pendingRecoveryIdentityError(recorded, &typeChanged); err == nil || !strings.Contains(err.Error(), "changed agent type") {
		t.Fatalf("type drift error=%v", err)
	}

	nameChanged := *candidate
	nameChanged.AgentMailName = "NewAgent"
	if err := pendingRecoveryIdentityError(recorded, &nameChanged); err == nil || !strings.Contains(err.Error(), "changed Agent Mail identity") {
		t.Fatalf("name drift error=%v", err)
	}
}

func TestAssignWorkKeepsUnknownDispatchFailClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-dispatch-unknown"
	request := assignmentstore.AtomicRequest{
		BeadID: "ntm-sending", BeadTitle: "Sending", Target: "%92", OccupancyKey: "%92", Pane: 1,
		AgentType: "cod", AgentName: "BlueLake", Actor: "BlueLake", Prompt: "ambiguous delivery",
		IdempotencyKey: "coordinator-sending-key",
	}
	store := assignmentstore.NewStore(session)
	actor := assignmentstore.StableClaimActor(request.Actor, request.IdempotencyKey)
	if _, err := store.RecordAtomicIntent(request, actor, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicIntent: %v", err)
	}
	if _, err := store.RecordAtomicClaim(request, assignmentstore.ClaimReceipt{BeadID: request.BeadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordAtomicClaim: %v", err)
	}
	if err := store.RecordAtomicDispatchStarted(request.BeadID, request.IdempotencyKey, time.Now().UTC()); err != nil {
		t.Fatalf("RecordAtomicDispatchStarted: %v", err)
	}

	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	addEligibleCoordinatorAgent(c, request.Target, request.AgentName)
	c.atomicCoordinatorFactory = func(*assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		t.Fatal("unknown dispatch must not reach an atomic retry")
		return nil
	}
	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork: %v", err)
	}
	if len(results) != 1 || results[0].Success || !strings.Contains(results[0].Error, assignmentstore.ErrDispatchOutcomeUnknown.Error()) {
		t.Fatalf("unknown dispatch results = %+v", results)
	}
	stored := store.Get(request.BeadID)
	if stored == nil || stored.DispatchState != assignmentstore.DispatchSending || stored.DispatchAttempts != 1 {
		t.Fatalf("unknown dispatch ledger changed = %+v", stored)
	}
}

func TestAssignWorkReleasesTerminalLeaseBeforePaneReuse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-terminal-lease-release"
	const beadID = "ntm-terminal-lease"
	store := assignmentstore.NewStore(session)
	store.Assignments[beadID] = &assignmentstore.Assignment{
		BeadID: beadID, BeadTitle: "Terminal lease", Pane: 1, AgentType: "cod", AgentName: "BlueLake",
		Status: assignmentstore.StatusAssigned, AssignedAt: time.Now().UTC(), IdempotencyKey: "terminal-lease-key",
		ClaimActor:     "BlueLake/terminal-lease-key",
		DispatchTarget: "%93", OccupancyKey: "%93", DispatchState: assignmentstore.DispatchSent,
		DispatchReceiptID: "mail-93", ReservationRequired: true, ReservationState: assignmentstore.ReservationReserved,
		ReservationCompleted: true, ReservationAgent: "BlueLake", ReservationTarget: "%93",
		ReservationRequested: []string{"internal/coordinator/**"}, ReservedPaths: []string{"internal/coordinator/**"}, ReservationIDs: []int{931},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal lease: %v", err)
	}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 931, AgentName: "BlueLake", PathPattern: "internal/coordinator/**", ProjectID: 9, Exclusive: true,
		Reason: "bead assignment: " + beadID, ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.reservationClient = client
	c.config.AutoAssign = true
	c.workItemStatusFn = func(context.Context, string) (string, error) { return "closed", nil }
	claimReleaseCalls := 0
	c.releaseWorkItemClaimFn = func(_ context.Context, projectKey, gotBeadID, actor string) (bool, error) {
		claimReleaseCalls++
		if projectKey != c.projectKey || gotBeadID != beadID || actor != "BlueLake/terminal-lease-key" {
			t.Fatalf("claim release project=%q bead=%q actor=%q", projectKey, gotBeadID, actor)
		}
		return true, nil
	}

	results, err := c.AssignWork(t.Context())
	if err != nil || len(results) != 0 {
		t.Fatalf("AssignWork results=%+v error=%v", results, err)
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{931}) {
		t.Fatalf("release IDs = %v", client.releaseIDs)
	}
	if claimReleaseCalls != 1 {
		t.Fatalf("claim release calls=%d, want 1", claimReleaseCalls)
	}
	reloaded, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload terminal ledger: %v", err)
	}
	terminal := reloaded.Get(beadID)
	if terminal == nil || terminal.Status != assignmentstore.StatusCompleted || terminal.ReservationState != assignmentstore.ReservationReleased ||
		len(terminal.ReservationIDs) != 0 || terminal.ClearState != assignmentstore.ClearStateNone || terminal.DispatchReceiptID != "mail-93" ||
		terminal.PendingCompletionEventID != "" || terminal.CompletionDetectedAt != nil || len(reloaded.ListPendingCompletionEvents()) != 0 {
		t.Fatalf("terminal lease ledger = %+v", terminal)
	}
}

func TestTerminalReconciliationSerializesExternalCleanupAcrossCoordinators(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		session = "coordinator-terminal-cleanup-lock"
		beadID  = "ntm-coordinator-terminal-cleanup-lock"
	)
	seed := assignmentstore.NewStore(session)
	seed.Assignments[beadID] = &assignmentstore.Assignment{
		BeadID: beadID, BeadTitle: "Concurrent coordinator cleanup", Pane: 1,
		AgentType: "cod", AgentName: "BlueLake", Status: assignmentstore.StatusAssigned,
		AssignedAt: time.Now().UTC(), IdempotencyKey: "coordinator-cleanup-key",
		ClaimActor: "BlueLake/coordinator-cleanup-key", DispatchTarget: "%97", OccupancyKey: "%97",
		DispatchState: assignmentstore.DispatchSent, DispatchReceiptID: "mail-97", ReservationRequired: true,
		ReservationState: assignmentstore.ReservationReserved, ReservationCompleted: true,
		ReservationAgent: "BlueLake", ReservationTarget: "%97",
		ReservationRequested: []string{"internal/coordinator/**"}, ReservedPaths: []string{"internal/coordinator/**"}, ReservationIDs: []int{971},
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed terminal cleanup assignment: %v", err)
	}
	stores := make([]*assignmentstore.AssignmentStore, 2)
	for index := range stores {
		var err error
		stores[index], err = assignmentstore.LoadStoreStrict(session)
		if err != nil {
			t.Fatalf("load store %d: %v", index, err)
		}
	}
	releaseStarted := make(chan struct{})
	allowRelease := make(chan struct{})
	client := &fakeCoordinatorReservationClient{
		releaseStarted: releaseStarted,
		allowRelease:   allowRelease,
		reservations: []agentmail.FileReservation{{
			ID: 971, AgentName: "BlueLake", PathPattern: "internal/coordinator/**", ProjectID: 9, Exclusive: true,
			Reason: "bead assignment: " + beadID, ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
		}},
	}
	var claimReleaseCalls atomic.Int32
	coordinators := make([]*SessionCoordinator, len(stores))
	for index := range coordinators {
		coordinator := New(session, t.TempDir(), &agentmail.Client{}, fmt.Sprintf("Coordinator%d", index))
		coordinator.reservationClient = client
		coordinator.workItemStatusFn = func(context.Context, string) (string, error) { return "closed", nil }
		coordinator.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) {
			claimReleaseCalls.Add(1)
			return true, nil
		}
		coordinators[index] = coordinator
	}

	start := make(chan struct{})
	results := make(chan error, len(stores))
	for index := range stores {
		go func(index int) {
			<-start
			results <- coordinators[index].reconcileTerminalAssignments(t.Context(), stores[index])
		}(index)
	}
	close(start)
	select {
	case <-releaseStarted:
	case <-time.After(5 * time.Second):
		close(allowRelease)
		t.Fatal("coordinator cleanup did not reach external release")
	}
	close(allowRelease)
	for range stores {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent terminal reconciliation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent terminal reconciliation remained blocked")
		}
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{971}) {
		t.Fatalf("reservation releases=%v, want one exact release", client.releaseIDs)
	}
	if got := claimReleaseCalls.Load(); got != 1 {
		t.Fatalf("claim release calls=%d, want 1", got)
	}
	terminal, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload terminal result: %v", err)
	}
	current := terminal.Get(beadID)
	if current == nil || current.Status != assignmentstore.StatusCompleted || current.ClearState != assignmentstore.ClearStateNone {
		t.Fatalf("terminal cleanup result=%+v", current)
	}
}

func TestAssignWorkRetainsTerminalBarrierUntilLeaseReleaseSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-terminal-lease-retry"
	const beadID = "ntm-terminal-lease-retry"
	store := assignmentstore.NewStore(session)
	store.Assignments[beadID] = &assignmentstore.Assignment{
		BeadID: beadID, BeadTitle: "Terminal lease retry", Pane: 1, AgentType: "cod", AgentName: "BlueLake",
		Status: assignmentstore.StatusAssigned, AssignedAt: time.Now().UTC(), IdempotencyKey: "terminal-retry-key",
		DispatchTarget: "%94", OccupancyKey: "%94", DispatchState: assignmentstore.DispatchSent,
		ReservationRequired: true, ReservationState: assignmentstore.ReservationReserved, ReservationCompleted: true,
		ReservationAgent: "BlueLake", ReservationTarget: "%94", ReservationRequested: []string{"internal/coordinator/**"},
		ReservedPaths: []string{"internal/coordinator/**"}, ReservationIDs: []int{941},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed terminal lease retry: %v", err)
	}
	client := &fakeCoordinatorReservationClient{
		reservations: []agentmail.FileReservation{{ID: 941, AgentName: "BlueLake", PathPattern: "internal/coordinator/**", ProjectID: 9, Exclusive: true, Reason: "bead assignment: " + beadID, ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)}}},
		releaseErr:   errors.New("Agent Mail release unavailable"),
	}
	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.reservationClient = client
	c.config.AutoAssign = true
	c.workItemStatusFn = func(context.Context, string) (string, error) { return "closed", nil }

	if _, err := c.AssignWork(t.Context()); err == nil || !strings.Contains(err.Error(), "remain active after 3 release attempts") {
		t.Fatalf("first terminal reconciliation error = %v", err)
	}
	failed, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload failed release ledger: %v", err)
	}
	barrier := failed.Get(beadID)
	if barrier == nil || barrier.Status != assignmentstore.StatusAssigned || barrier.ClearState != assignmentstore.ClearStateReservationReleasing ||
		barrier.ClearError == "" || !reflect.DeepEqual(barrier.ReservationIDs, []int{941}) {
		t.Fatalf("failed release barrier = %+v", barrier)
	}

	client.releaseErr = nil
	if results, err := c.AssignWork(t.Context()); err != nil || len(results) != 0 {
		t.Fatalf("terminal reconciliation retry results=%+v error=%v", results, err)
	}
	completed, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload completed release ledger: %v", err)
	}
	terminal := completed.Get(beadID)
	if terminal == nil || terminal.Status != assignmentstore.StatusCompleted || terminal.ClearState != assignmentstore.ClearStateNone || len(terminal.ReservationIDs) != 0 {
		t.Fatalf("terminal reconciliation retry ledger = %+v", terminal)
	}
}

func TestAssignWorkResumesPendingTerminalBarrierWithoutTrackerAndReleasesLeaseOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-pending-terminal-resume"
	const beadID = "ntm-pending-terminal-resume"
	store := assignmentstore.NewStore(session)
	store.Assignments[beadID] = &assignmentstore.Assignment{
		BeadID: beadID, BeadTitle: "Pending terminal", Pane: 1, AgentType: "cod", AgentName: "BlueLake",
		Status: assignmentstore.StatusAssigned, AssignedAt: time.Now().UTC(), IdempotencyKey: "pending-terminal-key",
		ClaimActor: "BlueLake/pending-terminal-key", DispatchTarget: "%96", OccupancyKey: "%96",
		DispatchState: assignmentstore.DispatchSent, DispatchReceiptID: "mail-96", ReservationRequired: true,
		ReservationState: assignmentstore.ReservationReserved, ReservationCompleted: true,
		ReservationRequested: []string{"internal/coordinator/pending.go"}, ReservedPaths: []string{"internal/coordinator/pending.go"}, ReservationIDs: []int{961},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed pending terminal assignment: %v", err)
	}
	observed := store.Get(beadID)
	if _, applied, err := store.BeginTerminalReconciliationIfCurrent(t.Context(), observed, assignmentstore.StatusCompleted, ""); err != nil || !applied {
		t.Fatalf("begin pending terminal barrier applied=%v error=%v", applied, err)
	}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 961, AgentName: "BlueLake", PathPattern: "internal/coordinator/pending.go", ProjectID: 9, Exclusive: true,
		Reason: "bead assignment: " + beadID, ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.config.AutoAssign = true
	c.reservationClient = client
	statusLookups := 0
	c.workItemStatusFn = func(context.Context, string) (string, error) {
		statusLookups++
		return "", errors.New("pending terminal barrier must not re-read tracker status")
	}
	claimCalls := 0
	c.releaseWorkItemClaimFn = func(context.Context, string, string, string) (bool, error) {
		claimCalls++
		if claimCalls == 1 {
			return false, errors.New("injected claim release interruption")
		}
		return true, nil
	}

	if _, err := c.AssignWork(t.Context()); err == nil || !strings.Contains(err.Error(), "injected claim release interruption") {
		t.Fatalf("first pending terminal reconciliation error=%v", err)
	}
	interrupted, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload interrupted terminal barrier: %v", err)
	}
	checkpoint := interrupted.Get(beadID)
	if checkpoint == nil || checkpoint.Status != assignmentstore.StatusAssigned || checkpoint.ClearState != assignmentstore.ClearStateLeasesReleased ||
		checkpoint.PendingTerminalStatus != assignmentstore.StatusCompleted || checkpoint.TerminalClaimReleased || len(checkpoint.ReservationIDs) != 0 {
		t.Fatalf("interrupted terminal checkpoint=%+v", checkpoint)
	}
	if len(client.releaseIDs) != 1 || statusLookups != 0 {
		t.Fatalf("first resume release calls=%v status lookups=%d", client.releaseIDs, statusLookups)
	}

	if results, err := c.AssignWork(t.Context()); err != nil || len(results) != 0 {
		t.Fatalf("second pending terminal reconciliation results=%+v error=%v", results, err)
	}
	if len(client.releaseIDs) != 1 || claimCalls != 2 || statusLookups != 0 {
		t.Fatalf("resumed side effects releases=%v claim=%d status lookups=%d", client.releaseIDs, claimCalls, statusLookups)
	}
	terminal, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload terminal assignment: %v", err)
	}
	completed := terminal.Get(beadID)
	if completed == nil || completed.Status != assignmentstore.StatusCompleted || completed.CompletedAt == nil ||
		completed.ClearState != assignmentstore.ClearStateNone || completed.PendingTerminalStatus != "" || len(completed.ReservationIDs) != 0 {
		t.Fatalf("resumed terminal assignment=%+v", completed)
	}
}

func TestAssignWorkReconcilesAndReleasesUnknownTerminalLease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const session = "coordinator-terminal-unknown-lease"
	const beadID = "ntm-terminal-unknown-lease"
	store := assignmentstore.NewStore(session)
	store.Assignments[beadID] = &assignmentstore.Assignment{
		BeadID: beadID, BeadTitle: "Unknown lease", Pane: 1, AgentType: "cod", AgentName: "BlueLake",
		Status: assignmentstore.StatusAssigned, AssignedAt: time.Now().UTC(), IdempotencyKey: "unknown-lease-key",
		DispatchTarget: "%95", OccupancyKey: "%95", DispatchState: assignmentstore.DispatchSent,
		ReservationRequired: true, ReservationState: assignmentstore.ReservationUnknown, ReservationAttempts: 1,
		ReservationAgent: "BlueLake", ReservationTarget: "%95",
		ReservationInputPaths: []string{"internal/coordinator/a.go", "internal/coordinator/b.go"},
	}
	if err := store.Save(); err != nil {
		t.Fatalf("seed unknown terminal lease: %v", err)
	}
	client := &fakeCoordinatorReservationClient{reservations: []agentmail.FileReservation{{
		ID: 951, AgentName: "BlueLake", PathPattern: "internal/coordinator/a.go", ProjectID: 9, Exclusive: true,
		Reason: "bead assignment: " + beadID, ExpiresTS: agentmail.FlexTime{Time: time.Now().UTC().Add(time.Hour)},
	}}}
	c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	c.reservationClient = client
	c.config.AutoAssign = true
	c.workItemStatusFn = func(context.Context, string) (string, error) { return "closed", nil }

	if results, err := c.AssignWork(t.Context()); err != nil || len(results) != 0 {
		t.Fatalf("unknown terminal reconciliation results=%+v error=%v", results, err)
	}
	if len(client.releaseIDs) != 1 || !reflect.DeepEqual(client.releaseIDs[0], []int{951}) {
		t.Fatalf("unknown lease release IDs = %v", client.releaseIDs)
	}
	terminal, err := assignmentstore.LoadStoreStrict(session)
	if err != nil {
		t.Fatalf("reload unknown terminal lease: %v", err)
	}
	if got := terminal.Get(beadID); got == nil || got.Status != assignmentstore.StatusCompleted || got.ReservationState != assignmentstore.ReservationReleased {
		t.Fatalf("unknown terminal reconciliation ledger = %+v", got)
	}
}

func TestFilterOccupiedAgentsUsesCanonicalPaneIDsAndRejectsLegacyRows(t *testing.T) {
	agents := []*AgentState{
		{PaneID: "%1", PaneIndex: 1},
		{PaneID: "%2", PaneIndex: 1},
		{PaneID: "%3", PaneIndex: 3},
	}
	got, err := filterOccupiedAgents(agents, []*assignmentstore.Assignment{{BeadID: "canonical", Pane: 1, OccupancyKey: "%1"}})
	if err != nil {
		t.Fatalf("canonical occupancy: %v", err)
	}
	if len(got) != 2 || got[0].PaneID != "%2" || got[1].PaneID != "%3" {
		t.Fatalf("eligible agents = %+v", got)
	}

	for name, legacy := range map[string]*assignmentstore.Assignment{
		"bare local index":    {BeadID: "legacy-index", Pane: 1},
		"window pane address": {BeadID: "legacy-window", Pane: 1, DispatchTarget: "1.1"},
		"invalid occupancy":   {BeadID: "legacy-occupancy", Pane: 1, OccupancyKey: "1.1"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := filterOccupiedAgents(agents, []*assignmentstore.Assignment{legacy})
			if !errors.Is(err, assignmentstore.ErrPaneIdentityMigrationRequired) || got != nil {
				t.Fatalf("noncanonical active row result=%+v error=%v, want typed migration error", got, err)
			}
		})
	}
}

func TestFilterActionableRecommendationsUsesAuthoritativeStatusWithoutMutatingCache(t *testing.T) {
	c := New("recommendation-filter", t.TempDir(), nil, "CoordinatorAgent")
	statuses := map[string]string{
		"ntm-open":        "open",
		"ntm-closed":      "closed",
		"ntm-tombstone":   "tombstone",
		"ntm-in-progress": "in_progress",
		"ntm-blocked":     "blocked",
	}
	lookups := make(map[string]int)
	c.workItemStatusFn = func(_ context.Context, beadID string) (string, error) {
		lookups[beadID]++
		return statuses[beadID], nil
	}
	source := []bv.TriageRecommendation{
		{ID: "ntm-open", Title: "Open", Type: "task"},
		{ID: "ntm-active", Title: "Already active", Type: "task"},
		{ID: "ntm-closed", Title: "Closed", Type: "task"},
		{ID: "ntm-tombstone", Title: "Tombstoned", Type: "task"},
		{ID: "ntm-in-progress", Title: "Externally claimed", Type: "task"},
		{ID: "ntm-blocked", Title: "Blocked", Type: "task"},
	}
	original := append([]bv.TriageRecommendation(nil), source...)
	filtered, terminal, err := c.filterActionableRecommendations(t.Context(), source, map[string]struct{}{"ntm-active": {}})
	if err != nil {
		t.Fatalf("filterActionableRecommendations() error = %v", err)
	}
	if !terminal || len(filtered) != 1 || filtered[0].ID != "ntm-open" {
		t.Fatalf("filtered=%+v terminal=%v", filtered, terminal)
	}
	if lookups["ntm-active"] != 0 {
		t.Fatalf("active recommendation status was redundantly queried %d times", lookups["ntm-active"])
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatalf("cached recommendation source mutated: got=%+v want=%+v", source, original)
	}
	filtered[0].Title = "mutated result"
	if source[0].Title != "Open" {
		t.Fatalf("filtered result aliases cached source: %+v", source[0])
	}
}

func TestFilterActionableRecommendationsFailsClosedOnStatusError(t *testing.T) {
	c := New("recommendation-filter-error", t.TempDir(), nil, "CoordinatorAgent")
	c.workItemStatusFn = func(context.Context, string) (string, error) {
		return "", errors.New("tracker unavailable")
	}
	filtered, terminal, err := c.filterActionableRecommendations(t.Context(), []bv.TriageRecommendation{{ID: "ntm-unknown"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "read recommendation ntm-unknown status") || filtered != nil || terminal {
		t.Fatalf("filtered=%+v terminal=%v error=%v", filtered, terminal, err)
	}
}

func TestFilterActionableRecommendationsSkipsDependencyAndOperatorGates(t *testing.T) {
	t.Parallel()
	c := New("recommendation-semantic-gates", t.TempDir(), nil, "CoordinatorAgent")
	lookups := make(map[string]int)
	c.workItemStatusFn = func(_ context.Context, beadID string) (string, error) {
		lookups[beadID]++
		if beadID == "ntm-blocked" {
			return "blocked", nil
		}
		return "open", nil
	}
	source := []bv.TriageRecommendation{
		{ID: "ntm-ready", Type: "task", Status: "open"},
		{ID: "ntm-dependency", Type: "task", Status: "open", BlockedBy: []string{"ntm-prerequisite"}},
		{ID: "ntm-operator", Type: "task", Status: "open", Labels: []string{"Backend", "Operator-Gated"}},
		{ID: "ntm-blocked", Type: "task", Status: "blocked"},
	}
	filtered, terminal, err := c.filterActionableRecommendations(t.Context(), source, nil)
	if err != nil || terminal || len(filtered) != 1 || filtered[0].ID != "ntm-ready" {
		t.Fatalf("filtered=%+v terminal=%v error=%v", filtered, terminal, err)
	}
	if lookups["ntm-ready"] != 1 || lookups["ntm-dependency"] != 1 || lookups["ntm-operator"] != 1 || lookups["ntm-blocked"] != 1 {
		t.Fatalf("status lookups=%v, want one authoritative lookup per recommendation", lookups)
	}
}

func TestRecommendationPassesSemanticGatesUsesCanonicalOperatorLabels(t *testing.T) {
	t.Parallel()

	for _, label := range bv.OperatorGatedLabels() {
		label := label
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			if recommendationPassesSemanticGates(bv.TriageRecommendation{
				ID: "ntm-operator", Status: "open", Labels: []string{"  " + strings.ToUpper(label) + "  "},
			}) {
				t.Fatalf("operator label %q passed coordinator semantic gates", label)
			}
		})
	}
}

// Containers (epics) are grouping nodes, never implementation work. The CLI
// classifier has always excluded them; the coordinator's gate did not, so
// `ntm coordinator run` claimed, reserved, and mailed open epics into worker
// panes that `ntm assign` refuses to dispatch.
func TestRecommendationPassesSemanticGatesExcludesContainerTypes(t *testing.T) {
	t.Parallel()

	for _, issueType := range []string{"epic", "EPIC", "  Epic  "} {
		issueType := issueType
		t.Run(issueType, func(t *testing.T) {
			t.Parallel()
			if recommendationPassesSemanticGates(bv.TriageRecommendation{
				ID: "ntm-container", Type: issueType, Status: "open",
			}) {
				t.Fatalf("container issue type %q passed coordinator semantic gates", issueType)
			}
		})
	}

	if !recommendationPassesSemanticGates(bv.TriageRecommendation{
		ID: "ntm-work", Type: "task", Status: "open",
	}) {
		t.Fatal("ordinary task was rejected by coordinator semantic gates")
	}
}

// Type evidence is part of the authorization boundary: without it the
// container gate cannot prove an item is implementation work, so the
// authoritative filter must stop rather than dispatch it.
func TestFilterActionableRecommendationsRefusesUnverifiedIssueType(t *testing.T) {
	t.Parallel()
	c := New("recommendation-unverified-type", t.TempDir(), nil, "CoordinatorAgent")
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		return &bv.BeadAssignmentDetails{ID: beadID, Title: "No type anywhere", Status: "open"}, nil
	}

	filtered, terminal, err := c.filterActionableRecommendations(t.Context(),
		[]bv.TriageRecommendation{{ID: "ntm-untyped", Status: "open"}}, nil)
	if err == nil {
		t.Fatalf("filtered=%+v terminal=%v, want an error for an unverifiable issue type", filtered, terminal)
	}
	if !strings.Contains(err.Error(), "no verified issue type") {
		t.Fatalf("error = %v, want it to name the missing type evidence", err)
	}
}

// A live read that omits the type must not erase evidence the plan proved.
func TestFilterActionableRecommendationsKeepsPlannedTypeWhenLiveReadOmitsIt(t *testing.T) {
	t.Parallel()
	c := New("recommendation-planned-type", t.TempDir(), nil, "CoordinatorAgent")
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		return &bv.BeadAssignmentDetails{ID: beadID, Title: "Live row without a type", Status: "open"}, nil
	}

	filtered, _, err := c.filterActionableRecommendations(t.Context(),
		[]bv.TriageRecommendation{{ID: "ntm-planned", Type: "task", Status: "open"}}, nil)
	if err != nil || len(filtered) != 1 || filtered[0].Type != "task" {
		t.Fatalf("filtered=%+v error=%v, want the planned type preserved", filtered, err)
	}

	// The same path must still reject a planned container.
	filtered, _, err = c.filterActionableRecommendations(t.Context(),
		[]bv.TriageRecommendation{{ID: "ntm-planned-epic", Type: "epic", Status: "open"}}, nil)
	if err != nil || len(filtered) != 0 {
		t.Fatalf("filtered=%+v error=%v, want the planned epic excluded", filtered, err)
	}
}

func TestFilterActionableRecommendationsUsesLiveDependencyAndLabelTruth(t *testing.T) {
	t.Parallel()
	c := New("recommendation-live-gates", t.TempDir(), nil, "CoordinatorAgent")
	details := map[string]*bv.BeadAssignmentDetails{
		"ntm-ready":      {ID: "ntm-ready", Title: "Current ready", IssueType: "task", Status: "open"},
		"ntm-dependency": {ID: "ntm-dependency", Title: "Now blocked", IssueType: "task", Status: "open", BlockedBy: []string{"ntm-prerequisite"}},
		"ntm-operator":   {ID: "ntm-operator", Title: "Needs human", IssueType: "task", Status: "open", Labels: []string{"operator-gated"}},
	}
	c.workItemDetailsFn = func(_ context.Context, beadID string) (*bv.BeadAssignmentDetails, error) {
		copy := *details[beadID]
		copy.Labels = append([]string(nil), copy.Labels...)
		copy.BlockedBy = append([]string(nil), copy.BlockedBy...)
		return &copy, nil
	}
	staleReady := []bv.TriageRecommendation{
		{ID: "ntm-ready", Title: "Stale title", Type: "task", Status: "open"},
		{ID: "ntm-dependency", Title: "Stale ready", Type: "task", Status: "open"},
		{ID: "ntm-operator", Title: "Stale ready", Type: "task", Status: "open"},
	}
	filtered, terminal, err := c.filterActionableRecommendations(t.Context(), staleReady, nil)
	if err != nil || terminal || len(filtered) != 1 || filtered[0].ID != "ntm-ready" || filtered[0].Title != "Current ready" {
		t.Fatalf("filtered=%+v terminal=%v error=%v", filtered, terminal, err)
	}
}

func TestAssignWorkDoesNotMutateSharedActionableRecommendations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	shared := []bv.TriageRecommendation{{
		ID: "ntm-cached", Title: "Cached recommendation", Type: "task", Status: "open", Priority: 1, Score: 1,
	}}
	original := append([]bv.TriageRecommendation(nil), shared...)

	for i, session := range []string{"coordinator-cache-first", "coordinator-cache-second"} {
		c := New(session, t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
		addEligibleCoordinatorAgent(c, fmt.Sprintf("%%%d", i+1), fmt.Sprintf("BlueLake%d", i+1))
		c.workItemStatusFn = func(context.Context, string) (string, error) { return "open", nil }
		c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return shared, nil }
		c.atomicCoordinatorFactory = successfulCoordinatorAtomicFactory(fmt.Sprintf("mail-cache-%d", i+1))

		results, err := c.AssignWork(t.Context())
		if err != nil || len(results) != 1 || !results[0].Success {
			t.Fatalf("AssignWork cycle %d results=%+v error=%v", i+1, results, err)
		}
		if !reflect.DeepEqual(shared, original) {
			t.Fatalf("shared actionable recommendations mutated after cycle %d: got=%+v want=%+v", i+1, shared, original)
		}
	}
}

func TestAssignWorkConcurrentCyclesDoNotRaceOnSharedActionableRecommendations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	shared := []bv.TriageRecommendation{{
		ID: "ntm-concurrent-cache", Title: "Shared concurrent recommendation", Type: "task", Status: "open", Priority: 1, Score: 1,
	}}
	original := append([]bv.TriageRecommendation(nil), shared...)

	const workers = 8
	type outcome struct {
		results []AssignmentResult
		err     error
	}
	outcomes := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		c := New(fmt.Sprintf("coordinator-cache-concurrent-%d", i), t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
		addEligibleCoordinatorAgent(c, fmt.Sprintf("%%%d", i+20), fmt.Sprintf("BlueLake%d", i+20))
		c.workItemStatusFn = func(context.Context, string) (string, error) { return "open", nil }
		c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) { return shared, nil }
		c.atomicCoordinatorFactory = successfulCoordinatorAtomicFactory(fmt.Sprintf("mail-concurrent-%d", i))

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results, err := c.AssignWork(t.Context())
			outcomes <- outcome{results: results, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	cycle := 0
	for got := range outcomes {
		cycle++
		if got.err != nil || len(got.results) != 1 || !got.results[0].Success {
			t.Fatalf("concurrent AssignWork cycle %d results=%+v error=%v", cycle, got.results, got.err)
		}
	}
	if !reflect.DeepEqual(shared, original) {
		t.Fatalf("shared actionable recommendations mutated by concurrent cycles: got=%+v want=%+v", shared, original)
	}
}

// TestAssignWorkSelectsByScoreNotFirstFit proves the bd-v394r wire: AssignWork
// plans through ScoreAndSelectAssignments, so agent/task affinity decides the
// pairing. The simple chore is listed FIRST — first-fit matching would hand it
// to whichever agent iterated first — yet the Claude agent must end up on the
// complex feature (+0.15 cc affinity) and the Codex agent on the chore (+0.15
// cod affinity), regardless of map iteration order.
func TestAssignWorkSelectsByScoreNotFirstFit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := New("coordinator-score-based", t.TempDir(), &agentmail.Client{}, "CoordinatorAgent")
	addEligibleCoordinatorAgent(c, "%1", "BlueLakeClaude")
	c.agents["%1"].AgentType = "cc"
	addEligibleCoordinatorAgent(c, "%2", "BlueLakeCodex")
	c.agents["%2"].PaneIndex = 2

	c.workItemStatusFn = func(context.Context, string) (string, error) { return "open", nil }
	c.actionableRecommendationsFn = func(context.Context, string, int) ([]bv.TriageRecommendation, error) {
		return []bv.TriageRecommendation{
			{ID: "ntm-chore", Title: "Tidy workspace", Type: "chore", Status: "open", Priority: 2, Score: 0.5},
			{ID: "ntm-feature", Title: "Ship new engine", Type: "feature", Status: "open", Priority: 2, Score: 0.5},
		}, nil
	}
	c.atomicCoordinatorFactory = successfulCoordinatorAtomicFactory("mail-score-based")

	results, err := c.AssignWork(t.Context())
	if err != nil {
		t.Fatalf("AssignWork error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 assignments, got %d: %+v", len(results), results)
	}
	beadByPane := make(map[string]string)
	for _, result := range results {
		if !result.Success || result.Assignment == nil {
			t.Fatalf("expected successful assignment, got %+v", result)
		}
		beadByPane[result.Assignment.AgentPaneID] = result.Assignment.BeadID
	}
	if beadByPane["%1"] != "ntm-feature" {
		t.Errorf("expected cc agent %%1 to receive ntm-feature (complex-task affinity), got %q", beadByPane["%1"])
	}
	if beadByPane["%2"] != "ntm-chore" {
		t.Errorf("expected cod agent %%2 to receive ntm-chore (simple-task affinity), got %q", beadByPane["%2"])
	}
}

func successfulCoordinatorAtomicFactory(deliveryID string) func(*assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
	return func(store *assignmentstore.AssignmentStore) *assignmentstore.AtomicCoordinator {
		claim := assignmentstore.ClaimFunc(func(_ context.Context, beadID, actor string) (assignmentstore.ClaimReceipt, error) {
			return assignmentstore.ClaimReceipt{BeadID: beadID, Actor: actor, Status: "in_progress", ClaimedAt: time.Now().UTC()}, nil
		})
		reserve := assignmentstore.ReservationFunc(func(_ context.Context, request assignmentstore.ReservationRequest) (assignmentstore.LeaseReceipt, error) {
			return assignmentstore.LeaseReceipt{AgentName: request.AgentName, Target: request.Target}, nil
		})
		dispatch := assignmentstore.DispatchFunc(func(context.Context, assignmentstore.DispatchRequest) (assignmentstore.DispatchReceipt, error) {
			return assignmentstore.DispatchReceipt{DeliveryID: deliveryID}, nil
		})
		preflight := assignmentstore.PromptPreflightFunc(func(_ context.Context, request assignmentstore.DispatchRequest) (assignmentstore.PromptPreflightResult, error) {
			return assignmentstore.PromptPreflightResult{DispatchPrompt: request.Prompt, DurablePrompt: request.Prompt}, nil
		})
		return assignmentstore.NewAtomicCoordinator(store, claim, reserve, dispatch, preflight)
	}
}

func TestValidatedAgentMailDeliveryIDRequiresOneConcreteReceipt(t *testing.T) {
	tests := []struct {
		name string
		sent *agentmail.SendResult
		want string
	}{
		{name: "nil result"},
		{name: "zero deliveries", sent: &agentmail.SendResult{}},
		{name: "multiple deliveries", sent: &agentmail.SendResult{Count: 2, Deliveries: []agentmail.MessageDelivery{{Payload: &agentmail.Message{ID: 1}}, {Payload: &agentmail.Message{ID: 2}}}}},
		{name: "nil payload", sent: &agentmail.SendResult{Count: 1, Deliveries: []agentmail.MessageDelivery{{}}}},
		{name: "zero message id", sent: &agentmail.SendResult{Count: 1, Deliveries: []agentmail.MessageDelivery{{Payload: &agentmail.Message{}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := validatedAgentMailDeliveryID(tt.sent, "/project", 9, "Coordinator", "BlueFox", "Work Assignment: ntm-1", "body"); err == nil || got != "" {
				t.Fatalf("validatedAgentMailDeliveryID() = %q, %v; want empty error result", got, err)
			}
		})
	}

	got, err := validatedAgentMailDeliveryID(validCoordinatorSendResult(), "/project", 9, "Coordinator", "BlueFox", "Work Assignment: ntm-1", "body")
	if err != nil || got != "73" {
		t.Fatalf("valid receipt = %q, %v; want 73", got, err)
	}
}

func TestValidatedAgentMailDeliveryIDRejectsProjectAndRecipientMismatch(t *testing.T) {
	base := validCoordinatorSendResult()
	if _, err := validatedAgentMailDeliveryID(base, "/other", 9, "Coordinator", "BlueFox", "Work Assignment: ntm-1", "body"); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("project mismatch error = %v", err)
	}
	if _, err := validatedAgentMailDeliveryID(base, "/project", 9, "Coordinator", "RedSeal", "Work Assignment: ntm-1", "body"); err == nil || !strings.Contains(err.Error(), "recipients") {
		t.Fatalf("recipient mismatch error = %v", err)
	}
}

func TestValidatedAgentMailDeliveryIDRejectsPayloadMismatch(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*agentmail.Message)
	}{
		{name: "project id", mutate: func(message *agentmail.Message) { message.ProjectID = 10 }},
		{name: "sender", mutate: func(message *agentmail.Message) { message.From = "OtherSender" }},
		{name: "subject", mutate: func(message *agentmail.Message) { message.Subject = "stale subject" }},
		{name: "body", mutate: func(message *agentmail.Message) { message.BodyMD = "stale body" }},
		{name: "importance", mutate: func(message *agentmail.Message) { message.Importance = "low" }},
		{name: "ack", mutate: func(message *agentmail.Message) { message.AckRequired = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := validCoordinatorSendResult()
			test.mutate(result.Deliveries[0].Payload)
			if got, err := validatedAgentMailDeliveryID(result, "/project", 9, "Coordinator", "BlueFox", "Work Assignment: ntm-1", "body"); err == nil || got != "" {
				t.Fatalf("mismatch result=%q error=%v", got, err)
			}
		})
	}
}

func validCoordinatorSendResult() *agentmail.SendResult {
	return &agentmail.SendResult{
		Count: 1,
		Deliveries: []agentmail.MessageDelivery{{
			Project: "/project",
			Payload: &agentmail.Message{
				ID: 73, ProjectID: 9, From: "Coordinator", To: []string{"BlueFox"},
				Subject: "Work Assignment: ntm-1", BodyMD: "body", Importance: "normal", AckRequired: true,
			},
		}},
	}
}

func TestScoreAndSelectAssignmentsPropagatesPaneAndReservationPaths(t *testing.T) {
	agent := &AgentState{PaneID: "%9", PaneIndex: 4, AgentType: "cod", AgentMailName: "BlueFox"}
	recommendations := []bv.TriageRecommendation{{
		ID: "ntm-files", Title: "Update internal/coordinator/assign.go", Status: "open", Score: 0.8,
		Reasons: []string{"also cover internal/coordinator/assign_test.go"},
	}}
	selected := ScoreAndSelectAssignments([]*AgentState{agent}, recommendations, DefaultScoreConfig(), nil)
	if len(selected) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(selected))
	}
	assignment := selected[0].Assignment
	if assignment == nil || assignment.AgentPaneID != "%9" || assignment.AgentPaneIndex != 4 {
		t.Fatalf("assignment pane identity = %+v", assignment)
	}
	want := map[string]bool{"internal/coordinator/assign.go": true, "internal/coordinator/assign_test.go": true}
	for _, path := range assignment.FilesToReserve {
		delete(want, path)
	}
	if len(want) != 0 {
		t.Fatalf("reservation paths = %v, missing %v", assignment.FilesToReserve, want)
	}
}

func TestFormatAssignmentMessage(t *testing.T) {
	c := New("test-session", "/tmp/test", nil, "TestAgent")

	assignment := &WorkAssignment{
		BeadID:    "ntm-1234",
		BeadTitle: "Implement feature X",
		Priority:  1,
		Score:     0.85,
	}

	rec := &bv.TriageRecommendation{
		ID:          "ntm-1234",
		Title:       "Implement feature X",
		Reasons:     []string{"High impact", "Unblocks others"},
		UnblocksIDs: []string{"ntm-2000", "ntm-2001"},
	}

	body := c.formatAssignmentMessage(assignment, rec)

	if body == "" {
		t.Error("expected non-empty message body")
	}
	if !strings.Contains(body, "# Work Assignment") {
		t.Error("expected markdown header in message")
	}
	if !strings.Contains(body, "ntm-1234") {
		t.Error("expected bead ID in message")
	}
	if !strings.Contains(body, "High impact") {
		t.Error("expected reasons in message")
	}
	if !strings.Contains(body, "br show") {
		t.Error("expected br show instruction in message")
	}
}

func TestFormatAssignmentMessageSaysClaimAlreadyOwned(t *testing.T) {
	t.Parallel()
	c := New("test-session", "/tmp/test", nil, "TestAgent")
	work := &WorkAssignment{BeadID: "ntm-1234", BeadTitle: "Implement feature X"}
	rec := &bv.TriageRecommendation{ID: work.BeadID, Title: work.BeadTitle}

	body := c.formatAssignmentMessage(work, rec, "BlueLake/ntm-deadbeef")
	if !strings.Contains(body, "already claimed this bead atomically") {
		t.Fatalf("missing atomic claim handoff: %s", body)
	}
	if strings.Contains(body, "br update") {
		t.Fatalf("message tells recipient to re-claim owned work: %s", body)
	}
}

func TestDefaultScoreConfig(t *testing.T) {
	config := DefaultScoreConfig()

	if !config.PreferCriticalPath {
		t.Error("expected PreferCriticalPath to be true by default")
	}
	if !config.PenalizeFileOverlap {
		t.Error("expected PenalizeFileOverlap to be true by default")
	}
	if !config.UseAgentProfiles {
		t.Error("expected UseAgentProfiles to be true by default")
	}
	if !config.BudgetAware {
		t.Error("expected BudgetAware to be true by default")
	}
	if config.ContextThreshold != 80 {
		t.Errorf("expected ContextThreshold 80, got %f", config.ContextThreshold)
	}
}

func TestEstimateTaskComplexity(t *testing.T) {
	tests := []struct {
		name     string
		rec      *bv.TriageRecommendation
		expected float64
		minExp   float64
		maxExp   float64
	}{
		{
			name:   "epic is complex",
			rec:    &bv.TriageRecommendation{Type: "epic", Priority: 2},
			minExp: 0.7,
			maxExp: 1.0,
		},
		{
			name:   "chore is simple",
			rec:    &bv.TriageRecommendation{Type: "chore", Priority: 2},
			minExp: 0.0,
			maxExp: 0.4,
		},
		{
			name:   "feature is moderately complex",
			rec:    &bv.TriageRecommendation{Type: "feature", Priority: 2},
			minExp: 0.6,
			maxExp: 0.8,
		},
		{
			name:   "epic with many unblocks is very complex",
			rec:    &bv.TriageRecommendation{Type: "epic", Priority: 2, UnblocksIDs: []string{"a", "b", "c", "d", "e"}},
			minExp: 0.9,
			maxExp: 1.0,
		},
		{
			name:   "critical bug is simpler",
			rec:    &bv.TriageRecommendation{Type: "bug", Priority: 0},
			minExp: 0.3,
			maxExp: 0.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			complexity := estimateTaskComplexity(tt.rec)
			if complexity < tt.minExp || complexity > tt.maxExp {
				t.Errorf("expected complexity in [%f, %f], got %f", tt.minExp, tt.maxExp, complexity)
			}
		})
	}
}

func TestComputeAgentTypeBonus(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		rec       *bv.TriageRecommendation
		wantSign  string // "positive", "negative", "zero"
	}{
		{
			name:      "claude on epic gets bonus",
			agentType: "cc",
			rec:       &bv.TriageRecommendation{Type: "epic", Priority: 2},
			wantSign:  "positive",
		},
		{
			name:      "claude on chore gets penalty",
			agentType: "claude",
			rec:       &bv.TriageRecommendation{Type: "chore", Priority: 2},
			wantSign:  "negative",
		},
		{
			name:      "claude alias on epic gets bonus",
			agentType: "claude_code",
			rec:       &bv.TriageRecommendation{Type: "epic", Priority: 2},
			wantSign:  "positive",
		},
		{
			name:      "codex on chore gets bonus",
			agentType: "cod",
			rec:       &bv.TriageRecommendation{Type: "chore", Priority: 2},
			wantSign:  "positive",
		},
		{
			name:      "codex on epic gets penalty",
			agentType: "codex",
			rec:       &bv.TriageRecommendation{Type: "epic", Priority: 2},
			wantSign:  "negative",
		},
		{
			name:      "codex alias on chore gets bonus",
			agentType: "openai-codex",
			rec:       &bv.TriageRecommendation{Type: "chore", Priority: 2},
			wantSign:  "positive",
		},
		{
			name:      "gemini on medium task neutral or small bonus",
			agentType: "gmi",
			rec:       &bv.TriageRecommendation{Type: "task", Priority: 2},
			wantSign:  "zero", // task is medium complexity
		},
		{
			name:      "gemini alias on medium task neutral or small bonus",
			agentType: "google-gemini",
			rec:       &bv.TriageRecommendation{Type: "task", Priority: 2},
			wantSign:  "zero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bonus := computeAgentTypeBonus(tt.agentType, tt.rec)
			switch tt.wantSign {
			case "positive":
				if bonus <= 0 {
					t.Errorf("expected positive bonus, got %f", bonus)
				}
			case "negative":
				if bonus >= 0 {
					t.Errorf("expected negative bonus, got %f", bonus)
				}
			case "zero":
				if bonus < -0.05 || bonus > 0.1 {
					t.Errorf("expected near-zero bonus, got %f", bonus)
				}
			}
		})
	}
}

func TestComputeContextPenalty(t *testing.T) {
	tests := []struct {
		name         string
		contextUsage float64
		threshold    float64
		wantZero     bool
	}{
		{"below threshold", 50, 80, true},
		{"at threshold", 80, 80, true},
		{"above threshold", 90, 80, false},
		{"way above threshold", 95, 80, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			penalty := computeContextPenalty(tt.contextUsage, tt.threshold)
			if tt.wantZero && penalty != 0 {
				t.Errorf("expected zero penalty, got %f", penalty)
			}
			if !tt.wantZero && penalty <= 0 {
				t.Errorf("expected positive penalty, got %f", penalty)
			}
		})
	}

	// Verify penalty values are reasonable (normalized to 0-1 scale)
	t.Run("penalty values are reasonable", func(t *testing.T) {
		// 10% over threshold (90% usage, 80% threshold)
		penalty10 := computeContextPenalty(90, 80)
		if penalty10 < 0.04 || penalty10 > 0.06 {
			t.Errorf("10%% over threshold should give ~0.05 penalty, got %f", penalty10)
		}

		// 20% over threshold (100% usage, 80% threshold)
		penalty20 := computeContextPenalty(100, 80)
		if penalty20 < 0.09 || penalty20 > 0.11 {
			t.Errorf("20%% over threshold should give ~0.10 penalty, got %f", penalty20)
		}
	})
}

func TestComputeFileOverlapPenalty(t *testing.T) {
	tests := []struct {
		name         string
		agent        *AgentState
		reservations map[string][]string
		wantZero     bool
	}{
		{
			name:         "no reservations",
			agent:        &AgentState{PaneID: "%0"},
			reservations: nil,
			wantZero:     true,
		},
		{
			name:         "agent with reservations",
			agent:        &AgentState{PaneID: "%0", Reservations: []string{"a.go", "b.go", "c.go"}},
			reservations: nil,
			wantZero:     false,
		},
		{
			name:         "reservations in map",
			agent:        &AgentState{PaneID: "%0"},
			reservations: map[string][]string{"%0": {"x.go"}},
			wantZero:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			penalty := computeFileOverlapPenalty(tt.agent, tt.reservations)
			if tt.wantZero && penalty != 0 {
				t.Errorf("expected zero penalty, got %f", penalty)
			}
			if !tt.wantZero && penalty <= 0 {
				t.Errorf("expected positive penalty, got %f", penalty)
			}
		})
	}
}

func TestScoreAndSelectAssignmentsWithAgentReservations(t *testing.T) {
	// Test that agent.Reservations are used when existingReservations map is nil
	agents := []*AgentState{
		{PaneID: "%1", AgentType: "cc", ContextUsage: 30, Status: robot.StateWaiting},
		{PaneID: "%2", AgentType: "cc", ContextUsage: 30, Status: robot.StateWaiting, Reservations: []string{"a.go", "b.go", "c.go"}},
	}

	recommendations := []bv.TriageRecommendation{
		{ID: "ntm-001", Title: "Task", Type: "task", Status: "open", Priority: 2, Score: 0.5},
	}

	config := DefaultScoreConfig()
	results := ScoreAndSelectAssignments(agents, recommendations, config, nil) // nil reservations map

	if len(results) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(results))
	}

	// Agent %1 should get the task because %2 has reservations (penalty)
	if results[0].Agent.PaneID != "%1" {
		t.Errorf("expected agent %%1 (no reservations) to get task, got %s", results[0].Agent.PaneID)
	}

	// Verify the penalty was applied to agent %2
	// Score for %1: 0.5 base (no penalty)
	// Score for %2: 0.5 - 0.05 = 0.45 (with reservation penalty for 3 files)
	if results[0].ScoreBreakdown.FileOverlapPenalty != 0 {
		t.Errorf("expected no file overlap penalty for agent %%1, got %f", results[0].ScoreBreakdown.FileOverlapPenalty)
	}
}

func TestScoreAndSelectAssignments(t *testing.T) {
	agents := []*AgentState{
		{PaneID: "%1", AgentType: "cc", ContextUsage: 30, Status: robot.StateWaiting},
		{PaneID: "%2", AgentType: "cod", ContextUsage: 50, Status: robot.StateWaiting},
	}

	recommendations := []bv.TriageRecommendation{
		{ID: "ntm-001", Title: "Feature work", Type: "feature", Status: "open", Priority: 2, Score: 0.8},
		{ID: "ntm-002", Title: "Quick fix", Type: "chore", Status: "open", Priority: 2, Score: 0.6},
		{ID: "ntm-003", Title: "Blocked", Type: "task", Status: "blocked", Priority: 2, Score: 0.9},
		{ID: "ntm-004", Title: "Dependency gated", Type: "task", Status: "open", Priority: 1, Score: 1, BlockedBy: []string{"ntm-prerequisite"}},
		{ID: "ntm-005", Title: "Operator gated", Type: "task", Status: "open", Priority: 1, Score: 1, Labels: []string{"operator-action"}},
		// A container node ranked above everything else. Containers are
		// grouping nodes, never implementation work, so this must not
		// be dispatched no matter how highly triage scores it.
		{ID: "ntm-006", Title: "Epic container", Type: "epic", Status: "open", Priority: 0, Score: 1},
	}

	config := DefaultScoreConfig()
	results := ScoreAndSelectAssignments(agents, recommendations, config, nil)

	if len(results) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(results))
	}

	// Verify each agent got exactly one task
	agentTasks := make(map[string]string)
	for _, r := range results {
		if existing, ok := agentTasks[r.Agent.PaneID]; ok {
			t.Errorf("agent %s assigned twice: %s and %s", r.Agent.PaneID, existing, r.Assignment.BeadID)
		}
		agentTasks[r.Agent.PaneID] = r.Assignment.BeadID
	}

	// Verify blocked, gated, and container items are not assigned
	for _, r := range results {
		switch r.Assignment.BeadID {
		case "ntm-003", "ntm-004", "ntm-005", "ntm-006":
			t.Errorf("gated task %s should not be assigned", r.Assignment.BeadID)
		}
	}
}

func TestScoreAndSelectAssignmentsEmpty(t *testing.T) {
	// Empty agents
	result := ScoreAndSelectAssignments(nil, []bv.TriageRecommendation{{ID: "ntm-001"}}, DefaultScoreConfig(), nil)
	if result != nil {
		t.Error("expected nil for empty agents")
	}

	// Nil recommendations
	agents := []*AgentState{{PaneID: "%0", AgentType: "cc"}}
	result = ScoreAndSelectAssignments(agents, nil, DefaultScoreConfig(), nil)
	if result != nil {
		t.Error("expected nil for nil recommendations")
	}

	// Empty recommendations
	result = ScoreAndSelectAssignments(agents, []bv.TriageRecommendation{}, DefaultScoreConfig(), nil)
	if result != nil {
		t.Error("expected nil for empty recommendations")
	}
}

func TestComputeCriticalPathBonus(t *testing.T) {
	tests := []struct {
		name      string
		breakdown *bv.ScoreBreakdown
		wantZero  bool
	}{
		{
			name:      "low pagerank",
			breakdown: &bv.ScoreBreakdown{Pagerank: 0.01, BlockerRatio: 0.01},
			wantZero:  true,
		},
		{
			name:      "high pagerank",
			breakdown: &bv.ScoreBreakdown{Pagerank: 0.1, BlockerRatio: 0.01},
			wantZero:  false,
		},
		{
			name:      "high blocker ratio",
			breakdown: &bv.ScoreBreakdown{Pagerank: 0.01, BlockerRatio: 0.1},
			wantZero:  false,
		},
		{
			name:      "high time to impact",
			breakdown: &bv.ScoreBreakdown{Pagerank: 0.01, BlockerRatio: 0.01, TimeToImpact: 0.06},
			wantZero:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bonus := computeCriticalPathBonus(tt.breakdown)
			if tt.wantZero && bonus != 0 {
				t.Errorf("expected zero bonus, got %f", bonus)
			}
			if !tt.wantZero && bonus <= 0 {
				t.Errorf("expected positive bonus, got %f", bonus)
			}
		})
	}
}

func TestExtractTaskTags(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		wantTags    []string
	}{
		{
			name:     "test-related title",
			title:    "Add unit tests for the parser",
			wantTags: []string{"testing", "implementation"}, // "add" triggers implementation
		},
		{
			name:     "architecture task",
			title:    "Refactor the API design patterns",
			wantTags: []string{"architecture"},
		},
		{
			name:     "documentation task",
			title:    "Update README with new features",
			wantTags: []string{"documentation", "implementation"}, // "feature" triggers implementation
		},
		{
			name:     "implementation task",
			title:    "Implement new feature for user auth",
			wantTags: []string{"implementation"},
		},
		{
			name:     "review task",
			title:    "Code review for PR #123",
			wantTags: []string{"review"},
		},
		{
			name:     "bug fix",
			title:    "Fix crash when loading config",
			wantTags: []string{"bugs"},
		},
		{
			name:        "multiple tags from description",
			title:       "Add tests",
			description: "Refactor the code and add documentation",
			wantTags:    []string{"testing", "architecture", "documentation", "implementation"}, // "add" triggers implementation
		},
		{
			name:     "no matching tags",
			title:    "Random task with no keywords",
			wantTags: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := ExtractTaskTags(tt.title, tt.description)
			if len(tags) != len(tt.wantTags) {
				t.Errorf("expected %d tags, got %d: %v", len(tt.wantTags), len(tags), tags)
				return
			}
			tagSet := make(map[string]bool)
			for _, tag := range tags {
				tagSet[tag] = true
			}
			for _, want := range tt.wantTags {
				if !tagSet[want] {
					t.Errorf("expected tag %q not found in %v", want, tags)
				}
			}
		})
	}
}

func TestExtractMentionedFiles(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		wantFiles   []string
	}{
		{
			name:      "go file in title",
			title:     "Fix bug in internal/config/config.go",
			wantFiles: []string{"internal/config/config.go"},
		},
		{
			name:      "multiple files",
			title:     "Update cmd/main.go and internal/cli/run.go",
			wantFiles: []string{"cmd/main.go", "internal/cli/run.go"},
		},
		{
			name:      "glob pattern",
			title:     "Refactor internal/**/*.go files",
			wantFiles: []string{"internal/**/*.go"},
		},
		{
			name:        "files in description",
			title:       "Fix tests",
			description: "The tests in tests/e2e/main_test.go are failing",
			wantFiles:   []string{"tests/e2e/main_test.go"},
		},
		{
			name:      "no files mentioned",
			title:     "Improve performance of the system",
			wantFiles: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := ExtractMentionedFiles(tt.title, tt.description)
			if len(files) != len(tt.wantFiles) {
				t.Errorf("expected %d files, got %d: %v", len(tt.wantFiles), len(files), files)
				return
			}
			fileSet := make(map[string]bool)
			for _, f := range files {
				fileSet[f] = true
			}
			for _, want := range tt.wantFiles {
				if !fileSet[want] {
					t.Errorf("expected file %q not found in %v", want, files)
				}
			}
		})
	}
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"internal/config/config.go", true},
		{"cmd/main.go", true},
		{"pkg/**/*.ts", true},
		{"README.md", true},
		{".gitignore", true},
		{"hello", false},
		{"the", false},
		{"Fix", false},
		{"", false},
		{"a", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isFilePath(tt.input)
			if got != tt.want {
				t.Errorf("isFilePath(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMatchFocusPattern(t *testing.T) {
	tests := []struct {
		pattern string
		file    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.ts", false},
		{"internal/**", "internal/cli/run.go", true},
		{"internal/**", "cmd/main.go", false},
		{"**/*.go", "internal/config/config.go", true},
		{"**/*.go", "config.ts", false},
		{"internal/*.go", "internal/foo.go", true},
		{"internal/*.go", "internal/sub/foo.go", false},
		{"docs/**", "docs/README.md", true},
		{"tests/**", "internal/test.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.file, func(t *testing.T) {
			got := matchFocusPattern(tt.pattern, tt.file)
			if got != tt.want {
				t.Errorf("matchFocusPattern(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

func TestComputeProfileTagBonus(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		taskTags []string
		weight   float64
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "full match",
			tags:     []string{"testing", "qa"},
			taskTags: []string{"testing"},
			weight:   0.15,
			wantMin:  0.075, // 50% match * 0.15
			wantMax:  0.16,
		},
		{
			name:     "no match",
			tags:     []string{"documentation"},
			taskTags: []string{"testing"},
			weight:   0.15,
			wantMin:  0,
			wantMax:  0,
		},
		{
			name:     "multiple overlapping tags",
			tags:     []string{"testing", "qa", "quality"},
			taskTags: []string{"testing", "quality"},
			weight:   0.15,
			wantMin:  0.09, // 2/3 match * 0.15 = 0.10
			wantMax:  0.16,
		},
		{
			name:     "nil profile tags",
			tags:     nil,
			taskTags: []string{"testing"},
			weight:   0.15,
			wantMin:  0,
			wantMax:  0,
		},
		{
			name:     "empty task tags",
			tags:     []string{"testing"},
			taskTags: []string{},
			weight:   0.15,
			wantMin:  0,
			wantMax:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &persona.Persona{Tags: tt.tags}
			bonus := computeProfileTagBonus(profile, tt.taskTags, tt.weight)
			if bonus < tt.wantMin || bonus > tt.wantMax {
				t.Errorf("expected bonus in [%f, %f], got %f", tt.wantMin, tt.wantMax, bonus)
			}
		})
	}
}

func TestComputeFocusPatternBonus(t *testing.T) {
	tests := []struct {
		name           string
		focusPatterns  []string
		mentionedFiles []string
		weight         float64
		wantMin        float64
		wantMax        float64
	}{
		{
			name:           "match internal files",
			focusPatterns:  []string{"internal/**"},
			mentionedFiles: []string{"internal/config/config.go"},
			weight:         0.10,
			wantMin:        0.09,
			wantMax:        0.11,
		},
		{
			name:           "no match",
			focusPatterns:  []string{"docs/**"},
			mentionedFiles: []string{"internal/config/config.go"},
			weight:         0.10,
			wantMin:        0,
			wantMax:        0,
		},
		{
			name:           "partial match",
			focusPatterns:  []string{"internal/**", "docs/**"},
			mentionedFiles: []string{"internal/cli.go", "cmd/main.go"},
			weight:         0.10,
			wantMin:        0.04, // 1/2 match * 0.10
			wantMax:        0.06,
		},
		{
			name:           "nil focus patterns",
			focusPatterns:  nil,
			mentionedFiles: []string{"internal/cli.go"},
			weight:         0.10,
			wantMin:        0,
			wantMax:        0,
		},
		{
			name:           "empty mentioned files",
			focusPatterns:  []string{"internal/**"},
			mentionedFiles: []string{},
			weight:         0.10,
			wantMin:        0,
			wantMax:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := &persona.Persona{FocusPatterns: tt.focusPatterns}
			bonus := computeFocusPatternBonus(profile, tt.mentionedFiles, tt.weight)
			if bonus < tt.wantMin || bonus > tt.wantMax {
				t.Errorf("expected bonus in [%f, %f], got %f", tt.wantMin, tt.wantMax, bonus)
			}
		})
	}
}

func TestScoreAssignmentWithProfile(t *testing.T) {
	// Test that profile bonuses are applied correctly in scoreAssignment
	testerProfile := &persona.Persona{
		Name:          "tester",
		Tags:          []string{"testing", "qa", "quality"},
		FocusPatterns: []string{"**/*_test.go", "tests/**"},
	}

	agent := &AgentState{
		PaneID:    "%1",
		AgentType: "cc",
		Profile:   testerProfile,
	}

	// Task that matches tester profile
	testingTask := &bv.TriageRecommendation{
		ID:    "ntm-001",
		Title: "Add unit tests for internal/config/config_test.go",
		Type:  "task",
		Score: 0.5,
	}

	// Task that doesn't match tester profile
	docTask := &bv.TriageRecommendation{
		ID:    "ntm-002",
		Title: "Update documentation in docs/README.md",
		Type:  "task",
		Score: 0.5,
	}

	config := ScoreConfig{
		UseAgentProfiles:        true,
		ProfileTagBoostWeight:   0.15,
		FocusPatternBoostWeight: 0.10,
	}

	testingResult := scoreAssignment(agent, testingTask, config, nil)
	docResult := scoreAssignment(agent, docTask, config, nil)

	// Testing task should have higher profile bonuses
	if testingResult.ScoreBreakdown.ProfileTagBonus <= docResult.ScoreBreakdown.ProfileTagBonus {
		t.Errorf("testing task should have higher ProfileTagBonus: testing=%f, doc=%f",
			testingResult.ScoreBreakdown.ProfileTagBonus, docResult.ScoreBreakdown.ProfileTagBonus)
	}

	if testingResult.ScoreBreakdown.FocusPatternBonus <= docResult.ScoreBreakdown.FocusPatternBonus {
		t.Errorf("testing task should have higher FocusPatternBonus: testing=%f, doc=%f",
			testingResult.ScoreBreakdown.FocusPatternBonus, docResult.ScoreBreakdown.FocusPatternBonus)
	}

	// Total score for testing task should be higher
	if testingResult.TotalScore <= docResult.TotalScore {
		t.Errorf("testing task should have higher total score: testing=%f, doc=%f",
			testingResult.TotalScore, docResult.TotalScore)
	}
}

func TestScoreAssignmentWithNilProfile(t *testing.T) {
	// Test that nil profile doesn't cause panic and results in zero bonuses
	agent := &AgentState{
		PaneID:    "%1",
		AgentType: "cc",
		Profile:   nil,
	}

	task := &bv.TriageRecommendation{
		ID:    "ntm-001",
		Title: "Add unit tests",
		Type:  "task",
		Score: 0.5,
	}

	config := ScoreConfig{
		UseAgentProfiles:        true,
		ProfileTagBoostWeight:   0.15,
		FocusPatternBoostWeight: 0.10,
	}

	result := scoreAssignment(agent, task, config, nil)

	if result.ScoreBreakdown.ProfileTagBonus != 0 {
		t.Errorf("expected zero ProfileTagBonus with nil profile, got %f", result.ScoreBreakdown.ProfileTagBonus)
	}
	if result.ScoreBreakdown.FocusPatternBonus != 0 {
		t.Errorf("expected zero FocusPatternBonus with nil profile, got %f", result.ScoreBreakdown.FocusPatternBonus)
	}
}

func TestCoordinatorMailDispatchErrorTerminalizesOnlyToolRejection(t *testing.T) {
	rejection := &agentmail.ToolRejectionError{Err: errors.New("sender is not registered")}
	if got := coordinatorMailDispatchError(rejection); !assignmentstore.IsGuaranteedNoActuation(got) {
		t.Fatalf("explicit server rejection stayed outcome-unknown: %v", got)
	}
	wrapped := fmt.Errorf("send work assignment: %w", rejection)
	if got := coordinatorMailDispatchError(wrapped); !assignmentstore.IsGuaranteedNoActuation(got) {
		t.Fatalf("wrapped server rejection stayed outcome-unknown: %v", got)
	}

	timeout := agentmail.NewAPIError("send_message", 0, agentmail.ErrTimeout)
	if got := coordinatorMailDispatchError(timeout); assignmentstore.IsGuaranteedNoActuation(got) || !errors.Is(got, agentmail.ErrTimeout) {
		t.Fatalf("ambiguous timeout was terminalized: %v", got)
	}

	transport := errors.New("connection reset by peer")
	if got := coordinatorMailDispatchError(transport); assignmentstore.IsGuaranteedNoActuation(got) {
		t.Fatalf("ambiguous transport failure was terminalized: %v", got)
	}

	if got := coordinatorMailDispatchError(nil); got != nil {
		t.Fatalf("nil error changed to %v", got)
	}
}

// TestRepositoryReservationPathsRequireRealRepositoryTargets pins ntm#302:
// reservation candidates come from free-text bead titles, so slash-bearing
// prose ("13714/2m", a date) used to become Agent Mail leases on paths that do
// not exist. Only candidates that resolve inside the repository survive.
func TestRepositoryReservationPathsRequireRealRepositoryTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "coordinator"), 0o755); err != nil {
		t.Fatalf("create repo tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "coordinator", "assign.go"), []byte("package coordinator\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	got := repositoryReservationPaths(root, []string{
		"internal/coordinator/assign.go", // real file
		"internal/coordinator/*.go",      // glob with a real literal prefix
		"internal/coordinator/assign.go", // duplicate
		"13714/2m",                       // rate expression
		"2026/09/03",                     // date
		"internal/coordinator/new.go",    // to-be-created file: parent exists
		"docs/does-not-exist.md",         // plausible but absent
		"/etc/passwd",                    // absolute
		"../outside/file.go",             // escapes the repo
		"   ",                            // blank
	})
	want := []string{"internal/coordinator/assign.go", "internal/coordinator/*.go", "internal/coordinator/new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repositoryReservationPaths() = %v, want %v", got, want)
	}

	if got := repositoryReservationPaths("", []string{"internal/coordinator/assign.go"}); len(got) != 0 {
		t.Fatalf("no project key must yield no reservations, got %v", got)
	}
}

// TestIsFilePathRejectsLeadingDigitProse is the extraction-side half of the
// same guard: "13714/2m" is a quantity, not a path.
func TestIsFilePathRejectsLeadingDigitProse(t *testing.T) {
	for _, s := range []string{"13714/2m", "2026/09/03", "3/4"} {
		if isFilePath(s) {
			t.Errorf("isFilePath(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"internal/coordinator/assign.go", "docs/README.md", "cmd/ntm", ".github/workflows"} {
		if !isFilePath(s) {
			t.Errorf("isFilePath(%q) = false, want true", s)
		}
	}
}

// TestCoordinatorDispatchRetryBackoff pins the spacing applied to retries of a
// pre-actuation dispatch failure (ntm#294): retryable is not the same as
// retry-immediately-every-cycle.
func TestCoordinatorDispatchRetryBackoff(t *testing.T) {
	now := time.Now().UTC()
	recorded := &assignmentstore.Assignment{
		DispatchState: assignmentstore.DispatchPending, DispatchAttempts: 1,
		DispatchStartedAt: &now, LastDispatchError: "composer not ready",
	}
	if coordinatorDispatchRetryReady(recorded, now.Add(coordinatorDispatchRetryInitial-time.Millisecond)) {
		t.Fatal("first retry became eligible before the initial backoff")
	}
	if !coordinatorDispatchRetryReady(recorded, now.Add(coordinatorDispatchRetryInitial)) {
		t.Fatal("first retry did not become eligible at the initial backoff")
	}

	recorded.DispatchAttempts = 9
	if coordinatorDispatchRetryReady(recorded, now.Add(coordinatorDispatchRetryMaximum-time.Millisecond)) {
		t.Fatal("retry became eligible before the capped backoff")
	}
	if !coordinatorDispatchRetryReady(recorded, now.Add(coordinatorDispatchRetryMaximum)) {
		t.Fatal("retry did not become eligible at the capped backoff")
	}

	// A row that has not failed yet is never held back.
	if !coordinatorDispatchRetryReady(&assignmentstore.Assignment{DispatchStartedAt: &now}, now) {
		t.Fatal("a row with no recorded dispatch error must not be delayed")
	}
	if !coordinatorDispatchRetryReady(nil, now) {
		t.Fatal("a nil row must not be delayed")
	}
}
