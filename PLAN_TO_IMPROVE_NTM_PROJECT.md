# NTM Improvement Plan

This document outlines strategic improvements to elevate NTM from a capable power-user tool to the definitive command center for AI-assisted development. NTM is the **cockpit** of the Agentic Coding Flywheel—the orchestration layer that coordinates all other tools in the Dicklesworthstone Stack.

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [The Flywheel Vision](#the-flywheel-vision)
3. [Integration: CAAM (Coding Agent Account Manager)](#integration-caam-coding-agent-account-manager)
4. [Integration: CASS Memory System](#integration-cass-memory-system)
5. [Integration: SLB (Safety Guardrails)](#integration-slb-safety-guardrails)
6. [Integration: MCP Agent Mail](#integration-mcp-agent-mail)
7. [Unified Architecture](#unified-architecture)
8. [Web Dashboard](#web-dashboard)
9. [Zero-Config Quick Start](#zero-config-quick-start)
10. [Notifications System](#notifications-system)
11. [Session Templates](#session-templates)
12. [Intelligent Error Recovery](#intelligent-error-recovery)
13. [IDE Integration](#ide-integration)
14. [Agent Orchestration Patterns](#agent-orchestration-patterns)
15. [Interactive Tutorial & Onboarding](#interactive-tutorial--onboarding)
16. [Shareable Sessions](#shareable-sessions)
17. [UX Polish](#ux-polish)
18. [Implementation Roadmap](#implementation-roadmap)

---

## Executive Summary

NTM is positioned as the **central orchestration layer** in a complete AI development ecosystem. The Agentic Coding Flywheel Setup (ACFS) demonstrates this vision: NTM is one of eight coordinated tools that together transform raw compute into a productive multi-agent development environment.

### The Dicklesworthstone Stack

| Tool | Command | Purpose | NTM Role |
|------|---------|---------|----------|
| **NTM** | `ntm` | Agent cockpit - spawn, orchestrate, monitor | **Central hub** |
| **MCP Agent Mail** | `am` | Inter-agent messaging & file reservations | Message routing |
| **UBS** | `ubs` | Bug scanning with quality guardrails | Pre-commit checks |
| **Beads Viewer** | `bv` | Task management with graph analysis | Work assignment |
| **CASS** | `cass` | Session search across all agents | Historical context |
| **CASS Memory** | `cm` | Procedural memory for agents | Rule injection |
| **CAAM** | `caam` | Account switching & rate limit failover | Auth orchestration |
| **SLB** | `slb` | Two-person rule for dangerous commands | Safety layer |

### Current Gaps & Improvements

| Gap | Impact | Solution |
|-----|--------|----------|
| No account orchestration | Agents hit rate limits, manual switching | CAAM integration |
| No persistent memory | Agents re-learn same lessons | CM integration |
| No safety gates | Dangerous commands execute unchecked | SLB integration |
| No web interface | Terminal-only monitoring | Web dashboard |
| Complex setup | High barrier to entry | Zero-config mode |
| Silent operation | Users miss important events | Notification system |
| No templates | Repetitive configuration | Template system |

---

## The Flywheel Vision

The Agentic Coding Flywheel is a closed-loop learning system where each cycle compounds:

```
                    ┌────────────────────────────────────────┐
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │        PLAN (Beads/bv)        │                        │
    │   - Ready work queue          │                        │
    │   - Dependency graph          │                        │
    │   - Priority scoring          │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │    COORDINATE (Agent Mail)    │                        │
    │   - File reservations         │                        │
    │   - Message routing           │                        │
    │   - Thread tracking           │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │      EXECUTE (NTM + Agents)   │ ◄── SAFETY (SLB)       │
    │   - Multi-agent sessions      │     Two-person rule    │
    │   - Account rotation (CAAM)   │     for dangerous ops  │
    │   - Parallel task dispatch    │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │         SCAN (UBS)            │                        │
    │   - Static analysis           │                        │
    │   - Bug detection             │                        │
    │   - Pre-commit checks         │                        │
    └───────────────┬───────────────┘                        │
                    │                                        │
    ┌───────────────▼───────────────┐                        │
    │    REMEMBER (CASS + CM)       │                        │
    │   - Session indexing          │                        │
    │   - Rule extraction           │                        │
    │   - Confidence scoring        │                        │
    └───────────────┴────────────────────────────────────────┘
```

NTM sits at the center of EXECUTE—the active work phase where agents create value. But NTM should also orchestrate the entire loop, pulling context from PLAN, coordinating via Agent Mail, gating through SLB, triggering SCAN, and feeding REMEMBER.

---

## Integration: CAAM (Coding Agent Account Manager)

### What CAAM Does

CAAM enables **sub-100ms account switching** for AI coding CLIs (Claude, Codex, Gemini) by managing OAuth token files directly. Key capabilities:

- **Vault-based profiles**: Backup/restore auth files without browser OAuth
- **Smart rotation**: Multi-factor scoring (health, recency, cooldown) for profile selection
- **Rate limit failover**: `caam run` automatically switches accounts on rate limit
- **Health scoring**: Token validity tracking with expiry warnings
- **Project associations**: Directory-specific account bindings

### Integration Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                           NTM Spawn                                  │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ntm spawn myproject --cc=3 --cod=2                                 │
│       │                                                             │
│       ▼                                                             │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐              │
│  │ Agent 1     │    │ Agent 2     │    │ Agent 3     │              │
│  │ (claude)    │    │ (claude)    │    │ (claude)    │              │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘              │
│         │                  │                  │                     │
│         ▼                  ▼                  ▼                     │
│  ┌──────────────────────────────────────────────────┐               │
│  │               CAAM Account Layer                  │               │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐          │               │
│  │  │ Profile A│ │ Profile B│ │ Profile C│          │               │
│  │  │ alice@   │ │ bob@     │ │ work@    │          │               │
│  │  │ Health:🟢│ │ Health:🟢│ │ Cooldown │          │               │
│  │  └──────────┘ └──────────┘ └──────────┘          │               │
│  └──────────────────────────────────────────────────┘               │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### Concrete Integration Points

#### 1. Spawn with Account Assignment

```bash
# New flag: --account-strategy
ntm spawn myproject --cc=3 --account-strategy=round-robin

# Explicit account assignment
ntm spawn myproject --cc=alice@gmail.com,bob@gmail.com,work@company.com

# Project-based (uses caam project associations)
ntm spawn myproject --cc=3 --accounts=project
```

**Implementation** (`internal/cli/spawn.go`):
```go
func assignAccounts(agentType string, count int, strategy string) ([]string, error) {
    switch strategy {
    case "round-robin":
        // Query caam for available profiles
        profiles, err := caamListProfiles(agentType)
        if err != nil {
            return nil, err
        }
        // Distribute agents across profiles
        assignments := make([]string, count)
        for i := 0; i < count; i++ {
            assignments[i] = profiles[i%len(profiles)].AccountLabel
        }
        return assignments, nil

    case "smart":
        // Use caam's smart selection for each agent
        assignments := make([]string, count)
        for i := 0; i < count; i++ {
            // Each call rotates to next-best profile
            profile, err := caamNextProfile(agentType)
            if err != nil {
                return nil, err
            }
            assignments[i] = profile.AccountLabel
        }
        return assignments, nil

    case "project":
        // Use caam project get for current directory
        profile, err := caamProjectGet(agentType)
        if err != nil {
            return nil, err
        }
        // All agents use same project-associated account
        assignments := make([]string, count)
        for i := 0; i < count; i++ {
            assignments[i] = profile.AccountLabel
        }
        return assignments, nil
    }
    return nil, fmt.Errorf("unknown strategy: %s", strategy)
}
```

#### 2. Automatic Rate Limit Failover

When an agent hits a rate limit, NTM should automatically:
1. Detect the rate limit (via output parsing or exit code)
2. Mark the current profile in cooldown (`caam cooldown set`)
3. Switch to next available profile (`caam activate --auto`)
4. Retry the operation

```go
// internal/monitor/ratelimit.go
type RateLimitHandler struct {
    session string
    paneID  string
}

func (h *RateLimitHandler) OnRateLimit(agentType, currentProfile string) error {
    // 1. Mark cooldown
    if err := exec.Command("caam", "cooldown", "set",
        fmt.Sprintf("%s/%s", agentType, currentProfile)).Run(); err != nil {
        log.Printf("Failed to set cooldown: %v", err)
    }

    // 2. Get next profile
    out, err := exec.Command("caam", "next", agentType, "--json").Output()
    if err != nil {
        return fmt.Errorf("no available profiles: %w", err)
    }
    var next struct { Profile string `json:"profile"` }
    json.Unmarshal(out, &next)

    // 3. Activate new profile
    if err := exec.Command("caam", "activate", agentType, next.Profile).Run(); err != nil {
        return fmt.Errorf("failed to activate %s: %w", next.Profile, err)
    }

    // 4. Notify via Agent Mail
    sendAgentMail(h.session, "rate-limit-failover", fmt.Sprintf(
        "Agent switched from %s to %s due to rate limit", currentProfile, next.Profile))

    return nil
}
```

#### 3. Health Dashboard Integration

Query CAAM's SQLite database for real-time health metrics:

```go
// internal/dashboard/caam.go
type ProfileHealth struct {
    Provider     string    `json:"provider"`
    AccountLabel string    `json:"account_label"`
    HealthStatus string    `json:"health_status"` // healthy, warning, critical
    TokenExpiry  time.Time `json:"token_expiry"`
    InCooldown   bool      `json:"in_cooldown"`
    CooldownEnds time.Time `json:"cooldown_ends,omitempty"`
}

func GetAccountHealth() ([]ProfileHealth, error) {
    // Query ~/.caam/data/caam.db directly
    db, err := sql.Open("sqlite3", filepath.Join(os.Getenv("HOME"), ".caam/data/caam.db"))
    if err != nil {
        return nil, err
    }
    defer db.Close()

    rows, err := db.Query(`
        SELECT p.provider, p.account_label, p.health_status,
               p.token_expiry, c.ends_at IS NOT NULL as in_cooldown, c.ends_at
        FROM profiles p
        LEFT JOIN cooldowns c ON p.id = c.profile_id AND c.ends_at > datetime('now')
        ORDER BY p.provider, p.account_label
    `)
    // ... parse and return
}
```

#### 4. Robot Mode Output

Add CAAM status to `--robot-status`:

```json
{
  "success": true,
  "sessions": [...],
  "account_health": {
    "claude": [
      {"account": "alice@gmail.com", "status": "healthy", "cooldown": false},
      {"account": "bob@gmail.com", "status": "warning", "cooldown": true, "cooldown_ends": "2025-01-03T16:30:00Z"}
    ],
    "codex": [
      {"account": "work@company.com", "status": "healthy", "cooldown": false}
    ]
  }
}
```

### New NTM Commands

```bash
# Account management
ntm accounts status                    # Show all account health
ntm accounts rotate <session>          # Force rotation for session's agents
ntm accounts cooldown list             # Show active cooldowns
ntm accounts set <pane> <account>      # Assign specific account to pane

# In robot mode
ntm --robot-accounts                   # JSON account health output
```

---

## Integration: CASS Memory System

### What CASS Memory Does

CASS Memory (`cm`) is a **three-layer cognitive memory system** that gives agents persistent learning:

1. **Episodic Memory** (CASS): Raw session logs from all agents
2. **Working Memory** (Diary): Structured session summaries
3. **Procedural Memory** (Playbook): Distilled rules with confidence scoring

Key capabilities:
- **Cross-agent learning**: Rules from Claude benefit Codex and vice versa
- **Confidence decay**: 90-day half-life prevents stale rules
- **Anti-pattern conversion**: Bad rules automatically become warnings
- **Evidence-based validation**: Rules require historical support before acceptance

### The ACE Pipeline

```
GENERATOR (cm context)    →    REFLECTOR (cm reflect)
Get relevant rules             Extract new rules from sessions
before task                    after task completion
       │                              │
       │                              ▼
       │                       VALIDATOR
       │                       Search evidence in CASS
       │                       Accept/reject based on proof
       │                              │
       ▼                              ▼
   AGENT WORK              CURATOR (deterministic)
   Apply rules             Update playbook with new rules
   Record feedback         Handle conflicts, duplicates
```

### Integration Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         NTM Session Lifecycle                        │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  PRE-TASK                                                           │
│  ┌────────────────────────────────────────────────────────────┐     │
│  │ cm context "implement JWT authentication" --json            │     │
│  │                                                             │     │
│  │ Returns:                                                    │     │
│  │ - relevantBullets: rules that apply to this task           │     │
│  │ - antiPatterns: pitfalls to avoid                          │     │
│  │ - historySnippets: past sessions solving similar problems  │     │
│  │ - suggestedCassQueries: deeper searches if needed          │     │
│  └───────────────────────────────┬────────────────────────────┘     │
│                                  │                                  │
│                                  ▼                                  │
│  INJECT INTO AGENT CONTEXT                                          │
│  ┌────────────────────────────────────────────────────────────┐     │
│  │ Agent system prompt includes:                               │     │
│  │                                                             │     │
│  │ ## CASS Memory Context                                      │     │
│  │ The following rules have been learned from past sessions:   │     │
│  │                                                             │     │
│  │ ### Rules (apply these)                                     │     │
│  │ - [b-8f3a2c] Always validate JWT expiry before debugging   │     │
│  │ - [b-2d4e6f] Use httptest for auth endpoint testing        │     │
│  │                                                             │     │
│  │ ### Anti-patterns (avoid these)                             │     │
│  │ - [b-9a1b3c] Don't cache tokens without expiry validation  │     │
│  │                                                             │     │
│  │ When a rule helps, note: [cass: helpful b-xyz]             │     │
│  │ When a rule harms, note: [cass: harmful b-xyz]             │     │
│  └────────────────────────────────────────────────────────────┘     │
│                                                                     │
│  DURING WORK                                                        │
│  Agent references rules, leaves inline feedback                     │
│  // [cass: helpful b-8f3a2c] - caught the token expiry issue        │
│                                                                     │
│  POST-TASK                                                          │
│  ┌────────────────────────────────────────────────────────────┐     │
│  │ cm reflect --days 1 --json                                  │     │
│  │                                                             │     │
│  │ Extracts patterns from new session, proposes rules          │     │
│  │ Validator checks evidence, curator updates playbook         │     │
│  └────────────────────────────────────────────────────────────┘     │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### Concrete Integration Points

#### 1. Pre-Task Context Injection

When spawning agents or sending prompts, inject relevant memory:

```go
// internal/memory/context.go
type MemoryContext struct {
    Rules        []Rule       `json:"relevantBullets"`
    AntiPatterns []Rule       `json:"antiPatterns"`
    History      []Snippet    `json:"historySnippets"`
    Queries      []string     `json:"suggestedCassQueries"`
}

func GetMemoryContext(taskDescription string) (*MemoryContext, error) {
    out, err := exec.Command("cm", "context", taskDescription, "--json").Output()
    if err != nil {
        // Graceful degradation: continue without memory
        log.Printf("cm context failed (continuing without memory): %v", err)
        return &MemoryContext{}, nil
    }

    var ctx MemoryContext
    if err := json.Unmarshal(out, &ctx); err != nil {
        return nil, err
    }
    return &ctx, nil
}

func FormatMemoryPrompt(ctx *MemoryContext) string {
    var sb strings.Builder
    sb.WriteString("## CASS Memory Context\n\n")

    if len(ctx.Rules) > 0 {
        sb.WriteString("### Rules (apply these)\n")
        for _, r := range ctx.Rules {
            sb.WriteString(fmt.Sprintf("- [%s] %s (confidence: %.2f)\n",
                r.ID, r.Content, r.EffectiveScore))
        }
        sb.WriteString("\n")
    }

    if len(ctx.AntiPatterns) > 0 {
        sb.WriteString("### Anti-patterns (avoid these)\n")
        for _, r := range ctx.AntiPatterns {
            sb.WriteString(fmt.Sprintf("- [%s] PITFALL: %s\n", r.ID, r.Content))
        }
        sb.WriteString("\n")
    }

    sb.WriteString("When a rule helps: `// [cass: helpful <id>]`\n")
    sb.WriteString("When a rule harms: `// [cass: harmful <id>]`\n")

    return sb.String()
}
```

#### 2. Automatic Post-Session Reflection

When a session ends or is compacted, trigger reflection:

```go
// internal/session/lifecycle.go
func (m *Manager) OnSessionEnd(session string) {
    // 1. Wait for session logs to be indexed by CASS
    time.Sleep(5 * time.Second)

    // 2. Trigger reflection on recent sessions
    go func() {
        cmd := exec.Command("cm", "reflect", "--days", "1", "--json")
        out, err := cmd.Output()
        if err != nil {
            log.Printf("Reflection failed: %v", err)
            return
        }

        var result struct {
            NewRules []struct {
                Content  string `json:"content"`
                Category string `json:"category"`
            } `json:"proposedRules"`
        }
        json.Unmarshal(out, &result)

        // 3. Notify via Agent Mail if new rules were learned
        if len(result.NewRules) > 0 {
            notifyNewRules(session, result.NewRules)
        }
    }()
}
```

#### 3. Memory-Aware Task Assignment

Use memory to inform agent selection:

```go
// internal/robot/assign.go - enhance existing logic
func generateAssignments(agents []agentInfo, beads []bv.BeadPreview, ...) []AssignRecommend {
    for _, bead := range beads {
        // Get memory context for this task
        memCtx, _ := GetMemoryContext(bead.Title)

        // Check if any rules suggest specific agent types
        for _, rule := range memCtx.Rules {
            if strings.Contains(rule.Content, "claude excels at") {
                // Boost claude agent preference
            }
            if strings.Contains(rule.Content, "codex faster for") {
                // Boost codex agent preference
            }
        }
    }
}
```

#### 4. Robot Mode Output

Add memory status to `--robot-snapshot`:

```json
{
  "success": true,
  "sessions": [...],
  "memory": {
    "playbook_size": 247,
    "proven_rules": 45,
    "established_rules": 89,
    "candidate_rules": 113,
    "anti_patterns": 23,
    "last_reflection": "2025-01-03T14:30:00Z",
    "top_categories": ["debugging", "testing", "git"]
  }
}
```

### New NTM Commands

```bash
# Memory integration
ntm memory status                      # Show playbook health
ntm memory context "task description"  # Get relevant rules
ntm memory reflect                     # Trigger post-session reflection
ntm memory inject <session> <task>     # Inject rules into running session

# In robot mode
ntm --robot-memory                     # JSON memory status
```

---

## Integration: SLB (Safety Guardrails)

### What SLB Does

SLB implements a **two-person rule** for potentially destructive commands. Before dangerous operations execute, a second agent (or human) must explicitly approve. Key capabilities:

- **Risk tier classification**: CRITICAL (2+ approvals), DANGEROUS (1 approval), CAUTION (auto-approve delay), SAFE (immediate)
- **HMAC-signed reviews**: Cryptographic proof of who approved what
- **Client-side execution**: Commands run in caller's environment (inherits credentials)
- **Audit trail**: Complete history in SQLite and Agent Mail

### Risk Tier Examples

| Tier | Pattern Examples | Approvals | Auto-approve |
|------|------------------|-----------|--------------|
| CRITICAL | `rm -rf /`, `DROP DATABASE`, `terraform destroy`, `git push --force main` | 2+ | Never |
| DANGEROUS | `rm -rf ./build`, `git reset --hard`, `kubectl delete deployment` | 1 | Never |
| CAUTION | `rm file.txt`, `npm uninstall`, `git branch -d` | 0 | After 30s |
| SAFE | `rm *.log`, `git stash`, `kubectl delete pod` | 0 | Immediate |

### Integration Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                    NTM + SLB Safety Layer                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  Agent wants to run: rm -rf ./node_modules                          │
│       │                                                             │
│       ▼                                                             │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                   SLB Pattern Engine                         │    │
│  │   Classifies: DANGEROUS (rm -rf with recursive flag)        │    │
│  │   Requires: 1 approval                                       │    │
│  └───────────────────────────┬─────────────────────────────────┘    │
│                              │                                      │
│       ┌──────────────────────┴────────────────────┐                 │
│       │                                           │                 │
│       ▼                                           ▼                 │
│  ┌────────────┐                           ┌────────────────────┐    │
│  │ Request    │   Agent Mail Notification │ Reviewer Agent     │    │
│  │ Created    │ ─────────────────────────▶│ (or Human via TUI) │    │
│  │            │                           │                    │    │
│  │ Blocked... │ ◀─────────────────────────│ APPROVED (signed)  │    │
│  └────────────┘                           └────────────────────┘    │
│       │                                                             │
│       ▼                                                             │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │                   Command Executes                           │    │
│  │   - In agent's shell environment                            │    │
│  │   - Audit logged to .slb/state.db                           │    │
│  │   - Notification sent to Agent Mail                         │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### Concrete Integration Points

#### 1. SLB-Wrapped Command Execution

For agents, wrap dangerous commands through SLB:

```go
// internal/agents/execute.go
type CommandExecutor struct {
    slbEnabled bool
    sessionID  string
    sessionKey string
}

func (e *CommandExecutor) Execute(cmd string, justification string) error {
    // Check if SLB is enabled and command is risky
    if e.slbEnabled && isRisky(cmd) {
        return e.executeWithSLB(cmd, justification)
    }
    return e.executeDirect(cmd)
}

func (e *CommandExecutor) executeWithSLB(cmd, justification string) error {
    // Use slb run for atomic request+wait+execute
    args := []string{
        "run", cmd,
        "--reason", justification,
        "--session-id", e.sessionID,
    }

    slbCmd := exec.Command("slb", args...)
    slbCmd.Stdout = os.Stdout
    slbCmd.Stderr = os.Stderr

    // This blocks until approved, rejected, or timeout
    if err := slbCmd.Run(); err != nil {
        if exitErr, ok := err.(*exec.ExitError); ok {
            if exitErr.ExitCode() == 2 {
                return fmt.Errorf("request rejected by reviewer")
            }
            if exitErr.ExitCode() == 3 {
                return fmt.Errorf("request timed out waiting for approval")
            }
        }
        return err
    }
    return nil
}
```

#### 2. NTM as SLB Reviewer Dispatcher

NTM can route SLB approval requests to appropriate reviewers:

```go
// internal/slb/dispatcher.go
func WatchSLBRequests(session string) {
    // Watch for pending SLB requests
    cmd := exec.Command("slb", "watch", "--json")
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    scanner := bufio.NewScanner(stdout)
    for scanner.Scan() {
        var event struct {
            Type      string `json:"type"`
            RequestID string `json:"request_id"`
            Tier      string `json:"tier"`
            Command   string `json:"command"`
            Requestor string `json:"requestor"`
        }
        json.Unmarshal(scanner.Bytes(), &event)

        if event.Type == "request_pending" {
            // Find appropriate reviewer (different agent than requestor)
            reviewer := findReviewer(session, event.Requestor, event.Tier)

            // Notify via Agent Mail
            sendReviewRequest(reviewer, event)
        }
    }
}

func findReviewer(session, requestor, tier string) string {
    panes, _ := tmux.GetPanes(session)

    // For CRITICAL: prefer different model
    // For DANGEROUS: prefer different agent
    for _, pane := range panes {
        if pane.AgentName != requestor {
            if tier == "critical" && pane.Model != getAgentModel(requestor) {
                return pane.AgentName
            }
            return pane.AgentName
        }
    }

    // Fallback: notify human via desktop notification
    return "HUMAN"
}
```

#### 3. SLB Status in Dashboard

Show pending approvals in NTM dashboard:

```go
// internal/dashboard/slb.go
type SLBStatus struct {
    PendingRequests []struct {
        ID        string    `json:"id"`
        Command   string    `json:"command"`
        Tier      string    `json:"tier"`
        Requestor string    `json:"requestor"`
        CreatedAt time.Time `json:"created_at"`
        ExpiresAt time.Time `json:"expires_at"`
    } `json:"pending"`
    RecentApprovals int `json:"recent_approvals_24h"`
    RecentRejections int `json:"recent_rejections_24h"`
}

func GetSLBStatus() (*SLBStatus, error) {
    out, err := exec.Command("slb", "pending", "--json").Output()
    if err != nil {
        return nil, err
    }
    var status SLBStatus
    json.Unmarshal(out, &status)
    return &status, nil
}
```

#### 4. Emergency Override Integration

For human operators, NTM can facilitate emergency overrides:

```bash
# In NTM TUI: Ctrl+E opens emergency override prompt
# Logs to SLB with audit trail
ntm emergency "rm -rf /stuck/process/files" --reason "Process hung, manual intervention required"
```

### New NTM Commands

```bash
# SLB integration
ntm safety status                      # Show pending requests
ntm safety approve <request-id>        # Approve as current user
ntm safety reject <request-id>         # Reject with reason
ntm safety history                     # Show recent approvals/rejections
ntm safety patterns test "cmd"         # Test what tier a command would be

# In robot mode
ntm --robot-safety                     # JSON safety status
```

---

## Integration: MCP Agent Mail

### Current State

MCP Agent Mail is already integrated with NTM at a basic level. This section describes deeper integration.

### Enhanced Integration Points

#### 1. Automatic Session Threads

When NTM spawns a session, automatically create an Agent Mail thread:

```go
// internal/session/mail.go
func CreateSessionThread(session string, agents []AgentInfo) error {
    // Create thread for session coordination
    threadID := fmt.Sprintf("NTM-%s", session)

    // Register all agents
    for _, agent := range agents {
        _, err := agentmail.RegisterAgent(session, agent.Type, agent.Model, agent.Name)
        if err != nil {
            return err
        }
    }

    // Send initial coordination message
    return agentmail.SendMessage(agentmail.Message{
        ProjectKey: getCurrentProjectPath(),
        SenderName: "NTM-Orchestrator",
        To:         getAgentNames(agents),
        Subject:    fmt.Sprintf("Session %s started", session),
        BodyMD:     formatSessionInfo(session, agents),
        ThreadID:   threadID,
        Importance: "normal",
    })
}
```

#### 2. File Reservation for Parallel Agents

Before agents work on files, reserve them to prevent conflicts:

```go
// internal/agents/files.go
func ReserveFilesForTask(session, agentName string, files []string) error {
    return agentmail.FileReservationPaths(agentmail.Reservation{
        ProjectKey: getCurrentProjectPath(),
        AgentName:  agentName,
        Paths:      files,
        TTLSeconds: 3600,
        Exclusive:  true,
        Reason:     fmt.Sprintf("Working on task in session %s", session),
    })
}

func ReleaseFilesAfterTask(session, agentName string) error {
    return agentmail.ReleaseFileReservations(getCurrentProjectPath(), agentName)
}
```

#### 3. Cross-Agent Communication via NTM

```bash
# Send message to specific agent in session
ntm mail send myproject/agent-1 "Please review the auth module changes"

# Broadcast to all agents
ntm mail broadcast myproject "Switching to phase 2"

# Check inbox for agent
ntm mail inbox myproject/agent-1
```

---

## Unified Architecture

### The NTM Daemon

To coordinate all integrations, NTM should run a lightweight daemon that:

1. **Monitors account health** (CAAM polling)
2. **Watches for SLB requests** (approval dispatch)
3. **Triggers memory reflection** (post-session)
4. **Routes Agent Mail** (session threads)
5. **Serves web dashboard** (HTTP/WebSocket)

```go
// internal/daemon/daemon.go
type Daemon struct {
    accountWatcher  *caam.HealthWatcher
    slbDispatcher   *slb.RequestDispatcher
    memoryReflector *cm.Reflector
    mailRouter      *agentmail.Router
    webServer       *dashboard.Server
}

func (d *Daemon) Start() error {
    // Start all subsystems
    go d.accountWatcher.Watch()
    go d.slbDispatcher.Watch()
    go d.memoryReflector.Watch()
    go d.mailRouter.Watch()
    return d.webServer.ListenAndServe()
}
```

### Configuration

Unified configuration for all integrations:

```toml
# ~/.config/ntm/config.toml

[integrations]
# Account management
caam_enabled = true
caam_auto_failover = true
caam_health_poll_interval = "30s"

# Safety layer
slb_enabled = true
slb_auto_dispatch_reviews = true
slb_default_timeout = "30m"

# Memory system
cm_enabled = true
cm_auto_inject = true
cm_reflect_on_session_end = true

# Agent mail
agent_mail_enabled = true
agent_mail_auto_threads = true
agent_mail_file_reservations = true

[daemon]
enabled = true
web_port = 8080
```

---

## Web Dashboard

### Enhanced Architecture with Integrations

```
┌─────────────────────────────────────────────────────────────────────┐
│                         NTM Web Dashboard                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    Session Overview                          │   │
│  │  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐            │   │
│  │  │ Agent 1 │ │ Agent 2 │ │ Agent 3 │ │ Agent 4 │            │   │
│  │  │ claude  │ │ claude  │ │ codex   │ │ gemini  │            │   │
│  │  │ 🟢 alice│ │ 🟡 bob  │ │ 🟢 work │ │ 🟢 main │ ← Accounts  │   │
│  │  │ Working │ │ Idle    │ │ Working │ │ Waiting │ ← Status   │   │
│  │  └─────────┘ └─────────┘ └─────────┘ └─────────┘            │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  ┌────────────────────────┐  ┌────────────────────────────────┐    │
│  │   SLB Pending (2)      │  │   Memory Context               │    │
│  │   ┌──────────────────┐ │  │   247 rules in playbook        │    │
│  │   │ DANGEROUS        │ │  │   45 proven, 89 established    │    │
│  │   │ rm -rf ./build   │ │  │                                │    │
│  │   │ [Approve][Reject]│ │  │   Active rules (this session): │    │
│  │   └──────────────────┘ │  │   - JWT validation first       │    │
│  │   ┌──────────────────┐ │  │   - Use httptest for auth      │    │
│  │   │ CRITICAL         │ │  │                                │    │
│  │   │ DROP TABLE users │ │  │   [View Playbook]              │    │
│  │   │ Needs 2 approvals│ │  │                                │    │
│  │   └──────────────────┘ │  └────────────────────────────────┘    │
│  └────────────────────────┘                                        │
│                                                                     │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    Agent Output Stream                        │   │
│  │  [Agent 1] Analyzing auth module structure...                │   │
│  │  [Agent 1] Found 3 files to modify                           │   │
│  │  [Agent 1] // [cass: helpful b-8f3a2c] - JWT check helped    │   │
│  │  [Agent 3] Tests passing: 47/50                              │   │
│  │  [SLB] Approval request created for: rm -rf ./old_tests      │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### WebSocket Events

```typescript
// Dashboard receives these events
interface NTMEvent {
  type: 'agent_output' | 'slb_request' | 'memory_rule' | 'account_health' | 'agent_status';
  timestamp: string;
  payload: any;
}

// Example events
{ type: 'slb_request', payload: { id: 'abc', tier: 'dangerous', command: 'rm -rf ./build' }}
{ type: 'account_health', payload: { provider: 'claude', account: 'bob@', status: 'cooldown' }}
{ type: 'memory_rule', payload: { id: 'b-xyz', applied: true, helpful: true }}
```

---

## Zero-Config Quick Start

### One-Liner with Auto-Detection

```bash
# Current (complex)
ntm spawn --session=myproject --agents=claude:2,codex:1 \
    --workdir=/path/to/project --config=~/.config/ntm/config.toml

# New (simple)
ntm go "refactor the auth module to use JWT"

# What happens:
# 1. Detect project type (Go, Node, Python, etc.)
# 2. Detect available agents (claude, codex, gemini)
# 3. Query CAAM for available accounts
# 4. Get memory context from cm
# 5. Spawn appropriate agents
# 6. Inject memory rules
# 7. Start web dashboard
# 8. Begin work
```

### Implementation

```go
// internal/cli/go.go
func runGoCommand(task string) error {
    // 1. Detect environment
    env := detectEnvironment()

    // 2. Get accounts
    accounts, err := getAvailableAccounts()
    if err != nil {
        log.Printf("CAAM not available, using defaults")
    }

    // 3. Get memory context
    memCtx, err := cm.GetContext(task)
    if err != nil {
        log.Printf("Memory not available, continuing without")
    }

    // 4. Configure agents
    agentCount := min(len(accounts.Claude), 2)
    config := SessionConfig{
        Name:     generateSessionName(task),
        Agents:   generateAgentConfig(env, accounts, agentCount),
        Memory:   memCtx,
        Task:     task,
    }

    // 5. Spawn session
    session, err := spawnSession(config)
    if err != nil {
        return err
    }

    // 6. Start dashboard
    fmt.Printf("Dashboard: http://localhost:8080\n")
    fmt.Printf("Session: %s\n", session.Name)

    return nil
}
```

---

## Notifications System

### Multi-Channel Notifications

```toml
[notifications]
# Desktop notifications (native OS)
desktop_enabled = true
desktop_events = ["slb_approval_needed", "agent_error", "task_complete"]

# Slack webhook
slack_enabled = true
slack_webhook = "https://hooks.slack.com/..."
slack_events = ["slb_approval_needed", "session_complete"]

# Discord webhook
discord_enabled = true
discord_webhook = "https://discord.com/api/webhooks/..."
discord_events = ["agent_error"]

# Sound cues
sound_enabled = true
sound_slb_approval = "alert"
sound_task_complete = "chime"
```

### Event Types

| Event | Urgency | Channels |
|-------|---------|----------|
| `slb_approval_needed` | High | Desktop, Slack, Discord |
| `agent_error` | High | Desktop, Discord |
| `rate_limit_hit` | Normal | Desktop |
| `account_switched` | Low | Dashboard only |
| `task_complete` | Normal | Desktop, Slack |
| `memory_rule_learned` | Low | Dashboard only |

---

## Session Templates

### Built-in Templates with Integration Awareness

```yaml
# templates/code-review.yaml
name: code-review
description: "Two reviewers + summarizer with memory context"

settings:
  slb_enabled: true           # Require approval for file deletions
  memory_injection: true      # Inject relevant rules
  account_strategy: smart     # Use CAAM smart selection

agents:
  - name: reviewer-1
    type: claude
    model: opus
    account: auto             # CAAM selects
    system_prompt: |
      You are a senior code reviewer.
      {{ .memory_context }}   # Injected from cm

  - name: reviewer-2
    type: claude
    model: opus
    account: auto
    system_prompt: |
      You are a senior code reviewer focusing on security.
      {{ .memory_context }}

  - name: summarizer
    type: claude
    model: sonnet
    depends_on: [reviewer-1, reviewer-2]
    system_prompt: |
      Summarize findings from reviewers.
```

---

## Implementation Roadmap

### Phase 1: Core Integrations (High Priority)

**CAAM Integration**
- [ ] Account assignment on spawn
- [ ] Rate limit detection and failover
- [ ] Health status in robot mode
- [ ] Account status in dashboard

**CM Integration**
- [ ] Pre-task context injection
- [ ] Post-session reflection trigger
- [ ] Memory status in robot mode
- [ ] Rule tracking in dashboard

**SLB Integration**
- [ ] SLB-wrapped command execution
- [ ] Approval dispatch to reviewers
- [ ] Pending requests in dashboard
- [ ] Emergency override via NTM

### Phase 2: Unified Experience

- [ ] NTM daemon for background coordination
- [ ] Unified configuration system
- [ ] `ntm go` zero-config command
- [ ] Multi-channel notifications

### Phase 3: Web Dashboard

- [ ] Real-time agent output streaming
- [ ] Account health visualization
- [ ] SLB approval UI
- [ ] Memory context display

### Phase 4: IDE Integration

- [ ] VSCode extension with integration awareness
- [ ] Neovim plugin
- [ ] API documentation

### Phase 5: Polish & Advanced Features

- [ ] Session templates with integration settings
- [ ] Interactive tutorial
- [ ] Session sharing
- [ ] Advanced pipeline orchestration

---

## Success Metrics

### Integration Health
- CAAM failover success rate > 95%
- Memory rule hit rate > 60%
- SLB approval latency < 2 minutes (for agents)
- Zero unreviewed CRITICAL commands

### User Experience
- Time to first working session < 3 minutes
- Zero-config success rate > 80%
- Dashboard adoption > 50% of users

### Ecosystem Adoption
- ACFS installation success rate > 90%
- All 8 stack tools working together
- Cross-agent learning measurable (rules from Claude used by Codex)

---

## Conclusion

NTM's position as the **cockpit** of the Agentic Coding Flywheel means it must integrate deeply with all stack tools. This plan provides concrete integration patterns for:

1. **CAAM**: Seamless account management with automatic failover
2. **CM**: Persistent memory that compounds across sessions
3. **SLB**: Safety gates that prevent disasters
4. **Agent Mail**: Coordination backbone for multi-agent workflows

The result is a closed-loop system where:
- Agents learn from every session (CM)
- Rate limits are handled transparently (CAAM)
- Dangerous operations require review (SLB)
- All coordination is auditable (Agent Mail)

NTM becomes not just a session manager, but the **intelligent orchestrator** that makes the entire flywheel spin.
