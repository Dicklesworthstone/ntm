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
	"sync/atomic"
	"testing"
	"time"
)

func jobOperationTestRequest() CreateJobRequest {
	return CreateJobRequest{Type: "swarm_spawn", Params: map[string]interface{}{"session": "durable", "cc_count": 2}}
}

func operationMetadata(t *testing.T, result map[string]interface{}) map[string]interface{} {
	t.Helper()
	meta, ok := result["_operation"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing operation recovery metadata: %#v", result)
	}
	return meta
}

func TestJobOperationDurableReplay(t *testing.T) {
	dir := t.TempDir()
	req := jobOperationTestRequest()
	calls := 0
	result, err := runJobOperation(context.Background(), dir, "deploy-42", "original-job", req,
		func(_ context.Context, got CreateJobRequest) (map[string]interface{}, error) {
			calls++
			if !reflect.DeepEqual(got, req) {
				t.Fatalf("request changed: %#v", got)
			}
			pending, err := readJobOperation(jobOperationPath(dir, "deploy-42"))
			if err != nil || pending == nil || pending.Status != JobStatusRunning || pending.JobID != "original-job" {
				t.Fatalf("engine ran without a durable intent: %+v %v", pending, err)
			}
			return map[string]interface{}{"session": "durable", "agents": []interface{}{map[string]interface{}{"pane": "%7"}}}, nil
		})
	if err != nil || result["session"] != "durable" || operationMetadata(t, result)["replayed"] != false {
		t.Fatalf("first execution: %+v %v", result, err)
	}
	// No shared in-memory store is passed to the next call. Reordering JSON
	// keys and changing the transport job ID must still find the disk receipt.
	req.Params = map[string]interface{}{"cc_count": 2, "session": "durable"}
	replayed, err := runJobOperation(context.Background(), dir, "deploy-42", "retry-job", req,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			calls++
			return nil, errors.New("must not execute")
		})
	meta := operationMetadata(t, replayed)
	if err != nil || calls != 1 || meta["original_job_id"] != "original-job" || meta["replayed"] != true || meta["status"] != "completed" {
		t.Fatalf("durable replay failed: %+v %v calls=%d", replayed, err, calls)
	}
	if !reflect.DeepEqual(replayed["agents"], result["agents"]) {
		t.Fatalf("pane recovery evidence lost: %+v", replayed)
	}
	info, err := os.Stat(jobOperationPath(dir, "deploy-42"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("operation receipt is not private: %v %v", info, err)
	}
	jobs, err := (&jobJournal{dir: dir}).load()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("operation receipts must not be interpreted as execution-history rows: %v %v", jobs, err)
	}
}

func TestJobOperationReplaysFailureAndCancellation(t *testing.T) {
	for _, cause := range []error{errors.New("second pane failed"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			dir := t.TempDir()
			original := fmt.Errorf("spawn partially created: %w", cause)
			result, err := runJobOperation(context.Background(), dir, "retryable-http", "first", jobOperationTestRequest(),
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
					return map[string]interface{}{"session": "already-created"}, original
				})
			if !errors.Is(err, cause) || result["session"] != "already-created" {
				t.Fatalf("original failure lost: %+v %v", result, err)
			}
			result, err = runJobOperation(context.Background(), dir, "retryable-http", "second", jobOperationTestRequest(),
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
					t.Fatal("failed operation was re-executed")
					return nil, nil
				})
			if err == nil || err.Error() != original.Error() || result["session"] != "already-created" || operationMetadata(t, result)["replayed"] != true {
				t.Fatalf("recorded failure or partial result lost: %+v %v", result, err)
			}
			if (cause == context.Canceled || cause == context.DeadlineExceeded) && !errors.Is(err, cause) {
				t.Fatalf("context error identity lost: %v", err)
			}
		})
	}
}

func TestJobOperationConflictingRequestsNeverExecute(t *testing.T) {
	dir := t.TempDir()
	original := jobOperationTestRequest()
	if _, err := runJobOperation(context.Background(), dir, "same-id", "first", original,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	for _, req := range []CreateJobRequest{
		{Type: "checkpoint_restore", Params: original.Params},
		{Type: original.Type, Params: original.Params, Session: "different"},
		{Type: original.Type, Params: map[string]interface{}{"session": "different", "cc_count": 2}},
		{Type: original.Type, Params: map[string]interface{}{"session": "durable", "cc_count": 2, "dry_run": true}},
	} {
		got, err := runJobOperation(context.Background(), dir, "same-id", "retry", req,
			func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
				t.Fatal("conflicting work was dispatched")
				return nil, nil
			})
		if err == nil || !strings.Contains(err.Error(), "conflicts") || operationMetadata(t, got)["status"] != "conflict" {
			t.Fatalf("conflicting request accepted: %+v %v", got, err)
		}
	}
}

