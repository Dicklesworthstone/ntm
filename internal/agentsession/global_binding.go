package agentsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	gopsprocess "github.com/shirou/gopsutil/v4/process"

	ntmgit "github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/shellword"
)

// GlobalCredentialBinding is live, process-correlated evidence for the narrow
// global-account recovery lane. Paths and process observations are private;
// callers must not serialize this proof as a public recovery receipt.
type GlobalCredentialBinding struct {
	Session          BindingObservation `json:"-"`
	ProcessPID       int                `json:"-"`
	ProcessStartedAt int64              `json:"-"` // Unix milliseconds
	CredentialHome   string             `json:"-"`
}

const globalBindingTimeout = 5 * time.Second
const globalBindingReadLimit = 1 << 20
const globalBindingFileLimit = 4096
const globalBindingHostProcessLimit = 16384

type globalBindingFile struct {
	path string
	info os.FileInfo // The open descriptor's inode, rather than the pathname's.
}

type globalBindingProcess struct {
	pid, parent int
	started     int64
	executable  string
	argv        []string
	children    []int
	environment []string
	cwd         string
	files       []globalBindingFile
}

type globalBindingDependencies struct {
	platform    string
	home        func() (string, error)
	environment func() []string
	process     func(context.Context, int, bool) (globalBindingProcess, error)
	systemPaths func(string) ([]string, error)
}

// ObserveGlobalCredentialBinding proves that a live Claude/Codex process uses
// the caller's default credential home and has actually opened its native main
// conversation. An argv-only --resume ID, newest workspace file, or inferred
// account is insufficient. Unreadable or ambiguous evidence refuses recovery.
// Linux is the initial supported platform; other platforms fail closed.
func ObserveGlobalCredentialBinding(ctx context.Context, agentType, workDir string, panePID int) (GlobalCredentialBinding, error) {
	return observeGlobalCredentialBinding(ctx, agentType, workDir, panePID, globalBindingDependencies{
		platform: runtime.GOOS, home: os.UserHomeDir, environment: os.Environ,
		process: readGlobalBindingProcess, systemPaths: globalBindingSystemPaths,
	})
}

// ValidateGlobalCredentialLaunchCommand checks that a recorded command can be
// resumed through the global account lane. A live process proof alone cannot
// authorize replay of separately edited metadata. The caller must also verify
// the launch spec, prompt hash, live process and workspace before activation.
func ValidateGlobalCredentialLaunchCommand(agentType, command string, opts ResumeLaunchOptions) error {
	provider := ResumeProvider(agentType)
	if provider != "claude" && provider != "codex" {
		return errors.New("global credential replay supports only Claude and Codex")
	}
	validated, err := ResumeLaunchCommand(provider, "global-credential-proof", command, opts)
	if err != nil {
		return errors.New("recorded command cannot preserve its settings through native recovery")
	}
	if provider == "codex" && opts.SystemPromptFile != "" {
		// ResumeLaunchCommand already validated this exact NTM-generated form.
		prefix := "CODEX_SYSTEM_PROMPT=\"$(cat " + shellQuote(opts.SystemPromptFile) + ")\" "
		validated = strings.TrimPrefix(validated, prefix)
	}
	words, err := shellword.Literal(validated)
	if err != nil || len(words) == 0 {
		return errors.New("recorded command is not a literal native provider invocation")
	}
	assignment := validated[words[0].Start:words[0].End]
	if resumeEnvironmentAssignment(assignment) {
		name, _, _ := strings.Cut(assignment, "=")
		return fmt.Errorf("recorded environment assignment %s prevents global credential replay", name)
	}
	start := 0
	if filepath.Base(words[0].Value) == "systemd-run" {
		// Its exact memory-limit wrapper was checked by ResumeLaunchCommand.
		start = 6
	}
	argv := make([]string, 0, len(words)-start)
	for _, word := range words[start:] {
		argv = append(argv, word.Value)
	}
	return validateGlobalBindingArguments(provider, argv)
}

