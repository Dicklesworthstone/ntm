package robot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

// PlanOutput provides an execution plan for what can be done
type PlanOutput struct {
	RobotResponse
	GeneratedAt    time.Time    `json:"generated_at"`
	Recommendation string       `json:"recommendation"`
	Actions        []PlanAction `json:"actions"`
	BeadActions    []BeadAction `json:"bead_actions,omitempty"`
	Warnings       []string     `json:"warnings,omitempty"`
}

// BeadAction represents a recommended action based on bead priority analysis
type BeadAction struct {
	BeadID        string         `json:"bead_id"`
	Title         string         `json:"title"`
	Priority      int            `json:"priority"`
	Impact        float64        `json:"impact_score"`
	Reasoning     []string       `json:"reasoning"`
	Command       string         `json:"command"`              // e.g., "br update ntm-xyz --status in_progress"
	IsReady       bool           `json:"is_ready"`             // true if no blockers
	BlockedBy     []string       `json:"blocked_by,omitempty"` // blocking bead IDs
	GraphPosition *GraphPosition `json:"graph_position,omitempty"`
}

// GraphPosition represents the position of an issue in the dependency graph
type GraphPosition struct {
	IsBottleneck    bool    `json:"is_bottleneck,omitempty"`
	BottleneckScore float64 `json:"bottleneck_score,omitempty"`
	IsKeystone      bool    `json:"is_keystone,omitempty"`
	KeystoneScore   float64 `json:"keystone_score,omitempty"`
	IsHub           bool    `json:"is_hub,omitempty"`
	HubScore        float64 `json:"hub_score,omitempty"`
	IsAuthority     bool    `json:"is_authority,omitempty"`
	AuthorityScore  float64 `json:"authority_score,omitempty"`
	Summary         string  `json:"summary,omitempty"` // Human-readable summary
}

// PlanAction is a suggested action
type PlanAction struct {
	Priority    int      `json:"priority"` // 1=high, 2=medium, 3=low
	Command     string   `json:"command"`
	Description string   `json:"description"`
	Args        []string `json:"args,omitempty"`
}

// GetPlan generates an execution plan.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetPlan() (*PlanOutput, error) {
	plan := &PlanOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Actions:       []PlanAction{},
		BeadActions:   []BeadAction{},
	}

	// Check tmux availability
	if !tmux.IsInstalled() {
		plan.Recommendation = "Install tmux first"
		plan.Warnings = append(plan.Warnings, "tmux is not installed or not in PATH")
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "brew install tmux",
			Description: "Install tmux using Homebrew (macOS)",
		})
		return plan, nil
	}

	// Check for existing sessions
	sessions, _ := tmux.ListSessions()

	if len(sessions) == 0 {
		plan.Recommendation = "Create your first coding session"
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    1,
			Command:     "ntm spawn myproject --cc=2",
			Description: "Create session with 2 Claude Code agents",
			Args:        []string{"spawn", "myproject", "--cc=2"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm tutorial",
			Description: "Learn NTM with an interactive tutorial",
			Args:        []string{"tutorial"},
		})
	} else {
		plan.Recommendation = "Attach to an existing session or create a new one"

		// Find unattached sessions
		for _, sess := range sessions {
			if !sess.Attached {
				plan.Actions = append(plan.Actions, PlanAction{
					Priority:    1,
					Command:     fmt.Sprintf("ntm attach %s", sess.Name),
					Description: fmt.Sprintf("Attach to session '%s' (%d windows)", sess.Name, sess.Windows),
					Args:        []string{"attach", sess.Name},
				})
			}
		}

		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    2,
			Command:     "ntm palette",
			Description: "Open command palette for quick actions",
			Args:        []string{"palette"},
		})
		plan.Actions = append(plan.Actions, PlanAction{
			Priority:    3,
			Command:     "ntm dashboard",
			Description: "Open visual session dashboard",
			Args:        []string{"dashboard"},
		})
	}

	// Add bead-based recommendations from bv priority analysis
	beadActions, beadWarnings := getBeadRecommendations(5) // Top 5 recommendations
	plan.BeadActions = beadActions
	plan.Warnings = append(plan.Warnings, beadWarnings...)

	// Update recommendation if there are high-impact beads to work on
	if len(plan.BeadActions) > 0 && plan.BeadActions[0].IsReady {
		plan.Recommendation = fmt.Sprintf("Work on high-impact bead: %s", plan.BeadActions[0].Title)
	}

	return plan, nil
}

// PrintPlan outputs an execution plan.
// This is a thin wrapper around GetPlan() for CLI output.
func PrintPlan() error {
	output, err := GetPlan()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot plan failed")
}

