# NTM Integration for VS Code

Companion extension for [NTM (Named Tmux Manager)](https://github.com/Dicklesworthstone/ntm). It shells out to the `ntm` binary on your PATH (configurable via the `ntm.binaryPath` setting) and surfaces:

- a **Sessions** tree view in the activity bar (sessions, agents, attach buttons),
- a **status bar item** showing the primary session and agent count,
- an **NTM Dashboard** webview (sessions, Agent Mail state, file reservations),
- **file decorations** marking files locked/watched by agent file reservations,
- commands: spawn agents, spawn and synthesize reasoning ensembles (`--robot-ensemble-spawn`, `--robot-ensemble-synthesize`), triage operator attention feed (`ntm attention`) and durable incidents (`ntm attention incidents`), inspect or create scored session Goal/Now handoffs (`--robot-handoff`), inspect multi-harness swarm & token analytics (`--robot-analytics`), inspect and verify tamper-evident session audit logs (`--robot-audit`), generate redacted diagnostic support bundles (`--robot-support-bundle`), run ecosystem & lock-contention doctor diagnostics (`--robot-doctor`), preflight-lint prompts or editor selections (`--robot-preflight`), plan ready-bead work assignments (`--robot-assign`), scale sessions across all 11 agent harnesses (`--robot-scale`), rebalance session workload & geometry (`--robot-rebalance`), generate or dispatch cross-agent peer review queues (`--robot-review-queue`), summarize session agent activity & file conflicts (`--robot-summary`), adopt external tmux sessions across all 12 pane types (`--robot-adopt`), preview secret/PII redaction without leaking raw tokens (`--robot-redact`), inspect per-agent context window gauges (`--robot-context`), inspect swarm backpressure (`--robot-backpressure`), manage isolated agent worktrees (`ntm worktrees`), launch the NTM Web Cockpit (`ntm web`), send selection/current file to agents, open command palette, attach terminal, show status.

Requires an `ntm` binary (v1.20+) on PATH and VS Code `^1.90.0`.

## Install (from .vsix)

The extension is **not published to the VS Code Marketplace** (deferred decision Q1 on bead `bd-ws5-ship-or-cut-jv0rc.2`; trigger: first user request). Instead, every DSR release attaches a prebuilt `ntm-vscode-<version>.vsix` asset. Install it with:

```sh
code --install-extension ntm-vscode-<version>.vsix
```

or in VS Code: Extensions view → `...` menu → **Install from VSIX...**

## Build from source

```sh
scripts/build_vsix.sh   # from the repo root: npm ci + tsc + vsce package
```

produces `vscode/dist/ntm-vscode-<version>.vsix`. The version in `vscode/package.json` is kept in lockstep with the repo `VERSION` file; the release pre-flight (`scripts/release_preflight.sh`) fails on skew, and the `vscode-extension` CI job compiles the extension on every push.
