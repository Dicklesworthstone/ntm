//go:build !windows

package robot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func lifecycleCASSBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cass")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func lifecycleCASSConfig(t *testing.T, marker string) CASSConfig {
	t.Helper()
	cfg := DefaultCASSConfig()
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	cfg.BinaryPath = lifecycleCASSBinary(t, "printf started > "+quoted+"\nexec sleep 30")
	cfg.Timeout = 30 * time.Second
	return cfg
}

func awaitCASSLifecycleStart(t *testing.T, marker string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("CASS search never reached its real subprocess")
		}
	}
}

func TestCASSQueryOperationContextRefusesBeforeLaunch(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name    string
		parents []context.Context
		want    string
	}{
		{"cancelled", []context.Context{cancelled}, "context canceled"},
		{"nil", []context.Context{nil}, "non-nil parent"},
		{"ambiguous", []context.Context{context.Background(), context.Background()}, "at most one"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "started")
			cfg := lifecycleCASSConfig(t, marker)
			out := QueryCASS("database transaction contention", cfg, tc.parents...)
			if out.Success || len(out.Hits) != 0 || !strings.Contains(out.Error, tc.want) {
				t.Fatalf("unexpected refusal: %+v", out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("refused query launched a process: %v", err)
			}
		})
	}
}

func TestCASSQueryOperationCancellationStopsRunningSearch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cfg := lifecycleCASSConfig(t, marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan CASSQueryResult, 1)
	go func() { done <- QueryCASS("database transaction contention", cfg, ctx) }()
	awaitCASSLifecycleStart(t, marker)
	cancel()
	select {
	case out := <-done:
		if out.Success || len(out.Hits) != 0 || !strings.Contains(out.Error, "context canceled") {
			t.Fatalf("cancelled search became success or usable data: %+v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("search ignored its operation cancellation and retained the 30s search budget")
	}
}

func TestCASSQueryOperationAndSearchDeadlines(t *testing.T) {
	for _, parentWins := range []bool{true, false} {
		t.Run(fmt.Sprintf("parent_wins_%t", parentWins), func(t *testing.T) {
			cfg := DefaultCASSConfig()
			cfg.BinaryPath = lifecycleCASSBinary(t, "exec sleep 30")
			cfg.Timeout = 30 * time.Second
			parentTimeout := 50 * time.Millisecond
			want := "context deadline exceeded"
			if !parentWins {
				parentTimeout, cfg.Timeout = 30*time.Second, 50*time.Millisecond
				want = "cass search timed out after 50ms"
			}
			ctx, cancel := context.WithTimeout(context.Background(), parentTimeout)
			defer cancel()
			start := time.Now()
			out := QueryCASS("database transaction contention", cfg, ctx)
			if out.Success || !strings.Contains(out.Error, want) {
				t.Fatalf("wrong deadline result: %+v", out)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("the shorter deadline did not bound search")
			}
		})
	}
}

func TestPromptContextCancellationJoinsSearchAndDoesNotStartMemory(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cfg := lifecycleCASSConfig(t, marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		prompt string
		cass   *CASSInjectionInfo
		memory *CMInjectionInfo
	}
	done := make(chan result, 1)
	opts := PromptContextOptions{WithCASS: true, WithMemory: true, CASSConfig: &cfg}
	go func() {
		prompt, cass, memory := injectPromptContext(ctx, "database transaction contention", "original task", FormatMarkdown, opts,
			func(cfg CMInjectConfig) (CMInjectConfig, *CMInjectionInfo) {
				t.Error("cancelled CASS lookup proceeded to memory scope resolution")
				return cfg, &CMInjectionInfo{SkippedReason: "test stops unexpected memory work"}
			})
		done <- result{prompt, cass, memory}
	}()
	awaitCASSLifecycleStart(t, marker)
	cancel()
	select {
	case out := <-done:
		if out.prompt != "original task" || out.cass == nil || out.memory == nil {
			t.Fatalf("lost original task or requested skip receipts: %+v", out)
		}
		if out.cass.ItemsInjected != 0 || out.cass.TokensAdded != 0 ||
			!strings.Contains(out.cass.SkippedReason, "context canceled") ||
			!strings.Contains(out.memory.SkippedReason, "context canceled") {
			t.Fatalf("cancelled retrieval claimed enrichment: cass=%+v memory=%+v", out.cass, out.memory)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared prompt pipeline failed to join a cancelled CASS process")
	}
}

func TestPromptContextAlreadyStoppedDoesNotResolveTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, parent := range []context.Context{ctx, nil} {
		for _, opts := range []PromptContextOptions{
			{WithCASS: true}, {WithMemory: true}, {WithCASS: true, WithMemory: true}, {},
		} {
			prompt, cass, memory := injectPromptContext(parent, "database transaction contention", "original task", FormatMarkdown, opts,
				func(cfg CMInjectConfig) (CMInjectConfig, *CMInjectionInfo) {
					t.Error("stopped prompt resolved memory")
					return cfg, &CMInjectionInfo{SkippedReason: "unexpected memory lookup"}
				})
			if prompt != "original task" || (cass != nil) != opts.WithCASS || (memory != nil) != opts.WithMemory {
				t.Fatalf("request/receipt contract changed: %q %+v %+v", prompt, cass, memory)
			}
		}
	}
}

func TestPromptContextCancellationDuringMemoryScopeDiscardsHistory(t *testing.T) {
	cfg := DefaultCASSConfig()
	cfg.BinaryPath = lifecycleCASSBinary(t, `printf '%s' '{"hits":[{"source_path":"session.jsonl","score":1,"content":"historical evidence"}],"total_matches":1}'`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scopeCalls := 0
	prompt, cass, memory := injectPromptContext(ctx, "database transaction contention", "original task", FormatMarkdown,
		PromptContextOptions{WithCASS: true, WithMemory: true, CASSConfig: &cfg},
		func(cfg CMInjectConfig) (CMInjectConfig, *CMInjectionInfo) {
			scopeCalls++
			cancel()
			return cfg, nil // Cancellation must be checked BEFORE querying CM.
		})
	if scopeCalls != 1 || prompt != "original task" || cass == nil || memory == nil {
		t.Fatalf("wrong scope cancellation result: calls=%d %q %+v %+v", scopeCalls, prompt, cass, memory)
	}
	if cass.ItemsInjected != 0 || cass.TokensAdded != 0 || !strings.Contains(memory.SkippedReason, "context canceled") {
		t.Fatalf("partial enrichment survived cancellation: %+v %+v", cass, memory)
	}
}

func TestCASSQueryConcurrentOperationContextsAreIndependent(t *testing.T) {
	cfg := DefaultCASSConfig()
	cfg.BinaryPath = lifecycleCASSBinary(t, `printf '%s' '{"hits":[{"source_path":"session.jsonl","score":1,"content":"historical evidence"}],"total_matches":1}'`)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(cancelFirst bool) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelFirst {
				cancel()
			}
			out := QueryCASS("database transaction contention", cfg, ctx)
			if out.Success == cancelFirst || (!cancelFirst && (len(out.Hits) != 1 || out.TotalMatches != 1)) {
				t.Errorf("query lifetime leaked across operations: cancelled=%t result=%+v", cancelFirst, out)
			}
		}(i%2 == 0)
	}
	wg.Wait()
}