// getBeadRecommendations returns recommended bead actions from bv priority analysis
func getBeadRecommendations(limit int) ([]BeadAction, []string) {
	var actions []BeadAction
	var warnings []string

	// Check if bv is available
	if !bv.IsInstalled() {
		warnings = append(warnings, "bv (beads_viewer) not installed - install for bead-based recommendations")
		return actions, warnings
	}

	// Get priority recommendations from bv
	recommendations, err := bv.GetNextActions("", limit)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get bv priority: %v", err))
		return actions, warnings
	}

	// Get ready issues to check blockers
	readyIssues := getReadyIssueIDs()

	// Collect issue IDs for batch graph position lookup
	var issueIDs []string
	for _, rec := range recommendations {
		issueIDs = append(issueIDs, rec.IssueID)
	}

	// Get graph positions in batch for efficiency
	graphPositions, graphErr := bv.GetGraphPositionsBatch("", issueIDs)
	if graphErr != nil {
		warnings = append(warnings, fmt.Sprintf("failed to get graph positions: %v", graphErr))
	}

	// Convert bv recommendations to BeadActions
	for _, rec := range recommendations {
		isReady := readyIssues[rec.IssueID]

		action := BeadAction{
			BeadID:    rec.IssueID,
			Title:     rec.Title,
			Priority:  rec.SuggestedPriority,
			Impact:    rec.ImpactScore,
			Reasoning: rec.Reasoning,
			Command:   fmt.Sprintf("br update %s --status in_progress", rec.IssueID),
			IsReady:   isReady,
		}

		// Add graph position if available
		if graphPositions != nil {
			if pos, ok := graphPositions[rec.IssueID]; ok && pos != nil {
				action.GraphPosition = &GraphPosition{
					IsBottleneck:    pos.IsBottleneck,
					BottleneckScore: pos.BottleneckScore,
					IsKeystone:      pos.IsKeystone,
					KeystoneScore:   pos.KeystoneScore,
					IsHub:           pos.IsHub,
					HubScore:        pos.HubScore,
					IsAuthority:     pos.IsAuthority,
					AuthorityScore:  pos.AuthorityScore,
					Summary:         pos.Summary,
				}
			}
		}

		// If not ready, try to determine blockers
		if !isReady {
			blockers := getBlockersForIssue(rec.IssueID)
			action.BlockedBy = blockers
		}

		actions = append(actions, action)
	}

	return actions, warnings
}

// getReadyIssueIDs returns a set of issue IDs that are ready (unblocked)
func getReadyIssueIDs() map[string]bool {
	ready := make(map[string]bool)

	// Try to run br ready --json to get ready issues
	output, err := bv.RunBd("", "ready", "--json")
	if err != nil {
		return ready
	}

	// Parse JSON array of issues
	var issues []struct {
		ID string `json:"id"`
	}
	if issues, err = bv.UnmarshalBdList[struct {
		ID string `json:"id"`
	}](output); err != nil {
		return ready
	}

	for _, issue := range issues {
		ready[issue.ID] = true
	}

	return ready
}

// getBlockersForIssue returns the IDs of issues blocking the given issue
func getBlockersForIssue(issueID string) []string {
	var blockers []string

	// Try to run br show <id> --json to get dependencies
	output, err := bv.RunBd("", "show", issueID, "--json")
	if err != nil {
		return blockers
	}

	// Parse JSON - br show returns an array with one element
	var issues []struct {
		Dependencies []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(output), &issues); err != nil {
		return blockers
	}

	if len(issues) > 0 {
		for _, dep := range issues[0].Dependencies {
			// Only include non-closed dependencies as blockers
			if dep.Status != "closed" {
				blockers = append(blockers, dep.ID)
			}
		}
	}

	return blockers
}

// GraphOutput provides project graph analysis from bv
type GraphOutput struct {
	RobotResponse
	GeneratedAt time.Time            `json:"generated_at"`
	Available   bool                 `json:"available"`
	Error       string               `json:"error,omitempty"`
	Insights    *bv.InsightsResponse `json:"insights,omitempty"`
	Priority    *bv.PriorityResponse `json:"priority,omitempty"`
	Health      *bv.HealthSummary    `json:"health,omitempty"`
	Correlation *GraphCorrelation    `json:"correlation,omitempty"`
}

// GraphCorrelation provides a best-effort cross-tool view of agents, beads, and mail threads.
type GraphCorrelation struct {
	GeneratedAt   time.Time                   `json:"generated_at"`
	Assignments   []GraphAgentAssignment      `json:"assignments"`
	BeadGraph     map[string]GraphBeadNode    `json:"bead_graph"`
	MailSummary   map[string]GraphMailSummary `json:"mail_summary"`
	OrphanBeads   []string                    `json:"orphan_beads"`
	OrphanThreads []string                    `json:"orphan_threads"`
	Errors        []string                    `json:"errors,omitempty"`
}

