package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestWave16AgentFilterFull11HarnessParityAcrossCLICommands(t *testing.T) {
	t.Parallel()

	allFlags := []string{
		"cc", "cod", "gmi", "agy", "grok",
		"cursor", "windsurf", "aider", "oc", "omp", "ollama",
	}

	for name, builder := range map[string]func() *cobra.Command{
		"copy":   newCopyCmd,
		"save":   newSaveCmd,
		"grep":   newGrepCmd,
		"errors": newErrorsCmd,
	} {
		cmd := builder()
		for _, flagName := range allFlags {
			if cmd.Flags().Lookup(flagName) == nil {
				t.Fatalf("command %q missing --%s harness filter flag", name, flagName)
			}
		}
	}

	cases := []struct {
		filter    AgentFilter
		agentType tmux.AgentType
		wantType  string
	}{
		{AgentFilter{Claude: true}, tmux.AgentClaude, "cc"},
		{AgentFilter{Codex: true}, tmux.AgentCodex, "cod"},
		{AgentFilter{Gemini: true}, tmux.AgentGemini, "gmi"},
		{AgentFilter{Antigravity: true}, tmux.AgentAntigravity, "agy"},
		{AgentFilter{Grok: true}, tmux.AgentGrok, "grok"},
		{AgentFilter{Cursor: true}, tmux.AgentCursor, "cursor"},
		{AgentFilter{Windsurf: true}, tmux.AgentWindsurf, "windsurf"},
		{AgentFilter{Aider: true}, tmux.AgentAider, "aider"},
		{AgentFilter{Opencode: true}, tmux.AgentOpencode, "oc"},
		{AgentFilter{Omp: true}, tmux.AgentOmp, "omp"},
		{AgentFilter{Ollama: true}, tmux.AgentOllama, "ollama"},
	}

	for _, tc := range cases {
		if tc.filter.IsEmpty() {
			t.Fatalf("expected filter %+v to be non-empty", tc.filter)
		}
		if !tc.filter.Matches(tc.agentType) {
			t.Fatalf("expected filter %+v to match %q", tc.filter, tc.agentType)
		}
		if tc.filter.Matches(tmux.AgentUser) {
			t.Fatalf("expected filter %+v not to match user pane", tc.filter)
		}
		selected := tc.filter.SelectedTypes()
		if len(selected) != 1 || selected[0] != tc.wantType {
			t.Fatalf("SelectedTypes() = %v, want [%s]", selected, tc.wantType)
		}
	}
}

