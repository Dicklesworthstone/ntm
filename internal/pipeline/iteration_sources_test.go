package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

var selectorPolicyKinds = []string{"branch", "models", "beads", "pairs", "debates", "br", "bead_query"}

// Use actual shell/br subprocesses through the controlled executor. A data
// callback would skip the process ownership and stream capture under test.
func selectorPolicyWorkflow(t *testing.T, kind, script string) (*Workflow, ExecutorConfig, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX process fixtures")
	}
	root := t.TempDir()
	output := "chosen\n"
	if kind == "pairs" {
		output = "DEBATE-1|H-1|H-2|pane-1|pane-2\n"
	}
	if kind == "br" || kind == "bead_query" {
		output = `{"issues":[{"id":"bd-1"}]}`
	}
	t.Setenv("NTM_SELECTOR_POLICY_RESULT", output)
	name := "selector-source"
	if kind == "br" || kind == "bead_query" {
		name = "br"
		t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []Step{{ID: "body", Command: "printf body > child-dispatched"}}
	step := Step{ID: "select"}
	switch kind {
	case "branch":
		step.Branch = "$(./selector-source)"
		step.Branches = map[string]interface{}{"chosen": body, "default": body}
	case "models":
		step.Foreach = &ForeachConfig{Models: StringOrList{"$(./selector-source)"}, Steps: body}
	case "beads":
		step.Foreach = &ForeachConfig{Beads: "$(./selector-source)", Steps: body}
	case "pairs":
		step.Foreach = &ForeachConfig{Pairs: "$(./selector-source)", Steps: body}
	case "debates":
		step.Foreach = &ForeachConfig{Debates: "$(./selector-source)", Steps: body}
	case "br":
		step.Foreach = &ForeachConfig{Beads: "hypothesis", Steps: body}
	case "bead_query":
		step.BeadQuery = &BeadQueryStep{Label: StringOrList{"hypothesis"}}
	default:
		t.Fatalf("unknown selector fixture %q", kind)
	}
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "selector-policy", Settings: DefaultWorkflowSettings(), Steps: []Step{
		step,
		{ID: "consume", DependsOn: []string{"select"}, Command: "printf downstream > dispatched"},
	}}
	cfg := DefaultExecutorConfig("selector-policy")
	cfg.ProjectDir, cfg.RunID = root, "selector-policy-run"
	return workflow, cfg, output
}

func assertSelectorPolicyNoDispatch(t *testing.T, cfg ExecutorConfig) {
	t.Helper()
	for _, name := range []string{"child-dispatched", "dispatched"} {
		if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid source admitted %s: %v", name, err)
		}
	}
}

func TestSelectorPolicyNativeOutputLimits(t *testing.T) {
	for _, kind := range selectorPolicyKinds {
		for _, stream := range []string{"stdout", "stderr", "exact"} {
			t.Run(kind+"/"+stream, func(t *testing.T) {
				script := `printf '%s' "$NTM_SELECTOR_POLICY_RESULT"`
				if stream == "stderr" {
					script += "; printf noise >&2"
				} else if stream == "exact" {
					script += "; printf note >&2"
				}
				workflow, cfg, output := selectorPolicyWorkflow(t, kind, script)
				workflow.Settings.Limits.MaxCommandStdoutBytes = int64(len(output))
				workflow.Settings.Limits.MaxCommandStderrBytes = 4
				if stream == "stdout" {
					workflow.Settings.Limits.MaxCommandStdoutBytes--
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				if stream == "exact" {
					if err != nil || state == nil || state.Status != StatusCompleted {
						t.Fatalf("exact-limit source failed: %+v %v", state, err)
					}
					if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "dispatched")); err != nil {
						t.Fatalf("complete source did not admit its consumer: %v", err)
					}
					return
				}
				if err == nil || state == nil || state.Status != StatusFailed {
					t.Fatalf("oversized %s was accepted: %+v %v", stream, state, err)
				}
				result := state.Steps["select"]
				if result.Error == nil || !strings.Contains(result.Error.Message, errCommandOutputLimit.Error()) || result.Output != "" {
					t.Fatalf("source did not reject the complete output: %+v", result)
				}
				if _, exists := state.CommandEvaluations["select"]; exists {
					t.Fatal("settled output-limit failure retained an evaluation receipt")
				}
				assertSelectorPolicyNoDispatch(t, cfg)
			})
		}
	}
}

