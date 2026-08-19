package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/persona"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const completionTimeout = 500 * time.Millisecond

func completeSessionArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterByPrefix(listSessions(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeSessionThenPane(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return filterByPrefix(listSessions(), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 1 {
		return filterByPrefix(listPaneIndexes(args[0]), toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeSessionSecondArg(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterByPrefix(listSessions(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeSessionColonPane(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	sessionPrefix := toComplete
	panePrefix := ""
	hasColon := strings.Contains(toComplete, ":")
	if idx := strings.Index(toComplete, ":"); idx >= 0 {
		sessionPrefix = toComplete[:idx]
		panePrefix = toComplete[idx+1:]
	}

	sessions := listSessions()
	if hasColon {
		var out []string
		for _, session := range sessions {
			if sessionPrefix != "" && !strings.HasPrefix(session, sessionPrefix) {
				continue
			}
			for _, pane := range listPaneIndexes(session) {
				if panePrefix == "" || strings.HasPrefix(pane, panePrefix) {
					out = append(out, fmt.Sprintf("%s:%s", session, pane))
				}
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}

	return filterByPrefix(sessions, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeProfileSwitchArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	session := sessionFromFlagOrSingle(cmd)
	switch len(args) {
	case 0:
		if session == "" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return filterByPrefix(listProfileSwitchAgentIDs(session), toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		if session == "" {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return filterByPrefix(listProfileNamesForSession(cmd.Context(), session), toComplete), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func completePaneIndexes(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	session := sessionFromArgsOrFlag(cmd, args)
	if session == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaSeparated(listSendPaneSelectors(session), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeSendPaneSelector(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	session := sessionFromArgsOrFlag(cmd, args)
	if session == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterByPrefix(listSendPaneSelectors(session), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeSendPaneSelectors(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	session := sessionFromArgsOrFlag(cmd, args)
	if session == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaSeparated(listSendPaneSelectors(session), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeEnsemblePresetNames(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return filterByPrefix(listEnsemblePresetNames(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeEnsemblePresetArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterByPrefix(listEnsemblePresetNames(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeModeIDsCommaSeparated(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeCommaSeparated(listReasoningModeIDs(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func sessionFromArgsOrFlag(cmd *cobra.Command, args []string) string {
	if session := sessionFromFlag(cmd); session != "" {
		return session
	}
	if len(args) > 0 {
		return args[0]
	}
	return singleSession()
}

func sessionFromFlagOrSingle(cmd *cobra.Command) string {
	if session := sessionFromFlag(cmd); session != "" {
		return session
	}
	return singleSession()
}

func sessionFromFlag(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	if flag := cmd.Flags().Lookup("session"); flag != nil {
		if value, err := cmd.Flags().GetString("session"); err == nil {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func singleSession() string {
	sessions := listSessions()
	if len(sessions) == 1 {
		return sessions[0]
	}
	return ""
}

func listSessions() []string {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return names
}

func listPaneIndexes(session string) []string {
	if session == "" {
		return nil
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(panes))
	for _, p := range panes {
		out = append(out, strconv.Itoa(p.Index))
	}
	sort.Strings(out)
	return out
}

func listSendPaneSelectors(session string) []string {
	if session == "" {
		return nil
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil
	}
	multiWindow := tmux.PanesSpanMultipleWindows(panes)
	out := make([]string, 0, len(panes)*2)
	for _, pane := range panes {
		out = append(out, tmux.PaneTargetKey(pane, multiWindow))
		if pane.ID != "" {
			out = append(out, pane.ID)
		}
	}
	sort.Strings(out)
	return out
}

func listProfileSwitchAgentIDs(session string) []string {
	if session == "" {
		return nil
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(panes))
	for _, p := range panes {
		if id, ok := profileSwitchCompletionAgentID(p); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func profileSwitchCompletionAgentID(p tmux.Pane) (string, bool) {
	if !supportsProfileSwitchAgentType(p.Type) {
		return "", false
	}
	return completionAgentID(p)
}

func completionAgentID(p tmux.Pane) (string, bool) {
	if p.NTMIndex <= 0 {
		return "", false
	}
	switch canonical := p.Type.Canonical(); canonical {
	case tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini, tmux.AgentAntigravity, tmux.AgentGrok, tmux.AgentCursor, tmux.AgentWindsurf, tmux.AgentAider, tmux.AgentOpencode, tmux.AgentOllama:
		return fmt.Sprintf("%s_%d", canonical, p.NTMIndex), true
	default:
		return "", false
	}
}

func listProfileNamesForSession(ctx context.Context, session string) []string {
	projectDir, err := resolveProfileSwitchProjectDirContext(ctx, session)
	if err != nil || projectDir == "" {
		return nil
	}
	registry, err := persona.LoadRegistry(projectDir)
	if err != nil {
		return nil
	}
	profiles := registry.List()
	out := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile != nil && profile.Name != "" {
			out = append(out, profile.Name)
		}
	}
	sort.Strings(out)
	return out
}

func listEnsemblePresetNames() []string {
	registry, err := ensemble.GlobalEnsembleRegistry()
	if err != nil || registry == nil {
		return nil
	}
	presets := registry.List()
	out := make([]string, 0, len(presets))
	for _, p := range presets {
		if strings.TrimSpace(p.Name) != "" {
			out = append(out, p.Name)
		}
	}
	sort.Strings(out)
	return out
}

func listReasoningModeIDs() []string {
	catalog, err := ensemble.GlobalCatalog()
	if err != nil || catalog == nil {
		return nil
	}
	modes := catalog.ListModes()
	out := make([]string, 0, len(modes))
	for _, m := range modes {
		if strings.TrimSpace(m.ID) != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

func filterByPrefix(options []string, prefix string) []string {
	if prefix == "" {
		return options
	}
	out := make([]string, 0, len(options))
	for _, opt := range options {
		if strings.HasPrefix(opt, prefix) {
			out = append(out, opt)
		}
	}
	return out
}

func completeCommaSeparated(options []string, toComplete string) []string {
	prefix := ""
	segment := toComplete
	if idx := strings.LastIndex(toComplete, ","); idx >= 0 {
		prefix = toComplete[:idx+1]
		segment = toComplete[idx+1:]
	}
	return prefixMatches(options, prefix, segment)
}

func prefixMatches(options []string, prefix, segment string) []string {
	out := make([]string, 0, len(options))
	for _, opt := range options {
		if segment == "" || strings.HasPrefix(opt, segment) {
			out = append(out, prefix+opt)
		}
	}
	return out
}
