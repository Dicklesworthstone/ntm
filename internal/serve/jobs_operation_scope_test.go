package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func scopeOperationMetadata(t *testing.T, result map[string]interface{}) map[string]interface{} {
	t.Helper()
	meta, ok := result["_operation"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing operation metadata: %#v", result)
	}
	return meta
}

// The public body does not contain the server's selected project. The durable
// receipt must bind that admitted execution namespace separately, even after
// the original operation failed or the original worker disappeared.
func TestJobOperationProjectScope(t *testing.T) {
	for _, kind := range []string{"pipeline_run", "pipeline_exec", "pipeline_resume", "swarm_spawn"} {
		for _, outcome := range []string{"completed", "failed", "cancelled", "interrupted"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				dir, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
				req := CreateJobRequest{Type: kind, Params: map[string]interface{}{"session": "same-name"}, executionProjectDir: projectA}
				var calls int
				func() {
					defer func() {
						if value := recover(); value != nil && (outcome != "interrupted" || value != "worker stopped") {
							t.Fatalf("unexpected panic: %v", value)
						}
					}()
					_, err := runJobOperation(context.Background(), dir, "same-operation", "original", req,
						func(ctx context.Context, got CreateJobRequest) (map[string]interface{}, error) {
							calls++
							if got.executionProjectDir != projectA {
								t.Fatal("engine did not receive its admitted project")
							}
							if err := os.WriteFile(filepath.Join(got.executionProjectDir, "effect"), []byte("once"), 0o600); err != nil {
								t.Fatal(err)
							}
							result := map[string]interface{}{"private_project_result": projectA, "pane_id": "%7"}
							if err := reportJobProgress(ctx, result); err != nil {
								t.Fatal(err)
							}
							switch outcome {
							case "interrupted":
								panic("worker stopped")
							case "failed":
								return result, errors.New("partially executed")
							case "cancelled":
								return result, context.Canceled
							default:
								return result, nil
							}
						})
					if (err != nil) != (outcome != "completed") {
						t.Fatalf("original outcome %s: %v", outcome, err)
					}
				}()
				path := jobOperationPath(dir, "same-operation")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				other := req
				other.executionProjectDir = projectB
				publicA, _ := jobOperationFingerprint(req)
				publicB, _ := jobOperationFingerprint(other)
				if publicA != publicB {
					t.Fatal("fixture must retain identical public request fingerprints")
				}
				result, err := runJobOperation(context.Background(), dir, "same-operation", "other-project", other,
					func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
						calls++
						return nil, os.WriteFile(filepath.Join(projectB, "effect"), []byte("wrong"), 0o600)
					})
				meta := scopeOperationMetadata(t, result)
				if err == nil || !strings.Contains(err.Error(), "conflicts") || meta["status"] != "conflict" || meta["replayed"] != false || calls != 1 {
					t.Fatalf("cross-project operation was replayed or run: result=%+v err=%v calls=%d", result, err, calls)
				}
				if _, leaked := result["private_project_result"]; leaked {
					t.Fatal("scope conflict exposed the other project's result")
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("conflict rewrote original recovery evidence: %v", err)
				}
				if _, err := os.Stat(filepath.Join(projectB, "effect")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("cross-project retry produced side effects")
				}
				// Returning to the original namespace still finds its receipt,
				// not a new operation created by the failed cross-project retry.
				result, err = runJobOperation(context.Background(), dir, "same-operation", "same-project-retry", req,
					func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
						t.Fatal("same-project retry re-executed work")
						return nil, nil
					})
				meta = scopeOperationMetadata(t, result)
				if result["private_project_result"] != projectA || meta["original_job_id"] != "original" || meta["replayed"] != (outcome != "interrupted") {
					t.Fatalf("same-project recovery lost its identity or evidence: %+v %v", result, err)
				}
				if (err == nil) != (outcome == "completed") {
					t.Fatalf("same-project recovery changed outcome: %v", err)
				}
			})
		}
	}
}

func TestJobOperationScopeCannotBeAddedOrDroppedOnReplay(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := "legacy receipt has no project proof"
		if drop {
			name = "scoped receipt cannot become unscoped"
		}
		t.Run(name, func(t *testing.T) {
			dir, project := t.TempDir(), t.TempDir()
			req := CreateJobRequest{Type: "pipeline_exec"}
			if drop {
				req.executionProjectDir = project
			}
			_, err := runJobOperation(context.Background(), dir, "retained", "first", req,
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			if drop {
				req.executionProjectDir = ""
			} else {
				req.executionProjectDir = project
			}
			result, err := runJobOperation(context.Background(), dir, "retained", "retry", req,
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
					t.Fatal("unknown scope was treated as a new operation")
					return nil, nil
				})
			if err == nil || scopeOperationMetadata(t, result)["status"] != "conflict" {
				t.Fatalf("unproven scope was adopted: %+v %v", result, err)
			}
		})
	}
}

