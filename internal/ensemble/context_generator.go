package ensemble

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tokenpkg "github.com/Dicklesworthstone/ntm/internal/tokens"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	maxLineCountSize   = 1 << 20 // 1MB max per file for line counting
	maxIssuesLineBytes = 4 << 20 // 4MB per JSONL line
)

// ContextPackGenerator builds a ContextPack for ensemble runs.
type ContextPackGenerator struct {
	ProjectDir string
	Logger     *slog.Logger
	Cache      *ContextPackCache
	MaxTokens  int
}

// NewContextPackGenerator returns a generator instance.
func NewContextPackGenerator(projectDir string, cache *ContextPackCache, logger *slog.Logger) *ContextPackGenerator {
	return &ContextPackGenerator{
		ProjectDir: projectDir,
		Logger:     logger,
		Cache:      cache,
	}
}

// Generate builds a context pack and uses cache when enabled.
func (g *ContextPackGenerator) Generate(question string, modeKey string, cacheCfg CacheConfig) (*ContextPack, error) {
	projectRoot := g.resolveProjectRoot()
	fingerprint := g.buildFingerprint(projectRoot, question, modeKey)
	cacheKey := fingerprint.cacheKey()

	if cacheCfg.Enabled && g.Cache != nil {
		if pack, ok := g.Cache.Get(cacheKey); ok {
			g.loggerSafe().Info("context pack cache hit",
				"key", cacheKey,
				"project", projectRoot,
			)
			if g.MaxTokens > 0 {
				return CapContextPackTokens(pack, g.MaxTokens), nil
			}
			return pack, nil
		}
		g.loggerSafe().Info("context pack cache miss",
			"key", cacheKey,
			"project", projectRoot,
		)
	}

	brief := g.buildProjectBrief(projectRoot)
	userCtx := &UserContext{
		ProblemStatement: strings.TrimSpace(question),
	}

	pack := &ContextPack{
		GeneratedAt:  time.Now().UTC(),
		ProjectBrief: brief,
		UserContext:  userCtx,
	}

	reasons := thinContextReasons(pack)
	if len(reasons) > 0 {
		pack.Questions = SelectQuestions(pack)
		g.loggerSafe().Info("context pack thin-context questions",
			"count", len(pack.Questions),
			"reasons", reasons,
			"project", projectRoot,
		)
	}

	pack.TokenEstimate = tokenpkg.EstimateTokensWithLanguageHint(formatContextPack(pack), tokenpkg.ContentMarkdown)
	pack.Hash = hashContextPack(pack)
	if g.MaxTokens > 0 {
		pack = CapContextPackTokens(pack, g.MaxTokens)
	}

	if cacheCfg.Enabled && g.Cache != nil {
		if err := g.Cache.Put(cacheKey, pack, fingerprint); err != nil {
			g.loggerSafe().Warn("context pack cache write failed",
				"key", cacheKey,
				"error", err,
			)
		}
	}

	g.loggerSafe().Info("context pack generated",
		"project", projectRoot,
		"languages", brief.Languages,
		"frameworks", brief.Frameworks,
		"open_issues", brief.OpenIssues,
	)

	return pack, nil
}

func (g *ContextPackGenerator) resolveProjectRoot() string {
	if g.ProjectDir == "" {
		return "."
	}
	root, err := findProjectRoot(g.ProjectDir)
	if err == nil && root != "" {
		return root
	}
	return g.ProjectDir
}

// findProjectRoot returns the git repository root for the given directory.
// This is inlined here to avoid import cycles with the git package.
func findProjectRoot(startDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (g *ContextPackGenerator) buildFingerprint(projectRoot, question, modeKey string) ContextFingerprint {
	head, status := gitState(projectRoot)
	readme := findReadme(projectRoot)
	return ContextFingerprint{
		ProjectRoot:  projectRoot,
		GitHead:      head,
		GitStatus:    status,
		ReadmeHash:   hashFileContents(readme),
		QuestionHash: hashString(question),
		ModeKey:      modeKey,
	}
}

func (g *ContextPackGenerator) loggerSafe() *slog.Logger {
	if g != nil && g.Logger != nil {
		return g.Logger
	}
	return slog.Default()
}

func (g *ContextPackGenerator) buildProjectBrief(projectRoot string) *ProjectBrief {
	name, description := readProjectOverview(projectRoot)
	structure, languages, frameworks := scanProjectStructure(projectRoot)
	commits := recentCommits(projectRoot, 8)
	openIssues := countOpenIssues(projectRoot)

	sort.Strings(languages)
	sort.Strings(frameworks)
	sort.Strings(structure.EntryPoints)
	sort.Strings(structure.CorePackages)

	return &ProjectBrief{
		Name:           name,
		Description:    description,
		Languages:      languages,
		Frameworks:     frameworks,
		Structure:      structure,
		RecentActivity: commits,
		OpenIssues:     openIssues,
	}
}

func readProjectOverview(root string) (string, string) {
	readme := findReadme(root)
	if readme == "" {
		return filepath.Base(root), ""
	}

	data, err := os.ReadFile(readme)
	if err != nil {
		return filepath.Base(root), ""
	}

	lines := strings.Split(string(data), "\n")
	name := ""
	desc := ""
	foundHeading := false
	var descLines []string
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			if foundHeading && len(descLines) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") && name == "" {
			name = strings.TrimSpace(strings.TrimLeft(line, "#"))
			foundHeading = true
			continue
		}
		if name == "" {
			name = line
			foundHeading = true
			continue
		}
		if foundHeading && desc == "" {
			descLines = append(descLines, line)
		}
	}
	if name == "" {
		name = filepath.Base(root)
	}
	if len(descLines) > 0 {
		desc = strings.Join(descLines, " ")
	}
	return name, desc
}

