package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/shellword"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type restartLaunchPlan struct {
	Commands    map[string]string
	Affinity    map[string]resilience.LaunchAffinity
	Specs       map[string]tmux.AgentLaunchSpec
	Replay      map[string]*resilience.AgentLaunchPlan
	Directories map[string]string
	Indices     map[string]int
	Preflight   *RestartPanePreflight
	// DeliveryCommands are exact, read-only previews of the eventual input.
	// Per-pane handoffs carry runtime values or long commands only in memory.
	// They are closed for successful, failed, and dry-run restart plans.
	DeliveryCommands map[string]string
	handoffs         map[string]*tmux.LaunchEnvironment
}

// RestartPanePreflight binds an in-memory preview to the physical panes and
// launch inputs that were observed. It is deliberately opaque and must never
// be serialized: callers may carry it from confirmation to execution, but may
// not turn a stale logical selector into permission to restart another pane.
type RestartPanePreflight struct {
	session string
	targets []restartPaneSnapshot
}

type restartPaneSnapshot struct {
	pane       tmux.Pane
	launchSpec *tmux.AgentLaunchSpec
	workingDir string
}

func (*RestartPanePreflight) String() string   { return "RestartPanePreflight{redacted}" }
func (*RestartPanePreflight) GoString() string { return "RestartPanePreflight{redacted}" }

func cloneRestartPane(pane tmux.Pane) tmux.Pane {
	pane.Tags = append([]string(nil), pane.Tags...)
	return pane
}

func cloneRestartLaunchSpec(spec *tmux.AgentLaunchSpec) *tmux.AgentLaunchSpec {
	if spec == nil {
		return nil
	}
	copy := *spec
	if spec.OmittedEnv != nil {
		copy.OmittedEnv = append([]string{}, spec.OmittedEnv...)
	}
	return &copy
}

// Process and topology evidence must still describe the original target. A
// foreground change matters even when the shell PID is unchanged: another
// recovery may have started a new agent in that shell during preflight.
func restartPaneSnapshotMatches(expected, current tmux.Pane) bool {
	return expected.ID != "" && expected.ID == current.ID && expected.PID > 0 && expected.PID == current.PID &&
		expected.Index == current.Index && expected.WindowIndex == current.WindowIndex &&
		restartPaneAgentType(expected) == restartPaneAgentType(current) &&
		expected.Command == current.Command && expected.Dead == current.Dead &&
		expected.Service == current.Service && expected.ServiceManager == current.ServiceManager &&
		expected.RotationToken == current.RotationToken
}

func (expected restartPaneSnapshot) matches(current tmux.Pane) bool {
	return restartPaneSnapshotMatches(expected.pane, current) &&
		(expected.launchSpec != nil || expected.pane.Variant == current.Variant)
}

func (preflight *RestartPanePreflight) selectTargets(session string, panes []tmux.Pane) ([]tmux.Pane, error) {
	if preflight == nil || preflight.session != session || len(preflight.targets) == 0 {
		return nil, errors.New("restart preflight does not match the selected session")
	}
	byID := make(map[string]tmux.Pane, len(panes))
	for _, pane := range panes {
		if _, duplicate := byID[pane.ID]; duplicate {
			return nil, errors.New("restart pane observation contains duplicate physical identities")
		}
		byID[pane.ID] = pane
	}
	targets := make([]tmux.Pane, 0, len(preflight.targets))
	for _, expected := range preflight.targets {
		current, exists := byID[expected.pane.ID]
		if !exists {
			return nil, fmt.Errorf("restart pane %s is no longer present in the selected session", expected.pane.ID)
		}
		if !expected.matches(current) {
			return nil, fmt.Errorf("restart pane %s identity changed since preflight", expected.pane.ID)
		}
		targets = append(targets, cloneRestartPane(current))
	}
	return targets, nil
}

