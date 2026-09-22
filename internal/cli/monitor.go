package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/archive"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/coordinator"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/supervisor"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-monitor <session>",
		Short:  "Run the resilience monitor for a session (internal use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonitorContext(cmd.Context(), args[0])
		},
	}
}

func runMonitor(session string) error {
	return runMonitorContext(context.Background(), session)
}

func runMonitorContext(parent context.Context, session string) (runErr error) {
	if tmux.DefaultClient.Remote != "" {
		return fmt.Errorf("internal monitors require local tmux sessions")
	}
	ctx, stopSignals := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	lease, err := resilience.AcquireSessionMonitor(ctx, session)
	if err != nil {
		return fmt.Errorf("acquire session monitor ownership: %w", err)
	}
	defer lease.Close()
	defer func() {
		if runErr != nil {
			lease.Fail(runErr)
		}
	}()
	ctx = lease.Context()
	// Load manifest
	manifest, err := resilience.LoadManifest(session)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}
	if manifest.AccountRotation != nil {
		return runAccountRotationMonitor(ctx, manifest, lease.ConfirmReady)
	}

	// Close the singleton ensemble state store on exit so the underlying
	// SQLite database is not leaked.
	defer ensemble.CloseDefaultStateStore()

	// Ensure session exists (retry a few times for transient tmux failures)
	const startupRetries = 3
	const startupRetryDelay = 2 * time.Second
	sessionFound := false
	for i := 0; i < startupRetries; i++ {
		exists, _ := tmux.SessionExistsContext(ctx, session)
		if ctx.Err() != nil {
			return nil
		}
		if exists {
			sessionFound = true
			break
		}
		if i < startupRetries-1 {
			fmt.Fprintf(os.Stderr, "Session '%s' not found on startup attempt %d/%d, retrying in %v...\n",
				session, i+1, startupRetries, startupRetryDelay)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(startupRetryDelay):
			}
		}
	}
	if !sessionFound {
		fmt.Fprintf(os.Stderr, "Session '%s' missing after %d startup checks (%s)\n",
			session, startupRetries, detectSessionTerminationCause(ctx, session))
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookSessionEnded,
			session,
			"",
			"",
			fmt.Sprintf("Session %s ended before monitor start", session),
			map[string]string{
				"project_dir": manifest.ProjectDir,
			},
		))
		// Don't delete manifest on startup failure — session may be transiently unavailable
		return fmt.Errorf("session %s ended before monitor startup", session)
	}

	// Enable project webhooks (if configured) for this session so monitor-driven
	// agent lifecycle events (crash/restart/rate_limit, etc) can fan out.
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, err := webhook.StartBridgeFromProjectConfig(manifest.ProjectDir, session, events.DefaultBus, &redactCfg)
		if err != nil {
			slog.Default().Debug("webhook bridge init failed", "session", session, "error", err)
		} else if bridge != nil {
			defer bridge.Close()
		}
	}

	// Load plugins to populate config
	pluginsDir := filepath.Join(selectedConfigDir(), "agents")
	if loadedPlugins, err := plugins.LoadAgentPlugins(pluginsDir); err == nil && cfg != nil {
		if cfg.Agents.Plugins == nil {
			cfg.Agents.Plugins = make(map[string]string)
		}
		for _, p := range loadedPlugins {
			cfg.Agents.Plugins[p.Name] = p.Command
		}
	}

	// Initialize resilience monitor
	monitor := resilience.NewMonitor(session, manifest.ProjectDir, cfg, manifest.AutoRestart)

	// Register agents
	for _, agent := range manifest.Agents {
		monitor.RegisterAgentWithBinding(agent.PaneID, agent.PaneIndex, 0, agent.Type, agent.Model, agent.Command, agent.LaunchBinding)
	}

	// Start monitoring
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := lease.ConfirmReady(ctx, os.Stdin); err != nil {
		return err
	}
	monitor.Start(ctx)
	defer monitor.Stop()

	// Only an authorized owner may start auxiliary daemons. Initialization
	// above is read-only, so parent cancellation before authorization leaves
	// no detached supervisors behind.
	sup, err := supervisor.New(supervisor.Config{SessionID: session, ProjectDir: manifest.ProjectDir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize supervisor: %v\n", err)
	} else {
		defer sup.Shutdown()
		amSupervised := shouldSuperviseAgentMailDaemon()
		for _, spec := range supervisor.DefaultSpecs() {
			if ctx.Err() != nil {
				return nil
			}
			if spec.Name == "am" && !amSupervised {
				fmt.Printf("Skipping daemon: am (Agent Mail is externally managed; set [agent_mail].supervisor_enabled = true to let ntm own it)\n")
				continue
			}
			if err := sup.Start(spec); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start daemon %s: %v\n", spec.Name, err)
			} else {
				fmt.Printf("Started daemon: %s\n", spec.Name)
			}
		}
	}

	// Initialize archiver for background CASS capture
	archiverOpts := archive.DefaultArchiverOptions(session)
	archiver, err := archive.NewArchiver(archiverOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize archiver: %v\n", err)
	} else {
		fmt.Printf("Starting archiver for session %s\n", session)
		archiveDone := make(chan struct{})
		go func() {
			defer close(archiveDone)
			if err := archiver.Run(ctx); err != nil && err != context.Canceled {
				fmt.Fprintf(os.Stderr, "Archiver error: %v\n", err)
			}
		}()
		defer func() {
			cancel()
			<-archiveDone
			_ = archiver.Close()
		}()
	}

	// Poll for session existence periodically to exit if session is killed.
	// Use consecutive-miss counting to tolerate transient tmux failures.
	const maxMisses = 5 // ~25 seconds at 5s interval before giving up
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Snapshot output periodically to generate summary on exit
	snapshotTicker := time.NewTicker(30 * time.Second)
	defer snapshotTicker.Stop()
	lastOutputs := make(map[string]string)

	// Sample file changes so the surfaces that read tracker.GlobalFileChanges —
	// `ntm changes`, `ntm conflicts`, --robot-status, the dashboard Files panel
	// and work-coordination's FileConflicts — have something to report. Each
	// sample costs one `git status` plus a stat per dirty file.
	changeSampler := newFileChangeSampler(manifest)
	fileChangeTicker := time.NewTicker(fileChangeSampleInterval)
	defer fileChangeTicker.Stop()

	fmt.Printf("Monitoring session '%s' for resilience...\n", session)
	fmt.Println(describeFileChangeSampler(ctx, manifest))

	missCount := 0
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Monitor stopping...")
			monitor.Stop()
			// Explicit stop must finish quiescing the owner. Starting fresh
			// summary work here would delay kill or forced session restore.
			return nil
		case <-ticker.C:
			exists, _ := tmux.SessionExistsContext(ctx, session)
			if ctx.Err() != nil {
				return nil
			}
			if !exists {
				missCount++
				cause := detectSessionTerminationCause(ctx, session)
				if missCount < maxMisses {
					fmt.Fprintf(os.Stderr, "Session '%s' not found (%d/%d consecutive misses, cause: %s)\n",
						session, missCount, maxMisses, cause)
					continue
				}
				// Confirmed permanently gone after maxMisses consecutive failures
				fmt.Printf("Session ended (%d consecutive misses), stopping monitor (%s)\n", missCount, cause)
				events.DefaultEmitter().Emit(events.NewWebhookEvent(
					events.WebhookSessionEnded,
					session,
					"",
					"",
					fmt.Sprintf("Session %s ended", session),
					map[string]string{
						"project_dir": manifest.ProjectDir,
					},
				))
				monitor.Stop()
				func() {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "Panic in session summary generation: %v\n", r)
						}
					}()
					generateEndSessionSummary(ctx, session, lastOutputs, manifest)
				}()
				_ = resilience.DeleteManifest(session)
				return nil
			}
			// Session found — reset miss counter
			if missCount > 0 {
				fmt.Printf("Session '%s' recovered after %d miss(es)\n", session, missCount)
				missCount = 0
			}
		case <-snapshotTicker.C:
			captureSessionOutputs(ctx, session, lastOutputs)
		case <-fileChangeTicker.C:
			changeSampler.Sample(ctx)
		}
	}
}

