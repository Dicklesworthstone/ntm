package agentsession

import (
	"bufio"
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

	"github.com/Dicklesworthstone/ntm/internal/config"
)

const globalBindingTestID = "019543cf-eec4-79ff-a77a-90d271789499"
const globalBindingOtherID = "019543cf-eec4-79ff-a77a-90d271789488"

type globalBindingFixture struct {
	home, workDir, provider, transcript string
	processes                           map[int]globalBindingProcess
	reads                               map[int]int
	deps                                globalBindingDependencies
}

func newGlobalBindingFixture(t *testing.T, provider string) *globalBindingFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &globalBindingFixture{
		home: filepath.Join(root, "home"), workDir: filepath.Join(root, "worktree"), provider: provider,
		processes: make(map[int]globalBindingProcess), reads: make(map[int]int),
	}
	for _, dir := range []string{f.home, f.workDir, filepath.Join(f.home, "."+provider)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.transcript = f.writeTranscript(t, globalBindingTestID, f.workDir, false)
	f.processes[100] = globalBindingProcess{pid: 100, parent: 10, started: 1700000000000,
		executable: "/bin/bash", argv: []string{"bash"}, children: []int{101}}
	f.processes[101] = globalBindingProcess{pid: 101, parent: 100, started: 1700000001000,
		executable: "/usr/bin/" + provider, argv: []string{provider, "--model", "example-model"},
		environment: []string{"HOME=" + f.home}, cwd: f.workDir, files: []globalBindingFile{bindingTestFile(t, f.transcript)}}
	f.deps = globalBindingDependencies{
		platform: "linux", home: func() (string, error) { return f.home, nil },
		environment: func() []string { return []string{"HOME=" + f.home} },
		systemPaths: func(string) ([]string, error) { return nil, nil },
		process: func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
			f.reads[pid]++
			process, ok := f.processes[pid]
			if !ok {
				return globalBindingProcess{}, os.ErrNotExist
			}
			return process, ctx.Err()
		},
	}
	return f
}

func bindingTestFile(t *testing.T, path string) globalBindingFile {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return globalBindingFile{path: path, info: info}
}

func writeGlobalBindingTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *globalBindingFixture) writeTranscript(t *testing.T, id, cwd string, subagent bool) string {
	t.Helper()
	var path string
	var record any
	if f.provider == "codex" {
		path = filepath.Join(f.home, ".codex", "sessions", "2026", "09", "22", "rollout-2026-09-22T10-00-00-"+id+".jsonl")
		var source any = "cli"
		if subagent {
			source = map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": globalBindingTestID}}}
		}
		record = map[string]any{"type": "session_meta", "payload": map[string]any{
			"id": id, "cwd": cwd, "source": source, "model_provider": "openai",
		}}
	} else {
		path = filepath.Join(f.home, ".claude", "projects", encodeClaudeProjectDir(cwd), id+".jsonl")
		record = map[string]any{"type": "user", "sessionId": id, "cwd": cwd, "isSidechain": subagent,
			"message": map[string]any{"role": "user", "content": "Continue the existing task"}}
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	writeGlobalBindingTestFile(t, path, string(data)+"\n")
	return path
}

func (f *globalBindingFixture) observe(t *testing.T) (GlobalCredentialBinding, error) {
	t.Helper()
	return observeGlobalCredentialBinding(t.Context(), f.provider, f.workDir, 100, f.deps)
}

func TestGlobalCredentialBindingRequiresOpenedMainConversation(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newGlobalBindingFixture(t, provider)
			binding, err := f.observe(t)
			if err != nil {
				t.Fatal(err)
			}
			if binding.ProcessPID != 101 || binding.ProcessStartedAt != 1700000001000 ||
				binding.CredentialHome != filepath.Join(f.home, "."+provider) ||
				binding.Session.SessionID != globalBindingTestID || binding.Session.SourcePath != f.transcript ||
				binding.Session.Source != DiscoverySourceProcessTree || binding.Session.Freshness != BindingFresh {
				t.Fatalf("unexpected binding: %#v", binding)
			}
			if f.reads[100] < 2 || f.reads[101] < 3 {
				t.Fatalf("process generations were not reobserved: %v", f.reads)
			}
			data, err := json.Marshal(binding)
			if err != nil || string(data) != "{}" {
				t.Fatalf("private binding leaked into JSON: %s, %v", data, err)
			}

			process := f.processes[101]
			process.files = nil
			if provider == "codex" {
				process.argv = []string{provider, "resume", globalBindingTestID}
			} else {
				process.argv = []string{provider, "--resume", globalBindingTestID}
			}
			f.processes[101] = process
			if _, err := f.observe(t); err == nil || !strings.Contains(err.Error(), "argv alone") {
				t.Fatalf("argv-only session was accepted: %v", err)
			}
		})
	}
}

