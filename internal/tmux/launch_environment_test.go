package tmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidateLaunchEnvironment(t *testing.T) {
	for _, environment := range []map[string]string{
		nil,
		{},
		{"API_TOKEN": ""},
		{"PATH": "/tool/bin:/usr/bin", "TOKEN": "literal 'quoted' $() \x60x\x60 ; # \\"},
	} {
		if err := ValidateLaunchEnvironment(environment); err != nil {
			t.Fatalf("valid environment rejected: %v", err)
		}
	}
	for _, tc := range []struct {
		name        string
		environment map[string]string
	}{
		{"assignment instead of name", map[string]string{"TOKEN=secret-marker": "value"}},
		{"shell injection name", map[string]string{"TOKEN;secret-marker": "value"}},
		{"numeric leading name", map[string]string{"9TOKEN": "secret-marker"}},
		{"NUL", map[string]string{"TOKEN": "secret-marker\x00"}},
		{"newline", map[string]string{"TOKEN": "secret-marker\nexit"}},
		{"escape", map[string]string{"TOKEN": "secret-marker\x1b"}},
		{"invalid UTF-8", map[string]string{"TOKEN": "secret-marker\xff"}},
		{"oversized value", map[string]string{"TOKEN": strings.Repeat("s", maxLaunchEnvironmentValue+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLaunchEnvironment(tc.environment)
			if err == nil || strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("invalid environment did not produce a redacted error: %v", err)
			}
		})
	}
	for _, name := range []string{
		"ENV", "BASH_ENV", "SHELLOPTS", "BASHOPTS", "BASH_XTRACEFD", "PS4", "IFS",
		"UID", "OPTIND", "HISTSIZE", "TMOUT", "COLUMNS", "LINES", "BASH_SUBSHELL",
		"PWD", "OLDPWD", "TMUX", "TMUX_PANE",
	} {
		if err := ValidateLaunchEnvironment(map[string]string{name: "secret-marker"}); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("reserved key %s was not rejected safely", name)
		}
	}
	oversized := make(map[string]string)
	for i := 0; i <= maxLaunchEnvironmentKeys; i++ {
		oversized[fmt.Sprintf("KEY_%d", i)] = ""
	}
	if err := ValidateLaunchEnvironment(oversized); err == nil {
		t.Fatal("unbounded environment key count accepted")
	}
	oversized = make(map[string]string)
	for i := 0; i < 17; i++ {
		oversized[fmt.Sprintf("KEY_%d", i)] = strings.Repeat("s", maxLaunchEnvironmentValue)
	}
	if err := ValidateLaunchEnvironment(oversized); err == nil {
		t.Fatal("unbounded aggregate environment size accepted")
	}
}

func TestRecoveryEnvironmentReplayGuards(t *testing.T) {
	spec := AgentLaunchSpec{
		Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex --model retained-model",
		OmittedEnv: []string{"AGENT_MAIL_AGENT", "BEADS_DIR", "BV_ROBOT_NOT_READY_LABELS", "PATH", "ZAI_SEAT_ROLE"},
	}
	environment := map[string]string{
		"AGENT_MAIL_AGENT": "BlueBear", "BEADS_DIR": "/work/.beads", "BV_ROBOT_NOT_READY_LABELS": "",
		"PATH": "/work/bin:/usr/bin:/bin", "ZAI_SEAT_ROLE": "reviewer",
	}
	before, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	for name := range environment {
		t.Setenv(name, "unrelated-ambient-value")
	}
	if err := spec.ValidateReplay(AgentCodex); err == nil {
		t.Fatal("ambient environment satisfied a missing explicit recovery input")
	}
	if err := spec.ValidateReplayWithEnvironment(AgentCodex, environment); err != nil {
		t.Fatalf("complete explicit recovery rejected: %v", err)
	}
	for _, missing := range spec.OmittedEnv {
		incomplete := make(map[string]string, len(environment))
		for name, value := range environment {
			if name != missing {
				incomplete[name] = value
			}
		}
		if err := spec.ValidateReplayWithEnvironment(AgentCodex, incomplete); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing input error=%v, want key %s", err, missing)
		}
	}
	environment["UNREQUESTED_TOKEN"] = "must-not-appear-in-errors"
	err = spec.ValidateReplayWithEnvironment(AgentCodex, environment)
	if err == nil || !strings.Contains(err.Error(), "UNREQUESTED_TOKEN") || strings.Contains(err.Error(), environment["UNREQUESTED_TOKEN"]) {
		t.Fatalf("unexpected input was not rejected safely: %v", err)
	}
	after, err := json.Marshal(spec)
	if err != nil || string(before) != string(after) {
		t.Fatal("recovery validation changed the durable launch specification")
	}
	if err := spec.ValidateReplay(AgentCodex); err == nil {
		t.Fatal("explicit recovery permanently cleared the omitted-environment safeguard")
	}
}