func observeGlobalCredentialBinding(ctx context.Context, agentType, workDir string, panePID int, deps globalBindingDependencies) (GlobalCredentialBinding, error) {
	var empty GlobalCredentialBinding
	if ctx == nil {
		return empty, errors.New("global credential observation requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, globalBindingTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	provider := ResumeProvider(agentType)
	if deps.platform != "linux" || (provider != "claude" && provider != "codex") || panePID <= 0 {
		return empty, errors.New("global credential proof is unsupported for this platform, provider, or pane")
	}
	home, err := deps.home()
	if err != nil {
		return empty, errors.New("cannot resolve global credential home")
	}
	home, err = globalBindingDirectory(home)
	if err != nil {
		return empty, errors.New("global home is not an existing absolute directory")
	}
	workDir, err = globalBindingDirectory(workDir)
	if err != nil {
		return empty, errors.New("workspace is not an existing absolute directory")
	}
	credentialHome := filepath.Join(home, "."+provider)
	resolvedCredentialHome, err := globalBindingDirectory(credentialHome)
	if err != nil || !pathWithin(home, resolvedCredentialHome) {
		return empty, errors.New("default credential directory is unavailable or outside the global home")
	}
	if resolvedCredentialHome != filepath.Clean(credentialHome) {
		// Account activation is serialized by canonical HOME. Following an
		// in-home symlink could reach a nested user's credentials under another
		// HOME lock, allowing concurrent activations against the same files.
		return empty, errors.New("symlinked credential directories cannot prove exclusive global account scope")
	}
	if err := validateGlobalBindingEnvironment(provider, home, credentialHome, deps.environment()); err != nil {
		return empty, fmt.Errorf("recovery process environment: %w", err)
	}
	systemPaths, err := deps.systemPaths(provider)
	if err != nil {
		return empty, errors.New("managed provider configuration cannot be inspected")
	}
	if err := validateGlobalBindingConfig(ctx, provider, home, workDir, systemPaths); err != nil {
		return empty, err
	}
	type queuedProcess struct{ pid, parent, depth, providerRoot int }
	queue := []queuedProcess{{pid: panePID}}
	seen := make(map[int]bool)
	roots := make(map[int]bool)
	var candidates []globalBindingProcess
	var originalTree []globalBindingProcess
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		node := queue[0]
		queue = queue[1:]
		if seen[node.pid] || len(seen) >= processTreeMaxNodes {
			return empty, errors.New("process tree is ambiguous or exceeds the observation bound")
		}
		seen[node.pid] = true
		process, err := deps.process(ctx, node.pid, false)
		if err != nil || process.pid != node.pid || process.started <= 0 {
			return empty, globalBindingReadError(ctx, "cannot observe the complete live process tree")
		}
		if node.parent != 0 && process.parent != node.parent {
			return empty, errors.New("provider process left the target pane process tree")
		}
		originalTree = append(originalTree, process)
		if globalBindingProviderProcess(provider, process) {
			if node.providerRoot == 0 {
				node.providerRoot = node.pid
				roots[node.pid] = true
			}
			candidates = append(candidates, process)
		}
		if len(process.children) > processTreeFanout || (node.depth >= processTreeMaxDepth && len(process.children) > 0) {
			return empty, errors.New("process tree exceeds the observation bound")
		}
		for _, child := range process.children {
			if child <= 0 {
				return empty, errors.New("process tree contains an invalid child")
			}
			queue = append(queue, queuedProcess{child, node.pid, node.depth + 1, node.providerRoot})
		}
	}
	if len(roots) != 1 {
		return empty, errors.New("expected exactly one main provider process lineage")
	}
	var result GlobalCredentialBinding
	var observed globalBindingProcess
	for _, candidate := range candidates {
		process, err := deps.process(ctx, candidate.pid, true)
		if err != nil || !sameGlobalBindingProcess(candidate, process) {
			return empty, globalBindingReadError(ctx, "provider process changed during observation")
		}
		if err := validateGlobalBindingEnvironment(provider, home, credentialHome, process.environment); err != nil {
			return empty, fmt.Errorf("provider environment: %w", err)
		}
		if err := validateGlobalBindingArguments(provider, process.argv); err != nil {
			return empty, err
		}
		cwd, err := globalBindingDirectory(process.cwd)
		if err != nil || cwd != workDir {
			return empty, errors.New("provider process workspace does not match the target pane")
		}
		binding, err := globalBindingOpenSession(ctx, provider, workDir, resolvedCredentialHome, process)
		if err != nil {
			return empty, err
		}
		if binding.SessionID == "" {
			continue // A node launcher can own a native provider child.
		}
		if result.ProcessPID != 0 {
			return empty, errors.New("more than one provider process owns a main transcript")
		}
		observed = process
		result = GlobalCredentialBinding{Session: binding, ProcessPID: process.pid, ProcessStartedAt: process.started, CredentialHome: resolvedCredentialHome}
	}
	if result.ProcessPID == 0 {
		return empty, errors.New("provider has no opened native main transcript; argv alone cannot prove a resumed conversation")
	}
	// A fresh process handle is necessary: gopsutil caches CreateTime on each
	// handle, so reusing the original one would not detect PID replacement.
	for _, original := range originalTree {
		current, err := deps.process(ctx, original.pid, false)
		if err != nil || !sameGlobalBindingProcess(original, current) || !sameGlobalBindingChildren(original.children, current.children) {
			return empty, globalBindingReadError(ctx, "pane process tree changed during observation")
		}
	}
	final, err := deps.process(ctx, result.ProcessPID, true)
	if err != nil || !sameGlobalBindingProcess(observed, final) || !reflect.DeepEqual(observed.environment, final.environment) || observed.cwd != final.cwd {
		return empty, globalBindingReadError(ctx, "provider identity or environment changed during observation")
	}
	confirmed, err := globalBindingOpenSession(ctx, provider, workDir, resolvedCredentialHome, final)
	if err != nil || confirmed.SessionID != result.Session.SessionID || confirmed.SourcePath != result.Session.SourcePath {
		return empty, globalBindingReadError(ctx, "opened native conversation changed during observation")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	systemPaths, err = deps.systemPaths(provider)
	if err != nil {
		return empty, errors.New("managed provider configuration cannot be inspected")
	}
	if err := validateGlobalBindingConfig(ctx, provider, home, workDir, systemPaths); err != nil {
		return empty, err
	}
	result.Session = confirmed
	return result, nil
}

func globalBindingReadError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New(message)
}

func globalBindingDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("absolute directory required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("existing directory required")
	}
	return filepath.Clean(resolved), nil
}

