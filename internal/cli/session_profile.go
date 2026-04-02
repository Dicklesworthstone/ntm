package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

// SessionProfile is the TOML-serializable spawn configuration stored in a profile.
type SessionProfile struct {
	CC        int    `toml:"cc,omitempty" json:"cc,omitempty"`
	Cod       int    `toml:"cod,omitempty" json:"cod,omitempty"`
	Gmi       int    `toml:"gmi,omitempty" json:"gmi,omitempty"`
	Ollama    int    `toml:"ollama,omitempty" json:"ollama,omitempty"`
	Cursor    int    `toml:"cursor,omitempty" json:"cursor,omitempty"`
	Windsurf  int    `toml:"windsurf,omitempty" json:"windsurf,omitempty"`
	Aider     int    `toml:"aider,omitempty" json:"aider,omitempty"`
	UserPane  *bool  `toml:"user_pane,omitempty" json:"user_pane,omitempty"`
	Prompt    string `toml:"prompt,omitempty" json:"prompt,omitempty"`
	InitFile  string `toml:"init_file,omitempty" json:"init_file,omitempty"`
	Safety    *bool  `toml:"safety,omitempty" json:"safety,omitempty"`
	Worktrees *bool  `toml:"worktrees,omitempty" json:"worktrees,omitempty"`
}

var validProfileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func validateSessionProfileName(name string) error {
	if !validProfileName.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: must be alphanumeric with hyphens/underscores", name)
	}
	return nil
}

func (p SessionProfile) Validate() error {
	counts := map[string]int{
		"cc":       p.CC,
		"cod":      p.Cod,
		"gmi":      p.Gmi,
		"ollama":   p.Ollama,
		"cursor":   p.Cursor,
		"windsurf": p.Windsurf,
		"aider":    p.Aider,
	}
	for name, count := range counts {
		if count < 0 {
			return fmt.Errorf("%s count cannot be negative", name)
		}
	}
	return nil
}

func decodeSessionProfile(name string, data []byte) (*SessionProfile, error) {
	var cfg SessionProfile
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parsing profile %q: %w", name, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		fields := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			fields = append(fields, key.String())
		}
		sort.Strings(fields)
		return nil, fmt.Errorf("parsing profile %q: unknown field(s): %s", name, strings.Join(fields, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid profile %q: %w", name, err)
	}
	return &cfg, nil
}

// sessionProfileDir returns the directory where profiles are stored.
func sessionProfileDir() string {
	return filepath.Join(selectedConfigDir(), "profiles")
}

// sessionProfileDirFunc allows tests to override the profile directory.
var sessionProfileDirFunc = sessionProfileDir

func sessionProfilePath(name string) string {
	return filepath.Join(sessionProfileDirFunc(), name+".toml")
}

// SaveSessionProfile writes a profile to disk.
func SaveSessionProfile(name string, cfg SessionProfile) error {
	if err := validateSessionProfileName(name); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid profile %q: %w", name, err)
	}
	dir := sessionProfileDirFunc()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating profiles directory: %w", err)
	}
	f, err := os.Create(sessionProfilePath(name))
	if err != nil {
		return fmt.Errorf("creating profile file: %w", err)
	}
	defer f.Close()
	enc := toml.NewEncoder(f)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encoding profile: %w", err)
	}
	return nil
}

// LoadSessionProfile reads a profile from disk.
func LoadSessionProfile(name string) (*SessionProfile, error) {
	if err := validateSessionProfileName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(sessionProfilePath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("profile %q not found", name)
		}
		return nil, fmt.Errorf("reading profile: %w", err)
	}
	return decodeSessionProfile(name, data)
}

// ListSessionProfiles returns the names of all saved profiles (sorted).
func ListSessionProfiles() ([]string, error) {
	dir := sessionProfileDirFunc()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading profiles directory: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		if err := validateSessionProfileName(name); err != nil {
			return nil, fmt.Errorf("invalid profile file %q: %w", e.Name(), err)
		}
		if _, err := LoadSessionProfile(name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// DeleteSessionProfile removes a profile from disk.
func DeleteSessionProfile(name string) error {
	if err := validateSessionProfileName(name); err != nil {
		return err
	}
	path := sessionProfilePath(name)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("profile %q not found", name)
		}
		return fmt.Errorf("deleting profile: %w", err)
	}
	return nil
}

// newSessionProfileCmd creates the `ntm profile` subcommand with save/list/delete/show.
func newSessionProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage session spawn profiles",
		Long:  "Save, list, and delete reusable spawn configurations as named profiles.",
	}

	cmd.AddCommand(newSessionProfileSaveCmd())
	cmd.AddCommand(newSessionProfileListCmd())
	cmd.AddCommand(newSessionProfileDeleteCmd())
	cmd.AddCommand(newSessionProfileShowCmd())

	return cmd
}