func TestSelectorPolicyNativeTimeoutsTerminateChildren(t *testing.T) {
	const script = `trap 'wait; exit 0' TERM
/bin/sh -c '
trap "printf terminated > child-terminated; exit 0" TERM
printf ready > child-ready
while :; do sleep 30; done
' &
printf '%s' "$NTM_SELECTOR_POLICY_RESULT"
wait`
	for _, kind := range selectorPolicyKinds {
		for _, timeoutSource := range []string{"default", "step"} {
			t.Run(kind+"/"+timeoutSource, func(t *testing.T) {
				workflow, cfg, _ := selectorPolicyWorkflow(t, kind, script)
				cfg.DefaultTimeout = 300 * time.Millisecond
				if timeoutSource == "step" {
					cfg.DefaultTimeout = 5 * time.Second
					workflow.Steps[0].Timeout = Duration{Duration: 300 * time.Millisecond}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				if ctx.Err() != nil {
					t.Fatalf("selector ignored its own %s timeout: %v", timeoutSource, ctx.Err())
				}
				if err == nil || state == nil || state.Status != StatusFailed {
					t.Fatalf("timed-out source succeeded: %+v %v", state, err)
				}
				result := state.Steps["select"]
				if result.Error == nil || !strings.Contains(result.Error.Message, context.DeadlineExceeded.Error()) || result.Output != "" {
					t.Fatalf("deadline admitted partial source output: %+v", result)
				}
				for _, name := range []string{"child-ready", "child-terminated"} {
					if _, err := os.Stat(filepath.Join(cfg.ProjectDir, name)); err != nil {
						t.Fatalf("owned child did not complete cooperative termination (%s): %v", name, err)
					}
				}
				if _, exists := state.CommandEvaluations["select"]; exists {
					t.Fatal("cooperatively terminated source retained an evaluation receipt")
				}
				assertSelectorPolicyNoDispatch(t, cfg)
			})
		}
	}
}

func TestSelectorPolicyDeadlineDoesNotBoundChildBody(t *testing.T) {
	for _, kind := range []string{"branch", "beads"} {
		t.Run(kind, func(t *testing.T) {
			workflow, cfg, _ := selectorPolicyWorkflow(t, kind, `printf '%s' "$NTM_SELECTOR_POLICY_RESULT"`)
			cfg.DefaultTimeout = 3 * time.Second
			workflow.Steps[0].Timeout = Duration{Duration: time.Second}
			body := []Step{{ID: "body", Command: "sleep 1.2; printf body > child-dispatched"}}
			if kind == "branch" {
				workflow.Steps[0].Branches["chosen"] = body
			} else {
				workflow.Steps[0].Foreach.Steps = body
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("source deadline escaped into its body: %+v %v", state, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "child-dispatched")); err != nil {
				t.Fatalf("body did not finish independently of source deadline: %v", err)
			}
		})
	}
}

func TestSelectorPolicyDryRunExecutesNoDynamicSource(t *testing.T) {
	for _, kind := range selectorPolicyKinds {
		t.Run(kind, func(t *testing.T) {
			workflow, cfg, _ := selectorPolicyWorkflow(t, kind, `printf source > source-effect; printf '%s' "$NTM_SELECTOR_POLICY_RESULT"`)
			cfg.DryRun = true
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("source preview failed: %+v %v", state, err)
			}
			if _, err := os.Stat(filepath.Join(cfg.ProjectDir, "source-effect")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry run executed its dynamic source: %v", err)
			}
			if len(state.CommandEvaluations) != 0 || len(state.ForeachState) != 0 {
				t.Fatalf("dry run published source execution evidence: %+v %+v", state.CommandEvaluations, state.ForeachState)
			}
			assertSelectorPolicyNoDispatch(t, cfg)
		})
	}
}

