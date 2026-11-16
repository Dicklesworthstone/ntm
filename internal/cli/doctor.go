package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

var (
	doctorVerbose bool
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate the NTM ecosystem health",
		Long: `Validates that all ecosystem tools and dependencies are properly installed
and configured. Checks:

  - Tool detection (bv, bd, am, cm, cass, s2p)
  - Version compatibility (including optional Grok Build and Oh My Pi discovery)
  - Daemon health and port availability
  - Configuration files

This command helps diagnose issues before spawning sessions.`,
		RunE: runDoctor,
	}

	cmd.Flags().BoolVarP(&doctorVerbose, "verbose", "v", false, "Show detailed output")
	cmd.AddCommand(newDoctorContentionCmd())

	return cmd
}

type contentionCommandOptions struct {
	terminatePID int
	confirm      bool
	force        bool
}

// ContentionHolder aliases the canonical ORI contention holder type in internal/robot.
type ContentionHolder = robot.DoctorContentionHolder

func newDoctorContentionCmd() *cobra.Command {
	opts := contentionCommandOptions{}
	cmd := &cobra.Command{
		Use:   "contention",
		Short: "Find processes holding shared build and Beads resources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			holders, err := findContentionHolders(ctx, cwd)
			if err != nil {
				return err
			}
			if opts.terminatePID > 0 {
				if err := terminateContentionHolder(ctx, holders, opts); err != nil {
					return err
				}
			}
			if IsJSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Holders []ContentionHolder `json:"holders"`
				}{Holders: holders})
			}
			if len(holders) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No shared-resource contention detected.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "PID\tAGE\tRESOURCE\tIN_SWARM\tCWD\tCOMMAND")
			for _, holder := range holders {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t%t\t%s\t%s\n", holder.PID, holder.Age, holder.Resource, holder.InSwarm, holder.CWD, holder.Command)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&opts.terminatePID, "terminate-pid", 0, "Send SIGTERM to a listed PID (requires --confirm)")
	cmd.Flags().BoolVar(&opts.confirm, "confirm", false, "Confirm termination of --terminate-pid")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Allow termination of an in-swarm PID (requires --confirm)")
	return cmd
}

func findContentionHolders(ctx context.Context, swarmCWD string) ([]ContentionHolder, error) {
	return robot.FindContentionHolders(ctx, swarmCWD)
}

func terminateContentionHolder(ctx context.Context, holders []ContentionHolder, opts contentionCommandOptions) error {
	return robot.TerminateContentionHolder(ctx, holders, opts.terminatePID, opts.confirm, opts.force)
}

// DoctorReport contains the full health check report for `ntm doctor`.
type DoctorReport struct {
	Timestamp      time.Time        `json:"timestamp"`
	Overall        string           `json:"overall"` // "healthy", "warning", "unhealthy"
	SafetyDefaults SafetyDefaults   `json:"safety_defaults"`
	Tools          []ToolCheck      `json:"tools"`
	Dependencies   []DepCheck       `json:"dependencies"`
	Daemons        []DaemonCheck    `json:"daemons"`
	Configuration  []ConfigCheck    `json:"configuration"`
	Invariants     []InvariantCheck `json:"invariants"`
	GuardHook      *GuardHookCheck  `json:"guard_hook,omitempty"`
	Warnings       int              `json:"warnings"`
	Errors         int              `json:"errors"`
}

// SafetyDefaults aliases the canonical ORI safety defaults struct in internal/robot.
type SafetyDefaults = robot.DoctorSafetyDefaults

// InvariantCheck aliases the canonical ORI invariant check struct in internal/robot.
type InvariantCheck = robot.DoctorInvariantCheck

// ToolCheck aliases the canonical ORI tool check struct in internal/robot.
type ToolCheck = robot.DoctorToolCheck

// DepCheck aliases the canonical ORI dependency check struct in internal/robot.
type DepCheck = robot.DoctorDepCheck

// DaemonCheck aliases the canonical ORI daemon check struct in internal/robot.
type DaemonCheck = robot.DoctorDaemonCheck

// ConfigCheck aliases the canonical ORI configuration check struct in internal/robot.
type ConfigCheck = robot.DoctorConfigCheck

