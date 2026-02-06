// Package robot provides machine-readable output for AI agents and automation.
// Use --robot-* flags to get JSON output suitable for piping to other tools.
package robot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/alerts"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/cass"
	"github.com/Dicklesworthstone/ntm/internal/config"
	ntmctx "github.com/Dicklesworthstone/ntm/internal/context"
	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/handoff"
	"github.com/Dicklesworthstone/ntm/internal/health"
	"github.com/Dicklesworthstone/ntm/internal/recipe"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/status"
	swarmlib "github.com/Dicklesworthstone/ntm/internal/swarm"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
)

// CASSStatusOutput represents the output for --robot-cass-status
type CASSStatusOutput struct {
	RobotResponse
	CASSAvailable bool           `json:"cass_available"`
	Healthy       bool           `json:"healthy"`
	Index         CASSIndexStats `json:"index"`
}

// CASSIndexStats holds index statistics
type CASSIndexStats struct {
	Exists        bool  `json:"exists"`
	Fresh         bool  `json:"fresh"`
	LastIndexedAt int64 `json:"last_indexed_at"`
	Conversations int64 `json:"conversations"`
	Messages      int64 `json:"messages"`
}

// GetCASSStatus collects CASS health and stats.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSStatus() (*CASSStatusOutput, error) {
	client := cass.NewClient()
	status, err := client.Status(context.Background())

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
		output.Healthy = status.Healthy
		output.Index.Exists = true
		output.Index.Fresh = status.Index.Healthy
		output.Index.LastIndexedAt = status.LastIndexedAt.Time.UnixMilli()
		output.Index.Conversations = status.Conversations
		output.Index.Messages = status.Messages
	} else {
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check cass index health and configuration",
		)
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
	return encodeJSON(output)
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
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check cass index health and query parameters",
			),
			Query: opts.Query,
			Hits:  []CASSSearchHit{},
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
			createdAt = hit.CreatedAt.Time.UnixMilli() // Convert to ms
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
	return encodeJSON(output)
}

// CASSInsightsOutput represents the output for --robot-cass-insights
type CASSInsightsOutput struct {
	RobotResponse
	Period string                   `json:"period"`
	Agents map[string]interface{}   `json:"agents"`
	Topics []map[string]interface{} `json:"topics"`
	Errors []map[string]interface{} `json:"errors"`
}

// GetCASSInsights returns aggregated insights.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetCASSInsights() (*CASSInsightsOutput, error) {
	client := cass.NewClient()
	if !client.IsInstalled() {
		return &CASSInsightsOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("cass not installed"),
				ErrCodeDependencyMissing,
				"Install cass to enable insights",
			),
			Period: "7d",
			Agents: map[string]interface{}{},
			Topics: []map[string]interface{}{},
			Errors: []map[string]interface{}{},
		}, nil
	}
	// Get aggregations for the last 7 days by default
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query: "*",
		Since: "7d",
		Limit: 0,
	})

	if err != nil {
		return &CASSInsightsOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check cass index health and configuration",
			),
			Period: "7d",
			Agents: map[string]interface{}{},
			Topics: []map[string]interface{}{},
			Errors: []map[string]interface{}{},
		}, nil
	}

	output := &CASSInsightsOutput{
		RobotResponse: NewRobotResponse(true),
		Period:        "7d",
		Agents:        map[string]interface{}{},
		Topics:        []map[string]interface{}{},
		Errors:        []map[string]interface{}{},
	}

	if resp.Aggregations != nil {
		// Convert agent map to buckets list
		var agentBuckets []map[string]interface{}
		for k, v := range resp.Aggregations.Agents {
			agentBuckets = append(agentBuckets, map[string]interface{}{
				"key":   k,
				"count": v,
			})
		}
		output.Agents["buckets"] = agentBuckets

		// Convert tags/topics
		for k, v := range resp.Aggregations.Tags {
			output.Topics = append(output.Topics, map[string]interface{}{
				"term":  k,
				"count": v,
			})
		}
	}

	return output, nil
}

// PrintCASSInsights outputs aggregated insights as JSON.
// This is a thin wrapper around GetCASSInsights() for CLI output.
func PrintCASSInsights() error {
	output, err := GetCASSInsights()
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
	KeyPoints []string `json:"key_points"`
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
	// Search for relevant sessions
	resp, err := client.Search(context.Background(), cass.SearchOptions{
		Query: query,
		Limit: 3,
	})

	if err != nil {
		return &CASSContextOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check cass index health",
			),
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

	for _, hit := range resp.Hits {
		when := "unknown"
		if hit.CreatedAt != nil {
			ts := hit.CreatedAt.Time
			when = ts.Format("2006-01-02")
		}

		session := CASSContextSession{
			Summary: hit.Title, // Use title as summary for now
			Source:  hit.SourcePath,
			Agent:   hit.Agent,
			When:    when,
		}
		// Extract potential key points from snippet?
		// For now just empty or placeholder
		session.KeyPoints = []string{}

		output.RelevantSessions = append(output.RelevantSessions, session)
		suggestions = append(suggestions, fmt.Sprintf("session '%s' (%s)", hit.Title, hit.Agent))
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
	return encodeJSON(output)
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
	return encodeJSON(output)
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

// JFPInstalledOutput represents the output for --robot-jfp-installed
type JFPInstalledOutput struct {
	RobotResponse
	Count  int             `json:"count"`
	Skills json.RawMessage `json:"skills"`
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

// JFPInstallOutput represents the output for --robot-jfp-install
type JFPInstallOutput struct {
	RobotResponse
	IDs     []string        `json:"ids"`
	Project string          `json:"project,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
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
	return encodeJSON(output)
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

	output.Prompts = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

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
	return encodeJSON(output)
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

	output.Results = data

	// Try to count results
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintJFPSearch outputs search results as JSON.
// This is a thin wrapper around GetJFPSearch() for CLI output.
func PrintJFPSearch(query string) error {
	output, err := GetJFPSearch(query)
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
	return encodeJSON(output)
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

	output.Suggestions = data
	return output, nil
}

// PrintJFPSuggest outputs prompt suggestions for a task as JSON.
// This is a thin wrapper around GetJFPSuggest() for CLI output.
func PrintJFPSuggest(task string) error {
	output, err := GetJFPSuggest(task)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetJFPInstalled returns installed Claude Code skills.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPInstalled() (*JFPInstalledOutput, error) {
	adapter := tools.NewJFPAdapter()

	output := &JFPInstalledOutput{
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
	data, err := adapter.Installed(ctx)

	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"INSTALLED_FAILED",
			"Check if Claude Code skills directory exists",
		)
		return output, nil
	}

	output.Skills = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintJFPInstalled outputs installed Claude Code skills as JSON.
// This is a thin wrapper around GetJFPInstalled() for CLI output.
func PrintJFPInstalled() error {
	output, err := GetJFPInstalled()
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
	return encodeJSON(output)
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
	return encodeJSON(output)
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
	return encodeJSON(output)
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

// GetJFPInstall installs one or more prompts by ID.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetJFPInstall(rawIDs, project string) (*JFPInstallOutput, error) {
	adapter := tools.NewJFPAdapter()
	ids := parseJFPIDs(rawIDs)

	output := &JFPInstallOutput{
		RobotResponse: NewRobotResponse(true),
		IDs:           ids,
		Project:       project,
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
			"Provide prompt IDs, e.g., --robot-jfp-install=prompt-123",
		)
		return output, nil
	}

	ctx := context.Background()
	data, err := adapter.Install(ctx, ids, project)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"INSTALL_FAILED",
			"Check prompt IDs and try again",
		)
		return output, nil
	}

	output.Result = data
	return output, nil
}

// PrintJFPInstall outputs install results as JSON.
// This is a thin wrapper around GetJFPInstall() for CLI output.
func PrintJFPInstall(rawIDs, project string) error {
	output, err := GetJFPInstall(rawIDs, project)
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
	return encodeJSON(output)
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
	data, err := adapter.Update(ctx)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"UPDATE_FAILED",
			"Run 'jfp update' to refresh the registry",
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
	return encodeJSON(output)
}

// ===========================================================================
// MS (Meta Skill) Robot Wrappers
// ===========================================================================

// MSSearchOutput represents the output for --robot-ms-search
type MSSearchOutput struct {
	RobotResponse
	Query  string          `json:"query"`
	Count  int             `json:"count"`
	Skills json.RawMessage `json:"skills"`
	Source string          `json:"source,omitempty"`
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
	data, err := adapter.Search(ctx, query)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			err,
			"MS_SEARCH_FAILED",
			"Try a different query or check ms health",
		)
		return output, nil
	}

	output.Skills = data

	// Try to count items
	var items []interface{}
	if json.Unmarshal(data, &items) == nil {
		output.Count = len(items)
	}

	return output, nil
}

// PrintMSSearch outputs skill matches as JSON.
// This is a thin wrapper around GetMSSearch() for CLI output.
func PrintMSSearch(query string) error {
	output, err := GetMSSearch(query)
	if err != nil {
		return err
	}
	return encodeJSON(output)
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
		if strings.Contains(err.Error(), "not found") {
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
	return encodeJSON(output)
}

// Build info - these will be set by the caller from cli package
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
	BuiltBy = "unknown"
)

// OutputFormat controls the output serialization format for robot commands.
// Set this from CLI flags, environment variables, or config before calling Print* functions.
// Default is FormatAuto which currently resolves to JSON.
var OutputFormat RobotFormat = FormatAuto

// Global state tracker for delta snapshots
var stateTracker = tracker.New()

// SessionInfo contains machine-readable session information
type SessionInfo struct {
	Name        string     `json:"name"`
	Exists      bool       `json:"exists"`
	Attached    bool       `json:"attached,omitempty"`
	Windows     int        `json:"windows,omitempty"`
	Panes       int        `json:"panes,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	Agents      []Agent    `json:"agents,omitempty"`
	PrivacyMode bool       `json:"privacy_mode,omitempty"` // True if privacy mode is enabled
}

// Agent represents an AI agent in a session
type Agent struct {
	Type     string `json:"type"`              // claude, codex, gemini
	Variant  string `json:"variant,omitempty"` // Model alias or persona name
	Pane     string `json:"pane"`
	Window   int    `json:"window"`
	PaneIdx  int    `json:"pane_idx"`
	IsActive bool   `json:"is_active"`

	// Status enrichment fields
	PID                  int       `json:"pid,omitempty"`                     // Shell PID
	ChildPID             int       `json:"child_pid,omitempty"`               // Agent process PID
	LastOutputTS         time.Time `json:"last_output_ts,omitempty"`          // Last output timestamp
	SecondsSinceOutput   int       `json:"seconds_since_output,omitempty"`    // Time since last output
	RateLimitDetected    bool      `json:"rate_limit_detected,omitempty"`     // Rate limit pattern matched
	RateLimitMatch       string    `json:"rate_limit_match,omitempty"`        // The specific pattern matched
	ProcessState         string    `json:"process_state,omitempty"`           // R, S, D, Z, T
	ProcessStateName     string    `json:"process_state_name,omitempty"`      // running, sleeping, etc.
	MemoryMB             int       `json:"memory_mb,omitempty"`               // Resident memory in MB
	OutputLinesSinceLast int       `json:"output_lines_since_last,omitempty"` // Lines since last check
	ContextTokens        int       `json:"context_tokens,omitempty"`          // Estimated tokens used
	ContextLimit         int       `json:"context_limit,omitempty"`           // Model context limit
	ContextPercent       float64   `json:"context_percent,omitempty"`         // Usage percentage (0-100+)
	ContextModel         string    `json:"context_model,omitempty"`           // Model name for context limit lookup
}

// SystemInfo contains system and runtime information
type SystemInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	TmuxOK    bool   `json:"tmux_available"`
}

// StatusOutput is the structured output for robot-status
type StatusOutput struct {
	RobotResponse
	GeneratedAt    time.Time              `json:"generated_at"`
	SafetyProfile  string                 `json:"safety_profile,omitempty"`
	System         SystemInfo             `json:"system"`
	Sessions       []SessionInfo          `json:"sessions"`
	Pagination     *PaginationInfo        `json:"pagination,omitempty"`
	AgentHints     *AgentHints            `json:"_agent_hints,omitempty"`
	Summary        StatusSummary          `json:"summary"`
	Beads          *bv.BeadsSummary       `json:"beads,omitempty"`
	Progress       *ProgressSummary       `json:"progress,omitempty"`
	GraphMetrics   *GraphMetrics          `json:"graph_metrics,omitempty"`
	AgentMail      *AgentMailSummary      `json:"agent_mail,omitempty"`
	Handoff        *HandoffSummary        `json:"handoff,omitempty"`
	Alerts         []StatusAlert          `json:"alerts,omitempty"`
	FileChanges    []FileChangeInfo       `json:"file_changes,omitempty"`
	Conflicts      []tracker.Conflict     `json:"conflicts,omitempty"`
	SchedulerStats *SchedulerStatsSummary `json:"scheduler_stats,omitempty"`
}

// AgentMailSummary provides a lightweight Agent Mail state for --robot-status.
type AgentMailSummary struct {
	Available          bool   `json:"available"`
	ServerURL          string `json:"server_url,omitempty"`
	SessionsRegistered int    `json:"sessions_registered,omitempty"`
	TotalUnread        int    `json:"total_unread,omitempty"`
	UrgentMessages     int    `json:"urgent_messages,omitempty"`
	TotalLocks         int    `json:"total_locks,omitempty"`
	Error              string `json:"error,omitempty"`
}

// HandoffSummary is the latest handoff across all sessions.
type HandoffSummary struct {
	Session    string `json:"session"`
	Goal       string `json:"goal,omitempty"`
	Now        string `json:"now,omitempty"`
	Path       string `json:"path,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
	Status     string `json:"status,omitempty"`
}

// SchedulerStatsSummary provides spawn scheduler statistics for robot-status.
// This surfaces queue depth, backoff state, headroom, and rate limit status
// to help agents understand spawn pacing.
type SchedulerStatsSummary struct {
	// Enabled indicates if spawn pacing is active.
	Enabled bool `json:"enabled"`

	// QueueDepth is the current number of jobs waiting in queue.
	QueueDepth int `json:"queue_depth"`

	// RunningCount is the number of currently executing spawn jobs.
	RunningCount int `json:"running_count"`

	// TotalSubmitted is the total jobs submitted since scheduler start.
	TotalSubmitted int64 `json:"total_submitted"`

	// TotalCompleted is the total jobs completed successfully.
	TotalCompleted int64 `json:"total_completed"`

	// TotalFailed is the total jobs that failed after all retries.
	TotalFailed int64 `json:"total_failed"`

	// IsPaused indicates if the scheduler is paused.
	IsPaused bool `json:"is_paused"`

	// InBackoff indicates if global backoff is currently active.
	InBackoff bool `json:"in_backoff"`

	// BackoffRemainingMs is the remaining backoff time in milliseconds.
	BackoffRemainingMs int64 `json:"backoff_remaining_ms,omitempty"`

	// HeadroomOK indicates if resource headroom is sufficient.
	HeadroomOK bool `json:"headroom_ok"`

	// HeadroomReason describes why headroom is blocked (if any).
	HeadroomReason string `json:"headroom_reason,omitempty"`

	// RateLimitTokens is the current available rate limit tokens.
	RateLimitTokens float64 `json:"rate_limit_tokens"`

	// AgentCaps shows per-agent concurrency status.
	AgentCaps *AgentCapsStatus `json:"agent_caps,omitempty"`

	// UptimeSeconds is how long the scheduler has been running.
	UptimeSeconds int64 `json:"uptime_seconds,omitempty"`
}

// AgentCapsStatus shows per-agent-type concurrency cap status.
type AgentCapsStatus struct {
	ClaudeCurrent int `json:"claude_current"`
	ClaudeMax     int `json:"claude_max"`
	CodexCurrent  int `json:"codex_current"`
	CodexMax      int `json:"codex_max"`
	GeminiCurrent int `json:"gemini_current"`
	GeminiMax     int `json:"gemini_max"`
}

// StatusAlert represents a warning or alert emitted by robot status.
type StatusAlert struct {
	Type         string  `json:"type"`
	Session      string  `json:"session,omitempty"`
	Pane         string  `json:"pane,omitempty"`
	PaneIdx      int     `json:"pane_idx,omitempty"`
	UsagePercent float64 `json:"usage_percent,omitempty"`
	ContextModel string  `json:"context_model,omitempty"`
	Severity     string  `json:"severity,omitempty"`
}

// GraphMetrics provides bv graph analysis metrics for status output
type GraphMetrics struct {
	TopBottlenecks []BottleneckInfo `json:"top_bottlenecks,omitempty"`
	Keystones      int              `json:"keystones_count"`
	HealthStatus   string           `json:"health_status"` // "ok", "warning", "critical"
	DriftMessage   string           `json:"drift_message,omitempty"`
}

// BottleneckInfo represents a bottleneck issue with its score
type BottleneckInfo struct {
	ID    string  `json:"id"`
	Title string  `json:"title,omitempty"`
	Score float64 `json:"score"`
}

// FileChangeInfo is a sanitized view of recorded file changes.
type FileChangeInfo struct {
	Session string    `json:"session"`
	Path    string    `json:"path"`
	Type    string    `json:"type"`
	Agents  []string  `json:"agents,omitempty"`
	At      time.Time `json:"at"`
}

const (
	fileChangeLookback = 30 * time.Minute
	fileChangeLimit    = 50
	conflictLimit      = 20
)

// StatusSummary provides aggregate stats
type StatusSummary struct {
	TotalSessions int `json:"total_sessions"`
	TotalAgents   int `json:"total_agents"`
	AttachedCount int `json:"attached_count"`
	ClaudeCount   int `json:"claude_count"`
	CodexCount    int `json:"codex_count"`
	GeminiCount   int `json:"gemini_count"`
	CursorCount   int `json:"cursor_count"`
	WindsurfCount int `json:"windsurf_count"`
	AiderCount    int `json:"aider_count"`
}

// ProgressSummary provides bead completion metrics for status and dashboard (bd-1qct).
type ProgressSummary struct {
	Assigned        int     `json:"assigned"`         // Beads currently in progress
	Completed       int     `json:"completed"`        // Beads closed
	Remaining       int     `json:"remaining"`        // Open + in-progress (not yet closed)
	Total           int     `json:"total"`            // All beads
	CompletionRatio float64 `json:"completion_ratio"` // Closed / Total (0.0 to 1.0)
}

// ComputeProgress derives a ProgressSummary from BeadsSummary counts.
func ComputeProgress(beads *bv.BeadsSummary) *ProgressSummary {
	if beads == nil || !beads.Available || beads.Total == 0 {
		return nil
	}
	remaining := beads.Open + beads.InProgress
	ratio := float64(beads.Closed) / float64(beads.Total)
	// Round to 4 decimal places for cleaner JSON
	ratio = float64(int(ratio*10000+0.5)) / 10000
	return &ProgressSummary{
		Assigned:        beads.InProgress,
		Completed:       beads.Closed,
		Remaining:       remaining,
		Total:           beads.Total,
		CompletionRatio: ratio,
	}
}

// PlanOutput provides an execution plan for what can be done
type PlanOutput struct {
	RobotResponse
	GeneratedAt    time.Time    `json:"generated_at"`
	Recommendation string       `json:"recommendation"`
	Actions        []PlanAction `json:"actions"`
	BeadActions    []BeadAction `json:"bead_actions,omitempty"`
	Warnings       []string     `json:"warnings,omitempty"`
}

// BeadAction represents a recommended action based on bead priority analysis
type BeadAction struct {
	BeadID        string         `json:"bead_id"`
	Title         string         `json:"title"`
	Priority      int            `json:"priority"`
	Impact        float64        `json:"impact_score"`
	Reasoning     []string       `json:"reasoning"`
	Command       string         `json:"command"`              // e.g., "bd update ntm-xyz --status in_progress"
	IsReady       bool           `json:"is_ready"`             // true if no blockers
	BlockedBy     []string       `json:"blocked_by,omitempty"` // blocking bead IDs
	GraphPosition *GraphPosition `json:"graph_position,omitempty"`
}

// GraphPosition represents the position of an issue in the dependency graph
type GraphPosition struct {
	IsBottleneck    bool    `json:"is_bottleneck,omitempty"`
	BottleneckScore float64 `json:"bottleneck_score,omitempty"`
	IsKeystone      bool    `json:"is_keystone,omitempty"`
	KeystoneScore   float64 `json:"keystone_score,omitempty"`
	IsHub           bool    `json:"is_hub,omitempty"`
	HubScore        float64 `json:"hub_score,omitempty"`
	IsAuthority     bool    `json:"is_authority,omitempty"`
	AuthorityScore  float64 `json:"authority_score,omitempty"`
	Summary         string  `json:"summary,omitempty"` // Human-readable summary
}

// PlanAction is a suggested action
type PlanAction struct {
	Priority    int      `json:"priority"` // 1=high, 2=medium, 3=low
	Command     string   `json:"command"`
	Description string   `json:"description"`
	Args        []string `json:"args,omitempty"`
}

// PrintHelp outputs AI agent help documentation
func PrintHelp() {
	help := `ntm (Named Tmux Manager) AI Agent Interface
=============================================
Robot mode provides a JSON API for AI agents to orchestrate coding sessions.

API Design Principles (see docs/robot-api-design.md):
-----------------------------------------------------
1. Global commands: bool flags (--robot-status, --robot-plan)
2. Session-scoped: =SESSION syntax (--robot-send=myproj, --robot-tail=myproj)
3. Modifiers: unprefixed global flags (--limit, --offset, --since, --type)
4. Output: JSON by default, TOON for token-efficient (--robot-format=toon)

Core Commands:
--------------
--robot-status          Session state, agents, alerts (start here)
--robot-snapshot        Unified state: sessions + beads + alerts + mail
--robot-capabilities    Machine-discoverable API schema
--robot-version         Version/build info (JSON)

Session Operations:
-------------------
--robot-spawn=SESSION   Create session with --spawn-cc=N, --spawn-cod=N, --spawn-gmi=N
--robot-ensemble-spawn=SESSION  Spawn ensemble with --preset/--modes and --question
--robot-send=SESSION    Send prompts (--msg="text", --panes=1,2, --type=claude)
--robot-tail=SESSION    Capture pane output (--lines=50, --panes=1,2)
--robot-ensemble=SESSION Ensemble state (modes, status, synthesis readiness)
--robot-interrupt=SESSION  Ctrl+C to agents (--interrupt-msg="new task")
--robot-is-working=SESSION Check if agents are busy
--robot-wait=SESSION    Wait for idle state (--timeout=5m, --condition=idle)

Note: Pane-targeting commands exclude the user pane by default.
Use --all to include the user pane (index depends on tmux pane-base-index).

Work Distribution:
------------------
--robot-assign=SESSION  Get assignment recommendations (--strategy=balanced)
--robot-bulk-assign=SESSION  Batch assign beads (--from-bv)

Analysis & Monitoring:
----------------------
--robot-triage          Prioritized work recommendations
--robot-graph           Dependency graph insights
--robot-context=SESSION Context window usage
--robot-agent-health=SESSION  Comprehensive health check
--robot-diagnose=SESSION Diagnose issues with fix recommendations

Tool Bridges:
-------------
--robot-cass-search=QUERY    Search past conversations (--limit=20, --since=7d)
--robot-acfs-status          Setup status via ACFS (alias: --robot-setup)
--robot-setup                Alias for --robot-acfs-status
--robot-giil-fetch=URL       Download image from share URL via giil
--robot-jfp-search=QUERY     Search prompts library
--robot-jfp-install=ID       Install prompt(s) (--jfp-project=PATH)
--robot-jfp-export=ID        Export prompt(s) (--jfp-format=skill|md)
--robot-jfp-update           Update JFP registry cache
--robot-ms-search=QUERY      Search Meta Skill catalog
--robot-ms-show=ID           Show Meta Skill details
--robot-slb-pending          List pending SLB approval requests
--robot-slb-approve=ID       Approve SLB request by ID
--robot-slb-deny=ID          Deny SLB request by ID (--reason="...")
--robot-tokens               Token usage stats (--days=30, --group-by=agent)
--robot-history=SESSION      Command history (--last=10)

Bead Management:
----------------
--robot-beads-list      List beads (--status=open, --priority=P0-P1)
--robot-bead-claim=ID   Claim a bead (--bead-assignee=agent-1)
--robot-bead-create     Create bead (--bead-title="Fix bug")
--robot-bead-close=ID   Close bead (--bead-close-reason="done")

Output Formats:
---------------
--robot-format=json     Full JSON (default)
--robot-format=toon     Token-efficient format
--robot-markdown        Markdown tables (~50% fewer tokens)
--robot-terse           Single-line state summary

Common Modifiers:
-----------------
--limit=N       Max results (works with search, list commands)
--offset=N      Pagination offset for list commands
--robot-limit=N  Explicit pagination alias for robot list outputs
--robot-offset=N Explicit pagination alias for robot list outputs
--since=DURATION  Time filter (1d, 7d, 30d, ISO8601, or duration like 1h)
--type=TYPE     Agent type filter (claude, codex, gemini)
--panes=X,Y     Pane filter (comma-separated indices)
--dry-run       Preview without executing
--verbose       Detailed output

Quick Start:
------------
1) Create session:    ntm --robot-spawn=proj --spawn-cc=2 --spawn-wait
2) Check state:       ntm --robot-status
3) Send prompt:       ntm --robot-send=proj --msg="implement auth" --track
4) Monitor progress:  ntm --robot-is-working=proj
5) Get output:        ntm --robot-tail=proj --lines=100

