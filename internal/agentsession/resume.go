package agentsession

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/shellword"
)

// antigravityModel is the model the Antigravity CLI (agy) must be pinned to on
// every (re)launch. The agy resume path is invalid without an explicit --model,
// and the migration mandate fixes it to this exact human-readable name.
const antigravityModel = "Gemini 4 Argon (High)"

// ResumeLaunchOptions provides evidence for NTM-generated launch syntax that
// contains a file expansion. The caller must verify the recorded prompt file's
// hash through launch-spec preflight before executing the resulting command.
type ResumeLaunchOptions struct {
	SystemPromptFile string
	// TrustedClaudeSettings must be independently generated from the effective
	// NTM policy, never copied from a saved command or live process arguments.
	TrustedClaudeSettings string
}

// PinPiLaunchSession gives an ordinary managed pi launch an exact, per-pane
// identity before the process starts. Pi closes its transcript between writes,
// so simultaneous panes cannot be distinguished by open files or creation time.
// The caller must record the returned command in launch metadata: --session-id
// opens the same session on restart and creates it only when it is absent.
// Explicit selectors, noninteractive modes and custom shell wrappers retain
// their original meaning and are not rewritten.
func PinPiLaunchSession(command string) string {
	words, err := shellword.Literal(command)
	if err != nil {
		return command
	}
	executable := 0
	for executable < len(words) && resumeEnvironmentAssignment(command[words[executable].Start:words[executable].End]) {
		executable++
	}
	argv := make([]string, 0, len(words)-executable)
	for _, word := range words[executable:] {
		argv = append(argv, word.Value)
	}
	selection, ok := piProcessSelection(argv)
	if !ok || selection.selector != "" || selection.picker {
		return command
	}
	// Reuse the native launch validator to avoid appending after a positional
	// prompt, option terminator or an unrecognized argument boundary.
	if _, err := ResumeLaunchCommand("pi", "ntm-launch", command, ResumeLaunchOptions{}); err != nil {
		return command
	}
	return strings.TrimRight(command, " \t") + " --session-id " + shellQuote("ntm-"+rand.Text())
}

