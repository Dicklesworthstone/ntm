package robot

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/invariants"
	"github.com/Dicklesworthstone/ntm/internal/state"
	statuspkg "github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func checkDoctorTools(ctx context.Context, verbose bool) []DoctorToolCheck {
	requiredTools := map[tools.ToolName]bool{
		tools.ToolBV: true,
		tools.ToolBD: true,
	}

	optionalTools := []tools.ToolName{
		tools.ToolAM,
		tools.ToolCM,
		tools.ToolCASS,
		tools.ToolS2P,
	}

	type registeredTool struct {
		name    tools.ToolName
		adapter tools.Adapter
	}
	allTools := tools.AllTools()
	registered := make([]registeredTool, 0, len(allTools))
	for _, toolName := range allTools {
		if adapter, ok := tools.Get(toolName); ok {
			registered = append(registered, registeredTool{name: toolName, adapter: adapter})
		}
	}

	checks := make([]DoctorToolCheck, len(registered))
	var wg sync.WaitGroup
	for i, rt := range registered {
		wg.Add(1)
		go func(idx int, toolName tools.ToolName, adapter tools.Adapter) {
			defer wg.Done()
			displayName := string(toolName)
			if toolName == tools.ToolBD {
				displayName = "br"
			}

			check := DoctorToolCheck{
				Name:     displayName,
				Required: requiredTools[toolName],
			}

			path, installed := adapter.Detect()
			check.Installed = installed
			check.Path = path

			if installed {
				var (
					toolWG  sync.WaitGroup
					version tools.Version
					verErr  error
					caps    []tools.Capability
					capsErr error
					health  *tools.HealthStatus
					hlthErr error
					details []string
				)
				toolWG.Add(3)
				go func() {
					defer toolWG.Done()
					version, verErr = adapter.Version(ctx)
				}()
				go func() {
					defer toolWG.Done()
					caps, capsErr = adapter.Capabilities(ctx)
				}()
				go func() {
					defer toolWG.Done()
					health, hlthErr = adapter.Health(ctx)
				}()
				if verbose {
					toolWG.Add(1)
					go func() {
						defer toolWG.Done()
						details = CollectVerboseToolDetails(ctx, adapter)
					}()
				}
				toolWG.Wait()

				if verErr == nil {
					check.Version = version.String()
				}
				if capsErr == nil {
					for _, c := range caps {
						check.Capabilities = append(check.Capabilities, string(c))
					}
				}
				if hlthErr == nil && health != nil && health.Healthy {
					check.Status = "ok"
					check.Message = check.Version
				} else {
					check.Status = "warning"
					check.Message = "health check failed"
				}
				check.Details = details
			} else {
				if check.Required {
					check.Status = "error"
					check.Message = "required tool not found"
				} else {
					check.Status = "warning"
					check.Message = "optional tool not found"
				}
			}

			checks[idx] = check
		}(i, rt.name, rt.adapter)
	}
	wg.Wait()

	for _, name := range optionalTools {
		found := false
		for _, c := range checks {
			if c.Name == string(name) {
				found = true
				break
			}
		}
		if !found {
			checks = append(checks, DoctorToolCheck{
				Name:     string(name),
				Required: false,
				Status:   "warning",
				Message:  "adapter not registered",
			})
		}
	}

	return checks
}

func summarizeDoctorDiagnosticText(raw string) string {
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		cleaned := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, strings.TrimSpace(line))
		if cleaned == "" {
			continue
		}
		runes := []rune(cleaned)
		if len(runes) > 120 {
			return string(runes[:117]) + "..."
		}
		return cleaned
	}
	return ""
}