Common Workflows:
-----------------
- Single agent: ntm --robot-spawn=proj --spawn-cc=1 --spawn-wait
- Send+wait:    ntm --robot-send=proj --msg="do X" --track
- Recover:      ntm --robot-snapshot --since=2025-01-01T00:00:00Z

Tips for AI Agents:
-------------------
- Start with --robot-status, then narrow with --panes and --lines.
- Prefer --robot-capabilities for schema discovery over parsing help text.

For complete API documentation: docs/robot-api-design.md
For machine-readable schema:    ntm --robot-capabilities
`
	fmt.Println(help)
}

// GetStatus collects machine-readable status.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetStatus() (*StatusOutput, error) {
	return GetStatusWithOptions(PaginationOptions{})
}

// GetStatusWithOptions collects status and applies pagination to sessions.
func GetStatusWithOptions(opts PaginationOptions) (*StatusOutput, error) {
	wd := mustGetwd()
	cfg, err := config.LoadMerged(wd, config.DefaultPath())
	if err != nil {
		cfg = config.Default()
	}

	output := &StatusOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		SafetyProfile: cfg.Safety.Profile,
		System: SystemInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			TmuxOK:    tmux.IsInstalled(),
		},
		Sessions:    []SessionInfo{},
		Summary:     StatusSummary{},
		Alerts:      []StatusAlert{},
		FileChanges: []FileChangeInfo{},
		Conflicts:   []tracker.Conflict{},
	}

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// tmux not running is not an error for status
		return output, nil
	}

	for _, sess := range sessions {
		info := SessionInfo{
			Name:     sess.Name,
			Exists:   true,
			Attached: sess.Attached,
			Windows:  sess.Windows,
			Agents:   []Agent{},
		}

		// Try to get agents from panes
		panes, err := tmux.GetPanes(sess.Name)
		if err == nil {
			info.Panes = len(panes)
			for _, pane := range panes {
				agent := Agent{
					Pane:     pane.ID,
					Window:   0, // GetPanes doesn't include window index
					PaneIdx:  pane.Index,
					IsActive: pane.Active,
					Variant:  pane.Variant,
					PID:      pane.PID,
				}

				// Use authoritative type from tmux package if available
				ntmType := agentTypeString(pane.Type)
				if ntmType != "user" && ntmType != "unknown" {
					agent.Type = ntmType
				} else {
					// Fallback to loose detection for other agents (cursor, windsurf, etc.)
					agent.Type = detectAgentType(pane.Title)
				}

				modelName := modelNameForPane(pane, cfg)

				// Enrich status with process/output info
				enrichAgentStatus(&agent, sess.Name, modelName)

				if agent.ContextPercent >= 70 {
					severity := "warning"
					if agent.ContextPercent >= 85 {
						severity = "critical"
					}
					output.Alerts = append(output.Alerts, StatusAlert{
						Type:         "context_warning",
						Session:      sess.Name,
						Pane:         pane.ID,
						PaneIdx:      pane.Index,
						UsagePercent: agent.ContextPercent,
						ContextModel: agent.ContextModel,
						Severity:     severity,
					})
				}

				info.Agents = append(info.Agents, agent)

				// Update summary counts
				switch agent.Type {
				case "claude":
					output.Summary.ClaudeCount++
				case "codex":
					output.Summary.CodexCount++
				case "gemini":
					output.Summary.GeminiCount++
				case "cursor":
					output.Summary.CursorCount++
				case "windsurf":
					output.Summary.WindsurfCount++
				case "aider":
					output.Summary.AiderCount++
				}
				output.Summary.TotalAgents++
			}
		}

		output.Sessions = append(output.Sessions, info)
		output.Summary.TotalSessions++
		if sess.Attached {
			output.Summary.AttachedCount++
		}
	}

	// Add beads summary if bv is available
	if bv.IsInstalled() {
		output.Beads = bv.GetBeadsSummary(wd, BeadLimit)
		output.Progress = ComputeProgress(output.Beads)
		output.GraphMetrics = getGraphMetrics()
	}

	// Add latest handoff across sessions (best-effort)
	if wd != "" {
		reader := handoff.NewReader(wd)
		if h, path, err := reader.FindLatestAny(); err == nil && h != nil {
			output.Handoff = &HandoffSummary{
				Session:    h.Session,
				Goal:       h.Goal,
				Now:        h.Now,
				Path:       path,
				Status:     h.Status,
				AgeSeconds: int64(time.Since(h.CreatedAt).Seconds()),
			}
		}
	}

	// Enrich with Agent Mail summary (best-effort; degrade gracefully)
	if summary, _ := getAgentMailSummary(); summary != nil {
		output.AgentMail = summary
	}

	// Include recent file changes (best-effort, bounded).
	appendFileChanges(output)
	appendConflicts(output)

	if paged, page := ApplyPagination(output.Sessions, opts); page != nil {
		output.Sessions = paged
		output.Pagination = page
		if next, pages := paginationHintOffsets(page); next != nil {
			output.AgentHints = &AgentHints{
				NextOffset:     next,
				PagesRemaining: pages,
			}
		}
	}

	return output, nil
}

// PrintStatus outputs machine-readable status.
// This is a thin wrapper around GetStatus() for CLI output.
func PrintStatus() error {
	output, err := GetStatus()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// PrintStatusWithOptions outputs status with pagination options.
func PrintStatusWithOptions(opts PaginationOptions) error {
	output, err := GetStatusWithOptions(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

func appendFileChanges(output *StatusOutput) {
	cutoff := time.Now().Add(-fileChangeLookback)
	changes := tracker.RecordedChangesSince(cutoff)
	if len(changes) == 0 {
		return
	}

	if len(changes) > fileChangeLimit {
		changes = changes[len(changes)-fileChangeLimit:]
	}

	wd, _ := os.Getwd()
	prefix := wd
	if prefix != "" && !strings.HasSuffix(prefix, string(os.PathSeparator)) {
		prefix += string(os.PathSeparator)
	}

	for _, change := range changes {
		path := change.Change.Path
		if prefix != "" && strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
		}

		output.FileChanges = append(output.FileChanges, FileChangeInfo{
			Session: change.Session,
			Path:    path,
			Type:    string(change.Change.Type),
			Agents:  change.Agents,
			At:      change.Timestamp,
		})
	}
}

func appendConflicts(output *StatusOutput) {
	conflicts := tracker.ConflictsSince(time.Now().Add(-fileChangeLookback), "")
	if len(conflicts) == 0 {
		return
	}
	if len(conflicts) > conflictLimit {
		conflicts = conflicts[:conflictLimit]
	}
	output.Conflicts = conflicts
}

// MailOptions configures the GetMail operation.
type MailOptions struct {
	Session    string
	ProjectKey string
}

// MailOutput represents the output for --robot-mail.
type MailOutput struct {
	RobotResponse
	GeneratedAt      time.Time                   `json:"generated_at"`
	Session          string                      `json:"session,omitempty"`
	ProjectKey       string                      `json:"project_key"`
	Available        bool                        `json:"available"`
	ServerURL        string                      `json:"server_url,omitempty"`
	SessionAgent     *agentmail.SessionAgentInfo `json:"session_agent,omitempty"`
	Agents           []AgentMailAgent            `json:"agents,omitempty"`
	UnmappedAgents   []AgentMailAgent            `json:"unmapped_agents,omitempty"`
	Messages         AgentMailMessageCounts      `json:"messages,omitempty"`
	FileReservations []AgentMailReservation      `json:"file_reservations,omitempty"`
	Conflicts        []AgentMailConflict         `json:"conflicts,omitempty"`
}

// GetMail returns detailed Agent Mail state for AI orchestrators.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetMail(opts MailOptions) (*MailOutput, error) {
	projectKey := opts.ProjectKey
	if projectKey == "" {
		wd, err := os.Getwd()
		if err == nil {
			if root, err := git.FindProjectRoot(wd); err == nil {
				projectKey = root
			} else {
				projectKey = wd
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	serverURL := client.BaseURL()

	output := &MailOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Session:       opts.Session,
		ProjectKey:    projectKey,
		Available:     false,
		ServerURL:     serverURL,
	}

	if !client.IsAvailable() {
		return output, nil
	}
	output.Available = true

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("ensure_project: %w", err),
			ErrCodeInternalError,
			"Verify Agent Mail server and project key",
		)
		return output, nil
	}

	// Session coordinator agent info (best-effort, when a session name is provided).
	if opts.Session != "" {
		if info, err := agentmail.LoadSessionAgent(opts.Session, projectKey); err == nil && info != nil {
			output.SessionAgent = info
		}
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("list_agents: %w", err),
			ErrCodeInternalError,
			"Verify Agent Mail server and project key",
		)
		return output, nil
	}

	agentByName := make(map[string]agentmail.Agent, len(agents))
	inboxByName := make(map[string]inboxTally, len(agents))

	// Gather per-agent mail counts (best-effort).
	for _, a := range agents {
		if a.Name == "HumanOverseer" {
			continue
		}
		agentByName[a.Name] = a
		tally := getInboxTally(ctx, client, projectKey, a.Name, 50)
		inboxByName[a.Name] = tally

		output.Messages.Total += tally.Total
		output.Messages.Unread += tally.Total
		output.Messages.Urgent += tally.Urgent
		output.Messages.PendingAck += tally.PendingAck
	}

	// Best-effort pane mapping when a session is provided and tmux is available.
	assigned := make(map[string]bool)
	if opts.Session != "" && tmux.IsInstalled() && tmux.SessionExists(opts.Session) {
		if panes, err := tmux.GetPanes(opts.Session); err == nil {
			mapping := resolveAgentsForSession(panes, agents)
			paneInfos := parseNTMPanes(panes)

			// Collect and sort all pane types found
			var paneTypes []string
			for t := range paneInfos {
				paneTypes = append(paneTypes, t)
			}
			sort.Strings(paneTypes)

			for _, paneType := range paneTypes {
				for _, pane := range paneInfos[paneType] {
					entry := AgentMailAgent{Pane: pane.Label}
					if agentName, ok := mapping[pane.Label]; ok {
						assigned[agentName] = true
						a := agentByName[agentName]
						tally := inboxByName[agentName]
						entry.AgentName = agentName
						entry.Program = a.Program
						entry.Model = a.Model
						entry.UnreadCount = tally.Total
						entry.UrgentCount = tally.Urgent
						entry.LastActiveTs = a.LastActiveTS.Time
					}
					output.Agents = append(output.Agents, entry)
				}
			}
		}
	}

	// If no panes were added (no session context), fall back to listing agents as-is.
	if len(output.Agents) == 0 {
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}
			tally := inboxByName[a.Name]
			output.Agents = append(output.Agents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Total,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS.Time,
			})
		}
	} else {
		// Include any registered agents that we couldn't map to panes.
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}
			if a.Program == "ntm" || assigned[a.Name] {
				continue
			}
			tally := inboxByName[a.Name]
			output.UnmappedAgents = append(output.UnmappedAgents, AgentMailAgent{
				AgentName:    a.Name,
				Program:      a.Program,
				Model:        a.Model,
				UnreadCount:  tally.Total,
				UrgentCount:  tally.Urgent,
				LastActiveTs: a.LastActiveTS.Time,
			})
		}
	}

	reservations, err := client.ListReservations(ctx, projectKey, "", true)
	if err == nil {
		output.FileReservations = summarizeReservations(reservations)
		output.Conflicts = detectReservationConflicts(reservations)
	}

	return output, nil
}

// PrintMail outputs detailed Agent Mail state for AI orchestrators.
// This is a thin wrapper around GetMail() for CLI output.
func PrintMail(sessionName, projectKey string) error {
	output, err := GetMail(MailOptions{
		Session:    sessionName,
		ProjectKey: projectKey,
	})
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// AgentMailAgent is a per-agent view for --robot-mail.
type AgentMailAgent struct {
	Pane         string    `json:"pane,omitempty"`
	AgentName    string    `json:"agent_name,omitempty"`
	Program      string    `json:"program,omitempty"`
	Model        string    `json:"model,omitempty"`
	UnreadCount  int       `json:"unread_count,omitempty"`
	UrgentCount  int       `json:"urgent_count,omitempty"`
	LastActiveTs time.Time `json:"last_active_ts,omitempty"`
}

type AgentMailMessageCounts struct {
	Total      int `json:"total"`
	Unread     int `json:"unread"`
	Urgent     int `json:"urgent"`
	PendingAck int `json:"pending_ack"`
}

type AgentMailReservation struct {
	ID               int    `json:"id"`
	Pattern          string `json:"pattern"`
	Agent            string `json:"agent"`
	Exclusive        bool   `json:"exclusive"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
	Reason           string `json:"reason,omitempty"`
}

type AgentMailConflict struct {
	Pattern string   `json:"pattern"`
	Holders []string `json:"holders"`
}

type inboxTally struct {
	Total      int
	Urgent     int
	PendingAck int
}

func getInboxTally(ctx context.Context, client *agentmail.Client, projectKey, agentName string, limit int) inboxTally {
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		UrgentOnly:    false,
		Limit:         limit,
		IncludeBodies: false,
	}
	msgs, err := client.FetchInbox(ctx, opts)
	if err != nil {
		return inboxTally{}
	}

	tally := inboxTally{Total: len(msgs)}
	for _, msg := range msgs {
		if strings.EqualFold(msg.Importance, "urgent") {
			tally.Urgent++
		}
		if msg.AckRequired {
			tally.PendingAck++
		}
	}
	return tally
}

type ntmPaneInfo struct {
	Label     string
	Type      string
	Index     int
	TmuxIndex int
	Variant   string
}

func parseNTMPanes(panes []tmux.Pane) map[string][]ntmPaneInfo {
	out := make(map[string][]ntmPaneInfo)

	for _, p := range panes {
		// Use the NTM-specific index parsed by the tmux package
		// This avoids duplicate regex parsing and ensures consistency
		idx := p.NTMIndex
		if idx == 0 && p.Type == "user" {
			// Skip user pane or panes that didn't parse correctly
			// Note: parseAgentFromTitle returns 0 if no match.
			// But for valid NTM panes, index should be > 0 (e.g. cc_1)
			// User pane might be user_0 or just user?
			// Let's check tmux.parseAgentFromTitle logic: matches[2] is the index group.
			// "session__cc_1" -> index 1.
			// "session__user" -> no match for regex which expects _\d+
			// So if NTMIndex is 0, it's not a standard numbered agent pane.
			continue
		}

		// Convert AgentType to string for map key
		typ := string(p.Type)

		out[typ] = append(out[typ], ntmPaneInfo{
			Label:     fmt.Sprintf("%s_%d", typ, idx),
			Type:      typ,
			Index:     idx,
			TmuxIndex: p.Index,
			Variant:   p.Variant,
		})
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].Index < out[typ][j].Index })
	}
	return out
}

func groupAgentsByType(agents []agentmail.Agent) map[string][]agentmail.Agent {
	out := make(map[string][]agentmail.Agent)

	for _, a := range agents {
		if a.Program == "" || a.Program == "ntm" {
			continue
		}
		typ := agentTypeFromProgram(a.Program)
		if typ == "" {
			continue
		}
		out[typ] = append(out[typ], a)
	}

	for typ := range out {
		sort.SliceStable(out[typ], func(i, j int) bool { return out[typ][i].InceptionTS.Before(out[typ][j].InceptionTS.Time) })
	}
	return out
}

