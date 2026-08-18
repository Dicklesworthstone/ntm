// Package agentsession discovers the per-pane agent CLI session ID for a given
// working directory and agent type. This is used by `ntm sessions save` to
// record which provider session ran in each pane so it can later be resumed via
// casr (Cross Agent Session Resumer) or the agent's native `--resume <id>`.
//
// ntm owns the tmux topology capture/restore; per-pane provider-session resume
// is intentionally delegated to casr / native --resume rather than
// reimplementing provider-specific session formats here. This package only
// needs to find the *id* of the most-recent session for a directory; casr does
// the rest.
package agentsession

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v4/process"

	processutil "github.com/Dicklesworthstone/ntm/internal/process"
)

// claudeNonAlnumPattern matches every character Claude Code rewrites when it
// derives a project directory name from a cwd. Claude Code's encoder is
// `cwd.replace(/[^a-zA-Z0-9]/g, "-")` — it collapses ALL non-alphanumeric
// characters (including '_', '.', '/', spaces, '+', ':') to '-'.
var claudeNonAlnumPattern = regexp.MustCompile(`[^a-zA-Z0-9]`)

var codexUUIDAtEndPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const (
	casrDiscoveryTimeout    = 30 * time.Second
	casrDiscoveryLimit      = 20
	processTreeMaxDepth     = 8
	processTreeFanout       = 64
	processTreeMaxNodes     = 256
	processFilesTimeout     = 2 * time.Second
	processSessionWindow    = 5 * time.Minute
	bindingFreshnessHorizon = processSessionWindow
)

// DiscoverySource identifies how a provider session was bound to a pane.
type DiscoverySource string

const (
	DiscoverySourceProcessTree DiscoverySource = "process_tree"
	DiscoverySourceCASR        DiscoverySource = "casr"
	DiscoverySourceNativeStore DiscoverySource = "native_store"
)

// BindingFreshness describes whether a provider binding is current enough to
// trust as a fresh sample. Stale bindings remain useful for explicit restore
// diagnostics but are never silently presented as fresh evidence.
type BindingFreshness string

const (
	BindingFresh       BindingFreshness = "fresh"
	BindingStale       BindingFreshness = "stale"
	BindingUnavailable BindingFreshness = "unavailable"
)

// Info describes a discovered agent CLI session for a pane.
type Info struct {
	// AgentType is the canonical ntm agent type ("cc", "cod", "gmi", "agy").
	AgentType string `json:"agent_type"`
	// SessionID is the provider session id (e.g. the Claude Code UUID).
	SessionID string `json:"session_id"`
	// Provider is the casr/native provider name ("claude", "codex", "gemini", "antigravity").
	Provider string `json:"provider"`
	// SourcePath is the on-disk session file the id was discovered from.
	SourcePath string `json:"-"`
	// UpdatedAt is the modification time of the session file.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// Source identifies the discovery mechanism that produced this binding.
	Source DiscoverySource `json:"source,omitempty"`
}

// BindingObservation is a bounded, point-in-time provider-session sample.
// SourcePath remains process-private so status surfaces do not expose local
// transcript locations; session persistence may copy it explicitly when needed.
type BindingObservation struct {
	AgentType       string           `json:"agent_type"`
	SessionID       string           `json:"session_id,omitempty"`
	Provider        string           `json:"provider,omitempty"`
	Source          DiscoverySource  `json:"source,omitempty"`
	ObservedAt      time.Time        `json:"observed_at"`
	SourceUpdatedAt time.Time        `json:"source_updated_at,omitempty"`
	Freshness       BindingFreshness `json:"freshness"`
	Confidence      float64          `json:"confidence"`
	FailureCode     string           `json:"failure_code,omitempty"`
	SourcePath      string           `json:"-"`
}

