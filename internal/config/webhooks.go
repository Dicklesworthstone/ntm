package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

type WebhookFilterConfig struct {
	// Session is an optional glob for matching session names (e.g. "myproject*").
	Session string `yaml:"session"`

	// AgentType is an optional allowlist of supported agent types and aliases.
	AgentType []string `yaml:"agent_type"`

	// Severity is an optional allowlist of severities ("info", "success", "warning", "error").
	Severity []string `yaml:"severity"`
}

type WebhookRetryConfig struct {
	MaxAttempts int    `yaml:"max_attempts"`
	Backoff     string `yaml:"backoff"`
}

type WebhookConfig struct {
	Name      string              `yaml:"name"`
	URL       string              `yaml:"url"`
	Events    []string            `yaml:"events"`
	Formatter string              `yaml:"formatter"`
	Filter    WebhookFilterConfig `yaml:"filter"`
	Retry     WebhookRetryConfig  `yaml:"retry"`
	Timeout   string              `yaml:"timeout"`
	Secret    string              `yaml:"secret"`
}

func (c *WebhookConfig) ValidateConfig() error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}

	urlStr := strings.TrimSpace(c.URL)
	if urlStr == "" {
		return errors.New("url is required")
	}
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", urlStr, err)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("invalid url scheme %q (must be http or https)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid url %q: missing host", urlStr)
	}
	if scheme == "http" {
		host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		switch host {
		case "localhost", "127.0.0.1", "::1":
			// Allow insecure localhost webhooks for development.
		default:
			return fmt.Errorf("invalid url scheme %q for %q: only https is allowed (http allowed only for localhost)", parsed.Scheme, parsed.Hostname())
		}
	}

	if strings.TrimSpace(c.Timeout) != "" {
		if _, err := time.ParseDuration(strings.TrimSpace(c.Timeout)); err != nil {
			return fmt.Errorf("invalid timeout %q: %w", c.Timeout, err)
		}
	}

	if strings.TrimSpace(c.Formatter) != "" {
		if !isValidWebhookFormatter(c.Formatter) {
			return fmt.Errorf("unknown formatter %q (supported: json, slack, discord, teams)", strings.TrimSpace(c.Formatter))
		}
	}

	for _, ev := range c.Events {
		ev = strings.TrimSpace(ev)
		if ev == "" {
			continue
		}
		if !isRecognizedWebhookEvent(ev) {
			return fmt.Errorf("unknown event %q", ev)
		}
	}

	if strings.TrimSpace(c.Filter.Session) != "" {
		if _, err := path.Match(c.Filter.Session, "example"); err != nil {
			return fmt.Errorf("invalid filter.session glob %q: %w", c.Filter.Session, err)
		}
	}

	for _, t := range c.Filter.AgentType {
		if !isValidWebhookAgentType(t) {
			return fmt.Errorf("invalid filter.agent_type %q (supported: claude, codex, gemini, cursor, windsurf, aider, ollama)", strings.TrimSpace(t))
		}
	}

	for _, s := range c.Filter.Severity {
		if !isValidWebhookSeverity(s) {
			return fmt.Errorf("invalid filter.severity %q (supported: info, success, warning, error)", strings.TrimSpace(s))
		}
	}

	if strings.TrimSpace(c.Retry.Backoff) != "" && strings.ToLower(strings.TrimSpace(c.Retry.Backoff)) != "exponential" {
		return fmt.Errorf("invalid retry.backoff %q (supported: exponential)", strings.TrimSpace(c.Retry.Backoff))
	}
	if c.Retry.MaxAttempts < 0 {
		return fmt.Errorf("invalid retry.max_attempts %d (must be >= 0)", c.Retry.MaxAttempts)
	}

	return nil
}

func (c *WebhookConfig) applyDefaults(maxAttemptsExplicit bool) {
	if strings.TrimSpace(c.Formatter) == "" {
		c.Formatter = "json"
	}
	if strings.TrimSpace(c.Timeout) == "" {
		c.Timeout = "10s"
	}
	if !maxAttemptsExplicit && c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = 5
	}
	if strings.TrimSpace(c.Retry.Backoff) == "" {
		c.Retry.Backoff = "exponential"
	}
}

