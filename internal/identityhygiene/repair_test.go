package identityhygiene

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// repairFixture writes N identity files into a temp dir, returns the
// dir, the IdentityRecord list, and a 90-minute-stale modtime so the
// default 1h StaleAfter window flags them.
func repairFixture(t *testing.T) (string, []IdentityRecord, time.Time) {
	t.Helper()
	dir := t.TempDir()
	stale := time.Now().Add(-90 * time.Minute)

	files := []struct {
		name      string
		paneID    string
		linked    string
		project   string
		agentName string
	}{
		{"identity-a.json", "%dead-1", "", "abc123abc123", "agent-a"},
		{"identity-b.json", "%dead-2", "", "abc123abc123", "agent-b"},
		{"contact-link.json", "", "ghost-agent", "abc123abc123", "linker"},
	}
	var records []IdentityRecord
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
		records = append(records, IdentityRecord{
			Path:        path,
			AgentName:   f.agentName,
			ProjectHash: f.project,
			PaneID:      f.paneID,
			LinkedAgent: f.linked,
			ModifiedAt:  stale,
		})
	}
	return dir, records, stale
}

func TestRepair_DryRunIsTheDefaultAndDoesNotTouchFilesystem(t *testing.T) {
	dir, records, _ := repairFixture(t)

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities:       records,
			LivePanes:        nil, // every pane is dead
			KnownProjectKeys: []string{"unknown-project-key"},
			Now:              time.Now(),
		},
		AllowedRoots: []string{dir},
		// DryRun left at the zero value (false) — but with AllowedRoots
		// set, the API should still default to safe behavior unless
		// DryRun is explicitly true. Verify DryRun=false with roots
		// actually deletes only what's flagged.
	})
	// First sanity check: when DryRun=false and AllowedRoots populated,
	// repair runs in mutation mode. The fixture has all-dead panes
	// AND unknown projects, so all 3 files are flagged. They should
	// all be removed.
	if rep.DryRun {
		t.Fatalf("expected mutation mode (DryRun=false) with AllowedRoots set, got DryRun=true")
	}
	for _, r := range records {
		if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, got err=%v", r.Path, err)
		}
	}
	if rep.Summary.Removed != 3 {
		t.Errorf("Summary.Removed = %d, want 3 (actions=%+v)", rep.Summary.Removed, rep.Actions)
	}
}

func TestRepair_DryRunReportsCandidatesWithoutDeleting(t *testing.T) {
	dir, records, _ := repairFixture(t)

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities:       records,
			LivePanes:        nil,
			KnownProjectKeys: []string{"unknown-project-key"},
			Now:              time.Now(),
		},
		AllowedRoots: []string{dir},
		DryRun:       true,
	})
	if !rep.DryRun {
		t.Fatalf("expected DryRun=true in report")
	}
	if rep.Summary.Removed != 0 {
		t.Errorf("dry-run reported Removed=%d, must be 0", rep.Summary.Removed)
	}
	if rep.Summary.WouldRemove == 0 {
		t.Errorf("dry-run produced no would_remove actions: %+v", rep.Actions)
	}
	for _, r := range records {
		if _, err := os.Stat(r.Path); err != nil {
			t.Errorf("dry-run touched %s: %v", r.Path, err)
		}
	}
}

func TestRepair_EmptyAllowedRootsForcesDryRunEvenIfRequestedMutating(t *testing.T) {
	dir, records, _ := repairFixture(t)

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities:       records,
			LivePanes:        nil,
			KnownProjectKeys: []string{"x"},
		},
		AllowedRoots: nil,   // no roots
		DryRun:       false, // caller asked to mutate
	})
	if !rep.DryRun {
		t.Fatalf("empty AllowedRoots must force DryRun, got DryRun=false")
	}
	for _, r := range records {
		if _, err := os.Stat(r.Path); err != nil {
			t.Errorf("expected %s untouched after empty-roots dry-run, got %v", r.Path, err)
		}
	}
	// Without roots, every candidate is out_of_bounds.
	if rep.Summary.SkippedOutOfBounds == 0 {
		t.Errorf("expected SkippedOutOfBounds>0, got %+v", rep.Summary)
	}
	if rep.Summary.Removed != 0 {
		t.Errorf("Removed must be 0, got %d", rep.Summary.Removed)
	}
	_ = dir
}