// CollectVerboseToolDetails probes extended diagnostic state from a tool adapter.
func CollectVerboseToolDetails(ctx context.Context, adapter tools.Adapter) []string {
	detailCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var details []string
	switch a := adapter.(type) {
	case *tools.ACFSAdapter:
		if info, err := a.GetInfo(detailCtx); err == nil && len(bytes.TrimSpace(info)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(info)); s != "" {
				details = append(details, "info: "+s)
			}
		}
		if st, err := a.GetStatus(detailCtx); err == nil && len(bytes.TrimSpace(st)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(st)); s != "" {
				details = append(details, "status: "+s)
			}
		}
		if doc, err := a.Doctor(detailCtx); err == nil && len(bytes.TrimSpace(doc)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(doc)); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.AMAdapter:
		if srvURL := strings.TrimSpace(a.ServerURL()); srvURL != "" {
			details = append(details, "endpoint: "+srvURL)
		}
		if hc, err := a.HealthCheck(detailCtx); err == nil && len(bytes.TrimSpace(hc)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(hc)); s != "" {
				details = append(details, "health_check: "+s)
			}
		}
	case *tools.CAAMAdapter:
		avail := a.IsAvailable(detailCtx)
		multi := a.HasMultipleAccounts(detailCtx)
		details = append(details, fmt.Sprintf("accounts_configured=%t multiple_accounts=%t", avail, multi))
		if active, err := a.GetActiveAccount(detailCtx); err == nil && active != nil {
			acct := active.ID
			if active.Name != "" {
				acct = active.Name
			}
			details = append(details, fmt.Sprintf("active_account: %s (%s)", acct, active.Provider))
		}
	case *tools.CASSAdapter:
		if rawCaps, err := a.GetCapabilities(detailCtx); err == nil && len(bytes.TrimSpace(rawCaps)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(rawCaps)); s != "" {
				details = append(details, "capabilities_json: "+s)
			}
		}
	case *tools.CMAdapter:
		if onboard, err := a.OnboardStatus(detailCtx); err == nil && len(bytes.TrimSpace(onboard)) > 0 {
			if s := summarizeDoctorDiagnosticText(string(onboard)); s != "" {
				details = append(details, "onboard: "+s)
			}
		}
	case *tools.DCGAdapter:
		if st, err := a.GetStatus(detailCtx); err == nil && st != nil {
			details = append(details, fmt.Sprintf("guard_enabled=%t blocked_patterns=%d", st.Enabled, len(st.BlockedPatterns)))
		}
	case *tools.RCHAdapter:
		avail := a.IsAvailable(detailCtx)
		healthy := a.HasHealthyWorkers(detailCtx)
		details = append(details, fmt.Sprintf("compatible=%t healthy_workers=%t", avail, healthy))
		if healthy {
			if best, err := a.SelectWorker(detailCtx, ""); err == nil && best != nil {
				details = append(details, fmt.Sprintf("selected_worker: %s (load=%d%%)", best.Name, best.Load))
			}
		}
	case *tools.RUAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDoctorDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.UBSAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDoctorDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	case *tools.XFAdapter:
		if doc, err := a.Doctor(detailCtx); err == nil && strings.TrimSpace(doc) != "" {
			if s := summarizeDoctorDiagnosticText(doc); s != "" {
				details = append(details, "doctor: "+s)
			}
		}
	}

	return details
}

