package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestComputeScrollbackDiff(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		current  string
		wantDiff string
	}{
		{
			name:     "empty base",
			base:     "",
			current:  "line1\nline2\nline3",
			wantDiff: "line1\nline2\nline3",
		},
		{
			name:     "empty current",
			base:     "line1\nline2",
			current:  "",
			wantDiff: "",
		},
		{
			name:     "new lines appended",
			base:     "line1\nline2",
			current:  "line1\nline2\nline3\nline4",
			wantDiff: "line3\nline4",
		},
		{
			name:     "no new lines",
			base:     "line1\nline2\nline3",
			current:  "line1\nline2",
			wantDiff: "",
		},
		{
			name:     "scrollback rollover keeps only new tail",
			base:     "line1\nline2\nline3",
			current:  "line2\nline3\nline4",
			wantDiff: "line4",
		},
		{
			name:     "scrollback rollover with full replacement falls back to current",
			base:     "line1\nline2\nline3",
			current:  "line4\nline5",
			wantDiff: "line4\nline5",
		},
		{
			name:     "both empty",
			base:     "",
			current:  "",
			wantDiff: "",
		},
		{
			name:     "identical content",
			base:     "line1\nline2",
			current:  "line1\nline2",
			wantDiff: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeScrollbackDiff(tt.base, tt.current)
			if got != tt.wantDiff {
				t.Errorf("computeScrollbackDiff() = %q, want %q", got, tt.wantDiff)
			}
		})
	}
}

func TestIncrementalCreator_loadPaneScrollback_UsesRecordedScrollbackFile(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)

	const (
		sessionName  = "test-session"
		checkpointID = "test-checkpoint"
		content      = "recorded scrollback\nline 2\n"
	)

	cpDir := storage.CheckpointDir(sessionName, checkpointID)
	if err := os.MkdirAll(filepath.Join(cpDir, PanesDir), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	compressed, err := gzipCompress([]byte(content))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	scrollbackRelPath := filepath.Join(PanesDir, "pane_from_file.txt.gz")
	scrollbackAbsPath := filepath.Join(cpDir, scrollbackRelPath)
	if err := os.WriteFile(scrollbackAbsPath, compressed, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	got, err := creator.loadPaneScrollback(sessionName, checkpointID, PaneState{
		ID:             "%stale",
		ScrollbackFile: scrollbackRelPath,
	})
	if err != nil {
		t.Fatalf("loadPaneScrollback failed: %v", err)
	}
	if got != content {
		t.Fatalf("loadPaneScrollback = %q, want %q", got, content)
	}
}

func TestIncrementalCreator_computeGitChange(t *testing.T) {
	ic := NewIncrementalCreator()
	stringPtr := func(value string) *string { return &value }

	tests := []struct {
		name       string
		base       GitState
		current    GitState
		wantChange bool
		wantBranch *string
	}{
		{
			name: "no changes",
			base: GitState{
				Branch: "main",
				Commit: "abc123",
			},
			current: GitState{
				Branch: "main",
				Commit: "abc123",
			},
			wantChange: false,
		},
		{
			name: "commit changed",
			base: GitState{
				Branch: "main",
				Commit: "abc123",
			},
			current: GitState{
				Branch: "main",
				Commit: "def456",
			},
			wantChange: true,
		},
		{
			name: "branch changed",
			base: GitState{
				Branch: "main",
				Commit: "abc123",
			},
			current: GitState{
				Branch: "feature",
				Commit: "abc123",
			},
			wantChange: true,
			wantBranch: stringPtr("feature"),
		},
		{
			name: "branch changed to detached head",
			base: GitState{
				Branch: "main",
				Commit: "abc123",
			},
			current: GitState{
				Branch: "",
				Commit: "abc123",
			},
			wantChange: true,
			wantBranch: stringPtr(""),
		},
		{
			name: "dirty state changed",
			base: GitState{
				Branch:  "main",
				Commit:  "abc123",
				IsDirty: false,
			},
			current: GitState{
				Branch:  "main",
				Commit:  "abc123",
				IsDirty: true,
			},
			wantChange: true,
		},
		{
			name: "untracked count changed",
			base: GitState{
				Branch:         "main",
				Commit:         "abc123",
				UntrackedCount: 0,
			},
			current: GitState{
				Branch:         "main",
				Commit:         "abc123",
				UntrackedCount: 2,
			},
			wantChange: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ic.computeGitChange(tt.base, tt.current)

			if tt.wantChange && got == nil {
				t.Error("computeGitChange() returned nil, want change")
			}

			if !tt.wantChange && got != nil {
				t.Error("computeGitChange() returned change, want nil")
			}

			if got != nil && !equalStringPointer(got.Branch, tt.wantBranch) {
				t.Errorf("computeGitChange().Branch = %v, want %v", got.Branch, tt.wantBranch)
			}
		})
	}
}

func TestIncrementalCreator_computeGitChangeForCheckpoints_DetectsArtifactOnlyChanges(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	ic := NewIncrementalCreatorWithStorage(storage)
	sessionName := "artifact-only"

	base := &Checkpoint{
		ID:          "base",
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session:     SessionState{Panes: []PaneState{}},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123",
			IsDirty:        true,
			StagedCount:    1,
			UnstagedCount:  0,
			UntrackedCount: 0,
		},
	}
	current := &Checkpoint{
		ID:          "current",
		Name:        "current",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123",
			IsDirty:        true,
			StagedCount:    1,
			UnstagedCount:  0,
			UntrackedCount: 0,
		},
	}

	for _, cp := range []*Checkpoint{base, current} {
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save(%s) failed: %v", cp.ID, err)
		}
	}

	if err := storage.SaveGitPatch(sessionName, base.ID, "diff --git a/base.txt b/base.txt\n"); err != nil {
		t.Fatalf("SaveGitPatch(base) failed: %v", err)
	}
	if err := storage.SaveGitPatch(sessionName, current.ID, "diff --git a/current.txt b/current.txt\n"); err != nil {
		t.Fatalf("SaveGitPatch(current) failed: %v", err)
	}
	if err := storage.SaveGitStatus(sessionName, base.ID, "M  base.txt\n"); err != nil {
		t.Fatalf("SaveGitStatus(base) failed: %v", err)
	}
	if err := storage.SaveGitStatus(sessionName, current.ID, "M  current.txt\n"); err != nil {
		t.Fatalf("SaveGitStatus(current) failed: %v", err)
	}
	base.Git.PatchFile = GitPatchFile
	base.Git.StatusFile = GitStatusFile
	current.Git.PatchFile = GitPatchFile
	current.Git.StatusFile = GitStatusFile
	for _, cp := range []*Checkpoint{base, current} {
		if err := storage.Save(cp); err != nil {
			t.Fatalf("resave %s failed: %v", cp.ID, err)
		}
	}

	change, err := ic.computeGitChangeForCheckpoints(sessionName, base, current)
	if err != nil {
		t.Fatalf("computeGitChangeForCheckpoints() error = %v", err)
	}
	if change == nil {
		t.Fatal("computeGitChangeForCheckpoints() returned nil, want change for artifact drift")
	}
	if change.FromCommit != "abc123" || change.ToCommit != "abc123" {
		t.Fatalf("computeGitChangeForCheckpoints() commits = %q -> %q, want abc123 -> abc123", change.FromCommit, change.ToCommit)
	}
	if change.Branch != nil {
		t.Fatalf("computeGitChangeForCheckpoints().Branch = %v, want nil when only artifacts changed", change.Branch)
	}
}

