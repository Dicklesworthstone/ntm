package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestRestoreOptions_Defaults(t *testing.T) {
	opts := RestoreOptions{}

	// All options should be false by default
	if opts.Force {
		t.Error("Force should be false by default")
	}
	if opts.SkipGitCheck {
		t.Error("SkipGitCheck should be false by default")
	}
	if opts.InjectContext {
		t.Error("InjectContext should be false by default")
	}
	if opts.DryRun {
		t.Error("DryRun should be false by default")
	}
	if opts.CustomDirectory != "" {
		t.Error("CustomDirectory should be empty by default")
	}
}

func TestRestoreResult_Fields(t *testing.T) {
	result := &RestoreResult{
		SessionName:     "test-session",
		PanesRestored:   3,
		ContextInjected: true,
		Warnings:        []string{"warning1", "warning2"},
		DryRun:          false,
	}

	if result.SessionName != "test-session" {
		t.Errorf("SessionName = %q, want %q", result.SessionName, "test-session")
	}
	if result.PanesRestored != 3 {
		t.Errorf("PanesRestored = %d, want %d", result.PanesRestored, 3)
	}
	if !result.ContextInjected {
		t.Error("ContextInjected should be true")
	}
	if len(result.Warnings) != 2 {
		t.Errorf("len(Warnings) = %d, want %d", len(result.Warnings), 2)
	}
}

func TestNewRestorer(t *testing.T) {
	r := NewRestorer()
	if r == nil {
		t.Fatal("NewRestorer() returned nil")
	}
	if r.storage == nil {
		t.Error("Restorer.storage should not be nil")
	}
}

func TestNewRestorerWithStorage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

	if r.storage != storage {
		t.Error("Restorer should use provided storage")
	}
}

func TestRestorer_RestoreFromCheckpoint_DirectoryNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  "/nonexistent/path/that/does/not/exist",
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
	}

	_, err = r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err == nil {
		t.Error("RestoreFromCheckpoint should fail for nonexistent directory")
	}
}

func TestRestorer_RestoreFromCheckpoint_NoPanes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{}, // Empty panes
		},
	}

	_, err = r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err != ErrNoAgentsToRestore {
		t.Errorf("Expected ErrNoAgentsToRestore, got: %v", err)
	}
}

func TestRestorer_RestoreFromCheckpoint_DryRun(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-dryrun-session",
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, ID: "%0", Title: "pane1"},
				{Index: 1, ID: "%1", Title: "pane2"},
			},
		},
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint(DryRun) failed: %v", err)
	}

	if !result.DryRun {
		t.Error("Result.DryRun should be true")
	}
	if result.PanesRestored != 2 {
		t.Errorf("PanesRestored = %d, want 2", result.PanesRestored)
	}
}

func TestRestorer_RestoreFromCheckpoint_DryRun_CustomDirectory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  "/original/nonexistent/path",
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
	}

	// Should succeed with custom directory override
	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{
		DryRun:          true,
		CustomDirectory: tmpDir,
	})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint with CustomDirectory failed: %v", err)
	}

	if result.PanesRestored != 1 {
		t.Errorf("PanesRestored = %d, want 1", result.PanesRestored)
	}
}

func TestRestorer_RestoreFromCheckpoint_WorkingDirNotDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  filePath,
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
	}

	_, err := r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if !errors.Is(err, ErrWorkingDirInvalid) {
		t.Fatalf("RestoreFromCheckpoint should fail with ErrWorkingDirInvalid, got %v", err)
	}
}

func TestRestorer_ValidateCheckpoint_DirectoryNotFound(t *testing.T) {
	r := NewRestorer()

	cp := &Checkpoint{
		WorkingDir: "/nonexistent/path",
		Session: SessionState{
			Panes: []PaneState{{Index: 0}},
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "directory not found") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected directory not found issue")
	}
}

func TestRestorer_ValidateCheckpoint_NoPanes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorer()

	cp := &Checkpoint{
		WorkingDir: tmpDir,
		Session: SessionState{
			Panes: []PaneState{}, // Empty
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "no panes") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'no panes' issue")
	}
}

func TestRestorer_ValidateCheckpoint_WorkingDirNotDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))
	cp := &Checkpoint{
		WorkingDir: filePath,
		Session: SessionState{
			Panes: []PaneState{{Index: 0}},
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "not a directory") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected not-a-directory issue, got %v", issues)
	}
}

