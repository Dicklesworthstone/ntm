package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/hooks"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

func newInitCmd() *cobra.Command {
	var nonInteractive bool
	var force bool
	var noHooks bool

	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Initialize NTM for a project directory",
		Long: `Initialize NTM orchestration for a project directory.

This command will set up project-local NTM configuration and integrations.
By default, it targets the current working directory.

Git hooks installed (unless --no-hooks):
  - pre-commit: Syncs beads and runs UBS quality checks
  - post-checkout: Warns about uncommitted beads changes`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := initOptions{
				NonInteractive: nonInteractive,
				Force:          force,
				NoHooks:        noHooks,
			}
			if len(args) > 0 {
				opts.TargetDir = args[0]
			}
			return runProjectInit(opts)
		},
	}

	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Disable prompts; fail on missing info")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing .ntm directory")
	cmd.Flags().BoolVar(&noHooks, "no-hooks", false, "Skip git hooks installation")

	return cmd
}

type initOptions struct {
	TargetDir      string
	NonInteractive bool
	Force          bool
	NoHooks        bool
}

func runProjectInit(opts initOptions) error {
	target := opts.TargetDir
	if target == "" {
		var err error
		target, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	// Backward compatibility: if user runs "ntm init zsh/bash/fish", redirect to shell integration
	if opts.TargetDir != "" && isShellName(opts.TargetDir) {
		if _, err := os.Stat(opts.TargetDir); err != nil {
			// Directory doesn't exist, so this is a shell integration request
			return runShellInit(opts.TargetDir)
		}
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target directory: %w", err)
	}

	stat, err := os.Stat(absTarget)
	if err != nil {
		return fmt.Errorf("target directory not found: %w", err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("target path is not a directory: %s", absTarget)
	}

	ntmDir := filepath.Join(absTarget, ".ntm")
	// Treat the project as "initialized" only once the project config exists.
	// This allows recovering from partial/failed initialization where `.ntm/` exists
	// but `config.toml` (or other scaffolding) is missing.
	projectConfigPath := filepath.Join(ntmDir, "config.toml")
	if fileExists(projectConfigPath) && !opts.Force {
		return fmt.Errorf("ntm already initialized at %s (use --force to reinitialize)", ntmDir)
	}

	auditStart := time.Now()
	_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.project_init", map[string]interface{}{
		"phase":           "start",
		"project_path":    absTarget,
		"non_interactive": opts.NonInteractive,
		"force":           opts.Force,
		"no_hooks":        opts.NoHooks,
		"correlation_id":  auditCorrelationID,
	}, nil)
	result, err := config.InitProjectConfigAt(absTarget, opts.Force)
	if err != nil {
		_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.project_init", map[string]interface{}{
			"phase":          "finish",
			"project_path":   absTarget,
			"success":        false,
			"error":          err.Error(),
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": auditCorrelationID,
		}, nil)
		return err
	}
	_ = audit.LogEvent("", audit.EventTypeCommand, audit.ActorUser, "config.project_init", map[string]interface{}{
		"phase":          "finish",
		"project_path":   absTarget,
		"ntm_dir":        result.NTMDir,
		"created_dirs":   len(result.CreatedDirs),
		"created_files":  len(result.CreatedFiles),
		"success":        true,
		"duration_ms":    time.Since(auditStart).Milliseconds(),
		"correlation_id": auditCorrelationID,
	}, nil)

	configPath := filepath.Join(result.NTMDir, "config.toml")
	registered, warning, err := registerAgentMailProject(absTarget, configPath)
	if err != nil {
		return err
	}

	// Install git hooks (unless --no-hooks)
	var hooksInstalled []string
	var hooksWarning string
	if !opts.NoHooks {
		hooksInstalled, hooksWarning = installGitHooks(absTarget, opts.Force)
	}

	if IsJSONOutput() {
		payload := map[string]interface{}{
			"success":         true,
			"project_path":    absTarget,
			"ntm_dir":         result.NTMDir,
			"created_dirs":    result.CreatedDirs,
			"created_files":   result.CreatedFiles,
			"agent_mail":      registered,
			"hooks_installed": hooksInstalled,
			"non_interactive": opts.NonInteractive,
			"force":           opts.Force,
			"no_hooks":        opts.NoHooks,
		}
		if warning != "" {
			payload["agent_mail_warning"] = warning
		}
		if hooksWarning != "" {
			payload["hooks_warning"] = hooksWarning
		}
		return output.PrintJSON(payload)
	}

	output.PrintSuccessf("Initialized NTM project in %s", result.NTMDir)
	if warning != "" {
		output.PrintWarningf("Agent Mail: %s", warning)
	} else if registered {
		output.PrintSuccess("Registered project with Agent Mail")
	}
	if len(result.CreatedDirs) > 0 {
		output.PrintInfof("Created %s", output.CountStr(len(result.CreatedDirs), "directory", "directories"))
	}
	if len(result.CreatedFiles) > 0 {
		output.PrintInfof("Created %s", output.CountStr(len(result.CreatedFiles), "file", "files"))
	}

	// Report hooks installation
	if len(hooksInstalled) > 0 {
		output.PrintSuccessf("Installed git hooks: %s", strings.Join(hooksInstalled, ", "))
	}
	if hooksWarning != "" {
		output.PrintWarningf("Git hooks: %s", hooksWarning)
	}

	return nil
}