func TestGlobalCredentialBindingNodeLauncherAndSubagentFiles(t *testing.T) {
	f := newGlobalBindingFixture(t, "codex")
	child := f.processes[101]
	child.pid, child.parent = 102, 101
	subagent := f.writeTranscript(t, globalBindingOtherID, f.workDir, true)
	child.files = append(child.files, bindingTestFile(t, subagent), bindingTestFile(t, f.transcript))
	f.processes[102] = child
	f.processes[101] = globalBindingProcess{pid: 101, parent: 100, started: 1700000000001,
		executable: "/usr/bin/node", argv: []string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"},
		children: []int{102}, environment: []string{"HOME=" + f.home}, cwd: f.workDir}
	binding, err := f.observe(t)
	if err != nil || binding.ProcessPID != 102 || binding.Session.SessionID != globalBindingTestID {
		t.Fatalf("native child owning one main rollout was not accepted: %#v, %v", binding, err)
	}
}

func TestGlobalCredentialBindingRejectsAmbiguousOrChangedProcesses(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, *globalBindingFixture)
	}{
		{"unrelated child", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.parent = 999
			f.processes[101] = p
		}},
		{"second main provider lineage", func(t *testing.T, f *globalBindingFixture) {
			root, other := f.processes[100], f.processes[101]
			root.children = append(root.children, 102)
			other.pid = 102
			f.processes[100], f.processes[102] = root, other
		}},
		{"multiple main transcripts", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.files = append(p.files, bindingTestFile(t, f.writeTranscript(t, globalBindingOtherID, f.workDir, false)))
			f.processes[101] = p
		}},
		{"only subagent transcript", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.files = []globalBindingFile{bindingTestFile(t, f.writeTranscript(t, globalBindingOtherID, f.workDir, true))}
			f.processes[101] = p
		}},
		{"different process workspace", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.cwd = f.home
			f.processes[101] = p
		}},
		{"different transcript workspace", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.files = []globalBindingFile{bindingTestFile(t, f.writeTranscript(t, globalBindingTestID, f.home, false))}
			f.processes[101] = p
		}},
		{"resume ID disagrees", func(t *testing.T, f *globalBindingFixture) {
			p := f.processes[101]
			p.argv = []string{"codex", "resume", globalBindingOtherID}
			f.processes[101] = p
		}},
		{"path replaced while descriptor stays open", func(t *testing.T, f *globalBindingFixture) {
			if err := os.Rename(f.transcript, f.transcript+".original"); err != nil {
				t.Fatal(err)
			}
			f.writeTranscript(t, globalBindingTestID, f.workDir, false)
		}},
		{"PID reused before detailed read", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				p, err := read(ctx, pid, inspect)
				if pid == 101 && inspect {
					p.started++
				}
				return p, err
			}
		}},
		{"pane generation replaced before final proof", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				p, err := read(ctx, pid, inspect)
				if pid == 100 && f.reads[pid] > 1 {
					p.started++
				}
				return p, err
			}
		}},
		{"new sibling appears before final proof", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				p, err := read(ctx, pid, inspect)
				if pid == 100 && f.reads[pid] > 1 {
					p.children = []int{101, 102}
				}
				return p, err
			}
		}},
		{"environment changes before final proof", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				p, err := read(ctx, pid, inspect)
				if pid == 101 && inspect && f.reads[pid] > 2 {
					p.environment = append(p.environment, "OPENAI_API_KEY=must-not-be-logged")
				}
				return p, err
			}
		}},
		{"transcript closes before final proof", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				p, err := read(ctx, pid, inspect)
				if pid == 101 && inspect && f.reads[pid] > 2 {
					p.files = nil
				}
				return p, err
			}
		}},
		{"cannot inspect child", func(t *testing.T, f *globalBindingFixture) {
			read := f.deps.process
			f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
				if pid == 101 {
					return globalBindingProcess{}, errors.New("opaque process detail must-not-be-logged")
				}
				return read(ctx, pid, inspect)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalBindingFixture(t, "codex")
			tc.edit(t, f)
			if _, err := f.observe(t); err == nil {
				t.Fatal("unproven native binding was accepted")
			} else if strings.Contains(err.Error(), "must-not-be-logged") || strings.Contains(err.Error(), f.home) {
				t.Fatalf("private evidence leaked into diagnostic: %v", err)
			}
		})
	}
}