func TestRestorer_ValidateCheckpoint_MissingScrollback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

	// Create a checkpoint directory without scrollback files
	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:          0,
					ID:             "%0",
					ScrollbackFile: "panes/pane_0.txt", // File doesn't exist
				},
			},
		},
	}

	// Create the checkpoint directory
	cpDir := storage.CheckpointDir(cp.SessionName, cp.ID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatalf("Failed to create checkpoint dir: %v", err)
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{InjectContext: true})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "scrollback file missing") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected 'scrollback file missing' issue, got: %v", issues)
	}
}

func TestRestorer_ValidateCheckpoint_WindowLayoutReferencesMissingWindow(t *testing.T) {
	r := NewRestorer()

	cp := &Checkpoint{
		WorkingDir: t.TempDir(),
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, WindowIndex: 0},
				{Index: 0, WindowIndex: 1},
			},
			WindowLayouts: []WindowLayoutState{
				{WindowIndex: 0, Layout: "even-horizontal"},
				{WindowIndex: 9, Layout: "main-vertical"},
			},
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "references missing window 9") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing-window layout issue, got %v", issues)
	}
}

func TestRestorer_ValidateCheckpoint_WindowLayoutMissingForExistingWindow(t *testing.T) {
	r := NewRestorer()

	cp := &Checkpoint{
		WorkingDir: t.TempDir(),
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, WindowIndex: 0},
				{Index: 0, WindowIndex: 1},
			},
			WindowLayouts: []WindowLayoutState{
				{WindowIndex: 0, Layout: "even-horizontal"},
			},
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "window layout missing for window 1") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected missing-layout issue, got %v", issues)
	}
}

func TestRestorer_ValidateCheckpoint_LegacyLayoutWarnsForMultiWindowSession(t *testing.T) {
	r := NewRestorer()

	cp := &Checkpoint{
		WorkingDir: t.TempDir(),
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, WindowIndex: 0},
				{Index: 0, WindowIndex: 1},
			},
			Layout: "even-horizontal",
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{})

	found := false
	for _, issue := range issues {
		if containsSubstr(issue, "legacy single layout string") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected legacy-layout warning, got %v", issues)
	}
}

func TestRestorer_RestoreFromCheckpoint_SkipsLegacyLayoutForMultiWindowSession(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(t.TempDir()))
	sessionName := "cplegacy-layout-" + time.Now().Format("150405000000")

	cp := &Checkpoint{
		ID:          "legacy-layout-checkpoint",
		SessionName: sessionName,
		WorkingDir:  workDir,
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:       0,
					WindowIndex: 0,
					Title:       sessionName + "__cc_1",
					AgentType:   "cc",
					Command:     "sleep 30",
				},
				{
					Index:       0,
					WindowIndex: 1,
					Title:       sessionName + "__cod_1",
					AgentType:   "cod",
					Command:     "sleep 30",
				},
			},
			Layout: "even-horizontal",
		},
	}

	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint failed: %v", err)
	}

	found := false
	for _, warning := range result.Warnings {
		if containsSubstr(warning, "skipping legacy single layout for multi-window checkpoint") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected legacy-layout skip warning, got %v", result.Warnings)
	}
}

func TestRestorer_loadPaneScrollback_Compressed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

	const (
		sessionName  = "test-session"
		checkpointID = "test-checkpoint"
		paneID       = "%0"
		content      = "compressed scrollback\nline 2\n"
	)

	compressed, err := gzipCompress([]byte(content))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	if _, err := storage.SaveCompressedScrollback(sessionName, checkpointID, paneID, compressed); err != nil {
		t.Fatalf("SaveCompressedScrollback failed: %v", err)
	}

	got, err := r.loadPaneScrollback(sessionName, checkpointID, paneID)
	if err != nil {
		t.Fatalf("loadPaneScrollback failed: %v", err)
	}
	if got != content {
		t.Fatalf("loadPaneScrollback = %q, want %q", got, content)
	}
}

func TestRestorer_loadPaneScrollbackForPane_UsesRecordedScrollbackFile(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

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

	got, err := r.loadPaneScrollbackForPane(sessionName, checkpointID, PaneState{
		ID:             "%stale",
		ScrollbackFile: scrollbackRelPath,
	})
	if err != nil {
		t.Fatalf("loadPaneScrollbackForPane failed: %v", err)
	}
	if got != content {
		t.Fatalf("loadPaneScrollbackForPane = %q, want %q", got, content)
	}
}

