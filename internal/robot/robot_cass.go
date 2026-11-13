package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func init() {
	MustRegisterSchemaPagination("cass_status", SchemaPaginationFlag{
		Reason: "bounded: fixed capability feature and connector sets from CASS",
	})
}

// CASSStatusOutput represents the output for --robot-cass-status
type CASSStatusOutput struct {
	RobotResponse
	CASSAvailable         bool           `json:"cass_available"`
	Healthy               bool           `json:"healthy"`
	Index                 CASSIndexStats `json:"index"`
	Features              []string       `json:"features,omitempty"`
	Connectors            []string       `json:"connectors,omitempty"`
	ActiveAgentConnectors []string       `json:"active_agent_connectors,omitempty"`
	SupportsAggregation   bool           `json:"supports_aggregation,omitempty"`
	SupportsTimeline      bool           `json:"supports_timeline,omitempty"`
}

// CASSIndexStats holds index statistics
type CASSIndexStats struct {
	Exists        bool   `json:"exists"`
	Fresh         bool   `json:"fresh"`
	LastIndexedAt int64  `json:"last_indexed_at"`
	Conversations *int64 `json:"conversations,omitempty"`
	Messages      *int64 `json:"messages,omitempty"`
	CountsSkipped bool   `json:"counts_skipped,omitempty"`
}

// GetCASSStatus collects CASS health and stats.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSStatus() (*CASSStatusOutput, error) {
	ctx := context.Background()
	client := cass.NewClient()
	status, err := client.Status(ctx)

	cassAvailable := client.IsInstalled()
	output := &CASSStatusOutput{
		RobotResponse: NewRobotResponse(true),
		CASSAvailable: cassAvailable,
		Healthy:       false,
		Index:         CASSIndexStats{},
	}

	if !cassAvailable {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("cass not installed"),
			ErrCodeDependencyMissing,
			"Install cass to enable search and context",
		)
		return output, nil
	}

	if err == nil {
		output.Healthy = status.IsHealthy()
		if !output.Healthy {
			output.Healthy = client.IsHealthy(ctx)
		}
		output.Index.Exists = status.Index.Exists
		output.Index.Fresh = status.Index.Healthy
		output.Index.LastIndexedAt = status.LastIndexedAt.UnixMilli()
		output.Index.CountsSkipped = status.Database.CountsSkipped
		if !status.Database.CountsSkipped {
			output.Index.Conversations = &status.Conversations
			output.Index.Messages = &status.Messages
		}
		if caps, capErr := client.Capabilities(ctx); capErr == nil && caps != nil {
			output.Features = caps.Features
			output.Connectors = caps.Connectors
			output.SupportsAggregation = caps.HasFeature("aggregations") || caps.HasFeature("aggregate")
			output.SupportsTimeline = caps.HasFeature("timeline")
			for _, candidate := range []string{"claude_code", "codex", "gemini", "cursor", "aider"} {
				if caps.HasConnector(candidate) {
					output.ActiveAgentConnectors = append(output.ActiveAgentConnectors, candidate)
				}
			}
		}
	} else {
		output.RobotResponse = cassErrorResponse(err, "Check cass index health and configuration")
	}

	return output, nil
}

// PrintCASSStatus outputs CASS health and stats as JSON.
// This is a thin wrapper around GetCASSStatus() for CLI output.
func PrintCASSStatus() error {
	output, err := GetCASSStatus()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot CASS status failed")
}

// CASSSearchOutput represents the output for --robot-cass-search
type CASSSearchOutput struct {
	RobotResponse
	Query        string          `json:"query"`
	Count        int             `json:"count"`
	TotalMatches int             `json:"total_matches"`
	Hits         []CASSSearchHit `json:"hits"`
}

// CASSSearchHit represents a single hit in robot search output
type CASSSearchHit struct {
	SourcePath string  `json:"source_path"`
	Agent      string  `json:"agent"`
	Title      string  `json:"title"`
	Score      float64 `json:"score"`
	Snippet    string  `json:"snippet"`
	CreatedAt  int64   `json:"created_at"`
}

// CASSSearchOptions configures the GetCASSSearch operation.
type CASSSearchOptions struct {
	Query     string
	Agent     string
	Workspace string
	Since     string
	Limit     int
}

