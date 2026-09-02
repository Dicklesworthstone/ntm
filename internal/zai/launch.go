// Package zai provides the constrained Claude-compatible Z.ai launch and
// probe boundary. It intentionally never accepts a shell fragment from a
// provider profile: NTM owns every argument after the executable reference.
package zai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/provider"
)

const (
	// ReadOnlyTools deliberately excludes Bash, Edit, Write, and every other
	// mutation-capable tool. A future test-capable policy needs separate review.
	ReadOnlyTools       = "Read,Glob,Grep,WebSearch"
	maxProbeOutput      = 1 << 20
	OfficialEndpoint    = "https://api.z.ai/api/anthropic"
	DefaultProbeTimeout = 60 * time.Second
)

// ValidateExecutable permits precisely argv[0], never a shell fragment.
func ValidateExecutable(binary string) error {
	if binary != strings.TrimSpace(binary) || binary == "" {
		return errors.New("must be a trimmed executable name or absolute path")
	}
	if hasControl(binary) || strings.ContainsAny(binary, ";&|<>`\"'") || strings.HasPrefix(binary, "-") {
		return errors.New("must not contain shell syntax, quoting, options, or control characters")
	}
	if len(strings.Fields(binary)) > 1 && !filepath.IsAbs(binary) {
		return errors.New("must be one executable name; NTM generates all arguments")
	}
	return nil
}

// RestrictedLaunchCommand compiles the only permitted unattended pane command.
// Endpoint and model are quoted as literal environment/argv values. The shell
// must supply an explicit Z.ai token; env -i then strips every unrelated
// inherited value before the provider runtime starts.
func RestrictedLaunchCommand(binary, endpoint, model string) (string, error) {
	if err := ValidateExecutable(binary); err != nil {
		return "", err
	}
	if endpoint != OfficialEndpoint {
		return "", fmt.Errorf("endpoint must be %s", OfficialEndpoint)
	}
	if model != strings.TrimSpace(model) || model == "" || hasControl(model) {
		return "", errors.New("model must be a non-empty trimmed literal value")
	}
	q := shellQuote
	auth := "${ZAI_API_KEY:-${ANTHROPIC_AUTH_TOKEN:-}}"
	launch := strings.Join([]string{
		"env", "-i",
		"PATH=\"$PATH\"", "HOME=\"$HOME\"", "TMPDIR=\"$TMPDIR\"", "LANG=\"$LANG\"", "TERM=\"$TERM\"",
		"ANTHROPIC_AUTH_TOKEN=\"" + auth + "\"",
		"ANTHROPIC_BASE_URL=" + q(endpoint),
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + q(model),
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + q(model),
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + q(model),
		q(binary),
		"--model", q(model),
		"--restricted", "--safe-mode", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome",
		"--permission-mode", "dontAsk",
		"--tools", q(ReadOnlyTools),
		"--allowedTools", q(ReadOnlyTools),
		"--disallowedTools", q("Bash,Edit,Write,NotebookEdit"),
	}, " ")
	return "if [ -z \"" + auth + "\" ]; then printf '%s\\n' 'NTM_ZAI_AUTH_REQUIRED' >&2; exit 78; fi; exec " + launch, nil
}

// ProbeSpec is the exact no-tool, headless preflight invocation.
type ProbeSpec struct{ Binary, Endpoint, Model string }

// Receipt is redacted evidence only. It has no raw provider output or secrets.
type Receipt struct {
	NonceSHA256          string              `json:"nonce_sha256"`
	OutputSHA256         string              `json:"output_sha256"`
	SessionIDSHA256      string              `json:"session_id_sha256,omitempty"`
	Model                string              `json:"model"`
	ModelSessionEvidence bool                `json:"model_session_evidence"`
	FailureClass         string              `json:"failure_class,omitempty"`
	HTTPStatus           int                 `json:"http_status,omitempty"`
	ProviderCode         string              `json:"provider_code,omitempty"`
	ProviderErrorClass   provider.ErrorClass `json:"provider_error_class,omitempty"`
}

