package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Exercise the shipped build -> SQLite -> inject --pack workflow. Only live
// tmux resolution/delivery is substituted; source preparation and storage are
// real, and no optional context tool is available.
func TestContextBuildCommandNativeSource(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	oldJSON, oldCfg := jsonOutput, cfg
	jsonOutput, cfg = true, nil
	t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldCfg })
	builder := ntmctx.NewContextPackBuilder(nil)
	builder.ClearCache()
	t.Cleanup(builder.ClearCache)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// cli-native-source-proof\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Directories and ** globs must survive the real command, pack rendering,
	// SQLite round-trip, and the stored-artifact delivery path below.
	for file, content := range map[string]string{
		"src/nested/worker.go": "package worker\n// cli-directory-source-proof\n",
		"lib/root.go":          "package lib\n// cli-zero-depth-glob-proof\n",
		"lib/deep/helper.go":   "package helper\n// cli-recursive-glob-proof\n",
		"lib/deep/ignored.txt": "cli-nonmatching-file-must-not-be-included",
	} {
		name := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}

	capture, err := os.CreateTemp(t.TempDir(), "context-output-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	oldStdout := os.Stdout
	os.Stdout = capture
	t.Cleanup(func() { os.Stdout = oldStdout })
	cmd := newContextCmd()
	cmd.SetArgs([]string{"build", "--agent", "cod", "--files", "main.go,src,lib/**/*.go"})
	err = cmd.Execute()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	var pack ntmctx.ContextPackFull
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatalf("context build did not emit a JSON pack: %v: %s", err, raw)
	}
	source := pack.Components["s2p"]
	if source == nil || source.Error != "" || !strings.Contains(pack.RenderedPrompt, "cli-native-source-proof") {
		t.Fatalf("context build omitted native source: %s", raw)
	}
	for _, marker := range []string{"cli-directory-source-proof", "cli-zero-depth-glob-proof", "cli-recursive-glob-proof"} {
		if !strings.Contains(pack.RenderedPrompt, marker) {
			t.Fatalf("context build omitted %s: %s", marker, raw)
		}
	}
	if strings.Contains(pack.RenderedPrompt, "cli-nonmatching-file-must-not-be-included") {
		t.Fatalf("context build included a nonmatching file: %s", raw)
	}

	store, err := state.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stored, err := store.GetContextPack(pack.ID)
	if err != nil || stored == nil || stored.RenderedPrompt != pack.RenderedPrompt {
		t.Fatalf("build printed an unusable pack ID: %+v, %v", stored, err)
	}
	// Pin database timestamp round-tripping as well as same-store idempotency.
	if err := persistContextPack(context.Background(), store, &pack.ContextPack); err != nil {
		t.Fatalf("persisting the identical stored artifact must be a no-op: %v", err)
	}
	calls := 0
	deps := contextInjectDeps{
		resolve: func(context.Context, string) (string, error) { return "demo", nil },
		panes: func(string) ([]tmux.Pane, error) {
			return []tmux.Pane{{ID: "%2", Index: 2, Type: tmux.AgentCodex}}, nil
		},
		load: store.GetContextPack,
		send: func(_ context.Context, pane tmux.Pane, text string) error {
			calls++
			if pane.ID != "%2" || text != pack.RenderedPrompt {
				t.Fatalf("stored source artifact changed during delivery to %+v", pane)
			}
			return nil
		},
	}
	var out bytes.Buffer
	inject := newContextInjectCmdWithDeps(&deps)
	inject.SetOut(&out)
	inject.SetArgs([]string{"demo", "--pack", pack.ID, "--pane", "2"})
	if err := inject.Execute(); err != nil {
		t.Fatal(err)
	}
	var result ContextInjectResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("inject did not emit one JSON envelope: %v: %s", err, out.String())
	}
	if calls != 1 || !result.Success || result.Mode != "pack" || len(result.Deliveries) != 1 || result.Deliveries[0].PackID != pack.ID {
		t.Fatalf("build/store/inject workflow failed: calls=%d result=%+v", calls, result)
	}
}

