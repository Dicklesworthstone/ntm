// Package bv provides integration with the beads_viewer (bv) tool
package bv

import "time"

// InsightsResponse contains graph analysis insights
type InsightsResponse struct {
	Bottlenecks []NodeScore `json:"Bottlenecks,omitempty"`
	Keystones   []NodeScore `json:"Keystones,omitempty"`
	Hubs        []NodeScore `json:"Hubs,omitempty"`
	Authorities []NodeScore `json:"Authorities,omitempty"`
	Cycles      []Cycle     `json:"Cycles,omitempty"`
}

// Cycle represents a dependency cycle
type Cycle struct {
	Nodes []string `json:"nodes"`
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
	Priority string `json:"priority"` // e.g., "P0", "P1"
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
	BlockersToClear []TriageRecommendation `json:"blockers_to_clear,omitempty"`
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
	StatusDistribution   map[string]int `json:"status_distribution,omitempty"`
	TypeDistribution     map[string]int `json:"type_distribution,omitempty"`
	PriorityDistribution map[string]int `json:"priority_distribution,omitempty"`
	GraphMetrics         *GraphMetrics  `json:"graph_metrics,omitempty"`
}

// GraphMetrics contains graph-level metrics
type GraphMetrics struct {
	TotalNodes int     `json:"total_nodes"`
	TotalEdges int     `json:"total_edges"`
	Density    float64 `json:"density"`
	AvgDegree  float64 `json:"avg_degree"`
	MaxDepth   int     `json:"max_depth"`
	CycleCount int     `json:"cycle_count"`
}
