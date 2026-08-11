package cm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotInstalled is returned when the cm binary is not found
var ErrNotInstalled = fmt.Errorf("cm is not installed")

type Client struct {
	baseURL   string
	sessionID string
	client    *http.Client
}

type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *mcpRPCError    `json:"error"`
}

type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
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
		baseURL:   fmt.Sprintf("http://127.0.0.1:%d", info.Port),
		sessionID: sessionID,
		client:    &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *Client) rpc(ctx context.Context, method string, params any, result any) error {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling cm MCP request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cm MCP %s failed: %s", method, resp.Status)
	}

	var rpcResponse mcpRPCResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&rpcResponse); err != nil {
		return fmt.Errorf("decoding cm MCP %s response: %w", method, err)
	}
	if rpcResponse.Error != nil {
		return fmt.Errorf("cm MCP %s failed (%d): %s", method, rpcResponse.Error.Code, rpcResponse.Error.Message)
	}
	if len(rpcResponse.Result) == 0 || string(rpcResponse.Result) == "null" {
		return fmt.Errorf("cm MCP %s response contained no result", method)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(rpcResponse.Result, result); err != nil {
		return fmt.Errorf("decoding cm MCP %s result: %w", method, err)
	}
	return nil
}

func (c *Client) callTool(ctx context.Context, name string, arguments any, result any) error {
	var toolResult mcpToolResult
	if err := c.rpc(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	}, &toolResult); err != nil {
		return err
	}

	var textResult string
	for _, content := range toolResult.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			textResult = content.Text
			break
		}
	}
	if toolResult.IsError {
		if textResult != "" {
			return fmt.Errorf("cm tool %s failed: %s", name, textResult)
		}
		return fmt.Errorf("cm tool %s failed", name)
	}
	if result == nil {
		return nil
	}
	if textResult == "" {
		return fmt.Errorf("cm tool %s response contained no text result", name)
	}
	if err := json.Unmarshal([]byte(textResult), result); err != nil {
		return fmt.Errorf("decoding cm tool %s result: %w", name, err)
	}
	return nil
}

type ContextResult struct {
	RelevantBullets  []Rule           `json:"relevantBullets"`
	AntiPatterns     []Rule           `json:"antiPatterns"`
	HistorySnippets  []CLIHistorySnip `json:"historySnippets"`
	SuggestedQueries []string         `json:"suggestedCassQueries"`
}