func TestRestorer_loadPaneScrollbackForPane_FallsBackToPaneID(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorageWithDir(tmpDir)
	r := NewRestorerWithStorage(storage)

	const (
		sessionName  = "test-session"
		checkpointID = "test-checkpoint"
		paneID       = "%0"
		content      = "fallback scrollback\nline 2\n"
	)

	compressed, err := gzipCompress([]byte(content))
	if err != nil {
		t.Fatalf("gzipCompress failed: %v", err)
	}
	if _, err := storage.SaveCompressedScrollback(sessionName, checkpointID, paneID, compressed); err != nil {
		t.Fatalf("SaveCompressedScrollback failed: %v", err)
	}

	got, err := r.loadPaneScrollbackForPane(sessionName, checkpointID, PaneState{ID: paneID})
	if err != nil {
		t.Fatalf("loadPaneScrollbackForPane failed: %v", err)
	}
	if got != content {
		t.Fatalf("loadPaneScrollbackForPane = %q, want %q", got, content)
	}
}

func TestTruncateToLines(t *testing.T) {
	tests := []struct {
		content  string
		maxLines int
		want     string
	}{
		{"", 5, ""},
		{"one", 5, "one"},
		{"one\ntwo\nthree", 5, "one\ntwo\nthree"},
		{"one\ntwo\nthree", 2, "two\nthree"},
		{"one\ntwo\nthree\nfour\nfive", 3, "three\nfour\nfive"},
		{"one\ntwo\nthree", 1, "three"},
	}

	for _, tt := range tests {
		got := truncateToLines(tt.content, tt.maxLines)
		if got != tt.want {
			t.Errorf("truncateToLines(%q, %d) = %q, want %q",
				tt.content, tt.maxLines, got, tt.want)
		}
	}
}

func TestValidateCheckpoint_RejectsScrollbackTraversal(t *testing.T) {
	storage := NewStorageWithDir(t.TempDir())
	r := NewRestorerWithStorage(storage)

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  t.TempDir(),
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:          0,
					ID:             "%0",
					ScrollbackFile: "../../etc/passwd",
				},
			},
		},
	}

	issues := r.ValidateCheckpoint(cp, RestoreOptions{InjectContext: true})
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %v", len(issues), issues)
	}
	if !containsSubstr(issues[0], "invalid scrollback path") {
		t.Fatalf("expected invalid scrollback path issue, got %v", issues)
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"one", 1},
		{"one\n", 1}, // trailing newline doesn't add extra line
		{"one\ntwo", 2},
		{"one\ntwo\n", 2}, // trailing newline doesn't add extra line
		{"one\ntwo\nthree", 3},
	}

	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("len(splitLines(%q)) = %d, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestJoinLines(t *testing.T) {
	tests := []struct {
		lines []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"one"}, "one"},
		{[]string{"one", "two"}, "one\ntwo"},
		{[]string{"one", "two", "three"}, "one\ntwo\nthree"},
	}

	for _, tt := range tests {
		got := joinLines(tt.lines)
		if got != tt.want {
			t.Errorf("joinLines(%v) = %q, want %q", tt.lines, got, tt.want)
		}
	}
}