// ResumeProvider maps an ntm agent type to the casr/provider name used by
// `casr <provider> resume` and `casr -<flag>`. Returns "" for agent types that
// have no provider-session resume path (user panes, editor agents, etc.).
//
// Note: "gmi"/"gemini" (the retired Gemini CLI) and "agy"/"antigravity" (its
// successor, the Antigravity CLI) are DISTINCT providers with distinct on-disk
// session stores under the shared ~/.gemini parent (~/.gemini/tmp for gmi vs
// ~/.gemini/antigravity-cli for agy). They must never be conflated.
func ResumeProvider(agentType string) string {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "cc", "claude", "claude-code", "claudecode":
		return "claude"
	case "cod", "codex":
		return "codex"
	case "gmi", "gemini":
		return "gemini"
	case "agy", "antigravity", "antigravity-cli":
		return "antigravity"
	default:
		return ""
	}
}

type casrListEnvelope struct {
	Items []casrListItem `json:"items"`
}

type casrListItem struct {
	SessionID    string `json:"session_id"`
	Provider     string `json:"provider"`
	Workspace    string `json:"workspace"`
	Path         string `json:"path"`
	StartedAt    int64  `json:"started_at"`
	LastActiveAt int64  `json:"last_active_at"`
}

// Discoverer resolves provider sessions for one topology capture. Its fallback
// cache prevents a multi-pane save from repeating CASR or native workspace
// scans when process-level discovery is unavailable.
type Discoverer struct {
	homeDir            func() (string, error)
	lookPath           func(string) (string, error)
	runCommand         func(context.Context, string, ...string) ([]byte, error)
	findProcessSession func(string, string, string, int) *Info
	findProcessStart   func(int, string) int64
	fallbackCache      map[string]*Info
	casrItemsCache     map[string][]casrListItem
	casrChecked        map[string]bool
	claimedSessions    map[string]bool
}

// NewDiscoverer creates a session discoverer scoped to one capture operation.
func NewDiscoverer() *Discoverer {
	return &Discoverer{
		homeDir:            os.UserHomeDir,
		lookPath:           exec.LookPath,
		runCommand:         runDiscoveryCommand,
		findProcessSession: discoverProcessSession,
		findProcessStart:   agentProcessStartMillis,
		fallbackCache:      make(map[string]*Info),
		casrItemsCache:     make(map[string][]casrListItem),
		casrChecked:        make(map[string]bool),
		claimedSessions:    make(map[string]bool),
	}
}

func runDiscoveryCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Discover finds the agent CLI session associated with a pane. It first uses
// the pane's process tree, which can distinguish multiple same-provider panes
// in one workspace. Codex and Gemini then delegate structured workspace
// discovery to CASR when available, with native structured-store fallbacks.
// It returns nil when no session can be located because topology capture must
// remain best-effort.
func (d *Discoverer) Discover(agentType, workDir string, panePID int) *Info {
	return d.DiscoverContext(context.Background(), agentType, workDir, panePID)
}

// DiscoverContext is the cancellation-aware form of Discover. External
// commands inherit ctx, and native fallbacks check cancellation between each
// bounded discovery stage.
func (d *Discoverer) DiscoverContext(ctx context.Context, agentType, workDir string, panePID int) *Info {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil
	}
	provider := ResumeProvider(agentType)
	if provider == "" {
		return nil
	}

	homeFn := d.homeDir
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, _ := homeFn()
	if ctx.Err() != nil {
		return nil
	}
	var processStartedAt int64
	if panePID > 0 {
		finder := d.findProcessSession
		if finder == nil {
			finder = discoverProcessSession
		}
		if info := finder(agentType, provider, home, panePID); info != nil {
			if ctx.Err() != nil {
				return nil
			}
			if info.Source == "" {
				info.Source = DiscoverySourceProcessTree
			}
			d.claim(info)
			return info
		}
	}
	if panePID > 0 {
		startFinder := d.findProcessStart
		if startFinder == nil {
			startFinder = agentProcessStartMillis
		}
		processStartedAt = startFinder(panePID, provider)
		if ctx.Err() != nil {
			return nil
		}
	}
	if workDir == "" {
		return nil
	}

	cleanWorkDir := filepath.Clean(workDir)
	cacheKey := provider + "\x00" + cleanWorkDir
	if panePID > 0 {
		cacheKey += "\x00pid=" + strconv.Itoa(panePID)
	}
	if processStartedAt > 0 {
		cacheKey += "\x00start=" + strconv.FormatInt(processStartedAt, 10)
	}
	if d.fallbackCache == nil {
		d.fallbackCache = make(map[string]*Info)
	}
	if cached, ok := d.fallbackCache[cacheKey]; ok {
		if ctx.Err() != nil {
			return nil
		}
		return cloneInfo(cached)
	}

	var info *Info
	if provider == "codex" || provider == "gemini" {
		info = d.discoverCASR(ctx, agentType, provider, cleanWorkDir, processStartedAt)
	}
	if ctx.Err() != nil {
		return nil
	}
	if info != nil {
		d.claim(info)
		d.fallbackCache[cacheKey] = cloneInfo(info)
		return info
	}
	if home == "" {
		d.fallbackCache[cacheKey] = nil
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}

	switch provider {
	case "claude":
		info = discoverClaudeContext(ctx, home, cleanWorkDir)
	case "codex":
		info = discoverCodexContext(ctx, home, cleanWorkDir, d.claimedSessions)
	case "gemini":
		info = discoverGeminiContext(ctx, home, cleanWorkDir, processStartedAt, d.claimedSessions)
	case "antigravity":
		info = discoverAntigravityContext(ctx, home, cleanWorkDir)
	}
	if ctx.Err() != nil {
		return nil
	}
	if info != nil && info.Source == "" {
		info.Source = DiscoverySourceNativeStore
	}
	d.claim(info)
	d.fallbackCache[cacheKey] = cloneInfo(info)
	return info
}

