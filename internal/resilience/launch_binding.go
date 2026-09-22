package resilience

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	caamLaunchBinding = "caam"
	caamProfileEnv    = "SHALLOW_PROFILE"
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
	// caam exec()s the argv after "--" directly, without a shell, but the
	// agent command is a shell string: templates carry env assignments
	// (CODEX_SYSTEM_PROMPT="$(cat …)" codex …, GEMINI_SYSTEM_MD=… gemini),
	// launch prefixes such as systemd-run, and operators write `a && b`. Hand
	// the whole string to one shell running under the profile so every part
	// of it inherits the profile's HOME. Passing it bare would either fail to
	// exec an env assignment or, worse, run the part after `&&` OUTSIDE the
	// profile while the restart reported the affinity as preserved.
	return wrapLaunchBindingCommand(binary, binding, command), LaunchAffinityPreserved, nil
}

func wrapLaunchBindingCommand(binary string, binding *LaunchBinding, command string) string {
	if binding == nil {
		return command
	}
	return tmux.ShellQuote(binary) + " shallow-spawn " + tmux.ShellQuote(binding.Identifier) +
		" -- sh -c " + tmux.ShellQuote(command)
}

// AgentLaunchPlan holds a validated snapshot of a pane's durable launch inputs.
// Preflight performs no filesystem provisioning, so callers can validate an
// entire checkpoint before replacing a session or while reporting a dry run.
type AgentLaunchPlan struct {
	spec       tmux.AgentLaunchSpec
	cfg        *config.Config
	binding    *LaunchBinding
	caamBinary string
}

// PreflightAgentLaunchSpec checks that saved launch settings can be replayed.
// Explicit environment values are never recovered from ambient process state:
// their omission is an error even if a variable with the same name exists now.
func PreflightAgentLaunchSpec(ctx context.Context, cfg *config.Config, spec tmux.AgentLaunchSpec, projectDir string) (*AgentLaunchPlan, error) {
	return preflightAgentLaunchSpec(ctx, cfg, spec, projectDir, swarm.CheckClaudeCredentialIsolation)
}

func preflightAgentLaunchSpec(ctx context.Context, cfg *config.Config, spec tmux.AgentLaunchSpec, projectDir string, checkIsolation func() error) (*AgentLaunchPlan, error) {
	if ctx == nil {
		return nil, errors.New("agent launch preflight requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.ValidateReplay(spec.AgentType); err != nil {
		return nil, err
	}
	if err := verifyAgentSystemPrompt(spec); err != nil {
		return nil, err
	}
	if spec.ClaudeIsolateCredentials {
		if err := checkIsolation(); err != nil {
			return nil, fmt.Errorf("preflight saved Claude credential isolation: %w", err)
		}
	}
	plan := &AgentLaunchPlan{spec: spec}
	plan.spec.OmittedEnv = append([]string(nil), spec.OmittedEnv...)
	if cfg == nil && (spec.ClaudeIsolateCredentials || spec.CAAMProfile != "") {
		var err error
		cfg, err = config.LoadMergedStrict(projectDir, "")
		if err != nil {
			return nil, fmt.Errorf("loading agent replay configuration: %w", err)
		}
	}
	if cfg != nil {
		copy := *cfg
		plan.cfg = &copy
		plan.caamBinary = cfg.Integrations.CAAM.BinaryPath
	}
	if spec.ClaudeIsolateCredentials {
		// Resolve only the saved file reference, not a new default from the
		// current configuration. The helper checks usability without exposing
		// its contents in the generated command or diagnostic messages.
		tokenFile, err := swarm.ResolveClaudeSetupTokenFile(spec.ClaudeTokenFile)
		if err != nil {
			return nil, fmt.Errorf("preflight saved Claude token file: %w", err)
		}
		plan.cfg.Agents.ClaudeIsolateCredentials = true
		plan.cfg.Agents.ClaudeTokenFile = tokenFile
	}
	if spec.CAAMProfile != "" {
		plan.binding = &LaunchBinding{
			Provider: string(spec.AgentType.Canonical()), Launcher: caamLaunchBinding, Identifier: spec.CAAMProfile,
		}
		plan.caamBinary = caamBinaryPath(plan.caamBinary)
		preflightCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _, err := PrepareLaunchCommand(preflightCtx, string(spec.AgentType), plan.caamBinary, plan.binding, spec.Command)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	return plan, ctx.Err()
}

func verifyAgentSystemPrompt(spec tmux.AgentLaunchSpec) error {
	if spec.SystemPromptFile == "" {
		return nil
	}
	info, err := os.Stat(spec.SystemPromptFile)
	if err != nil {
		return fmt.Errorf("inspect saved system prompt %s: %w", spec.SystemPromptFile, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("saved system prompt %s is not a regular file", spec.SystemPromptFile)
	}
	f, err := os.Open(spec.SystemPromptFile)
	if err != nil {
		return fmt.Errorf("open saved system prompt %s: %w", spec.SystemPromptFile, err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("saved system prompt %s is not a readable regular file", spec.SystemPromptFile)
	}
	// Prepared persona prompts are small text artifacts. Bound the read even
	// when a saved path has been replaced, without ever including its contents
	// in diagnostics or regenerating it from a changed persona registry.
	const maxPromptBytes = 1 << 20
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, maxPromptBytes+1))
	if err != nil {
		return fmt.Errorf("read saved system prompt %s: %w", spec.SystemPromptFile, err)
	}
	if n > maxPromptBytes {
		return fmt.Errorf("saved system prompt %s exceeds 1 MiB", spec.SystemPromptFile)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), spec.SystemPromptSHA256) {
		return fmt.Errorf("saved system prompt %s changed since the agent was launched", spec.SystemPromptFile)
	}
	return nil
}

// Prepare provisions destination-specific isolation after preflight has passed.
// It returns the launch command without a working-directory prefix. Callers keep
// the original specification on the pane; persisting this prepared command
// would nest account wrappers and reuse a predecessor's private configuration.
func (plan *AgentLaunchPlan) Prepare(ctx context.Context, projectDir, session string, paneIndex int) (string, error) {
	if plan == nil {
		return "", errors.New("agent launch plan is required")
	}
	if ctx == nil {
		return "", errors.New("agent launch preparation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("agent launch destination: %w", err)
	}
	if paneIndex < 0 {
		return "", errors.New("agent launch pane index must be non-negative")
	}
	if err := verifyAgentSystemPrompt(plan.spec); err != nil {
		return "", err
	}
	command := plan.spec.Command
	if plan.spec.ClaudeIsolateCredentials {
		env, err := swarm.ProvisionClaudeIsolation(plan.cfg, projectDir, session, paneIndex)
		if err != nil {
			return "", fmt.Errorf("provisioning saved Claude credential isolation: %w", err)
		}
		command = env.ApplyToCommand(command)
	}
	if plan.spec.AgentMailProject != "" {
		command = "AGENT_MAIL_PROJECT=" + tmux.ShellQuote(plan.spec.AgentMailProject) + " " + command
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return wrapLaunchBindingCommand(plan.caamBinary, plan.binding, command), nil
}

// PrepareAgentLaunchSpec is the shared replay path for operations that do not
// need to separate read-only preflight from destination provisioning.
func PrepareAgentLaunchSpec(ctx context.Context, cfg *config.Config, spec tmux.AgentLaunchSpec, projectDir, session string, paneIndex int) (string, error) {
	plan, err := PreflightAgentLaunchSpec(ctx, cfg, spec, projectDir)
	if err != nil {
		return "", err
	}
	return plan.Prepare(ctx, projectDir, session, paneIndex)
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