func newSessionProfileSaveCmd() *cobra.Command {
	var (
		cc, cod, gmi, ollamaCount int
		cursorCount, wsCount      int
		aiderCount                int
		userPane, safety, wt      bool
		prompt, initFile          string
	)

	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Save a spawn configuration as a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			cfg := SessionProfile{
				CC:     cc,
				Cod:    cod,
				Gmi:    gmi,
				Ollama: ollamaCount,
			}
			if cursorCount > 0 {
				cfg.Cursor = cursorCount
			}
			if wsCount > 0 {
				cfg.Windsurf = wsCount
			}
			if aiderCount > 0 {
				cfg.Aider = aiderCount
			}
			if prompt != "" {
				cfg.Prompt = prompt
			}
			if initFile != "" {
				cfg.InitFile = initFile
			}
			if userPane {
				cfg.UserPane = &userPane
			}
			if safety {
				cfg.Safety = &safety
			}
			if wt {
				cfg.Worktrees = &wt
			}
			if err := SaveSessionProfile(name, cfg); err != nil {
				return err
			}
			fmt.Printf("Profile %q saved to %s\n", name, sessionProfilePath(name))
			return nil
		},
	}

	cmd.Flags().IntVar(&cc, "cc", 0, "Number of Claude agents")
	cmd.Flags().IntVar(&cod, "cod", 0, "Number of Codex agents")
	cmd.Flags().IntVar(&gmi, "gmi", 0, "Number of Gemini agents")
	cmd.Flags().IntVar(&ollamaCount, "ollama", 0, "Number of Ollama agents")
	cmd.Flags().IntVar(&cursorCount, "cursor", 0, "Number of Cursor agents")
	cmd.Flags().IntVar(&wsCount, "windsurf", 0, "Number of Windsurf agents")
	cmd.Flags().IntVar(&aiderCount, "aider", 0, "Number of Aider agents")
	cmd.Flags().BoolVar(&userPane, "user-pane", false, "Include user pane")
	cmd.Flags().BoolVar(&safety, "safety", false, "Enable safety mode")
	cmd.Flags().BoolVar(&wt, "worktrees", false, "Enable git worktree isolation")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Default prompt text")
	cmd.Flags().StringVar(&initFile, "init-file", "", "Path to init prompt file")

	return cmd
}

func newSessionProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved profiles",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			names, err := ListSessionProfiles()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Println("No profiles saved.")
				return nil
			}
			for _, name := range names {
				fmt.Println(name)
			}
			return nil
		},
	}
}

func newSessionProfileDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := DeleteSessionProfile(args[0]); err != nil {
				return err
			}
			fmt.Printf("Profile %q deleted.\n", args[0])
			return nil
		},
	}
}

func newSessionProfileShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a saved profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if _, err := LoadSessionProfile(args[0]); err != nil {
				return err
			}
			data, err := os.ReadFile(sessionProfilePath(args[0]))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("profile %q not found", args[0])
				}
				return err
			}
			fmt.Print(string(data))
			return nil
		},
	}
}

// printSessionProfileList outputs profile list as JSON for robot mode.
func printSessionProfileList() error {
	type result struct {
		Success  bool     `json:"success"`
		Profiles []string `json:"profiles"`
	}
	names, err := ListSessionProfiles()
	if err != nil {
		return err
	}
	if names == nil {
		names = []string{}
	}
	data, err := json.MarshalIndent(result{Success: true, Profiles: names}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// printSessionProfileShow outputs a single profile as JSON for robot mode.
func printSessionProfileShow(name string) error {
	cfg, err := LoadSessionProfile(name)
	if err != nil {
		return err
	}
	type result struct {
		Success bool           `json:"success"`
		Name    string         `json:"name"`
		Profile SessionProfile `json:"profile"`
	}
	data, err := json.MarshalIndent(result{Success: true, Name: name, Profile: *cfg}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// ApplySessionProfileToSpawnOptions merges a loaded profile into SpawnOptions.
// Explicit flags override profile values (non-zero values in opts win).
func ApplySessionProfileToSpawnOptions(opts *SpawnOptions, profile *SessionProfile) {
	if opts.CCCount == 0 && profile.CC > 0 {
		opts.CCCount = profile.CC
	}
	if opts.CodCount == 0 && profile.Cod > 0 {
		opts.CodCount = profile.Cod
	}
	if opts.GmiCount == 0 && profile.Gmi > 0 {
		opts.GmiCount = profile.Gmi
	}
	if opts.OllamaCount == 0 && profile.Ollama > 0 {
		opts.OllamaCount = profile.Ollama
	}
	if opts.CursorCount == 0 && profile.Cursor > 0 {
		opts.CursorCount = profile.Cursor
	}
	if opts.WindsurfCount == 0 && profile.Windsurf > 0 {
		opts.WindsurfCount = profile.Windsurf
	}
	if opts.AiderCount == 0 && profile.Aider > 0 {
		opts.AiderCount = profile.Aider
	}
	if profile.UserPane != nil && *profile.UserPane {
		opts.UserPane = true
	}
	if opts.Prompt == "" && profile.Prompt != "" {
		opts.Prompt = profile.Prompt
	}
	if opts.InitPrompt == "" && profile.InitFile != "" {
		data, err := os.ReadFile(config.ExpandHome(profile.InitFile))
		if err == nil {
			opts.InitPrompt = strings.TrimSpace(string(data))
		}
	}
	if profile.Safety != nil && *profile.Safety {
		opts.Safety = true
	}
	if profile.Worktrees != nil && *profile.Worktrees {
		opts.UseWorktrees = true
	}
}
