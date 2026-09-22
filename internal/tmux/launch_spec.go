package tmux

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	// AgentLaunchSpecVersion is the current durable agent launch schema.
	AgentLaunchSpecVersion = 1
	// PaneLaunchSpecOption stores a pane-bound, base64-encoded launch record.
	PaneLaunchSpecOption    = "@ntm_agent_launch"
	maxAgentLaunchSpecBytes = 48 * 1024
)

// AgentLaunchSpec preserves a rendered launch command independently of a pane's
// mutable title and foreground executable. Command excludes the working-directory
// prefix and runtime environment injected by NTM. Environment values supplied by
// an operator or plugin are deliberately not persisted; OmittedEnv names them so
// automatic recovery can refuse a launch whose required inputs are unavailable.
//
// CAAMProfile and ClaudeTokenFile are references, never credential values. Claude
// isolation is provisioned again for the destination pane rather than sharing the
// predecessor's mutable credential directory during a rotation.
type AgentLaunchSpec struct {
	Version                  int       `json:"version"`
	AgentType                AgentType `json:"agent_type"`
	Command                  string    `json:"command"`
	Model                    string    `json:"model,omitempty"`
	ModelAlias               string    `json:"model_alias,omitempty"`
	Persona                  string    `json:"persona,omitempty"`
	ReasoningEffort          string    `json:"reasoning_effort,omitempty"`
	SystemPromptFile         string    `json:"system_prompt_file,omitempty"`
	SystemPromptSHA256       string    `json:"system_prompt_sha256,omitempty"`
	CAAMProfile              string    `json:"caam_profile,omitempty"`
	OmittedEnv               []string  `json:"omitted_env,omitempty"`
	ClaudeIsolateCredentials bool      `json:"claude_isolate_credentials,omitempty"`
	ClaudeTokenFile          string    `json:"claude_token_file,omitempty"`
	AgentMailProject         string    `json:"agent_mail_project,omitempty"`
}

// Validate checks the schema and its binding to the caller's observed agent
// type. An incomplete environment is valid capture metadata; ValidateReplay
// additionally checks whether it can be used for an automatic launch.
func (spec *AgentLaunchSpec) Validate(expected AgentType) error {
	if spec == nil {
		return errors.New("agent launch specification is missing")
	}
	if spec.Version != AgentLaunchSpecVersion {
		return fmt.Errorf("unsupported agent launch specification version %d", spec.Version)
	}
	actual := ParsePaneAgentTypeOption(string(spec.AgentType))
	want := ParsePaneAgentTypeOption(string(expected))
	if actual == AgentUnknown || actual == AgentUser || want == AgentUnknown || want == AgentUser {
		return errors.New("agent launch specification requires a recognized agent type")
	}
	if actual != want {
		return fmt.Errorf("agent launch specification type %q does not match pane type %q", actual, want)
	}
	if strings.TrimSpace(spec.Command) == "" {
		return errors.New("agent launch specification command is empty")
	}
	if _, err := SanitizePaneCommand(spec.Command); err != nil {
		return fmt.Errorf("invalid agent launch specification command: %w", err)
	}
	for _, value := range []string{spec.Model, spec.ModelAlias, spec.Persona, spec.ReasoningEffort, spec.SystemPromptFile, spec.CAAMProfile, spec.ClaudeTokenFile, spec.AgentMailProject} {
		if len(value) > 4096 {
			return errors.New("agent launch specification field exceeds size limit")
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return errors.New("agent launch specification field contains control characters")
			}
		}
	}
	if spec.SystemPromptFile != "" || spec.SystemPromptSHA256 != "" {
		digest, err := hex.DecodeString(spec.SystemPromptSHA256)
		if spec.SystemPromptFile == "" || err != nil || len(digest) != 32 {
			return errors.New("agent launch specification requires a prepared prompt path and SHA-256 digest together")
		}
	}
	for _, path := range []string{spec.SystemPromptFile, spec.ClaudeTokenFile, spec.AgentMailProject} {
		if path != "" && !filepath.IsAbs(path) {
			return errors.New("agent launch specification references must be absolute paths")
		}
	}
	if (spec.ClaudeIsolateCredentials || spec.ClaudeTokenFile != "") && actual != AgentClaude {
		return errors.New("Claude credential isolation requires a Claude launch specification")
	}
	if spec.ClaudeTokenFile != "" && !spec.ClaudeIsolateCredentials {
		return errors.New("Claude token file requires credential isolation")
	}
	if len(spec.OmittedEnv) > 256 {
		return errors.New("agent launch specification has too many omitted environment keys")
	}
	for _, name := range spec.OmittedEnv {
		if !validLaunchEnvironmentName(name) {
			return errors.New("agent launch specification has an invalid omitted environment key")
		}
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return errors.New("cannot encode agent launch specification")
	}
	if len(data) > maxAgentLaunchSpecBytes {
		return errors.New("agent launch specification exceeds size limit")
	}
	return nil
}

