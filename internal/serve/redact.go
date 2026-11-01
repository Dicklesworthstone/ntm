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
	"net/url"
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
	RequestID         string         `json:"request_id"`
	Path              string         `json:"path"`
	Method            string         `json:"method"`
	RequestFinds      int            `json:"request_findings"`
	ResponseFinds     int            `json:"response_findings"`
	Categories        map[string]int `json:"categories,omitempty"`
	Blocked           bool           `json:"blocked,omitempty"`
	InspectionLimited bool           `json:"inspection_limited,omitempty"`
}

// Callers of pipeline recovery must echo receipt_token exactly, so that one route
// returns it unredacted. The route and the value's shape both have to match; the
// same key anywhere else in serve is redacted like any other credential field.
var pipelineRecoverPath = regexp.MustCompile(`^/api/v1/pipelines/[^/]+/recover$`)

const receiptTokenAllowlist = `^"receipt_token"\s*:\s*"[0-9a-f]{64}"$`

// jsonCredentialPair splits a whole "key":"value" finding, keeping the key and
// the separator.
var jsonCredentialPair = regexp.MustCompile(`^("(?:\\.|[^"\\])*"\s*:\s*)"(?:\\.|[^"\\])*"$`)

// Persisted event output can be scanned again during replay. A complete marker
// is already protected; rehashing it would make live and replayed data disagree.
// Only built-in password findings use this exemption (never custom patterns or
// request admission), since their matcher also accepts arbitrary quoted text.
var jsonRedactionMarker = regexp.MustCompile(`^"\[REDACTED:[A-Z][A-Z0-9_]*:[0-9a-f]{8}\]"$`)

const redactionMaxRequestBodyBytes = maxRequestBodyBytes

// A response is filtered after its handler has run. Keep the handler's status
// and make an unavailable representation explicit without claiming rollback or
// encouraging a retry of an operation that may already have committed.
const redactionUnavailableResponse = `{"response_unavailable":true,"error_code":"RESPONSE_REDACTION_FAILED","error":"Response content could not be safely redacted","hint":"The handler has already run and may have completed the operation. Check its state before retrying."}`

// requestRedactionFinding identifies a location without disclosing the match,
// its placeholder hash, or a caller-controlled field name (which may itself be
// a secret). Offsets refer to the original request bytes; End is exclusive.
type requestRedactionFinding struct {
	Category redaction.Category `json:"category"`
	Start    int                `json:"start"`
	End      int                `json:"end"`
}

type requestRedactionConfigKey struct{}

// A request uses one policy for admission, correlation, and diagnostics. Event
// streams continue to snapshot their policy separately for each emitted frame.
func (s *Server) redactionConfigSnapshot() redaction.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.redactionCfg == nil || !s.redactionCfg.Enabled {
		return redaction.Config{Mode: redaction.ModeOff}
	}
	return s.redactionCfg.Config
}

func (s *Server) requestRedactionConfig(r *http.Request) redaction.Config {
	if cfg, ok := r.Context().Value(requestRedactionConfigKey{}).(redaction.Config); ok {
		return cfg
	}
	return s.redactionConfigSnapshot()
}

func enforcesRequestRedaction(cfg redaction.Config) bool {
	return cfg.Mode == redaction.ModeRedact || cfg.Mode == redaction.ModeBlock
}

// Metadata can contain a JSON document as well as plain text. Wrapping the
// exact value as a JSON string lets the existing scanner inspect both without
// changing the route, header, query, or principal used by application code.
func scanRequestMetadata(value string, cfg redaction.Config) redaction.Result {
	if value == "" || !enforcesRequestRedaction(cfg) {
		return redaction.Result{Mode: cfg.Mode, Output: value}
	}
	cfg.Mode = redaction.ModeBlock
	data, _ := json.Marshal(value) // Strings are always JSON-serializable.
	return scanServerJSON(string(data), cfg)
}

func scanServerJSON(value string, cfg redaction.Config) redaction.Result {
	if !enforcesRequestRedaction(cfg) {
		return redaction.ScanJSONAndRedact(value, cfg)
	}
	return redaction.ScanJSONAndRedactWithLimits(value, cfg, redaction.DefaultScanLimits())
}

func scanServerText(value string, cfg redaction.Config) redaction.Result {
	if !enforcesRequestRedaction(cfg) {
		return redaction.ScanAndRedact(value, cfg)
	}
	return redaction.ScanAndRedactWithLimits(value, cfg, redaction.DefaultScanLimits())
}

