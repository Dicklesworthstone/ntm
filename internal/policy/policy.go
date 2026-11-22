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

// ResolveEffectivePath reports which policy file LoadOrDefault would actually
// read, and whether such a file exists. Writers MUST use it so an update
// lands on the file that is being enforced: targeting the home path
// unconditionally would shadow an existing project-local policy with a
// freshly written one, silently dropping its blocked/approval_required rules
// (bd-fresh-eyes-audit .30).
//
// When no policy file exists, the returned path is where a new one should be
// created and exists is false.
func ResolveEffectivePath() (path string, exists bool, err error) {
	return resolveEffectivePath(DefaultPolicyPath)
}

// ResolveEffectivePathForProject reports which policy file LoadForProject would
// read for projectDir, and whether such a file exists. When projectDir is empty,
// it falls back to ResolveEffectivePath. Non-empty whitespace-only strings are
// rejected to match LoadForProject and ResolveProjectDir.
func ResolveEffectivePathForProject(projectDir string) (path string, exists bool, err error) {
	if projectDir == "" {
		return ResolveEffectivePath()
	}
	if !filepath.IsAbs(projectDir) {
		return "", false, fmt.Errorf("policy project directory must be absolute")
	}
	info, err := os.Stat(projectDir)
	if err != nil {
		return "", false, fmt.Errorf("inspect policy project directory %q: %w", projectDir, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("policy project path %q is not a directory", projectDir)
	}
	return resolveEffectivePath(filepath.Join(projectDir, DefaultPolicyPath))
}

func resolveEffectivePath(projectPolicyPath string) (path string, exists bool, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve policy home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", false, fmt.Errorf("resolve policy home directory: home directory must be absolute")
	}
	homePath := filepath.Join(home, DefaultPolicyPath)
	if _, err := os.Stat(homePath); err == nil {
		return homePath, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect home policy: %w", err)
	}
	if _, err := os.Stat(projectPolicyPath); err == nil {
		return projectPolicyPath, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect project policy: %w", err)
	}
	return homePath, false, nil
}

// LoadOrDefault loads the effective policy for the current working directory,
// or returns the built-in policy when no configured policy exists.
func LoadOrDefault() (*Policy, error) {
	return loadOrDefaultAt(DefaultPolicyPath)
}

