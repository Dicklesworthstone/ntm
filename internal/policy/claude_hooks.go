package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/integrations/dcg"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// Claude Code runs a hook only when a settings file lists it under "hooks":
// a script dropped into ~/.claude/hooks/ is never discovered, and there is no
// environment variable that configures hooks. ntm therefore (1) registers the
// installed hook script in ~/.claude/settings.json (`ntm safety install`) and
// (2) passes spawned Claude agents their PreToolUse hooks with --settings.
// PreToolUse hooks run in every permission mode, including
// --dangerously-skip-permissions, and exit status 2 refuses the tool call.

// ClaudeHookFileName is the hook script `ntm safety install` writes.
const ClaudeHookFileName = "ntm-safety.sh"

// ClaudeHookTimeoutSeconds bounds one policy evaluation inside Claude Code.
const ClaudeHookTimeoutSeconds = 10

// ClaudeHookScriptPath is where `ntm safety install` writes the hook script.
func ClaudeHookScriptPath(home string) string {
	return filepath.Join(home, ".claude", "hooks", "PreToolUse", ClaudeHookFileName)
}

// ClaudeUserSettingsPath is Claude Code's user-level settings file.
func ClaudeUserSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// ClaudePolicyHookCommand is the hook command a spawned Claude agent runs to
// check each Bash command against the ntm policy.
func ClaudePolicyHookCommand(ntmBinary string) string {
	if strings.TrimSpace(ntmBinary) == "" {
		ntmBinary = "ntm"
	}
	return shellQuote(ntmBinary) + " safety claude-hook"
}

// ClaudeHookInput is the part of Claude Code's PreToolUse payload ntm reads.
type ClaudeHookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
}

// maxClaudeHookInput caps the payload read from stdin. Claude Code sends a few
// hundred bytes plus the command; anything near this is not a hook payload.
const maxClaudeHookInput = 4 << 20

// ParseClaudeHookInput decodes the PreToolUse payload Claude Code writes to a
// command hook's stdin.
func ParseClaudeHookInput(r io.Reader) (ClaudeHookInput, error) {
	var in ClaudeHookInput
	data, err := io.ReadAll(io.LimitReader(r, maxClaudeHookInput+1))
	if err != nil {
		return in, fmt.Errorf("reading hook payload: %w", err)
	}
	if len(data) > maxClaudeHookInput {
		return in, fmt.Errorf("hook payload exceeds %d bytes", maxClaudeHookInput)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return in, errors.New("empty hook payload")
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Errorf("decoding hook payload: %w", err)
	}
	return in, nil
}

// ClaudePolicyHookEntry is the matcher group that runs command (the ntm policy
// check) on every Bash tool call.
func ClaudePolicyHookEntry(command string) dcg.HookEntry {
	return dcg.HookEntry{
		Matcher: "Bash",
		Hooks:   []dcg.HookHandler{{Type: "command", Command: command, Timeout: ClaudeHookTimeoutSeconds}},
	}
}

// claudeNTMBinary resolves the ntm executable spawned agents' policy hook
// runs. The PATH entry that runs this same binary is preferred: a package
// manager's bin/ symlink survives upgrades, while os.Executable resolves to a
// versioned path (a Homebrew Cellar directory) that an upgrade removes. It is
// a variable so tests can pin it.
var claudeNTMBinary = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "ntm"
	}
	if onPath, err := exec.LookPath("ntm"); err == nil {
		pathInfo, pathErr := os.Stat(onPath)
		exeInfo, exeErr := os.Stat(exe)
		if pathErr == nil && exeErr == nil && os.SameFile(pathInfo, exeInfo) {
			if abs, err := filepath.Abs(onPath); err == nil {
				return abs
			}
		}
	}
	return exe
}

// claudeHookLookPath resolves a hook binary; tests replace it.
var claudeHookLookPath = exec.LookPath

func claudeHookBinaryAvailable(configured, fallback string) bool {
	binary := strings.TrimSpace(configured)
	if binary == "" {
		binary = fallback
	}
	_, err := claudeHookLookPath(binary)
	return err == nil
}

// ClaudeAgentLaunch is what a Claude agent launch needs to carry ntm's hooks.
type ClaudeAgentLaunch struct {
	// Settings is the --settings JSON (empty when there are no hooks).
	Settings string
	// Sources names each hook carried: "ntm-policy", "dcg", "rch".
	Sources []string
	// Warnings explains hooks that were configured but could not be built.
	Warnings []string
}