var errRedactionInspectionLimit = errors.New("redaction inspection exceeded its work limit")

func writeRequestInspectionLimit(w http.ResponseWriter, source, requestID string) {
	writeErrorResponse(w, http.StatusUnprocessableEntity, "REDACTION_INSPECTION_LIMIT",
		"request could not be completely inspected within redaction limits; request values were not changed",
		map[string]interface{}{
			"source": source,
			"hint":   "Reduce the submitted input or simplify the configured redaction patterns before retrying.",
		}, requestID)
}

// admitDecodedRequest checks values revealed by a handler's secondary parser,
// such as YAML inside a JSON string. Call it before validation diagnostics or
// side effects can consume those values. The JSON copy is only for inspection;
// neither it nor scanner output replaces the submitted content.
func (s *Server) admitDecodedRequest(w http.ResponseWriter, r *http.Request, value any, source string) bool {
	cfg := s.requestRedactionConfig(r)
	if !enforcesRequestRedaction(cfg) {
		return true
	}
	reqID := requestIDFromContext(r.Context())
	data, err := json.Marshal(value)
	if err != nil {
		writeErrorResponse(w, http.StatusUnprocessableEntity, "REDACTION_INSPECTION_FAILED",
			"decoded request content could not be inspected; request values were not changed",
			map[string]interface{}{"source": source}, reqID)
		return false
	}
	cfg.Mode = redaction.ModeBlock
	result := scanServerJSON(string(data), cfg)
	if !result.Blocked && len(result.Findings) == 0 {
		return true
	}
	summary := &RedactionSummary{
		RequestID:         reqID,
		Path:              redactRequestDiagnostic(r.URL.Path, cfg),
		Method:            redactRequestDiagnostic(r.Method, cfg),
		Blocked:           true,
		InspectionLimited: result.LimitExceeded,
	}
	if result.LimitExceeded {
		logRedactionSummary(summary)
		writeRequestInspectionLimit(w, source, reqID)
		return false
	}
	summary.RequestFinds = len(result.Findings)
	summary.Categories = make(map[string]int)
	for _, finding := range result.Findings {
		summary.Categories[string(finding.Category)]++
	}
	logRedactionSummary(summary)
	// Decoded offsets do not identify locations in the original request. Keep
	// only category counts and a static source label, never values or field names.
	writeErrorResponse(w, http.StatusUnprocessableEntity, "SECRETS_DETECTED",
		"decoded request content contains sensitive values; refusing to substitute redaction placeholders",
		map[string]interface{}{
			"source":         source,
			"findings_count": summary.RequestFinds,
			"categories":     summary.Categories,
			"hint":           "Remove sensitive content from the submitted document and retry; request values were not changed.",
		}, reqID)
	return false
}

// redactRequestDiagnostic protects an observational copy only. Ambiguous
// custom-pattern replacements withhold that field, never its operational value.
func redactRequestDiagnostic(value string, cfg redaction.Config) string {
	if value == "" || !enforcesRequestRedaction(cfg) {
		return value
	}
	data, _ := json.Marshal(value)
	output, result, err := redactJSONBytes(data, cfg)
	if err != nil {
		return "[REDACTED]"
	}
	if len(result.Findings) == 0 {
		return value
	}
	var protected string
	if json.Unmarshal(output, &protected) != nil {
		return "[REDACTED]"
	}
	return protected
}

// Replay can predate the enforcing policy. These headers carry identities, so
// omit an unsafe cached value rather than substituting a different identifier
// or URL. The cache's original representation remains intact.
func protectReplayMetadata(header http.Header, cfg redaction.Config) {
	if !enforcesRequestRedaction(cfg) {
		return
	}
	for _, key := range []string{requestIDHeader, "Location"} {
		for _, value := range header.Values(key) {
			unsafe := scanRequestMetadata(value, cfg).Blocked
			if key == "Location" && !unsafe {
				decoded, err := url.QueryUnescape(value)
				unsafe = err != nil || scanRequestMetadata(decoded, cfg).Blocked
			}
			if unsafe {
				header.Del(key)
				break
			}
		}
	}
}