// Real context assembly and the real cm CLI run from a different, same-named
// project; only tmux discovery and delivery are recorded at the command seam.
func TestContextInjectCommandMemoryUsesTargetProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording cm executable requires a POSIX shell")
	}
	for _, mode := range []string{"deliver", "dry-run", "remote", "unknown-project", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			caller, target := filepath.Join(root, "client A", "app"), filepath.Join(root, "client B", "app")
			for _, dir := range []string{caller, target} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(caller)
			t.Setenv("PATH", root)
			t.Setenv("NTM_CONTEXT_CM_ROOT", root)
			t.Setenv("NTM_CONTEXT_CM_TARGET", target)
			const script = `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$NTM_CONTEXT_CM_ROOT/cm-argv"
if [ "$1" = context ] && [ "$4" = --workspace ] && [ "$5" = "$NTM_CONTEXT_CM_TARGET" ]; then
  printf '%s\n' '{"success":true,"data":{"relevantBullets":[{"id":"target-rule","content":"TARGET_WORKSPACE_MEMORY"}]}}'
else
  printf '%s\n' '{"success":true,"data":{"relevantBullets":[{"id":"wrong-rule","content":"CALLER_WORKSPACE_MEMORY"}]}}'
fi
`
			if err := os.WriteFile(filepath.Join(root, "cm"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package target\n"), 0600); err != nil {
				t.Fatal(err)
			}
			oldJSON, oldCfg := jsonOutput, cfg
			jsonOutput, cfg = true, nil
			t.Cleanup(func() { jsonOutput, cfg = oldJSON, oldCfg })
			builder := ntmctx.NewContextPackBuilder(nil)
			var delivered string
			deps := contextInjectDeps{
				resolve: func(context.Context, string) (string, error) { return "target", nil },
				project: func(context.Context, string) (string, error) {
					if mode == "unknown-project" {
						return "", os.ErrNotExist
					}
					return target, nil
				},
				panes: func(string) ([]tmux.Pane, error) {
					return []tmux.Pane{{ID: "%7", Index: 1, Type: tmux.AgentCodex}}, nil
				},
				build: builder.Build,
				send: func(_ context.Context, pane tmux.Pane, text string) error {
					delivered = text
					return nil
				},
				remote: mode == "remote",
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"target", "--build", "--files", "main.go", "--task", "fix auth"}
			if mode == "dry-run" {
				args = append(args, "--dry-run")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			cmd.SetContext(ctx)
			cmd.SetArgs(args)
			err := cmd.Execute()
			argv, _ := os.ReadFile(filepath.Join(root, "cm-argv"))
			if mode == "remote" {
				if err == nil || len(argv) != 0 || delivered != "" ||
					!strings.Contains(out.String(), ntmctx.ErrRemoteContextPreparation.Error()) {
					t.Fatalf("remote build reached local context preparation: err=%v argv=%s delivered=%q output=%s", err, argv, delivered, out.String())
				}
				return
			}
			if mode == "unknown-project" || mode == "canceled" {
				if err == nil || len(argv) != 0 || delivered != "" {
					t.Fatalf("invalid scope/cancellation queried or delivered context: err=%v argv=%s delivered=%q", err, argv, delivered)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(argv), "--workspace\n"+target+"\n") || strings.Contains(string(argv), caller) {
				t.Fatalf("CM received caller workspace: %s", argv)
			}
			if mode == "dry-run" {
				if delivered != "" {
					t.Fatal("dry run delivered a prompt")
				}
			} else if !strings.Contains(delivered, "TARGET_WORKSPACE_MEMORY") || strings.Contains(delivered, "CALLER_WORKSPACE_MEMORY") || !strings.Contains(delivered, "package target") {
				t.Fatalf("wrong project context delivered: %s", delivered)
			}
		})
	}
}

func TestContextInjectCommandRejectsRemoteSourcePreparation(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	t.Setenv("PATH", t.TempDir())

	for _, scenario := range []struct {
		name string
		args []string
	}{
		{"default files", nil},
		{"selected files", []string{"--files", "README.md"}},
		{"default build", []string{"--build"}},
		{"selected build", []string{"--build", "--files", "README.md"}},
	} {
		for _, dryRun := range []bool{false, true} {
			name := scenario.name
			if dryRun {
				name += " dry run"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				const localSecret = "LOCAL_PROJECT_CONTEXT_MUST_NOT_REACH_REMOTE"
				if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(localSecret), 0600); err != nil {
					t.Fatal(err)
				}
				var resolved, projects, listed, built, sent int
				builder := ntmctx.NewContextPackBuilder(nil)
				deps := contextInjectDeps{
					remote: true,
					resolve: func(context.Context, string) (string, error) {
						resolved++
						return "demo", nil
					},
					project: func(context.Context, string) (string, error) {
						projects++
						return dir, nil
					},
					panes: func(string) ([]tmux.Pane, error) {
						listed++
						return []tmux.Pane{{ID: "%7", Index: 1, Type: tmux.AgentClaude}}, nil
					},
					build: func(ctx context.Context, opts ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
						built++
						return builder.Build(ctx, opts)
					},
					send: func(context.Context, tmux.Pane, string) error {
						sent++
						return nil
					},
				}
				cmd := newContextInjectCmdWithDeps(&deps)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				args := append([]string{"demo"}, scenario.args...)
				if dryRun {
					args = append(args, "--dry-run")
				}
				cmd.SetArgs(args)
				err := cmd.ExecuteContext(t.Context())
				var result ContextInjectResult
				if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
					t.Fatalf("invalid remote preparation response: %v: %s", decodeErr, out.String())
				}
				if err == nil || result.Success || result.Error != ntmctx.ErrRemoteContextPreparation.Error() {
					t.Fatalf("remote source preparation was not refused: %+v err=%v", result, err)
				}
				if resolved != 0 || projects != 0 || listed != 0 || built != 0 || sent != 0 {
					t.Fatalf("remote preparation reached I/O: resolve=%d project=%d panes=%d build=%d send=%d", resolved, projects, listed, built, sent)
				}
				if result.TotalBytes != 0 || len(result.InjectedFiles) != 0 || len(result.PanesInjected) != 0 || strings.Contains(out.String(), localSecret) {
					t.Fatalf("remote refusal exposed or claimed local context: %+v", result)
				}
			})
		}
	}
}

