package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

func TestRedactionMiddleware_RejectsCredentialJSON(t *testing.T) {
	const secret = "syntheticNeverValidToken123456789"
	const providerKey = "sk-proj-FAKEtestkey1234567890123456789012345678901234"
	logs := &lockedBuffer{}
	previousLogWriter := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })

	cases := []struct {
		name, body string
		findings   int
	}{
		{"reported credential field", `{"prompt":"hello","api_token":"` + secret + `"}`, 1},
		{"nested credential", `{"settings":{"service":{"api_token":"` + secret + `"}},"enabled":true}`, 1},
		{"credential in array", `{"items":[{"name":"safe"},{"auth_token":"` + secret + `"}]}`, 1},
		{"multiple credentials", `{"api_token":"` + secret + `","nested":{"client_secret":"` + secret + `"}}`, 2},
		{"escaped field name", `{"api_\u0074oken":"` + secret + `"}`, 1},
		{"escaped password", `{"password":"synthetic-prefix\"synthetic-suffix"}`, 1},
		{"encoded credential object", `{"payload":"{\"api_token\":\"` + secret + `\"}"}`, 1},
		{"provider key in prompt", `{"prompt":"Use this credential: ` + providerKey + `"}`, 1},
		{"provider key used as field name", `{"` + providerKey + `":"ordinary value"}`, 1},
	}
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, tc := range cases {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				if !json.Valid([]byte(tc.body)) {
					t.Fatal("test must submit valid JSON")
				}
				srv := &Server{redactionCfg: &RedactionConfig{
					Enabled: true, Config: redaction.Config{Mode: mode},
				}}
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					w.WriteHeader(http.StatusNoContent)
				})
				req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "Application/JSON; charset=utf-8")
				req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "request-378"))
				rec := httptest.NewRecorder()
				srv.redactionMiddleware(next).ServeHTTP(rec, req)

				if called {
					t.Fatal("credential-bearing JSON reached the handler")
				}
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
				}
				var response APIError
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("rejection is not valid JSON: %v", err)
				}
				if response.Success || response.ErrorCode != "SECRETS_DETECTED" {
					t.Errorf("unexpected rejection envelope: %+v", response)
				}
				if response.RequestID != "request-378" {
					t.Errorf("request ID = %q, want request-378", response.RequestID)
				}
				if response.Details["findings_count"] != float64(tc.findings) {
					t.Errorf("findings_count = %v, want %d", response.Details["findings_count"], tc.findings)
				}
				findings, ok := response.Details["findings"].([]interface{})
				if !ok || len(findings) != tc.findings {
					t.Fatalf("missing safe diagnostic locations: %v", response.Details)
				}
				for _, finding := range findings {
					location, ok := finding.(map[string]interface{})
					if !ok {
						t.Fatalf("finding is not an object: %v", finding)
					}
					start, startOK := location["start"].(float64)
					end, endOK := location["end"].(float64)
					if !startOK || !endOK || start < 0 || end <= start || end > float64(len(tc.body)) {
						t.Errorf("invalid finding byte range: %v", location)
					}
					if category, ok := location["category"].(string); !ok || category == "" {
						t.Errorf("finding has no category: %v", location)
					}
					if len(location) != 3 {
						t.Errorf("diagnostic includes more than category and byte range: %v", location)
					}
				}
				if !strings.Contains(response.Error, "sensitive") {
					t.Errorf("rejection does not explain the sensitive-content policy: %q", response.Error)
				}
				for _, sensitive := range []string{secret, providerKey, "synthetic-prefix", "synthetic-suffix"} {
					if strings.Contains(rec.Body.String(), sensitive) {
						t.Errorf("rejection exposed submitted credential contents")
					}
					if strings.Contains(logs.String(), sensitive) {
						t.Errorf("redaction summary exposed submitted credential contents")
					}
				}
			})
		}
	}
	if !strings.Contains(logs.String(), `"blocked":true`) || !strings.Contains(logs.String(), `"request_findings":`) {
		t.Error("rejections did not retain a useful, non-secret summary")
	}
}