// GuardHookCheck aliases the canonical ORI guard-hook check struct in internal/robot.
type GuardHookCheck = robot.DoctorGuardHookCheck

// doctorExitFailure reports whether an overall verdict must produce a non-zero
// process exit.
func doctorExitFailure(overall string) bool {
	return overall == "unhealthy"
}

// doctorCheck is the seam tests use to drive runDoctor with a chosen verdict;
// production always runs the real probe. Mirrors healthKernelRun in health.go.
var doctorCheck = performDoctorCheck

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := doctorCheck(ctx)

	if IsJSONOutput() {
		if err := outputDoctorJSON(report); err != nil {
			return err
		}
		if doctorExitFailure(report.Overall) {
			return errJSONFailure
		}
		return nil
	}

	return renderDoctorTUI(report)
}

func performDoctorCheck(ctx context.Context) *DoctorReport {
	out, _ := robot.GetDoctor(ctx, robot.DoctorOptions{
		Verbose:    doctorVerbose,
		Config:     cfg,
		ConfigPath: selectedConfigPath(),
	})
	if out == nil {
		return &DoctorReport{
			Timestamp:      time.Now(),
			Overall:        "unhealthy",
			SafetyDefaults: robot.BuildSafetyDefaults(cfg),
			Tools:          []ToolCheck{},
			Dependencies:   []DepCheck{},
			Daemons:        []DaemonCheck{},
			Configuration:  []ConfigCheck{},
			Invariants:     []InvariantCheck{},
			Errors:         1,
		}
	}
	return &DoctorReport{
		Timestamp:      time.Now(),
		Overall:        out.Overall,
		SafetyDefaults: out.SafetyDefaults,
		Tools:          out.Tools,
		Dependencies:   out.Dependencies,
		Daemons:        out.Daemons,
		Configuration:  out.Configuration,
		Invariants:     out.Invariants,
		GuardHook:      out.GuardHook,
		Warnings:       out.Warnings,
		Errors:         out.Errors,
	}
}

func outputDoctorJSON(report *DoctorReport) error {
	return encodeDoctorJSON(os.Stdout, report)
}

func encodeDoctorJSON(w io.Writer, report *DoctorReport) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func renderDoctorTUI(report *DoctorReport) error {
	return renderDoctorTUITo(os.Stdout, report)
}

