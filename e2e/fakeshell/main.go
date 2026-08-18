// Command fakeshell is a command-executing agent-CLI stand-in for E2E
// fixtures (bd-h4t0j).
//
// The canonical-pane and spawn-family E2E fixtures historically ran bare
// bash panes (or `#!/bin/sh` fake-agent scripts) titled as agents
// ("<session>__cc_2", ...). Two intentional fail-closed hardenings made
// that impossible:
//
//   - the PANE_AGENT_DEAD liveness gate (ntm-0g0b) refuses dispatch to any
//     agent-typed pane whose foreground process is a bare shell, and on
//     macOS even a `#!/bin/sh` fake-agent script reports pane_current_command
//     "bash" (the kernel resolves the interpreter, not the script name);
//   - the composer-visibility gate (bd-dp9oy) requires a non-empty capture
//     of a Claude/codex pane to show the real composer glyph ("❯" / "›").
//
// fakeshell bridges both worlds: it is a REAL non-shell foreground process
// (pane_current_command is "fakeshell", so the liveness gate passes), it
// renders a prompt carrying the pane's composer glyph (so the composer gate
// passes), and it still processes every submitted line:
//
//   - --mode=exec (default): run the line through /bin/sh, so exact-marker
//     assertions built on `printf '%s\n' '<marker>'` observe real side
//     effects, exactly as they did against bash;
//   - --mode=echo: print "RECEIVED:<line>" like the historical sh-script
//     fake agents, never executing prompt text (work prompts contain live
//     `br close ...` instructions that must not run).
//
// Children run in the fixture's process group and inherit the pane tty, so
// tmux C-c interrupts (SIGINT to the foreground group) kill the running
// child while fakeshell itself survives and re-renders its prompt — the
// same contract an operator shell provides.
//
// The binary is a test fixture only: built on demand by the E2E harness and
// never shipped in releases.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	prompt := flag.String("prompt", "NTM_E2E> ", "prompt rendered before each read (may carry a composer glyph)")
	shell := flag.String("shell", "/bin/sh", "shell used to execute each submitted line in exec mode")
	mode := flag.String("mode", "exec", "exec: run lines via the shell; echo: print RECEIVED:<line> without executing")
	banner := flag.String("banner", "", `startup banner; literal \n sequences become newlines`)
	logPath := flag.String("log", "", "append each submitted non-empty line to this file")
	ttyNoEcho := flag.Bool("tty-noecho", false, "disable tty echo at startup (stty -echo), like the historical fake agents")
	flag.Parse()

	if *ttyNoEcho {
		stty := exec.Command("stty", "-echo")
		stty.Stdin = os.Stdin
		_ = stty.Run() // best-effort: absent stty just leaves echo on
	}

	// Survive C-c aimed at a running child: tmux delivers SIGINT to the
	// pane's whole foreground process group. The child dies (default
	// disposition); the fixture re-renders a fresh prompt like bash would.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		for range sigCh {
			fmt.Fprintf(os.Stdout, "\r\n%s", *prompt)
		}
	}()

	if *banner != "" {
		for _, line := range strings.Split(strings.ReplaceAll(*banner, `\n`, "\n"), "\n") {
			fmt.Fprintf(os.Stdout, "%s\r\n", line)
		}
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Fprint(os.Stdout, *prompt)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			// Emit our own line break after accepting input, exactly like
			// bash's readline does: when tty echo is off (stty -echo), the
			// tty does not echo the Enter, and without this the command
			// output (and the next prompt) would be glued onto the prompt
			// line, breaking exact-line marker assertions.
			fmt.Fprint(os.Stdout, "\r\n")
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) != "" {
			if *logPath != "" {
				if f, openErr := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); openErr == nil {
					_, _ = fmt.Fprintf(f, "%s\n", line)
					_ = f.Close()
				}
			}
			switch *mode {
			case "echo":
				fmt.Fprintf(os.Stdout, "RECEIVED:%s\r\n", line)
			default:
				cmd := exec.Command(*shell, "-c", line)
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				_ = cmd.Run()
			}
		}
		if err != nil {
			return // EOF: pane teardown
		}
		fmt.Fprint(os.Stdout, *prompt)
	}
}
