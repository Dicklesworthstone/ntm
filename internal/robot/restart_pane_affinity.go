package robot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agentpkg "github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

type restartLaunchPlan struct {
	Commands    map[string]string
	Affinity    map[string]resilience.LaunchAffinity
	Specs       map[string]tmux.AgentLaunchSpec
	Replay      map[string]*resilience.AgentLaunchPlan
	Directories map[string]string
	Indices     map[string]int
}

func prepareRestartLaunchPlan(
	ctx context.Context,
	session string,
	panes []tmux.Pane,
	multiWindow bool,
	cfg *config.Config,
	override restartLaunchOverride,
	deps RestartPaneDependencies,
) (restartLaunchPlan, error) {
	plan := restartLaunchPlan{
		Commands:    make(map[string]string),
		Affinity:    make(map[string]resilience.LaunchAffinity),
		Specs:       make(map[string]tmux.AgentLaunchSpec),
		Replay:      make(map[string]*resilience.AgentLaunchPlan),
		Directories: make(map[string]string),
		Indices:     make(map[string]int),
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
	for _, pane := range panes {
		resolvedType := restartPaneAgentType(pane)
		if !restartTargetIsAgent(resolvedType) {
			continue
		}
		key := paneTargetKey(pane, multiWindow)
		saved, err := deps.ReadLaunchSpec(ctx, pane.ID)
		if err != nil {
			return plan, fmt.Errorf("read pane %s launch specification: %w", key, err)
		}
		if saved != nil {
			if err := saved.Validate(tmux.AgentType(resolvedType)); err != nil {
				return plan, fmt.Errorf("validate pane %s launch specification: %w", key, err)
			}
			spec, err := restartSavedLaunchSpec(cfg, *saved, resolvedType, override)
			if err != nil {
				return plan, fmt.Errorf("compose saved launch for pane %s: %w", key, err)
			}
			dir, err := deps.PaneWorkingDir(ctx, pane.ID)
			if err != nil {
				return plan, fmt.Errorf("observe pane %s working directory: %w", key, err)
			}
			if !filepath.IsAbs(dir) {
				return plan, fmt.Errorf("pane %s has no absolute working directory for saved launch replay", key)
			}
			replay, err := resilience.PreflightAgentLaunchSpec(ctx, cfg, spec, dir)
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
	out, err := tmux.DefaultClient.RunContext(ctx, "display-message", "-p", "-t", tmux.ExactTarget(paneID), "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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
	return spec, spec.ValidateReplay(tmux.AgentType(resolvedType))
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

type restartShellWord struct {
	value      string
	start, end int
}

// removeRestartScalarOverrides removes complete option/value token spans before
// appending a replacement. In particular Codex's scalar --model/-m rejects
// duplicate occurrences; sending a duplicate would kill the replacement CLI.
func removeRestartScalarOverrides(command, resolvedType string, model, effort bool) (string, error) {
	words, err := restartSimpleShellTokens(command)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	copiedThrough := 0
	for i := 0; i < len(words); i++ {
		word := words[i]
		flag, _, inline := strings.Cut(word.value, "=")
		remove := model && (flag == "--model" || (resolvedType == "codex" || resolvedType == "gemini") && flag == "-m")
		if model && resolvedType == "codex" && strings.HasPrefix(word.value, "-m") && !strings.HasPrefix(word.value, "--") && len(word.value) > 2 {
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
			if setting, attached, configOption := restartCodexConfigSetting(word.value); configOption {
				if !attached && i+1 < len(words) {
					setting = words[i+1].value
				}
				key, _, _ := strings.Cut(setting, "=")
				remove = model && strings.TrimSpace(key) == "model" || effort && strings.TrimSpace(key) == "model_reasoning_effort"
				inline = attached
			}
		}
		if !remove {
			continue
		}
		if command[word.start] == '\'' || command[word.start] == '"' {
			return "", errors.New("saved scalar option is quoted and may be a literal value for another option")
		}
		if i > 0 && words[i-1].end > copiedThrough && strings.HasPrefix(words[i-1].value, "-") &&
			!strings.Contains(words[i-1].value, "=") && !restartKnownBooleanOption(words[i-1].value) {
			return "", errors.New("saved scalar option is ambiguous with a preceding custom option value")
		}
		end := word.end
		if !inline {
			if i+1 >= len(words) || strings.HasPrefix(words[i+1].value, "-") {
				return "", errors.New("saved scalar agent option has no literal value")
			}
			i++
			end = words[i].end
		}
		result.WriteString(command[copiedThrough:word.start])
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
	tokens, err := restartSimpleShellTokens(command)
	if err != nil {
		return nil, err
	}
	words := make([]string, len(tokens))
	for i, token := range tokens {
		words[i] = token.value
	}
	return words, nil
}

func restartSimpleShellTokens(command string) ([]restartShellWord, error) {
	var words []restartShellWord
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	wordStart := 0
	for i, r := range command {
		if !started && r != ' ' && r != '\t' {
			wordStart = i
		}
		if escaped {
			if quote == '"' && !strings.ContainsRune("$`\"\\", r) {
				word.WriteRune('\\')
			}
			word.WriteRune(r)
			escaped, started = false, true
			continue
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '$' || r == '`' || r == '\n' || r == '\r' {
			return nil, errors.New("shell expansion or control sequence is present")
		}
		if r == '\\' {
			escaped, started = true, true
			continue
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
			continue
		}
		if strings.ContainsRune(";&|<>()#*?[]{}~", r) {
			return nil, errors.New("shell operator, expansion, or comment is present")
		}
		if r == ' ' || r == '\t' {
			if started {
				words = append(words, restartShellWord{value: word.String(), start: wordStart, end: i})
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("shell quoting is incomplete")
	}
	if started {
		words = append(words, restartShellWord{value: word.String(), start: wordStart, end: len(command)})
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