func TestIncrementalCreatorCreate_UsesConfiguredStorageForCapturedDiffs(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	storage := NewStorageWithDir(t.TempDir())
	capturer := NewCapturerWithStorage(storage)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "inc-storage-" + time.Now().Format("150405000000")
	workDir := t.TempDir()

	if err := tmux.CreateSession(sessionName, workDir); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		t.Fatalf("GetPanes failed: %v", err)
	}
	if len(panes) != 1 {
		t.Fatalf("len(panes) = %d, want 1", len(panes))
	}
	paneID := panes[0].ID

	if err := tmux.SendKeys(paneID, "echo BASE_INCREMENTAL_MARKER", true); err != nil {
		t.Fatalf("SendKeys(base) failed: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	base, err := capturer.Create(sessionName, "base")
	if err != nil {
		t.Fatalf("capturer.Create(base) failed: %v", err)
	}

	if err := tmux.SendKeys(paneID, "echo CURRENT_INCREMENTAL_MARKER", true); err != nil {
		t.Fatalf("SendKeys(current) failed: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	inc, err := creator.Create(sessionName, "inc", base.ID)
	if err != nil {
		t.Fatalf("creator.Create() failed: %v", err)
	}

	change, ok := inc.Changes.PaneChanges[paneID]
	if !ok {
		t.Fatalf("Create() missing pane diff for %s; pane changes = %#v", paneID, inc.Changes.PaneChanges)
	}
	if change.NewLines == 0 {
		t.Fatalf("Create() pane diff NewLines = 0, want captured new output")
	}
	if change.DiffFile == "" {
		t.Fatalf("Create() pane diff file = %q, want saved diff path", change.DiffFile)
	}
}

func TestIncrementalCreator_computeGitChangeForCheckpoints_UsesRecordedGitArtifactPaths(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	ic := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base-custom-git",
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session:     SessionState{Panes: []PaneState{}},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123",
			IsDirty:        true,
			StagedCount:    1,
			UnstagedCount:  0,
			UntrackedCount: 0,
		},
	}
	current := &Checkpoint{
		ID:          "current-custom-git",
		Name:        "current",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Session:     SessionState{Panes: []PaneState{}},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123",
			IsDirty:        true,
			StagedCount:    1,
			UnstagedCount:  0,
			UntrackedCount: 0,
		},
	}

	for _, cp := range []*Checkpoint{base, current} {
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save(%s) failed: %v", cp.ID, err)
		}
	}

	base.Git.PatchFile = "git/base.patch"
	base.Git.StatusFile = "git/base.status"
	current.Git.PatchFile = "git/current.patch"
	current.Git.StatusFile = "git/current.status"

	for _, cp := range []*Checkpoint{base, current} {
		cpDir := storage.CheckpointDir(sessionName, cp.ID)
		if err := os.MkdirAll(filepath.Join(cpDir, "git"), 0755); err != nil {
			t.Fatalf("MkdirAll(%s) failed: %v", cp.ID, err)
		}
	}
	if err := os.WriteFile(filepath.Join(storage.CheckpointDir(sessionName, base.ID), base.Git.PatchFile), []byte("diff --git a/base.txt b/base.txt\n"), 0600); err != nil {
		t.Fatalf("WriteFile(base patch) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storage.CheckpointDir(sessionName, current.ID), current.Git.PatchFile), []byte("diff --git a/current.txt b/current.txt\n"), 0600); err != nil {
		t.Fatalf("WriteFile(current patch) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storage.CheckpointDir(sessionName, base.ID), base.Git.StatusFile), []byte("M  base.txt\n"), 0600); err != nil {
		t.Fatalf("WriteFile(base status) failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storage.CheckpointDir(sessionName, current.ID), current.Git.StatusFile), []byte("M  current.txt\n"), 0600); err != nil {
		t.Fatalf("WriteFile(current status) failed: %v", err)
	}
	for _, cp := range []*Checkpoint{base, current} {
		if err := storage.Save(cp); err != nil {
			t.Fatalf("Save(%s) failed: %v", cp.ID, err)
		}
	}

	change, err := ic.computeGitChangeForCheckpoints(sessionName, base, current)
	if err != nil {
		t.Fatalf("computeGitChangeForCheckpoints() error = %v", err)
	}
	if change == nil {
		t.Fatal("computeGitChangeForCheckpoints() returned nil, want change for recorded git artifact drift")
	}
	if change.FromCommit != "abc123" || change.ToCommit != "abc123" {
		t.Fatalf("computeGitChangeForCheckpoints() commits = %q -> %q, want abc123 -> abc123", change.FromCommit, change.ToCommit)
	}
}

func TestIncrementalCreator_computeSessionChange(t *testing.T) {
	ic := NewIncrementalCreator()
	stringPtr := func(value string) *string { return &value }
	intPtr := func(value int) *int { return &value }

	tests := []struct {
		name                string
		base                SessionState
		current             SessionState
		wantChange          bool
		wantLayout          *string
		wantWindowLayouts   []WindowLayoutState
		wantActivePaneIndex *int
		wantActivePaneID    *string
		wantPaneCount       *int
	}{
		{
			name: "no changes",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
				},
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
				},
			},
			wantChange: false,
		},
		{
			name: "layout changed",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
			},
			current: SessionState{
				Layout:          "tiled",
				ActivePaneIndex: 0,
			},
			wantChange:        true,
			wantLayout:        stringPtr("tiled"),
			wantWindowLayouts: []WindowLayoutState{{WindowIndex: 0, Layout: "tiled"}},
		},
		{
			name: "per-window layouts changed",
			base: SessionState{
				Panes: []PaneState{
					{ID: "%0", WindowIndex: 0},
					{ID: "%1", WindowIndex: 1},
				},
				WindowLayouts: []WindowLayoutState{
					{WindowIndex: 0, Layout: "even-horizontal"},
					{WindowIndex: 1, Layout: "main-horizontal"},
				},
			},
			current: SessionState{
				Panes: []PaneState{
					{ID: "%0", WindowIndex: 0},
					{ID: "%1", WindowIndex: 1},
				},
				WindowLayouts: []WindowLayoutState{
					{WindowIndex: 0, Layout: "even-horizontal"},
					{WindowIndex: 1, Layout: "main-vertical"},
				},
			},
			wantChange: true,
			wantWindowLayouts: []WindowLayoutState{
				{WindowIndex: 0, Layout: "even-horizontal"},
				{WindowIndex: 1, Layout: "main-vertical"},
			},
		},
		{
			name: "active pane changed",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
				},
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 1,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
				},
			},
			wantChange:          true,
			wantActivePaneIndex: intPtr(1),
			wantActivePaneID:    stringPtr("%1"),
		},
		{
			name: "active pane changed to zero",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 2,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
					{ID: "%2"},
				},
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
					{ID: "%2"},
				},
			},
			wantChange:          true,
			wantActivePaneIndex: intPtr(0),
			wantActivePaneID:    stringPtr("%0"),
		},
		{
			name: "active pane unchanged despite reordered pane slice",
			base: SessionState{
				Layout: "main",
				Panes: []PaneState{
					{ID: "%1"},
					{ID: "%0"},
				},
				ActivePaneIndex: 0,
			},
			current: SessionState{
				Layout: "main",
				Panes: []PaneState{
					{ID: "%0"},
					{ID: "%1"},
				},
				ActivePaneIndex: 1,
			},
			wantChange: false,
		},
		{
			name: "layout changed to empty string",
			base: SessionState{
				Layout:          "even-horizontal",
				ActivePaneIndex: 0,
			},
			current: SessionState{
				Layout:          "",
				ActivePaneIndex: 0,
			},
			wantChange: true,
			wantLayout: stringPtr(""),
		},
		{
			name: "pane count changed",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes:           make([]PaneState, 2),
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes:           make([]PaneState, 3),
			},
			wantChange:    true,
			wantPaneCount: intPtr(3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ic.computeSessionChange(tt.base, tt.current)

			if tt.wantChange && got == nil {
				t.Error("computeSessionChange() returned nil, want change")
			}

			if !tt.wantChange && got != nil {
				t.Error("computeSessionChange() returned change, want nil")
			}

			if got == nil {
				return
			}

			if !equalStringPointer(got.Layout, tt.wantLayout) {
				t.Errorf("computeSessionChange().Layout = %v, want %v", got.Layout, tt.wantLayout)
			}
			if !windowLayoutsEqual(got.WindowLayouts, tt.wantWindowLayouts) {
				t.Errorf("computeSessionChange().WindowLayouts = %#v, want %#v", got.WindowLayouts, tt.wantWindowLayouts)
			}

			if !equalIntPointer(got.ActivePaneIndex, tt.wantActivePaneIndex) {
				t.Errorf("computeSessionChange().ActivePaneIndex = %v, want %v", got.ActivePaneIndex, tt.wantActivePaneIndex)
			}

			if !equalStringPointer(got.ActivePaneID, tt.wantActivePaneID) {
				t.Errorf("computeSessionChange().ActivePaneID = %v, want %v", got.ActivePaneID, tt.wantActivePaneID)
			}

			if !equalIntPointer(got.PaneCount, tt.wantPaneCount) {
				t.Errorf("computeSessionChange().PaneCount = %v, want %v", got.PaneCount, tt.wantPaneCount)
			}
		})
	}
}