func shouldSuperviseAgentMailDaemon() bool {
	if cfg == nil || !cfg.AgentMail.Enabled {
		return false
	}
	return cfg.AgentMail.SupervisorEnabledOrDefault()
}

type accountRotationRunner interface {
	RunOnce(context.Context) []coordinator.AccountFailoverDecision
	Close() error
}

var newAccountRotationRunner = func(session string, options coordinator.AccountFailoverOptions) (accountRotationRunner, error) {
	return coordinator.NewAccountFailoverMonitor(session, options)
}

var rotationSessionIdentityPattern = regexp.MustCompile(`^[1-9][0-9]*:\$[0-9]+:[1-9][0-9]*$`)

func accountRotationSessionIdentity(ctx context.Context, session string) (string, error) {
	identity, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.TargetSession(session), "#{pid}:#{session_id}:#{session_created}")
	if err != nil {
		return "", err
	}
	identity = strings.TrimSpace(identity)
	if !rotationSessionIdentityPattern.MatchString(identity) {
		return "", fmt.Errorf("cannot establish original tmux session identity for %s", session)
	}
	return identity, nil
}

// Rotation-only monitors share the normal resident owner and stop protocol,
// while running only the canonical CAAM checker. In particular they do not
// start unrelated supervisors, automatic assignment, or crash restart loops.
func runAccountRotationMonitor(ctx context.Context, manifest *resilience.SpawnManifest, confirmReady func(context.Context, io.ReadCloser) error) error {
	if manifest == nil || manifest.AccountRotation == nil {
		return fmt.Errorf("account rotation monitor requires explicit policy")
	}
	if tmux.DefaultClient.Remote != "" {
		return fmt.Errorf("account rotation monitor requires local sessions and credentials")
	}
	if len(manifest.Agents) == 0 {
		return fmt.Errorf("account rotation monitor requires successfully launched panes")
	}
	if confirmReady == nil {
		return fmt.Errorf("account rotation monitor requires resident startup authorization")
	}
	options := manifest.AccountRotation
	effective := config.Default()
	if cfg != nil {
		copy := *cfg
		effective = &copy
	}
	effective.Integrations.CAAM.AutoFailover = true
	effective.Integrations.CAAM.BinaryPath = options.CAAMBinary
	effective.Integrations.CAAM.FailoverProviders = append([]string(nil), options.Providers...)
	effective.Integrations.CAAM.ResetHorizonMinutes = options.ResetHorizonMinutes
	targets := make(map[string]coordinator.AccountFailoverTarget, len(manifest.Agents))
	for _, agent := range manifest.Agents {
		if !filepath.IsAbs(agent.ProjectDir) {
			return fmt.Errorf("account rotation pane %s has no absolute project directory", agent.PaneID)
		}
		if _, exists := targets[agent.PaneID]; exists {
			return fmt.Errorf("duplicate account rotation pane %s", agent.PaneID)
		}
		targets[agent.PaneID] = coordinator.AccountFailoverTarget{AgentType: agent.Type, ProjectDir: agent.ProjectDir}
	}
	if err := validateAccountRotationScope(ctx, manifest, targets); err != nil {
		return err
	}
	if err := swarmPreflightAccountRotation(ctx, effective.Integrations.CAAM); err != nil {
		return err
	}
	runner, err := newAccountRotationRunner(manifest.Session, coordinator.AccountFailoverOptions{Config: effective, Targets: targets, ForceGlobalAuthClobber: options.ForceGlobalAuthClobber})
	if err != nil {
		return err
	}
	defer runner.Close()
	interval := time.Duration(options.PollSeconds) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// CAAM and runtime-store initialization may take time. Acknowledgment
	// must still refer to the original live session and its launched panes.
	if err := validateAccountRotationScope(ctx, manifest, targets); err != nil {
		return err
	}
	if err := confirmReady(ctx, os.Stdin); err != nil {
		return err
	}
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			current, err := accountRotationSessionIdentity(ctx, manifest.Session)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				misses++
				if misses >= 5 {
					return fmt.Errorf("account rotation original session unavailable: %w", err)
				}
				continue
			}
			if current != manifest.SessionIdentity {
				return fmt.Errorf("account rotation stopped because the original session was replaced")
			}
			misses = 0
			// Synchronous ticks ensure the lease is not released until every
			// in-flight account mutation and recovery has returned.
			for _, decision := range runner.RunOnce(ctx) {
				if err := json.NewEncoder(os.Stdout).Encode(decision); err != nil {
					return fmt.Errorf("record account rotation decision: %w", err)
				}
			}
		}
	}
}

