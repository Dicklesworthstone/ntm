package serve

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestJobJournalRoundTripAndInterruptedRecovery(t *testing.T) {
	dir := t.TempDir()
	journal, err := openJobJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	statuses := []JobStatus{JobStatusPending, JobStatusRunning, JobStatusCompleted, JobStatusFailed, JobStatusCancelled}
	for i, status := range statuses {
		job := &Job{ID: fmt.Sprintf("job-%d", i), Type: "swarm_spawn", Status: status, CreatedAt: now.Add(time.Duration(i) * time.Second).Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339), Error: "original reason", Result: map[string]interface{}{"session": "existing", "agents": []interface{}{map[string]interface{}{"pane": "%7"}}}}
		if err := journal.save(job); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, job.ID+".json"))
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("job file exposes private evidence: %v %v", info, err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = openJobJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	jobs, err := journal.recover(now.Add(time.Hour))
	if err != nil || len(jobs) != len(statuses) {
		t.Fatalf("recover = %+v, %v", jobs, err)
	}
	for i, job := range jobs {
		index := len(statuses) - 1 - i
		if job.ID != fmt.Sprintf("job-%d", index) || job.Result["session"] != "existing" {
			t.Fatalf("identity, result, or deterministic order lost: %+v", job)
		}
		if index < 2 {
			if job.Status != JobStatusFailed || job.Result["outcome_unknown"] != true || job.Result["interrupted"] != true || !strings.Contains(job.Error, "not automatically replayed") {
				t.Fatalf("interrupted operation was hidden or replayable: %+v", job)
			}
		} else if job.Status != statuses[index] || job.Error != "original reason" {
			t.Fatalf("recovery rewrote a terminal outcome: %+v", job)
		}
	}
	again, err := journal.recover(now.Add(2 * time.Hour))
	if err != nil || !reflect.DeepEqual(jobs, again) {
		t.Fatalf("recovery is not idempotent: %v\n%+v\n%+v", err, jobs, again)
	}
}

func TestJobJournalFailedSaveKeepsLastCheckpoint(t *testing.T) {
	journal, err := openJobJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	job := &Job{ID: "stable", Status: JobStatusRunning, Result: map[string]interface{}{"session": "keep"}}
	if err := journal.save(job); err != nil {
		t.Fatal(err)
	}
	job.Result["bad"] = make(chan int)
	if err := journal.save(job); err == nil {
		t.Fatal("unserializable result accepted")
	}
	jobs, err := journal.load()
	if err != nil || len(jobs) != 1 || jobs[0].Result["session"] != "keep" || jobs[0].Status != JobStatusRunning {
		t.Fatalf("last checkpoint destroyed: %+v %v", jobs, err)
	}
	job.Result = map[string]interface{}{"huge": strings.Repeat("x", jobJournalMaxRecordBytes)}
	if err := journal.save(job); err == nil {
		t.Fatal("oversized result accepted")
	}
	jobs, err = journal.load()
	if err != nil || jobs[0].Result["session"] != "keep" {
		t.Fatalf("oversized write destroyed checkpoint: %+v %v", jobs, err)
	}
}

