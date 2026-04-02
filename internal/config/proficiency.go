package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/cli/tiers"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// ProficiencyConfig stores user proficiency tier and usage statistics.
type ProficiencyConfig struct {
	Tier             int               `json:"tier"`
	UsageStats       UsageStats        `json:"usage_stats"`
	PromotionHistory []PromotionRecord `json:"promotion_history"`
	Suggestion       SuggestionState   `json:"suggestion,omitempty"`
	mu               sync.RWMutex      `json:"-"`
}

// UsageStats tracks command usage for tier promotion suggestions.
type UsageStats struct {
	CommandsRun      int            `json:"commands_run"`
	SessionsCreated  int            `json:"sessions_created"`
	PromptsSent      int            `json:"prompts_sent"`
	UniqueCommands   map[string]int `json:"unique_commands,omitempty"` // command -> count
	AdvancedAttempts int            `json:"advanced_attempts"`         // tried tier-locked commands
	DaysActive       int            `json:"days_active"`               // days with at least one command
	FirstUse         time.Time      `json:"first_use"`
	LastUse          time.Time      `json:"last_use"`
	LastActiveDate   string         `json:"last_active_date,omitempty"` // YYYY-MM-DD for tracking daily activity
}

// SuggestionState tracks when promotion suggestions were shown.
type SuggestionState struct {
	LastShownTime    time.Time `json:"last_shown_time,omitempty"`
	LastShownSession string    `json:"last_shown_session,omitempty"` // session ID to avoid repeat in same session
	TimesShown       int       `json:"times_shown"`
}

// PromotionRecord tracks when and why a tier change occurred.
type PromotionRecord struct {
	From   int       `json:"from"`
	To     int       `json:"to"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"` // "manual", "auto", "reset"
}

// proficiencyConfigPath returns the path to proficiency.json.
func proficiencyConfigPath() string {
	return filepath.Join(filepath.Dir(DefaultPath()), "proficiency.json")
}

// DefaultProficiencyConfig returns a new proficiency config with sensible defaults.
func DefaultProficiencyConfig() *ProficiencyConfig {
	now := time.Now()
	return &ProficiencyConfig{
		Tier: int(tiers.TierApprentice),
		UsageStats: UsageStats{
			UniqueCommands: make(map[string]int),
			FirstUse:       now,
			LastUse:        now,
			LastActiveDate: now.Format("2006-01-02"),
			DaysActive:     1,
		},
		PromotionHistory: []PromotionRecord{},
	}
}

// LoadProficiency reads proficiency config from disk.
// Returns default config if file doesn't exist or is corrupted.
func LoadProficiency() (*ProficiencyConfig, error) {
	path := proficiencyConfigPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// First use - return defaults and save them
			cfg := DefaultProficiencyConfig()
			if saveErr := cfg.Save(); saveErr != nil {
				// Log but don't fail - can still work without persistence
				return cfg, nil
			}
			return cfg, nil
		}
		// Other read error - return defaults
		return DefaultProficiencyConfig(), nil
	}

	var cfg ProficiencyConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Corrupted config - reset to defaults
		cfg := DefaultProficiencyConfig()
		_ = cfg.Save() // Try to fix it
		return cfg, nil
	}

	return &cfg, nil
}

// Save persists the proficiency config to disk.
func (c *ProficiencyConfig) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	path := proficiencyConfigPath()

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return util.AtomicWriteFile(path, data, 0644)
}