// ClaudeAgentLaunchSettings builds the PreToolUse hooks every Claude agent ntm
// launches gets through --settings: the ntm policy check ([safety]
// claude_policy_hook, default on; skipped when `ntm safety install` already
// registered the same check in ~/.claude/settings.json, which Claude Code
// would otherwise run twice), dcg ([integrations.dcg] enabled and dcg on
// PATH) and rch ([integrations.rch] enabled with intercept patterns).
func ClaudeAgentLaunchSettings(cfg *config.Config) ClaudeAgentLaunch {
	var launch ClaudeAgentLaunch
	if cfg == nil {
		return launch
	}
	var entries []dcg.HookEntry

	if cfg.Safety.ClaudePolicyHook && !userSettingsRegisterNTMHook() {
		entries = append(entries, ClaudePolicyHookEntry(ClaudePolicyHookCommand(claudeNTMBinary())))
		launch.Sources = append(launch.Sources, "ntm-policy")
	}

	if cfg.Integrations.DCG.Enabled && dcg.ShouldConfigureHooks(cfg.Integrations.DCG.Enabled, cfg.Integrations.DCG.BinaryPath) {
		// Modern dcg runs in Claude PreToolUse hook mode using its own native
		// config/packs/allowlists; passing NTM's send-side audit_log or custom
		// lists into GenerateHookConfig previously errored and silently omitted
		// the Claude PreToolUse dcg hook on every spawn. Always install the base
		// dcg hook when enabled+available and emit advisory warnings for custom
		// list scoping only.
		dcgConfig, err := dcg.GenerateHookConfig(dcg.DCGHookOptions{
			BinaryPath: cfg.Integrations.DCG.BinaryPath,
			Timeout:    5,
		})
		if err != nil {
			launch.Warnings = append(launch.Warnings, fmt.Sprintf("dcg hook not configured: %v", err))
		} else {
			entries = append(entries, dcgConfig.Hooks.PreToolUse...)
			launch.Sources = append(launch.Sources, "dcg")
			if len(cfg.Integrations.DCG.CustomBlocklist) > 0 || len(cfg.Integrations.DCG.CustomWhitelist) > 0 {
				launch.Warnings = append(launch.Warnings, "integrations.dcg custom_blocklist/custom_whitelist apply to NTM send-side checks; configure dcg packs/allowlists directly for Claude PreToolUse hook rules")
			}
		}
	}

	// rch is on by default "when available": a hook naming a missing binary
	// would make Claude Code report a hook error on every Bash call.
	if dcg.ShouldConfigureRCHHooks(cfg.Integrations.RCH.Enabled, cfg.Integrations.RCH.InterceptPatterns) &&
		claudeHookBinaryAvailable(cfg.Integrations.RCH.BinaryPath, "rch") {
		rchHook, err := dcg.GenerateRCHHookEntry(dcg.RCHHookOptions{
			BinaryPath: cfg.Integrations.RCH.BinaryPath,
			Patterns:   cfg.Integrations.RCH.InterceptPatterns,
			Timeout:    5,
		})
		if err != nil {
			launch.Warnings = append(launch.Warnings, fmt.Sprintf("rch hook not configured: %v", err))
		} else {
			entries = append(entries, rchHook)
			launch.Sources = append(launch.Sources, "rch")
		}
	}

	if len(entries) == 0 {
		return launch
	}
	data, err := json.Marshal(dcg.ClaudeHookConfig{Hooks: dcg.HooksSection{PreToolUse: entries}})
	if err != nil {
		launch.Warnings = append(launch.Warnings, fmt.Sprintf("claude hooks not configured: %v", err))
		launch.Sources = nil
		return launch
	}
	launch.Settings = string(data)
	return launch
}

// LogClaudeHooksNotApplied logs, for a rendered Claude launch command that
// could not carry its hooks, that the agent runs unprotected and how to fix
// it. It returns the warning ("" when the hooks were applied or absent) so
// interactive surfaces can also show it.
func LogClaudeHooksNotApplied(where, rendered, settings string) string {
	warning := config.ClaudeHooksNotAppliedWarning(rendered, settings)
	if warning != "" {
		slog.Default().Warn("claude agent launched without ntm hooks", "where", where, "detail", warning)
	}
	return warning
}

