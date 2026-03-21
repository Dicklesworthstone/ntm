package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/ensemble"
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

func completeAgentIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	session := sessionFromFlagOrSingle(cmd)
	if session == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return filterByPrefix(listAgentIDs(session), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completePaneIndexes(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	session := sessionFromArgsOrFlag(cmd, args)
	if session == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeCommaSeparated(listPaneIndexes(session), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeReadyBeadIDs(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeCommaSeparated(listReadyBeadIDs(), toComplete), cobra.ShellCompDirectiveNoFileComp
}

func completeOpenBeadIDs(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completeCommaSeparated(listBeadIDsByStatus([]string{"open", "in_progress"}), toComplete), cobra.ShellCompDirectiveNoFileComp
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

func completeTierValues(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	tiers := []string{"core", "advanced", "experimental", "all"}
	return filterByPrefix(tiers, toComplete), cobra.ShellCompDirectiveNoFileComp
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

func listAgentIDs(session string) []string {
	if session == "" {
		return nil
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(panes))
	for _, p := range panes {
		if p.NTMIndex <= 0 {
			continue
		}
		switch p.Type {
		case tmux.AgentClaude, tmux.AgentCodex, tmux.AgentGemini, tmux.AgentCursor, tmux.AgentWindsurf, tmux.AgentAider:
			ids = append(ids, fmt.Sprintf("%s_%d", p.Type, p.NTMIndex))
		}
	}
	sort.Strings(ids)
	return ids
}

func listReadyBeadIDs() []string {
	return listBeadIDsFromCommand("ready", "--json")
}

func listBeadIDsByStatus(statuses []string) []string {
	seen := make(map[string]struct{})
	for _, status := range statuses {
		if status == "" {
			continue
		}
		ids := listBeadIDsFromCommand("list", "--json", fmt.Sprintf("--status=%s", status))
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func listBeadIDsFromCommand(args ...string) []string {
	if _, err := exec.LookPath("br"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "br", args...)
	if wd, err := os.Getwd(); err == nil && wd != "" {
		cmd.Dir = wd
	}

	output, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return nil
	}

	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(output, &items); err != nil {
		return nil
	}

	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
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