func TestGlobalCredentialBindingRejectsAccountOverrides(t *testing.T) {
	cases := []struct {
		name, provider, env string
		args                []string
	}{
		{name: "Claude API key", provider: "claude", env: "ANTHROPIC_API_KEY=must-not-be-logged"},
		{name: "Claude OAuth token", provider: "claude", env: "CLAUDE_CODE_OAUTH_TOKEN=must-not-be-logged"},
		{name: "Claude AWS routing", provider: "claude", env: "CLAUDE_CODE_USE_BEDROCK=1"},
		{name: "Claude remote settings", provider: "claude", args: []string{"--settings", "must-not-be-logged"}},
		{name: "Claude settings sources", provider: "claude", args: []string{"--setting-sources=user"}},
		{name: "Codex API key", provider: "codex", env: "OPENAI_API_KEY=must-not-be-logged"},
		{name: "Codex base URL", provider: "codex", env: "CHATGPT_BASE_URL=must-not-be-logged"},
		{name: "Codex auth JSON", provider: "codex", env: "CODEX_AUTH_JSON=must-not-be-logged"},
		{name: "CAAM profile", provider: "codex", env: "CAAM_PROFILE=must-not-be-logged"},
		{name: "Codex profile", provider: "codex", args: []string{"-pprivate"}},
		{name: "Codex local provider", provider: "codex", args: []string{"--oss"}},
		{name: "Codex model provider", provider: "codex", args: []string{"-cmodel_provider=\"private\""}},
		{name: "Codex credential store", provider: "codex", args: []string{"--config=cli_auth_credentials_store=\"keyring\""}},
		{name: "Codex incomplete override", provider: "codex", args: []string{"-c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalBindingFixture(t, tc.provider)
			p := f.processes[101]
			if tc.env != "" {
				p.environment = append(p.environment, tc.env)
			}
			p.argv = append(p.argv, tc.args...)
			f.processes[101] = p
			if _, err := f.observe(t); err == nil {
				t.Fatal("account override was accepted")
			} else if strings.Contains(err.Error(), "must-not-be-logged") {
				t.Fatalf("credential value leaked: %v", err)
			}
		})
	}
}

func TestGlobalCredentialBindingDefaultHomesAndModelOverrides(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newGlobalBindingFixture(t, provider)
			p := f.processes[101]
			key := "CODEX_HOME"
			if provider == "claude" {
				key = "CLAUDE_CONFIG_DIR"
			} else {
				p.argv = append(p.argv, "-c", `model_reasoning_effort="high"`, "--config=sandbox_mode=\"workspace-write\"")
			}
			p.environment = append(p.environment, key+"="+filepath.Join(f.home, "."+provider))
			f.processes[101] = p
			if _, err := f.observe(t); err != nil {
				t.Fatalf("explicit default home or model override was rejected: %v", err)
			}
			p.environment[len(p.environment)-1] = key + "=" + f.workDir
			f.processes[101] = p
			if _, err := f.observe(t); err == nil {
				t.Fatal("isolated provider home was accepted")
			}
		})
	}
}