func agentTypeFromProgram(program string) string {
	p := strings.ToLower(program)
	switch {
	case strings.Contains(p, "claude"):
		return "cc"
	case strings.Contains(p, "codex"):
		return "cod"
	case strings.Contains(p, "gemini"):
		return "gmi"
	case strings.Contains(p, "cursor"):
		return "cursor"
	case strings.Contains(p, "windsurf"):
		return "windsurf"
	case strings.Contains(p, "aider"):
		return "aider"
	default:
		return p
	}
}

func normalizedProgramType(program string) string {
	p := strings.ToLower(program)
	switch {
	case strings.Contains(p, "claude"):
		return "claude"
	case strings.Contains(p, "codex"):
		return "codex"
	case strings.Contains(p, "gemini"):
		return "gemini"
	default:
		return "unknown"
	}
}

func assignAgentsToPanes(panes []ntmPaneInfo, agents []agentmail.Agent) map[string]string {
	assigned := make(map[string]bool)
	mapping := make(map[string]string)

	// Create a copy of panes to sort without affecting the caller
	sortedPanes := make([]ntmPaneInfo, len(panes))
	copy(sortedPanes, panes)

	// Prioritize panes with variants (more specific requirements)
	sort.SliceStable(sortedPanes, func(i, j int) bool {
		hasVarI := sortedPanes[i].Variant != ""
		hasVarJ := sortedPanes[j].Variant != ""
		if hasVarI && !hasVarJ {
			return true
		}
		if !hasVarI && hasVarJ {
			return false
		}
		return sortedPanes[i].Index < sortedPanes[j].Index
	})

	for _, pane := range sortedPanes {
		bestIdx := -1
		bestScore := -1

		for i, a := range agents {
			if assigned[a.Name] {
				continue
			}
			score := 0
			if pane.Variant != "" {
				v := strings.ToLower(pane.Variant)
				if strings.Contains(strings.ToLower(a.Model), v) {
					score = 2
				} else if strings.Contains(strings.ToLower(a.TaskDescription), v) {
					score = 1
				}
			}
			if bestIdx == -1 || score > bestScore {
				bestIdx = i
				bestScore = score
			}
		}

		if bestIdx == -1 {
			continue
		}

		chosen := agents[bestIdx]
		mapping[pane.Label] = chosen.Name
		assigned[chosen.Name] = true
	}

	return mapping
}

func summarizeReservations(reservations []agentmail.FileReservation) []AgentMailReservation {
	now := time.Now()
	out := make([]AgentMailReservation, 0, len(reservations))
	for _, r := range reservations {
		expiresIn := int(r.ExpiresTS.Sub(now).Seconds())
		if expiresIn < 0 {
			expiresIn = 0
		}
		out = append(out, AgentMailReservation{
			ID:               r.ID,
			Pattern:          r.PathPattern,
			Agent:            r.AgentName,
			Exclusive:        r.Exclusive,
			ExpiresInSeconds: expiresIn,
			Reason:           r.Reason,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}

func detectReservationConflicts(reservations []agentmail.FileReservation) []AgentMailConflict {
	type patternState struct {
		agents    map[string]bool
		exclusive bool
	}
	byPattern := make(map[string]*patternState)
	for _, r := range reservations {
		state := byPattern[r.PathPattern]
		if state == nil {
			state = &patternState{agents: make(map[string]bool)}
			byPattern[r.PathPattern] = state
		}
		state.agents[r.AgentName] = true
		if r.Exclusive {
			state.exclusive = true
		}
	}

	var conflicts []AgentMailConflict
	for pattern, state := range byPattern {
		if !state.exclusive || len(state.agents) <= 1 {
			continue
		}
		var holders []string
		for name := range state.agents {
			holders = append(holders, name)
		}
		sort.Strings(holders)
		conflicts = append(conflicts, AgentMailConflict{Pattern: pattern, Holders: holders})
	}
	sort.SliceStable(conflicts, func(i, j int) bool { return conflicts[i].Pattern < conflicts[j].Pattern })
	return conflicts
}

// getGraphMetrics returns bv graph analysis metrics
func getGraphMetrics() *GraphMetrics {
	metrics := &GraphMetrics{
		HealthStatus: "unknown",
	}

	wd := mustGetwd()

	// Get drift status directly
	drift := bv.CheckDrift(wd)
	switch drift.Status {
	case bv.DriftOK:
		metrics.HealthStatus = "ok"
	case bv.DriftWarning:
		metrics.HealthStatus = "warning"
	case bv.DriftCritical:
		metrics.HealthStatus = "critical"
	case bv.DriftNoBaseline:
		metrics.HealthStatus = "unknown"
	default:
		metrics.HealthStatus = "unknown"
	}
	metrics.DriftMessage = drift.Message

	// Get insights once for bottlenecks and keystones
	insights, err := bv.GetInsights(wd)
	if err == nil && insights != nil {
		metrics.Keystones = len(insights.Keystones)

		// Top 3 bottlenecks
		limit := 3
		if len(insights.Bottlenecks) < limit {
			limit = len(insights.Bottlenecks)
		}
		for i := 0; i < limit; i++ {
			b := insights.Bottlenecks[i]
			metrics.TopBottlenecks = append(metrics.TopBottlenecks, BottleneckInfo{
				ID:    b.ID,
				Score: b.Value,
			})
		}
	}

	return metrics
}

// VersionOutput represents the output for --robot-version
type VersionOutput struct {
	RobotResponse
	System SystemInfo `json:"system"`
}

// GetVersion returns version information.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetVersion() (*VersionOutput, error) {
	return &VersionOutput{
		RobotResponse: NewRobotResponse(true),
		System: SystemInfo{
			Version:   Version,
			Commit:    Commit,
			BuildDate: Date,
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		},
	}, nil
}

// PrintVersion outputs version as JSON.
// This is a thin wrapper around GetVersion() for CLI output.
func PrintVersion() error {
	output, err := GetVersion()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetSessions returns a minimal session list.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSessions() ([]SessionInfo, error) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return []SessionInfo{}, nil
	}

	output := make([]SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		output = append(output, SessionInfo{
			Name:     sess.Name,
			Exists:   true,
			Attached: sess.Attached,
			Windows:  sess.Windows,
		})
	}
	return output, nil
}

// PrintSessions outputs minimal session list.
// This is a thin wrapper around GetSessions() for CLI output.
func PrintSessions() error {
	output, err := GetSessions()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetPlan generates an execution plan.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetPlan() (*PlanOutput, error) {
	plan := &PlanOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Actions:       []PlanAction{},
		BeadActions:   []BeadAction{},
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		plan.Recommendation = "Install tmux first"
		plan.Warnings = append(plan.Warnings, "tmux is not installed or not in PATH")
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "brew install tmux",
			Description: "Install tmux using Homebrew (macOS)",
		})
		return plan, nil
	}

	// Check for existing sessions
	sessions, _ := tmux.ListSessions()

	if len(sessions) == 0 {
		plan.Recommendation = "Create your first coding session"
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "ntm spawn myproject --cc=2",
			Description: "Create session with 2 Claude Code agents",
			Args:        []string{"spawn", "myproject", "--cc=2"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm tutorial",
			Description: "Learn NTM with an interactive tutorial",
			Args:        []string{"tutorial"},
		})
	} else {
		plan.Recommendation = "Attach to an existing session or create a new one"

		// Find unattached sessions
		for _, sess := range sessions {
			if !sess.Attached {
				plan.Actions = append(plan.Actions, PlanAction{
					Priority:    1,
					Command:     fmt.Sprintf("ntm attach %s", sess.Name),
					Description: fmt.Sprintf("Attach to session '%s' (%d windows)", sess.Name, sess.Windows),
					Args:        []string{"attach", sess.Name},
				})
			}
		}

		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm palette",
			Description: "Open command palette for quick actions",
			Args:        []string{"palette"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    3,
			Command:     "ntm dashboard",
			Description: "Open visual session dashboard",
			Args:        []string{"dashboard"},
		})
	}

	// Add bead-based recommendations from bv priority analysis
	beadActions, beadWarnings := getBeadRecommendations(5) // Top 5 recommendations
	plan.BeadActions = beadActions
	plan.Warnings = append(plan.Warnings, beadWarnings...)

	// Update recommendation if there are high-impact beads to work on
	if len(plan.BeadActions) > 0 && plan.BeadActions[0].IsReady {
		plan.Recommendation = fmt.Sprintf("Work on high-impact bead: %s", plan.BeadActions[0].Title)
	}

	return plan, nil
}

// PrintPlan outputs an execution plan.
// This is a thin wrapper around GetPlan() for CLI output.
func PrintPlan() error {
	output, err := GetPlan()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// getBeadRecommendations returns recommended bead actions from bv priority analysis
func getBeadRecommendations(limit int) ([]BeadAction, []string) {
	var actions []BeadAction
	var warnings []string

	// Check if bv is available
	if !bv.IsInstalled() {
		warnings = append(warnings, "bv (beads_viewer) not installed - install for bead-based recommendations")
		return actions, warnings
	}

	// Get priority recommendations from bv
	recommendations, err := bv.GetNextActions("", limit)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get bv priority: %v", err))
		return actions, warnings
	}

	// Get ready issues to check blockers
	readyIssues := getReadyIssueIDs()

	// Collect issue IDs for batch graph position lookup
	var issueIDs []string
	for _, rec := range recommendations {
		issueIDs = append(issueIDs, rec.IssueID)
	}

	// Get graph positions in batch for efficiency
	graphPositions, graphErr := bv.GetGraphPositionsBatch("", issueIDs)
	if graphErr != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get graph positions: %v", graphErr))
	}

	// Convert bv recommendations to BeadActions
	for _, rec := range recommendations {
		isReady := readyIssues[rec.IssueID]

		action := BeadAction{
			BeadID:    rec.IssueID,
			Title:     rec.Title,
			Priority:  rec.SuggestedPriority,
			Impact:    rec.ImpactScore,
			Reasoning: rec.Reasoning,
			Command:   fmt.Sprintf("bd update %s --status in_progress", rec.IssueID),
			IsReady:   isReady,
		}

		// Add graph position if available
		if graphPositions != nil {
			if pos, ok := graphPositions[rec.IssueID]; ok && pos != nil {
				action.GraphPosition = &GraphPosition{
					IsBottleneck:    pos.IsBottleneck,
					BottleneckScore: pos.BottleneckScore,
					IsKeystone:      pos.IsKeystone,
					KeystoneScore:   pos.KeystoneScore,
					IsHub:           pos.IsHub,
					HubScore:        pos.HubScore,
					IsAuthority:     pos.IsAuthority,
					AuthorityScore:  pos.AuthorityScore,
					Summary:         pos.Summary,
				}
			}
		}

		// If not ready, try to determine blockers
		if !isReady {
			blockers := getBlockersForIssue(rec.IssueID)
			action.BlockedBy = blockers
		}

		actions = append(actions, action)
	}

	return actions, warnings
}

// getReadyIssueIDs returns a set of issue IDs that are ready (unblocked)
func getReadyIssueIDs() map[string]bool {
	ready := make(map[string]bool)

	// Try to run bd ready --json to get ready issues
	output, err := bv.RunBd("", "ready", "--json")
	if err != nil {
		return ready
	}

	// Parse JSON array of issues
	var issues []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return ready
	}

	for _, issue := range issues {
		ready[issue.ID] = true
	}

	return ready
}

// getBlockersForIssue returns the IDs of issues blocking the given issue
func getBlockersForIssue(issueID string) []string {
	var blockers []string

	// Try to run bd show <id> --json to get dependencies
	output, err := bv.RunBd("", "show", issueID, "--json")
	if err != nil {
		return blockers
	}

	// Parse JSON - bd show returns an array with one element
	var issues []struct {
		Dependencies []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return blockers
	}

	if len(issues) > 0 {
		for _, dep := range issues[0].Dependencies {
			// Only include non-closed dependencies as blockers
			if dep.Status != "closed" {
				blockers = append(blockers, dep.ID)
			}
		}
	}

	return blockers
}

func detectAgentType(title string) string {
	// Try to detect from pane title
	titleLower := strings.ToLower(title)

	// Check canonical forms
	switch {
	case strings.Contains(titleLower, "claude"):
		return "claude"
	case strings.Contains(titleLower, "codex"):
		return "codex"
	case strings.Contains(titleLower, "gemini"):
		return "gemini"
	case strings.Contains(titleLower, "cursor"):
		return "cursor"
	case strings.Contains(titleLower, "windsurf"):
		return "windsurf"
	case strings.Contains(titleLower, "aider"):
		return "aider"
	}

	// Check short forms in pane titles (e.g., "session__cc_1", "project__cod_2")
	// The pattern is: prefix__<short>_suffix or prefix__<short>__suffix
	// We use word boundary matching via "__<short>_" or "__<short>__"
	switch {
	case containsShortForm(titleLower, "cc"):
		return "claude"
	case containsShortForm(titleLower, "cod"):
		return "codex"
	case containsShortForm(titleLower, "gmi"):
		return "gemini"
	}

	return "unknown"
}

// DetectAgentType detects the agent type from a pane title.
// Returns one of: "claude", "codex", "gemini", "cursor", "windsurf", "aider", or "unknown".
func DetectAgentType(title string) string {
	return detectAgentType(title)
}

// containsShortForm checks if title contains the short form as a word boundary pattern
// It matches patterns like "__cc_" or "__cc__" to avoid false positives
func containsShortForm(title, short string) bool {
	// Check for "__<short>_" or "__<short>__"
	pattern1 := "__" + short + "_"
	pattern2 := "__" + short + "__"
	return strings.Contains(title, pattern1) || strings.Contains(title, pattern2)
}

// ResolveAgentType maps agent type aliases to canonical names.
// For example: "cc" -> "claude", "cod" -> "codex"
func ResolveAgentType(t string) string {
	// Trim whitespace
	trimmed := strings.TrimSpace(t)

	lower := strings.ToLower(trimmed)
	switch lower {
	case "cc", "claude-code", "claude_code", "claude":
		return "claude"
	case "cod", "codex-cli", "codex_cli", "codex":
		return "codex"
	case "gmi", "gemini-cli", "gemini_cli", "gemini":
		return "gemini"
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	case "aider":
		return "aider"
	case "user":
		return "user"
	default:
		return lower
	}
}

// detectModel attempts to detect the model from agent type and pane title.
func detectModel(agentType, title string) string {
	titleLower := strings.ToLower(title)
	// Check for specific model mentions in title
	switch {
	case strings.Contains(titleLower, "opus"):
		return "opus"
	case strings.Contains(titleLower, "sonnet"):
		return "sonnet"
	case strings.Contains(titleLower, "haiku"):
		return "haiku"
	case strings.Contains(titleLower, "gpt4") || strings.Contains(titleLower, "gpt-4"):
		return "gpt4"
	case strings.Contains(titleLower, "o1"):
		return "o1"
	case strings.Contains(titleLower, "o3"):
		return "o3"
	case strings.Contains(titleLower, "o4-mini"):
		return "o4-mini"
	case strings.Contains(titleLower, "flash"):
		return "flash"
	case strings.Contains(titleLower, "pro"):
		return "pro"
	case strings.Contains(titleLower, "gemini"):
		return "gemini"
	}
	// Default models by agent type
	switch agentType {
	case "claude":
		return "sonnet" // Default Claude model
	case "codex":
		return "gpt4" // Default Codex model
	case "gemini":
		return "gemini" // Default Gemini model
	default:
		return "unknown"
	}
}

// encodeJSON outputs the payload using the current OutputFormat.
// Despite the name (kept for backward compatibility), this now supports
// multiple formats: json, toon, or auto (default).
func encodeJSON(v interface{}) error {
	return Output(applyVerbosity(v, OutputVerbosity), OutputFormat)
}

// TailOutput is the structured output for --robot-tail
type TailOutput struct {
	RobotResponse                       // Embed standard response fields (success, timestamp, error)
	Session       string                `json:"session"`
	CapturedAt    time.Time             `json:"captured_at"`
	Panes         map[string]PaneOutput `json:"panes"`
	AgentHints    *TailAgentHints       `json:"_agent_hints,omitempty"`
}

// TailAgentHints provides agent guidance specific to tail output
type TailAgentHints struct {
	IdleAgents   []string `json:"idle_agents,omitempty"`   // Panes with idle agents ready for prompts
	ActiveAgents []string `json:"active_agents,omitempty"` // Panes with actively working agents
	Suggestions  []string `json:"suggestions,omitempty"`   // Actionable hints
}

// PaneOutput contains captured output from a single pane
type PaneOutput struct {
	Type      string   `json:"type"`
	State     string   `json:"state"` // active, idle, unknown
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// TailOptions configures the GetTail operation.
type TailOptions struct {
	Session    string
	Lines      int
	PaneFilter []string
}

// GetTail returns recent pane output for AI consumption.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetTail(opts TailOptions) (*TailOutput, error) {
	if !tmux.SessionExists(opts.Session) {
		return &TailOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", opts.Session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:    opts.Session,
			CapturedAt: time.Now().UTC(),
			Panes:      make(map[string]PaneOutput),
		}, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return &TailOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("failed to get panes: %w", err),
				ErrCodeInternalError,
				"Check tmux is running and session is accessible",
			),
			Session:    opts.Session,
			CapturedAt: time.Now().UTC(),
			Panes:      make(map[string]PaneOutput),
		}, nil
	}

	output := &TailOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Panes:         make(map[string]PaneOutput),
	}

	// Build pane filter map
	filterMap := make(map[string]bool)
	for _, p := range opts.PaneFilter {
		filterMap[p] = true
	}
	hasFilter := len(filterMap) > 0

	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Skip if filter is set and this pane is not in it
		if hasFilter && !filterMap[paneKey] && !filterMap[pane.ID] {
			continue
		}

		// Capture pane output
		captured, err := tmux.CapturePaneOutput(pane.ID, opts.Lines)
		if err != nil {
			// Include empty output on error
			output.Panes[paneKey] = PaneOutput{
				Type:      detectAgentType(pane.Title),
				State:     "unknown",
				Lines:     []string{},
				Truncated: false,
			}
			continue
		}

		// Strip ANSI codes and split into lines
		cleanOutput := status.StripANSI(captured)
		outputLines := splitLines(cleanOutput)

		// Detect state from output
		agentType := detectAgentType(pane.Title)
		state := determineState(captured, agentType)

		// Check if truncated (we captured exactly the requested lines)
		truncated := len(outputLines) >= opts.Lines

		output.Panes[paneKey] = PaneOutput{
			Type:      agentType,
			State:     state,
			Lines:     outputLines,
			Truncated: truncated,
		}
	}

	// Generate agent hints based on pane states
	output.AgentHints = generateTailHints(output.Panes)

	return output, nil
}

// PrintTail outputs recent pane output for AI consumption.
// This is a thin wrapper around GetTail() for CLI output.
func PrintTail(session string, lines int, paneFilter []string) error {
	output, err := GetTail(TailOptions{
		Session:    session,
		Lines:      lines,
		PaneFilter: paneFilter,
	})
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// generateTailHints analyzes pane states and provides actionable hints for AI agents
func generateTailHints(panes map[string]PaneOutput) *TailAgentHints {
	var idle, active []string
	var suggestions []string

	for paneKey, pane := range panes {
		switch pane.State {
		case "idle":
			idle = append(idle, paneKey)
		case "active":
			active = append(active, paneKey)
		case "error":
			suggestions = append(suggestions, fmt.Sprintf("Pane %s has an error - check output", paneKey))
		}
	}

	// Sort for deterministic output (map iteration order is random)
	sort.Strings(idle)
	sort.Strings(active)

	// Generate suggestions based on state distribution
	if len(idle) > 0 && len(active) == 0 {
		suggestions = append(suggestions, fmt.Sprintf("All %d agents idle - ready for new prompts", len(idle)))
	} else if len(idle) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("%d idle agents available for parallel work", len(idle)))
	}
	if len(active) > 0 {
		suggestions = append(suggestions, fmt.Sprintf("%d agents actively working - wait or check progress", len(active)))
	}

	// Return nil if no useful hints
	if len(idle) == 0 && len(active) == 0 && len(suggestions) == 0 {
		return nil
	}

	return &TailAgentHints{
		IdleAgents:   idle,
		ActiveAgents: active,
		Suggestions:  suggestions,
	}
}