func TestTrimSpace(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"hello", "hello"},
		{"  hello", "hello"},
		{"hello  ", "hello"},
		{"  hello  ", "hello"},
		{"\n\thello\n\t", "hello"},
		{"  \n\t  ", ""},
	}

	for _, tt := range tests {
		got := trimSpace(tt.input)
		if got != tt.want {
			t.Errorf("trimSpace(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{3 * time.Hour, "3h"},
		{36 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFormatContextInjection(t *testing.T) {
	content := "Hello\nWorld"
	checkpointTime := time.Now().Add(-2 * time.Hour)

	result := formatContextInjection(content, checkpointTime)

	if !containsSubstr(result, "Context from checkpoint") {
		t.Error("Expected header with 'Context from checkpoint'")
	}
	if !containsSubstr(result, "Hello") {
		t.Error("Expected content to be included")
	}
	if !containsSubstr(result, "World") {
		t.Error("Expected content to be included")
	}
}

func TestRestorer_Restore_CheckpointNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	_, err = r.Restore("nonexistent-session", "nonexistent-checkpoint", RestoreOptions{})
	if err == nil {
		t.Error("Restore should fail for nonexistent checkpoint")
	}
}

func TestRestoreResult_AssignmentsAndBVSummary(t *testing.T) {
	// Test RestoreResult with assignments and BV summary (bd-32ck)
	now := time.Now()
	result := &RestoreResult{
		SessionName:   "test-session",
		PanesRestored: 2,
		DryRun:        true,
		Assignments: []AssignmentSnapshot{
			{
				BeadID:     "bd-1234",
				BeadTitle:  "Fix the widget",
				Pane:       1,
				AgentType:  "claude",
				AgentName:  "BlueLake",
				Status:     "working",
				AssignedAt: now,
			},
			{
				BeadID:     "bd-5678",
				BeadTitle:  "Add tests",
				Pane:       2,
				AgentType:  "codex",
				Status:     "assigned",
				AssignedAt: now,
			},
		},
		BVSummary: &BVSnapshot{
			OpenCount:       15,
			ActionableCount: 8,
			BlockedCount:    5,
			InProgressCount: 2,
			TopPicks:        []string{"bd-1234", "bd-5678"},
			CapturedAt:      now,
		},
	}

	if len(result.Assignments) != 2 {
		t.Fatalf("len(Assignments) = %d, want 2", len(result.Assignments))
	}
	if result.Assignments[0].BeadID != "bd-1234" {
		t.Errorf("Assignments[0].BeadID = %q, want bd-1234", result.Assignments[0].BeadID)
	}
	if result.Assignments[0].AgentType != "claude" {
		t.Errorf("Assignments[0].AgentType = %q, want claude", result.Assignments[0].AgentType)
	}
	if result.Assignments[1].Status != "assigned" {
		t.Errorf("Assignments[1].Status = %q, want assigned", result.Assignments[1].Status)
	}
	if result.BVSummary == nil {
		t.Fatal("BVSummary should not be nil")
	}
	if result.BVSummary.ActionableCount != 8 {
		t.Errorf("BVSummary.ActionableCount = %d, want 8", result.BVSummary.ActionableCount)
	}
	if result.BVSummary.BlockedCount != 5 {
		t.Errorf("BVSummary.BlockedCount = %d, want 5", result.BVSummary.BlockedCount)
	}
	if len(result.BVSummary.TopPicks) != 2 {
		t.Errorf("len(BVSummary.TopPicks) = %d, want 2", len(result.BVSummary.TopPicks))
	}
}

func TestRestoreResult_NoAssignmentsOrBVSummary(t *testing.T) {
	// Test backward compat: RestoreResult without assignments (bd-32ck)
	result := &RestoreResult{
		SessionName:   "test-session",
		PanesRestored: 1,
	}

	if len(result.Assignments) != 0 {
		t.Errorf("len(Assignments) = %d, want 0", len(result.Assignments))
	}
	if result.BVSummary != nil {
		t.Error("BVSummary should be nil when not set")
	}
}

func TestRestorer_RestoreFromCheckpoint_DryRun_SurfacesAssignments(t *testing.T) {
	// Test that DryRun restore surfaces assignment and BV data (bd-32ck)
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	now := time.Now()
	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{
				{Index: 0, ID: "%0", Title: "user"},
				{Index: 1, ID: "%1", Title: "cc_1"},
			},
		},
		Assignments: []AssignmentSnapshot{
			{
				BeadID:     "bd-42",
				BeadTitle:  "Implement feature X",
				Pane:       1,
				AgentType:  "claude",
				AgentName:  "GreenCastle",
				Status:     "working",
				AssignedAt: now,
			},
		},
		BVSummary: &BVSnapshot{
			OpenCount:       20,
			ActionableCount: 12,
			BlockedCount:    6,
			InProgressCount: 2,
			TopPicks:        []string{"bd-42", "bd-99"},
			CapturedAt:      now,
		},
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint(DryRun) failed: %v", err)
	}

	// Verify assignments are surfaced in result
	if len(result.Assignments) != 1 {
		t.Fatalf("len(result.Assignments) = %d, want 1", len(result.Assignments))
	}
	if result.Assignments[0].BeadID != "bd-42" {
		t.Errorf("Assignments[0].BeadID = %q, want bd-42", result.Assignments[0].BeadID)
	}
	if result.Assignments[0].AgentName != "GreenCastle" {
		t.Errorf("Assignments[0].AgentName = %q, want GreenCastle", result.Assignments[0].AgentName)
	}

	// Verify BV summary is surfaced
	if result.BVSummary == nil {
		t.Fatal("result.BVSummary should not be nil")
	}
	if result.BVSummary.ActionableCount != 12 {
		t.Errorf("BVSummary.ActionableCount = %d, want 12", result.BVSummary.ActionableCount)
	}
	if result.BVSummary.InProgressCount != 2 {
		t.Errorf("BVSummary.InProgressCount = %d, want 2", result.BVSummary.InProgressCount)
	}
}

func TestRestorer_RestoreFromCheckpoint_DryRun_NoAssignments(t *testing.T) {
	// Test that restore without assignments still works (backward compat, bd-32ck)
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: "test-session",
		WorkingDir:  tmpDir,
		Session: SessionState{
			Panes: []PaneState{{Index: 0, ID: "%0"}},
		},
		// No Assignments or BVSummary
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint(DryRun) failed: %v", err)
	}

	if len(result.Assignments) != 0 {
		t.Errorf("len(result.Assignments) = %d, want 0", len(result.Assignments))
	}
	if result.BVSummary != nil {
		t.Error("result.BVSummary should be nil for checkpoint without BV data")
	}
}

