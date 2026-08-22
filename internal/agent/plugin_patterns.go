package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/util"
)

// PluginPatterns are the readiness patterns an agent plugin (a user-defined
// agent type loaded from <config>/agents/*.toml) declares so NTM's status,
// robot, and --verify-boot surfaces classify its panes the way they classify
// built-in agents (ntm#260). All three are optional:
//
//   - Working: any match in the live tail means a turn is in flight and
//     vetoes idle (e.g. OMP's `⟨esc⟩` hint, OpenCode's `esc interrupt`).
//   - Idle: any match in the last few lines, with no Working match, means
//     the composer is waiting for input.
//   - Error: any match in the recent output marks the pane as errored.
//
// Patterns are Go regexps matched against ANSI-stripped pane output.
type PluginPatterns struct {
	Idle    []*regexp.Regexp
	Working []*regexp.Regexp
	Error   []*regexp.Regexp
}

// Declared reports whether the plugin supplied at least one pattern of any
// kind; a plugin with none falls back to the generic heuristics.
func (p PluginPatterns) Declared() bool {
	return len(p.Idle) > 0 || len(p.Working) > 0 || len(p.Error) > 0
}

var (
	pluginPatternsMu sync.RWMutex
	pluginPatterns   = map[AgentType]PluginPatterns{}
)

// pluginLiveTailLines bounds the live-tail window scanned for a plugin's
// working veto, matching the codex/grok budget.
const pluginLiveTailLines = 15

func pluginKey(name string) AgentType {
	return AgentType(strings.ToLower(strings.TrimSpace(name)))
}

// RegisterPlugin records name as a plugin agent type together with its
// readiness patterns. Registering the same name again replaces the previous
// patterns. An empty name is rejected; an invalid regexp is rejected with the
// offending pattern named, and nothing from that call is registered.
func RegisterPlugin(name string, idle, working, errs []string) error {
	key := pluginKey(name)
	if key == "" {
		return fmt.Errorf("plugin agent type must not be empty")
	}
	compile := func(kind string, raw []string) ([]*regexp.Regexp, error) {
		out := make([]*regexp.Regexp, 0, len(raw))
		for _, r := range raw {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			re, err := regexp.Compile(r)
			if err != nil {
				return nil, fmt.Errorf("plugin %s: invalid %s pattern %q: %w", key, kind, r, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	var pp PluginPatterns
	var err error
	if pp.Idle, err = compile("idle", idle); err != nil {
		return err
	}
	if pp.Working, err = compile("working", working); err != nil {
		return err
	}
	if pp.Error, err = compile("error", errs); err != nil {
		return err
	}
	pluginPatternsMu.Lock()
	pluginPatterns[key] = pp
	pluginPatternsMu.Unlock()
	return nil
}

// UnregisterPlugins forgets every registered plugin (tests).
func UnregisterPlugins() {
	pluginPatternsMu.Lock()
	pluginPatterns = map[AgentType]PluginPatterns{}
	pluginPatternsMu.Unlock()
}

// IsPluginType reports whether t names a registered plugin agent type.
func IsPluginType(t AgentType) bool {
	_, ok := LookupPluginPatterns(t)
	return ok
}

// LookupPluginPatterns returns the readiness patterns registered for t.
func LookupPluginPatterns(t AgentType) (PluginPatterns, bool) {
	pluginPatternsMu.RLock()
	defer pluginPatternsMu.RUnlock()
	pp, ok := pluginPatterns[pluginKey(string(t))]
	return pp, ok
}

// matchAnyLine reports whether any single line of text matches any pattern.
// Plugin patterns are documented as per-line regexps, so `^` and `$` anchor a
// screen line without needing the (?m) flag.
func matchAnyLine(text string, patterns []*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, line := range strings.Split(stripANSICodes(text), "\n") {
		line = strings.TrimRight(line, "\r")
		for _, re := range patterns {
			if re.MatchString(line) {
				return true
			}
		}
	}
	return false
}

// PluginActivelyWorking reports whether any line of a registered plugin
// pane's trailing live window matches one of its declared Working patterns.
// False for unregistered types and for plugins that declared no Working
// patterns.
func PluginActivelyWorking(output string, t AgentType, paneWidth int) bool {
	pp, ok := LookupPluginPatterns(t)
	if !ok || len(pp.Working) == 0 {
		return false
	}
	tail := util.GetLastNLines(output, util.WidthAdaptiveTailLines(paneWidth, pluginLiveTailLines))
	return matchAnyLine(tail, pp.Working)
}

// PluginIdlePromptShowing reports whether any line of lastLines (already
// bounded by the caller) matches one of the plugin's declared Idle patterns.
// The second result is false when the plugin declared no Idle patterns, so
// callers can fall back to generic prompt heuristics.
func PluginIdlePromptShowing(lastLines string, t AgentType) (idle bool, declared bool) {
	pp, ok := LookupPluginPatterns(t)
	if !ok || len(pp.Idle) == 0 {
		return false, false
	}
	return matchAnyLine(lastLines, pp.Idle), true
}

// PluginErrorShowing reports whether any line of recentOutput matches one of
// the plugin's declared Error patterns; false when none are declared.
func PluginErrorShowing(recentOutput string, t AgentType) bool {
	pp, ok := LookupPluginPatterns(t)
	if !ok || len(pp.Error) == 0 {
		return false
	}
	return matchAnyLine(recentOutput, pp.Error)
}
