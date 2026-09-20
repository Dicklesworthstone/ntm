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
