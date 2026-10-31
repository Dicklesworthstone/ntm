package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sort"
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

// Callers of pipeline recovery must echo receipt_token exactly, so that one route
// returns it unredacted. The route and the value's shape both have to match; the
// same key anywhere else in serve is redacted like any other credential field.
var pipelineRecoverPath = regexp.MustCompile(`^/api/v1/pipelines/[^/]+/recover$`)

const receiptTokenAllowlist = `^"receipt_token"\s*:\s*"[0-9a-f]{64}"$`

// jsonCredentialPair splits a whole "key":"value" finding, keeping the key and
// the separator.
var jsonCredentialPair = regexp.MustCompile(`^("(?:\\.|[^"\\])*"\s*:\s*)"(?:\\.|[^"\\])*"$`)

const redactionMaxRequestBodyBytes = 10 << 20

// requestRedactionFinding identifies a location without disclosing the match,
// its placeholder hash, or a caller-controlled field name (which may itself be
// a secret). Offsets refer to the original request bytes; End is exclusive.
type requestRedactionFinding struct {
	Category redaction.Category `json:"category"`
	Start    int                `json:"start"`
	End      int                `json:"end"`
}

// responseRedactionConfig returns the config for the response to path.
func responseRedactionConfig(cfg redaction.Config, path string) redaction.Config {
	if pipelineRecoverPath.MatchString(path) {
		cfg.Allowlist = append(slices.Clip(cfg.Allowlist), receiptTokenAllowlist)
	}
	return cfg
}

// redactionMiddleware scans JSON requests and redacts JSON responses. In redact
// and block modes, requests with findings are rejected before dispatch. Request
// values must never be replaced: even a JSON-preserving rewrite could cause a
// handler to persist a placeholder as a credential or execute altered input.
func (s *Server) redactionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Snapshot config under lock to avoid races with SetRedactionConfig
		s.mu.RLock()
		rcfg := s.redactionCfg
		s.mu.RUnlock()

		// Skip if redaction not enabled
		if rcfg == nil || !rcfg.Enabled || rcfg.Config.Mode == redaction.ModeOff {
			next.ServeHTTP(w, r)
			return
		}

		reqID := requestIDFromContext(r.Context())
		cfg := rcfg.Config

		// Track findings for summary
		summary := &RedactionSummary{
			RequestID: reqID,
			Path:      r.URL.Path,
			Method:    r.Method,
		}
		categories := make(map[string]int)

		// Admit JSON input without changing any submitted values. Do not rely
		// on ContentLength: chunked bodies have no known length.
		if r.Body != nil && r.Body != http.NoBody {
			contentType := r.Header.Get("Content-Type")
			// Every v1 API body consumer decodes JSON, usually without checking
			// Content-Type. A missing or mislabeled header must not bypass the
			// policy on routes that can store or act on submitted values.
			if isJSONContent(contentType) || r.URL.Path == "/api/v1" || strings.HasPrefix(r.URL.Path, "/api/v1/") {
				// Read one byte past the limit so a prefix can never be accepted
				// as the entire request. The router may already wrap the body
				// in MaxBytesReader; recognize its error as the same refusal.
				limitedBody := io.LimitReader(r.Body, redactionMaxRequestBodyBytes+1)
				body, err := io.ReadAll(limitedBody)
				r.Body.Close()
				var tooLarge *http.MaxBytesError
				if len(body) > redactionMaxRequestBodyBytes || errors.As(err, &tooLarge) {
					writeErrorResponse(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE",
						"request body exceeds the 10 MiB limit", nil, reqID)
					return
				}
				if err != nil {
					writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "failed to read request body", nil, reqID)
					return
				}

				// Redact mode applies only to output. Scan input in block mode
				// instead so we neither construct nor forward substituted data.
				requestCfg := cfg
				if requestCfg.Mode == redaction.ModeRedact {
					requestCfg.Mode = redaction.ModeBlock
				}
				result := redaction.ScanAndRedact(string(body), requestCfg)
				summary.RequestFinds = len(result.Findings)
				for _, f := range result.Findings {
					categories[string(f.Category)]++
				}
				if result.Blocked {
					summary.Blocked = true
					summary.Categories = categories
					logRedactionSummary(summary)

					// Bound diagnostic output, and never serialize Finding itself:
					// it contains the submitted secret in Match.
					findings := make([]requestRedactionFinding, min(len(result.Findings), 100))
					for i, f := range result.Findings[:len(findings)] {
						findings[i] = requestRedactionFinding{Category: f.Category, Start: f.Start, End: f.End}
					}
					details := map[string]interface{}{
						"findings_count": len(result.Findings),
						"findings":       findings,
						"hint":           "Remove sensitive content at the reported byte offsets and retry; request values were not changed.",
					}
					if len(findings) < len(result.Findings) {
						details["findings_truncated"] = true
					}
					writeErrorResponse(w, http.StatusUnprocessableEntity, "SECRETS_DETECTED",
						"request contains sensitive content; refusing to substitute redaction placeholders for submitted values",
						details, reqID)
					return
				}

				// Keep syntax errors out of handlers too. Do not echo decoder
				// errors, which can include bytes from sensitive input. Empty
				// bodies remain available to routes with optional JSON input.
				if len(body) > 0 && !json.Valid(body) {
					writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid JSON request body", nil, reqID)
					return
				}

				// Preserve the original bytes, including whitespace, duplicate
				// keys, escaped strings and numbers requiring exact precision.
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
			}
		}

		// Wrap response writer to capture and redact response
		rw := &redactingResponseWriter{
			ResponseWriter: w,
			cfg:            responseRedactionConfig(cfg, r.URL.Path),
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
	hijacked    bool
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
	// After Flush() is called, write directly (streaming passthrough mode)
	if rw.finalized {
		return rw.ResponseWriter.Write(b)
	}
	// Buffer the response for redaction
	return rw.buffer.Write(b)
}