// ObserveBinding samples one provider binding with explicit evidence quality.
// A missing or cancelled lookup is represented as unavailable rather than an
// ambiguous empty successful result.
func (d *Discoverer) ObserveBinding(ctx context.Context, agentType, workDir string, panePID int, observedAt time.Time) BindingObservation {
	if ctx == nil {
		ctx = context.Background()
	}
	observedAt = observedAt.UTC()
	observation := BindingObservation{
		AgentType:  canonicalAgentType(agentType, ResumeProvider(agentType)),
		ObservedAt: observedAt,
		Freshness:  BindingUnavailable,
	}
	info := d.DiscoverContext(ctx, agentType, workDir, panePID)
	if info == nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			observation.FailureCode = "deadline_exceeded"
		case context.Canceled:
			observation.FailureCode = "cancelled"
		default:
			observation.FailureCode = "not_found"
		}
		return observation
	}

	observation.AgentType = info.AgentType
	observation.SessionID = info.SessionID
	observation.Provider = info.Provider
	observation.Source = info.Source
	observation.SourceUpdatedAt = info.UpdatedAt
	observation.SourcePath = info.SourcePath
	observation.Freshness = BindingFresh
	switch info.Source {
	case DiscoverySourceProcessTree:
		observation.Confidence = 0.99
	case DiscoverySourceCASR:
		observation.Confidence = 0.9
	case DiscoverySourceNativeStore:
		observation.Confidence = 0.75
	default:
		observation.Confidence = 0.5
	}
	if info.Source != DiscoverySourceProcessTree &&
		(info.UpdatedAt.IsZero() || info.UpdatedAt.After(observedAt) || observedAt.Sub(info.UpdatedAt) >= bindingFreshnessHorizon) {
		observation.Freshness = BindingStale
		observation.Confidence *= 0.5
	}
	return observation
}

func sessionClaimKey(provider, sessionID string) string {
	return provider + "\x00" + strings.TrimSpace(sessionID)
}

func (d *Discoverer) claim(info *Info) {
	if info == nil || strings.TrimSpace(info.SessionID) == "" {
		return
	}
	if d.claimedSessions == nil {
		d.claimedSessions = make(map[string]bool)
	}
	d.claimedSessions[sessionClaimKey(info.Provider, info.SessionID)] = true
}

func (d *Discoverer) isClaimed(provider, sessionID string) bool {
	return d.claimedSessions[sessionClaimKey(provider, sessionID)]
}

