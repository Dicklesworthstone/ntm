package robot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandoffInspectEmptySession(t *testing.T) {
	projectDir := t.TempDir()

	out, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "emptyproj",
		ProjectDir: projectDir,
	})
	if err != nil {
		t.Fatalf("GetHandoff returned unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected Success=true for empty session inspect, got error=%q code=%q", out.Error, out.ErrorCode)
	}
	if out.Action != "handoff_inspect" {
		t.Fatalf("Action = %q, want handoff_inspect", out.Action)
	}
	if out.Session != "emptyproj" {
		t.Fatalf("Session = %q, want emptyproj", out.Session)
	}
	if out.Sessions == nil || len(out.Sessions) != 0 {
		t.Fatalf("expected non-nil empty Sessions slice, got %#v", out.Sessions)
	}
	if out.Handoffs == nil || len(out.Handoffs) != 0 {
		t.Fatalf("expected non-nil empty Handoffs slice, got %#v", out.Handoffs)
	}
	if out.TotalReturned != 0 {
		t.Fatalf("TotalReturned = %d, want 0", out.TotalReturned)
	}
	if out.Latest != nil || out.Quality != nil {
		t.Fatalf("expected Latest=nil and Quality=nil for empty session, got latest=%+v quality=%+v", out.Latest, out.Quality)
	}
	if out.Ledger == nil || out.Ledger.Exists || out.Ledger.Bytes != 0 {
		t.Fatalf("expected Ledger{Exists:false, Bytes:0}, got %+v", out.Ledger)
	}
	if out.AgentHints == nil || len(out.AgentHints.SuggestedActions) == 0 {
		t.Fatalf("expected helpful AgentHints for empty session, got %+v", out.AgentHints)
	}
}

func TestHandoffCreateManualAndInspectWithQualityReport(t *testing.T) {
	projectDir := t.TempDir()

	created, err := GetHandoff(context.Background(), HandoffOptions{
		Session:     "myproject",
		ProjectDir:  projectDir,
		Create:      true,
		Goal:        "Implemented canonical ORI --robot-handoff surface",
		Now:         "Run deadcode and contracts guards before commit",
		Description: "ori-robot-handoff",
		IncludeGit:  false,
		Next:        []string{"Verify golden diff"},
		Decisions:   map[string]string{"surface": "ori-handoff"},
	})
	if err != nil {
		t.Fatalf("GetHandoff create failed: %v", err)
	}
	if !created.Success {
		t.Fatalf("expected Success=true, got error=%q code=%q", created.Error, created.ErrorCode)
	}
	if created.Action != "handoff_create" {
		t.Fatalf("Action = %q, want handoff_create", created.Action)
	}
	if created.CreatedPath == "" || created.LatestPath != created.CreatedPath {
		t.Fatalf("unexpected CreatedPath=%q LatestPath=%q", created.CreatedPath, created.LatestPath)
	}
	if !strings.Contains(filepath.Base(created.CreatedPath), "ori-robot-handoff") {
		t.Fatalf("expected CreatedPath basename to include description slug, got %q", created.CreatedPath)
	}
	if created.Latest == nil {
		t.Fatal("expected Latest handoff to be populated")
	}
	if created.Latest.Status != "complete" || created.Latest.Outcome != "SUCCEEDED" {
		t.Fatalf("expected status=complete outcome=SUCCEEDED, got status=%q outcome=%q", created.Latest.Status, created.Latest.Outcome)
	}
	if created.Quality == nil || created.Quality.Score <= 0 || len(created.Quality.Dimensions) != 4 {
		t.Fatalf("expected QualityReport with Score>0 and 4 dimensions, got %+v", created.Quality)
	}
	if created.Ledger == nil || !created.Ledger.Exists || created.Ledger.Bytes <= 0 {
		t.Fatalf("expected continuity ledger to be written, got %+v", created.Ledger)
	}
	if len(created.Handoffs) != 1 || created.TotalReturned != 1 {
		t.Fatalf("expected 1 summary handoff, got len=%d total=%d", len(created.Handoffs), created.TotalReturned)
	}

	// Blocked manual handoff sets status=blocked and outcome=PARTIAL_MINUS.
	blocked, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "myproject",
		ProjectDir: projectDir,
		Create:     true,
		Goal:       "Investigated flaky integration",
		Now:        "Await upstream fixture fix",
		IncludeGit: false,
		Blockers:   []string{"Upstream fixture missing"},
	})
	if err != nil {
		t.Fatalf("GetHandoff blocked create failed: %v", err)
	}
	if !blocked.Success || blocked.Latest == nil {
		t.Fatalf("expected blocked create success, got %+v", blocked)
	}
	if blocked.Latest.Status != "blocked" || blocked.Latest.Outcome != "PARTIAL_MINUS" {
		t.Fatalf("expected blocked/PARTIAL_MINUS, got status=%q outcome=%q", blocked.Latest.Status, blocked.Latest.Outcome)
	}

	// Inspect concrete session returns fresh QualityReport and bounded history.
	inspected, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "myproject",
		ProjectDir: projectDir,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("GetHandoff inspect failed: %v", err)
	}
	if !inspected.Success {
		t.Fatalf("expected inspect success, got %+v", inspected)
	}
	if inspected.TotalReturned != 1 || len(inspected.Handoffs) != 1 {
		t.Fatalf("expected Limit=1 to cap Handoffs to 1 item, got %d", len(inspected.Handoffs))
	}
	if inspected.Quality == nil || inspected.Quality.Score <= 0 || len(inspected.Quality.Dimensions) != 4 {
		t.Fatalf("expected fresh QualityReport on inspect, got %+v", inspected.Quality)
	}
}

