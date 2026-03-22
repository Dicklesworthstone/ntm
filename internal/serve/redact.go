package serve

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

// RedactionConfig holds the server-level redaction configuration.
type RedactionConfig struct {
	// Enabled determines if redaction is active.
	Enabled bool
	// Config is the redaction library configuration.
	Config redaction.Config
}

// RedactionSummary is logged after request/response redaction.
type RedactionSummary struct {
	RequestID     string         `json:"request_id"`
	Path          string         `json:"path"`
	Method        string         `json:"method"`
	RequestFinds  int            `json:"request_findings"`
	ResponseFinds int            `json:"response_findings"`
	Categories    map[string]int `json:"categories,omitempty"`
	Blocked       bool           `json:"blocked,omitempty"`
}

// redactionMiddleware creates middleware that redacts sensitive content in requests and responses.
// It scans JSON request bodies and response bodies for secrets and redacts or blocks as configured.
func (s *Server) redactionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip if redaction not enabled
		if s.redactionCfg == nil || !s.redactionCfg.Enabled || s.redactionCfg.Config.Mode == redaction.ModeOff {
			next.ServeHTTP(w, r)
			return
		}

		reqID := requestIDFromContext(r.Context())
		cfg := s.redactionCfg.Config

		// Track findings for summary
		summary := &RedactionSummary{
			RequestID: reqID,
			Path:      r.URL.Path,
			Method:    r.Method,
		}
		categories := make(map[string]int)

		// Handle request body redaction for JSON content
		if r.Body != nil && r.ContentLength > 0 {
			contentType := r.Header.Get("Content-Type")
			if isJSONContent(contentType) {
				body, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "failed to read request body", nil, reqID)
					return
				}

				// Scan/redact the request body
				result := redaction.ScanAndRedact(string(body), cfg)
				if result.Blocked {
					summary.Blocked = true
					summary.RequestFinds = len(result.Findings)
					for _, f := range result.Findings {
						categories[string(f.Category)]++
					}
					summary.Categories = categories
					logRedactionSummary(summary)
					writeErrorResponse(w, http.StatusUnprocessableEntity, "SECRETS_DETECTED",
						"request contains sensitive content that cannot be processed",
						map[string]interface{}{"findings_count": len(result.Findings)}, reqID)
					return
				}

				summary.RequestFinds = len(result.Findings)
				for _, f := range result.Findings {
					categories[string(f.Category)]++
				}

				// Replace body with redacted content
				r.Body = io.NopCloser(bytes.NewReader([]byte(result.Output)))
				r.ContentLength = int64(len(result.Output))
			}
		}

		// Wrap response writer to capture and redact response
		rw := &redactingResponseWriter{
			ResponseWriter: w,
			cfg:            cfg,
			summary:        summary,
			categories:     categories,
			buffer:         &bytes.Buffer{},
		}

		next.ServeHTTP(rw, r)

		// Finalize response (write redacted content)
		rw.finalize()

		// Log summary if there were any findings
		if summary.RequestFinds > 0 || summary.ResponseFinds > 0 {
			summary.Categories = categories
			logRedactionSummary(summary)
		}
	})
}

// redactingResponseWriter wraps http.ResponseWriter to intercept and redact response bodies.
type redactingResponseWriter struct {
	http.ResponseWriter
	cfg         redaction.Config
	summary     *RedactionSummary
	categories  map[string]int
	buffer      *bytes.Buffer
	statusCode  int
	wroteHeader bool
	finalized   bool
	mu          sync.Mutex
}

func (rw *redactingResponseWriter) WriteHeader(code int) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if !rw.wroteHeader {
		rw.statusCode = code
		rw.wroteHeader = true
	}
}

func (rw *redactingResponseWriter) Write(b []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if !rw.wroteHeader {
		rw.statusCode = http.StatusOK
		rw.wroteHeader = true
	}
	// Buffer the response for redaction
	return rw.buffer.Write(b)
}