func TestSelectorPolicyInjectedBrAdmission(t *testing.T) {
	for _, kind := range []string{"br", "bead_query"} {
		for _, failure := range []string{"limit", "deadline"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				workflow, cfg, output := selectorPolicyWorkflow(t, kind, "exit 99")
				workflow.Steps[0].Timeout = Duration{Duration: 100 * time.Millisecond}
				if failure == "limit" {
					workflow.Settings.Limits.MaxCommandStdoutBytes = int64(len(output) - 1)
				}
				calls := 0
				cfg.BeadQueryRunBr = func(ctx context.Context, _ []string) ([]byte, error) {
					calls++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > 200*time.Millisecond {
						return nil, errors.New("injected runner did not receive the selector deadline")
					}
					if failure == "deadline" {
						<-ctx.Done()
					}
					return []byte(output), nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				state, err := RunControlledPipeline(ctx, workflow, nil, cfg, nil)
				if err == nil || state == nil || state.Status != StatusFailed || calls != 1 || ctx.Err() != nil {
					t.Fatalf("injected runner bypassed source admission: %+v %v calls=%d context=%v", state, err, calls, ctx.Err())
				}
				want := errCommandOutputLimit.Error()
				if failure == "deadline" {
					want = context.DeadlineExceeded.Error()
				}
				if result := state.Steps["select"]; result.Error == nil || !strings.Contains(result.Error.Message, want) || result.Output != "" {
					t.Fatalf("injected result became authoritative after %s: %+v", failure, result)
				}
				assertSelectorPolicyNoDispatch(t, cfg)
			})
		}
	}
}

func TestSelectorPolicyDryRunPreservesStaticForeach(t *testing.T) {
	for _, kind := range []string{"items", "models"} {
		t.Run(kind, func(t *testing.T) {
			workflow, cfg, _ := selectorPolicyWorkflow(t, "models", "exit 99")
			cfg.DryRun = true
			if kind == "items" {
				workflow.Steps[0].Foreach.Items = `["one","two"]`
				workflow.Steps[0].Foreach.Models = nil
			} else {
				workflow.Steps[0].Foreach.Models = StringOrList{"cc", "cod"}
			}
			state, err := RunControlledPipeline(context.Background(), workflow, nil, cfg, nil)
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("static foreach preview failed: %+v %v", state, err)
			}
			for _, id := range []string{"select_iter0_body", "select_iter1_body"} {
				if result, exists := state.Steps[id]; !exists || result.Status != StatusCompleted {
					t.Fatalf("static preview lost materialized body %q: %+v", id, result)
				}
			}
			assertSelectorPolicyNoDispatch(t, cfg)
		})
	}
}

func TestResolveBeads_StructuredFormResolvesIDs(t *testing.T) {
	const fixture = `{
  "issues": [
    {"id": "bd-h001", "title": "First", "description": "d1", "labels": ["hypothesis"], "status": "active"},
    {"id": "bd-h002", "title": "Second", "description": "d2", "labels": ["hypothesis"], "status": "active"}
  ]
}`

	var capturedArgs []string
	r := &IterationSourceResolver{
		RunBr: func(_ context.Context, args []string) ([]byte, error) {
			capturedArgs = args
			return []byte(fixture), nil
		},
	}

	got, err := r.ResolveBeads(context.Background(), "hypothesis,state:active")
	if err != nil {
		t.Fatalf("ResolveBeads: %v", err)
	}
	wantArgs := []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "active"}
	if !reflect.DeepEqual(capturedArgs, wantArgs) {
		t.Fatalf("br args = %v, want %v", capturedArgs, wantArgs)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	wantIDs := []string{"bd-h001", "bd-h002"}
	for i, item := range got {
		m, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("item %d is %T, want map", i, item)
		}
		if m["id"] != wantIDs[i] {
			t.Errorf("item %d id = %v, want %s", i, m["id"], wantIDs[i])
		}
		if _, ok := m["title"]; !ok {
			t.Errorf("item %d missing title field", i)
		}
		if _, ok := m["status"]; !ok {
			t.Errorf("item %d missing status field", i)
		}
	}
}