func TestRestorer_RestoreLatest_NoCheckpoints(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ntm-restore-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	r := NewRestorerWithStorage(NewStorageWithDir(tmpDir))

	_, err = r.RestoreLatest("nonexistent-session", RestoreOptions{})
	if err == nil {
		t.Error("RestoreLatest should fail when no checkpoints exist")
	}
}

func TestRestorer_checkGitState_BranchMismatch(t *testing.T) {
	repoDir, branch, commit := initGitRepo(t)

	r := NewRestorer()
	cp := &Checkpoint{
		Git: GitState{
			Branch: branch + "-other",
			Commit: commit,
		},
	}

	warning := r.checkGitState(cp, repoDir)
	if !strings.Contains(warning, "git branch mismatch") {
		t.Fatalf("expected branch mismatch warning, got %q", warning)
	}
}

func TestRestorer_checkGitState_CommitMismatch(t *testing.T) {
	repoDir, branch, commit := initGitRepo(t)

	// Create a new commit to move HEAD forward.
	updateFile := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(updateFile, []byte("updated"), 0644); err != nil {
		t.Fatalf("failed to update file: %v", err)
	}
	runGitCmd(t, repoDir, "add", ".")
	runGitCmd(t, repoDir, "commit", "-m", "Second commit")

	r := NewRestorer()
	cp := &Checkpoint{
		Git: GitState{
			Branch: branch,
			Commit: commit,
		},
	}

	warning := r.checkGitState(cp, repoDir)
	if !strings.Contains(warning, "git commit mismatch") {
		t.Fatalf("expected commit mismatch warning, got %q", warning)
	}
}

func TestFromTmuxPane_PreservesWindowIndex(t *testing.T) {
	state := FromTmuxPane(tmux.Pane{
		ID:          "%9",
		Index:       2,
		WindowIndex: 3,
		Title:       "demo__cc_1",
		Type:        tmux.AgentClaude,
		Command:     "claude",
		Width:       120,
		Height:      40,
	})

	if state.WindowIndex != 3 {
		t.Fatalf("WindowIndex = %d, want 3", state.WindowIndex)
	}
}

