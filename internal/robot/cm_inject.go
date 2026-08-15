package robot

// CM (CASS Memory) per-task rule injection for robot sends (bd-3j6hm).
//
// This mirrors the CASS context-injection pipeline (cass_inject.go) at the
// send boundary: an opt-in --with-memory toggle on SendOptions queries the
// cm daemon (MCP cm_context via internal/cm.Client) or the cm CLI as a
// fallback, takes the top-N relevant rules within a token budget, and
// prepends a compact "## Project rules" block to the outgoing message.
// Injection metadata (rule IDs, token cost, skip reason) is recorded in the
// send envelope as CMInjectionInfo, the same place CASSInjectionInfo lands.
//
// Memory is best-effort enrichment: cm being missing, wedged, or empty must
// never fail or block a send beyond the bounded query timeout.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cm"
	"github.com/Dicklesworthstone/ntm/internal/process"
)

// DefaultCMQueryTimeout bounds the cm context query during a send. A wedged
// memory daemon must never stall dispatch for longer than this.
const DefaultCMQueryTimeout = 5 * time.Second

// DefaultCMOutcomeTimeout bounds the automatic cm_outcome report fired after
// a tracked send completes with a confirmed acknowledgment.
const DefaultCMOutcomeTimeout = 3 * time.Second

// defaultCMMaxRules and defaultCMBudgetTokens are the compiled-in defaults
// for [memory] send_max_rules / send_budget_tokens (kept in sync with
// config.DefaultMemoryConfig).
const (
	defaultCMMaxRules     = 5
	defaultCMBudgetTokens = 1500
)

// CMContextClient is the subset of the cm client used for rule injection.
// Both cm.Client (daemon MCP) and the CLI fallback adapter satisfy it.
type CMContextClient interface {
	GetContext(ctx context.Context, task string, workspace string) (*cm.ContextResult, error)
}

// CMOutcomeClient is the subset of the cm client used for outcome feedback.
type CMOutcomeClient interface {
	RecordOutcome(ctx context.Context, report cm.OutcomeReport) error
}

// CMInjectConfig configures per-send memory rule injection. The zero value is
// NOT usable; start from DefaultCMInjectConfig and override.
type CMInjectConfig struct {
	// Enabled is the master switch (config [memory] enabled). When false the
	// --with-memory toggle degrades to a recorded skip instead of an error.
	Enabled bool

	// MaxRules caps how many rules are injected (config send_max_rules).
	MaxRules int

	// BudgetTokens caps the estimated token cost of the injected block
	// (config send_budget_tokens). Estimation is ~4 chars/token, matching
	// the CASS injection pipeline.
	BudgetTokens int

	// QueryTimeout bounds the cm context query.
	QueryTimeout time.Duration

	// OutcomeTimeout bounds the automatic outcome report.
	OutcomeTimeout time.Duration

	// ProjectDir is the daemon-discovery root (contains .ntm/pids). Empty
	// means the current working directory.
	ProjectDir string

	// Workspace is passed to cm as the workspace scope so same-basename
	// projects do not bleed memory into each other (#132). Empty means
	// ProjectDir.
	Workspace string

	// CLIBinary overrides the cm binary used by the CLI fallback. Empty
	// means "cm" from PATH. Tests point it at a nonexistent path to make
	// unavailability deterministic.
	CLIBinary string

	// Client overrides daemon/CLI discovery. Used by tests and embedders.
	Client CMContextClient

	// Outcome overrides daemon discovery for outcome feedback.
	Outcome CMOutcomeClient
}

// DefaultCMInjectConfig returns the compiled-in defaults, matching
// config.DefaultMemoryConfig's send-injection keys.
func DefaultCMInjectConfig() CMInjectConfig {
	return CMInjectConfig{
		Enabled:        true,
		MaxRules:       defaultCMMaxRules,
		BudgetTokens:   defaultCMBudgetTokens,
		QueryTimeout:   DefaultCMQueryTimeout,
		OutcomeTimeout: DefaultCMOutcomeTimeout,
	}
}

// normalized fills unusable zero values with defaults so a partially
// populated config (e.g. decoded from JSON) still behaves sanely.
func (c CMInjectConfig) normalized() CMInjectConfig {
	if c.MaxRules <= 0 {
		c.MaxRules = defaultCMMaxRules
	}
	if c.BudgetTokens <= 0 {
		c.BudgetTokens = defaultCMBudgetTokens
	}
	if c.QueryTimeout <= 0 {
		c.QueryTimeout = DefaultCMQueryTimeout
	}
	if c.OutcomeTimeout <= 0 {
		c.OutcomeTimeout = DefaultCMOutcomeTimeout
	}
	if c.ProjectDir == "" {
		if wd, err := os.Getwd(); err == nil {
			c.ProjectDir = wd
		}
	}
	if c.Workspace == "" {
		c.Workspace = c.ProjectDir
	}
	return c
}