// determineState analyzes output to determine if agent is active, idle, or in error state.
// It delegates to the status package for consistent detection logic.
func determineState(output, agentType string) string {
	// Normalize agent type for status package (expects "cc", "cod", etc.)
	shortType := translateAgentTypeForStatus(agentType)

	if status.DetectErrorInOutput(output) != status.ErrorNone {
		return "error"
	}
	if status.DetectIdleFromOutput(output, shortType) {
		return "idle"
	}
	// If output is empty and it's a user pane, treat as idle (prompt)
	if strings.TrimSpace(output) == "" && (agentType == "" || agentType == "user") {
		return "idle"
	}
	// Otherwise assume active/working
	return "active"
}

// stripANSI removes ANSI escape sequences from text.
// This is a compatibility wrapper for status.StripANSI.
func stripANSI(s string) string {
	return status.StripANSI(s)
}

// detectState determines agent state from output lines and title.
// This is a compatibility wrapper for determineState that maintains
// the old function signature used by other files in this package.
func detectState(lines []string, title string) string {
	// Reconstruct the output from lines
	output := strings.Join(lines, "\n")
	agentType := detectAgentType(title)
	// Translate to short form for status package patterns
	agentType = translateAgentTypeForStatus(agentType)
	return determineState(output, agentType)
}

// translateAgentTypeForStatus converts long agent type names to short forms
// expected by the status package patterns.
func translateAgentTypeForStatus(agentType string) string {
	switch agentType {
	case "claude":
		return "cc"
	case "codex":
		return "cod"
	case "gemini":
		return "gmi"
	case "unknown":
		return ""
	default:
		return agentType
	}
}

// isIdlePrompt checks if a line looks like an idle prompt.
// This is a compatibility wrapper for status.IsPromptLine that uses an empty
// agent type for generic prompt detection.
func isIdlePrompt(line string) bool {
	return status.IsPromptLine(line, "")
}

// isPromptLine checks if a line looks like an idle prompt for a specific pane.
// This is a compatibility wrapper for status.IsPromptLine that extracts the
// agent type from the pane title.
func isPromptLine(line, paneTitle string) bool {
	agentType := translateAgentTypeForStatus(detectAgentType(paneTitle))
	return status.IsPromptLine(line, agentType)
}

// splitLines splits text into lines, preserving empty lines
func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	// Normalize line endings
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	lines := strings.Split(s, "\n")
	// Remove trailing empty line if present
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// fetchAgentMailData retrieves Agent Mail state for the project.
// Returns the summary, raw agent list, and per-agent stats map.
func fetchAgentMailData(projectKey string) (*SnapshotAgentMail, []agentmail.Agent, map[string]SnapshotAgentMailStats) {
	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))

	if !client.IsAvailable() {
		return nil, nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("ensure_project failed: %v", err),
			Project:   projectKey,
		}, nil, nil
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return &SnapshotAgentMail{
			Available: true,
			Reason:    fmt.Sprintf("list_agents failed: %v", err),
			Project:   projectKey,
		}, nil, nil
	}

	summary := &SnapshotAgentMail{
		Available: true,
		Project:   projectKey,
		Agents:    make(map[string]SnapshotAgentMailStats),
	}
	statsMap := make(map[string]SnapshotAgentMailStats)

	threadSet := make(map[string]struct{})
	for _, agent := range agents {
		if agent.Name == "HumanOverseer" {
			continue
		}

		inbox, err := client.FetchInbox(ctx, agentmail.FetchInboxOptions{
			ProjectKey:    projectKey,
			AgentName:     agent.Name,
			Limit:         25,
			IncludeBodies: false,
		})
		if err != nil {
			continue
		}
		unread := len(inbox)
		pendingAck := 0
		for _, msg := range inbox {
			if msg.AckRequired {
				pendingAck++
			}
			threadKey := ""
			if msg.ThreadID != nil && *msg.ThreadID != "" {
				threadKey = *msg.ThreadID
			} else {
				threadKey = fmt.Sprintf("%d", msg.ID)
			}
			threadSet[threadKey] = struct{}{}
		}
		summary.TotalUnread += unread
		stats := SnapshotAgentMailStats{
			Unread:     unread,
			PendingAck: pendingAck,
		}
		summary.Agents[agent.Name] = stats
		statsMap[agent.Name] = stats
	}

	if len(threadSet) > 0 {
		summary.ThreadsKnown = len(threadSet)
	}

	return summary, agents, statsMap
}

// resolveAgentsForSession maps pane titles to agent names for a specific session.
func resolveAgentsForSession(panes []tmux.Pane, mailAgents []agentmail.Agent) map[string]string {
	if len(mailAgents) == 0 || len(panes) == 0 {
		return nil
	}

	paneInfos := parseNTMPanes(panes)
	agentsByType := groupAgentsByType(mailAgents)
	mapping := make(map[string]string)

	for paneType, info := range paneInfos {
		if agents, ok := agentsByType[paneType]; ok {
			typeMapping := assignAgentsToPanes(info, agents)
			for k, v := range typeMapping {
				mapping[k] = v
			}
		}
	}

	return mapping
}

// buildSnapshotAgentMail assembles Agent Mail state for robot snapshot.
// Deprecated: Use fetchAgentMailData instead.
func buildSnapshotAgentMail() *SnapshotAgentMail {
	cwd, err := os.Getwd()
	if err != nil {
		return &SnapshotAgentMail{Available: false, Reason: "unable to determine working directory"}
	}
	summary, _, _ := fetchAgentMailData(cwd)
	return summary
}

// SnapshotOutput provides complete system state for AI orchestration
type SnapshotOutput struct {
	RobotResponse
	Timestamp      string             `json:"ts"`
	SafetyProfile  string             `json:"safety_profile,omitempty"`
	Sessions       []SnapshotSession  `json:"sessions"`
	Pagination     *PaginationInfo    `json:"pagination,omitempty"`
	AgentHints     *AgentHints        `json:"_agent_hints,omitempty"`
	BeadsSummary   *bv.BeadsSummary   `json:"beads_summary,omitempty"`
	AgentMail      *SnapshotAgentMail `json:"agent_mail,omitempty"`
	MailUnread     int                `json:"mail_unread,omitempty"`
	Tools          []ToolInfoOutput   `json:"tools,omitempty"`           // Flywheel tool inventory and health
	Swarm          *SwarmSnapshot     `json:"swarm,omitempty"`           // Active swarm orchestration state (optional)
	Alerts         []string           `json:"alerts"`                    // Legacy: simple string alerts
	AlertsDetailed []AlertInfo        `json:"alerts_detailed,omitempty"` // Rich alert objects
	AlertSummary   *AlertSummaryInfo  `json:"alert_summary,omitempty"`
}

// AlertInfo provides detailed alert information for robot output
type AlertInfo struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Severity   string                 `json:"severity"`
	Message    string                 `json:"message"`
	Session    string                 `json:"session,omitempty"`
	Pane       string                 `json:"pane,omitempty"`
	BeadID     string                 `json:"bead_id,omitempty"`
	Context    map[string]interface{} `json:"context,omitempty"`
	CreatedAt  string                 `json:"created_at"`
	DurationMs int64                  `json:"duration_ms"`
	Count      int                    `json:"count"`
}

// AlertSummaryInfo provides aggregate alert statistics
type AlertSummaryInfo struct {
	TotalActive int            `json:"total_active"`
	BySeverity  map[string]int `json:"by_severity"`
	ByType      map[string]int `json:"by_type"`
}

// SnapshotSession represents a session in the snapshot
type SnapshotSession struct {
	Name     string          `json:"name"`
	Attached bool            `json:"attached"`
	Agents   []SnapshotAgent `json:"agents"`
}

// SnapshotAgent represents an agent in the snapshot
type SnapshotAgent struct {
	Pane             string  `json:"pane"`
	Type             string  `json:"type"`              // claude, codex, gemini
	Variant          string  `json:"variant,omitempty"` // Model alias or persona name
	TypeConfidence   float64 `json:"type_confidence"`
	TypeMethod       string  `json:"type_method"`
	State            string  `json:"state"`
	LastOutputAgeSec int     `json:"last_output_age_sec"`
	OutputTailLines  int     `json:"output_tail_lines"`
	CurrentBead      *string `json:"current_bead"`
	PendingMail      int     `json:"pending_mail"`
}

// SnapshotAgentMail represents Agent Mail availability and inbox state.
type SnapshotAgentMail struct {
	Available    bool                              `json:"available"`
	Reason       string                            `json:"reason,omitempty"`
	Project      string                            `json:"project,omitempty"`
	TotalUnread  int                               `json:"total_unread,omitempty"`
	Agents       map[string]SnapshotAgentMailStats `json:"agents,omitempty"`
	ThreadsKnown int                               `json:"threads_known,omitempty"`
}

// SnapshotAgentMailStats holds per-agent inbox counts.
type SnapshotAgentMailStats struct {
	Pane       string `json:"pane,omitempty"`
	Unread     int    `json:"unread"`
	PendingAck int    `json:"pending_ack"`
}

type SwarmSnapshot struct {
	Active       bool               `json:"active"`
	Plan         SwarmSnapshotPlan  `json:"plan"`
	Sessions     []SwarmSessionInfo `json:"sessions"`
	Health       SwarmHealthSummary `json:"health"`
	RecentEvents []SwarmRecentEvent `json:"recent_events"`
}

type SwarmSnapshotPlan struct {
	CreatedAt   string                `json:"created_at"`
	ScanDir     string                `json:"scan_dir"`
	TotalAgents int                   `json:"total_agents"`
	Allocations []SwarmPlanAllocation `json:"allocations"`
}

type SwarmPlanAllocation struct {
	Project   string `json:"project"`
	Tier      int    `json:"tier"`
	OpenBeads int    `json:"open_beads"`
	CCAgents  int    `json:"cc_agents"`
	CodAgents int    `json:"cod_agents"`
	GmiAgents int    `json:"gmi_agents"`
}

type SwarmSessionInfo struct {
	Name      string `json:"name"`
	AgentType string `json:"agent_type"`
	PaneCount int    `json:"pane_count"`
	Healthy   int    `json:"healthy"`
	LimitHit  int    `json:"limit_hit"`
}

type SwarmHealthSummary struct {
	TotalAgents int `json:"total_agents"`
	Healthy     int `json:"healthy"`
	LimitHit    int `json:"limit_hit"`
	Respawning  int `json:"respawning"`
}

type SwarmRecentEvent struct {
	Type      string `json:"type"`
	Pane      string `json:"pane"`
	Timestamp string `json:"timestamp"`
}

// BeadLimit controls how many ready/in-progress beads to include in snapshot
var BeadLimit = 5

// GetSnapshot retrieves complete system state for AI orchestration.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSnapshot(cfg *config.Config) (*SnapshotOutput, error) {
	return GetSnapshotWithOptions(cfg, PaginationOptions{})
}

// GetSnapshotWithOptions retrieves complete system state with pagination applied to sessions.
func GetSnapshotWithOptions(cfg *config.Config, opts PaginationOptions) (*SnapshotOutput, error) {
	if cfg == nil {
		cfg = config.Default()
	}
	output := &SnapshotOutput{
		RobotResponse: NewRobotResponse(true),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		SafetyProfile: cfg.Safety.Profile,
		Sessions:      []SnapshotSession{},
		Alerts:        []string{},
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("tmux is not installed"),
			ErrCodeDependencyMissing,
			"Install tmux to enable snapshot",
		)
		output.Alerts = append(output.Alerts, "tmux is not installed")
		return output, nil
	}

	// Fetch Agent Mail data early
	var mailAgents []agentmail.Agent
	var mailStats map[string]SnapshotAgentMailStats
	var projectKey string

	cwd, err := os.Getwd()
	if err == nil {
		if root, err := git.FindProjectRoot(cwd); err == nil {
			projectKey = root
		} else {
			projectKey = cwd
		}
		// Build initial mail summary without pane mapping
		if summary, agents, stats := fetchAgentMailData(projectKey); summary != nil {
			output.AgentMail = summary
			output.MailUnread = summary.TotalUnread
			mailAgents = agents
			mailStats = stats
		}
	}

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// No sessions is not an error for snapshot
		return output, nil
	}

	for _, sess := range sessions {
		snapSession := SnapshotSession{
			Name:     sess.Name,
			Attached: sess.Attached,
			Agents:   []SnapshotAgent{},
		}

		// Get panes for this session
		panes, err := tmux.GetPanes(sess.Name)
		if err != nil {
			output.Alerts = append(output.Alerts, fmt.Sprintf("failed to get panes for %s: %v", sess.Name, err))
			continue
		}

		// Resolve agent mapping for this session
		agentMapping := resolveAgentsForSession(panes, mailAgents)

		for _, pane := range panes {
			// Capture output for state detection and enhanced type detection
			captured := ""
			capturedErr := error(nil)
			captured, capturedErr = tmux.CapturePaneOutput(pane.ID, 50)

			// Use enhanced agent type detection
			detection := DetectAgentTypeEnhanced(pane, captured)

			agent := SnapshotAgent{
				Pane:             fmt.Sprintf("%d.%d", 0, pane.Index),
				Type:             detection.Type,
				Variant:          pane.Variant,
				TypeConfidence:   detection.Confidence,
				TypeMethod:       string(detection.Method),
				State:            "unknown",
				LastOutputAgeSec: -1, // Unknown without pane_last_activity
				OutputTailLines:  0,
				CurrentBead:      nil,
				PendingMail:      0,
			}

			// Map pending mail if available
			if agentName, ok := agentMapping[pane.Title]; ok {
				if stats, ok := mailStats[agentName]; ok {
					agent.PendingMail = stats.Unread

					// Update the mail summary with the pane ID
					if output.AgentMail != nil && output.AgentMail.Agents != nil {
						if s, exists := output.AgentMail.Agents[agentName]; exists {
							s.Pane = agent.Pane
							output.AgentMail.Agents[agentName] = s
						}
					}
				}
			}

			// Process captured output for state
			if capturedErr == nil {
				lines := splitLines(status.StripANSI(captured))
				agent.OutputTailLines = len(lines)
				agent.State = determineState(captured, agent.Type)
			}

			snapSession.Agents = append(snapSession.Agents, agent)
		}

		output.Sessions = append(output.Sessions, snapSession)
	}

	output.Swarm = buildSwarmSnapshot(cfg, sessions)

	// Try to get beads summary
	beads := bv.GetBeadsSummary("", BeadLimit)
	if beads != nil {
		output.BeadsSummary = beads
	}

	// Add alerts for detected issues (legacy string format)
	for _, sess := range output.Sessions {
		for _, agent := range sess.Agents {
			if agent.State == "error" {
				output.Alerts = append(output.Alerts, fmt.Sprintf("agent %s in %s has error state", agent.Pane, sess.Name))
			}
		}
	}

	// Include tool inventory and health status
	toolCtx, toolCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer toolCancel()
	output.Tools = GetToolsSummary(toolCtx)

	// Generate and add detailed alerts using the alerts package
	var alertCfg alerts.Config
	if cfg != nil {
		alertCfg = alerts.ToConfigAlerts(
			cfg.Alerts.Enabled,
			cfg.Alerts.AgentStuckMinutes,
			cfg.Alerts.DiskLowThresholdGB,
			cfg.Alerts.MailBacklogThreshold,
			cfg.Alerts.BeadStaleHours,
			cfg.Alerts.ResolvedPruneMinutes,
			cfg.ProjectsBase,
		)
	} else {
		alertCfg = alerts.DefaultConfig()
	}
	activeAlerts := alerts.GetActiveAlerts(alertCfg)

	if len(activeAlerts) > 0 {
		output.AlertsDetailed = make([]AlertInfo, len(activeAlerts))
		for i, a := range activeAlerts {
			output.AlertsDetailed[i] = AlertInfo{
				ID:         a.ID,
				Type:       string(a.Type),
				Severity:   string(a.Severity),
				Message:    a.Message,
				Session:    a.Session,
				Pane:       a.Pane,
				BeadID:     a.BeadID,
				Context:    a.Context,
				CreatedAt:  a.CreatedAt.Format(time.RFC3339),
				DurationMs: a.Duration().Milliseconds(),
				Count:      a.Count,
			}
		}

		// Add to legacy alerts too for backwards compatibility
		for _, a := range activeAlerts {
			msg := a.Message
			if a.Session != "" {
				msg = a.Session + ": " + msg
			}
			output.Alerts = append(output.Alerts, msg)
		}

		// Add summary
		tracker := alerts.GetGlobalTracker()
		summary := tracker.Summary()
		output.AlertSummary = &AlertSummaryInfo{
			TotalActive: summary.TotalActive,
			BySeverity:  summary.BySeverity,
			ByType:      summary.ByType,
		}
	}

	if paged, page := ApplyPagination(output.Sessions, opts); page != nil {
		output.Sessions = paged
		output.Pagination = page
		if next, pages := paginationHintOffsets(page); next != nil {
			output.AgentHints = &AgentHints{
				NextOffset:     next,
				PagesRemaining: pages,
			}
		}
	}

	return output, nil
}

func buildSwarmSnapshot(cfg *config.Config, sessions []tmux.Session) *SwarmSnapshot {
	if cfg == nil || !cfg.Swarm.Enabled {
		return nil
	}

	type sessSpec struct {
		name      string
		agentType string
	}

	swarmSessions := make([]sessSpec, 0, len(sessions))
	for _, sess := range sessions {
		agentType, ok := parseSwarmSessionName(sess.Name)
		if !ok {
			continue
		}
		swarmSessions = append(swarmSessions, sessSpec{name: sess.Name, agentType: agentType})
	}
	if len(swarmSessions) == 0 {
		return nil
	}

	out := &SwarmSnapshot{
		Active:       true,
		Sessions:     make([]SwarmSessionInfo, 0, len(swarmSessions)),
		RecentEvents: []SwarmRecentEvent{},
	}

	totalAgents := 0
	totalHealthy := 0
	totalLimitHit := 0

	for _, sess := range swarmSessions {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		h, err := health.CheckSession(ctx, sess.name)
		cancel()
		if err != nil {
			continue
		}

		paneCount := 0
		healthy := 0
		limitHit := 0

		for _, agent := range h.Agents {
			// Swarm sessions are agent-only; ignore any user panes if present.
			if agent.AgentType == "user" {
				continue
			}
			paneCount++
			if agent.Status == health.StatusOK {
				healthy++
			}
			if agent.RateLimited {
				limitHit++
			}
		}

		totalAgents += paneCount
		totalHealthy += healthy
		totalLimitHit += limitHit

		out.Sessions = append(out.Sessions, SwarmSessionInfo{
			Name:      sess.name,
			AgentType: sess.agentType,
			PaneCount: paneCount,
			Healthy:   healthy,
			LimitHit:  limitHit,
		})
	}

	out.Health = SwarmHealthSummary{
		TotalAgents: totalAgents,
		Healthy:     totalHealthy,
		LimitHit:    totalLimitHit,
		Respawning:  0,
	}

	out.Plan = buildSwarmSnapshotPlan(cfg, totalAgents)

	return out
}

func buildSwarmSnapshotPlan(cfg *config.Config, fallbackTotalAgents int) SwarmSnapshotPlan {
	planOut := SwarmSnapshotPlan{
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
		ScanDir:     cfg.Swarm.DefaultScanDir,
		TotalAgents: fallbackTotalAgents,
		Allocations: []SwarmPlanAllocation{},
	}

	if cfg.Swarm.DefaultScanDir == "" {
		return planOut
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	scanner := swarmlib.NewBeadScanner(cfg.Swarm.DefaultScanDir)
	scan, err := scanner.Scan(ctx)
	if err != nil || scan == nil {
		return planOut
	}

	calc := swarmlib.NewAllocationCalculator(&cfg.Swarm)
	plan := calc.GenerateSwarmPlan(cfg.Swarm.DefaultScanDir, scan.Projects)
	if plan == nil {
		return planOut
	}

	planOut.CreatedAt = plan.CreatedAt.UTC().Format(time.RFC3339)
	planOut.ScanDir = plan.ScanDir
	planOut.TotalAgents = plan.TotalAgents
	planOut.Allocations = make([]SwarmPlanAllocation, 0, len(plan.Allocations))

	for _, alloc := range plan.Allocations {
		planOut.Allocations = append(planOut.Allocations, SwarmPlanAllocation{
			Project:   alloc.Project.Name,
			Tier:      alloc.Project.Tier,
			OpenBeads: alloc.Project.OpenBeads,
			CCAgents:  alloc.CCAgents,
			CodAgents: alloc.CodAgents,
			GmiAgents: alloc.GmiAgents,
		})
	}

	return planOut
}

func parseSwarmSessionName(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "cc_agents_"):
		return "cc", true
	case strings.HasPrefix(name, "cod_agents_"):
		return "cod", true
	case strings.HasPrefix(name, "gmi_agents_"):
		return "gmi", true
	default:
		return "", false
	}
}