func TestJobOperationFingerprintCoversWholeBodyWithoutStoringIt(t *testing.T) {
	dir := t.TempDir()
	secret := "PRIVATE-WORKFLOW-PARAMETER"
	large := strings.Repeat("x", (8<<20)+1)
	req := CreateJobRequest{Type: "pipeline_exec", Params: map[string]interface{}{"workflow": large + "a", "secret": secret}}
	if _, err := runJobOperation(context.Background(), dir, "large", "first", req,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jobOperationPath(dir, "large"))
	if err != nil || strings.Contains(string(data), secret) || len(data) > 2048 {
		t.Fatalf("request payload was persisted instead of hashed: bytes=%d err=%v", len(data), err)
	}
	req.Params["workflow"] = large + "b"
	_, err = runJobOperation(context.Background(), dir, "large", "retry", req,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			t.Fatal("changed suffix beyond 8 MiB was not fingerprinted")
			return nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("changed suffix did not conflict: %v", err)
	}
}

func TestJobOperationConcurrentDuplicatesAreFenced(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := runJobOperation(context.Background(), dir, "in-flight", "original", jobOperationTestRequest(),
			func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
				calls.Add(1)
				close(started)
				<-release
				return map[string]interface{}{"session": "durable"}, nil
			})
		done <- err
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original operation did not start")
	}
	got, err := runJobOperation(context.Background(), dir, "in-flight", "duplicate", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			calls.Add(1)
			return nil, nil
		})
	if err == nil || calls.Load() != 1 || operationMetadata(t, got)["original_job_id"] != "original" || operationMetadata(t, got)["status"] != "in_progress" {
		t.Fatalf("concurrent execution was not fenced: %+v %v calls=%d", got, err, calls.Load())
	}
	// The fence is per operation, not a global bottleneck for unrelated work.
	_, err = runJobOperation(context.Background(), dir, "independent", "another-job", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil })
	if err != nil {
		t.Fatalf("unrelated operation blocked: %v", err)
	}
}

func TestJobOperationCrashHelper(t *testing.T) {
	dir := os.Getenv("NTM_TEST_JOB_OPERATION_CRASH_DIR")
	if dir == "" {
		return
	}
	_, err := runJobOperation(context.Background(), dir, "crashed-operation", "original-before-crash", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			if err := os.WriteFile(filepath.Join(dir, "effect"), []byte("once"), 0o600); err != nil {
				t.Fatal(err)
			}
			os.Exit(23) // Simulate process death after effects and before receipt commit.
			return nil, nil
		})
	t.Fatalf("crash helper unexpectedly returned: %v", err)
}

func TestJobOperationCrashRefusesReexecution(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJobOperationCrashHelper$")
	cmd.Env = append(os.Environ(), "NTM_TEST_JOB_OPERATION_CRASH_DIR="+dir)
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("helper failed instead of crashing at the effect boundary: %v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "effect"))
	if err != nil || string(data) != "once" {
		t.Fatalf("child did not reach its side effect: %q %v", data, err)
	}
	got, err := runJobOperation(context.Background(), dir, "crashed-operation", "retry-after-restart", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			t.Fatal("crashed operation was re-executed")
			return nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "unknown") || operationMetadata(t, got)["status"] != "outcome_unknown" || operationMetadata(t, got)["original_job_id"] != "original-before-crash" {
		t.Fatalf("crash did not preserve uncertainty and identity: %+v %v", got, err)
	}
}

func TestJobOperationFailedFinalCheckpointBlocksRetry(t *testing.T) {
	dir := t.TempDir()
	got, err := runJobOperation(context.Background(), dir, "bad-result", "first", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			return map[string]interface{}{"session": "created", "unserializable": make(chan int)}, errors.New("backend failure")
		})
	if err == nil || !strings.Contains(err.Error(), "backend failure") || !strings.Contains(err.Error(), "checkpoint operation outcome") || got["session"] != "created" {
		t.Fatalf("checkpoint failure lost partial evidence: %+v %v", got, err)
	}
	got, err = runJobOperation(context.Background(), dir, "bad-result", "retry", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			t.Fatal("failed final checkpoint allowed a retry")
			return nil, nil
		})
	if err == nil || operationMetadata(t, got)["status"] != "outcome_unknown" {
		t.Fatalf("missing checkpoint was treated as safe: %+v %v", got, err)
	}
}