func TestRedactionMiddleware_BoundsRejectionDiagnostics(t *testing.T) {
	const secret = "syntheticNeverValidToken123456789"
	body := "[" + strings.TrimSuffix(strings.Repeat(`{"api_token":"`+secret+`"},`, 101), ",") + "]"
	srv := &Server{redactionCfg: &RedactionConfig{
		Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact},
	}}
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.redactionMiddleware(next).ServeHTTP(rec, req)
	if called || rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("request was not rejected: called=%v status=%d", called, rec.Code)
	}
	var response APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("bounded diagnostic is not valid JSON: %v", err)
	}
	findings, ok := response.Details["findings"].([]interface{})
	if !ok || len(findings) != 100 {
		t.Errorf("diagnostic locations = %d, want 100", len(findings))
	}
	if response.Details["findings_count"] != float64(101) || response.Details["findings_truncated"] != true {
		t.Error("truncated diagnostic did not retain the complete findings count and truncation flag")
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("bounded diagnostic leaked submitted credential contents")
	}
}

func TestRedactionMiddleware_PreservesPermittedJSONBytes(t *testing.T) {
	const ordinary = " \n{ \"prompt\" : \"hello\", \"nested\" : { \"items\" : [true, null, 9007199254740993] }, \"label\":\"first\", \"label\":\"last\" }\t\n"
	const secret = "syntheticNeverValidToken123456789"
	const credential = `{"prompt":"hello","api_token":"` + secret + `"}`

	cases := []struct {
		name string
		body string
		cfg  *RedactionConfig
	}{
		{"ordinary redact", ordinary, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}},
		{"ordinary block", ordinary, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeBlock}}},
		{"ordinary warn", ordinary, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeWarn}}},
		{"ordinary off", ordinary, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeOff}}},
		{"empty redact", "", &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}},
		{"credential warn", credential, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeWarn}}},
		{"credential off", credential, &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeOff}}},
		{"credential disabled", credential, &RedactionConfig{Enabled: false, Config: redaction.Config{Mode: redaction.ModeRedact}}},
		{"credential unconfigured", credential, nil},
		{"credential allowlisted redact", credential, &RedactionConfig{Enabled: true, Config: redaction.Config{
			Mode: redaction.ModeRedact, Allowlist: []string{`^` + regexp.QuoteMeta(`"api_token":"`+secret+`"`) + `$`},
		}}},
		{"credential allowlisted block", credential, &RedactionConfig{Enabled: true, Config: redaction.Config{
			Mode: redaction.ModeBlock, Allowlist: []string{`^` + regexp.QuoteMeta(`"api_token":"`+secret+`"`) + `$`},
		}}},
		{"credential category disabled", credential, &RedactionConfig{Enabled: true, Config: redaction.Config{
			Mode: redaction.ModeRedact, DisabledCategories: []redaction.Category{redaction.CategoryGenericSecret},
		}}},
	}
	for _, tc := range cases {
		for _, chunked := range []bool{false, true} {
			name := tc.name + "/known-length"
			if chunked {
				name = tc.name + "/chunked"
			}
			t.Run(name, func(t *testing.T) {
				srv := &Server{redactionCfg: tc.cfg}
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatalf("read forwarded body: %v", err)
					}
					if string(body) != tc.body {
						t.Error("permitted JSON changed before reaching its handler")
					}
					if len(body) > 0 && !json.Valid(body) {
						t.Error("handler received malformed JSON")
					}
					if r.ContentLength >= 0 && r.ContentLength != int64(len(body)) {
						t.Errorf("ContentLength = %d, body length = %d", r.ContentLength, len(body))
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"status":"accepted"}`)
				})
				req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				if chunked {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				rec := httptest.NewRecorder()
				srv.redactionMiddleware(next).ServeHTTP(rec, req)
				if !called || rec.Code != http.StatusOK {
					t.Fatalf("permitted request was not accepted: called=%v status=%d body=%s", called, rec.Code, rec.Body)
				}
			})
		}
	}
}

func TestRedactionMiddleware_CustomDetectionNeverEchoesSensitiveFieldNames(t *testing.T) {
	const sensitiveKey = "synthetic-private-field-name-378"
	body := `{"` + sensitiveKey + `":"ordinary value"}`
	logs := &lockedBuffer{}
	previousLogWriter := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })
	srv := &Server{redactionCfg: &RedactionConfig{Enabled: true, Config: redaction.Config{
		Mode: redaction.ModeRedact,
		ExtraPatterns: map[redaction.Category][]string{
			"CUSTOM_SECRET": {regexp.QuoteMeta(sensitiveKey)},
		},
	}}}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// A supplied Body with unknown length may use zero in an in-process request.
	// The middleware must inspect the actual bytes, not assume an absent body.
	req.ContentLength = 0
	rec := httptest.NewRecorder()
	srv.redactionMiddleware(next).ServeHTTP(rec, req)
	if called || rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("custom secret was dispatched: called=%v status=%d", called, rec.Code)
	}
	var response APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("rejection is not valid JSON: %v", err)
	}
	if response.ErrorCode != "SECRETS_DETECTED" || response.Details["findings_count"] != float64(1) {
		t.Errorf("unexpected custom detection diagnostic: %+v", response)
	}
	if strings.Contains(rec.Body.String(), sensitiveKey) || strings.Contains(logs.String(), sensitiveKey) {
		t.Error("diagnostic exposed a sensitive field name")
	}
}

func TestRedactionMiddleware_RequestScopeUsesAPIContract(t *testing.T) {
	const credential = `{"api_token":"syntheticNeverValidToken123456789"}`
	const ordinary = ` { "prompt" : "hello", "nested" : [true, 9007199254740993] } `
	for _, tc := range []struct {
		name, path, contentType, body string
		blocked                       bool
	}{
		{"API root", "/api/v1", "text/plain", credential, true},
		{"API missing type", "/api/v1/test", "", credential, true},
		{"API text type", "/api/v1/test", "text/plain", credential, true},
		{"API vendor JSON type", "/api/v1/test", "application/vnd.ntm+json", credential, true},
		{"ordinary API missing type", "/api/v1/test", "", ordinary, false},
		{"ordinary API text type", "/api/v1/test", "text/plain", ordinary, false},
		{"ordinary API vendor JSON type", "/api/v1/test", "application/vnd.ntm+json", ordinary, false},
		{"outside API text", "/hook", "text/plain", "plain, non-JSON input", false},
		{"different API version text", "/api/v10/test", "text/plain", credential, false},
		{"outside API explicit JSON", "/hook", "application/json", credential, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{redactionCfg: &RedactionConfig{
				Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact},
			}}
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.body {
					t.Errorf("permitted body changed: length=%d error=%v", len(body), err)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			srv.redactionMiddleware(next).ServeHTTP(rec, req)
			wantStatus := http.StatusNoContent
			if tc.blocked {
				wantStatus = http.StatusUnprocessableEntity
			}
			if called == tc.blocked || rec.Code != wantStatus {
				t.Errorf("called=%v status=%d, want blocked=%v status=%d", called, rec.Code, tc.blocked, wantStatus)
			}
		})
	}
}

func TestRedactionMiddleware_RejectsMalformedJSONWithoutLeakingParserInput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"unfinished object", `{"prompt":"hello"`, http.StatusBadRequest, ErrCodeBadRequest},
		{"trailing object", `{"prompt":"hello"}{"prompt":"ignored"}`, http.StatusBadRequest, ErrCodeBadRequest},
		{"trailing text", `{"prompt":"hello"}sensitive-unmatched-value`, http.StatusBadRequest, ErrCodeBadRequest},
		{"whitespace only", " \n\t", http.StatusBadRequest, ErrCodeBadRequest},
		{"credential in malformed JSON", `{"api_token":"syntheticNeverValidToken123456789",`, http.StatusUnprocessableEntity, "SECRETS_DETECTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{redactionCfg: &RedactionConfig{
				Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact},
			}}
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.redactionMiddleware(next).ServeHTTP(rec, req)
			if called {
				t.Fatal("malformed JSON reached the handler")
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			var response APIError
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("rejection is not valid JSON: %v", err)
			}
			if response.ErrorCode != tc.code || response.Success {
				t.Errorf("unexpected rejection envelope: %+v", response)
			}
			if strings.Contains(rec.Body.String(), "syntheticNeverValidToken") || strings.Contains(rec.Body.String(), "sensitive-unmatched-value") {
				t.Error("rejection quoted sensitive request contents")
			}
		})
	}
}

func TestRedactionMiddleware_JSONBodyLimitNeverForwardsAPrefix(t *testing.T) {
	const limit = 10 << 20
	// The first limit bytes are themselves valid JSON. Silently truncating at
	// the limit would let a handler execute a request that the caller never sent.
	boundary := `{}` + strings.Repeat(" ", limit-2)
	for _, tc := range []struct {
		name, body string
		status     int
		outerLimit bool
	}{
		{"exact limit", boundary, http.StatusNoContent, false},
		{"one byte over", boundary + " ", http.StatusRequestEntityTooLarge, false},
		{"credential after limit", boundary + `{"api_token":"syntheticNeverValidToken123456789"}`, http.StatusRequestEntityTooLarge, false},
		{"router body limit", boundary + " ", http.StatusRequestEntityTooLarge, true},
	} {
		for _, chunked := range []bool{false, true} {
			name := tc.name + "/known-length"
			if chunked {
				name = tc.name + "/chunked"
			}
			t.Run(name, func(t *testing.T) {
				srv := &Server{redactionCfg: &RedactionConfig{
					Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact},
				}}
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != tc.body {
						t.Errorf("handler received a partial or unreadable body: length=%d error=%v", len(body), err)
					}
					w.WriteHeader(http.StatusNoContent)
				})
				req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				if chunked {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				rec := httptest.NewRecorder()
				if tc.outerLimit {
					req.Body = http.MaxBytesReader(rec, req.Body, limit)
				}
				srv.redactionMiddleware(next).ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
				}
				if called != (tc.status == http.StatusNoContent) {
					t.Errorf("handler called = %v for status %d", called, tc.status)
				}
				if tc.status == http.StatusRequestEntityTooLarge {
					var response APIError
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatalf("rejection is not valid JSON: %v", err)
					}
					if response.ErrorCode != "REQUEST_TOO_LARGE" {
						t.Errorf("error code = %q, want REQUEST_TOO_LARGE", response.ErrorCode)
					}
				}
			})
		}
	}
}

type redactionTestBody struct {
	io.Reader
	closed bool
}

func (b *redactionTestBody) Close() error {
	b.closed = true
	return nil
}

func TestRedactionMiddleware_ReadFailureDoesNotDispatchOrExposeErrorContents(t *testing.T) {
	const sensitive = "syntheticNeverValidToken123456789"
	logs := &lockedBuffer{}
	previousLogWriter := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })

	srv := &Server{redactionCfg: &RedactionConfig{
		Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact},
	}}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	body := &redactionTestBody{Reader: io.MultiReader(
		strings.NewReader(`{"prompt":"hello"}`),
		iotest.ErrReader(errors.New("transport failure containing credential "+sensitive)),
	)}
	req.Body = body
	rec := httptest.NewRecorder()
	srv.redactionMiddleware(next).ServeHTTP(rec, req)
	if called {
		t.Fatal("partial read reached the handler")
	}
	if !body.closed {
		t.Error("request body was not closed after its read failed")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	var response APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("rejection is not valid JSON: %v", err)
	}
	if response.ErrorCode != ErrCodeBadRequest || response.Error != "failed to read request body" {
		t.Errorf("unexpected read failure diagnostic: %+v", response)
	}
	if strings.Contains(rec.Body.String(), sensitive) || strings.Contains(logs.String(), sensitive) {
		t.Error("read failure exposed the underlying error's credential contents")
	}
}

func serveRedactedJSON(t *testing.T, path, body string) string {
	t.Helper()
	srv, _ := setupTestServer(t)
	srv.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	rec := httptest.NewRecorder()
	srv.redactionMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestRedactedJSONResponseStaysJSON(t *testing.T) {
	const secret = "abcDEF1234567890abcDEF1234567890"
	for _, tc := range []struct {
		name, body string
		leak       string
	}{
		{"credential field", `{"status":"sending","api_token":"` + secret + `","endpoint":"e"}`, secret},
		{"spaced field", `{ "client_secret" : "` + secret + `" }`, secret},
		{"fields in an array", `{"items":[{"auth_token":"` + secret + `"},{"auth_token":"` + secret + `"}]}`, secret},
		{"escaped quote in the value", `{"password":"secret-prefix\"secret-suffix","status":"ready"}`, "secret-"},
		{"assignment closing a string", `{"note":"token=` + secret + `","next":1}`, secret},
		{"provider key in text", `{"note":"key sk-proj-FAKEtestkey1234567890123456789012345678901234 here"}`, "sk-proj-FAKE"},
		{"field inside an encoded string", `{"payload":"{\"api_token\":\"` + secret + `\"}"}`, secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := serveRedactedJSON(t, "/api/v1/sessions", tc.body)
			var parsed any
			if err := json.Unmarshal([]byte(out), &parsed); err != nil {
				t.Fatalf("redacted body is not JSON: %v\n%s", err, out)
			}
			if strings.Contains(out, tc.leak) {
				t.Fatalf("secret survived redaction: %s", out)
			}
			if !strings.Contains(out, "[REDACTED:") {
				t.Fatalf("nothing was redacted: %s", out)
			}
		})
	}
}

func TestRedactedJSONFieldKeepsItsKeyAndNeighbours(t *testing.T) {
	out := serveRedactedJSON(t, "/api/v1/sessions", `{"status":"sending","api_token":"abcDEF1234567890abcDEF1234567890","endpoint":"e"}`)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("redacted body is not JSON: %v\n%s", err, out)
	}
	if parsed["status"] != "sending" || parsed["endpoint"] != "e" || !strings.HasPrefix(parsed["api_token"], "[REDACTED:") {
		t.Fatalf("fields = %v", parsed)
	}
}

func serveJSONResponseWithConfig(t *testing.T, path, body string, status int, cfg redaction.Config) *httptest.ResponseRecorder {
	t.Helper()
	srv := &Server{redactionCfg: &RedactionConfig{Enabled: true, Config: cfg}}
	handlerCalls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write handler response: %v", err)
		}
	})
	rec := httptest.NewRecorder()
	srv.redactionMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if handlerCalls != 1 {
		t.Fatalf("response test handler called %d times, want 1", handlerCalls)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length = %q, response length = %d", got, rec.Body.Len())
	}
	return rec
}

func TestJSONResponseRedactionProtectsBlockModeAndDiagnostics(t *testing.T) {
	const secret = "syntheticNeverValidToken123456789"
	logs := &lockedBuffer{}
	previousLogWriter := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			body := `{"success":false,"error_code":"UPSTREAM_AUTH_FAILED","error":"upstream authentication failed","details":{"api_token":"` + secret + `","retryable":false},"retry_after":15}`
			rec := serveJSONResponseWithConfig(t, "/api/v1/test", body, http.StatusBadGateway, redaction.Config{Mode: mode})
			if rec.Code != http.StatusBadGateway {
				t.Errorf("handler status changed: %d, want 502", rec.Code)
			}
			var response struct {
				Success    bool           `json:"success"`
				ErrorCode  string         `json:"error_code"`
				Error      string         `json:"error"`
				Details    map[string]any `json:"details"`
				RetryAfter int            `json:"retry_after"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("redacted response is not valid JSON: %v", err)
			}
			if response.Success || response.ErrorCode != "UPSTREAM_AUTH_FAILED" || response.Error != "upstream authentication failed" || response.RetryAfter != 15 || response.Details["retryable"] != false {
				t.Errorf("meaningful non-secret diagnostics changed: %+v", response)
			}
			if token, ok := response.Details["api_token"].(string); !ok || !strings.HasPrefix(token, "[REDACTED:") {
				t.Error("credential field was not explicitly redacted")
			}
			if strings.Contains(rec.Body.String(), secret) || strings.Contains(logs.String(), secret) {
				t.Error("response redaction disclosed credential contents")
			}
		})
	}
}

