package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// A source session already exists. Only the explicitly selected destination
// may be queried or mutated. The fake executable records complete arguments.
func restoreTargetFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("NTM_TARGET_TEST_LOG", log)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "")
	bin := filepath.Join(dir, "tmux")
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_TARGET_TEST_LOG"
case "$1" in
  has-session)
    if [ "$NTM_TARGET_TEST_EXISTS" = 1 ]; then exit 0; fi
    case "$*" in *source*) exit 0 ;; esac
    echo "can't find session: recovery" >&2; exit 1 ;;
  list-windows) echo 0 ;;
  list-panes)
    printf '%%91_NTM_SEP_0_NTM_SEP_recovery__cc_1_NTM_SEP_claude_NTM_SEP_80_NTM_SEP_24_NTM_SEP_1_NTM_SEP_0_NTM_SEP_0_NTM_SEP_cc_NTM_SEP__NTM_SEP__NTM_SEP_0\n' ;;
  display-message) echo claude ;;
  capture-pane) printf '\342\235\257 \n' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_TMUX_BINARY", bin)
	old := tmux.DefaultClient
	tmux.DefaultClient = tmux.NewClient("")
	t.Cleanup(func() { tmux.DefaultClient = old })
	return log
}

func targetCheckpoint(t *testing.T) *Checkpoint {
	t.Helper()
	return &Checkpoint{
		Version: 1, ID: "cp-target", Name: "target-test", SessionName: "source",
		WorkingDir: t.TempDir(), CreatedAt: time.Now(), PaneCount: 1,
		Session: SessionState{Panes: []PaneState{{
			ID: "%11", Index: 0, WindowIndex: 0, Title: "source__cc_1",
			AgentType: "cc", Command: "claude",
		}}},
	}
}

func TestRestoreIntoTargetPreservesCheckpointAndSource(t *testing.T) {
	log := restoreTargetFixture(t)
	cp := targetCheckpoint(t)
	storage := &Storage{BaseDir: t.TempDir()}
	if err := storage.Save(cp); err != nil {
		t.Fatal(err)
	}
	file, err := storage.SaveScrollback(cp.SessionName, cp.ID, cp.Session.Panes[0].ID, "source recovery context")
	if err != nil {
		t.Fatal(err)
	}
	cp.Session.Panes[0].ScrollbackFile = file
	before, _ := json.Marshal(cp)
	r := NewRestorerWithStorage(storage)
	out, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{
		TargetSession: "recovery", SkipGitCheck: true, InjectContext: true,
	})
	if err != nil || out == nil || out.SessionName != "recovery" || out.SourceSession != "source" || out.Stage != "completed" {
		t.Fatalf("restore target failed: %+v, %v", out, err)
	}
	if !out.ContextInjected {
		t.Fatal("source scrollback was not restored into the destination")
	}
	after, _ := json.Marshal(cp)
	if string(before) != string(after) || r.sourceSession != "" || r.ctx != nil {
		t.Fatal("operation modified its source checkpoint or shared restorer")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "-t =source") || strings.Contains(string(calls), "-s source") || strings.Contains(string(calls), "source__cc_1") || strings.Contains(string(calls), "kill-session") {
		t.Fatalf("side-by-side restore touched the source session: %s", calls)
	}
	if !strings.Contains(string(calls), "recovery__cc_1") || !strings.Contains(string(calls), "new-session") {
		t.Fatalf("destination identity was not installed: %s", calls)
	}
	if _, err := os.Stat(filepath.Join(storage.BaseDir, "recovery")); !os.IsNotExist(err) {
		t.Fatalf("restore created/used a destination checkpoint namespace: %v", err)
	}
}

func TestRestoreTargetExistsRequiresForceOnDestination(t *testing.T) {
	log := restoreTargetFixture(t)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "1")
	cp := targetCheckpoint(t)
	r := NewRestorer()
	_, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery"})
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("occupied destination must be refused: %v", err)
	}
	out, err := r.RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery", Force: true})
	if err != nil || out.SessionName != "recovery" {
		t.Fatalf("authorized destination replacement failed: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "source") || strings.Count(string(calls), "kill-session") != 1 {
		t.Fatalf("force must replace only the selected destination: %s", calls)
	}
}

func TestRestoreTargetValidationBeforeIO(t *testing.T) {
	log := restoreTargetFixture(t)
	for _, target := range []string{" ", "../escape", "bad:name", "recovery.1", "bad\nname"} {
		_, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), targetCheckpoint(t), RestoreOptions{TargetSession: target, Force: true})
		if err == nil {
			t.Fatalf("invalid target %q accepted", target)
		}
	}
	cp := targetCheckpoint(t)
	cp.SessionName = "../bad-source"
	if _, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery"}); err == nil {
		t.Fatal("valid target masked invalid source namespace")
	}
	calls, _ := os.ReadFile(log)
	if len(calls) != 0 {
		t.Fatalf("invalid request invoked tmux: %s", calls)
	}
}