func TestContextInjectCommandRemoteStoredPackRemainsAvailable(t *testing.T) {
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, dryRun := range []bool{false, true} {
		name := "deliver"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			const artifact = "explicitly selected stored context"
			var loaded, sent int
			deps := contextInjectDeps{
				remote:  true,
				resolve: func(context.Context, string) (string, error) { return "demo", nil },
				project: func(context.Context, string) (string, error) {
					t.Fatal("stored artifact delivery must not resolve a project source path")
					return "", nil
				},
				panes: func(string) ([]tmux.Pane, error) {
					return []tmux.Pane{{ID: "%7", Index: 1, Type: tmux.AgentClaude}}, nil
				},
				load: func(id string) (*state.ContextPack, error) {
					loaded++
					if id != "pack-selected" {
						t.Fatalf("wrong artifact requested: %s", id)
					}
					return &state.ContextPack{ID: id, AgentType: "cc", RenderedPrompt: artifact}, nil
				},
				build: func(context.Context, ntmctx.BuildOptions) (*ntmctx.ContextPackFull, error) {
					t.Fatal("stored artifact delivery must not rebuild local context")
					return nil, nil
				},
				send: func(_ context.Context, pane tmux.Pane, text string) error {
					sent++
					if pane.ID != "%7" || text != artifact {
						t.Fatalf("wrong stored artifact delivery: pane=%+v text=%q", pane, text)
					}
					return nil
				},
			}
			cmd := newContextInjectCmdWithDeps(&deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"demo", "--pack", "pack-selected"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			var result ContextInjectResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantSent, wantStatus := 1, "delivered"
			if dryRun {
				wantSent, wantStatus = 0, "planned"
			}
			if !result.Success || result.Mode != "pack" || loaded != 1 || sent != wantSent || len(result.PanesInjected) != wantSent || len(result.Deliveries) != 1 {
				t.Fatalf("explicit remote artifact failed: result=%+v loaded=%d sent=%d", result, loaded, sent)
			}
			if result.Deliveries[0].PackID != "pack-selected" || result.Deliveries[0].Status != wantStatus {
				t.Fatalf("remote artifact receipt lost its source or outcome: %+v", result.Deliveries[0])
			}
		})
	}
}

func TestContextBuildCommandRejectsRemoteSourcePreparation(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("NTM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("LOCAL_BUILD_CONTEXT_MUST_NOT_BE_READ"), 0600); err != nil {
		t.Fatal(err)
	}
	oldClient, oldJSON, oldCfg := tmux.DefaultClient, jsonOutput, cfg
	tmux.DefaultClient, jsonOutput, cfg = tmux.NewClient("context-source.invalid"), true, nil
	t.Cleanup(func() { tmux.DefaultClient, jsonOutput, cfg = oldClient, oldJSON, oldCfg })
	for _, args := range [][]string{{"build"}, {"build", "--files", "README.md"}} {
		cmd := newContextCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, ntmctx.ErrRemoteContextPreparation) {
			t.Fatalf("context %v did not reject remote preparation: %v", args, err)
		}
	}
}

func TestRobotContextInjectRejectsRemoteSourcePreparation(t *testing.T) {
	// An accidental remote lookup must not reach a real ssh binary.
	t.Setenv("PATH", t.TempDir())
	run := newRobotProcessRunner(t)
	payload, code := run("--ssh=context-source.invalid", "--robot-format=json", "--robot-context-inject=demo", "--inject-files=README.md")
	message, _ := payload["error"].(string)
	hint, _ := payload["hint"].(string)
	if code != 2 || payload["success"] != false || payload["error_code"] != robot.ErrCodeNotImplemented ||
		!strings.Contains(message, "remote source collection") || !strings.Contains(hint, "--pack") {
		t.Fatalf("remote robot preparation did not return an actionable unavailable result: code=%d payload=%v", code, payload)
	}
}