func (d *Discoverer) discoverCASR(ctx context.Context, agentType, provider, workDir string, processStartedAt int64) *Info {
	cleanWorkDir := filepath.Clean(workDir)
	queryKey := provider + "\x00" + cleanWorkDir
	if d.casrItemsCache == nil {
		d.casrItemsCache = make(map[string][]casrListItem)
	}
	if d.casrChecked == nil {
		d.casrChecked = make(map[string]bool)
	}
	if !d.casrChecked[queryKey] {
		d.casrChecked[queryKey] = true
		lookup := d.lookPath
		if lookup == nil {
			lookup = exec.LookPath
		}
		binary, err := lookup("casr")
		if err == nil {
			runner := d.runCommand
			if runner == nil {
				runner = runDiscoveryCommand
			}
			commandCtx, cancel := context.WithTimeout(ctx, casrDiscoveryTimeout)
			output, runErr := runner(commandCtx, binary,
				"list",
				"--workspace", cleanWorkDir,
				"--provider", provider,
				"--sort", "date",
				"--limit", strconv.Itoa(casrDiscoveryLimit),
				"--json",
			)
			cancel()
			if runErr != nil && ctx.Err() != nil {
				delete(d.casrChecked, queryKey)
				return nil
			}
			if runErr == nil {
				var envelope casrListEnvelope
				if json.Unmarshal(output, &envelope) == nil {
					d.casrItemsCache[queryKey] = envelope.Items
				}
			}
		}
	}
	if ctx.Err() != nil {
		delete(d.casrChecked, queryKey)
		return nil
	}

	var best *casrListItem
	var bestActivity int64
	var bestDelta int64
	for _, item := range d.casrItemsCache[queryKey] {
		if ctx.Err() != nil {
			delete(d.casrChecked, queryKey)
			return nil
		}
		if strings.TrimSpace(item.Provider) != provider || strings.TrimSpace(item.SessionID) == "" {
			continue
		}
		if item.Workspace == "" || !pathWithin(cleanWorkDir, item.Workspace) {
			continue
		}
		if provider == "codex" && codexSessionIsSubagent(item.Path) {
			continue
		}
		if d.isClaimed(provider, item.SessionID) {
			continue
		}
		if processStartedAt > 0 {
			delta, ok := processStartDelta(processStartedAt, item.StartedAt)
			if !ok || (best != nil && delta >= bestDelta) {
				continue
			}
			itemCopy := item
			best = &itemCopy
			bestDelta = delta
			continue
		}
		activity := item.LastActiveAt
		if activity == 0 {
			activity = item.StartedAt
		}
		if best != nil && activity <= bestActivity {
			continue
		}
		itemCopy := item
		best = &itemCopy
		bestActivity = activity
	}
	if best == nil {
		return nil
	}
	info := &Info{
		AgentType:  canonicalAgentType(agentType, provider),
		SessionID:  strings.TrimSpace(best.SessionID),
		Provider:   provider,
		SourcePath: best.Path,
		Source:     DiscoverySourceCASR,
	}
	activity := best.LastActiveAt
	if activity == 0 {
		activity = best.StartedAt
	}
	if activity > 0 {
		info.UpdatedAt = time.UnixMilli(activity)
	}
	return info
}

func processStartDelta(processStartedAt, sessionStartedAt int64) (delta int64, ok bool) {
	if processStartedAt <= 0 || sessionStartedAt <= 0 {
		return 0, false
	}
	if sessionStartedAt < processStartedAt {
		return 0, false
	}
	delta = sessionStartedAt - processStartedAt
	return delta, delta <= processSessionWindow.Milliseconds()
}