func sameGlobalBindingProcess(a, b globalBindingProcess) bool {
	return a.pid == b.pid && a.started > 0 && a.started == b.started && a.parent == b.parent &&
		a.executable == b.executable && reflect.DeepEqual(a.argv, b.argv)
}

func sameGlobalBindingChildren(a, b []int) bool {
	a, b = append([]int(nil), a...), append([]int(nil), b...)
	sort.Ints(a)
	sort.Ints(b)
	return reflect.DeepEqual(a, b)
}

func globalBindingProviderProcess(provider string, process globalBindingProcess) bool {
	if len(process.argv) == 0 {
		return false
	}
	executable := filepath.Base(process.executable)
	if executable == provider && filepath.Base(process.argv[0]) == provider {
		return true
	}
	if provider == "claude" && filepath.Base(process.argv[0]) == "claude" &&
		strings.HasSuffix(filepath.ToSlash(filepath.Dir(process.executable)), "/claude/versions") {
		return true
	}
	if (executable != "node" && executable != "bun") || len(process.argv) < 2 {
		return false
	}
	script := filepath.ToSlash(process.argv[1])
	return (provider == "claude" && strings.HasSuffix(script, "/@anthropic-ai/claude-code/cli.js")) ||
		(provider == "codex" && strings.HasSuffix(script, "/@openai/codex/bin/codex.js"))
}

func readGlobalBindingProcess(ctx context.Context, pid int, inspect bool) (globalBindingProcess, error) {
	result := globalBindingProcess{pid: pid}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	process, err := gopsprocess.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return result, err
	}
	result.started, err = process.CreateTimeWithContext(ctx)
	if err != nil {
		return result, err
	}
	parent, err := process.PpidWithContext(ctx)
	if err != nil {
		return result, err
	}
	result.parent = int(parent)
	status, err := process.StatusWithContext(ctx)
	if err != nil {
		return result, err
	}
	for _, value := range status {
		if value == "zombie" || value == "dead" || value == "Z" || value == "X" {
			return result, errors.New("provider process has exited")
		}
	}
	result.argv, err = process.CmdlineSliceWithContext(ctx)
	if err != nil {
		return result, err
	}
	result.executable, err = process.ExeWithContext(ctx)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.children, err = globalBindingChildPIDs(ctx, pid)
	if err != nil {
		return result, err
	}
	if inspect {
		result.environment, err = process.EnvironWithContext(ctx)
		if err != nil {
			return result, err
		}
		// gopsutil splits Linux's trailing NUL into one empty entry. Remove
		// that terminator, while still rejecting malformed interior entries.
		if n := len(result.environment); n > 0 && result.environment[n-1] == "" {
			result.environment = result.environment[:n-1]
		}
		result.cwd, err = process.CwdWithContext(ctx)
		if err != nil {
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.files, err = globalBindingOpenFiles(ctx, pid)
		if err != nil {
			return result, err
		}
	}
	return result, ctx.Err()
}