// GetCASSSearch performs a CASS search and returns the results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSSearch(opts CASSSearchOptions) (*CASSSearchOutput, error) {
	client := cass.NewClient()
	if !client.IsInstalled() {
		return &CASSSearchOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("cass not installed"),
				ErrCodeDependencyMissing,
				"Install cass to enable search",
			),
			Query: opts.Query,
			Hits:  []CASSSearchHit{},
		}, nil
	}
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query:     opts.Query,
		Agent:     opts.Agent,
		Workspace: opts.Workspace,
		Since:     opts.Since,
		Limit:     opts.Limit,
	})

	if err != nil {
		return &CASSSearchOutput{
			RobotResponse: cassErrorResponse(err, "Check cass index health and query parameters"),
			Query:         opts.Query,
			Hits:          []CASSSearchHit{},
		}, nil
	}

	output := &CASSSearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         resp.Query,
		Count:         resp.Count,
		TotalMatches:  resp.TotalMatches,
		Hits:          make([]CASSSearchHit, len(resp.Hits)),
	}

	for i, hit := range resp.Hits {
		createdAt := int64(0)
		if hit.CreatedAt != nil {
			createdAt = hit.CreatedAt.UnixMilli() // Convert to ms
		}
		output.Hits[i] = CASSSearchHit{
			SourcePath: hit.SourcePath,
			Agent:      hit.Agent,
			Title:      hit.Title,
			Score:      hit.Score,
			Snippet:    hit.Snippet,
			CreatedAt:  createdAt,
		}
	}

	return output, nil
}

// PrintCASSSearch outputs search results as JSON.
// This is a thin wrapper around GetCASSSearch() for CLI output.
func PrintCASSSearch(query, agent, workspace, since string, limit int) error {
	output, err := GetCASSSearch(CASSSearchOptions{
		Query:     query,
		Agent:     agent,
		Workspace: workspace,
		Since:     since,
		Limit:     limit,
	})
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot CASS search failed")
}

// CASSInsightsOutput represents the output for --robot-cass-insights
type CASSInsightsOutput struct {
	RobotResponse
	Period     string                   `json:"period"`
	Agents     []cass.AggregationBucket `json:"agents"`
	Workspaces []cass.AggregationBucket `json:"workspaces"`
}

// GetCASSInsights returns which agents and workspaces dominate the indexed
// sessions since `since` (default 7d), from cass's server-side aggregations.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSInsights(since string) (*CASSInsightsOutput, error) {
	if since == "" {
		since = "7d"
	}
	output := &CASSInsightsOutput{
		Period:     since,
		Agents:     []cass.AggregationBucket{},
		Workspaces: []cass.AggregationBucket{},
	}

	client := cass.NewClient()
	if !client.IsInstalled() {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("cass not installed"),
			ErrCodeDependencyMissing,
			"Install cass to enable insights",
		)
		return output, nil
	}
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query:     "*",
		Since:     output.Period,
		Limit:     0,
		Aggregate: "agent,workspace",
	})
	if err != nil {
		output.RobotResponse = cassErrorResponse(err, "Check cass index health and configuration")
		return output, nil
	}

	output.RobotResponse = NewRobotResponse(true)
	if agg := resp.Aggregations; agg != nil {
		if agg.Agent != nil {
			output.Agents = agg.Agent.Buckets
		}
		if agg.Workspace != nil {
			output.Workspaces = agg.Workspace.Buckets
		}
	}
	return output, nil
}

// PrintCASSInsights outputs aggregated insights as JSON.
// This is a thin wrapper around GetCASSInsights() for CLI output.
func PrintCASSInsights(since string) error {
	output, err := GetCASSInsights(since)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot CASS insights failed")
}

// CASSContextOutput represents output for --robot-cass-context
type CASSContextOutput struct {
	RobotResponse
	Query            string               `json:"query"`
	RelevantSessions []CASSContextSession `json:"relevant_sessions"`
	SuggestedContext string               `json:"suggested_context"`
}

// CASSContextSession represents a session in context output
type CASSContextSession struct {
	Summary   string   `json:"summary"`
	KeyPoints []string `json:"key_points,omitempty"`
	Source    string   `json:"source"`
	Agent     string   `json:"agent"`
	When      string   `json:"when"`
}