func TestRestorableAgentCommand(t *testing.T) {
	tests := []struct {
		name string
		pane PaneState
		want string
	}{
		{
			name: "custom non-shell command is preserved",
			pane: PaneState{AgentType: "cc", Command: "/usr/local/bin/custom-claude-wrapper --fast"},
			want: "/usr/local/bin/custom-claude-wrapper --fast",
		},
		{
			name: "shell command falls back to default agent binary",
			pane: PaneState{AgentType: "codex", Command: "zsh"},
			want: "codex",
		},
		{
			name: "missing command falls back to default agent binary",
			pane: PaneState{AgentType: "google-gemini"},
			want: "gemini",
		},
		{
			name: "user panes are skipped",
			pane: PaneState{AgentType: "user", Command: "bash"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restorableAgentCommand(tt.pane); got != tt.want {
				t.Fatalf("restorableAgentCommand(%+v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
}

func TestExpectedPaneCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "plain command",
			command: "claude --print",
			want:    "claude",
		},
		{
			name:    "custom wrapper path",
			command: "/usr/local/bin/custom-claude-wrapper --fast",
			want:    "custom-claude-wrapper",
		},
		{
			name:    "leading env assignment",
			command: `NODE_OPTIONS="--max-old-space-size=32768" claude --dangerously-skip-permissions`,
			want:    "claude",
		},
		{
			name:    "command substitution in env assignment",
			command: `CODEX_SYSTEM_PROMPT="$(cat /tmp/system prompt.txt)" codex --search`,
			want:    "codex",
		},
		{
			name:    "env prefix with assignments",
			command: "env FOO=bar codex --search",
			want:    "codex",
		},
		{
			name:    "env prefix with options",
			command: "env -i FOO=bar codex --search",
			want:    "codex",
		},
		{
			name:    "absolute env wrapper path",
			command: "/usr/bin/env FOO=bar codex --search",
			want:    "codex",
		},
		{
			name:    "exec prefix",
			command: "exec codex --search",
			want:    "codex",
		},
		{
			name:    "env and exec prefixes",
			command: "env FOO=bar exec codex --search",
			want:    "codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expectedPaneCommand(tt.command); got != tt.want {
				t.Fatalf("expectedPaneCommand(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestSortedCheckpointPanes(t *testing.T) {
	panes := []PaneState{
		{WindowIndex: 2, Index: 0, Title: "w2p0"},
		{WindowIndex: 0, Index: 1, Title: "w0p1"},
		{WindowIndex: 0, Index: 0, Title: "w0p0"},
		{WindowIndex: 1, Index: 0, Title: "w1p0"},
	}

	sorted := sortedCheckpointPanes(panes)
	gotTitles := []string{sorted[0].Title, sorted[1].Title, sorted[2].Title, sorted[3].Title}
	wantTitles := []string{"w0p0", "w0p1", "w1p0", "w2p0"}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Fatalf("sorted title at %d = %q, want %q", i, gotTitles[i], wantTitles[i])
		}
	}
}

func TestRestoredPaneIndexForCheckpointIndex_UnsortedCheckpointOrder(t *testing.T) {
	panes := []PaneState{
		{ID: "%b", WindowIndex: 1, Index: 0, Title: "w1p0"},
		{ID: "%a", WindowIndex: 0, Index: 0, Title: "w0p0"},
		{ID: "%c", WindowIndex: 1, Index: 1, Title: "w1p1"},
	}

	tests := []struct {
		checkpointIndex int
		want            int
	}{
		{checkpointIndex: 0, want: 1},
		{checkpointIndex: 1, want: 0},
		{checkpointIndex: 2, want: 2},
	}

	for _, tt := range tests {
		if got := restoredPaneIndexForCheckpointIndex(panes, tt.checkpointIndex); got != tt.want {
			t.Fatalf("restoredPaneIndexForCheckpointIndex(%d) = %d, want %d", tt.checkpointIndex, got, tt.want)
		}
	}
}

func TestRestorer_RestoreFromCheckpoint_RelaunchesCommandsAcrossWindows(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(t.TempDir()))
	sessionName := "cprestore-" + time.Now().Format("150405000000")

	cp := &Checkpoint{
		ID:          "test-checkpoint",
		SessionName: sessionName,
		WorkingDir:  workDir,
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:       0,
					WindowIndex: 1,
					Title:       sessionName + "__cursor_1",
					AgentType:   "cursor",
					Command:     "sleep 30",
					ID:          "%old-1",
				},
				{
					Index:       0,
					WindowIndex: 0,
					Title:       sessionName + "__cc_1",
					AgentType:   "cc",
					Command:     "sleep 30",
					ID:          "%old-0",
				},
				{
					Index:       1,
					WindowIndex: 1,
					Title:       sessionName + "__cursor_2",
					AgentType:   "cursor",
					Command:     "sleep 30",
					ID:          "%old-2",
				},
			},
			ActivePaneIndex: 0,
		},
	}

	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint failed: %v", err)
	}
	if result.PanesRestored != 3 {
		t.Fatalf("PanesRestored = %d, want 3 (warnings=%v)", result.PanesRestored, result.Warnings)
	}

	var sorted []tmux.Pane
	deadline := time.Now().Add(5 * time.Second)
	for {
		panes, err := tmux.GetPanes(sessionName)
		if err != nil {
			t.Fatalf("GetPanes failed: %v", err)
		}
		if len(panes) != 3 {
			t.Fatalf("len(panes) = %d, want 3", len(panes))
		}

		sorted = sortedTmuxPanes(panes)
		if sorted[0].Command == "sleep" && sorted[1].Command == "sleep" && sorted[2].Command == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"timed out waiting for restored commands; commands=[%q %q %q] active=[%v %v %v]",
				sorted[0].Command,
				sorted[1].Command,
				sorted[2].Command,
				sorted[0].Active,
				sorted[1].Active,
				sorted[2].Active,
			)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if sorted[0].WindowIndex == sorted[1].WindowIndex {
		t.Fatalf("window indexes = [%d %d %d], want first pane in its own window", sorted[0].WindowIndex, sorted[1].WindowIndex, sorted[2].WindowIndex)
	}
	if sorted[1].WindowIndex != sorted[2].WindowIndex {
		t.Fatalf("window indexes = [%d %d %d], want panes 2 and 3 in the same window", sorted[0].WindowIndex, sorted[1].WindowIndex, sorted[2].WindowIndex)
	}
	if sorted[1].WindowIndex <= sorted[0].WindowIndex {
		t.Fatalf("window indexes = [%d %d %d], want ascending order", sorted[0].WindowIndex, sorted[1].WindowIndex, sorted[2].WindowIndex)
	}
	if sorted[0].Title != sessionName+"__cc_1" {
		t.Fatalf("first pane title = %q, want %q", sorted[0].Title, sessionName+"__cc_1")
	}
	if sorted[1].Title != sessionName+"__cursor_1" {
		t.Fatalf("second pane title = %q, want %q", sorted[1].Title, sessionName+"__cursor_1")
	}
	if sorted[2].Title != sessionName+"__cursor_2" {
		t.Fatalf("third pane title = %q, want %q", sorted[2].Title, sessionName+"__cursor_2")
	}

	activePaneID, err := tmux.DefaultClient.Run("display-message", "-p", "-t", sessionName, "#{pane_id}")
	if err != nil {
		t.Fatalf("display-message active pane failed: %v", err)
	}
	if activePaneID != sorted[1].ID {
		t.Fatalf("active pane id = %q, want %q", activePaneID, sorted[1].ID)
	}
}

