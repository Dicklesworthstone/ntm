package cass

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type contextSearchExecutorFunc func(context.Context, ...string) ([]byte, error)

func (f contextSearchExecutorFunc) Run(ctx context.Context, args ...string) ([]byte, error) {
	return f(ctx, args...)
}

func contextResponse(hits ...SearchHit) []byte {
	data, err := json.Marshal(SearchResponse{Hits: hits, TotalMatches: 19})
	if err != nil {
		panic(err)
	}
	return data
}

func contextHasWorkspace(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--workspace=") {
			return true
		}
	}
	return false
}

func TestContextSearchIncludesProjectBeyondGlobalLimit(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	var mu sync.Mutex
	var calls [][]string
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, append([]string(nil), args...))
		mu.Unlock()
		if contextHasWorkspace(args) {
			return contextResponse(SearchHit{SourcePath: "opaque-local", Workspace: project, Snippet: "LOCAL_HISTORY", Score: 0.9}), nil
		}
		return contextResponse(SearchHit{SourcePath: "foreign", Workspace: project + "-other", Content: "GLOBAL_HISTORY", Score: 1}), nil
	})))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{
		Query: "--literal query", Workspace: project, Limit: 1, Since: "7d", Agents: []string{"claude", "codex"},
	})
	if err != nil || len(out.Hits) != 2 || out.Hits[0].Content != "GLOBAL_HISTORY" || out.Hits[1].Snippet != "LOCAL_HISTORY" || out.TotalMatches != 19 {
		t.Fatalf("local history remained hidden behind global limit: %+v %v", out, err)
	}
	if len(calls) != 2 || !out.WorkspaceQueried || out.Degradation() != "" {
		t.Fatalf("wrong query count/evidence: %v %+v", calls, out)
	}
	for _, args := range calls {
		if !reflect.DeepEqual(args[len(args)-2:], []string{"--", "--literal query"}) {
			t.Fatalf("query escaped its positional boundary: %v", args)
		}
		for _, want := range []string{"--limit=1", "--since=7d", "--agent=claude", "--agent=codex"} {
			found := false
			for _, arg := range args {
				found = found || arg == want
			}
			if !found {
				t.Fatalf("scope dropped requested filter %q: %v", want, args)
			}
		}
	}
}

func TestContextSearchRejectsUnverifiedWorkspaceResults(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	foreign := filepath.Join(t.TempDir(), "project")
	line := 3
	global := SearchHit{SourcePath: "same", LineNumber: &line, Workspace: foreign, Content: "foreign"}
	local := []SearchHit{
		{SourcePath: "same", LineNumber: &line, Workspace: project, Content: "contradictory"},
		{SourcePath: "prefix", Workspace: project + "-backup"},
		{SourcePath: "basename", Workspace: "project"},
		{SourcePath: "missing"},
		{SourcePath: "valid", Workspace: project, Content: "local"},
	}
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(_ context.Context, args ...string) ([]byte, error) {
		if contextHasWorkspace(args) {
			return contextResponse(local...), nil
		}
		return contextResponse(global), nil
	})))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "auth", Workspace: project, Limit: 10})
	if err != nil || len(out.Hits) != 2 || out.Hits[0].Workspace != foreign || out.Hits[1].Content != "local" || out.RejectedWorkspaceHits != 4 {
		t.Fatalf("query scope fabricated project identity: %+v %v", out, err)
	}
}

func TestContextSearchDeduplicatesOnlyKnownMessageIdentity(t *testing.T) {
	project := t.TempDir()
	line := 7
	global := []SearchHit{
		{SourcePath: "same", LineNumber: &line, Agent: "codex", Content: "full original"},
		{SourcePath: "unknown", Content: "first message"},
	}
	local := []SearchHit{
		{SourcePath: "same", LineNumber: &line, Agent: "codex", Workspace: project, Snippet: "summary"},
		{SourcePath: "unknown", Workspace: project, Content: "different message"},
	}
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(_ context.Context, args ...string) ([]byte, error) {
		if contextHasWorkspace(args) {
			return contextResponse(local...), nil
		}
		return contextResponse(global...), nil
	})))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "auth", Workspace: project})
	if err != nil || len(out.Hits) != 3 || out.DuplicateHits != 1 || out.Hits[0].Content != "full original" || out.Hits[0].Workspace != project {
		t.Fatalf("duplicate merge lost evidence or collapsed unknown identities: %+v %v", out, err)
	}
}

