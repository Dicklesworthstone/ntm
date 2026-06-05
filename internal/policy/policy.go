// Package policy provides destructive command protection through pattern matching.
package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPolicyPath is the default location for the policy file.
const DefaultPolicyPath = ".ntm/policy.yaml"

// Action represents what should happen when a command matches a pattern.
type Action string

const (
	ActionBlock   Action = "block"
	ActionApprove Action = "approve" // requires approval
	ActionAllow   Action = "allow"
)

// Rule represents a single policy rule.
type Rule struct {
	Pattern string `yaml:"pattern"`
	Reason  string `yaml:"reason,omitempty"`
	SLB     bool   `yaml:"slb,omitempty"` // Requires SLB two-person approval
	regex   *regexp.Regexp
}

// AutomationConfig controls automatic operations.
type AutomationConfig struct {
	AutoPush     bool   `yaml:"auto_push"`     // Allow automatic git push
	AutoCommit   bool   `yaml:"auto_commit"`   // Allow automatic git commit
	ForceRelease string `yaml:"force_release"` // "never", "approval", "auto"
}

// Policy represents the complete policy configuration.
type Policy struct {
	Version          int              `yaml:"version"`
	Blocked          []Rule           `yaml:"blocked"`
	ApprovalRequired []Rule           `yaml:"approval_required"`
	Allowed          []Rule           `yaml:"allowed"`
	Automation       AutomationConfig `yaml:"automation"`
}

// Match represents a matched policy rule.
type Match struct {
	Action  Action
	Pattern string
	Reason  string
	Command string
	SLB     bool // Whether this match requires SLB approval
}

// DecodeYAML decodes policy YAML strictly, rejecting unknown fields.
func DecodeYAML(data []byte) (*Policy, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return &Policy{}, nil
	}

	var p Policy
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		if err == io.EOF {
			return &Policy{}, nil
		}
		return nil, fmt.Errorf("parsing policy file: %w", err)
	}

	return &p, nil
}

// Load reads and parses a policy file from the given path.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading policy file: %w", err)
	}

	p, err := DecodeYAML(data)
	if err != nil {
		return nil, err
	}

	if err := p.Validate(); err != nil {
		return nil, err
	}

	return p, nil
}

// LoadOrDefault loads the policy from the default path, or returns an empty policy if not found.
func LoadOrDefault() (*Policy, error) {
	path := DefaultPolicyPath

	// Try home directory first, then current directory
	if home, err := os.UserHomeDir(); err == nil {
		homePath := filepath.Join(home, DefaultPolicyPath)
		if _, err := os.Stat(homePath); err == nil {
			path = homePath
		}
	}

	// Check current directory
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Return default policy with common dangerous patterns
		return DefaultPolicy(), nil
	}

	return Load(path)
}

// DefaultPolicy returns a sensible default policy for destructive command protection.
func DefaultPolicy() *Policy {
	p := &Policy{
		Version: 1,
		// Automation defaults
		Automation: AutomationConfig{
			AutoPush:     false,      // Require explicit push
			AutoCommit:   true,       // Allow auto-commit
			ForceRelease: "approval", // Require approval for force release
		},
		// Allowed patterns checked FIRST - explicitly safe commands
		Allowed: []Rule{
			{Pattern: `git\s+push\s+.*--force-with-lease`, Reason: "Safe force push (prevents overwriting others' work)"},
			{Pattern: `git\s+reset\s+--soft`, Reason: "Soft reset preserves changes"},
			{Pattern: `git\s+reset\s+HEAD~?\d*$`, Reason: "Mixed reset preserves working directory"},
		},
		// Blocked patterns - dangerous commands (checked after allowed)
		Blocked: []Rule{
			{Pattern: `git\s+reset\s+--hard`, Reason: "Hard reset loses uncommitted changes"},
			{Pattern: `git\s+clean(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|$)`, Reason: "Removes untracked files permanently"},
			{Pattern: `git\s+push(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|$)`, Reason: "Force push can overwrite remote history"},
			{Pattern: `git\s+push(\s+.*?)?\s(\+|:\+)`, Reason: "Force push via +refspec can overwrite remote history"},
			{Pattern: `rm\s+-rf\s+(/|~|\*|\.|\.\.)(/|\s|$)`, Reason: "Critical recursive deletion"},
			{Pattern: `git\s+branch\s+-D`, Reason: "Force delete branch loses unmerged work"},
			{Pattern: `git\s+stash\s+(drop|clear)`, Reason: "Losing stashed work"},
		},
		// Approval required - potentially dangerous commands
		ApprovalRequired: []Rule{
			{Pattern: `git\s+rebase(\s+.*?)?\s(-i|--interactive)`, Reason: "Interactive rebase rewrites history"},
			{Pattern: `git\s+commit\s+--amend`, Reason: "Amending rewrites history"},
			{Pattern: `rm\s+-rf\s+\S`, Reason: "Recursive force delete"},
			{Pattern: `force_release`, Reason: "Force release another agent's reservation", SLB: true},
		},
	}
	// Compile patterns; ignore errors for default policy as these are hardcoded
	// and should always be valid. Any patterns that fail will just not match.
	_ = p.compile()
	return p
}