func TestRecoveryEnvironmentRejectsManagedIdentityCollisions(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec AgentLaunchSpec
		key  string
	}{
		{
			"saved mail project",
			AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex", AgentMailProject: "/work/project"},
			"AGENT_MAIL_PROJECT",
		},
		{
			"isolated Claude config",
			AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: AgentClaude, Command: "claude", ClaudeIsolateCredentials: true},
			"CLAUDE_CONFIG_DIR",
		},
		{
			"isolated Claude token",
			AgentLaunchSpec{Version: AgentLaunchSpecVersion, AgentType: AgentClaude, Command: "claude", ClaudeIsolateCredentials: true},
			"CLAUDE_CODE_OAUTH_TOKEN",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			spec.OmittedEnv = []string{tc.key}
			err := spec.ValidateReplayWithEnvironment(spec.AgentType, map[string]string{tc.key: "secret-marker"})
			if err == nil || !strings.Contains(err.Error(), tc.key) || strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("managed launch identity was not protected: %v", err)
			}
		})
	}
	for _, key := range []string{
		"HOME", "SHALLOW_PROFILE", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "GEMINI_HOME",
		"CAAM_SHALLOW_HOMES_DIR", "SHALLOW_DEFAULT_PROFILE", "XDG_CONFIG_HOME",
		"OPENAI_API_KEY", "CODEX_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY",
	} {
		spec := AgentLaunchSpec{
			Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex",
			CAAMProfile: "saved-profile", OmittedEnv: []string{key},
		}
		err := spec.ValidateReplayWithEnvironment(AgentCodex, map[string]string{key: "secret-marker"})
		if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("profile collision for %s was not rejected safely", key)
		}
	}
	spec := AgentLaunchSpec{
		Version: AgentLaunchSpecVersion, AgentType: AgentCodex, Command: "codex",
		CAAMProfile: "saved-profile", OmittedEnv: []string{"BEADS_DIR", "PATH"},
	}
	if err := spec.ValidateReplayWithEnvironment(AgentCodex, map[string]string{
		"BEADS_DIR": "/work/.beads", "PATH": "/work/bin:/usr/bin:/bin",
	}); err != nil {
		t.Fatalf("nonidentity profile inputs were rejected: %v", err)
	}
}

type launchEnvironmentProcessResult struct {
	Environment map[string]string
	Args        []string
	Input       string
}

// This process stands in for an agent without running a real provider CLI. Its
// private result is a test fixture; production transport writes no value file.
func TestLaunchEnvironmentProcessHelper(t *testing.T) {
	resultPath := os.Getenv("NTM_RECOVERY_ENV_TEST_RESULT")
	if resultPath == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal("cannot read helper stdin")
	}
	result := launchEnvironmentProcessResult{
		Environment: make(map[string]string), Args: os.Args, Input: string(input),
	}
	for _, key := range []string{
		"AGENT_MAIL_AGENT", "BEADS_DIR", "BV_ROBOT_NOT_READY_LABELS", "PATH", "ZAI_SEAT_ROLE", "API_TOKEN",
	} {
		value, ok := os.LookupEnv(key)
		if !ok {
			t.Fatalf("missing explicit environment key %s", key)
		}
		result.Environment[key] = value
	}
	encoded, err := json.Marshal(result)
	if err != nil || os.WriteFile(resultPath, encoded, 0600) != nil {
		t.Fatal("cannot write helper result")
	}
}

func TestLaunchEnvironmentPrivateTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resultPath := filepath.Join(t.TempDir(), "result.json")
	environment := map[string]string{
		"AGENT_MAIL_AGENT": "BlueBear", "BEADS_DIR": "/work/a project/.beads", "BV_ROBOT_NOT_READY_LABELS": "",
		"PATH": "/explicit/path/with/no/tools", "ZAI_SEAT_ROLE": "reviewer",
		"API_TOKEN":                    "private-'quoted'-$HOME-$(touch SHOULD_NOT_EXIST)-\x60touch SHOULD_NOT_EXIST\x60-;#{}=\\-λ",
		"NTM_RECOVERY_ENV_TEST_RESULT": resultPath,
	}
	launch, err := NewLaunchEnvironment(ctx, environment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	for _, path := range []string{launch.directory, launch.pipe} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatal("recovery transport is accessible to another user")
		}
		if path == launch.pipe && (info.Mode()&os.ModeNamedPipe == 0 || info.Size() != 0) {
			t.Fatal("recovery transport persists values in a regular file")
		}
	}
	encoded, err := json.Marshal(launch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v %s", launch, launch, encoded), environment["API_TOKEN"]) {
		t.Fatal("recovery handoff leaked through diagnostic formatting")
	}
	wantToken := environment["API_TOKEN"]
	environment["API_TOKEN"] = "mutated-after-preflight"
	var shell *exec.Cmd
	var output bytes.Buffer
	var typed string
	err = launch.send(ctx, "%7", ShellQuote(os.Args[0])+" -test.run=^TestLaunchEnvironmentProcessHelper$", AgentCodex,
		func(_ context.Context, target, command string, enter bool, agentType AgentType) error {
			if target != "%7" || !enter || agentType != AgentCodex {
				t.Fatal("recovery changed the pane launch routing")
			}
			typed = command
			// The parent intentionally has tracing and verbose input enabled.
			shell = exec.CommandContext(ctx, "/bin/sh", "-xv", "-c", command)
			shell.Env = append(os.Environ(), "SHELLOPTS=xtrace:verbose", "NTM_TEST_TMUX_ENV_OWNED=1")
			shell.Stdin = strings.NewReader("terminal stdin survives")
			shell.Stdout, shell.Stderr = &output, &output
			return shell.Start()
		})
	if err != nil {
		t.Fatalf("private environment send failed: %v", err)
	}
	if err := shell.Wait(); err != nil {
		t.Fatalf("agent stand-in did not finish: %v", err)
	}
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result launchEnvironmentProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal("invalid helper result")
	}
	for name, expected := range environment {
		if name == "NTM_RECOVERY_ENV_TEST_RESULT" {
			continue
		}
		if name == "API_TOKEN" {
			expected = wantToken
		}
		if value, ok := result.Environment[name]; !ok || value != expected {
			t.Errorf("explicit environment value %s was lost, expanded, or changed", name)
		}
	}
	if result.Input != "terminal stdin survives" {
		t.Fatal("environment delivery consumed the agent's terminal stdin")
	}
	for _, surface := range []string{typed, output.String(), strings.Join(result.Args, " ")} {
		if strings.Contains(surface, wantToken) || strings.Contains(surface, "private-'quoted'") {
			t.Fatal("credential appeared in pane input, shell output, or process arguments")
		}
	}
	if _, err := os.Stat(launch.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("consumed environment handoff was not removed")
	}
	if err := launch.Send(ctx, "%7", "true", true, AgentCodex); err == nil {
		t.Fatal("one-shot environment handoff was reused")
	}
	if err := launch.Close(); err != nil {
		t.Fatalf("second close is not idempotent: %v", err)
	}
}

func TestLaunchEnvironmentFailureBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	for _, testCase := range []string{"sender failure", "absent reader", "stalled reader"} {
		t.Run(testCase, func(t *testing.T) {
			environment := make(map[string]string)
			for i := 0; i < 16; i++ {
				environment[fmt.Sprintf("API_TOKEN_%d", i)] = strings.Repeat("secret-marker", 4000)
			}
			// With the padded command, the script is more than a FIFO holds for a
			// reader that never reads: 64 KiB on Linux, 1 MiB on macOS.
			command := "true" + strings.Repeat(" ", 512*1024)
			launch, err := NewLaunchEnvironment(t.Context(), environment)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = launch.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			readerOpened := make(chan *os.File, 1)
			started := time.Now()
			err = launch.send(ctx, "%7", command, AgentCodex, func(context.Context, string, string, bool, AgentType) error {
				if testCase == "sender failure" {
					return errors.New("secret-marker: diagnostic from an untrusted sender")
				}
				if testCase == "stalled reader" {
					go func() {
						file, _ := os.Open(launch.pipe)
						readerOpened <- file
					}()
				}
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("failed transport did not return a redacted error: %v", err)
			}
			if testCase != "sender failure" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("canceled transport returned %v", err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("canceled FIFO operation did not return promptly")
			}
			if testCase == "stalled reader" {
				select {
				case reader := <-readerOpened:
					if reader != nil {
						_ = reader.Close()
					}
				case <-time.After(time.Second):
					t.Fatal("FIFO reader remained blocked after cleanup")
				}
			}
			if _, err := os.Stat(launch.directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed environment handoff was not removed")
			}
		})
	}
}

