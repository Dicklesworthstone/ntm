package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

func TestE2EAgentMailReservationFlow(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)
	requireBr(t)
	client := requireAgentMail(t)
	ensureBDShim(t)

	session := fmt.Sprintf("am_reserve_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}

	configPath := writeTestConfig(t, projectsBase)

	runCmd(t, projectDir, "br", "init")
	beadID := createBead(t, projectDir, "Update internal/cli/send.go")
	runCmd(t, projectDir, "br", "sync", "--flush-only")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := client.EnsureProject(ctx, projectDir)
	cancel()
	if err != nil {
		t.Fatalf("ensure Agent Mail project: %v", err)
	}

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", session).Run()
	})

	runCmd(t, projectDir, "ntm", "--config", configPath, "spawn", session, "--cc=1")
	time.Sleep(500 * time.Millisecond)

	assignOut := runCmd(t, projectDir, "ntm", "--config", configPath, "assign", session, "--beads", beadID, "--auto", "--verbose")
	if strings.Contains(string(assignOut), "Agent Mail not available") {
		t.Fatalf("Agent Mail reported unavailable during assignment")
	}

	testutil.AssertEventually(t, testutil.NewTestLoggerStdout(t), 10*time.Second, 250*time.Millisecond,
		"reservation appears in Agent Mail", func() bool {
			reservations := listReservations(t, client, projectDir)
			return hasReservation(reservations, "internal/cli/send.go")
		})

	runCmd(t, projectDir, "br", "close", beadID, "--reason", "done", "--json")
	runCmd(t, projectDir, "ntm", "--config", configPath, "assign", session, "--clear", beadID, "--force", "--verbose")

	testutil.AssertEventually(t, testutil.NewTestLoggerStdout(t), 10*time.Second, 250*time.Millisecond,
		"reservation released in Agent Mail", func() bool {
			reservations := listReservations(t, client, projectDir)
			return !hasReservation(reservations, "internal/cli/send.go")
		})
}

func TestE2EAgentMailReservationConflict(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireTmux(t)
	testutil.RequireNTMBinary(t)
	requireBr(t)
	client := requireAgentMail(t)
	ensureBDShim(t)

	session := fmt.Sprintf("am_conflict_%d", time.Now().UnixNano())
	projectsBase := t.TempDir()
	projectDir := filepath.Join(projectsBase, session)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("create project dir: %v", err)
	}

	configPath := writeTestConfig(t, projectsBase)

	runCmd(t, projectDir, "br", "init")
	beadOne := createBead(t, projectDir, "Test conflict internal/cli/send.go")
	beadTwo := createBead(t, projectDir, "Another pass internal/cli/send.go")
	runCmd(t, projectDir, "br", "sync", "--flush-only")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := client.EnsureProject(ctx, projectDir)
	cancel()
	if err != nil {
		t.Fatalf("ensure Agent Mail project: %v", err)
	}

	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", session).Run()
	})

	runCmd(t, projectDir, "ntm", "--config", configPath, "spawn", session, "--cc=2")
	time.Sleep(500 * time.Millisecond)

	assignOut := runCmd(t, projectDir, "ntm", "--config", configPath, "assign", session, "--beads", beadOne+","+beadTwo, "--auto", "--strategy=round-robin", "--verbose")
	if !strings.Contains(string(assignOut), "file conflicts") {
		t.Fatalf("expected conflict warning in output, got:\n%s", string(assignOut))
	}

	testutil.AssertEventually(t, testutil.NewTestLoggerStdout(t), 10*time.Second, 250*time.Millisecond,
		"single reservation present after conflict", func() bool {
			reservations := listReservations(t, client, projectDir)
			return countReservations(reservations, "internal/cli/send.go") == 1
		})
}

func requireBr(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("br"); err != nil {
		t.Skip("br not installed, skipping Agent Mail reservation tests")
	}
}

func requireAgentMail(t *testing.T) *agentmail.Client {
	t.Helper()
	client := agentmail.NewClient()
	if !client.IsAvailable() {
		t.Skip("Agent Mail server not available, skipping tests")
	}
	return client
}

func ensureBDShim(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bd"); err == nil {
		return
	}
	brPath, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br not installed, cannot shim bd")
	}
	dir := t.TempDir()
	shimPath := filepath.Join(dir, "bd")
	script := fmt.Sprintf("#!/bin/sh\nexec %s \"$@\"\n", brPath)
	if err := os.WriteFile(shimPath, []byte(script), 0755); err != nil {
		t.Fatalf("write bd shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeTestConfig(t *testing.T, projectsBase string) string {
	t.Helper()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	config := fmt.Sprintf(`projects_base = %q

[agents]
claude = "bash"
codex = "bash"
gemini = "bash"

[agent_mail]
enabled = true
`, projectsBase)
	if err := os.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}

func runCmd(t *testing.T, dir, cmd string, args ...string) []byte {
	t.Helper()
	c := exec.Command(cmd, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s %s\nerr: %v\noutput:\n%s", cmd, strings.Join(args, " "), err, string(out))
	}
	return out
}

func createBead(t *testing.T, dir, title string) string {
	t.Helper()
	out := runCmd(t, dir, "br", "create", title, "-t", "task", "-p", "1", "--json")

	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issue); err == nil && issue.ID != "" {
		return issue.ID
	}

	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &issues); err == nil && len(issues) > 0 && issues[0].ID != "" {
		return issues[0].ID
	}

	t.Fatalf("unable to parse br create output: %s", string(out))
	return ""
}

func listReservations(t *testing.T, client *agentmail.Client, projectDir string) []agentmail.FileReservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reservations, err := client.ListReservations(ctx, projectDir, "", true)
	if err != nil {
		t.Fatalf("list reservations: %v", err)
	}
	return reservations
}

func hasReservation(reservations []agentmail.FileReservation, path string) bool {
	for _, r := range reservations {
		if r.PathPattern == path && r.ReleasedTS == nil {
			return true
		}
	}
	return false
}

func countReservations(reservations []agentmail.FileReservation, path string) int {
	count := 0
	for _, r := range reservations {
		if r.PathPattern == path && r.ReleasedTS == nil {
			count++
		}
	}
	return count
}