func TestHandoffCreateAutoAndListAll(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // keep external br/cass/tmux probes fast and deterministic

	// Also verify git enrichment path when git is available on system path; here hermetic PATH tests clean fallback.
	autoAlpha, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "alpha",
		ProjectDir: projectDir,
		Create:     true,
		Auto:       true,
		IncludeGit: true,
	})
	if err != nil {
		t.Fatalf("GetHandoff auto create failed: %v", err)
	}
	if !autoAlpha.Success {
		t.Fatalf("expected auto create success, got error=%q code=%q", autoAlpha.Error, autoAlpha.ErrorCode)
	}
	if autoAlpha.Latest == nil || strings.TrimSpace(autoAlpha.Latest.Goal) == "" || strings.TrimSpace(autoAlpha.Latest.Now) == "" {
		t.Fatalf("expected non-empty fallback Goal/Now for auto handoff, got %+v", autoAlpha.Latest)
	}
	if autoAlpha.Quality == nil || autoAlpha.Quality.Score <= 0 || len(autoAlpha.Quality.Dimensions) != 4 {
		t.Fatalf("expected 4-dimension QualityReport on auto handoff, got %+v", autoAlpha.Quality)
	}
	if len(autoAlpha.Handoffs) != 1 || !autoAlpha.Handoffs[0].IsAuto {
		t.Fatalf("expected IsAuto=true on auto handoff summary, got %+v", autoAlpha.Handoffs)
	}

	time.Sleep(15 * time.Millisecond)

	// Explicit Goal/Now override auto-generated values.
	autoBeta, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "beta",
		ProjectDir: projectDir,
		Create:     true,
		Auto:       true,
		Goal:       "Explicit override goal for beta",
		Now:        "Explicit override next step for beta",
		IncludeGit: false,
	})
	if err != nil {
		t.Fatalf("GetHandoff beta auto create failed: %v", err)
	}
	if !autoBeta.Success || autoBeta.Latest == nil {
		t.Fatalf("expected beta auto create success, got %+v", autoBeta)
	}
	if autoBeta.Latest.Goal != "Explicit override goal for beta" || autoBeta.Latest.Now != "Explicit override next step for beta" {
		t.Fatalf("expected explicit Goal/Now overrides to win, got goal=%q now=%q", autoBeta.Latest.Goal, autoBeta.Latest.Now)
	}

	// Inspect across all sessions ("all" and "*").
	for _, wildcard := range []string{"all", "*"} {
		allOut, err := GetHandoff(context.Background(), HandoffOptions{
			Session:    wildcard,
			ProjectDir: projectDir,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("GetHandoff(%q) failed: %v", wildcard, err)
		}
		if !allOut.Success {
			t.Fatalf("GetHandoff(%q) expected success, got %+v", wildcard, allOut)
		}
		if len(allOut.Sessions) != 2 {
			t.Fatalf("expected 2 discovered sessions, got %v", allOut.Sessions)
		}
		if len(allOut.Handoffs) != 2 || allOut.TotalReturned != 2 {
			t.Fatalf("expected 2 handoffs across sessions, got %+v", allOut.Handoffs)
		}
		if allOut.Latest == nil || allOut.Quality == nil {
			t.Fatalf("expected Latest and Quality populated for wildcard inspect, got latest=%+v quality=%+v", allOut.Latest, allOut.Quality)
		}
	}
}

func TestHandoffCreateAutoEnrichesGitRepository(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), gitBin, args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	runGit("init")
	tracked := filepath.Join(repoDir, "service.go")
	if err := os.WriteFile(tracked, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "service.go")
	runGit("commit", "-m", "initial commit")
	if err := os.WriteFile(tracked, []byte("package main\n// updated\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.Symlink(gitBin, filepath.Join(binDir, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	out, err := GetHandoff(context.Background(), HandoffOptions{
		Session:    "gitproj",
		ProjectDir: repoDir,
		Create:     true,
		Auto:       true,
		IncludeGit: true,
	})
	if err != nil {
		t.Fatalf("GetHandoff git auto failed: %v", err)
	}
	if !out.Success || out.Latest == nil {
		t.Fatalf("expected success, got %+v", out)
	}
	if len(out.Latest.Files.Modified) == 0 || out.Latest.Files.Modified[0] != "service.go" {
		t.Fatalf("expected modified file service.go in auto handoff, got %+v", out.Latest.Files)
	}
}