func TestGlobalCredentialBindingRejectsSharedExternalCredentialHome(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newGlobalBindingFixture(t, provider)
			credentials := filepath.Join(f.home, "."+provider)
			shared := filepath.Join(filepath.Dir(f.home), "shared-credentials")
			if err := os.Rename(credentials, shared); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(shared, credentials); err != nil {
				t.Fatal(err)
			}
			if _, err := f.observe(t); err == nil || !strings.Contains(err.Error(), "outside the global home") {
				t.Fatalf("shared external credential home was accepted: %v", err)
			}
			if len(f.reads) != 0 {
				t.Fatalf("unsupported shared credentials reached process discovery: %v", f.reads)
			}
		})
	}
}

func TestGlobalCredentialBindingRejectsNestedHomeCredentialAlias(t *testing.T) {
	f := newGlobalBindingFixture(t, "codex")
	credentials := filepath.Join(f.home, ".codex")
	nestedHome := filepath.Join(f.home, "nested-user")
	if err := os.MkdirAll(nestedHome, 0700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(nestedHome, ".codex")
	if err := os.Rename(credentials, shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := f.observe(t); err == nil || !strings.Contains(err.Error(), "exclusive global account scope") {
		t.Fatalf("nested HOME credential alias was accepted: %v", err)
	}
	if len(f.reads) != 0 {
		t.Fatalf("ambiguous lock scope reached process discovery: %v", f.reads)
	}
}

func TestValidateGlobalCredentialDefaultLaunchTemplates(t *testing.T) {
	templates := config.DefaultAgentTemplates()
	for _, tc := range []struct{ agentType, template string }{{"cc", templates.Claude}, {"cod", templates.Codex}} {
		for _, prompt := range []string{"", "/work/persona.md"} {
			t.Run(tc.agentType+"/"+filepath.Base(prompt), func(t *testing.T) {
				command, err := config.GenerateAgentCommand(tc.template, config.AgentTemplateVars{
					AgentType: tc.agentType, ProjectDir: "/work", SessionName: "swarm", SystemPromptFile: prompt,
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := ValidateGlobalCredentialLaunchCommand(tc.agentType, command, ResumeLaunchOptions{SystemPromptFile: prompt}); err != nil {
					t.Fatalf("default native launch was rejected: %v", err)
				}
			})
		}
	}
}

func TestValidateGlobalCredentialLaunchCommand(t *testing.T) {
	cases := []struct {
		name, provider, command, prompt string
		wantError                       bool
	}{
		{name: "Claude direct model and prompt", provider: "claude", command: `'/opt/agents/claude' --model 'custom-model' --system-prompt-file '/work/persona.md'`},
		{name: "Claude memory wrapper", provider: "cc", command: `systemd-run --user --scope -q -p MemoryMax=4096M claude --model example`},
		{name: "Codex reasoning", provider: "cod", command: `codex --model example -c 'model_reasoning_effort="high"'`},
		{name: "Codex persona", provider: "codex", command: `CODEX_SYSTEM_PROMPT="$(cat '/work/persona.md')" codex --model example`, prompt: "/work/persona.md"},
		{name: "Codex repeated native resume", provider: "codex", command: `codex resume 'old-id' --model example`},
		{name: "leading API key", provider: "codex", command: `OPENAI_API_KEY='must-not-be-logged' codex`, wantError: true},
		{name: "leading safe-looking env", provider: "claude", command: `MODE=private claude`, wantError: true},
		{name: "provider override in metadata", provider: "codex", command: `codex -c 'model_provider="private"'`, wantError: true},
		{name: "profile override in metadata", provider: "codex", command: `codex -p private`, wantError: true},
		{name: "different settings file", provider: "claude", command: `claude --settings '/private/config.json'`, wantError: true},
		{name: "future provider flag", provider: "codex", command: `codex --model-provider=private`, wantError: true},
		{name: "different workspace", provider: "codex", command: `codex -C '/other/worktree'`, wantError: true},
		{name: "persona path mismatch", provider: "codex", command: `CODEX_SYSTEM_PROMPT="$(cat '/private/other.md')" codex`, prompt: "/work/persona.md", wantError: true},
		{name: "opaque shell wrapper", provider: "claude", command: `sh -c 'claude --model example'`, wantError: true},
		{name: "arbitrary expansion", provider: "claude", command: `claude --model "$(private-command)"`, wantError: true},
		{name: "unsupported provider", provider: "gemini", command: `gemini`, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGlobalCredentialLaunchCommand(tc.provider, tc.command, ResumeLaunchOptions{SystemPromptFile: tc.prompt})
			if (err != nil) != tc.wantError {
				t.Fatalf("validation error = %v, want error %v", err, tc.wantError)
			}
			if err != nil && (strings.Contains(err.Error(), "must-not-be-logged") || strings.Contains(err.Error(), "private-command")) {
				t.Fatalf("recorded command data leaked into diagnostic: %v", err)
			}
			if tc.name == "leading API key" && !strings.Contains(err.Error(), "OPENAI_API_KEY") {
				t.Fatalf("assignment diagnostic omitted the variable name: %v", err)
			}
		})
	}
}

func TestGlobalCredentialBindingConfigSources(t *testing.T) {
	cases := []struct {
		name, provider, source, data string
	}{
		{"Claude user helper", "claude", "user", `{"apiKeyHelper":"must-not-be-logged"}`},
		{"Claude project token", "claude", "project", `{"env":{"ANTHROPIC_AUTH_TOKEN":"must-not-be-logged"}}`},
		{"Claude local token", "claude", "local", `{"env":{"ANTHROPIC_API_KEY":"must-not-be-logged"}}`},
		{"Claude ancestor routing", "claude", "ancestor", `{"env":{"ANTHROPIC_BASE_URL":"must-not-be-logged"}}`},
		{"Claude managed login", "claude", "managed", `{"forceLoginOrgUUID":"must-not-be-logged"}`},
		{"Claude remote policy helper", "claude", "remote", `{"policyHelper":"must-not-be-logged"}`},
		{"Claude old global key", "claude", "global", `{"primaryApiKey":"must-not-be-logged"}`},
		{"Claude malformed env type", "claude", "user", `{"env":{"ANTHROPIC_API_KEY":{"secret":"must-not-be-logged"}}}`},
		{"Claude malformed JSON", "claude", "user", `{ "env": must-not-be-logged`},
		{"Codex model provider", "codex", "user", `model_provider = "private"`},
		{"Codex project keyring", "codex", "project", `cli_auth_credentials_store = "keyring"`},
		{"Codex managed login URL", "codex", "managed", `chatgpt_base_url = "must-not-be-logged"`},
		{"Codex dormant profile", "codex", "user", "[profiles.private]\nmodel_provider = \"private\""},
		{"Codex malformed provider type", "codex", "user", `model_provider = ["must-not-be-logged"]`},
		{"Codex malformed store type", "codex", "user", `cli_auth_credentials_store = { key = "must-not-be-logged" }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalBindingFixture(t, tc.provider)
			base, filename := f.home, "settings.json"
			if tc.provider == "codex" {
				filename = "config.toml"
			}
			if tc.source == "project" || tc.source == "local" {
				base = f.workDir
			} else if tc.source == "ancestor" {
				base = filepath.Dir(f.workDir)
			}
			path := filepath.Join(base, "."+tc.provider, filename)
			switch tc.source {
			case "local":
				path = filepath.Join(base, ".claude", "settings.local.json")
			case "remote":
				path = filepath.Join(base, ".claude", "remote-settings.json")
			case "global":
				path = filepath.Join(base, ".claude.json")
			case "managed":
				path = filepath.Join(base, "managed-configuration")
				f.deps.systemPaths = func(string) ([]string, error) { return []string{path}, nil }
			}
			writeGlobalBindingTestFile(t, path, tc.data)
			if _, err := f.observe(t); err == nil {
				t.Fatal("unproven native configuration was accepted")
			} else if strings.Contains(err.Error(), "must-not-be-logged") || strings.Contains(err.Error(), f.home) {
				t.Fatalf("native configuration leaked into diagnostic: %v", err)
			}
		})
	}
}

func TestGlobalCredentialBindingClaudeLinkedWorktreeLocalSettings(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required to exercise native linked-worktree settings")
	}
	f := newGlobalBindingFixture(t, "claude")
	main := filepath.Join(filepath.Dir(f.workDir), "main-checkout")
	for _, args := range [][]string{
		{"init", "-b", "main", main},
		{"-C", main, "-c", "user.name=NTM test", "-c", "user.email=ntm-test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture"},
		{"-C", main, "worktree", "add", "--detach", f.workDir},
	} {
		command := exec.CommandContext(t.Context(), "git", args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("prepare linked worktree: %v: %s", err, output)
		}
	}
	if _, err := f.observe(t); err != nil {
		t.Fatalf("ordinary linked worktree was rejected: %v", err)
	}
	writeGlobalBindingTestFile(t, filepath.Join(main, ".claude", "settings.local.json"), `{"env":{"ANTHROPIC_AUTH_TOKEN":"must-not-be-logged"}}`)
	if _, err := f.observe(t); err == nil {
		t.Fatal("linked worktree ignored main-checkout local account override")
	}
}

func TestGlobalCredentialBindingTranscriptIdentity(t *testing.T) {
	cases := []struct {
		name, provider string
		edit           func(string, string) string
	}{
		{"Codex other provider", "codex", func(data, _ string) string {
			return strings.ReplaceAll(data, `"model_provider":"openai"`, `"model_provider":"private"`)
		}},
		{"Codex unknown provider", "codex", func(data, _ string) string {
			return strings.ReplaceAll(data, `"model_provider":"openai"`, `"model_provider":""`)
		}},
		{"Codex noninteractive source", "codex", func(data, _ string) string { return strings.ReplaceAll(data, `"source":"cli"`, `"source":"exec"`) }},
		{"Codex unknown source", "codex", func(data, _ string) string { return strings.ReplaceAll(data, `"source":"cli"`, `"source":null`) }},
		{"Codex filename mismatch", "codex", func(data, _ string) string {
			return strings.ReplaceAll(data, globalBindingTestID, globalBindingOtherID)
		}},
		{"Claude filename mismatch", "claude", func(data, _ string) string {
			return strings.ReplaceAll(data, globalBindingTestID, globalBindingOtherID)
		}},
		{"Claude no main marker", "claude", func(data, _ string) string {
			return strings.ReplaceAll(data, `"isSidechain":false`, `"isSidechain":null`)
		}},
		{"Claude conflicting later identity", "claude", func(data, _ string) string {
			return data + strings.ReplaceAll(data, globalBindingTestID, globalBindingOtherID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGlobalBindingFixture(t, tc.provider)
			data, err := os.ReadFile(f.transcript)
			if err != nil {
				t.Fatal(err)
			}
			writeGlobalBindingTestFile(t, f.transcript, tc.edit(string(data), f.workDir))
			if _, err := f.observe(t); err == nil {
				t.Fatal("ambiguous transcript header was accepted")
			}
		})
	}
}

func TestGlobalCredentialBindingConfigIsRechecked(t *testing.T) {
	f := newGlobalBindingFixture(t, "claude")
	path := filepath.Join(f.home, ".claude", "settings.json")
	writeGlobalBindingTestFile(t, path, `{"model":"custom-model","permissions":{"allow":["Read"]}}`)
	read := f.deps.process
	f.deps.process = func(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
		if pid == 101 && inspect && f.reads[pid] > 1 {
			writeGlobalBindingTestFile(t, path, `{"apiKeyHelper":"private-helper"}`)
		}
		return read(ctx, pid, inspect)
	}
	if _, err := f.observe(t); err == nil {
		t.Fatal("configuration change during process observation was ignored")
	}
}

func TestGlobalCredentialBindingCancellationAndBounds(t *testing.T) {
	f := newGlobalBindingFixture(t, "codex")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := observeGlobalCredentialBinding(ctx, "codex", f.workDir, 100, f.deps); !errors.Is(err, context.Canceled) || len(f.reads) != 0 {
		t.Fatalf("canceled observation read process state: %v, %v", err, f.reads)
	}
	if _, err := ObserveGlobalCredentialBinding(nil, "codex", f.workDir, 100); err == nil {
		t.Fatal("nil context was accepted")
	}
	f.deps.platform = "darwin"
	if _, err := f.observe(t); err == nil {
		t.Fatal("unsupported platform was accepted")
	}
	f.deps.platform = "linux"
	p := f.processes[100]
	p.children = make([]int, processTreeFanout+1)
	f.processes[100] = p
	if _, err := f.observe(t); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("oversized process tree was accepted: %v", err)
	}
	p.children = []int{100}
	f.processes[100] = p
	if _, err := f.observe(t); err == nil {
		t.Fatal("cyclic process tree was accepted")
	}
}

func TestGlobalBindingProviderHelper(t *testing.T) {
	if os.Getenv("NTM_GLOBAL_BINDING_TEST_HELPER") != "1" {
		return
	}
	transcript, err := os.OpenFile(os.Getenv("NTM_GLOBAL_BINDING_TEST_TRANSCRIPT"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer transcript.Close()
	if _, err := fmt.Fprintln(os.Stdout, "native-transcript-open"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalCredentialBindingRealProcessAndOpenDescriptor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("global credential process observation requires Linux procfs")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newGlobalBindingFixture(t, provider)
			original, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			input, err := os.Open(original)
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			binary := filepath.Join(t.TempDir(), provider)
			output, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(output, input)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatalf("copy helper: %v, %v", copyErr, closeErr)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestGlobalBindingProviderHelper$")
			command.Dir = f.workDir
			command.Env = []string{"HOME=" + f.home, "NTM_GLOBAL_BINDING_TEST_HELPER=1", "NTM_GLOBAL_BINDING_TEST_TRANSCRIPT=" + f.transcript}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = stdin.Close()
				if err := command.Wait(); err != nil {
					t.Errorf("provider helper exited unsuccessfully: %v", err)
				}
			}()
			ready, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || ready != "native-transcript-open\n" {
				t.Fatalf("provider helper did not open transcript: %q, %v", ready, err)
			}
			f.deps.process = readGlobalBindingProcess
			binding, err := observeGlobalCredentialBinding(ctx, provider, f.workDir, command.Process.Pid, f.deps)
			if err != nil {
				_, processErr := readGlobalBindingProcess(ctx, command.Process.Pid, false)
				t.Fatalf("native binding: %v; process observation: %v", err, processErr)
			}
			if binding.ProcessPID != command.Process.Pid || binding.ProcessStartedAt <= 0 || binding.Session.SourcePath != f.transcript || binding.Session.SessionID != globalBindingTestID {
				t.Fatalf("wrong live native binding: %#v", binding)
			}
			if err := os.Rename(f.transcript, f.transcript+".still-open"); err != nil {
				t.Fatal(err)
			}
			f.writeTranscript(t, globalBindingTestID, f.workDir, false)
			if _, err := observeGlobalCredentialBinding(ctx, provider, f.workDir, command.Process.Pid, f.deps); err == nil {
				t.Fatal("a new pathname file was mistaken for the provider's old open descriptor")
			}
		})
	}
}