// CMInjectionInfo reports memory rule injection details in robot responses.
// It is the CM analogue of CASSInjectionInfo and is additive (omitempty) on
// SendOutput.
type CMInjectionInfo struct {
	// Enabled indicates whether memory injection was attempted.
	Enabled bool `json:"enabled"`
	// RulesInjected lists the IDs of the rules that were injected.
	RulesInjected []string `json:"rules_injected,omitempty"`
	// TokensAdded is the estimated token count of the injected block.
	TokensAdded int `json:"tokens_added"`
	// SkippedReason explains why injection was skipped, if applicable.
	SkippedReason string `json:"skipped_reason,omitempty"`
	// Source records which transport produced the rules: "daemon" or "cli".
	Source string `json:"source,omitempty"`
}

// cmCLIContextAdapter adapts the cm CLI client to CMContextClient.
type cmCLIContextAdapter struct {
	cli *cm.CLIClient
}

func (a cmCLIContextAdapter) GetContext(ctx context.Context, task string, workspace string) (*cm.ContextResult, error) {
	resp, err := a.cli.GetContext(ctx, task, workspace)
	if err != nil || resp == nil {
		return nil, err
	}
	return &cm.ContextResult{
		Task:             resp.Task,
		RelevantBullets:  resp.RelevantBullets,
		AntiPatterns:     resp.AntiPatterns,
		HistorySnippets:  resp.HistorySnippets,
		SuggestedQueries: resp.SuggestedQueries,
	}, nil
}

// discoverCMDaemonClient scans projectDir/.ntm/pids for a live cm daemon and
// returns a connected MCP client for it. This is the same discovery contract
// as internal/serve's checkMemoryDaemon (PID file named cm-<sessionID>.pid,
// liveness-verified), reimplemented here because robot cannot depend on serve.
func discoverCMDaemonClient(projectDir string) (*cm.Client, bool) {
	pidsDir := filepath.Join(projectDir, ".ntm", "pids")
	entries, err := os.ReadDir(pidsDir)
	if err != nil {
		return nil, false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "cm-") || !strings.HasSuffix(name, ".pid") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pidsDir, name))
		if err != nil {
			continue
		}
		var info cm.PIDFileInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}
		if info.PID <= 0 || !process.IsAlive(info.PID) {
			continue
		}
		sessionID := strings.TrimSuffix(strings.TrimPrefix(name, "cm-"), ".pid")
		if info.Port <= 0 {
			continue
		}
		return cm.NewPortClient(info.Port, sessionID), true
	}
	return nil, false
}

// resolveCMContextClient picks the query transport: explicit override first,
// then a live daemon, then the cm CLI. The bool reports availability; the
// string names the transport for CMInjectionInfo.Source.
func resolveCMContextClient(cfg CMInjectConfig) (CMContextClient, string, bool) {
	if cfg.Client != nil {
		return cfg.Client, "daemon", true
	}
	if client, ok := discoverCMDaemonClient(cfg.ProjectDir); ok {
		return client, "daemon", true
	}
	cli := cm.NewCLIClient(cm.WithCLIBinaryPath(cfg.CLIBinary), cm.WithCLITimeout(cfg.QueryTimeout))
	if cli.IsInstalled() {
		return cmCLIContextAdapter{cli: cli}, "cli", true
	}
	return nil, "", false
}

// estimateCMTokens mirrors the CASS pipeline's rough 4-chars-per-token rule.
func estimateCMTokens(s string) int {
	return len(s) / 4
}

// cmRuleLine renders one rule as a single compact bullet, collapsing internal
// whitespace so multi-line rule content cannot break the block format.
func cmRuleLine(rule cm.Rule) string {
	content := strings.Join(strings.Fields(rule.Content), " ")
	id := strings.TrimSpace(rule.ID)
	if id == "" {
		return "- " + content + "\n"
	}
	return "- [" + id + "] " + content + "\n"
}

