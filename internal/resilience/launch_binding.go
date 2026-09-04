package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unicode"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	caamLaunchBinding = "caam"
	caamProfileEnv     = "SHALLOW_PROFILE"
)

// LaunchAffinity reports whether a relaunch is bound to the same provider
// profile selected at creation time. Unknown is the explicit compatibility
// state for manifests created before launch bindings existed.
type LaunchAffinity string

const (
	LaunchAffinityPreserved LaunchAffinity = "preserved"
	LaunchAffinityUnknown   LaunchAffinity = "unknown"
)

// LaunchBinding is the only creation-time account-affinity state NTM persists.
// Identifier is an opaque, provider-scoped launcher profile name. It is not a
// home directory, token, credential, or arbitrary pane environment.
type LaunchBinding struct {
	Provider   string `json:"provider"`
	Launcher   string `json:"launcher"`
	Identifier string `json:"identifier"`
}

// CloneLaunchBinding returns an independent copy suitable for long-lived
// monitor state.
func CloneLaunchBinding(binding *LaunchBinding) *LaunchBinding {
	if binding == nil {
		return nil
	}
	cloned := *binding
	return &cloned
}

// CaptureLaunchBinding captures only CAAM's documented, non-secret profile
// identity. No other environment variable is inspected or persisted.
func CaptureLaunchBinding(provider string) *LaunchBinding {
	identifier := strings.TrimSpace(os.Getenv(caamProfileEnv))
	if identifier == "" {
		return nil
	}
	return &LaunchBinding{
		Provider:   canonicalLaunchProvider(provider),
		Launcher:   caamLaunchBinding,
		Identifier: identifier,
	}
}

func canonicalLaunchProvider(provider string) string {
	return string(agentpkg.AgentType(provider).Canonical())
}

func (binding *LaunchBinding) displayName() string {
	if binding == nil {
		return "unknown"
	}
	return fmt.Sprintf("%s:%s/%s", binding.Launcher, binding.Provider, binding.Identifier)
}

func validateLaunchBinding(provider string, binding *LaunchBinding) error {
	if binding == nil {
		return nil
	}
	if strings.TrimSpace(binding.Launcher) != caamLaunchBinding {
		return fmt.Errorf("unsupported launch binding %s", binding.displayName())
	}
	if strings.TrimSpace(binding.Provider) == "" {
		return fmt.Errorf("launch binding %s has no provider", binding.displayName())
	}
	if strings.TrimSpace(binding.Identifier) == "" {
		return fmt.Errorf("launch binding %s has no identifier", binding.displayName())
	}
	for _, value := range []string{binding.Provider, binding.Identifier} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("launch binding %s contains control characters", binding.displayName())
			}
		}
	}
	expected := canonicalLaunchProvider(provider)
	actual := canonicalLaunchProvider(binding.Provider)
	if expected == "" || expected == string(agentpkg.AgentTypeUnknown) {
		return fmt.Errorf("cannot resolve launch binding %s for unknown provider %q", binding.displayName(), provider)
	}
	if actual != expected {
		return fmt.Errorf("launch binding %s is scoped to provider %q, not %q", binding.displayName(), actual, expected)
	}
	return nil
}

type launchBindingPreflight func(context.Context, string, *LaunchBinding) error

func caamBinaryPath(configured string) string {
	if binary := strings.TrimSpace(configured); binary != "" {
		return binary
	}
	return caamLaunchBinding
}

func preflightCAAMLaunchBinding(ctx context.Context, binary string, binding *LaunchBinding) error {
	cmd := exec.CommandContext(ctx, binary, "shallow-spawn", binding.Identifier, "--print-env")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// PrepareLaunchCommand resolves a persisted launch binding before returning a
// command that re-enters that profile. A nil binding preserves legacy launch
// behavior while explicitly reporting unknown affinity.
func PrepareLaunchCommand(
	ctx context.Context,
	provider string,
	configuredCAAMBinary string,
	binding *LaunchBinding,
	command string,
) (string, LaunchAffinity, error) {
	return prepareLaunchCommand(ctx, provider, configuredCAAMBinary, binding, command, preflightCAAMLaunchBinding)
}

func prepareLaunchCommand(
	ctx context.Context,
	provider string,
	configuredCAAMBinary string,
	binding *LaunchBinding,
	command string,
	preflight launchBindingPreflight,
) (string, LaunchAffinity, error) {
	if binding == nil {
		return command, LaunchAffinityUnknown, nil
	}
	if ctx == nil {
		return "", "", errors.New("launch binding preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := validateLaunchBinding(provider, binding); err != nil {
		return "", "", err
	}
	binary := caamBinaryPath(configuredCAAMBinary)
	if err := preflight(ctx, binary, binding); err != nil {
		return "", "", fmt.Errorf("resolve launch binding %s: %w", binding.displayName(), err)
	}
	prepared := tmux.ShellQuote(binary) + " shallow-spawn " + tmux.ShellQuote(binding.Identifier) + " -- " + command
	return prepared, LaunchAffinityPreserved, nil
}

var manifestMutationMu sync.Mutex

// UpsertAgentConfig persists restart metadata for an agent added to an existing
// session. It updates only the typed manifest row and never reads pane
// environment or process state.
func UpsertAgentConfig(session, projectDir string, agent AgentConfig) error {
	if strings.TrimSpace(agent.PaneID) == "" {
		return errors.New("cannot persist agent restart metadata without a pane ID")
	}
	manifestMutationMu.Lock()
	defer manifestMutationMu.Unlock()

	manifest, err := LoadManifest(session)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		manifest = &SpawnManifest{
			Session:    session,
			ProjectDir: projectDir,
			Agents:     []AgentConfig{},
		}
	}
	if strings.TrimSpace(manifest.Session) == "" {
		manifest.Session = session
	}
	if strings.TrimSpace(manifest.ProjectDir) == "" {
		manifest.ProjectDir = projectDir
	}
	agent.LaunchBinding = CloneLaunchBinding(agent.LaunchBinding)
	for i := range manifest.Agents {
		if manifest.Agents[i].PaneID == agent.PaneID {
			manifest.Agents[i] = agent
			return SaveManifest(manifest)
		}
	}
	manifest.Agents = append(manifest.Agents, agent)
	return SaveManifest(manifest)
}