// Paths and query values reach handler logs and can also select or modify
// stored resources. Refuse sensitive values before dispatch instead of passing
// a placeholder that would name a different resource or instruction.
func admitRequestURL(w http.ResponseWriter, r *http.Request, cfg redaction.Config, summary *RedactionSummary, categories map[string]int) bool {
	if !enforcesRequestRedaction(cfg) {
		return true
	}
	metadataCfg := cfg
	metadataCfg.Mode = redaction.ModeBlock
	// A query with many individually cheap values must not reset the work
	// budget or recompile patterns for every entry.
	scanner := redaction.NewScanner(metadataCfg, redaction.DefaultScanLimits())
	refuse := func(source, value string) bool {
		data, _ := json.Marshal(value)
		result := scanner.ScanJSONAndRedact(string(data))
		if result.LimitExceeded {
			summary.Blocked = true
			summary.InspectionLimited = true
			logRedactionSummary(summary)
			writeRequestInspectionLimit(w, source, summary.RequestID)
			return true
		}
		findings := result.Findings
		if len(findings) == 0 {
			return false
		}
		for _, finding := range findings {
			categories[string(finding.Category)]++
		}
		summary.RequestFinds = len(findings)
		summary.Blocked = true
		summary.Categories = categories
		logRedactionSummary(summary)
		writeErrorResponse(w, http.StatusUnprocessableEntity, "SECRETS_DETECTED",
			"request URL contains sensitive content; refusing to substitute redaction placeholders for submitted values",
			map[string]interface{}{
				"source": source,
				"hint":   "Remove sensitive content from the reported URL component and retry; request values were not changed.",
			}, summary.RequestID)
		return true
	}
	if refuse("path", r.URL.Path) {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeErrorResponse(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid request query encoding", nil, summary.RequestID)
		return false
	}
	if refuse("query", r.URL.RawQuery) {
		return false
	}
	for key, values := range query {
		for _, value := range values {
			// Keep each repeated value paired with its decoded key. Scanning
			// values alone (or an array) loses generic credential-field context.
			entry, _ := json.Marshal(map[string]string{key: value})
			if refuse("query", string(entry)) {
				return false
			}
		}
	}
	return true
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
		cfg := s.requestRedactionConfig(r)
		if cfg.Mode == redaction.ModeOff {
			next.ServeHTTP(w, r)
			return
		}

		reqID := requestIDFromContext(r.Context())
		// Track findings for summary
		summary := &RedactionSummary{
			RequestID: reqID,
			Path:      redactRequestDiagnostic(r.URL.Path, cfg),
			Method:    redactRequestDiagnostic(r.Method, cfg),
		}
		categories := make(map[string]int)
		if !admitRequestURL(w, r, cfg, summary, categories) {
			return
		}

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
				result := scanServerJSON(string(body), requestCfg)
				if result.LimitExceeded {
					summary.Blocked = true
					summary.InspectionLimited = true
					logRedactionSummary(summary)
					writeRequestInspectionLimit(w, "body", reqID)
					return
				}
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
		if summary.RequestFinds > 0 || summary.ResponseFinds > 0 || summary.InspectionLimited {
			summary.Categories = categories
			logRedactionSummary(summary)
		}
	})
}

// redactingResponseWriter wraps http.ResponseWriter to intercept and redact response bodies.
type redactingResponseWriter struct {
	http.ResponseWriter
	cfg        redaction.Config
	summary    *RedactionSummary
	categories map[string]int
	buffer     *bytes.Buffer
	// A nonpositive captureLimit uses the scanner's input budget. This is an
	// internal writer bound, not a separate server configuration.
	captureLimit      int
	captureOverflow   bool
	streamingRejected bool
	wroteBody         bool
	sseBeforeBody     bool
	statusCode        int
	wroteHeader       bool
	finalized         bool
	hijacked          bool
	mu                sync.Mutex
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
	rw.rejectLateSSELocked()
	if rw.captureOverflow || rw.streamingRejected {
		// The handler may already have changed state. Let it finish its
		// bookkeeping, including retaining an idempotency result, without
		// exposing a prefix or keeping more of the unavailable response.
		return len(b), nil
	}
	// Only explicitly framed SSE producers may enter streaming passthrough.
	// They protect each complete event before writing it.
	if rw.finalized {
		return rw.ResponseWriter.Write(b)
	}
	if len(b) > 0 && !rw.wroteBody {
		rw.wroteBody = true
		rw.sseBeforeBody = isSSEContent(rw.Header().Get("Content-Type"))
	}
	if rw.buffer == nil {
		rw.buffer = &bytes.Buffer{}
	}
	if enforcesRequestRedaction(rw.cfg) {
		limit := rw.captureLimit
		if limit <= 0 {
			limit = redaction.DefaultScanLimits().MaxInputBytes
		}
		current := rw.buffer.Len()
		if current > limit || len(b) > limit-current {
			rw.captureOverflow = true
			rw.buffer = nil
			if rw.summary != nil {
				rw.summary.InspectionLimited = true
			}
			return len(b), nil
		}
		needed := current + len(b)
		if needed > rw.buffer.Cap() || rw.buffer.Cap() > limit {
			// bytes.Buffer's ordinary geometric growth can retain capacity
			// beyond the input limit even when its length remains below it.
			capacity := rw.buffer.Cap()
			if capacity > limit/2 {
				capacity = limit
			} else {
				capacity *= 2
			}
			capacity = max(capacity, needed)
			data := make([]byte, current, capacity)
			copy(data, rw.buffer.Bytes())
			rw.buffer = bytes.NewBuffer(data)
		}
	}
	// Buffer the response for redaction
	return rw.buffer.Write(b)
}

