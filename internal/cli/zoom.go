package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tui/icons"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// SessionZoomInput is the kernel input for sessions.zoom.
type SessionZoomInput struct {
	Session string `json:"session"`
	Pane    *int   `json:"pane,omitempty"`
}

func init() {
	kernel.MustRegister(kernel.Command{
		Name:        "sessions.zoom",
		Description: "Zoom a specific pane in a session",
		Category:    "sessions",
		Input: &kernel.SchemaRef{
			Name: "SessionZoomInput",
			Ref:  "cli.SessionZoomInput",
		},
		Output: &kernel.SchemaRef{
			Name: "SuccessResponse",
			Ref:  "output.SuccessResponse",
		},
		REST: &kernel.RESTBinding{
			Method: "POST",
			Path:   "/sessions/{sessionId}/zoom",
		},
		Examples: []kernel.Example{
			{
				Name:        "zoom",
				Description: "Zoom pane 0 in a session",
				Command:     "ntm zoom myproject 0",
			},
		},
		SafetyLevel: kernel.SafetySafe,
		Idempotent:  false,
	})
	kernel.MustRegisterHandler("sessions.zoom", func(ctx context.Context, input any) (any, error) {
		opts := SessionZoomInput{}
		switch value := input.(type) {
		case SessionZoomInput:
			opts = value
		case *SessionZoomInput:
			if value != nil {
				opts = *value
			}
		}
		if strings.TrimSpace(opts.Session) == "" {
			return nil, fmt.Errorf("session is required")
		}
		if opts.Pane == nil {
			return nil, fmt.Errorf("pane is required")
		}
		return buildZoomResponse(ctx, opts.Session, *opts.Pane)
	})
}

func newZoomCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "zoom [session-name] [pane-index]",
		Aliases: []string{"z"},
		Short:   "Zoom a specific pane in a session",
		Long: `Zoom a specific pane in a tmux session and attach/switch to it.

If no session is specified:
- If inside tmux, operates on the current session
- Otherwise, shows a session selector

If no pane index is specified, shows a pane selector.

Examples:
  ntm zoom myproject 0      # Zoom pane 0
  ntm zoom myproject cc     # Zoom first Claude pane
  ntm zoom myproject        # Select pane to zoom
  ntm zoom                  # Select session and pane`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var session string
			paneIdx := -1
			paneSelector := ""

			if len(args) >= 1 {
				session = args[0]
			}
			if len(args) >= 2 {
				idx, err := strconv.Atoi(args[1])
				if err != nil {
					paneSelector = args[1]
				} else {
					paneIdx = idx
				}
			}

			return runZoom(cmd.OutOrStdout(), session, paneIdx, paneSelector)
		},
	}

	cmd.ValidArgsFunction = completeSessionThenPane

	return cmd
}

func runZoom(w io.Writer, session string, paneIdx int, paneSelector string) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	interactive := IsInteractive(w)
	t := theme.Current()

	// Determine target session
	res, err := ResolveSession(session, w)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return nil
	}
	res.ExplainIfInferred(os.Stderr)
	session = res.Session

	if !tmux.SessionExists(session) {
		if IsJSONOutput() {
			cause := fmt.Errorf("session '%s' not found", session)
			return emitJSONFailureEnvelopeWithCause(output.NewError(cause.Error()), cause)
		}
		return fmt.Errorf("session '%s' not found", session)
	}

	if paneIdx < 0 && strings.TrimSpace(paneSelector) != "" {
		resolved, err := resolvePaneSelector(session, paneSelector)
		if err != nil {
			if IsJSONOutput() {
				return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
			}
			return err
		}
		paneIdx = resolved
	}

	if IsJSONOutput() && paneIdx < 0 {
		cause := errors.New("pane index is required for JSON output")
		return emitJSONFailureEnvelopeWithCause(output.NewError(cause.Error()), cause)
	}

	// If no pane specified, let user select
	if paneIdx < 0 {
		if !interactive {
			return fmt.Errorf("non-interactive environment: pane index is required for zoom")
		}
		panes, err := tmux.GetPanes(session)
		if err != nil {
			return err
		}
		if len(panes) == 0 {
			return fmt.Errorf("no panes found in session '%s'", session)
		}

		// Show pane selector
		selected, err := runPaneSelector(session, panes)
		if err != nil {
			return err
		}
		if selected < 0 {
			return nil // User cancelled
		}
		paneIdx = selected
	}

	result, err := kernel.Run(context.Background(), "sessions.zoom", SessionZoomInput{
		Session: session,
		Pane:    &paneIdx,
	})
	if err != nil {
		if IsJSONOutput() {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return err
	}

	if IsJSONOutput() {
		resp, err := coerceSuccessResponse(result, "sessions.zoom")
		if err != nil {
			return emitJSONFailureEnvelopeWithCause(output.NewError(err.Error()), err)
		}
		return output.PrintJSON(resp)
	}

	fmt.Printf("%s✓%s Zoomed pane %d in '%s'\n",
		colorize(t.Success), colorize(t.Text), paneIdx, session)

	// Attach or switch to session
	return tmux.AttachOrSwitch(session)
}

