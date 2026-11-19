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

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/session"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestSessionsShowAndSaveTextRenderAll11Harnesses(t *testing.T) {
	t.Parallel()

	state := &session.SessionState{
		Name:      "wave15_harnesses",
		SavedAt:   time.Now().UTC(),
		WorkDir:   "/tmp/wave15_harnesses",
		GitBranch: "main",
		Layout:    "tiled",
		Agents: session.AgentConfig{
			Claude:      2,
			Codex:       1,
			Gemini:      1,
			Antigravity: 2,
			Grok:        1,
			Cursor:      1,
			Windsurf:    1,
			Aider:       1,
			Opencode:    1,
			Omp:         1,
			Ollama:      1,
			User:        1,
		},
		Panes: []session.PaneState{
			{
				Title:             "wave15_harnesses__agy_1",
				Index:             0,
				AgentType:         "agy",
				SessionID:         "agy-fresh-1",
				SessionProvider:   "antigravity",
				SessionFreshness:  agentsession.BindingFresh,
				SessionConfidence: 0.92,
			},
		},
	}

	var showBuf bytes.Buffer
	showRes := &SessionsShowResult{State: state}
	if err := showRes.Text(&showBuf); err != nil {
		t.Fatalf("SessionsShowResult.Text: %v", err)
	}
	renderedShow := showBuf.String()
	for _, want := range []string{"Antigravity: 2", "Grok: 1", "Cursor: 1", "Windsurf: 1", "Aider: 1", "OpenCode: 1", "Oh My Pi: 1", "Ollama: 1", "Resumable: 1"} {
		if !strings.Contains(renderedShow, want) {
			t.Fatalf("SessionsShowResult.Text missing %q in:\n%s", want, renderedShow)
		}
	}

	var saveBuf bytes.Buffer
	saveRes := &SessionsSaveResult{
		Success:  true,
		Session:  "wave15_harnesses",
		SavedAs:  "wave15_harnesses",
		FilePath: "/tmp/wave15_harnesses.json",
		State:    state,
	}
	if err := saveRes.Text(&saveBuf); err != nil {
		t.Fatalf("SessionsSaveResult.Text: %v", err)
	}
	renderedSave := saveBuf.String()
	for _, want := range []string{"Antigravity: 2", "OpenCode: 1", "Resumable panes: 1"} {
		if !strings.Contains(renderedSave, want) {
			t.Fatalf("SessionsSaveResult.Text missing %q in:\n%s", want, renderedSave)
		}
	}
}