// PrintSnapshot outputs complete system state for AI orchestration
func PrintSnapshot(cfg *config.Config) error {
	output, err := GetSnapshot(cfg)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// PrintSnapshotWithOptions outputs snapshot with pagination options.
func PrintSnapshotWithOptions(cfg *config.Config, opts PaginationOptions) error {
	output, err := GetSnapshotWithOptions(cfg, opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// agentTypeString converts AgentType to string for JSON
func agentTypeString(t tmux.AgentType) string {
	switch t {
	case tmux.AgentClaude:
		return "claude"
	case tmux.AgentCodex:
		return "codex"
	case tmux.AgentGemini:
		return "gemini"
	case tmux.AgentUser:
		return "user"
	default:
		return "unknown"
	}
}

func modelNameForPane(pane tmux.Pane, cfg *config.Config) string {
	if pane.Variant != "" {
		return pane.Variant
	}
	if cfg != nil {
		switch pane.Type {
		case tmux.AgentClaude:
			if cfg.Models.DefaultClaude != "" {
				return cfg.Models.DefaultClaude
			}
		case tmux.AgentCodex:
			if cfg.Models.DefaultCodex != "" {
				return cfg.Models.DefaultCodex
			}
		case tmux.AgentGemini:
			if cfg.Models.DefaultGemini != "" {
				return cfg.Models.DefaultGemini
			}
		}
	}
	switch pane.Type {
	case tmux.AgentClaude:
		return "claude-sonnet-4-20250514"
	case tmux.AgentCodex:
		return "gpt-4"
	case tmux.AgentGemini:
		return "gemini-2.0-flash"
	default:
		return ""
	}
}

// SendOutput is the structured output for --robot-send
type SendOutput struct {
	RobotResponse                     // Embed standard response fields (success, timestamp, error)
	Session        string             `json:"session"`
	SentAt         time.Time          `json:"sent_at"`
	Blocked        bool               `json:"blocked"`
	Redaction      RedactionSummary   `json:"redaction"`
	Warnings       []string           `json:"warnings"`
	Targets        []string           `json:"targets"`
	Successful     []string           `json:"successful"`
	Failed         []SendError        `json:"failed"`
	MessagePreview string             `json:"message_preview"`
	DryRun         bool               `json:"dry_run,omitempty"`
	WouldSendTo    []string           `json:"would_send_to,omitempty"`
	CASSInjection  *CASSInjectionInfo `json:"cass_injection,omitempty"`
	AgentHints     *SendAgentHints    `json:"_agent_hints,omitempty"`
}

// RedactionSummary is a safe-to-print summary of redaction findings.
// It intentionally does NOT include the matched secret values.
type RedactionSummary struct {
	Mode       string         `json:"mode"`
	Findings   int            `json:"findings"`
	Categories map[string]int `json:"categories,omitempty"`
	Action     string         `json:"action,omitempty"` // off|warn|redact|block
}

// CASSInjectionInfo reports CASS context injection details in robot responses.
type CASSInjectionInfo struct {
	// Enabled indicates whether CASS injection was enabled.
	Enabled bool `json:"enabled"`
	// Query is the search query that was executed.
	Query string `json:"query,omitempty"`
	// ItemsFound is how many CASS hits were found.
	ItemsFound int `json:"items_found"`
	// ItemsInjected is how many items were actually injected.
	ItemsInjected int `json:"items_injected"`
	// TokensAdded is the estimated token count of injected content.
	TokensAdded int `json:"tokens_added"`
	// Sources lists the sessions that provided context.
	Sources []CASSSource `json:"sources,omitempty"`
	// SkippedReason explains why injection was skipped, if applicable.
	SkippedReason string `json:"skipped_reason,omitempty"`
}

// CASSSource represents a session that provided CASS context.
type CASSSource struct {
	// Session is the session name or path.
	Session string `json:"session"`
	// Relevance is the relevance score (0-100).
	Relevance int `json:"relevance"`
	// AgeDays is how many days old the session is.
	AgeDays int `json:"age_days"`
}

// NewCASSInjectionInfo creates CASSInjectionInfo from an InjectionResult.
func NewCASSInjectionInfo(result InjectionResult, query string, hits []ScoredHit) *CASSInjectionInfo {
	info := &CASSInjectionInfo{
		Enabled:       result.Metadata.Enabled,
		Query:         query,
		ItemsFound:    result.Metadata.ItemsFound,
		ItemsInjected: result.Metadata.ItemsInjected,
		TokensAdded:   result.Metadata.TokensAdded,
		SkippedReason: result.Metadata.SkippedReason,
		Sources:       make([]CASSSource, 0, len(hits)),
	}

	now := time.Now()
	for _, hit := range hits {
		sessionDate := extractSessionDate(hit.SourcePath)
		ageDays := 0
		if !sessionDate.IsZero() {
			ageDays = int(now.Sub(sessionDate).Hours() / 24)
		}

		info.Sources = append(info.Sources, CASSSource{
			Session:   extractSessionName(hit.SourcePath),
			Relevance: int(hit.ComputedScore * 100),
			AgeDays:   ageDays,
		})
	}

	return info
}

// SendAgentHints provides agent guidance specific to send output
type SendAgentHints struct {
	Summary     string   `json:"summary,omitempty"`     // One-line summary of what happened
	Suggestions []string `json:"suggestions,omitempty"` // Actionable next steps
}

// SendError represents a failed send attempt
type SendError struct {
	Pane  string `json:"pane"`
	Error string `json:"error"`
}

// SendOptions configures the PrintSend operation
type SendOptions struct {
	Session    string // Target session name
	Message    string // Message to send
	Redaction  redaction.Config
	All        bool     // Send to all panes (including user)
	Panes      []string // Specific pane indices (e.g., "0", "1", "2")
	AgentTypes []string // Filter by agent types (e.g., "claude", "codex")
	Exclude    []string // Panes to exclude
	DelayMs    int      // Delay between sends in milliseconds
	DryRun     bool     // If true, show what would be sent without actually sending
	Enter      *bool    // If set, override Enter behavior after paste

	// CASS injection options
	WithCASS     bool          // Enable CASS context injection
	CASSConfig   *CASSConfig   // CASS query configuration (optional)
	FilterConfig *FilterConfig // CASS filter configuration (optional)
	InjectConfig *InjectConfig // CASS injection configuration (optional)
}

func normalizeSendRedactionConfig(cfg redaction.Config) redaction.Config {
	// Zero value is treated as off for backwards compatibility.
	if cfg.Mode == "" {
		cfg.Mode = redaction.ModeOff
	}
	return cfg
}

func summarizeSendRedactionResult(result redaction.Result) RedactionSummary {
	summary := RedactionSummary{
		Mode:     string(result.Mode),
		Findings: len(result.Findings),
	}

	cats := make(map[string]int, len(result.Findings))
	for _, f := range result.Findings {
		cats[string(f.Category)]++
	}
	if len(cats) > 0 {
		summary.Categories = cats
	}

	switch result.Mode {
	case redaction.ModeOff:
		summary.Action = "off"
	case redaction.ModeWarn:
		summary.Action = "warn"
	case redaction.ModeRedact:
		summary.Action = "redact"
	case redaction.ModeBlock:
		summary.Action = "block"
	}

	return summary
}

func formatRedactionCategoryCounts(categories map[string]int) string {
	if len(categories) == 0 {
		return ""
	}
	keys := make([]string, 0, len(categories))
	for k := range categories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, categories[k]))
	}
	return strings.Join(parts, ", ")
}

func applySendMessageRedaction(message string, cfg redaction.Config) (messageToSend string, preview string, summary RedactionSummary, warnings []string, blocked bool) {
	cfg = normalizeSendRedactionConfig(cfg)
	warnings = []string{}

	// Default summary always reflects configured mode.
	summary = RedactionSummary{
		Mode:     string(cfg.Mode),
		Findings: 0,
		Action:   string(cfg.Mode),
	}
	if cfg.Mode == redaction.ModeOff {
		summary.Action = "off"
		return message, truncateMessage(message), summary, warnings, false
	}

	result := redaction.ScanAndRedact(message, cfg)
	summary = summarizeSendRedactionResult(result)

	if len(result.Findings) == 0 {
		// No findings: keep message unchanged regardless of mode.
		return message, truncateMessage(message), summary, warnings, false
	}

	switch cfg.Mode {
	case redaction.ModeWarn:
		msg := "Warning: potential secrets detected in message"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)
		return message, truncateMessage(message), summary, warnings, false
	case redaction.ModeRedact:
		msg := "Warning: redacted potential secrets in message"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)
		return result.Output, truncateMessage(result.Output), summary, warnings, false
	case redaction.ModeBlock:
		msg := "Blocked: potential secrets detected in message (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			msg = fmt.Sprintf("%s (%s)", msg, parts)
		}
		warnings = append(warnings, msg)

		previewCfg := cfg
		previewCfg.Mode = redaction.ModeRedact
		previewRes := redaction.ScanAndRedact(message, previewCfg)
		return "", truncateMessage(previewRes.Output), summary, warnings, true
	default:
		return message, truncateMessage(message), summary, warnings, false
	}
}

// GetSend sends a message to multiple panes atomically and returns structured results.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSend(opts SendOptions) (*SendOutput, error) {
	redactCfg := normalizeSendRedactionConfig(opts.Redaction)
	_, initialPreview, initialSummary, initialWarnings, initialBlocked := applySendMessageRedaction(opts.Message, redactCfg)

	if initialBlocked {
		errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(initialSummary.Categories); parts != "" {
			errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
		}
		return &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        true,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{},
			MessagePreview: initialPreview,
		}, nil
	}

	if strings.TrimSpace(opts.Session) == "" {
		return &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("session name is required"), ErrCodeInvalidFlag, "Provide a session name"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: "session name is required"}},
			MessagePreview: initialPreview,
		}, nil
	}

	if !tmux.SessionExists(opts.Session) {
		return &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("session '%s' not found", opts.Session), ErrCodeSessionNotFound, "Use 'ntm list' to see available sessions"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "session", Error: fmt.Sprintf("session '%s' not found", opts.Session)}},
			MessagePreview: initialPreview,
		}, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		return &SendOutput{
			RobotResponse:  NewErrorResponse(fmt.Errorf("failed to get panes: %w", err), ErrCodeInternalError, "Check tmux is running"),
			Session:        opts.Session,
			SentAt:         time.Now().UTC(),
			Blocked:        false,
			Redaction:      initialSummary,
			Warnings:       initialWarnings,
			Targets:        []string{},
			Successful:     []string{},
			Failed:         []SendError{{Pane: "panes", Error: fmt.Sprintf("failed to get panes: %v", err)}},
			MessagePreview: initialPreview,
		}, nil
	}

	output := SendOutput{
		RobotResponse:  NewRobotResponse(true), // Will be updated based on results
		Session:        opts.Session,
		SentAt:         time.Now().UTC(),
		Blocked:        false,
		Redaction:      initialSummary,
		Warnings:       initialWarnings,
		Targets:        []string{},
		Successful:     []string{},
		Failed:         []SendError{},
		MessagePreview: initialPreview,
	}

	// Build exclusion map
	excludeMap := make(map[string]bool)
	for _, e := range opts.Exclude {
		excludeMap[e] = true
	}

	// Build pane filter map (if specific panes requested)
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	// Build agent type filter map
	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[strings.ToLower(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Determine which panes to target
	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Check exclusions
		if excludeMap[paneKey] || excludeMap[pane.ID] {
			continue
		}

		// Check specific pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		// Check agent type filter
		if hasTypeFilter {
			// Use authoritative type if available, otherwise fallback to loose detection
			agentType := agentTypeString(pane.Type)
			if agentType == "user" || agentType == "unknown" {
				agentType = detectAgentType(pane.Title)
			}

			if !typeFilterMap[agentType] {
				continue
			}
		}

		// If not --all and no filters, skip user panes by default
		if !opts.All && !hasPaneFilter && !hasTypeFilter {
			agentType := detectAgentType(pane.Title)
			// Skip user panes (first pane or explicitly marked as user)
			if pane.Index == 0 && agentType == "unknown" {
				continue
			}
			if agentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
		output.Targets = append(output.Targets, paneKey)
	}

	// Perform CASS injection if enabled
	messageToSend := opts.Message
	if opts.WithCASS {
		// Use provided configs or defaults
		queryConfig := DefaultCASSConfig()
		if opts.CASSConfig != nil {
			queryConfig = *opts.CASSConfig
		}

		filterConfig := DefaultFilterConfig()
		if opts.FilterConfig != nil {
			filterConfig = *opts.FilterConfig
		}

		injectConfig := DefaultInjectConfig()
		if opts.InjectConfig != nil {
			injectConfig = *opts.InjectConfig
		}

		// If there are target panes, try to determine agent type for formatting
		if len(targetPanes) > 0 {
			agentType := detectAgentType(targetPanes[0].Title)
			injectConfig.Format = FormatForAgent(agentType)
		}

		// Perform CASS query and injection
		injectResult, queryResult, filterResult := InjectContextFromQuery(
			opts.Message,
			queryConfig,
			filterConfig,
			injectConfig,
		)

		// Record injection metadata
		output.CASSInjection = NewCASSInjectionInfo(injectResult, queryResult.Query, filterResult.Hits)

		// Use modified message if injection succeeded
		if injectResult.Success && injectResult.ModifiedPrompt != "" {
			messageToSend = injectResult.ModifiedPrompt
		}
	}

	// Redaction preflight on final outbound message (after CASS injection, if any).
	redacted, preview, summary, warnings, blocked := applySendMessageRedaction(messageToSend, redactCfg)
	output.Redaction = summary
	output.Warnings = warnings
	output.Blocked = blocked
	output.MessagePreview = preview

	if blocked {
		errMsg := "refusing to proceed: potential secrets detected (redaction mode: block)"
		if parts := formatRedactionCategoryCounts(summary.Categories); parts != "" {
			errMsg = fmt.Sprintf("refusing to proceed: potential secrets detected (%s) (redaction mode: block)", parts)
		}
		output.RobotResponse = NewErrorResponse(fmt.Errorf("%s", errMsg), "SENSITIVE_DATA_BLOCKED", "Re-run with --allow-secret to bypass, or use --redact=warn/--redact=redact")
		output.Success = false
		return &output, nil
	}
	messageToSend = redacted

	// Dry-run mode: show what would happen without sending
	if opts.DryRun {
		output.DryRun = true
		if len(output.Targets) > 0 {
			output.WouldSendTo = append(output.WouldSendTo, output.Targets...)
			output.Success = true
		} else {
			output.Success = false
			output.Error = "no target panes matched the filter criteria"
			output.ErrorCode = ErrCodeInvalidFlag
		}
		return &output, nil
	}

	sendEnter := true
	if opts.Enter != nil {
		sendEnter = *opts.Enter
	}

	// Send to all targets
	for i, pane := range targetPanes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Apply delay between sends (except for first)
		if i > 0 && opts.DelayMs > 0 {
			time.Sleep(time.Duration(opts.DelayMs) * time.Millisecond)
		}

		// Determine appropriate Enter delay based on pane type.
		// User/shell panes need a longer delay than AI agent TUIs because
		// shells (bash, zsh) have different input buffering behavior.
		enterDelay := tmux.DefaultEnterDelay
		agentType := detectAgentType(pane.Title)
		if pane.Type == tmux.AgentUser || agentType == "user" || agentType == "unknown" {
			enterDelay = tmux.ShellEnterDelay
		}

		// Use agent-aware send method which handles Gemini's multi-line quirks
		// by using buffer-based paste instead of send-keys when content has newlines
		err := tmux.SendKeysForAgentWithDelay(pane.ID, messageToSend, sendEnter, enterDelay, pane.Type)
		if err != nil {
			output.Failed = append(output.Failed, SendError{
				Pane:  paneKey,
				Error: err.Error(),
			})
		} else {
			output.Successful = append(output.Successful, paneKey)
		}
	}

	// Update success based on results
	output.Success = len(output.Failed) == 0 && len(output.Successful) > 0
	if len(output.Failed) > 0 {
		output.Error = fmt.Sprintf("%d of %d sends failed", len(output.Failed), len(output.Targets))
		output.ErrorCode = ErrCodeInternalError
	}

	// Generate agent hints
	output.AgentHints = generateSendHints(output)

	return &output, nil
}

// PrintSend outputs the send operation result as JSON.
// This is a thin wrapper around GetSend() for CLI output.
func PrintSend(opts SendOptions) error {
	output, err := GetSend(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// generateSendHints creates actionable hints based on send results
func generateSendHints(output SendOutput) *SendAgentHints {
	var suggestions []string
	var summary string

	if len(output.Failed) == 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Sent to %d agent(s) successfully", len(output.Successful))
		suggestions = append(suggestions, "Wait for agent acknowledgment using --robot-tail")
	} else if len(output.Failed) > 0 && len(output.Successful) > 0 {
		summary = fmt.Sprintf("Partial success: %d sent, %d failed", len(output.Successful), len(output.Failed))
		suggestions = append(suggestions, "Retry failed panes individually")
	} else if len(output.Failed) > 0 {
		summary = fmt.Sprintf("All %d sends failed", len(output.Failed))
		suggestions = append(suggestions, "Check agent states with --robot-tail")
		suggestions = append(suggestions, "Verify session and pane existence")
	} else if len(output.Targets) == 0 {
		summary = "No target panes matched the filter criteria"
		suggestions = append(suggestions, "Check --all, --panes, or --agent-types flags")
	}

	if summary == "" {
		return nil
	}

	return &SendAgentHints{
		Summary:     summary,
		Suggestions: suggestions,
	}
}

// truncateMessage truncates a message to 50 runes with ellipsis.
// Uses rune count instead of byte count to handle UTF-8 correctly.
func truncateMessage(msg string) string {
	runes := []rune(msg)
	if len(runes) > 50 {
		return string(runes[:47]) + "..."
	}
	return msg
}

// SnapshotDeltaOutput provides changes since a given timestamp.
type SnapshotDeltaOutput struct {
	RobotResponse
	Timestamp string   `json:"ts"`
	Since     string   `json:"since"`
	Changes   []Change `json:"changes"`
}

// Change represents a state change event.
type Change struct {
	Type    string                 `json:"type"`
	Session string                 `json:"session,omitempty"`
	Pane    string                 `json:"pane,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// GetSnapshotDelta retrieves state changes since the given timestamp.
// Uses the internal state tracker ring buffer to return delta changes.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSnapshotDelta(since time.Time) (*SnapshotDeltaOutput, error) {
	output := &SnapshotDeltaOutput{
		RobotResponse: NewRobotResponse(true),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		Since:         since.Format(time.RFC3339),
		Changes:       []Change{},
	}

	// Query the state tracker for changes since the given timestamp
	trackerChanges := stateTracker.Since(since)

	// Convert tracker.StateChange to robot.Change
	for _, tc := range trackerChanges {
		change := Change{
			Type:    string(tc.Type),
			Session: tc.Session,
			Pane:    tc.Pane,
			Data:    tc.Details,
		}
		output.Changes = append(output.Changes, change)
	}

	return output, nil
}

// PrintSnapshotDelta outputs changes since the given timestamp.
func PrintSnapshotDelta(since time.Time) error {
	output, err := GetSnapshotDelta(since)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// RecordStateChange records a state change to the global tracker.
// This should be called by other parts of the application when state changes occur.
func RecordStateChange(changeType tracker.ChangeType, session, pane string, details map[string]interface{}) {
	stateTracker.Record(tracker.StateChange{
		Timestamp: time.Now(),
		Type:      changeType,
		Session:   session,
		Pane:      pane,
		Details:   details,
	})
}

// GetStateTracker returns the global state tracker for direct access.
func GetStateTracker() *tracker.StateTracker {
	return stateTracker
}

// GraphOutput provides project graph analysis from bv
type GraphOutput struct {
	RobotResponse
	GeneratedAt time.Time            `json:"generated_at"`
	Available   bool                 `json:"available"`
	Error       string               `json:"error,omitempty"`
	Insights    *bv.InsightsResponse `json:"insights,omitempty"`
	Priority    *bv.PriorityResponse `json:"priority,omitempty"`
	Health      *bv.HealthSummary    `json:"health,omitempty"`
	Correlation *GraphCorrelation    `json:"correlation,omitempty"`
}

// GraphCorrelation provides a best-effort cross-tool view of agents, beads, and mail threads.
type GraphCorrelation struct {
	GeneratedAt   time.Time                   `json:"generated_at"`
	Assignments   []GraphAgentAssignment      `json:"assignments"`
	BeadGraph     map[string]GraphBeadNode    `json:"bead_graph"`
	MailSummary   map[string]GraphMailSummary `json:"mail_summary"`
	OrphanBeads   []string                    `json:"orphan_beads"`
	OrphanThreads []string                    `json:"orphan_threads"`
	Errors        []string                    `json:"errors,omitempty"`
}

// GraphAgentAssignment captures bead/thread membership for an agent.
type GraphAgentAssignment struct {
	Agent        string   `json:"agent"`
	AgentName    string   `json:"agent_name,omitempty"`
	AgentType    string   `json:"agent_type"`
	Program      string   `json:"program,omitempty"`
	Model        string   `json:"model,omitempty"`
	Beads        []string `json:"beads"`
	MailThreads  []string `json:"mail_threads"`
	Pane         string   `json:"pane,omitempty"`
	Session      string   `json:"session,omitempty"`
	Detected     string   `json:"detected_type,omitempty"`
	DetectedFrom string   `json:"detected_from,omitempty"`
}

// GraphBeadNode summarizes bead status and relationships.
type GraphBeadNode struct {
	Status     string   `json:"status"`
	AssignedTo *string  `json:"assigned_to"`
	BlockedBy  []string `json:"blocked_by"`
	Blocking   []string `json:"blocking"`
	Title      string   `json:"title,omitempty"`
}

// GraphMailSummary summarizes a mail thread for correlation.
type GraphMailSummary struct {
	Subject      string    `json:"subject"`
	Participants []string  `json:"participants,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	Unread       int       `json:"unread,omitempty"`
}

// GetGraph returns bv graph insights for AI consumption.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetGraph() (*GraphOutput, error) {
	output := &GraphOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Available:     bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeDependencyMissing,
			"Install bv to enable graph insights",
		)
		// Even if bv is missing, still attempt correlation to provide partial data.
	} else {
		wd := mustGetwd()

		// Get insights (bottlenecks, keystones, etc.)
		insights, err := bv.GetInsights(wd)
		if err != nil {
			output.Error = fmt.Sprintf("failed to get insights: %v", err)
			output.RobotResponse = NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check bv graph data and repository state",
			)
		} else {
			output.Insights = insights
		}

		// Get priority recommendations
		priority, err := bv.GetPriority(wd)
		if err != nil {
			if output.Error == "" {
				output.Error = fmt.Sprintf("failed to get priority: %v", err)
			}
		} else {
			output.Priority = priority
		}

		// Get health summary
		health, err := bv.GetHealthSummary(wd)
		if err != nil {
			if output.Error == "" {
				output.Error = fmt.Sprintf("failed to get health: %v", err)
			}
		} else {
			output.Health = health
		}
	}

	// Build correlation graph (best-effort, independent of bv availability)
	output.Correlation = buildCorrelationGraph()

	return output, nil
}

