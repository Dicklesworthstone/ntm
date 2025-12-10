package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/spf13/cobra"
)

func newPipelineCmd() *cobra.Command {
	var stages []string

	cmd := &cobra.Command{
		Use:   "pipeline <session>",
		Short: "Run a multi-stage agent pipeline",
		Long: `Execute a sequence of agent prompts, passing output from one to the next.

Stages are defined using --stage flags. Each stage defines the agent type
(and optional model) and the prompt.

Format:
  --stage "type: prompt"
  --stage "type:model: prompt"

Examples:
  ntm pipeline myproject \
    --stage "cc: Design the API" \
    --stage "cod: Implement the API" \
    --stage "gmi: Write tests"

  ntm pipeline myproject \
    --stage "cc:opus: Architecture review" \
    --stage "cc:sonnet: Implementation"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session := args[0]
			if len(stages) == 0 {
				return fmt.Errorf("no stages defined (use --stage)")
			}

			var pipeStages []pipeline.Stage
			for _, s := range stages {
				// Split by first ": " to separate agent spec from prompt
				parts := strings.SplitN(s, ": ", 2)
				if len(parts) < 2 {
					// Fallback: try splitting by ":" if no space (less reliable)
					parts = strings.SplitN(s, ":", 2)
					if len(parts) < 2 {
						return fmt.Errorf("invalid stage format: %q (expected 'type: prompt')", s)
					}
				}

			agentSpec := strings.TrimSpace(parts[0])
			prompt := strings.TrimSpace(parts[1])

			agentType := agentSpec
			model := ""

			// Check for model in agent spec (e.g. "cc:opus")
			if strings.Contains(agentSpec, ":") {
				sub := strings.SplitN(agentSpec, ":", 2)
				agentType = sub[0]
				model = sub[1]
			}

			pipeStages = append(pipeStages, pipeline.Stage{
				AgentType: agentType,
				Prompt:    prompt,
				Model:     model,
			})
			}

			if err := tmux.EnsureInstalled(); err != nil {
				return err
			}

			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q not found", session)
			}

			return pipeline.Execute(context.Background(), pipeline.Pipeline{
				Session: session,
				Stages:  pipeStages,
			})
		},
	}

	cmd.Flags().StringArrayVar(&stages, "stage", nil, "Pipeline stage (format: 'type: prompt')")

	return cmd
}
