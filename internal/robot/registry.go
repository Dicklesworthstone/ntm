// Package robot provides machine-readable output for AI agents.
// registry.go centralizes robot surface, section, and schema metadata.
package robot

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// RobotRegistry exposes shared metadata about robot surfaces, sections,
// categories, and schema bindings. Downstream consumers can use this as the
// anti-drift source of truth instead of hand-maintaining parallel maps.
type RobotRegistry struct {
	Surfaces    []RobotSurfaceDescriptor `json:"surfaces"`
	Sections    []RobotSectionDescriptor `json:"sections"`
	Categories  []string                 `json:"categories"`
	SchemaTypes []string                 `json:"schema_types,omitempty"`

	surfaceByName map[string]RobotSurfaceDescriptor
	sectionByName map[string]RobotSectionDescriptor
	schemaByType  map[string]interface{}
}

// RobotSurfaceDescriptor describes a robot surface and the metadata that
// machine and human consumers need to reason about it.
type RobotSurfaceDescriptor struct {
	Name        string               `json:"name"`
	Flag        string               `json:"flag"`
	Category    string               `json:"category"`
	Summary     string               `json:"summary"`
	Description string               `json:"description"`
	SchemaID    string               `json:"schema_id"`
	SchemaType  string               `json:"schema_type,omitempty"`
	Sections    []string             `json:"sections,omitempty"`
	Parameters  []RobotParameter     `json:"parameters,omitempty"`
	Examples    []string             `json:"examples,omitempty"`
	Transports  []RobotTransportInfo `json:"transports,omitempty"`
}

