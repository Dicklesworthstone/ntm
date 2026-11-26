// Package bv provides integration with the beads_viewer (bv) tool
package bv

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InsightsResponse contains graph analysis insights
type InsightsResponse struct {
	Bottlenecks []NodeScore `json:"Bottlenecks,omitempty"`
	Keystones   []NodeScore `json:"Keystones,omitempty"`
	Hubs        []NodeScore `json:"Hubs,omitempty"`
	Authorities []NodeScore `json:"Authorities,omitempty"`
	// Cycles is bv's analysis.Insights.Cycles: each cycle is its list of
	// issue IDs. Decoding it as objects failed the whole response whenever
	// the graph had a cycle.
	Cycles [][]string `json:"Cycles,omitempty"`
}

// NodeScore represents a node with its metric score
type NodeScore struct {
	ID    string  `json:"ID"`
	Value float64 `json:"Value"`
}

// PriorityResponse contains priority recommendations
type PriorityResponse struct {
	GeneratedAt     time.Time                `json:"generated_at"`
	Recommendations []PriorityRecommendation `json:"recommendations"`
}

// PriorityRecommendation suggests priority adjustments
type PriorityRecommendation struct {
	IssueID           string   `json:"issue_id"`
	Title             string   `json:"title"`
	CurrentPriority   int      `json:"current_priority"`
	SuggestedPriority int      `json:"suggested_priority"`
	ImpactScore       float64  `json:"impact_score"`
	Confidence        float64  `json:"confidence"`
	Reasoning         []string `json:"reasoning"`
	Direction         string   `json:"direction"` // "increase" or "decrease"
}

// PlanResponse contains parallel work plan
type PlanResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Plan        Plan      `json:"plan"`
}

// Plan represents a parallel execution plan
type Plan struct {
	Tracks []Track `json:"tracks"`
}

// Track is a sequence of items to work on
type Track struct {
	TrackID string     `json:"track_id"`
	Items   []PlanItem `json:"items"`
	Reason  string     `json:"reason"`
}

// PlanItem is an item in a work track
type PlanItem struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Priority int      `json:"priority"`
	Status   string   `json:"status"`
	Unblocks []string `json:"unblocks"`
}

// RecipesResponse contains available recipes
type RecipesResponse struct {
	Recipes []Recipe `json:"recipes"`
}

// Recipe describes a filtering/sorting recipe
type Recipe struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "builtin" or "custom"
}

// DriftStatus represents the project drift state
type DriftStatus int

const (
	// DriftOK indicates no significant drift
	DriftOK DriftStatus = 0
	// DriftCritical indicates critical drift from baseline
	DriftCritical DriftStatus = 1
	// DriftWarning indicates minor drift from baseline
	DriftWarning DriftStatus = 2
	// DriftNoBaseline indicates no baseline exists
	DriftNoBaseline DriftStatus = 3
)

// String returns a human-readable drift status
func (d DriftStatus) String() string {
	switch d {
	case DriftOK:
		return "OK"
	case DriftCritical:
		return "critical"
	case DriftWarning:
		return "warning"
	case DriftNoBaseline:
		return "no baseline"
	default:
		return "unknown"
	}
}

// MarshalJSON encodes DriftStatus as its canonical string label rather than a raw enum integer.
func (d DriftStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts either canonical string labels or legacy integer enum codes.
func (d *DriftStatus) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "ok":
			*d = DriftOK
		case "critical":
			*d = DriftCritical
		case "warning":
			*d = DriftWarning
		case "no baseline", "no_baseline":
			*d = DriftNoBaseline
		case "unknown":
			*d = DriftStatus(-1)
		default:
			return fmt.Errorf("unknown drift status %q", s)
		}
		return nil
	}
	var code int
	if err := json.Unmarshal(data, &code); err != nil {
		return err
	}
	*d = DriftStatus(code)
	return nil
}

// DriftResult contains drift check results
type DriftResult struct {
	Status  DriftStatus
	Message string
}