func TestWave16RobotSubprocessContractsAndLiveTmuxGrepExtract(t *testing.T) {
	t.Parallel()
	homeDir := t.TempDir()
	configHome := filepath.Join(homeDir, ".config")
	dataHome := filepath.Join(homeDir, ".local", "share")
	repoDir := filepath.Join(homeDir, "repo")
	ntmCfgDir := filepath.Join(configHome, "ntm")
	if err := os.MkdirAll(filepath.Join(ntmCfgDir, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm"), 0o755); err != nil {
		t.Fatalf("mkdir repo .ntm: %v", err)
	}

	// Write valid config + agent plugin + policy
	if err := os.WriteFile(filepath.Join(ntmCfgDir, "config.toml"), []byte("projects_base = \""+repoDir+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	agentTOML := "[agent]\nname = \"wave16_helper\"\nalias = \"w16\"\ncommand = \"echo\"\ndescription = \"Wave 16 test plugin\"\n"
	if err := os.WriteFile(filepath.Join(ntmCfgDir, "agents", "wave16.toml"), []byte(agentTOML), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	policyYAML := "version: 1\nblocked:\n  - pattern: \"rm -rf /\"\n    reason: \"forbidden\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "policy.yaml"), []byte(policyYAML), 0o644); err != nil {
		t.Fatalf("write policy.yaml: %v", err)
	}

	runRobotSubprocess := func(args ...string) ([]byte, int) {
		t.Helper()
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
		cmd.Dir = repoDir
		cmd.Env = envWithOverrides(os.Environ(),
			"HOME="+homeDir,
			"XDG_CONFIG_HOME="+configHome,
			"XDG_DATA_HOME="+dataHome,
			"NTM_NO_COLOR=1",
			"NTM_CONFIG="+filepath.Join(ntmCfgDir, "config.toml"),
			"NTM_LOCAL_VRAM_GB=16",
			"NTM_ROBOT_FORMAT=json",
			"NTM_OUTPUT_FORMAT=",
			"TOON_DEFAULT_FORMAT=",
			"NTM_ROBOT_VERBOSITY=",
			"NTM_ROBOT_CONTRACT_ARGS="+string(rawArgs),
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exitCode := 0
		if runErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) {
				t.Fatalf("run subprocess %v: %v (stderr=%s)", args, runErr, stderr.String())
			}
			exitCode = exitErr.ExitCode()
		}
		return stdout.Bytes(), exitCode
	}

	// 1. --robot-validate
	valRaw, valCode := runRobotSubprocess("--robot-validate", "--project="+repoDir)
	if valCode != 0 {
		t.Fatalf("--robot-validate exit=%d, out=%s", valCode, string(valRaw))
	}
	var valOut robot.ValidationOutput
	if err := json.Unmarshal(valRaw, &valOut); err != nil || !valOut.Success || !valOut.Valid {
		t.Fatalf("unexpected --robot-validate output: %+v (%v, raw=%s)", valOut, err, string(valRaw))
	}

	// 2. --robot-plugins & `ntm plugins list --json`
	plugRaw, plugCode := runRobotSubprocess("--robot-plugins")
	if plugCode != 0 {
		t.Fatalf("--robot-plugins exit=%d, out=%s", plugCode, string(plugRaw))
	}
	var plugOut robot.PluginsOutput
	if err := json.Unmarshal(plugRaw, &plugOut); err != nil || !plugOut.Success || plugOut.TotalCount != 1 || plugOut.AgentPlugins[0].Name != "wave16_helper" {
		t.Fatalf("unexpected --robot-plugins output: %+v (%v, raw=%s)", plugOut, err, string(plugRaw))
	}

	cliPlugRaw, cliPlugCode := runRobotSubprocess("plugins", "list", "--json")
	if cliPlugCode != 0 {
		t.Fatalf("plugins list --json exit=%d, out=%s", cliPlugCode, string(cliPlugRaw))
	}
	var cliPlugOut robot.PluginsOutput
	if err := json.Unmarshal(cliPlugRaw, &cliPlugOut); err != nil || !cliPlugOut.Success || cliPlugOut.TotalCount != 1 {
		t.Fatalf("unexpected plugins list --json output: %+v (%v)", cliPlugOut, err)
	}

	// 3. --robot-models (VRAM advisor)
	modRaw, modCode := runRobotSubprocess("--robot-models")
	if modCode != 0 {
		t.Fatalf("--robot-models exit=%d, out=%s", modCode, string(modRaw))
	}
	var modOut robot.LocalModelsOutput
	if err := json.Unmarshal(modRaw, &modOut); err != nil || !modOut.Success || modOut.DetectedVRAMGB != 16 || len(modOut.Recommendations) == 0 {
		t.Fatalf("unexpected --robot-models output: %+v (%v, raw=%s)", modOut, err, string(modRaw))
	}

	// 4. Live isolated-tmux exercise of --robot-grep and --robot-extract
	testutil.RequireTmuxThrottled(t)
	sessName := fmt.Sprintf("ntm_wave16_%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessName, repoDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessName) })

	panes, err := tmux.GetPanes(sessName)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: %v (%d)", err, len(panes))
	}
	pane := panes[0]
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", pane.ID, "-T", sessName+"__agy_1"); err != nil {
		t.Fatalf("select-pane title: %v", err)
	}

	sampleFile := filepath.Join(repoDir, "pane_sample.txt")
	sampleContent := "WAVE16_SIGNAL_READY\n```go\n// internal/demo/wave16.go\npackage demo\n\nfunc Wave16Marker() int { return 42 }\n```\n"
	if err := os.WriteFile(sampleFile, []byte(sampleContent), 0o644); err != nil {
		t.Fatalf("write sampleFile: %v", err)
	}
	if err := tmux.SendKeys(pane.ID, "cat "+sampleFile, true); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Poll briefly until cat output renders in tmux scrollback
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := tmux.CapturePaneOutput(pane.ID, 50)
		if strings.Contains(out, "Wave16Marker") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	grepRaw, grepCode := runRobotSubprocess("--robot-grep=WAVE16_SIGNAL_READY", "--grep-session="+sessName, "--type=agy", "--grep-context=1")
	if grepCode != 0 {
		t.Fatalf("--robot-grep exit=%d, out=%s", grepCode, string(grepRaw))
	}
	var grepOut robot.GrepOutput
	if err := json.Unmarshal(grepRaw, &grepOut); err != nil || !grepOut.Success || grepOut.MatchCount == 0 {
		t.Fatalf("unexpected --robot-grep output: %+v (%v, raw=%s)", grepOut, err, string(grepRaw))
	}

	extRaw, extCode := runRobotSubprocess("--robot-extract="+sessName, "--extract-lang=go", "--lines=100")
	if extCode != 0 {
		t.Fatalf("--robot-extract exit=%d, out=%s", extCode, string(extRaw))
	}
	var extOut robot.ExtractOutput
	if err := json.Unmarshal(extRaw, &extOut); err != nil || !extOut.Success || extOut.TotalFound == 0 || !strings.Contains(extOut.Blocks[0].Content, "Wave16Marker") {
		t.Fatalf("unexpected --robot-extract output: %+v (%v, raw=%s)", extOut, err, string(extRaw))
	}
}