func buildZoomResponse(ctx context.Context, session string, paneIdx int) (output.SuccessResponse, error) {
	if err := tmux.EnsureInstalled(); err != nil {
		return output.SuccessResponse{}, err
	}
	resolvedSession, err := normalizeExplicitLiveSessionName(ctx, session, true)
	if err != nil {
		return output.SuccessResponse{}, err
	}
	session = resolvedSession
	if !tmux.SessionExists(session) {
		return output.SuccessResponse{}, fmt.Errorf("session '%s' not found", session)
	}
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return output.SuccessResponse{}, err
	}
	found := false
	for _, p := range panes {
		if p.Index == paneIdx {
			found = true
			break
		}
	}
	if !found {
		return output.SuccessResponse{}, fmt.Errorf("pane %d not found in session '%s'", paneIdx, session)
	}
	if err := tmux.ZoomPane(session, paneIdx); err != nil {
		return output.SuccessResponse{}, fmt.Errorf("failed to zoom pane: %w", err)
	}
	return output.NewSuccess(fmt.Sprintf("zoomed pane %d in '%s'", paneIdx, session)), nil
}

func resolvePaneSelector(session, selector string) (int, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return -1, fmt.Errorf("pane selector is required")
	}

	panes, err := tmux.GetPanes(session)
	if err != nil {
		return -1, err
	}

	normalized := normalizeAgentType(selector)
	candidates := make([]tmux.Pane, 0, len(panes))
	for _, p := range panes {
		if normalizeAgentType(string(p.Type)) == normalized {
			candidates = append(candidates, p)
		}
	}

	if len(candidates) == 0 {
		return -1, fmt.Errorf("no panes match selector %q", selector)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Index < candidates[j].Index
	})

	return candidates[0].Index, nil
}

// runPaneSelector shows a simple pane selector and returns the selected pane index
func zoomPanePresentation(pane tmux.Pane, th theme.Theme, ic icons.IconSet) (string, string) {
	colorKey, icon := sessionPanePresentation(pane, th, ic)
	return fmt.Sprintf("\033[38;2;%s", colorToRGB(colorKey)), icon
}

func runPaneSelector(session string, panes []tmux.Pane) (int, error) {
	t := theme.Current()

	if len(panes) == 0 {
		return -1, fmt.Errorf("no panes available")
	}

	// For now, use a simple numbered list // placebo-waiver: bd-d7z7i
	fmt.Printf("\n%sSelect pane to zoom:%s\n\n", "\033[1m", "\033[0m")

	ic := icons.Current()
	for i, p := range panes {
		typeColor, typeIcon := zoomPanePresentation(p, t, ic)

		num := i + 1
		if num <= 9 {
			fmt.Printf("  %s%d%s %s%s %s%s (%s)\n",
				"\033[38;5;245m", num, "\033[0m",
				typeColor, typeIcon, "\033[0m",
				p.Title, p.Command)
		}
	}

	fmt.Print("\nEnter number (or q to cancel): ")
	var input string
	fmt.Scanln(&input)

	if input == "q" || input == "" {
		return -1, nil
	}

	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > len(panes) {
		return -1, fmt.Errorf("invalid selection")
	}

	return panes[idx-1].Index, nil
}
