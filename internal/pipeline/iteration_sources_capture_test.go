//go:build !windows

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIterationSourceCapturePreservesCompleteResults(t *testing.T) {
	r := &IterationSourceResolver{ProjectDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	models, err := r.ResolveModels(ctx, StringOrList{"$(printf 'cc\ncod\n')"})
	if err != nil || !reflect.DeepEqual(models, []string{"cc", "cod"}) {
		t.Fatalf("models = %v: %v", models, err)
	}
	beads, err := r.ResolveBeads(ctx, `$(printf '["bd-a","bd-b"]')`)
	if err != nil || !reflect.DeepEqual(beads, []interface{}{"bd-a", "bd-b"}) {
		t.Fatalf("beads = %v: %v", beads, err)
	}
	debates, err := r.ResolveDebates(ctx, "$(printf 'DEBATE-1\nDEBATE-2\n')")
	if err != nil || !reflect.DeepEqual(debates, []interface{}{"DEBATE-1", "DEBATE-2"}) {
		t.Fatalf("debates = %v: %v", debates, err)
	}
	pairs, err := r.ResolvePairs(ctx, "$(printf 'DEBATE-1|H1|H2|pane1|pane2\n')")
	if err != nil || len(pairs) != 1 || pairs[0].(map[string]interface{})["debate_id"] != "DEBATE-1" {
		t.Fatalf("pairs = %v: %v", pairs, err)
	}
}

func TestIterationSourceCaptureRejectsOverflowBeforeParsing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "oversized"), []byte(strings.Repeat("x", DefaultMaxCommandStdoutBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	r := &IterationSourceResolver{ProjectDir: root}
	for _, source := range []string{"models", "beads", "pairs", "debates"} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var err error
			count := 0
			switch source {
			case "models":
				items, e := r.ResolveModels(ctx, StringOrList{"$(cat oversized)"})
				count, err = len(items), e
			case "beads":
				items, e := r.ResolveBeads(ctx, "$(cat oversized)")
				count, err = len(items), e
			case "pairs":
				items, e := r.ResolvePairs(ctx, "$(cat oversized)")
				count, err = len(items), e
			case "debates":
				items, e := r.ResolveDebates(ctx, "$(cat oversized)")
				count, err = len(items), e
			}
			if count != 0 || !errors.Is(err, errCommandOutputLimit) {
				t.Fatalf("oversized %s reached work parsing: count=%d error=%v", source, count, err)
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := r.ResolveBeads(ctx, "$(printf bd-partial; cat oversized >&2)")
	if len(items) != 0 || !errors.Is(err, errCommandOutputLimit) {
		t.Fatalf("stderr overflow admitted partial bead work: count=%d error=%v", len(items), err)
	}
}

func TestIterationSourceCaptureBoundsRealBrSubprocess(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > args\ncat response\n"
	if err := os.WriteFile(filepath.Join(bin, "br"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	response := filepath.Join(root, "response")
	if err := os.WriteFile(response, []byte(`{"issues":[{"id":"bd-live","title":"Live","status":"open"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := &IterationSourceResolver{ProjectDir: root}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	items, err := r.ResolveBeads(ctx, "label:ready")
	if err != nil || len(items) != 1 || items[0].(map[string]interface{})["id"] != "bd-live" {
		t.Fatalf("br source capture = %v: %v", items, err)
	}
	args, err := os.ReadFile(filepath.Join(root, "args"))
	if err != nil || !strings.Contains(string(args), "--limit\n0\n") {
		t.Fatalf("source capture lost unlimited br query: %q %v", args, err)
	}
	if err := os.WriteFile(response, []byte(strings.Repeat("x", DefaultMaxCommandStdoutBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	items, err = r.ResolveBeads(ctx, "label:ready")
	if len(items) != 0 || !errors.Is(err, errCommandOutputLimit) {
		t.Fatalf("oversized br output reached parser: count=%d error=%v", len(items), err)
	}
}

func TestIterationSourceCaptureRejectsCancelledInjectedWork(t *testing.T) {
	for _, br := range []bool{false, true} {
		for _, before := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if before {
				cancel()
			}
			called := false
			r := &IterationSourceResolver{
				RunShell: func(context.Context, string) ([]byte, error) {
					called = true
					cancel()
					return []byte("bd-stale"), nil
				},
				RunBr: func(context.Context, []string) ([]byte, error) {
					called = true
					cancel()
					return []byte(`{"issues":[{"id":"bd-stale"}]}`), nil
				},
			}
			expr := "$(unused)"
			if br {
				expr = "label:unused"
			}
			items, err := r.ResolveBeads(ctx, expr)
			cancel()
			if len(items) != 0 || !errors.Is(err, context.Canceled) || (before && called) {
				t.Fatalf("br=%v before=%v admitted cancelled work: items=%v err=%v called=%v", br, before, items, err, called)
			}
		}
	}
}

func TestIterationSourceCaptureRejectsOversizedInjectedWork(t *testing.T) {
	data := []byte(strings.Repeat("x", DefaultMaxCommandStdoutBytes+1))
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return data, nil },
		RunBr:    func(context.Context, []string) ([]byte, error) { return data, nil },
	}
	for _, expr := range []string{"$(unused)", "label:unused"} {
		items, err := r.ResolveBeads(context.Background(), expr)
		if len(items) != 0 || !errors.Is(err, errCommandOutputLimit) {
			t.Fatalf("injected source bypassed output admission: count=%d err=%v", len(items), err)
		}
	}
}

func TestIterationSourceCaptureOwnsRedirectedDescendant(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("unreaped-leader process ownership requires the Linux waiter")
	}
	root := t.TempDir()
	child := `printf '%s' "$$" > child.pid
while [ ! -f release ]; do sleep 0.01; done
printf orphan > late-effect
`
	if err := os.WriteFile(filepath.Join(root, "child.sh"), []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	r := &IterationSourceResolver{ProjectDir: root}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type outcome struct {
		items []string
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		items, err := r.ResolveModels(ctx, StringOrList{"$(/bin/sh child.sh >/dev/null 2>&1 & printf cc)"})
		done <- outcome{items, err}
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for pid == 0 && time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(root, "child.pid"))
		pid, _ = strconv.Atoi(string(data))
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if pid == 0 {
		cancel()
		<-done
		t.Fatal("iteration-source child did not start")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Kill()
	select {
	case result := <-done:
		t.Fatalf("iteration source returned before descendant cleanup: items=%v error=%v", result.items, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	result := <-done
	if len(result.items) != 0 || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("cancelled source admitted work: %v %v", result.items, result.err)
	}
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("new work"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "late-effect")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source child performed work after cancellation returned: %v", err)
	}
}