// GetCASSContext returns relevant past context for spawning.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSContext(query string) (*CASSContextOutput, error) {
	client := cass.NewClient()
	if !client.IsInstalled() {
		return &CASSContextOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("cass not installed"),
				ErrCodeDependencyMissing,
				"Install cass to enable context search",
			),
			Query:            query,
			RelevantSessions: []CASSContextSession{},
		}, nil
	}
	ctx := context.Background()
	workspace, _ := os.Getwd()
	// Search workspace-scoped past context first, falling back to global context if empty.
	resp, err := client.SearchForContext(ctx, query, workspace)
	if err == nil && workspace != "" && !resp.HasResults() {
		if globalResp, globalErr := client.SearchForContext(ctx, query, ""); globalErr == nil {
			resp = globalResp
		}
	}

	if err != nil {
		return &CASSContextOutput{
			RobotResponse:    cassErrorResponse(err, "Check cass index health"),
			Query:            query,
			RelevantSessions: []CASSContextSession{},
		}, nil
	}

	output := &CASSContextOutput{
		RobotResponse:    NewRobotResponse(true),
		Query:            query,
		RelevantSessions: []CASSContextSession{},
	}

	var suggestions []string

	if resp.HasResults() {
		for _, hit := range resp.Hits {
			when := "unknown"
			if hit.CreatedAt != nil {
				ts := hit.CreatedAt.Time
				when = ts.Format("2006-01-02")
			}

			session := CASSContextSession{
				Summary: hit.Title,
				Source:  hit.SourcePath,
				Agent:   hit.Agent,
				When:    when,
			}
			// KeyPoints left nil (omitted from JSON via omitempty) because
			// CASS SearchHit does not provide structured key-point data.

			output.RelevantSessions = append(output.RelevantSessions, session)
			suggestions = append(suggestions, fmt.Sprintf("session '%s' (%s)", hit.Title, hit.Agent))
		}
	}

	if len(suggestions) > 0 {
		output.SuggestedContext = fmt.Sprintf("Consider reviewing: %s", strings.Join(suggestions, ", "))
	}

	return output, nil
}

// PrintCASSContext outputs relevant past context for spawning.
// This is a thin wrapper around GetCASSContext() for CLI output.
func PrintCASSContext(query string) error {
	output, err := GetCASSContext(query)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot CASS context failed")
}

// ===========================================================================
// ACFS (Flywheel Setup) Robot Wrappers
// ===========================================================================

// SetupToolStatus represents a single tool status in setup checks.
type SetupToolStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Hint      string `json:"hint,omitempty"`
	Required  bool   `json:"required,omitempty"`
}

// ACFSStatusOutput represents the output for --robot-acfs-status / --robot-setup.
type ACFSStatusOutput struct {
	RobotResponse
	ACFSAvailable bool                       `json:"acfs_available"`
	ACFSVersion   string                     `json:"acfs_version,omitempty"`
	ACFSPath      string                     `json:"acfs_path,omitempty"`
	Tools         map[string]SetupToolStatus `json:"tools"`
}

type setupToolSpec struct {
	Key         string
	Command     string
	VersionArgs []string
	Hint        string
	Required    bool
}

var setupToolSpecs = []setupToolSpec{
	{
		Key:         "tmux",
		Command:     "tmux",
		VersionArgs: []string{"-V"},
		Hint:        "brew install tmux (macOS) / apt install tmux (Linux)",
		Required:    true,
	},
	{
		Key:         "br",
		Command:     "br",
		VersionArgs: []string{"--version"},
		Hint:        "Install beads_rust (br)",
	},
	{
		Key:         "bv",
		Command:     "bv",
		VersionArgs: []string{"--version"},
		Hint:        "Install beads_viewer (bv)",
	},
	{
		Key:         "cc",
		Command:     "claude",
		VersionArgs: []string{"--version"},
		Hint:        "npm install -g @anthropic-ai/claude-code",
	},
	{
		Key:         "cod",
		Command:     "codex",
		VersionArgs: []string{"--version"},
		Hint:        "npm install -g @openai/codex",
	},
	{
		Key:         "gmi",
		Command:     "gemini",
		VersionArgs: []string{"--version"},
		Hint:        "npm install -g @google/gemini-cli",
	},
	{
		Key:         "git",
		Command:     "git",
		VersionArgs: []string{"--version"},
		Hint:        "brew install git (macOS) / apt install git (Linux)",
	},
}