// userSettingsRegisterNTMHook reports whether ~/.claude/settings.json already
// runs the installed ntm hook script for Bash.
func userSettingsRegisterNTMHook() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	registered, err := ClaudeHookRegistered(ClaudeUserSettingsPath(home), ClaudeHookScriptPath(home))
	return err == nil && registered && fileExists(ClaudeHookScriptPath(home))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isNTMClaudeHookCommand reports whether a registered hook command is the
// script `ntm safety install` writes (at any home directory).
func isNTMClaudeHookCommand(command, scriptPath string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if command == scriptPath || command == shellQuote(scriptPath) {
		return true
	}
	unquoted := strings.Trim(command, `'"`)
	return filepath.Base(unquoted) == ClaudeHookFileName
}

// ClaudeHookRegistered reports whether settingsPath registers scriptPath as a
// PreToolUse hook that fires for the Bash tool. A missing settings file is
// "not registered", not an error.
func ClaudeHookRegistered(settingsPath, scriptPath string) (bool, error) {
	settings, _, err := readClaudeSettings(settingsPath)
	if err != nil || settings == nil {
		return false, err
	}
	groups, err := settings.preToolUseGroups()
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		if !matcherCoversBash(g.matcher) {
			continue
		}
		for _, h := range g.handlers {
			if isNTMClaudeHookCommand(h.Command, scriptPath) {
				return true, nil
			}
		}
	}
	return false, nil
}

// matcherCoversBash reports whether a PreToolUse matcher fires for Bash:
// empty and "*" match every tool, otherwise the matcher is a tool-name list.
func matcherCoversBash(matcher string) bool {
	matcher = strings.TrimSpace(matcher)
	if matcher == "" || matcher == "*" {
		return true
	}
	for _, part := range strings.Split(matcher, "|") {
		if strings.TrimSpace(part) == "Bash" {
			return true
		}
	}
	return false
}

// RegisterClaudeHook adds a Bash PreToolUse group running scriptPath to the
// Claude settings file, creating it when absent. Every other key, and the
// order of the top-level and "hooks" keys, is preserved. It reports whether
// the file changed (false when the hook was already registered).
func RegisterClaudeHook(settingsPath, scriptPath string) (bool, error) {
	if registered, err := ClaudeHookRegistered(settingsPath, scriptPath); err != nil {
		return false, err
	} else if registered {
		return false, nil
	}
	settings, mode, err := readClaudeSettings(settingsPath)
	if err != nil {
		return false, err
	}
	if settings == nil {
		settings = &orderedObject{values: map[string]json.RawMessage{}}
	}
	hooks, err := settings.object("hooks")
	if err != nil {
		return false, err
	}
	var groups []json.RawMessage
	if raw, ok := hooks.values["PreToolUse"]; ok {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return false, fmt.Errorf("%s: hooks.PreToolUse is not an array: %w", settingsPath, err)
		}
	}
	entry, err := json.Marshal(ClaudePolicyHookEntry(scriptPath))
	if err != nil {
		return false, err
	}
	groups = append(groups, entry)
	if err := hooks.setJSON("PreToolUse", groups); err != nil {
		return false, err
	}
	if err := settings.setJSON("hooks", hooks); err != nil {
		return false, err
	}
	return true, writeClaudeSettings(settingsPath, settings, mode)
}