func (preflight *RestartPanePreflight) compare(current *RestartPanePreflight) error {
	if preflight == nil || current == nil || preflight.session != current.session || len(preflight.targets) != len(current.targets) {
		return errors.New("restart targets changed since preflight")
	}
	for i, expected := range preflight.targets {
		observed := current.targets[i]
		if !expected.matches(observed.pane) {
			return fmt.Errorf("restart pane %s identity changed since preflight", expected.pane.ID)
		}
		if !reflect.DeepEqual(expected.launchSpec, observed.launchSpec) {
			return fmt.Errorf("restart pane %s launch specification changed since preflight", expected.pane.ID)
		}
		if expected.workingDir != observed.workingDir {
			return fmt.Errorf("restart pane %s working directory changed since preflight", expected.pane.ID)
		}
	}
	return nil
}

func (preflight *RestartPanePreflight) observe(ctx context.Context, deps RestartPaneDependencies) error {
	panes, err := deps.ListPanes(ctx, preflight.session)
	if err != nil {
		return fmt.Errorf("recheck restart pane identities: %w", err)
	}
	if _, err := preflight.selectTargets(preflight.session, panes); err != nil {
		return err
	}
	for _, expected := range preflight.targets {
		if err := expected.observeLaunch(ctx, deps); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (preflight *RestartPanePreflight) target(paneID string) (restartPaneSnapshot, error) {
	for _, expected := range preflight.targets {
		if expected.pane.ID == paneID {
			return expected, nil
		}
	}
	return restartPaneSnapshot{}, errors.New("restart target is not part of the preflighted physical panes")
}

func (expected restartPaneSnapshot) observeBeforeRespawn(ctx context.Context, session string, deps RestartPaneDependencies) error {
	preflight := &RestartPanePreflight{session: session, targets: []restartPaneSnapshot{expected}}
	return preflight.observe(ctx, deps)
}

// observeReplacement keeps every later lifecycle stage bound to the exact
// shell created by this restart. The expected PID is never adopted from a fresh
// lookup: a newer process belongs to another lifecycle operation. Tmux repeats
// the PID/ownership guard in the same command queue as each actual mutation.
func (preflight *RestartPanePreflight) observeReplacement(ctx context.Context, deps RestartPaneDependencies, paneID string, expectedPID int, requireShell bool, published *tmux.AgentLaunchSpec) error {
	panes, err := deps.ListPanes(ctx, preflight.session)
	if err != nil {
		return fmt.Errorf("recheck replacement pane %s: %w", paneID, err)
	}
	return preflight.observeReplacementPanes(ctx, deps, panes, paneID, expectedPID, requireShell, published)
}

func (preflight *RestartPanePreflight) observeReplacementPanes(ctx context.Context, deps RestartPaneDependencies, panes []tmux.Pane, paneID string, expectedPID int, requireShell bool, published *tmux.AgentLaunchSpec) error {
	expected, err := preflight.target(paneID)
	if err != nil {
		return err
	}
	if expectedPID <= 0 || expectedPID == expected.pane.PID {
		return fmt.Errorf("restart pane %s has no verified replacement process", paneID)
	}
	found := false
	for _, pane := range panes {
		if pane.ID == paneID {
			if found {
				return errors.New("replacement pane observation contains duplicate physical identities")
			}
			found = true
		}
	}
	for _, current := range panes {
		if current.ID != paneID {
			continue
		}
		expected.pane.PID = expectedPID
		expected.pane.Command = current.Command
		expected.pane.Dead = false
		if published != nil {
			expected.launchSpec = cloneRestartLaunchSpec(published)
			// Publication canonicalizes aliases and omits an empty key list.
			// Compare the record that SetPaneLaunchSpec actually writes.
			expected.launchSpec.AgentType = expected.launchSpec.AgentType.Canonical()
			expected.launchSpec.OmittedEnv = append([]string(nil), published.OmittedEnv...)
		}
		if !expected.matches(current) || current.IsServicePane() || current.RotationToken != "" {
			return fmt.Errorf("restart pane %s replacement identity changed", paneID)
		}
		if requireShell && !current.RunsBareShell() {
			return fmt.Errorf("restart pane %s is no longer at the replacement shell; launch was withheld", paneID)
		}
		return expected.observeLaunch(ctx, deps)
	}
	return fmt.Errorf("restart pane %s replacement is no longer present in the selected session", paneID)
}

func (expected restartPaneSnapshot) observeLaunch(ctx context.Context, deps RestartPaneDependencies) error {
	if !restartTargetIsAgent(restartPaneAgentType(expected.pane)) {
		return nil
	}
	spec, err := deps.ReadLaunchSpec(ctx, expected.pane.ID)
	if err != nil {
		return fmt.Errorf("recheck restart pane %s launch specification: %w", expected.pane.ID, err)
	}
	if !reflect.DeepEqual(expected.launchSpec, spec) {
		return fmt.Errorf("restart pane %s launch specification changed since preflight", expected.pane.ID)
	}
	if expected.workingDir != "" {
		dir, err := deps.PaneWorkingDir(ctx, expected.pane.ID)
		if err != nil {
			return fmt.Errorf("recheck restart pane %s working directory: %w", expected.pane.ID, err)
		}
		if dir != expected.workingDir {
			return fmt.Errorf("restart pane %s working directory changed since preflight", expected.pane.ID)
		}
	}
	return ctx.Err()
}

func (plan *restartLaunchPlan) close() {
	for _, handoff := range plan.handoffs {
		_ = handoff.Close()
	}
}

// prepareDelivery belongs to the destructive restart path. Other lifecycle
// operations reuse launch composition while retaining their existing shell;
// they must not create or leak unused restart-only FIFO handoffs.
func (plan *restartLaunchPlan) prepareDelivery(ctx context.Context, session string, environment map[string]string) error {
	plan.DeliveryCommands = make(map[string]string, len(plan.Commands))
	plan.handoffs = make(map[string]*tmux.LaunchEnvironment)
	for key, command := range plan.Commands {
		if replay := plan.Replay[key]; replay != nil {
			var err error
			command, err = replay.Preview(ctx, plan.Directories[key], session, plan.Indices[key])
			if err == nil {
				command, err = tmux.BuildPaneCommand(plan.Directories[key], command)
			}
			if err != nil {
				return fmt.Errorf("preflight pane %s launch delivery: %w", key, err)
			}
		}
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("preflight pane %s launch delivery: command is empty", key)
		}
		if _, err := tmux.SanitizePaneCommand(command); err != nil {
			return fmt.Errorf("preflight pane %s launch delivery: command contains control characters", key)
		}
		plan.DeliveryCommands[key] = command
		if len(environment) == 0 && len(command) <= tmux.MaxTypedLaunchCommandBytes {
			continue
		}
		handoff, err := tmux.NewLaunchEnvironment(ctx, environment)
		if err != nil {
			return fmt.Errorf("preflight pane %s private launch handoff: %w; run recovery on the tmux host when the saved launch needs a private handoff", key, err)
		}
		plan.handoffs[key] = handoff
	}
	if len(plan.Commands) > 0 {
		if err := tmux.DefaultClient.PreflightRestartShellContext(ctx); err != nil {
			return fmt.Errorf("preflight replacement shell: %w", err)
		}
	}
	return ctx.Err()
}

func prepareRestartLaunchPlan(
	ctx context.Context,
	session string,
	panes []tmux.Pane,
	multiWindow bool,
	cfg *config.Config,
	override restartLaunchOverride,
	deps RestartPaneDependencies,
	environment map[string]string,
) (restartLaunchPlan, error) {
	plan := restartLaunchPlan{
		Commands:    make(map[string]string),
		Affinity:    make(map[string]resilience.LaunchAffinity),
		Specs:       make(map[string]tmux.AgentLaunchSpec),
		Replay:      make(map[string]*resilience.AgentLaunchPlan),
		Directories: make(map[string]string),
		Indices:     make(map[string]int),
		Preflight:   &RestartPanePreflight{session: session, targets: make([]restartPaneSnapshot, len(panes))},
	}
	if environment != nil {
		if len(panes) != 1 || !restartTargetIsAgent(restartPaneAgentType(panes[0])) {
			return plan, errors.New("restart environment recovery requires exactly one agent pane")
		}
		if err := tmux.ValidateLaunchEnvironment(environment); err != nil {
			return plan, err
		}
	}
	manifest, err := deps.LoadManifest(session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return plan, fmt.Errorf("load launch affinity manifest: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		manifest = nil
	}
	caamBinary := ""
	if cfg != nil {
		caamBinary = cfg.Integrations.CAAM.BinaryPath
	}
	for paneIndex, pane := range panes {
		plan.Preflight.targets[paneIndex].pane = cloneRestartPane(pane)
		resolvedType := restartPaneAgentType(pane)
		if !restartTargetIsAgent(resolvedType) {
			continue
		}
		key := paneTargetKey(pane, multiWindow)
		saved, err := deps.ReadLaunchSpec(ctx, pane.ID)
		if err != nil {
			return plan, fmt.Errorf("read pane %s launch specification: %w", key, err)
		}
		plan.Preflight.targets[paneIndex].launchSpec = cloneRestartLaunchSpec(saved)
		if saved != nil {
			if err := saved.Validate(tmux.AgentType(resolvedType)); err != nil {
				return plan, fmt.Errorf("validate pane %s launch specification: %w", key, err)
			}
			spec, err := restartSavedLaunchSpec(cfg, *saved, resolvedType, override)
			if err != nil {
				return plan, fmt.Errorf("compose saved launch for pane %s: %w", key, err)
			}
			if err := spec.ValidateReplayWithEnvironment(tmux.AgentType(resolvedType), environment); err != nil {
				return plan, fmt.Errorf("preflight pane %s saved launch: %w", key, err)
			}
			dir, err := deps.PaneWorkingDir(ctx, pane.ID)
			if err != nil {
				return plan, fmt.Errorf("observe pane %s working directory: %w", key, err)
			}
			if !filepath.IsAbs(dir) {
				return plan, fmt.Errorf("pane %s has no absolute working directory for saved launch replay", key)
			}
			plan.Preflight.targets[paneIndex].workingDir = dir
			// Only this execution copy may omit the markers, after the explicit
			// values have satisfied every saved requirement. Specs retains the
			// original markers for durable publication; automatic replay still
			// refuses them on the next restart. Runtime values are delivered by
			// the private handoff prepared below, never by this shell command.
			replaySpec := spec
			replaySpec.OmittedEnv = nil
			replay, err := resilience.PreflightAgentLaunchSpec(ctx, cfg, replaySpec, dir)
			if err != nil {
				return plan, fmt.Errorf("preflight pane %s saved launch: %w", key, err)
			}
			plan.Specs[key] = spec
			plan.Replay[key] = replay
			plan.Directories[key] = dir
			plan.Indices[key] = pane.NTMIndex
			if pane.NTMIndex <= 0 {
				plan.Indices[key] = pane.Index
			}
			plan.Commands[key] = spec.Command
			plan.Affinity[key] = resilience.LaunchAffinityUnknown
			if spec.CAAMProfile != "" {
				plan.Affinity[key] = resilience.LaunchAffinityPreserved
			}
			continue
		}
		if environment != nil {
			return plan, fmt.Errorf("pane %s has no saved launch specification to validate recovery environment against", key)
		}
		command, err := restartAgentLaunchCommandWithOverride(cfg, resolvedType, pane.Variant, override)
		if err != nil {
			return plan, fmt.Errorf("compose relaunch command for pane %s: %w", key, err)
		}
		binding := restartLaunchBindingForPane(manifest, pane, resolvedType)
		prepared, affinity, err := deps.PrepareLaunchCommand(ctx, resolvedType, caamBinary, binding, command)
		if err != nil {
			return plan, fmt.Errorf("preflight pane %s launch affinity: %w", key, err)
		}
		plan.Commands[key] = prepared
		plan.Affinity[key] = affinity
		spec := tmux.AgentLaunchSpec{Version: tmux.AgentLaunchSpecVersion, AgentType: tmux.AgentType(resolvedType).Canonical(), Command: command}
		if binding != nil {
			spec.CAAMProfile = binding.Identifier
		}
		// Legacy titles cannot prove which settings an opaque configured
		// command used. Only describe explicit overrides applied above;
		// the captured command remains authoritative for all other settings.
		if override.Model != "" {
			spec.Model, spec.ModelAlias = override.Model, override.Model
			if cfg != nil {
				if model := cfg.Models.GetModelName(resolvedType, override.Model); model != "" {
					spec.Model = model
				}
			}
		}
		if override.Effort != "" {
			spec.ReasoningEffort = override.Effort
		}
		if override.Args != "" {
			spec.Model, spec.ModelAlias, spec.ReasoningEffort = "", "", ""
		}
		if err := spec.ValidateReplay(tmux.AgentType(resolvedType)); err != nil {
			return plan, fmt.Errorf("record pane %s relaunch command: %w", key, err)
		}
		plan.Specs[key] = spec
	}
	return plan, nil
}

func restartPaneWorkingDirectory(ctx context.Context, paneID string) (string, error) {
	// RunContext trims transport whitespace. Enclose the path so whitespace
	// belonging to a directory name survives that trimming unchanged.
	out, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(paneID), "[#{pane_current_path}]")
	if err != nil {
		return "", err
	}
	if len(out) < 3 || out[0] != '[' || out[len(out)-1] != ']' {
		return "", errors.New("cannot observe exact restart working directory")
	}
	return out[1 : len(out)-1], nil
}

// restartSavedLaunchSpec preserves the actual creation-time command. Explicit
// overrides may only replace flags in a direct, simple agent invocation whose
// argument grammar is known; opaque shell programs require a manual relaunch.
func restartSavedLaunchSpec(cfg *config.Config, saved tmux.AgentLaunchSpec, resolvedType string, override restartLaunchOverride) (tmux.AgentLaunchSpec, error) {
	spec := saved
	spec.OmittedEnv = append([]string(nil), saved.OmittedEnv...)
	if override.empty() {
		return spec, nil
	}
	if err := validateRestartOverrideCommand(saved.Command, resolvedType); err != nil {
		return spec, err
	}
	if err := validateRestartExtraArguments(override.Args, resolvedType); err != nil {
		return spec, err
	}
	resolved := override
	if override.Model != "" {
		if cfg != nil {
			if model := cfg.Models.GetModelName(resolvedType, override.Model); model != "" {
				resolved.Model = model
			}
		}
		spec.Model, spec.ModelAlias = resolved.Model, override.Model
	}
	if override.Effort != "" {
		spec.ReasoningEffort = override.Effort
	}
	flags, err := restartOverrideAppendFlags(resolvedType, resolved, true, true)
	if err != nil {
		return spec, err
	}
	spec.Command, err = removeRestartScalarOverrides(spec.Command, resolvedType, override.Model != "", override.Effort != "")
	if err != nil {
		return spec, err
	}
	spec.Command += flags
	if override.Args != "" {
		spec.Command += " " + override.Args
		// Raw arguments may change the persona or provider-specific settings.
		// Keep the exact command authoritative and
		// avoid claiming descriptive settings that are no longer provable.
		spec.Model, spec.ModelAlias, spec.ReasoningEffort, spec.Persona = "", "", "", ""
	}
	// Replay completeness is checked by prepareRestartLaunchPlan, where the
	// operator's explicit environment is available. Composition must preserve
	// the omission markers rather than rejecting or erasing them here.
	return spec, spec.Validate(tmux.AgentType(resolvedType))
}

func validateRestartExtraArguments(raw, resolvedType string) error {
	words, err := restartSimpleShellWords(raw)
	if err != nil {
		return fmt.Errorf("restart agent arguments must be literal shell words: %w", err)
	}
	if restartArgumentsContainScalarOverride(words, resolvedType) {
		return errors.New("restart agent arguments cannot override model or reasoning effort; use --restart-model=model@effort")
	}
	for _, word := range words {
		if word == "--" {
			return errors.New("restart agent arguments cannot end option parsing")
		}
		if restartSubcommandToken(word) {
			return errors.New("restart agent arguments cannot select an agent subcommand")
		}
	}
	return nil
}

func validateRestartOverrideCommand(command, resolvedType string) error {
	words, err := restartSimpleShellWords(command)
	if err != nil {
		return fmt.Errorf("saved launch is an opaque shell command; restart without overrides or launch the replacement explicitly: %w", err)
	}
	for len(words) > 0 {
		key, _, assignment := strings.Cut(words[0], "=")
		if !assignment || !restartEnvironmentName(key) {
			break
		}
		words = words[1:]
	}
	if len(words) == 0 {
		return errors.New("saved launch has no direct agent executable")
	}
	binary := filepath.Base(words[0])
	if ResolveAgentType(binary) != resolvedType {
		return errors.New("restart overrides require a direct agent executable; saved launch uses a wrapper or custom command")
	}
	if len(words) > 1 && !strings.HasPrefix(words[1], "-") {
		return errors.New("restart overrides cannot target a saved agent subcommand or positional prompt")
	}
	for _, word := range words[1:] {
		if word == "--" {
			return errors.New("restart overrides cannot follow the saved end-of-options marker")
		}
		if restartSubcommandToken(word) {
			return errors.New("restart overrides cannot target a saved agent subcommand")
		}
	}
	return nil
}

func restartSubcommandToken(word string) bool {
	switch word {
	case "exec", "resume", "review", "login", "logout", "mcp", "mcp-server", "app-server", "completion", "completions",
		"doctor", "update", "install", "uninstall", "auth", "config", "models", "run", "serve", "server":
		return true
	default:
		return false
	}
}

func restartArgumentsContainScalarOverride(words []string, resolvedType string) bool {
	for i, word := range words {
		flag, value, inline := strings.Cut(word, "=")
		if flag == "--model" || flag == "--effort" || flag == "--reasoning-effort" || flag == "--thinking" || flag == "-m" {
			return true
		}
		if resolvedType == "codex" && strings.HasPrefix(word, "-m") && !strings.HasPrefix(word, "--") && len(word) > 2 {
			return true
		}
		if setting, attached, configOption := restartCodexConfigSetting(word); resolvedType == "codex" && configOption {
			value, inline = setting, attached
			if !inline && i+1 < len(words) {
				value = words[i+1]
			}
			key, _, _ := strings.Cut(value, "=")
			if strings.TrimSpace(key) == "model" || strings.TrimSpace(key) == "model_reasoning_effort" {
				return true
			}
		}
	}
	return false
}

func restartCodexConfigSetting(word string) (setting string, inline, ok bool) {
	switch {
	case word == "-c", word == "--config":
		return "", false, true
	case strings.HasPrefix(word, "--config="):
		return strings.TrimPrefix(word, "--config="), true, true
	case strings.HasPrefix(word, "-c") && !strings.HasPrefix(word, "--") && len(word) > 2:
		return strings.TrimPrefix(word[2:], "="), true, true
	default:
		return "", false, false
	}
}

func restartEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// removeRestartScalarOverrides removes complete option/value token spans before
// appending a replacement. In particular Codex's scalar --model/-m rejects
// duplicate occurrences; sending a duplicate would kill the replacement CLI.
func removeRestartScalarOverrides(command, resolvedType string, model, effort bool) (string, error) {
	words, err := shellword.Literal(command)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	copiedThrough := 0
	for i := 0; i < len(words); i++ {
		word := words[i]
		flag, _, inline := strings.Cut(word.Value, "=")
		remove := model && (flag == "--model" || (resolvedType == "codex" || resolvedType == "gemini") && flag == "-m")
		if model && resolvedType == "codex" && strings.HasPrefix(word.Value, "-m") && !strings.HasPrefix(word.Value, "--") && len(word.Value) > 2 {
			remove, inline = true, true
		}
		if effort {
			switch resolvedType {
			case "claude", "grok":
				remove = remove || flag == "--effort" || resolvedType == "grok" && flag == "--reasoning-effort"
			case "omp":
				remove = remove || flag == "--thinking"
			}
		}
		if resolvedType == "codex" && (model || effort) {
			if setting, attached, configOption := restartCodexConfigSetting(word.Value); configOption {
				if !attached && i+1 < len(words) {
					setting = words[i+1].Value
				}
				key, _, _ := strings.Cut(setting, "=")
				remove = model && strings.TrimSpace(key) == "model" || effort && strings.TrimSpace(key) == "model_reasoning_effort"
				inline = attached
			}
		}
		if !remove {
			continue
		}
		if command[word.Start] == '\'' || command[word.Start] == '"' {
			return "", errors.New("saved scalar option is quoted and may be a literal value for another option")
		}
		if i > 0 && words[i-1].End > copiedThrough && strings.HasPrefix(words[i-1].Value, "-") &&
			!strings.Contains(words[i-1].Value, "=") && !restartKnownBooleanOption(words[i-1].Value) {
			return "", errors.New("saved scalar option is ambiguous with a preceding custom option value")
		}
		end := word.End
		if !inline {
			if i+1 >= len(words) || strings.HasPrefix(words[i+1].Value, "-") {
				return "", errors.New("saved scalar agent option has no literal value")
			}
			i++
			end = words[i].End
		}
		result.WriteString(command[copiedThrough:word.Start])
		copiedThrough = end
	}
	result.WriteString(command[copiedThrough:])
	return strings.TrimSpace(result.String()), nil
}

func restartKnownBooleanOption(word string) bool {
	switch word {
	case "--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox", "--full-auto", "--no-alt-screen",
		"--always-approve", "--auto-approve", "--yolo", "-y", "--continue", "--verbose", "--search", "--oss":
		return true
	default:
		return false
	}
}

func restartSimpleShellWords(command string) ([]string, error) {
	tokens, err := shellword.Literal(command)
	if err != nil {
		return nil, err
	}
	words := make([]string, len(tokens))
	for i, token := range tokens {
		words[i] = token.Value
	}
	return words, nil
}

func restartLaunchBindingForPane(
	manifest *resilience.SpawnManifest,
	pane tmux.Pane,
	resolvedType string,
) *resilience.LaunchBinding {
	if manifest == nil {
		return nil
	}
	for i := range manifest.Agents {
		if manifest.Agents[i].PaneID == pane.ID {
			return resilience.CloneLaunchBinding(manifest.Agents[i].LaunchBinding)
		}
	}

	logicalIndex := pane.NTMIndex
	if logicalIndex <= 0 {
		logicalIndex = pane.Index
	}
	canonicalType := string(agentpkg.AgentType(resolvedType).Canonical())
	var candidate *resilience.LaunchBinding
	found := false
	for i := range manifest.Agents {
		entry := &manifest.Agents[i]
		if entry.PaneIndex != logicalIndex ||
			string(agentpkg.AgentType(entry.Type).Canonical()) != canonicalType {
			continue
		}
		if pane.Variant != "" && entry.Model != "" && !strings.EqualFold(pane.Variant, entry.Model) {
			continue
		}
		if found {
			return nil
		}
		found = true
		candidate = entry.LaunchBinding
	}
	return resilience.CloneLaunchBinding(candidate)
}
