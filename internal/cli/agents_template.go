package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

type projectLanguage struct {
	Name  string
	Rules string
}

type agentsTemplateData struct {
	ProjectName   string
	Language      string
	LanguageRules string
	GeneratedAt   string
}

const agentsTemplate = `# AGENTS.md - NTM Agent Instructions

<INSTRUCTIONS>
## Project
- Name: {{.ProjectName}}
- Language: {{.Language}}
- Generated: {{.GeneratedAt}}

## Workflow
- Read AGENTS.md and README.md before starting work.
- Make focused, logical commits with detailed messages explaining the "why".
- Push after committing so other agents and CI can see your work.
- Use br for issue tracking; do not edit .beads files directly.
- Use bv --robot-triage to pick the next bead.
- Use Agent Mail (MCP) for coordination and file reservations.
- Use cass and cm to reuse prior context when needed.

## Issue Tracking (br)
- br ready --json
- br update <id> --status in_progress --json
- br close <id> --reason "Completed" --json
- br sync --flush-only before git commit

## Triage (bv)
- bv --robot-triage

## Agent Mail (MCP)
- ensure_project then register_agent using the absolute project path
- reserve files before editing (file_reservation_paths)
- fetch_inbox, acknowledge_message, send_message for coordination

## Context Tools
- cass search "query" --robot --limit 5
- cm context "task" --json

## Language-Specific Rules
{{.LanguageRules}}

## Safety

### Destructive Git Operations — BANNED
- NEVER run: git reset --hard, git clean -fd, git push --force, git checkout -- .
- These destroy work from concurrent agents and are unrecoverable.
- If you need to undo changes, use git revert to create a new commit instead.

### Destructive Filesystem Operations
- NEVER run rm -rf on project directories or broad glob patterns.
- Bulk deletes (removing multiple files) require explicit user approval.
- Never delete files or directories without explicit approval.

### Dirty Worktree Discipline
- Never stash or revert other agents' uncommitted work.
- Treat unknown/unexpected changes in the worktree as someone else's work in progress.
- Multiple agents work concurrently — files change constantly during sessions.

### No File Proliferation
- Prefer editing existing files over creating new ones.
- Do not create documentation files (*.md, README) unless explicitly requested.
- Avoid bulk mechanical edits; make small, reviewed changes.

### Verification
- Test after substantive changes using the project's test commands.
- Check git status after committing — more changes may appear from concurrent agents.
- Never claim something is "clean" or "passing" without actually verifying.

### Coordination
- Respect Agent Mail locks when present.
- Check beads/bv for task assignments before starting new work.
- Do not merge PRs — mine them for ideas, implement independently, close with explanation.
</INSTRUCTIONS>
`

func detectProjectLanguage(projectDir string) projectLanguage {
	if fileExists(filepath.Join(projectDir, "go.mod")) {
		return projectLanguage{Name: "Go", Rules: goLanguageRules}
	}
	if fileExists(filepath.Join(projectDir, "pyproject.toml")) || fileExists(filepath.Join(projectDir, "requirements.txt")) {
		return projectLanguage{Name: "Python", Rules: pythonLanguageRules}
	}
	if fileExists(filepath.Join(projectDir, "package.json")) {
		return projectLanguage{Name: "Node", Rules: nodeLanguageRules}
	}
	if fileExists(filepath.Join(projectDir, "Cargo.toml")) {
		return projectLanguage{Name: "Rust", Rules: rustLanguageRules}
	}
	return projectLanguage{Name: "Generic", Rules: genericLanguageRules}
}

const (
	goLanguageRules = `- Build: go build ./...
- Test: go test ./...
- Format: gofmt or goimports
- Use the Go version specified in go.mod`

	pythonLanguageRules = `- Test: python -m pytest or pytest
- Format: black (if configured)
- Lint/type: ruff or mypy if configured`

	nodeLanguageRules = `- Install: npm install (or yarn/pnpm if repo uses it)
- Test: npm test
- Lint/format: npm run lint / npm run format if configured`

	rustLanguageRules = `- Build: cargo build
- Test: cargo test
- Format: cargo fmt
- Lint: cargo clippy`

	genericLanguageRules = `- Use the project's documented build/test/format commands
- Do not introduce new toolchains without approval`
)

func renderAgentsTemplate(projectDir string) (string, error) {
	lang := detectProjectLanguage(projectDir)
	data := agentsTemplateData{
		ProjectName:   filepath.Base(projectDir),
		Language:      lang.Name,
		LanguageRules: lang.Rules,
		GeneratedAt:   time.Now().Format(time.RFC3339),
	}

	tpl, err := template.New("agents").Parse(agentsTemplate)
	if err != nil {
		return "", fmt.Errorf("parse AGENTS template: %w", err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render AGENTS template: %w", err)
	}

	content := strings.TrimSpace(buf.String()) + "\n"
	return content, nil
}

func writeAgentsFile(projectDir string, force bool) (bool, error) {
	path := filepath.Join(projectDir, "AGENTS.md")
	if fileExists(path) && !force {
		return false, nil
	}

	content, err := renderAgentsTemplate(projectDir)
	if err != nil {
		return false, err
	}

	if err := util.AtomicWriteFile(path, []byte(content), 0644); err != nil {
		return false, fmt.Errorf("write AGENTS.md: %w", err)
	}

	return true, nil
}