func TestHandoffValidationAndPathTraversalRefusal(t *testing.T) {
	validDir := t.TempDir()
	fileInDir := filepath.Join(validDir, "not-a-dir.txt")
	if err := os.WriteFile(fileInDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		opts HandoffOptions
	}{
		{
			name: "empty session",
			opts: HandoffOptions{Session: "   ", ProjectDir: validDir},
		},
		{
			name: "create with wildcard all",
			opts: HandoffOptions{Session: "all", ProjectDir: validDir, Create: true, Auto: true},
		},
		{
			name: "create with wildcard star",
			opts: HandoffOptions{Session: "*", ProjectDir: validDir, Create: true, Auto: true},
		},
		{
			name: "inspect path traversal dots",
			opts: HandoffOptions{Session: "../bad", ProjectDir: validDir},
		},
		{
			name: "create path traversal slash",
			opts: HandoffOptions{Session: "bad/session", ProjectDir: validDir, Create: true, Goal: "g", Now: "n"},
		},
		{
			name: "create path traversal backslash",
			opts: HandoffOptions{Session: `bad\session`, ProjectDir: validDir, Create: true, Goal: "g", Now: "n"},
		},
		{
			name: "manual create missing goal and now",
			opts: HandoffOptions{Session: "myproject", ProjectDir: validDir, Create: true},
		},
		{
			name: "manual create missing now",
			opts: HandoffOptions{Session: "myproject", ProjectDir: validDir, Create: true, Goal: "done"},
		},
		{
			name: "relative project dir",
			opts: HandoffOptions{Session: "myproject", ProjectDir: "relative/path"},
		},
		{
			name: "nonexistent project dir",
			opts: HandoffOptions{Session: "myproject", ProjectDir: filepath.Join(validDir, "missing-subdir")},
		},
		{
			name: "project dir points to file",
			opts: HandoffOptions{Session: "myproject", ProjectDir: fileInDir},
		},
		{
			name: "negative limit",
			opts: HandoffOptions{Session: "myproject", ProjectDir: validDir, Limit: -5},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := GetHandoff(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("GetHandoff returned unexpected Go error: %v", err)
			}
			if out.Success {
				t.Fatalf("expected Success=false for %s, got %+v", tc.name, out)
			}
			if out.ErrorCode != ErrCodeInvalidFlag {
				t.Fatalf("ErrorCode = %q, want %q (error=%q)", out.ErrorCode, ErrCodeInvalidFlag, out.Error)
			}
			if out.Sessions == nil || out.Handoffs == nil {
				t.Fatalf("expected non-nil Sessions and Handoffs slices on error, got sessions=%#v handoffs=%#v", out.Sessions, out.Handoffs)
			}
		})
	}
}

func TestHandoffPrintJSONAndRegistryContract(t *testing.T) {
	projectDir := t.TempDir()

	prevFormat := GetOutputFormat()
	SetOutputFormat(FormatJSON)
	t.Cleanup(func() { SetOutputFormat(prevFormat) })

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printErr := PrintHandoff(context.Background(), HandoffOptions{
		Session:    "printproj",
		ProjectDir: projectDir,
		Create:     true,
		Goal:       "Verified PrintHandoff JSON output",
		Now:        "Validate registry and schema bindings",
		IncludeGit: false,
	})
	_ = w.Close()
	os.Stdout = origStdout
	if printErr != nil {
		t.Fatalf("PrintHandoff returned error: %v", printErr)
	}

	var decoded HandoffOutput
	if err := json.NewDecoder(r).Decode(&decoded); err != nil {
		t.Fatalf("decode PrintHandoff stdout: %v", err)
	}
	_ = r.Close()
	if !decoded.Success || decoded.Action != "handoff_create" || decoded.Quality == nil {
		t.Fatalf("unexpected PrintHandoff payload: %+v", decoded)
	}

	surface, ok := GetRobotRegistry().Surface("handoff")
	if !ok {
		t.Fatal("expected 'handoff' surface in GetRobotRegistry()")
	}
	if surface.SchemaID != "ntm:robot:handoff:v1" || surface.SchemaType != "handoff" {
		t.Fatalf("unexpected surface schema metadata: id=%q type=%q", surface.SchemaID, surface.SchemaType)
	}

	schemaOut, err := GetSchema("handoff")
	if err != nil {
		t.Fatalf("GetSchema(handoff) failed: %v", err)
	}
	if !schemaOut.Success || schemaOut.Schema == nil {
		t.Fatalf("expected valid JSON schema for handoff, got %+v", schemaOut)
	}
	for _, prop := range []string{"action", "session", "handoffs", "total_returned", "quality", "latest", "ledger"} {
		if _, exists := schemaOut.Schema.Properties[prop]; !exists {
			t.Fatalf("expected property %q in handoff JSON schema, got keys=%v", prop, schemaOut.Schema.Properties)
		}
	}
}
