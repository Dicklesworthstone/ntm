package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBeadQuery_ParseYAMLAndValidate(t *testing.T) {
	content := `
schema_version: "2.0"
name: bead-query
steps:
  - id: collect
    bead_query:
      label: [hypothesis, phase-4]
      status: open
      filter: 'priority==1 && label==hypothesis'
    output_var: hypothesis_beads
`

	workflow, err := ParseString(content, "yaml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}

	query := workflow.Steps[0].BeadQuery
	if query == nil {
		t.Fatal("BeadQuery = nil")
	}
	if !reflect.DeepEqual(query.Label, StringOrList{"hypothesis", "phase-4"}) {
		t.Fatalf("BeadQuery.Label = %#v", query.Label)
	}
	if query.Status != "open" || query.Filter != "priority==1 && label==hypothesis" {
		t.Fatalf("BeadQuery = %#v", query)
	}
}

func TestBeadQuery_ParseTOMLKnownFields(t *testing.T) {
	content := `
schema_version = "2.0"
name = "bead-query-toml"

[[steps]]
id = "collect"
output_var = "hypothesis_beads"

[steps.bead_query]
label = "hypothesis"
status = "open"
filter = "status==open"
`

	workflow, err := ParseString(content, "toml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}
	if got := workflow.Steps[0].BeadQuery.Label; !reflect.DeepEqual(got, StringOrList{"hypothesis"}) {
		t.Fatalf("BeadQuery.Label = %#v", got)
	}
}

func TestBeadQuery_JSONRoundTrip(t *testing.T) {
	step := Step{
		ID: "collect",
		BeadQuery: &BeadQueryStep{
			Label:  StringOrList{"hypothesis"},
			Status: "open",
			Filter: "priority==1",
		},
		OutputVar: "beads",
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got Step
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v\nJSON:\n%s", err, data)
	}
	if !reflect.DeepEqual(got, step) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\n got: %#v\nJSON:\n%s", step, got, data)
	}
}

func TestBeadQuery_ValidationConflict(t *testing.T) {
	result := Validate(&Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-conflict",
		Steps: []Step{{
			ID:        "bad",
			Command:   "br list --json",
			BeadQuery: &BeadQueryStep{Label: StringOrList{"hypothesis"}},
		}},
	})
	if result.Valid {
		t.Fatal("Validate() succeeded, want conflict")
	}
	for _, err := range result.Errors {
		if strings.Contains(err.Message, "cannot combine bead_query") {
			return
		}
	}
	t.Fatalf("Validate() errors = %+v, want bead_query conflict", result.Errors)
}

func TestBeadQueryValidateLiteralFilters(t *testing.T) {
	for _, tc := range []struct {
		expr  string
		valid bool
	}{
		{"", true},
		{`status==open && (label==hypothesis || label==phase-4)`, true},
		{`title=="a && (b || c)"`, true},
		{`priority==${vars.target_priority}`, true},
		{`${steps.previous.output}`, true},
		{`status==open || missing==x`, false},
		{`status==open && priority==high`, false},
		{`status==open && (label==hypothesis`, false},
		{`status==open ||`, false},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			result := Validate(&Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "validate-bead-filter",
				Steps:         []Step{{ID: "collect", BeadQuery: &BeadQueryStep{Filter: tc.expr}}},
			})
			if result.Valid != tc.valid {
				t.Fatalf("Validate().Valid = %t, want %t; errors = %+v", result.Valid, tc.valid, result.Errors)
			}
			if !tc.valid {
				if len(result.Errors) != 1 || result.Errors[0].Field != "steps[0].bead_query.filter" {
					t.Fatalf("filter diagnostic = %+v", result.Errors)
				}
			}
		})
	}
}