func TestContextSearchSharesBudgetAndKeepsSuccessfulScope(t *testing.T) {
	for _, failGlobal := range []bool{false, true} {
		t.Run(map[bool]string{false: "workspace timeout", true: "global timeout"}[failGlobal], func(t *testing.T) {
			project := t.TempDir()
			var active atomic.Int32
			started := make(chan struct{}, 2)
			client := NewClient(WithTimeout(80*time.Millisecond), WithExecutor(contextSearchExecutorFunc(func(ctx context.Context, args ...string) ([]byte, error) {
				active.Add(1)
				defer active.Add(-1)
				started <- struct{}{}
				if contextHasWorkspace(args) != failGlobal {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return contextResponse(SearchHit{SourcePath: "success", Workspace: project, Content: "usable"}), nil
			})))
			parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			out, err := client.SearchContext(parent, ContextSearchOptions{Query: "auth", Workspace: project})
			if err != nil || len(out.Hits) != 1 || active.Load() != 0 || len(started) != 2 || parent.Err() != nil {
				t.Fatalf("timeout lost success or abandoned a search: %+v %v active=%d", out, err, active.Load())
			}
			if failGlobal && (!errors.Is(out.GlobalError, context.DeadlineExceeded) || out.TotalMatches != 1) {
				t.Fatalf("invented a global count or lost failure: %+v", out)
			}
			if !failGlobal && !errors.Is(out.WorkspaceError, context.DeadlineExceeded) {
				t.Fatalf("workspace deadline was hidden: %+v", out)
			}
			if out.Degradation() == "" {
				t.Fatal("partial source failure was silent")
			}
		})
	}
}

func TestContextSearchParentCancellationJoinsBothScopes(t *testing.T) {
	project := t.TempDir()
	started := make(chan struct{}, 2)
	var active atomic.Int32
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(ctx context.Context, _ ...string) ([]byte, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		// Even a backend racing cancellation with valid JSON cannot publish it.
		return contextResponse(SearchHit{Workspace: project, Content: "must not survive"}), nil
	})))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		out, err := client.SearchContext(ctx, ContextSearchOptions{Query: "auth", Workspace: project})
		if out != nil {
			t.Error("cancelled operation retained candidates")
		}
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("both scopes were not started concurrently")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || active.Load() != 0 {
			t.Fatalf("cancellation did not join queries: %v active=%d", err, active.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stalled")
	}
}

func TestContextSearchGlobalOnlyAndResponseBounds(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(_ context.Context, args ...string) ([]byte, error) {
		calls.Add(1)
		if contextHasWorkspace(args) {
			t.Error("unrequested workspace search")
		}
		return contextResponse(make([]SearchHit, 20)...), nil
	})))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "auth"})
	if err != nil || out.WorkspaceQueried || len(out.Hits) != 5 || calls.Load() != 1 {
		t.Fatalf("global-only/default limit changed: %+v %v calls=%d", out, err, calls.Load())
	}
}

func TestContextSearchRefusesInvalidOrCancelledInput(t *testing.T) {
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(context.Context, ...string) ([]byte, error) {
		t.Error("invalid request invoked the executor")
		return nil, errors.New("unexpected execution")
	})))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, request := range []struct {
		ctx       context.Context
		workspace string
	}{
		{nil, ""}, {ctx, ""}, {context.Background(), "relative"}, {context.Background(), t.TempDir() + "\n"},
	} {
		if _, err := client.SearchContext(request.ctx, ContextSearchOptions{Query: "auth", Workspace: request.workspace}); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestContextSearchFailuresDoNotBecomeEmptySuccess(t *testing.T) {
	cause := errors.New("PRIVATE_QUERY diagnostic")
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(context.Context, ...string) ([]byte, error) { return nil, cause })))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "auth", Workspace: t.TempDir()})
	if !errors.Is(err, cause) || out == nil || len(out.Hits) != 0 || strings.Contains(out.Degradation(), "PRIVATE_QUERY") {
		t.Fatalf("failure hidden or diagnostic leaked: %+v %v", out, err)
	}
}

func TestContextSearchDuplicateFillsMissingEvidenceWithoutReplacingScore(t *testing.T) {
	project := t.TempDir()
	line := 7
	created := &FlexTime{Time: time.Now().UTC()}
	client := NewClient(WithExecutor(contextSearchExecutorFunc(func(_ context.Context, args ...string) ([]byte, error) {
		if contextHasWorkspace(args) {
			return contextResponse(SearchHit{SourcePath: "same", LineNumber: &line, Workspace: project, Content: "full evidence", CreatedAt: created, Score: 0.2}), nil
		}
		return contextResponse(SearchHit{SourcePath: "same", LineNumber: &line, Snippet: "summary", CreatedAt: &FlexTime{}, Score: 0.9}), nil
	})))
	out, err := client.SearchContext(context.Background(), ContextSearchOptions{Query: "auth", Workspace: project})
	if err != nil || len(out.Hits) != 1 {
		t.Fatalf("duplicate was not merged: %+v %v", out, err)
	}
	hit := out.Hits[0]
	if hit.Content != "full evidence" || hit.Snippet != "summary" || hit.Score != 0.9 || hit.CreatedAt == nil || !hit.CreatedAt.Equal(created.Time) {
		t.Fatalf("merge lost full content/date or replaced existing rank evidence: %+v", hit)
	}
}
