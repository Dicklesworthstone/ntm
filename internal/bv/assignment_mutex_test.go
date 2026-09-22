package bv

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
)

// Use real SQLite connections and the production claim transactions. No br
// install, global workspace mutex, or mocked claim can serialize these tests.
func assignmentMutexFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beads.db")
	db, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(path, "busy_timeout(5000)", "foreign_keys(ON)"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', status TEXT,
		 assignee TEXT, updated_at TEXT NOT NULL, content_hash TEXT, defer_until TEXT,
		 pinned INTEGER DEFAULT 0, ephemeral INTEGER DEFAULT 0, is_template INTEGER DEFAULT 0)`,
		`CREATE TABLE labels (issue_id TEXT, label TEXT)`,
		`CREATE INDEX labels_issue ON labels(issue_id)`,
		`CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT)`,
		`CREATE TABLE events (issue_id TEXT, event_type TEXT, actor TEXT, old_value TEXT,
		 new_value TEXT, comment TEXT, created_at TEXT, agent_name TEXT, harness TEXT, model TEXT)`,
		`CREATE TABLE dirty_issues (issue_id TEXT PRIMARY KEY, marked_at TEXT)`,
		`CREATE TABLE export_hashes (issue_id TEXT PRIMARY KEY, content_hash TEXT)`,
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT)`,
	} {
		mutexSQL(t, db, statement)
	}
	return path, db
}

func mutexSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

func mutexIssue(t *testing.T, db *sql.DB, id, status, actor string, labels ...string) {
	t.Helper()
	mutexSQL(t, db, `INSERT INTO issues (id, title, status, assignee, updated_at, content_hash)
	 VALUES (?, ?, ?, ?, ?, 'original')`, id, "private title for "+id, status, actor, time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano))
	for _, label := range labels {
		mutexSQL(t, db, "INSERT INTO labels (issue_id, label) VALUES (?, ?)", id, label)
	}
}

func mutexRefusal(t *testing.T, err error, groups ...string) {
	t.Helper()
	var refusal *AssignmentMutexError
	if !errors.Is(err, ErrAssignmentMutexHeld) || !errors.Is(err, ErrBeadAssignmentIneligible) ||
		!errors.As(err, &refusal) || !reflect.DeepEqual(refusal.Groups, groups) {
		t.Fatalf("missing typed mutex refusal for %v: %v", groups, err)
	}
}

