// Package ollama provides an adapter for communicating with local Ollama instances.
// It enables NTM to use local LLMs for agent operations.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

// Default Ollama settings
const (
	DefaultHost    = "http://localhost:11434"
	DefaultTimeout = 120 * time.Second
)

// Common errors
var (
	ErrNotConnected          = errors.New("not connected to Ollama")
	ErrConnectionFailed      = errors.New("failed to connect to Ollama")
	ErrModelNotFound         = errors.New("model not found")
	ErrContextLengthExceeded = errors.New("context length exceeded")
	ErrGPUMemoryExhausted    = errors.New("GPU memory exhausted")
	ErrPullFailed            = errors.New("model pull failed")
	ErrDeleteFailed          = errors.New("model delete failed")
	ErrStreamClosed          = errors.New("response stream closed")
)

// AgentBackend defines the interface for LLM agent backends.
// This allows NTM to interact with different LLM providers uniformly.
type AgentBackend interface {
	// Connect establishes a connection to the backend
	Connect(host string) error

	// SendPrompt sends a prompt and returns the complete response
	SendPrompt(ctx context.Context, prompt string) (*Response, error)

	// StreamResponse sends a prompt and returns a channel of tokens
	StreamResponse(ctx context.Context, prompt string) (<-chan Token, error)

	// ListModels returns available models
	ListModels(ctx context.Context) ([]Model, error)

	// PullModel downloads a model
	PullModel(ctx context.Context, name string) error

	// PullModelWithProgress downloads a model and streams progress updates.
	PullModelWithProgress(ctx context.Context, name string, onProgress func(ModelPullProgress)) error

	// DeleteModel removes a local model.
	DeleteModel(ctx context.Context, name string) error

	// Close releases any resources
	Close() error
}

// Response represents a complete LLM response
type Response struct {
	Content      string        `json:"content"`
	Model        string        `json:"model"`
	Done         bool          `json:"done"`
	TotalTokens  int           `json:"total_tokens,omitempty"`
	PromptTokens int           `json:"prompt_tokens,omitempty"`
	OutputTokens int           `json:"output_tokens,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
}

// Token represents a single token from a streaming response
type Token struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   error  `json:"-"`
}

// Model represents an available Ollama model
type Model struct {
	Name       string       `json:"name"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	ModifiedAt time.Time    `json:"modified_at"`
	Details    ModelDetails `json:"details,omitempty"`
}

// ModelDetails contains additional model metadata
type ModelDetails struct {
	Format            string   `json:"format,omitempty"`
	Family            string   `json:"family,omitempty"`
	Families          []string `json:"families,omitempty"`
	ParameterSize     string   `json:"parameter_size,omitempty"`
	QuantizationLevel string   `json:"quantization_level,omitempty"`
}

// ModelPullProgress represents a single pull progress update from Ollama.
type ModelPullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
	Done      bool   `json:"done,omitempty"`
}

// Adapter implements AgentBackend for Ollama
type Adapter struct {
	mu        sync.RWMutex
	host      string
	client    *http.Client
	model     string // Current model to use
	connected bool
}

// NewAdapter creates a new Ollama adapter with default settings
func NewAdapter() *Adapter {
	return &Adapter{
		client: &http.Client{
			Timeout: DefaultTimeout,
		},
	}
}

// NewAdapterWithHost creates a new Ollama adapter with a specific host
func NewAdapterWithHost(host string) *Adapter {
	a := NewAdapter()
	_ = a.Connect(host)
	return a
}

// NewAdapterFromEnv creates an adapter using NTM_OLLAMA_HOST environment variable
func NewAdapterFromEnv() *Adapter {
	host := os.Getenv("NTM_OLLAMA_HOST")
	if host == "" {
		host = DefaultHost
	}
	return NewAdapterWithHost(host)
}

// Connect establishes a connection to the Ollama server
func (a *Adapter) Connect(host string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if host == "" {
		host = DefaultHost
	}

	// Normalize host URL
	host = strings.TrimSuffix(host, "/")
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	a.host = host

	// Test connection with a simple API call
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", a.host+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v (is Ollama running at %s?)", ErrConnectionFailed, err, host)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: server returned %d", ErrConnectionFailed, resp.StatusCode)
	}

	a.connected = true
	return nil
}

// SetModel sets the default model for prompts
func (a *Adapter) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
}

// GetModel returns the current model
func (a *Adapter) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

// IsConnected returns whether the adapter is connected
func (a *Adapter) IsConnected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.connected
}

// Host returns the current Ollama host
func (a *Adapter) Host() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.host
}

// ollamaGenerateRequest is the request body for /api/generate
type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

// ollamaChatRequest is the request body for /api/chat
type ollamaChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// ChatMessage represents a message in a chat conversation
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaGenerateResponse is the response from /api/generate
type ollamaGenerateResponse struct {
	Model              string `json:"model"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	Context            []int  `json:"context,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int    `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
}