func TestJobOperationUnreadableReceiptFailsClosed(t *testing.T) {
	for _, kind := range []string{"corrupt", "null", "trailing", "oversized", "directory", "symlink", "wrong identity", "bad status", "bad version"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := jobOperationPath(dir, "damaged")
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			data := []byte("{")
			switch kind {
			case "null":
				data = []byte("null")
			case "trailing":
				data = []byte("{} {}")
			case "oversized":
				data = []byte(strings.Repeat(" ", jobJournalMaxRecordBytes+1))
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(dir, "absent"), path); err != nil {
					t.Skip(err)
				}
			case "wrong identity", "bad status", "bad version":
				fingerprint, err := jobOperationFingerprint(jobOperationTestRequest())
				if err != nil {
					t.Fatal(err)
				}
				receipt := &jobOperationReceipt{Version: 1, Fingerprint: fingerprint, JobID: "old", Type: "swarm_spawn", Status: JobStatusCompleted,
					OperationKey: strings.TrimSuffix(filepath.Base(path), ".json")}
				if kind == "wrong identity" {
					receipt.OperationKey = "other-operation"
				}
				if kind == "bad status" {
					receipt.Status = "unknown"
				}
				if kind == "bad version" {
					receipt.Version = 2
				}
				if err := receipt.save(path); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "corrupt" || kind == "null" || kind == "trailing" || kind == "oversized" {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := runJobOperation(context.Background(), dir, "damaged", "retry", jobOperationTestRequest(),
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
					t.Fatal("unreadable receipt was treated as absent")
					return nil, nil
				})
			if err == nil {
				t.Fatal("damaged receipt accepted")
			}
		})
	}
}

func TestJobOperationCancellationBeforeClaimHasNoEffects(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runJobOperation(ctx, dir, "cancelled", "unused", jobOperationTestRequest(),
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
			t.Fatal("cancelled work executed")
			return nil, nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled work claimed an operation: %v %v", entries, err)
	}
}

func TestSplitJobOperationRequest(t *testing.T) {
	for _, id := range []interface{}{nil, false, 3, "", " ", " leading", "trailing ", "a\nb", strings.Repeat("x", 201)} {
		req := jobOperationTestRequest()
		req.Params["operation_id"] = id
		if _, _, err := splitJobOperationRequest(req); err == nil {
			t.Fatalf("invalid operation_id accepted: %#v", id)
		}
	}
	req := jobOperationTestRequest()
	req.Params["operation_id"] = "deployment/42"
	req.Params["dry_rnu"] = true
	id, filtered, err := splitJobOperationRequest(req)
	if err != nil || id != "deployment/42" || filtered.Params["operation_id"] != nil || filtered.Params["dry_rnu"] != true {
		t.Fatalf("wrong filtering: %q %#v %v", id, filtered, err)
	}
	filtered.Params["session"] = "changed"
	if req.Params["session"] != "durable" || req.Params["operation_id"] != id {
		t.Fatal("filter mutated caller params")
	}
	path := jobOperationPath("/safe", "../../outside")
	if filepath.Dir(path) != filepath.Join("/safe", "operations") {
		t.Fatalf("operation ID escaped namespace: %s", path)
	}
}

func TestJobExecutionParamsSessionBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     CreateJobRequest
		want    string
		wantErr string
	}{
		{name: "saved resume session", req: CreateJobRequest{Type: "pipeline_resume"}},
		{name: "nested only", req: CreateJobRequest{Params: map[string]interface{}{"session": "nested"}}, want: "nested"},
		{name: "envelope only", req: CreateJobRequest{Session: "chosen"}, want: "chosen"},
		{name: "envelope with params", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"dry_run": true}}, want: "chosen"},
		{name: "matching", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": "chosen"}}, want: "chosen"},
		{name: "conflict", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": "other"}}, wantErr: "session conflict"},
		{name: "case-sensitive", req: CreateJobRequest{Session: "Chosen", Params: map[string]interface{}{"session": "chosen"}}, wantErr: "session conflict"},
		{name: "explicit empty", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": ""}}, wantErr: "session conflict"},
		{name: "explicit null", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": nil}}, wantErr: "must be a string"},
		{name: "explicit number", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": 3}}, wantErr: "must be a string"},
		{name: "explicit bool", req: CreateJobRequest{Session: "chosen", Params: map[string]interface{}{"session": true}}, wantErr: "must be a string"},
		{name: "blank envelope", req: CreateJobRequest{Session: " \t"}, wantErr: "must not be whitespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := jobOperationFingerprint(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			params, err := jobExecutionParams(tc.req)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("invalid binding accepted: %#v %v", params, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if session, _ := params["session"].(string); session != tc.want {
				t.Fatalf("target = %q, want %q", session, tc.want)
			}
			if tc.req.Session != "" {
				params["session"] = "caller-mutation"
			}
			after, err := jobOperationFingerprint(tc.req)
			if err != nil || before != after {
				t.Fatalf("session normalization mutated durable request identity: %s %s %v", before, after, err)
			}
		})
	}
}