// GraphAgentAssignment captures bead/thread membership for an agent.
type GraphAgentAssignment struct {
	Agent        string   `json:"agent"`
	AgentName    string   `json:"agent_name,omitempty"`
	AgentType    string   `json:"agent_type"`
	Program      string   `json:"program,omitempty"`
	Model        string   `json:"model,omitempty"`
	Beads        []string `json:"beads"`
	MailThreads  []string `json:"mail_threads"`
	Pane         string   `json:"pane,omitempty"`
	Session      string   `json:"session,omitempty"`
	Detected     string   `json:"detected_type,omitempty"`
	DetectedFrom string   `json:"detected_from,omitempty"`
}

// GraphBeadNode summarizes bead status and relationships.
type GraphBeadNode struct {
	Status     string   `json:"status"`
	AssignedTo *string  `json:"assigned_to,omitempty"`
	BlockedBy  []string `json:"blocked_by"`
	Blocking   []string `json:"blocking"`
	Title      string   `json:"title,omitempty"`
}

// GraphMailSummary summarizes a mail thread for correlation.
type GraphMailSummary struct {
	Subject      string    `json:"subject"`
	Participants []string  `json:"participants,omitempty"`
	LastActivity time.Time `json:"last_activity"`
	Unread       int       `json:"unread,omitempty"`
}

// GetGraph returns bv graph insights for AI consumption.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetGraph() (*GraphOutput, error) {
	output := &GraphOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Available:     bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeDependencyMissing,
			"Install bv to enable graph insights",
		)
		// Even if bv is missing, still attempt correlation to provide partial data.
	} else {
		wd := mustGetwd()

		// Get insights (bottlenecks, keystones, etc.)
		insights, err := bv.GetInsights(wd)
		if err != nil {
			output.Error = fmt.Sprintf("failed to get insights: %v", err)
			output.RobotResponse = bvErrorResponse(err, "Check bv graph data and repository state")
		} else {
			output.Insights = insights
		}

		// Get priority recommendations
		priority, err := bv.GetPriority(wd)
		if err != nil {
			if output.Error == "" {
				output.Error = fmt.Sprintf("failed to get priority: %v", err)
			}
		} else {
			output.Priority = priority
		}

		// Get health summary
		health, err := bv.GetHealthSummary(wd)
		if err != nil {
			if output.Error == "" {
				output.Error = fmt.Sprintf("failed to get health: %v", err)
			}
		} else {
			output.Health = health
		}
	}

	// Build correlation graph (best-effort, independent of bv availability)
	output.Correlation = buildCorrelationGraph()

	return output, nil
}

// PrintGraph outputs bv graph insights for AI consumption.
// This is a thin wrapper around GetGraph() for CLI output.
func PrintGraph() error {
	output, err := GetGraph()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot graph failed")
}

