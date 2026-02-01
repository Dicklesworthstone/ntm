// Package redaction provides detection and redaction of sensitive content
// such as API keys, tokens, passwords, and other secrets.
package redaction

// Mode defines the redaction behavior.
type Mode string

const (
	// ModeOff disables all scanning and redaction.
	ModeOff Mode = "off"
	// ModeWarn scans and logs findings but doesn't modify content.
	ModeWarn Mode = "warn"
	// ModeRedact replaces sensitive content with placeholders.
	ModeRedact Mode = "redact"
	// ModeBlock fails the operation if sensitive content is detected.
	ModeBlock Mode = "block"
)

// Category identifies the type of sensitive content detected.
type Category string

const (
	CategoryOpenAIKey     Category = "OPENAI_KEY"
	CategoryAnthropicKey  Category = "ANTHROPIC_KEY"
	CategoryGitHubToken   Category = "GITHUB_TOKEN"
	CategoryAWSAccessKey  Category = "AWS_ACCESS_KEY"
	CategoryAWSSecretKey  Category = "AWS_SECRET_KEY"
	CategoryJWT           Category = "JWT"
	CategoryGoogleAPIKey  Category = "GOOGLE_API_KEY"
	CategoryPrivateKey    Category = "PRIVATE_KEY"
	CategoryDatabaseURL   Category = "DATABASE_URL"
	CategoryPassword      Category = "PASSWORD"
	CategoryGenericAPIKey Category = "GENERIC_API_KEY"
	CategoryGenericSecret Category = "GENERIC_SECRET"
	CategoryBearerToken   Category = "BEARER_TOKEN"
)

// Finding represents a single detected secret.
type Finding struct {
	// Category is the type of secret detected.
	Category Category `json:"category"`
	// Match is the original matched content.
	Match string `json:"match"`
	// Redacted is the placeholder that replaces the match.
	Redacted string `json:"redacted"`
	// Start is the byte offset where the match begins.
	Start int `json:"start"`
	// End is the byte offset where the match ends.
	End int `json:"end"`
	// Line is the 1-indexed line number (optional, set by caller).
	Line int `json:"line,omitempty"`
	// Column is the 1-indexed column number (optional, set by caller).
	Column int `json:"column,omitempty"`
}

// Result contains the outcome of a scan/redaction operation.
type Result struct {
	// Mode is the redaction mode that was applied.
	Mode Mode `json:"mode"`
	// Findings is the list of detected secrets.
	Findings []Finding `json:"findings"`
	// Output is the (potentially redacted) content.
	Output string `json:"output"`
	// Blocked indicates if the operation should be blocked (ModeBlock + findings).
	Blocked bool `json:"blocked"`
	// OriginalLength is the length of the input before redaction.
	OriginalLength int `json:"original_length"`
}

// Config configures the redaction behavior.
type Config struct {
	// Mode determines how detected secrets are handled.
	Mode Mode `json:"mode"`
	// Allowlist contains regex patterns that should not be flagged.
	Allowlist []string `json:"allowlist,omitempty"`
	// ExtraPatterns contains additional patterns to detect.
	ExtraPatterns map[Category][]string `json:"extra_patterns,omitempty"`
	// DisabledCategories lists categories to skip during scanning.
	DisabledCategories []Category `json:"disabled_categories,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Mode: ModeWarn,
	}
}

// Validate checks if the config is valid.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeOff, ModeWarn, ModeRedact, ModeBlock:
		// valid
	default:
		return &ConfigError{Field: "mode", Message: "invalid mode: " + string(c.Mode)}
	}
	return nil
}

// ConfigError represents a configuration error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "redaction config error: " + e.Field + ": " + e.Message
}

