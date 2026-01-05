# NTM - Named Tmux Manager

![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS-blue.svg)
![Go Version](https://img.shields.io/badge/go-1.25+-00ADD8.svg)
![License](https://img.shields.io/badge/license-MIT-green.svg)
![CI](https://img.shields.io/github/actions/workflow/status/Dicklesworthstone/ntm/ci.yml?label=CI)
![Release](https://img.shields.io/github/v/release/Dicklesworthstone/ntm?include_prereleases)

**A powerful tmux session management tool for orchestrating multiple AI coding agents in parallel.**

Spawn, manage, and coordinate Claude Code, OpenAI Codex, and Google Gemini CLI agents across tiled tmux panes with simple commands and a stunning TUI featuring animated gradients, visual dashboards, and a beautiful command palette.

<div align="center">

```bash
# One-line install
curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash
```

</div>

---

## Quick Start

```bash
# Install NTM
curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash

# Add shell integration
echo 'eval "$(ntm init zsh)"' >> ~/.zshrc && source ~/.zshrc

# Run the interactive tutorial
ntm tutorial

# Check dependencies
ntm deps -v

# Create your first multi-agent session
ntm spawn myproject --cc=2 --cod=1

# Send a prompt to all Claude agents
ntm send myproject --cc "Hello! Explore this codebase and summarize its architecture."

# Open the command palette
ntm palette myproject
```

---

## Why This Exists

### The Problem

Modern AI-assisted development often involves running multiple coding agents simultaneously: Claude for architecture decisions, Codex for implementation, Gemini for testing. But managing these agents across terminal windows is painful:

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
7. **Context monitoring**: Automatic compaction detection and recovery when agents hit context limits
8. **Multi-channel notifications**: Desktop, webhook, shell, and log notifications for important events
9. **Conflict tracking**: Detect when multiple agents modify the same files
10. **Event logging & analytics**: JSONL logging of all session activity for debugging and audit

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
ntm copy myproject:1          # Copy from specific pane
ntm copy myproject --all      # Copy all pane outputs to clipboard
ntm copy myproject --cc       # Copy Claude panes only
ntm copy myproject --pattern 'ERROR'  # Filter lines by regex
ntm copy myproject --code             # Extract only markdown code blocks
ntm copy myproject --output out.txt   # Save output to file instead of clipboard
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
- **Pinned + recent commands** so you re-search less (pin/favorite with `Ctrl+P` / `Ctrl+F`)
- **Live preview pane** showing full prompt text + target metadata to reduce misfires
- **Nerd Font icons** (with Unicode/ASCII fallbacks for basic terminals)
- **Visual target selector** with animated color-coded agent badges
- **Quick select**: Numbers 1-9 for instant command selection
- **Smooth animations**: Pulsing indicators, gradient transitions
- **Help overlay**: Press `?` (or `F1`) for key hints
- **Keyboard-driven**: Full keyboard navigation with vim-style keys

### Interactive Dashboard

Open a stunning visual dashboard for any session:

```bash
ntm dashboard myproject       # Or use alias: ntm dash myproject
```

The dashboard provides:
- **Visual pane grid** with color-coded agent cards
- **Live agent counts** showing Claude, Codex, Gemini, and user panes
- **Token velocity badges** showing real-time tokens-per-minute (tpm) for each agent
- **Animated status indicators** with pulsing selection highlights
- **Quick navigation**: Use 1-9 to select panes, z/Enter to zoom
- **Real-time refresh**: Press r to update pane status
- **Context + mail shortcuts**: Press `c` for context, `m` for Agent Mail
- **Help overlay**: Press `?` for key hints (Esc closes)
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

### Interactive Tutorial

Get started quickly with the built-in interactive tutorial:

```bash
ntm tutorial              # Launch the animated tutorial
ntm tutorial --skip       # Skip animations (accessibility mode)
```

The tutorial walks you through:
- Core concepts (sessions, panes, agents)
- Essential commands with examples
- Multi-agent coordination strategies
- Power user tips and keyboard shortcuts

### Self-Update

Keep NTM up-to-date with the built-in upgrade command:

```bash
ntm upgrade               # Check for updates and prompt to install
ntm upgrade --check       # Check only, don't install
ntm upgrade --yes         # Auto-confirm installation
ntm upgrade --force       # Force reinstall even if up-to-date
```

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

### Go Install

```bash
go install github.com/Dicklesworthstone/ntm/cmd/ntm@latest
```

### Docker

Run NTM in a container (useful for CI/CD or isolated environments):

```bash
# Pull the latest image
docker pull ghcr.io/dicklesworthstone/ntm:latest

# Run interactively
docker run -it --rm ghcr.io/dicklesworthstone/ntm:latest

# Or use a specific version
docker pull ghcr.io/dicklesworthstone/ntm:v1.0.0
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
| `ntm status` | `snt` | `<session>` | Show pane details with type indicators (C/X/G) and agent counts |
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
| `ntm copy` | `cpnt` | `<session[:pane]> [--all\|--cc\|--cod\|--gmi] [-l lines] [--pattern REGEX] [--code] [--output FILE] [--quiet]` | Copy pane output to clipboard or file with filters |
| `ntm save` | `svnt` | `<session> [-o dir] [-l lines] [--all\|--cc\|--cod\|--gmi]` | Save outputs to files |

**Examples:**

```bash
cpnt myproject:1           # Copy specific pane
cpnt myproject --all       # Copy all panes to clipboard
cpnt myproject --cc -l 500 # Copy last 500 lines from Claude panes
cpnt myproject --pattern 'ERROR' --output /tmp/errors.txt # Filter + save to file
svnt myproject -o ~/logs   # Save all outputs to ~/logs
svnt myproject --cod       # Save only Codex pane outputs
```

### Monitoring & Analysis

| Command | Alias | Arguments | Description |
|---------|-------|-----------|-------------|
| `ntm activity` | | `[session] [--cc\|--cod\|--gmi] [-w] [--interval MS]` | Show real-time agent activity states |
| `ntm health` | | `[session] [--json]` | Check agent health status |
| `ntm watch` | `w` | `[session] [--cc\|--cod\|--gmi] [--activity] [--tail N]` | Stream agent output in real-time |
| `ntm extract` | | `<session> [pane] [--lang=X] [--copy] [--apply]` | Extract code blocks from output |
| `ntm diff` | | `<session> <pane1> <pane2> [--unified] [--code-only]` | Compare outputs from two panes |
| `ntm grep` | | `<pattern> [session] [-i] [-C N] [--cc\|--cod\|--gmi]` | Search pane output with regex |
| `ntm analytics` | | `[--days N] [--since DATE] [--format X] [--sessions]` | View session analytics and statistics |
| `ntm locks` | | `<session> [--all-agents] [--json]` | Show active file reservations |

**Examples:**

```bash
ntm activity myproject --watch        # Real-time activity monitoring
ntm health myproject                  # Check all agent health
ntm watch myproject --cc              # Stream Claude agent output
ntm extract myproject --lang=go       # Extract Go code blocks
ntm diff myproject cc_1 cod_1         # Compare Claude vs Codex output
ntm grep 'error' myproject -C 3       # Search with context
ntm analytics --days 7                # Last 7 days statistics
ntm locks myproject --all-agents      # All project file reservations
```

### Checkpoints

| Command | Arguments | Description |
|---------|-----------|-------------|
| `ntm checkpoint save` | `<session> [-m "desc"] [--scrollback=N] [--no-git]` | Create session checkpoint |
| `ntm checkpoint list` | `[session] [--json]` | List checkpoints |
| `ntm checkpoint show` | `<session> <id> [--json]` | Show checkpoint details |
| `ntm checkpoint delete` | `<session> <id> [-f]` | Delete a checkpoint |

**Examples:**

```bash
ntm checkpoint save myproject -m "Before refactor"
ntm checkpoint list myproject
ntm checkpoint show myproject 20251210-143052
ntm checkpoint delete myproject 20251210-143052 -f
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
| `ntm tutorial` | | `[--skip] [--slide=N]` | Interactive tutorial |
| `ntm upgrade` | | `[--check] [--yes] [--force]` | Self-update to latest version |

**Examples:**

```bash
ntm deps            # Check all dependencies
knt myproject       # Prompts for confirmation
knt -f myproject    # Force kill, no prompt
ntm bind            # Set up F6 popup keybinding
ntm config init     # Create ~/.config/ntm/config.toml
ntm tutorial        # Launch interactive tutorial
ntm upgrade         # Check for and install updates
```

### Agent Profiles

Profiles (also called personas) define agent behavioral characteristics including agent type, model, system prompts, and focus patterns. NTM includes built-in profiles for common roles like architect, implementer, reviewer, tester, and documenter.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `ntm profiles list` | `[--agent TYPE] [--tag TAG] [--json]` | List available profiles |
| `ntm profiles show` | `<name> [--json]` | Show detailed profile information |
| `ntm personas list` | `[--agent TYPE] [--tag TAG] [--json]` | Alias for `profiles list` |
| `ntm personas show` | `<name> [--json]` | Alias for `profiles show` |

**Profile Sources (later overrides earlier):**
1. **Built-in**: Compiled into NTM (architect, implementer, reviewer, tester, documenter)
2. **User**: `~/.config/ntm/personas.toml`
3. **Project**: `.ntm/personas.toml`

**Profile Sets:**

Profile sets are named groups of profiles for quick spawning:

```toml
# In ~/.config/ntm/personas.toml or .ntm/personas.toml
[[persona_sets]]
name = "backend-team"
description = "Full backend development team"
personas = ["architect", "implementer", "implementer", "tester"]
```

**Examples:**

```bash
ntm profiles list                    # List all profiles
ntm profiles list --agent claude     # Filter by agent type
ntm profiles list --tag review       # Filter by tag
ntm profiles list --json             # JSON output for scripts
ntm profiles show architect          # Show profile details
ntm profiles show architect --json   # JSON output with source info
```

**Using Profiles with Spawn:**

```bash
ntm spawn myproject --profiles=architect,implementer,tester
ntm spawn myproject --profile-set=backend-team
```

### AI Agent Integration (Robot Mode)

NTM provides machine-readable output for integration with AI coding agents and automation pipelines. All robot commands output JSON by default and follow consistent exit codes (0=success, 1=error, 2=unavailable).

**State Inspection:**

```bash
ntm --robot-status              # Sessions, panes, agent states
ntm --robot-context=SESSION     # Context window usage per agent
ntm --robot-snapshot            # Unified state: sessions + beads + alerts + mail
ntm --robot-tail=SESSION        # Recent pane output (--lines=50 --panes=1,2)
ntm --robot-inspect-pane=SESS   # Detailed pane inspection (--inspect-index=N)
ntm --robot-files=SESSION       # File changes with agent attribution (--files-window=15m)
ntm --robot-metrics=SESSION     # Session metrics export (--metrics-period=24h)
ntm --robot-palette             # Query command palette (--palette-category=NAME)
ntm --robot-plan                # bv execution plan with parallelizable tracks
ntm --robot-graph               # Dependency graph insights
ntm --robot-dashboard           # Dashboard summary (markdown or --json)
ntm --robot-terse               # Single-line encoded state (minimal tokens)
ntm --robot-markdown            # System state as markdown tables
ntm --robot-health              # Project health summary
ntm --robot-version             # Version info
ntm --robot-help                # Full robot mode documentation
```

**Agent Control:**

```bash
ntm --robot-send=SESSION --msg="Fix auth" --type=claude  # Send to agents
ntm --robot-ack=SESSION --ack-timeout=30s                # Watch for responses
ntm --robot-spawn=SESSION --spawn-cc=2 --spawn-wait      # Create session
ntm --robot-interrupt=SESSION --interrupt-msg="Stop"     # Send Ctrl+C
ntm --robot-assign=SESSION --assign-beads=bd-1,bd-2      # Assign work to agents
ntm --robot-replay=SESSION --replay-id=ID                # Replay command from history
ntm --robot-dismiss-alert=ALERT_ID                       # Dismiss an alert
```

**Bead Management:**

```bash
ntm --robot-bead-claim=BEAD_ID --bead-assignee=agent     # Claim a bead for work
ntm --robot-bead-create --bead-title="Fix auth bug" --bead-type=bug --bead-priority=1
ntm --robot-bead-show=BEAD_ID                            # Show bead details
ntm --robot-bead-close=BEAD_ID --bead-close-reason="Fixed"  # Close a bead
```

**CASS Integration (Cross-Agent Search):**

```bash
ntm --robot-cass-search="auth error" --cass-since=7d     # Search past conversations
ntm --robot-cass-status                                  # CASS health/stats
ntm --robot-cass-context="how to implement auth"         # Get relevant context
```

**Session State Management:**

```bash
ntm --robot-save=SESSION --save-output=/path/state.json  # Save session state
ntm --robot-restore=mystate --restore-dry                # Restore (dry-run)
ntm --robot-history=SESSION --history-stats              # Session history
ntm --robot-tokens --tokens-group-by=model               # Token usage analytics
```

**Supporting Flags:**

| Flag | Use With | Description |
|------|----------|-------------|
| `--panes=1,2,3` | tail, send, ack, interrupt | Filter to specific pane indices |
| `--type=claude` | send, ack, interrupt | Filter by agent type (claude/cc, codex/cod, gemini/gmi) |
| `--all` | send, interrupt | Include user pane |
| `--lines=N` | tail | Lines per pane (default 20) |
| `--since=TIMESTAMP` | snapshot | RFC3339 timestamp for delta |
| `--track` | send | Combined send+ack mode |
| `--json` | dashboard, markdown | Force JSON output |
| `--inspect-index=N` | inspect-pane | Pane index to inspect (default 0) |
| `--inspect-lines=N` | inspect-pane | Output lines to capture (default 100) |
| `--inspect-code` | inspect-pane | Parse and extract code blocks |
| `--files-window=T` | files | Time window: 5m, 15m, 1h, all (default 15m) |
| `--files-limit=N` | files | Max changes to return (default 100) |
| `--metrics-period=T` | metrics | Period: 1h, 24h, 7d, all (default 24h) |
| `--palette-category` | palette | Filter commands by category |
| `--palette-search` | palette | Search commands by text |
| `--replay-id=ID` | replay | History entry ID to replay |
| `--replay-dry-run` | replay | Preview without executing |
| `--dismiss-all` | dismiss-alert | Dismiss all matching alerts |
| `--bead-title=TEXT` | bead-create | Title for new bead (required) |
| `--bead-type=TYPE` | bead-create | Type: task, bug, feature, epic, chore |
| `--bead-priority=N` | bead-create | Priority 0-4 (0=critical, 4=backlog) |
| `--bead-description=TEXT` | bead-create | Description for new bead |
| `--bead-labels=a,b` | bead-create | Comma-separated labels |
| `--bead-depends-on=id1,id2` | bead-create | Comma-separated dependency IDs |
| `--bead-assignee=NAME` | bead-claim | Assignee name for claim |
| `--bead-close-reason=TEXT` | bead-close | Reason for closing |

This enables AI agents to:
- Discover existing sessions and their agent configurations
- Plan multi-agent workflows programmatically
- Monitor context window usage across agents
- Inspect individual panes with detailed state detection
- Track file changes with agent attribution and conflict detection
- Export session metrics for analysis
- Query the command palette programmatically
- Replay commands from history
- Manage alerts programmatically
- Search past agent conversations via CASS
- Assign beads/tasks to specific agents
- Manage beads programmatically (claim, create, show, close)
- Save and restore session state
- Track token usage and history

**Example JSON output (`--robot-status`):**

```json
{
  "success": true,
  "timestamp": "2025-01-15T10:30:00Z",
  "sessions": [
    {
      "name": "myproject",
      "attached": true,
      "windows": 1,
      "agents": [
        {"type": "claude", "pane": "myproject__cc_1", "active": true},
        {"type": "codex", "pane": "myproject__cod_1", "active": true}
      ]
    }
  ],
  "summary": {
    "total_sessions": 1,
    "total_agents": 2,
    "by_type": {"claude": 1, "codex": 1}
  }
}
```

**Example JSON output (`--robot-context`):**

```json
{
  "success": true,
  "session": "myproject",
  "captured_at": "2025-01-15T10:30:00Z",
  "agents": [
    {
      "pane": "myproject__cc_1",
      "agent_type": "claude",
      "model": "sonnet",
      "estimated_tokens": 45000,
      "with_overhead": 54000,
      "context_limit": 200000,
      "usage_percent": 27.0,
      "usage_level": "Low",
      "confidence": "estimated"
    },
    {
      "pane": "myproject__cod_1",
      "agent_type": "codex",
      "model": "gpt4",
      "estimated_tokens": 85000,
      "with_overhead": 102000,
      "context_limit": 128000,
      "usage_percent": 79.7,
      "usage_level": "High",
      "confidence": "estimated"
    }
  ],
  "summary": {
    "total_agents": 2,
    "high_usage_count": 1,
    "avg_usage": 53.35
  },
  "_agent_hints": {
    "low_usage_agents": ["myproject__cc_1"],
    "high_usage_agents": ["myproject__cod_1"],
    "suggestions": ["1 agent(s) have high context usage", "1 agent(s) have room for additional work"]
  }
}
```

**Example JSON output (`--robot-files`):**

```json
{
  "success": true,
  "timestamp": "2025-01-15T10:35:00Z",
  "session": "myproject",
  "time_window": "15m",
  "count": 3,
  "changes": [
    {
      "timestamp": "2025-01-15T10:33:00Z",
      "path": "internal/api/handler.go",
      "operation": "modify",
      "agents": ["claude"],
      "session": "myproject"
    }
  ],
  "summary": {
    "total_changes": 3,
    "unique_files": 2,
    "by_agent": {"claude": 2, "codex": 1},
    "by_operation": {"modify": 3},
    "most_active_agent": "claude",
    "conflicts": [
      {
        "path": "internal/api/handler.go",
        "agents": ["claude", "codex"],
        "severity": "warning",
        "first_edit": "2025-01-15T10:30:00Z",
        "last_edit": "2025-01-15T10:33:00Z"
      }
    ]
  },
  "_agent_hints": {
    "summary": "3 changes to 2 files in the last 15m",
    "warnings": ["1 file(s) modified by multiple agents - potential conflicts"]
  }
}
```

**Exit Codes:**

| Code | Meaning | JSON Field |
|------|---------|------------|
| 0 | Success | `"success": true` |
| 1 | Error | `"success": false, "error_code": "..."` |
| 2 | Unavailable | `"success": false, "error_code": "NOT_IMPLEMENTED"` |

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

In status output and tables, agent types are shown with compact indicators:
- **C** = Claude
- **X** = Codex
- **G** = Gemini
- **U** = User pane

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

NTM works out of the box with sensible defaults; no configuration file is required. When no config file exists, NTM uses built-in defaults appropriate for your platform.

Optional configuration lives in `~/.config/ntm/config.toml`:

```bash
# Create default config (optional - NTM works without it)
ntm config init

# Show current config (shows effective config, including defaults)
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

### Project Config (`.ntm/`)

NTM also supports **project-specific configuration** when you run commands inside a repo that contains a `.ntm/config.toml` (NTM searches upward from your current directory).

Create a scaffold in the current directory:

```bash
ntm config project init
ntm config project init --force   # overwrite .ntm/config.toml if it already exists
```

Project config overrides the global config and is useful for:
- Default agent counts for `ntm spawn` (when you don’t pass `--cc/--cod/--gmi`)
- Project palette commands (`[palette].file`, relative to `.ntm/`)
- Project prompt templates (`[templates].dir`, relative to `.ntm/`)

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NTM_PROJECTS_BASE` | `~/Developer` (macOS) or `/data/projects` (Linux) | Base directory for all projects |
| `NTM_THEME` | `auto` | Color theme: `auto` (detect light/dark), `mocha`, `macchiato`, `nord`, `latte`, or `plain` (no-color) |
| `NTM_ICONS` | auto-detect | Icon set: `nerd`, `unicode`, `ascii` |
| `NTM_USE_ICONS` | auto-detect | Force icons: `1` (on) or `0` (off) |
| `NERD_FONTS` | auto-detect | Nerd Fonts available: `1` or `0` |

---

## Command Hooks

NTM supports pre- and post-command hooks that run custom scripts before and after key operations. This enables automation, logging, notifications, and integration with external tools.

### Hook Configuration

Hooks are defined in `~/.config/ntm/hooks.toml` (or in the main `config.toml` under `[[command_hooks]]`):

```toml
# ~/.config/ntm/hooks.toml

# Pre-spawn hook: runs before agents are spawned
[[command_hooks]]
event = "pre-spawn"
command = "echo 'Starting new session'"
name = "log-spawn-start"
description = "Log when a session starts"

# Post-spawn hook: runs after agents are spawned
[[command_hooks]]
event = "post-spawn"
command = "notify-send 'NTM' 'Agents spawned for $NTM_SESSION'"
name = "desktop-notify"
description = "Send desktop notification"

# Pre-send hook: runs before prompts are sent
[[command_hooks]]
event = "pre-send"
command = "echo \"$(date): Sending to $NTM_SEND_TARGETS\" >> ~/.ntm-send.log"
name = "log-sends"
description = "Log all send commands"

# Post-send hook: runs after prompts are delivered
[[command_hooks]]
event = "post-send"
command = "/path/to/my-webhook.sh"
name = "webhook"
timeout = "10s"
continue_on_error = true
```

### Available Events

| Event | When It Runs | Use Cases |
|-------|--------------|-----------|
| `pre-spawn` | Before creating session/agents | Validation, setup, cleanup |
| `post-spawn` | After agents are launched | Notifications, logging, auto-send initial prompts |
| `pre-send` | Before sending prompts | Logging, rate limiting, prompt validation |
| `post-send` | After prompts delivered | Webhooks, analytics, notifications |
| `pre-add` | Before adding agents | Validation |
| `post-add` | After adding agents | Notifications |
| `pre-create` | Before creating session | Validation |
| `post-create` | After creating session | Setup |
| `pre-shutdown` | Before killing session | Cleanup, backup |
| `post-shutdown` | After killing session | Cleanup |

### Hook Options

```toml
[[command_hooks]]
event = "post-send"              # Required: which event triggers this hook
command = "./my-script.sh"       # Required: shell command to execute

# Optional settings
name = "my-hook"                 # Identifier for logging
description = "What this does"   # Documentation
timeout = "30s"                  # Max execution time (default: 30s, max: 10m)
enabled = true                   # Set to false to disable without removing
continue_on_error = false        # If true, NTM continues even if hook fails
workdir = "${PROJECT}"           # Working directory (supports variables)

# Custom environment variables
[command_hooks.env]
MY_VAR = "custom_value"
```

### Environment Variables

All hooks have access to these environment variables:

| Variable | Description | Available In |
|----------|-------------|--------------|
| `NTM_SESSION` | Session name | All events |
| `NTM_PROJECT_DIR` | Project directory path | All events |
| `NTM_HOOK_EVENT` | Event name (e.g., "pre-send") | All events |
| `NTM_HOOK_NAME` | Hook name (if specified) | All events |
| `NTM_PANE` | Pane identifier | Pane-specific events |
| `NTM_MESSAGE` | Prompt being sent (truncated to 1000 chars) | Send events |
| `NTM_SEND_TARGETS` | Target description (e.g., "cc", "all", "agents") | Send events |
| `NTM_TARGET_CC` | "true" if targeting Claude | Send events |
| `NTM_TARGET_COD` | "true" if targeting Codex | Send events |
| `NTM_TARGET_GMI` | "true" if targeting Gemini | Send events |
| `NTM_TARGET_ALL` | "true" if targeting all panes | Send events |
| `NTM_PANE_INDEX` | Specific pane index (-1 if not specified) | Send events |
| `NTM_DELIVERED_COUNT` | Number of successful deliveries | Post-send only |
| `NTM_FAILED_COUNT` | Number of failed deliveries | Post-send only |
| `NTM_TARGET_PANES` | List of targeted pane indices | Post-send only |
| `NTM_AGENT_COUNT_CC` | Number of Claude agents | Spawn events |
| `NTM_AGENT_COUNT_COD` | Number of Codex agents | Spawn events |
| `NTM_AGENT_COUNT_GMI` | Number of Gemini agents | Spawn events |
| `NTM_AGENT_COUNT_TOTAL` | Total number of agents | Spawn events |

### Example Hooks

**Log all sends to a file:**

```toml
[[command_hooks]]
event = "pre-send"
name = "send-logger"
command = '''
echo "$(date -Iseconds) | Session: $NTM_SESSION | Targets: $NTM_SEND_TARGETS" >> ~/.ntm/send.log
echo "Message: $NTM_MESSAGE" >> ~/.ntm/send.log
echo "---" >> ~/.ntm/send.log
'''
```

**Desktop notification on spawn:**

```toml
[[command_hooks]]
event = "post-spawn"
name = "spawn-notify"
command = "notify-send 'NTM' 'Session $NTM_SESSION ready with $NTM_AGENT_COUNT_TOTAL agents'"
```

**Webhook integration:**

```toml
[[command_hooks]]
event = "post-send"
name = "slack-webhook"
timeout = "5s"
continue_on_error = true
command = '''
curl -s -X POST "$SLACK_WEBHOOK_URL" \
  -H 'Content-type: application/json' \
  -d "{\"text\": \"NTM: Sent prompt to $NTM_SEND_TARGETS in $NTM_SESSION\"}"
'''

[command_hooks.env]
SLACK_WEBHOOK_URL = "https://hooks.slack.com/services/..."
```

**Validate prompts before sending:**

```toml
[[command_hooks]]
event = "pre-send"
name = "prompt-validator"
command = '''
# Block empty prompts
if [ -z "$NTM_MESSAGE" ]; then
  echo "Error: Empty prompt not allowed" >&2
  exit 1
fi

# Block prompts containing sensitive patterns
if echo "$NTM_MESSAGE" | grep -qiE "(password|secret|api.?key)"; then
  echo "Warning: Prompt may contain sensitive data" >&2
  exit 1
fi
'''
```

**Auto-save outputs before shutdown:**

```toml
[[command_hooks]]
event = "pre-shutdown"
name = "auto-backup"
command = '''
mkdir -p ~/.ntm/backups
ntm save "$NTM_SESSION" -o ~/.ntm/backups 2>/dev/null || true
'''
continue_on_error = true
```

### Hook Behavior

**Pre-hooks:**
- Run before the command executes
- If a pre-hook fails (non-zero exit), the command is aborted
- Set `continue_on_error = true` to run the command even if hook fails

**Post-hooks:**
- Run after the command completes
- Failures are logged but don't fail the overall command
- Useful for notifications and cleanup

**Timeouts:**
- Default: 30 seconds
- Maximum: 10 minutes
- Hooks that exceed timeout are killed

**Execution:**
- Hooks run in a shell (`sh -c "command"`)
- Working directory defaults to project directory
- Standard output and errors are captured and displayed

---

## CASS Integration

CASS (Cross-Agent Search System) indexes past agent conversations across multiple tools (Claude Code, Codex, Cursor, Gemini, ChatGPT) so you can reuse solved problems and learn from prior sessions.

### Querying Past Sessions

```bash
# Search for relevant past work
ntm --robot-cass-search="authentication error" --cass-since=7d

# Get context relevant to current task
ntm --robot-cass-context="how to implement rate limiting"

# Check CASS health and stats
ntm --robot-cass-status
```

### Dashboard Integration

The NTM dashboard displays CASS context in a dedicated panel, showing:
- Relevant past sessions matching current project context
- Similarity scores for each match
- Quick access to session details

### Configuration

CASS works automatically when installed. Configure search behavior in your config:

```toml
[cass]
# Default search parameters
default_limit = 10
include_agents = ["claude", "codex", "gemini"]
```

---

## Context Window Rotation

NTM monitors context window usage for each AI agent and automatically rotates agents before they exhaust their context, ensuring uninterrupted workflows during long sessions.

### How It Works

1. **Monitoring**: Token usage is estimated using multiple strategies (message counts, cumulative tokens, session duration)
2. **Warning**: When usage exceeds the warning threshold (default 80%), NTM alerts you
3. **Compaction**: Before rotating, NTM tries to compact the context (using `/compact` for Claude or summarization prompts)
4. **Rotation**: If compaction doesn't reduce usage enough, a fresh agent is spawned with a handoff summary

### Configuration

Context rotation is enabled by default. Configure in `~/.config/ntm/config.toml`:

```toml
[context_rotation]
enabled = true              # Master toggle
warning_threshold = 0.80    # Warn at 80% usage
rotate_threshold = 0.95     # Rotate at 95% usage
summary_max_tokens = 2000   # Max tokens for handoff summary
min_session_age_sec = 300   # Don't rotate agents younger than 5 minutes
try_compact_first = true    # Try compaction before rotation
require_confirm = false     # Auto-rotate without confirmation
```

### Robot Mode Commands

```bash
ntm --robot-context=SESSION          # Get context usage for all agents
ntm --robot-context=SESSION --json   # JSON output for automation
```

**Example output:**

```json
{
  "success": true,
  "session": "myproject",
  "agents": [
    {
      "pane": "myproject__cc_1",
      "estimated_tokens": 145000,
      "context_limit": 200000,
      "usage_percent": 72.5,
      "usage_level": "Medium",
      "needs_warning": false,
      "needs_rotation": false
    }
  ]
}
```

### Handoff Summary

When an agent is rotated, the old agent is asked for a structured summary containing:
- Current task being worked on
- Progress made so far
- Key decisions taken
- Active files being modified
- Any blockers or issues

This summary is passed to the fresh agent so it can continue where the old one left off.

### Dashboard Integration

The dashboard displays context usage for each agent pane:
- **Green**: < 40% usage (plenty of room)
- **Yellow**: 40-60% usage (comfortable)
- **Orange**: 60-80% usage (approaching threshold)
- **Red**: > 80% usage (warning/needs attention)

### Automatic Compaction Recovery

When an agent's context is compacted (conversation summarized to reduce tokens), NTM can automatically send a recovery prompt to help the agent regain project context. This prevents the disorientation that occurs when an agent loses detailed conversation history.

**How It Works:**

1. **Detection**: NTM monitors agent output for compaction indicators (e.g., "Conversation compacted")
2. **Recovery Prompt**: A customizable prompt is automatically sent (default: "Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.")
3. **Bead Context**: If you use Beads for issue tracking, the recovery prompt includes current project state (bottlenecks, next actions, in-progress tasks)
4. **Cooldown Protection**: A 30-second cooldown prevents prompt spam
5. **Max Recoveries**: Limits per-pane recoveries (default: 5) to avoid infinite loops

**Configuration:**

```toml
[context_rotation.recovery]
enabled = true
prompt = "Reread AGENTS.md so it's still fresh in your mind. Use ultrathink."
cooldown_seconds = 30
max_recoveries_per_pane = 5
include_bead_context = true   # Include project state from bv
```

**Detection Patterns by Agent Type:**

| Agent | Detection Patterns |
|-------|-------------------|
| **Claude** | "Conversation compacted", "ran out of context", "This conversation is getting long" |
| **Codex** | "context limit reached", "history cleared", "Context was truncated" |
| **Gemini** | "context window exceeded", "conversation reset", "history was compacted" |
| **Generic** | "continuing from summary", "previous context was summarized", "I've lost some context" |

**With Bead Context Enabled:**

The recovery prompt includes live project information:

```
Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.

# Project Context from Beads

## Current Bottlenecks (resolve these to unblock progress):
- bd-42

## Recommended Next Actions:
- [bd-45] Implement user authentication
- [bd-47] Add unit tests for API

## Project Health: healthy

## Dependency Summary

### Tasks In Progress:
- [bd-43] Refactor database layer

**Status**: 2 blocked, 5 ready to work on
```

---

## Agent Mail Integration

NTM integrates with Agent Mail for multi-agent coordination across sessions and projects.

### Features

- **Message routing**: Send messages between agents in different sessions
- **File reservations**: Claim files to prevent conflicting edits
- **Thread tracking**: Organize discussions by topic or feature
- **Human Overseer mode**: Send high-priority instructions from the CLI

### CLI Commands

```bash
ntm mail send myproject --to GreenCastle "Review the API changes"
ntm mail send myproject --all "Checkpoint: sync and report status"
ntm mail inbox myproject                    # View agent inboxes
ntm mail read myproject --agent BlueLake    # Read specific agent's mail
ntm mail ack myproject 42                   # Acknowledge message
```

### Robot Mode

```bash
ntm --robot-mail                            # Get mail state as JSON
```

### Pre-commit Guard

Install the Agent Mail pre-commit guard to prevent commits that conflict with other agents' file reservations:

```bash
ntm hooks guard install
ntm hooks guard uninstall  # Remove later
```

---

## Notifications

NTM can notify you when important events occur in your agent sessions. Notifications can be delivered through multiple channels at once.

### Notification Channels

| Channel | Description | Use Case |
|---------|-------------|----------|
| **Desktop** | Native OS notifications (macOS/Linux) | Immediate attention for errors/rate limits |
| **Webhook** | HTTP POST to any URL with templated payload | Slack, Discord, custom dashboards |
| **Shell** | Execute arbitrary shell commands | Custom integrations, logging pipelines |
| **Log File** | Append to a log file | Audit trails, debugging |

### Configuration

Configure notifications in `~/.config/ntm/config.toml`:

```toml
[notifications]
enabled = true
# Which events trigger notifications
events = ["agent.error", "agent.crashed", "agent.rate_limit", "rotation.needed"]

[notifications.desktop]
enabled = true
title = "NTM"   # Title prefix for desktop notifications

[notifications.webhook]
enabled = false
url = "https://hooks.slack.com/services/..."
method = "POST"
template = '{"text": "NTM: {{.Type}} - {{jsonEscape .Message}}"}'

[notifications.webhook.headers]
Authorization = "Bearer your-token"

[notifications.shell]
enabled = false
command = "/path/to/your-handler.sh"
pass_json = true   # Pass event as JSON via stdin

[notifications.log]
enabled = true
path = "~/.config/ntm/notifications.log"
```

### Event Types

| Event | Description |
|-------|-------------|
| `agent.error` | Agent hit an error state |
| `agent.crashed` | Agent process exited unexpectedly |
| `agent.restarted` | Agent was auto-restarted |
| `agent.idle` | Agent waiting for input |
| `agent.rate_limit` | Agent hit rate limit |
| `rotation.needed` | Account rotation recommended |
| `session.created` | New session spawned |
| `session.killed` | Session terminated |
| `health.degraded` | Overall session health dropped |

### Webhook Templates

Webhook payloads use Go templates with access to event data:

```toml
# Slack-compatible template
template = '''
{
  "text": "NTM Alert: {{.Type}}",
  "attachments": [{
    "color": "{{if eq .Type "agent.error"}}danger{{else}}warning{{end}}",
    "fields": [
      {"title": "Session", "value": "{{jsonEscape .Session}}", "short": true},
      {"title": "Agent", "value": "{{jsonEscape .Agent}}", "short": true},
      {"title": "Message", "value": "{{jsonEscape .Message}}"}
    ]
  }]
}
'''
```

Available template fields:
- `{{.Type}}` - Event type (e.g., "agent.error")
- `{{.Timestamp}}` - ISO 8601 timestamp
- `{{.Session}}` - Session name
- `{{.Pane}}` - Pane identifier
- `{{.Agent}}` - Agent type (claude, codex, gemini)
- `{{.Message}}` - Human-readable message
- `{{.Details}}` - Map of additional details (varies by event)
- `{{jsonEscape .Field}}` - Escape strings for safe JSON embedding

### Shell Handler

When `pass_json = true`, your script receives the full event as JSON via stdin:

```bash
#!/bin/bash
# /path/to/your-handler.sh

# Read JSON from stdin
EVENT=$(cat)

# Parse with jq
TYPE=$(echo "$EVENT" | jq -r '.type')
MESSAGE=$(echo "$EVENT" | jq -r '.message')
SESSION=$(echo "$EVENT" | jq -r '.session')

# Custom handling
if [[ "$TYPE" == "agent.crashed" ]]; then
    # Send to PagerDuty, restart agent, etc.
    curl -X POST "https://your-alert-system/..."
fi
```

Environment variables are also set for simpler scripts:
- `NTM_EVENT_TYPE` - Event type
- `NTM_EVENT_MESSAGE` - Message
- `NTM_EVENT_SESSION` - Session name
- `NTM_EVENT_PANE` - Pane ID
- `NTM_EVENT_AGENT` - Agent type

---

## Themes & Icons

### Color Themes

NTM uses the Catppuccin color palette by default, with support for multiple themes:

| Theme | Description |
|-------|-------------|
| `auto` | Detects terminal background; dark → mocha, light → latte |
| `mocha` | Default dark theme, warm and cozy |
| `macchiato` | Darker variant with more contrast |
| `latte` | Light variant for light terminals |
| `nord` | Arctic-inspired, cooler tones |
| `plain` | No-color theme (uses terminal defaults; best for low-color terminals) |

Set via environment variable:

```bash
export NTM_THEME=auto
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

### Accessibility & Terminal Compatibility

Reduce motion (disable shimmer/pulse animations):

```bash
export NTM_REDUCE_MOTION=1
```

Disable colors (respects the `NO_COLOR` standard, with an NTM override):

```bash
export NO_COLOR=1        # Any value disables colors
export NTM_NO_COLOR=1    # NTM-specific no-color toggle
export NTM_NO_COLOR=0    # Force colors ON (even if NO_COLOR is set)
export NTM_THEME=plain   # Explicit no-color theme (escape hatch)
```

### Wide/High-Resolution Displays
- Width tiers: stacked layouts below 120 cols; split list/detail at 120+; richer metadata at 200+; tertiary labels/variants/locks at 240+; mega layouts at 320+.
- Give dashboard/status/palette at least 120 cols for split view; 200+ unlocks wider gutters and secondary columns; 240+ enables the full detail bars; 320+ enables mega layouts.
- Icons are ASCII-first by default. Switch to `NTM_ICONS=unicode` or `NTM_ICONS=nerd` only if your terminal font renders them cleanly; otherwise stay on ASCII to avoid misaligned gutters.
- Troubleshooting: if text wraps or glyphs drift, widen the pane, drop to `NTM_ICONS=ascii`, and ensure a true monospace font (Nerd Fonts installed before using `NTM_ICONS=nerd`).

| Tier | Width | Behavior |
| ---- | ----- | -------- |
| Narrow | <120 cols | Stacked layout, minimal badges |
| Split | 120-199 cols | List/detail split view |
| Wide | 200-239 cols | Secondary metadata, wider gutters |
| Ultra | 240-319 cols | Tertiary labels/variants/locks, max detail |
| Mega | ≥320 cols | Mega layouts, richest metadata |

---

## Typical Workflow

### Workflow Cookbook

#### First run (10 minutes)

```bash
# 1) Install + shell integration (zsh example)
curl -fsSL https://raw.githubusercontent.com/Dicklesworthstone/ntm/main/install.sh | bash
echo 'eval "$(ntm init zsh)"' >> ~/.zshrc && source ~/.zshrc

# 2) Sanity check + quick orientation
ntm deps -v
ntm tutorial

# 3) Spawn a session and bind the palette popup key (F6 by default)
ntm spawn myapi --cc=2 --cod=1
ntm bind
```

#### Daily loop (attach → palette → send → dashboard → copy/save → detach)

```bash
ntm attach myapi

# Inside the dashboard/palette: press ? for key hints
ntm dashboard myapi
ntm palette myapi

# Useful capture loop
ntm copy myapi --cc --last 200
ntm save myapi -o ~/logs/myapi

# Detach from tmux (agents keep running): Ctrl+B, then D
```

#### SSH flow (remote-first)

```bash
ssh user@host

# Sessions persist on the server
ntm list
ntm attach myapi

# Inside tmux, these auto-select the current session:
ntm dashboard
ntm palette

# If clipboard isn't available on the remote, save to a file instead:
ntm copy myapi --cc --output out.txt
```

#### Troubleshooting patterns (fast fixes)

- No sessions exist: `ntm spawn <name>`
- Icons drift/misaligned gutters: `export NTM_ICONS=ascii`
- Too much motion/flicker: `export NTM_REDUCE_MOTION=1`
- Need plain output / low-color terminal: `export NTM_THEME=plain` (or `export NO_COLOR=1`)
- Copy complains about non-interactive mode: pass a session explicitly (e.g. `ntm copy myapi --cc`)

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
# Press ? for key hints/help overlay
# Ctrl+P to pin/unpin a command; Ctrl+F to favorite/unfavorite
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

**Pre-commit: Block Conflicting Commits (Agent Mail Guard)**

Install the Agent Mail pre-commit guard so commits fail when you’re about to commit files reserved by other agents:

```bash
ntm hooks guard install

# Warn-only mode (doesn't block commits)
export AGENT_MAIL_GUARD_MODE=warn
```

Remove it later:

```bash
ntm hooks guard uninstall
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

## Auto-Scanner (UBS Integration)

NTM integrates with [UBS (Ultimate Bug Scanner)](https://github.com/...) to automatically scan your project for bugs when files change.

### How It Works

1. **File Watching**: NTM monitors your project directory for file changes
2. **Debouncing**: Multiple rapid changes are batched (default: 1 second delay)
3. **Automatic Scan**: UBS is triggered on relevant file changes
4. **Exclusions**: Common directories (`.git`, `node_modules`, `vendor`) are ignored

### Configuration

Configure auto-scanning in `~/.config/ntm/config.toml` or `.ntm/config.toml`:

```toml
[scanner]
enabled = true
ubs_path = ""              # Path to ubs binary (auto-detect if empty)
debounce_ms = 1000         # Wait time before scanning after changes
timeout_seconds = 60       # Max time for a scan to complete

[scanner.defaults]
timeout = "60s"
exclude = [".git", "node_modules", "vendor", ".beads", "*.min.js", "*.min.css"]

# Dashboard integration
[scanner.dashboard]
show_findings = true       # Display findings in dashboard
max_display = 10           # Max findings to show
```

### Dashboard Integration

When auto-scanning is enabled, the NTM dashboard displays:
- Scan status (running, complete, error)
- Latest scan results with severity breakdown
- Quick access to finding details

### Manual Scanning

You can also trigger scans manually:

```bash
# Scan specific files
ubs internal/cli/send.go

# Scan staged files before commit
ubs $(git diff --name-only --cached)

# Language-filtered scan
ubs --only=go internal/
```

---

## Conflict Tracking

NTM tracks file modifications across all agents to detect potential conflicts when multiple agents edit the same files.

### How It Works

1. **Change Recording**: All file modifications by agents are logged with timestamps
2. **Conflict Detection**: When multiple agents modify the same file, a conflict is flagged
3. **Severity Classification**:
   - **Warning**: Two agents edited the same file
   - **Critical**: Three or more agents, or edits within 10 minutes of each other

### Viewing Conflicts

```bash
# Check for conflicts in robot mode
ntm --robot-snapshot --since=1h | jq '.conflicts'
```

### Dashboard Integration

The dashboard shows conflict indicators on affected panes, with visual severity coding (yellow for warnings, red for critical).

### Prevention

Use Agent Mail file reservations to prevent conflicts:

```bash
# Reserve files before editing
ntm mail reserve myproject --agent BlueLake --paths "internal/api/*.go"
```

---

## Event Logging & Analytics

NTM logs all session activity to JSONL files for analytics, debugging, and audit trails.

### Event Types

| Category | Events |
|----------|--------|
| **Session** | `session_create`, `session_kill`, `session_attach` |
| **Agent** | `agent_spawn`, `agent_add`, `agent_crash`, `agent_restart` |
| **Communication** | `prompt_send`, `prompt_broadcast`, `interrupt` |
| **State** | `checkpoint_create`, `checkpoint_restore`, `session_save`, `session_restore` |
| **Templates** | `template_use` |
| **Errors** | `error` |

### Log Location

Events are logged to `~/.config/ntm/events.jsonl` with automatic rotation.

### Log Format

```json
{
  "timestamp": "2025-01-02T15:04:05Z",
  "type": "prompt_send",
  "session": "myproject",
  "data": {
    "target_count": 3,
    "prompt_length": 256,
    "template": "code_review",
    "estimated_tokens": 85
  }
}
```

### Querying Events

```bash
# View recent events
tail -100 ~/.config/ntm/events.jsonl | jq 'select(.type == "prompt_send")'

# Count events by type
cat ~/.config/ntm/events.jsonl | jq -s 'group_by(.type) | map({type: .[0].type, count: length})'

# Get session history
cat ~/.config/ntm/events.jsonl | jq 'select(.session == "myproject")'
```

### Configuration

```toml
[events]
enabled = true
path = "~/.config/ntm/events.jsonl"
max_file_size_mb = 50      # Rotate when file exceeds this size
retention_days = 30        # Delete logs older than this
```

---

## Agent Monitoring

NTM provides comprehensive real-time monitoring of agent states, health, and output through dedicated commands.

### Activity States

The `ntm activity` command displays real-time activity states for all agents in a session:

```bash
# Show activity for all agents
ntm activity myproject

# Filter by agent type
ntm activity myproject --cc          # Only Claude agents
ntm activity myproject --cod         # Only Codex agents
ntm activity myproject --gmi         # Only Gemini agents

# Watch mode (auto-refresh)
ntm activity myproject --watch
ntm activity myproject -w --interval 1000   # Refresh every 1s

# JSON output for scripting
ntm activity myproject --json
```

**Activity States:**

| State | Icon | Color | Description |
|-------|------|-------|-------------|
| WAITING | ● | Green | Agent is idle, ready for work |
| GENERATING | ▶ | Blue | Agent is actively producing output |
| THINKING | ◐ | Yellow | Agent is processing (no output yet) |
| ERROR | ✗ | Red | Agent encountered an error |
| STALLED | ◯ | Red | Agent stopped unexpectedly |

**Output Fields:**

- **Pane**: The pane index within the session
- **Agent**: Agent type (claude, codex, gemini)
- **State**: Current activity state
- **Velocity**: Output rate in characters/second
- **Duration**: Time in current state

### Health Checking

The `ntm health` command provides a comprehensive health assessment:

```bash
# Check all agents
ntm health myproject

# JSON output
ntm health myproject --json
```

**Health Indicators:**

| Status | Description |
|--------|-------------|
| OK | Agent is healthy and responsive |
| WARN | Potential issues detected (stale, slow) |
| ERROR | Agent has crashed or is unresponsive |

**Detected Issues:**
- Rate limit detection
- Process crashes
- Memory pressure
- Stale output (no activity for extended periods)

The command exits with appropriate codes for scripting:
- Exit 0: All healthy
- Exit 1: Warnings detected
- Exit 2: Errors detected

### Output Streaming

The `ntm watch` command streams agent output without attaching to the tmux session:

```bash
# Stream all panes
ntm watch myproject

# Filter by agent type
ntm watch myproject --cc          # Only Claude agents
ntm watch myproject --cod         # Only Codex agents

# Only show new output (no initial tail)
ntm watch myproject --activity

# Customize tail and polling
ntm watch myproject --tail 50 --interval 500

# Disable timestamps
ntm watch myproject --no-timestamps
```

**File Watcher Mode:**

Trigger commands in agents when files change:

```bash
# Run tests when Go files change
ntm watch myproject --pattern="*.go" --command="go test ./..."

# Rebuild on source changes
ntm watch myproject --pattern="src/*.ts" --command="npm run build"

# Watch specific agent type
ntm watch myproject --cc --pattern="*.py" --command="pytest"
```

The file watcher automatically ignores common directories like `.git`, `node_modules`, `dist`, `vendor`, and `__pycache__`.

---

## Output Analysis

NTM includes tools for analyzing, searching, and extracting content from agent output.

### Code Block Extraction

The `ntm extract` command parses code blocks from agent output:

```bash
# Extract from all panes
ntm extract myproject

# Extract from specific pane
ntm extract myproject cc_1

# From most recently active pane
ntm extract myproject --last

# Filter by language
ntm extract myproject --lang=python
ntm extract myproject --lang=go

# Copy to clipboard
ntm extract myproject --copy
ntm extract myproject --copy -s 1    # Copy specific block (1-indexed)

# Apply code to detected files
ntm extract myproject --apply

# JSON output
ntm extract myproject --json
```

**Features:**
- Parses markdown fenced code blocks from agent output
- Detects file paths from context (comments, headers)
- Identifies new files vs. updates to existing files
- Interactive apply mode with confirmation prompts
- Warns about risky paths (absolute or escaping current directory)

### Output Comparison

The `ntm diff` command compares outputs from different agents:

```bash
# Compare two panes by title
ntm diff myproject cc_1 cod_1

# Compare by pane index
ntm diff myproject 1 2

# Unified diff format
ntm diff myproject cc_1 cod_1 --unified

# Compare only extracted code blocks
ntm diff myproject cc_1 cod_1 --code-only

# JSON output
ntm diff myproject cc_1 cod_1 --json
```

**Output Includes:**
- Line count comparison
- Similarity percentage
- Unified diff (with `--unified`)

This is useful for comparing how different agents (Claude vs. Codex) approach the same problem.

### Pane Search

The `ntm grep` command searches across all pane output buffers with regex support:

```bash
# Search all panes in a session
ntm grep 'error' myproject

# Case insensitive
ntm grep 'TODO' myproject -i

# Show context lines
ntm grep 'function' myproject -C 3     # 3 lines before and after
ntm grep 'error' myproject -B 2 -A 5   # 2 before, 5 after

# Filter by agent type
ntm grep 'error' myproject --cc        # Only Claude panes
ntm grep 'pattern' myproject --cod     # Only Codex panes

# Search all sessions
ntm grep 'pattern' --all

# List matching panes only
ntm grep 'error' myproject -l

# Limit search depth
ntm grep 'pattern' myproject -n 100    # Search last 100 lines per pane

# Invert match
ntm grep 'success' myproject -v        # Show non-matching lines

# JSON output
ntm grep 'error' myproject --json
```

**Regex Support:**

The search uses Go's regex syntax:
- `'error.*timeout'` - Error followed by timeout
- `'def\s+\w+'` - Python function definitions
- `'(?i)warning'` - Case insensitive

---

## Session Analytics

The `ntm analytics` command provides aggregated statistics from session events:

```bash
# Last 30 days summary
ntm analytics

# Last 7 days
ntm analytics --days 7

# Since specific date
ntm analytics --since 2025-01-01

# Include per-session breakdown
ntm analytics --sessions

# Output formats
ntm analytics --format json
ntm analytics --format csv
```

**Statistics Include:**
- Total sessions created
- Agent counts by type (Claude, Codex, Gemini)
- Prompts sent and character counts
- Estimated token usage
- Error occurrences by type

**Example Output:**

```
NTM Analytics - Last 30 days
========================================

Summary:
  Sessions:     47
  Agents:       142
  Prompts:      1,847
  Characters:   2,341,892
  Tokens (est): 668.2K

Agent Breakdown:
  Claude:
    Spawned:      89
    Prompts:      1,102
    Tokens (est): 412.5K
  Codex:
    Spawned:      38
    Prompts:      512
    Tokens (est): 178.2K
  Gemini:
    Spawned:      15
    Prompts:      233
    Tokens (est): 77.5K

Errors: 3
  rate_limit: 2
  timeout: 1
```

---

## File Reservations

When using Agent Mail for multi-agent coordination, NTM can display active file reservations:

```bash
# Show session's file reservations
ntm locks myproject

# Show all project reservations (all agents)
ntm locks myproject --all-agents

# JSON output
ntm locks myproject --json
```

**Output Fields:**
- Path pattern (glob supported)
- Agent holding the reservation
- Lock type (Exclusive or Shared)
- Time remaining until expiration
- Reason for the reservation

File reservations prevent conflicts when multiple agents work on the same codebase by signaling intent to modify specific files.

---

## Performance Profiler

NTM includes a built-in profiler for measuring startup performance and command execution times.

### Enabling Profiling

```bash
# Enable for a single command
NTM_PROFILE=1 ntm spawn myproject --cc=2

# Enable globally
export NTM_PROFILE=1
```

### Profile Output

When profiling is enabled, NTM outputs timing information:

```
=== NTM Performance Profile ===
Total Duration: 245.32ms
Span Count: 12

By Phase:
  startup:        42.15ms (17.2%) [4 spans]
  config:         18.50ms ( 7.5%) [2 spans]
  command:       184.67ms (75.3%) [6 spans]

Top Spans by Duration:
  145.20ms  tmux_spawn [command]
   38.50ms  config_load [startup]
   25.30ms  palette_load [config]
   18.20ms  theme_detect [startup]

Recommendations:
  ℹ [startup] Consider lazy loading palette commands
```

### JSON Output

```bash
NTM_PROFILE=1 ntm --robot-status --json 2>&1 | jq '.profile'
```

### Profile Categories

- **startup**: Initial loading (config, themes, icons)
- **config**: Configuration parsing and validation
- **command**: Actual command execution
- **deferred**: Lazy-loaded components
- **shutdown**: Cleanup operations

### Recommendations

The profiler automatically generates recommendations based on performance data:
- Slow startup components that could be lazy-loaded
- Commands that exceed expected duration
- Resource-intensive operations

---

## Prompt History

NTM maintains a history of all prompts sent to agents, enabling replay, analysis, and debugging.

### How It Works

Every prompt sent via `ntm send` or the command palette is recorded with:
- Timestamp and unique ID
- Session and target panes
- Full prompt text
- Source (CLI, palette, replay)
- Template used (if any)
- Success/error status
- Execution duration

### Viewing History

```bash
# Get session history via robot mode
ntm --robot-history=myproject --history-stats

# Filter by time range
ntm --robot-history=myproject --since=1h

# JSON output for processing
ntm --robot-history=myproject --json
```

### History Storage

History is stored in `~/.config/ntm/history.jsonl`:

```json
{
  "id": "1735830245123-a1b2c3d4",
  "ts": "2025-01-02T15:04:05Z",
  "session": "myproject",
  "targets": ["0", "1", "2"],
  "prompt": "Review the authentication implementation",
  "source": "palette",
  "template": "code_review",
  "success": true,
  "duration_ms": 125
}
```

### Configuration

```toml
[history]
enabled = true
path = "~/.config/ntm/history.jsonl"
max_entries = 10000        # Max entries to keep
retention_days = 90        # Delete entries older than this
```

### Use Cases

- **Debugging**: See exactly what was sent when an agent misbehaves
- **Replay**: Re-send successful prompts to new sessions
- **Analytics**: Track prompt patterns and agent usage
- **Audit**: Review all instructions given to agents

---

## Work Distribution

NTM integrates with bv (Beads Viewer) to provide intelligent work distribution based on dependency graph analysis. The `ntm work` commands help you prioritize tasks, identify bottlenecks, and distribute work effectively across agents.

### Triage Analysis

Get a complete triage analysis with prioritized recommendations:

```bash
ntm work triage              # Full triage with recommendations
ntm work triage --by-label   # Group by label/domain
ntm work triage --by-track   # Group by execution track
ntm work triage --quick      # Show only quick wins
ntm work triage --health     # Include project health metrics
ntm work triage --json       # Machine-readable output
```

The triage analysis includes:
- **Recommendations**: Ranked actionable items with impact scores
- **Quick Wins**: Low-effort, high-impact items to tackle first
- **Bottlenecks**: Issues blocking the most downstream work
- **Project Health**: Status distributions, graph metrics

### Alerts

Monitor for drift and proactive issues:

```bash
ntm work alerts                      # All alerts
ntm work alerts --critical-only      # Only critical alerts
ntm work alerts --type=stale_issue   # Filter by type
ntm work alerts --label=backend      # Filter by label
```

Alert types include:
- **Stale Issues**: Items untouched for too long
- **Priority Drift**: Urgency mismatches vs. actual impact
- **Blocking Cascades**: Single issues blocking many others
- **Cycle Detection**: Circular dependencies that need breaking

### Semantic Search

Search across issues and their context:

```bash
ntm work search "JWT authentication"  # Semantic search
ntm work search "rate limiting" -n 20 # Limit results
ntm work search "API" --label=backend # Filter by label
```

### Impact Analysis

Analyze the impact of specific files or changes:

```bash
ntm work impact src/api/*.go          # Impact of API files
ntm work impact --show-graph          # Include dependency visualization
```

### Next Action

Get the single best next action:

```bash
ntm work next                         # Top recommendation + claim command
ntm work next --json                  # Machine-readable
```

This returns the highest-impact, unblocked item with a ready-to-run `bd update` command to claim it.

---

## Safety System

NTM includes a safety system that blocks or warns about dangerous commands that could cause data loss or other irreversible damage. This is particularly important when AI agents are running autonomously.

### Protected Commands

The safety system recognizes dangerous patterns including:

| Pattern | Risk | Action |
|---------|------|--------|
| `git reset --hard` | Loses uncommitted changes | Block |
| `git push --force` | Overwrites remote history | Block |
| `rm -rf /` | Catastrophic deletion | Block |
| `git clean -fd` | Deletes untracked files | Approval required |
| `DROP TABLE` | Database destruction | Block |

### Status and Configuration

```bash
ntm safety status              # Show protection status
ntm safety blocked             # View blocked command history
ntm safety blocked --hours=24  # Blocked commands in last 24h
ntm safety check "git reset --hard HEAD~1"  # Test a command
```

### Installation

Install safety wrappers and hooks:

```bash
ntm safety install             # Install git wrapper + Claude hook
ntm safety install --force     # Overwrite existing
ntm safety uninstall           # Remove all safety components
```

When installed, the safety system:
1. **Git Wrapper**: Intercepts dangerous git commands before execution
2. **Claude Hook**: Integrates with Claude Code's PreToolUse hooks
3. **Policy Engine**: Evaluates commands against configurable rules

### Custom Policy

Create a custom policy in `~/.ntm/policy.yaml`:

```yaml
rules:
  - pattern: "rm -rf ${HOME}"
    action: block
    reason: "Prevents accidental home directory deletion"

  - pattern: "git push.*--force"
    action: approval
    reason: "Force push requires explicit confirmation"

  - pattern: "npm publish"
    action: approval
    reason: "Publishing requires review"
```

Actions:
- **block**: Immediately reject the command
- **approval**: Require explicit confirmation
- **warn**: Log a warning but allow execution
- **allow**: Explicitly permit (overrides default rules)

### Policy Management

```bash
ntm policy show                # Display current policy
ntm policy show --all          # Include default rules
ntm policy validate            # Check policy syntax
ntm policy reset               # Reset to defaults
ntm policy edit                # Open in $EDITOR
```

---

## Configuration Management

NTM provides commands for inspecting and managing your configuration.

### Viewing Configuration

```bash
ntm config show                # Display effective configuration
ntm config show --json         # JSON output
```

### Comparing with Defaults

See what you've customized:

```bash
ntm config diff                # Show differences from defaults
ntm config diff --json         # Machine-readable diff
```

Example output:
```
Configuration differences from defaults:

  projects_base: ~/Developer (default: /data/projects)
  tmux.default_panes: 12 (default: 10)
  context_rotation.warning_threshold: 0.85 (default: 0.8)
```

### Validation

Check your configuration for errors:

```bash
ntm config validate            # Validate current config
ntm config validate --json     # Machine-readable output
```

Validation checks:
- Syntax errors in TOML
- Invalid threshold values (e.g., > 1.0 for percentages)
- Missing required fields
- Path existence for critical directories
- Agent command validity

### Getting Specific Values

Retrieve individual configuration values:

```bash
ntm config get projects_base                    # Get single value
ntm config get context_rotation.warning_threshold
ntm config get agents.claude                    # Get agent command
ntm config get --json                           # All config as JSON
```

### Editing Configuration

```bash
ntm config edit                # Open in $EDITOR
```

### Reset to Defaults

```bash
ntm config reset               # Requires --confirm
ntm config reset --confirm     # Actually reset
```

---

## Pipeline Workflows

NTM supports YAML-defined workflows for complex multi-step operations. Pipelines can orchestrate agents, run commands, and manage dependencies.

### Defining a Pipeline

Create `.ntm/pipelines/` or use built-in pipelines:

```yaml
# .ntm/pipelines/review.yaml
name: code-review
description: Comprehensive code review workflow

variables:
  branch: main

steps:
  - id: fetch
    name: Fetch latest changes
    command: git fetch origin ${branch}

  - id: analyze
    name: Static analysis
    command: ubs .
    continue_on_error: true

  - id: review
    name: AI code review
    agent: claude
    prompt: |
      Review the changes in this branch. Focus on:
      1. Security vulnerabilities
      2. Performance issues
      3. Code quality
    depends_on: [fetch, analyze]

  - id: tests
    name: Run tests
    command: go test ./...
    parallel: true
    depends_on: [fetch]

  - id: summary
    name: Generate summary
    agent: claude
    prompt: "Summarize the review findings and test results."
    depends_on: [review, tests]
```

### Running Pipelines

```bash
ntm pipeline run review                    # Run the review pipeline
ntm pipeline run review --dry-run          # Show what would happen
ntm pipeline run review --var branch=dev   # Override variables
ntm pipeline run review --stage=analyze    # Run specific stage only
```

### Pipeline Commands

```bash
ntm pipeline list                          # List available pipelines
ntm pipeline status                        # Show running pipelines
ntm pipeline status --watch                # Live status updates
ntm pipeline cancel <run-id>               # Cancel a running pipeline
```

### Step Types

| Type | Description |
|------|-------------|
| `command` | Shell command execution |
| `agent` | Send prompt to AI agent |
| `parallel` | Run steps in parallel |
| `loop` | Iterate over items |
| `conditional` | Execute based on condition |

### Dependency Resolution

Pipelines use topological sorting to resolve dependencies:
- Steps with `depends_on` wait for dependencies
- Independent steps can run in parallel
- Cycle detection prevents infinite loops

---

## Session Checkpoints

Checkpoints capture the complete state of a tmux session at a point in time, enabling rollback and recovery.

### Creating Checkpoints

```bash
# Create a checkpoint
ntm checkpoint save myproject

# With description
ntm checkpoint save myproject -m "Before major refactor"

# Custom scrollback depth
ntm checkpoint save myproject --scrollback=500

# Skip git state capture
ntm checkpoint save myproject --no-git
```

**Captured Data:**
- Pane layout and configuration
- Agent types and commands
- Scrollback buffer content (configurable depth)
- Git repository state (branch, commit, uncommitted changes)
- Working directory

### Listing Checkpoints

```bash
# List all checkpoints across sessions
ntm checkpoint list

# List checkpoints for a specific session
ntm checkpoint list myproject

# JSON output
ntm checkpoint list --json
```

### Viewing Checkpoint Details

```bash
ntm checkpoint show myproject 20251210-143052
ntm checkpoint show myproject 20251210-143052 --json
```

**Displayed Information:**
- Creation timestamp
- Pane count and agent types
- Git branch and commit
- Dirty status (staged/unstaged/untracked counts)
- Description (if provided)

### Deleting Checkpoints

```bash
# Interactive deletion
ntm checkpoint delete myproject 20251210-143052

# Force delete without confirmation
ntm checkpoint delete myproject 20251210-143052 --force
```

### Auto-Checkpoints

NTM automatically creates checkpoints before risky operations:
- Broadcasting prompts to multiple agents
- Adding or removing agents from a session
- Spawning new sessions with agent configurations
- Operations flagged as potentially destructive

This provides automatic rollback points without manual intervention.

### Storage Location

Checkpoints are stored in `~/.local/share/ntm/checkpoints/` organized by session name. Each checkpoint includes:
- `checkpoint.json` - Metadata and session configuration
- `panes/*.txt` - Scrollback content for each pane
- `git.patch` - Uncommitted changes (if any)

---

## Session Persistence

Save and restore complete session state, including agent configurations, prompts, and context.

### Saving Sessions

```bash
ntm sessions save myproject                # Save current state
ntm sessions save myproject --name="pre-refactor"  # Named snapshot
ntm sessions save myproject --include-history      # Include prompt history
```

Saved state includes:
- Agent pane configuration (types, counts)
- Current working directories
- Recent prompts (optional)
- Checkpoint references
- Custom metadata

### Listing Saved Sessions

```bash
ntm sessions list                          # List all saved sessions
ntm sessions list --json                   # Machine-readable
```

### Viewing Session Details

```bash
ntm sessions show myproject                # Show latest save
ntm sessions show myproject --name="pre-refactor"  # Specific snapshot
```

### Restoring Sessions

```bash
ntm sessions restore myproject             # Restore latest
ntm sessions restore myproject --name="pre-refactor"  # Specific snapshot
ntm sessions restore myproject --dry-run   # Preview what would happen
```

### Deleting Saved Sessions

```bash
ntm sessions delete myproject --name="old-snapshot"
ntm sessions delete myproject --all --force  # Delete all snapshots
```

---

## Recipes

Recipes are predefined operation sequences for common tasks. They combine multiple NTM commands into a single workflow.

### Available Recipes

```bash
ntm recipes list                           # List all recipes
ntm recipes list --category=setup          # Filter by category
ntm recipes show morning-standup           # Show recipe details
```

### Built-in Recipes

| Recipe | Description |
|--------|-------------|
| `morning-standup` | Start day: sync git, triage work, spawn agents |
| `code-review` | Full review cycle: analyze, review, test |
| `context-recovery` | Recover from compaction: re-read AGENTS.md, get beads context |
| `end-of-day` | Save state, commit changes, push, kill session |
| `fresh-start` | Kill session, clean state, respawn |

### Running Recipes

```bash
ntm recipes run morning-standup myproject
ntm recipes run code-review myproject --dry-run
```

### Custom Recipes

Define custom recipes in `~/.config/ntm/recipes.yaml`:

```yaml
recipes:
  - name: my-deploy
    description: Deploy to staging
    category: deployment
    steps:
      - command: ntm send ${session} --all "Stop current work, we're deploying"
      - command: git push origin staging
      - command: ntm send ${session} --cc "Monitor deployment logs"
```

---

## Prompt Templates

NTM includes a built-in template system for common prompting patterns, reducing repetitive typing and ensuring consistent agent interactions.

### Built-in Templates

| Template | Description |
|----------|-------------|
| `code_review` | Review code for quality, bugs, performance, and security issues |
| `explain` | Walk through code with control flow analysis |
| `refactor` | Improve structure, naming conventions, and simplification |
| `test` | Generate comprehensive test coverage |
| `document` | Add documentation (JSDoc, GoDoc, docstring styles) |
| `fix` | Fix specific issues with root cause analysis |
| `implement` | Implement features or functions from specifications |
| `optimize` | Optimize for time complexity, memory, or both |

### Using Templates

```bash
# Use a template with ntm send
ntm send myproject --cc --template code_review

# With custom variables
ntm send myproject --cc --template fix --var issue="null pointer exception"

# List available templates
ntm templates list

# Show template content
ntm templates show code_review
```

### Variable Substitution

Templates support variable substitution with defaults:

```
Review {{file|main.go}} for {{focus|all issues}}
```

**Built-in Variables:**
- `{{cwd}}` - Current working directory
- `{{date}}` - Current date (YYYY-MM-DD)
- `{{time}}` - Current time (HH:MM:SS)
- `{{session}}` - Active session name
- `{{clipboard}}` - Clipboard contents (if available)

### Conditional Sections

Templates support Mustache-style conditionals:

```
{{#has_tests}}
Also update the test file at {{test_file}}.
{{/has_tests}}
```

### Template Sources

Templates are loaded from three locations (later sources override earlier):

1. **Built-in**: Compiled into NTM
2. **User**: `~/.config/ntm/templates/`
3. **Project**: `.ntm/templates/`

### Custom Templates

Create custom templates in `~/.config/ntm/templates/my-template.txt`:

```
You are reviewing {{language|Go}} code.

Focus on:
- Error handling
- Edge cases
- Performance implications

{{#context}}
Additional context: {{context}}
{{/context}}
```

---

## Agent Resilience

NTM monitors agent health and can automatically recover from crashes, rate limits, and other failures.

### Auto-Restart

When enabled, NTM automatically restarts crashed agents:

```toml
[resilience]
auto_restart = true            # Enable auto-restart (opt-in)
max_restarts = 3               # Max restarts before giving up
restart_delay_seconds = 30     # Delay between restart attempts
health_check_seconds = 10      # Health check interval
notify_on_crash = true         # Desktop notification on crash
notify_on_max_restarts = true  # Notify when max restarts exceeded
```

### Rate Limit Detection

NTM detects rate limit messages and can trigger account rotation:

```toml
[resilience.rate_limit]
detect = true                  # Enable rate limit detection
notify = true                  # Notify when rate limited
auto_rotate = false            # Trigger account rotation
patterns = [                   # Custom detection patterns
  "rate limit exceeded",
  "too many requests"
]
```

### Health Monitoring

Each agent tracks:
- Restart count since session start
- Current health status (healthy, warning, error)
- Rate limit state
- Last activity timestamp

View health status:

```bash
ntm health myproject           # Check all agent health
ntm health myproject --json    # Programmatic access
```

---

## Notification System

NTM can send notifications for important events through multiple channels.

### Event Types

| Event | Description |
|-------|-------------|
| `agent.error` | Agent entered error state |
| `agent.crashed` | Agent process exited unexpectedly |
| `agent.restarted` | Agent was automatically restarted |
| `agent.idle` | Agent waiting for input |
| `agent.rate_limit` | Rate limit detected |
| `rotation.needed` | Account rotation recommended |
| `session.created` | New session spawned |
| `session.killed` | Session terminated |
| `health.degraded` | Overall health dropped |

### Notification Channels

**Desktop Notifications:**

```toml
[notifications]
enabled = true
channels = ["desktop"]

[notifications.desktop]
enabled = true
# Uses osascript on macOS, notify-send on Linux
```

**Webhook:**

```toml
[notifications.webhook]
enabled = true
url = "https://hooks.slack.com/services/..."
method = "POST"
template = '''
{
  "text": "NTM: {{.Type}} - {{.Message}}"
}
'''
```

**Shell Command:**

```toml
[notifications.shell]
enabled = true
command = "my-notifier"
# Receives JSON on stdin with event details
# Environment: NTM_EVENT_TYPE, NTM_EVENT_MESSAGE, NTM_SESSION
```

**Log File:**

```toml
[notifications.log]
enabled = true
path = "~/.config/ntm/notifications.log"
# Append-only logging of all events
```

### Event Filtering

```toml
[notifications]
events = ["agent.crashed", "agent.rate_limit", "health.degraded"]
# Only receive notifications for these events
```

---

## Token Estimation

NTM estimates token usage to help manage context windows and prevent exhaustion.

### Content-Aware Estimation

Token estimation varies by content type:

| Content Type | Chars/Token | Rationale |
|--------------|-------------|-----------|
| Code | 2.8 | More punctuation, operators |
| JSON | 3.0 | Structured, repetitive |
| Markdown | 3.5 | Mixed content |
| Prose | 4.0 | Natural language |

### Context Limits

Built-in limits for popular models:

| Model | Context Limit |
|-------|---------------|
| Claude (Opus/Sonnet/Haiku) | 200,000 tokens |
| GPT-4 / GPT-4o | 128,000 tokens |
| Gemini (Pro/Flash/Ultra) | 1,000,000 tokens |

### Usage Monitoring

```bash
# View context usage for all agents
ntm --robot-context=myproject

# JSON output for automation
ntm --robot-context=myproject --json
```

### Overhead Estimation

System prompts and conversation history add overhead:

| Factor | Multiplier |
|--------|------------|
| System prompt | 1.2x |
| Conversation history | 1.5x |
| Tool usage | 2.0x |

---

## Account Rotation

For high-volume usage, NTM supports rotating between multiple accounts to avoid rate limits.

### Configuration

```toml
[rotation]
enabled = true
prefer_restart = true          # Restart agent vs in-pane reauth
auto_open_browser = false      # Auto-open auth URLs
auto_trigger = false           # Automatic rotation on rate limit
continuation_prompt = "Continue where you left off. Previous context..."

[[rotation.accounts]]
provider = "claude"
email = "primary@example.com"
alias = "main"
priority = 1                   # Lower = higher priority

[[rotation.accounts]]
provider = "claude"
email = "backup@example.com"
alias = "backup"
priority = 2
```

### Provider-Specific Authentication

| Provider | Method | Notes |
|----------|--------|-------|
| Claude | In-pane `/login` | Browser-based OAuth |
| Codex | Restart-based | Requires process restart |
| Gemini | In-pane `/auth` | Google OAuth |

### Manual Rotation

```bash
# Trigger rotation for a session
ntm rotate myproject

# Rotate specific agent type
ntm rotate myproject --cc

# Check rotation status
ntm --robot-quota=myproject
```

### Thresholds

```toml
[rotation.thresholds]
warning_percent = 80           # Warn at 80% quota usage
critical_percent = 95          # Force rotate at 95%
restart_if_tokens_above = 100000
restart_if_session_hours = 8   # Rotate after 8 hours
```

---

## Prompt Context Injection

Inject file contents directly into prompts for quick context sharing.

### Basic Usage

```bash
# Inject a file into the prompt
ntm send myproject --cc --files "main.go" "Review this code"

# Multiple files
ntm send myproject --cc --files "main.go,handler.go" "Review these files"
```

### Line Range Selection

Select specific lines from files:

```bash
# Lines 10-50
ntm send myproject --cc --files "main.go:10-50" "Focus on this function"

# From line 100 to end
ntm send myproject --cc --files "main.go:100-" "Check the rest of the file"

# First 50 lines only
ntm send myproject --cc --files "main.go:-50" "Review the header"
```

### Size Limits

- **Per-file limit**: 1MB (prevents accidental large file injection)
- **Total injection limit**: 10MB
- **Binary detection**: Automatically skips binary files

### Format

Injected files appear with code fence headers:

```
=== File: main.go (lines 10-50) ===
```go
func main() {
    // ...
}
```

Your prompt text here...
```

---

## Design Principles

NTM is built around six core invariants that guide all design decisions.

### 1. No Silent Data Loss

Destructive commands require explicit approval:
- `git reset --hard`, `rm -rf`, `git push --force` are blocked by default
- Force-release of file reservations requires SLB (two-person) approval
- All destructive actions are logged with audit trails

### 2. Graceful Degradation

Missing dependencies don't crash NTM:
- Optional tools (bv, cass, cm) fallback with warnings
- Features degrade gracefully when unavailable
- Clear error messages explain what's missing

### 3. Idempotent Orchestration

Retry-safe operations:
- Spawning an existing session is safe (attaches instead)
- Reserving already-held files is safe
- Assigning work already assigned is safe
- Sending duplicate messages is safe

### 4. Recoverable State

Session state survives crashes:
- Tmux sessions persist independently of NTM
- Checkpoints capture full state for recovery
- Git state tracking enables rollback

### 5. Auditable Actions

All critical operations are logged:
- Event log with timestamps and correlation IDs
- Git-committed `.beads/` state
- Notification history

### 6. Safe-by-Default

Dangerous features are opt-in:
- Auto-restart disabled by default
- Account rotation disabled by default
- Force operations require explicit flags
- Policy engine gates destructive commands

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

### Updating NTM

Use the built-in upgrade command:

```bash
ntm upgrade
```

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

### Getting Started

**Q: What's the fastest way to learn NTM?**

A: Run the interactive tutorial:

```bash
ntm tutorial
```

It walks you through all the core concepts with animated examples.

**Q: How do I keep NTM updated?**

A: Use the built-in upgrade command:

```bash
ntm upgrade           # Check for updates and install
ntm upgrade --check   # Just check, don't install
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

### Building with Docker

```bash
# Build the container image
docker build -t ntm:local .

# Build with version info
docker build \
  --build-arg VERSION=1.0.0 \
  --build-arg COMMIT=$(git rev-parse HEAD) \
  --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  -t ntm:local .
```

### CI/CD

NTM uses GitHub Actions for continuous integration:

- **Lint**: golangci-lint with 40+ linters
- **Test**: Unit tests with coverage on Linux and macOS
- **Build**: Cross-platform builds (Linux, macOS, Windows, FreeBSD)
- **Security**: Vulnerability scanning with govulncheck and gosec
- **Release**: Automated releases via GoReleaser with multi-arch Docker images

### Project Structure

```
ntm/
├── cmd/ntm/              # Main entry point
├── internal/
│   ├── agentmail/        # Agent Mail client for multi-agent coordination
│   ├── auth/             # Authentication and account rotation
│   ├── bv/               # Beads/bv integration for issue tracking
│   ├── cass/             # CASS (Cross-Agent Search System) client
│   ├── checkpoint/       # Session checkpoint types
│   ├── cli/              # Cobra commands and help rendering
│   ├── config/           # TOML configuration and palette loading
│   ├── context/          # Context window monitoring and estimation
│   ├── events/           # Event logging framework (JSONL)
│   ├── history/          # Prompt history tracking
│   ├── hooks/            # Pre/post command hooks
│   ├── notify/           # Multi-channel notifications (desktop, webhook, shell, log)
│   ├── output/           # Formatting utilities (diff, JSON, progress)
│   ├── palette/          # Command palette TUI with animations
│   ├── profiler/         # Performance profiling with recommendations
│   ├── quota/            # Rate limit and quota tracking
│   ├── robot/            # Machine-readable JSON output for AI agents
│   ├── rotation/         # Account rotation providers
│   ├── scanner/          # UBS auto-scanner integration
│   ├── startup/          # Lazy initialization framework
│   ├── status/           # Agent status detection, compaction recovery
│   ├── templates/        # Built-in prompt templates
│   ├── tmux/             # Tmux session/pane/window operations
│   ├── tracker/          # File conflict tracking across agents
│   ├── tutorial/         # Interactive tutorial with animated slides
│   ├── updater/          # Self-update from GitHub releases
│   ├── watcher/          # File watching with debouncing
│   └── tui/
│       ├── components/   # Reusable components (spinners, progress, banner)
│       ├── dashboard/    # Interactive session dashboard
│       ├── icons/        # Nerd Font / Unicode / ASCII icon sets
│       ├── styles/       # Gradient text, shimmer, glow effects
│       └── theme/        # Catppuccin themes (Mocha, Macchiato, Nord)
├── .github/workflows/    # CI/CD pipelines
├── .goreleaser.yaml      # Release configuration
└── Dockerfile            # Container image definition
```

---

## License

MIT License. See [LICENSE](LICENSE) for details.

---

> *About Contributions:* Please don't take this the wrong way, but I do not accept outside contributions for any of my projects. I simply don't have the mental bandwidth to review anything, and it's my name on the thing, so I'm responsible for any problems it causes; thus, the risk-reward is highly asymmetric from my perspective. I'd also have to worry about other "stakeholders," which seems unwise for tools I mostly make for myself for free. Feel free to submit issues, and even PRs if you want to illustrate a proposed fix, but know I won't merge them directly. Instead, I'll have Claude or Codex review submissions via `gh` and independently decide whether and how to address them. Bug reports in particular are welcome. Sorry if this offends, but I want to avoid wasted time and hurt feelings. I understand this isn't in sync with the prevailing open-source ethos that seeks community contributions, but it's the only way I can move at this velocity and keep my sanity.

---

## Acknowledgments

- [tmux](https://github.com/tmux/tmux) - The terminal multiplexer that makes this possible
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - The TUI framework
- [Catppuccin](https://github.com/catppuccin/catppuccin) - The beautiful color palette
- [Nerd Fonts](https://www.nerdfonts.com/) - The icon fonts
- [Cobra](https://github.com/spf13/cobra) - The CLI framework
- [Claude Code](https://claude.ai/code), [Codex](https://openai.com/codex), [Gemini CLI](https://ai.google.dev/) - The AI agents this tool orchestrates
