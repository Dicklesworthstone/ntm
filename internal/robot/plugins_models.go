package robot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent/ollama"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
)

const (
	pluginsSchemaID      = "ntm:robot:plugins:v1"
	pluginsSchemaVersion = "1.0.0"

	PluginsSchemaID      = pluginsSchemaID
	PluginsSchemaVersion = pluginsSchemaVersion

	modelsSchemaID      = "ntm:robot:models:v1"
	modelsSchemaVersion = "1.0.0"

	ModelsSchemaID      = modelsSchemaID
	ModelsSchemaVersion = modelsSchemaVersion

	ModelDiskLowThreshold = int64(5 * 1024 * 1024 * 1024) // 5 GiB
)

// AgentPluginEntry summarizes an installed agent plugin without exposing secret env values.
type AgentPluginEntry struct {
	Name         string   `json:"name"`
	Alias        string   `json:"alias,omitempty"`
	Command      string   `json:"command"`
	ProbeCommand string   `json:"probe_command,omitempty"`
	Description  string   `json:"description,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	EnvKeys      []string `json:"env_keys,omitempty"`
	HasReadiness bool     `json:"has_readiness"`
}

// CommandPluginEntry summarizes an installed executable command plugin.
type CommandPluginEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Usage       string `json:"usage,omitempty"`
	Description string `json:"description,omitempty"`
}

// PluginsOptions configures GetPlugins.
type PluginsOptions struct {
	ConfigDir string
}

// PluginsOutput is the canonical robot envelope for --robot-plugins and GET /api/v1/plugins.
type PluginsOutput struct {
	RobotResponse
	SchemaID       string               `json:"schema_id"`
	SchemaVersion  string               `json:"schema_version"`
	ConfigDir      string               `json:"config_dir"`
	AgentsDir      string               `json:"agents_dir"`
	CommandsDir    string               `json:"commands_dir"`
	AgentPlugins   []AgentPluginEntry   `json:"agent_plugins"`
	CommandPlugins []CommandPluginEntry `json:"command_plugins"`
	TotalCount     int                  `json:"total_count"`
}

// GetPlugins loads installed agent and command plugins from the active NTM config directory.
func GetPlugins(opts PluginsOptions) (*PluginsOutput, error) {
	configDir := strings.TrimSpace(opts.ConfigDir)
	if configDir == "" {
		if env := strings.TrimSpace(os.Getenv("NTM_CONFIG")); env != "" {
			configDir = filepath.Dir(config.ExpandHome(env))
		} else {
			configDir = filepath.Dir(config.DefaultPath())
		}
	}

	agentsDir := filepath.Join(configDir, "agents")
	commandsDir := filepath.Join(configDir, "commands")

	out := &PluginsOutput{
		RobotResponse:  NewRobotResponse(true),
		SchemaID:       pluginsSchemaID,
		SchemaVersion:  pluginsSchemaVersion,
		ConfigDir:      configDir,
		AgentsDir:      agentsDir,
		CommandsDir:    commandsDir,
		AgentPlugins:   []AgentPluginEntry{},
		CommandPlugins: []CommandPluginEntry{},
	}

	agentPlugins, err := plugins.LoadAgentPlugins(agentsDir)
	if err != nil {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("load agent plugins from %s: %w", agentsDir, err),
			ErrCodeInternalError,
			"Verify permissions and TOML syntax in the agents/ plugin directory",
		)
		return out, nil
	}
	commandPlugins, err := plugins.LoadCommandPlugins(commandsDir)
	if err != nil {
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("load command plugins from %s: %w", commandsDir, err),
			ErrCodeInternalError,
			"Verify permissions in the commands/ plugin directory",
		)
		return out, nil
	}

	for _, p := range agentPlugins {
		var envKeys []string
		for k := range p.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		hasReadiness := len(p.Readiness.IdlePatterns) > 0 ||
			len(p.Readiness.WorkingPatterns) > 0 ||
			len(p.Readiness.ErrorPatterns) > 0
		out.AgentPlugins = append(out.AgentPlugins, AgentPluginEntry{
			Name:         p.Name,
			Alias:        p.Alias,
			Command:      p.Command,
			ProbeCommand: p.ProbeCommand(),
			Description:  p.Description,
			DefaultModel: p.Defaults.Model,
			Tags:         append([]string(nil), p.Defaults.Tags...),
			EnvKeys:      envKeys,
			HasReadiness: hasReadiness,
		})
	}
	sort.Slice(out.AgentPlugins, func(i, j int) bool {
		return out.AgentPlugins[i].Name < out.AgentPlugins[j].Name
	})

	for _, cp := range commandPlugins {
		out.CommandPlugins = append(out.CommandPlugins, CommandPluginEntry{
			Name:        cp.Name,
			Path:        cp.Path,
			Usage:       cp.Usage,
			Description: cp.Description,
		})
	}
	sort.Slice(out.CommandPlugins, func(i, j int) bool {
		return out.CommandPlugins[i].Name < out.CommandPlugins[j].Name
	})

	out.TotalCount = len(out.AgentPlugins) + len(out.CommandPlugins)
	return out, nil
}

// PrintPlugins writes the terminal JSON/TOON response for --robot-plugins.
func PrintPlugins(opts PluginsOptions) error {
	out, err := GetPlugins(opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot plugins failed")
}

func pluginsCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "plugins",
		Flag:        "--robot-plugins",
		Category:    "utility",
		Summary:     "List installed agent and command plugins.",
		Description: "Inspect installed TOML agent plugins and executable command plugins from the active NTM config directory.",
		Examples: []string{
			"ntm --robot-plugins",
		},
	}
}

// =============================================================================
// Local Models (Ollama Inventory + VRAM Recommendation Engine)
// =============================================================================

type modelRecommendationBand struct {
	minGB  float64
	maxGB  float64
	models []string
}

var modelRecommendationBands = []modelRecommendationBand{
	{minGB: 0, maxGB: 4, models: []string{"codellama:7b-instruct", "deepseek-coder:6.7b"}},
	{minGB: 4, maxGB: 8, models: []string{"codellama:13b", "deepseek-coder:6.7b-instruct"}},
	{minGB: 8, maxGB: 16, models: []string{"codellama:34b", "deepseek-coder:33b"}},
	{minGB: 16, maxGB: -1, models: []string{"mixtral:8x7b", "codellama:70b"}},
}

var darwinVRAMRegex = regexp.MustCompile(`(?i)VRAM[^:]*:\s*([0-9]+(?:\.[0-9]+)?)\s*(GB|MB)`)

// LocalModelEntry summarizes a single locally installed Ollama model.
type LocalModelEntry struct {
	Name       string    `json:"name"`
	SizeBytes  int64     `json:"size_bytes"`
	ModifiedAt time.Time `json:"modified_at"`
	Family     string    `json:"family,omitempty"`
	Parameters string    `json:"parameters,omitempty"`
}

// LocalModelsOptions configures GetLocalModels.
type LocalModelsOptions struct {
	Action string // "recommend" (default) or "list"
	Host   string // Optional Ollama host override
}

// LocalModelsOutput is the canonical robot envelope for --robot-models and GET /api/v1/models.
type LocalModelsOutput struct {
	RobotResponse
	SchemaID           string            `json:"schema_id"`
	SchemaVersion      string            `json:"schema_version"`
	Action             string            `json:"action"`
	Host               string            `json:"host,omitempty"`
	ModelCount         int               `json:"model_count,omitempty"`
	TotalSizeBytes     int64             `json:"total_size_bytes,omitempty"`
	DiskFreeBytes      int64             `json:"disk_free_bytes,omitempty"`
	DiskLow            bool              `json:"disk_low,omitempty"`
	CleanupSuggestions []string          `json:"cleanup_suggestions,omitempty"`
	Models             []LocalModelEntry `json:"models"`
	DetectedVRAMGB     float64           `json:"detected_vram_gb,omitempty"`
	VRAMSource         string            `json:"vram_source,omitempty"`
	Recommendations    []string          `json:"recommendations"`
}

// RecommendationsForVRAM returns recommended local coding models for vramGB.
func RecommendationsForVRAM(vramGB float64) []string {
	for _, band := range modelRecommendationBands {
		if vramGB < band.minGB {
			continue
		}
		if band.maxGB < 0 || vramGB < band.maxGB {
			return append([]string(nil), band.models...)
		}
	}
	return append([]string(nil), modelRecommendationBands[len(modelRecommendationBands)-1].models...)
}

// ParseNvidiaSMIMemoryMB parses `nvidia-smi --query-gpu=memory.total` MiB lines and returns the largest GPU memory.
func ParseNvidiaSMIMemoryMB(raw string) float64 {
	var maxMB float64
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v, err := strconv.ParseFloat(line, 64)
		if err != nil {
			continue
		}
		if v > maxMB {
			maxMB = v
		}
	}
	return maxMB
}

// ParseDarwinVRAMGB parses macOS `system_profiler SPDisplaysDataType` output and returns the largest VRAM in GiB.
func ParseDarwinVRAMGB(raw string) float64 {
	matches := darwinVRAMRegex.FindAllStringSubmatch(raw, -1)
	var maxGB float64
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if strings.EqualFold(m[2], "MB") {
			n = n / 1024.0
		}
		if n > maxGB {
			maxGB = n
		}
	}
	return maxGB
}

// DetectVRAMGB inspects NTM_LOCAL_VRAM_GB, nvidia-smi, or macOS system_profiler for GPU VRAM.
func DetectVRAMGB() (float64, string) {
	if env := strings.TrimSpace(os.Getenv("NTM_LOCAL_VRAM_GB")); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil && v > 0 {
			return v, "env:NTM_LOCAL_VRAM_GB"
		}
	}
	if out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output(); err == nil {
		if v := ParseNvidiaSMIMemoryMB(string(out)); v > 0 {
			return v / 1024.0, "nvidia-smi"
		}
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output(); err == nil {
			if v := ParseDarwinVRAMGB(string(out)); v > 0 {
				return v, "system_profiler"
			}
		}
	}
	return 0, ""
}

// ResolveOllamaHost returns the effective Ollama daemon URL.
func ResolveOllamaHost(hostOverride string) string {
	for _, candidate := range []string{
		strings.TrimSpace(hostOverride),
		strings.TrimSpace(os.Getenv("NTM_OLLAMA_HOST")),
		strings.TrimSpace(os.Getenv("OLLAMA_HOST")),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return ollama.DefaultHost
}

// ValidateSafeOllamaHostOverride rejects non-loopback HTTP host overrides unless they match the server's configured Ollama host (preventing SSRF).
func ValidateSafeOllamaHostOverride(hostOverride string) error {
	trimmed := strings.TrimSpace(hostOverride)
	if trimmed == "" {
		return nil
	}
	configured := ResolveOllamaHost("")
	if trimmed == configured {
		return nil
	}
	rawURL := trimmed
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid ollama host %q: %w", trimmed, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported ollama host scheme %q", parsed.Scheme)
	}
	hostname := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if hostname == "localhost" {
		return nil
	}
	if ip := net.ParseIP(hostname); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("host override %q must target loopback (localhost/127.0.0.1/::1) or match configured OLLAMA_HOST", trimmed)
}

// BuildLocalModelEntries sorts Ollama models by size descending and maps them to LocalModelEntry values.
func BuildLocalModelEntries(models []ollama.Model) ([]LocalModelEntry, int64) {
	sorted := append([]ollama.Model(nil), models...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Size == sorted[j].Size {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Size > sorted[j].Size
	})

	var totalSize int64
	entries := make([]LocalModelEntry, 0, len(sorted))
	for _, m := range sorted {
		totalSize += m.Size
		entries = append(entries, LocalModelEntry{
			Name:       m.Name,
			SizeBytes:  m.Size,
			ModifiedAt: m.ModifiedAt,
			Family:     m.Details.Family,
			Parameters: m.Details.ParameterSize,
		})
	}
	return entries, totalSize
}

// SuggestModelCleanup returns up to limit oldest/largest model names as LRU cleanup candidates.
func SuggestModelCleanup(models []ollama.Model, limit int) []string {
	if limit <= 0 || len(models) == 0 {
		return nil
	}
	candidates := append([]ollama.Model(nil), models...)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ModifiedAt.Equal(candidates[j].ModifiedAt) {
			return candidates[i].Size > candidates[j].Size
		}
		return candidates[i].ModifiedAt.Before(candidates[j].ModifiedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Name)
	}
	return out
}

// CheckModelDiskSpace returns free bytes and low-disk flag for path.
func CheckModelDiskSpace(path string) (int64, bool, error) {
	if path == "" {
		path = "."
	}
	if runtime.GOOS == "windows" {
		return 0, false, errors.New("disk free check not supported on windows")
	}
	out, err := exec.Command("df", "-k", path).Output()
	if err != nil {
		return 0, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, false, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, false, fmt.Errorf("unexpected df output")
	}
	availKB, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0, false, err
	}
	freeBytes := availKB * 1024
	return freeBytes, freeBytes > 0 && freeBytes < ModelDiskLowThreshold, nil
}

// GetLocalModels recommends VRAM-appropriate local models or queries the local Ollama daemon inventory.
func GetLocalModels(ctx context.Context, opts LocalModelsOptions) (*LocalModelsOutput, error) {
	action := strings.ToLower(strings.TrimSpace(opts.Action))
	if action == "" {
		action = "recommend"
	}

	vramGB, source := DetectVRAMGB()
	if vramGB <= 0 {
		vramGB = 4
		source = "default"
	}
	recs := RecommendationsForVRAM(vramGB)

	out := &LocalModelsOutput{
		RobotResponse:   NewRobotResponse(true),
		SchemaID:        modelsSchemaID,
		SchemaVersion:   modelsSchemaVersion,
		Action:          action,
		Models:          []LocalModelEntry{},
		DetectedVRAMGB:  vramGB,
		VRAMSource:      source,
		Recommendations: recs,
	}

	switch action {
	case "recommend":
		out.Host = ResolveOllamaHost(opts.Host)
		return out, nil
	case "list":
		host := ResolveOllamaHost(opts.Host)
		out.Host = host
		adapter := ollama.NewAdapter()
		if err := adapter.Connect(host); err != nil {
			out.RobotResponse = NewErrorResponse(
				err,
				ErrCodeDependencyMissing,
				"Start the local Ollama daemon (`ollama serve`) or use --models-action=recommend for offline VRAM guidance",
			)
			return out, nil
		}
		defer func() { _ = adapter.Close() }()

		if ctx == nil {
			ctx = context.Background()
		}
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		models, err := adapter.ListModels(queryCtx)
		cancel()
		if err != nil {
			out.RobotResponse = NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Verify the Ollama daemon is reachable at "+adapter.Host(),
			)
			return out, nil
		}

		entries, totalSize := BuildLocalModelEntries(models)
		diskFree, diskLow, _ := CheckModelDiskSpace(".")
		out.Host = adapter.Host()
		out.Models = entries
		out.ModelCount = len(entries)
		out.TotalSizeBytes = totalSize
		out.DiskFreeBytes = diskFree
		out.DiskLow = diskLow
		out.CleanupSuggestions = SuggestModelCleanup(models, 3)
		return out, nil
	default:
		out.RobotResponse = NewErrorResponse(
			fmt.Errorf("invalid models action %q (expected recommend or list)", opts.Action),
			ErrCodeInvalidFlag,
			"Use --models-action=recommend or --models-action=list",
		)
		return out, nil
	}
}

// PrintLocalModels writes the terminal JSON/TOON response for --robot-models.
func PrintLocalModels(ctx context.Context, opts LocalModelsOptions) error {
	out, err := GetLocalModels(ctx, opts)
	if err != nil {
		return err
	}
	return encodeTerminalRobotOutput(out, out.RobotResponse, "robot models failed")
}

func modelsCapabilityCommand() RobotCommandInfo {
	return RobotCommandInfo{
		Name:        "models",
		Flag:        "--robot-models",
		Category:    "utility",
		Summary:     "Inspect or recommend local Ollama models.",
		Description: "Recommend local coding models based on detected GPU VRAM or list installed Ollama models, storage usage, and LRU cleanup hints.",
		Parameters: []RobotParameter{
			{Name: "models-action", Flag: "--models-action", Type: "string", Required: false, Default: "recommend", Description: "Action: recommend (offline VRAM advisor, default) or list (live Ollama inventory)"},
			{Name: "models-host", Flag: "--models-host", Type: "string", Required: false, Description: "Optional Ollama daemon URL override"},
		},
		Examples: []string{
			"ntm --robot-models",
			"ntm --robot-models --models-action=list",
		},
	}
}