func TestRestoreTargetDryRunPreservesCustomTitles(t *testing.T) {
	log := restoreTargetFixture(t)
	cp := targetCheckpoint(t)
	cp.Session.Panes[0].Title = "custom title referring to source"
	out, err := NewRestorer().RestoreFromCheckpointContext(context.Background(), cp, RestoreOptions{TargetSession: "recovery", DryRun: true})
	if err != nil || out == nil || out.SessionName != "recovery" || out.SourceSession != "source" {
		t.Fatalf("dry run lost source/destination: %+v, %v", out, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Count(strings.TrimSpace(string(calls)), "\n") != 0 || !strings.HasPrefix(string(calls), "has-session ") {
		t.Fatalf("dry run mutated session: %s", calls)
	}
	if cp.Session.Panes[0].Title != "custom title referring to source" {
		t.Fatal("custom title was rewritten")
	}
}

func TestRestoreForceJoinsDestinationMonitorBeforeReplacement(t *testing.T) {
	log := restoreTargetFixture(t)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "1")
	oldStop := stopRestoreSessionMonitor
	t.Cleanup(func() { stopRestoreSessionMonitor = oldStop })
	entered, release := make(chan string, 1), make(chan struct{})
	stopRestoreSessionMonitor = func(ctx context.Context, session string) error {
		entered <- session
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	beforeStop := make(chan RestoreProgress, 1)
	ctx = WithRestoreProgress(ctx, func(_ context.Context, event RestoreProgress) error {
		if event.Stage == "stop_session" && event.Phase == "before" {
			beforeStop <- event
		}
		return nil
	})
	cp := targetCheckpoint(t)
	done := make(chan error, 1)
	completed := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-completed
	})
	go func() {
		defer close(completed)
		_, err := NewRestorer().RestoreFromCheckpointContext(ctx, cp, RestoreOptions{TargetSession: "recovery", Force: true, SkipGitCheck: true})
		done <- err
	}()
	select {
	case session := <-entered:
		if session != "recovery" {
			t.Fatalf("stopped %q, want selected destination recovery", session)
		}
	case err := <-done:
		t.Fatalf("restore returned before monitor stop: %v", err)
	case <-ctx.Done():
		t.Fatal("restore never reached monitor stop")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") {
		t.Fatalf("replacement began before monitor joined: %s", calls)
	}
	select {
	case <-beforeStop:
	default:
		t.Fatal("monitor stop must follow the durable before event")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("restore after monitor joined: %v", err)
	}
	calls, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	kill, create := strings.Index(string(calls), "kill-session -t =recovery"), strings.Index(string(calls), "new-session")
	if kill < 0 || create <= kill || strings.Contains(string(calls), "-t =source") {
		t.Fatalf("restore did not replace only the destination after joining: %s", calls)
	}
}

func TestRestoreForceMonitorStopHonorsPreviewCancellationAndJournal(t *testing.T) {
	stopFailure := errors.New("resident cleanup is incomplete")
	journalFailure := errors.New("journal write unavailable")
	for _, tc := range []struct {
		name      string
		dryRun    bool
		stopError error
		reject    bool
		cancel    bool
		wantError error
		wantStops int
	}{
		{name: "preview", dryRun: true},
		{name: "resident failure", stopError: stopFailure, wantError: stopFailure, wantStops: 1},
		{name: "journal before failure", reject: true, wantError: journalFailure},
		{name: "canceled during journal before write", cancel: true, wantError: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := restoreTargetFixture(t)
			t.Setenv("NTM_TARGET_TEST_EXISTS", "1")
			oldStop := stopRestoreSessionMonitor
			t.Cleanup(func() { stopRestoreSessionMonitor = oldStop })
			stops := 0
			stopRestoreSessionMonitor = func(_ context.Context, session string) error {
				stops++
				if session != "recovery" {
					t.Errorf("stopped %q, want recovery", session)
				}
				return tc.stopError
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx = WithRestoreProgress(ctx, func(_ context.Context, event RestoreProgress) error {
				if event.Stage == "stop_session" && event.Phase == "before" {
					if tc.cancel {
						cancel()
					}
					if tc.reject {
						return journalFailure
					}
				}
				return nil
			})
			_, err := NewRestorer().RestoreFromCheckpointContext(ctx, targetCheckpoint(t), RestoreOptions{
				TargetSession: "recovery", Force: true, DryRun: tc.dryRun, SkipGitCheck: true,
			})
			if !errors.Is(err, tc.wantError) || stops != tc.wantStops {
				t.Fatalf("restore error=%v stops=%d, want %v and %d", err, stops, tc.wantError, tc.wantStops)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "kill-session") || strings.Contains(string(calls), "new-session") {
				t.Fatalf("restore mutated topology despite an unfulfilled shutdown boundary: %s", calls)
			}
		})
	}
}

func TestRestoreRemoteTargetKeepsSameNamedLocalMonitor(t *testing.T) {
	logPath := restoreTargetFixture(t)
	t.Setenv("NTM_TARGET_TEST_EXISTS", "1")
	dir := filepath.Dir(logPath)
	const sshScript = `#!/bin/sh
if [ "$1" != -- ] || [ "$2" != operator@remote.example ]; then exit 2; fi
printf 'remote-host %s\n' "$2" >> "$NTM_TARGET_TEST_LOG"
exec /bin/sh -c "$3"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(sshScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmux.DefaultClient = tmux.NewClient("operator@remote.example")
	oldStop := stopRestoreSessionMonitor
	t.Cleanup(func() { stopRestoreSessionMonitor = oldStop })
	stops := 0
	stopRestoreSessionMonitor = func(context.Context, string) error {
		stops++
		return errors.New("same-named local resident must remain running")
	}
	result, err := NewRestorer().RestoreFromCheckpointContext(t.Context(), targetCheckpoint(t), RestoreOptions{
		TargetSession: "recovery", Force: true, SkipGitCheck: true,
	})
	if err != nil || result == nil || result.SessionName != "recovery" || stops != 0 {
		t.Fatalf("remote restore touched local monitor: result=%+v err=%v local stops=%d", result, err, stops)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "remote-host operator@remote.example") ||
		!strings.Contains(string(calls), "kill-session -t =recovery") || !strings.Contains(string(calls), "new-session") {
		t.Fatalf("remote destination was not replaced: %s", calls)
	}
}