// ResumeLaunchCommand resumes a session using its recorded launch command,
// preserving the executable, literal environment and all non-selector argument
// source text. It never delegates to casr, which cannot carry these settings.
// Only unambiguous interactive invocations are accepted; callers must finish
// this preflight before replacing a live pane or session.
func ResumeLaunchCommand(provider, sessionID, command string, opts ResumeLaunchOptions) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.HasPrefix(sessionID, "-") {
		return "", errors.New("native resume requires a nonempty literal session ID")
	}
	for _, r := range sessionID {
		if r < ' ' || r == 127 {
			return "", errors.New("native resume session ID contains a control character")
		}
	}
	switch provider {
	case "claude", "codex", "gemini", "antigravity", "omp", "pi":
	default:
		return "", fmt.Errorf("provider %q does not support settings-preserving native resume", provider)
	}
	// Codex's generated persona prefix is the only supported expansion. Its
	// exact path is bound to launch metadata and independently hash-verified by
	// replay preflight; never infer a trusted path from arbitrary shell text.
	prefix := ""
	if provider == "codex" && opts.SystemPromptFile != "" {
		if !filepath.IsAbs(opts.SystemPromptFile) || strings.ContainsAny(opts.SystemPromptFile, "\x00\r\n\t") {
			return "", errors.New("recorded system prompt must have an absolute literal path")
		}
		var err error
		command, err = config.PinPersonaPromptReader(command, provider, opts.SystemPromptFile)
		if err != nil {
			return "", err
		}
		expected := "CODEX_SYSTEM_PROMPT=\"$(/bin/cat " + shellQuote(opts.SystemPromptFile) + ")\" "
		if strings.HasPrefix(command, expected) {
			prefix, command = expected, strings.TrimPrefix(command, expected)
		}
	}
	words, err := shellword.Literal(command)
	if err != nil {
		return "", fmt.Errorf("saved launch cannot be safely composed for native resume: %w", err)
	}
	executable := 0
	for executable < len(words) && resumeEnvironmentAssignment(command[words[executable].Start:words[executable].End]) {
		executable++
	}
	// This is the exact memory-limit wrapper emitted by NTM's Claude template.
	// Arbitrary wrappers cannot prove where provider arguments begin.
	if executable < len(words) && filepath.Base(words[executable].Value) == "systemd-run" {
		prefix := []string{"--user", "--scope", "-q", "-p"}
		if executable+6 >= len(words) {
			return "", errors.New("saved launch has an unsupported systemd-run wrapper")
		}
		for i, want := range prefix {
			if words[executable+1+i].Value != want {
				return "", errors.New("saved launch has an unsupported systemd-run wrapper")
			}
		}
		limit := strings.TrimPrefix(words[executable+5].Value, "MemoryMax=")
		digits := strings.TrimSuffix(limit, "M")
		if provider != "claude" || limit == words[executable+5].Value || digits == limit || digits == "" || strings.Trim(digits, "0123456789") != "" {
			return "", errors.New("saved launch has an unsupported memory limit")
		}
		executable += 6
	}
	if executable >= len(words) || !resumeExecutableMatches(provider, filepath.Base(words[executable].Value)) {
		return "", errors.New("native resume requires the recorded direct provider executable")
	}
	selectorStart, selectorEnd := -1, -1
	codexResume, hasModel := false, false
	for i := executable + 1; i < len(words); i++ {
		word := words[i]
		if provider == "codex" && word.Value == "resume" {
			if codexResume || selectorStart >= 0 {
				return "", errors.New("saved launch has multiple resume selectors")
			}
			codexResume = true
			continue
		}
		if !strings.HasPrefix(word.Value, "-") {
			if provider == "codex" && codexResume && selectorStart < 0 {
				selectorStart, selectorEnd = word.Start, word.End
				continue
			}
			return "", errors.New("saved launch has a positional prompt or unsupported subcommand")
		}
		flag, value, inline := strings.Cut(word.Value, "=")
		if flag == "--" {
			return "", errors.New("saved launch ends provider option parsing")
		}
		piSelector := provider == "pi" && (flag == "--session" || flag == "--session-id")
		nativeSelector := provider != "codex" && provider != "pi" && (provider != "antigravity" && (flag == "--resume" || flag == "-r") || flag == "--conversation" && provider == "antigravity")
		if piSelector || nativeSelector {
			if selectorStart >= 0 {
				return "", errors.New("saved launch has multiple resume selectors")
			}
			selectorStart, selectorEnd = word.Start, word.End
			if inline {
				if value == "" || strings.HasPrefix(value, "-") {
					return "", errors.New("saved resume selector has no literal session ID")
				}
			} else {
				if i+1 >= len(words) || words[i+1].Value == "" || strings.HasPrefix(words[i+1].Value, "-") {
					return "", errors.New("saved resume selector has no literal session ID")
				}
				i++
				selectorEnd = words[i].End
			}
			continue
		}
		kind := resumeOption(provider, flag)
		// Codex permits attached short scalar values, e.g. -mMODEL and
		// -cmodel_reasoning_effort=high, in its ordinary and resume modes.
		if provider == "codex" && !strings.HasPrefix(flag, "--") && len(flag) > 2 {
			switch flag[:2] {
			case "-m", "-c", "-p", "-s", "-a", "-C":
				kind, inline, value = resumeValue, true, strings.TrimPrefix(word.Value[2:], "=")
			}
		}
		switch kind {
		case resumeForbidden:
			return "", fmt.Errorf("saved launch option %q conflicts with interactive native resume", flag)
		case resumeBoolean:
			if inline {
				return "", fmt.Errorf("saved boolean option %q has an ambiguous value", flag)
			}
		case resumeValue:
			if !inline {
				if i+1 >= len(words) || strings.HasPrefix(words[i+1].Value, "-") {
					return "", fmt.Errorf("saved launch option %q has no literal value", flag)
				}
				i++
				value = words[i].Value
			}
			if value == "" {
				return "", fmt.Errorf("saved launch option %q has an empty value", flag)
			}
		case resumeUnknown:
			// Unknown options are retained only with an explicit --key=value
			// boundary. Guessing their arity could consume a prompt or selector.
			if !inline || !strings.HasPrefix(flag, "--") || len(flag) <= 2 || value == "" {
				return "", fmt.Errorf("saved launch option %q has unknown argument boundaries; use --option=value", flag)
			}
		}
		hasModel = hasModel || flag == "--model" || flag == "-m"
	}
	if provider == "antigravity" && !hasModel {
		return "", errors.New("saved Antigravity launch has no required model selection")
	}
	selector := "--resume " + shellQuote(sessionID)
	if provider == "antigravity" {
		selector = "--conversation " + shellQuote(sessionID)
	}
	if provider == "pi" {
		// --session-id creates a new session when absent. Native resume
		// must use --session with the validated transcript selection.
		selector = "--session " + shellQuote(sessionID)
	}
	if provider == "codex" {
		if codexResume {
			if selectorStart < 0 {
				return "", errors.New("saved Codex resume command has no explicit session ID")
			}
			selector = shellQuote(sessionID)
		} else {
			end := words[executable].End
			return prefix + command[:end] + " resume " + shellQuote(sessionID) + command[end:], nil
		}
	}
	if selectorStart >= 0 {
		return prefix + command[:selectorStart] + selector + command[selectorEnd:], nil
	}
	return prefix + strings.TrimRight(command, " \t") + " " + selector, nil
}