func cloneInfo(info *Info) *Info {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

func canonicalAgentType(agentType, provider string) string {
	switch provider {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	case "antigravity":
		return "agy"
	default:
		return strings.ToLower(strings.TrimSpace(agentType))
	}
}

// pathWithin reports whether candidate is root or a real descendant of root.
// filepath.Rel avoids the raw-prefix bug where /repo also matches /repo-other.
func pathWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func providerCommandMarkers(provider string) []string {
	switch provider {
	case "claude", "codex", "gemini":
		return []string{provider}
	case "antigravity":
		return []string{"agy", "antigravity"}
	default:
		return nil
	}
}

func argvMatchesProvider(provider string, argv []string) bool {
	markers := providerCommandMarkers(provider)
	limit := len(argv)
	if limit > 4 {
		limit = 4
	}
	for _, arg := range argv[:limit] {
		lower := strings.ToLower(arg)
		for _, marker := range markers {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func agentProcessStartMillis(panePID int, provider string) int64 {
	if panePID <= 0 || len(providerCommandMarkers(provider)) == 0 {
		return 0
	}
	type processNode struct {
		pid   int
		depth int
	}
	queue := []processNode{{pid: panePID}}
	seen := map[int]bool{panePID: true}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if argvMatchesProvider(provider, processutil.GetCmdline(node.pid)) {
			proc, err := gopsprocess.NewProcess(int32(node.pid))
			if err == nil {
				if startedAt, err := proc.CreateTime(); err == nil && startedAt > 0 {
					return startedAt
				}
			}
		}
		if node.depth >= processTreeMaxDepth {
			continue
		}
		for _, child := range processutil.GetChildPIDs(node.pid, processTreeFanout) {
			if child <= 0 || seen[child] || len(seen) >= processTreeMaxNodes {
				continue
			}
			seen[child] = true
			queue = append(queue, processNode{pid: child, depth: node.depth + 1})
		}
	}
	return 0
}

// discoverProcessSession identifies a provider session owned by the pane's
// process tree. Explicit resume argv wins; otherwise open session files are
// inspected in numeric fd order, with Codex subagent thread files excluded.
// Linux uses /proc, macOS uses lsof, and other platforms fall through to
// CASR/native discovery.
func discoverProcessSession(agentType, provider, _ string, panePID int) *Info {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || panePID <= 0 {
		return nil
	}

	type processNode struct {
		pid   int
		depth int
	}
	queue := []processNode{{pid: panePID}}
	seen := map[int]bool{panePID: true}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if info := processNodeSession(agentType, provider, processutil.GetCmdline(node.pid), func() []string {
			return openSessionFiles(node.pid, provider)
		}); info != nil {
			return info
		}
		if node.depth >= processTreeMaxDepth {
			continue
		}
		for _, child := range processutil.GetChildPIDs(node.pid, processTreeFanout) {
			if child <= 0 || seen[child] || len(seen) >= processTreeMaxNodes {
				continue
			}
			seen[child] = true
			queue = append(queue, processNode{pid: child, depth: node.depth + 1})
		}
	}
	return nil
}

func processNodeSession(agentType, provider string, argv []string, openFiles func() []string) *Info {
	if !argvMatchesProvider(provider, argv) {
		return nil
	}
	if id := resumedSessionID(provider, argv); id != "" {
		return &Info{
			AgentType: canonicalAgentType(agentType, provider),
			SessionID: id,
			Provider:  provider,
		}
	}
	if openFiles == nil {
		return nil
	}
	for _, path := range openFiles() {
		if provider == "codex" && codexSessionIsSubagent(path) {
			continue
		}
		if info := infoFromSessionFile(agentType, provider, path); info != nil {
			return info
		}
	}
	return nil
}

func resumedSessionID(provider string, argv []string) string {
	var flag string
	switch provider {
	case "claude", "gemini":
		flag = "--resume"
	case "codex":
		flag = "resume"
	case "antigravity":
		flag = "--conversation"
	default:
		return ""
	}
	if !argvMatchesProvider(provider, argv) {
		return ""
	}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] != flag {
			continue
		}
		if provider == "codex" {
			for _, candidate := range argv[i+1:] {
				candidate = strings.TrimSpace(candidate)
				if codexUUIDAtEndPattern.FindString(candidate) == candidate {
					return candidate
				}
			}
			return ""
		}
		candidate := strings.TrimSpace(argv[i+1])
		if candidate == "" || strings.HasPrefix(candidate, "-") {
			return ""
		}
		if provider == "gemini" {
			if strings.EqualFold(candidate, "latest") {
				return ""
			}
			if _, err := strconv.Atoi(candidate); err == nil {
				return ""
			}
		}
		return candidate
	}
	return ""
}

func openSessionFiles(pid int, provider string) []string {
	openFiles := processOpenFiles(pid)
	sort.Slice(openFiles, func(i, j int) bool { return openFiles[i].fd < openFiles[j].fd })

	paths := make([]string, 0, 1)
	for _, openFile := range openFiles {
		if !filepath.IsAbs(openFile.path) {
			continue
		}
		if isProviderSessionFile(provider, openFile.path) {
			paths = append(paths, openFile.path)
		}
	}
	return paths
}