// GetTier returns the effective proficiency tier.
// Priority: environment variable > config file > default
func (c *ProficiencyConfig) GetTier() tiers.Tier {
	// Check environment override first
	if envTier := os.Getenv("NTM_PROFICIENCY_TIER"); envTier != "" {
		if t, err := strconv.Atoi(envTier); err == nil && t >= 1 && t <= 3 {
			return tiers.Tier(t)
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	// Validate tier value
	if c.Tier < 1 || c.Tier > 3 {
		return tiers.TierApprentice
	}

	return tiers.Tier(c.Tier)
}

// SetTier changes the proficiency tier and records the change.
func (c *ProficiencyConfig) SetTier(newTier tiers.Tier, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldTier := c.Tier
	if oldTier == int(newTier) {
		return nil // No change
	}

	c.Tier = int(newTier)
	c.PromotionHistory = append(c.PromotionHistory, PromotionRecord{
		From:   oldTier,
		To:     int(newTier),
		At:     time.Now(),
		Reason: reason,
	})

	return c.saveUnlocked()
}

// RecordUsage updates usage statistics.
func (c *ProficiencyConfig) RecordUsage(commandsRun, sessionsCreated, promptsSent int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.UsageStats.CommandsRun += commandsRun
	c.UsageStats.SessionsCreated += sessionsCreated
	c.UsageStats.PromptsSent += promptsSent
	c.UsageStats.LastUse = time.Now()

	return c.saveUnlocked()
}

// IncrementCommand increments the command counter and updates last use.
func (c *ProficiencyConfig) IncrementCommand() error {
	return c.RecordUsage(1, 0, 0)
}

// IncrementSession increments the session counter.
func (c *ProficiencyConfig) IncrementSession() error {
	return c.RecordUsage(0, 1, 0)
}

// IncrementPrompt increments the prompt counter.
func (c *ProficiencyConfig) IncrementPrompt() error {
	return c.RecordUsage(0, 0, 1)
}

// saveUnlocked persists config (caller must hold lock).
func (c *ProficiencyConfig) saveUnlocked() error {
	path := proficiencyConfigPath()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	return util.AtomicWriteFile(path, data, 0644)
}

// GetUsageStats returns a deep copy of current usage statistics.
func (c *ProficiencyConfig) GetUsageStats() UsageStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	stats := c.UsageStats
	if stats.UniqueCommands != nil {
		cp := make(map[string]int, len(stats.UniqueCommands))
		for k, v := range stats.UniqueCommands {
			cp[k] = v
		}
		stats.UniqueCommands = cp
	}
	return stats
}

// GetPromotionHistory returns a copy of promotion history.
func (c *ProficiencyConfig) GetPromotionHistory() []PromotionRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()

	history := make([]PromotionRecord, len(c.PromotionHistory))
	copy(history, c.PromotionHistory)
	return history
}

// Reset resets to default tier and clears history.
func (c *ProficiencyConfig) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	oldTier := c.Tier
	c.Tier = int(tiers.TierApprentice)
	c.UsageStats = UsageStats{
		UniqueCommands: make(map[string]int),
		FirstUse:       now,
		LastUse:        now,
		LastActiveDate: now.Format("2006-01-02"),
		DaysActive:     1,
	}
	c.Suggestion = SuggestionState{} // Clear suggestion state
	c.PromotionHistory = append(c.PromotionHistory, PromotionRecord{
		From:   oldTier,
		To:     int(tiers.TierApprentice),
		At:     now,
		Reason: "reset",
	})

	return c.saveUnlocked()
}

// DaysSinceFirstUse returns the number of days since first use.
func (c *ProficiencyConfig) DaysSinceFirstUse() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.UsageStats.FirstUse.IsZero() {
		return 0
	}
	return int(time.Since(c.UsageStats.FirstUse).Hours() / 24)
}

// ShouldSuggestPromotion returns true if the user might be ready for the next tier.
// Uses configurable criteria based on usage patterns.
func (c *ProficiencyConfig) ShouldSuggestPromotion() (bool, tiers.Tier, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	currentTier := tiers.Tier(c.Tier)
	if currentTier >= tiers.TierMaster {
		return false, currentTier, ""
	}

	stats := c.UsageStats

	// Tier 1 -> 2 (Apprentice -> Journeyman): any of these criteria
	if currentTier == tiers.TierApprentice {
		if stats.CommandsRun >= 100 {
			return true, tiers.TierJourneyman, "You've run 100+ commands! Consider upgrading to Journeyman tier."
		}
		if stats.SessionsCreated >= 20 {
			return true, tiers.TierJourneyman, "You've created 20+ sessions! Consider upgrading to Journeyman tier."
		}
		if stats.DaysActive >= 7 {
			return true, tiers.TierJourneyman, "You've been using NTM for 7+ days! Consider upgrading to Journeyman tier."
		}
		if stats.AdvancedAttempts >= 3 {
			return true, tiers.TierJourneyman, "You've tried accessing advanced commands! Upgrade to Journeyman tier to unlock them."
		}
	}

	// Tier 2 -> 3 (Journeyman -> Master): any of these criteria
	if currentTier == tiers.TierJourneyman {
		if stats.CommandsRun >= 500 {
			return true, tiers.TierMaster, "You've run 500+ commands! Consider upgrading to Master tier."
		}
		if stats.SessionsCreated >= 100 {
			return true, tiers.TierMaster, "You've created 100+ sessions! Consider upgrading to Master tier."
		}
		if stats.DaysActive >= 30 {
			return true, tiers.TierMaster, "You've been using NTM for 30+ days! Consider upgrading to Master tier."
		}
		if stats.AdvancedAttempts >= 5 {
			return true, tiers.TierMaster, "You've tried accessing advanced commands! Upgrade to Master tier to unlock them."
		}
	}

	return false, currentTier, ""
}