func TestExecuteBeadQueryCapturesTypedRecords(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-exec",
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label:  StringOrList{"hypothesis"},
				Status: "open",
				Filter: "status==open && label==hypothesis",
			},
			OutputVar: "hypothesis_beads",
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Helper()
		wantArgs := []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "open"}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("br args = %#v, want %#v", args, wantArgs)
		}
		return []byte(`{"issues":[
			{"id":"bd-1","title":"One","description":"first","labels":["hypothesis","phase-4"],"status":"open","priority":1,"issue_type":"task"},
			{"id":"bd-2","title":"Two","labels":["other"],"status":"open","priority":1,"issue_type":"task"},
			{"id":"bd-3","title":"Three","labels":["hypothesis"],"status":"closed","priority":1,"issue_type":"task"}
		]}`), nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := state.Steps["collect"]
	records, ok := result.ParsedData.([]BeadRecord)
	if !ok {
		t.Fatalf("ParsedData = %T, want []BeadRecord", result.ParsedData)
	}
	if len(records) != 1 || records[0].ID != "bd-1" || records[0].Title != "One" {
		t.Fatalf("records = %#v", records)
	}

	var output []BeadRecord
	if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
		t.Fatalf("result.Output is not BeadRecord JSON: %v\n%s", err, result.Output)
	}
	if !reflect.DeepEqual(output, records) {
		t.Fatalf("output records = %#v, want %#v", output, records)
	}
	if got := state.Variables["hypothesis_beads"]; got != result.Output {
		t.Fatalf("output var = %#v, want %#v", got, result.Output)
	}
	if got := state.Variables["hypothesis_beads_parsed"]; !reflect.DeepEqual(got, records) {
		t.Fatalf("parsed output var = %#v, want %#v", got, records)
	}
}

func TestBeadQueryFilterOperators(t *testing.T) {
	records := []BeadRecord{
		{ID: "bd-1", Labels: []string{"hypothesis"}, Status: "open", Priority: 1},
		{ID: "bd-2", Labels: []string{"question"}, Status: "open", Priority: 2},
		{ID: "bd-3", Labels: []string{"hypothesis"}, Status: "closed", Priority: 2},
	}

	filter, err := compileBeadFilter(`label==hypothesis && status!=closed || id=="bd-2"`)
	if err != nil {
		t.Fatalf("compileBeadFilter() error = %v", err)
	}
	got, err := filterBeadRecords(records, filter)
	if err != nil {
		t.Fatalf("filterBeadRecords() error = %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, record := range got {
		ids = append(ids, record.ID)
	}
	if !reflect.DeepEqual(ids, []string{"bd-1", "bd-2"}) {
		t.Fatalf("filtered ids = %#v", ids)
	}
}

func TestBeadQueryFilterGroupingAndLiteralTypes(t *testing.T) {
	records := []BeadRecord{
		{ID: "bd-1", Labels: []string{"hypothesis", "01"}, Status: "open", Priority: 1, Title: `Parser (a && b) || "fallback"`},
		{ID: "bd-2", Labels: []string{"phase-4", "1"}, Status: "open", State: "queued", Priority: 2, Title: "Don't split != or =="},
		{ID: "bd-3", Labels: []string{"phase-4"}, Status: "closed", Priority: 1},
		{ID: "bd-4", Labels: []string{"other"}, Status: "open", Priority: 1},
	}
	for _, tc := range []struct {
		expr string
		want []string
	}{
		{`status==open && (label==hypothesis || label==phase-4)`, []string{"bd-1", "bd-2"}},
		{`(status==open && label==hypothesis) || label==phase-4`, []string{"bd-1", "bd-2", "bd-3"}},
		{`status==open && ((label==hypothesis || label==phase-4) && priority!="2")`, []string{"bd-1"}},
		{`Title=="Parser (a && b) || \"fallback\""`, []string{"bd-1"}},
		{`title=='Don\'t split != or =='`, []string{"bd-2"}},
		{`label=="01"`, []string{"bd-1"}},
		{`label==1`, []string{"bd-2"}},
		{`PRIORITY=="01"`, []string{"bd-1", "bd-3", "bd-4"}},
		{`state==open`, []string{"bd-1", "bd-4"}},
		{`state==queued`, []string{"bd-2"}},
		{`assignee=="" && label!=phase-4`, []string{"bd-1", "bd-4"}},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			filter, err := compileBeadFilter(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := filterBeadRecords(records, filter)
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(got))
			for _, record := range got {
				ids = append(ids, record.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("filtered IDs = %v, want %v", ids, tc.want)
			}
		})
	}
}