// PrintGraph outputs bv graph insights for AI consumption.
// This is a thin wrapper around GetGraph() for CLI output.
func PrintGraph() error {
	output, err := GetGraph()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// buildCorrelationGraph assembles a best-effort correlation map across agents, beads, and mail.
func buildCorrelationGraph() *GraphCorrelation {
	now := time.Now().UTC()
	corr := &GraphCorrelation{
		GeneratedAt:   now,
		Assignments:   make([]GraphAgentAssignment, 0),
		BeadGraph:     make(map[string]GraphBeadNode),
		MailSummary:   make(map[string]GraphMailSummary),
		OrphanBeads:   make([]string, 0),
		OrphanThreads: make([]string, 0),
	}

	wd, err := os.Getwd()
	if err != nil {
		corr.Errors = append(corr.Errors, fmt.Sprintf("working directory unavailable: %v", err))
		return corr
	}

	// Collect Agent Mail agents (if available)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentMailClient := agentmail.NewClient(agentmail.WithProjectKey(wd))
	var agents []agentmail.Agent
	if agentMailClient.IsAvailable() {
		if _, err := agentMailClient.EnsureProject(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail ensure_project: %v", err))
		} else if list, err := agentMailClient.ListProjectAgents(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail list_agents: %v", err))
		} else {
			agents = list
		}
	} else {
		corr.Errors = append(corr.Errors, "agent mail not available")
	}

	assignmentByAgent := make(map[string]*GraphAgentAssignment)
	for _, a := range agents {
		if a.Name == "HumanOverseer" {
			continue
		}
		assignmentByAgent[a.Name] = &GraphAgentAssignment{
			Agent:       a.Name,
			AgentName:   a.Name,
			AgentType:   normalizedProgramType(a.Program),
			Program:     a.Program,
			Model:       a.Model,
			Beads:       make([]string, 0),
			MailThreads: make([]string, 0),
		}
	}

	// Add bead assignments from bv summary (if present)
	if beads := bv.GetBeadsSummary(wd, BeadLimit); beads != nil && beads.Available {
		for _, inProg := range beads.InProgressList {
			node := GraphBeadNode{
				Status:    "in_progress",
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     inProg.Title,
			}
			if inProg.Assignee != "" {
				assign := inProg.Assignee
				node.AssignedTo = &assign
				a := assignmentByAgent[assign]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       assign,
						AgentName:   assign,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[assign] = a
				}
				a.Beads = appendUnique(a.Beads, inProg.ID)
			} else {
				corr.OrphanBeads = appendUnique(corr.OrphanBeads, inProg.ID)
			}
			corr.BeadGraph[inProg.ID] = node
		}

		for _, ready := range beads.ReadyPreview {
			status := "ready"
			node := GraphBeadNode{
				Status:    status,
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     ready.Title,
			}
			corr.BeadGraph[ready.ID] = node
		}
	} else if beads != nil && !beads.Available && beads.Reason != "" {
		corr.Errors = append(corr.Errors, fmt.Sprintf("beads unavailable: %s", beads.Reason))
	}

	// Gather mail threads from per-agent inboxes (best-effort, bounded).
	if len(agents) > 0 && agentMailClient.IsAvailable() {
		const inboxLimit = 50
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}

			inbox, err := agentMailClient.FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey:    wd,
				AgentName:     a.Name,
				Limit:         inboxLimit,
				IncludeBodies: false,
			})
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail fetch_inbox %s: %v", a.Name, err))
				continue
			}
			for _, msg := range inbox {
				if msg.ThreadID == nil || strings.TrimSpace(*msg.ThreadID) == "" {
					continue
				}
				tid := strings.TrimSpace(*msg.ThreadID)
				thread := corr.MailSummary[tid]
				if thread.Subject == "" {
					thread.Subject = msg.Subject
				}
				if msg.CreatedTS.After(thread.LastActivity) {
					thread.LastActivity = msg.CreatedTS.Time
				}
				thread.Unread++
				corr.MailSummary[tid] = thread

				assign := assignmentByAgent[a.Name]
				if assign == nil {
					assign = &GraphAgentAssignment{
						Agent:       a.Name,
						AgentName:   a.Name,
						AgentType:   normalizedProgramType(a.Program),
						Program:     a.Program,
						Model:       a.Model,
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[a.Name] = assign
				}
				assign.MailThreads = appendUnique(assign.MailThreads, tid)
			}
		}

		// Add participants (best-effort) for a few most-recent threads.
		var threadIDs []string
		for tid := range corr.MailSummary {
			threadIDs = append(threadIDs, tid)
		}
		sort.SliceStable(threadIDs, func(i, j int) bool {
			return corr.MailSummary[threadIDs[i]].LastActivity.After(corr.MailSummary[threadIDs[j]].LastActivity)
		})

		const maxSummaries = 10
		for i, tid := range threadIDs {
			if i >= maxSummaries {
				break
			}
			summary, err := agentMailClient.SummarizeThread(ctx, agentmail.SummarizeThreadOptions{
				ProjectKey:      wd,
				ThreadID:        tid,
				IncludeExamples: false,
			})
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("summarize_thread %s: %v", tid, err))
				continue
			}
			thread := corr.MailSummary[tid]
			thread.Participants = summary.Participants
			corr.MailSummary[tid] = thread

			for _, participant := range summary.Participants {
				a := assignmentByAgent[participant]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       participant,
						AgentName:   participant,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[participant] = a
				}
				a.MailThreads = appendUnique(a.MailThreads, tid)
			}
		}
	}

	// Best-effort tmux pane mapping for Agent Mail agents (NTM sessions).
	if tmux.IsInstalled() {
		sessions, err := tmux.ListSessions()
		if err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("tmux list_sessions: %v", err))
		} else {
			agentsByType := groupAgentsByType(agents)
			for _, sess := range sessions {
				panes, err := tmux.GetPanes(sess.Name)
				if err != nil {
					continue
				}
				paneInfos := parseNTMPanes(panes)
				for _, paneType := range []string{"cc", "cod", "gmi"} {
					mapping := assignAgentsToPanes(paneInfos[paneType], agentsByType[paneType])
					for _, pane := range paneInfos[paneType] {
						agentName := mapping[pane.Label]
						if agentName == "" {
							continue
						}
						a := assignmentByAgent[agentName]
						if a == nil {
							a = &GraphAgentAssignment{
								Agent:       agentName,
								AgentName:   agentName,
								AgentType:   normalizedProgramType(""),
								Beads:       make([]string, 0),
								MailThreads: make([]string, 0),
							}
							assignmentByAgent[agentName] = a
						}
						a.Session = sess.Name
						a.Pane = fmt.Sprintf("%d.%d", 0, pane.TmuxIndex)
						a.Agent = fmt.Sprintf("%s:%s", sess.Name, a.Pane)
						a.Detected = paneType
						a.DetectedFrom = "ntm_pane_title"
					}
				}
			}
		}
	}

	// Fill dependency edges for in-progress beads (best-effort, bounded).
	if _, err := exec.LookPath("bd"); err == nil {
		for beadID, node := range corr.BeadGraph {
			if node.Status != "in_progress" {
				continue
			}
			blockedBy, deps, err := getBeadNeighbors(wd, beadID, "down")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("bd dep tree down %s: %v", beadID, err))
			} else {
				node.BlockedBy = blockedBy
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			blocking, deps, err := getBeadNeighbors(wd, beadID, "up")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("bd dep tree up %s: %v", beadID, err))
			} else {
				node.Blocking = blocking
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			corr.BeadGraph[beadID] = node
		}
	}

	// Orphan threads: threads not linked to any bead ID.
	for tid := range corr.MailSummary {
		if _, ok := corr.BeadGraph[tid]; !ok {
			corr.OrphanThreads = appendUnique(corr.OrphanThreads, tid)
		}
	}

	// Materialize assignment list (stable order).
	for _, a := range assignmentByAgent {
		corr.Assignments = append(corr.Assignments, *a)
	}
	sort.SliceStable(corr.Assignments, func(i, j int) bool {
		return corr.Assignments[i].AgentName < corr.Assignments[j].AgentName
	})

	return corr
}

// appendUnique adds a value if absent.
func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

type bdDepTreeNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
}

func getBeadNeighbors(dir, issueID, direction string) ([]string, []bdDepTreeNode, error) {
	if issueID == "" {
		return nil, nil, fmt.Errorf("issue id is empty")
	}
	if direction != "down" && direction != "up" {
		return nil, nil, fmt.Errorf("invalid direction %q", direction)
	}

	out, err := bv.RunBd(dir, "dep", "tree", issueID, "--direction="+direction, "--max-depth=1", "--json")
	if err != nil {
		return nil, nil, fmt.Errorf("bd dep tree: %w", err)
	}

	var nodes []bdDepTreeNode
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, nil, fmt.Errorf("parse bd dep tree json: %w", err)
	}

	seen := make(map[string]bool)
	ids := make([]string, 0)
	cleaned := make([]bdDepTreeNode, 0)
	for _, n := range nodes {
		n.ID = strings.TrimSpace(n.ID)
		if n.ID == "" || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		if strings.TrimSpace(n.Status) == "" {
			n.Status = "unknown"
		}
		ids = append(ids, n.ID)
		cleaned = append(cleaned, n)
	}

	sort.Strings(ids)
	sort.SliceStable(cleaned, func(i, j int) bool { return cleaned[i].ID < cleaned[j].ID })
	return ids, cleaned, nil
}

// AlertsOutput provides machine-readable alert information
type AlertsOutput struct {
	RobotResponse
	GeneratedAt time.Time        `json:"generated_at"`
	Enabled     bool             `json:"enabled"`
	Active      []AlertInfo      `json:"active"`
	Resolved    []AlertInfo      `json:"resolved,omitempty"`
	Summary     AlertSummaryInfo `json:"summary"`
}

// GetAlertsDetailed returns all alerts.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetAlertsDetailed(includeResolved bool) (*AlertsOutput, error) {
	alertCfg := alerts.DefaultConfig()
	tracker := alerts.GenerateAndTrack(alertCfg)

	active, resolved := tracker.GetAll()
	summary := tracker.Summary()

	output := &AlertsOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Enabled:       alertCfg.Enabled,
		Active:        make([]AlertInfo, len(active)),
		Summary: AlertSummaryInfo{
			TotalActive: summary.TotalActive,
			BySeverity:  summary.BySeverity,
			ByType:      summary.ByType,
		},
	}

	for i, a := range active {
		output.Active[i] = AlertInfo{
			ID:         a.ID,
			Type:       string(a.Type),
			Severity:   string(a.Severity),
			Message:    a.Message,
			Session:    a.Session,
			Pane:       a.Pane,
			BeadID:     a.BeadID,
			Context:    a.Context,
			CreatedAt:  a.CreatedAt.Format(time.RFC3339),
			DurationMs: a.Duration().Milliseconds(),
			Count:      a.Count,
		}
	}

	if includeResolved {
		output.Resolved = make([]AlertInfo, len(resolved))
		for i, a := range resolved {
			output.Resolved[i] = AlertInfo{
				ID:         a.ID,
				Type:       string(a.Type),
				Severity:   string(a.Severity),
				Message:    a.Message,
				Session:    a.Session,
				Pane:       a.Pane,
				BeadID:     a.BeadID,
				Context:    a.Context,
				CreatedAt:  a.CreatedAt.Format(time.RFC3339),
				DurationMs: a.Duration().Milliseconds(),
				Count:      a.Count,
			}
		}
	}

	return output, nil
}

// PrintAlertsDetailed outputs all alerts in JSON format.
// This is a thin wrapper around GetAlertsDetailed() for CLI output.
func PrintAlertsDetailed(includeResolved bool) error {
	output, err := GetAlertsDetailed(includeResolved)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// RecipeInfo represents a recipe in JSON output
type RecipeInfo struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source"` // builtin, user, project
	TotalAgents int               `json:"total_agents"`
	Agents      []RecipeAgentInfo `json:"agents"`
}

// RecipeAgentInfo represents an agent specification in a recipe
type RecipeAgentInfo struct {
	Type    string `json:"type"` // cc, cod, gmi
	Count   int    `json:"count"`
	Model   string `json:"model,omitempty"`
	Persona string `json:"persona,omitempty"`
}

// RecipesOutput is the structured output for --robot-recipes
type RecipesOutput struct {
	RobotResponse
	GeneratedAt time.Time    `json:"generated_at"`
	Count       int          `json:"count"`
	Recipes     []RecipeInfo `json:"recipes"`
}

// GetRecipes returns available recipes for AI orchestrators.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetRecipes() (*RecipesOutput, error) {
	loader := recipe.NewLoader()
	recipes, err := loader.LoadAll()
	if err != nil {
		// Return empty list on error
		return &RecipesOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Check recipe configuration and file paths",
			),
			GeneratedAt: time.Now().UTC(),
			Count:       0,
			Recipes:     []RecipeInfo{},
		}, nil
	}

	output := &RecipesOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Count:         len(recipes),
		Recipes:       make([]RecipeInfo, len(recipes)),
	}

	for i, r := range recipes {
		agents := make([]RecipeAgentInfo, len(r.Agents))
		for j, a := range r.Agents {
			agents[j] = RecipeAgentInfo{
				Type:    a.Type,
				Count:   a.Count,
				Model:   a.Model,
				Persona: a.Persona,
			}
		}

		output.Recipes[i] = RecipeInfo{
			Name:        r.Name,
			Description: r.Description,
			Source:      r.Source,
			TotalAgents: r.TotalAgents(),
			Agents:      agents,
		}
	}

	return output, nil
}