// Probe runs a bounded no-tool Claude print/stream-json request. Success
// requires a nonce echo plus a JSON event binding the exact requested model to
// a session id. Clients that cannot expose that evidence remain launch NO-GO.
func Probe(ctx context.Context, spec ProbeSpec) (Receipt, error) {
	if ctx == nil {
		return Receipt{FailureClass: "invalid_context"}, errors.New("Z.ai probe requires a context")
	}
	if err := ValidateExecutable(spec.Binary); err != nil {
		return Receipt{FailureClass: "invalid_executable"}, err
	}
	if spec.Endpoint != OfficialEndpoint || spec.Model != strings.TrimSpace(spec.Model) || spec.Model == "" || hasControl(spec.Model) {
		return Receipt{FailureClass: "invalid_identity"}, errors.New("probe requires endpoint and model")
	}
	nonce, err := newNonce()
	if err != nil {
		return Receipt{FailureClass: "nonce_generation"}, err
	}
	if canonicalAuth(os.Environ()) == "" {
		return Receipt{FailureClass: "authentication", ProviderErrorClass: provider.ErrorAuthentication}, errors.New("Z.ai probe has no configured authentication")
	}
	args := probeArgs(nonce, spec.Model)
	cmd := exec.CommandContext(ctx, spec.Binary, args...)
	cmd.Env = minimalEnvironment(os.Environ(), spec.Endpoint, spec.Model)
	stdout, stderr := &boundedBuffer{limit: maxProbeOutput}, &boundedBuffer{limit: maxProbeOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	output := append(append(append([]byte(nil), stdout.Bytes()...), '\n'), stderr.Bytes()...)
	r := Receipt{NonceSHA256: hash([]byte(nonce)), OutputSHA256: hash(output), Model: spec.Model}
	r.HTTPStatus, r.ProviderCode = structuredErrorStatus(output)
	if r.HTTPStatus != 0 || r.ProviderCode != "" {
		r.ProviderErrorClass = provider.ClassifyProviderError(r.HTTPStatus, r.ProviderCode)
	}
	if stdout.exceeded || stderr.exceeded {
		r.FailureClass = "output_limit"
		return r, errors.New("Z.ai probe output exceeded redacted limit")
	}
	if runErr != nil {
		r.FailureClass = classify(runErr)
		return r, fmt.Errorf("Z.ai headless probe failed: %s", r.FailureClass)
	}
	nonceSeen, modelSession, sessionID := parseEvidence(stdout.Bytes(), nonce, spec.Model)
	if !nonceSeen {
		r.FailureClass = "nonce_missing"
		return r, errors.New("Z.ai probe did not echo its nonce")
	}
	if !modelSession {
		r.FailureClass = "model_session_evidence_missing"
		return r, errors.New("Z.ai probe did not provide exact session-scoped model evidence")
	}
	r.ModelSessionEvidence = true
	r.SessionIDSHA256 = hash([]byte(sessionID))
	return r, nil
}

func probeArgs(nonce, model string) []string {
	return []string{"-p", "Reply with this exact nonce and no other text: " + nonce, "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--model", model, "--restricted", "--safe-mode", "--strict-mcp-config", "--disable-slash-commands", "--no-chrome", "--permission-mode", "dontAsk", "--tools", "", "--allowedTools", "", "--disallowedTools", "Bash,Edit,Write,NotebookEdit"}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// structuredErrorStatus reads only JSON numeric status and code values. It
// intentionally ignores provider prose, headers, and free-form stderr text.
func structuredErrorStatus(output []byte) (int, string) {
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		var object map[string]any
		if json.Unmarshal(line, &object) != nil {
			continue
		}
		status, code := exactStructuredError(object)
		if status != 0 || code != "" {
			return status, code
		}
	}
	return 0, ""
}

// exactStructuredError accepts only a top-level error record, a top-level
// error object, or an explicitly failed result event. It never searches
// arbitrary provider content for strings that merely resemble error codes.
func exactStructuredError(object map[string]any) (int, string) {
	candidate := object
	nestedError := false
	if nested, ok := object["error"].(map[string]any); ok {
		candidate = nested
		nestedError = true
	} else if typeName, _ := object["type"].(string); typeName != "" {
		if typeName != "error" {
			isError, _ := object["is_error"].(bool)
			if typeName != "result" || !isError {
				return 0, ""
			}
		}
	}
	status := firstJSONStatus(candidate, "status", "status_code", "http_status")
	if status == 0 && nestedError {
		status = firstJSONStatus(object, "status", "status_code", "http_status")
	}
	return status, firstJSONCode(candidate, "code", "error_code")
}

func firstJSONStatus(object map[string]any, keys ...string) int {
	for _, key := range keys {
		if value, ok := jsonNumber(object[key]); ok {
			return value
		}
	}
	return 0
}

func firstJSONCode(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := jsonCode(object[key]); ok {
			return value
		}
	}
	return ""
}
func jsonNumber(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 100 && n <= 599 && n == float64(int(n)) {
			return int(n), true
		}
	case string:
		value, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil && value >= 100 && value <= 599 {
			return value, true
		}
	}
	return 0, false
}
func jsonCode(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		if x != "" {
			return x, true
		}
	case float64:
		if x == float64(int(x)) {
			return fmt.Sprintf("%d", int(x)), true
		}
	}
	return "", false
}

