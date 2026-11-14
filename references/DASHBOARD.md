# Human Dashboard and TUI Surfaces

These surfaces are for humans, not agents trying to automate NTM.

## Primary Commands

```bash
ntm dashboard myproject
ntm palette myproject
ntm web
ntm bind
ntm view myproject
```

- `dashboard`: live terminal operator overview
- `palette`: fuzzy searchable command launcher
- `web`: embedded browser cockpit (`http://127.0.0.1:7337`) with REST/SSE/WebSocket API
- `bind`: install convenience bindings such as the palette keybinding
- `view`: quick way to tile and attach

## Embedded Web Cockpit

```bash
ntm web
ntm web --port 8080 --project-dir .
```

`ntm web` serves the embedded browser cockpit at `http://127.0.0.1:7337` alongside the REST/SSE/WebSocket API and live OpenAPI 3.1 spec at `http://127.0.0.1:7337/api/v1/openapi.json` (`--auth-mode local|api_key|oidc|mtls`).

## Command Palette

```bash
ntm palette myproject
```

Command list keys (search filter stays focused, so printable characters filter commands):

| Key | Action |
| --- | --- |
| `↑/↓` | Navigate commands (`PgUp/PgDn`, `Ctrl+U/Ctrl+D`, `Home/End` scroll) |
| `Enter` | Select command and open target picker |
| `Ctrl+P` / `Ctrl+F` | Pin / favorite selected command |
| `Ctrl+N` | Compose a custom message |
| `Ctrl+K` | Search `xf` archive |
| `F1` | Toggle help overlay |
| `Esc` / `Ctrl+C` | Quit |

Target selection keys (after choosing a command):

| Key | Action |
| --- | --- |
| `1-5` | Send to All / Claude (`cc`) / Codex (`cod`) / Gemini (`gmi`) / Antigravity (`agy`) |
| `7` | Send to Oh My Pi (`omp`) agents |
| `8` | Send to Grok (`grok`) agents |
| `9` | Send to OpenCode (`oc`) agents |
| `6` | Pick individual agent panes (`Space` toggle, `a` all, `n` none, `Enter` send) |
| `e` | Edit prompt before sending (`Ctrl+S` save) |
| `?` / `F1` | Toggle help overlay |
| `Esc` / `q` | Back to command list / quit |

## Dashboard Notes

The dashboard is useful when:

- you are tending a swarm manually
- you want pane-level visual status
- you need a fast operator overview of sessions, activity, history, and focus

It is the wrong tool when:

- another agent or script needs deterministic state
- you need replayable automation
- you are trying to integrate with external systems

In those cases, use `--robot-*` or `ntm serve`.

## Useful Internal Notes for NTM Contributors

If you are working on NTM itself, these dashboard architecture notes are still useful:

| File | Purpose |
| --- | --- |
| `model.go` | Model structs, types, constants |
| `lifecycle.go` | Init, cleanup, subscriptions |
| `keymap.go` | Keymap and help bindings |
| `messages.go` | Messages and refresh intervals |
| `popup.go` | Popup / overlay rendering |
| `run.go` | Entry points |
| `dashboard.go` | Core update/view path |
| `focus.go` | Focus management |

The TUI stack also leans heavily on Bubble Tea and the Charmbracelet ecosystem.
That is useful context when changing dashboard behavior or trying to trace a rendering issue.