// finalize processes the buffered response, applies redaction, and writes to the underlying writer.
func (rw *redactingResponseWriter) finalize() {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if rw.finalized {
		return
	}
	rw.finalized = true

	body := rw.buffer.Bytes()
	if len(body) == 0 {
		if rw.wroteHeader {
			rw.ResponseWriter.WriteHeader(rw.statusCode)
		}
		return
	}

	// Check if response is JSON
	contentType := rw.Header().Get("Content-Type")
	if isJSONContent(contentType) {
		// Apply redaction to JSON response
		result := redaction.ScanAndRedact(string(body), rw.cfg)
		rw.summary.ResponseFinds = len(result.Findings)
		for _, f := range result.Findings {
			rw.categories[string(f.Category)]++
		}
		body = []byte(result.Output)
	}

	// Write the actual response
	rw.ResponseWriter.WriteHeader(rw.statusCode)
	rw.ResponseWriter.Write(body)
}

// Flush implements http.Flusher for streaming responses.
func (rw *redactingResponseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		// For streaming, we can't buffer - just pass through
		rw.finalize()
		f.Flush()
	}
}

// isJSONContent checks if the content type is JSON.
// Content-Type comparison is case-insensitive per RFC 2616.
func isJSONContent(contentType string) bool {
	ct := strings.ToLower(contentType)
	if ct == "application/json" {
		return true
	}
	// Match "application/json; charset=utf-8" but not "application/jsonpatch"
	return len(ct) > 16 && ct[:16] == "application/json" && (ct[16] == ';' || ct[16] == ' ')
}

// logRedactionSummary logs a redaction summary.
func logRedactionSummary(summary *RedactionSummary) {
	data, err := json.Marshal(summary)
	if err != nil {
		log.Printf("REDACTION: request_id=%s path=%s request_findings=%d response_findings=%d blocked=%v",
			summary.RequestID, summary.Path, summary.RequestFinds, summary.ResponseFinds, summary.Blocked)
		return
	}
	log.Printf("REDACTION: %s", string(data))
}

// RedactJSON redacts sensitive content in a JSON value.
// This is useful for redacting specific fields in request/response structures.
func RedactJSON(data interface{}, cfg redaction.Config) (interface{}, int) {
	if cfg.Mode == redaction.ModeOff {
		return data, 0
	}

	totalFindings := 0

	switch v := data.(type) {
	case string:
		result := redaction.ScanAndRedact(v, cfg)
		return result.Output, len(result.Findings)
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(v))
		for key, val := range v {
			redactedVal, findings := RedactJSON(val, cfg)
			redacted[key] = redactedVal
			totalFindings += findings
		}
		return redacted, totalFindings
	case []interface{}:
		redacted := make([]interface{}, len(v))
		for i, val := range v {
			redactedVal, findings := RedactJSON(val, cfg)
			redacted[i] = redactedVal
			totalFindings += findings
		}
		return redacted, totalFindings
	default:
		return data, 0
	}
}

// RedactRequestFields redacts specific fields in a request struct.
// Fields must be string pointers or string fields.
func RedactRequestFields(cfg redaction.Config, fields ...*string) int {
	if cfg.Mode == redaction.ModeOff {
		return 0
	}

	totalFindings := 0
	for _, field := range fields {
		if field == nil || *field == "" {
			continue
		}
		result := redaction.ScanAndRedact(*field, cfg)
		totalFindings += len(result.Findings)
		if cfg.Mode == redaction.ModeRedact {
			*field = result.Output
		}
	}
	return totalFindings
}

// SetRedactionConfig sets the redaction configuration for the server.
// Pass nil to disable redaction.
func (s *Server) SetRedactionConfig(cfg *RedactionConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redactionCfg = cfg
}

// GetRedactionConfig returns the current redaction configuration.
func (s *Server) GetRedactionConfig() *RedactionConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.redactionCfg
}