func TestRestorer_RestoreFromCheckpoint_PreservesSparseWindowIndexes(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := t.TempDir()
	r := NewRestorerWithStorage(NewStorageWithDir(t.TempDir()))
	sessionName := "cprestore-sparse-" + time.Now().Format("150405000000")

	cp := &Checkpoint{
		ID:          "test-checkpoint-sparse",
		SessionName: sessionName,
		WorkingDir:  workDir,
		Session: SessionState{
			Panes: []PaneState{
				{
					Index:       0,
					WindowIndex: 5,
					Title:       sessionName + "__cod_1",
					AgentType:   "cod",
					Command:     "sleep 30",
					ID:          "%old-5",
				},
				{
					Index:       0,
					WindowIndex: 2,
					Title:       sessionName + "__cc_1",
					AgentType:   "cc",
					Command:     "sleep 30",
					ID:          "%old-2",
				},
				{
					Index:       1,
					WindowIndex: 5,
					Title:       sessionName + "__cod_2",
					AgentType:   "cod",
					Command:     "sleep 30",
					ID:          "%old-6",
				},
			},
			ActivePaneIndex: 0,
		},
	}

	t.Cleanup(func() {
		if tmux.SessionExists(sessionName) {
			_ = tmux.KillSession(sessionName)
		}
	})

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint failed: %v", err)
	}
	if result.PanesRestored != 3 {
		t.Fatalf("PanesRestored = %d, want 3 (warnings=%v)", result.PanesRestored, result.Warnings)
	}

	var sorted []tmux.Pane
	deadline := time.Now().Add(5 * time.Second)
	for {
		panes, err := tmux.GetPanes(sessionName)
		if err != nil {
			t.Fatalf("GetPanes failed: %v", err)
		}
		if len(panes) != 3 {
			t.Fatalf("len(panes) = %d, want 3", len(panes))
		}

		sorted = sortedTmuxPanes(panes)
		if sorted[0].Command == "sleep" && sorted[1].Command == "sleep" && sorted[2].Command == "sleep" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"timed out waiting for restored commands; commands=[%q %q %q] window_indexes=[%d %d %d]",
				sorted[0].Command,
				sorted[1].Command,
				sorted[2].Command,
				sorted[0].WindowIndex,
				sorted[1].WindowIndex,
				sorted[2].WindowIndex,
			)
		}
		time.Sleep(100 * time.Millisecond)
	}

	gotWindowIndexes := []int{sorted[0].WindowIndex, sorted[1].WindowIndex, sorted[2].WindowIndex}
	wantWindowIndexes := []int{2, 5, 5}
	for i := range wantWindowIndexes {
		if gotWindowIndexes[i] != wantWindowIndexes[i] {
			t.Fatalf("window indexes = %v, want %v", gotWindowIndexes, wantWindowIndexes)
		}
	}

	activePaneID, err := tmux.DefaultClient.Run("display-message", "-p", "-t", sessionName, "#{pane_id}")
	if err != nil {
		t.Fatalf("display-message active pane failed: %v", err)
	}
	if activePaneID != sorted[1].ID {
		t.Fatalf("active pane id = %q, want %q", activePaneID, sorted[1].ID)
	}
}

