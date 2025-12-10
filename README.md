# NTM - Named Tmux Manager

![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![Status](https://img.shields.io/badge/status-stable-brightgreen.svg)

**A powerful tmux session management tool for orchestrating multiple AI coding agents in parallel.**

Spawn, manage, and coordinate Claude Code, OpenAI Codex, and Google Gemini CLI agents across tiled tmux panes with simple commands and a stunning TUI featuring animated gradients, visual dashboards, and a beautiful command palette.

<div align="center">

```bash
# One-line install
curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash
```

</div>

---

## Why This Exists

### The Problem

Modern AI-assisted development often involves running multiple coding agents simultaneously—Claude for architecture decisions, Codex for implementation, Gemini for testing. But managing these agents across terminal windows is painful:

- **Window chaos**: Each agent needs its own terminal, leading to cluttered desktops
- **Context switching**: Jumping between windows breaks flow and loses context
- **No orchestration**: Sending the same prompt to multiple agents requires manual copy-paste
- **Session fragility**: Disconnecting from SSH loses all your agent sessions
- **Setup friction**: Starting a new project means manually creating directories, initializing git, and spawning agents one by one
- **Visual noise**: Plain terminal output with no visual hierarchy or status indication
- **No visibility**: Hard to see agent status at a glance across many panes

### The Solution

NTM transforms tmux into a **multi-agent command center**:

1. **One session, many agents**: All your AI agents live in a single tmux session with tiled panes
2. **Named panes**: Each agent pane is labeled (e.g., `myproject__cc_1`, `myproject__cod_2`) for easy identification
3. **Broadcast prompts**: Send the same task to all agents of a specific type with one command
4. **Persistent sessions**: Detach and reattach without losing any agent state
5. **Quick project setup**: Create directory, initialize git, and spawn agents in a single command
6. **Stunning TUI**: Animated gradients, visual dashboards, shimmering effects, and a beautiful command palette with Catppuccin themes

### Who Benefits

- **Individual developers**: Run multiple AI agents in parallel for faster iteration
- **Researchers**: Compare responses from different AI models side-by-side
- **Power users**: Build complex multi-agent workflows with scriptable commands
- **Remote workers**: Keep agent sessions alive across SSH disconnections

---

## Key Features

### Quick Project Setup

Create a new project with git initialization, VSCode settings, Claude config, and spawn agents in one command:

```bash
ntm quick myproject --template=go
ntm spawn myproject --cc=3 --cod=2 --gmi=1
```

This creates `~/projects/myproject` with all the scaffolding you need, then launches 6 AI agents in tiled panes.

### Multi-Agent Orchestration

Spawn specific combinations of agents:

```bash
ntm spawn myproject --cc=4 --cod=4 --gmi=2   # 4 Claude + 4 Codex + 2 Gemini = 10 agents + 1 user pane
```

Add more agents to an existing session:

```bash
ntm add myproject --cc=2   # Add 2 more Claude agents
```

### Broadcast Prompts

Send the same prompt to all agents of a specific type:

```bash
ntm send myproject --cc "fix all TypeScript errors in src/"
ntm send myproject --cod "add comprehensive unit tests"
ntm send myproject --all "explain your current approach"
```

### Interrupt All Agents

Stop all running agents instantly:

```bash
ntm interrupt myproject   # Send Ctrl+C to all agent panes
```

### Session Management

```bash
ntm list                      # List all tmux sessions
ntm status myproject          # Show detailed status with agent counts
ntm attach myproject          # Reattach to session
ntm view myproject            # View all panes in tiled layout
ntm zoom myproject 2          # Zoom to specific pane
ntm dashboard myproject       # Open interactive visual dashboard
ntm kill -f myproject         # Kill session (force, no confirmation)
```

### Output Capture

```bash
ntm copy myproject --all      # Copy all pane outputs to clipboard
ntm copy myproject --cc       # Copy Claude panes only
ntm save myproject -o ~/logs  # Save all pane outputs to timestamped files
```

### Command Palette

Invoke a stunning fuzzy-searchable palette of pre-configured prompts with a single keystroke:

```bash
ntm palette myproject         # Open palette for session
# Or press F6 in tmux (after running ntm bind)
```

The palette features:
- **Animated gradient banner** with shimmering title effects
- **Catppuccin color theme** with elegant gradients throughout
- **Fuzzy search** through all commands with live filtering
- **Live preview pane** showing full prompt text with syntax highlighting
- **Nerd Font icons** (with Unicode/ASCII fallbacks for basic terminals)
- **Visual target selector** with animated color-coded agent badges
- **Quick select**: Numbers 1-9 for instant command selection
- **Smooth animations**: Pulsing indicators, gradient transitions
- **Keyboard-driven**: Full keyboard navigation with vim-style keys

### Interactive Dashboard

Open a stunning visual dashboard for any session:

```bash
ntm dashboard myproject       # Or use alias: ntm dash myproject
```

The dashboard provides:
- **Visual pane grid** with color-coded agent cards
- **Live agent counts** showing Claude, Codex, Gemini, and user panes
- **Animated status indicators** with pulsing selection highlights
- **Quick navigation**: Use 1-9 to select panes, z/Enter to zoom
- **Real-time refresh**: Press r to update pane status
- **Responsive layout**: Adapts to terminal size automatically

### Tmux Keybinding Setup

Set up a convenient F6 hotkey to open the palette in a tmux popup:

```bash
ntm bind                      # Bind F6 (default)
ntm bind --key=F5             # Use different key
ntm bind --show               # Show current binding
ntm bind --unbind             # Remove the binding
```

After binding, press F6 inside any tmux session to open the palette in a floating popup.

### Dependency Check

Verify all required tools are installed:

```bash
ntm deps           # Quick check
ntm deps -v        # Verbose output with versions
```

---

## Installation

### One-Line Install (Recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash
```

### Homebrew (macOS/Linux)

```bash
brew install dicklesworthstone/tap/ntm
```

### From Source

```bash
git clone https://github.com/Dicklesworthstone/ntm.git
cd ntm
go build -o ntm ./cmd/ntm
sudo mv ntm /usr/local/bin/
```

### Shell Integration

After installing, add to your shell rc file:

```bash
# zsh (~/.zshrc)
eval "$(ntm init zsh)"

# bash (~/.bashrc)
eval "$(ntm init bash)"

# fish (~/.config/fish/config.fish)
ntm init fish | source
```

Then reload your shell:

```bash
source ~/.zshrc
```

### What Gets Installed

Shell integration adds:

| Category | Aliases | Description |
|----------|---------|-------------|
| **Agent** | `cc`, `cod`, `gmi` | Launch Claude, Codex, Gemini |
| **Session Creation** | `cnt`, `sat`, `qps` | create, spawn, quick |
| **Agent Mgmt** | `ant`, `bp`, `int` | add, send, interrupt |
| **Navigation** | `rnt`, `lnt`, `snt`, `vnt`, `znt` | attach, list, status, view, zoom |
| **Dashboard** | `dash`, `d` | Interactive visual dashboard |
| **Output** | `cpnt`, `svnt` | copy, save |
| **Utilities** | `ncp`, `knt`, `cad` | palette, kill, deps |

Plus:
- Tab completions for all commands
- F6 keybinding support (run `ntm bind` to configure)

---

## Command Reference

Type `ntm` for a colorized help display with all commands.

### Session Creation

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm create` | `cnt` | `<session> [--panes=N]` | Create empty session with N panes |
| `ntm spawn` | `sat` | `<session> --cc=N --cod=N --gmi=N` | Create session and launch agents |
| `ntm quick` | `qps` | `<project> [--template=go\|python\|node\|rust]` | Full project setup with git, VSCode, Claude config |

**Examples:**

```bash
cnt myproject --panes=10              # 10 empty panes
sat myproject --cc=6 --cod=6 --gmi=2  # 6 Claude + 6 Codex + 2 Gemini
qps myproject --template=go           # Create Go project scaffold
```

### Agent Management

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm add` | `ant` | `<session> --cc=N --cod=N --gmi=N` | Add more agents to existing session |
| `ntm send` | `bp` | `<session> [--cc\|--cod\|--gmi\|--all] "prompt"` | Send prompt to agents by type |
| `ntm interrupt` | `int` | `<session>` | Send Ctrl+C to all agent panes |

**Filter flags for `send`:**

| Flag | Description |
|------|-------------|
| `--all` | Send to all agent panes (excludes user pane) |
| `--cc` | Send only to Claude panes |
| `--cod` | Send only to Codex panes |
| `--gmi` | Send only to Gemini panes |

**Examples:**

```bash
ant myproject --cc=2                           # Add 2 Claude agents
bp myproject --cc "fix the linting errors"     # Broadcast to Claude
bp myproject --all "summarize your progress"   # Broadcast to all agents
int myproject                                  # Stop all agents
```

### Session Navigation

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm attach` | `rnt` | `<session>` | Attach (offers to create if missing) |
| `ntm list` | `lnt` | | List all tmux sessions |
| `ntm status` | `snt` | `<session>` | Show pane details and agent counts |
| `ntm view` | `vnt` | `<session>` | Unzoom, tile layout, and attach |
| `ntm zoom` | `znt` | `<session> [pane-index]` | Zoom to specific pane |
| `ntm dashboard` | `d`, `dash` | `[session]` | Interactive visual dashboard |

**Examples:**

```bash
rnt myproject      # Reattach to session
lnt                # Show all sessions
snt myproject      # Detailed status with icons
vnt myproject      # View all panes tiled
znt myproject 3    # Zoom to pane 3
ntm dash myproject # Open interactive dashboard
```

### Output Management

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm copy` | `cpnt` | `<session> [--all\|--cc\|--cod\|--gmi] [-l lines]` | Copy pane output to clipboard |
| `ntm save` | `svnt` | `<session> [-o dir] [-l lines] [--all\|--cc\|--cod\|--gmi]` | Save outputs to files |

**Examples:**

```bash
cpnt myproject --all       # Copy all panes to clipboard
cpnt myproject --cc -l 500 # Copy last 500 lines from Claude panes
svnt myproject -o ~/logs   # Save all outputs to ~/logs
svnt myproject --cod       # Save only Codex pane outputs
```

### Command Palette & Dashboard

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm palette` | `ncp` | `[session]` | Open interactive command palette |
| `ntm dashboard` | `d`, `dash` | `[session]` | Open visual session dashboard |
| `ntm bind` | | `[--key=F6] [--unbind] [--show]` | Configure tmux popup keybinding |

**Examples:**

```bash
ncp myproject              # Open palette for session
ncp                        # Select session first, then palette
ntm dash myproject         # Open dashboard for session
ntm bind                   # Set up F6 keybinding for palette popup
```

**Palette Navigation:**

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate commands |
| `1-9` | Quick select command |
| `Enter` | Select command |
| `Esc` | Back / Quit |
| Type | Filter commands |

**Dashboard Navigation:**

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate panes |
| `1-9` | Quick select pane |
| `z` or `Enter` | Zoom to pane |
| `r` | Refresh pane data |
| `q` or `Esc` | Quit dashboard |

### Utilities

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm deps` | `cad` | `[-v]` | Check installed dependencies |
| `ntm kill` | `knt` | `<session> [-f]` | Kill session (with confirmation) |
| `ntm bind` | | `[--key=F6] [--unbind] [--show]` | Configure tmux F6 keybinding |
| `ntm config init` | | | Create default config file |
| `ntm config show` | | | Display current configuration |

**Examples:**

```bash
ntm deps            # Check all dependencies
knt myproject       # Prompts for confirmation
knt -f myproject    # Force kill, no prompt
ntm bind            # Set up F6 popup keybinding
ntm config init     # Create ~/.config/ntm/config.toml
```

---

## Architecture

### Pane Naming Convention

Agent panes are named using the pattern: `<project>__<agent>_<number>`

Examples:
- `myproject__cc_1` - First Claude agent
- `myproject__cod_2` - Second Codex agent
- `myproject__gmi_1` - First Gemini agent
- `myproject__cc_added_1` - Claude agent added later via `add`

This naming enables targeted commands via filters (`--cc`, `--cod`, `--gmi`).

### Session Layout

```
┌─────────────────────────────────────────────────────────────────┐
│                      Session: myproject                          │
├─────────────────┬─────────────────┬─────────────────────────────┤
│   User Pane     │  myproject__cc_1 │  myproject__cc_2           │
│   (your shell)  │  (Claude #1)     │  (Claude #2)               │
├─────────────────┼─────────────────┼─────────────────────────────┤
│ myproject__cod_1│ myproject__cod_2 │  myproject__gmi_1          │
│ (Codex #1)      │ (Codex #2)       │  (Gemini #1)               │
└─────────────────┴─────────────────┴─────────────────────────────┘
```

- **User pane** (index 0): Always preserved as your command pane
- **Agent panes** (index 1+): Each runs one AI agent
- **Tiled layout**: Automatically arranged for optimal visibility

### Directory Structure

| Platform | Default Projects Base |
|----------|-----------------------|
| macOS | `~/Developer` |
| Linux | `/data/projects` |

Override with config or: `export NTM_PROJECTS_BASE="/your/custom/path"`

Each project creates a subdirectory: `$PROJECTS_BASE/<session-name>/`

### Project Scaffolding (Quick Setup)

The `ntm quick` command creates:

```
myproject/
├── .git/                    # Initialized git repo
├── .gitignore               # Language-appropriate ignores
├── .vscode/
│   └── settings.json        # VSCode workspace settings
├── .claude/
│   ├── settings.toml        # Claude Code config
│   └── commands/
│       └── review.md        # Sample slash command
└── [template files]         # main.go, main.py, etc.
```

---

## Configuration

Configuration lives in `~/.config/ntm/config.toml`:

```bash
# Create default config
ntm config init

# Show current config
ntm config show

# Edit config
$EDITOR ~/.config/ntm/config.toml
```

### Example Config

```toml
# NTM (Named Tmux Manager) Configuration
# https://github.com/Dicklesworthstone/ntm

# Base directory for projects
projects_base = "~/Developer"

[agents]
# Commands used to launch each agent type
claude = 'NODE_OPTIONS="--max-old-space-size=32768" claude --dangerously-skip-permissions'
codex = "codex --dangerously-bypass-approvals-and-sandbox -m gpt-5.1-codex-max"
gemini = "gemini --yolo"

[tmux]
# Tmux-specific settings
default_panes = 10
palette_key = "F6"

# Command Palette entries
# Quick Actions
[[palette]]
key = "fresh_review"
label = "Fresh Eyes Review"
category = "Quick Actions"
prompt = """
Take a step back and carefully reread the most recent code changes.
Fix any obvious bugs or issues you spot.
"""

[[palette]]
key = "git_commit"
label = "Commit Changes"
category = "Quick Actions"
prompt = "Commit all changed files with detailed commit messages and push."

# Code Quality
[[palette]]
key = "refactor"
label = "Refactor Code"
category = "Code Quality"
prompt = """
Review the current code for opportunities to improve:
- Extract reusable functions
- Simplify complex logic
- Improve naming
- Remove duplication
"""

# Coordination
[[palette]]
key = "status_update"
label = "Status Update"
category = "Coordination"
prompt = """
Provide a brief status update:
1. What you just completed
2. What you're currently working on
3. Any blockers or questions
"""
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NTM_PROJECTS_BASE` | `~/Developer` (macOS) or `/data/projects` (Linux) | Base directory for all projects |
| `NTM_THEME` | `mocha` | Color theme: `mocha`, `macchiato`, `nord` |
| `NTM_ICONS` | auto-detect | Icon set: `nerd`, `unicode`, `ascii` |
| `NTM_USE_ICONS` | auto-detect | Force icons: `1` (on) or `0` (off) |
| `NERD_FONTS` | auto-detect | Nerd Fonts available: `1` or `0` |

---

## Themes & Icons

### Color Themes

NTM uses the Catppuccin color palette by default, with support for multiple themes:

| Theme | Description |
|-------|-------------|
| `mocha` | Default dark theme, warm and cozy |
| `macchiato` | Darker variant with more contrast |
| `nord` | Arctic-inspired, cooler tones |

Set via environment variable:

```bash
export NTM_THEME=macchiato
```

### Agent Colors

Each agent type has a distinct color for visual identification:

| Agent | Color | Hex |
|-------|-------|-----|
| Claude | Mauve (Purple) | `#cba6f7` |
| Codex | Blue | `#89b4fa` |
| Gemini | Yellow | `#f9e2af` |
| User | Green | `#a6e3a1` |

### Icon Sets

NTM auto-detects your terminal's capabilities:

| Set | Detection | Example Icons |
|-----|-----------|---------------|
| **Nerd Fonts** | Powerlevel10k, iTerm2, WezTerm, Kitty | `󰗣 󰊤    ` |
| **Unicode** | UTF-8 locale, modern terminals | `✓ ✗ ● ○ ★ ⚠ ℹ` |
| **ASCII** | Fallback | `[x] [X] * o` |

Force a specific set:

```bash
export NTM_ICONS=nerd    # Force Nerd Fonts
export NTM_ICONS=unicode # Force Unicode
export NTM_ICONS=ascii   # Force ASCII
```

---

## Typical Workflow

### Starting a New Project

```bash
# 1. Check if agent CLIs are installed
ntm deps -v

# 2. Create project scaffold (optional)
ntm quick myapi --template=go

# 3. Spawn agents
ntm spawn myapi --cc=3 --cod=2

# 4. You're now attached to the session with 5 agents + 1 user pane
```

### During Development

```bash
# Send task to all Claude agents
ntm send myapi --cc "implement the /users endpoint with full CRUD operations"

# Send different task to Codex agents
ntm send myapi --cod "write comprehensive unit tests for the users module"

# Check status
ntm status myapi

# Zoom to a specific agent to see details
ntm zoom myapi 2

# View all panes
ntm view myapi
```

### Using the Command Palette

```bash
# Open palette (or press F6 in tmux)
ntm palette myapi

# Use fuzzy search to find commands
# Type "fix" to filter to "Fix the Bug"
# Press 1-9 for quick select
# Select target: 1=All, 2=Claude, 3=Codex, 4=Gemini
```

### Scaling Up/Down

```bash
# Need more Claude agents? Add 2 more
ntm add myapi --cc=2

# Interrupt all agents to give new instructions
ntm interrupt myapi

# Send new prompt to all
ntm send myapi --all "stop current work and focus on fixing the CI pipeline"
```

### Saving Work

```bash
# Save all agent outputs before ending session
ntm save myapi -o ~/logs/myapi

# Or copy specific agent output to clipboard
ntm copy myapi --cc
```

### Ending Session

```bash
# Detach (agents keep running)
# Press: Ctrl+B, then D

# Later, reattach
ntm attach myapi

# When done, kill session
ntm kill -f myapi
```

---

## Multi-Agent Coordination Strategies

Different problems call for different agent orchestration patterns. Here are proven strategies:

### Strategy 1: Divide and Conquer

Assign different aspects of a task to different agent types based on their strengths:

```bash
# Start with architecture (Claude excels at high-level design)
ntm send myproject --cc "design the database schema for user management"

# Implementation (Codex for code generation)
ntm send myproject --cod "implement the User and Role models based on the schema"

# Testing (Gemini for comprehensive test coverage)
ntm send myproject --gmi "write unit and integration tests for the models"
```

**Best for:** Large features with distinct phases (design → implement → test)

### Strategy 2: Competitive Comparison

Have multiple agents solve the same problem independently, then compare approaches:

```bash
# Same prompt to all agents
ntm send myproject --all "implement a rate limiter middleware that allows 100 requests per minute per IP"

# View all panes side-by-side
ntm view myproject

# Compare implementations, pick the best one (or combine ideas)
```

**Best for:** Problems with multiple valid solutions, learning different approaches

### Strategy 3: Specialist Teams

Create agents with specific responsibilities:

```bash
# Create session with specialists
ntm spawn myproject --cc=2 --cod=2 --gmi=2

# Claude team: architecture and review
ntm send myproject --cc "focus on code architecture and reviewing others' work"

# Codex team: implementation
ntm send myproject --cod "focus on implementing features and fixing bugs"

# Gemini team: testing and docs
ntm send myproject --gmi "focus on testing and documentation"
```

**Best for:** Large projects with multiple concerns

### Strategy 4: Review Pipeline

Use agents to review each other's work:

```bash
# Implementation
ntm send myproject --cc "implement feature X with full error handling"

# Wait for completion, then peer review
ntm send myproject --cod "review the code Claude just wrote - look for bugs and improvements"

# Final validation
ntm send myproject --gmi "write tests that would catch the bugs mentioned in the review"
```

**Best for:** Quality assurance, catching edge cases

### Strategy 5: Rubber Duck Escalation

Start simple, escalate when stuck:

```bash
# Start with one Claude agent
ntm spawn myproject --cc=1

# If stuck, add more perspectives
ntm add myproject --cc=1 --cod=1

# Still stuck? More agents
ntm add myproject --gmi=1

# Broadcast the problem to all
ntm send myproject --all "I'm stuck on X. Here's what I've tried: Y. What am I missing?"
```

**Best for:** Debugging, breaking through blockers

---

## Integration Examples

### Git Hooks

**Pre-commit: Save Agent Context**

```bash
#!/bin/bash
# .git/hooks/pre-commit

SESSION=$(basename "$(pwd)")
if tmux has-session -t "$SESSION" 2>/dev/null; then
    mkdir -p .agent-logs
    ntm save "$SESSION" -o .agent-logs 2>/dev/null
fi
```

### Shell Scripts

**Automated Project Bootstrap:**

```bash
#!/bin/bash
# bootstrap-project.sh

set -e

PROJECT="$1"
TEMPLATE="${2:-go}"

echo "Creating project: $PROJECT"

# Create project with template
ntm quick "$PROJECT" --template="$TEMPLATE"

# Spawn agents
ntm spawn "$PROJECT" --cc=2 --cod=2

# Give initial context
ntm send "$PROJECT" --all "You are working on a new $TEMPLATE project. Read any existing code and prepare to implement features."

echo "Project $PROJECT ready!"
echo "Run: ntm attach $PROJECT"
```

**Status Report:**

```bash
#!/bin/bash
# status-all.sh

echo "=== Agent Status Report ==="
echo "Generated: $(date)"
echo ""

for session in $(tmux list-sessions -F '#{session_name}' 2>/dev/null); do
    echo "## $session"
    ntm status "$session"
    echo ""
done
```

### VS Code Integration

**tasks.json:**

```json
{
    "version": "2.0.0",
    "tasks": [
        {
            "label": "NTM: Start Agents",
            "type": "shell",
            "command": "ntm spawn ${workspaceFolderBasename} --cc=2 --cod=2"
        },
        {
            "label": "NTM: Send to Claude",
            "type": "shell",
            "command": "ntm send ${workspaceFolderBasename} --cc \"${input:prompt}\""
        },
        {
            "label": "NTM: Open Palette",
            "type": "shell",
            "command": "ntm palette ${workspaceFolderBasename}"
        }
    ],
    "inputs": [
        {
            "id": "prompt",
            "type": "promptString",
            "description": "Enter prompt for agents"
        }
    ]
}
```

### Tmux Configuration

Add these to your `~/.tmux.conf` for better agent management:

```bash
# Increase scrollback buffer (default is 2000)
set-option -g history-limit 50000

# Enable mouse support for pane selection
set -g mouse on

# Show pane titles in status bar
set -g pane-border-status top
set -g pane-border-format " #{pane_title} "

# Better colors for pane borders (Catppuccin-inspired)
set -g pane-border-style fg=colour238
set -g pane-active-border-style fg=colour39

# Faster key repetition
set -s escape-time 0
```

Reload with: `tmux source-file ~/.tmux.conf`

---

## Tmux Essentials

If you're new to tmux, here are the key bindings (default prefix is `Ctrl+B`):

| Keys | Action |
|------|--------|
| `Ctrl+B, D` | Detach from session |
| `Ctrl+B, [` | Enter scroll/copy mode |
| `Ctrl+B, z` | Toggle zoom on current pane |
| `Ctrl+B, Arrow` | Navigate between panes |
| `Ctrl+B, c` | Create new window |
| `Ctrl+B, ,` | Rename current window |
| `q` | Exit scroll mode |
| `F6` | Open NTM palette (after shell integration) |

---

## Troubleshooting

### "tmux not found"

NTM will offer to help install tmux. If that fails:

```bash
# macOS
brew install tmux

# Ubuntu/Debian
sudo apt install tmux

# Fedora
sudo dnf install tmux
```

### "Session already exists"

Use `--force` or attach to the existing session:

```bash
ntm attach myproject    # Attach to existing
# OR
ntm kill -f myproject && ntm spawn myproject --cc=3   # Kill and recreate
```

### Panes not tiling correctly

Force a re-tile:

```bash
ntm view myproject
```

### Agent not responding

Interrupt and restart:

```bash
ntm interrupt myproject
ntm send myproject --cc "continue where you left off"
```

### Icons not displaying

Check your terminal supports Nerd Fonts or force a fallback:

```bash
export NTM_ICONS=unicode   # Use Unicode icons
export NTM_ICONS=ascii     # Use ASCII only
```

### Commands not found after install

Reload your shell configuration:

```bash
source ~/.zshrc   # or ~/.bashrc
```

### Palette numbers select wrong command

This was a bug in earlier versions. Update NTM to the latest version where visual numbering matches selection.

---

## Frequently Asked Questions

### General

**Q: Does this work with bash?**

A: Yes! NTM is a compiled Go binary that works with any shell. The shell integration (`ntm init bash`) provides aliases and completions for bash.

**Q: Can I use this over SSH?**

A: Yes! This is one of the primary use cases. Tmux sessions persist on the server:
1. SSH to your server
2. Start agents: `ntm spawn myproject --cc=3`
3. Detach: `Ctrl+B, D`
4. Disconnect SSH
5. Later: SSH back, run `ntm attach myproject`

All agents continue running while you're disconnected.

**Q: How many agents can I run simultaneously?**

A: Practically limited by:
- **Memory**: Each agent CLI uses 100-500MB RAM
- **API rate limits**: Provider-specific throttling
- **Screen real estate**: Beyond ~16 panes, they become too small

**Q: Does this work on Windows?**

A: Not natively. Options:
- **WSL2**: Install in WSL2, works perfectly
- **Git Bash**: Limited support (no tmux)

### Agents

**Q: Why are agents run with "dangerous" flags?**

A: The flags (`--dangerously-skip-permissions`, `--yolo`, etc.) allow agents to work autonomously without confirmation prompts. This is intentional for productivity. Only use in development environments.

**Q: Can I add support for other AI CLIs?**

A: Yes! Edit your config to add custom agent commands:

```toml
[agents]
claude = "my-custom-claude-wrapper"
codex = "aider --yes-always"
gemini = "cursor --accept-all"
```

**Q: Do agents share context with each other?**

A: No, each agent runs independently. They:
- ✅ Can see the same filesystem
- ✅ Can read each other's file changes
- ❌ Cannot communicate directly
- ❌ Don't share conversation history

Use broadcast (`ntm send`) to coordinate.

### Sessions

**Q: What happens if an agent crashes?**

A: The pane stays open with a shell prompt. You can:
- Restart by typing the agent alias (`cc`, `cod`, `gmi`)
- Check what happened by scrolling up (`Ctrl+B, [`)
- The pane title remains, so filters still work

**Q: How do I increase scrollback history?**

A: Add to `~/.tmux.conf`:

```bash
set-option -g history-limit 50000  # Default is 2000
```

---

## Security Considerations

The agent aliases include flags that bypass safety prompts:

| Alias | Flag | Purpose |
|-------|------|---------|
| `cc` | `--dangerously-skip-permissions` | Allows Claude full system access |
| `cod` | `--dangerously-bypass-approvals-and-sandbox` | Allows Codex full system access |
| `gmi` | `--yolo` | Allows Gemini to execute without confirmation |

**These are intentional for productivity** but mean the agents can:
- Read/write any files
- Execute system commands
- Make network requests

**Recommendations:**
- Only use in development environments
- Review agent outputs before committing code
- Don't use with sensitive credentials in scope
- Consider sandboxed environments for untrusted projects

---

## Performance Considerations

### Memory Usage

| Component | Typical RAM | Notes |
|-----------|-------------|-------|
| tmux server | 5-10 MB | Single process for all sessions |
| Per tmux pane | 1-2 MB | Minimal overhead |
| Claude CLI (`cc`) | 200-400 MB | Node.js process |
| Codex CLI (`cod`) | 150-300 MB | Varies by model |
| Gemini CLI (`gmi`) | 100-200 MB | Lighter footprint |

**Rough formula:**

```
Total RAM ≈ 10 + (panes × 2) + (claude × 300) + (codex × 200) + (gemini × 150) MB
```

**Example:** Session with 3 Claude + 2 Codex + 1 Gemini + 1 user pane:
```
10 + (7 × 2) + (3 × 300) + (2 × 200) + (1 × 150) = 1,474 MB ≈ 1.5 GB
```

### Scaling Tips

1. **Start minimal, scale up**
   ```bash
   ntm spawn myproject --cc=1
   ntm add myproject --cc=1 --cod=1  # Add more as needed
   ```

2. **Use multiple windows instead of many panes**
   ```bash
   tmux new-window -t myproject -n "tests"
   ```

3. **Save outputs before scrollback is lost**
   ```bash
   ntm save myproject -o ~/logs
   ```

---

## Comparison with Alternatives

| Approach | Pros | Cons |
|----------|------|------|
| **NTM** | Purpose-built for AI agents, beautiful TUI, named panes, broadcast prompts | Requires tmux |
| **Multiple Terminal Windows** | Simple, no setup | No persistence, window chaos, no orchestration |
| **Tmux (manual)** | Full control | Verbose commands, no agent-specific features |
| **Screen** | Available everywhere | Fewer features, dated |
| **Docker Containers** | Full isolation | Heavyweight, complex |

### When to Use NTM

✅ **Good fit:**
- Running multiple AI agents in parallel
- Remote development over SSH
- Projects requiring persistent sessions
- Workflows needing broadcast prompts
- Developers comfortable with CLI

❌ **Consider alternatives:**
- Single-agent workflows (just use the CLI directly)
- GUI-preferred workflows (use IDE integration)
- Windows without WSL

---

## Development

### Building from Source

```bash
git clone https://github.com/Dicklesworthstone/ntm.git
cd ntm
go build -o ntm ./cmd/ntm
```

### Running Tests

```bash
go test ./...
```

### Project Structure

```
ntm/
├── cmd/ntm/              # Main entry point
├── internal/
│   ├── cli/              # Cobra commands and help rendering
│   ├── config/           # TOML configuration
│   ├── palette/          # Command palette TUI (enhanced with animations)
│   ├── tmux/             # Tmux operations
│   └── tui/
│       ├── components/   # Reusable TUI components (spinners, progress, banner)
│       ├── dashboard/    # Interactive session dashboard
│       ├── icons/        # Nerd Font / Unicode / ASCII icon sets
│       ├── styles/       # Gradient text, shimmer, glow effects
│       └── theme/        # Catppuccin themes (Mocha, Macchiato, Nord)
└── README.md
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.

---

## Acknowledgments

- [tmux](https://github.com/tmux/tmux) - The terminal multiplexer that makes this possible
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - The TUI framework
- [Catppuccin](https://github.com/catppuccin/catppuccin) - The beautiful color palette
- [Nerd Fonts](https://www.nerdfonts.com/) - The icon fonts
- [Cobra](https://github.com/spf13/cobra) - The CLI framework
- [Claude Code](https://claude.ai/code), [Codex](https://openai.com/codex), [Gemini CLI](https://ai.google.dev/) - The AI agents this tool orchestrates