// CheckDoctorDependencies probes core runtime and optional agent CLI dependencies.
func CheckDoctorDependencies(ctx context.Context) []DoctorDepCheck {
	checks := make([]DoctorDepCheck, 4)
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		defer wg.Done()
		tmuxCheck := DoctorDepCheck{
			Name:       "tmux",
			MinVersion: "3.0",
			Required:   true,
		}
		if tmux.DefaultClient.IsInstalled() {
			path := tmux.BinaryPath()
			tmuxCheck.Installed = true
			tmuxCheck.Path = path
			cmd := exec.CommandContext(ctx, path, "-V")
			cmd.WaitDelay = 2 * time.Second
			if out, err := cmd.Output(); err == nil {
				tmuxCheck.Version = SanitizeDependencyVersion(string(out))
			}
			tmuxCheck.Status = "ok"
		} else {
			tmuxCheck.Status = "error"
			tmuxCheck.Message = "tmux is required for NTM"
		}
		checks[0] = tmuxCheck
	}()

	go func() {
		defer wg.Done()
		goCheck := DoctorDepCheck{
			Name:       "go",
			MinVersion: "1.25",
		}
		if path, err := exec.LookPath("go"); err == nil {
			goCheck.Installed = true
			goCheck.Path = path
			cmd := exec.CommandContext(ctx, path, "version")
			cmd.WaitDelay = 2 * time.Second
			if out, err := cmd.Output(); err == nil {
				parts := strings.Fields(string(out))
				if len(parts) >= 3 && parts[0] == "go" && parts[1] == "version" {
					goCheck.Version = parts[2]
				} else {
					goCheck.Version = "unknown"
				}
				goCheck.Status = "ok"
			} else {
				goCheck.Status = "warning"
				goCheck.Message = "version check failed"
			}
		} else {
			goCheck.Installed = false
			goCheck.Status = "warning"
			goCheck.Message = "not found (needed for plugins)"
		}
		checks[1] = goCheck
	}()

	go func() {
		defer wg.Done()
		grokInstalled, grokVersion, grokPath := probeOptionalBinaryVersion(ctx, "grok", "--version")
		grokCheck := DoctorDepCheck{
			Name:      "grok",
			Installed: grokInstalled,
			Version:   grokVersion,
			Path:      grokPath,
			Status:    "ok",
		}
		if !grokCheck.Installed {
			grokCheck.Message = "optional agent not found"
		}
		checks[2] = grokCheck
	}()

	go func() {
		defer wg.Done()
		ompInstalled, ompVersion, ompPath := probeOptionalBinaryVersion(ctx, "omp", "--version")
		ompCheck := DoctorDepCheck{
			Name:      "omp",
			Installed: ompInstalled,
			Version:   ompVersion,
			Path:      ompPath,
			Status:    "ok",
		}
		if !ompCheck.Installed {
			ompCheck.Message = "optional agent not found"
		}
		checks[3] = ompCheck
	}()

	wg.Wait()
	return checks
}

func probeOptionalBinaryVersion(ctx context.Context, binary string, args ...string) (installed bool, version string, path string) {
	foundPath, err := exec.LookPath(binary)
	if err != nil {
		return false, "", ""
	}
	if len(args) > 0 {
		probeCtx, cancel := context.WithTimeout(ctx, doctorDepVersionTimeout)
		defer cancel()
		cmd := exec.CommandContext(probeCtx, foundPath, args...)
		cmd.WaitDelay = 2 * time.Second
		out, _ := cmd.CombinedOutput()
		if s := SanitizeDependencyVersion(string(out)); s != "" {
			return true, s, foundPath
		}
	}
	return true, "", foundPath
}

// SanitizeDependencyVersion constrains untrusted child-process output before it
// reaches JSON, logs, or a terminal.
func SanitizeDependencyVersion(raw string) string {
	clean := statuspkg.StripANSI(raw)
	runes := make([]rune, 0, len(clean))
	spacePending := false
	for _, r := range clean {
		if unicode.IsSpace(r) {
			spacePending = len(runes) > 0
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			continue
		}
		if spacePending {
			runes = append(runes, ' ')
			spacePending = false
		}
		runes = append(runes, r)
	}

	const maxVersionRunes = 240
	if len(runes) > maxVersionRunes {
		return string(runes[:maxVersionRunes]) + "..."
	}
	return string(runes)
}

// DaemonVerdict renders the status and message for one daemon probe.
func DaemonVerdict(running, expected bool, port int) (status, message string) {
	switch {
	case running:
		return "ok", fmt.Sprintf("listening on port %d", port)
	case expected:
		return "warning", fmt.Sprintf("enabled in config but not listening on port %d", port)
	default:
		return "unknown", fmt.Sprintf("not running (port %d free)", port)
	}
}

// AgentMailDaemonAddr resolves the configured Agent Mail daemon host and TCP port.
func AgentMailDaemonAddr() (string, int) {
	const defaultHost = "127.0.0.1"
	const defaultPort = 8765

	raw := strings.TrimSpace(tools.NewAMAdapter().ServerURL())
	if raw == "" {
		return defaultHost, defaultPort
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return defaultHost, defaultPort
	}
	host := parsed.Hostname()
	if host == "" {
		host = defaultHost
	}
	if portStr := parsed.Port(); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil && port > 0 && port <= 65535 {
			return host, port
		}
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return host, 443
	case "http":
		return host, 80
	default:
		return host, defaultPort
	}
}