func TestBeadQueryGroupedFilterRunsWorkflow(t *testing.T) {
	workflow, err := ParseString(`
schema_version: "2.0"
name: grouped-bead-selection
vars:
  target_priority:
    type: string
    default: "1"
steps:
  - id: collect
    bead_query:
      filter: 'status==open && (Label=="api || (parser)" || issue_type==bug) && priority==${vars.target_priority}'
    output_var: selected
`, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	config := DefaultExecutorConfig("session")
	config.ProjectDir = t.TempDir()
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		calls++
		if want := []string{"list", "--json", "--limit", "0"}; !reflect.DeepEqual(args, want) {
			return nil, fmt.Errorf("br args = %v, want %v", args, want)
		}
		return []byte(`{"issues":[
			{"id":"bd-source","labels":["api || (parser)"],"status":"open","priority":1,"issue_type":"feature"},
			{"id":"bd-bug","labels":[],"status":"open","priority":1,"issue_type":"bug"},
			{"id":"bd-closed","labels":["api || (parser)"],"status":"closed","priority":1,"issue_type":"bug"},
			{"id":"bd-later","labels":[],"status":"open","priority":2,"issue_type":"bug"},
			{"id":"bd-other","labels":[],"status":"open","priority":1,"issue_type":"feature"}
		]}`), nil
	}
	state, err := NewExecutor(config).Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("br called %d times, want 1", calls)
	}
	var selected []BeadRecord
	if err := json.Unmarshal([]byte(state.Steps["collect"].Output), &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].ID != "bd-source" || selected[1].ID != "bd-bug" {
		t.Fatalf("selected = %+v", selected)
	}
	// Typed output and serialized records retain empty label lists so a
	// checkpoint reload exposes the same downstream data.
	wantTyped := []BeadRecord{
		{ID: "bd-source", Labels: []string{"api || (parser)"}, Status: "open", Priority: 1, IssueType: "feature"},
		{ID: "bd-bug", Labels: []string{}, Status: "open", Priority: 1, IssueType: "bug"},
	}
	if got := state.Variables["selected_parsed"]; !reflect.DeepEqual(got, wantTyped) {
		t.Fatalf("downstream typed records = %#v, want %#v", got, wantTyped)
	}
}

func TestBeadQueryInvalidFiltersFailBeforeQuery(t *testing.T) {
	for _, expr := range []string{
		`status==open || missing==x`,
		`status==closed && priority==high`,
		`status==open ||`,
		`status==open && (label==a`,
		`status==open)`,
		`label=="unterminated`,
		`"status"==open`,
		`status`,
		`priority!=1==something`,
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry_run=%t", expr, dryRun), func(t *testing.T) {
				workflow := &Workflow{
					SchemaVersion: SchemaVersion,
					Name:          "invalid-bead-filter",
					Vars:          map[string]VarDef{"selection": {Type: VarTypeString, Default: expr}},
					Steps:         []Step{{ID: "collect", BeadQuery: &BeadQueryStep{Filter: "${vars.selection}"}}},
				}
				config := DefaultExecutorConfig("session")
				config.ProjectDir = t.TempDir()
				config.DryRun = dryRun
				queryCalls := 0
				config.BeadQueryRunBr = func(context.Context, []string) ([]byte, error) {
					queryCalls++
					return []byte(`{"issues":[]}`), nil
				}
				state, err := NewExecutor(config).Run(context.Background(), workflow, nil, nil)
				if queryCalls != 0 {
					t.Fatal("invalid filter invoked br; even an empty result must not hide invalid syntax or fields")
				}
				if err == nil {
					t.Fatal("Run() succeeded with an invalid filter")
				}
				result := state.Steps["collect"]
				if result.Status != StatusFailed || result.Error == nil || !strings.Contains(result.Error.Message, "filter failed") {
					t.Fatalf("filter failure = %+v", result)
				}
			})
		}
	}
}