func TestIncrementalResolverResolve_PreservesZeroValueSessionChanges(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"
	empty := ""

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Layout:          "even-horizontal",
			ActivePaneIndex: 2,
			Panes: []PaneState{
				{ID: "%0", AgentType: "cc", Title: "Claude"},
				{ID: "%1"},
				{ID: "%2"},
			},
		},
		PaneCount: 3,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	layout := ""
	activePaneIndex := 0
	activePaneID := "%0"
	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-zero-session-change",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					AgentType: &empty,
					Title:     &empty,
				},
			},
			SessionChange: &SessionChange{
				Layout:          &layout,
				ActivePaneIndex: &activePaneIndex,
				ActivePaneID:    &activePaneID,
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Session.Layout != "" {
		t.Errorf("Resolve().Session.Layout = %q, want empty string", resolved.Session.Layout)
	}
	if resolved.Session.WindowLayouts != nil {
		t.Errorf("Resolve().Session.WindowLayouts = %#v, want nil", resolved.Session.WindowLayouts)
	}
	if resolved.Session.ActivePaneIndex != 0 {
		t.Errorf("Resolve().Session.ActivePaneIndex = %d, want 0", resolved.Session.ActivePaneIndex)
	}
	if resolved.Session.Panes[0].AgentType != "" {
		t.Errorf("Resolve().Session.Panes[0].AgentType = %q, want empty string", resolved.Session.Panes[0].AgentType)
	}
	if resolved.Session.Panes[0].Title != "" {
		t.Errorf("Resolve().Session.Panes[0].Title = %q, want empty string", resolved.Session.Panes[0].Title)
	}
}

func TestIncrementalResolverApplyIncremental_PreservesZeroValueSessionChanges(t *testing.T) {
	layout := ""
	activePaneIndex := 0
	activePaneID := "%0"

	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Layout:          "main-vertical",
			ActivePaneIndex: 1,
			Panes: []PaneState{
				{ID: "%0", AgentType: "cod", Title: "Codex"},
				{ID: "%1"},
			},
		},
		PaneCount: 2,
	}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-apply-zero-session-change",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					AgentType: &layout,
					Title:     &layout,
				},
			},
			SessionChange: &SessionChange{
				Layout:          &layout,
				ActivePaneIndex: &activePaneIndex,
				ActivePaneID:    &activePaneID,
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Session.Layout != "" {
		t.Errorf("applyIncremental().Session.Layout = %q, want empty string", resolved.Session.Layout)
	}
	if resolved.Session.WindowLayouts != nil {
		t.Errorf("applyIncremental().Session.WindowLayouts = %#v, want nil", resolved.Session.WindowLayouts)
	}
	if resolved.Session.ActivePaneIndex != 0 {
		t.Errorf("applyIncremental().Session.ActivePaneIndex = %d, want 0", resolved.Session.ActivePaneIndex)
	}
	if resolved.Session.Panes[0].AgentType != "" {
		t.Errorf("applyIncremental().Session.Panes[0].AgentType = %q, want empty string", resolved.Session.Panes[0].AgentType)
	}
	if resolved.Session.Panes[0].Title != "" {
		t.Errorf("applyIncremental().Session.Panes[0].Title = %q, want empty string", resolved.Session.Panes[0].Title)
	}
}

func TestIncrementalResolverApplyIncremental_PreservesPerWindowLayouts(t *testing.T) {
	layout := ""
	windowLayouts := []WindowLayoutState{
		{WindowIndex: 0, Layout: "even-horizontal"},
		{WindowIndex: 2, Layout: "main-vertical"},
	}

	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base-layouts"),
		Name:      "base-layouts",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Layout: "main-horizontal",
			Panes: []PaneState{
				{ID: "%0", WindowIndex: 0},
				{ID: "%1", WindowIndex: 2},
			},
		},
		PaneCount: 2,
	}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-window-layouts",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			SessionChange: &SessionChange{
				Layout:        &layout,
				WindowLayouts: windowLayouts,
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Session.Layout != "" {
		t.Fatalf("applyIncremental().Session.Layout = %q, want empty string", resolved.Session.Layout)
	}
	if !windowLayoutsEqual(resolved.Session.WindowLayouts, windowLayouts) {
		t.Fatalf("applyIncremental().Session.WindowLayouts = %#v, want %#v", resolved.Session.WindowLayouts, windowLayouts)
	}
	if base.Session.Layout != "main-horizontal" {
		t.Fatalf("base.Session.Layout mutated to %q", base.Session.Layout)
	}
	if base.Session.WindowLayouts != nil {
		t.Fatalf("base.Session.WindowLayouts mutated to %#v", base.Session.WindowLayouts)
	}
}

func TestIncrementalCreatorComputePaneChanges_PreservesEmptyStringFields(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", AgentType: "cc", Title: "Claude", Command: "claude"},
			},
		},
	}
	current := &Checkpoint{
		ID:          "current",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", AgentType: "", Title: "", Command: ""},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	if err := storage.Save(current); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, base.ID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback(base) error = %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, current.ID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback(current) error = %v", err)
	}

	changes, err := creator.computePaneChanges(sessionName, base, current)
	if err != nil {
		t.Fatalf("computePaneChanges() error = %v", err)
	}

	change, ok := changes["%0"]
	if !ok {
		t.Fatal("computePaneChanges() missing pane change for %0")
	}
	if !equalStringPointer(change.AgentType, &current.Session.Panes[0].AgentType) {
		t.Errorf("computePaneChanges().AgentType = %v, want empty string pointer", change.AgentType)
	}
	if !equalStringPointer(change.Title, &current.Session.Panes[0].Title) {
		t.Errorf("computePaneChanges().Title = %v, want empty string pointer", change.Title)
	}
	if !equalStringPointer(change.Command, &current.Session.Panes[0].Command) {
		t.Errorf("computePaneChanges().Command = %v, want empty string pointer", change.Command)
	}
}

