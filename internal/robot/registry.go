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

	// Consumer metadata for machine integrations
	ConsumerGuidance *ConsumerGuidance   `json:"consumer_guidance,omitempty"`
	Boundedness      *BoundednessInfo    `json:"boundedness,omitempty"`
	FollowUp         *FollowUpInfo       `json:"follow_up,omitempty"`
	ActionHandoff    *ActionHandoffInfo  `json:"action_handoff,omitempty"`
	RequestSemantics *RequestSemantics   `json:"request_semantics,omitempty"`
	AttentionOps     *AttentionOpsInfo   `json:"attention_ops,omitempty"`
	Explainability   *ExplainabilityInfo `json:"explainability,omitempty"`
	Lifecycle        *LifecycleInfo      `json:"lifecycle,omitempty"`
}

// ConsumerGuidance provides hints for machine consumers about how to interpret
// and use this surface.
type ConsumerGuidance struct {
	// IntendedUse describes the primary consumer use case for this surface.
	IntendedUse string `json:"intended_use,omitempty"`
	// PollingRecommendation indicates whether this surface is suited for polling.
	PollingRecommendation string `json:"polling_recommendation,omitempty"`
	// SummaryHint describes how to generate summaries from this surface.
	SummaryHint string `json:"summary_hint,omitempty"`
	// CompressionSemantics explains how compacted or aggregated views work.
	CompressionSemantics string `json:"compression_semantics,omitempty"`
}

// BoundednessInfo describes limits, truncation, and pagination behavior.
type BoundednessInfo struct {
	// DefaultLimit is the default item limit if not specified.
	DefaultLimit int `json:"default_limit,omitempty"`
	// MaxLimit is the maximum allowed limit.
	MaxLimit int `json:"max_limit,omitempty"`
	// SupportsPagination indicates whether offset/limit pagination is supported.
	SupportsPagination bool `json:"supports_pagination,omitempty"`
	// TruncationBehavior describes what happens when output exceeds limits.
	TruncationBehavior string `json:"truncation_behavior,omitempty"`
	// PayloadBudgetBytes is the approximate target payload size.
	PayloadBudgetBytes int `json:"payload_budget_bytes,omitempty"`
}

// FollowUpInfo describes recommended drill-down paths and related surfaces.
type FollowUpInfo struct {
	// InspectSurfaces lists surfaces for deeper inspection of items.
	InspectSurfaces []string `json:"inspect_surfaces,omitempty"`
	// RelatedSurfaces lists conceptually related surfaces.
	RelatedSurfaces []string `json:"related_surfaces,omitempty"`
	// DrillDownPattern describes how to construct follow-up requests.
	DrillDownPattern string `json:"drill_down_pattern,omitempty"`
}

// ActionHandoffInfo describes structured action hints for operators.
type ActionHandoffInfo struct {
	// SupportsActions indicates whether this surface emits action hints.
	SupportsActions bool `json:"supports_actions,omitempty"`
	// ActionTypes lists the types of actions that may be recommended.
	ActionTypes []string `json:"action_types,omitempty"`
	// RemediationFormat describes how remediation hints are structured.
	RemediationFormat string `json:"remediation_format,omitempty"`
}

// RequestSemantics describes request identity, idempotency, and correlation.
type RequestSemantics struct {
	// SupportsIdempotency indicates whether requests can be safely retried.
	SupportsIdempotency bool `json:"supports_idempotency,omitempty"`
	// IdempotencyKeyParam is the parameter name for idempotency keys.
	IdempotencyKeyParam string `json:"idempotency_key_param,omitempty"`
	// SupportsCorrelation indicates whether request/response correlation is supported.
	SupportsCorrelation bool `json:"supports_correlation,omitempty"`
	// CorrelationIDField is the response field containing correlation IDs.
	CorrelationIDField string `json:"correlation_id_field,omitempty"`
}

// AttentionOpsInfo describes supported attention-state operations.
type AttentionOpsInfo struct {
	// SupportsAcknowledge indicates whether items can be acknowledged.
	SupportsAcknowledge bool `json:"supports_acknowledge,omitempty"`
	// SupportsSnooze indicates whether items can be snoozed.
	SupportsSnooze bool `json:"supports_snooze,omitempty"`
	// SupportsPin indicates whether items can be pinned.
	SupportsPin bool `json:"supports_pin,omitempty"`
	// SupportsEscalate indicates whether items can be manually escalated.
	SupportsEscalate bool `json:"supports_escalate,omitempty"`
	// OperatorStateField is the field tracking operator attention state.
	OperatorStateField string `json:"operator_state_field,omitempty"`
}

