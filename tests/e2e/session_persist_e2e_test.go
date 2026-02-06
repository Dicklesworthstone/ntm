package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestSessionsSaveRestore_E2E(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmuxThrottled(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLogger(t, t.TempDir())

	projectsBase := t.TempDir()
	homeDir := t.TempDir()

	// Keep all session persistence artifacts out of the real home dir.
	// internal/session.StorageDir() uses os.UserHomeDir() => ~/.ntm/sessions.
	env := map[string]string{
		"HOME":              homeDir,
		"NTM_PROJECTS_BASE": projectsBase,
	}

	sessionName := fmt.Sprintf("e2e_sessions_persist_%d", time.Now().UnixNano())
	projectDir := filepath.Join(projectsBase, sessionName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("failed to create project directory: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContent := fmt.Sprintf(`
projects_base = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	t.Cleanup(func() {
		logger.LogSection("Teardown: Killing restored/original sessions (best-effort)")
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName).Run()
		_ = exec.Command(tmux.BinaryPath(), "kill-session", "-t", sessionName+"_restored").Run()
	})

	logger.LogSection("Step 1: Spawn session")
	_, err := runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "spawn", sessionName, "--json", "--cc=1", "--cod=1")
	if err != nil {
		t.Fatalf("spawn failed: %v", err)
	}
	time.Sleep(750 * time.Millisecond)

	testutil.AssertSessionExists(t, logger, sessionName)
	testutil.AssertPaneCountAtLeast(t, logger, sessionName, 3) // 1 user + 2 agents

	saveName := fmt.Sprintf("%s_save", sessionName)

	logger.LogSection("Step 2: sessions save")
	saveOut, err := runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "--json", "sessions", "save", sessionName, "--name", saveName, "--overwrite")
	if err != nil {
		t.Fatalf("sessions save failed: %v\nOutput: %s", err, string(saveOut))
	}

	var saveRes struct {
		Success  bool   `json:"success"`
		Session  string `json:"session"`
		SavedAs  string `json:"saved_as"`
		FilePath string `json:"file_path"`
		Error    string `json:"error,omitempty"`
		State    struct {
			Name   string `json:"name"`
			Work   string `json:"cwd"`
			Layout string `json:"layout"`
			Panes  []struct {
				Title string `json:"title"`
			} `json:"panes"`
		} `json:"state"`
	}
	if err := json.Unmarshal(saveOut, &saveRes); err != nil {
		t.Fatalf("failed to parse sessions save JSON: %v\nOutput: %s", err, string(saveOut))
	}
	if !saveRes.Success {
		t.Fatalf("sessions save failed: %s", saveRes.Error)
	}
	if saveRes.Session != sessionName {
		t.Fatalf("sessions save session=%q want %q", saveRes.Session, sessionName)
	}
	if saveRes.SavedAs != saveName {
		t.Fatalf("sessions save saved_as=%q want %q", saveRes.SavedAs, saveName)
	}
	if saveRes.FilePath == "" || !strings.Contains(saveRes.FilePath, filepath.Join(homeDir, ".ntm", "sessions")) {
		t.Fatalf("sessions save file_path=%q expected under HOME/.ntm/sessions", saveRes.FilePath)
	}
	if saveRes.State.Name != sessionName {
		t.Fatalf("sessions save state.name=%q want %q", saveRes.State.Name, sessionName)
	}
	if saveRes.State.Work == "" {
		t.Fatalf("sessions save state.cwd is empty")
	}
	if len(saveRes.State.Panes) < 3 {
		t.Fatalf("sessions save state.panes=%d want at least 3", len(saveRes.State.Panes))
	}

	logger.LogSection("Step 3: sessions list contains saved name")
	listOut, err := runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "--json", "sessions", "list")
	if err != nil {
		t.Fatalf("sessions list failed: %v\nOutput: %s", err, string(listOut))
	}
	var listRes struct {
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(listOut, &listRes); err != nil {
		t.Fatalf("failed to parse sessions list JSON: %v\nOutput: %s", err, string(listOut))
	}
	found := false
	for _, s := range listRes.Sessions {
		if s.Name == saveName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("sessions list missing saved name %q (count=%d)", saveName, listRes.Count)
	}

	logger.LogSection("Step 4: sessions show")
	showOut, err := runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "--json", "sessions", "show", saveName)
	if err != nil {
		t.Fatalf("sessions show failed: %v\nOutput: %s", err, string(showOut))
	}
	var showState struct {
		Name   string `json:"name"`
		Work   string `json:"cwd"`
		Layout string `json:"layout"`
	}
	if err := json.Unmarshal(showOut, &showState); err != nil {
		t.Fatalf("failed to parse sessions show JSON: %v\nOutput: %s", err, string(showOut))
	}
	if showState.Name != sessionName {
		t.Fatalf("sessions show name=%q want %q", showState.Name, sessionName)
	}
	if showState.Work == "" {
		t.Fatalf("sessions show cwd is empty")
	}

	logger.LogSection("Step 5: Kill original session")
	_, _ = runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "kill", "-f", sessionName)
	time.Sleep(500 * time.Millisecond)
	testutil.AssertSessionNotExists(t, logger, sessionName)

	logger.LogSection("Step 6: sessions restore as a new name")
	restoredName := sessionName + "_restored"
	restoreOut, err := runCmdWithEnvOverride(t, logger, projectDir, env, "ntm", "--config", configPath, "--json", "sessions", "restore", saveName, "--name", restoredName)
	if err != nil {
		t.Fatalf("sessions restore failed: %v\nOutput: %s", err, string(restoreOut))
	}

	var restoreRes struct {
		Success    bool   `json:"success"`
		SavedName  string `json:"saved_name"`
		RestoredAs string `json:"restored_as"`
		Error      string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(restoreOut, &restoreRes); err != nil {
		t.Fatalf("failed to parse sessions restore JSON: %v\nOutput: %s", err, string(restoreOut))
	}
	if !restoreRes.Success {
		t.Fatalf("sessions restore returned success=false: %s", restoreRes.Error)
	}
	if restoreRes.SavedName != saveName {
		t.Fatalf("sessions restore saved_name=%q want %q", restoreRes.SavedName, saveName)
	}
	if restoreRes.RestoredAs != restoredName {
		t.Fatalf("sessions restore restored_as=%q want %q", restoreRes.RestoredAs, restoredName)
	}

	time.Sleep(750 * time.Millisecond)
	testutil.AssertSessionExists(t, logger, restoredName)
	testutil.AssertPaneCountAtLeast(t, logger, restoredName, 3)
}

