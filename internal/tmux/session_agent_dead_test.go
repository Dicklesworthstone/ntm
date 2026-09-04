package tmux

import (
	"os/exec"
	"testing"
	"time"
)

// A shell whose foreground is the pane's process-group leader but which is
// running the real agent as a child (a non-exec'ing wrapper script, a `a && b`
// command list) is alive; only a shell with nothing under it is dead.
func TestAgentCLIDeadLooksUnderWrapperShells(t *testing.T) {
	wrapper := exec.Command("sh", "-c", "sleep 30")
	if err := wrapper.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	t.Cleanup(func() { _ = wrapper.Process.Kill(); _, _ = wrapper.Process.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for !shellHasLiveDescendant(wrapper.Process.Pid, 4) {
		if time.Now().After(deadline) {
			t.Fatalf("shellHasLiveDescendant(%d) stayed false; sleep child never observed", wrapper.Process.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	live := Pane{ID: "%1", Type: AgentClaude, Command: "sh", PID: wrapper.Process.Pid}
	if live.AgentCLIDead() {
		t.Fatalf("AgentCLIDead() = true for a wrapper shell with a live child")
	}

	// A shell executing a -c command (like the wrapper above, or a script path)
	// is running something even while it waits on stdin: not dead.
	waiting := Pane{ID: "%w", Type: AgentClaude, Command: "sh", PID: wrapper.Process.Pid}
	if waiting.AgentCLIDead() {
		t.Fatalf("AgentCLIDead() = true for a shell started with -c")
	}

	bare := exec.Command("sh", "-i")
	stdin, err := bare.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := bare.Start(); err != nil {
		t.Fatalf("start bare shell: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = bare.Process.Kill(); _, _ = bare.Process.Wait() })
	time.Sleep(50 * time.Millisecond)
	dead := Pane{ID: "%2", Type: AgentClaude, Command: "sh", PID: bare.Process.Pid}
	if !dead.AgentCLIDead() {
		t.Fatalf("AgentCLIDead() = false for a bare shell with no children")
	}
	if (Pane{ID: "%3", Type: AgentClaude, Command: "zsh"}).AgentCLIDead() != true {
		t.Fatalf("AgentCLIDead() must fall back to the command when no pid is known")
	}
	if (Pane{ID: "%4", Type: AgentUser, Command: "zsh", PID: bare.Process.Pid}).AgentCLIDead() {
		t.Fatalf("user panes are never dead")
	}
}

func TestPaneProcessStartedCommandOnlySemantics(t *testing.T) {
	cases := []struct {
		name string
		pane Pane
		want bool
	}{
		{"empty", Pane{ID: "%1"}, false},
		{"still starting", Pane{ID: "%1", Command: "tmux"}, false},
		{"idle shell, no pid", Pane{ID: "%1", Command: "zsh"}, false},
		{"agent", Pane{ID: "%1", Command: "node"}, true},
		{"agent path", Pane{ID: "%1", Command: "/opt/bin/claude"}, true},
	}
	for _, tc := range cases {
		if got := paneProcessStarted(tc.pane); got != tc.want {
			t.Fatalf("%s: paneProcessStarted(%+v) = %v, want %v", tc.name, tc.pane, got, tc.want)
		}
	}
}