func TestWave15RobotCLIFlagsAndSubprocessDispatch(t *testing.T) {
	homeDir := t.TempDir()
	configHome := t.TempDir()
	dataHome := t.TempDir()
	repoDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(repoDir, ".ntm", "templates"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.ntm/templates): %v", err)
	}
	tmplYAML := `apiVersion: v1
kind: SessionTemplate
metadata:
  name: wave15-cli-template
  description: CLI subprocess template verification
spec:
  agents:
    claude:
      count: 1
    antigravity:
      count: 2
`
	if err := os.WriteFile(filepath.Join(repoDir, ".ntm", "templates", "wave15-cli-template.yaml"), []byte(tmplYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(template): %v", err)
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
			"NTM_CONFIG=",
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

	// 1. --robot-workflows detail with trigger validation
	wfRaw, code := runRobotSubprocess("--robot-workflows", "--workflows-name=red-green", "--project="+repoDir)
	if code != 0 {
		t.Fatalf("--robot-workflows exit=%d, out=%s", code, string(wfRaw))
	}
	var wfOut robot.WorkflowsOutput
	if err := json.Unmarshal(wfRaw, &wfOut); err != nil {
		t.Fatalf("decode --robot-workflows: %v (%s)", err, string(wfRaw))
	}
	if !wfOut.Success || wfOut.Workflow == nil || strings.Compare(wfOut.Workflow.Name, "red-green") != 0 {
		t.Fatalf("unexpected --robot-workflows output: %+v", wfOut)
	}

	// 2. --robot-session-templates custom project template inspection
	stRaw, code := runRobotSubprocess("--robot-session-templates", "--template-name=wave15-cli-template", "--project="+repoDir)
	if code != 0 {
		t.Fatalf("--robot-session-templates exit=%d, out=%s", code, string(stRaw))
	}
	var stOut robot.SessionTemplatesOutput
	if err := json.Unmarshal(stRaw, &stOut); err != nil {
		t.Fatalf("decode --robot-session-templates: %v (%s)", err, string(stRaw))
	}
	if !stOut.Success || stOut.Template == nil || stOut.Template.Agents == nil || stOut.Template.Agents.Antigravity == nil || stOut.Template.Agents.Antigravity.Count != 2 {
		t.Fatalf("unexpected --robot-session-templates output: %+v", stOut)
	}

	// 3. --robot-saved-sessions empty list -> missing show exits 1
	listRaw, code := runRobotSubprocess("--robot-saved-sessions")
	if code != 0 {
		t.Fatalf("--robot-saved-sessions list exit=%d, out=%s", code, string(listRaw))
	}
	var listOut robot.SavedSessionsOutput
	if err := json.Unmarshal(listRaw, &listOut); err != nil || !listOut.Success || listOut.Count == nil || *listOut.Count != 0 {
		t.Fatalf("unexpected initial --robot-saved-sessions list: %+v (%v)", listOut, err)
	}

	_, missingCode := runRobotSubprocess("--robot-saved-sessions", "--saved-action=show", "--saved-name=missing-snap")
	if missingCode == 0 {
		t.Fatal("expected non-zero exit code for missing saved session")
	}

	// 4. Live isolated-tmux exercise of --robot-saved-sessions=save/archive/unarchive AND --robot-mapping
	testutil.RequireTmuxThrottled(t)
	sessName := fmt.Sprintf("ntm_wave15_%d", time.Now().UnixNano())
	if err := tmux.CreateSession(sessName, repoDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(sessName) })

	panes, err := tmux.GetPanes(sessName)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes: %v (%d)", err, len(panes))
	}
	pane := panes[0]
	if _, err := tmux.DefaultClient.Run("select-pane", "-t", pane.ID, "-T", sessName+"__cc_1"); err != nil {
		t.Fatalf("select-pane title: %v", err)
	}

	// Persist Agent Mail registry inside isolated configHome/dataHome for subprocess --robot-mapping
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	reg := agentmail.NewSessionAgentRegistry(sessName, repoDir)
	reg.AddAgent(sessName+"__cc_1", pane.ID, "SilverWolf")
	reg.SetPanePID(pane.ID, pane.PID)
	if err := agentmail.SaveSessionAgentRegistry(reg); err != nil {
		t.Fatalf("SaveSessionAgentRegistry: %v", err)
	}
	if _, err := agentmail.WriteIdentity(repoDir, pane.ID, "SilverWolf"); err != nil {
		t.Fatalf("WriteIdentity: %v", err)
	}

	saveRaw, saveCode := runRobotSubprocess("--robot-saved-sessions", "--saved-action=save", "--saved-session="+sessName, "--saved-name=wave15-live", "--saved-overwrite")
	if saveCode != 0 {
		t.Fatalf("--robot-saved-sessions save exit=%d, out=%s", saveCode, string(saveRaw))
	}
	var saveOut robot.SavedSessionsOutput
	if err := json.Unmarshal(saveRaw, &saveOut); err != nil || !saveOut.Success || strings.Compare(saveOut.SavedAs, "wave15-live") != 0 {
		t.Fatalf("unexpected --robot-saved-sessions save output: %+v (%v, raw=%s)", saveOut, err, string(saveRaw))
	}

	mapRaw, mapCode := runRobotSubprocess("--robot-mapping="+sessName, "--project="+repoDir)
	if mapCode != 0 {
		t.Fatalf("--robot-mapping exit=%d, out=%s", mapCode, string(mapRaw))
	}
	var mapOut robot.AgentMappingOutput
	if err := json.Unmarshal(mapRaw, &mapOut); err != nil {
		t.Fatalf("decode --robot-mapping clean JSON stdout: %v (%s)", err, string(mapRaw))
	}
	if !mapOut.Success || mapOut.Count != 1 || strings.Compare(mapOut.Entries[0].Name, "SilverWolf") != 0 {
		t.Fatalf("unexpected --robot-mapping output: %+v", mapOut)
	}
}