// ollamaTagsResponse is the response from /api/tags
type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

// ollamaModel is a model entry from /api/tags
type ollamaModel struct {
	Name       string       `json:"name"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	ModifiedAt time.Time    `json:"modified_at"`
	Details    ModelDetails `json:"details"`
}

// ollamaPullRequest is the request body for /api/pull
type ollamaPullRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

// ollamaDeleteRequest is the request body for /api/delete
type ollamaDeleteRequest struct {
	Name string `json:"name"`
}

// ollamaPullResponse is a streaming response from /api/pull
type ollamaPullResponse struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

// SendPrompt sends a prompt and waits for the complete response
func (a *Adapter) SendPrompt(ctx context.Context, prompt string) (respOut *Response, err error) {
	a.mu.RLock()
	if !a.connected {
		a.mu.RUnlock()
		return nil, ErrNotConnected
	}
	host := a.host
	model := a.model
	a.mu.RUnlock()

	if model == "" {
		return nil, errors.New("no model set; call SetModel first")
	}

	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	_ = audit.LogEvent("", audit.EventTypeSend, audit.ActorSystem, "ollama.send", map[string]interface{}{
		"phase":          "start",
		"backend":        "ollama",
		"model":          model,
		"stream":         false,
		"prompt_preview": util.Truncate(strings.TrimSpace(prompt), 100),
		"prompt_length":  len(prompt),
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"backend":        "ollama",
			"model":          model,
			"stream":         false,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if respOut != nil {
			payload["output_preview"] = util.Truncate(strings.TrimSpace(respOut.Content), 120)
			payload["output_length"] = len(respOut.Content)
			payload["total_tokens"] = respOut.TotalTokens
			payload["prompt_tokens"] = respOut.PromptTokens
			payload["output_tokens"] = respOut.OutputTokens
			payload["done"] = respOut.Done
		}
		if err != nil {
			payload["error"] = err.Error()
			_ = audit.LogEvent("", audit.EventTypeError, audit.ActorSystem, "ollama.send", payload, nil)
			return
		}
		_ = audit.LogEvent("", audit.EventTypeResponse, audit.ActorSystem, "ollama.response", payload, nil)
	}()

	reqBody := ollamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.classifyError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, a.parseErrorResponse(resp)
	}

	var ollamaResp ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	respOut = &Response{
		Content:      ollamaResp.Response,
		Model:        ollamaResp.Model,
		Done:         ollamaResp.Done,
		PromptTokens: ollamaResp.PromptEvalCount,
		OutputTokens: ollamaResp.EvalCount,
		TotalTokens:  ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		Duration:     time.Duration(ollamaResp.TotalDuration),
	}
	return respOut, nil
}

// StreamResponse sends a prompt and returns a channel of streaming tokens
func (a *Adapter) StreamResponse(ctx context.Context, prompt string) (stream <-chan Token, err error) {
	a.mu.RLock()
	if !a.connected {
		a.mu.RUnlock()
		return nil, ErrNotConnected
	}
	host := a.host
	model := a.model
	a.mu.RUnlock()

	if model == "" {
		return nil, errors.New("no model set; call SetModel first")
	}

	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	_ = audit.LogEvent("", audit.EventTypeSend, audit.ActorSystem, "ollama.stream", map[string]interface{}{
		"phase":          "start",
		"backend":        "ollama",
		"model":          model,
		"stream":         true,
		"prompt_preview": util.Truncate(strings.TrimSpace(prompt), 100),
		"prompt_length":  len(prompt),
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"backend":        "ollama",
			"model":          model,
			"stream":         true,
			"stream_started": err == nil,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if err != nil {
			payload["error"] = err.Error()
			_ = audit.LogEvent("", audit.EventTypeError, audit.ActorSystem, "ollama.stream", payload, nil)
			return
		}
		_ = audit.LogEvent("", audit.EventTypeResponse, audit.ActorSystem, "ollama.stream", payload, nil)
	}()

	reqBody := ollamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", host+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req) //nolint:bodyclose // body is closed in goroutine below
	if err != nil {
		return nil, a.classifyError(err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, a.parseErrorResponse(resp)
	}

	tokenChan := make(chan Token, 100)

	go func() {
		defer close(tokenChan)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		// Increase buffer size for large responses
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				tokenChan <- Token{Error: ctx.Err()}
				return
			default:
			}

			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}

			var ollamaResp ollamaGenerateResponse
			if err := json.Unmarshal(line, &ollamaResp); err != nil {
				tokenChan <- Token{Error: fmt.Errorf("failed to parse stream: %w", err)}
				return
			}

			tokenChan <- Token{
				Content: ollamaResp.Response,
				Done:    ollamaResp.Done,
			}

			if ollamaResp.Done {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			tokenChan <- Token{Error: fmt.Errorf("stream error: %w", err)}
		}
	}()

	return tokenChan, nil
}

// ListModels returns all available models on the Ollama server
func (a *Adapter) ListModels(ctx context.Context) ([]Model, error) {
	a.mu.RLock()
	if !a.connected {
		a.mu.RUnlock()
		return nil, ErrNotConnected
	}
	host := a.host
	a.mu.RUnlock()

	req, err := http.NewRequestWithContext(ctx, "GET", host+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, a.classifyError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, a.parseErrorResponse(resp)
	}

	var tagsResp ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	models := make([]Model, len(tagsResp.Models))
	for i, m := range tagsResp.Models {
		models[i] = Model(m)
	}

	return models, nil
}

// PullModel downloads a model from the Ollama registry
func (a *Adapter) PullModel(ctx context.Context, name string) error {
	return a.PullModelWithProgress(ctx, name, nil)
}

// PullModelWithProgress downloads a model from the Ollama registry and emits progress callbacks.
func (a *Adapter) PullModelWithProgress(ctx context.Context, name string, onProgress func(ModelPullProgress)) error {
	a.mu.RLock()
	if !a.connected {
		a.mu.RUnlock()
		return ErrNotConnected
	}
	host := a.host
	a.mu.RUnlock()

	reqBody := ollamaPullRequest{
		Name:   name,
		Stream: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", host+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPullFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return a.parseErrorResponse(resp)
	}

	// Read streaming response to completion
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var lastStatus string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var pullResp ollamaPullResponse
		if err := json.Unmarshal(line, &pullResp); err != nil {
			continue // Skip malformed lines
		}

		lastStatus = pullResp.Status
		done := pullResp.Status == "success" ||
			strings.Contains(strings.ToLower(pullResp.Status), "done") ||
			strings.Contains(strings.ToLower(pullResp.Status), "complete")
		if onProgress != nil {
			onProgress(ModelPullProgress{
				Status:    pullResp.Status,
				Digest:    pullResp.Digest,
				Total:     pullResp.Total,
				Completed: pullResp.Completed,
				Done:      done,
			})
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%w: stream error: %v", ErrPullFailed, err)
	}

	if lastStatus != "success" && lastStatus != "" {
		// Some status messages indicate completion without "success"
		if !strings.Contains(strings.ToLower(lastStatus), "done") &&
			!strings.Contains(strings.ToLower(lastStatus), "complete") {
			return fmt.Errorf("%w: %s", ErrPullFailed, lastStatus)
		}
	}

	return nil
}

// DeleteModel removes a local model from Ollama.
func (a *Adapter) DeleteModel(ctx context.Context, name string) error {
	a.mu.RLock()
	if !a.connected {
		a.mu.RUnlock()
		return ErrNotConnected
	}
	host := a.host
	a.mu.RUnlock()

	reqBody := ollamaDeleteRequest{Name: name}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", host+"/api/delete", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeleteFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return a.parseErrorResponse(resp)
	}

	return nil
}

// Close releases any resources held by the adapter
func (a *Adapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected = false
	return nil
}

// classifyError converts network/connection errors to specific error types
func (a *Adapter) classifyError(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Connection refused usually means Ollama isn't running
	if strings.Contains(errStr, "connection refused") {
		return fmt.Errorf("%w: %v (is Ollama running?)", ErrConnectionFailed, err)
	}

	// Timeout errors
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
		return fmt.Errorf("request timed out: %w", err)
	}

	return err
}

// parseErrorResponse extracts error information from an HTTP response
func (a *Adapter) parseErrorResponse(resp *http.Response) error {
	// Limit read to 1MB to prevent OOM on unexpected large error responses
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	bodyStr := string(body)

	// Try to parse as JSON error
	var errResp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
		bodyStr = errResp.Error
	}

	// Classify error based on status code and message
	switch resp.StatusCode {
	case http.StatusNotFound:
		if strings.Contains(bodyStr, "model") {
			return fmt.Errorf("%w: %s", ErrModelNotFound, bodyStr)
		}
		return fmt.Errorf("not found: %s", bodyStr)

	case http.StatusBadRequest:
		if strings.Contains(strings.ToLower(bodyStr), "context") &&
			strings.Contains(strings.ToLower(bodyStr), "length") {
			return ErrContextLengthExceeded
		}
		return fmt.Errorf("bad request: %s", bodyStr)

	case http.StatusInternalServerError:
		if strings.Contains(strings.ToLower(bodyStr), "memory") ||
			strings.Contains(strings.ToLower(bodyStr), "gpu") ||
			strings.Contains(strings.ToLower(bodyStr), "cuda") {
			return fmt.Errorf("%w: %s", ErrGPUMemoryExhausted, bodyStr)
		}
		return fmt.Errorf("server error: %s", bodyStr)

	default:
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, bodyStr)
	}
}