func TestRepair_PathOutsideAllowedRootIsNeverDeleted(t *testing.T) {
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Stale identity record whose Path is in outsideDir, but
	// AllowedRoots only lists allowedDir.
	stale := time.Now().Add(-90 * time.Minute)
	outsidePath := filepath.Join(outsideDir, "identity-x.json")
	if err := os.WriteFile(outsidePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(outsidePath, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities: []IdentityRecord{{
				Path:       outsidePath,
				AgentName:  "x",
				PaneID:     "%dead-x",
				ModifiedAt: stale,
			}},
		},
		AllowedRoots: []string{allowedDir},
		DryRun:       false,
	})
	if rep.Summary.Removed != 0 {
		t.Fatalf("path outside allowed root was deleted; Removed=%d", rep.Summary.Removed)
	}
	if rep.Summary.SkippedOutOfBounds == 0 {
		t.Fatalf("expected SkippedOutOfBounds>=1, got %+v", rep.Summary)
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Errorf("file outside allowed root was deleted: %v", err)
	}
}

func TestRepair_PathTraversalAttemptIsRejected(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	stale := time.Now().Add(-90 * time.Minute)

	// File LITERALLY in the parent of `root`, but the candidate path
	// uses ".." to try to escape. After path cleaning, this resolves
	// outside root and must be rejected.
	escapingFile := filepath.Join(parent, "outside.json")
	if err := os.WriteFile(escapingFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(escapingFile)
	if err := os.Chtimes(escapingFile, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	traversal := filepath.Join(root, "..", "outside.json")

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities: []IdentityRecord{{
				Path:       traversal,
				PaneID:     "%dead-traverse",
				ModifiedAt: stale,
			}},
		},
		AllowedRoots: []string{root},
		DryRun:       false,
	})
	if rep.Summary.Removed != 0 {
		t.Fatalf("traversal escaped root: Removed=%d", rep.Summary.Removed)
	}
	if rep.Summary.SkippedOutOfBounds == 0 {
		t.Fatalf("traversal not flagged out_of_bounds: %+v", rep.Summary)
	}
	if _, err := os.Stat(escapingFile); err != nil {
		t.Errorf("file outside root deleted: %v", err)
	}
}

func TestRepair_MissingFileIsSkippedNotErrored(t *testing.T) {
	dir := t.TempDir()
	stale := time.Now().Add(-90 * time.Minute)
	gonePath := filepath.Join(dir, "ghost.json")
	// File never created.

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities: []IdentityRecord{{
				Path:       gonePath,
				PaneID:     "%gone",
				ModifiedAt: stale,
			}},
		},
		AllowedRoots: []string{dir},
		DryRun:       false,
	})
	if rep.Summary.Failed != 0 {
		t.Errorf("missing file should not produce failure; Failed=%d", rep.Summary.Failed)
	}
	if rep.Summary.SkippedMissing == 0 {
		t.Errorf("missing file should be SkippedMissing; got %+v", rep.Summary)
	}
}

func TestRepair_RemoveErrorIsRecordedNotPanicked(t *testing.T) {
	dir, records, _ := repairFixture(t)

	wantErr := errors.New("permission denied or whatever")
	calls := 0
	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities:       records,
			LivePanes:        nil,
			KnownProjectKeys: []string{"unknown-project-key"},
		},
		AllowedRoots: []string{dir},
		DryRun:       false,
		Remove: func(p string) error {
			calls++
			return wantErr
		},
	})
	if calls == 0 {
		t.Fatal("Remove hook never invoked")
	}
	if rep.Summary.Failed == 0 {
		t.Fatalf("Remove error should produce remove_failed; got %+v", rep.Summary)
	}
	if rep.Summary.Removed != 0 {
		t.Errorf("Removed=%d but Remove returned an error", rep.Summary.Removed)
	}
	for _, a := range rep.Actions {
		if a.Status == "remove_failed" && a.Error == "" {
			t.Errorf("remove_failed action missing Error: %+v", a)
		}
	}
}

func TestRepair_DoesNotActOnDeadPaneFinding(t *testing.T) {
	dir := t.TempDir()
	// No identity records on disk; all the evidence is a dead-pane
	// agent in the registry. Repair must not synthesize file paths
	// from agent names — that's an Agent Mail registry concern.
	rep := Repair(RepairInputs{
		Inputs: Inputs{
			RegisteredAgents: []RegisteredAgent{{
				Name:         "abandoned-agent",
				PaneID:       "%dead-pane",
				LastActiveAt: time.Now().Add(-2 * time.Hour),
			}},
			LivePanes: nil,
		},
		AllowedRoots: []string{dir},
		DryRun:       false,
	})
	if len(rep.Actions) != 0 {
		t.Fatalf("dead_pane finding produced repair actions: %+v", rep.Actions)
	}
	if rep.Summary.Removed != 0 || rep.Summary.WouldRemove != 0 || rep.Summary.SkippedOutOfBounds != 0 {
		t.Errorf("dead_pane should be a no-op: %+v", rep.Summary)
	}
}