// ExplainabilityInfo describes diagnostic and evidence metadata.
type ExplainabilityInfo struct {
	// HasDiagnosticEntryPoints indicates whether this surface provides diagnostics.
	HasDiagnosticEntryPoints bool `json:"has_diagnostic_entry_points,omitempty"`
	// DiagnosticHandleField is the field containing diagnostic handles.
	DiagnosticHandleField string `json:"diagnostic_handle_field,omitempty"`
	// HasEvidenceSummary indicates whether evidence summaries are included.
	HasEvidenceSummary bool `json:"has_evidence_summary,omitempty"`
	// EvidenceSummaryField is the field containing evidence summaries.
	EvidenceSummaryField string `json:"evidence_summary_field,omitempty"`
	// AggregationCues describes how aggregated items can be expanded.
	AggregationCues string `json:"aggregation_cues,omitempty"`
}

// LifecycleInfo describes the lifecycle state and transition metadata.
type LifecycleInfo struct {
	// Status is the lifecycle status: stable, beta, deprecated, removed.
	Status string `json:"status,omitempty"`
	// DeprecatedSince is the version when this surface was deprecated.
	DeprecatedSince string `json:"deprecated_since,omitempty"`
	// ReplacedBy names the surface that replaces this one.
	ReplacedBy string `json:"replaced_by,omitempty"`
	// RemovalTarget is the version when this surface will be removed.
	RemovalTarget string `json:"removal_target,omitempty"`
	// MigrationHint describes how to migrate to the replacement.
	MigrationHint string `json:"migration_hint,omitempty"`
}

// RobotSectionDescriptor describes a projection section that can appear in
// registry-backed robot surfaces.
type RobotSectionDescriptor struct {
	Name        string `json:"name"`
	SchemaID    string `json:"schema_id"`
	Scope       string `json:"scope"`
	Summary     string `json:"summary"`
	Description string `json:"description"`

	// Consumer metadata for section-level guidance
	ConsumerGuidance *SectionConsumerGuidance `json:"consumer_guidance,omitempty"`
	Boundedness      *SectionBoundedness      `json:"boundedness,omitempty"`
	Explainability   *SectionExplainability   `json:"explainability,omitempty"`
}

// SectionConsumerGuidance provides section-specific hints for machine consumers.
type SectionConsumerGuidance struct {
	// SummaryHint describes how to summarize this section.
	SummaryHint string `json:"summary_hint,omitempty"`
	// KeyFields lists the most important fields for quick consumption.
	KeyFields []string `json:"key_fields,omitempty"`
	// CompressionSemantics explains how this section may be compacted.
	CompressionSemantics string `json:"compression_semantics,omitempty"`
}

// SectionBoundedness describes limits and truncation for a section.
type SectionBoundedness struct {
	// DefaultLimit is the default item limit for this section.
	DefaultLimit int `json:"default_limit,omitempty"`
	// TruncationBehavior describes what happens when limits are exceeded.
	TruncationBehavior string `json:"truncation_behavior,omitempty"`
	// SupportsIndependentPagination indicates section-specific pagination.
	SupportsIndependentPagination bool `json:"supports_independent_pagination,omitempty"`
}

// SectionExplainability describes diagnostic metadata for a section.
type SectionExplainability struct {
	// HasDiagnosticLinks indicates whether items link to diagnostic surfaces.
	HasDiagnosticLinks bool `json:"has_diagnostic_links,omitempty"`
	// DrillDownSurface is the surface for deeper inspection of section items.
	DrillDownSurface string `json:"drill_down_surface,omitempty"`
}

// RobotTransportInfo describes where a robot surface is exposed.
type RobotTransportInfo struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Method   string `json:"method,omitempty"`
}