func TestJSONResponseRedactionPreservesUnchangedBytes(t *testing.T) {
	const body = " \n{ \"error\":\"service unavailable\", \"count\":9007199254740993, \"label\":\"first\", \"label\":\"last\", \"items\":[true,null] }\t\n"
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock, redaction.ModeWarn, redaction.ModeOff} {
		t.Run(string(mode), func(t *testing.T) {
			rec := serveJSONResponseWithConfig(t, "/api/v1/test", body, http.StatusServiceUnavailable, redaction.Config{Mode: mode})
			if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != body {
				t.Errorf("response without findings changed: status=%d body=%s", rec.Code, rec.Body)
			}
		})
	}
}

func TestJSONResponseRedactionHandlesCustomMatchBoundaries(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, tc := range []struct {
			name, body, match, leak string
			originalValue           any
			withheld                bool
		}{
			{"number", `{"private":123456789,"error":"service unavailable"}`, "123456789", "123456789", float64(123456789), true},
			{"boolean", `{"private":true,"error":"service unavailable"}`, "true", "", true, true},
			{"null", `{"private":null,"error":"service unavailable"}`, "null", "", nil, true},
			{"quoted scalar", `{"private":"sensitive-data","error":"service unavailable"}`, `"sensitive-data"`, "sensitive-data", "sensitive-data", true},
			{"whole nested object", `{"private":{"inner":"sensitive-data"},"error":"service unavailable"}`, `{"inner":"sensitive-data"}`, "sensitive-data", map[string]any{"inner": "sensitive-data"}, true},
			{"whole property", `{"private":{"inner":"sensitive-data"},"error":"service unavailable"}`, `"private":{"inner":"sensitive-data"}`, "sensitive-data", map[string]any{"inner": "sensitive-data"}, true},
			{"sensitive property name in pair", `{"sensitive-field-name":"ordinary","error":"service unavailable"}`, `"sensitive-field-name":"ordinary"`, "sensitive-field-name", nil, true},
			{"escaped string boundary", `{"private":"before\"sensitive-data\"after","error":"service unavailable"}`, `\"sensitive-data\"`, "sensitive-data", `before"sensitive-data"after`, false},
		} {
			t.Run(string(mode)+"/"+tc.name, func(t *testing.T) {
				if !json.Valid([]byte(tc.body)) {
					t.Fatal("fixture must start as valid JSON")
				}
				cfg := redaction.Config{Mode: mode, ExtraPatterns: map[redaction.Category][]string{
					"CUSTOM_SENSITIVE": {regexp.QuoteMeta(tc.match)},
				}}
				rec := serveJSONResponseWithConfig(t, "/api/v1/test", tc.body, http.StatusServiceUnavailable, cfg)
				var parsed map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
					t.Fatalf("custom match produced invalid JSON: %v", err)
				}
				if rec.Code == http.StatusUnprocessableEntity || parsed["error_code"] == "SECRETS_DETECTED" {
					t.Error("response filtering incorrectly claimed the already-handled request was rejected")
				}
				if tc.leak != "" && strings.Contains(rec.Body.String(), tc.leak) {
					t.Error("custom response match disclosed sensitive content")
				}
				if value, exists := parsed["private"]; exists && reflect.DeepEqual(value, tc.originalValue) {
					t.Error("custom response match left the sensitive value unchanged")
				}
				if rec.Code != http.StatusServiceUnavailable {
					t.Errorf("response filtering changed handler status: %d", rec.Code)
				}
				if tc.withheld {
					assertJSONResponseWithheld(t, rec.Body.Bytes())
				} else if parsed["error"] != "service unavailable" {
					t.Error("safe response diagnostic was lost during string redaction")
				}
			})
		}
	}
}