// PrintRecipes outputs available recipes as JSON for AI orchestrators.
// This is a thin wrapper around GetRecipes() for CLI output.
func PrintRecipes() error {
	output, err := GetRecipes()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// TerseState represents the ultra-compact state for token-constrained scenarios.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:
type TerseState struct {
	Session        string `json:"session"`
	ActiveAgents   int    `json:"active_agents"`
	TotalAgents    int    `json:"total_agents"`
	WorkingAgents  int    `json:"working_agents"` // Agents actively processing
	IdleAgents     int    `json:"idle_agents"`    // Agents waiting at prompt
	ErrorAgents    int    `json:"error_agents"`   // Agents in error state
	ContextPct     int    `json:"context_pct"`    // Average context usage %
	ReadyBeads     int    `json:"ready_beads"`    // Beads ready to work on
	BlockedBeads   int    `json:"blocked_beads"`  // Blocked beads
	InProgressBead int    `json:"in_progress_beads"`
	UnreadMail     int    `json:"unread_mail"`
	CriticalAlerts int    `json:"critical_alerts"`
	WarningAlerts  int    `json:"warning_alerts"`
}

// String returns the ultra-compact string representation.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:
func (t TerseState) String() string {
	// Build alerts string (only include if non-zero)
	alertStr := ""
	if t.CriticalAlerts > 0 || t.WarningAlerts > 0 {
		var parts []string
		if t.CriticalAlerts > 0 {
			parts = append(parts, fmt.Sprintf("%dc", t.CriticalAlerts))
		}
		if t.WarningAlerts > 0 {
			parts = append(parts, fmt.Sprintf("%dw", t.WarningAlerts))
		}
		alertStr = strings.Join(parts, ",")
	} else {
		alertStr = "0"
	}

	return fmt.Sprintf("S:%s|A:%d/%d|W:%d|I:%d|E:%d|C:%d%%|B:R%d/I%d/B%d|M:%d|!:%s",
		t.Session,
		t.ActiveAgents, t.TotalAgents,
		t.WorkingAgents, t.IdleAgents, t.ErrorAgents,
		t.ContextPct,
		t.ReadyBeads, t.InProgressBead, t.BlockedBeads,
		t.UnreadMail,
		alertStr)
}

// TerseOutput wraps terse state for robot API output.
type TerseOutput struct {
	RobotResponse
	States     []TerseState `json:"states"`
	TerseLines []string     `json:"terse_lines"` // Pre-formatted terse strings
}

// GetTerse retrieves ultra-compact single-line state for token-constrained scenarios.
func GetTerse(cfg *config.Config) (*TerseOutput, error) {
	output := &TerseOutput{
		RobotResponse: RobotResponse{
			Success:   true,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		States:     []TerseState{},
		TerseLines: []string{},
	}

	// Get alert breakdown (critical vs warning)
	var criticalAlerts, warningAlerts int
	if cfg != nil {
		alertCfg := alerts.ToConfigAlerts(
			cfg.Alerts.Enabled,
			cfg.Alerts.AgentStuckMinutes,
			cfg.Alerts.DiskLowThresholdGB,
			cfg.Alerts.MailBacklogThreshold,
			cfg.Alerts.BeadStaleHours,
			cfg.Alerts.ResolvedPruneMinutes,
			cfg.ProjectsBase,
		)
		activeAlerts := alerts.GetActiveAlerts(alertCfg)
		for _, a := range activeAlerts {
			switch a.Severity {
			case alerts.SeverityCritical:
				criticalAlerts++
			case alerts.SeverityWarning:
				warningAlerts++
			}
		}
	}

	// Get beads summary (same for all sessions in same project)
	var beadsSummary *bv.BeadsSummary
	if bv.IsInstalled() {
		beadsSummary = bv.GetBeadsSummary("", 0)
	}

	// Get mail count (best-effort)
	mailCount := getTerseMailCount()

	// Get all sessions
	sessions, err := tmux.ListSessions()
	if err != nil {
		// No sessions - output minimal state with just beads info
		state := TerseState{
			Session:        "-",
			CriticalAlerts: criticalAlerts,
			WarningAlerts:  warningAlerts,
			UnreadMail:     mailCount,
		}
		if beadsSummary != nil {
			state.ReadyBeads = beadsSummary.Ready
			state.BlockedBeads = beadsSummary.Blocked
			state.InProgressBead = beadsSummary.InProgress
		}

		output.States = append(output.States, state)
		output.TerseLines = append(output.TerseLines, state.String())
		return output, nil
	}

	for _, sess := range sessions {
		state := TerseState{
			Session:        sess.Name,
			CriticalAlerts: criticalAlerts,
			WarningAlerts:  warningAlerts,
			UnreadMail:     mailCount,
		}

		// Get panes for this session
		panes, err := tmux.GetPanes(sess.Name)
		if err == nil {
			state.TotalAgents = len(panes)
			// Count agents by state: working (active), idle, error
			for _, pane := range panes {
				agentType := agentTypeString(pane.Type)
				if agentType != "user" && agentType != "unknown" {
					// Capture output to detect state
					captured, captureErr := tmux.CapturePaneOutput(pane.ID, 20)
					if captureErr == nil {
						_ = splitLines(status.StripANSI(captured)) // Just for consistency, unused here
						paneState := determineState(captured, agentType)
						switch paneState {
						case "active":
							state.WorkingAgents++
							state.ActiveAgents++
						case "idle":
							state.IdleAgents++
							state.ActiveAgents++
						case "error":
							state.ErrorAgents++
							state.ActiveAgents++
						default:
							// Unknown state counts as active
							state.ActiveAgents++
						}
					} else {
						// Assume active/working if we can't capture
						state.WorkingAgents++
						state.ActiveAgents++
					}
				}
			}
		}

		// Add beads summary (same for all sessions in same project)
		if beadsSummary != nil {
			state.ReadyBeads = beadsSummary.Ready
			state.BlockedBeads = beadsSummary.Blocked
			state.InProgressBead = beadsSummary.InProgress
		}

		// Context percentage is not available at session level yet
		// Would require aggregating from individual agent outputs
		state.ContextPct = 0

		output.States = append(output.States, state)
		output.TerseLines = append(output.TerseLines, state.String())
	}

	return output, nil
}

// ParseTerse parses the ultra-compact terse string into a TerseState.
// Format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:
func ParseTerse(s string) (*TerseState, error) {
	state := &TerseState{}

	// Split by pipe
	parts := strings.Split(s, "|")
	for _, part := range parts {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := kv[0], kv[1]

		switch key {
		case "S":
			state.Session = val
		case "A":
			// Parse "active/total" format
			agentParts := strings.Split(val, "/")
			if len(agentParts) == 2 {
				fmt.Sscanf(agentParts[0], "%d", &state.ActiveAgents)
				fmt.Sscanf(agentParts[1], "%d", &state.TotalAgents)
			}
		case "W":
			fmt.Sscanf(val, "%d", &state.WorkingAgents)
		case "I":
			fmt.Sscanf(val, "%d", &state.IdleAgents)
		case "E":
			fmt.Sscanf(val, "%d", &state.ErrorAgents)
		case "C":
			// Parse "78%" format
			fmt.Sscanf(strings.TrimSuffix(val, "%"), "%d", &state.ContextPct)
		case "B":
			// Parse "R3/I2/B1" format
			beadParts := strings.Split(val, "/")
			for _, bp := range beadParts {
				if len(bp) < 2 {
					continue
				}
				prefix := bp[0]
				var count int
				fmt.Sscanf(bp[1:], "%d", &count)
				switch prefix {
				case 'R':
					state.ReadyBeads = count
				case 'I':
					state.InProgressBead = count
				case 'B':
					state.BlockedBeads = count
				}
			}
		case "M":
			fmt.Sscanf(val, "%d", &state.UnreadMail)
		case "!":
			// Parse "1c,2w" or "0" format
			if val == "0" {
				state.CriticalAlerts = 0
				state.WarningAlerts = 0
			} else {
				alertParts := strings.Split(val, ",")
				for _, ap := range alertParts {
					if strings.HasSuffix(ap, "c") {
						fmt.Sscanf(strings.TrimSuffix(ap, "c"), "%d", &state.CriticalAlerts)
					} else if strings.HasSuffix(ap, "w") {
						fmt.Sscanf(strings.TrimSuffix(ap, "w"), "%d", &state.WarningAlerts)
					}
				}
			}
		}
	}

	return state, nil
}

// PrintTerse outputs ultra-compact single-line state for token-constrained scenarios.
// Output format: S:session|A:active/total|W:working|I:idle|E:errors|C:ctx%|B:Rn/In/Bn|M:mail|!:
// Multiple sessions are separated by semicolons.
func PrintTerse(cfg *config.Config) error {
	output, err := GetTerse(cfg)
	if err != nil {
		return err
	}

	// Output all sessions separated by semicolons (preserving original format)
	fmt.Println(strings.Join(output.TerseLines, ";"))
	return nil
}

// getTerseMailCount returns unread mail count for terse output (best-effort).
func getTerseMailCount() int {
	projectKey, err := os.Getwd()
	if err != nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	if !client.IsAvailable() {
		return 0
	}

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		return 0
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		return 0
	}

	// Sum unread across all agents
	total := 0
	for _, a := range agents {
		total += countInbox(ctx, client, projectKey, a.Name, false)
	}

	return total
}

// getAgentMailSummary returns a best-effort Agent Mail summary for --robot-status.
func getAgentMailSummary() (*AgentMailSummary, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	projectKey := cwd
	if root, err := git.FindProjectRoot(cwd); err == nil {
		projectKey = root
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	client := agentmail.NewClient(agentmail.WithProjectKey(projectKey))
	summary := &AgentMailSummary{
		Available: false,
		ServerURL: client.BaseURL(),
	}

	if !client.IsAvailable() {
		return summary, nil
	}
	summary.Available = true

	// Ensure project exists
	if _, err := client.EnsureProject(ctx, projectKey); err != nil {
		summary.Error = fmt.Sprintf("ensure_project: %v", err)
		return summary, nil
	}

	agents, err := client.ListProjectAgents(ctx, projectKey)
	if err != nil {
		summary.Error = fmt.Sprintf("list_agents: %v", err)
		return summary, nil
	}
	summary.SessionsRegistered = len(agents)

	// Aggregate unread/urgent counts
	for _, a := range agents {
		summary.TotalUnread += countInbox(ctx, client, projectKey, a.Name, false)
		summary.UrgentMessages += countInbox(ctx, client, projectKey, a.Name, true)
	}

	// Locks (best-effort)
	if locks, err := client.ListReservations(ctx, projectKey, "", true); err == nil {
		summary.TotalLocks = len(locks)
	}

	return summary, nil
}

// countInbox returns the count of inbox entries for an agent.
// If urgentOnly is true, only urgent messages are counted.
func countInbox(ctx context.Context, client *agentmail.Client, projectKey, agentName string, urgentOnly bool) int {
	limit := 50
	opts := agentmail.FetchInboxOptions{
		ProjectKey:    projectKey,
		AgentName:     agentName,
		UrgentOnly:    urgentOnly,
		Limit:         limit,
		IncludeBodies: false,
	}
	msgs, err := client.FetchInbox(ctx, opts)
	if err != nil {
		return 0
	}
	return len(msgs)
}

// ContextOutput is the structured output for --robot-context
type ContextOutput struct {
	RobotResponse
	Session          string                       `json:"session"`
	CapturedAt       time.Time                    `json:"captured_at"`
	Agents           []AgentContextInfo           `json:"agents"`
	Summary          ContextSummary               `json:"summary"`
	PendingRotations []ContextPendingRotationInfo `json:"pending_rotations,omitempty"`
	AgentHints       *ContextAgentHints           `json:"_agent_hints,omitempty"`
}

// ContextPendingRotationInfo contains information about a pending rotation confirmation
type ContextPendingRotationInfo struct {
	AgentID        string  `json:"agent_id"`
	SessionName    string  `json:"session_name"`
	PaneID         string  `json:"pane_id"`
	ContextPercent float64 `json:"context_percent"`
	CreatedAt      string  `json:"created_at"`
	TimeoutAt      string  `json:"timeout_at"`
	DefaultAction  string  `json:"default_action"`
	WorkDir        string  `json:"work_dir,omitempty"`
}

// AgentContextInfo contains context window information for a single agent pane
type AgentContextInfo struct {
	Pane            string  `json:"pane"`
	PaneIdx         int     `json:"pane_idx"`
	AgentType       string  `json:"agent_type"`
	Model           string  `json:"model"`
	EstimatedTokens int     `json:"estimated_tokens"`
	WithOverhead    int     `json:"with_overhead"`
	ContextLimit    int     `json:"context_limit"`
	UsagePercent    float64 `json:"usage_percent"`
	UsageLevel      string  `json:"usage_level"`
	Confidence      string  `json:"confidence"`
	State           string  `json:"state"`
}

// ContextSummary aggregates context usage across all agents
type ContextSummary struct {
	TotalAgents    int     `json:"total_agents"`
	HighUsageCount int     `json:"high_usage_count"`
	AvgUsage       float64 `json:"avg_usage"`
}

// ContextAgentHints provides agent guidance for context output
type ContextAgentHints struct {
	LowUsageAgents  []string `json:"low_usage_agents,omitempty"`
	HighUsageAgents []string `json:"high_usage_agents,omitempty"`
	Suggestions     []string `json:"suggestions,omitempty"`
}

// getUsageLevel returns a human-readable usage level based on percentage
func getUsageLevel(pct float64) string {
	switch {
	case pct < 40:
		return "Low"
	case pct < 70:
		return "Medium"
	case pct < 85:
		return "High"
	default:
		return "Critical"
	}
}

// getContextLimit returns the context window limit for a model
func getContextLimit(model string) int {
	switch model {
	case "opus", "sonnet":
		return 200000
	case "haiku":
		return 200000
	case "gpt4", "o4-mini":
		return 128000
	case "o1", "o3":
		return 200000
	case "gemini", "pro", "flash":
		return 1000000
	default:
		return 128000 // Conservative default
	}
}

// generateContextHints creates agent hints based on usage patterns
func generateContextHints(lowUsage, highUsage []string, highCount, total int) *ContextAgentHints {
	if total == 0 {
		return nil
	}

	hints := &ContextAgentHints{
		LowUsageAgents:  lowUsage,
		HighUsageAgents: highUsage,
		Suggestions:     make([]string, 0),
	}

	if highCount == 0 {
		// No high usage agents
		if len(lowUsage) == total {
			hints.Suggestions = append(hints.Suggestions, "All agents healthy - context usage is low across the board")
		} else if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have low usage, others are moderate", len(lowUsage)))
		} else {
			hints.Suggestions = append(hints.Suggestions, "All agents at moderate context usage - no immediate concerns")
		}
	} else if highCount == total {
		hints.Suggestions = append(hints.Suggestions, "All agents have high context usage - consider spawning new sessions")
	} else {
		hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have high context usage", highCount))
		if len(lowUsage) > 0 {
			hints.Suggestions = append(hints.Suggestions, fmt.Sprintf("%d agent(s) have room for additional work", len(lowUsage)))
		}
	}

	return hints
}

// GetContext retrieves context window usage information for all agents in a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetContext(session string, lines int) (*ContextOutput, error) {
	if !tmux.SessionExists(session) {
		return &ContextOutput{
			RobotResponse: NewErrorResponse(
				fmt.Errorf("session '%s' not found", session),
				ErrCodeSessionNotFound,
				"Use 'ntm list' to see available sessions",
			),
			Session:    session,
			CapturedAt: time.Now().UTC(),
		}, nil
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return &ContextOutput{
			RobotResponse: NewErrorResponse(err, ErrCodeInternalError, "Failed to get panes"),
			Session:       session,
			CapturedAt:    time.Now().UTC(),
		}, nil
	}

	output := &ContextOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentContextInfo, 0, len(panes)),
	}

	var lowUsage, highUsage []string
	var totalUsage float64

	for _, pane := range panes {
		agentType := detectAgentType(pane.Title)
		if agentType == "unknown" || agentType == "user" {
			continue // Skip non-agent panes
		}

		model := detectModel(agentType, pane.Title)

		scrollback, _ := tmux.CapturePaneOutput(pane.ID, lines)
		cleanText := stripANSI(scrollback)
		lineList := splitLines(cleanText)
		state := detectState(lineList, pane.Title)

		charCount := len(cleanText)
		// Rough token estimate: ~4 chars per token
		estTokens := charCount / 4
		// Add overhead for system prompts and other context (2.5x multiplier)
		withOverhead := int(float64(estTokens) * 2.5)
		contextLimit := getContextLimit(model)
		usagePct := float64(withOverhead) / float64(contextLimit) * 100

		paneKey := fmt.Sprintf("%d", pane.Index)
		usageLevel := getUsageLevel(usagePct)

		// Align thresholds with getUsageLevel: <40% is Low, >=70% is High/Critical
		if usagePct < 40 {
			lowUsage = append(lowUsage, paneKey)
		} else if usagePct >= 70 {
			highUsage = append(highUsage, paneKey)
		}
		totalUsage += usagePct

		agentInfo := AgentContextInfo{
			Pane:            paneKey,
			PaneIdx:         pane.Index,
			AgentType:       agentType,
			Model:           model,
			EstimatedTokens: estTokens,
			WithOverhead:    withOverhead,
			ContextLimit:    contextLimit,
			UsagePercent:    usagePct,
			UsageLevel:      usageLevel,
			Confidence:      "low", // Scrollback-based estimation is low confidence
			State:           state,
		}
		output.Agents = append(output.Agents, agentInfo)
	}

	output.Summary.TotalAgents = len(output.Agents)
	output.Summary.HighUsageCount = len(highUsage)
	if len(output.Agents) > 0 {
		output.Summary.AvgUsage = totalUsage / float64(len(output.Agents))
	}

	// Add pending rotations for this session
	pendingRotations, _ := ntmctx.GetPendingRotationsForSession(session)
	for _, p := range pendingRotations {
		output.PendingRotations = append(output.PendingRotations, ContextPendingRotationInfo{
			AgentID:        p.AgentID,
			SessionName:    p.SessionName,
			PaneID:         p.PaneID,
			ContextPercent: p.ContextPercent,
			CreatedAt:      p.CreatedAt.Format(time.RFC3339),
			TimeoutAt:      p.TimeoutAt.Format(time.RFC3339),
			DefaultAction:  string(p.DefaultAction),
			WorkDir:        p.WorkDir,
		})
	}

	output.AgentHints = generateContextHints(lowUsage, highUsage, len(highUsage), len(output.Agents))

	return output, nil
}

// PrintContext outputs context window usage information for all agents in a session.
func PrintContext(session string, lines int) error {
	output, err := GetContext(session, lines)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// =============================================================================
// Activity Detection API
// =============================================================================

// ActivityOptions holds options for the activity API.
type ActivityOptions struct {
	Session    string   // Required: session name
	Panes      []string // Optional: filter to specific pane indices
	AgentTypes []string // Optional: filter to specific agent types (claude, codex, gemini)
}

// ActivityOutput represents the output for --robot-activity
type ActivityOutput struct {
	RobotResponse
	Session    string              `json:"session"`
	CapturedAt time.Time           `json:"captured_at"`
	Agents     []AgentActivityInfo `json:"agents"`
	Summary    ActivitySummary     `json:"summary"`
	AgentHints *ActivityAgentHints `json:"_agent_hints,omitempty"`
}

// AgentActivityInfo contains activity state for a single agent pane.
type AgentActivityInfo struct {
	Pane             string   `json:"pane"`                        // pane index as string
	PaneIdx          int      `json:"pane_idx"`                    // pane index as int
	AgentType        string   `json:"agent_type"`                  // claude, codex, gemini
	State            string   `json:"state"`                       // GENERATING, WAITING, THINKING, ERROR, STALLED, UNKNOWN
	Confidence       float64  `json:"confidence"`                  // 0.0-1.0
	Velocity         float64  `json:"velocity"`                    // chars/sec
	StateSince       string   `json:"state_since,omitempty"`       // RFC3339 timestamp
	DetectedPatterns []string `json:"detected_patterns,omitempty"` // pattern names that matched
	LastOutput       string   `json:"last_output,omitempty"`       // RFC3339 timestamp of last output
}

// ActivitySummary provides aggregate state counts.
type ActivitySummary struct {
	TotalAgents int            `json:"total_agents"`
	ByState     map[string]int `json:"by_state"` // state -> count
}

// ActivityAgentHints provides actionable hints for AI agents.
type ActivityAgentHints struct {
	Summary          string   `json:"summary"`
	AvailableAgents  []string `json:"available_agents,omitempty"` // panes in WAITING state
	BusyAgents       []string `json:"busy_agents,omitempty"`      // panes in GENERATING/THINKING state
	ProblemAgents    []string `json:"problem_agents,omitempty"`   // panes in ERROR/STALLED state
	SuggestedActions []string `json:"suggested_actions,omitempty"`
}

// GetActivity returns agent activity state for a session.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetActivity(opts ActivityOptions) (*ActivityOutput, error) {
	output := &ActivityOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		CapturedAt:    time.Now().UTC(),
		Agents:        make([]AgentActivityInfo, 0),
		Summary: ActivitySummary{
			ByState: make(map[string]int),
		},
	}

	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
		return output, nil
	}

	output.Agents = make([]AgentActivityInfo, 0, len(panes))

	// Build filter maps
	paneFilterMap := make(map[string]bool)
	for _, p := range opts.Panes {
		paneFilterMap[p] = true
	}
	hasPaneFilter := len(paneFilterMap) > 0

	typeFilterMap := make(map[string]bool)
	for _, t := range opts.AgentTypes {
		typeFilterMap[normalizeAgentType(t)] = true
	}
	hasTypeFilter := len(typeFilterMap) > 0

	// Collect activity data
	var availableAgents, busyAgents, problemAgents []string

	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		// Apply pane filter
		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		agentType := detectAgentType(pane.Title)

		// Skip non-agent panes (user, unknown)
		if agentType == "unknown" || agentType == "user" {
			continue
		}

		// Apply type filter
		if hasTypeFilter && !typeFilterMap[agentType] {
			continue
		}

		// Create classifier for this pane
		classifier := NewStateClassifier(pane.ID, &ClassifierConfig{
			AgentType: agentType,
		})

		// Classify current state
		activity, err := classifier.Classify()
		if err != nil {
			// Include with unknown state on error
			output.Agents = append(output.Agents, AgentActivityInfo{
				Pane:       paneKey,
				PaneIdx:    pane.Index,
				AgentType:  agentType,
				State:      string(StateUnknown),
				Confidence: 0.0,
			})
			output.Summary.ByState[string(StateUnknown)]++
			continue
		}

		// Build agent info
		info := AgentActivityInfo{
			Pane:             paneKey,
			PaneIdx:          pane.Index,
			AgentType:        activity.AgentType,
			State:            string(activity.State),
			Confidence:       activity.Confidence,
			Velocity:         activity.Velocity,
			DetectedPatterns: activity.DetectedPatterns,
		}

		if !activity.StateSince.IsZero() {
			info.StateSince = FormatTimestamp(activity.StateSince)
		}
		if !activity.LastOutput.IsZero() {
			info.LastOutput = FormatTimestamp(activity.LastOutput)
		}

		output.Agents = append(output.Agents, info)

		// Update summary
		stateStr := string(activity.State)
		output.Summary.ByState[stateStr]++

		// Categorize for hints
		switch activity.State {
		case StateWaiting:
			availableAgents = append(availableAgents, paneKey)
		case StateGenerating, StateThinking:
			busyAgents = append(busyAgents, paneKey)
		case StateError, StateStalled:
			problemAgents = append(problemAgents, paneKey)
		}
	}

	output.Summary.TotalAgents = len(output.Agents)

	// Generate agent hints
	output.AgentHints = generateActivityHints(availableAgents, busyAgents, problemAgents, output.Summary)

	return output, nil
}