// installGitHooks installs pre-commit and post-checkout hooks for the project.
// Returns the list of installed hooks and any warning message.
func installGitHooks(projectDir string, force bool) ([]string, string) {
	var installed []string

	// Try to create a hook manager - this will fail if not a git repo
	mgr, err := hooks.NewManager(projectDir)
	if err != nil {
		if err == hooks.ErrNotGitRepo {
			return nil, "not a git repository, skipping hooks"
		}
		return nil, fmt.Sprintf("failed to initialize hooks: %v", err)
	}

	// Install pre-commit hook (beads sync + UBS)
	if err := mgr.Install(hooks.HookPreCommit, force); err != nil {
		if err != hooks.ErrHookExists {
			return installed, fmt.Sprintf("pre-commit: %v", err)
		}
		// Hook exists and force is false - skip but don't warn
	} else {
		installed = append(installed, "pre-commit")
	}

	// Install post-checkout hook (beads warning)
	if err := mgr.Install(hooks.HookPostCheckout, force); err != nil {
		if err != hooks.ErrHookExists {
			return installed, fmt.Sprintf("post-checkout: %v", err)
		}
		// Hook exists and force is false - skip but don't warn
	} else {
		installed = append(installed, "post-checkout")
	}

	return installed, ""
}

func isShellName(value string) bool {
	switch value {
	case "zsh", "bash", "fish":
		return true
	default:
		return false
	}
}

func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell <shell>",
		Short: "Generate shell integration script",
		Long: `Generate shell integration for zsh, bash, or fish.

Add to your shell rc file:
  zsh:  eval "$(ntm shell zsh)"   → ~/.zshrc
  bash: eval "$(ntm shell bash)"  → ~/.bashrc
  fish: ntm shell fish | source   → ~/.config/fish/config.fish

This adds:
  - Agent aliases (cc, cod, gmi)
  - Short command aliases (cnt, sat, rnt, etc.)
  - Tab completions
  - Optional F6 keybinding for palette`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"zsh", "bash", "fish"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShellInit(args[0])
		},
	}
}

func runShellInit(shell string) error {
	cfg := loadSelectedConfigOrDefault()

	switch shell {
	case "zsh":
		fmt.Print(generateZsh(cfg))
	case "bash":
		fmt.Print(generateBash(cfg))
	case "fish":
		fmt.Print(generateFish(cfg))
	default:
		return fmt.Errorf("unsupported shell: %s (use zsh, bash, or fish)", shell)
	}

	return nil
}