// rejectLateSSELocked prevents a mutable header from releasing an earlier
// buffered JSON or unknown body. Callers must hold rw.mu.
func (rw *redactingResponseWriter) rejectLateSSELocked() {
	if !rw.finalized && enforcesRequestRedaction(rw.cfg) && rw.wroteBody &&
		!rw.sseBeforeBody && isSSEContent(rw.Header().Get("Content-Type")) {
		rw.streamingRejected = true
		rw.buffer = nil
	}
}

// finalize processes the buffered response, applies redaction, and writes to the underlying writer.
func (rw *redactingResponseWriter) finalize() {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if rw.finalized || rw.hijacked {
		return
	}
	rw.rejectLateSSELocked()
	rw.finalized = true

	var body []byte
	if rw.buffer != nil {
		body = rw.buffer.Bytes()
		rw.buffer = nil
	}
	if rw.captureOverflow || rw.streamingRejected {
		body = rw.unavailableResponseBody()
		if rw.captureOverflow {
			log.Print("redact: response withheld because capture exceeded the inspection limit")
		} else {
			log.Print("redact: response withheld because streaming was enabled after body output started")
		}
	}
	if len(body) == 0 {
		if rw.wroteHeader {
			rw.ResponseWriter.WriteHeader(rw.statusCode)
		}
		return
	}

	// Check if response is JSON
	contentType := rw.Header().Get("Content-Type")
	if !rw.captureOverflow && !rw.streamingRejected && isJSONContent(contentType) {
		// Protect output in block mode too. Refusing a request belongs before
		// dispatch; at this point the handler may already have changed state.
		output, result, err := redactJSONBytes(body, rw.cfg)
		rw.summary.ResponseFinds = len(result.Findings)
		for _, f := range result.Findings {
			rw.categories[string(f.Category)]++
		}
		body = output
		if err != nil {
			rw.summary.InspectionLimited = errors.Is(err, errRedactionInspectionLimit)
			body = rw.unavailableResponseBody()
			log.Print("redact: JSON response withheld because it could not be safely redacted")
		}
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

// unavailableResponseBody describes only the replacement representation. Keep
// the operation's status and correlation headers: withholding cannot undo work
// that the handler completed or make an unsafe retry safe.
func (rw *redactingResponseWriter) unavailableResponseBody() []byte {
	body := []byte(redactionUnavailableResponse)
	header := rw.Header()
	// Removing only the declaration could turn a former trailer into an
	// ordinary header on the replacement response.
	for _, declaration := range header.Values("Trailer") {
		for _, name := range strings.Split(declaration, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for name := range header {
		if strings.HasPrefix(name, http.TrailerPrefix) {
			delete(header, name)
		}
	}
	for _, name := range []string{
		"Content-Disposition", "Content-Encoding", "Content-Range",
		"ETag", "Last-Modified", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest",
		"Transfer-Encoding", "Trailer",
	} {
		header.Del(name)
	}
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	header.Set("Cache-Control", "no-store")
	return body
}

// redactJSONBytes protects a complete JSON document for an output surface.
// Never use this to rewrite input: a syntactically valid placeholder can still
// corrupt credentials or instructions that a handler stores or executes.
//
// Both enforcing modes redact output. Unlike request admission, an output
// failure cannot undo a handler's work. Callers must use the returned bytes,
// never Result.Output, and explicitly withhold/reset on error. Errors contain
// no source content, field names, or custom-pattern details.
func redactJSONBytes(body []byte, cfg redaction.Config) ([]byte, redaction.Result, error) {
	if cfg.Mode == redaction.ModeBlock {
		cfg.Mode = redaction.ModeRedact
	}
	if cfg.Mode != redaction.ModeRedact {
		return body, redaction.ScanJSONAndRedact(string(body), cfg), nil
	}
	scanner := redaction.NewScanner(cfg, redaction.DefaultScanLimits())
	result := scanner.ScanJSONAndRedact(string(body))
	if result.LimitExceeded {
		return nil, result, errRedactionInspectionLimit
	}
	if !json.Valid(body) {
		return nil, result, errors.New("invalid JSON output")
	}
	protectedPassword := func(f redaction.Finding) bool {
		if f.Category != redaction.CategoryPassword || len(cfg.ExtraPatterns[f.Category]) > 0 {
			return false
		}
		pair := jsonCredentialPair.FindStringSubmatch(f.Match)
		return pair != nil && jsonRedactionMarker.MatchString(f.Match[len(pair[1]):])
	}
	result.Findings = slices.DeleteFunc(result.Findings, protectedPassword)
	if len(result.Findings) == 0 {
		return body, result, nil
	}
	for _, f := range result.Findings {
		if f.Start < 0 || f.End <= f.Start || f.End > len(body) {
			return nil, result, errors.New("invalid JSON redaction boundary")
		}
		// Built-in credential-field findings identify public key context.
		// A custom pattern may classify the key itself as sensitive; retaining
		// that key while replacing only its value would disclose the match.
		if len(cfg.ExtraPatterns[f.Category]) > 0 && jsonCredentialPair.Match(body[f.Start:f.End]) {
			return nil, result, errors.New("ambiguous JSON redaction boundary")
		}
	}
	output := []byte(jsonResponseOutput(string(body), result))
	if !json.Valid(output) {
		return nil, result, errors.New("unsafe JSON redaction boundary")
	}
	// Repaired JSON must not retain detectable secret content. This also
	// protects against a structurally valid but incomplete custom rewrite.
	// Ignore complete markers: PASSWORD detects arbitrary quoted values and
	// would otherwise mistake already protected values for new passwords.
	check := scanner.ScanJSONAndRedact(string(output))
	if check.LimitExceeded {
		return nil, result, errRedactionInspectionLimit
	}
	for _, f := range check.Findings {
		if !protectedPassword(f) {
			return nil, result, errors.New("JSON redaction left sensitive content")
		}
	}
	return output, result, nil
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

// Flush permits streaming only for SSE, whose producers redact complete frames.
// JSON (including a flush before Content-Type is set) remains buffered until
// finalize: a secret or JSON token can straddle arbitrary Write/Flush calls.
func (rw *redactingResponseWriter) Flush() {
	rw.mu.Lock()
	rw.rejectLateSSELocked()
	if rw.captureOverflow || rw.streamingRejected {
		rw.mu.Unlock()
		return
	}
	if !rw.finalized && !isSSEContent(rw.Header().Get("Content-Type")) {
		rw.mu.Unlock()
		return
	}
	if !rw.finalized {
		rw.finalized = true // Switch to passthrough mode

		if rw.wroteHeader {
			rw.ResponseWriter.WriteHeader(rw.statusCode)
		}

		// Flush any already-buffered content first
		var body []byte
		if rw.buffer != nil {
			body = rw.buffer.Bytes()
			rw.buffer = nil
		}
		if len(body) > 0 {
			if _, err := rw.ResponseWriter.Write(body); err != nil {
				log.Printf("redact: failed to flush buffered streaming body: %v", err)
			}
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
	rw.mu.Lock()
	rw.rejectLateSSELocked()
	if rw.captureOverflow || rw.streamingRejected {
		rw.mu.Unlock()
		return nil, nil, errors.New("response could not be safely disclosed before connection upgrade")
	}
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		rw.mu.Unlock()
		return nil, nil, http.ErrNotSupported
	}

	rw.hijacked = true
	rw.finalized = true
	rw.buffer = nil
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

func isSSEContent(contentType string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]), "text/event-stream")
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