func checkDoctorDaemons(ctx context.Context, cfg *config.Config) []DoctorDaemonCheck {
	amHost, amPort := AgentMailDaemonAddr()

	type daemonPort struct {
		name     string
		host     string
		port     int
		expected bool
	}
	daemons := []daemonPort{
		{"agent-mail", amHost, amPort, cfg != nil && cfg.AgentMail.Enabled},
		{"cm-server", "127.0.0.1", 8766, false},
	}

	checks := make([]DoctorDaemonCheck, len(daemons))
	var wg sync.WaitGroup
	for i, dp := range daemons {
		wg.Add(1)
		go func(idx int, dp daemonPort) {
			defer wg.Done()
			dialer := &net.Dialer{Timeout: time.Second}
			check := DoctorDaemonCheck{
				Name: dp.name,
				Port: dp.port,
			}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(dp.host, strconv.Itoa(dp.port)))
			if err == nil {
				_ = conn.Close()
				check.Running = true
			}
			check.Status, check.Message = DaemonVerdict(check.Running, dp.expected, dp.port)
			checks[idx] = check
		}(i, dp)
	}
	wg.Wait()

	return checks
}

func checkDoctorConfiguration(ctx context.Context, projectDir, configPath string) ([]DoctorConfigCheck, *DoctorGuardHookCheck) {
	checks := make([]DoctorConfigCheck, 0, 6)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		checks = append(checks, DoctorConfigCheck{
			Name:    ".ntm directory",
			Status:  "error",
			Message: "cannot determine home directory",
		})
	} else {
		ntmDir := filepath.Join(homeDir, ".ntm")
		if info, err := os.Stat(ntmDir); err == nil && info.IsDir() {
			if f, err := os.CreateTemp(ntmDir, ".doctor_test_*"); err == nil {
				name := f.Name()
				_ = f.Close()
				_ = os.Remove(name)
				checks = append(checks, DoctorConfigCheck{
					Name:    ".ntm directory",
					Valid:   true,
					Status:  "ok",
					Message: "exists and writable",
				})
			} else {
				checks = append(checks, DoctorConfigCheck{
					Name:    ".ntm directory",
					Valid:   false,
					Status:  "error",
					Message: "not writable",
				})
			}
		} else {
			checks = append(checks, DoctorConfigCheck{
				Name:    ".ntm directory",
				Valid:   false,
				Status:  "warning",
				Message: "does not exist (will be created)",
			})
		}
	}

	beadsPath := ".beads"
	if strings.TrimSpace(projectDir) != "" {
		beadsPath = filepath.Join(projectDir, ".beads")
	}
	if _, err := os.Stat(beadsPath); err == nil {
		checks = append(checks, DoctorConfigCheck{
			Name:    ".beads directory",
			Valid:   true,
			Status:  "ok",
			Message: "project has beads initialized",
		})
	} else {
		checks = append(checks, DoctorConfigCheck{
			Name:    ".beads directory",
			Valid:   false,
			Status:  "warning",
			Message: "no beads in current directory",
		})
	}

	guardHook, guardCfgCheck := CheckDoctorGuardState()
	checks = append(checks, guardCfgCheck)
	checks = append(checks, CheckDoctorGuardHookPathContext(ctx, projectDir))
	checks = append(checks, removedDoctorKnobChecks(configPath)...)
	checks = append(checks, deprecatedDoctorKnobChecks(configPath)...)

	return checks, guardHook
}