func assertJSONResponseWithheld(t *testing.T, body []byte) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("withheld response is not valid JSON: %v", err)
	}
	want := map[string]any{
		"response_unavailable": true,
		"error_code":           "RESPONSE_REDACTION_FAILED",
		"error":                "Response content could not be safely redacted",
		"hint":                 "The handler has already run and may have completed the operation. Check its state before retrying.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected withholding envelope: %v", got)
	}
}

func TestJSONResponseRedactionFallbackPreservesCompletedHandlerStatus(t *testing.T) {
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := redaction.Config{Mode: mode, ExtraPatterns: map[redaction.Category][]string{
				"PRIVATE_ID": {"123456789"},
			}}
			rec := serveJSONResponseWithConfig(t, "/api/v1/test", `{"success":true,"id":123456789}`, http.StatusCreated, cfg)
			if rec.Code != http.StatusCreated {
				t.Errorf("completed operation status changed: %d, want 201", rec.Code)
			}
			assertJSONResponseWithheld(t, rec.Body.Bytes())
		})
	}
}

func TestJSONResponseRedactionFlushCannotBypassProtection(t *testing.T) {
	const secret = "syntheticNeverValidToken123456789"
	body := `{"error_code":"UPSTREAM_FAILURE","error":"try later","api_token":"` + secret + `"}`
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, flushAt := range []string{"complete body", "before content type", "after headers", "inside credential"} {
			t.Run(string(mode)+"/"+flushAt, func(t *testing.T) {
				srv := &Server{redactionCfg: &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: mode}}}
				rec := httptest.NewRecorder()
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					flusher := w.(http.Flusher)
					if flushAt == "before content type" {
						flusher.Flush()
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
					w.WriteHeader(http.StatusBadGateway)
					if flushAt == "after headers" {
						flusher.Flush()
					}
					if flushAt == "inside credential" {
						split := strings.Index(body, secret) + len(secret)/2
						if _, err := io.WriteString(w, body[:split]); err != nil {
							t.Fatal(err)
						}
						flusher.Flush()
						if rec.Body.Len() != 0 {
							t.Error("partial credential escaped before response redaction could run")
						}
						if _, err := io.WriteString(w, body[split:]); err != nil {
							t.Fatal(err)
						}
					} else if _, err := io.WriteString(w, body); err != nil {
						t.Fatal(err)
					}
					flusher.Flush()
					if strings.Contains(rec.Body.String(), secret) {
						t.Error("Flush exposed the credential before handler completion")
					}
				})
				srv.redactionMiddleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/test", nil))
				if rec.Code != http.StatusBadGateway {
					t.Errorf("Flush changed handler status: %d, want 502", rec.Code)
				}
				var parsed map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
					t.Fatalf("flushed response is not valid JSON: %v", err)
				}
				if parsed["error_code"] != "UPSTREAM_FAILURE" || parsed["error"] != "try later" || !strings.HasPrefix(parsed["api_token"], "[REDACTED:") || strings.Contains(rec.Body.String(), secret) {
					t.Error("Flush bypassed redaction or destroyed safe diagnostics")
				}
				if rec.Result().Header.Get("Content-Type") != "application/json" || rec.Result().ContentLength != int64(rec.Body.Len()) {
					t.Errorf("Flush committed stale headers: %v", rec.Result().Header)
				}
			})
		}
	}
}