func TestResolveBeads_ShellFormParsesLineDelimitedIDs(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, cmd string) ([]byte, error) {
			if !strings.Contains(cmd, "br list") {
				t.Fatalf("unexpected shell cmd: %q", cmd)
			}
			return []byte("bd-foo\nbd-bar\n\nbd-baz\n"), nil
		},
	}

	got, err := r.ResolveBeads(context.Background(), "$(br list --label=hypothesis --status=open --json | jq -r '.issues[].id')")
	if err != nil {
		t.Fatalf("ResolveBeads: %v", err)
	}
	want := []interface{}{"bd-foo", "bd-bar", "bd-baz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBeads_ShellFormParsesJSONArray(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`["bd-a","bd-b","bd-c"]`), nil
		},
	}

	got, err := r.ResolveBeads(context.Background(), "$(br list --json | jq -c '.issues|map(.id)')")
	if err != nil {
		t.Fatalf("ResolveBeads: %v", err)
	}
	want := []interface{}{"bd-a", "bd-b", "bd-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBeads_ShellFormStripsQuotes(t *testing.T) {
	// `jq '.issues[].id'` (without -r) emits quoted strings, one per line.
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`"bd-q1"` + "\n" + `"bd-q2"` + "\n"), nil
		},
	}

	got, err := r.ResolveBeads(context.Background(), `$(br list --json | jq '.issues[].id')`)
	if err != nil {
		t.Fatalf("ResolveBeads: %v", err)
	}
	want := []interface{}{"bd-q1", "bd-q2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBeads_EmptyResultIsNotError(t *testing.T) {
	cases := map[string]*IterationSourceResolver{
		"shell": {
			RunShell: func(context.Context, string) ([]byte, error) { return []byte(""), nil },
		},
		"structured": {
			RunBr: func(context.Context, []string) ([]byte, error) { return []byte(`{"issues":[]}`), nil },
		},
		"structured-empty-stdout": {
			RunBr: func(context.Context, []string) ([]byte, error) { return []byte(""), nil },
		},
	}
	exprs := map[string]string{
		"shell":                   "$(true)",
		"structured":              "hypothesis,state:active",
		"structured-empty-stdout": "label:hypothesis",
	}

	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := r.ResolveBeads(context.Background(), exprs[name])
			if err != nil {
				t.Fatalf("ResolveBeads: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("got %d items, want 0", len(got))
			}
		})
	}
}

