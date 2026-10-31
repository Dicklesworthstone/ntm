package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
)

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