// PrintActivity handles the --robot-activity command.
// This is a thin wrapper around GetActivity() for CLI output.
func PrintActivity(opts ActivityOptions) error {
	output, err := GetActivity(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// generateActivityHints creates actionable hints based on agent states.
func generateActivityHints(available, busy, problem []string, summary ActivitySummary) *ActivityAgentHints {
	hints := &ActivityAgentHints{
		AvailableAgents: available,
		BusyAgents:      busy,
		ProblemAgents:   problem,
	}

	// Build summary
	total := summary.TotalAgents
	availCount := len(available)
	busyCount := len(busy)
	problemCount := len(problem)

	if total == 0 {
		hints.Summary = "No agents found in session"
		hints.SuggestedActions = []string{"Use --robot-spawn to create agents"}
		return hints
	}

	hints.Summary = fmt.Sprintf("%d agents: %d available, %d busy, %d problems",
		total, availCount, busyCount, problemCount)

	// Generate suggestions
	if problemCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("Check error/stalled agents in panes: %s", strings.Join(problem, ", ")))
	}

	if availCount > 0 && busyCount == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			"All agents idle - ready for new prompts")
	}

	if availCount == 0 && busyCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			"All agents busy - wait or use --robot-ack to monitor completion")
	}

	if availCount > 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("Send work to available panes: %s", strings.Join(available, ", ")))
	}

	return hints
}

// normalizeAgentType normalizes agent type aliases.
func normalizeAgentType(t string) string {
	switch strings.ToLower(t) {
	case "cc", "claude-code", "claude":
		return "claude"
	case "cod", "codex-cli", "codex":
		return "codex"
	case "gmi", "gemini-cli", "gemini":
		return "gemini"
	default:
		return strings.ToLower(t)
	}
}

// ============================================================================
// --robot-diff: Compare agent activity and file changes
// ============================================================================

// DiffOptions holds options for the --robot-diff command.
type DiffOptions struct {
	Session string        // Required: session name
	Since   time.Duration // Duration to look back (default: 15m)
}

// DiffOutput is the structured output for --robot-diff.
type DiffOutput struct {
	RobotResponse
	Session       string          `json:"session"`
	Timeframe     DiffTimeframe   `json:"timeframe"`
	Files         DiffFiles       `json:"files"`
	AgentActivity []DiffAgentInfo `json:"agent_activity"`
	AgentHints    *DiffAgentHints `json:"_agent_hints,omitempty"`
}

// DiffTimeframe describes the analysis time window.
type DiffTimeframe struct {
	Since      string `json:"since"`       // Duration string (e.g., "15m")
	SinceTS    string `json:"since_ts"`    // RFC3339 timestamp
	CapturedAt string `json:"captured_at"` // RFC3339 timestamp
}

// DiffFiles categorizes files by modification status.
type DiffFiles struct {
	Modified           []string       `json:"modified"`
	PotentialConflicts []DiffConflict `json:"potential_conflicts"`
	Clean              []string       `json:"clean"`
}

// DiffConflict represents a potential file conflict.
type DiffConflict struct {
	File            string   `json:"file"`
	LikelyModifiers []string `json:"likely_modifiers"`
	Reason          string   `json:"reason"`
	Confidence      float64  `json:"confidence"`
}

// DiffAgentInfo provides activity info for a single agent pane.
type DiffAgentInfo struct {
	Pane        string `json:"pane"`
	AgentType   string `json:"agent_type"`
	State       string `json:"state"`
	OutputLines int    `json:"output_lines"`
	ActiveTime  string `json:"active_time,omitempty"`
}

// DiffAgentHints provides actionable hints for AI agents.
type DiffAgentHints struct {
	Summary          string   `json:"summary"`
	ConflictWarnings []string `json:"conflict_warnings,omitempty"`
	SuggestedActions []string `json:"suggested_actions,omitempty"`
}

// GetDiff returns agent activity comparison and file change analysis.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetDiff(opts DiffOptions) (*DiffOutput, error) {
	// Default to 15 minutes if not specified
	if opts.Since == 0 {
		opts.Since = 15 * time.Minute
	}

	now := time.Now().UTC()
	sinceTime := now.Add(-opts.Since)

	output := &DiffOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Timeframe: DiffTimeframe{
			Since:      opts.Since.String(),
			SinceTS:    sinceTime.Format(time.RFC3339),
			CapturedAt: now.Format(time.RFC3339),
		},
		Files: DiffFiles{
			Modified:           []string{},
			PotentialConflicts: []DiffConflict{},
			Clean:              []string{},
		},
		AgentActivity: []DiffAgentInfo{},
	}

	// Validate session exists
	if !tmux.SessionExists(opts.Session) {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session '%s' not found", opts.Session),
			ErrCodeSessionNotFound,
			"Use 'ntm list' to see available sessions",
		)
		return output, nil
	}

	// Get panes for agent activity
	panes, err := tmux.GetPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
		return output, nil
	}

	// Create conflict detector for file analysis
	wd, err := os.Getwd()
	if err != nil {
		// Fall back to empty path - conflict detection will be limited
		wd = ""
	}
	detector := NewConflictDetector(&ConflictDetectorConfig{
		RepoPath: wd,
	})

	// Analyze activity windows per pane
	for _, pane := range panes {
		agentType := string(pane.Type)
		if agentType == "" || agentType == "unknown" {
			agentType = "user"
		}

		// Capture pane output for state detection
		captured, _ := tmux.CapturePaneOutput(pane.ID, 100)
		lines := strings.Split(captured, "\n")

		// Use the proper detectState function for accurate state detection
		state := detectState(lines, pane.Title)
		if state == "" {
			state = "idle"
		}

		info := DiffAgentInfo{
			Pane:        pane.Title,
			AgentType:   agentType,
			State:       state,
			OutputLines: len(lines),
		}
		output.AgentActivity = append(output.AgentActivity, info)

		// Record activity window for conflict detection
		detector.RecordActivity(pane.ID, agentType, sinceTime, now, len(lines) > 0)
	}

	// Track issues for hints
	var analysisIssues []string

	// Get modified files from git
	gitStatus, gitErr := detector.GetGitStatus()
	if gitErr == nil {
		for _, fs := range gitStatus {
			output.Files.Modified = append(output.Files.Modified, fs.Path)
		}
	} else if wd != "" {
		// Only note git issues if we had a valid working directory
		analysisIssues = append(analysisIssues, "Could not get git status")
	}

	// Detect potential conflicts
	ctx := context.Background()
	conflicts, conflictErr := detector.DetectConflicts(ctx)
	if conflictErr != nil && wd != "" {
		analysisIssues = append(analysisIssues, "Conflict detection incomplete")
	}
	for _, c := range conflicts {
		output.Files.PotentialConflicts = append(output.Files.PotentialConflicts, DiffConflict{
			File:            c.Path,
			LikelyModifiers: c.LikelyModifiers,
			Reason:          string(c.Reason),
			Confidence:      c.Confidence,
		})
	}

	// Generate hints
	hints := &DiffAgentHints{
		Summary: fmt.Sprintf("%s activity: %d files modified, %d agents",
			opts.Session, len(output.Files.Modified), len(panes)),
	}

	// Add analysis issues as warnings
	if len(analysisIssues) > 0 {
		hints.ConflictWarnings = append(hints.ConflictWarnings, analysisIssues...)
	}

	if len(output.Files.PotentialConflicts) > 0 {
		hints.ConflictWarnings = append(hints.ConflictWarnings,
			fmt.Sprintf("%d potential conflict(s) detected", len(output.Files.PotentialConflicts)))
		hints.SuggestedActions = append(hints.SuggestedActions,
			"Review conflicts before committing")
	}

	if len(output.Files.Modified) == 0 {
		hints.SuggestedActions = append(hints.SuggestedActions,
			fmt.Sprintf("No file changes in the last %s", opts.Since.String()))
	}

	output.AgentHints = hints

	return output, nil
}

// PrintDiff handles the --robot-diff command.
// This is a thin wrapper around GetDiff() for CLI output.
func PrintDiff(opts DiffOptions) error {
	output, err := GetDiff(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// TriageOptions configures the triage output
type TriageOptions struct {
	Limit int // Max recommendations per category (default 10)
}

// TriageOutput is the robot-triage JSON output structure
type TriageOutput struct {
	RobotResponse
	GeneratedAt     time.Time                 `json:"generated_at"`
	Available       bool                      `json:"available"`
	DataHash        string                    `json:"data_hash,omitempty"`
	Error           string                    `json:"error,omitempty"`
	QuickRef        *bv.TriageQuickRef        `json:"quick_ref,omitempty"`
	Recommendations []bv.TriageRecommendation `json:"recommendations,omitempty"`
	QuickWins       []bv.TriageRecommendation `json:"quick_wins,omitempty"`
	BlockersToClear []bv.BlockerToClear       `json:"blockers_to_clear,omitempty"`
	ProjectHealth   *bv.ProjectHealth         `json:"project_health,omitempty"`
	Commands        map[string]string         `json:"commands,omitempty"`
	CacheInfo       *TriageCacheInfo          `json:"cache_info,omitempty"`
}

// TriageCacheInfo provides cache metadata
type TriageCacheInfo struct {
	Cached bool  `json:"cached"`
	AgeMs  int64 `json:"age_ms,omitempty"`
	TTLMs  int64 `json:"ttl_ms"`
}

// GetTriage returns bv triage analysis data.
func GetTriage(opts TriageOptions) (*TriageOutput, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	output := &TriageOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Available:     bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeDependencyMissing,
			"Install bv to enable triage",
		)
		return output, nil
	}

	wd := mustGetwd()

	// Get triage data (uses internal cache)
	triage, err := bv.GetTriage(wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get triage: %v", err)
		output.RobotResponse = NewErrorResponse(
			err,
			ErrCodeInternalError,
			"Check bv triage cache and repository state",
		)
		return output, nil
	}

	if triage == nil {
		output.Error = "no triage data returned"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeInternalError,
			"Rebuild bv cache or retry triage",
		)
		return output, nil
	}

	// Copy data with limits applied
	output.DataHash = triage.DataHash
	output.QuickRef = &triage.Triage.QuickRef
	output.QuickWins = triage.Triage.QuickWins
	output.BlockersToClear = triage.Triage.BlockersToClear
	output.ProjectHealth = triage.Triage.ProjectHealth
	output.Commands = triage.Triage.Commands

	// Apply limits to recommendations
	if len(triage.Triage.Recommendations) > opts.Limit {
		output.Recommendations = triage.Triage.Recommendations[:opts.Limit]
	} else {
		output.Recommendations = triage.Triage.Recommendations
	}

	// Add cache info
	output.CacheInfo = &TriageCacheInfo{
		Cached: bv.IsCacheValid(),
		AgeMs:  bv.GetCacheAge().Milliseconds(),
		TTLMs:  bv.TriageCacheTTL.Milliseconds(),
	}

	return output, nil
}

// PrintTriage outputs bv triage analysis for AI consumption
func PrintTriage(opts TriageOptions) error {
	output, err := GetTriage(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// Additional BV robot modes for comprehensive analysis

// LabelAttentionOptions configures label attention analysis
type LabelAttentionOptions struct {
	Limit int
}

// FileBeadsOptions configures file-to-beads analysis
type FileBeadsOptions struct {
	FilePath string
	Limit    int
}

// FileHotspotsOptions configures file hotspots analysis
type FileHotspotsOptions struct {
	Limit int
}

// FileRelationsOptions configures file relations analysis
type FileRelationsOptions struct {
	FilePath  string
	Limit     int
	Threshold float64
}

// ForecastOutput is the JSON output for --robot-forecast
type ForecastOutput struct {
	RobotResponse
	Target    string               `json:"target"`
	Available bool                 `json:"available"`
	Forecast  *bv.ForecastResponse `json:"forecast,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// GetForecast returns BV forecast analysis data.
func GetForecast(target string) (*ForecastOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &ForecastOutput{
		RobotResponse: NewRobotResponse(true),
		Target:        target,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetForecast(context.Background(), wd, target)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get forecast: %v", err)
		output.Success = false
		return output, nil
	}

	var forecast bv.ForecastResponse
	if err := json.Unmarshal(raw, &forecast); err != nil {
		output.Error = fmt.Sprintf("failed to parse forecast: %v", err)
		output.Success = false
		return output, nil
	}
	output.Forecast = &forecast

	return output, nil
}

// PrintForecast outputs BV forecast analysis for ETA predictions
func PrintForecast(target string) error {
	output, err := GetForecast(target)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// SuggestOutput is the JSON output for --robot-suggest
type SuggestOutput struct {
	RobotResponse
	Available   bool                    `json:"available"`
	Suggestions *bv.SuggestionsResponse `json:"suggestions,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

// GetSuggest returns BV hygiene suggestions data.
func GetSuggest() (*SuggestOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &SuggestOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetSuggestions(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get suggestions: %v", err)
		output.Success = false
		return output, nil
	}

	var suggestions bv.SuggestionsResponse
	if err := json.Unmarshal(raw, &suggestions); err != nil {
		output.Error = fmt.Sprintf("failed to parse suggestions: %v", err)
		output.Success = false
		return output, nil
	}
	output.Suggestions = &suggestions

	return output, nil
}

// PrintSuggest outputs BV hygiene suggestions for code quality improvements
func PrintSuggest() error {
	output, err := GetSuggest()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// ImpactOutput is the JSON output for --robot-impact
type ImpactOutput struct {
	RobotResponse
	FilePath  string             `json:"file_path"`
	Available bool               `json:"available"`
	Impact    *bv.ImpactResponse `json:"impact,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// GetImpact returns BV impact analysis data.
func GetImpact(filePath string) (*ImpactOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &ImpactOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      filePath,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetImpact(context.Background(), wd, filePath)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get impact analysis: %v", err)
		output.Success = false
		return output, nil
	}

	var impact bv.ImpactResponse
	if err := json.Unmarshal(raw, &impact); err != nil {
		output.Error = fmt.Sprintf("failed to parse impact analysis: %v", err)
		output.Success = false
		return output, nil
	}
	output.Impact = &impact

	return output, nil
}

// PrintImpact outputs BV impact analysis for file changes
func PrintImpact(filePath string) error {
	output, err := GetImpact(filePath)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// SearchOutput is the JSON output for --robot-search
type SearchOutput struct {
	RobotResponse
	Query     string             `json:"query"`
	Available bool               `json:"available"`
	Results   *bv.SearchResponse `json:"results,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// GetSearch returns BV semantic vector search results.
func GetSearch(query string) (*SearchOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &SearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         query,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetSearch(context.Background(), wd, query)
	if err != nil {
		output.Error = fmt.Sprintf("failed to perform search: %v", err)
		output.Success = false
		return output, nil
	}

	var results bv.SearchResponse
	if err := json.Unmarshal(raw, &results); err != nil {
		output.Error = fmt.Sprintf("failed to parse search results: %v", err)
		output.Success = false
		return output, nil
	}
	output.Results = &results

	return output, nil
}

// PrintSearch outputs BV semantic vector search results
func PrintSearch(query string) error {
	output, err := GetSearch(query)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// LabelAttentionOutput is the JSON output for --robot-label-attention
type LabelAttentionOutput struct {
	RobotResponse
	Available bool                       `json:"available"`
	Labels    *bv.LabelAttentionResponse `json:"labels,omitempty"`
	Limit     int                        `json:"limit"`
	Error     string                     `json:"error,omitempty"`
}

// GetLabelAttention returns BV label attention ranking data.
func GetLabelAttention(opts LabelAttentionOptions) (*LabelAttentionOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelAttentionOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelAttention(context.Background(), wd, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label attention: %v", err)
		output.Success = false
		return output, nil
	}

	var labels bv.LabelAttentionResponse
	if err := json.Unmarshal(raw, &labels); err != nil {
		output.Error = fmt.Sprintf("failed to parse label attention: %v", err)
		output.Success = false
		return output, nil
	}
	output.Labels = &labels

	return output, nil
}

// PrintLabelAttention outputs BV label attention ranking
func PrintLabelAttention(opts LabelAttentionOptions) error {
	output, err := GetLabelAttention(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// LabelFlowOutput is the JSON output for --robot-label-flow
type LabelFlowOutput struct {
	RobotResponse
	Available bool                  `json:"available"`
	Flow      *bv.LabelFlowResponse `json:"flow,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// GetLabelFlow returns BV cross-label dependency flow data.
func GetLabelFlow() (*LabelFlowOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelFlowOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelFlow(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label flow: %v", err)
		output.Success = false
		return output, nil
	}

	var flow bv.LabelFlowResponse
	if err := json.Unmarshal(raw, &flow); err != nil {
		output.Error = fmt.Sprintf("failed to parse label flow: %v", err)
		output.Success = false
		return output, nil
	}
	output.Flow = &flow

	return output, nil
}

// PrintLabelFlow outputs BV cross-label dependency flow analysis
func PrintLabelFlow() error {
	output, err := GetLabelFlow()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// LabelHealthOutput is the JSON output for --robot-label-health
type LabelHealthOutput struct {
	RobotResponse
	Available bool                    `json:"available"`
	Health    *bv.LabelHealthResponse `json:"health,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// GetLabelHealth returns BV per-label health data.
func GetLabelHealth() (*LabelHealthOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelHealth(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label health: %v", err)
		output.Success = false
		return output, nil
	}

	var health bv.LabelHealthResponse
	if err := json.Unmarshal(raw, &health); err != nil {
		output.Error = fmt.Sprintf("failed to parse label health: %v", err)
		output.Success = false
		return output, nil
	}
	output.Health = &health

	return output, nil
}

// PrintLabelHealth outputs BV per-label health analysis
func PrintLabelHealth() error {
	output, err := GetLabelHealth()
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// FileBeadsOutput is the JSON output for --robot-file-beads
type FileBeadsOutput struct {
	RobotResponse
	FilePath  string                `json:"file_path"`
	Available bool                  `json:"available"`
	Beads     *bv.FileBeadsResponse `json:"beads,omitempty"`
	Limit     int                   `json:"limit"`
	Error     string                `json:"error,omitempty"`
}

// GetFileBeads returns BV file-to-beads mapping data.
func GetFileBeads(opts FileBeadsOptions) (*FileBeadsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileBeadsOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      opts.FilePath,
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileBeads(context.Background(), wd, opts.FilePath, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file beads: %v", err)
		output.Success = false
		return output, nil
	}

	var beads bv.FileBeadsResponse
	if err := json.Unmarshal(raw, &beads); err != nil {
		output.Error = fmt.Sprintf("failed to parse file beads: %v", err)
		output.Success = false
		return output, nil
	}
	output.Beads = &beads

	return output, nil
}

// PrintFileBeads outputs BV file-to-beads mapping
func PrintFileBeads(opts FileBeadsOptions) error {
	output, err := GetFileBeads(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// FileHotspotsOutput is the JSON output for --robot-file-hotspots
type FileHotspotsOutput struct {
	RobotResponse
	Available bool                     `json:"available"`
	Hotspots  *bv.FileHotspotsResponse `json:"hotspots,omitempty"`
	Limit     int                      `json:"limit"`
	Error     string                   `json:"error,omitempty"`
}

// GetFileHotspots returns BV file quality hotspots data.
func GetFileHotspots(opts FileHotspotsOptions) (*FileHotspotsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileHotspotsOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileHotspots(context.Background(), wd, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file hotspots: %v", err)
		output.Success = false
		return output, nil
	}

	var hotspots bv.FileHotspotsResponse
	if err := json.Unmarshal(raw, &hotspots); err != nil {
		output.Error = fmt.Sprintf("failed to parse file hotspots: %v", err)
		output.Success = false
		return output, nil
	}
	output.Hotspots = &hotspots

	return output, nil
}

// PrintFileHotspots outputs BV file quality hotspots
func PrintFileHotspots(opts FileHotspotsOptions) error {
	output, err := GetFileHotspots(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// FileRelationsOutput is the JSON output for --robot-file-relations
type FileRelationsOutput struct {
	RobotResponse
	FilePath  string                    `json:"file_path"`
	Available bool                      `json:"available"`
	Relations *bv.FileRelationsResponse `json:"relations,omitempty"`
	Limit     int                       `json:"limit"`
	Threshold float64                   `json:"threshold"`
	Error     string                    `json:"error,omitempty"`
}

// GetFileRelations returns BV file co-change relations data.
func GetFileRelations(opts FileRelationsOptions) (*FileRelationsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileRelationsOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      opts.FilePath,
		Available:     installed,
		Limit:         opts.Limit,
		Threshold:     opts.Threshold,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		output.Success = false
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileRelations(context.Background(), wd, opts.FilePath, opts.Limit, opts.Threshold)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file relations: %v", err)
		output.Success = false
		return output, nil
	}

	var relations bv.FileRelationsResponse
	if err := json.Unmarshal(raw, &relations); err != nil {
		output.Error = fmt.Sprintf("failed to parse file relations: %v", err)
		output.Success = false
		return output, nil
	}
	output.Relations = &relations

	return output, nil
}

// PrintFileRelations outputs BV file co-change relations
func PrintFileRelations(opts FileRelationsOptions) error {
	output, err := GetFileRelations(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}
