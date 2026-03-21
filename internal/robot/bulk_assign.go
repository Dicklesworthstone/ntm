package robot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const defaultBulkAssignTemplate = "Read AGENTS.md, register with Agent Mail. Work on: {bead_id} - {bead_title}.\nUse br show {bead_id} for details. Mark in_progress when starting. Use ultrathink."

// BulkAssignOptions configures --robot-bulk-assign behavior.
type BulkAssignOptions struct {
	Session            string
	FromBV             bool
	Strategy           string
	AllocationJSON     string
	DryRun             bool
	Parallel           bool
	Stagger            time.Duration
	SkipPanes          []int
	PromptTemplatePath string
	Deps               *BulkAssignDependencies
}

// BulkAssignDependencies allows tests to stub external interactions.
type BulkAssignDependencies struct {
	FetchTriage      func(dir string) (*bv.TriageResponse, error)
	FetchInProgress  func(dir string, limit int) ([]bv.BeadInProgress, error)
	ListPanes        func(session string) ([]tmux.Pane, error)
	SendKeys         func(paneID, message string, enter bool) error
	ReadFile         func(path string) ([]byte, error)
	FetchBeadTitle   func(dir, beadID string) (string, error)
	FetchBeadDetails func(dir, beadID string) (BeadDetails, error)
	Now              func() time.Time
	Cwd              func() (string, error)
}

// BeadDetails captures metadata used for bulk prompt templating.
type BeadDetails struct {
	Title        string
	Type         string
	Dependencies []string
}

// BulkAssignOutput is the structured output for --robot-bulk-assign.
type BulkAssignOutput struct {
	RobotResponse
	Session          string                 `json:"session"`
	Strategy         string                 `json:"strategy"`
	Timestamp        time.Time              `json:"timestamp"`
	Assignments      []BulkAssignAssignment `json:"assignments"`
	Summary          BulkAssignSummary      `json:"summary"`
	UnassignedBeads  []string               `json:"unassigned_beads,omitempty"`
	UnassignedPanes  []int                  `json:"unassigned_panes,omitempty"`
	DryRun           bool                   `json:"dry_run,omitempty"`
	AllocationSource string                 `json:"allocation_source,omitempty"`
}

// BulkAssignAssignment is a single pane-to-bead allocation.
type BulkAssignAssignment struct {
	Pane       int    `json:"pane"`
	Bead       string `json:"bead"`
	BeadTitle  string `json:"bead_title"`
	Reason     string `json:"reason"`
	AgentType  string `json:"agent_type"`
	Status     string `json:"status"`
	PromptSent bool   `json:"prompt_sent"`
	Error      string `json:"error,omitempty"`
}