// buildCorrelationGraph assembles a best-effort correlation map across agents, beads, and mail.
func buildCorrelationGraph() *GraphCorrelation {
	now := time.Now().UTC()
	corr := &GraphCorrelation{
		GeneratedAt:   now,
		Assignments:   make([]GraphAgentAssignment, 0),
		BeadGraph:     make(map[string]GraphBeadNode),
		MailSummary:   make(map[string]GraphMailSummary),
		OrphanBeads:   make([]string, 0),
		OrphanThreads: make([]string, 0),
	}

	wd, err := os.Getwd()
	if err != nil {
		corr.Errors = append(corr.Errors, fmt.Sprintf("working directory unavailable: %v", err))
		return corr
	}

	// Collect Agent Mail agents (if available)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agentMailClient := agentmail.NewClient(agentmail.WithProjectKey(wd))
	var agents []agentmail.Agent
	if agentMailClient.IsAvailable() {
		if _, err := agentMailClient.EnsureProject(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail ensure_project: %v", err))
		} else if list, err := agentMailClient.ListProjectAgents(ctx, wd); err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail list_agents: %v", err))
		} else {
			agents = list
		}
	} else {
		corr.Errors = append(corr.Errors, "agent mail not available")
	}

	assignmentByAgent := make(map[string]*GraphAgentAssignment)
	for _, a := range agents {
		if a.Name == "HumanOverseer" {
			continue
		}
		assignmentByAgent[a.Name] = &GraphAgentAssignment{
			Agent:       a.Name,
			AgentName:   a.Name,
			AgentType:   normalizedProgramType(a.Program),
			Program:     a.Program,
			Model:       a.Model,
			Beads:       make([]string, 0),
			MailThreads: make([]string, 0),
		}
	}

	// Add bead assignments from bv summary (if present)
	if beads := bv.GetBeadsSummary(wd, BeadLimit); beads != nil && beads.Available {
		for _, inProg := range beads.InProgressList {
			node := GraphBeadNode{
				Status:    "in_progress",
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     inProg.Title,
			}
			if inProg.Assignee != "" {
				assign := inProg.Assignee
				node.AssignedTo = &assign
				a := assignmentByAgent[assign]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       assign,
						AgentName:   assign,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[assign] = a
				}
				a.Beads = appendUnique(a.Beads, inProg.ID)
			} else {
				corr.OrphanBeads = appendUnique(corr.OrphanBeads, inProg.ID)
			}
			corr.BeadGraph[inProg.ID] = node
		}

		for _, ready := range beads.ReadyPreview {
			status := "ready"
			node := GraphBeadNode{
				Status:    status,
				BlockedBy: make([]string, 0),
				Blocking:  make([]string, 0),
				Title:     ready.Title,
			}
			corr.BeadGraph[ready.ID] = node
		}
	} else if beads != nil && !beads.Available && beads.Reason != "" {
		corr.Errors = append(corr.Errors, fmt.Sprintf("beads unavailable: %s", beads.Reason))
	}

	// Gather mail threads from per-agent inboxes (best-effort, bounded).
	if len(agents) > 0 && agentMailClient.IsAvailable() {
		const inboxLimit = 50
		for _, a := range agents {
			if a.Name == "HumanOverseer" {
				continue
			}

			inbox, err := agentMailClient.FetchInbox(ctx, agentmail.FetchInboxOptions{
				ProjectKey:    wd,
				AgentName:     a.Name,
				Limit:         inboxLimit,
				IncludeBodies: false,
			})
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("agent mail fetch_inbox %s: %v", a.Name, err))
				continue
			}
			for _, msg := range inbox {
				if msg.ThreadID == nil || strings.TrimSpace(*msg.ThreadID) == "" {
					continue
				}
				tid := strings.TrimSpace(*msg.ThreadID)
				thread := corr.MailSummary[tid]
				if thread.Subject == "" {
					thread.Subject = msg.Subject
				}
				if msg.CreatedTS.After(thread.LastActivity) {
					thread.LastActivity = msg.CreatedTS.Time
				}
				if isUnreadInboxMessage(msg) {
					thread.Unread++
				}
				corr.MailSummary[tid] = thread

				assign := assignmentByAgent[a.Name]
				if assign == nil {
					assign = &GraphAgentAssignment{
						Agent:       a.Name,
						AgentName:   a.Name,
						AgentType:   normalizedProgramType(a.Program),
						Program:     a.Program,
						Model:       a.Model,
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[a.Name] = assign
				}
				assign.MailThreads = appendUnique(assign.MailThreads, tid)
			}
		}

		// Add participants (best-effort) for a few most-recent threads.
		var threadIDs []string
		for tid := range corr.MailSummary {
			threadIDs = append(threadIDs, tid)
		}
		sort.SliceStable(threadIDs, func(i, j int) bool {
			return corr.MailSummary[threadIDs[i]].LastActivity.After(corr.MailSummary[threadIDs[j]].LastActivity)
		})

		const maxSummaries = 10
		for i, tid := range threadIDs {
			if i >= maxSummaries {
				break
			}
			includeExamples := false
			summary, err := agentMailClient.SummarizeThread(ctx, agentmail.SummarizeThreadOptions{
				ProjectKey:      wd,
				ThreadID:        tid,
				IncludeExamples: &includeExamples,
			})
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("summarize_thread %s: %v", tid, err))
				continue
			}
			thread := corr.MailSummary[tid]
			thread.Participants = summary.Summary.Participants
			corr.MailSummary[tid] = thread

			for _, participant := range summary.Summary.Participants {
				a := assignmentByAgent[participant]
				if a == nil {
					a = &GraphAgentAssignment{
						Agent:       participant,
						AgentName:   participant,
						AgentType:   "unknown",
						Beads:       make([]string, 0),
						MailThreads: make([]string, 0),
					}
					assignmentByAgent[participant] = a
				}
				a.MailThreads = appendUnique(a.MailThreads, tid)
			}
		}
	}

	// Best-effort tmux pane mapping for Agent Mail agents (NTM sessions).
	if tmux.IsInstalled() {
		sessions, err := tmux.ListSessions()
		if err != nil {
			corr.Errors = append(corr.Errors, fmt.Sprintf("tmux list_sessions: %v", err))
		} else {
			for _, sess := range sessions {
				panes, err := tmux.GetPanes(sess.Name)
				if err != nil {
					continue
				}
				mapping := resolveAgentsForSession(panes, agents)
				if len(mapping) == 0 {
					continue
				}
				paneInfos := parseNTMPanes(panes)
				for paneType, infos := range paneInfos {
					for _, pane := range infos {
						agentName := mapping[pane.Key]
						if agentName == "" {
							agentName = mapping[pane.Label]
						}
						if agentName == "" {
							continue
						}
						a := assignmentByAgent[agentName]
						if a == nil {
							a = &GraphAgentAssignment{
								Agent:       agentName,
								AgentName:   agentName,
								AgentType:   normalizedProgramType(""),
								Beads:       make([]string, 0),
								MailThreads: make([]string, 0),
							}
							assignmentByAgent[agentName] = a
						}
						a.Session = sess.Name
						// Emit the pane's real window index (#172) so the W.P
						// address round-trips on multi-window layouts; the prior
						// hardcoded 0 misaddressed panes outside window 0.
						a.Pane = fmt.Sprintf("%d.%d", pane.WindowIndex, pane.TmuxIndex)
						a.Agent = fmt.Sprintf("%s:%s", sess.Name, a.Pane)
						a.Detected = paneType
						a.DetectedFrom = "ntm_pane_title"
					}
				}
			}
		}
	}

	// Fill dependency edges for in-progress beads (best-effort, bounded).
	if _, err := exec.LookPath("br"); err == nil {
		for beadID, node := range corr.BeadGraph {
			if node.Status != "in_progress" {
				continue
			}
			blockedBy, deps, err := getBeadNeighbors(wd, beadID, "down")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("br dep tree down %s: %v", beadID, err))
			} else {
				node.BlockedBy = blockedBy
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			blocking, deps, err := getBeadNeighbors(wd, beadID, "up")
			if err != nil {
				corr.Errors = append(corr.Errors, fmt.Sprintf("br dep tree up %s: %v", beadID, err))
			} else {
				node.Blocking = blocking
				for _, dep := range deps {
					if _, ok := corr.BeadGraph[dep.ID]; ok {
						continue
					}
					corr.BeadGraph[dep.ID] = GraphBeadNode{
						Status:     dep.Status,
						AssignedTo: nil,
						BlockedBy:  make([]string, 0),
						Blocking:   make([]string, 0),
						Title:      dep.Title,
					}
				}
			}

			corr.BeadGraph[beadID] = node
		}
	}

	// Orphan threads: threads not linked to any bead ID.
	for tid := range corr.MailSummary {
		if _, ok := corr.BeadGraph[tid]; !ok {
			corr.OrphanThreads = appendUnique(corr.OrphanThreads, tid)
		}
	}

	// Materialize assignment list (stable order).
	for _, a := range assignmentByAgent {
		corr.Assignments = append(corr.Assignments, *a)
	}
	sort.SliceStable(corr.Assignments, func(i, j int) bool {
		return corr.Assignments[i].AgentName < corr.Assignments[j].AgentName
	})

	return corr
}

