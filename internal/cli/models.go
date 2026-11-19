package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/agent/ollama"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	defaultModelsPullTimeout = 30 * time.Minute
	modelDiskLowThreshold    = robot.ModelDiskLowThreshold
)

type modelsListEntry = robot.LocalModelEntry

type modelsListOutput struct {
	Host               string            `json:"host"`
	ModelCount         int               `json:"model_count"`
	TotalSizeBytes     int64             `json:"total_size_bytes"`
	DiskFreeBytes      int64             `json:"disk_free_bytes,omitempty"`
	DiskLow            bool              `json:"disk_low"`
	CleanupSuggestions []string          `json:"cleanup_suggestions,omitempty"`
	Models             []modelsListEntry `json:"models"`
}

type modelsRecommendOutput struct {
	DetectedVRAMGB  float64  `json:"detected_vram_gb"`
	Source          string   `json:"source"`
	Recommendations []string `json:"recommendations"`
}

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Manage local Ollama models",
		Long: `Manage Ollama models used by local NTM agents.

Subcommands:
  list       List installed local models and storage usage
  pull       Download a model from the Ollama registry
  remove     Delete a local model to free space
  recommend  Recommend models based on detected VRAM`,
	}

	cmd.AddCommand(newModelsListCmd())
	cmd.AddCommand(newModelsPullCmd())
	cmd.AddCommand(newModelsRemoveCmd())
	cmd.AddCommand(newModelsRecommendCmd())

	return cmd
}

func newModelsListCmd() *cobra.Command {
	var host string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List local Ollama models",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsList(host)
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Ollama host URL (overrides NTM_OLLAMA_HOST/OLLAMA_HOST)")
	return cmd
}

func newModelsPullCmd() *cobra.Command {
	var host string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "pull <model>",
		Short: "Pull a model from the Ollama registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsPull(host, args[0], timeout)
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Ollama host URL (overrides NTM_OLLAMA_HOST/OLLAMA_HOST)")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultModelsPullTimeout, "Maximum time to wait for model download")
	return cmd
}

func newModelsRemoveCmd() *cobra.Command {
	var host string
	var yes bool

	cmd := &cobra.Command{
		Use:     "remove <model>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a local Ollama model",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsRemove(host, args[0], yes)
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Ollama host URL (overrides NTM_OLLAMA_HOST/OLLAMA_HOST)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func newModelsRecommendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recommend",
		Short: "Recommend local models based on detected VRAM",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runModelsRecommend()
		},
	}
}

func runModelsList(hostOverride string) error {
	adapter, err := connectOllamaAdapter(hostOverride)
	if err != nil {
		return err
	}
	defer func() { _ = adapter.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	models, err := adapter.ListModels(ctx)
	cancel()
	if err != nil {
		return err
	}

	entries, totalSize := robot.BuildLocalModelEntries(models)
	diskFreeBytes, diskErr := detectDiskFreeBytes(".")
	diskLow := diskErr == nil && diskFreeBytes > 0 && diskFreeBytes < modelDiskLowThreshold
	cleanupSuggestions := suggestModelCleanup(models, 3)

	if IsJSONOutput() {
		out := modelsListOutput{
			Host:               adapter.Host(),
			ModelCount:         len(models),
			TotalSizeBytes:     totalSize,
			DiskFreeBytes:      diskFreeBytes,
			DiskLow:            diskLow,
			CleanupSuggestions: cleanupSuggestions,
			Models:             entries,
		}
		return output.PrintJSON(out)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\tSIZE\tMODIFIED\tFAMILY\tPARAMETERS\n")
	fmt.Fprintf(w, "----\t----\t--------\t------\t----------\n")
	for _, m := range entries {
		modified := "-"
		if !m.ModifiedAt.IsZero() {
			modified = m.ModifiedAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			util.FormatBytes(m.SizeBytes),
			modified,
			blankDash(m.Family),
			blankDash(m.Parameters),
		)
	}
	_ = w.Flush()

	fmt.Printf("\nHost:         %s\n", adapter.Host())
	fmt.Printf("Model count:  %d\n", len(entries))
	fmt.Printf("Total size:   %s\n", util.FormatBytes(totalSize))
	if diskErr == nil && diskFreeBytes > 0 {
		fmt.Printf("Disk free:    %s\n", util.FormatBytes(diskFreeBytes))
		if diskLow {
			output.PrintWarningf("Low disk space: %s free (threshold: %s)", util.FormatBytes(diskFreeBytes), util.FormatBytes(modelDiskLowThreshold))
		}
	}
	if len(cleanupSuggestions) > 0 {
		fmt.Printf("Cleanup hint: oldest model(s): %s\n", strings.Join(cleanupSuggestions, ", "))
	}

	return nil
}

func runModelsPull(hostOverride, model string, timeout time.Duration) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model name is required")
	}
	if !modelPattern.MatchString(model) {
		return fmt.Errorf("invalid model name %q; allowed: letters, numbers, . _ / @ : + -", model)
	}

	adapter, err := connectOllamaAdapter(hostOverride)
	if err != nil {
		return err
	}
	defer func() { _ = adapter.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if IsJSONOutput() {
		if err := adapter.PullModel(ctx, model); err != nil {
			return err
		}
		return output.PrintJSON(map[string]any{
			"success": true,
			"model":   model,
			"host":    adapter.Host(),
		})
	}

	fmt.Printf("Pulling %s from %s\n", model, adapter.Host())
	progress := newOllamaPullProgressPrinter("  ")
	if err := adapter.PullModelWithProgress(ctx, model, progress); err != nil {
		return err
	}
	fmt.Printf("Model %s is ready.\n", model)
	return nil
}

func runModelsRemove(hostOverride, model string, yes bool) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model name is required")
	}
	if !modelPattern.MatchString(model) {
		return fmt.Errorf("invalid model name %q; allowed: letters, numbers, . _ / @ : + -", model)
	}

	if !yes && !IsJSONOutput() {
		prompt := fmt.Sprintf("Remove local model %q?", model)
		if !output.ConfirmWithOptions(prompt, output.ConfirmOptions{Style: output.StyleDestructive, Default: false}) {
			return errors.New("aborted")
		}
	}

	adapter, err := connectOllamaAdapter(hostOverride)
	if err != nil {
		return err
	}
	defer func() { _ = adapter.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := adapter.DeleteModel(ctx, model); err != nil {
		return err
	}

	if IsJSONOutput() {
		return output.PrintJSON(map[string]any{
			"success": true,
			"model":   model,
			"host":    adapter.Host(),
		})
	}

	fmt.Printf("Removed model %s from %s\n", model, adapter.Host())
	return nil
}