type processOpenFile struct {
	fd   int
	path string
}

func processOpenFiles(pid int) []processOpenFile {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), processFilesTimeout)
		defer cancel()
		output, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Ffn").Output()
		if err != nil {
			return nil
		}
		return parseLsofOpenFiles(string(output))
	}
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil
	}
	files := make([]processOpenFile, 0, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err != nil {
			continue
		}
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		files = append(files, processOpenFile{fd: fd, path: target})
	}
	return files
}

func parseLsofOpenFiles(output string) []processOpenFile {
	files := make([]processOpenFile, 0)
	currentFD := -1
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'f':
			currentFD = -1
			end := 1
			for end < len(line) && line[end] >= '0' && line[end] <= '9' {
				end++
			}
			if end > 1 {
				currentFD, _ = strconv.Atoi(line[1:end])
			}
		case 'n':
			if currentFD >= 0 {
				files = append(files, processOpenFile{fd: currentFD, path: line[1:]})
			}
		}
	}
	return files
}

func isProviderSessionFile(provider, path string) bool {
	base := filepath.Base(path)
	switch provider {
	case "claude":
		return strings.HasSuffix(base, ".jsonl") &&
			filepath.Base(filepath.Dir(filepath.Dir(path))) == "projects"
	case "codex":
		return strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl") &&
			hasAncestor(filepath.Dir(path), "sessions", 4)
	case "gemini":
		return filepath.Base(filepath.Dir(path)) == "chats" &&
			strings.HasPrefix(base, "session-") &&
			(strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".jsonl"))
	case "antigravity":
		return strings.HasSuffix(base, ".db") && filepath.Base(filepath.Dir(path)) == "conversations"
	default:
		return false
	}
}

