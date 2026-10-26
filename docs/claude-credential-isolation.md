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
   pane so its credential-isolation home is stable across restarts.
2. Symlinks the operator's `~/.claude` entries into it **except
   `.credentials.json`**. Settings, MCP servers, and skills remain available;
   the rotating credential is structurally unreachable.
3. Exports `CLAUDE_CONFIG_DIR=<that dir>` and, when a token file is configured,
   `CLAUDE_CODE_OAUTH_TOKEN=<setup token>` on the pane's launch command.

With no credentials file reachable and no refresh cycle to race, the panes
become stateless readers of one static token.

### Shared writable state

This is **credential isolation only**, not a private Claude workspace. At
present, entries such as `projects/`, `sessions/`, `todos/`, `history.jsonl`,
and caches are linked to the operator's `~/.claude`, so panes and the
interactive Claude session still share their conversation and working state.
`claude --resume` therefore sees shared history, not pane-private history.

This sharing is deliberate for the current opt-in credential fix. Do not rely
on it to protect concurrent writes to those entries. If a tool atomically
replaces a linked entry, the pane receives a local divergent copy instead of a
live link; ntm preserves that pane-written file and logs a warning on the next
provision rather than discarding data silently.

## Setup-token bootstrap (required for credentialed isolation)

Isolation alone gives each pane a config dir with **no credential at all**, so
each pane would prompt for an interactive login. Logging in inside a pane
works: Claude Code writes that login to the pane's own
`.credentials.json`, which starts its own refresh-token chain shared with no
other pane, and ntm keeps it across restarts (see
[Verifying isolation](#verifying-isolation)). To avoid one login per pane,
supply a **non-rotating** setup token instead:

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
# linked config entries appear as symlinks; .credentials.json is absent,
# or is the pane's own login (see below)
```

Any entry whose name contains `credential` (in any case) counts, not only
`.credentials.json`: backups such as `.credentials.json.bak-<date>` carry the
same refresh token. Re-provisioning, which happens on every spawn and restart,
removes credential **symlinks** from the pane dir, such as the links to
backups that older ntm versions created. Removing a link never touches the
file it points to.

ntm never deletes a credential-named **regular file or directory** there. It
only ever creates links, so such a file came from somewhere else: usually a
login inside the pane, sometimes a copy of your credential.

A login inside the pane is the pane's **own login**: Claude Code writes it to
`CLAUDE_CONFIG_DIR/.credentials.json` with a refresh token that no other pane
or your own session shares, so the pane stays isolated. ntm accepts a
`.credentials.json` in the pane dir as the pane's own login only when all of
these hold:

1. It is a regular file, not a symlink, with exactly one link (no other name
   anywhere, such as another pane dir or a backup, shares it).
2. It is not the same file as any credential entry in `~/.claude`.
3. Its bytes differ from every credential entry in `~/.claude`.

ntm reads the file only to make that comparison and never logs or prints its
contents. A file that passes is kept, and with no `claude_token_file`
configured the pane launches with it, restart after restart.

Everything else is treated as a copy of a credential: a hard link or copy
of `~/.claude/.credentials.json` or one of its backups, or of another pane's
`.credentials.json` (a copy is the same bytes or the same refresh token, so a
reformatted copy counts too), any other
credential-named file or directory, a credential entry in `~/.claude` that is
a symlink to the pane's file, and on Windows, where ntm does not read a file's
link count, any `.credentials.json` at all. Claude Code would prefer
such a file over the setup token, and a copy shares the operator's refresh
token, so the pane does not launch: spawn, add and restart stop with an error
that names each such entry and its kind. Move or delete it yourself, then
retry. Do not copy a credential into a pane dir to save a login. Once either
side refreshes, the copy no longer matches `~/.claude` and ntm cannot tell it
from a login, although it still descends from your refresh-token chain. A copy
of another pane's login stops both panes until one copy is moved, because it
puts them on one refresh-token chain. Log in separately in each pane, or use a
setup token.

With a `claude_token_file` configured, a pane's own login is still isolated,
but Claude Code would use it instead of the setup token, possibly for a
different account, while your configuration says the setup token is in use.
ntm therefore refuses to launch such a pane, as it did before it recognized a
pane's own login, with an error that names the pane, the login file and the
token file. Move the login out of the pane dir to use the setup token, or
unset `claude_token_file` to keep the pane's login.

If a normally linked file appears as a regular file, it has diverged after an
atomic replacement. ntm reports that condition during re-provisioning and
leaves the pane's file intact; compare it with the operator copy before
choosing whether to merge or remove it.

## Scope

This is opt-in and off by default. It changes nothing for single-pane use, for
Claude panes spawned without the flag, or for any other agent type. The Codex
side has an analogous mechanism (per-pane `CODEX_HOME`, [#194]).