// UnregisterClaudeHook removes every handler running the ntm hook script from
// the Claude settings file, dropping matcher groups, the PreToolUse list and
// the hooks object when that leaves them empty. It reports whether the file
// changed.
func UnregisterClaudeHook(settingsPath, scriptPath string) (bool, error) {
	settings, mode, err := readClaudeSettings(settingsPath)
	if err != nil || settings == nil {
		return false, err
	}
	if _, ok := settings.values["hooks"]; !ok {
		return false, nil
	}
	hooks, err := settings.object("hooks")
	if err != nil {
		return false, err
	}
	raw, ok := hooks.values["PreToolUse"]
	if !ok {
		return false, nil
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(raw, &groups); err != nil {
		return false, fmt.Errorf("%s: hooks.PreToolUse is not an array: %w", settingsPath, err)
	}
	changed := false
	kept := make([]json.RawMessage, 0, len(groups))
	for _, rawGroup := range groups {
		group, err := parseOrderedObject(rawGroup)
		if err != nil {
			kept = append(kept, rawGroup) // not ours to judge; leave it alone
			continue
		}
		var handlers []json.RawMessage
		if rawHandlers, ok := group.values["hooks"]; ok {
			if err := json.Unmarshal(rawHandlers, &handlers); err != nil {
				kept = append(kept, rawGroup)
				continue
			}
		}
		keptHandlers := make([]json.RawMessage, 0, len(handlers))
		for _, rawHandler := range handlers {
			var h dcg.HookHandler
			if json.Unmarshal(rawHandler, &h) == nil && isNTMClaudeHookCommand(h.Command, scriptPath) {
				changed = true
				continue
			}
			keptHandlers = append(keptHandlers, rawHandler)
		}
		if len(keptHandlers) == len(handlers) {
			kept = append(kept, rawGroup)
			continue
		}
		if len(keptHandlers) == 0 {
			continue
		}
		if err := group.setJSON("hooks", keptHandlers); err != nil {
			return false, err
		}
		encoded, err := group.MarshalJSON()
		if err != nil {
			return false, err
		}
		kept = append(kept, encoded)
	}
	if !changed {
		return false, nil
	}
	if len(kept) == 0 {
		hooks.delete("PreToolUse")
	} else if err := hooks.setJSON("PreToolUse", kept); err != nil {
		return false, err
	}
	if len(hooks.keys) == 0 {
		settings.delete("hooks")
	} else if err := settings.setJSON("hooks", hooks); err != nil {
		return false, err
	}
	return true, writeClaudeSettings(settingsPath, settings, mode)
}

type registeredGroup struct {
	matcher  string
	handlers []dcg.HookHandler
}

func (o *orderedObject) preToolUseGroups() ([]registeredGroup, error) {
	raw, ok := o.values["hooks"]
	if !ok {
		return nil, nil
	}
	var hooks struct {
		PreToolUse []struct {
			Matcher string            `json:"matcher"`
			Hooks   []dcg.HookHandler `json:"hooks"`
		} `json:"PreToolUse"`
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, fmt.Errorf("claude settings hooks: %w", err)
	}
	groups := make([]registeredGroup, 0, len(hooks.PreToolUse))
	for _, g := range hooks.PreToolUse {
		groups = append(groups, registeredGroup{matcher: g.Matcher, handlers: g.Hooks})
	}
	return groups, nil
}

// readClaudeSettings loads a settings file as an order-preserving object.
// A missing file returns (nil, 0, nil); a file that is not a JSON object is
// an error so ntm never rewrites something it cannot represent.
func readClaudeSettings(path string) (*orderedObject, os.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &orderedObject{values: map[string]json.RawMessage{}}, info.Mode().Perm(), nil
	}
	obj, err := parseOrderedObject(data)
	if err != nil {
		return nil, 0, fmt.Errorf("%s is not a JSON object ntm can update: %w", path, err)
	}
	return obj, info.Mode().Perm(), nil
}

// writeClaudeSettings replaces the settings file atomically. A symlinked
// settings file (dotfile managers) is updated at its target.
func writeClaudeSettings(path string, settings *orderedObject, mode os.FileMode) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
	}
	data, err := settings.MarshalJSON()
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		return fmt.Errorf("formatting %s: %w", path, err)
	}
	pretty.WriteByte('\n')
	return util.AtomicWriteFile(target, pretty.Bytes(), mode)
}

// orderedObject is a JSON object that remembers its key order, so editing one
// key of a user's settings file does not reshuffle the rest.
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func parseOrderedObject(data []byte) (*orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("top level is not an object")
	}
	obj := &orderedObject{values: map[string]json.RawMessage{}}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if _, dup := obj.values[key]; !dup {
			obj.keys = append(obj.keys, key)
		}
		obj.values[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after object")
	}
	return obj, nil
}

// object returns key's value as an ordered object, or a new empty one when
// the key is absent.
func (o *orderedObject) object(key string) (*orderedObject, error) {
	raw, ok := o.values[key]
	if !ok {
		return &orderedObject{values: map[string]json.RawMessage{}}, nil
	}
	obj, err := parseOrderedObject(raw)
	if err != nil {
		return nil, fmt.Errorf("claude settings %q is not an object: %w", key, err)
	}
	return obj, nil
}

func (o *orderedObject) setJSON(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding %q: %w", key, err)
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
	return nil
}

func (o *orderedObject) delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// MarshalJSON writes the keys in their remembered order.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(o.values[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