func TestAssignmentMutexClaimsSerializeAcrossConnections(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	const count = 16
	for i := 0; i < count; i++ {
		mutexIssue(t, db, fmt.Sprintf("task-%d", i), "open", "", "mutex:db")
	}
	start := make(chan struct{})
	type outcome struct {
		result  BeadClaimResult
		changed bool
		err     error
	}
	results := make(chan outcome, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, changed, err := claimBeadForAssignmentTransaction(ctx, path, fmt.Sprintf("task-%d", index), fmt.Sprintf("actor-%d", index), nil)
			results <- outcome{result, changed, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for out := range results {
		if out.err == nil {
			winners++
			if !out.changed || out.result.ID == "" || out.result.Status != "in_progress" {
				t.Fatalf("invalid successful claim: %+v", out)
			}
		} else {
			mutexRefusal(t, out.err, "db")
			if out.changed || out.result.ID != "" {
				t.Fatalf("refused claim returned an effect: %+v", out)
			}
		}
	}
	var active, events, dirty int
	if err := db.QueryRow("SELECT count(*) FROM issues WHERE status = 'in_progress'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM dirty_issues").Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if winners != 1 || active != 1 || events != 2 || dirty != 1 {
		t.Fatalf("competing claims escaped one transaction: winners=%d active=%d events=%d dirty=%d", winners, active, events, dirty)
	}
}

func TestAssignmentMutexOwnerLifecycleAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		status, owner string
		held          bool
	}{
		{"in_progress", "", true}, {" IN_PROGRESS ", "\t", true},
		{"open", "private actor", true}, {"blocked", "private actor", true},
		{"deferred", "private actor", true}, {"unknown", "private actor", true},
		{"closed", "historical actor", false}, {" TOMBSTONE ", "historical actor", false},
		{"open", "\t\u2003", false}, {"deferred", "", false},
	} {
		t.Run(tc.status+"/"+tc.owner, func(t *testing.T) {
			path, db := assignmentMutexFixture(t)
			mutexIssue(t, db, "candidate", "open", "", "\t MuTeX: \u2003ÄREA\u00a0", "mutex:ÄREA", "mutex:")
			mutexIssue(t, db, "private-holder", tc.status, tc.owner, "\u2003MUTEX:ärea\t", "private")
			_, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "candidate", "worker", nil)
			if tc.held {
				mutexRefusal(t, err, "ärea")
				if changed {
					t.Fatal("refusal changed tracker")
				}
				encoded, marshalErr := json.Marshal(err)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				for _, text := range []string{err.Error(), string(encoded)} {
					if strings.Contains(text, "private-holder") || strings.Contains(text, "private actor") || strings.Contains(text, "private title") {
						t.Fatalf("mutex refusal disclosed its holder: %s", text)
					}
				}
			} else if err != nil || !changed {
				t.Fatalf("historical/non-owner row blocked new work: changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestAssignmentMutexExactRetryReleaseAndIndependentWork(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "first", "open", "", "mutex:db")
	mutexIssue(t, db, "second", "open", "", "mutex:db", "mutex:docs")
	mutexIssue(t, db, "independent", "open", "", "mutex:docs")
	ctx := context.Background()
	if _, _, err := claimBeadForAssignmentTransaction(ctx, path, "first", "same-actor", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := claimBeadForAssignmentTransaction(ctx, path, "first", "same-actor", nil); err != nil {
		t.Fatalf("exact retry blocked itself: %v", err)
	}
	_, changed, err := claimBeadForAssignmentTransaction(ctx, path, "second", "same-actor", nil)
	mutexRefusal(t, err, "db")
	if changed {
		t.Fatal("same actor acquired overlapping work")
	}
	if _, _, err := claimBeadForAssignmentTransaction(ctx, path, "independent", "other-actor", nil); err != nil {
		t.Fatalf("failed multi-group claim reserved docs: %v", err)
	}
	if _, _, err := claimBeadForAssignmentTransaction(ctx, path, "first", "same-actor", nil); err != nil {
		t.Fatal(err)
	}
	released, err := releaseBeadClaimTransaction(ctx, path, "first", "wrong-owner")
	if err != nil || released.Released {
		t.Fatalf("wrong owner released group: %+v %v", released, err)
	}
	_, _, err = claimBeadForAssignmentTransaction(ctx, path, "second", "same-actor", nil)
	mutexRefusal(t, err, "db", "docs")
	for _, pair := range [][2]string{{"first", "same-actor"}, {"independent", "other-actor"}} {
		released, err = releaseBeadClaimTransaction(ctx, path, pair[0], pair[1])
		if err != nil || !released.Released {
			t.Fatalf("owner release failed: %+v %v", released, err)
		}
	}
	if _, changed, err := claimBeadForAssignmentTransaction(ctx, path, "second", "same-actor", nil); err != nil || !changed {
		t.Fatalf("released groups did not become available: %v %v", changed, err)
	}
}

func TestAssignmentMutexFailedWriteDoesNotHoldGroups(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "failing", "open", "", "mutex:db")
	mutexIssue(t, db, "next", "open", "", "mutex:db")
	mutexSQL(t, db, `CREATE TRIGGER fail_claim BEFORE INSERT ON events WHEN NEW.issue_id = 'failing'
	 BEGIN SELECT RAISE(ABORT, 'injected event write failure'); END`)
	_, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "failing", "worker", nil)
	if err == nil || changed || !strings.Contains(err.Error(), "injected event write failure") {
		t.Fatalf("write failure hidden: %v %v", changed, err)
	}
	var status, owner, hash string
	if err := db.QueryRow("SELECT status, assignee, content_hash FROM issues WHERE id = 'failing'").Scan(&status, &owner, &hash); err != nil {
		t.Fatal(err)
	}
	if status != "open" || owner != "" || hash != "original" {
		t.Fatalf("rolled-back claim changed state: %q %q %q", status, owner, hash)
	}
	if _, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "next", "worker", nil); err != nil || !changed {
		t.Fatalf("rollback retained a mutex: %v %v", changed, err)
	}
}

func TestAssignmentMutexStaleRecoveryAndIdempotentRecheck(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "stale", "in_progress", "", "mutex:db")
	mutexIssue(t, db, "holder", "open", "owner", "mutex:db")
	var raw string
	if err := db.QueryRow("SELECT updated_at FROM issues WHERE id = 'stale'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	updated, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := claimBeadNonTerminalTransaction(context.Background(), path, "stale", "recoverer", updated, nil)
	mutexRefusal(t, err, "db")
	if changed {
		t.Fatal("stale adoption ignored mutex holder")
	}
	mutexSQL(t, db, "UPDATE issues SET status = 'closed' WHERE id = 'holder'")
	if _, changed, err := claimBeadNonTerminalTransaction(context.Background(), path, "stale", "recoverer", updated, nil); err != nil || !changed {
		t.Fatalf("stale task blocked itself: %v %v", changed, err)
	}
	if _, _, err := claimBeadNonTerminalTransaction(context.Background(), path, "stale", "recoverer", updated, nil); err != nil {
		t.Fatalf("idempotent stale retry: %v", err)
	}
	// An external tracker writer can violate the invariant. A retry must not
	// turn the historical claim receipt into permission to dispatch anyway.
	mutexSQL(t, db, "UPDATE issues SET status = 'open' WHERE id = 'holder'")
	for _, claim := range []func() error{
		func() error {
			_, _, err := claimBeadForAssignmentTransaction(context.Background(), path, "stale", "recoverer", nil)
			return err
		},
		func() error {
			_, _, err := claimBeadNonTerminalTransaction(context.Background(), path, "stale", "recoverer", updated, nil)
			return err
		},
	} {
		mutexRefusal(t, claim(), "db")
	}
}

func TestAssignmentMutexFailsClosedOnUnverifiableHolders(t *testing.T) {
	for _, broken := range []string{"orphan label", "null lifecycle", "blank lifecycle", "missing labels"} {
		t.Run(broken, func(t *testing.T) {
			path, db := assignmentMutexFixture(t)
			mutexIssue(t, db, "candidate", "open", "", "mutex:db")
			mutexIssue(t, db, "holder", "open", "", "mutex:db")
			switch broken {
			case "orphan label":
				mutexSQL(t, db, "INSERT INTO labels (issue_id, label) VALUES ('absent', 'mutex:db')")
			case "null lifecycle":
				mutexSQL(t, db, "UPDATE issues SET status = NULL WHERE id = 'holder'")
			case "blank lifecycle":
				mutexSQL(t, db, "UPDATE issues SET status = ' ' WHERE id = 'holder'")
			case "missing labels":
				mutexSQL(t, db, "ALTER TABLE labels RENAME TO unavailable_labels")
			}
			_, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "candidate", "worker", nil)
			if err == nil || changed {
				t.Fatalf("unverifiable holder accepted: changed=%v err=%v", changed, err)
			}
			var status string
			if err := db.QueryRow("SELECT status FROM issues WHERE id = 'candidate'").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "open" {
				t.Fatal("failed verification changed candidate")
			}
		})
	}
}

func TestAssignmentMutexCancellationDoesNotAct(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "candidate", "open", "", "mutex:db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, changed, err := claimBeadForAssignmentTransaction(ctx, path, "candidate", "worker", nil)
	if !errors.Is(err, context.Canceled) || changed {
		t.Fatalf("cancelled claim = %v %v", changed, err)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM issues WHERE id = 'candidate'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "open" {
		t.Fatal("cancelled claim changed tracker")
	}
}

func TestAssignmentMutexPublicClaimRefusesBeforeExport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("br fixture uses POSIX shell")
	}
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "candidate", "open", "", "mutex:db")
	mutexIssue(t, db, "holder", "in_progress", "private-owner", "mutex:db")
	project := filepath.Dir(path)
	if err := os.Mkdir(filepath.Join(project, ".beads"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	log := filepath.Join(project, "br-calls")
	info, err := json.Marshal(beadsWorkspaceInfo{DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NTM_MUTEX_TEST_INFO", string(info))
	t.Setenv("NTM_MUTEX_TEST_LOG", log)
	const script = `#!/bin/sh
printf '%s\n' "$*" >> "$NTM_MUTEX_TEST_LOG"
case " $* " in
  *' info '*) printf '%s\n' "$NTM_MUTEX_TEST_INFO" ;;
  *) echo 'unexpected br mutation' >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "br"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = ClaimBeadForAssignmentWithOperatorGatedLabels(context.Background(), project, "candidate", "worker", nil)
	mutexRefusal(t, err, "db")
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "info") || strings.Contains(string(calls), "sync") || strings.Contains(string(calls), "--claim") {
		t.Fatalf("refusal exported or bypassed claim guard: %s", calls)
	}
}

// Independent OS processes must see the same winner, not only goroutines
// sharing the package's workspace mutex. Each child uses the real transaction.
func TestAssignmentMutexProcessWorker(t *testing.T) {
	path, id := os.Getenv("NTM_MUTEX_CHILD_DB"), os.Getenv("NTM_MUTEX_CHILD_ID")
	if path == "" || id == "" {
		t.Skip("subprocess entry point")
	}
	_, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, id, id, nil)
	if err == nil && changed {
		fmt.Println("MUTEX_CLAIM_WON")
		return
	}
	if errors.Is(err, ErrAssignmentMutexHeld) && !changed {
		fmt.Println("MUTEX_CLAIM_REFUSED")
		return
	}
	t.Fatalf("unexpected child claim: changed=%v err=%v", changed, err)
}

func TestAssignmentMutexClaimsSerializeAcrossProcesses(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	const count = 4
	var commands []*exec.Cmd
	var outputs [count]strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("process-%d", i)
		mutexIssue(t, db, id, "open", "", "mutex:shared")
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAssignmentMutexProcessWorker$", "-test.count=1")
		cmd.Env = append(os.Environ(), "NTM_MUTEX_CHILD_DB="+path, "NTM_MUTEX_CHILD_ID="+id)
		cmd.Stdout, cmd.Stderr = &outputs[i], &outputs[i]
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	winners, refused := 0, 0
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outputs[i].String())
		}
		if strings.Contains(outputs[i].String(), "MUTEX_CLAIM_WON") {
			winners++
		}
		if strings.Contains(outputs[i].String(), "MUTEX_CLAIM_REFUSED") {
			refused++
		}
	}
	if winners != 1 || refused != count-1 {
		t.Fatalf("process ownership split: winners=%d refused=%d", winners, refused)
	}
}

func TestAssignmentMutexFailedCommitDoesNotHoldGroups(t *testing.T) {
	path, db := assignmentMutexFixture(t)
	mutexIssue(t, db, "failing", "open", "", "mutex:db")
	mutexIssue(t, db, "next", "open", "", "mutex:db")
	mutexSQL(t, db, "CREATE TABLE required_parent (id INTEGER PRIMARY KEY)")
	mutexSQL(t, db, `CREATE TABLE deferred_check (id INTEGER REFERENCES required_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	mutexSQL(t, db, `CREATE TRIGGER fail_commit AFTER UPDATE ON issues WHEN NEW.id = 'failing'
	 BEGIN INSERT INTO deferred_check(id) VALUES (1); END`)
	_, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "failing", "worker", nil)
	if err == nil || changed || !strings.Contains(err.Error(), "commit assignment Beads claim") {
		t.Fatalf("commit failure hidden: changed=%v err=%v", changed, err)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM issues WHERE id = 'failing'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "open" {
		t.Fatalf("failed commit retained ownership: %s", status)
	}
	if _, changed, err := claimBeadForAssignmentTransaction(context.Background(), path, "next", "worker", nil); err != nil || !changed {
		t.Fatalf("failed commit retained a mutex: changed=%v err=%v", changed, err)
	}
}

func TestAssignmentMutexRefusalCannotTriggerDatabaseRebuild(t *testing.T) {
	for _, group := range beadsDBCorruptionSignatures {
		err := &AssignmentMutexError{BeadID: "candidate", Groups: []string{group}}
		if isBeadsDBCorruptionError(fmt.Errorf("claim refused: %w", err)) {
			t.Fatalf("mutex name %q authorized rebuilding the tracker", group)
		}
		calls := 0
		_, got := withBeadsCorruptionRecovery(context.Background(), t.TempDir(), func() (BeadClaimResult, error) {
			calls++
			return BeadClaimResult{}, err
		})
		if got != err || calls != 1 {
			t.Fatalf("refusal was retried or rebuilt: calls=%d err=%v", calls, got)
		}
	}
	// Actual SQLite diagnostics still reach the existing recovery policy.
	if !isBeadsDBCorruptionError(errors.New("database disk image is malformed")) {
		t.Fatal("genuine corruption no longer recognized")
	}
}