func resumeExecutableMatches(provider, binary string) bool {
	if provider == "antigravity" {
		return binary == "agy" || binary == "agy-locked"
	}
	return binary == provider
}

func resumeEnvironmentAssignment(raw string) bool {
	name, _, ok := strings.Cut(raw, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

type resumeOptionKind uint8

const (
	resumeUnknown resumeOptionKind = iota
	resumeBoolean
	resumeValue
	resumeForbidden
)

func resumeOption(provider, flag string) resumeOptionKind {
	// These modes select another session, fork it, inject a new initial task,
	// or leave the interactive UI. They must never be silently combined with
	// the saved session ID. Provider-specific short aliases are handled below.
	switch flag {
	case "--continue", "--last", "--all", "--session", "--session-id", "--fork", "--fork-session", "--from-pr", "--teleport", "--resume", "--conversation", "--print", "--prompt", "--prompt-interactive", "--mode", "--output-format", "--input-format", "--list-sessions", "--delete-session", "--no-session", "--help", "--version", "-h", "-v":
		return resumeForbidden
	}
	var booleans, values string
	switch provider {
	case "claude":
		if flag == "-c" || flag == "-p" || flag == "-w" || flag == "--worktree" {
			return resumeForbidden
		}
		booleans = "--dangerously-skip-permissions --allow-dangerously-skip-permissions --verbose --ide --strict-mcp-config --disable-slash-commands --chrome --no-chrome --no-session-persistence --bare --safe-mode"
		values = "--model --effort --system-prompt --system-prompt-file --append-system-prompt --append-system-prompt-file --agent --agents --settings --setting-sources --permission-mode --permission-prompt-tool --allowedTools --allowed-tools --disallowedTools --disallowed-tools --tools --add-dir --mcp-config --plugin-dir --fallback-model --betas --name -n --teammate-mode"
	case "codex":
		if flag == "-r" || flag == "-i" || flag == "--image" {
			return resumeForbidden
		}
		booleans = "--dangerously-bypass-approvals-and-sandbox --full-auto --no-alt-screen --search --oss"
		values = "--model -m --config -c --profile -p --sandbox -s --ask-for-approval -a --cd -C --add-dir --enable --disable --local-provider"
	case "gemini":
		if flag == "-p" || flag == "-i" {
			return resumeForbidden
		}
		booleans = "--yolo -y --sandbox -s --debug -d --screen-reader"
		values = "--model -m --approval-mode --allowed-mcp-server-names --allowed-tools --extensions -e --include-directories --checkpointing"
	case "antigravity":
		if flag == "-p" || flag == "-i" || flag == "-r" {
			return resumeForbidden
		}
		booleans = "--dangerously-skip-permissions --verbose --debug"
		values = "--model"
	case "omp":
		if flag == "-c" || flag == "-p" {
			return resumeForbidden
		}
		booleans = "--auto-approve --no-tools --no-extensions --no-skills --no-prompt-templates --no-themes --offline"
		values = "--model --provider --thinking --system-prompt --append-system-prompt --session-dir --tools --extension -e --skill --prompt-template --theme --smol --slow --plan"
	case "pi":
		if flag == "-c" || flag == "-r" || flag == "-p" || flag == "--export" || flag == "--list-models" {
			return resumeForbidden
		}
		booleans = "--approve -a --no-approve -na --offline --verbose --no-tools -nt --no-builtin-tools -nbt --no-extensions -ne --no-mcp --no-skills -ns --no-prompt-templates -np --no-themes --no-context-files -nc"
		values = "--provider --model --api-key --system-prompt --append-system-prompt --session-dir --name -n --models --thinking --tools -t --exclude-tools -xt --extension -e --skill --prompt-template --theme --use-theme --tui-mode"
	}
	for _, known := range strings.Fields(booleans) {
		if known == flag {
			return resumeBoolean
		}
	}
	for _, known := range strings.Fields(values) {
		if known == flag {
			return resumeValue
		}
	}
	return resumeUnknown
}

// ResumeCommand builds a resume command when the original launch settings are
// unavailable. Callers with a recorded launch must use ResumeLaunchCommand to
// preserve those settings. This legacy path delegates to casr (Cross Agent
// Session Resumer) when available, with the provider's native command as fallback.
//
//	provider   casr/native provider name ("claude", "codex", "gemini",
//	           "antigravity", "omp")
//	sessionID  the captured provider session id
//	preferCASR when true (and casr is on PATH), use casr; otherwise native.
//
// "gemini" (the retired Gemini CLI) and "antigravity" (its successor, agy) are
// distinct providers with distinct resume commands and must not be conflated.
//
// Returns "" if no resume command can be constructed (unknown provider or empty
// id). The returned string is a single command line suitable for sending to a
// tmux pane via SendKeysForAgent.
func ResumeCommand(provider, sessionID string, preferCASR bool) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	sessionID = strings.TrimSpace(sessionID)
	if provider == "" || sessionID == "" {
		return ""
	}

	if preferCASR && casrAvailable() {
		// casr auto-detects the source provider from the id and writes a
		// native session for the target, then prints/launches the resume.
		// The short flag form is the documented ergonomic path.
		switch provider {
		case "claude":
			return "casr -cc " + shellQuote(sessionID)
		case "codex":
			return "casr -cod " + shellQuote(sessionID)
		case "gemini":
			return "casr -gmi " + shellQuote(sessionID)
		}
		// Antigravity has no casr short-flag; fall through to its native
		// resume command below.
	}

	// Native fallback: each agent CLI accepts a resume-by-id flag.
	switch provider {
	case "claude":
		return "claude --resume " + shellQuote(sessionID)
	case "codex":
		return "codex resume " + shellQuote(sessionID)
	case "gemini":
		return "gemini --resume " + shellQuote(sessionID)
	case "antigravity":
		// agy resumes a conversation by id and REQUIRES the model pinned.
		return "agy --conversation " + shellQuote(sessionID) +
			" --model " + shellQuote(antigravityModel)
	case "omp":
		// omp resumes by session id (or id prefix / transcript path); the
		// approval flag matches ntm's fresh omp launch so a resumed pane
		// does not stall on tool approvals. casr has no omp provider.
		return "omp --auto-approve --resume " + shellQuote(sessionID)
	case "pi":
		return "pi --session " + shellQuote(sessionID)
	}
	return ""
}

// casrAvailable reports whether the casr binary is on PATH. Overridable in
// tests via the lookPath indirection.
func casrAvailable() bool {
	_, err := lookPath("casr")
	return err == nil
}

var lookPath = exec.LookPath

// shellQuote single-quotes a value for safe inclusion in a shell command,
// escaping embedded single quotes. Session ids are normally UUID-like, but we
// quote defensively.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ResolvePiResumeSession resolves a saved exact pi identity before any pane is
// replaced. Pi can create a fresh session for a nonexistent --session path, so
// a resume launch must point to an existing transcript with the saved header ID
// and workspace. A captured transcriptPath pins the store used by the original
// process, even when its environment differs from the caller's. If that exact
// file is unavailable or changed, do not substitute another store's session.
// The returned absolute path is process-private launch input; callers should
// continue reporting the original saved session ID.
func ResolvePiResumeSession(ctx context.Context, sessionID, command, workDir, transcriptPath string) (string, error) {
	if ctx == nil {
		return "", errors.New("pi resume requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !piSessionIDPattern.MatchString(sessionID) || !filepath.IsAbs(workDir) {
		return "", errors.New("pi resume requires an exact session ID and absolute working directory")
	}
	workDir = filepath.Clean(workDir)
	if strings.TrimSpace(command) == "" {
		command = "pi"
	}
	if _, err := ResumeLaunchCommand("pi", sessionID, command, ResumeLaunchOptions{}); err != nil {
		return "", err
	}
	if transcriptPath != "" {
		if !filepath.IsAbs(transcriptPath) {
			return "", errors.New("saved pi transcript reference must be an absolute path")
		}
		path := filepath.Clean(transcriptPath)
		header, ok := readPiSessionHeader(path)
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !ok || header.ID != sessionID || header.Cwd != workDir {
			return "", errors.New("saved pi transcript no longer matches its captured identity and workspace")
		}
		return path, nil
	}
	words, err := shellword.Literal(command)
	if err != nil {
		return "", err
	}
	environment := make(map[string]string)
	executable := 0
	for executable < len(words) && resumeEnvironmentAssignment(command[words[executable].Start:words[executable].End]) {
		name, value, _ := strings.Cut(words[executable].Value, "=")
		environment[name] = value
		executable++
	}
	getenv := func(name string) string {
		if value, ok := environment[name]; ok {
			return value
		}
		return os.Getenv(name)
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	argv := make([]string, 0, len(words)-executable)
	for _, word := range words[executable:] {
		argv = append(argv, word.Value)
	}
	selection, ok := piProcessSelection(argv)
	if !ok {
		return "", errors.New("saved pi launch has an ambiguous session selection")
	}
	roots := piSessionRoots(home, workDir, getenv, selection.sessionDir)
	if !selection.exact && selection.selector != "" &&
		(strings.ContainsAny(selection.selector, `/\`) || strings.HasSuffix(selection.selector, ".jsonl")) {
		path := piResolvePath(home, workDir, selection.selector)
		if header, ok := readPiSessionHeader(path); ok && header.ID == sessionID && header.Cwd == workDir {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return path, nil
		}
		// Pi derives the session directory from an explicit file path when
		// there is no override. A later /new session can share that directory.
		if path != "" && selection.sessionDir == "" && getenv("PI_CODING_AGENT_SESSION_DIR") == "" {
			roots = []string{filepath.Dir(path)}
		}
	}
	info := resolvePiSelector(ctx, roots, home, workDir, piSelection{selector: sessionID, exact: true})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if info == nil {
		return "", fmt.Errorf("pi session %q has no unique valid transcript in the saved workspace and store", sessionID)
	}
	return info.SourcePath, nil
}
