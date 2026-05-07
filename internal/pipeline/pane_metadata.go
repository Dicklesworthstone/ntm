package pipeline

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"gopkg.in/yaml.v3"
)

const paneVariableKey = "pane"

// PaneMetadata is the structured per-pane context exposed through ${pane.X}.
type PaneMetadata struct {
	PaneID                string
	Index                 int
	NTMIndex              int
	Title                 string
	Type                  string
	Role                  string
	Model                 string
	Domains               []string
	ProductiveIgnorance   bool
	ProductiveIgnoranceOK bool
	Source                string
}

func (m PaneMetadata) variableMap() map[string]interface{} {
	domain := ""
	if len(m.Domains) > 0 {
		domain = m.Domains[0]
	}
	return map[string]interface{}{
		"id":                       m.PaneID,
		"pane_id":                  m.PaneID,
		"index":                    m.Index,
		"ntm_index":                m.NTMIndex,
		"title":                    m.Title,
		"type":                     m.Type,
		"role":                     m.Role,
		"model":                    m.Model,
		"domain":                   domain,
		"domains":                  append([]string(nil), m.Domains...),
		"productive_ignorance":     m.ProductiveIgnorance,
		"productive_ignorance_set": m.ProductiveIgnoranceOK,
		"source":                   m.Source,
	}
}

// PaneMetadataLoader lazily builds a per-run pane metadata cache. Loading is
// guarded by sync.Once so repeated substitutions do not re-query tmux or reparse
// roster files.
type PaneMetadataLoader struct {
	client     TmuxClient
	session    string
	projectDir string

	once  sync.Once
	cache *PaneMetadataCache
	err   error
}

func NewPaneMetadataLoader(client TmuxClient, session, projectDir string) *PaneMetadataLoader {
	if client == nil {
		client = realTmuxClient{}
	}
	return &PaneMetadataLoader{client: client, session: session, projectDir: projectDir}
}

func (l *PaneMetadataLoader) Lookup(paneRef string) (PaneMetadata, error) {
	cache, err := l.Cache()
	if err != nil {
		return PaneMetadata{}, err
	}
	return cache.Lookup(paneRef)
}

func (l *PaneMetadataLoader) Cache() (*PaneMetadataCache, error) {
	l.once.Do(func() {
		l.cache, l.err = LoadPaneMetadataCache(l.client, l.session, l.projectDir)
	})
	return l.cache, l.err
}

type PaneMetadataCache struct {
	byID    map[string]PaneMetadata
	byIndex map[int]PaneMetadata
}

func newPaneMetadataCache(entries []PaneMetadata) *PaneMetadataCache {
	cache := &PaneMetadataCache{
		byID:    make(map[string]PaneMetadata, len(entries)),
		byIndex: make(map[int]PaneMetadata, len(entries)),
	}
	for _, entry := range entries {
		if entry.PaneID != "" {
			cache.byID[entry.PaneID] = entry
		}
		if entry.Index != 0 {
			cache.byIndex[entry.Index] = entry
		}
		if entry.NTMIndex != 0 {
			cache.byIndex[entry.NTMIndex] = entry
		}
	}
	return cache
}

func (c *PaneMetadataCache) Lookup(paneRef string) (PaneMetadata, error) {
	if c == nil {
		return PaneMetadata{}, fmt.Errorf("pane metadata cache is not initialized")
	}
	paneRef = strings.TrimSpace(paneRef)
	if paneRef == "" {
		return PaneMetadata{}, fmt.Errorf("pane reference is empty")
	}
	if meta, ok := c.byID[paneRef]; ok {
		return meta, nil
	}
	if idx, err := strconv.Atoi(strings.TrimPrefix(paneRef, "pane ")); err == nil {
		if meta, ok := c.byIndex[idx]; ok {
			return meta, nil
		}
	}
	return PaneMetadata{}, fmt.Errorf("pane metadata not found for %q", paneRef)
}

