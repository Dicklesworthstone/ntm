# NTM Integration for VS Code

Companion extension for [NTM (Named Tmux Manager)](https://github.com/Dicklesworthstone/ntm). It shells out to the `ntm` binary on your PATH (configurable via the `ntm.binaryPath` setting) and surfaces:

- a **Sessions** tree view in the activity bar (sessions, agents, attach buttons),
- a **status bar item** showing the primary session and agent count,
- an **NTM Dashboard** webview (sessions, Agent Mail state, file reservations),
- **file decorations** marking files locked/watched by agent file reservations,
- commands: spawn agents, send selection/current file to agents, open palette, attach terminal, show status.

Requires an `ntm` binary (v1.20+) on PATH and VS Code `^1.120.0`.

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
