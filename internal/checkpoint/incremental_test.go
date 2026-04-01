package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestIncrementalCreator_computeGitChange(t *testing.T) {
	ic := NewIncrementalCreator()

	tests := []struct {
		name       string
		base       GitState
		current    GitState
		wantChange bool
		wantBranch string
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
			wantBranch: "", // Branch didn't change
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
			wantBranch: "feature",
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

			if got != nil && got.Branch != tt.wantBranch {
				t.Errorf("computeGitChange().Branch = %q, want %q", got.Branch, tt.wantBranch)
			}
		})
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
		wantActivePaneIndex *int
		wantPaneCount       *int
	}{
		{
			name: "no changes",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes:           make([]PaneState, 2),
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
				Panes:           make([]PaneState, 2),
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
			wantChange: true,
			wantLayout: stringPtr("tiled"),
		},
		{
			name: "active pane changed",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 1,
			},
			wantChange:          true,
			wantActivePaneIndex: intPtr(1),
		},
		{
			name: "active pane changed to zero",
			base: SessionState{
				Layout:          "main",
				ActivePaneIndex: 2,
			},
			current: SessionState{
				Layout:          "main",
				ActivePaneIndex: 0,
			},
			wantChange:          true,
			wantActivePaneIndex: intPtr(0),
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

			if !equalIntPointer(got.ActivePaneIndex, tt.wantActivePaneIndex) {
				t.Errorf("computeSessionChange().ActivePaneIndex = %v, want %v", got.ActivePaneIndex, tt.wantActivePaneIndex)
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
					ScrollbackFile:  "panes/stale.txt",
					ScrollbackLines: 99,
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
					ScrollbackFile:  "panes/stale.txt",
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
	firstPane := PaneState{ID: "%0", Index: 0, WindowIndex: 0, Title: "first"}
	secondPane := PaneState{ID: "%1", Index: 1, WindowIndex: 0, Title: "second"}
	inc := &IncrementalCheckpoint{
		Version:   IncrementalVersion,
		ID:        "inc-sort-added-panes",
		CreatedAt: time.Now(),
		Changes: IncrementalChanges{
			SessionChange: &SessionChange{
				ActivePaneIndex: &activePaneIndex,
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
				{ID: "pane1", ScrollbackFile: "panes/pane1.txt"},
			},
		},
	}

	// Save the base checkpoint
	if err := storage.Save(base); err != nil {
		t.Fatalf("Failed to save base checkpoint: %v", err)
	}

	// Save some mock scrollback
	_, err := storage.SaveScrollback("test-session", baseID, "pane1", "line1\nline2\nline3\nline4\nline5")
	if err != nil {
		t.Fatalf("Failed to save scrollback: %v", err)
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

func TestIncrementalCreator_incrementalDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	ic := NewIncrementalCreatorWithStorage(storage)

	dir := ic.incrementalDir("my-session", "inc-123")
	expected := filepath.Join(tmpDir, "my-session", "incremental", "inc-123")

	if dir != expected {
		t.Errorf("incrementalDir() = %q, want %q", dir, expected)
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
}

func TestIncrementalResolver_ListIncrementals_NoSession(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)

	ir := NewIncrementalResolverWithStorage(storage)
	incrementals, err := ir.ListIncrementals("nonexistent-session")
	if err != nil {
		t.Fatalf("ListIncrementals() error = %v", err)
	}

	if incrementals != nil && len(incrementals) != 0 {
		t.Errorf("ListIncrementals() = %v, want empty", incrementals)
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
