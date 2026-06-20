package swarm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// defaultCodexHomeTimeout bounds caam invocations made while provisioning or
// repopulating an isolated CODEX_HOME.
const defaultCodexHomeTimeout = 10 * time.Second

// runCmdCapture runs an external command with a timeout, capturing stdout and
// stderr separately. It is used for the caam isolated-profile primitives.
func runCmdCapture(ctx context.Context, timeout time.Duration, name string, args ...string) (stdout, stderr string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultCodexHomeTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	if runErr != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return out.String(), errb.String(), fmt.Errorf("%s %v: timeout", name, args)
		}
		return out.String(), errb.String(), runErr
	}
	return out.String(), errb.String(), nil
}

// CodexHomeEnvVar is the environment variable Codex honors to locate its config
// and auth directory. When set, Codex reads auth.json from $CODEX_HOME/auth.json
// instead of the default global ~/.codex/auth.json.
const CodexHomeEnvVar = "CODEX_HOME"

// CodexHomeProvisioner provisions per-pane isolated CODEX_HOME directories so
// that Codex panes in a swarm never share the global ~/.codex/auth.json. Each
// pane gets its own directory under <baseDir>/.ntm/codex-homes/<session>/<pane>/
// seeded from a caam profile's auth, and is launched with CODEX_HOME pointing
// there. Pane-local rotation then repopulates only that pane's directory and
// restarts only that pane — never the shared global file.
//
// This closes the core risk in #194: many live panes, shared global auth, and
// automatic rate-limit-triggered global switching.
type CodexHomeProvisioner struct {
	// BaseDir is the directory under which .ntm/codex-homes/... lives. Typically
	// the swarm data directory or project root. Required.
	BaseDir string

	// CaamPath is the path to the caam binary (default: "caam").
	CaamPath string

	// CommandTimeout bounds caam invocations.
	CommandTimeout time.Duration

	// Logger for structured logging.
	Logger *slog.Logger
}

// NewCodexHomeProvisioner creates a provisioner rooted at baseDir.
func NewCodexHomeProvisioner(baseDir string) *CodexHomeProvisioner {
	return &CodexHomeProvisioner{
		BaseDir:        baseDir,
		CaamPath:       "caam",
		CommandTimeout: defaultCodexHomeTimeout,
		Logger:         slog.Default(),
	}
}

// WithCaamPath sets the caam binary path.
func (p *CodexHomeProvisioner) WithCaamPath(path string) *CodexHomeProvisioner {
	if path != "" {
		p.CaamPath = path
	}
	return p
}

// WithLogger sets the logger.
func (p *CodexHomeProvisioner) WithLogger(l *slog.Logger) *CodexHomeProvisioner {
	p.Logger = l
	return p
}

func (p *CodexHomeProvisioner) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// sanitizeSegment makes a session/pane identifier safe for use as a single path
// segment (no slashes, colons, or shell-hostile characters).
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	return out
}

// HomePath returns the isolated CODEX_HOME directory for a (session, pane) pair.
// It does not create the directory; use ProvisionPaneHome for that.
func (p *CodexHomeProvisioner) HomePath(session, pane string) string {
	return filepath.Join(p.BaseDir, ".ntm", "codex-homes", sanitizeSegment(session), sanitizeSegment(pane))
}

// ProvisionPaneHome creates an isolated CODEX_HOME for the pane and seeds it from
// the given caam profile's auth (via caam's isolated-profile primitives, NOT the
// global `caam switch`). It returns the absolute CODEX_HOME path that the pane
// should be launched with. If profile is empty, the directory is created empty
// (the pane will then need an interactive login or a later RepopulatePaneHome).
func (p *CodexHomeProvisioner) ProvisionPaneHome(ctx context.Context, session, pane, profile string) (string, error) {
	if p.BaseDir == "" {
		return "", fmt.Errorf("CodexHomeProvisioner: BaseDir is required")
	}
	home := p.HomePath(session, pane)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("create codex home %s: %w", home, err)
	}

	if profile != "" {
		if err := p.seedFromProfile(ctx, home, profile); err != nil {
			return "", err
		}
	}

	p.logger().Info("[CodexHome] provisioned",
		"session", session,
		"pane", pane,
		"codex_home", home,
		"profile", profile,
		"seeded", profile != "")
	return home, nil
}

