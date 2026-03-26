# NTM Configuration and Project Resolution

Use this file when `spawn`, `quick`, templates, or project-local overrides behave
in surprising ways.

## Project Resolution

`ntm spawn <name>` expects NTM to resolve `<name>` to a project directory.

```bash
ntm config get projects_base
ntm quick myproject --template=go
```

If the repo is elsewhere, make it resolvable from `projects_base` or use the repo's
preferred layout.

Labels extend the session name as:

```text
project--frontend
project--backend
```

That means:

```bash
ntm quick myproject --label frontend
ntm spawn myproject --label frontend --cc=2
ntm add myproject --label frontend --cc=1
```

## Useful Config Commands

```bash
ntm config init
ntm config show
ntm config diff
ntm config get projects_base
ntm config edit
ntm config reset
```

## User-Level Assets

Common user-level locations:

- `~/.config/ntm/config.toml`
- `~/.config/ntm/recipes.toml`
- `~/.config/ntm/workflows/`
- `~/.config/ntm/personas.toml`
- `~/.config/ntm/templates/`
- `~/.ntm/policy.yaml`

## Project-Level Assets

Project-local assets usually live under `.ntm/` and override user defaults where appropriate.

Common examples:

- `.ntm/workflows/`
- `.ntm/pipelines/`
- `.ntm/templates/`
- `.ntm/personas.toml`
- `.ntm/recipes.toml`
- `.ntm/checkpoints/`

These matter because session templates, prompt templates, workflows, pipelines, and
persona definitions are often project-specific rather than globally shared.