func runModelsRecommend() error {
	vramGB, source := detectVRAMGB()
	if vramGB <= 0 {
		vramGB = 4
		source = "default"
	}
	recommendations := recommendationsForVRAM(vramGB)

	if IsJSONOutput() {
		return output.PrintJSON(modelsRecommendOutput{
			DetectedVRAMGB:  vramGB,
			Source:          source,
			Recommendations: recommendations,
		})
	}

	fmt.Printf("Detected VRAM: %.1f GB (source: %s)\n", vramGB, source)
	fmt.Printf("Recommended models:\n")
	for _, model := range recommendations {
		fmt.Printf("  - %s\n", model)
	}
	if source == "default" {
		output.PrintWarningf("Could not detect GPU VRAM automatically; used conservative defaults.")
	}
	return nil
}

func connectOllamaAdapter(hostOverride string) (*ollama.Adapter, error) {
	host := resolveOllamaHost(hostOverride)
	adapter := ollama.NewAdapter()
	if err := adapter.Connect(host); err != nil {
		return nil, err
	}
	return adapter, nil
}

func resolveOllamaHost(hostOverride string) string {
	return robot.ResolveOllamaHost(hostOverride)
}

func recommendationsForVRAM(vramGB float64) []string {
	return robot.RecommendationsForVRAM(vramGB)
}

func detectVRAMGB() (float64, string) {
	if env := strings.TrimSpace(os.Getenv("NTM_LOCAL_VRAM_GB")); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil && v > 0 {
			return v, "env:NTM_LOCAL_VRAM_GB"
		}
	}

	if out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		if v := parseNvidiaSMIMemoryMB(string(out)); v > 0 {
			return v / 1024.0, "nvidia-smi"
		}
	}

	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output(); err == nil {
			if v := parseDarwinVRAMGB(string(out)); v > 0 {
				return v, "system_profiler"
			}
		}
	}

	return 0, ""
}

func parseNvidiaSMIMemoryMB(raw string) float64 {
	return robot.ParseNvidiaSMIMemoryMB(raw)
}

func parseDarwinVRAMGB(raw string) float64 {
	return robot.ParseDarwinVRAMGB(raw)
}

func detectDiskFreeBytes(path string) (int64, error) {
	freeBytes, _, err := robot.CheckModelDiskSpace(path)
	return freeBytes, err
}

func suggestModelCleanup(models []ollama.Model, limit int) []string {
	return robot.SuggestModelCleanup(models, limit)
}

func blankDash(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "-"
	}
	return v
}

func newOllamaPullProgressPrinter(prefix string) func(ollama.ModelPullProgress) {
	lastStatus := ""
	lastPercent := -5
	return func(p ollama.ModelPullProgress) {
		status := strings.TrimSpace(p.Status)
		if status == "" {
			status = "pulling"
		}

		if p.Total > 0 {
			percent := int(float64(p.Completed) * 100 / float64(p.Total))
			if !p.Done && status == lastStatus && percent < lastPercent+5 {
				return
			}
			lastStatus = status
			lastPercent = percent
			fmt.Printf("%s%s %d%% (%s/%s)\n", prefix, status, percent, util.FormatBytes(p.Completed), util.FormatBytes(p.Total))
			return
		}

		if !p.Done && status == lastStatus {
			return
		}
		lastStatus = status
		fmt.Printf("%s%s\n", prefix, status)
	}
}
