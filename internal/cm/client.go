package cm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ErrNotInstalled is returned when the cm binary is not found
var ErrNotInstalled = fmt.Errorf("cm is not installed")

type Client struct {
	baseURL string
	client  *http.Client
}

// PIDFileInfo matches supervisor.PIDFileInfo
type PIDFileInfo struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	OwnerID   string    `json:"owner_id"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
}

// NewClient creates a new CM client by discovering the daemon port from the PID file.
func NewClient(projectDir, sessionID string) (*Client, error) {
	// Read PID file to find port
	pidPath := filepath.Join(projectDir, ".ntm", "pids", fmt.Sprintf("cm-%s.pid", sessionID))
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return nil, fmt.Errorf("reading cm pid file: %w", err)
	}

	var info PIDFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing cm pid file: %w", err)
	}

	return &Client{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", info.Port),
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type ContextResult struct {
	RelevantBullets  []Rule    `json:"relevantBullets"`
	AntiPatterns     []Rule    `json:"antiPatterns"`
	HistorySnippets  []Snippet `json:"historySnippets"`
	SuggestedQueries []string  `json:"suggestedCassQueries"`
}

type Rule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category"`
}

type Snippet struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// GetContext queries CM for task-relevant rules via HTTP.
func (c *Client) GetContext(ctx context.Context, task string) (*ContextResult, error) {
	reqBody := map[string]string{"task": task}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/context", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cm context failed: %s", resp.Status)
	}

	var result ContextResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

type OutcomeStatus string

const (
	OutcomeSuccess OutcomeStatus = "success"
	OutcomeFailure OutcomeStatus = "failure"
	OutcomePartial OutcomeStatus = "partial"
)

type OutcomeReport struct {
	Status    OutcomeStatus `json:"status"`
	RuleIDs   []string      `json:"rule_ids"`
	Sentiment string        `json:"sentiment"`
	Notes     string        `json:"notes,omitempty"`
}

// RecordOutcome sends feedback about rule effectiveness.
func (c *Client) RecordOutcome(ctx context.Context, report OutcomeReport) error {
	data, _ := json.Marshal(report)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/outcome", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("cm outcome failed: %s", resp.Status)
	}
	return nil
}

// CLIClient interacts with the CM CLI directly via exec.Command.
// This is used for recovery context where we may not have a daemon running.
type CLIClient struct {
	binaryPath string
	timeout    time.Duration
}

// CLIContextResponse matches the JSON output of `cm context --json`
type CLIContextResponse struct {
	Success         bool               `json:"success"`
	Task            string             `json:"task"`
	RelevantBullets []CLIRule          `json:"relevantBullets"`
	AntiPatterns    []CLIRule          `json:"antiPatterns"`
	HistorySnippets []CLIHistorySnip   `json:"historySnippets"`
	SuggestedQueries []string          `json:"suggestedCassQueries"`
}

// CLIRule represents a rule from CM playbook
type CLIRule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

// CLIHistorySnip represents a historical snippet from CM
type CLIHistorySnip struct {
	SourcePath string  `json:"source_path"`
	LineNumber int     `json:"line_number"`
	Agent      string  `json:"agent"`
	Workspace  string  `json:"workspace"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	CreatedAt  int64   `json:"created_at"`
}

// CLIClientOption configures the CLI client
type CLIClientOption func(*CLIClient)

// WithCLIBinaryPath sets the path to the cm binary
func WithCLIBinaryPath(path string) CLIClientOption {
	return func(c *CLIClient) {
		if path != "" {
			c.binaryPath = path
		}
	}
}

// WithCLITimeout sets the command timeout
func WithCLITimeout(d time.Duration) CLIClientOption {
	return func(c *CLIClient) {
		c.timeout = d
	}
}

// NewCLIClient creates a new CM CLI client
func NewCLIClient(opts ...CLIClientOption) *CLIClient {
	c := &CLIClient{
		binaryPath: "cm",
		timeout:    30 * time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// IsInstalled checks if the cm binary is available
func (c *CLIClient) IsInstalled() bool {
	path, err := exec.LookPath(c.binaryPath)
	return err == nil && path != ""
}

// GetContext queries CM for task-relevant rules and history via CLI.
// It executes: cm context '<task>' --json
// Returns nil with no error if CM is not installed (graceful degradation).
func (c *CLIClient) GetContext(ctx context.Context, task string) (*CLIContextResponse, error) {
	if !c.IsInstalled() {
		return nil, nil // Graceful degradation: CM not available
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.binaryPath, "context", task, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check if context was cancelled
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("cm context timed out after %v", c.timeout)
		}
		// Non-zero exit but may still have valid JSON (e.g., empty results)
		// Try to parse stdout anyway
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("cm context failed: %w (stderr: %s)", err, stderr.String())
		}
	}

	var result CLIContextResponse
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("parsing cm output: %w (raw: %s)", err, stdout.String())
	}

	return &result, nil
}

// GetRecoveryContext is a convenience method that formats the task for recovery use.
// It queries CM with a recovery-focused task description and limits results.
func (c *CLIClient) GetRecoveryContext(ctx context.Context, projectName string, maxRules, maxSnippets int) (*CLIContextResponse, error) {
	task := fmt.Sprintf("%s: starting new coding session", projectName)
	result, err := c.GetContext(ctx, task)
	if err != nil || result == nil {
		return result, err
	}

	// Apply limits to avoid context bloat
	if maxRules > 0 && len(result.RelevantBullets) > maxRules {
		result.RelevantBullets = result.RelevantBullets[:maxRules]
	}
	if maxRules > 0 && len(result.AntiPatterns) > maxRules {
		result.AntiPatterns = result.AntiPatterns[:maxRules]
	}
	if maxSnippets > 0 && len(result.HistorySnippets) > maxSnippets {
		result.HistorySnippets = result.HistorySnippets[:maxSnippets]
	}

	return result, nil
}

// FormatForRecovery formats the CM context result as a markdown string for agent injection.
func (c *CLIClient) FormatForRecovery(result *CLIContextResponse) string {
	if result == nil {
		return ""
	}

	var buf bytes.Buffer

	if len(result.RelevantBullets) > 0 {
		buf.WriteString("## Procedural Memory (Key Rules)\n\n")
		for _, rule := range result.RelevantBullets {
			buf.WriteString(fmt.Sprintf("- **[%s]** %s\n", rule.ID, rule.Content))
		}
		buf.WriteString("\n")
	}

	if len(result.AntiPatterns) > 0 {
		buf.WriteString("## Anti-Patterns to Avoid\n\n")
		for _, pattern := range result.AntiPatterns {
			buf.WriteString(fmt.Sprintf("- ⚠️ **[%s]** %s\n", pattern.ID, pattern.Content))
		}
		buf.WriteString("\n")
	}

	if len(result.HistorySnippets) > 0 {
		buf.WriteString("## Relevant Past Work\n\n")
		for _, snippet := range result.HistorySnippets {
			buf.WriteString(fmt.Sprintf("- **%s** (%s)\n  %s\n", snippet.Title, snippet.Agent, truncate(snippet.Snippet, 200)))
		}
		buf.WriteString("\n")
	}

	return buf.String()
}

// truncate shortens a string to maxLen, adding ellipsis if needed
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