// GetACFSStatus returns ACFS setup status and core tool availability.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetACFSStatus() (*ACFSStatusOutput, error) {
	adapter := tools.NewACFSAdapter()

	output := &ACFSStatusOutput{
		RobotResponse: NewRobotResponse(true),
		Tools:         make(map[string]SetupToolStatus, len(setupToolSpecs)),
	}

	// Collect tool statuses
	for _, spec := range setupToolSpecs {
		output.Tools[spec.Key] = buildSetupToolStatus(spec)
	}

	// Check ACFS availability
	path, installed := adapter.Detect()
	output.ACFSAvailable = installed
	output.ACFSPath = path

	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("acfs not installed"),
			ErrCodeDependencyMissing,
			"Install acfs to enable setup status checks",
		)
		return output, nil
	}

	ctx := context.Background()
	version, err := adapter.Version(ctx)
	if err == nil {
		output.ACFSVersion = version.Raw
	}

	return output, nil
}

// PrintACFSStatus outputs ACFS status as JSON.
// This is a thin wrapper around GetACFSStatus() for CLI output.
func PrintACFSStatus() error {
	output, err := GetACFSStatus()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot ACFS status failed")
}

func buildSetupToolStatus(spec setupToolSpec) SetupToolStatus {
	status := SetupToolStatus{
		Installed: false,
		Required:  spec.Required,
	}

	path, err := exec.LookPath(spec.Command)
	if err != nil {
		status.Hint = spec.Hint
		return status
	}

	status.Installed = true
	status.Path = path

	if len(spec.VersionArgs) > 0 {
		// Some CLIs can hang or take a long time when invoked for version detection.
		// Keep this check best-effort and bounded so robot mode and tests never stall.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, spec.Command, spec.VersionArgs...)
		cmd.WaitDelay = 2 * time.Second
		out, err := cmd.CombinedOutput()
		if err == nil && ctx.Err() == nil {
			status.Version = strings.TrimSpace(string(out))
		}
	}

	return status
}

// ===========================================================================
// JFP (JeffreysPrompts) Robot Wrappers
// ===========================================================================

// JFPStatusOutput represents the output for --robot-jfp-status
type JFPStatusOutput struct {
	RobotResponse
	JFPAvailable bool        `json:"jfp_available"`
	Healthy      bool        `json:"healthy"`
	Version      string      `json:"version,omitempty"`
	Data         interface{} `json:"data,omitempty"`
}

// JFPListOutput represents the output for --robot-jfp-list
type JFPListOutput struct {
	RobotResponse
	Count   int             `json:"count"`
	Prompts json.RawMessage `json:"prompts"`
}

// JFPSearchOutput represents the output for --robot-jfp-search
type JFPSearchOutput struct {
	RobotResponse
	Query   string          `json:"query"`
	Count   int             `json:"count"`
	Results json.RawMessage `json:"results"`
}

// JFPShowOutput represents the output for --robot-jfp-show
type JFPShowOutput struct {
	RobotResponse
	ID     string          `json:"id"`
	Prompt json.RawMessage `json:"prompt,omitempty"`
}

// JFPSuggestOutput represents the output for --robot-jfp-suggest
type JFPSuggestOutput struct {
	RobotResponse
	Task        string          `json:"task"`
	Suggestions json.RawMessage `json:"suggestions"`
}

// JFPCategoriesOutput represents the output for --robot-jfp-categories
type JFPCategoriesOutput struct {
	RobotResponse
	Count      int             `json:"count"`
	Categories json.RawMessage `json:"categories"`
}

// JFPTagsOutput represents the output for --robot-jfp-tags
type JFPTagsOutput struct {
	RobotResponse
	Count int             `json:"count"`
	Tags  json.RawMessage `json:"tags"`
}

// JFPBundlesOutput represents the output for --robot-jfp-bundles
type JFPBundlesOutput struct {
	RobotResponse
	Count   int             `json:"count"`
	Bundles json.RawMessage `json:"bundles"`
}