func renderDoctorTUITo(w io.Writer, report *DoctorReport) error {
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("99"))

	sectionStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("75")).
		MarginTop(1)

	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))     // Green
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))  // Orange
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")) // Red
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")) // Gray

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		MarginTop(1)

	statusIcon := func(s string) string {
		switch s {
		case "ok":
			return okStyle.Render("✓")
		case "warning":
			return warnStyle.Render("⚠")
		case "error":
			return errorStyle.Render("✗")
		default:
			return mutedStyle.Render("?")
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, titleStyle.Render("NTM Doctor"))
	fmt.Fprintln(w)

	fmt.Fprintln(w, sectionStyle.Render("Tools:"))
	for _, t := range report.Tools {
		icon := statusIcon(t.Status)
		if t.Installed {
			caps := ""
			if len(t.Capabilities) > 0 && doctorVerbose {
				caps = fmt.Sprintf(" (%v)", t.Capabilities)
			}
			fmt.Fprintf(w, "  %s %s %s%s\n", icon, t.Name, mutedStyle.Render(t.Message), caps)
			if doctorVerbose {
				for _, detail := range t.Details {
					fmt.Fprintf(w, "      %s\n", mutedStyle.Render(detail))
				}
			}
		} else {
			msg := t.Message
			if t.Required {
				msg = errorStyle.Render(msg)
			} else {
				msg = mutedStyle.Render(msg)
			}
			fmt.Fprintf(w, "  %s %s %s\n", icon, t.Name, msg)
		}
	}

	fmt.Fprintln(w, sectionStyle.Render("Dependencies:"))
	for _, d := range report.Dependencies {
		icon := statusIcon(d.Status)
		version := ""
		if d.Version != "" {
			version = mutedStyle.Render(fmt.Sprintf(" (%s)", d.Version))
		}
		fmt.Fprintf(w, "  %s %s%s\n", icon, d.Name, version)
	}

	fmt.Fprintln(w, sectionStyle.Render("Daemons:"))
	for _, d := range report.Daemons {
		icon := statusIcon(d.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, d.Name, mutedStyle.Render(d.Message))
	}

	fmt.Fprintln(w, sectionStyle.Render("Configuration:"))
	for _, c := range report.Configuration {
		icon := statusIcon(c.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, c.Name, mutedStyle.Render(c.Message))
	}

	fmt.Fprintln(w, sectionStyle.Render("Safety Defaults:"))
	redactionStatus := statusIcon("ok")
	if report.SafetyDefaults.RedactionMode == "off" {
		redactionStatus = statusIcon("warning")
	}
	allowlistSummary := "none"
	if report.SafetyDefaults.RedactionAllowlistEnabled {
		allowlistSummary = fmt.Sprintf("%d entries", report.SafetyDefaults.RedactionAllowlistCount)
	}
	fmt.Fprintf(w, "  %s Redaction mode: %s (allowlist: %s)\n",
		redactionStatus,
		mutedStyle.Render(report.SafetyDefaults.RedactionMode),
		mutedStyle.Render(allowlistSummary),
	)

	privacyStatus := statusIcon("ok")
	privacyLabel := "disabled"
	if report.SafetyDefaults.PrivacyDefaultEnabled {
		privacyLabel = "enabled"
	}
	fmt.Fprintf(w, "  %s Privacy default: %s\n", privacyStatus, mutedStyle.Render(privacyLabel))

	encryptionStatus := statusIcon("ok")
	encryptionLabel := "disabled"
	if report.SafetyDefaults.EncryptionAtRestEnabled {
		encryptionLabel = "enabled"
	}
	fmt.Fprintf(w, "  %s Encryption at rest: %s\n", encryptionStatus, mutedStyle.Render(encryptionLabel))

	preflightStatus := statusIcon("ok")
	strictLabel := "strict=off"
	if report.SafetyDefaults.PreflightDefaultStrict {
		strictLabel = "strict=on"
	}
	fmt.Fprintf(w, "  %s Prompt preflight: %s\n", preflightStatus, mutedStyle.Render(strictLabel))

	hooksStatus, hooksLabel := statusIcon("ok"), ""
	switch {
	case report.SafetyDefaults.ClaudeAgentHooksWarning != "":
		hooksStatus, hooksLabel = statusIcon("warning"), "unprotected: "+report.SafetyDefaults.ClaudeAgentHooksWarning
	case len(report.SafetyDefaults.ClaudeAgentHooks) > 0:
		hooksLabel = strings.Join(report.SafetyDefaults.ClaudeAgentHooks, ", ") + " via --settings"
		if report.SafetyDefaults.ClaudePolicyHookRegistered {
			hooksLabel += "; policy hook registered in ~/.claude/settings.json"
		}
	default:
		hooksLabel = "policy hook registered in ~/.claude/settings.json"
	}
	fmt.Fprintf(w, "  %s Claude agent hooks: %s\n", hooksStatus, mutedStyle.Render(hooksLabel))

	fmt.Fprintln(w, sectionStyle.Render("Design Invariants:"))
	for _, i := range report.Invariants {
		icon := statusIcon(i.Status)
		fmt.Fprintf(w, "  %s %s %s\n", icon, i.Name, mutedStyle.Render(i.Message))
		if doctorVerbose && len(i.Details) > 0 {
			for _, detail := range i.Details {
				fmt.Fprintf(w, "      %s\n", mutedStyle.Render(detail))
			}
		}
	}

	var statusMsg string
	var statusStyle lipgloss.Style
	switch report.Overall {
	case "healthy":
		statusMsg = "Overall: HEALTHY"
		statusStyle = okStyle
	case "warning":
		statusMsg = fmt.Sprintf("Overall: HEALTHY (%d warnings)", report.Warnings)
		statusStyle = warnStyle
	case "unhealthy":
		statusMsg = fmt.Sprintf("Overall: UNHEALTHY (%d errors, %d warnings)", report.Errors, report.Warnings)
		statusStyle = errorStyle
	}
	fmt.Fprintln(w, boxStyle.Render(statusStyle.Render(statusMsg)))
	fmt.Fprintln(w)

	if doctorExitFailure(report.Overall) {
		return errors.New("ecosystem is unhealthy")
	}
	return nil
}