func TestIncrementalCreatorComputePaneChanges_PreservesAddedPaneMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base",
		SessionName: sessionName,
		Session:     SessionState{Panes: nil},
	}
	current := &Checkpoint{
		ID:          "current",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{
					ID:              "%9",
					Index:           1,
					WindowIndex:     2,
					Title:           "Cursor",
					AgentType:       "cursor",
					Command:         "cursor-agent",
					Width:           120,
					Height:          40,
					ScrollbackLines: 3,
				},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	if err := storage.Save(current); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, current.ID, "%9", "line1\nline2\nline3"); err != nil {
		t.Fatalf("SaveScrollback(current) error = %v", err)
	}

	changes, err := creator.computePaneChanges(sessionName, base, current)
	if err != nil {
		t.Fatalf("computePaneChanges() error = %v", err)
	}

	change, ok := changes["%9"]
	if !ok {
		t.Fatal("computePaneChanges() missing added pane change for %9")
	}
	if !change.Added {
		t.Fatal("computePaneChanges() change.Added = false, want true")
	}
	if change.Pane == nil {
		t.Fatal("computePaneChanges() change.Pane = nil, want pane metadata")
	}
	if change.Pane.Title != "Cursor" || change.Pane.AgentType != "cursor" || change.Pane.Command != "cursor-agent" {
		t.Errorf("computePaneChanges() pane metadata = %+v, want title/agent/command preserved", *change.Pane)
	}
	if change.Pane.Index != 1 || change.Pane.WindowIndex != 2 || change.Pane.Width != 120 || change.Pane.Height != 40 {
		t.Errorf("computePaneChanges() pane geometry = %+v, want index/window/size preserved", *change.Pane)
	}
	if change.Pane.ScrollbackFile != "" {
		t.Errorf("computePaneChanges() change.Pane.ScrollbackFile = %q, want empty string", change.Pane.ScrollbackFile)
	}
	if change.NewLines != 3 {
		t.Errorf("computePaneChanges() change.NewLines = %d, want 3", change.NewLines)
	}
}

func TestIncrementalCreatorComputePaneChanges_PreservesModifiedPaneMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{
					ID:              "%0",
					Index:           0,
					WindowIndex:     0,
					Title:           "Claude",
					AgentType:       "cc",
					Command:         "claude",
					Width:           80,
					Height:          24,
					ScrollbackLines: 2,
				},
			},
		},
	}
	current := &Checkpoint{
		ID:          "current",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{
					ID:              "%0",
					Index:           1,
					WindowIndex:     2,
					Title:           "Codex",
					AgentType:       "cod",
					Command:         "codex",
					Width:           120,
					Height:          40,
					ScrollbackLines: 2,
				},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	if err := storage.Save(current); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, base.ID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback(base) error = %v", err)
	}
	if _, err := storage.SaveScrollback(sessionName, current.ID, "%0", "line1\nline2"); err != nil {
		t.Fatalf("SaveScrollback(current) error = %v", err)
	}

	changes, err := creator.computePaneChanges(sessionName, base, current)
	if err != nil {
		t.Fatalf("computePaneChanges() error = %v", err)
	}

	change, ok := changes["%0"]
	if !ok {
		t.Fatal("computePaneChanges() missing pane change for %0")
	}
	if change.Pane == nil {
		t.Fatal("computePaneChanges() change.Pane = nil, want updated pane metadata")
	}
	if change.Pane.Index != 1 || change.Pane.WindowIndex != 2 || change.Pane.Width != 120 || change.Pane.Height != 40 {
		t.Errorf("computePaneChanges() pane geometry = %+v, want updated index/window/size", *change.Pane)
	}
	if change.Pane.Title != "Codex" || change.Pane.AgentType != "cod" || change.Pane.Command != "codex" {
		t.Errorf("computePaneChanges() pane metadata = %+v, want updated title/agent/command", *change.Pane)
	}
	if change.Pane.ScrollbackFile != "" {
		t.Errorf("computePaneChanges() change.Pane.ScrollbackFile = %q, want empty string", change.Pane.ScrollbackFile)
	}
}

func TestIncrementalCreatorComputePaneChanges_AllowsMissingScrollbackWhenPaneHasNoArtifact(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Claude"},
			},
		},
	}
	current := &Checkpoint{
		ID:          "current",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Codex"},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	if err := storage.Save(current); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}

	changes, err := creator.computePaneChanges(sessionName, base, current)
	if err != nil {
		t.Fatalf("computePaneChanges() error = %v", err)
	}

	change, ok := changes["%0"]
	if !ok {
		t.Fatal("computePaneChanges() missing pane change for %0")
	}
	if change.Title == nil || *change.Title != "Codex" {
		t.Fatalf("computePaneChanges().Title = %v, want Codex", change.Title)
	}
}

func TestIncrementalCreatorComputePaneChanges_ErrorsOnMissingReferencedScrollback(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	creator := NewIncrementalCreatorWithStorage(storage)
	sessionName := "test-session"

	base := &Checkpoint{
		ID:          "base",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Claude", ScrollbackLines: 2},
			},
		},
	}
	current := &Checkpoint{
		ID:          "current",
		SessionName: sessionName,
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Codex", ScrollbackLines: 2},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	if err := storage.Save(current); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}
	base.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"
	current.Session.Panes[0].ScrollbackFile = "panes/pane__0.txt"

	_, err := creator.computePaneChanges(sessionName, base, current)
	if err == nil {
		t.Fatal("computePaneChanges() error = nil, want missing scrollback error")
	}
	if !strings.Contains(err.Error(), "loading base scrollback for pane %0") {
		t.Fatalf("computePaneChanges() error = %v, want base scrollback context", err)
	}
}

func TestIncrementalResolverApplyIncremental_DoesNotMutateBasePanes(t *testing.T) {
	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Before", AgentType: "cc", Command: "claude"},
			},
		},
		PaneCount: 1,
	}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-no-base-mutation",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					Title:     stringPointer("After"),
					AgentType: stringPointer("cod"),
					Command:   stringPointer("codex"),
				},
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Session.Panes[0].Title != "After" || resolved.Session.Panes[0].AgentType != "cod" || resolved.Session.Panes[0].Command != "codex" {
		t.Errorf("applyIncremental() resolved pane = %+v, want updated title/agent/command", resolved.Session.Panes[0])
	}
	if base.Session.Panes[0].Title != "Before" || base.Session.Panes[0].AgentType != "cc" || base.Session.Panes[0].Command != "claude" {
		t.Errorf("applyIncremental() mutated base pane = %+v, want original values preserved", base.Session.Panes[0])
	}
}

func TestIncrementalResolverApplyIncremental_ClearsStaleScrollbackFileWhenDiffApplied(t *testing.T) {
	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Title: "Before", ScrollbackFile: "panes/base.txt", ScrollbackLines: 10},
			},
		},
		PaneCount: 1,
	}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-clear-scrollback-file",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					NewLines: 3,
					DiffFile: filepath.Join(DiffPanesDir, "pane__0_diff.txt.gz"),
				},
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Session.Panes[0].ScrollbackLines != 13 {
		t.Fatalf("applyIncremental().Session.Panes[0].ScrollbackLines = %d, want 13", resolved.Session.Panes[0].ScrollbackLines)
	}
	if resolved.Session.Panes[0].ScrollbackFile != "" {
		t.Fatalf("applyIncremental().Session.Panes[0].ScrollbackFile = %q, want empty string", resolved.Session.Panes[0].ScrollbackFile)
	}
	if base.Session.Panes[0].ScrollbackFile != "panes/base.txt" {
		t.Fatalf("applyIncremental() mutated base scrollback file to %q, want base path preserved", base.Session.Panes[0].ScrollbackFile)
	}
}