// RecordCommand tracks a command execution with enhanced statistics.
func (c *ProficiencyConfig) RecordCommand(commandName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Initialize map if needed
	if c.UsageStats.UniqueCommands == nil {
		c.UsageStats.UniqueCommands = make(map[string]int)
	}

	// Track command
	c.UsageStats.CommandsRun++
	c.UsageStats.UniqueCommands[commandName]++
	c.UsageStats.LastUse = time.Now()

	// Track daily activity
	today := time.Now().Format("2006-01-02")
	if c.UsageStats.LastActiveDate != today {
		c.UsageStats.DaysActive++
		c.UsageStats.LastActiveDate = today
	}

	return c.saveUnlocked()
}

// RecordAdvancedAttempt tracks when user tries a command above their tier.
func (c *ProficiencyConfig) RecordAdvancedAttempt(commandName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.UsageStats.AdvancedAttempts++
	c.UsageStats.LastUse = time.Now()

	return c.saveUnlocked()
}

// CheckPromotion checks if promotion should be suggested and returns a non-intrusive message.
// It tracks when suggestions were shown to avoid spamming.
// sessionID should be unique per CLI session to avoid repeat suggestions.
func (c *ProficiencyConfig) CheckPromotion(sessionID string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Don't suggest if already shown this session
	if c.Suggestion.LastShownSession == sessionID {
		return false, ""
	}

	// Don't suggest more than once per hour
	if time.Since(c.Suggestion.LastShownTime) < time.Hour {
		return false, ""
	}

	currentTier := tiers.Tier(c.Tier)
	if currentTier >= tiers.TierMaster {
		return false, ""
	}

	stats := c.UsageStats

	// Check promotion criteria (same as ShouldSuggestPromotion but inline for lock)
	var suggest bool
	var nextTier tiers.Tier

	switch currentTier {
	case tiers.TierApprentice:
		if stats.CommandsRun >= 100 || stats.SessionsCreated >= 20 ||
			stats.DaysActive >= 7 || stats.AdvancedAttempts >= 3 {
			suggest = true
			nextTier = tiers.TierJourneyman
		}
	case tiers.TierJourneyman:
		if stats.CommandsRun >= 500 || stats.SessionsCreated >= 100 ||
			stats.DaysActive >= 30 || stats.AdvancedAttempts >= 5 {
			suggest = true
			nextTier = tiers.TierMaster
		}
	}

	if !suggest {
		return false, ""
	}

	// Mark suggestion as shown
	c.Suggestion.LastShownTime = time.Now()
	c.Suggestion.LastShownSession = sessionID
	c.Suggestion.TimesShown++
	_ = c.saveUnlocked()

	msg := fmt.Sprintf("Tip: You've been using NTM like a pro! Run 'ntm level %s' to unlock more features.",
		nextTier.String())
	return true, msg
}

// GetUniqueCommandCount returns the number of distinct commands used.
func (c *ProficiencyConfig) GetUniqueCommandCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.UsageStats.UniqueCommands)
}

// ProficiencyConfigPath returns the path for external use (testing, etc.)
func ProficiencyConfigPath() string {
	return proficiencyConfigPath()
}