// ToYAML returns the policy serialized as YAML.
func (p *Policy) ToYAML() (string, error) {
	data, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// compile compiles all regex patterns in the policy.
// compile compiles every regex pattern in the policy, attempting each
// rule even when earlier rules fail. Failures are joined via
// errors.Join and returned together. Rules whose pattern compiled
// successfully have their regex set; rules that failed have a nil
// regex and are skipped by Check (which silently treats them as
// non-matching). Returning all failures at once means a typo in
// Blocked[0] does not silently disable Blocked[1..N] for the caller.
func (p *Policy) compile() error {
	var errs error

	for i := range p.Blocked {
		// Always reset compiled state first so a recompile after policy edits
		// cannot keep matching an old regex when the new pattern is invalid.
		p.Blocked[i].regex = nil
		re, err := regexp.Compile(p.Blocked[i].Pattern)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("invalid blocked pattern %q: %w", p.Blocked[i].Pattern, err))
			continue
		}
		p.Blocked[i].regex = re
	}

	for i := range p.ApprovalRequired {
		p.ApprovalRequired[i].regex = nil
		re, err := regexp.Compile(p.ApprovalRequired[i].Pattern)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("invalid approval_required pattern %q: %w", p.ApprovalRequired[i].Pattern, err))
			continue
		}
		p.ApprovalRequired[i].regex = re
	}

	for i := range p.Allowed {
		p.Allowed[i].regex = nil
		re, err := regexp.Compile(p.Allowed[i].Pattern)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("invalid allowed pattern %q: %w", p.Allowed[i].Pattern, err))
			continue
		}
		p.Allowed[i].regex = re
	}

	return errs
}

// Check evaluates a command against the policy and returns a match if found.
// Returns nil if the command is not matched by any rule (implicitly allowed).
// Order of precedence: allowed > blocked > approval_required
func (p *Policy) Check(command string) *Match {
	// Normalize command for matching
	cmd := strings.TrimSpace(command)

	// Check allowed first (explicit allowlist takes precedence)
	for _, rule := range p.Allowed {
		if rule.regex != nil && rule.regex.MatchString(cmd) {
			return &Match{
				Action:  ActionAllow,
				Pattern: rule.Pattern,
				Reason:  rule.Reason,
				Command: cmd,
			}
		}
	}

	// Check blocked patterns
	for _, rule := range p.Blocked {
		if rule.regex != nil && rule.regex.MatchString(cmd) {
			return &Match{
				Action:  ActionBlock,
				Pattern: rule.Pattern,
				Reason:  rule.Reason,
				Command: cmd,
			}
		}
	}

	// Check approval required patterns
	for _, rule := range p.ApprovalRequired {
		if rule.regex != nil && rule.regex.MatchString(cmd) {
			return &Match{
				Action:  ActionApprove,
				Pattern: rule.Pattern,
				Reason:  rule.Reason,
				Command: cmd,
				SLB:     rule.SLB,
			}
		}
	}

	// No match - implicitly allowed
	return nil
}

// IsBlocked returns true if the command matches a blocked pattern.
func (p *Policy) IsBlocked(command string) bool {
	match := p.Check(command)
	return match != nil && match.Action == ActionBlock
}

// NeedsApproval returns true if the command requires approval.
func (p *Policy) NeedsApproval(command string) bool {
	match := p.Check(command)
	return match != nil && match.Action == ActionApprove
}

// Stats returns counts of rules by type.
func (p *Policy) Stats() (blocked, approval, allowed int) {
	return len(p.Blocked), len(p.ApprovalRequired), len(p.Allowed)
}

// NeedsSLBApproval returns true if the action requires SLB two-person approval.
func (p *Policy) NeedsSLBApproval(action string) bool {
	match := p.Check(action)
	return match != nil && match.Action == ActionApprove && match.SLB
}

// AutomationEnabled checks if a specific automation feature is enabled.
func (p *Policy) AutomationEnabled(feature string) bool {
	switch feature {
	case "auto_push":
		return p.Automation.AutoPush
	case "auto_commit":
		return p.Automation.AutoCommit
	default:
		return false
	}
}

// ForceReleasePolicy returns the force release policy: "never", "approval", or "auto".
func (p *Policy) ForceReleasePolicy() string {
	if p.Automation.ForceRelease == "" {
		return "approval" // Default to requiring approval
	}
	return p.Automation.ForceRelease
}

// Validate checks the policy for errors.
func (p *Policy) Validate() error {
	// Validate version
	if p.Version < 1 {
		p.Version = 1 // Default to version 1
	}

	// Validate force_release value
	switch p.Automation.ForceRelease {
	case "", "never", "approval", "auto":
		// Valid values
	default:
		return fmt.Errorf("invalid force_release value: %q (must be never, approval, or auto)", p.Automation.ForceRelease)
	}

	// Compile patterns to validate them
	return p.compile()
}
