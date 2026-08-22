package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestLivenessFromPanes(t *testing.T) {
	isLive := livenessFromPanes([]tmux.Pane{
		{ID: "%5", PID: 4242},
		{ID: "%6", PID: 0}, // pid unknown on the tmux side
		{ID: ""},           // malformed entry is ignored
	})

	cases := []struct {
		name        string
		paneID      string
		recordedPID int
		want        bool
	}{
		{"present, pid matches", "%5", 4242, true},
		{"present, no recorded pid", "%5", 0, true},
		{"present, recorded pid differs", "%5", 1, false},
		{"present, tmux pid unknown", "%6", 99, true},
		{"absent", "%9", 0, false},
		{"empty id", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLive(tc.paneID, tc.recordedPID); got != tc.want {
				t.Fatalf("isLive(%q, %d) = %v, want %v", tc.paneID, tc.recordedPID, got, tc.want)
			}
		})
	}
}

func TestNextPaneIndices_CountsRetitledLivePaneViaRegistry(t *testing.T) {
	// ntm#256 reproduction: pane %5 was spawned as sess__cc_1 and then
	// retitled. Title parsing alone yields no cc index; the registry knows
	// %5 holds sess__cc_1 and %5 is live, so the next cc index must be 2.
	panes := []tmux.Pane{
		{ID: "%4", PID: 1, Title: "sess__user_0"},
		{ID: "%5", PID: 4242, Title: "my custom title"},
		{ID: "%6", PID: 6, Title: "sess__cod_1"},
	}
	registry := agentmail.NewSessionAgentRegistry("sess", "/proj")
	registry.AddAgent("sess__cc_1", "%5", "GreenCastle")
	registry.SetPanePID("%5", 4242)

	got := nextPaneIndices(panes, registry)
	if got["cc"] != 1 {
		t.Fatalf("cc max index = %d, want 1 (occupied by retitled live pane)", got["cc"])
	}
	if got["cod"] != 1 {
		t.Fatalf("cod max index = %d, want 1 (from live title)", got["cod"])
	}
}

func TestNextPaneIndices_IgnoresDeadRegistryEntries(t *testing.T) {
	// A registry entry whose pane is gone (or re-incarnated with a new pid)
	// must not reserve a slot: same-session respawn keeps its low numbers.
	panes := []tmux.Pane{
		{ID: "%8", PID: 8, Title: "sess__user_0"},
		{ID: "%5", PID: 777, Title: "something else"}, // %5 reused by a new process
	}
	registry := agentmail.NewSessionAgentRegistry("sess", "/proj")
	registry.AddAgent("sess__cc_3", "%5", "GreenCastle")
	registry.SetPanePID("%5", 4242)
	registry.AddAgent("sess__cod_7", "%2", "BlueLake") // pane absent

	got := nextPaneIndices(panes, registry)
	if got["cc"] != 0 || got["cod"] != 0 {
		t.Fatalf("dead registry entries reserved slots: %v", got)
	}
}

func TestNextPaneIndices_NilRegistryAndTitleMax(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%1", Title: "sess__cc_1"},
		{ID: "%2", Title: "sess__cc_3_opus"},
		{ID: "%3", Title: "sess__cc_2[api]"},
		{ID: "%4", Title: "not an ntm title"},
	}
	got := nextPaneIndices(panes, nil)
	want := map[string]int{"cc": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nextPaneIndices(nil registry) = %v, want %v", got, want)
	}
}

func TestIdentityPublishKeys(t *testing.T) {
	if got := identityPublishKeys("", ""); len(got) != 0 {
		t.Fatalf("empty inputs produced keys: %v", got)
	}

	// Non-existent paths cannot be symlink-resolved and must not duplicate.
	session := filepath.Join(string(os.PathSeparator), "nonexistent-ntm-257", "proj")
	if got := identityPublishKeys(session, ""); !reflect.DeepEqual(got, []string{session}) {
		t.Fatalf("plain spawn keys = %v, want [%s]", got, session)
	}
	if got := identityPublishKeys(session, session); !reflect.DeepEqual(got, []string{session}) {
		t.Fatalf("pane dir equal to session key must dedupe, got %v", got)
	}

	worktree := filepath.Join(session, ".ntm", "worktrees", "sess", "cc-1")
	got := identityPublishKeys(session, worktree)
	want := []string{session, worktree}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worktree keys = %v, want %v", got, want)
	}
	if got[0] != session {
		t.Fatalf("session key must come first, got %v", got)
	}
}

func TestIdentityPublishKeys_ResolvesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on windows")
	}
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	realProj := filepath.Join(real, "proj")
	if err := os.MkdirAll(realProj, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "alias")
	if err := os.Symlink(realProj, link); err != nil {
		t.Fatal(err)
	}

	got := identityPublishKeys(link, "")
	want := []string{link, realProj}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symlinked session keys = %v, want %v", got, want)
	}

	// Canonical input yields a single key.
	if got := identityPublishKeys(realProj, ""); !reflect.DeepEqual(got, []string{realProj}) {
		t.Fatalf("canonical session keys = %v, want [%s]", got, realProj)
	}
}