// BeadsSummary provides issue tracking stats
type BeadsSummary struct {
	Available      bool             `json:"available"`
	Reason         string           `json:"reason,omitempty"` // Reason if not available
	Project        string           `json:"project,omitempty"`
	Total          int              `json:"total,omitempty"`
	Open           int              `json:"open,omitempty"`
	InProgress     int              `json:"in_progress,omitempty"`
	Blocked        int              `json:"blocked,omitempty"`
	Ready          int              `json:"ready,omitempty"`
	Closed         int              `json:"closed,omitempty"`
	ReadyPreview   []BeadPreview    `json:"ready_preview,omitempty"`
	InProgressList []BeadInProgress `json:"in_progress_list,omitempty"`
}

// BeadPreview is a minimal bead representation for ready items
type BeadPreview struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority string `json:"priority"`       // e.g., "P0", "P1"
	Type     string `json:"type,omitempty"` // task, bug, feature, epic, etc.
}

// BeadInProgress represents an in-progress bead with assignee
type BeadInProgress struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Assignee  string    `json:"assignee,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// TriageResponse contains the complete -robot-triage output
type TriageResponse struct {
	GeneratedAt time.Time  `json:"generated_at"`
	DataHash    string     `json:"data_hash"`
	Triage      TriageData `json:"triage"`
}

// TriageData contains the triage analysis
type TriageData struct {
	Meta            TriageMeta             `json:"meta"`
	QuickRef        TriageQuickRef         `json:"quick_ref"`
	Recommendations []TriageRecommendation `json:"recommendations"`
	QuickWins       []TriageRecommendation `json:"quick_wins,omitempty"`
	BlockersToClear []BlockerToClear       `json:"blockers_to_clear,omitempty"`
	ProjectHealth   *ProjectHealth         `json:"project_health,omitempty"`
	Commands        map[string]string      `json:"commands,omitempty"`
}

// TriageMeta contains metadata about the triage
type TriageMeta struct {
	Version       string    `json:"version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Phase2Ready   bool      `json:"phase2_ready"`
	IssueCount    int       `json:"issue_count"`
	ComputeTimeMs int       `json:"compute_time_ms"`
}

// TriageQuickRef provides at-a-glance counts and top picks
type TriageQuickRef struct {
	OpenCount       int             `json:"open_count"`
	ActionableCount int             `json:"actionable_count"`
	BlockedCount    int             `json:"blocked_count"`
	InProgressCount int             `json:"in_progress_count"`
	TopPicks        []TriageTopPick `json:"top_picks"`
}

// TriageTopPick is a compact recommendation for quick reference
type TriageTopPick struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
	Unblocks int      `json:"unblocks"`
}

// TriageRecommendation is a full recommendation with scoring breakdown
type TriageRecommendation struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Priority    int             `json:"priority"`
	Labels      []string        `json:"labels,omitempty"`
	Score       float64         `json:"score"`
	Breakdown   *ScoreBreakdown `json:"breakdown,omitempty"`
	Action      string          `json:"action"`
	Reasons     []string        `json:"reasons"`
	UnblocksIDs []string        `json:"unblocks_ids,omitempty"`
	BlockedBy   []string        `json:"blocked_by,omitempty"` // IDs that block this item
}

// BlockerToClear represents a blocker item from blockers_to_clear response
type BlockerToClear struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	UnblocksCount int      `json:"unblocks_count"`
	UnblocksIDs   []string `json:"unblocks_ids,omitempty"`
	Actionable    bool     `json:"actionable"`
	BlockedBy     []string `json:"blocked_by,omitempty"`
}

// ScoreBreakdown contains the components of a recommendation score
type ScoreBreakdown struct {
	Pagerank                float64 `json:"pagerank"`
	Betweenness             float64 `json:"betweenness"`
	BlockerRatio            float64 `json:"blocker_ratio"`
	Staleness               float64 `json:"staleness"`
	PriorityBoost           float64 `json:"priority_boost"`
	TimeToImpact            float64 `json:"time_to_impact"`
	Urgency                 float64 `json:"urgency"`
	Risk                    float64 `json:"risk"`
	TimeToImpactExplanation string  `json:"time_to_impact_explanation,omitempty"`
	UrgencyExplanation      string  `json:"urgency_explanation,omitempty"`
	RiskExplanation         string  `json:"risk_explanation,omitempty"`
}