// seedFromProfile writes auth.json into home from the named caam profile using
// caam's isolated-profile export. We prefer `caam profile export <profile>
// --provider openai --json` (isolated read, no global clobber). The result is
// written to <home>/auth.json with 0600 perms.
func (p *CodexHomeProvisioner) seedFromProfile(ctx context.Context, home, profile string) error {
	auth, err := p.exportProfileAuth(ctx, profile)
	if err != nil {
		return fmt.Errorf("seed codex home from profile %q: %w", profile, err)
	}
	if len(auth) == 0 {
		return fmt.Errorf("seed codex home from profile %q: caam returned empty auth", profile)
	}
	authPath := filepath.Join(home, "auth.json")
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", authPath, err)
	}
	return nil
}

// exportProfileAuth asks caam for the raw auth payload of an isolated profile
// without touching the global ~/.codex/auth.json. It tries the modern
// isolated-profile primitives in order and returns the first non-empty payload.
func (p *CodexHomeProvisioner) exportProfileAuth(ctx context.Context, profile string) ([]byte, error) {
	caam := p.CaamPath
	if caam == "" {
		caam = "caam"
	}
	// Preferred: a dedicated export of profile auth that does not clobber global.
	attempts := [][]string{
		{"profile", "export", profile, "--provider", "openai", "--json"},
		{"profile", "auth", profile, "--provider", "openai", "--json"},
		{"creds", "openai", "--profile", profile, "--json"},
	}
	var lastErr error
	for _, args := range attempts {
		out, stderr, err := runCmdCapture(ctx, p.CommandTimeout, caam, args...)
		if err != nil {
			lastErr = fmt.Errorf("caam %v: %w (%s)", args, err, strings.TrimSpace(stderr))
			continue
		}
		trimmed := strings.TrimSpace(out)
		if trimmed != "" {
			return []byte(trimmed), nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no caam isolated-profile export produced auth for %q", profile)
	}
	return nil, lastErr
}

// RepopulatePaneHome refreshes an existing pane's isolated CODEX_HOME with the
// auth for a (new) profile — the pane-local rotation primitive. It never touches
// the global ~/.codex/auth.json. The caller is responsible for restarting only
// that pane afterwards. Returns the CODEX_HOME path that was repopulated.
func (p *CodexHomeProvisioner) RepopulatePaneHome(ctx context.Context, session, pane, profile string) (string, error) {
	if profile == "" {
		return "", fmt.Errorf("RepopulatePaneHome: profile is required for pane-local rotation")
	}
	home := p.HomePath(session, pane)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("ensure codex home %s: %w", home, err)
	}
	if err := p.seedFromProfile(ctx, home, profile); err != nil {
		return "", err
	}
	p.logger().Info("[CodexHome] repopulated_pane_local",
		"session", session,
		"pane", pane,
		"codex_home", home,
		"profile", profile)
	return home, nil
}

// EnvForPane returns the environment-variable assignment (CODEX_HOME=<path>) for
// launching a pane with isolated auth. The launcher merges this into the pane's
// per-agent env.
func (p *CodexHomeProvisioner) EnvForPane(session, pane string) map[string]string {
	return map[string]string{CodexHomeEnvVar: p.HomePath(session, pane)}
}

// ----------------------------------------------------------------------------
// Live tmux probe wiring the CodexHomeInspector from 4765d665 to real panes.
// ----------------------------------------------------------------------------

// codexHomeProbe is the minimal tmux surface the inspector needs. It is an
// interface so the probe stays unit-testable without a live tmux server.
type codexHomeProbe interface {
	GetPanes(session string) ([]tmux.Pane, error)
	// ShowEnvironment returns the value of CODEX_HOME for a pane target, and
	// whether it is set. It maps onto `tmux show-environment -t <target> CODEX_HOME`.
	PaneCodexHome(target string) (value string, set bool, err error)
}

