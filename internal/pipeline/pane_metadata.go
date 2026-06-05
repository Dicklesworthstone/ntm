package pipeline

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const paneVariableKey = "pane"

// PaneMetadata is the structured per-pane context exposed through ${pane.X}.
//
// The *FromTag flags distinguish session metadata that came from an explicit
// tmux tag (real specified pane metadata) from session metadata derived
// purely from pane.Type/pane.Variant (generic fallback). The roster merge
// uses those flags to honor the bd-6lkqr.1 source-priority contract:
// session wins where it has real specified metadata, but tag-missing fields
// are overlaid from the structured roster sources rather than being
// permanently masked by the generic Type/Variant fallback.
type PaneMetadata struct {
	PaneID                string
	Index                 int
	NTMIndex              int
	Title                 string
	Type                  string
	Role                  string
	RoleFromTag           bool
	Model                 string
	ModelFromTag          bool
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
	var sessionEntries []PaneMetadata
	if client != nil && session != "" {
		entries, err := paneMetadataFromSession(client, session)
		if err != nil {
			// bd-ujk04: a tmux/session lookup failure (resumed-offline,
			// missing session, etc.) must NOT short-circuit the cascading
			// fallback chain. Treat it as a miss, log context, and let
			// RESUME.md / roster.yaml / phase0 still populate the cache.
			slog.Warn("pipeline.pane_metadata.session_lookup_failed",
				"session", session,
				"project_dir", projectDir,
				"error", err.Error(),
			)
		} else {
			sessionEntries = entries
		}
	}

	rosterEntries, rosterSource, err := loadRosterFallbackEntries(projectDir)
	if err != nil {
		return nil, err
	}

	if len(sessionEntries) == 0 {
		if rosterSource == "phase0_roster" && len(rosterEntries) > 0 {
			slog.Warn("pipeline.pane_metadata.phase0_roster_fallback",
				"source", rosterSource,
				"project_dir", projectDir,
			)
		}
		return newPaneMetadataCache(rosterEntries), nil
	}

	if len(rosterEntries) == 0 {
		return newPaneMetadataCache(sessionEntries), nil
	}

	merged := mergeSessionWithRoster(sessionEntries, rosterEntries)
	return newPaneMetadataCache(merged), nil
}

// loadRosterFallbackEntries probes the documented structured roster sources
// in priority order (RESUME.md → roster.yaml → phase0_scope_decision.md) and
// returns the first non-empty set plus its source name. This used to be
// inlined in LoadPaneMetadataCache but is now reused by the merge path so
// session metadata and roster metadata can both contribute.
func loadRosterFallbackEntries(projectDir string) ([]PaneMetadata, string, error) {
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
			return nil, "", err
		}
		if len(entries) > 0 {
			return entries, loader.name, nil
		}
	}
	return nil, "", nil
}

// mergeSessionWithRoster overlays roster pane metadata onto the live session
// metadata so each session pane keeps its tag-derived values but tag-missing
// fields are filled in from the structured roster source. Implements the
// bd-6lkqr.1 source-priority contract: session wins on real specified
// metadata, but generic Type/Variant fallbacks no longer mask roster values.
func mergeSessionWithRoster(session, roster []PaneMetadata) []PaneMetadata {
	if len(roster) == 0 {
		return session
	}
	rosterByID := make(map[string]PaneMetadata, len(roster))
	rosterByIndex := make(map[int]PaneMetadata, len(roster))
	for _, r := range roster {
		if r.PaneID != "" {
			rosterByID[r.PaneID] = r
		}
		if r.Index != 0 {
			rosterByIndex[r.Index] = r
		}
	}

	merged := make([]PaneMetadata, 0, len(session))
	for _, s := range session {
		r, ok := rosterByID[s.PaneID]
		if !ok && s.Index != 0 {
			r, ok = rosterByIndex[s.Index]
		}
		if !ok && s.NTMIndex != 0 {
			r, ok = rosterByIndex[s.NTMIndex]
		}
		if ok {
			if !s.RoleFromTag && r.Role != "" {
				s.Role = r.Role
			}
			if !s.ModelFromTag && r.Model != "" {
				s.Model = r.Model
			}
			if len(s.Domains) == 0 && len(r.Domains) > 0 {
				s.Domains = append([]string(nil), r.Domains...)
			}
			if !s.ProductiveIgnoranceOK && r.ProductiveIgnoranceOK {
				s.ProductiveIgnorance = r.ProductiveIgnorance
				s.ProductiveIgnoranceOK = true
			}
		}
		merged = append(merged, s)
	}
	return merged
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
	roleTag := tagValue(pane.Tags, "role")
	role := roleTag
	if role == "" {
		role = string(pane.Type)
	}
	modelTag := tagValue(pane.Tags, "model")
	model := modelTag
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
		RoleFromTag:           roleTag != "",
		Model:                 model,
		ModelFromTag:          modelTag != "",
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

// bindStepPaneMetadata pushes pane metadata for the given step's pane
// reference around a single step's dispatch. It is a no-op when the step
// has no Pane reference, or when an outer foreach iteration has already
// bound pane vars (cloneInterfaceMap shape stored under paneVariableKey)
// — foreach assigns pane metadata from the iteration plan and we should
// not override that with a roster-only lookup that may not know per-
// iteration overrides.
//
// Errors from the lookup are swallowed and the step proceeds without
// pane vars: the existing strict substitutor surfaces unresolved
// ${pane.X} references with its own actionable error, so silently
// dropping the lookup error here matches the existing behavior of
// substituteVariables when pane data is genuinely missing.
//
// Returns a release function the caller must defer to restore the prior
// scope. The release is always safe to call (no-op when nothing was
// pushed).
func (e *Executor) bindStepPaneMetadata(step *Step) func() {
	if e == nil || step == nil || e.state == nil {
		return func() {}
	}
	paneRef := paneRefFromStep(step)
	if paneRef == "" {
		return func() {}
	}

	e.varMu.RLock()
	_, alreadyBound := e.state.Variables[paneVariableKey]
	e.varMu.RUnlock()
	if alreadyBound {
		return func() {}
	}

	scope, err := e.pushPaneMetadataVars(paneRef)
	if err != nil {
		slog.Debug("pane metadata lookup failed; ${pane.X} will fall back to substitutor error",
			"run_id", e.state.RunID,
			"step_id", step.ID,
			"pane_ref", paneRef,
			"error", err,
		)
		return func() {}
	}
	return func() {
		e.popPaneMetadataVars(scope)
	}
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
