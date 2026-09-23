package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

func snapshotSourceFixture(t *testing.T, text string) string {
	t.Helper()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return project
}

func snapshotCandidateFixture(ids ...string) *WorkSection {
	work := NewWorkSection()
	work.Available = true
	work.Summary = &WorkSummary{Ready: len(ids), Total: len(ids)}
	for _, id := range ids {
		work.Ready = append(work.Ready, WorkItem{ID: id, Title: "Task " + id, Priority: 2})
	}
	return work
}

func snapshotPayload(t *testing.T, project string, input *WorkSection, policy WorkVerificationPolicy) []byte {
	t.Helper()
	work, err := collectWorkWithSource(context.Background(), project, policy, func(context.Context) (*WorkSection, error) { return input, nil })
	if err != nil {
		t.Fatal(err)
	}
	work = limitVerifiedWorkPreview(work, 1)
	key, payload, err := MarshalWorkSnapshot(work)
	if err != nil || key == "" {
		t.Fatalf("encode snapshot: %s %v", key, err)
	}
	return payload
}

func TestPersistedWorkSnapshotRebuildsFullCountsAndEmptyEvidence(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"blocked","status":"open","dependencies":[{"depends_on_id":"missing","type":"blocks"}]}`+"\n"+`{"id":"a","status":"open"}`+"\n"+`{"id":"b","status":"open"}`+"\n")
	input := snapshotCandidateFixture("blocked", "a", "b")
	before, _ := json.Marshal(input)
	payload := snapshotPayload(t, project, input, WorkVerificationPolicy{})
	// Emulate a fresh process: serialize to disk and discard the live objects.
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, payload)
	if err != nil || !got.Available || len(got.Ready) != 1 || got.Ready[0].ID != "a" || got.Summary.Ready != 2 {
		t.Fatalf("full ready set lost to display cutoff: %+v, %v", got, err)
	}
	if !got.Verification.FromCache || got.Verification.VerifiedReady == nil || *got.Verification.VerifiedReady != 2 || len(got.Verification.Excluded) != 1 {
		t.Fatalf("cache receipt lost: %+v", got.Verification)
	}
	after, _ := json.Marshal(input)
	if string(after) != string(before) {
		t.Fatal("verification mutated the input collection")
	}

	empty := snapshotPayload(t, project, snapshotCandidateFixture(), WorkVerificationPolicy{})
	got, err = restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 1, empty)
	if err != nil || !got.Available || got.Summary.Ready != 0 || got.Ready == nil || got.Verification.ReasonCode != worksource.NoClaimableCode {
		t.Fatalf("checked-empty work was confused with missing evidence: %+v, %v", got, err)
	}
}

func TestPersistedWorkSnapshotRejectsChangedSourceAndForeignProject(t *testing.T) {
	text := `{"id":"a","status":"open"}` + "\n"
	project := snapshotSourceFixture(t, text)
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
	other := snapshotSourceFixture(t, text)
	for _, dir := range []string{other, project} {
		if dir == project {
			if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(`{"id":"a","status":"closed"}`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
			calls++
			return nil, errors.New("must not read peers before source verification")
		}}
		got, err := restoreWorkSnapshot(context.Background(), dir, policy, 10, payload)
		if !errors.Is(err, worksource.ErrStale) || got == nil || got.Available || len(got.Ready) != 0 || calls != 0 {
			t.Fatalf("stale or foreign work escaped: %+v, %v, reads=%d", got, err, calls)
		}
	}
}

func TestPersistedWorkSnapshotChecksGitHeadAndAllowsOrdinaryDirtyFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", project}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	run("init", "-b", "main")
	run("add", ".beads/issues.jsonl")
	run("commit", "-m", "baseline")
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
	if err := os.WriteFile(filepath.Join(project, "active-work.txt"), []byte("ordinary dirty development"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 10, payload)
	if err != nil || !got.Verification.Dirty {
		t.Fatalf("dirty local development rejected: %+v %v", got, err)
	}
	run("commit", "--allow-empty", "-m", "new HEAD")
	got, err = restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 10, payload)
	if !errors.Is(err, worksource.ErrStale) || got.Available {
		t.Fatalf("changed HEAD escaped: %+v %v", got, err)
	}
}

func TestPersistedWorkSnapshotRefreshesReservationsBeforePreview(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n"+`{"id":"b","status":"open"}`+"\n"+`{"id":"c","status":"open"}`+"\n")
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a", "b", "c"), WorkVerificationPolicy{})
	calls := 0
	policy := WorkVerificationPolicy{readReservations: func(ctx context.Context, key string) (*agentmail.WorkReservationSnapshot, error) {
		calls++
		return &agentmail.WorkReservationSnapshot{ProjectID: 9, ProjectKey: key, ObservedAt: time.Now(), Active: 1, ByBead: map[string][]string{"a": {"peer"}}}, nil
	}}
	got, err := restoreWorkSnapshot(context.Background(), project, policy, 1, payload)
	if err != nil || calls != 1 || len(got.Ready) != 1 || got.Ready[0].ID != "b" || got.Summary.Ready != 2 || got.Verification.Reservations.State != "observed" {
		t.Fatalf("reservation was replayed or preview-limited: %+v %v", got, err)
	}
}

func TestPersistedWorkSnapshotCancellationAndSourceChangeDuringReservationRead(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n")
			payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := WorkVerificationPolicy{readReservations: func(context.Context, string) (*agentmail.WorkReservationSnapshot, error) {
				if cancelRead {
					cancel()
				} else if err := os.WriteFile(filepath.Join(project, ".beads", "issues.jsonl"), []byte(`{"id":"a","status":"closed"}`+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				return nil, errors.New("reservation unavailable")
			}}
			got, err := restoreWorkSnapshot(ctx, project, policy, 10, payload)
			want := worksource.ErrStale
			if cancelRead {
				want = context.Canceled
			}
			if !errors.Is(err, want) || got.Available || len(got.Ready) != 0 {
				t.Fatalf("failed validation escaped: %+v %v", got, err)
			}
		})
	}
}

func TestPersistedWorkSnapshotRejectsMalformedAndUnverifiedPayloads(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open"}`+"\n")
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), WorkVerificationPolicy{})
	var original workSnapshotEnvelope
	if err := json.Unmarshal(payload, &original); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"null", "trailing", "version", "missing-ready", "duplicate", "wrong-policy", "unbound"} {
		t.Run(kind, func(t *testing.T) {
			var current workSnapshotEnvelope
			_ = json.Unmarshal(payload, &current)
			switch kind {
			case "version":
				current.Version++
			case "missing-ready":
				current.Work.Ready = nil
			case "duplicate":
				current.Work.Ready = append(current.Work.Ready, current.Work.Ready[0])
			case "wrong-policy":
				current.Policy.RequireClean = true
			case "unbound":
				current.Source = nil
			}
			data, _ := json.Marshal(current)
			if kind == "null" {
				data = []byte("null")
			}
			if kind == "trailing" {
				data = append(data, []byte(" {}")...)
			}
			got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 10, data)
			if !errors.Is(err, worksource.ErrStale) || got.Available || len(got.Ready) != 0 {
				t.Fatalf("invalid payload accepted: %+v %v", got, err)
			}
		})
	}
	failed := NewWorkSection()
	failed.Reason = "tracker could not be read"
	failed.Verification = &WorkVerification{ProjectDir: project}
	_, marker, err := MarshalWorkSnapshot(failed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 10, marker)
	if !errors.Is(err, ErrWorkSnapshotUnavailable) || got.Available {
		t.Fatalf("unavailable marker became empty work: %+v %v", got, err)
	}
}