// BulkAssignSummary aggregates assignment stats.
type BulkAssignSummary struct {
	TotalPanes int `json:"total_panes"`
	Assigned   int `json:"assigned"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
}

type bulkBeadSource string

const (
	bulkSourceImpact bulkBeadSource = "impact"
	bulkSourceReady  bulkBeadSource = "ready"
	bulkSourceStale  bulkBeadSource = "stale"
)

type bulkBead struct {
	ID            string
	Title         string
	Priority      int
	UnblocksCount int
	Status        string
	UpdatedAt     time.Time
	Source        bulkBeadSource
}

type bulkPane struct {
	Index     int
	AgentType string
}

// GetBulkAssign generates the bulk assignment plan and returns the result.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetBulkAssign(opts BulkAssignOptions) (*BulkAssignOutput, error) {
	output := &BulkAssignOutput{
		RobotResponse:   NewRobotResponse(true),
		Session:         opts.Session,
		Assignments:     []BulkAssignAssignment{},
		UnassignedBeads: []string{},
		UnassignedPanes: []int{},
		DryRun:          opts.DryRun,
	}

	if opts.Session == "" {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("session name is required"),
			ErrCodeInvalidFlag,
			"Provide session name: ntm --robot-bulk-assign=myproject",
		)
		return output, nil
	}

	deps := bulkAssignDeps(opts.Deps)
	strategy := normalizeBulkAssignStrategy(opts.Strategy)
	output.Strategy = strategy
	output.Timestamp = deps.Now().UTC()

	panes, err := deps.ListPanes(opts.Session)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to get panes: %w", err),
			ErrCodeInternalError,
			"Check tmux is running and session is accessible",
		)
		return output, nil
	}

	paneList := filterBulkAssignPanes(panes, opts.SkipPanes)

	if opts.AllocationJSON != "" {
		allocation, err := parseBulkAssignAllocation(opts.AllocationJSON)
		if err != nil {
			output.RobotResponse = NewErrorResponse(err, ErrCodeInvalidFlag, "Provide valid JSON mapping pane->bead")
			return output, nil
		}
		plan := planBulkAssignFromAllocation(opts, deps, paneList, allocation)
		output.AllocationSource = "explicit"
		applyBulkAssignPlan(opts, deps, output, plan)
		return output, nil
	}

	if !opts.FromBV {
		output.RobotResponse = NewErrorResponse(
			errors.New("either --from-bv or --allocation is required"),
			ErrCodeInvalidFlag,
			"Use --from-bv or provide --allocation JSON",
		)
		return output, nil
	}

	wd, err := deps.Cwd()
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("failed to resolve working directory: %w", err),
			ErrCodeInternalError,
			"Run from a valid project directory",
		)
		return output, nil
	}

	triage, err := deps.FetchTriage(wd)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("bv triage failed: %w", err),
			ErrCodeInternalError,
			"Ensure bv is installed and .beads exists",
		)
		return output, nil
	}

	inProgress, err := deps.FetchInProgress(wd, 200)
	if err != nil {
		output.RobotResponse = NewErrorResponse(
			fmt.Errorf("fetch in-progress failed: %w", err),
			ErrCodeInternalError,
			"Ensure br/bd is available for in-progress beads",
		)
		return output, nil
	}

	plan := planBulkAssignFromBV(opts, deps, paneList, triage, inProgress)
	output.AllocationSource = "bv"
	applyBulkAssignPlan(opts, deps, output, plan)
	return output, nil
}

// PrintBulkAssign handles the --robot-bulk-assign command.
// This is a thin wrapper around GetBulkAssign() for CLI output.
func PrintBulkAssign(opts BulkAssignOptions) error {
	output, err := GetBulkAssign(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

func bulkAssignDeps(custom *BulkAssignDependencies) BulkAssignDependencies {
	deps := BulkAssignDependencies{
		FetchTriage:      bv.GetTriage,
		FetchInProgress:  func(dir string, limit int) ([]bv.BeadInProgress, error) { return bv.GetInProgressList(dir, limit), nil },
		ListPanes:        tmux.GetPanes,
		SendKeys:         tmux.SendKeys,
		ReadFile:         os.ReadFile,
		FetchBeadTitle:   fetchBeadTitle,
		FetchBeadDetails: fetchBeadDetails,
		Now:              time.Now,
		Cwd:              os.Getwd,
	}

	if custom == nil {
		return deps
	}
	if custom.FetchTriage != nil {
		deps.FetchTriage = custom.FetchTriage
	}
	if custom.FetchInProgress != nil {
		deps.FetchInProgress = custom.FetchInProgress
	}
	if custom.ListPanes != nil {
		deps.ListPanes = custom.ListPanes
	}
	if custom.SendKeys != nil {
		deps.SendKeys = custom.SendKeys
	}
	if custom.ReadFile != nil {
		deps.ReadFile = custom.ReadFile
	}
	if custom.FetchBeadTitle != nil {
		deps.FetchBeadTitle = custom.FetchBeadTitle
	}
	if custom.FetchBeadDetails != nil {
		deps.FetchBeadDetails = custom.FetchBeadDetails
	}
	if custom.Now != nil {
		deps.Now = custom.Now
	}
	if custom.Cwd != nil {
		deps.Cwd = custom.Cwd
	}

	return deps
}

func normalizeBulkAssignStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return "impact"
	}
	switch strategy {
	case "impact", "ready", "stale", "balanced":
		return strategy
	default:
		return "impact"
	}
}

func filterBulkAssignPanes(panes []tmux.Pane, skip []int) []bulkPane {
	skipSet := make(map[int]bool)
	for _, p := range skip {
		skipSet[p] = true
	}

	var filtered []bulkPane
	for _, pane := range panes {
		if skipSet[pane.Index] {
			continue
		}
		agentType := detectAgentType(pane.Title)
		if agentType == "unknown" || agentType == "user" {
			continue
		}
		filtered = append(filtered, bulkPane{Index: pane.Index, AgentType: agentType})
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Index < filtered[j].Index
	})

	return filtered
}

func planBulkAssignFromBV(opts BulkAssignOptions, deps BulkAssignDependencies, panes []bulkPane, triage *bv.TriageResponse, inProgress []bv.BeadInProgress) bulkAssignPlan {
	candidates := buildBulkAssignCandidates(triage, inProgress)
	beads := selectBulkAssignBeads(normalizeBulkAssignStrategy(opts.Strategy), candidates)
	return allocateBulkAssignBeads(panes, beads)
}

func planBulkAssignFromAllocation(opts BulkAssignOptions, deps BulkAssignDependencies, panes []bulkPane, allocation map[int]string) bulkAssignPlan {
	paneSet := make(map[int]bulkPane)
	for _, pane := range panes {
		paneSet[pane.Index] = pane
	}

	plan := bulkAssignPlan{}
	for paneIdx, beadID := range allocation {
		pane, ok := paneSet[paneIdx]
		assignment := BulkAssignAssignment{
			Pane:      paneIdx,
			Bead:      beadID,
			AgentType: "unknown",
			Status:    "planned",
		}
		if !ok {
			assignment.Status = "failed"
			assignment.Error = "pane not available"
			plan.Assignments = append(plan.Assignments, assignment)
			plan.failed++
			continue
		}
		assignment.AgentType = pane.AgentType
		assignment.Reason = "explicit"

		if beadID == "" {
			assignment.Status = "failed"
			assignment.Error = "empty bead id"
			plan.Assignments = append(plan.Assignments, assignment)
			plan.failed++
			continue
		}

		title, err := deps.FetchBeadTitle(getBulkAssignDir(deps), beadID)
		if err != nil {
			assignment.Status = "failed"
			assignment.Error = err.Error()
		} else {
			assignment.BeadTitle = title
		}
		plan.Assignments = append(plan.Assignments, assignment)
	}

	for paneIdx := range paneSet {
		if _, ok := allocation[paneIdx]; !ok {
			plan.UnassignedPanes = append(plan.UnassignedPanes, paneIdx)
		}
	}

	sort.Slice(plan.Assignments, func(i, j int) bool {
		return plan.Assignments[i].Pane < plan.Assignments[j].Pane
	})
	sort.Ints(plan.UnassignedPanes)

	return plan
}

type bulkAssignPlan struct {
	Assignments     []BulkAssignAssignment
	UnassignedBeads []string
	UnassignedPanes []int
	assigned        int
	failed          int
	skipped         int
}

func buildBulkAssignCandidates(triage *bv.TriageResponse, inProgress []bv.BeadInProgress) bulkAssignCandidates {
	candidates := bulkAssignCandidates{}
	if triage != nil {
		for _, blocker := range triage.Triage.BlockersToClear {
			candidates.impact = append(candidates.impact, bulkBead{
				ID:            blocker.ID,
				Title:         blocker.Title,
				UnblocksCount: blocker.UnblocksCount,
				Source:        bulkSourceImpact,
			})
		}

		for _, rec := range triage.Triage.Recommendations {
			priority := rec.Priority
			if priority < 0 {
				priority = 0
			}
			candidates.ready = append(candidates.ready, bulkBead{
				ID:            rec.ID,
				Title:         rec.Title,
				Priority:      priority,
				Status:        strings.ToLower(rec.Status),
				UnblocksCount: len(rec.UnblocksIDs),
				Source:        bulkSourceReady,
			})
		}
	}

	for _, item := range inProgress {
		candidates.stale = append(candidates.stale, bulkBead{
			ID:        item.ID,
			Title:     item.Title,
			UpdatedAt: item.UpdatedAt,
			Source:    bulkSourceStale,
		})
	}

	return candidates
}

type bulkAssignCandidates struct {
	impact []bulkBead
	ready  []bulkBead
	stale  []bulkBead
}

func selectBulkAssignBeads(strategy string, candidates bulkAssignCandidates) []bulkBead {
	switch strategy {
	case "ready":
		return selectReadyBeads(candidates.ready)
	case "stale":
		return selectStaleBeads(candidates.stale)
	case "balanced":
		return selectBalancedBeads(candidates)
	default:
		return selectImpactBeads(candidates)
	}
}

func selectImpactBeads(candidates bulkAssignCandidates) []bulkBead {
	impact := append([]bulkBead(nil), candidates.impact...)
	if len(impact) == 0 {
		return selectReadyBeads(candidates.ready)
	}
	sort.Slice(impact, func(i, j int) bool {
		if impact[i].UnblocksCount == impact[j].UnblocksCount {
			return impact[i].ID < impact[j].ID
		}
		return impact[i].UnblocksCount > impact[j].UnblocksCount
	})
	return impact
}

func selectReadyBeads(ready []bulkBead) []bulkBead {
	filtered := make([]bulkBead, 0, len(ready))
	for _, bead := range ready {
		switch bead.Status {
		case "", "ready", "open":
			filtered = append(filtered, bead)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Priority == filtered[j].Priority {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].Priority < filtered[j].Priority
	})
	return filtered
}

func selectStaleBeads(stale []bulkBead) []bulkBead {
	filtered := append([]bulkBead(nil), stale...)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].UpdatedAt.Before(filtered[j].UpdatedAt)
	})
	return filtered
}

func selectBalancedBeads(candidates bulkAssignCandidates) []bulkBead {
	impact := selectImpactBeads(candidates)
	ready := selectReadyBeads(candidates.ready)
	stale := selectStaleBeads(candidates.stale)

	var result []bulkBead
	idx := 0
	for len(result) < len(impact)+len(ready)+len(stale) {
		added := false
		if idx < len(impact) {
			result = append(result, impact[idx])
			added = true
		}
		if idx < len(ready) {
			result = append(result, ready[idx])
			added = true
		}
		if idx < len(stale) {
			result = append(result, stale[idx])
			added = true
		}
		if !added {
			break
		}
		idx++
	}
	return result
}

func allocateBulkAssignBeads(panes []bulkPane, beads []bulkBead) bulkAssignPlan {
	plan := bulkAssignPlan{}

	if len(panes) == 0 {
		for _, bead := range beads {
			plan.UnassignedBeads = append(plan.UnassignedBeads, bead.ID)
		}
		return plan
	}

	limit := len(panes)
	if len(beads) < limit {
		limit = len(beads)
	}

	for i := 0; i < limit; i++ {
		pane := panes[i]
		bead := beads[i]
		assignment := BulkAssignAssignment{
			Pane:      pane.Index,
			Bead:      bead.ID,
			BeadTitle: bead.Title,
			AgentType: pane.AgentType,
			Reason:    bulkAssignReason(bead),
			Status:    "planned",
		}
		plan.Assignments = append(plan.Assignments, assignment)
		plan.assigned++
	}

	if len(beads) > limit {
		for i := limit; i < len(beads); i++ {
			plan.UnassignedBeads = append(plan.UnassignedBeads, beads[i].ID)
		}
	}

	if len(panes) > limit {
		for i := limit; i < len(panes); i++ {
			plan.UnassignedPanes = append(plan.UnassignedPanes, panes[i].Index)
		}
	}

	return plan
}

func bulkAssignReason(bead bulkBead) string {
	switch bead.Source {
	case bulkSourceImpact:
		return fmt.Sprintf("highest_unblocks (%d items)", bead.UnblocksCount)
	case bulkSourceStale:
		if bead.UpdatedAt.IsZero() {
			return "stale_in_progress (unknown)"
		}
		return fmt.Sprintf("stale_in_progress (%s)", bead.UpdatedAt.UTC().Format(time.RFC3339))
	default:
		if bead.Priority > 0 {
			return fmt.Sprintf("ready_priority P%d", bead.Priority)
		}
		return "ready_priority"
	}
}

func applyBulkAssignPlan(opts BulkAssignOptions, deps BulkAssignDependencies, output *BulkAssignOutput, plan bulkAssignPlan) {
	template, templateErr := loadBulkAssignTemplate(opts, deps)
	if templateErr != nil {
		for i := range plan.Assignments {
			plan.Assignments[i].Status = "failed"
			plan.Assignments[i].Error = templateErr.Error()
			plan.failed++
		}
	}

	needsDetails := strings.Contains(template, "{bead_type}") || strings.Contains(template, "{bead_deps}")

	if opts.Parallel {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for i := range plan.Assignments {
			assignment := &plan.Assignments[i]
			if assignment.Status == "failed" {
				continue
			}
			wg.Add(1)
			go func(a *BulkAssignAssignment) {
				defer wg.Done()
				prompt, err := buildBulkAssignPrompt(template, deps, a, output.Session, needsDetails)
				if err != nil {
					a.Status = "failed"
					a.Error = err.Error()
					a.PromptSent = false
					mu.Lock()
					plan.failed++
					mu.Unlock()
					return
				}

				if opts.DryRun {
					a.Status = "planned"
					a.PromptSent = false
					return
				}

				paneID := fmt.Sprintf("%s:.%d", output.Session, a.Pane)
				if err := deps.SendKeys(paneID, prompt, true); err != nil {
					a.Status = "failed"
					a.Error = err.Error()
					a.PromptSent = false
					mu.Lock()
					plan.failed++
					mu.Unlock()
					return
				}

				a.Status = "assigned"
				a.PromptSent = true
			}(assignment)
		}
		wg.Wait()
	} else {
		for i := range plan.Assignments {
			assignment := &plan.Assignments[i]
			if assignment.Status == "failed" {
				continue
			}
			prompt, err := buildBulkAssignPrompt(template, deps, assignment, output.Session, needsDetails)
			if err != nil {
				assignment.Status = "failed"
				assignment.Error = err.Error()
				assignment.PromptSent = false
				plan.failed++
				continue
			}
			if opts.DryRun {
				assignment.Status = "planned"
				assignment.PromptSent = false
			} else {
				paneID := fmt.Sprintf("%s:.%d", output.Session, assignment.Pane)
				if err := deps.SendKeys(paneID, prompt, true); err != nil {
					assignment.Status = "failed"
					assignment.Error = err.Error()
					assignment.PromptSent = false
					plan.failed++
					continue
				}
				assignment.Status = "assigned"
				assignment.PromptSent = true
			}

			if opts.Stagger > 0 && i < len(plan.Assignments)-1 {
				time.Sleep(opts.Stagger)
			}
		}
	}

	output.Assignments = append(output.Assignments, plan.Assignments...)
	output.UnassignedBeads = append(output.UnassignedBeads, plan.UnassignedBeads...)
	output.UnassignedPanes = append(output.UnassignedPanes, plan.UnassignedPanes...)

	assigned := 0
	failed := 0
	for _, assignment := range output.Assignments {
		switch assignment.Status {
		case "assigned":
			assigned++
		case "failed":
			failed++
		}
	}

	output.Summary = BulkAssignSummary{
		TotalPanes: len(output.Assignments) + len(output.UnassignedPanes),
		Assigned:   assigned,
		Skipped:    0,
		Failed:     failed,
	}
}

func buildBulkAssignPrompt(template string, deps BulkAssignDependencies, assignment *BulkAssignAssignment, session string, needsDetails bool) (string, error) {
	beadType := ""
	var beadDeps []string
	if needsDetails {
		if deps.FetchBeadDetails == nil {
			return "", fmt.Errorf("bead details fetcher not configured")
		}
		details, err := deps.FetchBeadDetails(getBulkAssignDir(deps), assignment.Bead)
		if err != nil {
			return "", err
		}
		if assignment.BeadTitle == "" {
			assignment.BeadTitle = details.Title
		}
		beadType = details.Type
		beadDeps = details.Dependencies
	}

	return expandBulkAssignTemplate(
		template,
		assignment.Bead,
		assignment.BeadTitle,
		beadType,
		beadDeps,
		session,
		assignment.Pane,
	), nil
}

func loadBulkAssignTemplate(opts BulkAssignOptions, deps BulkAssignDependencies) (string, error) {
	if opts.PromptTemplatePath == "" {
		return defaultBulkAssignTemplate, nil
	}
	data, err := deps.ReadFile(opts.PromptTemplatePath)
	if err != nil {
		return "", fmt.Errorf("failed to read prompt template: %w", err)
	}
	return string(data), nil
}

func expandBulkAssignTemplate(template, beadID, beadTitle, beadType string, beadDeps []string, session string, pane int) string {
	if beadType == "" {
		beadType = "unknown"
	}
	depsValue := formatBulkAssignDeps(beadDeps)
	replacer := strings.NewReplacer(
		"{bead_id}", beadID,
		"{bead_title}", beadTitle,
		"{bead_type}", beadType,
		"{bead_deps}", depsValue,
		"{session}", session,
		"{pane}", strconv.Itoa(pane),
	)
	return replacer.Replace(template)
}

func formatBulkAssignDeps(deps []string) string {
	if len(deps) == 0 {
		return "none"
	}
	return strings.Join(deps, ", ")
}

func parseBulkAssignAllocation(raw string) (map[int]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("allocation JSON is empty")
	}

	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("allocation JSON parse failed: %w", err)
	}

	result := make(map[int]string)
	for k, v := range decoded {
		pane, err := strconv.Atoi(strings.TrimSpace(k))
		if err != nil {
			return nil, fmt.Errorf("invalid pane index %q", k)
		}
		result[pane] = strings.TrimSpace(v)
	}

	return result, nil
}

// decodeBulkAssignTriage parses bv --robot-triage JSON payloads.
func decodeBulkAssignTriage(raw []byte) (*bv.TriageResponse, error) {
	var resp bv.TriageResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func fetchBeadTitle(dir, beadID string) (string, error) {
	details, err := fetchBeadDetails(dir, beadID)
	if err != nil {
		return "", err
	}
	return details.Title, nil
}

func fetchBeadDetails(dir, beadID string) (BeadDetails, error) {
	cmd := exec.Command("br", "show", beadID, "--json")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return BeadDetails{}, fmt.Errorf("br show %s failed: %w", beadID, err)
	}

	var issues []struct {
		Title        string `json:"title"`
		IssueType    string `json:"issue_type"`
		Dependencies []struct {
			ID      string `json:"id"`
			DepType string `json:"dep_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(output, &issues); err != nil {
		return BeadDetails{}, fmt.Errorf("parse br show output: %w", err)
	}
	if len(issues) == 0 || issues[0].Title == "" {
		return BeadDetails{}, fmt.Errorf("bead %s not found", beadID)
	}

	depSet := make(map[string]struct{})
	for _, dep := range issues[0].Dependencies {
		if dep.DepType != "blocks" {
			continue
		}
		if dep.ID != "" {
			depSet[dep.ID] = struct{}{}
		}
	}
	deps := make([]string, 0, len(depSet))
	for id := range depSet {
		deps = append(deps, id)
	}
	sort.Strings(deps)

	return BeadDetails{
		Title:        issues[0].Title,
		Type:         issues[0].IssueType,
		Dependencies: deps,
	}, nil
}

func getBulkAssignDir(deps BulkAssignDependencies) string {
	wd, err := deps.Cwd()
	if err != nil {
		return ""
	}
	return wd
}

func parseBulkAssignSkipPanes(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		value, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid pane index %q", trimmed)
		}
		values = append(values, value)
	}
	return values, nil
}