func validateAccountRotationScope(ctx context.Context, manifest *resilience.SpawnManifest, targets map[string]coordinator.AccountFailoverTarget) error {
	identity, err := accountRotationSessionIdentity(ctx, manifest.Session)
	if err != nil {
		return err
	}
	if manifest.SessionIdentity == "" || identity != manifest.SessionIdentity {
		return fmt.Errorf("account rotation original session identity changed before monitor startup")
	}
	panes, err := tmux.GetPanesContext(ctx, manifest.Session)
	if err != nil {
		return err
	}
	found := 0
	for _, pane := range panes {
		target, ok := targets[pane.ID]
		if !ok {
			continue
		}
		if pane.Dead || pane.IsServicePane() || pane.Type.Canonical() != tmux.AgentType(target.AgentType).Canonical() {
			return fmt.Errorf("account rotation pane %s no longer has its launched identity", pane.ID)
		}
		found++
	}
	if found != len(targets) {
		return fmt.Errorf("account rotation found %d of %d launched panes", found, len(targets))
	}
	return nil
}

func detectSessionTerminationCause(ctx context.Context, session string) string {
	output, err := tmux.DefaultClient.RunContext(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "no server running"),
			strings.Contains(errMsg, "error connecting to"),
			strings.Contains(errMsg, "No such file or directory"):
			return "tmux server not running"
		case strings.Contains(errMsg, "no sessions"):
			return "tmux reports no sessions"
		default:
			return fmt.Sprintf("tmux error: %s", errMsg)
		}
	}

	if strings.TrimSpace(output) == "" {
		return "no tmux sessions found"
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == session {
			return "session still exists (race)"
		}
	}

	return "session not found in tmux list"
}