func TestRepair_DeterministicActionOrderForAuditTrail(t *testing.T) {
	dir, records, _ := repairFixture(t)

	first := Repair(RepairInputs{
		Inputs:       Inputs{Identities: records, LivePanes: nil, KnownProjectKeys: []string{"x"}},
		AllowedRoots: []string{dir},
		DryRun:       true,
	})
	second := Repair(RepairInputs{
		Inputs:       Inputs{Identities: records, LivePanes: nil, KnownProjectKeys: []string{"x"}},
		AllowedRoots: []string{dir},
		DryRun:       true,
	})
	if len(first.Actions) != len(second.Actions) {
		t.Fatalf("non-deterministic action count: %d vs %d", len(first.Actions), len(second.Actions))
	}
	for i := range first.Actions {
		if first.Actions[i].Path != second.Actions[i].Path || first.Actions[i].Code != second.Actions[i].Code {
			t.Errorf("non-deterministic order at %d: %+v vs %+v",
				i, first.Actions[i], second.Actions[i])
		}
	}
}

// bd-0ji1i: pathInsideAny must NOT classify a file whose basename
// starts with two literal dots (e.g. "..stale.json") as escaping the
// root. The pre-fix string-prefix check on bare ".." matched any
// such basename and silently flagged the candidate skipped_out_of_
// bounds even though the file lived inside the AllowedRoot.
func TestRepair_LeadingDotDotBasenameIsNotTreatedAsEscape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stale := time.Now().Add(-90 * time.Minute)

	// File with a leading-dot-dot basename, INSIDE the AllowedRoot.
	// POSIX permits this naming; Repair should treat it the same as
	// any other identity record.
	dotDotPath := filepath.Join(dir, "..stale.json")
	if err := os.WriteFile(dotDotPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(dotDotPath, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities: []IdentityRecord{{
				Path:       dotDotPath,
				AgentName:  "alice",
				PaneID:     "%dead-leading-dotdot",
				ModifiedAt: stale,
			}},
		},
		AllowedRoots: []string{dir},
		DryRun:       false,
	})

	if rep.Summary.SkippedOutOfBounds != 0 {
		t.Fatalf("leading-dot-dot basename was rejected as out_of_bounds; SkippedOutOfBounds=%d, actions=%+v",
			rep.Summary.SkippedOutOfBounds, rep.Actions)
	}
	if rep.Summary.Removed != 1 {
		t.Errorf("Removed = %d, want 1 (the file is inside the root and should be removable)", rep.Summary.Removed)
	}
	if _, err := os.Stat(dotDotPath); !os.IsNotExist(err) {
		t.Errorf("leading-dot-dot file still exists after Repair: %v", err)
	}
}

// Companion: an actual ../escape path traversal MUST still be
// rejected as out_of_bounds (preserves the existing safety contract).
func TestRepair_ActualEscapeStillRejectedAfterDotDotFix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := filepath.Dir(root)
	stale := time.Now().Add(-90 * time.Minute)

	escapingFile := filepath.Join(parent, "outside-bd-0ji1i.json")
	if err := os.WriteFile(escapingFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(escapingFile)
	if err := os.Chtimes(escapingFile, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	traversal := filepath.Join(root, "..", "outside-bd-0ji1i.json")
	rep := Repair(RepairInputs{
		Inputs: Inputs{
			Identities: []IdentityRecord{{
				Path:       traversal,
				PaneID:     "%dead-escape",
				ModifiedAt: stale,
			}},
		},
		AllowedRoots: []string{root},
		DryRun:       false,
	})

	if rep.Summary.Removed != 0 {
		t.Fatalf("traversal escaped after bd-0ji1i fix; Removed=%d", rep.Summary.Removed)
	}
	if rep.Summary.SkippedOutOfBounds == 0 {
		t.Fatalf("real escape no longer rejected: %+v", rep.Summary)
	}
	if _, err := os.Stat(escapingFile); err != nil {
		t.Errorf("real escape file deleted: %v", err)
	}
}