func TestIncrementalResolverResolve_PreservesAddedPaneMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Title: "Claude", AgentType: "cc"}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	addedPane := PaneState{
		ID:              "%9",
		Index:           1,
		WindowIndex:     2,
		Title:           "Cursor",
		AgentType:       "cursor",
		Command:         "cursor-agent",
		Width:           120,
		Height:          40,
		ScrollbackFile:  "panes/stale.txt",
		ScrollbackLines: 99,
	}
	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-added-pane-metadata",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%9": {
					Added:    true,
					NewLines: 7,
					Pane:     &addedPane,
				},
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	var restoredAddedPane *PaneState
	for i := range resolved.Session.Panes {
		if resolved.Session.Panes[i].ID == "%9" {
			restoredAddedPane = &resolved.Session.Panes[i]
			break
		}
	}
	if restoredAddedPane == nil {
		t.Fatal("Resolve() missing added pane %9")
	}
	if restoredAddedPane.Title != "Cursor" || restoredAddedPane.AgentType != "cursor" || restoredAddedPane.Command != "cursor-agent" {
		t.Errorf("Resolve() added pane metadata = %+v, want title/agent/command preserved", *restoredAddedPane)
	}
	if restoredAddedPane.Index != 1 || restoredAddedPane.WindowIndex != 2 || restoredAddedPane.Width != 120 || restoredAddedPane.Height != 40 {
		t.Errorf("Resolve() added pane geometry = %+v, want index/window/size preserved", *restoredAddedPane)
	}
	if restoredAddedPane.ScrollbackLines != 7 {
		t.Errorf("Resolve() added pane ScrollbackLines = %d, want 7", restoredAddedPane.ScrollbackLines)
	}
	if restoredAddedPane.ScrollbackFile != "" {
		t.Errorf("Resolve() added pane ScrollbackFile = %q, want empty string", restoredAddedPane.ScrollbackFile)
	}
}

func TestIncrementalResolverResolve_PreservesCommandChanges(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Title: "Claude", AgentType: "cc", Command: "claude"}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-command-change",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					Command: stringPointer("codex"),
				},
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Session.Panes[0].Command != "codex" {
		t.Errorf("Resolve().Session.Panes[0].Command = %q, want codex", resolved.Session.Panes[0].Command)
	}
}

func TestIncrementalResolverResolve_ClearsStaleScrollbackFileWhenDiffApplied(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{
				ID:              "%0",
				Title:           "Claude",
				ScrollbackLines: 4,
			}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}
	compressed, err := gzipCompress([]byte("base line 1\nbase line 2\nbase line 3\nbase line 4"))
	if err != nil {
		t.Fatalf("gzipCompress() error = %v", err)
	}
	scrollbackPath, err := storage.SaveCompressedScrollback(sessionName, base.ID, "%0", compressed)
	if err != nil {
		t.Fatalf("SaveCompressedScrollback(base) error = %v", err)
	}
	base.Session.Panes[0].ScrollbackFile = scrollbackPath
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base with scrollback) error = %v", err)
	}

	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-scrollback-diff",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					NewLines: 2,
					DiffFile: filepath.Join(DiffPanesDir, "pane__0_diff.txt.gz"),
				},
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(filepath.Join(incDir, DiffPanesDir), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Session.Panes[0].ScrollbackLines != 6 {
		t.Fatalf("Resolve().Session.Panes[0].ScrollbackLines = %d, want 6", resolved.Session.Panes[0].ScrollbackLines)
	}
	if resolved.Session.Panes[0].ScrollbackFile != "" {
		t.Fatalf("Resolve().Session.Panes[0].ScrollbackFile = %q, want empty string", resolved.Session.Panes[0].ScrollbackFile)
	}
}

func TestIncrementalResolverResolve_PreservesModifiedPaneGeometry(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{
				ID:          "%0",
				Index:       0,
				WindowIndex: 0,
				Title:       "Claude",
				AgentType:   "cc",
				Command:     "claude",
				Width:       80,
				Height:      24,
			}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	modifiedPane := PaneState{
		ID:          "%0",
		Index:       1,
		WindowIndex: 2,
		Title:       "Codex",
		AgentType:   "cod",
		Command:     "codex",
		Width:       120,
		Height:      40,
	}
	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-pane-geometry",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"%0": {
					Pane:    &modifiedPane,
					Title:   stringPointer("Codex"),
					Command: stringPointer("codex"),
				},
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	pane := resolved.Session.Panes[0]
	if pane.Index != 1 || pane.WindowIndex != 2 || pane.Width != 120 || pane.Height != 40 {
		t.Errorf("Resolve() pane geometry = %+v, want updated index/window/size", pane)
	}
	if pane.Title != "Codex" || pane.AgentType != "cod" || pane.Command != "codex" {
		t.Errorf("Resolve() pane metadata = %+v, want updated title/agent/command", pane)
	}
}

func TestIncrementalResolverResolve_NormalizesPaneOrderAndRemapsActivePane(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%1", Index: 1, WindowIndex: 0, Title: "second"},
				{ID: "%0", Index: 0, WindowIndex: 0, Title: "first"},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 2,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-normalize-pane-order",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes:          IncrementalChanges{},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got := []string{resolved.Session.Panes[0].ID, resolved.Session.Panes[1].ID}; got[0] != "%0" || got[1] != "%1" {
		t.Errorf("Resolve() pane order = %v, want [%%0 %%1]", got)
	}
	if resolved.Session.ActivePaneIndex != 1 {
		t.Errorf("Resolve().Session.ActivePaneIndex = %d, want 1", resolved.Session.ActivePaneIndex)
	}
}

func TestIncrementalResolverApplyIncremental_SortsAddedPanesBeforeApplyingExplicitActiveIndex(t *testing.T) {
	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%2", Index: 2, WindowIndex: 0},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 1,
	}
	activePaneIndex := 1
	activePaneID := "%1"
	firstPane := PaneState{ID: "%0", Index: 0, WindowIndex: 0, Title: "first"}
	secondPane := PaneState{ID: "%1", Index: 1, WindowIndex: 0, Title: "second"}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-sort-added-panes",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			SessionChange: &SessionChange{
				ActivePaneIndex: &activePaneIndex,
				ActivePaneID:    &activePaneID,
			},
			PaneChanges: map[string]PaneChange{
				"%1": {
					Added:    true,
					NewLines: 1,
					Pane:     &secondPane,
				},
				"%0": {
					Added:    true,
					NewLines: 1,
					Pane:     &firstPane,
				},
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	gotOrder := []string{resolved.Session.Panes[0].ID, resolved.Session.Panes[1].ID, resolved.Session.Panes[2].ID}
	wantOrder := []string{"%0", "%1", "%2"}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("applyIncremental() pane order = %v, want %v", gotOrder, wantOrder)
		}
	}
	if resolved.Session.ActivePaneIndex != 1 {
		t.Errorf("applyIncremental().Session.ActivePaneIndex = %d, want 1", resolved.Session.ActivePaneIndex)
	}
}