func TestResolveBeads_EmptyExpressionShortCircuits(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) {
			t.Fatal("shell runner must not be called for empty expr")
			return nil, nil
		},
		RunBr: func(context.Context, []string) ([]byte, error) {
			t.Fatal("br runner must not be called for empty expr")
			return nil, nil
		},
	}
	got, err := r.ResolveBeads(context.Background(), "  ")
	if err != nil {
		t.Fatalf("ResolveBeads: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestResolveBeads_ShellErrorPropagates(t *testing.T) {
	want := errors.New("nonzero exit")
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return nil, want },
	}
	_, err := r.ResolveBeads(context.Background(), "$(false)")
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestResolveBeads_BrErrorPropagates(t *testing.T) {
	want := errors.New("br: not found")
	r := &IterationSourceResolver{
		RunBr: func(context.Context, []string) ([]byte, error) { return nil, want },
	}
	_, err := r.ResolveBeads(context.Background(), "hypothesis")
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestResolveBeads_BrJSONParseError(t *testing.T) {
	r := &IterationSourceResolver{
		RunBr: func(context.Context, []string) ([]byte, error) { return []byte("not-json"), nil },
	}
	_, err := r.ResolveBeads(context.Background(), "hypothesis")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse br --json output") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseStructuredBeadsQuery_Translations(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want []string
	}{
		{
			name: "single label",
			expr: "hypothesis",
			want: []string{"list", "--json", "--limit", "0", "--label", "hypothesis"},
		},
		{
			name: "label and status alias",
			expr: "hypothesis,state:active",
			want: []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "active"},
		},
		{
			name: "explicit label key",
			expr: "label:foo,status:open",
			want: []string{"list", "--json", "--limit", "0", "--label", "foo", "--status", "open"},
		},
		{
			name: "type/priority/assignee",
			expr: "type:bug,priority:1,assignee:alice",
			want: []string{"list", "--json", "--limit", "0", "--type", "bug", "--priority", "1", "--assignee", "alice"},
		},
		{
			name: "skips empty terms",
			expr: ",hypothesis,,state:open,",
			want: []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "open"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStructuredBeadsQuery(tc.expr)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseStructuredBeadsQuery_RejectsUnknownKeys(t *testing.T) {
	_, err := parseStructuredBeadsQuery("foo:bar")
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "unsupported filter key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParseStructuredBeadsQuery_RejectsEmptyValue(t *testing.T) {
	_, err := parseStructuredBeadsQuery("status:")
	if err == nil {
		t.Fatal("expected error for empty value")
	}
}

// bd-ftsqw: foreach beads queries must request unlimited results so large
// hypothesis/debate/backlog iteration sets are not silently truncated to
// br list's default page size.
func TestParseStructuredBeadsQuery_AlwaysIncludesUnlimitedLimit(t *testing.T) {
	cases := []string{
		"hypothesis",
		"hypothesis,state:active",
		"type:bug,priority:1",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			args, err := parseStructuredBeadsQuery(expr)
			if err != nil {
				t.Fatalf("parseStructuredBeadsQuery(%q): %v", expr, err)
			}
			var foundLimitZero bool
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "--limit" && args[i+1] == "0" {
					foundLimitZero = true
					break
				}
			}
			if !foundLimitZero {
				t.Fatalf("args = %v, want --limit 0 to disable br pagination", args)
			}
		})
	}
}