func findReadme(root string) string {
	candidates := []string{
		"README.md",
		"README.txt",
		"README",
	}
	for _, name := range candidates {
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

type projectScan struct {
	totalFiles  int
	totalLines  int
	entryPoints map[string]bool
	corePkgs    map[string]bool
	languages   map[string]bool
	frameworks  map[string]bool
}

func scanProjectStructure(root string) (*ProjectStructure, []string, []string) {
	scan := &projectScan{
		entryPoints: make(map[string]bool),
		corePkgs:    make(map[string]bool),
		languages:   make(map[string]bool),
		frameworks:  make(map[string]bool),
	}

	skipDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		".cache":       true,
		".beads":       true,
		".idea":        true,
		".vscode":      true,
		"dist":         true,
		"build":        true,
		"out":          true,
		".venv":        true,
		"tmp":          true,
	}

	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		scan.totalFiles++
		rel, _ := filepath.Rel(root, path)
		scan.detectEntryPoint(rel)
		scan.detectLanguage(path)
		scan.detectFrameworks(path)
		scan.countLines(path)
		return nil
	})

	scan.detectCorePackages(root)

	structure := &ProjectStructure{
		EntryPoints:  mapKeys(scan.entryPoints),
		CorePackages: mapKeys(scan.corePkgs),
		TestCoverage: 0,
		TotalFiles:   scan.totalFiles,
		TotalLines:   scan.totalLines,
	}

	return structure, mapKeys(scan.languages), mapKeys(scan.frameworks)
}

func (p *projectScan) detectEntryPoint(rel string) {
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	switch base {
	case "main.go", "main.py", "main.rs", "main.ts", "main.js", "index.js", "index.ts", "app.js", "app.ts", "server.js", "server.ts":
		p.entryPoints[rel] = true
		return
	}
	if strings.HasPrefix(rel, "cmd/") && strings.HasSuffix(rel, "main.go") {
		p.entryPoints[rel] = true
	}
}

func (p *projectScan) detectLanguage(path string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		p.languages["Go"] = true
	case ".js", ".jsx":
		p.languages["JavaScript"] = true
	case ".ts", ".tsx":
		p.languages["TypeScript"] = true
	case ".py":
		p.languages["Python"] = true
	case ".rs":
		p.languages["Rust"] = true
	case ".java":
		p.languages["Java"] = true
	case ".rb":
		p.languages["Ruby"] = true
	case ".c", ".h":
		p.languages["C"] = true
	case ".cpp", ".cxx", ".cc", ".hpp":
		p.languages["C++"] = true
	case ".sh":
		p.languages["Shell"] = true
	case ".lua":
		p.languages["Lua"] = true
	case ".php":
		p.languages["PHP"] = true
	}
}

func (p *projectScan) detectFrameworks(path string) {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "go.mod":
		for _, fw := range frameworksFromGoMod(path) {
			p.frameworks[fw] = true
		}
	case "package.json":
		for _, fw := range frameworksFromPackageJSON(path) {
			p.frameworks[fw] = true
		}
	case "cargo.toml":
		for _, fw := range frameworksFromCargo(path) {
			p.frameworks[fw] = true
		}
	case "pyproject.toml", "requirements.txt":
		for _, fw := range frameworksFromPython(path) {
			p.frameworks[fw] = true
		}
	}
}

func (p *projectScan) countLines(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if info.Size() > maxLineCountSize {
		return
	}
	if !isCountableFile(path) {
		return
	}

	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if len(line) > 0 {
					p.totalLines++
				}
			}
			break
		}
		p.totalLines++
	}
}