func runCmdWithEnvOverride(t *testing.T, logger *testutil.TestLogger, dir string, overrides map[string]string, name string, args ...string) ([]byte, error) {
	t.Helper()
	if logger != nil {
		logger.Log("EXEC: %s %s", name, strings.Join(args, " "))
		logger.Log("DIR: %s", dir)
		for k, v := range overrides {
			logger.Log("ENV: %s=%s", k, v)
		}
	}

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = envWithOverrides(os.Environ(), overrides)

	out, err := cmd.CombinedOutput()
	if logger != nil {
		outStr := string(out)
		if len(outStr) > 2000 {
			outStr = outStr[:2000] + "\n... (truncated)"
		}
		if outStr != "" {
			logger.Log("OUTPUT:\n%s", outStr)
		}
		if err != nil {
			logger.Log("EXIT: error: %v", err)
		} else {
			logger.Log("EXIT: success (exit 0)")
		}
	}
	return out, err
}

func envWithOverrides(base []string, overrides map[string]string) []string {
	// Avoid duplicate keys (behavior differs across platforms).
	out := make([]string, 0, len(base)+len(overrides))
	skip := map[string]bool{}
	for k := range overrides {
		skip[k+"="] = true
	}
	for _, e := range base {
		keep := true
		for p := range skip {
			if strings.HasPrefix(e, p) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, e)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