func TestResolveItems_JSONArrayLiteral(t *testing.T) {
	r := &IterationSourceResolver{}

	got, err := r.ResolveItems(context.Background(), `["a", "b", "c"]`, nil)
	if err != nil {
		t.Fatalf("ResolveItems: %v", err)
	}
	want := []interface{}{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveItems_NestedVarsReference(t *testing.T) {
	// bd-8tlee: ${vars.group.files} must walk into a nested map so workflow
	// authors can group iteration sources without flattening them out.
	r := IterationSourceResolver{}
	vars := map[string]interface{}{
		"group": map[string]interface{}{
			"files": []interface{}{"a.go", "b.go"},
		},
	}

	got, err := r.ResolveItems(context.Background(), "${vars.group.files}", vars)
	if err != nil {
		t.Fatalf("ResolveItems: %v", err)
	}
	if want := []interface{}{"a.go", "b.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveItems = %#v, want %#v", got, want)
	}
}

func TestResolveItems_NestedMissingFieldErrors(t *testing.T) {
	// bd-8tlee: a missing nested field should surface a clear error rather
	// than returning the literal placeholder.
	r := IterationSourceResolver{}
	vars := map[string]interface{}{
		"group": map[string]interface{}{
			"files": []interface{}{"a.go"},
		},
	}
	_, err := r.ResolveItems(context.Background(), "${vars.group.missing}", vars)
	if err == nil {
		t.Fatal("ResolveItems error = nil, want missing-field error")
	}
}

func TestResolveItems_VarsListReference(t *testing.T) {
	r := &IterationSourceResolver{}
	vars := map[string]interface{}{
		"list": []interface{}{"x", "y"},
	}

	got, err := r.ResolveItems(context.Background(), "${vars.list}", vars)
	if err != nil {
		t.Fatalf("ResolveItems: %v", err)
	}
	want := []interface{}{"x", "y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveItems_UnknownVarsReferenceErrors(t *testing.T) {
	r := &IterationSourceResolver{}

	_, err := r.ResolveItems(context.Background(), "${vars.unknown}", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "undefined variable") {
		t.Fatalf("err = %v, want undefined variable", err)
	}
}

func TestResolveItems_JSONArrayPreservesScalarTypes(t *testing.T) {
	r := &IterationSourceResolver{}

	got, err := r.ResolveItems(context.Background(), `[1, 2.5, true]`, nil)
	if err != nil {
		t.Fatalf("ResolveItems: %v", err)
	}
	want := []interface{}{1, 2.5, true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveItems_DirectScalarReferenceBecomesSingleItem(t *testing.T) {
	r := &IterationSourceResolver{}
	vars := map[string]interface{}{
		"session_id": "ntm-squad-001",
	}

	got, err := r.ResolveItems(context.Background(), "${session_id}", vars)
	if err != nil {
		t.Fatalf("ResolveItems: %v", err)
	}
	want := []interface{}{"ntm-squad-001"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveItems_VarsScalarReferenceErrors(t *testing.T) {
	r := &IterationSourceResolver{}
	vars := map[string]interface{}{
		"name": "not-a-list",
	}

	_, err := r.ResolveItems(context.Background(), "${vars.name}", vars)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "expected array-like value") {
		t.Fatalf("err = %v, want array-like type error", err)
	}
}

func TestResolveModels_InlineList(t *testing.T) {
	r := &IterationSourceResolver{}

	got, err := r.ResolveModels(context.Background(), StringOrList{"cc", "cod"})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	want := []string{"cc", "cod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveModels_ShellForm(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, cmd string) ([]byte, error) {
			if cmd != `printf 'cc\ncod\ngmi\n'` {
				t.Fatalf("shell cmd = %q", cmd)
			}
			return []byte("cc\ncod\ngmi\n"), nil
		},
	}

	got, err := r.ResolveModels(context.Background(), StringOrList{`$(printf 'cc\ncod\ngmi\n')`})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	want := []string{"cc", "cod", "gmi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveModels_LiteralSlug(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) {
			t.Fatal("literal slug must not invoke shell")
			return nil, nil
		},
	}

	got, err := r.ResolveModels(context.Background(), StringOrList{"cc"})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	want := []string{"cc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestResolveModels_ShellErrorPropagates(t *testing.T) {
	want := errors.New("model command failed")
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return nil, want },
	}

	_, err := r.ResolveModels(context.Background(), StringOrList{"printf cc | sort"})
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestResolvePairs_ParsesThreeWellFormedLines(t *testing.T) {
	const stdout = `DEBATE-001|H-001|H-002|p1|p2
DEBATE-002|H-003|H-004|p3|p4
DEBATE-003|H-005|H-006|p5|p6
`
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, cmd string) ([]byte, error) {
			if !strings.Contains(cmd, "generate-debate-pairs") {
				t.Fatalf("unexpected shell cmd: %q", cmd)
			}
			return []byte(stdout), nil
		},
	}

	got, err := r.ResolvePairs(context.Background(), "$(./scripts/generate-debate-pairs.sh --workspace=/tmp/ws)")
	if err != nil {
		t.Fatalf("ResolvePairs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}
	wantKeys := []string{"debate_id", "h1", "h2", "champion_a", "champion_b"}
	first, ok := got[0].(map[string]interface{})
	if !ok {
		t.Fatalf("item 0 is %T, want map", got[0])
	}
	for _, k := range wantKeys {
		if _, ok := first[k]; !ok {
			t.Errorf("first item missing %q", k)
		}
	}
	if first["debate_id"] != "DEBATE-001" {
		t.Errorf("debate_id = %v, want DEBATE-001", first["debate_id"])
	}
	if first["h1"] != "H-001" {
		t.Errorf("h1 = %v, want H-001", first["h1"])
	}
	if first["h2"] != "H-002" {
		t.Errorf("h2 = %v, want H-002", first["h2"])
	}
	if first["champion_a"] != "p1" {
		t.Errorf("champion_a = %v, want p1", first["champion_a"])
	}
	if first["champion_b"] != "p2" {
		t.Errorf("champion_b = %v, want p2", first["champion_b"])
	}
}

func TestResolvePairs_SkipsMalformedLines(t *testing.T) {
	const stdout = `DEBATE-001|H-001|H-002|p1|p2
this-line-has-no-pipes
DEBATE-002|H-003|H-004|p3|p4
TOO|FEW|FIELDS
DEBATE-003|H-005|H-006||p6
DEBATE-004|H-007|H-008|p7|p8
`
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return []byte(stdout), nil },
	}

	got, err := r.ResolvePairs(context.Background(), "$(echo)")
	if err != nil {
		t.Fatalf("ResolvePairs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3 (only well-formed lines)", len(got))
	}
	wantIDs := []string{"DEBATE-001", "DEBATE-002", "DEBATE-004"}
	for i, item := range got {
		m := item.(map[string]interface{})
		if m["debate_id"] != wantIDs[i] {
			t.Errorf("item %d debate_id = %v, want %s", i, m["debate_id"], wantIDs[i])
		}
	}
}

func TestResolvePairs_EmptyOutputZeroIterations(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return []byte(""), nil },
	}
	got, err := r.ResolvePairs(context.Background(), "$(true)")
	if err != nil {
		t.Fatalf("ResolvePairs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestResolvePairs_RequiresShellForm(t *testing.T) {
	r := &IterationSourceResolver{}
	_, err := r.ResolvePairs(context.Background(), "literal-not-shell")
	if err == nil {
		t.Fatal("expected error for non-shell expression")
	}
	if !strings.Contains(err.Error(), "must be a shell expression") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolvePairs_EmptyExpression(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) {
			t.Fatal("must not run shell on empty expr")
			return nil, nil
		},
	}
	got, err := r.ResolvePairs(context.Background(), "  ")
	if err != nil {
		t.Fatalf("ResolvePairs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestResolvePairs_ShellErrorPropagates(t *testing.T) {
	want := errors.New("script blew up")
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return nil, want },
	}
	_, err := r.ResolvePairs(context.Background(), "$(false)")
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestResolveDebates_ParsesLineDelimitedIDs(t *testing.T) {
	const stdout = `DEBATE-001
DEBATE-002
DEBATE-003
`
	r := &IterationSourceResolver{
		RunShell: func(_ context.Context, cmd string) ([]byte, error) {
			if !strings.Contains(cmd, "br list") {
				t.Fatalf("unexpected shell cmd: %q", cmd)
			}
			return []byte(stdout), nil
		},
	}
	got, err := r.ResolveDebates(context.Background(), `$(br list --label=debate --status=open --json | jq -r '.issues[].id')`)
	if err != nil {
		t.Fatalf("ResolveDebates: %v", err)
	}
	want := []interface{}{"DEBATE-001", "DEBATE-002", "DEBATE-003"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveDebates_EmptyOutputZeroIterations(t *testing.T) {
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return []byte("\n  \n"), nil },
	}
	got, err := r.ResolveDebates(context.Background(), "$(true)")
	if err != nil {
		t.Fatalf("ResolveDebates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestResolveDebates_ShellErrorPropagates(t *testing.T) {
	want := errors.New("br missing")
	r := &IterationSourceResolver{
		RunShell: func(context.Context, string) ([]byte, error) { return nil, want },
	}
	_, err := r.ResolveDebates(context.Background(), "$(br list)")
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
}

func TestResolveDebates_RequiresShellForm(t *testing.T) {
	r := &IterationSourceResolver{}
	_, err := r.ResolveDebates(context.Background(), "DEBATE-001,DEBATE-002")
	if err == nil {
		t.Fatal("expected error for non-shell expression")
	}
	if !strings.Contains(err.Error(), "must be a shell expression") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStripShellInvocation(t *testing.T) {
	cases := map[string]struct {
		in     string
		want   string
		wantOk bool
	}{
		"shell":        {"$(echo foo)", "echo foo", true},
		"empty-shell":  {"$()", "", true},
		"plain":        {"hypothesis", "", false},
		"missing-open": {"echo foo)", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := stripShellInvocation(tc.in)
			if ok != tc.wantOk || got != tc.want {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
}