// JFPExportOutput represents the output for --robot-jfp-export
type JFPExportOutput struct {
	RobotResponse
	IDs    []string        `json:"ids"`
	Format string          `json:"format,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// JFPUpdateOutput represents the output for --robot-jfp-update
type JFPUpdateOutput struct {
	RobotResponse
	Result json.RawMessage `json:"result,omitempty"`
}

// GetJFPStatus returns JFP health and status.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPStatus() (*JFPStatusOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPStatusOutput{
		RobotResponse: NewRobotResponse(true),
		JFPAvailable:  false,
		Healthy:       false,
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	output.JFPAvailable = installed

	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	// Check health
	ctx := context.Background()
	health, err := adapter.Health(ctx)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"HEALTH_CHECK_FAILED",
			"Run 'jfp doctor' to diagnose issues",
		)
		return output, nil
	}

	output.Healthy = health.Healthy

	// Get version
	version, err := adapter.Version(ctx)
	if err == nil {
		output.Version = version.Raw
	}

	// Get registry status
	statusData, err := adapter.Status(ctx)
	if err == nil && len(statusData) > 0 {
		output.Data = json.RawMessage(statusData)
	}

	return output, nil
}

// PrintJFPStatus outputs JFP health and status as JSON.
// This is a thin wrapper around GetJFPStatus() for CLI output.
func PrintJFPStatus() error {
	output, err := GetJFPStatus()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP status failed")
}

// JFPListOptions configures the GetJFPList operation.
type JFPListOptions struct {
	Category string
	Tag      string
}

// GetJFPList returns all prompts.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPList(opts JFPListOptions) (*JFPListOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPListOutput{
		RobotResponse: NewRobotResponse(true),
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	ctx := context.Background()
	var data json.RawMessage
	var err error

	if opts.Category != "" {
		data, err = adapter.ListByCategory(ctx, opts.Category)
	} else if opts.Tag != "" {
		data, err = adapter.ListByTag(ctx, opts.Tag)
	} else {
		data, err = adapter.List(ctx)
	}

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"LIST_FAILED",
			"Check 'jfp status' for registry connectivity",
		)
		return output, nil
	}

	prompts, count, err := jfpListField(data, "prompts")
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, "LIST_FAILED", "Check 'jfp list --json' output")
		return output, nil
	}
	output.Prompts = prompts
	output.Count = count
	return output, nil
}

// PrintJFPList outputs all prompts as JSON.
// This is a thin wrapper around GetJFPList() for CLI output.
func PrintJFPList(category, tag string) error {
	output, err := GetJFPList(JFPListOptions{
		Category: category,
		Tag:      tag,
	})
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP list failed")
}

// GetJFPSearch returns search results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPSearch(query string) (*JFPSearchOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPSearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         query,
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	if query == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("query is required"),
			ErrCodeInvalidFlag,
			"Provide a search query, e.g., --robot-jfp-search='debugging'",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Search(ctx, query)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"SEARCH_FAILED",
			"Try a different search query",
		)
		return output, nil
	}

	results, count, err := jfpListField(data, "results")
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, "SEARCH_FAILED", "Check 'jfp search --json' output")
		return output, nil
	}
	output.Results = results
	output.Count = count
	return output, nil
}

// PrintJFPSearch outputs search results as JSON.
// This is a thin wrapper around GetJFPSearch() for CLI output.
func PrintJFPSearch(query string) error {
	output, err := GetJFPSearch(query)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP search failed")
}

// GetJFPShow returns a specific prompt by ID.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPShow(id string) (*JFPShowOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPShowOutput{
		RobotResponse: NewRobotResponse(true),
		ID:            id,
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	if id == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("prompt ID is required"),
			ErrCodeInvalidFlag,
			"Provide a prompt ID, e.g., --robot-jfp-show=my-prompt-id",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Show(ctx, id)

	if err != nil {
		code := "SHOW_FAILED"
		if strings.Contains(err.Error(), "not found") {
			code = "NOT_FOUND"
		}
		output.RobotResponse = NewErrorResponse(
			err,
			code,
			"Use --robot-jfp-search to find available prompts",
		)
		return output, nil
	}

	output.Prompt = data
	return output, nil
}

// PrintJFPShow outputs a specific prompt by ID as JSON.
// This is a thin wrapper around GetJFPShow() for CLI output.
func PrintJFPShow(id string) error {
	output, err := GetJFPShow(id)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP show failed")
}

// GetJFPSuggest returns prompt suggestions for a task.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPSuggest(task string) (*JFPSuggestOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPSuggestOutput{
		RobotResponse: NewRobotResponse(true),
		Task:          task,
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	if task == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("task description is required"),
			ErrCodeInvalidFlag,
			"Provide a task description, e.g., --robot-jfp-suggest='build a REST API'",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Suggest(ctx, task)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"SUGGEST_FAILED",
			"Try a different task description",
		)
		return output, nil
	}

	suggestions, _, err := jfpListField(data, "suggestions")
	if err != nil {
		output.RobotResponse = NewErrorResponse(err, "SUGGEST_FAILED", "Check 'jfp suggest --json' output")
		return output, nil
	}
	output.Suggestions = suggestions
	return output, nil
}

// jfpListField returns one array field of a jfp JSON object (list, search and
// suggest wrap their items, e.g. {"prompts":[...],"count":N}) and its length.
func jfpListField(data json.RawMessage, field string) (json.RawMessage, int, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, 0, fmt.Errorf("decode jfp output: %w", err)
	}
	var items []json.RawMessage
	list, ok := envelope[field]
	if !ok || json.Unmarshal(list, &items) != nil {
		return nil, 0, fmt.Errorf("jfp output has no %q array", field)
	}
	return list, len(items), nil
}

// jfpMovedResponse reports a jfp command that upstream retired as unavailable
// rather than as a failure.
func jfpMovedResponse(err error) RobotResponse {
	return NewErrorResponse(err, ErrCodeNotImplemented, "Skill management moved from jfp to jsm; use jsm directly")
}

// PrintJFPSuggest outputs prompt suggestions for a task as JSON.
// This is a thin wrapper around GetJFPSuggest() for CLI output.
func PrintJFPSuggest(task string) error {
	output, err := GetJFPSuggest(task)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP suggest failed")
}

// GetJFPCategories returns all categories with counts.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPCategories() (*JFPCategoriesOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPCategoriesOutput{
		RobotResponse: NewRobotResponse(true),
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Categories(ctx)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"CATEGORIES_FAILED",
			"Run 'jfp status' to confirm registry health",
		)
		return output, nil
	}

	output.Categories = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintJFPCategories outputs all categories with counts as JSON.
// This is a thin wrapper around GetJFPCategories() for CLI output.
func PrintJFPCategories() error {
	output, err := GetJFPCategories()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP categories failed")
}

// GetJFPTags returns all tags with counts.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPTags() (*JFPTagsOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPTagsOutput{
		RobotResponse: NewRobotResponse(true),
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Tags(ctx)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"TAGS_FAILED",
			"Run 'jfp status' to confirm registry health",
		)
		return output, nil
	}

	output.Tags = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintJFPTags outputs all tags with counts as JSON.
// This is a thin wrapper around GetJFPTags() for CLI output.
func PrintJFPTags() error {
	output, err := GetJFPTags()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP tags failed")
}

// GetJFPBundles returns all bundles.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPBundles() (*JFPBundlesOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPBundlesOutput{
		RobotResponse: NewRobotResponse(true),
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Bundles(ctx)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"BUNDLES_FAILED",
			"Run 'jfp status' to confirm registry health",
		)
		return output, nil
	}

	output.Bundles = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintJFPBundles outputs all bundles as JSON.
// This is a thin wrapper around GetJFPBundles() for CLI output.
func PrintJFPBundles() error {
	output, err := GetJFPBundles()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP bundles failed")
}

func parseJFPIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ' ', '\n', '\t':
			return true
		default:
			return false
		}
	})
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ids = append(ids, part)
	}
	return ids
}

// GetJFPExport exports one or more prompts by ID.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPExport(rawIDs, format string) (*JFPExportOutput, error) {
	adapter := tools.NewJFPAdapter()
	ids := parseJFPIDs(rawIDs)

	output := &JFPExportOutput{
		RobotResponse: NewRobotResponse(true),
		IDs:           ids,
		Format:        format,
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	if len(ids) == 0 {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("prompt ID is required"),
			ErrCodeInvalidFlag,
			"Provide prompt IDs, e.g., --robot-jfp-export=prompt-123",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Export(ctx, ids, format)
	if errors.Is(err, tools.ErrJFPCommandMoved) {
		output.RobotResponse = jfpMovedResponse(err)
		return output, nil
	}
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"EXPORT_FAILED",
			"Check prompt IDs and format, then retry",
		)
		return output, nil
	}

	output.Result = data
	return output, nil
}

// PrintJFPExport outputs export results as JSON.
// This is a thin wrapper around GetJFPExport() for CLI output.
func PrintJFPExport(rawIDs, format string) error {
	output, err := GetJFPExport(rawIDs, format)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP export failed")
}

// GetJFPUpdate refreshes the local prompt registry/cache.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPUpdate() (*JFPUpdateOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPUpdateOutput{
		RobotResponse: NewRobotResponse(true),
	}

	// Check if jfp is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("jfp not installed"),
			ErrCodeDependencyMissing,
			"Install jfp with: npm install -g jeffreysprompts",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Refresh(ctx)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"UPDATE_FAILED",
			"Run 'jfp refresh' to refresh the registry",
		)
		return output, nil
	}

	output.Result = data
	return output, nil
}

// PrintJFPUpdate outputs update results as JSON.
// This is a thin wrapper around GetJFPUpdate() for CLI output.
func PrintJFPUpdate() error {
	output, err := GetJFPUpdate()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot JFP update failed")
}

// ===========================================================================
// MS (Meta Skill) Robot Wrappers
// ===========================================================================

// MSSearchOutput represents the output for --robot-ms-search
type MSSearchOutput struct {
	RobotResponse
	Query  string               `json:"query"`
	Count  int                  `json:"count"`
	Skills []tools.MSSkillMatch `json:"skills"`
	Source string               `json:"source,omitempty"`
}

// MSShowOutput represents the output for --robot-ms-show
type MSShowOutput struct {
	RobotResponse
	ID     string          `json:"id"`
	Skill  json.RawMessage `json:"skill,omitempty"`
	Source string          `json:"source,omitempty"`
}

// GetMSSearch returns skill matches for a query.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMSSearch(query string) (*MSSearchOutput, error) {
	adapter := tools.NewMSAdapter()

	output := &MSSearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         query,
		Skills:        []tools.MSSkillMatch{},
		Source:        "ms",
	}

	// Check if ms is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ms not installed"),
			ErrCodeDependencyMissing,
			"Install Meta Skill (ms) and ensure it is on PATH",
		)
		return output, nil
	}

	if strings.TrimSpace(query) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("query is required"),
			ErrCodeInvalidFlag,
			"Provide a query, e.g., --robot-ms-search='commit workflow'",
		)
		return output, nil
	}

	ctx := context.Background()
	matches, err := adapter.Search(ctx, query)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"MS_SEARCH_FAILED",
			"Try a different query or check ms health",
		)
		return output, nil
	}

	output.Skills = matches
	output.Count = len(matches)
	return output, nil
}

// PrintMSSearch outputs skill matches as JSON.
// This is a thin wrapper around GetMSSearch() for CLI output.
func PrintMSSearch(query string) error {
	output, err := GetMSSearch(query)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot MS search failed")
}

// GetMSShow returns a specific skill by ID.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMSShow(id string) (*MSShowOutput, error) {
	adapter := tools.NewMSAdapter()

	output := &MSShowOutput{
		RobotResponse: NewRobotResponse(true),
		ID:            id,
		Source:        "ms",
	}

	// Check if ms is installed
	_, installed := adapter.Detect()
	if !installed {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ms not installed"),
			ErrCodeDependencyMissing,
			"Install Meta Skill (ms) and ensure it is on PATH",
		)
		return output, nil
	}

	if strings.TrimSpace(id) == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("skill ID is required"),
			ErrCodeInvalidFlag,
			"Provide a skill ID, e.g., --robot-ms-show='commit-and-release'",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Show(ctx, id)
	if err != nil {
		code := "MS_SHOW_FAILED"
		lowerErr := strings.ToLower(err.Error())
		if strings.Contains(lowerErr, "not found") || strings.Contains(lowerErr, "not_found") || strings.Contains(lowerErr, "no such skill") {
			code = "NOT_FOUND"
		}
		output.RobotResponse = NewErrorResponse(
			err,
			code,
			"Use --robot-ms-search to find available skills",
		)
		return output, nil
	}

	output.Skill = data
	return output, nil
}

// PrintMSShow outputs a specific skill as JSON.
// This is a thin wrapper around GetMSShow() for CLI output.
func PrintMSShow(id string) error {
	output, err := GetMSShow(id)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot MS show failed")
}

// cassErrorResponse is the failure for a cass call: DEPENDENCY_MISSING naming
// the index command when cass is installed but has never indexed, else
// INTERNAL_ERROR.
func cassErrorResponse(err error, hint string) RobotResponse {
	if errors.Is(err, cass.ErrNotInitialized) {
		return NewErrorResponse(err, ErrCodeDependencyMissing, "Run 'cass index --full' once to build the cass index")
	}
	return NewErrorResponse(err, ErrCodeInternalError, hint)
}