func (p *projectScan) detectCorePackages(root string) {
	candidates := []string{"internal", "pkg", "src", "app"}
	for _, dir := range candidates {
		base := filepath.Join(root, dir)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				p.corePkgs[filepath.ToSlash(filepath.Join(dir, entry.Name()))] = true
			}
		}
	}
}

func isCountableFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".ts", ".tsx", ".js", ".jsx",
		".py", ".rs", ".rb", ".java", ".c", ".cc", ".cpp", ".h", ".hpp", ".sh":
		return true
	default:
		return false
	}
}

func frameworksFromGoMod(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)
	matches := map[string]string{
		"github.com/gin-gonic/gin":           "Gin",
		"github.com/labstack/echo":           "Echo",
		"github.com/go-chi/chi":              "Chi",
		"github.com/spf13/cobra":             "Cobra",
		"github.com/spf13/viper":             "Viper",
		"github.com/charmbracelet/bubbletea": "Bubble Tea",
		"github.com/charmbracelet/lipgloss":  "Lipgloss",
		"gorm.io/gorm":                       "GORM",
		"github.com/jmoiron/sqlx":            "sqlx",
		"github.com/urfave/cli":              "urfave/cli",
	}
	var frameworks []string
	for needle, name := range matches {
		if strings.Contains(content, needle) {
			frameworks = append(frameworks, name)
		}
	}
	return frameworks
}

func frameworksFromPackageJSON(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	type pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	var p pkg
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	deps := make(map[string]bool)
	for name := range p.Dependencies {
		deps[name] = true
	}
	for name := range p.DevDependencies {
		deps[name] = true
	}
	matches := map[string]string{
		"react":   "React",
		"next":    "Next.js",
		"express": "Express",
		"koa":     "Koa",
		"vite":    "Vite",
	}
	var frameworks []string
	for needle, name := range matches {
		if deps[needle] {
			frameworks = append(frameworks, name)
		}
	}
	return frameworks
}

func frameworksFromCargo(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)
	matches := map[string]string{
		"tokio": "Tokio",
		"axum":  "Axum",
		"actix": "Actix",
		"clap":  "Clap",
	}
	var frameworks []string
	for needle, name := range matches {
		if strings.Contains(content, needle) {
			frameworks = append(frameworks, name)
		}
	}
	return frameworks
}

func frameworksFromPython(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := strings.ToLower(string(data))
	matches := map[string]string{
		"django":  "Django",
		"flask":   "Flask",
		"fastapi": "FastAPI",
		"typer":   "Typer",
	}
	var frameworks []string
	for needle, name := range matches {
		if strings.Contains(content, needle) {
			frameworks = append(frameworks, name)
		}
	}
	return frameworks
}

func recentCommits(root string, limit int) []CommitSummary {
	if limit <= 0 {
		return nil
	}
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", limit), "--pretty=format:%H|%an|%s|%cI")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	commits := make([]CommitSummary, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		date, err := time.Parse(time.RFC3339, parts[3])
		if err != nil {
			date = time.Time{}
		}
		commits = append(commits, CommitSummary{
			Hash:    parts[0],
			Author:  parts[1],
			Summary: parts[2],
			Date:    date,
		})
	}
	return commits
}

func gitState(root string) (string, string) {
	head := ""
	statusHash := ""

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		head = strings.TrimSpace(string(out))
	}

	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		statusHash = hashString(string(out))
	}

	return head, statusHash
}