// NewTmuxCodexHomeInspector builds a CodexHomeInspector that reports the live
// Codex panes of a session and each pane's effective CODEX_HOME by probing tmux.
// A pane whose CODEX_HOME is unset (or points at the default global ~/.codex) is
// reported as NOT isolated, so the guard can refuse an unsafe global rotation.
func NewTmuxCodexHomeInspector(session string) CodexHomeInspector {
	return newTmuxCodexHomeInspector(session, defaultCodexProbe{})
}

// newTmuxCodexHomeInspector is the injectable form used by tests.
func newTmuxCodexHomeInspector(session string, probe codexHomeProbe) CodexHomeInspector {
	return func() ([]CodexPaneInfo, error) {
		panes, err := probe.GetPanes(session)
		if err != nil {
			return nil, fmt.Errorf("list panes for session %q: %w", session, err)
		}
		var out []CodexPaneInfo
		for _, pane := range panes {
			if !isCodexAgentType(pane.Type) {
				continue
			}
			target := pane.ID
			if target == "" {
				target = formatPaneTarget(session, pane.Index)
			}
			home, set, perr := probe.PaneCodexHome(target)
			if perr != nil {
				// Treat a probe failure for one pane as "unknown" => not isolated,
				// so the guard fails closed for that pane.
				out = append(out, CodexPaneInfo{SessionPane: target, CodexHome: ""})
				continue
			}
			info := CodexPaneInfo{SessionPane: target}
			if set && !isGlobalCodexHome(home) {
				info.CodexHome = home
			}
			out = append(out, info)
		}
		return out, nil
	}
}

// isCodexAgentType reports whether a tmux pane agent type is a Codex agent.
func isCodexAgentType(t tmux.AgentType) bool {
	return agent.AgentType(string(t)).Canonical() == agent.AgentTypeCodex
}

// isGlobalCodexHome reports whether a CODEX_HOME value resolves to the default
// global ~/.codex directory (i.e. NOT an isolated per-pane home).
func isGlobalCodexHome(home string) bool {
	home = strings.TrimSpace(home)
	if home == "" {
		return true
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		global := filepath.Join(homeDir, ".codex")
		if filepath.Clean(home) == filepath.Clean(global) {
			return true
		}
	}
	// Heuristic fallback for tests / odd $HOME: a bare ~/.codex tail with no
	// per-pane suffix is global.
	cleaned := filepath.Clean(home)
	if strings.HasSuffix(cleaned, string(filepath.Separator)+".codex") || cleaned == ".codex" {
		return true
	}
	return false
}

// defaultCodexProbe implements codexHomeProbe against the real tmux client.
type defaultCodexProbe struct{}

func (defaultCodexProbe) GetPanes(session string) ([]tmux.Pane, error) {
	return tmux.GetPanes(session)
}

func (defaultCodexProbe) PaneCodexHome(target string) (string, bool, error) {
	out, err := tmux.DefaultClient.Run("show-environment", "-t", target, CodexHomeEnvVar)
	if err != nil {
		// tmux exits non-zero with "unknown variable" when CODEX_HOME is unset for
		// the pane's session environment; treat that as "not set", not an error.
		if strings.Contains(strings.ToLower(err.Error()), "unknown variable") {
			return "", false, nil
		}
		return "", false, err
	}
	return parseShowEnvironment(out, CodexHomeEnvVar)
}

// parseShowEnvironment parses `tmux show-environment` output for a single var.
// Output lines look like "CODEX_HOME=/path" (set) or "-CODEX_HOME" (unset/removed).
func parseShowEnvironment(out, name string) (value string, set bool, err error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-"+name) {
			return "", false, nil
		}
		if strings.HasPrefix(line, name+"=") {
			return strings.TrimPrefix(line, name+"="), true, nil
		}
	}
	return "", false, nil
}