func globalBindingReadDirectory(path string, limit int) ([]os.DirEntry, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("process observation exceeds directory bound")
	}
	return entries, nil
}

// A process can spawn children from any thread. Reading every bounded task
// children file avoids an unbounded, racy scan of every process on the host.
func globalBindingChildPIDs(ctx context.Context, pid int) ([]int, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid), "task")
	tasks, err := globalBindingReadDirectory(root, processTreeMaxNodes)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]bool)
	var children []int
	for _, task := range tasks {
		data, _, err := readGlobalBindingFile(ctx, filepath.Join(root, task.Name(), "children"))
		if errors.Is(err, os.ErrNotExist) {
			// Linux exposes task/children only with checkpoint/restore support.
			// A vanished thread also takes this conservative fresh snapshot path.
			return globalBindingChildPIDsFromParents(ctx, pid)
		}
		if err != nil || len(data) > globalBindingReadLimit {
			return nil, globalBindingReadError(ctx, "cannot inspect complete provider process children")
		}
		for _, value := range strings.Fields(string(data)) {
			child, err := strconv.Atoi(value)
			if err != nil || child <= 0 {
				return nil, errors.New("process children observation is malformed")
			}
			if seen[child] {
				continue
			}
			seen[child] = true
			children = append(children, child)
			if len(children) > processTreeFanout {
				return nil, errors.New("process children exceed observation bound")
			}
		}
	}
	sort.Ints(children)
	return children, ctx.Err()
}

func globalBindingChildPIDsFromParents(ctx context.Context, pid int) ([]int, error) {
	entries, err := globalBindingReadDirectory("/proc", globalBindingHostProcessLimit)
	if err != nil {
		return nil, err
	}
	var children []int
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child, err := strconv.ParseInt(entry.Name(), 10, 32)
		if err != nil || child <= 0 {
			continue
		}
		process := &gopsprocess.Process{Pid: int32(child)}
		parent, err := process.PpidWithContext(ctx)
		if errors.Is(err, os.ErrNotExist) {
			continue // A vanished process is not a live descendant.
		}
		if err != nil {
			return nil, globalBindingReadError(ctx, "cannot inspect complete process parent table")
		}
		if int(parent) == pid {
			children = append(children, int(child))
			if len(children) > processTreeFanout {
				return nil, errors.New("process children exceed observation bound")
			}
		}
	}
	sort.Ints(children)
	return children, ctx.Err()
}

func globalBindingOpenFiles(ctx context.Context, pid int) ([]globalBindingFile, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := globalBindingReadDirectory(root, globalBindingFileLimit)
	if err != nil {
		return nil, err
	}
	var files []globalBindingFile
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fdPath := filepath.Join(root, entry.Name())
		path, err := os.Readlink(fdPath)
		if err != nil {
			return nil, errors.New("provider descriptor set changed during observation")
		}
		if !isProviderSessionFile("claude", path) && !isProviderSessionFile("codex", path) {
			continue
		}
		info, err := os.Stat(fdPath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("opened native transcript descriptor cannot be inspected")
		}
		files = append(files, globalBindingFile{path: path, info: info})
	}
	return files, ctx.Err()
}

func globalBindingUnsafeEnvironment(provider, key string) bool {
	key = strings.ToUpper(key)
	if strings.HasPrefix(key, "SHALLOW_") || strings.HasPrefix(key, "CAAM_") ||
		strings.HasPrefix(key, "GIT_CONFIG") || key == "GIT_DIR" || key == "GIT_WORK_TREE" || key == "GIT_COMMON_DIR" ||
		key == "GIT_CEILING_DIRECTORIES" || key == "GIT_DISCOVERY_ACROSS_FILESYSTEM" || key == "HOST_PROC" {
		return true
	}
	if provider == "claude" {
		return strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "CLAUDE_CODE_OAUTH_") ||
			strings.HasPrefix(key, "CLAUDE_CODE_USE_") || (strings.HasPrefix(key, "CLAUDE_") &&
			(strings.Contains(key, "TOKEN") || strings.Contains(key, "API_KEY") || strings.Contains(key, "AUTH") || strings.Contains(key, "BASE_URL")))
	}
	return strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "AZURE_OPENAI_") ||
		key == "CHATGPT_BASE_URL" || key == "CODEX_MODEL_PROVIDER" || key == "CODEX_PROVIDER" ||
		(strings.HasPrefix(key, "CODEX_") && (strings.Contains(key, "AUTH") || strings.Contains(key, "TOKEN") ||
			strings.Contains(key, "API_KEY") || strings.Contains(key, "BASE_URL") || strings.Contains(key, "CONFIG")))
}