func parseEvidence(output []byte, nonce, model string) (bool, bool, string) {
	sessionID := ""
	modelSession, nonceSeen, conflicting := false, false, false
	for _, line := range strings.Split(string(output), "\n") {
		var value any
		if json.Unmarshal([]byte(line), &value) != nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := object["type"].(string)
		if typeName == "system" {
			if subtype, _ := object["subtype"].(string); subtype == "init" {
				session, _ := object["session_id"].(string)
				actual, _ := object["model"].(string)
				if session == "" || actual == "" {
					conflicting = true
					continue
				}
				if actual != model || (sessionID != "" && sessionID != session) {
					conflicting = true
					continue
				}
				sessionID, modelSession = session, true
			}
			continue
		}
		if typeName == "result" && modelSession && !conflicting {
			subtype, _ := object["subtype"].(string)
			result, _ := object["result"].(string)
			resultSession, _ := object["session_id"].(string)
			isError, ok := object["is_error"].(bool)
			if subtype == "success" && ok && !isError && result == nonce && resultSession == sessionID {
				nonceSeen = true
			}
		}
	}
	return nonceSeen && !conflicting, modelSession && !conflicting, sessionID
}

func minimalEnvironment(in []string, endpoint, model string) []string {
	keep := map[string]bool{"PATH": true, "HOME": true, "USERPROFILE": true, "SystemRoot": true, "WINDIR": true, "TMP": true, "TEMP": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "TERM": true}
	out := make([]string, 0, 16)
	for _, item := range in {
		key, _, _ := strings.Cut(item, "=")
		if keep[key] {
			out = append(out, item)
		}
	}
	if auth := canonicalAuth(in); auth != "" {
		out = append(out, "ANTHROPIC_AUTH_TOKEN="+auth)
	}
	return append(out, "ANTHROPIC_BASE_URL="+endpoint, "ANTHROPIC_DEFAULT_OPUS_MODEL="+model, "ANTHROPIC_DEFAULT_SONNET_MODEL="+model, "ANTHROPIC_DEFAULT_HAIKU_MODEL="+model)
}
func canonicalAuth(in []string) string {
	var zaiToken, anthropicToken string
	for _, item := range in {
		key, value, _ := strings.Cut(item, "=")
		switch key {
		case "ZAI_API_KEY":
			zaiToken = value
		case "ANTHROPIC_AUTH_TOKEN":
			anthropicToken = value
		}
	}
	if zaiToken != "" {
		return zaiToken
	}
	return anthropicToken
}

func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ntm-zai-" + hex.EncodeToString(b), nil
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\\"'\\\"'") + "'" }
func classify(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "process_failed"
}
