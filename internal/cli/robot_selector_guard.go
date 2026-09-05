package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

// robotPaneSelectorFlags are the root-level pane targeting flags. They are
// shared across many robot surfaces, but each surface reads only the ones it
// declares in the robot registry. An explicitly supplied selector that the
// invoked surface does not read must fail closed before dispatch: silently
// ignoring it turns a single-pane request into a session-wide action
// (ntm#308: --robot-interrupt --pane=%12 interrupted every agent pane).
//
// Each entry lists the canonical registry flag it stands for; deprecated
// prefixed aliases (--history-pane, --wait-panes) resolve to their canonical
// flag only on the surface whose prefix they carry.
var robotPaneSelectorFlags = []struct {
	name      string
	canonical string
}{
	{name: "pane", canonical: "pane"},
	{name: "panes", canonical: "panes"},
	{name: "history-pane", canonical: "pane"},
	{name: "wait-panes", canonical: "panes"},
}

// robotPaneSelectorFlagError reports an explicitly supplied pane selector that
// the invoked robot surface does not read.
type robotPaneSelectorFlagError struct {
	flag    string // the flag the caller supplied, without dashes
	command string // robot command, e.g. "robot-interrupt"
	hint    string
}

func (e *robotPaneSelectorFlagError) Error() string {
	return fmt.Sprintf("--%s is not supported with --%s; it was not applied", e.flag, e.command)
}

// validateRobotPaneSelectorFlags fails closed when a pane selector flag was
// explicitly set for a robot surface that does not declare it. Surfaces the
// registry does not know about are left alone: there is nothing to validate
// against, and the surface's own handler owns its flag contract.
func validateRobotPaneSelectorFlags(cmd *cobra.Command, robotCommand string) error {
	if cmd == nil || robotCommand == "" {
		return nil
	}
	flags := cmd.Flags()
	for _, selector := range robotPaneSelectorFlags {
		if !flags.Changed(selector.name) {
			continue
		}
		if robotSurfaceReadsSelector(robotCommand, selector.name, selector.canonical) {
			continue
		}
		value, _ := flags.GetString(selector.name)
		return &robotPaneSelectorFlagError{
			flag:    selector.name,
			command: robotCommand,
			hint:    robotPaneSelectorHint(robotCommand, selector.name, selector.canonical, strings.TrimSpace(value)),
		}
	}
	return nil
}

// robotSurfaceReadsSelector reports whether the surface behind robotCommand
// reads the given selector flag. A deprecated prefixed alias
// (e.g. --wait-panes) counts only on its own surface (--robot-wait), where
// the resolver falls back to the canonical flag.
func robotSurfaceReadsSelector(robotCommand, flagName, canonical string) bool {
	accepts, known := robot.SurfaceParameterSupport(robotCommand, flagName)
	if !known || accepts {
		return true // unknown surface: nothing to validate against
	}
	if flagName == canonical {
		return false
	}
	// Alias form: --<surface>-<canonical> is honoured by --robot-<surface>.
	surface := strings.TrimPrefix(robotCommand, "robot-")
	if strings.TrimPrefix(flagName, surface+"-") != canonical {
		return false
	}
	return surfaceAcceptsSelector(robotCommand, canonical)
}

func robotPaneSelectorHint(robotCommand, flagName, canonical, value string) string {
	// Point at the selector the surface does read: the canonical form of a
	// misapplied alias, else the sibling of the other cardinality.
	target := ""
	switch {
	case flagName != canonical && surfaceAcceptsSelector(robotCommand, canonical):
		target = canonical
	case canonical == "pane" && surfaceAcceptsSelector(robotCommand, "panes"):
		target = "panes"
	case canonical == "panes" && surfaceAcceptsSelector(robotCommand, "pane"):
		target = "pane"
	}
	if target != "" {
		example := "--" + target
		switch {
		case target == "pane" && strings.Contains(value, ","):
			example += "=<one selector>"
		case value != "":
			example += "=" + value
		}
		return fmt.Sprintf("Use %s with --%s (--%s selects %s)", example, robotCommand, target, selectorCardinality(target))
	}
	accepting := robot.SurfaceFlagsAcceptingParameter(flagName)
	if len(accepting) == 0 {
		return fmt.Sprintf("--%s does not accept a pane selector; remove --%s", robotCommand, flagName)
	}
	return fmt.Sprintf("--%s is only read by %s; remove it from --%s", flagName, strings.Join(accepting, ", "), robotCommand)
}

func surfaceAcceptsSelector(robotCommand, flagName string) bool {
	accepts, _ := robot.SurfaceParameterSupport(robotCommand, flagName)
	return accepts
}

func selectorCardinality(flagName string) string {
	if flagName == "pane" {
		return "exactly one pane"
	}
	return "a comma-separated set of panes"
}