func validateGlobalBindingEnvironment(provider, home, credentialHome string, entries []string) error {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			return errors.New("process environment is malformed")
		}
		if _, duplicate := env[key]; duplicate {
			return errors.New("process environment has duplicate keys")
		}
		env[key] = value
		if value != "" && globalBindingUnsafeEnvironment(provider, key) {
			return errors.New("explicit account, credential, or provider environment prevents global account proof")
		}
	}
	actualHome, err := globalBindingDirectory(env["HOME"])
	if err != nil || actualHome != home {
		return errors.New("process HOME differs from the recovery process")
	}
	key := "CODEX_HOME"
	if provider == "claude" {
		key = "CLAUDE_CONFIG_DIR"
	}
	if value := env[key]; value != "" {
		actual, err := globalBindingDirectory(value)
		expected, expectedErr := globalBindingDirectory(credentialHome)
		if err != nil || expectedErr != nil || actual != expected {
			return errors.New("provider uses a non-default credential directory")
		}
	}
	return nil
}

func validateGlobalBindingArguments(provider string, argv []string) error {
	for i := 1; i < len(argv); i++ {
		argument := argv[i]
		if !strings.HasPrefix(argument, "-") {
			continue
		}
		key, _, _ := strings.Cut(argument, "=")
		if strings.Contains(key, "provider") || strings.Contains(key, "auth") || strings.Contains(key, "credential") ||
			strings.Contains(key, "token") || strings.Contains(key, "api-key") || strings.Contains(key, "base-url") ||
			strings.Contains(key, "endpoint") || strings.Contains(key, "config-dir") || key == "--home" || key == "--profile" {
			return errors.New("explicit credential or provider arguments prevent global account proof")
		}
		switch key {
		case "--cwd", "--cd", "-C":
			return errors.New("command workspace overrides prevent global configuration proof")
		}
		if provider == "claude" && (key == "--settings" || key == "--setting-sources" || key == "--settings-sources") {
			return errors.New("custom Claude settings prevent global credential proof")
		}
		if provider == "codex" {
			if strings.HasPrefix(argument, "-C") {
				return errors.New("command workspace overrides prevent global configuration proof")
			}
			if key == "--profile" || key == "-p" || key == "--oss" || key == "--local-provider" || strings.HasPrefix(argument, "-p") {
				return errors.New("Codex configuration overrides prevent global credential proof")
			}
			var override string
			switch {
			case argument == "--config" || argument == "-c":
				i++
				if i >= len(argv) {
					return errors.New("incomplete Codex configuration override")
				}
				override = argv[i]
			case strings.HasPrefix(argument, "--config="):
				override = strings.TrimPrefix(argument, "--config=")
			case strings.HasPrefix(argument, "-c"):
				override = strings.TrimPrefix(strings.TrimPrefix(argument, "-c"), "=")
			default:
				continue
			}
			setting, _, hasValue := strings.Cut(override, "=")
			if !hasValue {
				return errors.New("ambiguous Codex configuration override")
			}
			switch strings.TrimSpace(setting) {
			case "model", "model_reasoning_effort", "model_reasoning_summary", "model_reasoning_summary_format", "model_verbosity", "approval_policy", "sandbox_mode":
			default:
				return errors.New("Codex configuration overrides prevent global credential proof")
			}
		}
	}
	return nil
}

func globalBindingSystemPaths(provider string) ([]string, error) {
	if provider == "codex" {
		return []string{"/etc/codex/config.toml", "/etc/codex/managed_config.toml", "/etc/codex/requirements.toml"}, nil
	}
	paths := []string{"/etc/claude-code/managed-settings.json"}
	entries, err := globalBindingReadDirectory("/etc/claude-code/managed-settings.d", 256)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			paths = append(paths, filepath.Join("/etc/claude-code/managed-settings.d", entry.Name()))
		}
	}
	return paths, nil
}

