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

// Client talks to a `cm serve` daemon. The daemon speaks MCP JSON-RPC over
// HTTP at its root path (there are no REST routes such as /context or
// /health), so every operation is either a plain JSON-RPC method (tools/list)
// or a tools/call invocation (cm_context, cm_outcome).
type Client struct {
	baseURL   string
	sessionID string
	client    *http.Client
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

	return NewPortClient(info.Port, sessionID), nil
}

// NewPortClient creates a CM client for a daemon on a known localhost port.
// sessionID may be empty for callers that only query context or health;
// RecordOutcome requires it.
func NewPortClient(port int, sessionID string) *Client {
	return &Client{
		baseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		sessionID: sessionID,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ContextResult is the payload of a CM context query. The same shape is
// produced by the MCP cm_context tool and by `cm context --json` (under its
// success/data envelope on current releases).
type ContextResult struct {
	Task             string           `json:"task,omitempty"`
	RelevantBullets  []Rule           `json:"relevantBullets"`
	AntiPatterns     []Rule           `json:"antiPatterns"`
	HistorySnippets  []HistorySnippet `json:"historySnippets"`
	SuggestedQueries []string         `json:"suggestedCassQueries"`
}

// Rule is a playbook bullet returned by CM. CM's bullet schema carries many
// more fields (scope, maturity, scoring); NTM consumes the identity and text.
type Rule struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
}

// HistorySnippet is a CASS search hit surfaced by CM context queries.
// This matches CM's actual wire schema; there are no `id`/`content` fields.
type HistorySnippet struct {
	SourcePath string  `json:"source_path"`
	LineNumber int     `json:"line_number"`
	Agent      string  `json:"agent"`
	Workspace  string  `json:"workspace,omitempty"`
	Title      string  `json:"title,omitempty"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score,omitempty"`
	CreatedAt  int64   `json:"created_at,omitempty"`
}

type rpcErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponsePayload struct {
	Result json.RawMessage  `json:"result"`
	Error  *rpcErrorPayload `json:"error"`
}

// rpc performs one MCP JSON-RPC call against the daemon root. A JSON-RPC
// error object is surfaced as a Go error even though the HTTP status is 200 —
// treating those responses as success is exactly the silent context drop
// reported in #249.
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
		return fmt.Errorf("marshal cm %s request: %w", method, err)
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
		return fmt.Errorf("cm %s failed: %s", method, resp.Status)
	}

	var rpcResp rpcResponsePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 10*1024*1024)).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode cm %s response: %w", method, err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("cm %s failed (%d): %s", method, rpcResp.Error.Code, rpcResp.Error.Message)
	}
	// A JSON-RPC response with neither result nor error is not a valid
	// answer to any method we call. Enforcing this even when the caller
	// discards the result keeps Health() from blessing an arbitrary HTTP
	// service that happens to answer 200 with unrelated JSON.
	if len(rpcResp.Result) == 0 || string(rpcResp.Result) == "null" {
		return fmt.Errorf("cm %s returned no result", method)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(rpcResp.Result, result)
}

// mcpToolEnvelope is the standard MCP tools/call result wrapper. Newer CM
// releases use it; older releases return the tool payload directly as the
// JSON-RPC result object, so callTool accepts both shapes.
type mcpToolEnvelope struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callTool invokes an MCP tool and decodes its JSON payload into result.
// It handles both known CM result shapes:
//   - standard MCP: {"content":[{"type":"text","text":"<json>"}],"isError":bool}
//   - direct object: the payload itself as the JSON-RPC result (older cm)
func (c *Client) callTool(ctx context.Context, name string, arguments any, result any) error {
	var raw json.RawMessage
	if err := c.rpc(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	}, &raw); err != nil {
		return err
	}

	// Shape detection keys off the PRESENCE of the standard envelope
	// members, not their values: an envelope with empty content and
	// isError=false must be treated as an (invalid) envelope, never
	// silently decoded as a direct-object payload — decoding the envelope
	// itself into the result yields an empty-but-successful context, the
	// exact silent drop (#249) this client exists to prevent.
	var probe map[string]json.RawMessage
	isEnvelope := false
	if err := json.Unmarshal(raw, &probe); err == nil {
		_, hasContent := probe["content"]
		_, hasIsError := probe["isError"]
		isEnvelope = hasContent || hasIsError
	}
	var envelope mcpToolEnvelope
	if isEnvelope && json.Unmarshal(raw, &envelope) == nil {
		var text string
		for _, content := range envelope.Content {
			if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
				text = content.Text
				break
			}
		}
		if envelope.IsError {
			if text != "" {
				return fmt.Errorf("cm tool %s failed: %s", name, text)
			}
			return fmt.Errorf("cm tool %s failed", name)
		}
		if result == nil {
			return nil
		}
		if text == "" {
			return fmt.Errorf("cm tool %s returned no text payload", name)
		}
		if err := json.Unmarshal([]byte(text), result); err != nil {
			return fmt.Errorf("decode cm tool %s payload: %w", name, err)
		}
		return nil
	}

	// Direct-object shape: the JSON-RPC result is the tool payload itself.
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("decode cm tool %s result: %w", name, err)
	}
	return nil
}

