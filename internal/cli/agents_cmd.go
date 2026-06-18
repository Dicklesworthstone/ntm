package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agents"
	"github.com/Dicklesworthstone/ntm/internal/output"
)

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage agent profiles and capabilities",
		Long: `Manage agent capability profiles for intelligent task assignment.

Agent profiles define the capabilities, specializations, and preferences
of different AI agents (Claude, Codex, Gemini). Use these commands to:
  - View available agent profiles
  - Check agent performance statistics
  - Get recommendations for task assignment

Subcommands:
  list      List all agent profiles
  show      Show details of a specific agent profile
  stats     Show performance statistics for agents
  recommend Recommend the best agent for a task

Examples:
  ntm agents list                           # List all profiles
  ntm agents show claude                    # Show Claude's profile
  ntm agents stats                          # Performance stats
  ntm agents recommend --title "Fix bug"    # Get recommendation`,
	}

	cmd.AddCommand(newAgentsListCmd())
	cmd.AddCommand(newAgentsShowCmd())
	cmd.AddCommand(newAgentsStatsCmd())
	cmd.AddCommand(newAgentsRecommendCmd())

	return cmd
}

func newAgentsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"profiles", "ls"},
		Short:   "List all agent profiles",
		Long: `List all available agent profiles with their key capabilities.

Shows each agent's type, model, context budget, and specializations.

Examples:
  ntm agents list
  ntm agents profiles
  ntm agents list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentsList()
		},
	}

	return cmd
}

func newAgentsShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show <agent-type>",
		Aliases: []string{"profile", "get"},
		Short:   "Show details of a specific agent profile",
		Long: `Show detailed information about a specific agent profile.

Agent types: claude, codex, gemini
Aliases are supported (cc for claude, cod for codex, gmi for gemini).

Examples:
  ntm agents show claude
  ntm agents show cc
  ntm agents profile codex
  ntm agents show gemini --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentsShow(args[0])
		},
	}

	return cmd
}

func newAgentsStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show performance statistics for agents",
		Long: `Show performance statistics for all agents.

Displays:
  - Tasks completed
  - Success rate
  - Average completion time
  - Last activity timestamp

Examples:
  ntm agents stats
  ntm agents stats --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentsStats()
		},
	}

	return cmd
}

func newAgentsRecommendCmd() *cobra.Command {
	var title string
	var taskType string
	var files []string
	var labels []string
	var estimatedTokens int

	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Recommend the best agent for a task",
		Long: `Recommend the best agent for a given task based on capabilities.

The recommendation considers:
  - Task type and complexity
  - Affected files (patterns matched against agent preferences)
  - Labels/tags
  - Estimated context size