func TestPersistedWorkSnapshotRecollectsAtDeferredBoundary(t *testing.T) {
	deadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	project := snapshotSourceFixture(t, fmt.Sprintf("{\"id\":\"later\",\"status\":\"open\",\"defer_until\":%q}\n", deadline.Format(time.RFC3339)))
	// The direct ready response is empty: the deferred issue is not a candidate.
	payload := snapshotPayload(t, project, snapshotCandidateFixture(), WorkVerificationPolicy{})
	var envelope workSnapshotEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.RecollectAt.Equal(deadline) {
		t.Fatalf("omitted task's time boundary was lost: %+v", envelope)
	}
	// Advance the envelope's cutoff rather than sleep; source bytes stay fixed.
	envelope.RecollectAt = time.Now().Add(-time.Second)
	payload, _ = json.Marshal(envelope)
	got, err := restoreWorkSnapshot(context.Background(), project, WorkVerificationPolicy{}, 10, payload)
	if !errors.Is(err, ErrWorkSnapshotUnavailable) || got.Available {
		t.Fatalf("obsolete empty set reused: %+v %v", got, err)
	}
}

func TestPersistedWorkSnapshotNormalizesEquivalentProgramPolicies(t *testing.T) {
	project := snapshotSourceFixture(t, `{"id":"a","status":"open","labels":["program:x"]}`+"\n")
	first := WorkVerificationPolicy{ProgramLabels: []string{"program:x", "PROGRAM:Y", "program:x"}}
	payload := snapshotPayload(t, project, snapshotCandidateFixture("a"), first)
	second := WorkVerificationPolicy{ProgramLabels: []string{"program:y", "program:x"}}
	got, err := restoreWorkSnapshot(context.Background(), project, second, 10, payload)
	if err != nil || !reflect.DeepEqual([]string{got.Ready[0].ID}, []string{"a"}) {
		t.Fatalf("equivalent policy rejected: %+v %v", got, err)
	}
}
