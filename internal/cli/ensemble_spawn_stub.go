//go:build !ensemble_experimental
// +build !ensemble_experimental

// Ensemble spawn is gated behind the ensemble_experimental build tag.
// This is intentional: ensemble spawn creates complex multi-branch tmux
// layouts that are still being stabilized. Other ensemble commands
// (modes, run, compare, stop, suggest) work in the default build.
//
// To enable: go build -tags ensemble_experimental ./cmd/ntm

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/output"
)

type ensembleSpawnOptions struct {
	Session       string
	Question      string
	Preset        string
	Modes         []string
	AllowAdvanced bool
	AgentMix      string
	Assignment    string
	Synthesis     string
	BudgetTotal   int
	BudgetPerMode int
	NoCache       bool
	NoInject      bool
	Project       string
}

func newEnsembleSpawnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Spawn a reasoning ensemble session (experimental)",
		Long: `Spawn a reasoning ensemble session.

This command is experimental and requires building with -tags ensemble_experimental.`,
		Example: `  ntm ensemble spawn mysession --preset project-diagnosis --question "What are the main issues?"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ensembleSpawnUnavailable()
		},
	}
	return cmd
}

func bindEnsembleSharedFlags(cmd *cobra.Command, opts *ensembleSpawnOptions) {
	cmd.Flags().StringVar(&opts.Project, "project", "", "Project directory (default: current dir)")
}

func runEnsembleSpawn(cmd *cobra.Command, opts ensembleSpawnOptions) error {
	return ensembleSpawnUnavailable()
}

func resolveEnsembleProjectDir(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
		return dir, nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	return abs, nil
}

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func defaultEnsembleSessionName(projectDir string) string {
	base := filepath.Base(projectDir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "ensemble"
	}
	sanitized := sessionNameSanitizer.ReplaceAllString(base, "-")
	sanitized = strings.Trim(sanitized, "-_")
	if sanitized == "" {
		sanitized = "ensemble"
	}
	return sanitized
}

func uniqueEnsembleSessionName(base string) string {
	return base
}

func ensembleSpawnUnavailable() error {
	err := fmt.Errorf("ensemble spawn is experimental; rebuild with -tags ensemble_experimental")
	if IsJSONOutput() {
		_ = output.PrintJSON(output.NewError(err.Error()))
	}
	return err
}
