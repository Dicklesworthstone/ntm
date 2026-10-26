package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newRespawnCmd() *cobra.Command {
	var force bool
	var panes string
	var agentType string
	var all bool
	var dryRun bool
	var environmentFile string

	cmd := &cobra.Command{
		Use:     "respawn <session>",
		Aliases: []string{"restart"},
		Short:   "Kill and restart worker agents in a session",
		Long: `Kill and restart worker agents in a tmux session.

This command uses tmux's respawn-pane -k to kill each selected pane's
process and restore a fresh shell, then relaunches the pane's agent CLI
(agent CLIs are started by keystroke after spawn, so a bare respawn
would otherwise leave an empty shell) and waits for it to become ready.

By default, only agent panes are restarted (not the user pane at index 0).
Use --all to include all panes, or --panes to target N, W.P, or %N selectors.

If the saved launch requires environment values that were not persisted,
select exactly one agent pane with --panes and supply --restart-env-file.
The file must be a private regular file (mode 0600 or 0400) containing a
JSON object of the required environment names and string values. Values
are read once, used only for this restart, and never saved in launch metadata.
Dry-run performs the same recovery preflight without restarting any pane.

Examples:
  ntm respawn myproject              # Restart all agent panes (prompts for confirmation)
  ntm respawn myproject --force      # No confirmation
  ntm respawn myproject --panes=1,2  # Restart only panes 1 and 2
  ntm respawn myproject --type=cc    # Restart only Claude agents
  ntm respawn myproject --all        # Include user pane (index 0)
  ntm respawn myproject --dry-run    # Check recovery and preview affected panes
  ntm respawn myproject --panes=%7 --restart-env-file=/private/recovery.json --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRespawn(cmd.Context(), args[0], force, panes, agentType, all, dryRun, environmentFile)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringVarP(&panes, "panes", "p", "", "comma-separated N, W.P, or %N pane selectors to restart (e.g., 1,2.0,%7)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "", "filter by agent type (cc, claude, cod, codex, gmi, gemini, agy, antigravity, grok, grok-build, omp, oh-my-pi)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include all panes (including user pane)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate recovery and preview which panes would be restarted")
	cmd.Flags().StringVar(&environmentFile, "restart-env-file", "", "private JSON file supplying omitted launch environment for one explicitly selected agent pane")

	return cmd
}

func runRespawn(ctx context.Context, session string, force bool, panesFlag string, agentType string, all bool, dryRun bool, environmentFile string) error {
	if ctx == nil {
		return fmt.Errorf("respawn context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	environment, err := readRestartEnvironment(environmentFile)
	if err != nil {
		return err
	}
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSessionWithOptionsContext(ctx, session, nil, SessionResolveOptions{})
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	exists, err := tmux.SessionExistsContext(ctx, session)
	if err != nil {
		return fmt.Errorf("check session %q: %w", session, err)
	}
	if !exists {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Parse pane filter
	var paneFilter []string
	if panesFlag != "" {
		paneFilter = strings.Split(panesFlag, ",")
		for i := range paneFilter {
			paneFilter[i] = strings.TrimSpace(paneFilter[i])
			// Reject malformed selectors before the confirmation prompt; the
			// restart engine refuses them anyway.
			if _, err := tmux.ParsePaneSelector(paneFilter[i]); err != nil {
				return fmt.Errorf("--panes: %w", err)
			}
		}
	}

	// Use the same selection and launch preflight as the robot surface before
	// previewing or requesting confirmation. In particular, omitted environment
	// must never make a dry-run promise a restart that execution would refuse.
	opts := robot.RestartPaneOptions{
		Session:       session,
		Panes:         paneFilter,
		Type:          agentType,
		All:           all,
		DryRun:        true,
		Environment:   environment,
		Config:        cfg,
		ConfigPath:    selectedConfigPath(),
		RequireConfig: selectedConfigIsExplicit(),
	}
	preflight, err := robot.GetRestartPaneContext(ctx, opts)
	if err != nil {
		return err
	}
	if err := respawnResultError(preflight); err != nil {
		return err
	}
	if len(preflight.WouldAffect) == 0 {
		fmt.Println("No panes matched the filter criteria.")
		return nil
	}

	// Dry-run mode
	if dryRun {
		fmt.Printf("Would restart %d pane(s) in session '%s':\n", len(preflight.WouldAffect), session)
		for _, pane := range preflight.WouldAffect {
			fmt.Printf("  - Pane %s\n", pane)
		}
		return nil
	}

	// Confirmation
	if !force {
		title := fmt.Sprintf("Restart %d pane(s)?", len(preflight.WouldAffect))
		desc := fmt.Sprintf("Agents in session '%s' will be killed and relaunched.", session)
		if !confirmHuh(title, desc) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Restart targets via the shared robot engine, which relaunches agent
	// CLIs after the respawn and ready-gates them (#187) — respawn-pane -k
	// alone only restores the pane's default command (the login shell).
	fmt.Printf("Restarting %d pane(s) (relaunching agent CLIs)...\n", len(preflight.WouldAffect))
	opts.DryRun = false
	// Preserve the exact physical panes and launch evidence that the operator
	// reviewed. A logical selector may identify a different pane by the time
	// confirmation returns; the engine must reject changed recovery targets.
	opts.Preflight = preflight.Preflight
	out, err := robot.GetRestartPaneContext(ctx, opts)
	if err != nil {
		return err
	}
	if out == nil {
		return respawnResultError(nil)
	}
	resultErr := respawnResultError(out)

	// Report results
	if len(out.Restarted) > 0 {
		fmt.Printf("Restarted panes: %s\n", strings.Join(out.Restarted, ", "))
		for _, paneKey := range out.Restarted {
			if status, ok := respawnRelaunchDisplayStatus(out, paneKey); ok {
				fmt.Printf("  - Pane %s: %s\n", paneKey, status)
			}
		}
	}
	if len(out.Failed) > 0 {
		fmt.Printf("Failed to restart:\n")
		for _, f := range out.Failed {
			fmt.Printf("  - %s: %s\n", f.Pane, f.Reason)
		}
		if resultErr == nil {
			resultErr = fmt.Errorf("%d pane(s) failed to restart cleanly", len(out.Failed))
		}
	}

	return resultErr
}

func respawnRelaunchDisplayStatus(out *robot.RestartPaneOutput, paneKey string) (string, bool) {
	if out == nil {
		return "", false
	}
	if status, ok := out.AgentRelaunchStatus[paneKey]; ok {
		switch status {
		case robot.RestartAgentRelaunchReady:
			return "agent relaunched and ready", true
		case robot.RestartAgentRelaunchNotReady:
			if out.ProcessAlive[paneKey] {
				return "agent process is alive but not ready", true
			}
			return "agent did not relaunch", true
		case robot.RestartAgentRelaunchUnknown:
			if out.ProcessAlive[paneKey] {
				return "agent relaunch status UNKNOWN (live child observed; inspect pane before retrying)", true
			}
			return "agent relaunch status UNKNOWN (inspect pane before retrying)", true
		case robot.RestartAgentRelaunchFailed:
			return "agent relaunch FAILED", true
		default:
			return fmt.Sprintf("agent relaunch status %q (inspect pane)", status), true
		}
	}
	if relaunched, ok := out.AgentRelaunched[paneKey]; ok {
		if relaunched {
			return "agent relaunched", true
		}
		return "agent relaunch FAILED (pane left at a shell)", true
	}
	return "", false
}

func respawnResultError(out *robot.RestartPaneOutput) error {
	if out == nil {
		return fmt.Errorf("respawn failed: empty restart response")
	}
	if out.Success {
		return nil
	}
	message := out.Error
	if out.Hint != "" {
		message += "; " + out.Hint
	}
	if out.ErrorCode != "" {
		return fmt.Errorf("respawn unavailable (%s): %s", out.ErrorCode, message)
	}
	return fmt.Errorf("respawn failed: %s", message)
}

// normalizeAgentType normalizes agent type aliases to canonical form.
func normalizeAgentType(t string) string {
	return robot.ResolveAgentType(t)
}

const maxRestartEnvironmentFileBytes = 1 << 20

// readRestartEnvironment snapshots explicit recovery input once. Neither the
// path nor file contents are included in diagnostics: malformed JSON can quote
// a secret in its parser error, and must be reported without the original error.
func readRestartEnvironment(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("--restart-env-file: cannot access recovery environment file")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("--restart-env-file: recovery input must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0400 == 0 {
		return nil, fmt.Errorf("--restart-env-file: recovery input must be private and owner-readable (mode 0600 or 0400)")
	}
	if info.Size() > maxRestartEnvironmentFileBytes {
		return nil, fmt.Errorf("--restart-env-file: recovery input exceeds 1 MiB")
	}
	file, err := tmux.OpenLaunchEnvironmentFile(path)
	if err != nil {
		return nil, fmt.Errorf("--restart-env-file: cannot read recovery environment file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || opened.Mode().Perm()&0400 == 0 {
		return nil, fmt.Errorf("--restart-env-file: recovery input changed or is not private")
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return nil, fmt.Errorf("--restart-env-file: recovery input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRestartEnvironmentFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("--restart-env-file: cannot read recovery environment file")
	}
	if len(data) > maxRestartEnvironmentFileBytes {
		return nil, fmt.Errorf("--restart-env-file: recovery input exceeds 1 MiB")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("--restart-env-file: recovery input must be UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("--restart-env-file: expected a JSON object of environment names and string values")
	}
	environment := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("--restart-env-file: malformed environment object")
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("--restart-env-file: malformed environment name")
		}
		if _, duplicate := environment[name]; duplicate {
			return nil, fmt.Errorf("--restart-env-file: duplicate environment name")
		}
		value, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("--restart-env-file: malformed environment value")
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("--restart-env-file: environment values must be JSON strings")
		}
		environment[name] = text
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("--restart-env-file: malformed environment object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("--restart-env-file: unexpected trailing input")
	}
	if err := tmux.ValidateLaunchEnvironment(environment); err != nil {
		return nil, fmt.Errorf("--restart-env-file: %w", err)
	}
	return environment, nil
}