// finalize processes the buffered response, applies redaction, and writes to the underlying writer.
func (rw *redactingResponseWriter) finalize() {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if rw.finalized || rw.hijacked {
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
		body = []byte(jsonResponseOutput(string(body), result))
	}

	// Since we buffered the response (and potentially changed its length during redaction),
	// we must update the Content-Length header to prevent HTTP protocol framing errors.
	if rw.Header().Get("Content-Length") != "" {
		rw.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}

	// Write the actual response
	rw.ResponseWriter.WriteHeader(rw.statusCode)
	if _, err := rw.ResponseWriter.Write(body); err != nil {
		log.Printf("redact: failed to write response body: %v", err)
	}
}

// jsonResponseOutput returns result's output for a JSON body. ScanAndRedact
// replaces a whole "key":"value" credential pair, quotes included, which suits
// free text but leaves a JSON response unparseable. Here only the value goes, and
// a closing quote that a finding swallowed stays.
func jsonResponseOutput(input string, result redaction.Result) string {
	if result.Mode != redaction.ModeRedact || len(result.Findings) == 0 {
		return result.Output
	}
	findings := slices.Clone(result.Findings)
	sort.Slice(findings, func(i, j int) bool { return findings[i].Start < findings[j].Start })

	var out strings.Builder
	out.Grow(len(input))
	last := 0
	for _, f := range findings {
		if f.Start < last || f.Start > f.End || f.End > len(input) {
			continue
		}
		span := input[f.Start:f.End]
		out.WriteString(input[last:f.Start])
		if pair := jsonCredentialPair.FindStringSubmatch(span); pair != nil {
			out.WriteString(pair[1] + `"` + f.Redacted + `"`)
		} else if strings.HasSuffix(span, `"`) && !strings.HasSuffix(span, `\"`) {
			out.WriteString(f.Redacted + `"`)
		} else {
			out.WriteString(f.Redacted)
		}
		last = f.End
	}
	out.WriteString(input[last:])
	return out.String()
}

// Flush implements http.Flusher for streaming responses (SSE, chunked).
// Once Flush is called, the writer switches to passthrough mode — subsequent
// writes go directly to the underlying ResponseWriter without buffering,
// since streaming responses can't be buffered-then-redacted as a whole.
func (rw *redactingResponseWriter) Flush() {
	rw.mu.Lock()
	if !rw.finalized {
		rw.finalized = true // Switch to passthrough mode

		if rw.wroteHeader {
			rw.ResponseWriter.WriteHeader(rw.statusCode)
			rw.wroteHeader = false
		}

		// Flush any already-buffered content first
		body := rw.buffer.Bytes()
		if len(body) > 0 {
			if _, err := rw.ResponseWriter.Write(body); err != nil {
				log.Printf("redact: failed to flush buffered streaming body: %v", err)
			}
			rw.buffer.Reset()
		}
	}
	rw.mu.Unlock()

	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack preserves websocket and other upgraded connection flows when redaction
// middleware wraps the response writer.
func (rw *redactingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}

	rw.mu.Lock()
	rw.hijacked = true
	rw.finalized = true
	rw.buffer.Reset()
	rw.mu.Unlock()

	return hijacker.Hijack()
}

// Push preserves optional HTTP/2 server push support when available.
func (rw *redactingResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := rw.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

// Unwrap exposes the underlying response writer for ResponseController callers.
func (rw *redactingResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
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