// ParseWebhookConfig extracts and validates the `webhooks:` list from a .ntm.yaml/.ntm.yml file.
// The input may contain other top-level configuration (e.g. `scanner:`) which is ignored.
func ParseWebhookConfig(yamlBytes []byte) ([]WebhookConfig, error) {
	if len(bytes.TrimSpace(yamlBytes)) == 0 {
		return nil, nil
	}

	expanded, err := expandEnvPlaceholders(yamlBytes)
	if err != nil {
		return nil, err
	}

	var root yaml.Node
	if err := yaml.Unmarshal(expanded, &root); err != nil {
		return nil, err
	}

	webhooksNode := findTopLevelYAMLKey(&root, "webhooks")
	if webhooksNode == nil {
		return nil, nil
	}
	if webhooksNode.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("webhooks: expected a list")
	}

	out := make([]WebhookConfig, 0, len(webhooksNode.Content))
	for idx, item := range webhooksNode.Content {
		raw, err := yaml.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("webhooks[%d]: marshal: %w", idx, err)
		}

		var cfg WebhookConfig
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("webhooks[%d]: %w", idx, err)
		}

		retryNode := findYAMLMappingKey(item, "retry")
		retryMaxAttemptsExplicit := findYAMLMappingKey(retryNode, "max_attempts") != nil

		cfg.applyDefaults(retryMaxAttemptsExplicit)
		if err := cfg.ValidateConfig(); err != nil {
			name := strings.TrimSpace(cfg.Name)
			if name == "" {
				name = "(unnamed)"
			}
			return nil, fmt.Errorf("webhooks[%d] %s: %w", idx, name, err)
		}

		out = append(out, cfg)
	}

	return out, nil
}

// LoadProjectWebhooks loads webhook configuration from .ntm.yaml/.ntm.yml in projectDir.
// If no file exists, it returns an empty list.
func LoadProjectWebhooks(projectDir string) ([]WebhookConfig, error) {
	paths := []string{
		filepath.Join(projectDir, ".ntm.yaml"),
		filepath.Join(projectDir, ".ntm.yml"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return ParseWebhookConfig(data)
	}
	return nil, nil
}

func findTopLevelYAMLKey(root *yaml.Node, key string) *yaml.Node {
	return findYAMLMappingKey(root, key)
}

func findYAMLMappingKey(root *yaml.Node, key string) *yaml.Node {
	n := root
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		v := n.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	return nil
}

var envPlaceholderRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnvPlaceholders(in []byte) ([]byte, error) {
	s := string(in)
	missing := make(map[string]struct{})

	out := envPlaceholderRe.ReplaceAllStringFunc(s, func(m string) string {
		// m looks like ${VAR}
		key := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		missing[key] = struct{}{}
		return m
	})

	if len(missing) == 0 {
		return []byte(out), nil
	}

	keys := make([]string, 0, len(missing))
	for k := range missing {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return nil, fmt.Errorf("missing environment variables: %s", strings.Join(keys, ", "))
}

func isRecognizedWebhookEvent(ev string) bool {
	switch strings.ToLower(strings.TrimSpace(ev)) {
	case "agent.error",
		"agent.started",
		"agent.stopped",
		"agent.crashed",
		"agent.restarted",
		"agent.idle",
		"agent.busy",
		"agent.rate_limit",
		"agent.completed",
		"rotation.needed",
		"session.created",
		"session.killed",
		"session.ended",
		"bead.assigned",
		"bead.completed",
		"bead.failed",
		"health.degraded":
		return true
	default:
		return false
	}
}

func isValidWebhookFormatter(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "slack", "discord", "teams", "msteams", "ms-teams", "microsoft-teams", "microsoft_teams":
		return true
	default:
		return false
	}
}
func isValidWebhookAgentType(agentType string) bool {
	switch agent.AgentType(agentType).Canonical() {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOllama:
		return true
	default:
		return false
	}
}

func isValidWebhookSeverity(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "info", "success", "warning", "warn", "error":
		return true
	default:
		return false
	}
}