func TestJSONResponseRedactionKeepsOnlyRecoveryReceiptExemption(t *testing.T) {
	const secret = "syntheticNeverValidToken123456789"
	token := strings.Repeat("ab", 32)
	body := `{"delivery":{"receipt_token":"` + token + `","api_token":"` + secret + `"},"status":"pending"}`
	for _, mode := range []redaction.Mode{redaction.ModeRedact, redaction.ModeBlock} {
		for _, path := range []string{"/api/v1/pipelines/run-1/recover", "/api/v1/sessions"} {
			t.Run(string(mode)+path, func(t *testing.T) {
				rec := serveJSONResponseWithConfig(t, path, body, http.StatusOK, redaction.Config{Mode: mode})
				var response struct {
					Delivery map[string]string `json:"delivery"`
					Status   string            `json:"status"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("response is not valid JSON: %v", err)
				}
				if response.Status != "pending" || !strings.HasPrefix(response.Delivery["api_token"], "[REDACTED:") || strings.Contains(rec.Body.String(), secret) {
					t.Error("unrelated credential or non-secret status escaped response policy")
				}
				if path == "/api/v1/pipelines/run-1/recover" {
					if response.Delivery["receipt_token"] != token {
						t.Error("recovery lost its exact receipt token")
					}
				} else if strings.Contains(rec.Body.String(), token) {
					t.Error("recovery receipt exemption escaped its route")
				}
			})
		}
	}
}

func TestReceiptTokenExemptionIsLimitedToPipelineRecovery(t *testing.T) {
	token := strings.Repeat("ab", 32)
	body := `{"delivery":{"receipt_token":"` + token + `"}}`

	if out := serveRedactedJSON(t, "/api/v1/pipelines/run-1/recover", body); out != body {
		t.Fatalf("recovery response changed: %s", out)
	}
	for _, path := range []string{
		"/api/v1/pipelines/run-1",
		"/api/v1/pipelines/run-1/resume",
		"/api/v1/pipelines/run-1/recover/extra",
		"/api/v1/pipelines/a/b/recover",
		"/api/v1/jobs/recover",
		"/api/v1/sessions",
	} {
		out := serveRedactedJSON(t, path, body)
		if strings.Contains(out, token) || !json.Valid([]byte(out)) {
			t.Errorf("%s: receipt_token not redacted as JSON: %s", path, out)
		}
	}

	// On the recovery route the exemption covers that key with that shape only.
	for _, other := range []string{
		`{"receipt_token":"` + strings.Repeat("zy", 32) + `"}`,
		`{"receipt_token":"` + token + `0"}`,
		`{"api_token":"` + token + `"}`,
	} {
		out := serveRedactedJSON(t, "/api/v1/pipelines/run-1/recover", other)
		if out == other || !json.Valid([]byte(out)) {
			t.Errorf("recovery route let through %s: %s", other, out)
		}
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The exact token leaves through the recovery response and nowhere else, and an
// Idempotency-Key replay, which never reaches the handler, returns it too.
func TestPipelineRecoveryTokenStaysOutOfServerLogs(t *testing.T) {
	logs := &lockedBuffer{}
	log.SetOutput(logs)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		slog.SetDefault(previous)
	})

	srv, _ := setupTestServer(t)
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "pipeline-recovery-test-key", Role: RoleOperator}
	srv.redactionCfg = &RedactionConfig{Enabled: true, Config: redaction.Config{Mode: redaction.ModeRedact}}
	root := srv.projectDir
	prior := pipelineRecoveryHTTPCheckpoint(t, root, "prompt")
	pipelineRecoveryHTTPTmux(t, root, prior.AgentDeliveries["agent"].BeforeOutput+"\nFRESH-HTTP-ANSWER\n")
	path := "/api/v1/pipelines/" + prior.RunID + "/recover"
	canonical, err := pipeline.InspectAgentDeliveryRecovery(context.Background(), root, prior.RunID)
	if err != nil {
		t.Fatal(err)
	}
	token := canonical.Deliveries[0].ReceiptToken

	rec, _ := pipelineRecoveryHTTPServe(t, srv, pipelineRecoveryHTTPRequest(http.MethodGet, path, ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), token) {
		t.Fatalf("inspection lost the exact token: %d %s", rec.Code, rec.Body)
	}
	body := pipelineRecoveryHTTPBody(t, token, "Observed original response")
	for attempt := 0; attempt < 2; attempt++ {
		req := pipelineRecoveryHTTPRequest(http.MethodPost, path, body)
		req.Header.Set("Idempotency-Key", "recovery-token-log-test")
		rec, _ := pipelineRecoveryHTTPServe(t, srv, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), token) {
			t.Fatalf("attempt %d lost the exact token: %d %s", attempt, rec.Code, rec.Body)
		}
	}
	if strings.Contains(logs.String(), token) {
		t.Fatalf("receipt token reached the server logs:\n%s", logs.String())
	}
}