// CheckDoctorGuardState inspects the pre-commit guard degraded-mode ledger
// read-only without creating or migrating the SQLite state database.
func CheckDoctorGuardState() (*DoctorGuardHookCheck, DoctorConfigCheck) {
	hookCheck := &DoctorGuardHookCheck{
		Name:         "guard-hook",
		Status:       "ok",
		DegradedRuns: 0,
	}
	cfgCheck := DoctorConfigCheck{
		Name:   "guard-hook degraded runs",
		Valid:  true,
		Status: "ok",
	}

	dbPath := state.DefaultPath()
	info, err := os.Stat(dbPath)
	if err != nil || info.IsDir() {
		cfgCheck.Message = "no degraded (unchecked) guard runs recorded in the last 7 days"
		return hookCheck, cfgCheck
	}

	store, err := state.Open(dbPath)
	if err != nil {
		msg := fmt.Sprintf("state DB unavailable: %v", err)
		hookCheck.Status = "warning"
		hookCheck.Message = msg
		cfgCheck.Status = "warning"
		cfgCheck.Message = msg
		return hookCheck, cfgCheck
	}
	defer func() { _ = store.Close() }()

	since := time.Now().UTC().Add(-GuardDegradedDoctorWindow)
	stats, err := store.GuardDegradedEventStats(since)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			cfgCheck.Message = "no degraded (unchecked) guard runs recorded in the last 7 days"
			return hookCheck, cfgCheck
		}
		msg := fmt.Sprintf("could not read guard degraded events: %v", err)
		hookCheck.Status = "warning"
		hookCheck.Message = msg
		cfgCheck.Status = "warning"
		cfgCheck.Message = msg
		return hookCheck, cfgCheck
	}

	if stats.Count > 0 {
		transport, app := 0, 0
		if recent, listErr := store.ListGuardDegradedEvents(200); listErr == nil {
			for _, ev := range recent {
				if ev.CreatedAt.Before(since) {
					continue
				}
				if ev.Reason == doctorGuardUnreachable {
					transport++
				} else {
					app++
				}
			}
		}
		msg := fmt.Sprintf(
			"guard hook ran degraded %d time(s) in the last 7 days (first %s; %d with Agent Mail unreachable, %d with Agent Mail errors; commits were allowed unchecked)",
			stats.Count,
			stats.FirstAt.Local().Format(time.RFC3339),
			transport,
			app,
		)
		hookCheck.Status = "warning"
		hookCheck.DegradedRuns = stats.Count
		hookCheck.Since = stats.FirstAt.UTC().Format(time.RFC3339)
		hookCheck.Message = msg

		cfgCheck.Valid = false
		cfgCheck.Status = "warning"
		cfgCheck.Message = msg
		return hookCheck, cfgCheck
	}

	cfgCheck.Message = "no degraded (unchecked) guard runs recorded in the last 7 days"
	return hookCheck, cfgCheck
}

// CheckDoctorGuardHookPathContext detects an installed NTM pre-commit guard hook
// under projectDir (or the current working directory when empty) when `ntm` itself
// is missing from PATH.
func CheckDoctorGuardHookPathContext(ctx context.Context, projectDir string) DoctorConfigCheck {
	check := DoctorConfigCheck{Name: "guard-hook ntm on PATH", Valid: true, Status: "ok"}
	if ctx == nil {
		ctx = context.Background()
	}

	startDir := strings.TrimSpace(projectDir)
	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			check.Message = "could not determine current directory"
			return check
		}
		startDir = cwd
	}
	repoPath, err := doctorGitRoot(ctx, startDir)
	if err != nil {
		check.Message = "not in a git repository (guard hook not applicable)"
		return check
	}
	hookPath, err := doctorGitHookPath(ctx, repoPath, "pre-commit")
	if err != nil || !doctorPathExists(hookPath) {
		check.Message = "no pre-commit guard hook installed here"
		return check
	}
	content, err := os.ReadFile(hookPath)
	if err != nil || !hooks.PreCommitRunsReservationGuard(string(content)) {
		check.Message = "pre-commit hook is not an NTM guard"
		return check
	}
	if _, err := exec.LookPath("ntm"); err != nil {
		check.Valid = false
		check.Status = "warning"
		check.Message = "guard hook is installed but 'ntm' is not on PATH: the hook silently skips the reservation check on every commit (and cannot record a degraded event); add ntm to PATH or set NTM_GUARD_STRICT=1"
		return check
	}
	check.Message = "guard hook installed and 'ntm' resolves on PATH"
	return check
}