func TestIncrementalResolverResolve_UsesActivePaneIDFromUnsortedCaptureOrder(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          GenerateID("base"),
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "%0", Index: 0, WindowIndex: 0},
				{ID: "%1", Index: 1, WindowIndex: 0},
			},
			ActivePaneIndex: 0,
		},
		PaneCount: 2,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) error = %v", err)
	}

	activePaneIndex := 0
	activePaneID := "%1"
	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "inc-active-pane-id-precedence",
		SessionName:      sessionName,
		BaseCheckpointID: base.ID,
		BaseTimestamp:    base.CreatedAt,
		CreatedAt:        time.Now(),
		Changes: IncrementalChanges{
			SessionChange: &SessionChange{
				ActivePaneIndex: &activePaneIndex,
				ActivePaneID:    &activePaneID,
			},
		},
	}

	incDir := filepath.Join(tmpDir, sessionName, "incremental", inc.ID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(inc)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), data, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	resolver := NewIncrementalResolverWithStorage(storage)
	resolved, err := resolver.Resolve(sessionName, inc.ID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if resolved.Session.ActivePaneIndex != 1 {
		t.Fatalf("Resolve().Session.ActivePaneIndex = %d, want 1 for pane %%1", resolved.Session.ActivePaneIndex)
	}
}

func TestIncrementalResolverApplyIncremental_PreservesEmptyGitBranch(t *testing.T) {
	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0"}},
		},
		Git: GitState{
			Branch: "main",
			Commit: "abc123",
		},
		PaneCount: 1,
	}
	emptyBranch := ""
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-empty-git-branch",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			GitChange: &GitChange{
				FromCommit: "abc123",
				ToCommit:   "abc123",
				Branch:     &emptyBranch,
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Git.Branch != "" {
		t.Fatalf("applyIncremental().Git.Branch = %q, want empty string", resolved.Git.Branch)
	}
}

func TestIncrementalResolverApplyIncremental_ClearsStaleGitArtifactPaths(t *testing.T) {
	base := &Checkpoint{
		Version:   2,
		ID:        GenerateID("base"),
		Name:      "base",
		CreatedAt: time.Now().Add(-time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0"}},
		},
		Git: GitState{
			Branch:         "main",
			Commit:         "abc123",
			IsDirty:        true,
			StagedCount:    1,
			UnstagedCount:  0,
			UntrackedCount: 0,
			StatusFile:     GitStatusFile,
			PatchFile:      GitPatchFile,
		},
		PaneCount: 1,
	}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-artifacts-clear",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			GitChange: &GitChange{
				FromCommit:     "abc123",
				ToCommit:       "abc123",
				IsDirty:        true,
				StagedCount:    1,
				UnstagedCount:  0,
				UntrackedCount: 0,
			},
		},
	}

	resolver := NewIncrementalResolver()
	resolved, err := resolver.applyIncremental(base, inc)
	if err != nil {
		t.Fatalf("applyIncremental() error = %v", err)
	}

	if resolved.Git.PatchFile != "" {
		t.Fatalf("applyIncremental().Git.PatchFile = %q, want empty string", resolved.Git.PatchFile)
	}
	if resolved.Git.StatusFile != "" {
		t.Fatalf("applyIncremental().Git.StatusFile = %q, want empty string", resolved.Git.StatusFile)
	}
	if base.Git.PatchFile != GitPatchFile || base.Git.StatusFile != GitStatusFile {
		t.Fatalf("applyIncremental() mutated base git artifacts to patch=%q status=%q", base.Git.PatchFile, base.Git.StatusFile)
	}
}

func equalStringPointer(got, want *string) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func equalIntPointer(got, want *int) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func stringPointer(value string) *string {
	return &value
}

func TestRemovePaneByID(t *testing.T) {
	panes := []PaneState{
		{ID: "pane1", Title: "Pane 1"},
		{ID: "pane2", Title: "Pane 2"},
		{ID: "pane3", Title: "Pane 3"},
	}

	// Remove middle pane
	result := removePaneByID(panes, "pane2")

	if len(result) != 2 {
		t.Errorf("removePaneByID() len = %d, want 2", len(result))
	}

	for _, p := range result {
		if p.ID == "pane2" {
			t.Error("removePaneByID() failed to remove pane2")
		}
	}

	// Remove non-existent pane
	result = removePaneByID(panes, "pane99")
	if len(result) != 3 {
		t.Errorf("removePaneByID() len = %d, want 3 (no change)", len(result))
	}
}

func TestIncrementalCheckpoint_StorageSavings(t *testing.T) {
	// Create a temporary storage directory
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create a mock base checkpoint
	baseID := GenerateID("test-base")
	base := &Checkpoint{
		Version:     2,
		ID:          baseID,
		Name:        "test-base",
		SessionName: "test-session",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "pane1", ScrollbackLines: 5},
			},
		},
	}

	// Save the base checkpoint
	if err := storage.Save(base); err != nil {
		t.Fatalf("Failed to save base checkpoint: %v", err)
	}

	// Save some mock scrollback
	scrollbackRelPath, err := storage.SaveScrollback("test-session", baseID, "pane1", "line1\nline2\nline3\nline4\nline5")
	if err != nil {
		t.Fatalf("Failed to save scrollback: %v", err)
	}
	base.Session.Panes[0].ScrollbackFile = scrollbackRelPath
	if err := storage.Save(base); err != nil {
		t.Fatalf("Failed to resave base checkpoint: %v", err)
	}

	// Create an incremental checkpoint
	inc := &IncrementalCheckpoint{
		SessionName:      "test-session",
		BaseCheckpointID: baseID,
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"pane1": {NewLines: 2}, // Only 2 new lines
			},
		},
	}

	savedBytes, percentSaved, err := inc.StorageSavings(storage)
	if err != nil {
		t.Fatalf("StorageSavings() error = %v", err)
	}

	// We should have some savings since incremental has fewer lines
	if savedBytes <= 0 {
		t.Logf("StorageSavings() savedBytes = %d, percentSaved = %.2f%%", savedBytes, percentSaved)
	}
}

func TestIncrementalCheckpoint_StorageSavings_UsesRecordedScrollbackFile(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	longLine := strings.Repeat("x", 100)
	scrollback := strings.Join([]string{longLine, longLine, longLine, longLine, longLine}, "\n")

	baseID := GenerateID("test-base-custom")
	base := &Checkpoint{
		Version:     2,
		ID:          baseID,
		Name:        "test-base",
		SessionName: "test-session",
		CreatedAt:   time.Now(),
		Session: SessionState{
			Panes: []PaneState{
				{ID: "pane1", ScrollbackLines: 5},
			},
		},
	}

	if err := storage.Save(base); err != nil {
		t.Fatalf("Failed to save base checkpoint: %v", err)
	}

	cpDir := storage.CheckpointDir("test-session", baseID)
	if err := os.MkdirAll(filepath.Join(cpDir, PanesDir), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	base.Session.Panes[0].ScrollbackFile = filepath.Join(PanesDir, "custom-pane.txt")
	if err := os.WriteFile(filepath.Join(cpDir, base.Session.Panes[0].ScrollbackFile), []byte(scrollback), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Failed to resave base checkpoint: %v", err)
	}

	inc := &IncrementalCheckpoint{
		SessionName:      "test-session",
		BaseCheckpointID: baseID,
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{
				"pane1": {NewLines: 2},
			},
		},
	}

	savedBytes, _, err := inc.StorageSavings(storage)
	if err != nil {
		t.Fatalf("StorageSavings() error = %v", err)
	}
	if savedBytes <= 0 {
		t.Fatalf("StorageSavings() savedBytes = %d, want positive savings using recorded scrollback file", savedBytes)
	}
}