type robotSurfaceMetadata struct {
	SchemaID         string
	SchemaType       string
	Sections         []string
	Transports       []RobotTransportInfo
	ConsumerGuidance *ConsumerGuidance
	Boundedness      *BoundednessInfo
	FollowUp         *FollowUpInfo
	ActionHandoff    *ActionHandoffInfo
	RequestSemantics *RequestSemantics
	AttentionOps     *AttentionOpsInfo
	Explainability   *ExplainabilityInfo
	Lifecycle        *LifecycleInfo
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
		surface.ConsumerGuidance = cloneConsumerGuidance(surface.ConsumerGuidance)
		surface.Boundedness = cloneBoundednessInfo(surface.Boundedness)
		surface.FollowUp = cloneFollowUpInfo(surface.FollowUp)
		surface.ActionHandoff = cloneActionHandoffInfo(surface.ActionHandoff)
		surface.RequestSemantics = cloneRequestSemantics(surface.RequestSemantics)
		surface.AttentionOps = cloneAttentionOpsInfo(surface.AttentionOps)
		surface.Explainability = cloneExplainabilityInfo(surface.Explainability)
		surface.Lifecycle = cloneLifecycleInfo(surface.Lifecycle)
	}
	return surface, ok
}

// Section returns a registry section by name.
func (r *RobotRegistry) Section(name string) (RobotSectionDescriptor, bool) {
	if r == nil {
		return RobotSectionDescriptor{}, false
	}
	section, ok := r.sectionByName[name]
	if ok {
		section.ConsumerGuidance = cloneSectionConsumerGuidance(section.ConsumerGuidance)
		section.Boundedness = cloneSectionBoundedness(section.Boundedness)
		section.Explainability = cloneSectionExplainability(section.Explainability)
	}
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
			Name:             command.Name,
			Flag:             command.Flag,
			Category:         command.Category,
			Summary:          summarizeDescription(command.Description),
			Description:      command.Description,
			SchemaID:         firstNonEmptyString(meta.SchemaID, defaultRobotSchemaID(command.Name)),
			SchemaType:       firstNonEmptyString(meta.SchemaType, schemaTypeForCommand(command.Name, schemaByType)),
			Sections:         cloneStrings(meta.Sections),
			Parameters:       cloneRobotParameters(command.Parameters),
			Examples:         cloneStrings(command.Examples),
			Transports:       cloneTransports(meta.Transports),
			ConsumerGuidance: cloneConsumerGuidance(meta.ConsumerGuidance),
			Boundedness:      cloneBoundednessInfo(meta.Boundedness),
			FollowUp:         cloneFollowUpInfo(meta.FollowUp),
			ActionHandoff:    cloneActionHandoffInfo(meta.ActionHandoff),
			RequestSemantics: cloneRequestSemantics(meta.RequestSemantics),
			AttentionOps:     cloneAttentionOpsInfo(meta.AttentionOps),
			Explainability:   cloneExplainabilityInfo(meta.Explainability),
			Lifecycle:        cloneLifecycleInfo(meta.Lifecycle),
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
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Quick health check and session inventory",
				PollingRecommendation: "Suitable for polling every 5-30 seconds",
				SummaryHint:           "Use session_count and active_agents for dashboards",
			},
			Boundedness: &BoundednessInfo{
				DefaultLimit:       0,
				SupportsPagination: true,
				TruncationBehavior: "Sessions beyond limit are omitted; use offset for more",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-session", "inspect-agent"},
				RelatedSurfaces:  []string{"snapshot", "terse"},
				DrillDownPattern: "--robot-inspect-session=<session_name>",
			},
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
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Full state capture for resync or initial context",
				PollingRecommendation: "Use for recovery; prefer attention for steady-state",
				SummaryHint:           "Extract summary section for quick overview",
				CompressionSemantics:  "Attention items may be aggregated; use --since for deltas",
			},
			Boundedness: &BoundednessInfo{
				DefaultLimit:       0,
				SupportsPagination: true,
				TruncationBehavior: "Section-specific limits apply; some arrays truncated",
				PayloadBudgetBytes: 65536,
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-session", "inspect-work", "inspect-incident"},
				RelatedSurfaces:  []string{"attention", "digest", "terse"},
				DrillDownPattern: "Use inspect surfaces with IDs from snapshot",
			},
			RequestSemantics: &RequestSemantics{
				SupportsCorrelation: true,
				CorrelationIDField:  "request_id",
			},
			ActionHandoff: &ActionHandoffInfo{
				SupportsActions:   true,
				ActionTypes:       []string{"remediation", "inspect", "acknowledge"},
				RemediationFormat: "next_actions array with action_type and command fields",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				DiagnosticHandleField:    "incidents[].diagnostic_id",
				HasEvidenceSummary:       true,
				EvidenceSummaryField:     "incidents[].evidence_summary",
			},
		},
		"events": {
			Sections: []string{"events", "cursor"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:          "Event replay and debugging",
				SummaryHint:          "Count events by type for activity summary",
				CompressionSemantics: "Events are not aggregated; cursor enables pagination",
			},
			Boundedness: &BoundednessInfo{
				DefaultLimit:       100,
				MaxLimit:           1000,
				SupportsPagination: true,
				TruncationBehavior: "Oldest events dropped; use cursor for continuation",
			},
		},
		"digest": {
			Sections: []string{"attention", "incidents", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Non-blocking summary for background checks",
				PollingRecommendation: "Use when full attention loop is not needed",
				SummaryHint:           "Focus on attention.items for urgent work",
			},
			ActionHandoff: &ActionHandoffInfo{
				SupportsActions:   true,
				ActionTypes:       []string{"inspect", "acknowledge"},
				RemediationFormat: "next_actions array with action_type and command fields",
			},
		},
		"attention": {
			Sections: []string{"attention", "incidents", "next_actions", "cursor"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Steady-state operator tending loop",
				PollingRecommendation: "Primary loop surface; use cursor for incremental updates",
				SummaryHint:           "Attention items ranked by priority; process in order",
				CompressionSemantics:  "Similar items may be aggregated; aggregation_key groups them",
			},
			Boundedness: &BoundednessInfo{
				DefaultLimit:       50,
				MaxLimit:           200,
				SupportsPagination: true,
				TruncationBehavior: "Lower-priority items dropped; cursor resumes position",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-incident", "inspect-work"},
				DrillDownPattern: "--robot-inspect-incident=<incident_id>",
			},
			AttentionOps: &AttentionOpsInfo{
				SupportsAcknowledge: true,
				SupportsSnooze:      true,
				SupportsPin:         true,
				SupportsEscalate:    true,
				OperatorStateField:  "items[].operator_state",
			},
			ActionHandoff: &ActionHandoffInfo{
				SupportsActions:   true,
				ActionTypes:       []string{"acknowledge", "snooze", "pin", "escalate", "inspect"},
				RemediationFormat: "next_actions array with action_type, command, and reason fields",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				DiagnosticHandleField:    "items[].diagnostic_handle",
				HasEvidenceSummary:       true,
				EvidenceSummaryField:     "items[].evidence_summary",
				AggregationCues:          "Aggregated items have aggregation_key; expand with inspect",
			},
		},
		"dashboard": {
			Sections: []string{"summary", "sessions", "attention", "work", "alerts"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Human-facing summary for dashboards and reports",
				PollingRecommendation: "Suitable for 10-60 second intervals",
				SummaryHint:           "Render sections as cards or panels",
			},
		},
		"terse": {
			Sections: []string{"summary", "attention"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Minimal-token output for constrained contexts",
				PollingRecommendation: "Use when token budget is critical",
				SummaryHint:           "Already compressed; use as-is",
				CompressionSemantics:  "Extreme compression; details omitted",
			},
			Boundedness: &BoundednessInfo{
				PayloadBudgetBytes: 2048,
				TruncationBehavior: "All sections heavily truncated for minimal tokens",
			},
		},
		"markdown": {
			Sections: []string{"summary", "sessions", "work", "alerts", "attention"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Human-readable markdown for reports and chat",
				SummaryHint: "Rendered markdown; embed directly in documents",
			},
		},
		"health": {
			Sections: []string{"source_health", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Quick health check for adapters and sources",
				PollingRecommendation: "Suitable for periodic health monitoring",
				SummaryHint:           "Check source_health.status for overall health",
			},
			ActionHandoff: &ActionHandoffInfo{
				SupportsActions:   true,
				ActionTypes:       []string{"remediation", "restart"},
				RemediationFormat: "next_actions with suggested recovery commands",
			},
		},
		"diagnose": {
			Sections: []string{"source_health", "incidents", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep diagnostic investigation of issues",
				SummaryHint: "Focus on incidents for root cause analysis",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-incident"},
				DrillDownPattern: "--robot-inspect-incident=<incident_id>",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				DiagnosticHandleField:    "incidents[].diagnostic_id",
				HasEvidenceSummary:       true,
				EvidenceSummaryField:     "incidents[].evidence_summary",
			},
		},
		"probe": {
			Sections: []string{"source_health", "incidents", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Active probing of session health",
				SummaryHint: "Check for degraded sources or active incidents",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				HasEvidenceSummary:       true,
			},
		},
		"wait": {
			Sections: []string{"next_actions", "cursor"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Block until state changes or timeout",
				SummaryHint: "Use cursor for continuation after wait returns",
			},
		},
		"beads-list": {
			SchemaType: "beads_list",
			Sections:   []string{"work"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "List work items from beads database",
				SummaryHint: "Filter by status for ready work",
			},
			Boundedness: &BoundednessInfo{
				DefaultLimit:       50,
				MaxLimit:           500,
				SupportsPagination: true,
				TruncationBehavior: "Beads beyond limit omitted; use offset for more",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-work"},
				DrillDownPattern: "--robot-inspect-work=<bead_id>",
			},
		},
		"watch-bead": {
			Sections: []string{"work", "events"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:          "Monitor a specific bead for changes",
				CompressionSemantics: "Events streamed as they occur",
			},
		},
		"assign": {
			Sections: []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Assign work to specific agents or panes",
				SummaryHint: "Check next_actions for assignment confirmation",
			},
			RequestSemantics: &RequestSemantics{
				SupportsIdempotency: true,
				IdempotencyKeyParam: "idempotency_key",
			},
		},
		"bulk-assign": {
			Sections: []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Assign multiple work items at once",
				SummaryHint: "Review next_actions for per-item results",
			},
			RequestSemantics: &RequestSemantics{
				SupportsIdempotency: true,
				IdempotencyKeyParam: "idempotency_key",
			},
		},
		"triage": {
			Sections: []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse:           "Graph-aware work prioritization",
				PollingRecommendation: "Query when selecting next task",
				SummaryHint:           "Top items in work section are highest priority",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-work"},
				RelatedSurfaces:  []string{"plan", "graph", "forecast"},
				DrillDownPattern: "--robot-inspect-work=<bead_id>",
			},
		},
		"plan": {
			Sections: []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Execution plan with parallelizable tracks",
				SummaryHint: "Tracks can execute concurrently; deps within tracks are serial",
			},
			FollowUp: &FollowUpInfo{
				RelatedSurfaces: []string{"triage", "graph", "forecast"},
			},
		},
		"graph": {
			Sections: []string{"work"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Dependency graph visualization and analysis",
				SummaryHint: "Use for impact analysis and bottleneck detection",
			},
		},
		"forecast": {
			Sections: []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "ETA predictions with dependency-aware scheduling",
				SummaryHint: "Forecasts include confidence intervals",
			},
		},
		"capabilities": {
			Sections: []string{"command_catalog"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Discover available robot commands and their metadata",
				SummaryHint: "Use to build dynamic UIs or select appropriate commands",
			},
			Lifecycle: &LifecycleInfo{
				Status: "stable",
			},
		},
		"schema": {
			Sections: []string{"command_catalog"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "JSON Schema for robot command outputs",
				SummaryHint: "Use for validation and code generation",
			},
		},
		"docs": {
			Sections: []string{"command_catalog"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Human-readable documentation for robot commands",
				SummaryHint: "Markdown format suitable for rendering",
			},
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
		"activity": {
			Sections: []string{"summary", "next_actions"},
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
		"restart-pane": {
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
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of a specific pane",
				SummaryHint: "Contains detailed pane output and agent state",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				HasEvidenceSummary:       true,
			},
		},
		"inspect-session": {
			SchemaType: "inspect_session",
			Sections:   []string{"sessions", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of a session and all its panes",
				SummaryHint: "Full session state including all agent panes",
			},
			FollowUp: &FollowUpInfo{
				InspectSurfaces:  []string{"inspect-pane", "inspect-agent"},
				DrillDownPattern: "--robot-inspect-pane=<session>:<pane_index>",
			},
		},
		"inspect-agent": {
			SchemaType: "inspect_agent",
			Sections:   []string{"sessions", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of a specific agent",
				SummaryHint: "Agent-specific state, history, and health",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
			},
		},
		"inspect-work": {
			SchemaType: "inspect_work",
			Sections:   []string{"work", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of a work item (bead)",
				SummaryHint: "Full bead details including dependencies and history",
			},
			FollowUp: &FollowUpInfo{
				RelatedSurfaces: []string{"triage", "plan", "graph"},
			},
		},
		"inspect-coordination": {
			SchemaType: "inspect_coordination",
			Sections:   []string{"coordination", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of coordination state",
				SummaryHint: "Agent Mail, reservations, and cross-agent state",
			},
		},
		"inspect-quota": {
			SchemaType: "inspect_quota",
			Sections:   []string{"quota", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of quota and rate-limit state",
				SummaryHint: "Detailed token/context budget information",
			},
		},
		"inspect-incident": {
			SchemaType: "inspect_incident",
			Sections:   []string{"incidents", "next_actions"},
			ConsumerGuidance: &ConsumerGuidance{
				IntendedUse: "Deep inspection of a specific incident",
				SummaryHint: "Full incident details including evidence and timeline",
			},
			Explainability: &ExplainabilityInfo{
				HasDiagnosticEntryPoints: true,
				DiagnosticHandleField:    "incident.diagnostic_id",
				HasEvidenceSummary:       true,
				EvidenceSummaryField:     "incident.evidence_summary",
			},
			ActionHandoff: &ActionHandoffInfo{
				SupportsActions:   true,
				ActionTypes:       []string{"acknowledge", "remediation"},
				RemediationFormat: "next_actions with recovery commands",
			},
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
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Use session_count, active_agents, and overall_health for dashboards",
				KeyFields:            []string{"session_count", "active_agents", "overall_health", "timestamp"},
				CompressionSemantics: "Always present; minimal overhead",
			},
		},
		"sessions": {
			Name:        "sessions",
			SchemaID:    defaultRobotSectionSchemaID("sessions"),
			Scope:       "global",
			Summary:     "Session inventory section",
			Description: "Lists sessions, panes, and agent-level runtime state for the current project.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Each session contains panes; each pane has agent_type and state",
				KeyFields:            []string{"name", "panes", "agent_count", "created_at"},
				CompressionSemantics: "May be paginated; use offset/limit for large inventories",
			},
			Boundedness: &SectionBoundedness{
				DefaultLimit:                  20,
				TruncationBehavior:            "Sessions beyond limit omitted",
				SupportsIndependentPagination: true,
			},
			Explainability: &SectionExplainability{
				HasDiagnosticLinks: true,
				DrillDownSurface:   "inspect-session",
			},
		},
		"work": {
			Name:        "work",
			SchemaID:    defaultRobotSectionSchemaID("work"),
			Scope:       "global",
			Summary:     "Work queue section",
			Description: "Captures bead, queue, assignment, or plan information that helps agents choose the next action.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Items sorted by priority; top items are highest impact",
				KeyFields:            []string{"id", "title", "priority", "status", "blockers"},
				CompressionSemantics: "Large work graphs may be summarized; use inspect for full details",
			},
			Boundedness: &SectionBoundedness{
				DefaultLimit:                  50,
				TruncationBehavior:            "Lower-priority items truncated",
				SupportsIndependentPagination: true,
			},
			Explainability: &SectionExplainability{
				HasDiagnosticLinks: true,
				DrillDownSurface:   "inspect-work",
			},
		},
		"coordination": {
			Name:        "coordination",
			SchemaID:    defaultRobotSectionSchemaID("coordination"),
			Scope:       "global",
			Summary:     "Coordination state section",
			Description: "Captures Agent Mail, reservation, and other cross-agent coordination state.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Check reservations and pending_messages for conflicts",
				KeyFields:   []string{"reservations", "pending_messages", "conflicts"},
			},
			Explainability: &SectionExplainability{
				HasDiagnosticLinks: true,
				DrillDownSurface:   "inspect-coordination",
			},
		},
		"quota": {
			Name:        "quota",
			SchemaID:    defaultRobotSectionSchemaID("quota"),
			Scope:       "global",
			Summary:     "Quota section",
			Description: "Summarizes token, context, or execution-budget capacity signals.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Check remaining_tokens and rate_limit_status for budget",
				KeyFields:   []string{"remaining_tokens", "rate_limit_status", "reset_at"},
			},
			Explainability: &SectionExplainability{
				DrillDownSurface: "inspect-quota",
			},
		},
		"alerts": {
			Name:        "alerts",
			SchemaID:    defaultRobotSectionSchemaID("alerts"),
			Scope:       "global",
			Summary:     "Alert section",
			Description: "Contains elevated warnings that may require immediate operator attention.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Alerts sorted by severity; process high-severity first",
				KeyFields:            []string{"id", "severity", "message", "source"},
				CompressionSemantics: "Similar alerts may be aggregated by source",
			},
			Boundedness: &SectionBoundedness{
				DefaultLimit:       20,
				TruncationBehavior: "Oldest low-severity alerts dropped first",
			},
		},
		"incidents": {
			Name:        "incidents",
			SchemaID:    defaultRobotSectionSchemaID("incidents"),
			Scope:       "global",
			Summary:     "Incident section",
			Description: "Describes degraded or failed runtime conditions and the evidence attached to them.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Each incident has evidence_summary and diagnostic_id for drill-down",
				KeyFields:   []string{"id", "status", "severity", "evidence_summary", "diagnostic_id"},
			},
			Explainability: &SectionExplainability{
				HasDiagnosticLinks: true,
				DrillDownSurface:   "inspect-incident",
			},
		},
		"attention": {
			Name:        "attention",
			SchemaID:    defaultRobotSectionSchemaID("attention"),
			Scope:       "global",
			Summary:     "Attention queue section",
			Description: "Summarizes attention-feed items, wake reasons, and prioritized actions for an operator loop.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Items ranked by priority; operator_state tracks acknowledgment",
				KeyFields:            []string{"id", "priority", "wake_reason", "operator_state", "aggregation_key"},
				CompressionSemantics: "Items with same aggregation_key are grouped; expand with inspect",
			},
			Boundedness: &SectionBoundedness{
				DefaultLimit:       50,
				TruncationBehavior: "Lower-priority items dropped; cursor enables continuation",
			},
			Explainability: &SectionExplainability{
				HasDiagnosticLinks: true,
			},
		},
		"source_health": {
			Name:        "source_health",
			SchemaID:    defaultRobotSectionSchemaID("source_health"),
			Scope:       "global",
			Summary:     "Source health section",
			Description: "Reports freshness and health of upstream adapters or runtime sources used to build the response.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Check status and last_success for each source",
				KeyFields:   []string{"source", "status", "last_success", "error"},
			},
		},
		"next_actions": {
			Name:        "next_actions",
			SchemaID:    defaultRobotSectionSchemaID("next_actions"),
			Scope:       "global",
			Summary:     "Next-actions section",
			Description: "Provides machine-readable follow-up actions or recommended next steps.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Each action has action_type, command, and reason",
				KeyFields:   []string{"action_type", "command", "reason", "priority"},
			},
		},
		"events": {
			Name:        "events",
			SchemaID:    defaultRobotSectionSchemaID("events"),
			Scope:       "global",
			Summary:     "Event replay section",
			Description: "Contains event log or replay payloads used for inspection, replay, or debugging.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint:          "Events ordered chronologically; use cursor for pagination",
				KeyFields:            []string{"id", "type", "timestamp", "source"},
				CompressionSemantics: "Events not aggregated; full fidelity preserved",
			},
			Boundedness: &SectionBoundedness{
				DefaultLimit:                  100,
				TruncationBehavior:            "Oldest events dropped; cursor enables continuation",
				SupportsIndependentPagination: true,
			},
		},
		"cursor": {
			Name:        "cursor",
			SchemaID:    defaultRobotSectionSchemaID("cursor"),
			Scope:       "global",
			Summary:     "Cursor handoff section",
			Description: "Carries cursor or resume-position data used for incremental polling and replay.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Pass cursor value to next request for continuation",
				KeyFields:   []string{"value", "expires_at", "resync_required"},
			},
		},
		"command_catalog": {
			Name:        "command_catalog",
			SchemaID:    defaultRobotSectionSchemaID("command_catalog"),
			Scope:       "global",
			Summary:     "Command catalog section",
			Description: "Describes the robot command catalog for help, capabilities, or other machine discovery surfaces.",
			ConsumerGuidance: &SectionConsumerGuidance{
				SummaryHint: "Use to discover available commands and their parameters",
				KeyFields:   []string{"name", "flag", "category", "parameters"},
			},
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
	if registrySurfaceOmitsSchemaType(name) {
		return ""
	}
	if _, ok := schemaByType[name]; ok {
		return name
	}
	candidate := strings.ReplaceAll(name, "-", "_")
	if _, ok := schemaByType[candidate]; ok {
		return candidate
	}
	return ""
}