func TestRestorer_RestoreFromCheckpoint_PreservesPerWindowLayouts(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	workDir := t.TempDir()
	templateSession := "cplayout-src-" + time.Now().Format("150405000000")
	restoreSession := "cplayout-dst-" + time.Now().Format("150405000000")

	if err := tmux.CreateSession(templateSession, workDir); err != nil {
		t.Fatalf("CreateSession(template) failed: %v", err)
	}
	t.Cleanup(func() {
		if tmux.SessionExists(templateSession) {
			_ = tmux.KillSession(templateSession)
		}
		if tmux.SessionExists(restoreSession) {
			_ = tmux.KillSession(restoreSession)
		}
	})

	firstWindow, err := tmux.GetFirstWindow(templateSession)
	if err != nil {
		t.Fatalf("GetFirstWindow(template) failed: %v", err)
	}
	firstTarget := fmt.Sprintf("%s:%d", templateSession, firstWindow)
	if _, err := tmux.DefaultClient.Run("split-window", "-t", firstTarget, "-c", workDir, "-P", "-F", "#{pane_id}"); err != nil {
		t.Fatalf("split-window first template window failed: %v", err)
	}
	if err := tmux.DefaultClient.RunSilent("select-layout", "-t", firstTarget, "even-horizontal"); err != nil {
		t.Fatalf("select-layout first template window failed: %v", err)
	}

	secondWindowIndex, err := tmux.DefaultClient.Run("new-window", "-d", "-t", templateSession, "-c", workDir, "-P", "-F", "#{window_index}")
	if err != nil {
		t.Fatalf("new-window template failed: %v", err)
	}
	secondTarget := fmt.Sprintf("%s:%s", templateSession, strings.TrimSpace(secondWindowIndex))
	if _, err := tmux.DefaultClient.Run("split-window", "-t", secondTarget, "-c", workDir, "-P", "-F", "#{pane_id}"); err != nil {
		t.Fatalf("split-window second template window failed: %v", err)
	}
	if err := tmux.DefaultClient.RunSilent("select-layout", "-t", secondTarget, "main-vertical"); err != nil {
		t.Fatalf("select-layout second template window failed: %v", err)
	}

	capturer := NewCapturerWithStorage(NewStorageWithDir(t.TempDir()))
	sessionState, err := capturer.captureSessionState(templateSession)
	if err != nil {
		t.Fatalf("captureSessionState(template) failed: %v", err)
	}
	if len(sessionState.WindowLayouts) != 2 {
		t.Fatalf("len(sessionState.WindowLayouts) = %d, want 2", len(sessionState.WindowLayouts))
	}

	r := NewRestorerWithStorage(NewStorageWithDir(t.TempDir()))
	cp := &Checkpoint{
		ID:          "layout-checkpoint",
		SessionName: restoreSession,
		WorkingDir:  workDir,
		Session:     sessionState,
	}

	result, err := r.RestoreFromCheckpoint(cp, RestoreOptions{})
	if err != nil {
		t.Fatalf("RestoreFromCheckpoint failed: %v", err)
	}
	if result.PanesRestored != len(sessionState.Panes) {
		t.Fatalf("PanesRestored = %d, want %d (warnings=%v)", result.PanesRestored, len(sessionState.Panes), result.Warnings)
	}

	restoredLayouts, err := getSessionWindowLayouts(restoreSession)
	if err != nil {
		t.Fatalf("getSessionWindowLayouts(restore) failed: %v", err)
	}
	if !windowLayoutsEqual(normalizeWindowLayouts(restoredLayouts), normalizeWindowLayouts(sessionState.WindowLayouts)) {
		t.Fatalf("restored layouts = %#v, want %#v", restoredLayouts, sessionState.WindowLayouts)
	}
}

var paneLayoutIDPattern = regexp.MustCompile(`(\d+x\d+,\d+,\d+),\d+`)

func normalizeWindowLayouts(layouts []WindowLayoutState) []WindowLayoutState {
	normalized := cloneWindowLayouts(layouts)
	for i := range normalized {
		normalized[i].Layout = normalizeWindowLayoutString(normalized[i].Layout)
	}
	return normalized
}

func normalizeWindowLayoutString(layout string) string {
	if comma := strings.IndexByte(layout, ','); comma >= 0 {
		layout = layout[comma+1:]
	}
	return paneLayoutIDPattern.ReplaceAllString(layout, `${1},pane`)
}

// containsSubstr checks if s contains substr (case-insensitive).
func containsSubstr(s, substr string) bool {
	return filepath.Base(s) == substr || len(s) >= len(substr) && findSubstr(s, substr)
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if matchIgnoreCase(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func matchIgnoreCase(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func initGitRepo(t *testing.T) (string, string, string) {
	t.Helper()

	repoDir, err := os.MkdirTemp("", "ntm-restore-git-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(repoDir) })

	runGitCmd(t, repoDir, "init")
	runGitCmd(t, repoDir, "config", "user.email", "test@example.com")
	runGitCmd(t, repoDir, "config", "user.name", "Test User")

	readme := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readme, []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	runGitCmd(t, repoDir, "add", ".")
	runGitCmd(t, repoDir, "commit", "-m", "Initial commit")

	branch := runGitCmd(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	commit := runGitCmd(t, repoDir, "rev-parse", "HEAD")

	return repoDir, strings.TrimSpace(branch), strings.TrimSpace(commit)
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()

	allArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", allArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (output: %s)", args, err, string(out))
	}
	return string(out)
}