func countOpenIssues(root string) int {
	path := filepath.Join(root, ".beads", "issues.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 1024*64)
	scanner.Buffer(buf, maxIssuesLineBytes)

	openCount := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if strings.ToLower(entry.Status) != "closed" {
			openCount++
		}
	}
	return openCount
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func hashContextPack(pack *ContextPack) string {
	if pack == nil {
		return ""
	}
	clone := *pack
	clone.Hash = ""
	clone.GeneratedAt = time.Time{}
	data, err := json.Marshal(clone)
	if err != nil {
		// Fallback: hash the raw content to avoid collisions
		fallback := fmt.Sprintf("%s:%d", pack.Hash, pack.TokenEstimate)
		sum := sha256.Sum256([]byte(fallback))
		return hex.EncodeToString(sum[:])[:16]
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// CapContextPackTokens returns a ContextPack trimmed to stay within maxTokens when maxTokens > 0.
func CapContextPackTokens(pack *ContextPack, maxTokens int) *ContextPack {
	if pack == nil || maxTokens <= 0 {
		return pack
	}
	estimate := func(cp *ContextPack) int {
		return tokenpkg.EstimateTokensWithLanguageHint(formatContextPack(cp), tokenpkg.ContentMarkdown)
	}
	if pack.TokenEstimate <= 0 {
		pack.TokenEstimate = estimate(pack)
	}
	if pack.TokenEstimate <= maxTokens {
		return pack
	}

	capped := *pack
	if pack.Questions != nil {
		capped.Questions = append([]TargetedQuestion(nil), pack.Questions...)
	}
	if pack.ProjectBrief != nil {
		b := *pack.ProjectBrief
		b.Languages = append([]string(nil), pack.ProjectBrief.Languages...)
		b.Frameworks = append([]string(nil), pack.ProjectBrief.Frameworks...)
		b.RecentActivity = append([]CommitSummary(nil), pack.ProjectBrief.RecentActivity...)
		if pack.ProjectBrief.Structure != nil {
			s := *pack.ProjectBrief.Structure
			s.EntryPoints = append([]string(nil), pack.ProjectBrief.Structure.EntryPoints...)
			s.CorePackages = append([]string(nil), pack.ProjectBrief.Structure.CorePackages...)
			b.Structure = &s
		}
		capped.ProjectBrief = &b
	}
	if pack.UserContext != nil {
		u := *pack.UserContext
		u.FocusAreas = append([]string(nil), pack.UserContext.FocusAreas...)
		u.Constraints = append([]string(nil), pack.UserContext.Constraints...)
		u.Stakeholders = append([]string(nil), pack.UserContext.Stakeholders...)
		u.Decisions = append([]string(nil), pack.UserContext.Decisions...)
		u.History = append([]string(nil), pack.UserContext.History...)
		u.SuccessCriteria = append([]string(nil), pack.UserContext.SuccessCriteria...)
		capped.UserContext = &u
	}

	recompute := func() bool {
		capped.TokenEstimate = estimate(&capped)
		return capped.TokenEstimate <= maxTokens
	}

	for len(capped.Questions) > 0 && !recompute() {
		capped.Questions = capped.Questions[:len(capped.Questions)-1]
	}
	if capped.ProjectBrief != nil {
		for len(capped.ProjectBrief.RecentActivity) > 0 && !recompute() {
			capped.ProjectBrief.RecentActivity = capped.ProjectBrief.RecentActivity[:len(capped.ProjectBrief.RecentActivity)-1]
		}
		for len(capped.ProjectBrief.Description) > 0 && !recompute() {
			nextLen := len(capped.ProjectBrief.Description) / 2
			if nextLen < 24 {
				capped.ProjectBrief.Description = ""
			} else {
				capped.ProjectBrief.Description = util.SafeSlice(capped.ProjectBrief.Description, nextLen)
			}
		}
		if capped.ProjectBrief.Structure != nil {
			for len(capped.ProjectBrief.Structure.CorePackages) > 0 && !recompute() {
				capped.ProjectBrief.Structure.CorePackages = capped.ProjectBrief.Structure.CorePackages[:len(capped.ProjectBrief.Structure.CorePackages)-1]
			}
			for len(capped.ProjectBrief.Structure.EntryPoints) > 0 && !recompute() {
				capped.ProjectBrief.Structure.EntryPoints = capped.ProjectBrief.Structure.EntryPoints[:len(capped.ProjectBrief.Structure.EntryPoints)-1]
			}
			if !recompute() {
				capped.ProjectBrief.Structure = nil
			}
		}
	}
	if capped.UserContext != nil {
		trimSlice := func(items *[]string) {
			for len(*items) > 0 && !recompute() {
				*items = (*items)[:len(*items)-1]
			}
		}
		trimSlice(&capped.UserContext.History)
		trimSlice(&capped.UserContext.Decisions)
		trimSlice(&capped.UserContext.Stakeholders)
		trimSlice(&capped.UserContext.Constraints)
		trimSlice(&capped.UserContext.FocusAreas)
		trimSlice(&capped.UserContext.SuccessCriteria)
		for len(capped.UserContext.ProblemStatement) > 16 && !recompute() {
			nextLen := len(capped.UserContext.ProblemStatement) / 2
			if nextLen < 16 {
				nextLen = 16
			}
			capped.UserContext.ProblemStatement = util.SafeSlice(capped.UserContext.ProblemStatement, nextLen)
			if nextLen == 16 {
				break
			}
		}
	}
	if !recompute() && capped.ProjectBrief != nil {
		capped.ProjectBrief = nil
		_ = recompute()
	}
	if capped.TokenEstimate > maxTokens {
		capped.TokenEstimate = maxTokens
	}
	capped.Hash = hashContextPack(&capped)
	return &capped
}