// A reader that starts draining late still receives a script larger than the
// FIFO holds (1 MiB on macOS, where Go reports a full FIFO as EAGAIN).
func TestLaunchEnvironmentWaitsForSlowReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	environment := make(map[string]string)
	for i := 0; i < 16; i++ {
		environment[fmt.Sprintf("API_TOKEN_%d", i)] = strings.Repeat("slow-reader", 4000)
	}
	command := "true" + strings.Repeat(" ", 512*1024)
	launch, err := NewLaunchEnvironment(t.Context(), environment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	err = launch.send(ctx, "%7", command, AgentCodex, func(context.Context, string, string, bool, AgentType) error {
		go func() {
			file, err := os.Open(launch.pipe)
			if err != nil {
				result <- readResult{nil, err}
				return
			}
			defer file.Close()
			time.Sleep(200 * time.Millisecond)
			data, err := io.ReadAll(file)
			result <- readResult{data, err}
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("slow reader did not receive the handoff: %v", err)
	}
	read := <-result
	if read.err != nil || len(read.data) <= 1<<20 || !bytes.HasSuffix(read.data, []byte("\n}\n")) {
		t.Fatalf("reader got %d bytes (%v), want the complete script of more than 1 MiB", len(read.data), read.err)
	}
}

func TestLaunchEnvironmentPreflightAndUnusedCleanup(t *testing.T) {
	if _, err := NewLaunchEnvironment(nil, nil); err == nil {
		t.Fatal("nil recovery context accepted")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewLaunchEnvironment(canceled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery preflight returned %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	launch, err := NewLaunchEnvironment(t.Context(), map[string]string{"TOKEN": "secret-marker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	called := false
	send := func(context.Context, string, string, bool, AgentType) error { called = true; return nil }
	if err := launch.Send(t.Context(), "%7", "true", false, AgentCodex); err == nil {
		t.Fatal("deferred Enter could leave a blocked recovery reader")
	}
	if err := launch.send(t.Context(), "session:0.0", "true", AgentCodex, send); err == nil {
		t.Fatal("nonphysical recovery target accepted")
	}
	if err := launch.send(t.Context(), "%7", "true\nsecret-marker", AgentCodex, send); err == nil || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("unsafe command did not fail safely: %v", err)
	}
	if called {
		t.Fatal("invalid recovery input reached the pane")
	}
	if err := launch.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(launch.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run handoff was not removed")
	}
}

func TestLaunchEnvironmentTruncatedPayloadDoesNotLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	launch, err := NewLaunchEnvironment(ctx, map[string]string{"TOKEN": "private-truncation-marker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	marker := filepath.Join(t.TempDir(), "launched")
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	err = launch.send(ctx, "%7", "printf launched > "+ShellQuote(marker), AgentCodex,
		func(context.Context, string, string, bool, AgentType) error {
			go func() {
				data, err := os.ReadFile(launch.pipe)
				result <- readResult{data, err}
			}()
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	select {
	case read := <-result:
		if read.err != nil {
			t.Fatal(read.err)
		}
		payload = read.data
	case <-ctx.Done():
		t.Fatal("payload reader did not finish")
	}
	// Parse actual production payload truncated in a value and after the
	// launch command but before receipt of the closing compound.
	for _, cut := range []int{strings.Index(string(payload), "truncation"), strings.LastIndex(string(payload), "\n}\n")} {
		if cut <= 0 {
			t.Fatal("private launch payload lacks its atomic compound")
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-p")
		cmd.Stdin = bytes.NewReader(payload[:cut])
		output, runErr := cmd.CombinedOutput()
		if runErr == nil {
			t.Fatal("truncated launch program was accepted")
		}
		if strings.Contains(string(output), "private-truncation-marker") {
			t.Fatal("shell diagnostics revealed a truncated environment value")
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a partially received launch program executed the agent command")
		}
	}
}

func TestLaunchEnvironmentCloseReleasesBlockedReader(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff requires Unix FIFOs")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	launch, err := NewLaunchEnvironment(ctx, map[string]string{"TOKEN": "never-delivered"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = launch.Close() })
	reader := exec.CommandContext(ctx, "/bin/sh", "-p", launch.pipe)
	if err := reader.Start(); err != nil {
		t.Fatal(err)
	}
	// The reader either waits on open or races with removal. Both must end.
	if err := launch.Close(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Wait()
	if ctx.Err() != nil {
		t.Fatal("close left the shell blocked opening the removed FIFO")
	}
}

func TestLaunchEnvironmentRejectsRemoteTransport(t *testing.T) {
	original := DefaultClient
	DefaultClient = NewClient("remote.example")
	t.Cleanup(func() { DefaultClient = original })
	if _, err := NewLaunchEnvironment(t.Context(), map[string]string{"TOKEN": "secret-marker"}); err == nil || !strings.Contains(err.Error(), "local tmux") || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("remote environment transport was not rejected safely: %v", err)
	}
}

func TestOpenLaunchEnvironmentFileRefusesUnsafeTypes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("safe local environment file opening requires Unix")
	}
	dir := t.TempDir()
	regular := filepath.Join(dir, "environment.json")
	if err := os.WriteFile(regular, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := OpenLaunchEnvironmentFile(regular)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := createFIFO(fifo); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, fifo, dir} {
		started := time.Now()
		file, err := OpenLaunchEnvironmentFile(path)
		if file != nil {
			_ = file.Close()
		}
		if err == nil {
			t.Fatal("unsafe environment source was opened")
		}
		if time.Since(started) > time.Second {
			t.Fatal("environment source preflight blocked on a special file")
		}
	}
}