func LoadPaneMetadataCache(client TmuxClient, session, projectDir string) (*PaneMetadataCache, error) {
	if client != nil && session != "" {
		entries, err := paneMetadataFromSession(client, session)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return newPaneMetadataCache(entries), nil
		}
	}

	loaders := []struct {
		name string
		fn   func(string) ([]PaneMetadata, error)
	}{
		{name: "resume_roster", fn: paneMetadataFromResume},
		{name: "roster_yaml", fn: paneMetadataFromRosterYAML},
		{name: "phase0_roster", fn: paneMetadataFromPhase0Roster},
	}
	for _, loader := range loaders {
		entries, err := loader.fn(projectDir)
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			if loader.name == "phase0_roster" {
				slog.Warn("pipeline.pane_metadata.phase0_roster_fallback",
					"source", loader.name,
					"project_dir", projectDir,
				)
			}
			return newPaneMetadataCache(entries), nil
		}
	}

	return newPaneMetadataCache(nil), nil
}

func paneMetadataFromSession(client TmuxClient, session string) ([]PaneMetadata, error) {
	panes, err := client.GetPanes(session)
	if err != nil {
		return nil, fmt.Errorf("load pane metadata from ntm session: %w", err)
	}
	entries := make([]PaneMetadata, 0, len(panes))
	for _, pane := range panes {
		entries = append(entries, paneMetadataFromTmuxPane(pane))
	}
	return entries, nil
}

func paneMetadataFromTmuxPane(pane tmux.Pane) PaneMetadata {
	role := tagValue(pane.Tags, "role")
	if role == "" {
		role = string(pane.Type)
	}
	model := tagValue(pane.Tags, "model")
	if model == "" {
		model = pane.Variant
	}
	if model == "" {
		model = string(pane.Type)
	}
	productive, productiveOK := tagBool(pane.Tags, "productive_ignorance")
	return PaneMetadata{
		PaneID:                pane.ID,
		Index:                 pane.Index,
		NTMIndex:              pane.NTMIndex,
		Title:                 pane.Title,
		Type:                  string(pane.Type),
		Role:                  role,
		Model:                 model,
		Domains:               tagList(pane.Tags, "domain"),
		ProductiveIgnorance:   productive,
		ProductiveIgnoranceOK: productiveOK,
		Source:                "ntm_session",
	}
}

func paneMetadataFromResume(projectDir string) ([]PaneMetadata, error) {
	if projectDir == "" {
		return nil, nil
	}
	path := filepath.Join(projectDir, "RESUME.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read RESUME.md roster: %w", err)
	}
	block := extractRosterYAMLBlock(string(data))
	if block == "" {
		return nil, nil
	}
	return parsePaneRosterYAML([]byte(block), "resume_roster")
}

func paneMetadataFromRosterYAML(projectDir string) ([]PaneMetadata, error) {
	if projectDir == "" {
		return nil, nil
	}
	path := filepath.Join(projectDir, ".brenner_workspace", "roster.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read roster.yaml: %w", err)
	}
	return parsePaneRosterYAML(data, "roster_yaml")
}

func paneMetadataFromPhase0Roster(projectDir string) ([]PaneMetadata, error) {
	if projectDir == "" {
		return nil, nil
	}
	path := filepath.Join(projectDir, "phase0_scope_decision.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read phase0 roster: %w", err)
	}
	block := extractRosterYAMLBlock(string(data))
	if block == "" {
		return nil, nil
	}
	return parsePaneRosterYAML([]byte(block), "phase0_roster")
}

type paneRosterFile struct {
	Panes  []paneRosterEntry `yaml:"panes"`
	Roster []paneRosterEntry `yaml:"roster"`
}

type paneRosterEntry struct {
	Pane                  int        `yaml:"pane"`
	PaneID                string     `yaml:"pane_id"`
	Index                 int        `yaml:"index"`
	Role                  string     `yaml:"role"`
	Model                 string     `yaml:"model"`
	Domain                domainList `yaml:"domain"`
	ProductiveIgnorance   bool       `yaml:"productive_ignorance"`
	ProductiveIgnoranceOK bool
}