func validateGlobalBindingConfig(ctx context.Context, provider, home, workDir string, systemPaths []string) error {
	paths := append([]string(nil), systemPaths...)
	if provider == "claude" {
		paths = append(paths, filepath.Join(home, ".claude", "settings.json"),
			filepath.Join(home, ".claude", "remote-settings.json"), filepath.Join(home, ".claude.json"))
	} else {
		paths = append(paths, filepath.Join(home, ".codex", "config.toml"))
	}
	// Provider project configuration can be inherited from an ancestor. Reading
	// each bounded ancestor is conservative even when a provider would stop at
	// an earlier trust/root boundary.
	dir := workDir
	gitRootSeen := false
	for depth := 0; ; depth++ {
		if depth >= 64 {
			return errors.New("workspace ancestry exceeds configuration proof bound")
		}
		if provider == "claude" {
			paths = append(paths, filepath.Join(dir, ".claude", "settings.json"), filepath.Join(dir, ".claude", "settings.local.json"))
			if !gitRootSeen {
				info, statErr := os.Stat(filepath.Join(dir, ".git"))
				if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
					return errors.New("workspace Git configuration cannot be inspected")
				}
				if statErr == nil {
					gitRootSeen = true
					if !info.IsDir() {
						// Claude reads linked-worktree local settings from the main
						// checkout. Reuse Git's existing common-dir resolution.
						common, err := ntmgit.CommonDir(ctx, dir)
						if err != nil || filepath.Base(common) != ".git" {
							return globalBindingReadError(ctx, "linked workspace settings cannot be proven")
						}
						paths = append(paths, filepath.Join(filepath.Dir(common), ".claude", "settings.local.json"))
					}
				}
			}
		} else {
			paths = append(paths, filepath.Join(dir, ".codex", "config.toml"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		data, _, err := readGlobalBindingFile(ctx, path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return globalBindingReadError(ctx, "native credential configuration cannot be inspected")
		}
		if len(data) > globalBindingReadLimit {
			return errors.New("native credential configuration exceeds the proof size bound")
		}
		var settings map[string]interface{}
		if provider == "codex" {
			_, err = toml.Decode(string(data), &settings)
		} else {
			err = json.Unmarshal(data, &settings)
		}
		if err != nil || settings == nil {
			return errors.New("native credential configuration is malformed")
		}
		if !globalBindingConfigSafe(provider, settings) {
			return errors.New("native configuration overrides account credentials or provider routing")
		}
	}
	return ctx.Err()
}

func globalBindingConfigSafe(provider string, value interface{}) bool {
	switch entries := value.(type) {
	case map[string]interface{}:
		for key, item := range entries {
			normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			text, isString := item.(string)
			switch normalized {
			case "home", "codexhome", "claudeconfigdir":
				return false
			}
			if provider == "codex" {
				switch normalized {
				case "modelprovider":
					if !isString || text != "openai" {
						return false
					}
				case "cliauthcredentialsstore":
					if !isString || text != "file" {
						return false
					}
				case "forcedloginmethod":
					if !isString || text != "chatgpt" {
						return false
					}
				case "modelproviders", "profiles", "profile", "forcedchatgptworkspaceid", "experimentalbearertoken", "envkey", "httpheaders", "envhttpheaders", "requiresopenaiauth", "authmode", "chatgptbaseurl", "preferredauthmethod", "oauthissuer":
					return false
				}
			}
			if globalBindingUnsafeEnvironment(provider, key) && item != nil && (!isString || text != "") {
				return false
			}
			switch normalized {
			case "apikeyhelper", "apikey", "primaryapikey", "authtoken", "oauthtoken", "baseurl", "apibaseurl", "authfile", "credentialfile", "forceloginmethod", "forceloginorguuid", "policyhelper", "cloudgateway":
				return false
			}
			if !globalBindingConfigSafe(provider, item) {
				return false
			}
		}
	case []interface{}:
		for _, item := range entries {
			if !globalBindingConfigSafe(provider, item) {
				return false
			}
		}
	}
	return true
}

func readGlobalBindingFile(ctx context.Context, path string) ([]byte, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("regular file required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, nil, errors.New("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, globalBindingReadLimit+1))
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return data, opened, nil
}

func globalBindingOpenSession(ctx context.Context, provider, workDir, credentialHome string, process globalBindingProcess) (BindingObservation, error) {
	var result BindingObservation
	seen := make(map[string]bool)
	for _, opened := range process.files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path := opened.path
		if !filepath.IsAbs(path) || !isProviderSessionFile(provider, path) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !pathWithin(credentialHome, resolved) || !isProviderSessionFile(provider, resolved) {
			return result, errors.New("opened native transcript is outside the global credential home")
		}
		data, info, err := readGlobalBindingFile(ctx, resolved)
		if err != nil {
			return result, globalBindingReadError(ctx, "opened native transcript cannot be read")
		}
		if opened.info == nil || !os.SameFile(opened.info, info) {
			return result, errors.New("native transcript path no longer names the provider's open file")
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		id, subagent, err := globalBindingTranscript(provider, workDir, resolved, data)
		if err != nil {
			return result, err
		}
		if subagent {
			continue
		}
		if result.SessionID != "" {
			return result, errors.New("provider has multiple opened main transcripts")
		}
		requested := resumedSessionID(provider, process.argv)
		if provider == "claude" {
			for i, arg := range process.argv {
				key, value, inline := strings.Cut(arg, "=")
				if key != "--resume" && key != "-r" && key != "--session-id" {
					continue
				}
				if inline {
					requested = value
				} else if i+1 < len(process.argv) {
					requested = process.argv[i+1]
				}
			}
		}
		if requested != "" && requested != id {
			return result, errors.New("opened transcript does not match the requested native conversation")
		}
		result = BindingObservation{
			AgentType: canonicalAgentType("", provider), Provider: provider, SessionID: id,
			Source: DiscoverySourceProcessTree, SourcePath: resolved, SourceUpdatedAt: info.ModTime(),
			ObservedAt: time.Now().UTC(), Freshness: BindingFresh, Confidence: 0.99,
		}
	}
	return result, ctx.Err()
}

func globalBindingTranscript(provider, workDir, path string, data []byte) (string, bool, error) {
	invalid := errors.New("opened native transcript has no unambiguous main-session identity and workspace")
	if provider == "codex" {
		var meta codexSessionMeta
		if err := json.NewDecoder(bytes.NewReader(data)).Decode(&meta); err != nil || meta.Type != "session_meta" {
			return "", false, invalid
		}
		var routing struct {
			Payload struct {
				Source         json.RawMessage `json:"source"`
				ModelProvider  string          `json:"model_provider"`
				ParentThreadID string          `json:"parent_thread_id"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(bytes.NewReader(data)).Decode(&routing); err != nil {
			return "", false, invalid
		}
		if (meta.Payload.ThreadSource != "" && meta.Payload.ThreadSource != "user") ||
			routing.Payload.ParentThreadID != "" || bytes.Contains(routing.Payload.Source, []byte("subagent")) {
			return "", true, nil
		}
		var source string
		if json.Unmarshal(routing.Payload.Source, &source) != nil || source != "cli" {
			return "", false, errors.New("native transcript does not prove a main interactive Codex session")
		}
		if routing.Payload.ModelProvider != "openai" {
			return "", false, errors.New("native transcript uses a non-default model provider")
		}
		id := codexMetaID(meta)
		cwd, err := globalBindingDirectory(codexMetaWorkspace(meta))
		fileID := codexUUIDAtEndPattern.FindString(strings.TrimSuffix(filepath.Base(path), ".jsonl"))
		if err != nil || cwd != workDir || codexUUIDAtEndPattern.FindString(id) != id || id == "" || fileID != id {
			return "", false, invalid
		}
		return id, false, nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if id == "" || codexUUIDAtEndPattern.FindString(id) != id || filepath.Base(filepath.Dir(path)) != encodeClaudeProjectDir(workDir) {
		return "", false, invalid
	}
	var mainSeen bool
	for _, line := range bytes.Split(data, []byte("\n")) {
		var entry struct {
			SessionID   string `json:"sessionId"`
			Cwd         string `json:"cwd"`
			IsSidechain *bool  `json:"isSidechain"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.SessionID == "" || entry.Cwd == "" {
			continue
		}
		cwd, err := globalBindingDirectory(entry.Cwd)
		if entry.SessionID != id || err != nil || cwd != workDir {
			return "", false, invalid
		}
		if entry.IsSidechain == nil {
			continue
		}
		if *entry.IsSidechain {
			return "", true, nil
		}
		mainSeen = true
	}
	if mainSeen {
		return id, false, nil
	}
	return "", false, invalid
}
