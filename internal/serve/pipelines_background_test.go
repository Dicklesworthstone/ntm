//go:build unix

package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
)

// A real HTTP server launches the work in a separate Go process and then exits.
// The second dependent step cannot be started by a shell orphaned when the
// server died: only the surviving canonical workflow executor can dispatch it.
func TestRESTBackgroundPipelineSurvivesServerExit(t *testing.T) {
	for _, mode := range []string{"file", "inline"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			id := launchAPIDetachedFixture(t, root, mode)
			if err := os.WriteFile(filepath.Join(root, "release"), []byte("ready"), 0600); err != nil {
				t.Fatal(err)
			}
			state := awaitAPIDetachedState(t, root, id, pipeline.StatusCompleted)
			if state.Steps["finish"].Status != pipeline.StatusCompleted {
				t.Fatal("dependent step was never scheduled after the launching server exited")
			}
			data, err := os.ReadFile(filepath.Join(root, "result"))
			if err != nil || string(data) != "completed" {
				t.Fatalf("dependent command did not complete: %q %v", data, err)
			}

			// A freshly constructed server must inspect and list the configured
			// project even though its own cwd is elsewhere and registry is empty.
			srv := &Server{projectDir: root}
			inspect := httptest.NewRecorder()
			srv.handleGetPipeline(inspect, apiDetachedRequest(http.MethodGet, id))
			if inspect.Code != http.StatusOK || !strings.Contains(inspect.Body.String(), `"completed"`) {
				t.Fatalf("fresh server cannot inspect result: %d %s", inspect.Code, inspect.Body.String())
			}
			listed := httptest.NewRecorder()
			srv.handleListPipelines(listed, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
			var list struct {
				Pipelines []pipeline.PipelineSummary `json:"pipelines"`
			}
			if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || listed.Code != http.StatusOK || len(list.Pipelines) != 1 || list.Pipelines[0].RunID != id {
				t.Fatalf("fresh server lost configured-project run: %v %d %s", err, listed.Code, listed.Body.String())
			}

			// Replay the worker's actual ordered events after producer exit.
			// The stream footer is drained before run ownership is released.
			var kinds []string
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = pipeline.FollowBackgroundProgress(ctx, root, id, func(event pipeline.ProgressEvent) {
				if _, ok := pipelineEventTypeFromProgressType(event.Type); ok {
					kinds = append(kinds, event.Type+":"+event.StepID)
				}
			})
			want := "workflow_start:,step_complete:gate,step_complete:finish,workflow_complete:"
			if err != nil || strings.Join(kinds, ",") != want {
				t.Fatalf("detached progress was lost or fabricated: %v %v", kinds, err)
			}
		})
	}
}

