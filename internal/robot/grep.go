package robot

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	grepSchemaID      = "ntm:robot:grep:v1"
	grepSchemaVersion = "1.0.0"

	GrepSchemaID      = grepSchemaID
	GrepSchemaVersion = grepSchemaVersion

	// Hard upper bounds protecting memory and CPU against unbounded scrollback queries.
	MaxScrollbackLines        = 10000
	MaxScrollbackContextLines = 50
	MaxScrollbackPatternBytes = 4096
	MaxGrepMatches            = 5000
)

// GrepMatch represents a single regex match in captured tmux pane output.
type GrepMatch struct {
	Session       string   `json:"session"`
	Pane          string   `json:"pane"`
	PaneID        string   `json:"pane_id"`
	PaneIndex     int      `json:"pane_index"`
	AgentType     string   `json:"agent_type,omitempty"`
	Line          int      `json:"line"`
	Content       string   `json:"content"`
	Context       []string `json:"context,omitempty"`
	ContextBefore int      `json:"context_before,omitempty"`
	ContextAfter  int      `json:"context_after,omitempty"`
}

// GrepOptions configures GetGrep across single-session or multi-session pane scrollback.
type GrepOptions struct {
	Pattern         string
	Session         string
	AllSessions     bool
	AgentTypes      []string
	IncludeAllPanes bool
	CaseInsensitive bool
	InvertMatch     bool
	ContextLines    int
	BeforeLines     int
	AfterLines      int
	MaxLines        int
	ListOnly        bool
}

// GrepOutput is the canonical robot envelope for --robot-grep and /api/v1/*grep.
type GrepOutput struct {
	RobotResponse
	SchemaID        string      `json:"schema_id"`
	SchemaVersion   string      `json:"schema_version"`
	Pattern         string      `json:"pattern"`
	Session         string      `json:"session"`
	Matches         []GrepMatch `json:"matches"`
	MatchingPanes   []string    `json:"matching_panes"`
	TotalLines      int         `json:"total_lines_searched"`
	MatchCount      int         `json:"match_count"`
	PaneCount       int         `json:"panes_searched"`
	CaseInsensitive bool        `json:"case_insensitive,omitempty"`
	InvertMatch     bool        `json:"invert_match,omitempty"`
	ListOnly        bool        `json:"list_only,omitempty"`
	Truncated       bool        `json:"truncated,omitempty"`
	Warnings        []string    `json:"warnings,omitempty"`
}