// RobotSectionDescriptor describes a projection section that can appear in
// registry-backed robot surfaces.
type RobotSectionDescriptor struct {
	Name        string `json:"name"`
	SchemaID    string `json:"schema_id"`
	Scope       string `json:"scope"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
}

// RobotTransportInfo describes where a robot surface is exposed.
type RobotTransportInfo struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Method   string `json:"method,omitempty"`
}

type robotSurfaceMetadata struct {
	SchemaID   string
	SchemaType string
	Sections   []string
	Transports []RobotTransportInfo
}

var (
	robotRegistryOnce sync.Once
	robotRegistry     *RobotRegistry
)

// GetRobotRegistry returns a detached snapshot of the cached robot registry.
func GetRobotRegistry() *RobotRegistry {
	robotRegistryOnce.Do(func() {
		robotRegistry = buildRobotRegistry()
	})
	return cloneRobotRegistry(robotRegistry)
}

// Surface returns a registry surface by name.
func (r *RobotRegistry) Surface(name string) (RobotSurfaceDescriptor, bool) {
	if r == nil {
		return RobotSurfaceDescriptor{}, false
	}
	surface, ok := r.surfaceByName[name]
	if ok {
		surface.Sections = cloneStrings(surface.Sections)
		surface.Parameters = cloneRobotParameters(surface.Parameters)
		surface.Examples = cloneStrings(surface.Examples)
		surface.Transports = cloneTransports(surface.Transports)
	}
	return surface, ok
}

// Section returns a registry section by name.
func (r *RobotRegistry) Section(name string) (RobotSectionDescriptor, bool) {
	if r == nil {
		return RobotSectionDescriptor{}, false
	}
	section, ok := r.sectionByName[name]
	return section, ok
}

// SchemaBinding returns the output type registered for a schema type.
func (r *RobotRegistry) SchemaBinding(schemaType string) (interface{}, bool) {
	if r == nil {
		return nil, false
	}
	binding, ok := r.schemaByType[schemaType]
	return binding, ok
}

func buildRobotRegistry() *RobotRegistry {
	commands := buildCommandRegistry()
	metadata := buildRobotSurfaceMetadata()
	schemaByType := cloneSchemaBindings(SchemaCommand)
	sectionsByName := buildRobotSectionCatalog()
	surfaces := make([]RobotSurfaceDescriptor, 0, len(commands))
	surfaceByName := make(map[string]RobotSurfaceDescriptor, len(commands))

	for _, command := range commands {
		meta := metadata[command.Name]
		surface := RobotSurfaceDescriptor{
			Name:        command.Name,
			Flag:        command.Flag,
			Category:    command.Category,
			Summary:     summarizeDescription(command.Description),
			Description: command.Description,
			SchemaID:    firstNonEmptyString(meta.SchemaID, defaultRobotSchemaID(command.Name)),
			SchemaType:  firstNonEmptyString(meta.SchemaType, schemaTypeForCommand(command.Name, schemaByType)),
			Sections:    cloneStrings(meta.Sections),
			Parameters:  cloneRobotParameters(command.Parameters),
			Examples:    cloneStrings(command.Examples),
			Transports:  cloneTransports(meta.Transports),
		}
		if len(surface.Transports) == 0 {
			surface.Transports = []RobotTransportInfo{
				{
					Type:     "cli",
					Endpoint: fmt.Sprintf("ntm %s", command.Flag),
				},
			}
		}
		for _, sectionName := range surface.Sections {
			if _, ok := sectionsByName[sectionName]; !ok {
				sectionsByName[sectionName] = defaultRobotSection(sectionName)
			}
		}
		surfaces = append(surfaces, surface)
		surfaceByName[surface.Name] = surface
	}

	sort.Slice(surfaces, func(i, j int) bool {
		if surfaces[i].Category != surfaces[j].Category {
			return categoryIndex(surfaces[i].Category) < categoryIndex(surfaces[j].Category)
		}
		return surfaces[i].Name < surfaces[j].Name
	})

	sections := make([]RobotSectionDescriptor, 0, len(sectionsByName))
	sectionByName := make(map[string]RobotSectionDescriptor, len(sectionsByName))
	for _, name := range sortedSectionNames(sectionsByName) {
		section := sectionsByName[name]
		sections = append(sections, section)
		sectionByName[name] = section
	}

	return &RobotRegistry{
		Surfaces:      surfaces,
		Sections:      sections,
		Categories:    cloneStrings(categoryOrder),
		SchemaTypes:   sortedSchemaTypes(schemaByType),
		surfaceByName: surfaceByName,
		sectionByName: sectionByName,
		schemaByType:  schemaByType,
	}
}

func buildRobotSurfaceMetadata() map[string]robotSurfaceMetadata {
	return map[string]robotSurfaceMetadata{
		"status": {
			Sections: []string{"summary", "sessions"},
		},
		"snapshot": {
			Sections: []string{
				"summary",
				"sessions",
				"work",
				"coordination",
				"quota",
				"alerts",
				"incidents",
				"attention",
				"source_health",
				"next_actions",
			},
		},
		"events": {
			Sections: []string{"events", "cursor"},
		},
		"digest": {
			Sections: []string{"attention", "incidents", "next_actions"},
		},
		"attention": {
			Sections: []string{"attention", "incidents", "next_actions", "cursor"},
		},
		"dashboard": {
			Sections: []string{"summary", "sessions", "attention", "work", "alerts"},
		},
		"terse": {
			Sections: []string{"summary", "attention"},
		},
		"markdown": {
			Sections: []string{"summary", "sessions", "work", "alerts", "attention"},
		},
		"health": {
			Sections: []string{"source_health", "next_actions"},
		},
		"diagnose": {
			Sections: []string{"source_health", "incidents", "next_actions"},
		},
		"probe": {
			Sections: []string{"source_health", "incidents", "next_actions"},
		},
		"wait": {
			Sections: []string{"next_actions", "cursor"},
		},
		"beads-list": {
			SchemaType: "beads_list",
			Sections:   []string{"work"},
		},
		"watch-bead": {
			Sections: []string{"work", "events"},
		},
		"assign": {
			Sections: []string{"work", "next_actions"},
		},
		"bulk-assign": {
			Sections: []string{"work", "next_actions"},
		},
		"triage": {
			Sections: []string{"work", "next_actions"},
		},
		"plan": {
			Sections: []string{"work", "next_actions"},
		},
		"graph": {
			Sections: []string{"work"},
		},
		"forecast": {
			Sections: []string{"work", "next_actions"},
		},
		"capabilities": {
			Sections: []string{"command_catalog"},
		},
		"schema": {
			Sections: []string{"command_catalog"},
		},
		"docs": {
			Sections: []string{"command_catalog"},
		},
		"mail": {
			Sections: []string{"coordination"},
		},
		"mail-check": {
			Sections: []string{"coordination", "next_actions"},
		},
		"history": {
			Sections: []string{"events"},
		},
		"errors": {
			Sections: []string{"events", "source_health"},
		},
		"agent-health": {
			Sections: []string{"source_health", "next_actions"},
		},
		"health-oauth": {
			Sections: []string{"source_health", "quota"},
		},
		"is-working": {
			Sections: []string{"summary"},
		},
		"logs": {
			Sections: []string{"events"},
		},
		"monitor": {
			Sections: []string{"source_health", "next_actions"},
		},
		"smart-restart": {
			Sections: []string{"source_health", "next_actions"},
		},
		"support-bundle": {
			Sections: []string{"source_health", "events"},
		},
		"agent-names": {
			Sections: []string{"sessions"},
		},
		"controller-spawn": {
			Sections: []string{"sessions", "next_actions"},
		},
		"context-inject": {
			Sections: []string{"sessions", "next_actions"},
		},
		"env": {
			Sections: []string{"source_health"},
		},
		"quota-status": {
			Sections: []string{"quota"},
		},
		"quota-check": {
			Sections: []string{"quota", "next_actions"},
		},
		"account-status": {
			Sections: []string{"quota"},
		},
		"accounts-list": {
			Sections: []string{"quota"},
		},
		"switch-account": {
			Sections: []string{"quota", "next_actions"},
		},
		"default-prompts": {
			Sections: []string{"command_catalog"},
		},
		"profile-list": {
			Sections: []string{"command_catalog"},
		},
		"profile-show": {
			Sections: []string{"command_catalog"},
		},
		"xf-search": {
			Sections: []string{"events"},
		},
		"xf-status": {
			Sections: []string{"source_health"},
		},
		"cass-insights": {
			Sections: []string{"events"},
		},
		"ensemble-suggest": {
			Sections: []string{"next_actions"},
		},
		"ensemble-stop": {
			Sections: []string{"sessions", "next_actions"},
		},
		"proxy-status": {
			Sections: []string{"source_health"},
		},
		"health-restart-stuck": {
			SchemaType: "auto_restart_stuck",
			Sections:   []string{"source_health", "incidents", "next_actions"},
		},
		"inspect-pane": {
			SchemaType: "inspect",
			Sections:   []string{"events", "next_actions"},
		},
		"inspect-session": {
			SchemaType: "inspect_session",
			Sections:   []string{"sessions", "next_actions"},
		},
		"inspect-agent": {
			SchemaType: "inspect_agent",
			Sections:   []string{"sessions", "next_actions"},
		},
		"inspect-work": {
			SchemaType: "inspect_work",
			Sections:   []string{"work", "next_actions"},
		},
		"inspect-coordination": {
			SchemaType: "inspect_coordination",
			Sections:   []string{"coordination", "next_actions"},
		},
		"inspect-quota": {
			SchemaType: "inspect_quota",
			Sections:   []string{"quota", "next_actions"},
		},
		"inspect-incident": {
			SchemaType: "inspect_incident",
			Sections:   []string{"incidents", "next_actions"},
		},
	}
}

func buildRobotSectionCatalog() map[string]RobotSectionDescriptor {
	return map[string]RobotSectionDescriptor{
		"summary": {
			Name:        "summary",
			SchemaID:    defaultRobotSectionSchemaID("summary"),
			Scope:       "global",
			Summary:     "High-level summary section",
			Description: "Condensed top-level system state intended for cheap polling and quick operator context.",
		},
		"sessions": {
			Name:        "sessions",
			SchemaID:    defaultRobotSectionSchemaID("sessions"),
			Scope:       "global",
			Summary:     "Session inventory section",
			Description: "Lists sessions, panes, and agent-level runtime state for the current project.",
		},
		"work": {
			Name:        "work",
			SchemaID:    defaultRobotSectionSchemaID("work"),
			Scope:       "global",
			Summary:     "Work queue section",
			Description: "Captures bead, queue, assignment, or plan information that helps agents choose the next action.",
		},
		"coordination": {
			Name:        "coordination",
			SchemaID:    defaultRobotSectionSchemaID("coordination"),
			Scope:       "global",
			Summary:     "Coordination state section",
			Description: "Captures Agent Mail, reservation, and other cross-agent coordination state.",
		},
		"quota": {
			Name:        "quota",
			SchemaID:    defaultRobotSectionSchemaID("quota"),
			Scope:       "global",
			Summary:     "Quota section",
			Description: "Summarizes token, context, or execution-budget capacity signals.",
		},
		"alerts": {
			Name:        "alerts",
			SchemaID:    defaultRobotSectionSchemaID("alerts"),
			Scope:       "global",
			Summary:     "Alert section",
			Description: "Contains elevated warnings that may require immediate operator attention.",
		},
		"incidents": {
			Name:        "incidents",
			SchemaID:    defaultRobotSectionSchemaID("incidents"),
			Scope:       "global",
			Summary:     "Incident section",
			Description: "Describes degraded or failed runtime conditions and the evidence attached to them.",
		},
		"attention": {
			Name:        "attention",
			SchemaID:    defaultRobotSectionSchemaID("attention"),
			Scope:       "global",
			Summary:     "Attention queue section",
			Description: "Summarizes attention-feed items, wake reasons, and prioritized actions for an operator loop.",
		},
		"source_health": {
			Name:        "source_health",
			SchemaID:    defaultRobotSectionSchemaID("source_health"),
			Scope:       "global",
			Summary:     "Source health section",
			Description: "Reports freshness and health of upstream adapters or runtime sources used to build the response.",
		},
		"next_actions": {
			Name:        "next_actions",
			SchemaID:    defaultRobotSectionSchemaID("next_actions"),
			Scope:       "global",
			Summary:     "Next-actions section",
			Description: "Provides machine-readable follow-up actions or recommended next steps.",
		},
		"events": {
			Name:        "events",
			SchemaID:    defaultRobotSectionSchemaID("events"),
			Scope:       "global",
			Summary:     "Event replay section",
			Description: "Contains event log or replay payloads used for inspection, replay, or debugging.",
		},
		"cursor": {
			Name:        "cursor",
			SchemaID:    defaultRobotSectionSchemaID("cursor"),
			Scope:       "global",
			Summary:     "Cursor handoff section",
			Description: "Carries cursor or resume-position data used for incremental polling and replay.",
		},
		"command_catalog": {
			Name:        "command_catalog",
			SchemaID:    defaultRobotSectionSchemaID("command_catalog"),
			Scope:       "global",
			Summary:     "Command catalog section",
			Description: "Describes the robot command catalog for help, capabilities, or other machine discovery surfaces.",
		},
	}
}

func cloneSchemaBindings(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for name, binding := range src {
		dst[name] = binding
	}
	return dst
}

func schemaTypeForCommand(name string, schemaByType map[string]interface{}) string {
	if _, ok := schemaByType[name]; ok {
		return name
	}
	candidate := strings.ReplaceAll(name, "-", "_")
	if _, ok := schemaByType[candidate]; ok {
		return candidate
	}
	return ""
}

func sortedSchemaTypes(schemaByType map[string]interface{}) []string {
	types := make([]string, 0, len(schemaByType))
	for name := range schemaByType {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

func sortedSectionNames(sections map[string]RobotSectionDescriptor) []string {
	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func summarizeDescription(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	if idx := strings.Index(description, "."); idx >= 0 {
		return strings.TrimSpace(description[:idx+1])
	}
	return description
}

func defaultRobotSchemaID(name string) string {
	return fmt.Sprintf("ntm:robot:%s:v1", normalizeRobotRegistryName(name))
}

func defaultRobotSectionSchemaID(name string) string {
	return fmt.Sprintf("ntm:section:%s:v1", normalizeRobotRegistryName(name))
}

func defaultRobotSection(name string) RobotSectionDescriptor {
	label := humanizeRobotRegistryName(name)
	return RobotSectionDescriptor{
		Name:        name,
		SchemaID:    defaultRobotSectionSchemaID(name),
		Scope:       "global",
		Summary:     label + " section",
		Description: "Registry-backed section descriptor for " + label + ".",
	}
}

func normalizeRobotRegistryName(name string) string {
	name = strings.ReplaceAll(name, "_", "-")
	return strings.ToLower(name)
}

func humanizeRobotRegistryName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	if name == "" {
		return ""
	}
	words := strings.Fields(strings.ToLower(name))
	initialisms := map[string]string{
		"acfs": "ACFS",
		"api":  "API",
		"bv":   "BV",
		"cass": "CASS",
		"giil": "GIIL",
		"id":   "ID",
		"jfp":  "JFP",
		"json": "JSON",
		"mcp":  "MCP",
		"sse":  "SSE",
		"slb":  "SLB",
		"tmux": "tmux",
		"tui":  "TUI",
		"url":  "URL",
	}
	for i, word := range words {
		if word == "" {
			continue
		}
		if initialism, ok := initialisms[word]; ok {
			words[i] = initialism
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func cloneRobotParameters(parameters []RobotParameter) []RobotParameter {
	if parameters == nil {
		return nil
	}
	cloned := make([]RobotParameter, len(parameters))
	copy(cloned, parameters)
	return cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func cloneTransports(transports []RobotTransportInfo) []RobotTransportInfo {
	if transports == nil {
		return nil
	}
	cloned := make([]RobotTransportInfo, len(transports))
	copy(cloned, transports)
	return cloned
}

func cloneRobotSurfaceDescriptors(surfaces []RobotSurfaceDescriptor) []RobotSurfaceDescriptor {
	if surfaces == nil {
		return nil
	}

	cloned := make([]RobotSurfaceDescriptor, len(surfaces))
	for i, surface := range surfaces {
		cloned[i] = surface
		cloned[i].Sections = cloneStrings(surface.Sections)
		cloned[i].Parameters = cloneRobotParameters(surface.Parameters)
		cloned[i].Examples = cloneStrings(surface.Examples)
		cloned[i].Transports = cloneTransports(surface.Transports)
	}

	return cloned
}

func cloneRobotRegistry(src *RobotRegistry) *RobotRegistry {
	if src == nil {
		return nil
	}

	cloned := &RobotRegistry{
		Surfaces:      cloneRobotSurfaceDescriptors(src.Surfaces),
		Sections:      cloneRobotSections(src.Sections),
		Categories:    cloneStrings(src.Categories),
		SchemaTypes:   cloneStrings(src.SchemaTypes),
		surfaceByName: cloneSurfaceDescriptorMap(src.surfaceByName),
		sectionByName: cloneSectionDescriptorMap(src.sectionByName),
		schemaByType:  cloneSchemaBindings(src.schemaByType),
	}

	return cloned
}

func cloneRobotSections(sections []RobotSectionDescriptor) []RobotSectionDescriptor {
	if sections == nil {
		return nil
	}
	cloned := make([]RobotSectionDescriptor, len(sections))
	copy(cloned, sections)
	return cloned
}

func cloneSurfaceDescriptorMap(src map[string]RobotSurfaceDescriptor) map[string]RobotSurfaceDescriptor {
	if src == nil {
		return nil
	}
	cloned := make(map[string]RobotSurfaceDescriptor, len(src))
	for name, surface := range src {
		copied := surface
		copied.Sections = cloneStrings(surface.Sections)
		copied.Parameters = cloneRobotParameters(surface.Parameters)
		copied.Examples = cloneStrings(surface.Examples)
		copied.Transports = cloneTransports(surface.Transports)
		cloned[name] = copied
	}
	return cloned
}

func cloneSectionDescriptorMap(src map[string]RobotSectionDescriptor) map[string]RobotSectionDescriptor {
	if src == nil {
		return nil
	}
	cloned := make(map[string]RobotSectionDescriptor, len(src))
	for name, section := range src {
		cloned[name] = section
	}
	return cloned
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