func TestRESTBackgroundPipelineFreshServerCancelsActualWorker(t *testing.T) {
	root := t.TempDir()
	id := launchAPIDetachedFixture(t, root, "inline")
	srv := &Server{projectDir: root}
	rec := httptest.NewRecorder()
	srv.handleCancelPipeline(rec, apiDetachedRequest(http.MethodPost, id))
	var response struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusAccepted || response.Status != "cancellation_requested" {
		t.Fatalf("fresh-server cancellation was not acknowledged by the worker: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	awaitAPIDetachedState(t, root, id, pipeline.StatusCancelled)
	if _, err := os.Stat(filepath.Join(root, "result")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancellation allowed dependent dispatch: %v", err)
	}
	var terminal []pipeline.ProgressEvent
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pipeline.FollowBackgroundProgress(ctx, root, id, func(event pipeline.ProgressEvent) {
		if event.Type == "workflow_error" || event.Type == "workflow_complete" {
			terminal = append(terminal, event)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(terminal) != 1 || terminal[0].Type != "workflow_error" {
		t.Fatalf("cancelled worker published a successful or missing terminal event: %+v", terminal)
	}
}

func TestRESTBackgroundPipelineRejectsBeforeAcceptingWork(t *testing.T) {
	for _, mode := range []string{"cancelled", "storage", "variables"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			srv := &Server{projectDir: root}
			workflow := &pipeline.Workflow{SchemaVersion: "2.0", Name: "reject", Steps: []pipeline.Step{
				{ID: "work", Command: fmt.Sprintf("printf dispatched > %q", filepath.Join(root, "marker"))},
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var vars map[string]interface{}
			switch mode {
			case "cancelled":
				cancel()
			case "storage":
				parent := filepath.Join(root, ".ntm", "pipelines")
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(parent, "background"), []byte("retain evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			case "variables":
				vars = map[string]interface{}{"unserializable": make(chan int)}
			}
			out := srv.execPipelineInline(ctx, workflow, "rejected", vars, true)
			if out.Success || out.Error == "" || out.Status == "running" || out.Status == "pending" {
				t.Fatalf("unaccepted work reported success: %+v", out)
			}
			if _, err := os.Stat(filepath.Join(root, "marker")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected request dispatched work: %v", err)
			}
			if _, err := pipeline.LoadState(root, out.RunID); err == nil {
				t.Fatal("rejected request published execution state")
			}
			if mode == "cancelled" {
				if _, err := os.Stat(filepath.Join(root, ".ntm")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("pre-cancelled request created background artifacts")
				}
			}
		})
	}
}

func TestRESTPipelineListUsesConfiguredProjectAndReportsUnreadableStates(t *testing.T) {
	root := t.TempDir()
	srv := &Server{projectDir: root}
	when := time.Now().Add(-time.Hour)
	for _, id := range []string{"run-b", "run-a"} {
		if err := pipeline.SaveState(root, &pipeline.ExecutionState{
			RunID: id, WorkflowID: "scoped", Status: pipeline.StatusCompleted, StartedAt: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, ".ntm", "pipelines", "run-broken.json")
	if err := os.WriteFile(path, []byte("broken; do not hide me"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.handleListPipelines(rec, httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil))
	var response struct {
		Pipelines []pipeline.PipelineSummary `json:"pipelines"`
		Warnings  []string                   `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || len(response.Pipelines) != 2 {
		t.Fatalf("scoped listing: %v %d %s", err, rec.Code, rec.Body.String())
	}
	if response.Pipelines[0].RunID != "run-a" || response.Pipelines[1].RunID != "run-b" {
		t.Fatalf("nondeterministic tie ordering: %+v", response.Pipelines)
	}
	if len(response.Warnings) != 1 || !strings.Contains(response.Warnings[0], "run-broken") {
		t.Fatalf("unreadable checkpoint silently omitted: %+v", response.Warnings)
	}
	empty := &Server{projectDir: t.TempDir()}
	if empty.pipelineSnapshot("run-a") != nil {
		t.Fatal("pipeline inspection crossed project boundaries")
	}
}

func apiDetachedRequest(method, id string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/pipelines/"+id, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
}

func launchAPIDetachedFixture(t *testing.T, root, mode string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAPIDetachedPipelineProcess$", "--", "launcher", root, mode)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output() // Returns only once the launching server is gone.
	if err != nil {
		t.Fatalf("launching HTTP server: %v\n%s\n%s", err, data, stderr.String())
	}
	var out pipeline.PipelineRunOutput
	if err := json.Unmarshal(data, &out); err != nil || !out.Success || out.RunID == "" {
		t.Fatalf("background HTTP response: %v %s\n%s", err, data, stderr.String())
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if st, err := pipeline.LoadState(root, out.RunID); err == nil && (st.Status == pipeline.StatusRunning || st.Status == pipeline.StatusPending) {
			_ = pipeline.RequestRunCancellation(ctx, root, out.RunID)
		}
	})
	return out.RunID
}

func awaitAPIDetachedState(t *testing.T, root, id string, want pipeline.ExecutionStatus) *pipeline.ExecutionState {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var last *pipeline.ExecutionState
	for time.Now().Before(deadline) {
		state, err := pipeline.LoadState(root, id)
		if err == nil {
			last = state
			if state.Status == want {
				return state
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	log, _ := os.ReadFile(filepath.Join(root, ".ntm", "pipelines", "background", id+".log"))
	t.Fatalf("worker did not reach %s: %+v\n%s", want, last, log)
	return nil
}

func TestAPIDetachedPipelineProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "__pipeline-worker" && len(os.Args) == i+4 && os.Args[i+1] == "--" {
			if err := pipeline.RunBackgroundWorker(context.Background(), os.Args[i+2], os.Args[i+3], os.Stdin); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	for i, arg := range os.Args {
		if arg != "launcher" || len(os.Args) != i+3 {
			continue
		}
		root, mode := os.Args[i+1], os.Args[i+2]
		pipeline.BackgroundWorkerFlags = func() ([]string, error) {
			return []string{"-test.run=^TestAPIDetachedPipelineProcess$", "--"}, nil
		}
		if mode == "resume" {
			srv := NewHermeticServer("test")
			srv.projectDir = root
			httpServer := httptest.NewServer(srv.Router())
			defer httpServer.Close()
			client := &http.Client{Timeout: 5 * time.Second}
			response, err := client.Post(httpServer.URL+"/api/v1/pipelines/"+apiDetachedResumeRunID+"/resume",
				"application/json", strings.NewReader(`{"background":true}`))
			if err != nil {
				t.Fatal(err)
			}
			result, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusAccepted {
				t.Fatalf("POST background resume: %v %d %s", err, response.StatusCode, result)
			}
			awaitAPIDetachedState(t, root, apiDetachedResumeRunID, pipeline.StatusRunning)
			fmt.Fprint(os.Stdout, string(result))
			// The worker must continue after the entire HTTP server exits.
			os.Exit(0)
		}
		gate := fmt.Sprintf("printf ready > %q; n=0; while [ ! -f %q ]; do n=$((n+1)); [ \"$n\" -ge 200 ] && exit 9; sleep 0.05; done", filepath.Join(root, "started"), filepath.Join(root, "release"))
		workflow := map[string]interface{}{
			"schema_version": "2.0", "name": "detached-http",
			"steps": []interface{}{
				map[string]interface{}{"id": "gate", "command": gate},
				map[string]interface{}{"id": "finish", "depends_on": []string{"gate"}, "command": fmt.Sprintf("printf completed > %q", filepath.Join(root, "result"))},
			},
		}
		body := map[string]interface{}{"session": "detachedhttp", "background": true}
		srv := &Server{projectDir: root}
		handler := srv.handleExecPipeline
		if mode == "file" {
			// JSON maps are valid YAML and do not bypass native schema scalar
			// hooks by serializing zero-valued PaneSpec or Duration structs.
			data, _ := json.Marshal(workflow)
			path := filepath.Join(root, "flow.yaml")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			body["workflow_file"] = path
			handler = srv.handleRunPipeline
		} else {
			body["workflow"] = workflow
		}
		httpServer := httptest.NewServer(http.HandlerFunc(handler))
		defer httpServer.Close()
		data, _ := json.Marshal(body)
		client := &http.Client{Timeout: 15 * time.Second}
		response, err := client.Post(httpServer.URL, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		result, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("POST background: %v %d %s", err, response.StatusCode, result)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("accepted request did not start the real command")
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(os.Stdout, string(result))
		// Terminate the entire launching server, not just its HTTP handler.
		os.Exit(0)
	}
}

func TestRESTPipelineCancellationUsesProjectOwnerNotCollidingRegistry(t *testing.T) {
	for _, hasOwner := range []bool{true, false} {
		t.Run(fmt.Sprintf("owner=%v", hasOwner), func(t *testing.T) {
			root, foreignRoot := t.TempDir(), t.TempDir()
			id := pipeline.GenerateRunID()
			state := &pipeline.ExecutionState{RunID: id, WorkflowID: "local", Session: "local", Status: pipeline.StatusRunning}
			if err := pipeline.SaveState(root, state); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, ".ntm", "pipelines", id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			foreignCtx, cancelForeign := context.WithCancel(context.Background())
			defer cancelForeign()
			foreignConfig := pipeline.DefaultExecutorConfig("foreign")
			foreignConfig.ProjectDir = foreignRoot
			pipeline.RegisterPipeline(pipeline.NewTrackedExecution(id, "foreign", "foreign", 1, pipeline.NewExecutor(foreignConfig), cancelForeign))
			t.Cleanup(func() {
				pipeline.UpdatePipelineFromState(id, &pipeline.ExecutionState{RunID: id, Status: pipeline.StatusCompleted})
			})
			var owner *pipeline.RunControl
			if hasOwner {
				owner, err = pipeline.AcquireRunControl(context.Background(), root, id)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
			}
			srv := &Server{projectDir: root}
			rec := httptest.NewRecorder()
			srv.handleCancelPipeline(rec, apiDetachedRequest(http.MethodDelete, id))
			if foreignCtx.Err() != nil {
				t.Fatal("cancellation selected another project's colliding registry entry")
			}
			if hasOwner {
				if rec.Code != http.StatusAccepted || owner.Context().Err() == nil || !strings.Contains(rec.Body.String(), `"cancellation_requested"`) {
					t.Fatalf("configured owner did not acknowledge cancellation: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != http.StatusConflict {
				t.Fatalf("unowned state cancelled a registry handle: %d %s", rec.Code, rec.Body.String())
			}
			after, err := os.ReadFile(filepath.Join(root, ".ntm", "pipelines", id+".json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("controller forged terminal state before executor cleanup")
			}
		})
	}
}

func TestRESTPipelineCancellationHonorsPreCanceledRequests(t *testing.T) {
	root := t.TempDir()
	id := pipeline.GenerateRunID()
	if err := pipeline.SaveState(root, &pipeline.ExecutionState{RunID: id, Status: pipeline.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	owner, err := pipeline.AcquireRunControl(context.Background(), root, id)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	registeredCtx, cancelRegistered := context.WithCancel(context.Background())
	defer cancelRegistered()
	pipeline.RegisterPipeline(pipeline.NewTrackedExecution(id, "local", "local", 1, pipeline.NewExecutor(pipeline.DefaultExecutorConfig("local")), cancelRegistered))
	t.Cleanup(func() {
		pipeline.UpdatePipelineFromState(id, &pipeline.ExecutionState{RunID: id, Status: pipeline.StatusCompleted})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := apiDetachedRequest(http.MethodPost, id)
	// Preserve the route context while replacing its cancellation parent.
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	(&Server{projectDir: root}).handleCancelPipeline(rec, req)
	if rec.Code != http.StatusConflict || owner.Context().Err() != nil || registeredCtx.Err() != nil {
		t.Fatalf("pre-canceled request changed a live execution: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRESTPipelineInspectionNeverFallsBackToForeignRegistry(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id := pipeline.GenerateRunID()
	foreignConfig := pipeline.DefaultExecutorConfig("foreign")
	foreignConfig.ProjectDir = t.TempDir()
	foreignCtx, cancelForeign := context.WithCancel(context.Background())
	defer cancelForeign()
	pipeline.RegisterPipeline(pipeline.NewTrackedExecution(id, "foreign-secret", "foreign", 1, pipeline.NewExecutor(foreignConfig), cancelForeign))
	t.Cleanup(func() {
		pipeline.UpdatePipelineFromState(id, &pipeline.ExecutionState{RunID: id, Status: pipeline.StatusCompleted})
	})
	srv := &Server{projectDir: cwd}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		rec := httptest.NewRecorder()
		if method == http.MethodGet {
			srv.handleGetPipeline(rec, apiDetachedRequest(method, id))
		} else {
			srv.handleCancelPipeline(rec, apiDetachedRequest(method, id))
		}
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "foreign-secret") || foreignCtx.Err() != nil {
			t.Fatalf("cwd-scoped request used a foreign registry entry: %d %s", rec.Code, rec.Body.String())
		}
	}
}

const apiDetachedResumeRunID = "run-http-detached-resume"

// The canonical executor completes a prefix and then stops during a loop's
// delay. No workflow command is dispatched: the authored conditions are false.
// This exercises real worker ownership and persistence without requiring tmux
// or native process-group supervision on the test host.
func interruptedAPIDetachedResume(t *testing.T, root string) *pipeline.ExecutionState {
	t.Helper()
	workflow := &pipeline.Workflow{
		SchemaVersion: pipeline.SchemaVersion, Name: "http-detached-resume",
		Settings: pipeline.WorkflowSettings{OnError: pipeline.ErrorActionFail}, Steps: []pipeline.Step{
			{ID: "once", Loop: &pipeline.LoopConfig{Times: 1, Steps: []pipeline.Step{
				{ID: "once_body", When: "false", Command: "exit 91"},
			}}},
			{ID: "delay", DependsOn: []string{"once"}, Loop: &pipeline.LoopConfig{
				Items: `[0,1,2]`, Delay: pipeline.Duration{Duration: time.Second},
				Steps: []pipeline.Step{{ID: "tick", When: "false", Command: "exit 92"}},
			}},
			{ID: "finish", DependsOn: []string{"delay"}, Loop: &pipeline.LoopConfig{Times: 1, Steps: []pipeline.Step{
				{ID: "finish_body", When: "false", Command: "exit 93"},
			}}},
		},
	}
	cfg := pipeline.DefaultExecutorConfig("saved-resume-session")
	cfg.ProjectDir, cfg.RunID = root, apiDetachedResumeRunID
	cfg.GlobalTimeout = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := make(chan pipeline.ProgressEvent, 32)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		for event := range progress {
			if event.Type == "loop_start" && event.StepID == "delay" {
				cancel()
			}
		}
	}()
	state, err := pipeline.RunControlledPipeline(ctx, workflow, map[string]interface{}{"saved": "keep"}, cfg, progress)
	close(progress)
	<-watchDone
	if !errors.Is(err, context.Canceled) || state == nil || state.Status != pipeline.StatusCancelled || state.Steps["once"].Status != pipeline.StatusCompleted {
		t.Fatalf("fixture did not persist a completed prefix before interruption: state=%+v error=%v", state, err)
	}
	prior, err := pipeline.LoadState(root, cfg.RunID)
	if err != nil || prior.Steps["once"].Status != pipeline.StatusCompleted || prior.Steps["finish"].Status == pipeline.StatusCompleted || len(prior.CommandExecutions) != 0 {
		t.Fatalf("interrupted checkpoint lost its prefix or dispatched a skipped command: state=%+v error=%v", prior, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if state, err := pipeline.LoadState(root, cfg.RunID); err == nil && (state.Status == pipeline.StatusRunning || state.Status == pipeline.StatusPending) {
			_ = pipeline.RequestRunCancellation(ctx, root, cfg.RunID)
		}
	})
	return prior
}

func useAPIDetachedResumeWorker(t *testing.T) {
	t.Helper()
	previous := pipeline.BackgroundWorkerFlags
	pipeline.BackgroundWorkerFlags = func() ([]string, error) {
		return []string{"-test.run=^TestAPIDetachedPipelineProcess$", "--"}, nil
	}
	t.Cleanup(func() { pipeline.BackgroundWorkerFlags = previous })
}

func TestRESTBackgroundResumeSurvivesServerExit(t *testing.T) {
	root := t.TempDir()
	prior := interruptedAPIDetachedResume(t, root)
	id := launchAPIDetachedFixture(t, root, "resume")
	if id != prior.RunID {
		t.Fatalf("resume replaced its saved run ID: got %q want %q", id, prior.RunID)
	}
	state := awaitAPIDetachedState(t, root, id, pipeline.StatusCompleted)
	if !reflect.DeepEqual(state.Steps["once"], prior.Steps["once"]) || state.Steps["delay"].Status != pipeline.StatusCompleted || state.Steps["finish"].Status != pipeline.StatusCompleted {
		t.Fatalf("detached continuation repeated its prefix or lost downstream work: %+v", state.Steps)
	}
	if state.Session != prior.Session || state.Variables["saved"] != "keep" || len(state.CommandExecutions) != 0 {
		t.Fatalf("detached continuation changed saved inputs or ran an excluded command: %+v", state)
	}
	// A new server has no in-process registry entry for the resumed worker.
	srv := &Server{projectDir: root}
	rec := httptest.NewRecorder()
	srv.handleGetPipeline(rec, apiDetachedRequest(http.MethodGet, id))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"completed"`) {
		t.Fatalf("fresh server cannot inspect detached resume: %d %s", rec.Code, rec.Body)
	}
}

func TestRESTBackgroundResumeOwnsLifetimeAfterRequestCancellation(t *testing.T) {
	root := t.TempDir()
	prior := interruptedAPIDetachedResume(t, root)
	useAPIDetachedResumeWorker(t)
	srv := NewHermeticServer("test")
	defer srv.Stop()
	srv.projectDir = root
	ctx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	path := "/api/v1/pipelines/" + prior.RunID + "/resume"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"background":true,"variables":{},"max_selector_output_bytes_total":12345,"max_step_count_total":40,"max_materialized_step_nodes":50}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	var accepted struct {
		Success    bool   `json:"success"`
		RunID      string `json:"run_id"`
		Session    string `json:"session"`
		Status     string `json:"status"`
		Background bool   `json:"background"`
		Resumed    bool   `json:"resumed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil || rec.Code != http.StatusAccepted || !accepted.Success || !accepted.Background || !accepted.Resumed || accepted.RunID != prior.RunID || accepted.Session != prior.Session || accepted.Status != "pending" {
		t.Fatalf("detached startup did not acknowledge the original run: %d %s error=%v", rec.Code, rec.Body, err)
	}
	cancelRequest()
	awaitAPIDetachedState(t, root, prior.RunID, pipeline.StatusRunning)
	// Inspect the actual immutable worker request: the HTTP overrides must
	// reach child preparation, while omission of session retains its default.
	requests, err := filepath.Glob(filepath.Join(root, ".ntm", "pipelines", "background", "resume-*.json"))
	if err != nil || len(requests) != 1 {
		t.Fatalf("resume did not create one private attempt: %v %v", requests, err)
	}
	data, err := os.ReadFile(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	var launch struct {
		Resume    bool                   `json:"resume"`
		Variables map[string]interface{} `json:"variables"`
		Config    struct {
			RunID         string                 `json:"run_id"`
			Session       string                 `json:"session"`
			ResumeOptions pipeline.ResumeOptions `json:"resume_options"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &launch); err != nil || !launch.Resume || launch.Config.RunID != prior.RunID || launch.Config.Session != "" || len(launch.Variables) != 0 || launch.Config.ResumeOptions.MaxSelectorOutputBytesTotal != 12345 || launch.Config.ResumeOptions.MaxStepCountTotal != 40 || launch.Config.ResumeOptions.MaxMaterializedStepNodes != 50 {
		t.Fatalf("HTTP resume changed the detached worker inputs: %s error=%v", data, err)
	}
	// Cancellation is addressed to the same run through a fresh server,
	// not to the already-ended request or a process-local registry handle.
	fresh := &Server{projectDir: root}
	cancelled := httptest.NewRecorder()
	fresh.handleCancelPipeline(cancelled, apiDetachedRequest(http.MethodPost, prior.RunID))
	if cancelled.Code != http.StatusAccepted || !strings.Contains(cancelled.Body.String(), "cancellation_requested") {
		t.Fatalf("fresh server could not cancel the resumed worker: %d %s", cancelled.Code, cancelled.Body)
	}
	final := awaitAPIDetachedState(t, root, prior.RunID, pipeline.StatusCancelled)
	if !reflect.DeepEqual(final.Steps["once"], prior.Steps["once"]) || final.Steps["finish"].Status == pipeline.StatusCompleted || len(final.CommandExecutions) != 0 {
		t.Fatalf("cancellation repeated the prefix or dispatched downstream work: %+v", final)
	}
}

func TestRESTBackgroundResumeRejectsInvalidInputsBeforeLaunch(t *testing.T) {
	for _, body := range []string{
		`{"background":true,"variables":{"saved":"replace"}}`,
		`{"background":true,"session":"bad:session"}`,
		`{"background":true,"max_selector_output_bytes_total":-1}`,
		`{"background":true,"max_step_count_total":-1}`,
		`{"background":true,"max_materialized_step_nodes":-1}`,
	} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			prior := interruptedAPIDetachedResume(t, root)
			before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			srv := NewHermeticServer("test")
			defer srv.Stop()
			srv.projectDir = root
			req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/"+prior.RunID+"/resume", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), ErrCodeBadRequest) {
				t.Fatalf("unsupported background request was accepted: %d %s", rec.Code, rec.Body)
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) {
				t.Fatal("rejected request rewrote its checkpoint")
			}
			if _, err := os.Stat(filepath.Join(root, ".ntm", "pipelines", "background")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid inputs reached detached launch: %v", err)
			}
		})
	}
}

func TestRESTBackgroundResumeRefusesUnacceptedStartup(t *testing.T) {
	for _, failure := range []string{"live owner", "cancelled request", "launcher failure", "outside workflow", "unconfigured project"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			prior := interruptedAPIDetachedResume(t, root)
			useAPIDetachedResumeWorker(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantStatus, wantCode := http.StatusBadRequest, ErrCodePipelineFailed
			var owner *pipeline.RunControl
			var err error
			switch failure {
			case "live owner":
				owner, err = pipeline.AcquireRunControl(context.Background(), root, prior.RunID)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
				wantStatus, wantCode = http.StatusConflict, ErrCodePipelineRunning
			case "cancelled request":
				cancel()
				wantStatus, wantCode = http.StatusGatewayTimeout, ErrCodeTimeout
			case "launcher failure":
				pipeline.BackgroundWorkerFlags = func() ([]string, error) { return nil, errors.New("worker configuration unavailable") }
			case "outside workflow":
				foreign := filepath.Join(t.TempDir(), "outside.yaml")
				if err := os.WriteFile(foreign, []byte("must not load this outside workflow"), 0600); err != nil {
					t.Fatal(err)
				}
				prior.WorkflowFile = foreign
				if err := pipeline.SaveState(root, prior); err != nil {
					t.Fatal(err)
				}
			}
			before := pipelineRecoveryHTTPJournal(t, root, prior.RunID)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/"+prior.RunID+"/resume", strings.NewReader(`{"background":true}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			srv := NewHermeticServer("test")
			defer srv.Stop()
			srv.projectDir = root
			if failure == "unconfigured project" {
				t.Chdir(root)
				srv.projectDir = ""
				wantCode = ErrCodeInvalidWorkflow
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != wantStatus || !strings.Contains(rec.Body.String(), wantCode) {
				t.Fatalf("unaccepted worker startup reported success or lost its cause: %d %s", rec.Code, rec.Body)
			}
			if !bytes.Equal(before, pipelineRecoveryHTTPJournal(t, root, prior.RunID)) || (owner != nil && owner.Context().Err() != nil) {
				t.Fatal("refused startup changed the checkpoint or cancelled its existing owner")
			}
			if failure == "outside workflow" && strings.Contains(rec.Body.String(), "must not load") {
				t.Fatal("out-of-project workflow was read before confinement")
			}
		})
	}
}

func TestRESTBackgroundResumeKeepsPermissionAndProjectScope(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleViewer}
	projectA, projectB := srv.projectDir, t.TempDir()
	prior := interruptedAPIDetachedResume(t, projectA)
	other := interruptedAPIDetachedResume(t, projectB)
	useAPIDetachedResumeWorker(t)
	path := "/api/v1/pipelines/" + prior.RunID + "/resume"
	const body = `{"background":true}`
	beforeA, beforeB := pipelineRecoveryHTTPJournal(t, projectA, prior.RunID), pipelineRecoveryHTTPJournal(t, projectB, other.RunID)
	rec, _ := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodPost, path, body))
	if rec.Code != http.StatusForbidden || !bytes.Equal(beforeA, pipelineRecoveryHTTPJournal(t, projectA, prior.RunID)) {
		t.Fatalf("viewer launched or changed a pipeline: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(projectA, ".ntm", "pipelines", "background")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("permission refusal reached detached launch: %v", err)
	}
	srv.auth.Role = RoleAdmin
	owner, err := pipeline.AcquireRunControl(context.Background(), projectB, other.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	const key = "detached-resume-scope"
	rec, accepted := pipelineScopeHTTPSwitchDuringAdmission(t, srv, path, body, key, projectB)
	if rec.Code != http.StatusAccepted || !accepted.Success || accepted.RunID != prior.RunID {
		t.Fatalf("admitted resume followed the new project's owner: %d %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(beforeB, pipelineRecoveryHTTPJournal(t, projectB, other.RunID)) || owner.Context().Err() != nil {
		t.Fatal("project A's resume mutated or cancelled project B")
	}
	pipelineScopeHTTPPatchProject(t, srv, projectA)
	rec, replayed := pipelineScopeHTTPPost(t, srv, path, body, key)
	if rec.Code != http.StatusAccepted || !replayed.Success || rec.Header().Get("X-Idempotent-Replay") != "true" {
		t.Fatalf("accepted resume lost its HTTP replay identity: %d %s", rec.Code, rec.Body)
	}
	requests, err := filepath.Glob(filepath.Join(projectA, ".ntm", "pipelines", "background", "resume-*.json"))
	if err != nil || len(requests) != 1 {
		t.Fatalf("HTTP replay launched another resume attempt: %v %v", requests, err)
	}
	final := awaitAPIDetachedState(t, projectA, prior.RunID, pipeline.StatusCompleted)
	if !reflect.DeepEqual(final.Steps["once"], prior.Steps["once"]) || final.Steps["finish"].Status != pipeline.StatusCompleted {
		t.Fatalf("project-scoped continuation lost its completed prefix or tail: %+v", final.Steps)
	}
}