// LoadForProject loads the policy for an explicitly identified local project.
// Home policy retains precedence over project policy. The project must be an
// absolute existing directory; neither an invalid target nor a missing policy
// may redirect enforcement to the calling process's working directory.
func LoadForProject(projectDir string) (*Policy, error) {
	if !filepath.IsAbs(projectDir) {
		return nil, fmt.Errorf("policy project directory must be absolute")
	}
	info, err := os.Stat(projectDir)
	if err != nil {
		return nil, fmt.Errorf("inspect policy project directory %q: %w", projectDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("policy project path %q is not a directory", projectDir)
	}
	return loadOrDefaultAt(filepath.Join(projectDir, DefaultPolicyPath))
}

func loadOrDefaultAt(projectPolicyPath string) (*Policy, error) {
	path, exists, err := resolveEffectivePath(projectPolicyPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		// Return default policy with common dangerous patterns
		return DefaultPolicy(), nil
	}

	return Load(path)
}

// DefaultPolicy returns a sensible default policy for destructive command protection.
func DefaultPolicy() *Policy {
	const gitOpts = `(?:\s+(?:-[Cc](?:\S+|\s+(?:"[^"]*"|'[^']*'|\S+))|--git-dir(?:=(?:"[^"]*"|'[^']*'|\S+)|\s+(?:"[^"]*"|'[^']*'|\S+))|--work-tree(?:=(?:"[^"]*"|'[^']*'|\S+)|\s+(?:"[^"]*"|'[^']*'|\S+))|--namespace(?:=(?:"[^"]*"|'[^']*'|\S+)|\s+(?:"[^"]*"|'[^']*'|\S+))|--config-env(?:=(?:"[^"]*"|'[^']*'|\S+)|\s+(?:"[^"]*"|'[^']*'|\S+))|--no-pager|-P|-p|--paginate|--bare|--no-replace-objects|--no-optional-locks|--literal-pathspecs|--icase-pathspecs|--noglob-pathspecs))*`
	const rmArgNoDD = `(?:[^\s-]\S*|-[^\s-]\S*|--\S+)`
	const rmFlagR = `(?:-[a-zA-Z]*[rR][a-zA-Z]*|--recursive)`
	const rmFlagF = `(?:-[a-zA-Z]*f[a-zA-Z]*|--force)`
	const rmFlagRFCombined = `-[a-zA-Z]*(?:[rR][a-zA-Z]*f|f[a-zA-Z]*[rR])[a-zA-Z]*`
	const rmFlagPair = `(?:` + rmFlagRFCombined + `|` + rmFlagR + `(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagF + `|` + rmFlagF + `(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagR + `)`
	const rmRF = `\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagPair
	const rmCritTarget = `['"]?(?:/\*?|~(?:/\*?)?|\*|\.(?:/\*?)?|\.\.(?:/\*?)?)['"]?`
	const rmCritBlocked = `(?:` +
		rmRF + `(?:\s+\S+)*\s+` + rmCritTarget + `(?:\s|['")]|$)` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmCritTarget + `(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagPair + `(?:\s|['")]|$)` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagR + `(?:\s+` + rmArgNoDD + `)*\s+` + rmCritTarget + `(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagF + `(?:\s|['")]|$)` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagF + `(?:\s+` + rmArgNoDD + `)*\s+` + rmCritTarget + `(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagR + `(?:\s|['")]|$)` +
		`)`
	const rmApprove = `(?:` +
		rmRF + `\s+\S` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+[^-\s]\S*(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagPair + `(?:\s|['")]|$)` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagR + `(?:\s+` + rmArgNoDD + `)*\s+[^-\s]\S*(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagF + `(?:\s|['")]|$)` +
		`|\brm(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagF + `(?:\s+` + rmArgNoDD + `)*\s+[^-\s]\S*(?:\s+` + rmArgNoDD + `)*\s+` + rmFlagR + `(?:\s|['")]|$)` +
		`)`
	p := &Policy{
		Version: 1,
		// Automation defaults
		Automation: AutomationConfig{
			AutoPush:     false,      // Require explicit push
			AutoCommit:   true,       // Allow auto-commit
			ForceRelease: "approval", // Require approval for force release
		},
		// Allowed patterns checked FIRST per subcommand - explicitly safe commands
		Allowed: []Rule{
			{Pattern: `^git` + gitOpts + `\s+push\s+.*--force-with-lease(\b|=)`, Reason: "Safe force push (prevents overwriting others' work)"},
			{Pattern: `^git` + gitOpts + `\s+reset\s+--soft(\s|$)`, Reason: "Soft reset preserves changes"},
			{Pattern: `^git` + gitOpts + `\s+reset\s+HEAD~?\d*$`, Reason: "Mixed reset preserves working directory"},
		},
		// Blocked patterns - dangerous commands (checked after allowed per subcommand)
		Blocked: []Rule{
			{Pattern: `git` + gitOpts + `\s+reset(\s+.*?)?\s--hard(\s|['")]|$)`, Reason: "Hard reset loses uncommitted changes"},
			{Pattern: `git` + gitOpts + `\s+clean(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|['")]|$)`, Reason: "Removes untracked files permanently"},
			{Pattern: `git` + gitOpts + `\s+push(\s+.*?)?\s(-[\w]*f[\w]*|--force)(\s|['")]|$)`, Reason: "Force push can overwrite remote history"},
			{Pattern: `git` + gitOpts + `\s+push(\s+.*?)?\s['"]?(\+|:\+)`, Reason: "Force push via +refspec can overwrite remote history"},
			{Pattern: rmCritBlocked, Reason: "Critical recursive deletion"},
			{Pattern: `git` + gitOpts + `\s+branch(\s+.*?)?\s(-[a-zA-Z]*D[a-zA-Z]*|-[a-zA-Z]*(?:d[a-zA-Z]*f|f[a-zA-Z]*d)[a-zA-Z]*|(?:--delete|-[a-zA-Z]*d[a-zA-Z]*)\s+(?:.*\s)?(?:--force|-[a-zA-Z]*f[a-zA-Z]*)|(?:--force|-[a-zA-Z]*f[a-zA-Z]*)\s+(?:.*\s)?(?:--delete|-[a-zA-Z]*d[a-zA-Z]*))(\s|['")]|$)`, Reason: "Force delete branch loses unmerged work"},
			{Pattern: `git` + gitOpts + `\s+stash\s+(drop|clear)(\s|['")]|$)`, Reason: "Losing stashed work"},
		},
		// Approval required - potentially dangerous commands
		ApprovalRequired: []Rule{
			{Pattern: `git` + gitOpts + `\s+rebase(\s+.*?)?\s(-[a-zA-Z]*i[a-zA-Z]*|--interactive)(\s|['")]|$)`, Reason: "Interactive rebase rewrites history"},
			{Pattern: `git` + gitOpts + `\s+commit(\s+.*?)?\s--amend(\s|['")]|$)`, Reason: "Amending rewrites history"},
			{Pattern: rmApprove, Reason: "Recursive force delete"},
			{Pattern: `force_release`, Reason: "Force release another agent's reservation", SLB: true},
		},
	}
	// Compile patterns; ignore errors for default policy as these are hardcoded
	// and should always be valid. Any patterns that fail will just not match.
	_ = p.compile()
	return p
}

// DefaultPolicyYAML renders DefaultPolicy() as YAML so CLI/REST policy reset
// and install paths stay locked to the hardened built-in ruleset.
func DefaultPolicyYAML() string {
	return DefaultPolicy().FormatYAML()
}

// FormatYAML serializes the policy as YAML using single-quoted regex patterns
// so backslashes round-trip through policy.Load unchanged.
func (p *Policy) FormatYAML() string {
	var sb strings.Builder

	sb.WriteString("# NTM Policy Configuration\n")
	sb.WriteString(fmt.Sprintf("version: %d\n\n", p.Version))

	sb.WriteString("automation:\n")
	sb.WriteString(fmt.Sprintf("  auto_commit: %v\n", p.Automation.AutoCommit))
	sb.WriteString(fmt.Sprintf("  auto_push: %v\n", p.Automation.AutoPush))
	sb.WriteString(fmt.Sprintf("  force_release: %s\n\n", p.ForceReleasePolicy()))

	writeRules := func(header string, rules []Rule, includeSLB bool) {
		if len(rules) == 0 {
			return
		}
		sb.WriteString(header + ":\n")
		for _, r := range rules {
			escapedPattern := strings.ReplaceAll(r.Pattern, "'", "''")
			sb.WriteString(fmt.Sprintf("  - pattern: '%s'\n", escapedPattern))
			if r.Reason != "" {
				escapedReason := strings.ReplaceAll(strings.ReplaceAll(r.Reason, `\`, `\\`), `"`, `\"`)
				sb.WriteString(fmt.Sprintf("    reason: \"%s\"\n", escapedReason))
			}
			if includeSLB && r.SLB {
				sb.WriteString("    slb: true\n")
			}
		}
		sb.WriteString("\n")
	}

	writeRules("allowed", p.Allowed, false)
	writeRules("blocked", p.Blocked, false)
	writeRules("approval_required", p.ApprovalRequired, true)

	return sb.String()
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

var (
	gitPushUnconditionalForceRE = regexp.MustCompile(`\s(-[\w]*f[\w]*|--force|--no-force-with-lease)(\s|['")]|$)`)
	gitPushOptionLeaseRE        = regexp.MustCompile(`\s(?:-o(?:=|\s*)|--push-option(?:=|\s+))['"]?--force-with-lease(\b|=)`)
	gitResetHardRE              = regexp.MustCompile(`\s--hard(\s|['")]|$)`)
	simpleWordQuoteRE           = regexp.MustCompile(`(^|[\s(])(?:'([^'\s\\$` + "`" + `;|&()]+)'|"([^"\s\\$` + "`" + `;|&()]+)")($|[\s)])`)
)

// splitShellSubcommands splits a compound shell command on unquoted command
// separators (&&, ||, ;, |, &, newline), strips unquoted shell comments (# ...),
// handles ANSI-C quoting ($'...'), and extracts nested command substitutions
// ($(...) and `...`) so an allowed prefix or wrapper cannot smuggle a
// destructive command past policy enforcement.
func splitShellSubcommands(command string) []string {
	return splitShellSubcommandsDepth(command, 0)
}

func splitShellSubcommandsDepth(command string, depth int) []string {
	var parts []string
	var nested []string
	var cur strings.Builder
	inSingle := false
	inANSIC := false
	inDouble := false
	escaped := false

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			parts = append(parts, s)
		}
		cur.Reset()
	}

	n := len(command)
	for i := 0; i < n; i++ {
		ch := command[i]
		if escaped {
			cur.WriteByte(ch)
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			cur.WriteByte(ch)
			continue
		}
		if inANSIC {
			if ch == '\\' && i+1 < n {
				cur.WriteByte(ch)
				cur.WriteByte(command[i+1])
				i++
				continue
			}
			if ch == '\'' {
				inANSIC = false
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '\\' {
			escaped = true
			cur.WriteByte(ch)
			continue
		}
		// Command substitutions $(...) and `...` are evaluated both unquoted
		// and inside double quotes; process substitutions <(...) and >(...)
		// are evaluated when unquoted.
		if (ch == '$' || (!inDouble && (ch == '<' || ch == '>'))) && i+1 < n && command[i+1] == '(' {
			end := findMatchingParen(command, i+1)
			if end > i+1 {
				inner := command[i+2 : end]
				if depth < 8 {
					nested = append(nested, splitShellSubcommandsDepth(inner, depth+1)...)
				}
				cur.WriteString(command[i : end+1])
				i = end
				continue
			}
		}
		if ch == '`' {
			end := findMatchingBacktick(command, i)
			if end > i {
				inner := unescapeBacktickBody(command[i+1 : end])
				if depth < 8 {
					nested = append(nested, splitShellSubcommandsDepth(inner, depth+1)...)
				}
				cur.WriteString(command[i : end+1])
				i = end
				continue
			}
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
			}
			cur.WriteByte(ch)
			continue
		}
		if ch == '$' && i+1 < n && command[i+1] == '\'' {
			inANSIC = true
			cur.WriteString("$'")
			i++
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
			cur.WriteByte(ch)
		case '"':
			inDouble = true
			cur.WriteByte(ch)
		case '#':
			// POSIX shell treats '#' as a comment start when at the start of a word.
			if cur.Len() == 0 || i == 0 || command[i-1] == ' ' || command[i-1] == '\t' || command[i-1] == '\n' || command[i-1] == ';' || command[i-1] == '|' || command[i-1] == '&' {
				for i < n && command[i] != '\n' {
					i++
				}
				flush()
				continue
			}
			cur.WriteByte(ch)
		case ';', '\n':
			flush()
		case '|':
			if i+1 < n && command[i+1] == '|' {
				i++
			}
			flush()
		case '&':
			if i+1 < n && command[i+1] == '&' {
				i++
				flush()
				continue
			}
			// Split standalone async '&' unless it is part of a redirection (>&, <&, &>).
			prev := byte(0)
			if i > 0 {
				prev = command[i-1]
			}
			next := byte(0)
			if i+1 < n {
				next = command[i+1]
			}
			if prev != '>' && prev != '<' && next != '>' {
				flush()
				continue
			}
			cur.WriteByte(ch)
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	if len(nested) > 0 {
		parts = append(parts, nested...)
	}
	return parts
}

func findMatchingParen(s string, openIdx int) int {
	depth := 0
	inSingle := false
	inANSIC := false
	inDouble := false
	escaped := false
	// Stack of whether each open paren was entered from inside double quotes
	// (via "$(...)"), so nested substitutions restore quoting state in O(N)
	// without exponential recursive backtracking on unclosed "$( sequences.
	inDoubleStack := make([]bool, 0, 8)
	for i := openIdx; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		if inANSIC {
			if ch == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if ch == '\'' {
				inANSIC = false
			}
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inDouble {
			if ch == '$' && i+1 < len(s) && s[i+1] == '(' {
				inDoubleStack = append(inDoubleStack, true)
				inDouble = false
				depth++
				i++
				continue
			}
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		if ch == '$' && i+1 < len(s) && s[i+1] == '\'' {
			inANSIC = true
			i++
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '#':
			// POSIX shell treats '#' as a comment start when at the start of a word;
			// skip through newline so quotes inside comments cannot desync matching.
			if i == openIdx+1 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\n' || s[i-1] == ';' || s[i-1] == '|' || s[i-1] == '&' || s[i-1] == '(' {
				for i < len(s) && s[i] != '\n' {
					i++
				}
			}
		case '(':
			inDoubleStack = append(inDoubleStack, false)
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
			if n := len(inDoubleStack); n > 0 {
				inDouble = inDoubleStack[n-1]
				inDoubleStack = inDoubleStack[:n-1]
			}
		}
	}
	return -1
}

func findMatchingBacktick(s string, openIdx int) int {
	escaped := false
	for i := openIdx + 1; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '`' {
			return i
		}
	}
	return -1
}

// unescapeBacktickBody applies POSIX backtick backslash removal (\`, \$, \\)
// before recursively analyzing nested command substitutions.
func unescapeBacktickBody(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '`', '$', '\\':
				b.WriteByte(s[i+1])
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hasShellSubstitution reports whether cmd contains unquoted or double-quoted
// command/process substitution tokens ($(, `, <(, >(). Allowed rules must not
// short-circuit a subcommand carrying embedded shell execution.
func hasShellSubstitution(cmd string) bool {
	inSingle := false
	inANSIC := false
	inDouble := false
	escaped := false
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		if inANSIC {
			if ch == '\\' && i+1 < len(cmd) {
				i++
				continue
			}
			if ch == '\'' {
				inANSIC = false
			}
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '`' {
			return true
		}
		if ch == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
			return true
		}
		if !inDouble && (ch == '<' || ch == '>') && i+1 < len(cmd) && cmd[i+1] == '(' {
			return true
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		if ch == '$' && i+1 < len(cmd) && cmd[i+1] == '\'' {
			inANSIC = true
			i++
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		}
	}
	return false
}

func stripSimpleWordQuotes(cmd string) string {
	// Run twice so adjacent quoted tokens sharing a space boundary (e.g.
	// "'rm' '-rf' '/'") are all unwrapped without touching multi-word strings.
	out := cmd
	for range 2 {
		next := simpleWordQuoteRE.ReplaceAllStringFunc(out, func(m string) string {
			sub := simpleWordQuoteRE.FindStringSubmatch(m)
			if len(sub) < 5 {
				return m
			}
			word := sub[2]
			if word == "" {
				word = sub[3]
			}
			return sub[1] + word + sub[4]
		})
		if next == out {
			break
		}
		out = next
	}
	return out
}

func (p *Policy) checkSingle(cmd, fullCmd string) *Match {
	norm := stripSimpleWordQuotes(cmd)

	// Check allowed first (explicit allowlist takes precedence per subcommand),
	// unless the subcommand embeds command/process substitution that could hide
	// destructive shell code (e.g. case pattern ')' or unbalanced quotes).
	if !hasShellSubstitution(cmd) {
		for _, rule := range p.Allowed {
			if rule.regex != nil && rule.regex.MatchString(cmd) {
				// Guard against `git push --force-with-lease --force` (or `-4f`,
				// `--no-force-with-lease`, or `-o --force-with-lease`, even when
				// flags are single-word quoted), where git honors unconditional
				// force or treats --force-with-lease as a push option value.
				if strings.Contains(rule.Pattern, "force-with-lease") &&
					(gitPushUnconditionalForceRE.MatchString(cmd) || gitPushUnconditionalForceRE.MatchString(norm) ||
						gitPushOptionLeaseRE.MatchString(cmd) || gitPushOptionLeaseRE.MatchString(norm)) {
					continue
				}
				// Guard against `git reset --soft --hard HEAD` (raw or quoted),
				// where git honors the later --hard flag.
				if strings.Contains(rule.Pattern, "reset") && strings.Contains(rule.Pattern, "--soft") &&
					(gitResetHardRE.MatchString(cmd) || gitResetHardRE.MatchString(norm)) {
					continue
				}
				return &Match{
					Action:  ActionAllow,
					Pattern: rule.Pattern,
					Reason:  rule.Reason,
					Command: fullCmd,
				}
			}
		}
	}

	// Check blocked patterns (raw and quote-unwrapped single-word tokens).
	for _, rule := range p.Blocked {
		if rule.regex != nil && (rule.regex.MatchString(cmd) || (norm != cmd && rule.regex.MatchString(norm))) {
			return &Match{
				Action:  ActionBlock,
				Pattern: rule.Pattern,
				Reason:  rule.Reason,
				Command: fullCmd,
			}
		}
	}

	// Check approval required patterns (raw and quote-unwrapped single-word tokens).
	for _, rule := range p.ApprovalRequired {
		if rule.regex != nil && (rule.regex.MatchString(cmd) || (norm != cmd && rule.regex.MatchString(norm))) {
			return &Match{
				Action:  ActionApprove,
				Pattern: rule.Pattern,
				Reason:  rule.Reason,
				Command: fullCmd,
				SLB:     rule.SLB,
			}
		}
	}

	return nil
}

// Check evaluates a command against the policy and returns a match if found.
// Returns nil if the command is not matched by any rule (implicitly allowed).
// For compound commands (&&, ||, ;, |, newlines), every subcommand is checked
// independently and the most restrictive action wins (block > approve > allow).
func (p *Policy) Check(command string) *Match {
	// Normalize command for matching
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return nil
	}

	subcommands := splitShellSubcommands(cmd)
	if len(subcommands) == 0 {
		return nil
	}
	if len(subcommands) == 1 {
		return p.checkSingle(subcommands[0], cmd)
	}

	var firstApprove *Match
	var firstAllow *Match
	for _, sub := range subcommands {
		m := p.checkSingle(sub, cmd)
		if m == nil {
			continue
		}
		switch m.Action {
		case ActionBlock:
			return m
		case ActionApprove:
			if firstApprove == nil || (m.SLB && !firstApprove.SLB) {
				firstApprove = m
			}
		case ActionAllow:
			if firstAllow == nil {
				firstAllow = m
			}
		}
	}
	if firstApprove != nil {
		return firstApprove
	}
	return firstAllow
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
