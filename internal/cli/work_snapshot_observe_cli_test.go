package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/worksource"
)

// Native integration coverage: the real Cobra command, state store/migrations,
// canonical JSONL verifier, durable adapter and tracker subprocesses. The helper
// only supplies deterministic external br/bv responses and an unavailable
// optional Agent Mail endpoint. No production collection function is replaced.
func workObservationCommandFixture(t *testing.T) (project, source, log string, initial []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture executables require /bin/sh")
	}
	project, bin, home := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, home)
	}
	t.Setenv("NTM_CONFIG", filepath.Join(home, "config.toml"))
	t.Setenv("AGENT_MAIL_URL", "http://127.0.0.1:1/mcp/")
	t.Setenv("AGENT_MAIL_TOKEN", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bv.InvalidateTriageCache()
	t.Cleanup(bv.InvalidateTriageCache)
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(project, "tracker-calls")
	t.Setenv("NTM_WORK_OBSERVATION_TEST_LOG", log)
	br := `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_WORK_OBSERVATION_TEST_LOG"
case "$*" in
  *stats*) printf '%s\n' '{"summary":{"total_issues":3,"open_issues":3,"in_progress_issues":0,"blocked_issues":0,"ready_issues":3,"closed_issues":0}}' ;;
  *ready*) printf '%s\n' '[{"id":"a","title":"A","priority":1},{"id":"b","title":"B","priority":2},{"id":"c","title":"C","priority":3}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	for name, script := range map[string]string{"br": br, "bv": "#!/bin/sh\nprintf '%s\\n' '{\"triage\":{\"recommendations\":[]}}'\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	source = filepath.Join(project, ".beads", "issues.jsonl")
	initial = []byte("{\"id\":\"a\",\"status\":\"open\",\"labels\":[\"mutex:db\"]}\n{\"id\":\"b\",\"status\":\"open\",\"labels\":[\"mutex:db\"]}\n{\"id\":\"c\",\"status\":\"open\",\"labels\":[\"mutex:docs\"]}\n")
	if err := os.WriteFile(source, initial, 0600); err != nil {
		t.Fatal(err)
	}
	return project, source, log, initial
}

func executeWorkObservationCommand(t *testing.T, ctx context.Context, out io.Writer, args ...string) error {
	t.Helper()
	cmd := newWorkSnapshotCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	return cmd.ExecuteContext(ctx)
}

func decodeWorkObservationReplies(t *testing.T, data []byte) []workSnapshotReply {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var replies []workSnapshotReply
	for {
		var reply workSnapshotReply
		if err := decoder.Decode(&reply); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("invalid JSON stream %q: %v", data, err)
		}
		replies = append(replies, reply)
	}
	return replies
}

func TestWorkReadyCommandUsesCompleteMutexFilteredCount(t *testing.T) {
	project, source, log, initial := workObservationCommandFixture(t)
	var output bytes.Buffer
	err := executeWorkObservationCommand(t, context.Background(), &output,
		"--project", project, "--wait-ready=2", "--limit=1", "--wait-timeout=5s")
	replies := decodeWorkObservationReplies(t, output.Bytes())
	if err != nil || len(replies) != 1 {
		t.Fatalf("wait output=%s err=%v", output.String(), err)
	}
	r := replies[0]
	if !r.Success || r.Observation == nil || !r.Observation.Matched || r.Work == nil || r.Work.Summary.Ready != 2 || len(r.Work.Ready) != 1 {
		t.Fatalf("wait did not use the complete conflict-free count: %+v", r)
	}
	before, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	err = executeWorkObservationCommand(t, context.Background(), &output,
		"--project", project, "--wait-ready=2", "--limit=1", "--require-reservations", "--wait-timeout=150ms", "--interval=100ms")
	replies = decodeWorkObservationReplies(t, output.Bytes())
	if !errors.Is(err, context.DeadlineExceeded) || len(replies) != 1 || replies[0].Success || replies[0].ErrorCode != "TIMEOUT" || replies[0].Work != nil {
		t.Fatalf("missing live reservations satisfied strict wait: %s %v", output.String(), err)
	}
	after, err := os.ReadFile(log)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cached wait reran tracker tools")
	}
	unchanged, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(initial, unchanged) {
		t.Fatal("wait changed tracker ownership or source")
	}
}

type workObservationCallbackWriter struct {
	bytes.Buffer
	onRecord func(int) error
	records  int
}

func (w *workObservationCallbackWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if err != nil {
		return n, err
	}
	count := bytes.Count(w.Buffer.Bytes(), []byte{'\n'})
	for w.records < count {
		w.records++
		if err := w.onRecord(w.records); err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestWorkWatchCommandReportsSourceFailureAndResumesWithoutImplicitRefresh(t *testing.T) {
	project, source, log, initial := workObservationCommandFixture(t)
	var warm bytes.Buffer
	if err := executeWorkObservationCommand(t, context.Background(), &warm, "--project", project, "--limit=1"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	writer := &workObservationCallbackWriter{}
	writer.onRecord = func(n int) error {
		switch n {
		case 1:
			return os.WriteFile(source, []byte("{\"id\":\"a\",\"status\":\"closed\"}\n"), 0600)
		case 2:
			return os.WriteFile(source, initial, 0600)
		case 3:
			cancel()
		}
		return nil
	}
	err = executeWorkObservationCommand(t, ctx, writer, "--project", project, "--limit=1", "--watch", "--interval=100ms")
	replies := decodeWorkObservationReplies(t, writer.Bytes())
	if !errors.Is(err, context.Canceled) || len(replies) != 4 {
		t.Fatalf("watch did not terminate cleanly: %s %v", writer.String(), err)
	}
	if !replies[0].Success || replies[1].Success || replies[1].ErrorCode != worksource.StaleCode || replies[1].Work != nil || !replies[2].Success || replies[3].ErrorCode != "CANCELLED" || !replies[3].Observation.Terminal {
		t.Fatalf("watch replayed work through a failed source read: %s", writer.String())
	}
	if replies[0].Work.Verification.CacheCollectedAt != replies[2].Work.Verification.CacheCollectedAt || !replies[2].Work.Verification.FromCache {
		t.Fatal("watch extended freshness or replaced the original collection")
	}
	for i, reply := range replies {
		if reply.Observation == nil || reply.Observation.Sequence != uint64(i+1) {
			t.Fatalf("invalid event sequence: %+v", reply)
		}
	}
	after, err := os.ReadFile(log)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("watch performed an implicit live tracker refresh")
	}
}

func TestWorkObservationCommandRejectsInvalidModeFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--watch", "--wait-ready=1"}, {"--wait-ready=0"}, {"--wait-ready=-1"}, {"--wait-ready=100001"},
		{"--interval=1s"}, {"--watch", "--interval=0"}, {"--watch", "--interval=1m"},
		{"--wait-timeout=1m"}, {"--wait-ready=1", "--wait-timeout=0"}, {"--wait-ready=1", "--wait-timeout=25h"},
		{"--require-reservations"}, {"--watch", "--require-reservations"},
	} {
		var output bytes.Buffer
		if err := executeWorkObservationCommand(t, context.Background(), &output, args...); err == nil {
			t.Fatalf("accepted invalid flags: %v", args)
		}
		if output.Len() != 0 {
			t.Fatalf("invalid flags reached collection/output: %v %s", args, output.String())
		}
	}
}

func TestWorkObservationReplyPreservesCancellationClassification(t *testing.T) {
	cause := fmt.Errorf("collection ended: %w", errors.Join(&worksource.StaleError{Reason: "source read failed"}, context.Canceled))
	reply, err := makeWorkSnapshotReply("/project", observationWork(999, 1), cause, &workObservationInfo{Terminal: true})
	if reply.Success || reply.Work != nil || reply.ErrorCode != "CANCELLED" || !errors.Is(err, context.Canceled) || !errors.Is(err, worksource.ErrStale) {
		t.Fatalf("reply lost failure classification or exposed failed ready work: %+v %v", reply, err)
	}
}