// ValidateReplay refuses to silently drop explicitly supplied environment. The
// values are intentionally absent from durable metadata and must be provided by
// the operator when launching a replacement manually.
func (spec *AgentLaunchSpec) ValidateReplay(expected AgentType) error {
	if err := spec.Validate(expected); err != nil {
		return err
	}
	if len(spec.OmittedEnv) > 0 {
		return fmt.Errorf("agent launch requires environment values that were not persisted: %s", strings.Join(spec.OmittedEnv, ", "))
	}
	return nil
}

func validLaunchEnvironmentName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

type paneLaunchRecord struct {
	PaneID string          `json:"pane_id"`
	Spec   AgentLaunchSpec `json:"spec"`
}

func validateLaunchSpecTarget(ctx context.Context, paneID string) error {
	if ctx == nil {
		return errors.New("agent launch specification requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(paneID) < 2 || len(paneID) > 21 || paneID[0] != '%' {
		return errors.New("agent launch specification requires a physical pane ID")
	}
	for _, r := range paneID[1:] {
		if r < '0' || r > '9' {
			return errors.New("agent launch specification requires a physical pane ID")
		}
	}
	return nil
}

// SetPaneLaunchSpecContext records a command before it is launched. The envelope
// binds the record to its physical pane, preventing inherited or copied options
// from being mistaken for the launch metadata of another pane.
func (c *Client) SetPaneLaunchSpecContext(ctx context.Context, paneID string, spec AgentLaunchSpec) error {
	if err := validateLaunchSpecTarget(ctx, paneID); err != nil {
		return err
	}
	if err := spec.Validate(spec.AgentType); err != nil {
		return err
	}
	spec.AgentType = spec.AgentType.Canonical()
	data, err := json.Marshal(paneLaunchRecord{PaneID: paneID, Spec: spec})
	if err != nil {
		return errors.New("cannot encode pane launch record")
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if err := c.RunSilentContext(ctx, "set-option", "-p", "-t", ExactTarget(paneID), PaneLaunchSpecOption, encoded); err != nil {
		// A CommandError includes the payload in argv. Do not expose the saved
		// launch command through diagnostic strings; keep cancellation testable.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot persist agent launch specification for pane %s (%s)", paneID, ClassifyCommandError(err).Kind)
	}
	return nil
}

// ReadPaneLaunchSpecContext returns nil only for an absent pane-local option.
// Failed reads, malformed records and a mismatched pane binding are errors.
// Metadata is fetched separately so routine topology observations stay small.
func (c *Client) ReadPaneLaunchSpecContext(ctx context.Context, paneID string) (*AgentLaunchSpec, error) {
	if err := validateLaunchSpecTarget(ctx, paneID); err != nil {
		return nil, err
	}
	// Without -A show-options reads only the pane-local value. Do not use -q:
	// it suppresses missing-pane errors as well as absent-option errors. Only
	// the exact missing user-option diagnostic establishes legacy absence.
	encoded, err := c.RunContext(ctx, "show-options", "-p", "-v", "-t", ExactTarget(paneID), PaneLaunchSpecOption)
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && strings.TrimSpace(commandErr.Stderr) == "invalid option: "+PaneLaunchSpecOption {
			return nil, nil
		}
		return nil, fmt.Errorf("read agent launch specification for pane %s: %w", paneID, err)
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("pane launch record is empty")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxAgentLaunchSpecBytes+512) {
		return nil, errors.New("pane launch record exceeds size limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("pane launch record is not valid base64")
	}
	var record paneLaunchRecord
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, errors.New("pane launch record is not valid JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("pane launch record contains trailing data")
	}
	if record.PaneID != paneID {
		return nil, errors.New("pane launch record belongs to a different physical pane")
	}
	if err := record.Spec.Validate(record.Spec.AgentType); err != nil {
		return nil, err
	}
	return &record.Spec, nil
}

// SetPaneLaunchSpecContext records a launch specification using the default client.
func SetPaneLaunchSpecContext(ctx context.Context, paneID string, spec AgentLaunchSpec) error {
	return DefaultClient.SetPaneLaunchSpecContext(ctx, paneID, spec)
}

// ReadPaneLaunchSpecContext reads a launch specification using the default client.
func ReadPaneLaunchSpecContext(ctx context.Context, paneID string) (*AgentLaunchSpec, error) {
	return DefaultClient.ReadPaneLaunchSpecContext(ctx, paneID)
}