func TestJobJournalRejectsInvalidHistory(t *testing.T) {
	cases := []struct{ name, data string }{
		{"truncated", `{"version":1,"job":`},
		{"new version", `{"version":2,"job":{"id":"job","status":"pending"}}`},
		{"wrong identity", `{"version":1,"job":{"id":"other","status":"pending"}}`},
		{"nil record", `{"version":1,"job":null}`},
		{"unknown status", `{"version":1,"job":{"id":"job","status":"pretend"}}`},
		{"trailing data", `{"version":1,"job":{"id":"job","status":"pending"}} {}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			journal, err := openJobJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			path := filepath.Join(journal.dir, "job.json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if jobs, err := journal.recover(time.Now()); err == nil || jobs != nil {
				t.Fatalf("unreadable history became a healthy view: %+v %v", jobs, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != tc.data {
				t.Fatalf("recovery destroyed unreadable evidence: %q %v", data, err)
			}
		})
	}
}

func TestJobJournalRejectsTraversalAndIgnoresUncommittedTemps(t *testing.T) {
	journal, err := openJobJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	for _, id := range []string{"", "..", "../escape", "/absolute", `bad\path`, "a.b", strings.Repeat("a", 201)} {
		if err := journal.save(&Job{ID: id}); err == nil {
			t.Errorf("accepted bad identity %q", id)
		}
	}
	if err := os.WriteFile(filepath.Join(journal.dir, ".job-interrupted.tmp"), []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if jobs, err := journal.recover(time.Now()); err != nil || len(jobs) != 0 {
		t.Fatalf("uncommitted temp affected recovery: %+v %v", jobs, err)
	}
}

func TestJobJournalExclusiveOwnershipAndLiveWorkerFence(t *testing.T) {
	dir := t.TempDir()
	journal, err := openJobJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openJobJournal(dir); err == nil {
		second.Close()
		t.Fatal("two servers own the same execution journal")
	}
	release, err := lockJobJournal(filepath.Join(dir, "job.worker.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := journal.save(&Job{ID: "job", Status: JobStatusRunning}); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = openJobJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.recover(time.Now()); err == nil {
		t.Fatal("live worker was relabeled as interrupted")
	}
	jobs, err := journal.load()
	if err != nil || jobs[0].Status != JobStatusRunning {
		t.Fatalf("failed recovery modified active state: %+v %v", jobs, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if jobs, err := journal.recover(time.Now()); err != nil || jobs[0].Status != JobStatusFailed {
		t.Fatalf("abandoned worker could not be recovered: %+v %v", jobs, err)
	}
}

func TestJobJournalProcessExitReleasesOwnership(t *testing.T) {
	if dir := os.Getenv("NTM_JOB_JOURNAL_EXIT_FIXTURE"); dir != "" {
		journal, err := openJobJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.save(&Job{ID: "crashed", Status: JobStatusRunning}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Deliberately bypass Close: only the kernel releases ownership.
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestJobJournalProcessExitReleasesOwnership$")
	cmd.Env = append(os.Environ(), "NTM_JOB_JOURNAL_EXIT_FIXTURE="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	journal, err := openJobJournal(dir)
	if err != nil {
		t.Fatalf("dead process left a stale lock: %v", err)
	}
	defer journal.Close()
	jobs, err := journal.recover(time.Now())
	if err != nil || len(jobs) != 1 || jobs[0].Result["outcome_unknown"] != true {
		t.Fatalf("crash recovery: %+v %v", jobs, err)
	}
}

func TestJobJournalDrainCancelsAndWaitsForWorkers(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	job := srv.jobStore.Create("swarm_spawn")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.jobStore.SetCancel(job.ID, cancel)
	done := make(chan error, 1)
	go func() { done <- srv.drainJobWorkers(context.Background()) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not signal actual worker")
	}
	select {
	case err := <-done:
		t.Fatalf("ownership released before final checkpoint: %v", err)
	default:
	}
	srv.jobStore.ClearCancel(job.ID)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after worker checkpoint")
	}
	if got := srv.jobStore.Get(job.ID); got.Status != JobStatusCancelled || got.Error != "server shutting down" {
		t.Fatalf("shutdown status lost: %+v", got)
	}
}

func TestJobJournalDrainRespectsDeadline(t *testing.T) {
	srv := &Server{jobStore: NewJobStore()}
	job := srv.jobStore.Create("swarm_spawn")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.jobStore.SetCancel(job.ID, cancel)
	deadline, stop := context.WithCancel(context.Background())
	stop()
	if err := srv.drainJobWorkers(deadline); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown deadline lost: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("expired shutdown skipped cancellation signaling")
	}
	srv.jobStore.ClearCancel(job.ID)
}

func progressJournalServer(t *testing.T) (*Server, *jobJournal) {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := &Server{stateStore: store, jobStore: NewJobStore()}
	dir, err := srv.jobJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	journal, err := openJobJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	return srv, journal
}

func TestJobProgressCheckpointIsImmutableAndDurable(t *testing.T) {
	srv, journal := progressJournalServer(t)
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.jobStore.Update(job.ID, JobStatusRunning, 0, nil, "")
	input := map[string]interface{}{"session": "partial", "agents": []interface{}{map[string]interface{}{"pane": "%7"}}}
	snapshot, err := srv.checkpointJobProgress(job.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input["agents"].([]interface{})[0].(map[string]interface{})["pane"] = "changed"
	if snapshot["agents"].([]interface{})[0].(map[string]interface{})["pane"] != "%7" {
		t.Fatal("published progress aliases the producer")
	}
	jobs, err := journal.load()
	if err != nil || len(jobs) != 1 || jobs[0].Status != JobStatusRunning || jobs[0].Result["_execution_in_progress"] != true || jobs[0].Result["session"] != "partial" {
		t.Fatalf("running evidence was not flushed: %+v %v", jobs, err)
	}
	// DELETE can win while the engine is still returning a useful receipt.
	srv.jobStore.Update(job.ID, JobStatusCancelled, 0, nil, "cancelled by user")
	if _, err := srv.checkpointJobProgress(job.ID, snapshot); err != nil {
		t.Fatal(err)
	}
	final := finishJobProgress(snapshot, map[string]interface{}{"session": "partial", "monitor_pid": 42})
	srv.jobStore.retainCancelledResult(job.ID, final)
	got := srv.jobStore.Get(job.ID)
	if got.Status != JobStatusCancelled || got.Error != "cancelled by user" || got.Result["monitor_pid"] != 42 || got.Result["_execution_in_progress"] != nil {
		t.Fatalf("late final result did not replace progress safely: %+v", got)
	}
	srv.jobStore.retainCancelledResult(job.ID, map[string]interface{}{"monitor_pid": 99})
	if srv.jobStore.Get(job.ID).Result["monitor_pid"] != 42 {
		t.Fatal("a final cancellation receipt was replaced")
	}
}

func TestJobProgressPanicRetainsCreatedAgent(t *testing.T) {
	srv, journal := progressJournalServer(t)
	srv.spawnAgents = func(ctx context.Context, _ robot.SpawnOptions) (*robot.SpawnOutput, error) {
		if err := reportJobProgress(ctx, map[string]interface{}{"session": "partial", "agent_pane": "%7"}); err != nil {
			return nil, err
		}
		panic("failure after first launch")
	}
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.dispatchJob(job.ID, CreateJobRequest{Type: JobTypeSwarmSpawn, Params: map[string]interface{}{"session": "partial", "cc_count": 2}})
	got := srv.jobStore.Get(job.ID)
	if got.Status != JobStatusFailed || !strings.Contains(got.Error, "failure after first launch") || got.Result["agent_pane"] != "%7" || got.Result["_execution_in_progress"] != nil {
		t.Fatalf("panic erased recovery evidence: %+v", got)
	}
	jobs, err := journal.load()
	if err != nil || len(jobs) != 1 || jobs[0].Result["agent_pane"] != "%7" || jobs[0].Status != JobStatusFailed {
		t.Fatalf("panic receipt did not survive to disk: %+v %v", jobs, err)
	}
}

func TestJobProgressRecoveryClearsDeadWorkerMarker(t *testing.T) {
	for _, status := range []JobStatus{JobStatusRunning, JobStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			journal, err := openJobJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			if err := journal.save(&Job{ID: "interrupted", Status: status, Error: "original reason", Result: map[string]interface{}{
				"_execution_in_progress": true, "agent_pane": "%7",
			}}); err != nil {
				t.Fatal(err)
			}
			jobs, err := journal.recover(time.Now())
			if err != nil || len(jobs) != 1 || jobs[0].Result["_execution_in_progress"] != nil || jobs[0].Result["outcome_unknown"] != true || jobs[0].Result["agent_pane"] != "%7" {
				t.Fatalf("recovery invented a live/completed worker: %+v %v", jobs, err)
			}
			if status == JobStatusCancelled && (jobs[0].Status != status || jobs[0].Error != "original reason") {
				t.Fatalf("recovery changed operator cancellation: %+v", jobs[0])
			}
			again, err := journal.recover(time.Now().Add(time.Hour))
			if err != nil || !reflect.DeepEqual(jobs, again) {
				t.Fatalf("progress recovery was not idempotent: %+v %v", again, err)
			}
		})
	}
}

func TestSpawnJobProgressObserverStopsOnLostCheckpoint(t *testing.T) {
	srv, journal := progressJournalServer(t)
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.jobStore.Update(job.ID, JobStatusRunning, 0, nil, "")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var reports, launches int
	diskErr := errors.New("disk unavailable")
	ctx = context.WithValue(ctx, jobProgressContextKey{}, jobProgressReporter(func(result map[string]interface{}) error {
		reports++
		if reports == 2 {
			return diskErr
		}
		_, err := srv.checkpointJobProgress(job.ID, result)
		return err
	}))
	opts := robot.WithSpawnProgress(robot.SpawnOptions{LifecycleDeps: &robot.SpawnLifecycleDependencies{
		LaunchAgent: func(context.Context, tmux.Pane, string, string, int, string, string) (robot.SpawnedAgent, error) {
			launches++
			return robot.SpawnedAgent{Pane: "0.1"}, nil
		},
	}}, spawnJobProgressObserver(ctx, cancel))
	agent, err := opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%7"}, "partial", "claude", 1, "/project", "secret command")
	if agent.Pane != "0.1" || !errors.Is(err, diskErr) || !errors.Is(context.Cause(ctx), errJobProgressCheckpoint) {
		t.Fatalf("checkpoint failure lost its cause or partial receipt: %+v %v %v", agent, err, context.Cause(ctx))
	}
	_, _ = opts.LifecycleDeps.LaunchAgent(ctx, tmux.Pane{ID: "%8"}, "partial", "claude", 2, "/project", "secret command")
	if launches != 1 {
		t.Fatal("next agent launched after checkpoint failure")
	}
	jobs, err := journal.load()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("last valid intent missing: %+v %v", jobs, err)
	}
	progress := jobs[0].Result["spawn_progress"].(map[string]interface{})
	intent := progress["last_event"].(map[string]interface{})
	if intent["phase"] != "started" || intent["pane_id"] != "%7" {
		t.Fatalf("last durable boundary lost its exact target: %+v", intent)
	}
}

func TestSpawnJobProgressObserverSnapshotsDoNotAlias(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var snapshots []map[string]interface{}
	ctx = context.WithValue(ctx, jobProgressContextKey{}, jobProgressReporter(func(result map[string]interface{}) error {
		snapshots = append(snapshots, result)
		return nil
	}))
	observe := spawnJobProgressObserver(ctx, cancel)
	for _, event := range []robot.SpawnProgress{
		{Stage: "create_session", Phase: "finished", Session: "partial", WorkingDir: "/project"},
		{Stage: "launch_agent", Phase: "finished", PaneID: "%7", Agent: &robot.SpawnedAgent{Pane: "0.1", Type: "claude"}},
		{Stage: "launch_agent", Phase: "started", PaneID: "%8", AgentType: "codex"},
	} {
		if err := observe(event); err != nil {
			t.Fatal(err)
		}
	}
	if len(snapshots[0]["agents"].([]interface{})) != 0 || len(snapshots[2]["agents"].([]interface{})) != 1 {
		t.Fatal("later events changed an old snapshot or intent counted as a launch")
	}
	progress := snapshots[2]["spawn_progress"].(map[string]interface{})
	if progress["session_created"] != true || progress["agent_pane_ids"].(map[string]interface{})["0.1"] != "%7" || snapshots[2]["working_dir"] != "/project" {
		t.Fatalf("progress lost earlier lifecycle evidence: %+v", snapshots[2])
	}
	if snapshot := finishJobProgress(snapshots[2], map[string]interface{}{"agents": []interface{}{}}); snapshot["spawn_progress"] == nil {
		t.Fatal("empty final envelope erased independent recovery evidence")
	}
}

func TestJobProgressFailedWriteKeepsPublishedCheckpoint(t *testing.T) {
	srv, journal := progressJournalServer(t)
	job := srv.jobStore.Create(JobTypeSwarmSpawn)
	srv.jobStore.Update(job.ID, JobStatusRunning, 0, nil, "")
	if _, err := srv.checkpointJobProgress(job.ID, map[string]interface{}{"session": "keep"}); err != nil {
		t.Fatal(err)
	}
	_, err := srv.checkpointJobProgress(job.ID, map[string]interface{}{"huge": strings.Repeat("x", jobJournalMaxRecordBytes)})
	if err == nil {
		t.Fatal("oversized progress was published")
	}
	if got := srv.jobStore.Get(job.ID); got.Result["session"] != "keep" || got.Result["huge"] != nil {
		t.Fatalf("failed write replaced the published checkpoint: %+v", got)
	}
	jobs, err := journal.load()
	if err != nil || len(jobs) != 1 || jobs[0].Result["session"] != "keep" {
		t.Fatalf("failed write replaced durable evidence: %+v %v", jobs, err)
	}
	srv.jobStore.Update(job.ID, JobStatusCompleted, 100, map[string]interface{}{"final": true}, "")
	if _, err := srv.checkpointJobProgress(job.ID, map[string]interface{}{"late": true}); err == nil || srv.jobStore.Get(job.ID).Result["final"] != true {
		t.Fatal("late progress overwrote a terminal outcome")
	}
}