func captureSessionOutputs(ctx context.Context, session string, lastOutputs map[string]string) {
	panes, err := tmux.GetPanesContext(ctx, session)
	if err != nil {
		return
	}
	for _, p := range panes {
		if ctx.Err() != nil {
			return
		}
		// Capture every pane with a recognized type (agents and user panes
		// alike); only untyped/unknown panes are skipped.
		if p.Type == "" || p.Type == "unknown" {
			continue
		}
		// Capture reasonable amount of context
		out, err := tmux.DefaultClient.CapturePaneOutputContext(ctx, p.ID, 1000)
		if err == nil {
			lastOutputs[p.ID] = out
		}
	}
}

func generateEndSessionSummary(ctx context.Context, session string, lastOutputs map[string]string, manifest *resilience.SpawnManifest) {
	if ctx.Err() != nil || len(lastOutputs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var outputs []summary.AgentOutput
	for _, agent := range manifest.Agents {
		if out, ok := lastOutputs[agent.PaneID]; ok {
			outputs = append(outputs, summary.AgentOutput{
				AgentID:   agent.PaneID,
				AgentType: agent.Type,
				Output:    out,
			})
		}
	}

	if len(outputs) == 0 {
		return
	}

	opts := summary.Options{
		Session:        session,
		Outputs:        outputs,
		Format:         summary.FormatHandoff, // Handoff format is good for end of session
		ProjectKey:     manifest.ProjectDir,
		ProjectDir:     manifest.ProjectDir,
		IncludeGitDiff: true,
	}

	s, err := summary.SummarizeSession(ctx, opts)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate session summary: %v\n", err)
		return
	}

	// Store summary
	summaryDir := filepath.Join(manifest.ProjectDir, ".ntm", "summaries")
	if err := os.MkdirAll(summaryDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create summary dir: %v\n", err)
		return
	}

	timestamp := time.Now().Format("20060102-150405")
	filename := filepath.Join(summaryDir, fmt.Sprintf("%s-%s.json", session, timestamp))
	file, err := os.Create(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create summary file: %v\n", err)
		return
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(s); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write summary file: %v\n", err)
	} else {
		fmt.Printf("Session summary saved to %s\n", filename)
	}
}
