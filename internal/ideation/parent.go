package ideation

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultParentResolutionTimeout = 6 * time.Second

// Parent resolution sources recorded in the queue-dry ideation envelope.
const (
	ParentSourceFlag         = "flag"
	ParentSourceDetectedEpic = "detected_epic"
	ParentSourceNone         = "none"
	ParentSourceAmbiguous    = "ambiguous"
)

// ParentResolution reports how the roadmap parent bead was chosen. The
// resolver never guesses: an explicit flag wins, a single open epic in the
// TARGET project's beads database is used, and anything else (zero epics,
// multiple epics, or an unreadable database) yields no parent; ambiguity and
// resolution failures additionally carry an explanatory warning.
type ParentResolution struct {
	ParentID   string   `json:"parent_id,omitempty"`
	Source     string   `json:"source"`
	Candidates []string `json:"candidates,omitempty"`
	Warning    string   `json:"warning,omitempty"`
}

// ParentResolutionOptions configures ResolveRoadmapParent.
type ParentResolutionOptions struct {
	// ProjectDir is the TARGET project whose beads database is inspected.
	ProjectDir string
	// ExplicitParent short-circuits detection when non-empty (--parent flag).
	ExplicitParent string
	CommandTimeout time.Duration
	Runner         CommandRunner
}

// ResolveRoadmapParent picks the parent bead for queue-dry ideation output.
// Resolution order: explicit flag > exactly one open epic in the target
// project's beads DB > no parent (with a warning naming any ambiguity).
func ResolveRoadmapParent(ctx context.Context, opts ParentResolutionOptions) ParentResolution {
	if explicit := strings.TrimSpace(opts.ExplicitParent); explicit != "" {
		return ParentResolution{ParentID: explicit, Source: ParentSourceFlag}
	}

	dir := strings.TrimSpace(opts.ProjectDir)
	if dir == "" {
		return ParentResolution{
			Source:  ParentSourceNone,
			Warning: "parent_resolution: no target project directory; creating beads without a parent (pass --parent to set one)",
		}
	}
	// br auto-discovers any .beads/*.db, not only the default beads.db, so
	// gate on the same pattern (W1 gate finding on bd-ws1-truth-safety-l5ddi.4).
	if matches, err := filepath.Glob(filepath.Join(dir, ".beads", "*.db")); err != nil || len(matches) == 0 {
		return ParentResolution{
			Source:  ParentSourceNone,
			Warning: "parent_resolution: target project has no readable beads database (.beads/*.db); creating beads without a parent (pass --parent to set one)",
		}
	}

	timeout := opts.CommandTimeout
	if timeout <= 0 {
		timeout = defaultParentResolutionTimeout
	}
	runner := opts.Runner
	if runner == nil {
		runner = ExecCommandRunner{OutputLimitBytes: defaultCollectorOutputLimit}
	}
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := runner.Run(listCtx, dir, "br", []string{
		"list", "--status", "open", "--type", "epic", "--limit", "0",
		"--json", "--no-auto-flush", "--no-auto-import",
	})
	if err != nil {
		return ParentResolution{
			Source:  ParentSourceNone,
			Warning: fmt.Sprintf("parent_resolution: could not list open epics in target project (%v); creating beads without a parent (pass --parent to set one)", err),
		}
	}
	issues, err := parseBRIssues(output)
	if err != nil {
		return ParentResolution{
			Source:  ParentSourceNone,
			Warning: fmt.Sprintf("parent_resolution: could not parse open-epic listing from target project (%v); creating beads without a parent (pass --parent to set one)", err),
		}
	}

	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		if id := strings.TrimSpace(issue.ID); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	switch len(ids) {
	case 0:
		return ParentResolution{Source: ParentSourceNone}
	case 1:
		return ParentResolution{ParentID: ids[0], Source: ParentSourceDetectedEpic, Candidates: ids}
	default:
		return ParentResolution{
			Source:     ParentSourceAmbiguous,
			Candidates: ids,
			Warning: fmt.Sprintf("parent_resolution: multiple open epics in target project (%s); creating beads without a parent — pass --parent to choose one",
				strings.Join(ids, ", ")),
		}
	}
}
