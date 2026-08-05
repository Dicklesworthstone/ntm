package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShellQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "''"},
		{name: "simple", in: "foo", want: "'foo'"},
		{name: "space", in: "foo bar", want: "'foo bar'"},
		{name: "single quote", in: "weird'quote", want: `'weird'\''quote'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShellQuote(tt.in)
			if got != tt.want {
				t.Fatalf("ShellQuote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildRemoteShellCommand(t *testing.T) {
	t.Parallel()

	got := buildRemoteShellCommand("tmux", "display-message", "-t", "sess", "hello world")
	want := `tmux 'display-message' '-t' 'sess' 'hello world'`
	if got != want {
		t.Fatalf("buildRemoteShellCommand() = %q, want %q", got, want)
	}

	got = buildRemoteShellCommand("tmux", "new-session", "-s", "x; rm -rf /")
	if !strings.Contains(got, `'x; rm -rf /'`) {
		t.Fatalf("buildRemoteShellCommand() did not quote dangerous arg: %q", got)
	}
}

func TestClassifyCommandError(t *testing.T) {
	t.Parallel()

	exit1 := commandExitError(t, 1)
	exit255 := commandExitError(t, 255)

	tests := []struct {
		name string
		err  error
		want CommandErrorClass
	}{
		{
			name: "nil",
			err:  nil,
			want: CommandErrorClass{Kind: CommandErrorNone},
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
			want: CommandErrorClass{Kind: CommandErrorTimeout, Infrastructure: true, Retryable: true},
		},
		{
			// Deliberately NOT Infrastructure. Infrastructure feeds only
			// circuit-breaker accounting, and cancellation is caller-initiated
			// (operator Ctrl+C, a per-tick context in a poll loop, a
			// shutting-down request) — it says nothing about tmux's health.
			// Counting it let five cancelled calls open the breaker for every
			// caller in the process. DeadlineExceeded above stays
			// Infrastructure: a timeout IS evidence of a wedged server.
			name: "canceled",
			err:  context.Canceled,
			want: CommandErrorClass{Kind: CommandErrorCanceled},
		},
		{
			name: "circuit open",
			err:  ErrCircuitOpen,
			want: CommandErrorClass{Kind: CommandErrorCircuitOpen, Infrastructure: true, Retryable: true},
		},
		{
			name: "binary unavailable",
			err:  &exec.Error{Name: "tmux", Err: exec.ErrNotFound},
			want: CommandErrorClass{Kind: CommandErrorBinaryUnavailable, Infrastructure: true},
		},
		{
			name: "permission denied",
			err:  &exec.Error{Name: "tmux", Err: os.ErrPermission},
			want: CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true},
		},
		{
			name: "permission denied from stderr",
			err:  fmt.Errorf("tmux list-sessions: %w: permission denied", exit1),
			want: CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true},
		},
		{
			name: "permission denied while connecting",
			err:  fmt.Errorf("tmux has-session: %w: error connecting to /tmp/tmux-1000/default (Permission denied)", exit1),
			want: CommandErrorClass{Kind: CommandErrorPermissionDenied, Infrastructure: true},
		},
		{
			name: "missing session",
			err:  fmt.Errorf("tmux list-panes: %w: can't find session: missing", exit1),
			want: CommandErrorClass{Kind: CommandErrorSessionNotFound},
		},
		{
			name: "missing pane",
			err:  fmt.Errorf("tmux select-pane: %w: can't find pane: %%99", exit1),
			want: CommandErrorClass{Kind: CommandErrorPaneNotFound},
		},
		{
			// Retryable (a server may start later) but NOT Infrastructure.
			// Infrastructure feeds circuit-breaker accounting only, and "no
			// server running" is an instant definitive answer meaning "there
			// are no sessions" — there is no sick server to shed load from.
			// While the breaker had a bug that stopped it ever shedding load,
			// this classification had no observable consequence; once the
			// breaker worked, counting this tripped it during ordinary
			// session queries on a machine with no tmux server, degrading a
			// correct SESSION_NOT_FOUND into a circuit-open INTERNAL_ERROR.
			name: "no tmux server",
			err:  fmt.Errorf("tmux list-sessions: %w: no server running on /tmp/tmux-1000/default", exit1),
			want: CommandErrorClass{Kind: CommandErrorNoServer, Retryable: true},
		},
		{
			name: "remote unavailable",
			err:  fmt.Errorf("ssh host: %w: connection timed out", exit255),
			want: CommandErrorClass{Kind: CommandErrorRemoteUnavailable, Infrastructure: true, Retryable: true},
		},
		{
			name: "malformed output",
			err:  errors.New("unexpected session format"),
			want: CommandErrorClass{Kind: CommandErrorMalformedOutput, Infrastructure: true, Retryable: true},
		},
		{
			name: "ordinary tmux command failure",
			err:  fmt.Errorf("tmux display-message: %w: unknown option: -Z", exit1),
			want: CommandErrorClass{Kind: CommandErrorCommandFailed},
		},
		{
			name: "unknown error",
			err:  errors.New("read pipe failed"),
			want: CommandErrorClass{Kind: CommandErrorUnknown, Infrastructure: true, Retryable: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyCommandError(tt.err); got != tt.want {
				t.Fatalf("ClassifyCommandError(%v) = %+v, want %+v", tt.err, got, tt.want)
			}
		})
	}
}

func commandExitError(t *testing.T, code int) error {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestCommandExitErrorHelper")
	cmd.Env = append(
		os.Environ(),
		"NTM_TEST_EXIT_CODE="+strconv.Itoa(code),
		"NTM_TEST_TMUX_ENV_OWNED=1",
	)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper exit error = %T %v, want *exec.ExitError", err, err)
	}
	return err
}

func TestCommandExitErrorHelper(t *testing.T) {
	code := os.Getenv("NTM_TEST_EXIT_CODE")
	if code == "" {
		return
	}
	parsed, err := strconv.Atoi(code)
	if err != nil {
		os.Exit(2)
	}
	os.Exit(parsed)
}

func TestBuildPaneCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		projectDir string
		cmd        string
		want       string
		wantErr    bool
	}{
		{
			name:       "simple command",
			projectDir: "/projects/foo",
			cmd:        "ls -la",
			want:       "cd '/projects/foo' && ls -la",
			wantErr:    false,
		},
		{
			name:       "command with spaces",
			projectDir: "/projects/foo bar",
			cmd:        "echo hello",
			want:       "cd '/projects/foo bar' && echo hello",
			wantErr:    false,
		},
		{
			name:       "unsafe command",
			projectDir: "/projects/foo",
			cmd:        "echo hello\nrm -rf /",
			want:       "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPaneCommand(tt.projectDir, tt.cmd)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildPaneCommand() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("BuildPaneCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetPanes_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	_, err := GetPanes("nonexistent_session_12345")
	if err == nil {
		t.Error("GetPanes should fail for non-existent session")
	}
}

func TestGetFirstWindow_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	_, err := GetFirstWindow("nonexistent_session_12345")
	if err == nil {
		t.Error("GetFirstWindow should fail for non-existent session")
	}
}

func TestGetDefaultPaneIndex_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	_, err := GetDefaultPaneIndex("nonexistent_session_12345")
	if err == nil {
		t.Error("GetDefaultPaneIndex should fail for non-existent session")
	}
}

func TestZoomPane_Error(t *testing.T) {
	t.Parallel()
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	err := ZoomPane("nonexistent_session_12345", 0)
	if err == nil {
		t.Error("ZoomPane should fail for non-existent session")
	}
}

func TestGetCurrentSession_Simulated(t *testing.T) {
	// cannot run in parallel due to t.Setenv
	skipIfNoTmux(t)
	acquireGlobalTmuxTestLock(t)
	// Simulate being in tmux but command failing (since we aren't actually in a client)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")

	// usage of run() will likely fail or return empty because we aren't attached
	// but we want to ensure it doesn't panic
	session := GetCurrentSession()
	if session != "" {
		// If it actually works (e.g. nested tmux), that's fine too, but unlikely in this env
		t.Logf("GetCurrentSession returned: %q", session)
	}
}
func TestParseAgentFromTitle_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title       string
		wantType    AgentType
		wantVariant string
		wantTags    []string
	}{
		{"invalid_format", AgentUser, "", nil},
		{"session__invalid_1", AgentType("invalid"), "", nil}, // valid regex but invalid type
		{"session__cc_1", AgentClaude, "", nil},
		{"session__cc_1_variant", AgentClaude, "variant", nil},
		{"session__cod_2_gpt4", AgentCodex, "gpt4", nil},
		// With tags
		{"session__cc_1[frontend]", AgentClaude, "", []string{"frontend"}},
		{"session__cc_1[frontend,backend]", AgentClaude, "", []string{"frontend", "backend"}},
		{"session__cc_1_opus[api,urgent]", AgentClaude, "opus", []string{"api", "urgent"}},
		{"session__cod_1[]", AgentCodex, "", nil}, // empty tags
		{"session__gmi_1[test]", AgentGemini, "", []string{"test"}},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			gotType, _, gotVariant, gotTags := parseAgentFromTitle(tt.title)
			if gotType != tt.wantType {
				t.Errorf("parseAgentFromTitle() type = %v, want %v", gotType, tt.wantType)
			}
			if gotVariant != tt.wantVariant {
				t.Errorf("parseAgentFromTitle() variant = %q, want %q", gotVariant, tt.wantVariant)
			}
			if len(gotTags) != len(tt.wantTags) {
				t.Errorf("parseAgentFromTitle() tags = %v, want %v", gotTags, tt.wantTags)
			} else {
				for i := range gotTags {
					if gotTags[i] != tt.wantTags[i] {
						t.Errorf("parseAgentFromTitle() tags[%d] = %q, want %q", i, gotTags[i], tt.wantTags[i])
					}
				}
			}
		})
	}
}

func TestParseAgentFromTitle(t *testing.T) {
	tests := []struct {
		title       string
		wantType    AgentType
		wantIndex   int
		wantVariant string
		wantTags    []string
	}{
		{"myproj__cc_1", AgentClaude, 1, "", nil},
		{"myproj__cod_2_gpt4", AgentCodex, 2, "gpt4", nil},
		{"myproj__gmi_3[foo,bar]", AgentGemini, 3, "", []string{"foo", "bar"}},
		{"myproj__user", AgentUser, 0, "", nil},
		{"other", AgentUser, 0, "", nil},
	}

	for _, tt := range tests {
		gotType, gotIndex, gotVariant, gotTags := parseAgentFromTitle(tt.title)
		if gotType != tt.wantType {
			t.Errorf("parseAgentFromTitle(%q) type = %v, want %v", tt.title, gotType, tt.wantType)
		}
		if gotIndex != tt.wantIndex {
			t.Errorf("parseAgentFromTitle(%q) index = %v, want %v", tt.title, gotIndex, tt.wantIndex)
		}
		if gotVariant != tt.wantVariant {
			t.Errorf("parseAgentFromTitle(%q) variant = %v, want %v", tt.title, gotVariant, tt.wantVariant)
		}
		if len(gotTags) != len(tt.wantTags) {
			t.Errorf("parseAgentFromTitle(%q) tags = %v, want %v", tt.title, gotTags, tt.wantTags)
		}
	}
}

func TestFormatTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tags []string
		want string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"frontend"}, "[frontend]"},
		{[]string{"frontend", "backend"}, "[frontend,backend]"},
		{[]string{"api", "urgent", "test"}, "[api,urgent,test]"},
	}

	for _, tt := range tests {
		name := "nil"
		if tt.tags != nil {
			name = FormatTags(tt.tags)
			if name == "" {
				name = "empty"
			}
		}
		t.Run(name, func(t *testing.T) {
			got := FormatTags(tt.tags)
			if got != tt.want {
				t.Errorf("FormatTags(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestStripTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"session__cc_1", "session__cc_1"},
		{"session__cc_1[frontend]", "session__cc_1"},
		{"session__cc_1_opus[backend,api]", "session__cc_1_opus"},
		{"session__cc_1[]", "session__cc_1"},
		{"title_with[brackets]_in_middle[tags]", "title_with[brackets]_in_middle"},
		{"no_tags_at_all", "no_tags_at_all"},
		// Edge case: [ found but no closing ]
		{"session__cc_1[incomplete", "session__cc_1[incomplete"},
		// Edge case: [ at the end with nothing after
		{"session__cc_1[", "session__cc_1["},
		// Edge case: ] without matching [ at end
		{"session__cc_1]", "session__cc_1]"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripTags(tt.input)
			if got != tt.want {
				t.Errorf("stripTags(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestPaneTitleSessionAndSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title       string
		wantSession string
		wantSuffix  string
	}{
		{"project__cc_1", "project", "cc_1"},
		{"my__project__cursor_2", "my__project", "cursor_2"},
		{"my__project__cod_3_opus[frontend]", "my__project", "cod_3_opus[frontend]"},
		{"__cc_1", "", "cc_1"},
		{"plain_title", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			t.Parallel()

			if got := PaneTitleSession(tt.title); got != tt.wantSession {
				t.Fatalf("PaneTitleSession(%q) = %q, want %q", tt.title, got, tt.wantSession)
			}
			if got := PaneTitleSuffix(tt.title); got != tt.wantSuffix {
				t.Fatalf("PaneTitleSuffix(%q) = %q, want %q", tt.title, got, tt.wantSuffix)
			}
		})
	}
}

func TestParseTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"frontend", []string{"frontend"}},
		{"frontend,backend", []string{"frontend", "backend"}},
		{"api, urgent, test", []string{"api", "urgent", "test"}}, // with spaces
		{",empty,", []string{"empty"}},                           // leading/trailing commas
	}

	for _, tt := range tests {
		name := tt.input
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got := parseTags(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("parseTags(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// bd-byoam: `send-keys` carries the agent's PROMPT in its argv, and agent
// prompts routinely discuss tooling failures. Classification must read the
// command's stderr and nothing else, or caller payload steers retry policy and
// circuit-breaker accounting.
func TestClassifyCommandError_IgnoresCallerPayloadInArgv(t *testing.T) {
	t.Parallel()

	exit1 := commandExitError(t, 1)

	tests := []struct {
		name   string
		prompt string
		stderr string
		want   CommandErrorClass
	}{
		{
			name:   "prompt mentioning permission denied",
			prompt: "explain why the deploy failed with permission denied",
			stderr: "unknown option: -Z",
			want:   CommandErrorClass{Kind: CommandErrorCommandFailed},
		},
		{
			name:   "prompt mentioning no server running",
			prompt: "the docs say no server running means tmux is not started",
			stderr: "unknown option: -Z",
			want:   CommandErrorClass{Kind: CommandErrorCommandFailed},
		},
		{
			name:   "prompt mentioning cant find pane",
			prompt: "handle the case where tmux says can't find pane",
			stderr: "unknown option: -Z",
			want:   CommandErrorClass{Kind: CommandErrorCommandFailed},
		},
		{
			// The real signal still classifies correctly.
			name:   "genuine no-server stderr",
			prompt: "summarize the auth module",
			stderr: "no server running on /tmp/tmux-1000/default",
			want:   CommandErrorClass{Kind: CommandErrorNoServer, Retryable: true},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &CommandError{
				Command: "/usr/bin/tmux",
				Args:    []string{"send-keys", "-t", "proj:0.1", "-l", tt.prompt},
				Stderr:  tt.stderr,
				Err:     exit1,
			}
			if got := ClassifyCommandError(err); got != tt.want {
				t.Fatalf("ClassifyCommandError = %+v, want %+v (prompt must not steer classification)", got, tt.want)
			}
		})
	}
}

// bd-45pfa: on window expiry the breaker CLOSED instead of half-opening, so
// "one probe per window" held only inside the window. Against a still-broken
// tmux every caller waiting at expiry was admitted at once — a 40-pane sweep
// failed fast for the backoff and then issued 40 execs in a burst.
func TestCircuitBreaker_HalfOpensAtExpiryInsteadOfClosing(t *testing.T) {
	c := NewClient("")

	// Trip the breaker.
	for i := 0; i < cbMaxFailures; i++ {
		c.cbRecordFailure()
	}
	if err := c.cbCheck(); err != nil {
		t.Fatalf("first caller in the window should be the probe, got %v", err)
	}
	if err := c.cbCheck(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second caller in the window = %v, want ErrCircuitOpen", err)
	}

	// The probe fails, which extends the window and keeps the gate armed.
	c.cbRecordFailure()

	// Retire the window without waiting for real time.
	c.cbOpenUntil.Store(time.Now().Add(-time.Millisecond).UnixNano())

	// Exactly ONE of many concurrent callers may be admitted.
	const callers = 32
	var admitted atomic.Int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	for i := 0; i < callers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			if err := c.cbCheck(); err == nil {
				admitted.Add(1)
			}
		}()
	}
	start.Done()
	done.Wait()

	if got := admitted.Load(); got != 1 {
		t.Fatalf("%d callers admitted at window expiry, want exactly 1 (half-open)", got)
	}
	if c.cbOpenUntil.Load() == 0 {
		t.Fatal("the circuit closed at expiry; only a successful probe should close it")
	}
}

// A successful probe is the only thing that closes the circuit.
func TestCircuitBreaker_ClosesOnlyOnSuccess(t *testing.T) {
	c := NewClient("")
	for i := 0; i < cbMaxFailures; i++ {
		c.cbRecordFailure()
	}
	if c.cbOpenUntil.Load() == 0 {
		t.Fatal("breaker did not open after the failure threshold")
	}

	c.cbRecordSuccess()
	if c.cbOpenUntil.Load() != 0 {
		t.Fatal("a successful probe must close the circuit")
	}
	// Everyone is admitted once closed.
	for i := 0; i < 5; i++ {
		if err := c.cbCheck(); err != nil {
			t.Fatalf("closed circuit rejected a caller: %v", err)
		}
	}
}

// A stale observation of the deadline must not disarm a freshly opened breaker.
func TestCircuitBreaker_ExpiryCannotClobberANewerDeadline(t *testing.T) {
	c := NewClient("")
	for i := 0; i < cbMaxFailures; i++ {
		c.cbRecordFailure()
	}

	stale := time.Now().Add(-time.Millisecond).UnixNano()
	c.cbOpenUntil.Store(stale)

	// Simulate cbRecordFailure installing a newer deadline between another
	// caller's Load and its transition attempt.
	fresh := time.Now().Add(cbBackoffDuration).UnixNano()
	c.cbOpenUntil.Store(fresh)
	c.cbProbing.Store(true)

	// A caller acting on the stale value must not win the transition.
	if c.cbOpenUntil.CompareAndSwap(stale, time.Now().UnixNano()) {
		t.Fatal("a stale deadline observation won the CAS and disarmed the breaker")
	}
	if got := c.cbOpenUntil.Load(); got != fresh {
		t.Fatalf("deadline = %d, want the fresher %d", got, fresh)
	}
}

// bd-qs6rj: the pane title is the only record of a pane's launch spec that
// survives into a respawn. Encoding only the model meant a session spawned with
// `--cod=8:gpt-5.6-terra:high` came back on the config DEFAULT reasoning effort
// after any recovery — silently, with no operator signal that the swarm's
// reasoning budget had changed.
func TestPaneVariantRoundTripsModelAndEffort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		model       string
		effort      string
		wantVariant string
	}{
		{"model and effort", "gpt-5.6-terra", "high", "gpt-5.6-terra@high"},
		{"model only", "gpt-5.6-terra", "", "gpt-5.6-terra"},
		{"effort with no model is dropped", "", "high", ""},
		{"neither", "", "", ""},
		{"whitespace is trimmed", "  gpt-5.6-terra  ", "  high  ", "gpt-5.6-terra@high"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FormatPaneVariant(tc.model, tc.effort)
			if got != tc.wantVariant {
				t.Fatalf("FormatPaneVariant(%q, %q) = %q, want %q", tc.model, tc.effort, got, tc.wantVariant)
			}
			model, effort := ParsePaneVariant(got)
			if want := strings.TrimSpace(tc.model); model != want {
				t.Fatalf("round-tripped model = %q, want %q", model, want)
			}
			if want := strings.TrimSpace(tc.effort); tc.model != "" && effort != want {
				t.Fatalf("round-tripped effort = %q, want %q", effort, want)
			}
		})
	}
}

// The encoded variant must survive the pane-title regex, or the whole scheme
// silently degrades to "no variant at all".
func TestPaneTitleCarriesModelAndEffortThroughParsing(t *testing.T) {
	t.Parallel()

	variant := FormatPaneVariant("gpt-5.6-terra", "high")
	title := FormatPaneName("ntm", "cod", 3, variant)
	if title != "ntm__cod_3_gpt-5.6-terra@high" {
		t.Fatalf("title = %q", title)
	}

	agentType, idx, parsedVariant, _ := parseAgentFromTitle(title)
	if agentType != AgentType("cod") || idx != 3 {
		t.Fatalf("parsed type=%v index=%d, want cod/3", agentType, idx)
	}
	if parsedVariant != variant {
		t.Fatalf("parsed variant = %q, want %q — '@' must be inside the title regex charset", parsedVariant, variant)
	}

	model, effort := ParsePaneVariant(parsedVariant)
	if model != "gpt-5.6-terra" || effort != "high" {
		t.Fatalf("recovered (%q, %q), want (gpt-5.6-terra, high)", model, effort)
	}
}

// A pane titled before this encoding existed carries a bare model. It must keep
// working, not be read as a model named after an effort.
func TestPaneVariantBackCompatBareModel(t *testing.T) {
	t.Parallel()

	model, effort := ParsePaneVariant("opus")
	if model != "opus" || effort != "" {
		t.Fatalf("bare variant parsed as (%q, %q), want (opus, \"\")", model, effort)
	}
}