// GetContext queries CM for task-relevant rules via the MCP cm_context tool.
//
// `workspace`, when non-empty, is sent as a `workspace` argument so the
// daemon can scope its retrieval to that workspace and avoid same-basename
// cross-project context bleed (#132).
func (c *Client) GetContext(ctx context.Context, task string, workspace string) (*ContextResult, error) {
	arguments := map[string]any{"task": task}
	if strings.TrimSpace(workspace) != "" {
		arguments["workspace"] = workspace
	}

	var result ContextResult
	if err := c.callTool(ctx, "cm_context", arguments, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// Health checks whether the CM daemon is responding. tools/list is used as
// the probe because every MCP-speaking CM release supports it; the ping
// method only exists on newer releases and legacy GET /health routes no
// longer exist at all.
func (c *Client) Health(ctx context.Context) error {
	return c.rpc(ctx, "tools/list", nil, nil)
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

// RecordOutcome sends feedback about rule effectiveness via the MCP
// cm_outcome tool. The tool requires a session ID and accepts outcomes
// "success" | "failure" | "mixed"; NTM's "partial" maps to "mixed". CM's
// outcome schema has no sentiment field, so a non-empty sentiment is
// preserved in the notes instead of being silently dropped.
func (c *Client) RecordOutcome(ctx context.Context, report OutcomeReport) error {
	if strings.TrimSpace(c.sessionID) == "" {
		return fmt.Errorf("cm outcome requires a session ID")
	}

	outcome := string(report.Status)
	if report.Status == OutcomePartial {
		outcome = "mixed"
	}

	notes := strings.TrimSpace(report.Notes)
	if sentiment := strings.TrimSpace(report.Sentiment); sentiment != "" {
		if notes != "" {
			notes = fmt.Sprintf("sentiment=%s\n%s", sentiment, notes)
		} else {
			notes = "sentiment=" + sentiment
		}
	}

	arguments := map[string]any{
		"sessionId": c.sessionID,
		"outcome":   outcome,
	}
	if len(report.RuleIDs) > 0 {
		arguments["rulesUsed"] = report.RuleIDs
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

// CLIContextResponse matches the JSON output of `cm context --json`.
// Current CM releases wrap the payload in a {success, data} envelope;
// older releases emit the fields at the top level. decodeCLIContextResponse
// accepts both.
type CLIContextResponse struct {
	Success          bool             `json:"success"`
	Task             string           `json:"task,omitempty"`
	RelevantBullets  []Rule           `json:"relevantBullets"`
	AntiPatterns     []Rule           `json:"antiPatterns"`
	HistorySnippets  []HistorySnippet `json:"historySnippets"`
	SuggestedQueries []string         `json:"suggestedCassQueries"`
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

// decodeCLIContextResponse decodes `cm context --json` output. Current CM
// releases wrap the payload as {"success":true,"data":{...}}; older releases
// put the fields at the top level next to "success". Decoding the envelope
// shape into the flat struct silently produced empty context (#249), so the
// data envelope is unwrapped explicitly and preferred when present.
func decodeCLIContextResponse(data []byte) (*CLIContextResponse, error) {
	var envelope struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		var result CLIContextResponse
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			return nil, err
		}
		result.Success = envelope.Success
		return &result, nil
	}

	var result CLIContextResponse
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
			title := snippet.Title
			if title == "" {
				title = snippet.SourcePath
			}
			buf.WriteString(fmt.Sprintf("- **%s** (%s)\n  %s\n", title, snippet.Agent, truncate(snippet.Snippet, 200)))
		}
		buf.WriteString("\n")
	}

	return buf.String()
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