func doctorGitRoot(ctx context.Context, startPath string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "git", "-C", startPath, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("finding git root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func doctorGitHookPath(ctx context.Context, repoPath, hookName string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "git", "-C", repoPath, "rev-parse", "--git-path", "hooks/"+hookName)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving git hook path: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("resolving git hook path: empty path")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(repoPath, path), nil
}

func deprecatedDoctorKnobChecks(configPath string) []DoctorConfigCheck {
	knobs, err := config.ScanDeprecatedKnobs(configPath)
	if err != nil {
		return []DoctorConfigCheck{{
			Name:    "deprecated config keys",
			Valid:   false,
			Status:  "warning",
			Message: fmt.Sprintf("could not scan config for deprecated keys: %v", err),
		}}
	}
	if len(knobs) == 0 {
		return []DoctorConfigCheck{{
			Name:    "deprecated config keys",
			Valid:   true,
			Status:  "ok",
			Message: "no deprecated config keys present",
		}}
	}
	checks := make([]DoctorConfigCheck, 0, len(knobs))
	for _, knob := range knobs {
		checks = append(checks, DoctorConfigCheck{
			Name:    "deprecated config key: " + knob.Key,
			Valid:   false,
			Status:  "error",
			Message: DeadKnobDoctorMessage(knob),
		})
	}
	return checks
}

func removedDoctorKnobChecks(configPath string) []DoctorConfigCheck {
	knobs, err := config.ScanRemovedKnobs(configPath)
	if err != nil {
		return []DoctorConfigCheck{{
			Name:    "removed config keys",
			Valid:   false,
			Status:  "warning",
			Message: fmt.Sprintf("could not scan config for removed keys: %v", err),
		}}
	}
	if len(knobs) == 0 {
		return []DoctorConfigCheck{{
			Name:    "removed config keys",
			Valid:   true,
			Status:  "ok",
			Message: "no removed config keys present",
		}}
	}
	checks := make([]DoctorConfigCheck, 0, len(knobs))
	for _, knob := range knobs {
		checks = append(checks, DoctorConfigCheck{
			Name:    "removed config key: " + knob.Key,
			Valid:   false,
			Status:  "error",
			Message: DeadKnobDoctorMessage(knob),
		})
	}
	return checks
}

// DeadKnobDoctorMessage renders the remediation message for a removed or deprecated config knob.
func DeadKnobDoctorMessage(knob config.RemovedKnob) string {
	if provenance := config.DeadKeyTierProvenance(knob.Tier); provenance != "" {
		return fmt.Sprintf("%s (%s) — delete it from your config file", knob.Disposition, provenance)
	}
	return fmt.Sprintf("%s — delete it from your config file", knob.Disposition)
}

func doctorToolAvailabilityFromChecks(toolChecks []DoctorToolCheck) []invariants.ToolAvailability {
	out := make([]invariants.ToolAvailability, 0, len(toolChecks))
	for _, t := range toolChecks {
		out = append(out, invariants.ToolAvailability{
			Name:      t.Name,
			Available: t.Installed,
			Reported:  t.Installed || (t.Status != "ok" && t.Message != ""),
		})
	}
	return out
}

func liveDoctorSchemaConstraints() ([]string, error) {
	dbPath := state.DefaultPath()
	info, err := os.Stat(dbPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("state DB not initialized at %s", dbPath)
	}

	store, err := state.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	defer func() { _ = store.Close() }()

	db := store.DB()
	if db == nil {
		return nil, fmt.Errorf("state store exposes no database handle")
	}
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type = 'table' AND sql IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stmts []string
	for rows.Next() {
		var stmt string
		if err := rows.Scan(&stmt); err != nil {
			return nil, fmt.Errorf("scan schema row: %w", err)
		}
		stmts = append(stmts, stmt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema: %w", err)
	}
	return stmts, nil
}

func checkDoctorInvariants(ctx context.Context, projectDir string, toolChecks []DoctorToolCheck) []DoctorInvariantCheck {
	checks := make([]DoctorInvariantCheck, 0)

	cwd := strings.TrimSpace(projectDir)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			cwd = "."
		}
	}

	checker := invariants.NewChecker(cwd).
		WithToolReport(func() []invariants.ToolAvailability {
			return doctorToolAvailabilityFromChecks(toolChecks)
		}).
		WithSchemaConstraints(liveDoctorSchemaConstraints)
	report := checker.CheckAll(ctx)

	defs := invariants.Definitions()
	for _, id := range invariants.AllInvariants() {
		result, ok := report.Results[id]
		if !ok {
			continue
		}
		def := defs[id]
		checks = append(checks, DoctorInvariantCheck{
			ID:      string(id),
			Name:    def.Name,
			Status:  result.Status,
			Message: result.Message,
			Details: result.Details,
		})
	}

	return checks
}