func TestBeadQuerySubstitutesVariablesInArgsAndFilter(t *testing.T) {
	// bead_query is meant to replace shell-piped br|jq commands. Without
	// variable substitution, fields like label/status/filter would be sent
	// verbatim (literal "${vars.target_label}") to br, returning the wrong
	// result set with no diagnostic.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-vars",
		Vars: map[string]VarDef{
			"target_label":    {Type: VarTypeString, Default: "hypothesis"},
			"target_state":    {Type: VarTypeString, Default: "open"},
			"target_priority": {Type: VarTypeString, Default: "1"},
		},
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label:    StringOrList{"${vars.target_label}"},
				Status:   "${vars.target_state}",
				Priority: "${vars.target_priority}",
				Filter:   "status==${vars.target_state}",
			},
			OutputVar: "beads",
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Helper()
		wantArgs := []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "open", "--priority", "1"}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("br args = %#v, want %#v", args, wantArgs)
		}
		return []byte(`{"issues":[
			{"id":"bd-1","status":"open","labels":["hypothesis"],"priority":1,"issue_type":"task"},
			{"id":"bd-2","status":"closed","labels":["hypothesis"],"priority":1,"issue_type":"task"}
		]}`), nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	records, ok := state.Steps["collect"].ParsedData.([]BeadRecord)
	if !ok {
		t.Fatalf("ParsedData = %T, want []BeadRecord", state.Steps["collect"].ParsedData)
	}
	// Filter "status==open" must apply against the resolved value, leaving
	// only bd-1.
	if len(records) != 1 || records[0].ID != "bd-1" {
		t.Fatalf("filtered records = %#v, want one record bd-1", records)
	}
}

func TestBeadQueryFailsOnUnresolvedVariable(t *testing.T) {
	// A typo'd reference must fail the step rather than silently sending the
	// literal "${vars.unknown}" to br.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-unresolved",
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label: StringOrList{"${vars.unknown}"},
			},
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Fatalf("br must not be called when variable substitution fails; args=%v", args)
		return nil, nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want substitution failure")
	}
	stepResult := state.Steps["collect"]
	if stepResult.Status != StatusFailed {
		t.Fatalf("Status = %v, want %v", stepResult.Status, StatusFailed)
	}
	if stepResult.Error == nil || !strings.Contains(stepResult.Error.Message, "variable substitution failed") {
		t.Fatalf("step error = %#v, want substitution failure", stepResult.Error)
	}
}

func TestBeadQueryBrErrorFailsStep(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-error",
		Steps: []Step{{
			ID:        "collect",
			BeadQuery: &BeadQueryStep{Label: StringOrList{"hypothesis"}},
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want bead_query failure")
	}
	if got := state.Steps["collect"].Error; got == nil || !strings.Contains(got.Message, "bead_query") {
		t.Fatalf("step error = %#v, want bead_query error", got)
	}
}

// Operator-looking values require quotes; an accidental chained comparison
// must never become a silently different work selection.
func TestBeadFilterComparisonOperators(t *testing.T) {
	record := BeadRecord{
		ID:       "bd-test",
		Priority: 1,
		Title:    "1==something",
	}
	cases := []struct {
		clause  string
		want    bool
		wantErr bool
	}{
		{clause: "priority!=2", want: true},
		{clause: "priority==1", want: true},
		{clause: "priority!=1==something", wantErr: true},
		{clause: "priority==2!=junk", wantErr: true},
		{clause: `priority!="not-an-integer"`, wantErr: true},
		{clause: `title=="1==something"`, want: true},
		{clause: `title!="2!=junk"`, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			filter, err := compileBeadFilter(tc.clause)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("compileBeadFilter(%q) err = nil, want error", tc.clause)
				}
				return
			}
			if err != nil {
				t.Fatalf("compileBeadFilter(%q) err = %v", tc.clause, err)
			}
			got, err := filter.eval(FilterContext{Item: record})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("filter %q = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}
}

// TestExecuteBeadQuery_DryRun_SanitizesControlBytes covers bd-g3mfx: the
// dry-run banner emitted by executeBeadQuery must scrub ANSI/OSC/C0 control
// bytes from the substituted args. bead_query scalar fields support
// ${steps.X.output}, so an upstream agent's output can flow into the
// joined args and reach the operator's terminal during --dry-run unless
// sanitized first. Same attack class as bd-82zsc (command) and bd-g40ad
// (agent/parallel/branch). Each control byte must round-trip as '?'.
func TestExecuteBeadQuery_DryRun_SanitizesControlBytes(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("bead-query-dryrun-sanitize")
	cfg.ProjectDir = tmpDir
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "run-bq-sanitize",
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps: map[string]StepResult{
			"evil": {Output: "\x1b[2J\x1b[H\x07payload"},
		},
	}

	step := &Step{
		ID: "dryrun-bq",
		BeadQuery: &BeadQueryStep{
			// Status flows into brListArgs via "--status <value>", so the
			// substituted bytes reach the joined dry-run banner. Filter is
			// in-memory only and never appears in args, so it would not
			// exercise the banner sanitization path.
			Status: "${steps.evil.output}",
		},
	}
	result := e.executeBeadQuery(context.Background(), step, &Workflow{Name: "test"})

	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q; error = %+v", result.Status, StatusCompleted, result.Error)
	}
	for _, b := range []byte(result.Output) {
		if b == '\x1b' || b == '\x07' || b == '\x00' {
			t.Fatalf("dry-run bead_query banner contains unsanitized control byte 0x%02x: %q", b, result.Output)
		}
	}
	if !strings.Contains(result.Output, "payload") {
		t.Errorf("dry-run bead_query banner dropped the trailing payload after sanitizing controls: %q", result.Output)
	}
	if !strings.Contains(result.Output, "Would run br") {
		t.Errorf("dry-run bead_query banner missing canonical prefix: %q", result.Output)
	}
}