// CompileGrepRegex compiles a grep pattern with optional case-insensitivity.
func CompileGrepRegex(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("grep pattern is required")
	}
	if len(pattern) > MaxScrollbackPatternBytes {
		return nil, fmt.Errorf("grep pattern exceeds maximum length (%d bytes)", MaxScrollbackPatternBytes)
	}
	rePattern := pattern
	if caseInsensitive {
		rePattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(rePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	return re, nil
}

// GetGrep searches pane scrollback buffers with regular expressions across all built-in harnesses.
func GetGrep(opts GrepOptions) (*GrepOutput, error) {
	return GetGrepContext(context.Background(), opts)
}

// GetGrepContext searches pane scrollback buffers respecting ctx cancellation and bounded limits.
func GetGrepContext(ctx context.Context, opts GrepOptions) (*GrepOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := &GrepOutput{
		RobotResponse:   NewRobotResponse(true),
		SchemaID:        grepSchemaID,
		SchemaVersion:   grepSchemaVersion,
		Pattern:         opts.Pattern,
		Session:         strings.TrimSpace(opts.Session),
		Matches:         []GrepMatch{},
		MatchingPanes:   []string{},
		CaseInsensitive: opts.CaseInsensitive,
		InvertMatch:     opts.InvertMatch,
		ListOnly:        opts.ListOnly,
	}

	re, err := CompileGrepRegex(opts.Pattern, opts.CaseInsensitive)
	if err != nil {
		out.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Pass a valid Go regular expression to --robot-grep=<pattern>")
		return out, nil
	}

	if err := tmux.EnsureInstalled(); err != nil {
		out.RobotResponse = NewErrorResponse(err, ErrCodeDependencyMissing, "Install tmux to search live pane scrollback")
		return out, nil
	}

	var sessions []string
	if opts.AllSessions {
		sessionList, err := tmux.ListSessionsContext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				out.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Request cancelled or timed out while listing tmux sessions")
				return out, nil
			}
			out.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Verify the tmux server is reachable")
			return out, nil
		}
		out.Session = "<all>"
		for _, s := range sessionList {
			sessions = append(sessions, s.Name)
		}
		sort.Strings(sessions)
	} else {
		if out.Session == "" {
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("session name required unless --all is specified"),
				ErrCodeInvalidFlag,
				"Pass --grep-session=<session> or --all to search across all tmux sessions",
			)
			return out, nil
		}
		exists, err := tmux.SessionExistsContext(ctx, out.Session)
		if err != nil {
			if ctx.Err() != nil {
				out.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Request cancelled or timed out while checking session")
				return out, nil
			}
			out.RobotResponse = NewErrorResponse(err, ErrCodeInternalError, "Verify the tmux server is reachable")
			return out, nil
		}
		if !exists {
			out.RobotResponse = NewErrorResponse(
				fmt.Errorf("session %q not found", out.Session),
				ErrCodeSessionNotFound,
				"Use ntm --robot-status to inspect active tmux sessions",
			)
			return out, nil
		}
		sessions = []string{out.Session}
	}

	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = 1000
	} else if maxLines > MaxScrollbackLines {
		maxLines = MaxScrollbackLines
	}
	beforeCount := opts.BeforeLines
	afterCount := opts.AfterLines
	if opts.ContextLines > 0 && beforeCount == 0 && afterCount == 0 {
		beforeCount = opts.ContextLines
		afterCount = opts.ContextLines
	}
	if beforeCount < 0 {
		beforeCount = 0
	} else if beforeCount > MaxScrollbackContextLines {
		beforeCount = MaxScrollbackContextLines
	}
	if afterCount < 0 {
		afterCount = 0
	} else if afterCount > MaxScrollbackContextLines {
		afterCount = MaxScrollbackContextLines
	}

	wantedTypes := normalizedAgentTypeFilter(opts.AgentTypes)
	matchingPaneSet := make(map[string]struct{})

sessionLoop:
	for _, sess := range sessions {
		if err := ctx.Err(); err != nil {
			out.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Request cancelled or timed out while searching scrollback")
			return out, nil
		}
		panes, err := tmux.GetPanesContext(ctx, sess)
		if err != nil {
			if ctx.Err() != nil {
				out.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Request cancelled or timed out while listing panes")
				return out, nil
			}
			out.Warnings = append(out.Warnings, fmt.Sprintf("failed to list panes for %s: %v", sess, err))
			continue
		}

		for _, pane := range panes {
			if err := ctx.Err(); err != nil {
				out.RobotResponse = NewErrorResponse(err, ErrCodeTimeout, "Request cancelled or timed out while searching panes")
				return out, nil
			}
			if pane.IsServicePane() && !opts.IncludeAllPanes && len(wantedTypes) == 0 {
				continue
			}

			captured, err := tmux.CapturePaneOutputContext(ctx, pane.ID, maxLines)
			if err != nil {
				if ctx.Err() != nil {
					out.RobotResponse = NewErrorResponse(ctx.Err(), ErrCodeTimeout, "Request cancelled or timed out while capturing pane output")
					return out, nil
				}
				out.Warnings = append(out.Warnings, fmt.Sprintf("failed to capture pane %s/%s: %v", sess, pane.Title, err))
				continue
			}

			detectedType := ResolveAgentType(string(pane.Type))
			if detectedType == "" || detectedType == "unknown" || detectedType == "user" {
				if enhanced := DetectAgentTypeEnhanced(pane, captured); enhanced.Type != "" {
					detectedType = ResolveAgentType(enhanced.Type)
				}
			}

			if len(wantedTypes) > 0 {
				matchedType := false
				for _, wt := range wantedTypes {
					if detectedType == wt || ResolveAgentType(string(pane.Type)) == wt {
						matchedType = true
						break
					}
				}
				if !matchedType {
					continue
				}
			} else if !opts.IncludeAllPanes && pane.Type == tmux.AgentUser && (detectedType == "" || detectedType == "user" || detectedType == "unknown") {
				continue
			}

			lines := strings.Split(captured, "\n")
			out.TotalLines += len(lines)
			out.PaneCount++

			paneLabel := pane.Title
			if strings.TrimSpace(paneLabel) == "" {
				paneLabel = fmt.Sprintf("pane_%d", pane.Index)
			}
			paneKey := sess + "/" + paneLabel

			for i, line := range lines {
				matched := re.MatchString(line)
				if opts.InvertMatch {
					matched = !matched
				}
				if !matched {
					continue
				}
				matchingPaneSet[paneKey] = struct{}{}
				if opts.ListOnly {
					continue
				}
				if len(out.Matches) >= MaxGrepMatches {
					out.Truncated = true
					out.Warnings = append(out.Warnings, fmt.Sprintf("results truncated at %d matches", MaxGrepMatches))
					break sessionLoop
				}

				var ctxLines []string
				actualBefore := 0
				actualAfter := 0
				for j := max(0, i-beforeCount); j < i; j++ {
					ctxLines = append(ctxLines, lines[j])
					actualBefore++
				}
				for j := i + 1; j <= i+afterCount && j < len(lines); j++ {
					ctxLines = append(ctxLines, lines[j])
					actualAfter++
				}

				out.Matches = append(out.Matches, GrepMatch{
					Session:       sess,
					Pane:          paneLabel,
					PaneID:        pane.ID,
					PaneIndex:     pane.Index,
					AgentType:     detectedType,
					Line:          i + 1,
					Content:       line,
					Context:       ctxLines,
					ContextBefore: actualBefore,
					ContextAfter:  actualAfter,
				})
			}
		}
	}

	for paneKey := range matchingPaneSet {
		out.MatchingPanes = append(out.MatchingPanes, paneKey)
	}
	sort.Strings(out.MatchingPanes)
	if opts.ListOnly {
		out.MatchCount = len(out.MatchingPanes)
	} else {
		out.MatchCount = len(out.Matches)
	}
	return out, nil
}