// appendUnique adds a value if absent.
func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

type bdDepTreeNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
}

func getBeadNeighbors(dir, issueID, direction string) ([]string, []bdDepTreeNode, error) {
	if issueID == "" {
		return nil, nil, fmt.Errorf("issue id is empty")
	}
	if direction != "down" && direction != "up" {
		return nil, nil, fmt.Errorf("invalid direction %q", direction)
	}

	out, err := bv.RunBd(dir, "dep", "tree", issueID, "--direction="+direction, "--max-depth=1", "--json")
	if err != nil {
		return nil, nil, fmt.Errorf("br dep tree: %w", err)
	}

	var nodes []bdDepTreeNode
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		return nil, nil, fmt.Errorf("parse br dep tree json: %w", err)
	}

	seen := make(map[string]bool)
	ids := make([]string, 0)
	cleaned := make([]bdDepTreeNode, 0)
	for _, n := range nodes {
		n.ID = strings.TrimSpace(n.ID)
		// br dep tree lists the queried issue itself at depth 0; it is not
		// its own blocker or dependent.
		if n.ID == "" || seen[n.ID] || n.Depth == 0 || n.ID == issueID {
			continue
		}
		seen[n.ID] = true
		if strings.TrimSpace(n.Status) == "" {
			n.Status = "unknown"
		}
		ids = append(ids, n.ID)
		cleaned = append(cleaned, n)
	}

	sort.Strings(ids)
	sort.SliceStable(cleaned, func(i, j int) bool { return cleaned[i].ID < cleaned[j].ID })
	return ids, cleaned, nil
}

// TriageOptions configures the triage output
type TriageOptions struct {
	Limit int // Max recommendations per category (default 10)
}

// TriageOutput is the robot-triage JSON output structure
type TriageOutput struct {
	RobotResponse
	GeneratedAt     time.Time                 `json:"generated_at"`
	Available       bool                      `json:"available"`
	DataHash        string                    `json:"data_hash,omitempty"`
	Error           string                    `json:"error,omitempty"`
	QuickRef        *bv.TriageQuickRef        `json:"quick_ref,omitempty"`
	Recommendations []bv.TriageRecommendation `json:"recommendations,omitempty"`
	QuickWins       []bv.TriageRecommendation `json:"quick_wins,omitempty"`
	BlockersToClear []bv.BlockerToClear       `json:"blockers_to_clear,omitempty"`
	ProjectHealth   *bv.ProjectHealth         `json:"project_health,omitempty"`
	Commands        map[string]string         `json:"commands,omitempty"`
	CacheInfo       *TriageCacheInfo          `json:"cache_info,omitempty"`
}