func TestIncrementalCreator_incrementalDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	ic := NewIncrementalCreatorWithStorage(storage)

	dir, err := ic.incrementalDir("my-session", "inc-123")
	if err != nil {
		t.Fatalf("incrementalDir() error = %v", err)
	}
	expected := filepath.Join(tmpDir, "my-session", "incremental", "inc-123")

	if dir != expected {
		t.Errorf("incrementalDir() = %q, want %q", dir, expected)
	}
}

func TestIncrementalCreator_save_RejectsSymlinkIncrementalBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	ic := NewIncrementalCreatorWithStorage(storage)

	sessionName := "test-session"
	sessionDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("MkdirAll(session dir) failed: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(sessionDir, "incremental")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	inc := &IncrementalCheckpoint{
		ID:          "inc-creator-symlink-base",
		SessionName: sessionName,
		CreatedAt:   time.Now(),
		Changes: IncrementalChanges{
			PaneChanges: map[string]PaneChange{},
		},
	}

	err := ic.save(inc, "")
	if err == nil {
		t.Fatal("save() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "incremental path must not be a symlink") {
		t.Fatalf("save() error = %v, want incremental base symlink rejection", err)
	}
}

func TestIncrementalResolver_loadIncremental(t *testing.T) {
	// Create a temporary storage directory
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	// Create incremental directory structure
	sessionName := "test-session"
	incID := "inc-test-123"
	incDir := filepath.Join(tmpDir, sessionName, "incremental", incID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental directory: %v", err)
	}

	// Write a mock incremental metadata file
	metadata := `{
		"version": 1,
		"id": "inc-test-123",
		"session_name": "test-session",
		"base_checkpoint_id": "base-123",
		"created_at": "2025-01-06T10:00:00Z",
		"changes": {}
	}`

	metaPath := filepath.Join(incDir, IncrementalMetadataFile)
	if err := os.WriteFile(metaPath, []byte(metadata), 0600); err != nil {
		t.Fatalf("Failed to write metadata: %v", err)
	}

	// Test loading
	ir := NewIncrementalResolverWithStorage(storage)
	inc, err := ir.loadIncremental(sessionName, incID)
	if err != nil {
		t.Fatalf("loadIncremental() error = %v", err)
	}

	if inc.ID != incID {
		t.Errorf("loadIncremental().ID = %q, want %q", inc.ID, incID)
	}

	if inc.BaseCheckpointID != "base-123" {
		t.Errorf("loadIncremental().BaseCheckpointID = %q, want %q", inc.BaseCheckpointID, "base-123")
	}
}

func TestIncrementalResolver_loadIncremental_RejectsSessionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	incID := "inc-test-123"
	incDir := filepath.Join(tmpDir, sessionName, "incremental", incID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental directory: %v", err)
	}

	metadata := `{
		"version": 1,
		"id": "inc-test-123",
		"session_name": "other-session",
		"base_checkpoint_id": "base-123",
		"created_at": "2025-01-06T10:00:00Z",
		"changes": {}
	}`

	metaPath := filepath.Join(incDir, IncrementalMetadataFile)
	if err := os.WriteFile(metaPath, []byte(metadata), 0600); err != nil {
		t.Fatalf("Failed to write metadata: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental(sessionName, incID)
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want session mismatch error")
	}
	if !strings.Contains(err.Error(), "incremental metadata session mismatch") {
		t.Fatalf("loadIncremental() error = %v, want session mismatch context", err)
	}
}

func TestIncrementalResolver_loadIncremental_RejectsUnsafeIncrementalID(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental("test-session", "../escape")
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want invalid checkpoint ID")
	}
	if !strings.Contains(err.Error(), "invalid checkpoint ID") {
		t.Fatalf("loadIncremental() error = %v, want invalid checkpoint ID context", err)
	}
}

func TestIncrementalResolver_loadIncremental_RejectsUnsafeBaseCheckpointID(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	incID := "inc-test-123"
	incDir := filepath.Join(tmpDir, sessionName, "incremental", incID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental directory: %v", err)
	}

	metadata := `{
		"version": 1,
		"id": "inc-test-123",
		"session_name": "test-session",
		"base_checkpoint_id": "../base-123",
		"created_at": "2025-01-06T10:00:00Z",
		"changes": {}
	}`

	metaPath := filepath.Join(incDir, IncrementalMetadataFile)
	if err := os.WriteFile(metaPath, []byte(metadata), 0600); err != nil {
		t.Fatalf("Failed to write metadata: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental(sessionName, incID)
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want invalid base checkpoint ID")
	}
	if !strings.Contains(err.Error(), "invalid incremental metadata base checkpoint ID") {
		t.Fatalf("loadIncremental() error = %v, want invalid base checkpoint ID context", err)
	}
}

func TestIncrementalResolver_loadIncremental_RejectsSymlinkMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	incID := "inc-test-symlink"
	incDir := filepath.Join(tmpDir, sessionName, "incremental", incID)
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental directory: %v", err)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside-incremental.json")
	metadata := `{
		"version": 1,
		"id": "inc-test-symlink",
		"session_name": "test-session",
		"base_checkpoint_id": "base-123",
		"created_at": "2025-01-06T10:00:00Z",
		"changes": {}
	}`
	if err := os.WriteFile(outsidePath, []byte(metadata), 0600); err != nil {
		t.Fatalf("Failed to write outside metadata: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(incDir, IncrementalMetadataFile)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental(sessionName, incID)
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("loadIncremental() error = %v, want symlink rejection", err)
	}
}

func TestIncrementalResolver_loadIncremental_RejectsSymlinkIncrementalDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	incID := "inc-test-symlink-dir"
	incBaseDir := filepath.Join(tmpDir, sessionName, "incremental")
	if err := os.MkdirAll(incBaseDir, 0755); err != nil {
		t.Fatalf("Failed to create incremental base directory: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(incBaseDir, incID)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental(sessionName, incID)
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "incremental path must not be a symlink") {
		t.Fatalf("loadIncremental() error = %v, want incremental dir symlink rejection", err)
	}
}

