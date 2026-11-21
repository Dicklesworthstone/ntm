# ADR-0002: tmux PTY Control Plane vs. Structured ACP Agent Transport

- **Status:** Accepted (Disposition Recorded)
- **Date:** 2026-11-20

## Context

Coding agent CLIs vary widely in terminal UI rendering, keystroke handling, and structured protocol support. Discussions around Agent Client Protocol (ACP) / JSON-RPC transports raised the question of whether NTM should replace its `tmux` PTY orchestration layer with an ACP client transport.

## Decision

NTM retains **`tmux` PTY orchestration backed by the declarative 12-harness capability registry (`internal/agent/capabilities.go`)** as its primary control plane. ACP transport adoption is **deferred** until multi-vendor CLI harnesses expose stable, opt-in interactive ACP endpoints that do not sacrifice live human attachment (`ntm attach`, `ntm zoom`, `ntm view`).

## Rationale

1. **Human + Agent Shared Observability:** A core NTM requirement is that human operators can attach directly to any agent pane in `tmux`, inspect live output, intervene interactively, and detach without restarting the agent. Pure headless JSON-RPC sessions break native terminal observability unless paired with a custom terminal emulator.
2. **Broad Heterogeneous Harness Coverage:** NTM supports 12 built-in CLI harnesses (`cc`, `cod`, `gmi`, `agy`, `grok`, `oc`, `omp`, `pi`, `cursor`, `windsurf`, `aider`, `ollama`) plus user-defined plugin manifests (`examples/agents/*.toml`). Most of these tools only ship stable interactive terminal interfaces today.
3. **Hardened Terminal Reliability:** NTM mitigates PTY fragility through composer-gated delivery verification, ANSI/OSC stripping, pane-identity fencing (`%N` physical IDs), transcript-backed context accounting where available (Claude, Codex, Gemini, Pi), and metamorphic/fuzz tests on state parsers.

## Future Re-evaluation Criteria

An optional ACP transport adapter may be reconsidered in a future release only if:
- Multiple first-tier harnesses ship stable interactive ACP servers that coexist with human inspection;
- The adapter routes through NTM's single ORI dispatch, redaction, policy, and capability surfaces rather than creating a parallel orchestration stack.