// TriageCacheInfo provides cache metadata
type TriageCacheInfo struct {
	Cached bool  `json:"cached"`
	AgeMs  int64 `json:"age_ms,omitempty"`
	TTLMs  int64 `json:"ttl_ms"`
}

// GetTriage returns bv triage analysis data.
func GetTriage(opts TriageOptions) (*TriageOutput, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	output := &TriageOutput{
		RobotResponse: NewRobotResponse(true),
		GeneratedAt:   time.Now().UTC(),
		Available:     bv.IsInstalled(),
	}

	if !bv.IsInstalled() {
		output.Error = "bv (beads_viewer) is not installed"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeDependencyMissing,
			"Install bv to enable triage",
		)
		return output, nil
	}

	wd := mustGetwd()

	// Get triage data (uses internal cache)
	triage, err := bv.GetTriage(wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get triage: %v", err)
		output.RobotResponse = bvErrorResponse(err, "Check bv triage cache and repository state")
		return output, nil
	}

	if triage == nil {
		output.Error = "no triage data returned"
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("%s", output.Error),
			ErrCodeInternalError,
			"Rebuild bv cache or retry triage",
		)
		return output, nil
	}

	// Copy data with limits applied
	output.DataHash = triage.DataHash
	output.QuickRef = &triage.Triage.QuickRef
	output.QuickWins = triage.Triage.QuickWins
	output.BlockersToClear = triage.Triage.BlockersToClear
	output.ProjectHealth = triage.Triage.ProjectHealth
	output.Commands = triage.Triage.Commands

	// Apply limits to recommendations
	if len(triage.Triage.Recommendations) > opts.Limit {
		output.Recommendations = triage.Triage.Recommendations[:opts.Limit]
	} else {
		output.Recommendations = triage.Triage.Recommendations
	}

	// Add cache info
	output.CacheInfo = &TriageCacheInfo{
		Cached: bv.IsCacheValid(),
		AgeMs:  bv.GetCacheAge().Milliseconds(),
		TTLMs:  bv.TriageCacheTTL.Milliseconds(),
	}

	return output, nil
}

