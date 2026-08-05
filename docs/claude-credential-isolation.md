# Claude credential isolation (per-pane `CLAUDE_CONFIG_DIR`)

Opt-in shielding for multi-pane Claude swarms on a single subscription.
Addresses [#237](https://github.com/Dicklesworthstone/ntm/issues/237).

## The problem this shields

Anthropic OAuth uses **single-use rotating refresh tokens**, and interactive
Claude Code stores the credential in a shared file (`~/.claude/.credentials.json`)
that it rewrites on every refresh.

When ntm spawns N Claude panes off one subscription, all N instances share that
file. Whichever pane refreshes first invalidates the refresh token every other
pane is holding, and they 401 in cascade — including the operator's own
interactive Claude session on the same machine.

Because it is a race, it presents as intermittent, spawn-count-dependent
"account trouble": panes frozen on an authentication-error frame while
`ntm --robot-health` still reads them as alive.

**This is Claude Code's own behavior, not ntm's.** ntm can only shield it.

## What ntm does when isolation is enabled

For each Claude pane at spawn:

1. Builds a pane-private config dir at
   `<project>/.ntm/claude-homes/<session>/<pane>/`. The path is keyed to the
   pane so `claude --resume` keeps finding the same conversation history across
   restarts.
2. Symlinks **every entry** of `~/.claude` into it **except `.credentials.json`**.
   Settings, MCP servers, and skills all survive; the rotating credential is
   structurally unreachable.
3. Exports `CLAUDE_CONFIG_DIR=<that dir>` and, when a token file is configured,
   `CLAUDE_CODE_OAUTH_TOKEN=<setup token>` on the pane's launch command.

With no credentials file reachable and no refresh cycle to race, the panes
become stateless readers of one static token.

## Setup-token bootstrap (required for credentialed isolation)

Isolation alone gives each pane a config dir with **no credential at all**, so
each pane would prompt for an interactive login. Supply a **non-rotating**
setup token instead:

```bash
# Mint once per account. Store it OUTSIDE any Claude config dir so it can
# never be linked into a pane.
claude setup-token            # prints an sk-ant-oat… token
mkdir -p ~/.ntm/secrets
printf '%s\n' 'sk-ant-oat-…' > ~/.ntm/secrets/claude-setup-token
chmod 600 ~/.ntm/secrets/claude-setup-token
```

Then enable it in your ntm config:

```toml
[agents]
claude_isolate_credentials = true
claude_token_file = "~/.ntm/secrets/claude-setup-token"
```

A configured-but-unreadable or empty token file is a **hard spawn error**, not
a silent downgrade — continuing would leave you believing the panes are
credentialed while they quietly rejoin the shared-credential race.

## Approaches that do NOT work

Both were tested empirically (see #237), so nobody needs to retry them:

- **Injecting `CLAUDE_CODE_OAUTH_TOKEN` alone**, without isolating the config
  dir. With a `.credentials.json` still reachable, interactive Claude Code
  prefers the file credential and keeps racing.
- **`apiKeyHelper`**. It feeds its value as an `x-api-key` header, and an
  `sk-ant-oat…` token is rejected as "Invalid API key" — it is subscription
  OAuth, not an API key.

## Verifying isolation

The property that actually prevents the cascade is "no rotating credential is
reachable from the pane's config dir". After spawning:

```bash
ls -la <project>/.ntm/claude-homes/<session>/<pane>/
# every config entry appears as a symlink; .credentials.json is absent
```

A `.credentials.json` that later appears inside an isolated dir defeats the
mechanism (Claude Code would prefer it over the static token). Re-provisioning
— which happens on every spawn — removes it.

## Scope

This is opt-in and off by default. It changes nothing for single-pane use, for
Claude panes spawned without the flag, or for any other agent type. The Codex
side has an analogous mechanism (per-pane `CODEX_HOME`, [#194]).