// ProjectHealth contains overall project health metrics
type ProjectHealth struct {
	Total                int            `json:"total,omitempty"`
	StatusDistribution   map[string]int `json:"status_distribution,omitempty"`
	TypeDistribution     map[string]int `json:"type_distribution,omitempty"`
	PriorityDistribution map[string]int `json:"priority_distribution,omitempty"`
	GraphMetrics         *GraphMetrics  `json:"graph_metrics,omitempty"`
}

// UnmarshalJSON reads bv's triage project_health, counts{total, by_status,
// by_type, by_priority} and graph{node_count, edge_count, density,
// has_cycles, cycle_count} (bv pkg/analysis/triage.go), and still accepts the
// older status_distribution/graph_metrics layout. Reading only the older names
// left the health section of every triage empty against current bv.
func (h *ProjectHealth) UnmarshalJSON(data []byte) error {
	type legacyProjectHealth ProjectHealth
	var wire struct {
		legacyProjectHealth
		Counts *struct {
			Total      int            `json:"total"`
			ByStatus   map[string]int `json:"by_status"`
			ByType     map[string]int `json:"by_type"`
			ByPriority map[string]int `json:"by_priority"`
		} `json:"counts"`
		Graph *struct {
			NodeCount  int     `json:"node_count"`
			EdgeCount  int     `json:"edge_count"`
			Density    float64 `json:"density"`
			HasCycles  bool    `json:"has_cycles"`
			CycleCount int     `json:"cycle_count"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*h = ProjectHealth(wire.legacyProjectHealth)
	if c := wire.Counts; c != nil {
		if h.Total == 0 {
			h.Total = c.Total
		}
		if h.StatusDistribution == nil {
			h.StatusDistribution = c.ByStatus
		}
		if h.TypeDistribution == nil {
			h.TypeDistribution = c.ByType
		}
		if h.PriorityDistribution == nil {
			h.PriorityDistribution = c.ByPriority
		}
	}
	if g := wire.Graph; g != nil && h.GraphMetrics == nil {
		h.GraphMetrics = &GraphMetrics{
			TotalNodes: g.NodeCount,
			TotalEdges: g.EdgeCount,
			Density:    g.Density,
			HasCycles:  g.HasCycles,
			CycleCount: g.CycleCount,
		}
	}
	return nil
}

// GraphMetrics contains graph-level metrics
type GraphMetrics struct {
	TotalNodes int     `json:"total_nodes"`
	TotalEdges int     `json:"total_edges"`
	Density    float64 `json:"density"`
	AvgDegree  float64 `json:"avg_degree"`
	MaxDepth   int     `json:"max_depth"`
	HasCycles  bool    `json:"has_cycles,omitempty"`
	CycleCount int     `json:"cycle_count"`
}

// ForecastResponse is the envelope emitted by bv --robot-forecast.
type ForecastResponse struct {
	GeneratedAt   time.Time       `json:"generated_at"`
	DataHash      string          `json:"data_hash"`
	OutputFormat  string          `json:"output_format"`
	Version       string          `json:"version"`
	Agents        int             `json:"agents"`
	ForecastCount int             `json:"forecast_count"`
	Forecasts     []ForecastItem  `json:"forecasts"`
	Summary       ForecastSummary `json:"summary"`
}

// ForecastItem represents one issue forecast emitted by bv. bv does not
// include issue titles in this surface, so callers must not present an empty
// title as though it were authoritative data.
type ForecastItem struct {
	IssueID               string    `json:"issue_id"`
	EstimatedMinutes      int       `json:"estimated_minutes"`
	EstimatedDays         float64   `json:"estimated_days"`
	ETADate               time.Time `json:"eta_date"`
	ETADateLow            time.Time `json:"eta_date_low"`
	ETADateHigh           time.Time `json:"eta_date_high"`
	Confidence            float64   `json:"confidence"`
	VelocityMinutesPerDay float64   `json:"velocity_minutes_per_day"`
	Agents                int       `json:"agents"`
	Factors               []string  `json:"factors,omitempty"`
}

// ForecastSummary is the aggregate capacity projection emitted with a bv
// forecast response.
type ForecastSummary struct {
	TotalMinutes  int       `json:"total_minutes"`
	TotalDays     float64   `json:"total_days"`
	AvgConfidence float64   `json:"avg_confidence"`
	EarliestETA   time.Time `json:"earliest_eta"`
	LatestETA     time.Time `json:"latest_eta"`
}

// SuggestionsResponse is bv --robot-suggest output. The hygiene suggestions
// sit in a SuggestionSet under "suggestions" (bv analysis.SuggestionSet);
// decoding that object as a list failed every --robot-suggest call.
type SuggestionsResponse struct {
	Suggestions SuggestionSet `json:"suggestions"`
}

// SuggestionSet is bv's analysis.SuggestionSet.
type SuggestionSet struct {
	Suggestions []Suggestion    `json:"suggestions"`
	GeneratedAt time.Time       `json:"generated_at"`
	DataHash    string          `json:"data_hash,omitempty"`
	Stats       SuggestionStats `json:"stats"`
}

// Suggestion is one bv hygiene suggestion (bv analysis.Suggestion).
type Suggestion struct {
	Type          string  `json:"type"`
	TargetBead    string  `json:"target_bead"`
	RelatedBead   string  `json:"related_bead,omitempty"`
	Summary       string  `json:"summary"`
	Reason        string  `json:"reason"`
	Confidence    float64 `json:"confidence"`
	ActionCommand string  `json:"action_command,omitempty"`
}

// SuggestionStats summarizes a SuggestionSet.
type SuggestionStats struct {
	Total               int            `json:"total"`
	ByType              map[string]int `json:"by_type"`
	ByConfidence        map[string]int `json:"by_confidence"`
	HighConfidenceCount int            `json:"high_confidence_count"`
}

// ImpactResponse is the envelope emitted by bv --robot-impact.
type ImpactResponse struct {
	GeneratedAt   time.Time            `json:"generated_at"`
	DataHash      string               `json:"data_hash"`
	OutputFormat  string               `json:"output_format"`
	Version       string               `json:"version"`
	Files         []string             `json:"files"`
	RiskLevel     string               `json:"risk_level"`
	RiskScore     float64              `json:"risk_score"`
	Summary       string               `json:"summary"`
	Warnings      []string             `json:"warnings"`
	AffectedBeads []ImpactAffectedBead `json:"affected_beads"`
}

// ImpactAffectedBead is a bead whose recorded file changes overlap an impact query.
type ImpactAffectedBead struct {
	BeadID       string    `json:"bead_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	OverlapFiles []string  `json:"overlap_files"`
	OverlapCount int       `json:"overlap_count"`
	LastActivity time.Time `json:"last_activity"`
	Relevance    float64   `json:"relevance"`
	TotalChanges int       `json:"total_changes"`
}

// SearchResponse is the envelope emitted by bv --robot-search.
type SearchResponse struct {
	GeneratedAt time.Time      `json:"generated_at"`
	DataHash    string         `json:"data_hash"`
	Query       string         `json:"query"`
	Results     []SearchResult `json:"results"`
}

// SearchResult represents a single search result
type SearchResult struct {
	IssueID string  `json:"issue_id"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
}

// LabelAttentionResponse contains attention-ranked labels
type LabelAttentionResponse struct {
	Labels []LabelAttention `json:"labels"`
}

// LabelAttention represents a label with attention score
type LabelAttention struct {
	Rank            int     `json:"rank"`
	Label           string  `json:"label"`
	AttentionScore  float64 `json:"attention_score"`
	NormalizedScore float64 `json:"normalized_score"`
	Reason          string  `json:"reason"`
	OpenCount       int     `json:"open_count"`
	BlockedCount    int     `json:"blocked_count"`
	StaleCount      int     `json:"stale_count"`
	PageRankSum     float64 `json:"pagerank_sum"`
	VelocityFactor  float64 `json:"velocity_factor"`
}

// LabelHealthResponse contains health metrics per label
type LabelHealthResponse struct {
	Results LabelHealthResults `json:"results"`
}

// LabelHealthResults contains the actual health data
type LabelHealthResults struct {
	Labels []LabelHealth `json:"labels"`
}

// LabelHealth contains health metrics for a single label
type LabelHealth struct {
	Label        string               `json:"label"`
	IssueCount   int                  `json:"issue_count"`
	OpenCount    int                  `json:"open_count"`
	ClosedCount  int                  `json:"closed_count"`
	BlockedCount int                  `json:"blocked_count"`
	Health       float64              `json:"health"`
	HealthLevel  string               `json:"health_level"` // healthy, warning, critical
	Velocity     LabelHealthVelocity  `json:"velocity"`
	Freshness    LabelHealthFreshness `json:"freshness"`
}

// LabelHealthVelocity contains the recent close-rate metrics for a label.
type LabelHealthVelocity struct {
	ClosedLast7Days  int     `json:"closed_last_7_days"`
	ClosedLast30Days int     `json:"closed_last_30_days"`
	AvgDaysToClose   float64 `json:"avg_days_to_close"`
	TrendDirection   string  `json:"trend_direction"`
	TrendPercent     float64 `json:"trend_percent"`
	VelocityScore    float64 `json:"velocity_score"`
}

// LabelHealthFreshness contains the staleness metrics for a label.
type LabelHealthFreshness struct {
	MostRecentUpdate   time.Time `json:"most_recent_update"`
	OldestOpenIssue    time.Time `json:"oldest_open_issue"`
	AvgDaysSinceUpdate float64   `json:"avg_days_since_update"`
	StaleCount         int       `json:"stale_count"`
	StaleThresholdDays int       `json:"stale_threshold_days"`
	FreshnessScore     float64   `json:"freshness_score"`
}

// LabelFlowResponse contains cross-label dependency analysis
type LabelFlowResponse struct {
	GeneratedAt time.Time     `json:"generated_at"`
	DataHash    string        `json:"data_hash"`
	Flow        LabelFlowData `json:"flow"`
}

// LabelFlowData contains the label ordering, adjacency matrix, and dependency edges.
type LabelFlowData struct {
	Labels           []string          `json:"labels"`
	FlowMatrix       [][]int           `json:"flow_matrix"`
	Dependencies     []LabelDependency `json:"dependencies"`
	BottleneckLabels []string          `json:"bottleneck_labels"`
}

// LabelDependency represents a dependency between labels
type LabelDependency struct {
	FromLabel  string   `json:"from_label"`
	ToLabel    string   `json:"to_label"`
	IssueCount int      `json:"issue_count"`
	IssueIDs   []string `json:"issue_ids"`
}

// FileBeadsResponse contains file-to-bead mapping
// The --robot-file-* shapes below follow bv's cmd/bv/robot_registry.go and
// pkg/correlation types. ntm decoded invented shapes (files[], path/score,
// relations[]), so these surfaces returned success with empty data.
type FileBeadsResponse struct {
	FilePath    string          `json:"file_path"`
	TotalBeads  int             `json:"total_beads"`
	OpenBeads   []BeadReference `json:"open_beads"`
	ClosedBeads []BeadReference `json:"closed_beads"`
}

// BeadReference is a bead that touched a file (bv correlation.BeadReference).
type BeadReference struct {
	BeadID       string    `json:"bead_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	CommitSHAs   []string  `json:"commit_shas"`
	LastTouch    time.Time `json:"last_touch"`
	TotalChanges int       `json:"total_changes"`
}

// FileHotspotsResponse is bv --robot-file-hotspots output.
type FileHotspotsResponse struct {
	Hotspots []FileHotspot  `json:"hotspots"`
	Stats    map[string]any `json:"stats,omitempty"`
}

// FileHotspot is a file touched by many beads (bv correlation.FileHotspot).
type FileHotspot struct {
	FilePath    string `json:"file_path"`
	TotalBeads  int    `json:"total_beads"`
	OpenBeads   int    `json:"open_beads"`
	ClosedBeads int    `json:"closed_beads"`
}

// FileRelationsResponse is bv --robot-file-relations output.
type FileRelationsResponse struct {
	FilePath     string          `json:"file_path"`
	TotalCommits int             `json:"total_commits"`
	Threshold    float64         `json:"threshold"`
	RelatedFiles []CoChangeEntry `json:"related_files"`
}

// CoChangeEntry is a file that changes together with the queried one (bv
// correlation.CoChangeEntry).
type CoChangeEntry struct {
	FilePath      string   `json:"file_path"`
	CoChangeCount int      `json:"co_change_count"`
	TotalCommits  int      `json:"total_commits"`
	Correlation   float64  `json:"correlation"`
	SampleCommits []string `json:"sample_commits"`
}