// PrintTriage outputs bv triage analysis for AI consumption
func PrintTriage(opts TriageOptions) error {
	output, err := GetTriage(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot triage failed")
}

// Additional BV robot modes for comprehensive analysis

// LabelAttentionOptions configures label attention analysis
type LabelAttentionOptions struct {
	Limit int
}

// FileBeadsOptions configures file-to-beads analysis
type FileBeadsOptions struct {
	FilePath string
	Limit    int
}

// FileHotspotsOptions configures file hotspots analysis
type FileHotspotsOptions struct {
	Limit int
}

// FileRelationsOptions configures file relations analysis
type FileRelationsOptions struct {
	FilePath  string
	Limit     int
	Threshold float64
}

func setBVRobotFailure(response *RobotResponse, message, code string) {
	hint := "Check bv output and repository state"
	switch {
	case code == ErrCodeDependencyMissing:
		hint = "Install bv to enable this analysis"
	case code == ErrCodeInternalError && bv.IsNotInitialized(errors.New(message)):
		code, hint = ErrCodeDependencyMissing, beadsInitHint
	}
	*response = NewErrorResponse(errors.New(message), code, hint)
}

// beadsInitHint answers a bv/br analysis run in a project with no beads
// workspace: nothing is wrong, there is just no tracker to analyze yet.
const beadsInitHint = "No beads workspace in this project: run 'br init' to start tracking work"

// bvErrorResponse is the failure for a bv/br call: DEPENDENCY_MISSING with
// beadsInitHint when the project has no beads workspace, else INTERNAL_ERROR.
func bvErrorResponse(err error, hint string) RobotResponse {
	if bv.IsNotInitialized(err) {
		return NewErrorResponse(err, ErrCodeDependencyMissing, beadsInitHint)
	}
	return NewErrorResponse(err, ErrCodeInternalError, hint)
}

// ForecastOutput is the JSON output for --robot-forecast
type ForecastOutput struct {
	RobotResponse
	Target    string               `json:"target"`
	Available bool                 `json:"available"`
	Forecast  *bv.ForecastResponse `json:"forecast,omitempty"`
	Error     string               `json:"error,omitempty"`
}

// GetForecast returns BV forecast analysis data.
func GetForecast(target string) (*ForecastOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &ForecastOutput{
		RobotResponse: NewRobotResponse(true),
		Target:        target,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetForecast(context.Background(), wd, target)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get forecast: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var forecast bv.ForecastResponse
	if err := json.Unmarshal(raw, &forecast); err != nil {
		output.Error = fmt.Sprintf("failed to parse forecast: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Forecast = &forecast

	return output, nil
}

// PrintForecast outputs BV forecast analysis for ETA predictions
func PrintForecast(target string) error {
	output, err := GetForecast(target)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot forecast failed")
}

// SuggestOutput is the JSON output for --robot-suggest
type SuggestOutput struct {
	RobotResponse
	Available   bool                    `json:"available"`
	Suggestions *bv.SuggestionsResponse `json:"suggestions,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

// GetSuggest returns BV hygiene suggestions data.
func GetSuggest() (*SuggestOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &SuggestOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetSuggestions(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get suggestions: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var suggestions bv.SuggestionsResponse
	if err := json.Unmarshal(raw, &suggestions); err != nil {
		output.Error = fmt.Sprintf("failed to parse suggestions: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Suggestions = &suggestions

	return output, nil
}

// PrintSuggest outputs BV hygiene suggestions for code quality improvements
func PrintSuggest() error {
	output, err := GetSuggest()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot suggest failed")
}

// ImpactOutput is the JSON output for --robot-impact
type ImpactOutput struct {
	RobotResponse
	FilePath  string             `json:"file_path"`
	Available bool               `json:"available"`
	Impact    *bv.ImpactResponse `json:"impact,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// GetImpact returns BV impact analysis data.
func GetImpact(filePath string) (*ImpactOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &ImpactOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      filePath,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetImpact(context.Background(), wd, filePath)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get impact analysis: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var impact bv.ImpactResponse
	if err := json.Unmarshal(raw, &impact); err != nil {
		output.Error = fmt.Sprintf("failed to parse impact analysis: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Impact = &impact

	return output, nil
}

// PrintImpact outputs BV impact analysis for file changes
func PrintImpact(filePath string) error {
	output, err := GetImpact(filePath)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot impact failed")
}

// SearchOutput is the JSON output for --robot-search
type SearchOutput struct {
	RobotResponse
	Query     string             `json:"query"`
	Available bool               `json:"available"`
	Results   *bv.SearchResponse `json:"results,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// GetSearch returns BV semantic vector search results, at most limit of
// them when limit > 0 (bv's own default otherwise).
func GetSearch(query string, limit int) (*SearchOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &SearchOutput{
		RobotResponse: NewRobotResponse(true),
		Query:         query,
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetSearchWithOptions(context.Background(), wd, tools.BVSearchOptions{Query: query, Limit: limit})
	if err != nil {
		output.Error = fmt.Sprintf("failed to perform search: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var results bv.SearchResponse
	if err := json.Unmarshal(raw, &results); err != nil {
		output.Error = fmt.Sprintf("failed to parse search results: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Results = &results

	return output, nil
}

// PrintSearch outputs BV semantic vector search results
func PrintSearch(query string, limit int) error {
	output, err := GetSearch(query, limit)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot search failed")
}

// LabelAttentionOutput is the JSON output for --robot-label-attention
type LabelAttentionOutput struct {
	RobotResponse
	Available bool                       `json:"available"`
	Labels    *bv.LabelAttentionResponse `json:"labels,omitempty"`
	Limit     int                        `json:"limit"`
	Error     string                     `json:"error,omitempty"`
}

// GetLabelAttention returns BV label attention ranking data.
func GetLabelAttention(opts LabelAttentionOptions) (*LabelAttentionOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelAttentionOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelAttention(context.Background(), wd, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label attention: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var labels bv.LabelAttentionResponse
	if err := json.Unmarshal(raw, &labels); err != nil {
		output.Error = fmt.Sprintf("failed to parse label attention: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Labels = &labels

	return output, nil
}

// PrintLabelAttention outputs BV label attention ranking
func PrintLabelAttention(opts LabelAttentionOptions) error {
	output, err := GetLabelAttention(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot label attention failed")
}

// LabelFlowOutput is the JSON output for --robot-label-flow
type LabelFlowOutput struct {
	RobotResponse
	Available bool                  `json:"available"`
	Flow      *bv.LabelFlowResponse `json:"flow,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// GetLabelFlow returns BV cross-label dependency flow data.
func GetLabelFlow() (*LabelFlowOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelFlowOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelFlow(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label flow: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var flow bv.LabelFlowResponse
	if err := json.Unmarshal(raw, &flow); err != nil {
		output.Error = fmt.Sprintf("failed to parse label flow: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Flow = &flow

	return output, nil
}

// PrintLabelFlow outputs BV cross-label dependency flow analysis
func PrintLabelFlow() error {
	output, err := GetLabelFlow()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot label flow failed")
}

// LabelHealthOutput is the JSON output for --robot-label-health
type LabelHealthOutput struct {
	RobotResponse
	Available bool                    `json:"available"`
	Health    *bv.LabelHealthResponse `json:"health,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// GetLabelHealth returns BV per-label health data.
func GetLabelHealth() (*LabelHealthOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &LabelHealthOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetLabelHealth(context.Background(), wd)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get label health: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var health bv.LabelHealthResponse
	if err := json.Unmarshal(raw, &health); err != nil {
		output.Error = fmt.Sprintf("failed to parse label health: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Health = &health

	return output, nil
}

// PrintLabelHealth outputs BV per-label health analysis
func PrintLabelHealth() error {
	output, err := GetLabelHealth()
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot label health failed")
}

// FileBeadsOutput is the JSON output for --robot-file-beads
type FileBeadsOutput struct {
	RobotResponse
	FilePath  string                `json:"file_path"`
	Available bool                  `json:"available"`
	Beads     *bv.FileBeadsResponse `json:"beads,omitempty"`
	Limit     int                   `json:"limit"`
	Error     string                `json:"error,omitempty"`
}

// GetFileBeads returns BV file-to-beads mapping data.
func GetFileBeads(opts FileBeadsOptions) (*FileBeadsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileBeadsOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      opts.FilePath,
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileBeads(context.Background(), wd, opts.FilePath, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file beads: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var beads bv.FileBeadsResponse
	if err := json.Unmarshal(raw, &beads); err != nil {
		output.Error = fmt.Sprintf("failed to parse file beads: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Beads = &beads

	return output, nil
}

// PrintFileBeads outputs BV file-to-beads mapping
func PrintFileBeads(opts FileBeadsOptions) error {
	output, err := GetFileBeads(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot file beads failed")
}

// FileHotspotsOutput is the JSON output for --robot-file-hotspots
type FileHotspotsOutput struct {
	RobotResponse
	Available bool                     `json:"available"`
	Hotspots  *bv.FileHotspotsResponse `json:"hotspots,omitempty"`
	Limit     int                      `json:"limit"`
	Error     string                   `json:"error,omitempty"`
}

// GetFileHotspots returns BV file quality hotspots data.
func GetFileHotspots(opts FileHotspotsOptions) (*FileHotspotsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileHotspotsOutput{
		RobotResponse: NewRobotResponse(true),
		Available:     installed,
		Limit:         opts.Limit,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileHotspots(context.Background(), wd, opts.Limit)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file hotspots: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var hotspots bv.FileHotspotsResponse
	if err := json.Unmarshal(raw, &hotspots); err != nil {
		output.Error = fmt.Sprintf("failed to parse file hotspots: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Hotspots = &hotspots

	return output, nil
}

// PrintFileHotspots outputs BV file quality hotspots
func PrintFileHotspots(opts FileHotspotsOptions) error {
	output, err := GetFileHotspots(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot file hotspots failed")
}

// FileRelationsOutput is the JSON output for --robot-file-relations
type FileRelationsOutput struct {
	RobotResponse
	FilePath  string                    `json:"file_path"`
	Available bool                      `json:"available"`
	Relations *bv.FileRelationsResponse `json:"relations,omitempty"`
	Limit     int                       `json:"limit"`
	Threshold float64                   `json:"threshold"`
	Error     string                    `json:"error,omitempty"`
}

// GetFileRelations returns BV file co-change relations data.
func GetFileRelations(opts FileRelationsOptions) (*FileRelationsOutput, error) {
	adapter := tools.NewBVAdapter()
	_, installed := adapter.Detect()

	output := &FileRelationsOutput{
		RobotResponse: NewRobotResponse(true),
		FilePath:      opts.FilePath,
		Available:     installed,
		Limit:         opts.Limit,
		Threshold:     opts.Threshold,
	}

	if !installed {
		output.Error = "bv (beads_viewer) is not installed"
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeDependencyMissing)
		return output, nil
	}

	wd := mustGetwd()
	raw, err := adapter.GetFileRelations(context.Background(), wd, opts.FilePath, opts.Limit, opts.Threshold)
	if err != nil {
		output.Error = fmt.Sprintf("failed to get file relations: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}

	var relations bv.FileRelationsResponse
	if err := json.Unmarshal(raw, &relations); err != nil {
		output.Error = fmt.Sprintf("failed to parse file relations: %v", err)
		setBVRobotFailure(&output.RobotResponse, output.Error, ErrCodeInternalError)
		return output, nil
	}
	output.Relations = &relations

	return output, nil
}

// PrintFileRelations outputs BV file co-change relations
func PrintFileRelations(opts FileRelationsOptions) error {
	output, err := GetFileRelations(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(output, output.RobotResponse, "robot file relations failed")
}