func hasAncestor(path, name string, maxDepth int) bool {
	for depth := 0; depth <= maxDepth; depth++ {
		if filepath.Base(path) == name {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return false
}

func infoFromSessionFile(agentType, provider, path string) *Info {
	var id string
	switch provider {
	case "claude":
		id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	case "codex":
		id = codexSessionID(path)
	case "gemini":
		id = geminiSessionID(path)
	case "antigravity":
		id = strings.TrimSuffix(filepath.Base(path), ".db")
	}
	if id == "" {
		return nil
	}
	info := &Info{
		AgentType:  canonicalAgentType(agentType, provider),
		SessionID:  id,
		Provider:   provider,
		SourcePath: path,
	}
	if stat, err := os.Stat(path); err == nil {
		info.UpdatedAt = stat.ModTime()
	}
	return info
}

// encodeClaudeProjectDir reproduces Claude Code's project-directory encoding:
// every non-alphanumeric character in the absolute cwd becomes '-', e.g.
//
//	/data/projects/ntm                -> -data-projects-ntm
//	/data/projects/mcp_agent_mail     -> -data-projects-mcp-agent-mail
//	/data/projects/jeffreys-skills.md -> -data-projects-jeffreys-skills-md
//
// This must match Claude Code's own encoder exactly (`replace(/[^a-zA-Z0-9]/g,
// "-")`). An earlier revision preserved '_', which made discoverClaude look in a
// non-existent (or, worse, a different project's hyphen-variant) directory and
// resume the wrong session — verified against the real `~/.claude/projects/`
// session dirs, where every underscore cwd maps to a hyphenated directory.
func encodeClaudeProjectDir(workDir string) string {
	cleaned := filepath.Clean(workDir)
	return claudeNonAlnumPattern.ReplaceAllString(cleaned, "-")
}

func discoverClaudeContext(ctx context.Context, home, workDir string) *Info {
	if ctx.Err() != nil {
		return nil
	}
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	path, mod := newestFileWithExtContext(ctx, projDir, ".jsonl")
	if path == "" {
		return nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cc",
		SessionID:  id,
		Provider:   "claude",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

func discoverCodexContext(ctx context.Context, home, workDir string, claimed map[string]bool) *Info {
	if ctx.Err() != nil {
		return nil
	}
	root := filepath.Join(home, ".codex", "sessions")
	path, mod := newestCodexRolloutForCwdContext(ctx, root, filepath.Clean(workDir), claimed)
	if path == "" {
		return nil
	}
	id := codexSessionID(path)
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cod",
		SessionID:  id,
		Provider:   "codex",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

func discoverGeminiContext(ctx context.Context, home, workDir string, processStartedAt int64, claimed map[string]bool) *Info {
	if ctx.Err() != nil {
		return nil
	}
	root := filepath.Join(home, ".gemini", "tmp")
	path, mod := newestGeminiSessionForCwdContext(ctx, root, filepath.Clean(workDir), processStartedAt, claimed)
	if path == "" {
		return nil
	}
	id := geminiSessionID(path)
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "gmi",
		SessionID:  id,
		Provider:   "gemini",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

func discoverAntigravityContext(ctx context.Context, home, workDir string) *Info {
	if ctx.Err() != nil {
		return nil
	}
	root := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	path, mod := newestAntigravityConversationForCwdContext(ctx, root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".db")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "agy",
		SessionID:  id,
		Provider:   "antigravity",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

func newestFileWithExtContext(ctx context.Context, dir, ext string) (string, time.Time) {
	if ctx.Err() != nil {
		return "", time.Time{}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if ctx.Err() != nil {
			return "", time.Time{}
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestMod) {
			bestPath = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestMod
}

func newestCodexRolloutForCwdContext(ctx context.Context, root, workDir string, claimed map[string]bool) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			return nil
		}
		meta, ok := readCodexSessionMeta(path)
		if !ok || codexMetaIsSubagent(meta) {
			return nil
		}
		if claimed[sessionClaimKey("codex", codexMetaID(meta))] {
			return nil
		}
		if workspace := codexMetaWorkspace(meta); workspace == "" || !pathWithin(workDir, workspace) {
			return nil
		}
		bestPath = path
		bestMod = info.ModTime()
		return nil
	})
	if ctx.Err() != nil {
		return "", time.Time{}
	}
	return bestPath, bestMod
}

type codexSessionMeta struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Payload struct {
		ID           string `json:"id"`
		Cwd          string `json:"cwd"`
		ThreadSource string `json:"thread_source"`
	} `json:"payload"`
}

func readCodexSessionMeta(path string) (codexSessionMeta, bool) {
	file, err := os.Open(path)
	if err != nil {
		return codexSessionMeta{}, false
	}
	defer file.Close()
	var meta codexSessionMeta
	if err := json.NewDecoder(file).Decode(&meta); err != nil || meta.Type != "session_meta" {
		return codexSessionMeta{}, false
	}
	return meta, true
}

func codexMetaWorkspace(meta codexSessionMeta) string {
	if meta.Payload.Cwd != "" {
		return filepath.Clean(meta.Payload.Cwd)
	}
	if meta.Cwd != "" {
		return filepath.Clean(meta.Cwd)
	}
	return ""
}

func codexSessionIsSubagent(path string) bool {
	meta, ok := readCodexSessionMeta(path)
	return ok && codexMetaIsSubagent(meta)
}

func codexMetaIsSubagent(meta codexSessionMeta) bool {
	return strings.EqualFold(strings.TrimSpace(meta.Payload.ThreadSource), "subagent")
}

// codexSessionID reads the provider-native UUID from session_meta. Modern
// rollout filenames also contain a timestamp, so stripping only "rollout-"
// produces an id that `codex resume` rejects.
func codexSessionID(path string) string {
	if meta, ok := readCodexSessionMeta(path); ok {
		if id := codexMetaID(meta); id != "" {
			return id
		}
	}
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if id := codexUUIDAtEndPattern.FindString(base); id != "" {
		return id
	}
	return strings.TrimPrefix(base, "rollout-")
}

func codexMetaID(meta codexSessionMeta) string {
	if id := strings.TrimSpace(meta.Payload.ID); id != "" {
		return id
	}
	return strings.TrimSpace(meta.ID)
}

func geminiProjectHash(workDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(workDir)))
	return fmt.Sprintf("%x", sum)
}

type geminiSessionHeader struct {
	SessionID string `json:"sessionId"`
	StartTime string `json:"startTime"`
}

func readGeminiSessionHeader(path string) (geminiSessionHeader, bool) {
	file, err := os.Open(path)
	if err != nil {
		return geminiSessionHeader{}, false
	}
	defer file.Close()
	var header geminiSessionHeader
	if json.NewDecoder(file).Decode(&header) != nil {
		return geminiSessionHeader{}, false
	}
	return header, true
}

func geminiSessionID(path string) string {
	if header, ok := readGeminiSessionHeader(path); ok && strings.TrimSpace(header.SessionID) != "" {
		return strings.TrimSpace(header.SessionID)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.TrimPrefix(base, "session-")
}

func geminiSessionStartedAtMillis(path string) int64 {
	header, ok := readGeminiSessionHeader(path)
	if !ok || strings.TrimSpace(header.StartTime) == "" {
		return 0
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(header.StartTime))
	if err != nil {
		return 0
	}
	return startedAt.UnixMilli()
}

func geminiWorkspaceChatDirsContext(ctx context.Context, root, workDir string) []string {
	if ctx.Err() != nil {
		return nil
	}
	var registry struct {
		Projects map[string]string `json:"projects"`
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(root), "projects.json")); err == nil {
		if json.Unmarshal(data, &registry) != nil {
			registry.Projects = nil
		}
	}
	if ctx.Err() != nil {
		return nil
	}

	dirs := make([]string, 0, 2)
	if slug := strings.TrimSpace(registry.Projects[filepath.Clean(workDir)]); slug != "" {
		candidate := filepath.Join(root, slug, "chats")
		if pathWithin(root, candidate) {
			dirs = append(dirs, candidate)
		}
	}
	legacy := filepath.Join(root, geminiProjectHash(workDir), "chats")
	if len(dirs) == 0 || filepath.Clean(dirs[0]) != filepath.Clean(legacy) {
		dirs = append(dirs, legacy)
	}
	return dirs
}

func newestGeminiSessionForCwdContext(ctx context.Context, root, workDir string, processStartedAt int64, claimed map[string]bool) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	var bestDelta int64
	for _, chatsDir := range geminiWorkspaceChatDirsContext(ctx, root, workDir) {
		if ctx.Err() != nil {
			return "", time.Time{}
		}
		entries, err := os.ReadDir(chatsDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return "", time.Time{}
			}
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "session-") ||
				(!strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl")) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			path := filepath.Join(chatsDir, name)
			if claimed[sessionClaimKey("gemini", geminiSessionID(path))] {
				continue
			}
			if processStartedAt > 0 {
				delta, ok := processStartDelta(processStartedAt, geminiSessionStartedAtMillis(path))
				if !ok || (bestPath != "" && delta >= bestDelta) {
					continue
				}
				bestPath = path
				bestMod = info.ModTime()
				bestDelta = delta
				continue
			}
			if bestPath != "" && !info.ModTime().After(bestMod) {
				continue
			}
			bestPath = path
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestMod
}

func newestAntigravityConversationForCwdContext(ctx context.Context, root, workDir string) (string, time.Time) {
	if ctx.Err() != nil {
		return "", time.Time{}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if ctx.Err() != nil {
			return "", time.Time{}
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if !fileContainsCwdContext(ctx, path, workDir) {
			continue
		}
		bestPath = path
		bestMod = info.ModTime()
	}
	return bestPath, bestMod
}

func fileContainsCwdContext(ctx context.Context, path, workDir string) bool {
	if ctx.Err() != nil || workDir == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	needle := []byte(workDir)
	buffer := make([]byte, 64*1024)
	carry := make([]byte, 0, len(needle)-1)
	for {
		if ctx.Err() != nil {
			return false
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			window := make([]byte, 0, len(carry)+read)
			window = append(window, carry...)
			window = append(window, buffer[:read]...)
			if bytes.Contains(window, needle) {
				return true
			}
			keep := len(needle) - 1
			if keep > len(window) {
				keep = len(window)
			}
			carry = append(carry[:0], window[len(window)-keep:]...)
		}
		if readErr != nil {
			return false
		}
	}
}