// Label aliases retain membership semantics regardless of field-name case.
func TestBeadFilterLabelCaseInsensitive(t *testing.T) {
	record := BeadRecord{
		ID:     "bd-test",
		Labels: []string{"alpha", "beta", "gamma"},
	}
	cases := []struct {
		clause string
		want   bool
	}{
		{"label==alpha", true},
		{"Label==alpha", true},
		{"LABEL==alpha", true},
		{"labels==beta", true},
		{"Labels==beta", true},
		{"label==zeta", false},
		{"Label!=alpha", false},
		{"Label!=zeta", true},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			filter, err := compileBeadFilter(tc.clause)
			if err != nil {
				t.Fatalf("compileBeadFilter(%q) err = %v", tc.clause, err)
			}
			got, err := filter.eval(FilterContext{Item: record})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("filter %q = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}
}

func TestBeadQueryRecordsFlowThroughFreshAndResumedWorkflows(t *testing.T) {
	for _, resumeBeforeConsumption := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resumeBeforeConsumption), func(t *testing.T) {
			workflow := &Workflow{
				SchemaVersion: SchemaVersion,
				Name:          "native-bead-data",
				Settings:      DefaultWorkflowSettings(),
				Steps: []Step{
					{
						ID:        "collect",
						BeadQuery: &BeadQueryStep{},
						OutputVar: "selected",
					},
					{
						ID:        "gate",
						DependsOn: []string{"collect"},
						OnError:   ErrorActionFail,
						BeadQuery: &BeadQueryStep{
							Label: StringOrList{"checkpoint-gate"},
						},
					},
					{
						ID:        "inspect",
						DependsOn: []string{"gate"},
						BeadQuery: &BeadQueryStep{
							Label: StringOrList{
								"${steps.collect.data[0].id}",
								"${vars.selected_parsed.1.title}",
							},
							Type:     "${steps.collect.data[0].issue_type}",
							Priority: "${steps.collect.data[0].priority}",
							Assignee: "${steps.collect.data[0].assignee}",
						},
					},
					{
						ID:            "fanout",
						DependsOn:     []string{"inspect"},
						OutputVar:     "details",
						OutputVarMode: OutputVarModeCollect,
						Foreach: &ForeachConfig{
							Items:  "${vars.selected_parsed}",
							As:     "task",
							Filter: `priority==0 && assignee==""`,
							Steps: []Step{
								{
									ID: "detail",
									BeadQuery: &BeadQueryStep{
										Label: StringOrList{
											"${task.id}",
											"${item.title}",
											"${loop.item.id}",
										},
										Type:     "${item.issue_type}",
										Priority: "${task.priority}",
										Assignee: "${task.assignee}",
									},
									OutputVar: "details",
								},
							},
						},
					},
				},
			}

			cfg := DefaultExecutorConfig("native-bead-data")
			cfg.ProjectDir = t.TempDir()
			allowGate := !resumeBeforeConsumption
			collectionCalls := 0
			var consumedQueries [][]string
			cfg.BeadQueryRunBr = func(_ context.Context, args []string) ([]byte, error) {
				if reflect.DeepEqual(args, []string{"list", "--json", "--limit", "0"}) {
					collectionCalls++
					return []byte(`{"issues":[
						{"id":"bd-critical","title":"Critical work","labels":null,"status":"open","priority":0,"issue_type":"bug","assignee":""},
						{"id":"bd-high","title":"High work","status":"open","priority":1,"issue_type":"task","assignee":""}
					]}`), nil
				}
				if reflect.DeepEqual(args, []string{
					"list", "--json", "--limit", "0", "--label", "checkpoint-gate",
				}) {
					if !allowGate {
						return nil, fmt.Errorf("temporary bead query failure")
					}
					return []byte(`{"issues":[]}`), nil
				}
				consumedQueries = append(consumedQueries, append([]string(nil), args...))
				return []byte(`{"issues":[]}`), nil
			}

			state, err := NewExecutor(cfg).Run(context.Background(), workflow, nil, nil)
			if state == nil {
				t.Fatalf("Run() returned no state: %v", err)
			}
			records, ok := state.Steps["collect"].ParsedData.([]BeadRecord)
			if !ok || len(records) != 2 {
				t.Fatalf("native query lost typed records: %#v", state.Steps["collect"].ParsedData)
			}
			if records[0].Priority != 0 || records[0].Labels == nil || records[1].Labels == nil {
				t.Fatalf("native records lost zero/empty values: %#v", records)
			}

			if resumeBeforeConsumption {
				if err == nil || state.Steps["gate"].Status != StatusFailed {
					t.Fatalf("first Run() must stop at the gate: state=%+v error=%v", state, err)
				}
				if len(consumedQueries) != 0 {
					t.Fatalf("downstream work ran before the failed gate: %#v", consumedQueries)
				}
				prior, loadErr := LoadState(cfg.ProjectDir, state.RunID)
				if loadErr != nil {
					t.Fatalf("LoadState() error = %v", loadErr)
				}
				allowGate = true
				state, err = NewExecutor(cfg).ResumeWithOptions(
					context.Background(), workflow, prior, ResumeOptions{
						Mode:           ResumeModeContinue,
						KeepState:      true,
						OnRosterChange: ResumeRosterAbort,
					}, nil,
				)
			}
			if err != nil || state == nil || state.Status != StatusCompleted {
				t.Fatalf("workflow did not consume its native records: state=%+v error=%v", state, err)
			}
			if collectionCalls != 1 {
				t.Fatalf("completed collection ran %d times, want 1", collectionCalls)
			}

			wantQueries := [][]string{
				{
					"list", "--json", "--limit", "0",
					"--label", "bd-critical", "--label", "High work",
					"--type", "bug", "--priority", "0",
				},
				{
					"list", "--json", "--limit", "0",
					"--label", "bd-critical", "--label", "Critical work",
					"--label", "bd-critical", "--type", "bug", "--priority", "0",
				},
			}
			if !reflect.DeepEqual(consumedQueries, wantQueries) {
				t.Fatalf("downstream br requests = %#v, want %#v", consumedQueries, wantQueries)
			}
			if got := state.Variables["details"]; !reflect.DeepEqual(got, map[string]string{
				"bd-critical": "[]",
			}) {
				t.Fatalf("collect output lost stable bead identity: %#v", got)
			}

			var wireRecords []map[string]interface{}
			if err := json.Unmarshal([]byte(state.Steps["collect"].Output), &wireRecords); err != nil {
				t.Fatal(err)
			}
			if len(wireRecords) != 2 || wireRecords[0]["priority"] != float64(0) {
				t.Fatalf("serialized query lost P0 priority: %#v", wireRecords)
			}
			if assignee, exists := wireRecords[0]["assignee"]; !exists || assignee != "" {
				t.Fatalf("serialized query lost empty assignee: %#v", wireRecords[0])
			}
			if labels, ok := wireRecords[0]["labels"].([]interface{}); !ok || len(labels) != 0 {
				t.Fatalf("serialized query labels = %#v, want []", wireRecords[0]["labels"])
			}

			sub := NewSubstitutor(state, cfg.Session, workflow.Name)
			for _, ref := range []string{
				"${steps.collect.data[0].unknown}",
				"${steps.collect.data[0].ID}",
				"${steps.collect.data[-1].id}",
				"${steps.collect.data[2].id}",
				"${steps.collect.data[invalid].id}",
				"${vars.selected_parsed.0.unknown}",
			} {
				if value, err := sub.SubstituteStrict(ref); err == nil {
					t.Fatalf("invalid native-record reference %q resolved as %q", ref, value)
				}
			}
		})
	}
}