func (e *paneRosterEntry) UnmarshalYAML(value *yaml.Node) error {
	type rawEntry paneRosterEntry
	var raw rawEntry
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*e = paneRosterEntry(raw)
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i].Value == "productive_ignorance" {
			e.ProductiveIgnoranceOK = true
			break
		}
	}
	return nil
}

type domainList []string

func (d *domainList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(value.Value) == "" {
			*d = nil
			return nil
		}
		*d = splitMetadataList(value.Value)
		return nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			if strings.TrimSpace(item.Value) != "" {
				out = append(out, strings.TrimSpace(item.Value))
			}
		}
		*d = out
		return nil
	default:
		return fmt.Errorf("domain must be a string or list")
	}
}

func parsePaneRosterYAML(data []byte, source string) ([]PaneMetadata, error) {
	var file paneRosterFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse pane roster YAML: %w", err)
	}
	entries := file.Panes
	if len(entries) == 0 {
		entries = file.Roster
	}
	if len(entries) == 0 {
		var list []paneRosterEntry
		if err := yaml.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse pane roster YAML list: %w", err)
		}
		entries = list
	}

	metadata := make([]PaneMetadata, 0, len(entries))
	for _, entry := range entries {
		index := entry.Index
		if index == 0 {
			index = entry.Pane
		}
		paneID := entry.PaneID
		if paneID == "" && index != 0 {
			paneID = fmt.Sprintf("%%%d", index)
		}
		metadata = append(metadata, PaneMetadata{
			PaneID:                paneID,
			Index:                 index,
			NTMIndex:              index,
			Role:                  entry.Role,
			Model:                 entry.Model,
			Domains:               []string(entry.Domain),
			ProductiveIgnorance:   entry.ProductiveIgnorance,
			ProductiveIgnoranceOK: entry.ProductiveIgnoranceOK,
			Source:                source,
		})
	}
	return metadata, nil
}

func extractRosterYAMLBlock(content string) string {
	lines := strings.Split(content, "\n")
	inRoster := false
	inFence := false
	sawFence := false
	var block []string
	var section []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if inRoster {
				break
			}
			if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(trimmed, "##")), "Roster") {
				inRoster = true
			}
			continue
		}
		if !inRoster {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			sawFence = true
			inFence = !inFence
			continue
		}
		if inFence {
			block = append(block, line)
			continue
		}
		section = append(section, line)
	}
	if sawFence {
		return strings.TrimSpace(strings.Join(block, "\n"))
	}
	return strings.TrimSpace(strings.Join(section, "\n"))
}

func (e *Executor) paneMetadataLoader() *PaneMetadataLoader {
	e.paneMu.Lock()
	defer e.paneMu.Unlock()
	if e.paneMeta == nil {
		e.paneMeta = NewPaneMetadataLoader(e.tmuxClient(), e.config.Session, e.config.ProjectDir)
	}
	return e.paneMeta
}

func (e *Executor) resetPaneMetadataLoader() {
	e.paneMu.Lock()
	defer e.paneMu.Unlock()
	e.paneMeta = nil
}

func (e *Executor) lookupPaneMetadata(paneRef string) (PaneMetadata, error) {
	return e.paneMetadataLoader().Lookup(paneRef)
}