// PrintGrep writes the terminal JSON/TOON response for --robot-grep.
func PrintGrep(opts GrepOptions) error {
	out, err := GetGrep(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot grep failed")
}

func grepCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "grep",
		Flag:        "--robot-grep",
		Category:    "state",
		Summary:     "Search pane scrollback with regex.",
		Description: "Search captured tmux pane output with regular expressions across one or all sessions, with 12-harness agent filtering and before/after context lines.",
		Parameters: []RobotParameter{
			{Name: "pattern", Flag: "--robot-grep", Type: "string", Required: true, Description: "Go regular expression to search for"},
			{Name: "grep-session", Flag: "--grep-session", Type: "string", Required: false, Description: "Target session name (required unless --all is set)"},
			{Name: "all", Flag: "--all", Type: "bool", Required: false, Default: "false", Description: "Search across all active tmux sessions"},
			{Name: "type", Flag: "--type", Type: "string", Required: false, Description: "Comma-separated harness filter (cc, cod, gmi, agy, grok, cursor, windsurf, aider, oc, omp, pi, ollama)"},
			{Name: "grep-ignore-case", Flag: "--grep-ignore-case", Type: "bool", Required: false, Default: "false", Description: "Case-insensitive regex matching"},
			{Name: "grep-invert", Flag: "--grep-invert", Type: "bool", Required: false, Default: "false", Description: "Invert matching to return non-matching lines"},
			{Name: "grep-context", Flag: "--grep-context", Type: "int", Required: false, Default: "0", Description: "Symmetric context lines before and after each match"},
			{Name: "grep-before", Flag: "--grep-before", Type: "int", Required: false, Default: "0", Description: "Context lines before each match"},
			{Name: "grep-after", Flag: "--grep-after", Type: "int", Required: false, Default: "0", Description: "Context lines after each match"},
			{Name: "lines", Flag: "--lines", Type: "int", Required: false, Default: "1000", Description: "Max scrollback lines captured per pane"},
			{Name: "grep-list-only", Flag: "--grep-list-only", Type: "bool", Required: false, Default: "false", Description: "Return only matching session/pane identifiers without match bodies"},
		},
		Examples: []string{
			"ntm --robot-grep='panic:|FAIL' --grep-session=myproject",
			"ntm --robot-grep='TODO' --all --type=cc,cod --grep-context=2",
			"ntm --robot-grep='rate limit' --all --grep-ignore-case --grep-list-only",
		},
	}
}
