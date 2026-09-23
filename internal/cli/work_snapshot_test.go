package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// This exercises the real command, SQLite migrations, adapter, br/bv subprocess
// boundary and restart read. It requires the normal repository dependencies,
// not the focused snapshot verification harness.
func TestWorkSnapshotCommandReopensAndRequiresExplicitRefresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture executables require /bin/sh")
	}
	project, bin, configDir := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_CONFIG", filepath.Join(configDir, "config.toml"))
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bv.InvalidateTriageCache()
	t.Cleanup(bv.InvalidateTriageCache)
	logPath := filepath.Join(project, "tracker-calls")
	t.Setenv("NTM_WORK_SNAPSHOT_TEST_LOG", logPath)
	br := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_WORK_SNAPSHOT_TEST_LOG"
case "$*" in
  *stats*) printf '%s\n' '{"summary":{"total_issues":3,"open_issues":3,"in_progress_issues":0,"blocked_issues":1,"ready_issues":3,"closed_issues":0}}' ;;
  *ready*) printf '%s\n' '[{"id":"blocked","title":"Blocked task","priority":1},{"id":"a","title":"Task A","priority":2},{"id":"b","title":"Task B","priority":2}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	for name, script := range map[string]string{"br": br, "bv": "#!/bin/sh\nprintf '%s\\n' '{\"triage\":{\"recommendations\":[]}}'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(project, ".beads", "issues.jsonl")
	initial := `{"id":"blocked","status":"open","dependencies":[{"depends_on_id":"missing","type":"blocks"}]}` + "\n" + `{"id":"a","status":"open"}` + "\n" + `{"id":"b","status":"open"}` + "\n"
	if err := os.WriteFile(source, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	type reply struct {
		Success   bool                  `json:"success"`
		ErrorCode string                `json:"error_code"`
		Work      *adapters.WorkSection `json:"work"`
	}
	run := func(refresh bool) (reply, error) {
		t.Helper()
		cmd := newWorkSnapshotCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetContext(context.Background())
		args := []string{"--project", project, "--limit", "1"}
		if refresh {
			args = append(args, "--refresh")
		}
		cmd.SetArgs(args)
		var buffer bytes.Buffer
		cmd.SetOut(&buffer)
		err := cmd.Execute()
		var out reply
		if decodeErr := json.Unmarshal(buffer.Bytes(), &out); decodeErr != nil {
			t.Fatalf("invalid command JSON %q: %v (command error: %v)", buffer.String(), decodeErr, err)
		}
		return out, err
	}
	assertReady := func(out reply, err error, total int, cached bool) {
		t.Helper()
		if err != nil || !out.Success || out.Work == nil || !out.Work.Available || out.Work.Summary.Ready != total || len(out.Work.Ready) != 1 || out.Work.Verification.FromCache != cached {
			t.Fatalf("command output=%+v work=%+v error=%v", out, out.Work, err)
		}
	}
	first, err := run(false)
	assertReady(first, err, 2, false)
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := run(false)
	assertReady(second, err, 2, true)
	after, err := os.ReadFile(logPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reopened cache reran tracker tools")
	}
	changed := `{"id":"blocked","status":"closed"}` + "\n" + `{"id":"a","status":"closed"}` + "\n" + `{"id":"b","status":"open"}` + "\n"
	if err := os.WriteFile(source, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	stale, err := run(false)
	if err == nil || stale.Success || stale.ErrorCode != worksource.StaleCode {
		t.Fatalf("stale cache was silently refreshed: %+v %v", stale, err)
	}
	after, _ = os.ReadFile(logPath)
	if !bytes.Equal(before, after) {
		t.Fatal("source mismatch executed tracker tools without explicit refresh")
	}
	fresh, err := run(true)
	assertReady(fresh, err, 1, false)
	if fresh.Work.Ready[0].ID != "b" {
		t.Fatal("refresh advertised closed work")
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != changed {
		t.Fatal("command repaired or rewrote the tracker")
	}
}

func TestWorkSnapshotCommandIsRegisteredAndRejectsInvalidLimits(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"work-snapshot"})
	if err != nil || cmd == nil || cmd.Name() != "work-snapshot" {
		t.Fatalf("command is not reachable: %v", err)
	}
	for _, args := range [][]string{{"--limit=0"}, {"--limit=-1"}, {"--limit=100001"}, {"--timeout=0"}, {"--timeout=2m"}} {
		cmd := newWorkSnapshotCmd()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid flags: %v", args)
		}
	}
}