// rosterDomainsByPaneRef loads pane domain ownership from the documented
// structured roster sources (RESUME.md, .brenner_workspace/roster.yaml,
// phase0_scope_decision.md) without going through the LoadPaneMetadataCache
// short-circuit that prefers session metadata. Used to enrich foreach
// strategy panes when the live tmux pane tags do not carry domain
// information — typically the case for resumed or brennerbot-style
// sessions whose authoritative domain mapping lives in roster files
// rather than tmux tags.
//
// Returns a map keyed by both pane ID (e.g. "%2") and "%<index>" form so
// callers can match against either reference. Session metadata always
// wins: this loader is only consulted to fill empty Domains lists.
func (e *Executor) rosterDomainsByPaneRef() map[string][]string {
	if e == nil || e.config.ProjectDir == "" {
		return nil
	}
	loaders := []func(string) ([]PaneMetadata, error){
		paneMetadataFromResume,
		paneMetadataFromRosterYAML,
		paneMetadataFromPhase0Roster,
	}
	out := make(map[string][]string)
	for _, loader := range loaders {
		entries, err := loader(e.config.ProjectDir)
		if err != nil || len(entries) == 0 {
			continue
		}
		for _, meta := range entries {
			if len(meta.Domains) == 0 {
				continue
			}
			domainsCopy := append([]string(nil), meta.Domains...)
			if meta.PaneID != "" {
				if _, exists := out[meta.PaneID]; !exists {
					out[meta.PaneID] = domainsCopy
				}
			}
			if meta.Index != 0 {
				key := fmt.Sprintf("%%%d", meta.Index)
				if _, exists := out[key]; !exists {
					out[key] = domainsCopy
				}
			}
		}
	}
	return out
}

// enrichStrategyPanesFromRoster fills empty Domains lists on strategyPanes
// using the structured roster sources. Panes that already carry domain
// information from tmux tags are left unchanged so live session metadata
// continues to win, per the source-priority contract from bd-6lkqr.1.
func (e *Executor) enrichStrategyPanesFromRoster(strategyPanes []paneStrategyPane) []paneStrategyPane {
	if e == nil || len(strategyPanes) == 0 {
		return strategyPanes
	}
	rosterDomains := e.rosterDomainsByPaneRef()
	if len(rosterDomains) == 0 {
		return strategyPanes
	}
	for i, sp := range strategyPanes {
		if len(sp.Domains) > 0 {
			continue
		}
		if domains, ok := rosterDomains[sp.ID]; ok {
			strategyPanes[i].Domains = append([]string(nil), domains...)
		}
	}
	return strategyPanes
}

func (e *Executor) pushPaneMetadataVars(paneRef string) (VariableScope, error) {
	meta, err := e.lookupPaneMetadata(paneRef)
	if err != nil {
		return VariableScope{}, err
	}
	e.varMu.Lock()
	defer e.varMu.Unlock()
	if e.state == nil {
		return VariableScope{}, fmt.Errorf("execution state is not initialized")
	}
	return BindPaneMetadata(e.state, meta), nil
}

func (e *Executor) popPaneMetadataVars(scope VariableScope) {
	e.varMu.Lock()
	defer e.varMu.Unlock()
	if e.state != nil {
		scope.Restore(e.state.Variables)
	}
}

func paneRefFromStep(step *Step) string {
	if step == nil {
		return ""
	}
	if step.Pane.Index > 0 {
		return strconv.Itoa(step.Pane.Index)
	}
	if step.Pane.Expr != "" {
		return step.Pane.Expr
	}
	return ""
}

func BindPaneMetadata(state *ExecutionState, meta PaneMetadata) VariableScope {
	if state.Variables == nil {
		state.Variables = make(map[string]interface{})
	}
	scope := CaptureVariableScope(state.Variables, paneVariableKey)
	state.Variables[paneVariableKey] = meta.variableMap()
	return scope
}

func (s *Substitutor) resolvePane(parts []string) (interface{}, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("pane requires a field name")
	}
	if s.state == nil || s.state.Variables == nil {
		return nil, fmt.Errorf("pane is only available inside pane-scoped dispatch")
	}
	pane, ok := s.state.Variables[paneVariableKey]
	if !ok {
		return nil, fmt.Errorf("pane is only available inside pane-scoped dispatch")
	}
	return navigateNested(pane, parts)
}

func tagValue(tags []string, key string) string {
	prefix := key + "="
	for _, tag := range tags {
		if value, ok := strings.CutPrefix(tag, prefix); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func tagList(tags []string, key string) []string {
	value := tagValue(tags, key)
	if value == "" {
		return nil
	}
	return splitMetadataList(value)
}

func tagBool(tags []string, key string) (bool, bool) {
	value := strings.ToLower(tagValue(tags, key))
	switch value {
	case "true", "1", "yes", "y":
		return true, true
	case "false", "0", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func splitMetadataList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