type Rule struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Category   string   `json:"category"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// GetContext queries CM for task-relevant rules through its MCP HTTP server.
//
// `workspace`, when non-empty, is sent as a `workspace` field on the request
// body so the daemon can scope its retrieval to that workspace and avoid
// same-basename cross-project context bleed (#132).
func (c *Client) GetContext(ctx context.Context, task string, workspace string) (*ContextResult, error) {
	arguments := map[string]string{"task": task}
	if strings.TrimSpace(workspace) != "" {
		arguments["workspace"] = workspace
	}
	var result ContextResult
	if err := c.callTool(ctx, "cm_context", arguments, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Health checks whether the CM daemon responds to the MCP ping method.
func (c *Client) Health(ctx context.Context) error {
	return c.rpc(ctx, "ping", map[string]any{}, nil)
}

// Port returns the daemon port extracted from the client's base URL.
// Returns 0 when the URL cannot be parsed or has no valid port.
func (c *Client) Port() int {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return 0
	}
	p := u.Port()
	if p == "" {
		return 0
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0
	}
	return port
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
	if strings.TrimSpace(c.sessionID) == "" {
		return fmt.Errorf("cm outcome failed: session ID is empty")
	}

	notes := strings.TrimSpace(report.Notes)
	if sentiment := strings.TrimSpace(report.Sentiment); sentiment != "" {
		// CM's MCP outcome schema has no separate sentiment field. Preserve the
		// legacy NTM value in notes instead of silently dropping it.
		if notes != "" {
			notes = fmt.Sprintf("sentiment=%s\n%s", sentiment, notes)
		} else {
			notes = "sentiment=" + sentiment
		}
	}

	arguments := map[string]any{
		"sessionId": c.sessionID,
		"outcome":   string(report.Status),
		"rulesUsed": report.RuleIDs,
	}
	if notes != "" {
		arguments["notes"] = notes
	}
	return c.callTool(ctx, "cm_outcome", arguments, nil)
}

// CLIClient interacts with the CM CLI directly via exec.Command.
// This is used for recovery context where we may not have a daemon running.
type CLIClient struct {
	binaryPath string
	timeout    time.Duration
}

// CLIContextResponse matches the JSON output of `cm context --json`
type CLIContextResponse struct {
	Success          bool             `json:"success"`
	Task             string           `json:"task"`
	RelevantBullets  []CLIRule        `json:"relevantBullets"`
	AntiPatterns     []CLIRule        `json:"antiPatterns"`
	HistorySnippets  []CLIHistorySnip `json:"historySnippets"`
	SuggestedQueries []string         `json:"suggestedCassQueries"`
}

// CLIRule represents a rule from CM playbook
type CLIRule struct {
	ID         string   `json:"id"`
	Content    string   `json:"content"`
	Category   string   `json:"category,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
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
// It executes: cm context '<task>' --json [--workspace <path>]
// Returns nil with no error if CM is not installed (graceful degradation).
//
// `workspace` should be the absolute path of the workspace that owns this
// query. When non-empty it is passed as `--workspace <path>` so same-basename
// workspaces (e.g. /clientA/app and /clientB/app) do not share recovery
// context (#132). Empty workspace falls through to the unscoped CM query.
func (c *CLIClient) GetContext(ctx context.Context, task string, workspace string) (*CLIContextResponse, error) {
	if !c.IsInstalled() {
		return nil, nil // Graceful degradation: CM not available
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := []string{"context", task, "--json"}
	if strings.TrimSpace(workspace) != "" {
		args = append(args, "--workspace", workspace)
	}
	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	cmd.WaitDelay = 2 * time.Second
	// Run from a stable directory. Some environments (and some tests) temporarily
	// chdir into directories that may later be removed; if that happens, `cm`
	// can fail even though it doesn't need the current project directory.
	cmd.Dir = os.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check if context was cancelled or timed out
		if ctx.Err() != nil {
			return nil, fmt.Errorf("cm context %v after %v", ctx.Err(), c.timeout)
		}
		// Non-zero exit but may still have valid JSON (e.g., empty results)
		// Try to parse stdout anyway
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("cm context failed: %w (stderr: %s)", err, stderr.String())
		}
	}

	result, err := decodeCLIContextResponse(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("parsing cm output: %w (raw: %s)", err, stdout.String())
	}

	return result, nil
}

func decodeCLIContextResponse(data []byte) (*CLIContextResponse, error) {
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	var result CLIContextResponse
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			return nil, err
		}
		result.Success = envelope.Success
		return &result, nil
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetRecoveryContext is a convenience method that formats the task for recovery use.
// It queries CM with a recovery-focused task description and limits results.
//
// `projectName` becomes part of the task string (CM uses task text for
// retrieval), and `workspace` is the absolute path passed through to CM as
// `--workspace`. The two are independent: same-basename workspaces produce
// the same projectName but different `--workspace`, so CM's own scoping
// keeps their recovery memories distinct (#132).
func (c *CLIClient) GetRecoveryContext(ctx context.Context, projectName string, workspace string, maxRules, maxSnippets int) (*CLIContextResponse, error) {
	task := fmt.Sprintf("%s: starting new coding session", projectName)
	result, err := c.GetContext(ctx, task, workspace)
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
			buf.WriteString(fmt.Sprintf("- **[%s]%s** %s\n", rule.ID, formatRuleConfidence(rule.Confidence), rule.Content))
		}
		buf.WriteString("\n")
	}

	if len(result.AntiPatterns) > 0 {
		buf.WriteString("## Anti-Patterns to Avoid\n\n")
		for _, pattern := range result.AntiPatterns {
			buf.WriteString(fmt.Sprintf("- ⚠️ **[%s]%s** %s\n", pattern.ID, formatRuleConfidence(pattern.Confidence), pattern.Content))
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

func formatRuleConfidence(confidence *float64) string {
	if confidence == nil {
		return ""
	}
	return fmt.Sprintf(" (confidence: %.0f%%)", *confidence*100)
}

// truncate shortens a string to maxLen runes, adding ellipsis if needed
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