Examples:
  ntm agents recommend --title "Fix auth bug"
  ntm agents recommend --title "Write tests" --type task
  ntm agents recommend --title "Refactor API" --files "internal/api/*.go"
  ntm agents recommend --title "Epic" --type epic --tokens 150000`,
		RunE: func(cmd *cobra.Command, args []string) error {
			task := agents.TaskInfo{
				Title:           title,
				Type:            taskType,
				AffectedFiles:   files,
				Labels:          labels,
				EstimatedTokens: estimatedTokens,
			}
			return runAgentsRecommend(task)
		},
	}

	cmd.Flags().StringVarP(&title, "title", "t", "", "task title (required)")
	cmd.Flags().StringVar(&taskType, "type", "task", "task type: task, bug, feature, epic, docs")
	cmd.Flags().StringSliceVarP(&files, "files", "f", nil, "affected file patterns")
	cmd.Flags().StringSliceVarP(&labels, "labels", "l", nil, "task labels")
	cmd.Flags().IntVar(&estimatedTokens, "tokens", 0, "estimated token count")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

// effectiveAgentModel resolves the model an agent of the given profile type
// will actually be spawned with, honoring active config overrides (the same
// `cfg.Models.GetModelName(type, "")` chain the spawn launcher uses). It falls
// back to the profile's baked-in default only when config resolution yields
// nothing (e.g. an unmapped agent type or no loaded config). Without this,
// `ntm agents show`/`list` print the static constant and misreport which model
// the next spawn uses. See ntm#192.
func effectiveAgentModel(profile *agents.AgentProfile) string {
	if profile == nil {
		return ""
	}
	if resolved := ResolveModel(AgentType(profile.Type), ""); resolved != "" {
		return resolved
	}
	return profile.Model
}

// runAgentsList displays all agent profiles.
func runAgentsList() error {
	pm := agents.NewProfileMatcher()
	profiles := pm.AllProfiles()

	// Reflect the config-resolved spawn model rather than the baked-in default
	// so the listing matches what `ntm spawn` actually launches (ntm#192).
	for _, p := range profiles {
		p.Model = effectiveAgentModel(p)
	}

	// Sort by type for consistent output
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Type < profiles[j].Type
	})

	if IsJSONOutput() {
		return output.PrintJSON(profiles)
	}

	// Text output with tabwriter
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tMODEL\tCONTEXT\tSPECIALIZATIONS")
	fmt.Fprintln(w, "----\t-----\t-------\t---------------")

	for _, p := range profiles {
		specs := make([]string, len(p.Specializations))
		for i, s := range p.Specializations {
			specs[i] = string(s)
		}
		fmt.Fprintf(w, "%s\t%s\t%dk\t%s\n",
			p.Type, p.Model, p.ContextBudget/1000, strings.Join(specs, ", "))
	}

	return w.Flush()
}

// runAgentsShow displays a specific agent profile.
func runAgentsShow(agentName string) error {
	pm := agents.NewProfileMatcher()
	profile := pm.GetProfileByName(agentName)

	if profile == nil {
		return fmt.Errorf("unknown agent type: %s", agentName)
	}

	// Reflect the config-resolved spawn model rather than the baked-in default
	// so the displayed Model matches what `ntm spawn` actually launches and
	// what Agent Mail registers (ntm#192). GetProfileByName returns a copy, so
	// overwriting Model here does not mutate shared matcher state.
	profile.Model = effectiveAgentModel(profile)

	if IsJSONOutput() {
		return output.PrintJSON(profile)
	}

	// Text output
	fmt.Printf("Agent Profile: %s\n", profile.Type)
	fmt.Println(strings.Repeat("=", 40))
	fmt.Printf("Model:          %s\n", profile.Model)
	fmt.Printf("Context Budget: %d tokens (%dk)\n", profile.ContextBudget, profile.ContextBudget/1000)

	fmt.Printf("\nSpecializations:\n")
	for _, s := range profile.Specializations {
		fmt.Printf("  - %s\n", s)
	}

	if len(profile.Preferences.PreferredFiles) > 0 {
		fmt.Printf("\nPreferred Files:\n")
		for _, f := range profile.Preferences.PreferredFiles {
			fmt.Printf("  - %s\n", f)
		}
	}

	if len(profile.Preferences.PreferredLabels) > 0 {
		fmt.Printf("\nPreferred Labels:\n")
		for _, l := range profile.Preferences.PreferredLabels {
			fmt.Printf("  - %s\n", l)
		}
	}

	fmt.Printf("\nPerformance:\n")
	fmt.Printf("  Tasks Completed: %d\n", profile.Performance.TasksCompleted)
	fmt.Printf("  Success Rate:    %.1f%%\n", profile.Performance.SuccessRate*100)
	if profile.Performance.AvgCompletionTime > 0 {
		fmt.Printf("  Avg Time:        %s\n", profile.Performance.AvgCompletionTime)
	}

	return nil
}

// runAgentsStats displays performance statistics for all agents.
func runAgentsStats() error {
	pm := agents.NewProfileMatcher()
	stats := pm.GetPerformanceStats()

	if IsJSONOutput() {
		return output.PrintJSON(stats)
	}

	// Text output
	fmt.Println("Agent Performance Statistics")
	fmt.Println(strings.Repeat("=", 60))

	// Sort agent types for consistent output
	var types []agents.AgentType
	for t := range stats {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i] < types[j]
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tTASKS\tSUCCESS\tAVG TIME\tLAST ACTIVE")
	fmt.Fprintln(w, "-----\t-----\t-------\t--------\t-----------")

	for _, t := range types {
		p := stats[t]
		lastActive := "-"
		if !p.LastUpdated.IsZero() {
			lastActive = p.LastUpdated.Format("2006-01-02 15:04")
		}
		avgTime := "-"
		if p.AvgCompletionTime > 0 {
			avgTime = p.AvgCompletionTime.String()
		}
		fmt.Fprintf(w, "%s\t%d\t%.1f%%\t%s\t%s\n",
			t, p.TasksCompleted, p.SuccessRate*100, avgTime, lastActive)
	}

	return w.Flush()
}

// runAgentsRecommend recommends the best agent for a task.
func runAgentsRecommend(task agents.TaskInfo) error {
	pm := agents.NewProfileMatcher()

	// Get scores for all agents
	type agentScore struct {
		Agent  agents.AgentType   `json:"agent"`
		Result agents.ScoreResult `json:"result"`
	}
	var scores []agentScore

	for _, t := range []agents.AgentType{agents.AgentTypeClaude, agents.AgentTypeCodex, agents.AgentTypeGemini} {
		result := pm.ScoreAssignment(t, task)
		scores = append(scores, agentScore{Agent: t, Result: result})
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Result.Score > scores[j].Result.Score
	})

	recommended, recommendedResult := pm.RecommendAgent(task)

	if IsJSONOutput() {
		jsonData := map[string]interface{}{
			"task":        task,
			"recommended": recommended,
			"scores":      scores,
		}
		data, err := json.MarshalIndent(jsonData, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Text output
	fmt.Printf("Task Analysis\n")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Title: %s\n", task.Title)
	fmt.Printf("Type:  %s\n", task.Type)
	if len(task.AffectedFiles) > 0 {
		fmt.Printf("Files: %s\n", strings.Join(task.AffectedFiles, ", "))
	}
	if len(task.Labels) > 0 {
		fmt.Printf("Labels: %s\n", strings.Join(task.Labels, ", "))
	}
	if task.EstimatedTokens > 0 {
		fmt.Printf("Estimated Tokens: %d\n", task.EstimatedTokens)
	}

	fmt.Printf("\nRecommendation: %s (score: %.2f)\n", recommended, recommendedResult.Score)
	if recommendedResult.SpecializationHit {
		fmt.Println("  ✓ Matches agent specialization")
	}
	if recommendedResult.FileMatchScore > 1.0 {
		fmt.Printf("  ✓ File pattern match (%.2fx boost)\n", recommendedResult.FileMatchScore)
	}
	if recommendedResult.LabelMatchScore > 1.0 {
		fmt.Printf("  ✓ Label match (%.2fx boost)\n", recommendedResult.LabelMatchScore)
	}

	fmt.Printf("\nAll Scores:\n")
	for _, s := range scores {
		status := "✓"
		if !s.Result.CanHandle {
			status = "✗"
		}
		fmt.Printf("  %s %s: %.2f", status, s.Agent, s.Result.Score)
		if !s.Result.CanHandle {
			fmt.Printf(" (%s)", s.Result.Reason)
		}
		fmt.Println()
	}

	return nil
}