func TestIncrementalResolver_loadIncremental_RejectsSymlinkIncrementalBaseDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	sessionDir := filepath.Join(tmpDir, sessionName)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatalf("MkdirAll(session dir) failed: %v", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(sessionDir, "incremental")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.loadIncremental(sessionName, "inc-test-symlink-base")
	if err == nil {
		t.Fatal("loadIncremental() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "incremental path must not be a symlink") {
		t.Fatalf("loadIncremental() error = %v, want incremental base symlink rejection", err)
	}
}

func TestIncrementalResolver_ListIncrementals(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	sessionName := "test-session"
	incDir := filepath.Join(tmpDir, sessionName, "incremental")

	// Create two incremental checkpoints
	for i, id := range []string{"inc-001", "inc-002"} {
		dir := filepath.Join(incDir, id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}

		metadata := `{
			"version": 1,
			"id": "` + id + `",
			"session_name": "test-session",
			"base_checkpoint_id": "base-123",
			"created_at": "2025-01-0` + string(rune('6'+i)) + `T10:00:00Z",
			"changes": {}
		}`

		if err := os.WriteFile(filepath.Join(dir, IncrementalMetadataFile), []byte(metadata), 0600); err != nil {
			t.Fatalf("Failed to write metadata: %v", err)
		}
	}

	ir := NewIncrementalResolverWithStorage(storage)
	incrementals, err := ir.ListIncrementals(sessionName)
	if err != nil {
		t.Fatalf("ListIncrementals() error = %v", err)
	}

	if len(incrementals) != 2 {
		t.Errorf("ListIncrementals() len = %d, want 2", len(incrementals))
	}
	if len(incrementals) == 2 {
		if incrementals[0].ID != "inc-002" || incrementals[1].ID != "inc-001" {
			t.Fatalf("ListIncrementals() order = [%s %s], want [inc-002 inc-001]", incrementals[0].ID, incrementals[1].ID)
		}
	}
}

func TestIncrementalResolver_ListIncrementals_NoSession(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	ir := NewIncrementalResolverWithStorage(storage)
	incrementals, err := ir.ListIncrementals("nonexistent-session")
	if err != nil {
		t.Fatalf("ListIncrementals() error = %v", err)
	}

	if len(incrementals) != 0 {
		t.Errorf("ListIncrementals() = %v, want empty", incrementals)
	}
}

func TestIncrementalResolver_ListIncrementals_RejectsInvalidSessionName(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.ListIncrementals("../escape")
	if err == nil {
		t.Fatal("ListIncrementals() error = nil, want invalid session name")
	}
	if !strings.Contains(err.Error(), "invalid session name") {
		t.Fatalf("ListIncrementals() error = %v, want invalid session name", err)
	}
}

func TestIncrementalResolver_ChainResolve_RejectsInvalidBaseIncremental(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          "base-full",
		Name:        "base",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Title: "base"}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) failed: %v", err)
	}

	writeIncremental := func(id, metadata string) {
		t.Helper()
		incDir := filepath.Join(tmpDir, sessionName, "incremental", id)
		if err := os.MkdirAll(incDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s) failed: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), []byte(metadata), 0600); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", id, err)
		}
	}

	writeIncremental("inc-base", `{
		"version": 1,
		"id": "inc-base",
		"session_name": "other-session",
		"base_checkpoint_id": "base-full",
		"created_at": "2025-01-06T10:00:00Z",
		"changes": {}
	}`)
	writeIncremental("inc-top", `{
		"version": 1,
		"id": "inc-top",
		"session_name": "test-session",
		"base_checkpoint_id": "inc-base",
		"created_at": "2025-01-07T10:00:00Z",
		"changes": {}
	}`)

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.ChainResolve(sessionName, "inc-top")
	if err == nil {
		t.Fatal("ChainResolve() error = nil, want invalid base incremental error")
	}
	if !strings.Contains(err.Error(), "loading incremental checkpoint inc-base") {
		t.Fatalf("ChainResolve() error = %v, want invalid base incremental context", err)
	}
}

func TestIncrementalResolver_ChainResolve_RejectsBrokenSymlinkBaseIncremental(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	base := &Checkpoint{
		Version:     2,
		ID:          "inc-base",
		Name:        "conflicting-full",
		SessionName: sessionName,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		Session: SessionState{
			Panes: []PaneState{{ID: "%0", Title: "full-checkpoint"}},
		},
		PaneCount: 1,
	}
	if err := storage.Save(base); err != nil {
		t.Fatalf("Save(base) failed: %v", err)
	}

	writeIncremental := func(id, metadata string) {
		t.Helper()
		incDir := filepath.Join(tmpDir, sessionName, "incremental", id)
		if err := os.MkdirAll(incDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s) failed: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), []byte(metadata), 0600); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", id, err)
		}
	}

	writeIncremental("inc-top", `{
		"version": 1,
		"id": "inc-top",
		"session_name": "test-session",
		"base_checkpoint_id": "inc-base",
		"created_at": "2025-01-06T12:00:00Z",
		"changes": {}
	}`)

	incBaseDir := filepath.Join(tmpDir, sessionName, "incremental", "inc-base")
	if err := os.MkdirAll(incBaseDir, 0755); err != nil {
		t.Fatalf("MkdirAll(inc-base) failed: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing-incremental.json"), filepath.Join(incBaseDir, IncrementalMetadataFile)); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.ChainResolve(sessionName, "inc-top")
	if err == nil {
		t.Fatal("ChainResolve() error = nil, want broken incremental metadata error")
	}
	if !strings.Contains(err.Error(), "loading incremental checkpoint inc-base") || !strings.Contains(err.Error(), IncrementalMetadataFile) {
		t.Fatalf("ChainResolve() error = %v, want invalid base incremental metadata context", err)
	}
}

func TestIncrementalResolver_ChainResolve_RejectsSymlinkBaseIncrementalDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	sessionName := "test-session"

	writeIncremental := func(id, metadata string) {
		t.Helper()
		incDir := filepath.Join(tmpDir, sessionName, "incremental", id)
		if err := os.MkdirAll(incDir, 0755); err != nil {
			t.Fatalf("MkdirAll(%s) failed: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(incDir, IncrementalMetadataFile), []byte(metadata), 0600); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", id, err)
		}
	}

	writeIncremental("inc-top", `{
		"version": 1,
		"id": "inc-top",
		"session_name": "test-session",
		"base_checkpoint_id": "inc-base",
		"created_at": "2025-01-06T12:00:00Z",
		"changes": {}
	}`)

	incBaseRoot := filepath.Join(tmpDir, sessionName, "incremental")
	if err := os.Symlink(t.TempDir(), filepath.Join(incBaseRoot, "inc-base")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	ir := NewIncrementalResolverWithStorage(storage)
	_, err := ir.ChainResolve(sessionName, "inc-top")
	if err == nil {
		t.Fatal("ChainResolve() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checking base incremental inc-base") || !strings.Contains(err.Error(), "incremental path must not be a symlink") {
		t.Fatalf("ChainResolve() error = %v, want incremental dir symlink rejection", err)
	}
}

func TestPaneChange_States(t *testing.T) {
	// Test pane change states
	added := PaneChange{Added: true, NewLines: 100}
	if !added.Added {
		t.Error("PaneChange.Added should be true")
	}

	removed := PaneChange{Removed: true}
	if !removed.Removed {
		t.Error("PaneChange.Removed should be true")
	}

	modified := PaneChange{
		AgentType: stringPointer("cc"),
		Title:     stringPointer("New Title"),
		Command:   stringPointer("claude"),
		NewLines:  50,
	}
	if modified.Added || modified.Removed {
		t.Error("Modified pane should not be marked as Added or Removed")
	}
}

func TestIncrementalChanges_Empty(t *testing.T) {
	changes := IncrementalChanges{}

	if changes.PaneChanges != nil {
		t.Error("Empty IncrementalChanges should have nil PaneChanges")
	}

	if changes.GitChange != nil {
		t.Error("Empty IncrementalChanges should have nil GitChange")
	}

	if changes.SessionChange != nil {
		t.Error("Empty IncrementalChanges should have nil SessionChange")
	}
}

func TestIncrementalCheckpoint_Fields(t *testing.T) {
	now := time.Now()
	baseTime := now.Add(-time.Hour)

	inc := &IncrementalCheckpoint{
		Version:          IncrementalVersion,
		ID:               "test-inc-123",
		SessionName:      "my-session",
		BaseCheckpointID: "base-checkpoint-456",
		BaseTimestamp:    baseTime,
		CreatedAt:        now,
		Description:      "Test incremental",
		Changes:          IncrementalChanges{},
	}

	if inc.Version != IncrementalVersion {
		t.Errorf("Version = %d, want %d", inc.Version, IncrementalVersion)
	}

	if inc.ID != "test-inc-123" {
		t.Errorf("ID = %q, want %q", inc.ID, "test-inc-123")
	}

	if inc.SessionName != "my-session" {
		t.Errorf("SessionName = %q, want %q", inc.SessionName, "my-session")
	}

	if !inc.BaseTimestamp.Equal(baseTime) {
		t.Errorf("BaseTimestamp = %v, want %v", inc.BaseTimestamp, baseTime)
	}
}