func TestJobOperationActiveScopeDoesNotLeakProgress(t *testing.T) {
	dir, projectA, projectB := t.TempDir(), t.TempDir(), t.TempDir()
	req := CreateJobRequest{Type: "swarm_spawn", executionProjectDir: projectA}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := runJobOperation(context.Background(), dir, "active", "original", req,
			func(ctx context.Context, _ CreateJobRequest) (map[string]interface{}, error) {
				calls.Add(1)
				err := reportJobProgress(ctx, map[string]interface{}{"private": "original-project-progress"})
				close(started)
				<-release
				return nil, err
			})
		done <- err
	}()
	t.Cleanup(func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("original worker did not publish progress")
	}
	other := req
	other.executionProjectDir = projectB
	result, err := runJobOperation(context.Background(), dir, "active", "different-project", other,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) { calls.Add(1); return nil, nil })
	meta := scopeOperationMetadata(t, result)
	if err == nil || calls.Load() != 1 || meta["replayed"] != false {
		t.Fatalf("concurrent scope conflict was executed: %+v %v", result, err)
	}
	if result["private"] != nil || meta["original_job_id"] != "" {
		t.Fatalf("ownership error leaked mismatched project recovery data: %+v", result)
	}
}

func TestJobOperationScopeReceiptIsPrivate(t *testing.T) {
	dir, project := t.TempDir(), filepath.Join(t.TempDir(), "private-project-name")
	req := CreateJobRequest{Type: "pipeline_run", executionProjectDir: project}
	if _, err := runJobOperation(context.Background(), dir, "private-scope", "first", req,
		func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jobOperationPath(dir, "private-scope"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]interface{}
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	binding, _ := receipt["execution_scope"].(string)
	if len(binding) != 64 || bytes.Contains(data, []byte("private-project-name")) {
		t.Fatalf("receipt lacks a private namespace binding: %s", data)
	}
}

func TestJobOperationScopeCrashHelper(t *testing.T) {
	dir := os.Getenv("NTM_TEST_SCOPE_CRASH_DIR")
	if dir == "" {
		return
	}
	req := CreateJobRequest{Type: "pipeline_exec", executionProjectDir: filepath.Join(dir, "project-a")}
	_, err := runJobOperation(context.Background(), dir, "crashed", "original", req,
		func(ctx context.Context, got CreateJobRequest) (map[string]interface{}, error) {
			if err := os.WriteFile(filepath.Join(got.executionProjectDir, "effect"), []byte("once"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := reportJobProgress(ctx, map[string]interface{}{"private": "project-a-progress"}); err != nil {
				t.Fatal(err)
			}
			os.Exit(23)
			return nil, nil
		})
	t.Fatalf("helper did not crash after its effect: %v", err)
}

func TestJobOperationScopeSurvivesProcessDeath(t *testing.T) {
	dir := t.TempDir()
	projectA, projectB := filepath.Join(dir, "project-a"), filepath.Join(dir, "project-b")
	for _, project := range []string{projectA, projectB} {
		if err := os.Mkdir(project, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJobOperationScopeCrashHelper$")
	cmd.Env = append(os.Environ(), "NTM_TEST_SCOPE_CRASH_DIR="+dir)
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("child did not reach crash boundary: %v %s", err, output)
	}
	effect, err := os.ReadFile(filepath.Join(projectA, "effect"))
	if err != nil || string(effect) != "once" {
		t.Fatalf("child did not execute its effect: %q %v", effect, err)
	}
	for _, project := range []string{projectB, projectA} {
		req := CreateJobRequest{Type: "pipeline_exec", executionProjectDir: project}
		result, err := runJobOperation(context.Background(), dir, "crashed", "retry", req,
			func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
				t.Fatal("crashed operation was re-executed")
				return nil, nil
			})
		want := "conflict"
		if project == projectA {
			want = "outcome_unknown"
			if result["private"] != "project-a-progress" {
				t.Fatal("same-project crash recovery lost progress")
			}
		} else if result["private"] != nil {
			t.Fatal("cross-project crash recovery exposed progress")
		}
		if err == nil || scopeOperationMetadata(t, result)["status"] != want {
			t.Fatalf("crash retry = %+v %v, want %s", result, err, want)
		}
	}
}

func TestJobOperationScopeRejectsMalformedEvidence(t *testing.T) {
	for _, binding := range []string{"not-a-hash", strings.Repeat("z", 64), strings.Repeat("a", 62)} {
		t.Run(binding, func(t *testing.T) {
			dir := t.TempDir()
			req := CreateJobRequest{Type: "pipeline_run", executionProjectDir: t.TempDir()}
			_, err := runJobOperation(context.Background(), dir, "damaged", "first", req,
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) { return nil, nil })
			if err != nil {
				t.Fatal(err)
			}
			path := jobOperationPath(dir, "damaged")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var receipt map[string]interface{}
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			receipt["execution_scope"] = binding
			data, err = json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readJobOperation(path); err == nil {
				t.Fatal("malformed scope accepted as recovery evidence")
			}
			_, err = runJobOperation(context.Background(), dir, "damaged", "retry", req,
				func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
					t.Fatal("malformed evidence replayed")
					return nil, nil
				})
			if err == nil {
				t.Fatal("damaged receipt was treated as successful work")
			}
		})
	}
}

func TestJobOperationScopeRejectsUnresolvedDirectoryBeforeIntent(t *testing.T) {
	for _, project := range []string{"relative/project", filepath.Join(t.TempDir(), "bad\x00name")} {
		dir := t.TempDir()
		req := CreateJobRequest{Type: "swarm_spawn", executionProjectDir: project}
		_, err := runJobOperation(context.Background(), dir, "not-admitted", "first", req,
			func(context.Context, CreateJobRequest) (map[string]interface{}, error) {
				t.Fatal("unresolved execution directory reached an engine")
				return nil, nil
			})
		if err == nil {
			t.Fatalf("unresolved scope %q was accepted", project)
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("invalid scope created an operation intent: %v %v", entries, readErr)
		}
	}
}