// formatCMRulesBlock builds the compact "## Project rules" block from the
// top-N rules that fit within the token budget. It returns the block and the
// IDs of the rules included. A rule with an empty ID still counts against
// maxRules and is injected, but contributes no entry to the returned IDs:
// those IDs feed rules_injected and the automatic cm_outcome report, and an
// empty-string rule ID would poison both.
func formatCMRulesBlock(rules []cm.Rule, maxRules, budgetTokens int) (string, []string) {
	if len(rules) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("## Project rules\n\n")

	var ids []string
	injected := 0
	for _, rule := range rules {
		if injected >= maxRules {
			break
		}
		if strings.TrimSpace(rule.Content) == "" {
			continue
		}
		line := cmRuleLine(rule)
		if estimateCMTokens(b.String())+estimateCMTokens(line) > budgetTokens {
			break
		}
		b.WriteString(line)
		injected++
		if id := strings.TrimSpace(rule.ID); id != "" {
			ids = append(ids, id)
		}
	}

	if injected == 0 {
		return "", nil
	}
	return b.String(), ids
}

// InjectCMRules queries CM for rules relevant to task and prepends them to
// message. It never fails the send: every degraded path returns the message
// unchanged with a populated SkippedReason.
//
// task is the retrieval query (the caller's original message text); message
// is the payload to prepend to, which may already carry CASS-injected
// context.
func InjectCMRules(ctx context.Context, task, message string, cfg CMInjectConfig) (string, *CMInjectionInfo) {
	info := &CMInjectionInfo{Enabled: true}

	if !cfg.Enabled {
		info.Enabled = false
		info.SkippedReason = "memory integration disabled in config (memory.enabled=false)"
		return message, info
	}
	cfg = cfg.normalized()

	client, source, ok := resolveCMContextClient(cfg)
	if !ok {
		info.SkippedReason = "cm is not available (no daemon running and cm CLI not installed)"
		return message, info
	}
	info.Source = source

	queryCtx, cancel := context.WithTimeout(ctx, cfg.QueryTimeout)
	defer cancel()

	result, err := client.GetContext(queryCtx, task, cfg.Workspace)
	if err != nil {
		info.SkippedReason = fmt.Sprintf("cm context query failed: %v", err)
		return message, info
	}
	if result == nil || len(result.RelevantBullets) == 0 {
		info.SkippedReason = "no relevant rules found"
		return message, info
	}

	block, ids := formatCMRulesBlock(result.RelevantBullets, cfg.MaxRules, cfg.BudgetTokens)
	if block == "" {
		info.SkippedReason = "token budget too small for any rule"
		return message, info
	}

	info.RulesInjected = ids
	info.TokensAdded = estimateCMTokens(block)
	return block + "\n---\n\n" + message, info
}

// shouldReportCMSendOutcome is the evidence gate for automatic outcome
// feedback after a tracked send. Only unambiguous evidence reports: at least
// one confirmed acknowledgment and no ack timeout. A timeout (or a send that
// never injected rules) reports nothing — ambiguous results must not train
// the memory system.
func shouldReportCMSendOutcome(withMemory bool, info *CMInjectionInfo, confirmations int, timedOut bool) bool {
	return withMemory &&
		info != nil &&
		len(info.RulesInjected) > 0 &&
		confirmations > 0 &&
		!timedOut
}

// reportCMOutcome sends automatic outcome feedback for previously injected
// rules. It is evidence-gated by the caller (only called on a confirmed ack)
// and conservative here: without a daemon (or explicit override) it does
// nothing, and errors are logged, never surfaced. The RPC runs in a goroutine
// bounded by OutcomeTimeout; the call itself returns once the report finishes
// or the bound elapses, so a wedged daemon cannot hang the send path.
func reportCMOutcome(cfg CMInjectConfig, status cm.OutcomeStatus, ruleIDs []string) {
	if len(ruleIDs) == 0 {
		return
	}
	cfg = cfg.normalized()

	client := cfg.Outcome
	if client == nil {
		daemon, ok := discoverCMDaemonClient(cfg.ProjectDir)
		if !ok {
			slog.Debug("cm outcome skipped: no daemon available", "rules", ruleIDs)
			return
		}
		client = daemon
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.OutcomeTimeout)
		defer cancel()
		report := cm.OutcomeReport{
			Status:  status,
			RuleIDs: ruleIDs,
			Notes:   "ntm robot send: acknowledgment confirmed",
		}
		if err := client.RecordOutcome(ctx, report); err != nil {
			slog.Debug("cm outcome report failed", "error", err, "rules", ruleIDs)
		}
	}()

	select {
	case <-done:
	case <-time.After(cfg.OutcomeTimeout + 500*time.Millisecond):
		slog.Debug("cm outcome report timed out", "rules", ruleIDs)
	}
}
