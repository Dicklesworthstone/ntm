package swarm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

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

// CodexHomeProvisioner resolves CAAM-owned, isolated CODEX_HOME directories so
// that Codex panes in a swarm never share the global ~/.codex/auth.json. A
// named CAAM profile supplies its own codex_home; an empty profile retains the
// legacy empty per-pane directory for an interactive login. Pane-local rotation
// changes only the affected pane's CODEX_HOME and never the shared global file.
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

// ----------------------------------------------------------------------------
// Live tmux probe wiring the CodexHomeInspector from 4765d665 to real panes.
// ----------------------------------------------------------------------------

// codexHomeProbe is the minimal surface the inspector needs. It is an
// interface so the probe stays unit-testable without a live tmux server.
type codexHomeProbe interface {
	GetPanes(session string) ([]tmux.Pane, error)
	// PaneCodexHome returns the isolated CODEX_HOME provisioned for a pane of
	// this session, and whether one exists.
	PaneCodexHome(session string, pane tmux.Pane) (value string, set bool, err error)
}

// provisionedCodexProbe implements codexHomeProbe by reading the durable
// pane->home mapping that CodexHomeProvisioner creates on disk.
//
// It replaces a `tmux show-environment -t <target> CODEX_HOME` probe that was
// structurally incapable of ever reporting isolation. show-environment's -t is
// a target-SESSION, not a target-pane: tmux resolves "session:1.2" down to the
// session and returns session-wide environment. CODEX_HOME reaches a pane as a
// command-line assignment on the codex process, so it lives in that process's
// environment and is invisible to tmux entirely. The inspector could therefore
// only ever answer "not isolated", which made guardAutoRotation refuse every
// automatic Codex rotation with reason "shared_global_codex_home" (bd-91hy2).
//
// The provisioner's layout is the mapping: HomePath is a pure function of
// (BaseDir, session, pane), so an existing directory at that path IS the record
// that this pane was provisioned an isolated home. No process probing needed,
// and it cannot drift from what ProvisionPaneHome actually created.
type provisionedCodexProbe struct {
	baseDir string
}