// quoteAlias quotes a string for use in a shell alias (single quotes).
func quoteAlias(s string) string {
	if s == "" {
		return "''"
	}
	// Replace single quotes with '\''
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func generateZsh(cfg *config.Config) string {
	var b strings.Builder

	b.WriteString(`# NTM Shell Integration (generated by 'ntm shell zsh')
# Add to ~/.zshrc: eval "$(ntm shell zsh)"

`)

	// Render agent command templates with empty vars (for basic alias usage)
	emptyVars := config.AgentTemplateVars{}
	claudeCmd, _ := config.GenerateAgentCommand(cfg.Agents.Claude, emptyVars)
	codexCmd, _ := config.GenerateAgentCommand(cfg.Agents.Codex, emptyVars)
	geminiCmd, _ := config.GenerateAgentCommand(cfg.Agents.Gemini, emptyVars)

	// Agent aliases
	b.WriteString("# Agent aliases\n")
	b.WriteString(fmt.Sprintf("alias cc=%s\n", quoteAlias(claudeCmd)))
	b.WriteString(fmt.Sprintf("alias cod=%s\n", quoteAlias(codexCmd)))
	b.WriteString(fmt.Sprintf("alias gmi=%s\n", quoteAlias(geminiCmd)))
	b.WriteString("\n")

	// Command aliases
	b.WriteString(`# Short aliases for ntm commands
# Session creation
alias cnt='ntm create'
alias sat='ntm spawn'
alias qps='ntm quick'

# Agent management
alias ant='ntm add'
alias bp='ntm send'
alias int='ntm interrupt'

# Session navigation
alias rnt='ntm attach'
alias lnt='ntm list'
alias snt='ntm status'
alias vnt='ntm view'
alias znt='ntm zoom'

# Output management
alias cpnt='ntm copy'
alias svnt='ntm save'

# Utilities
alias ncp='ntm palette'
alias knt='ntm kill'
alias dnt='ntm deps'

`)

	// Completions
	b.WriteString(`# Tab completions
_ntm_complete_sessions() {
  local sessions
  sessions=(${(f)"$(ntm list 2>/dev/null | awk -F: '{gsub(/^[[:space:]]+/, "", $1); print $1}')"})
  _describe 'session' sessions
}

_ntm_complete_ensemble_presets() {
  local presets
  presets=(${(f)"$(ntm ensemble presets --format=json 2>/dev/null | sed -n 's/.*\"name\"[[:space:]]*:[[:space:]]*\"\\([^\"\\\\]\\+\\)\".*/\\1/p' | sort -u)"})
  _describe 'preset' presets
}

_ntm_complete_mode_ids() {
  local modes
  modes=(${(f)"$(ntm modes list --format=json --all 2>/dev/null | sed -n 's/.*\"id\"[[:space:]]*:[[:space:]]*\"\\([^\"\\\\]\\+\\)\".*/\\1/p' | sort -u)"})
  _describe 'mode' modes
}

_ntm_complete_tiers() {
  local tiers
  tiers=('core' 'advanced' 'experimental' 'all')
  _describe 'tier' tiers
}

_ntm() {
  local -a commands
  commands=(
    'create:Create a new tmux session'
    'spawn:Create session and spawn agents'
    'quick:Quick project setup'
    'add:Add agents to existing session'
    'send:Send prompt to agents'
    'interrupt:Send Ctrl+C to agents'
    'attach:Attach to a session'
    'list:List all sessions'
    'status:Show session status'
    'view:View all panes (unzoom, tile)'
    'zoom:Zoom a specific pane'
    'copy:Copy pane output to clipboard'
    'save:Save pane outputs to files'
    'palette:Open command palette'
    'deps:Check dependencies'
    'kill:Kill a session'
    'init:Initialize project'
    'shell:Generate shell integration'
    'completion:Generate completions'
    'config:Manage configuration'
    'ensemble:Manage reasoning ensembles'
    'version:Print version'
    '--robot-ensemble-modes:Robot: list reasoning modes (JSON)'
    '--robot-ensemble-presets:Robot: list ensemble presets (JSON)'
    '--robot-ensemble:Robot: run ensemble (JSON)'
    '--robot-ensemble-spawn:Robot: spawn ensemble (JSON)'
    '--robot-ensemble-suggest:Robot: suggest ensemble (JSON)'
    '--robot-ensemble-stop:Robot: stop ensemble (JSON)'
  )

  if (( CURRENT == 2 )); then
    _describe 'command' commands
  else
    case "${words[2]}" in
      ensemble)
        local -a ensemble_commands
        ensemble_commands=(
          'spawn:Spawn an ensemble session'
          'presets:Manage ensemble presets'
          'modes:List reasoning modes'
          'status:Show ensemble status'
          'stop:Stop ensemble session'
          'suggest:Suggest ensemble configuration'
          'estimate:Estimate ensemble token usage'
          'synthesize:Synthesize ensemble findings'
          'export-findings:Export ensemble findings'
          'provenance:Show ensemble provenance'
          'compare:Compare ensemble runs'
          'resume:Resume an ensemble run'
          'rerun-mode:Rerun a mode in an ensemble'
          'clean-checkpoints:Clean ensemble checkpoints'
        )
        if (( CURRENT == 3 )); then
          _describe 'ensemble command' ensemble_commands
          return
        fi
        case "${words[3]}" in
          spawn|status|stop|synthesize|compare|resume|rerun-mode)
            _ntm_complete_sessions
            ;;
          presets)
            _ntm_complete_ensemble_presets
            ;;
        esac
        ;;
      attach|status|send|interrupt|kill|add|palette|view|zoom|copy|save)
        _ntm_complete_sessions
        ;;
    esac
  fi
}

compdef _ntm ntm
compdef _ntm_complete_sessions rnt snt knt bp int ant ncp vnt znt cpnt svnt

`)

	// F6 keybinding (optional, check if in tmux)
	b.WriteString(`# F6 palette binding (works inside and outside tmux)
_ntm_palette_widget() {
  BUFFER="ntm palette"
  zle accept-line
}
zle -N _ntm_palette_widget
bindkey '^[[17~' _ntm_palette_widget  # F6

# Tmux popup palette (F6 opens floating palette)
if [[ -n "$TMUX" ]]; then
  # Override F6 to use tmux popup for better UX
  bindkey -r '^[[17~'
  _ntm_tmux_popup() {
    tmux popup -E -w 80% -h 80% "ntm palette"
  }
  zle -N _ntm_tmux_popup
  bindkey '^[[17~' _ntm_tmux_popup  # F6
fi
`)

	return b.String()
}

func generateBash(cfg *config.Config) string {
	var b strings.Builder

	b.WriteString(`# NTM Shell Integration (generated by 'ntm shell bash')
# Add to ~/.bashrc: eval "$(ntm shell bash)"

`)

	// Render agent command templates with empty vars (for basic alias usage)
	emptyVars := config.AgentTemplateVars{}
	claudeCmd, _ := config.GenerateAgentCommand(cfg.Agents.Claude, emptyVars)
	codexCmd, _ := config.GenerateAgentCommand(cfg.Agents.Codex, emptyVars)
	geminiCmd, _ := config.GenerateAgentCommand(cfg.Agents.Gemini, emptyVars)

	// Agent aliases
	b.WriteString("# Agent aliases\n")
	b.WriteString(fmt.Sprintf("alias cc=%s\n", quoteAlias(claudeCmd)))
	b.WriteString(fmt.Sprintf("alias cod=%s\n", quoteAlias(codexCmd)))
	b.WriteString(fmt.Sprintf("alias gmi=%s\n", quoteAlias(geminiCmd)))
	b.WriteString("\n")

	// Command aliases
	b.WriteString(`# Short aliases for ntm commands
# Session creation
alias cnt='ntm create'
alias sat='ntm spawn'
alias qps='ntm quick'

# Agent management
alias ant='ntm add'
alias bp='ntm send'
alias int='ntm interrupt'

# Session navigation
alias rnt='ntm attach'
alias lnt='ntm list'
alias snt='ntm status'
alias vnt='ntm view'
alias znt='ntm zoom'

# Output management
alias cpnt='ntm copy'
alias svnt='ntm save'

# Utilities
alias ncp='ntm palette'
alias knt='ntm kill'
alias dnt='ntm deps'

`)

	// Completions
	b.WriteString(`# Tab completions
	_ntm_list_sessions() {
	  ntm list 2>/dev/null | awk -F: '{gsub(/^[[:space:]]+/, "", $1); print $1}'
	}

	_ntm_list_ensemble_presets() {
	  ntm ensemble presets --format=json 2>/dev/null | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]\+\)".*/\1/p' | sort -u
	}

	_ntm_list_mode_ids() {
	  ntm modes list --format=json --all 2>/dev/null | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]\+\)".*/\1/p' | sort -u
	}

	_ntm_completions() {
	  local cur="${COMP_WORDS[COMP_CWORD]}"
	  local prev="${COMP_WORDS[COMP_CWORD-1]}"

	  # Flag value completions (supports both --flag value and --flag=value)
	  if [[ "$prev" == "--tier" ]]; then
	    COMPREPLY=($(compgen -W "core advanced experimental all" -- "$cur"))
	    return
	  fi
	  if [[ "$cur" == --tier=* ]]; then
	    local val="${cur#--tier=}"
	    local matches=($(compgen -W "core advanced experimental all" -- "$val"))
	    COMPREPLY=("${matches[@]/#/--tier=}")
	    return
	  fi

	  if [[ "$prev" == "--preset" ]]; then
	    local presets=$(_ntm_list_ensemble_presets)
	    COMPREPLY=($(compgen -W "$presets" -- "$cur"))
	    return
	  fi
	  if [[ "$cur" == --preset=* ]]; then
	    local val="${cur#--preset=}"
	    local presets=$(_ntm_list_ensemble_presets)
	    local matches=($(compgen -W "$presets" -- "$val"))
	    COMPREPLY=("${matches[@]/#/--preset=}")
	    return
	  fi

	  if [[ "$prev" == "--modes" ]]; then
	    local modes=$(_ntm_list_mode_ids)
	    local prefix=""
	    local seg="$cur"
	    if [[ "$cur" == *,* ]]; then
	      prefix="${cur%,*},"
	      seg="${cur##*,}"
	    fi
	    local matches=($(compgen -W "$modes" -- "$seg"))
	    COMPREPLY=("${matches[@]/#/$prefix}")
	    return
	  fi
	  if [[ "$cur" == --modes=* ]]; then
	    local val="${cur#--modes=}"
	    local prefix="--modes="
	    local modes=$(_ntm_list_mode_ids)
	    local seg="$val"
	    local prefixList=""
	    if [[ "$val" == *,* ]]; then
	      prefixList="${val%,*},"
	      seg="${val##*,}"
	    fi
	    local matches=($(compgen -W "$modes" -- "$seg"))
	    COMPREPLY=("${matches[@]/#/$prefix$prefixList}")
	    return
	  fi

	  if [[ ${COMP_CWORD} -eq 1 ]]; then
	    COMPREPLY=($(compgen -W "create spawn quick add send interrupt attach list status view zoom copy save palette deps kill init shell completion config ensemble version --robot-ensemble-modes --robot-ensemble-presets --robot-ensemble --robot-ensemble-spawn --robot-ensemble-suggest --robot-ensemble-stop" -- "$cur"))
	  else
	    # Robot ensemble flags
	    if [[ "$cur" == -* ]]; then
	      for w in "${COMP_WORDS[@]}"; do
	        case "$w" in
	          --robot-ensemble-modes)
	            COMPREPLY=($(compgen -W "--tier --category --limit --offset" -- "$cur"))
	            return
	            ;;
	          --robot-ensemble-spawn)
	            COMPREPLY=($(compgen -W "--preset --modes --question --agents --assignment --allow-advanced --budget-total --budget-per-agent --no-cache --project" -- "$cur"))
	            return
	            ;;
	        esac
	      done
	    fi

	    case "${COMP_WORDS[1]}" in
	      attach|status|send|interrupt|kill|add|palette|view|zoom|copy|save)
	        local sessions=$(_ntm_list_sessions)
	        COMPREPLY=($(compgen -W "$sessions" -- "$cur"))
	        ;;
	      ensemble)
	        if [[ ${COMP_CWORD} -eq 2 ]]; then
	          local presets=$(_ntm_list_ensemble_presets)
	          COMPREPLY=($(compgen -W "spawn presets list status synthesize stop suggest estimate compare provenance export-findings resume rerun-mode clean-checkpoints $presets" -- "$cur"))
	        else
	          case "${COMP_WORDS[2]}" in
	            status|synthesize|stop)
	              if [[ ${COMP_CWORD} -eq 3 ]]; then
	                local sessions=$(_ntm_list_sessions)
	                COMPREPLY=($(compgen -W "$sessions" -- "$cur"))
	              fi
	              ;;
	            estimate)
	              if [[ ${COMP_CWORD} -eq 3 ]]; then
	                local presets=$(_ntm_list_ensemble_presets)
	                COMPREPLY=($(compgen -W "$presets" -- "$cur"))
	              fi
	              ;;
	          esac
	        fi
	        ;;
	    esac
	  fi
	}

complete -F _ntm_completions ntm
complete -F _ntm_completions rnt snt knt bp int ant ncp vnt znt cpnt svnt

# F6 palette binding
bind '"\e[17~":"ntm palette\n"'
`)

	return b.String()
}

func generateFish(cfg *config.Config) string {
	var b strings.Builder

	b.WriteString(`# NTM Shell Integration (generated by 'ntm shell fish')
# Add to config.fish: ntm shell fish | source

`)

	// Render agent command templates with empty vars (for basic alias usage)
	emptyVars := config.AgentTemplateVars{}
	claudeCmd, _ := config.GenerateAgentCommand(cfg.Agents.Claude, emptyVars)
	codexCmd, _ := config.GenerateAgentCommand(cfg.Agents.Codex, emptyVars)
	geminiCmd, _ := config.GenerateAgentCommand(cfg.Agents.Gemini, emptyVars)

	// Agent aliases
	b.WriteString("# Agent aliases\n")
	b.WriteString(fmt.Sprintf("alias cc %s\n", quoteAlias(claudeCmd)))
	b.WriteString(fmt.Sprintf("alias cod %s\n", quoteAlias(codexCmd)))
	b.WriteString(fmt.Sprintf("alias gmi %s\n", quoteAlias(geminiCmd)))
	b.WriteString("\n")

	// Command abbreviations
	b.WriteString(`# Short aliases for ntm commands
# Session creation
abbr -a cnt 'ntm create'
abbr -a sat 'ntm spawn'
abbr -a qps 'ntm quick'

# Agent management
abbr -a ant 'ntm add'
abbr -a bp 'ntm send'
abbr -a int 'ntm interrupt'

# Session navigation
abbr -a rnt 'ntm attach'
abbr -a lnt 'ntm list'
abbr -a snt 'ntm status'
abbr -a vnt 'ntm view'
abbr -a znt 'ntm zoom'

# Output management
abbr -a cpnt 'ntm copy'
abbr -a svnt 'ntm save'

# Utilities
abbr -a ncp 'ntm palette'
abbr -a knt 'ntm kill'
abbr -a dnt 'ntm deps'

`)

	// Completions
	b.WriteString(`# Tab completions
	function __fish_ntm_sessions
	  ntm list 2>/dev/null | string match -r '^\s*\S+' | string trim
	end

	function __fish_ntm_ensemble_presets
	  ntm ensemble presets --format=json 2>/dev/null | string match -rg '"name"\s*:\s*"([^"]+)"' '$1' | sort -u
	end

	function __fish_ntm_mode_ids
	  ntm modes list --format=json --all 2>/dev/null | string match -rg '"id"\s*:\s*"([^"]+)"' '$1' | sort -u
	end

	complete -c ntm -f
	complete -c ntm -l robot-ensemble-modes -d "Robot: list reasoning modes (JSON)"
	complete -c ntm -l robot-ensemble-presets -d "Robot: list ensemble presets (JSON)"
	complete -c ntm -l robot-ensemble -d "Robot: get ensemble state for session (JSON)"
	complete -c ntm -l robot-ensemble-spawn -d "Robot: spawn an ensemble (JSON)"
	complete -c ntm -l robot-ensemble-suggest -d "Robot: suggest best preset for question (JSON)"
	complete -c ntm -l robot-ensemble-stop -d "Robot: stop an ensemble run (JSON)"

	complete -c ntm -l tier -d "Filter by tier (robot ensemble modes)" -a "core advanced experimental all" -n "__fish_seen_argument -l robot-ensemble-modes"
	complete -c ntm -l preset -d "Ensemble preset name" -a "(__fish_ntm_ensemble_presets)" -n "__fish_seen_argument -l robot-ensemble-spawn"
	complete -c ntm -l modes -d "Explicit mode IDs or codes (comma-separated)" -a "(__fish_ntm_mode_ids)" -n "__fish_seen_argument -l robot-ensemble-spawn"
	complete -c ntm -l allow-advanced -d "Allow advanced/experimental modes" -n "__fish_seen_argument -l robot-ensemble-spawn"

	complete -c ntm -n "__fish_use_subcommand" -a "create" -d "Create a new tmux session"
	complete -c ntm -n "__fish_use_subcommand" -a "spawn" -d "Create session and spawn agents"
	complete -c ntm -n "__fish_use_subcommand" -a "quick" -d "Quick project setup"
	complete -c ntm -n "__fish_use_subcommand" -a "add" -d "Add agents to existing session"
	complete -c ntm -n "__fish_use_subcommand" -a "send" -d "Send prompt to agents"
	complete -c ntm -n "__fish_use_subcommand" -a "interrupt" -d "Send Ctrl+C to agents"
	complete -c ntm -n "__fish_use_subcommand" -a "attach" -d "Attach to a session"
	complete -c ntm -n "__fish_use_subcommand" -a "list" -d "List all sessions"
	complete -c ntm -n "__fish_use_subcommand" -a "status" -d "Show session status"
	complete -c ntm -n "__fish_use_subcommand" -a "view" -d "View all panes (unzoom, tile)"
	complete -c ntm -n "__fish_use_subcommand" -a "zoom" -d "Zoom a specific pane"
	complete -c ntm -n "__fish_use_subcommand" -a "copy" -d "Copy pane output to clipboard"
	complete -c ntm -n "__fish_use_subcommand" -a "save" -d "Save pane outputs to files"
	complete -c ntm -n "__fish_use_subcommand" -a "palette" -d "Open command palette"
	complete -c ntm -n "__fish_use_subcommand" -a "deps" -d "Check dependencies"
	complete -c ntm -n "__fish_use_subcommand" -a "kill" -d "Kill a session"
	complete -c ntm -n "__fish_use_subcommand" -a "init" -d "Initialize project"
	complete -c ntm -n "__fish_use_subcommand" -a "shell" -d "Generate shell integration"
	complete -c ntm -n "__fish_use_subcommand" -a "config" -d "Manage configuration"
	complete -c ntm -n "__fish_use_subcommand" -a "ensemble" -d "Manage reasoning ensembles"

	complete -c ntm -n "__fish_seen_subcommand_from ensemble" -a "spawn presets list status synthesize stop suggest estimate compare provenance export-findings resume rerun-mode clean-checkpoints"
	complete -c ntm -n "__fish_seen_subcommand_from ensemble" -a "(__fish_ntm_ensemble_presets)"

	complete -c ntm -n "__fish_seen_subcommand_from attach status send interrupt kill add palette view zoom copy save synthesize stop" -a "(__fish_ntm_sessions)"

	# F6 keybinding for palette
	bind \e\[17~ 'commandline -r "ntm palette"; commandline -f execute'
	`)

	return b.String()
}

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion <shell>",
		Short: "Generate shell completion script",
		Long: `Generate completion scripts for various shells.

Bash:
  ntm completion bash > /etc/bash_completion.d/ntm
  # or
  ntm completion bash >> ~/.bashrc

Zsh:
  ntm completion zsh > "${fpath[1]}/_ntm"
  # You may need to run 'compinit'

Fish:
  ntm completion fish > ~/.config/fish/completions/ntm.fish`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return rootCmd.GenBashCompletion(os.Stdout)
			case "zsh":
				return rootCmd.GenZshCompletion(os.Stdout)
			case "fish":
				return rootCmd.GenFishCompletion(os.Stdout, true)
			case "powershell":
				return rootCmd.GenPowerShellCompletion(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell: %s", args[0])
			}
		},
	}

	return cmd
}