func registrySurfaceOmitsSchemaType(name string) bool {
	switch name {
	case "attention":
		// The attention surface is versioned by the dedicated attention
		// contract/capabilities metadata rather than the generic schema_type
		// projection used by most robot surfaces.
		return true
	default:
		return false
	}
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
		r := []rune(word)
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
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
		cloned[i].ConsumerGuidance = cloneConsumerGuidance(surface.ConsumerGuidance)
		cloned[i].Boundedness = cloneBoundednessInfo(surface.Boundedness)
		cloned[i].FollowUp = cloneFollowUpInfo(surface.FollowUp)
		cloned[i].ActionHandoff = cloneActionHandoffInfo(surface.ActionHandoff)
		cloned[i].RequestSemantics = cloneRequestSemantics(surface.RequestSemantics)
		cloned[i].AttentionOps = cloneAttentionOpsInfo(surface.AttentionOps)
		cloned[i].Explainability = cloneExplainabilityInfo(surface.Explainability)
		cloned[i].Lifecycle = cloneLifecycleInfo(surface.Lifecycle)
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
	for i, section := range sections {
		cloned[i] = section
		cloned[i].ConsumerGuidance = cloneSectionConsumerGuidance(section.ConsumerGuidance)
		cloned[i].Boundedness = cloneSectionBoundedness(section.Boundedness)
		cloned[i].Explainability = cloneSectionExplainability(section.Explainability)
	}
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
		copied.ConsumerGuidance = cloneConsumerGuidance(surface.ConsumerGuidance)
		copied.Boundedness = cloneBoundednessInfo(surface.Boundedness)
		copied.FollowUp = cloneFollowUpInfo(surface.FollowUp)
		copied.ActionHandoff = cloneActionHandoffInfo(surface.ActionHandoff)
		copied.RequestSemantics = cloneRequestSemantics(surface.RequestSemantics)
		copied.AttentionOps = cloneAttentionOpsInfo(surface.AttentionOps)
		copied.Explainability = cloneExplainabilityInfo(surface.Explainability)
		copied.Lifecycle = cloneLifecycleInfo(surface.Lifecycle)
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
		copied := section
		copied.ConsumerGuidance = cloneSectionConsumerGuidance(section.ConsumerGuidance)
		copied.Boundedness = cloneSectionBoundedness(section.Boundedness)
		copied.Explainability = cloneSectionExplainability(section.Explainability)
		cloned[name] = copied
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

// Clone functions for consumer metadata types

func cloneConsumerGuidance(src *ConsumerGuidance) *ConsumerGuidance {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneBoundednessInfo(src *BoundednessInfo) *BoundednessInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneFollowUpInfo(src *FollowUpInfo) *FollowUpInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.InspectSurfaces = cloneStrings(src.InspectSurfaces)
	cloned.RelatedSurfaces = cloneStrings(src.RelatedSurfaces)
	return &cloned
}

func cloneActionHandoffInfo(src *ActionHandoffInfo) *ActionHandoffInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.ActionTypes = cloneStrings(src.ActionTypes)
	return &cloned
}

func cloneRequestSemantics(src *RequestSemantics) *RequestSemantics {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneAttentionOpsInfo(src *AttentionOpsInfo) *AttentionOpsInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneExplainabilityInfo(src *ExplainabilityInfo) *ExplainabilityInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneLifecycleInfo(src *LifecycleInfo) *LifecycleInfo {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneSectionConsumerGuidance(src *SectionConsumerGuidance) *SectionConsumerGuidance {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.KeyFields = cloneStrings(src.KeyFields)
	return &cloned
}

func cloneSectionBoundedness(src *SectionBoundedness) *SectionBoundedness {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func cloneSectionExplainability(src *SectionExplainability) *SectionExplainability {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}