// Both native bead consumers must refuse the entire response rather than
// treating missing data as an empty query or exposing a valid prefix.
func TestBeadQueryResponseRequiresCompleteWorkSet(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"empty stdout", ""},
		{"whitespace stdout", " \n\t"},
		{"null envelope", `null`},
		{"array envelope", `[]`},
		{"missing issues", `{}`},
		{"unknown envelope", `{"records":[]}`},
		{"case ambiguous array", `{"Issues":[]}`},
		{"duplicate issues", `{"issues":[{"id":"bd-lost"}],"issues":[]}`},
		{"duplicate total", `{"issues":[{"id":"bd-one"}],"total":2,"total":1}`},
		{"duplicate offset", `{"issues":[],"offset":1,"offset":0}`},
		{"duplicate limit", `{"issues":[],"limit":1,"limit":0}`},
		{"duplicate has_more", `{"issues":[],"has_more":true,"has_more":false}`},
		{"duplicate success", `{"issues":[],"success":false,"success":true}`},
		{"duplicate error", `{"issues":[],"error":"database unavailable","error":null}`},
		{"issues case collision", `{"issues":[],"Issues":[{"id":"bd-lost"}]}`},
		{"total case collision", `{"issues":[],"total":0,"Total":1}`},
		{"offset case alias", `{"issues":[],"Offset":1}`},
		{"limit case alias", `{"issues":[],"Limit":1}`},
		{"has_more case alias", `{"issues":[],"HAS_MORE":true}`},
		{"success case collision", `{"issues":[],"success":true,"Success":false}`},
		{"error case collision", `{"issues":[],"error":null,"Error":"database unavailable"}`},
		{"null issues", `{"issues":null}`},
		{"object issues", `{"issues":{}}`},
		{"null row", `{"issues":[null]}`},
		{"array row", `{"issues":[[]]}`},
		{"string row", `{"issues":["bd-one"]}`},
		{"missing identity", `{"issues":[{}]}`},
		{"null identity", `{"issues":[{"id":null}]}`},
		{"numeric identity", `{"issues":[{"id":1}]}`},
		{"empty identity", `{"issues":[{"id":""}]}`},
		{"padded identity", `{"issues":[{"id":" bd-one"}]}`},
		{"embedded whitespace", `{"issues":[{"id":"bd one"}]}`},
		{"control identity", `{"issues":[{"id":"bd-\u0000one"}]}`},
		{"repeated identity key", `{"issues":[{"id":"bd-one","id":"bd-two"}]}`},
		{"case ambiguous identity", `{"issues":[{"ID":"bd-one"}]}`},
		{"typed identity replacement", `{"issues":[{"id":"bd-one","ID":"bd-same"},{"id":"bd-two","ID":"bd-same"}]}`},
		{"duplicate identities", `{"issues":[{"id":"bd-one"},{"id":"bd-one"}]}`},
		{"invalid row after valid prefix", `{"issues":[{"id":"bd-one"},{}]}`},
		{"missing total rows", `{"issues":[{"id":"bd-one"}],"total":2}`},
		{"excess total rows", `{"issues":[{"id":"bd-one"}],"total":0}`},
		{"negative total", `{"issues":[],"total":-1}`},
		{"null total", `{"issues":[],"total":null}`},
		{"string total", `{"issues":[],"total":"0"}`},
		{"fractional total", `{"issues":[],"total":0.5}`},
		{"offset page", `{"issues":[{"id":"bd-one"}],"offset":1}`},
		{"negative offset", `{"issues":[],"offset":-1}`},
		{"null offset", `{"issues":[],"offset":null}`},
		{"limited page", `{"issues":[{"id":"bd-one"}],"limit":1}`},
		{"negative limit", `{"issues":[],"limit":-1}`},
		{"null limit", `{"issues":[],"limit":null}`},
		{"additional page", `{"issues":[{"id":"bd-one"}],"has_more":true}`},
		{"null has_more", `{"issues":[],"has_more":null}`},
		{"string has_more", `{"issues":[],"has_more":"false"}`},
		{"reported failure", `{"issues":[],"success":false}`},
		{"null success", `{"issues":[],"success":null}`},
		{"reported error", `{"issues":[],"error":"database unavailable"}`},
		{"structured error", `{"issues":[{"id":"bd-one"}],"error":{"code":"DATABASE_ERROR"}}`},
		{"invalid trailing whitespace", "{\"issues\":[]}\v"},
		{"trailing JSON", `{"issues":[]} {"issues":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if records, err := parseBeadQueryRecords([]byte(tc.payload)); err == nil || records != nil {
				t.Fatalf("native query accepted unusable response: records=%+v err=%v", records, err)
			}
			if items, err := parseBrListJSON([]byte(tc.payload)); err == nil || items != nil {
				t.Fatalf("structured foreach accepted unusable response: items=%+v err=%v", items, err)
			}
		})
	}
}

func TestBeadQueryResponsePreservesExplicitEmptyAndCompleteLists(t *testing.T) {
	for _, payload := range []string{
		`{"issues":[]}`,
		`{"issues":[],"total":0,"limit":0,"offset":0,"has_more":false}`,
		`{"issues":[],"success":true,"error":null}`,
		`{"issues":[],"success":true,"error":""}`,
	} {
		records, err := parseBeadQueryRecords([]byte(payload))
		if err != nil || records == nil || len(records) != 0 {
			t.Fatalf("explicit empty query must return []: %+v %v", records, err)
		}
		items, err := parseBrListJSON([]byte(payload))
		if err != nil || items == nil || len(items) != 0 {
			t.Fatalf("explicit empty foreach source must return []: %+v %v", items, err)
		}
	}

	const payload = `{"issues":[
		{"id":"bd-one","title":"First","priority":0,"custom":{"owner":"Ada"},"dependency_count":2},
		{"id":"bd-two","labels":["api"],"priority":1,"custom":{"owner":"Grace"}}
	],"total":2,"limit":0,"offset":0,"has_more":false,"extension":"retained"}`
	records, err := parseBeadQueryRecords([]byte(payload))
	if err != nil || len(records) != 2 || records[0].ID != "bd-one" || records[1].ID != "bd-two" {
		t.Fatalf("complete native query failed: %+v %v", records, err)
	}
	if records[0].Labels == nil || len(records[0].Labels) != 0 {
		t.Fatalf("native record lost its explicit empty label list: %+v", records[0])
	}
	items, err := parseBrListJSON([]byte(payload))
	if err != nil || len(items) != 2 {
		t.Fatalf("complete foreach source failed: %+v %v", items, err)
	}
	first, ok := items[0].(map[string]interface{})
	if !ok || first["id"] != "bd-one" || first["dependency_count"] != float64(2) ||
		!reflect.DeepEqual(first["custom"], map[string]interface{}{"owner": "Ada"}) {
		t.Fatalf("foreach lost extension fields: %#v", items[0])
	}
}
