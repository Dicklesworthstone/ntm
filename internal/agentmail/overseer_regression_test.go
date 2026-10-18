package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendOverseerMessage_HTTPCompatibilityMatrix(t *testing.T) {
	t.Parallel()
	duplicates := make([]string, 101)
	for i := range duplicates {
		duplicates[i] = "BlueLake"
	}
	for _, legacy := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			recipients []string
			want       []string
			reason     string
			thread     string
		}{
			{"single", []string{"BlueLake"}, []string{"BlueLake"}, "", ""},
			{"multiple", []string{"BlueLake", "GreenCastle"}, []string{"BlueLake", "GreenCastle"}, "Deployment investigation", "thread-1"},
			{"duplicates", []string{" BlueLake ", "bluelake", "BlueLake"}, []string{"BlueLake"}, "Check progress", "thread-1"},
			{"duplicates above raw limit", duplicates, []string{"BlueLake"}, "Check progress", ""},
		} {
			t.Run(fmt.Sprintf("legacy=%t/%s", legacy, tc.name), func(t *testing.T) {
				t.Parallel()
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != "/mail/ntm/overseer/send" || r.Method != http.MethodPost {
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if r.Header.Get("Authorization") != "Bearer test-overseer-token" || r.Header.Get("Content-Type") != "application/json" {
						t.Error("missing bearer authorization or JSON content type")
					}
					// Python v0.3.2 ignores extra audit keys; Rust v0.3.39
					// requires intent, reason, and multiple-recipient confirmation.
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					var recipients []string
					if err := json.Unmarshal(body["recipients"], &recipients); err != nil || !slices.Equal(recipients, tc.want) {
						t.Errorf("recipients = %v, want %v (decode: %v)", recipients, tc.want, err)
					}
					wantReason := tc.reason
					if wantReason == "" {
						wantReason = DefaultOverseerReason
					}
					for field, want := range map[string]string{
						"intent": "human_overseer_send", "reason": wantReason,
						"subject": "Urgent task", "body_md": "Please do this",
					} {
						var got string
						if err := json.Unmarshal(body[field], &got); err != nil || got != want {
							t.Errorf("%s = %q, want %q (decode: %v)", field, got, want, err)
						}
					}
					var confirmed bool
					if err := json.Unmarshal(body["confirm_multiple"], &confirmed); err != nil || confirmed != (len(tc.want) > 1) {
						t.Errorf("confirm_multiple = %t, recipients = %v (decode: %v)", confirmed, recipients, err)
					}
					if !legacy && (len(body["intent"]) == 0 || len(body["reason"]) == 0 || (len(recipients) > 1 && !confirmed)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					for _, field := range []string{"broadcast", "sender_name", "importance", "ack_required"} {
						if _, exists := body[field]; exists {
							t.Errorf("client must leave %s to the intervention route", field)
						}
					}
					if tc.thread == "" {
						if _, exists := body["thread_id"]; exists {
							t.Error("empty thread_id should be omitted")
						}
					} else if string(body["thread_id"]) != `"thread-1"` {
						t.Errorf("thread_id = %s", body["thread_id"])
					}
					receipt := map[string]interface{}{
						"success": true, "message_id": 300, "recipients": recipients,
						"sent_at": "2026-10-17T12:00:00Z",
					}
					if !legacy {
						receipt["audit"] = map[string]interface{}{"actor": HumanOverseerAgentName, "human_overseer": true, "reason": wantReason}
					}
					_ = json.NewEncoder(w).Encode(receipt)
				}))
				defer server.Close()
				c := NewClient(WithBaseURL(server.URL+"/mcp/"), WithToken("test-overseer-token"))
				result, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
					ProjectKey: "/project/ntm", ProjectSlug: "ntm", Recipients: tc.recipients,
					Subject: "Urgent task", BodyMD: "Please do this", ThreadID: tc.thread, Reason: tc.reason,
				})
				if err != nil {
					t.Fatalf("send: %v", err)
				}
				if !result.Success || result.MessageID != 300 || result.SentAt.IsZero() || !slices.Equal(result.Recipients, tc.want) {
					t.Errorf("unexpected delivery receipt: %+v", result)
				}
				if calls.Load() != 1 {
					t.Errorf("wanted a single intervention, got %d requests", calls.Load())
				}
				if tc.name == "duplicates" && tc.recipients[0] != " BlueLake " {
					t.Error("normalization mutated caller-owned recipients")
				}
			})
		}
	}
}

func TestSendOverseerMessage_UnicodeLimits(t *testing.T) {
	t.Parallel()
	subject, body, reason := strings.Repeat("界", 200), strings.Repeat("界", 50_000), strings.Repeat("界", 500)
	recipients := make([]string, 100)
	for i := range recipients {
		recipients[i] = fmt.Sprintf("Agent%d", i)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Subject         string   `json:"subject"`
			BodyMD          string   `json:"body_md"`
			Reason          string   `json:"reason"`
			Recipients      []string `json:"recipients"`
			ConfirmMultiple bool     `json:"confirm_multiple"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Subject != subject || payload.BodyMD != body || payload.Reason != reason || !slices.Equal(payload.Recipients, recipients) || !payload.ConfirmMultiple {
			t.Error("Unicode/count boundary changed the intervention request")
		}
		_ = json.NewEncoder(w).Encode(OverseerSendResult{Success: true, MessageID: 1, Recipients: recipients})
	}))
	defer server.Close()
	c := NewClient(WithBaseURL(server.URL))
	_, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
		ProjectSlug: "ntm", Recipients: recipients, Subject: subject, BodyMD: body, Reason: reason,
	})
	if err != nil {
		t.Fatalf("valid Unicode character boundaries rejected: %v", err)
	}
}

func TestSendOverseerMessage_ServerErrorsDoNotRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   []string
		kind   error
	}{
		{"current validation", 400, `{"error":"Unknown recipient(s): MissingBird"}`, []string{"Unknown recipient(s): MissingBird"}, ErrInvalidRequest},
		{"legacy validation", 422, `{"detail":"Message too long (including preamble)"}`, []string{"including preamble"}, ErrInvalidRequest},
		{"authentication", 401, `{"detail":"Bearer token expired"}`, []string{"Bearer token expired"}, ErrUnauthorized},
		{"authorization", 403, `{"error":"forbidden","detail":"cross-origin request rejected"}`, []string{"forbidden", "cross-origin request rejected"}, ErrUnauthorized},
		{"missing route", 404, `{"detail":"Not Found"}`, []string{"Not Found"}, ErrNotFound},
		{"method unsupported", 405, `{"error":"Method not allowed"}`, []string{"Method not allowed"}, nil},
		{"busy", 429, "Try again later", []string{"Try again later"}, nil},
		{"server failure", 500, `{"error":"Audit store unavailable"}`, []string{"Audit store unavailable"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := NewClient(WithBaseURL(server.URL))
			// An existing identity must not turn a rejection into a downgrade.
			c.SetRegistrationToken("/project/ntm", HumanOverseerAgentName, "existing-token")
			_, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
				ProjectKey: "/project/ntm", ProjectSlug: "ntm", Recipients: []string{"BlueLake"}, Subject: "Task", BodyMD: "Do this",
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("lost status %d: %v", tc.status, err)
			}
			if tc.kind != nil && !errors.Is(err, tc.kind) {
				t.Errorf("lost classification %v: %v", tc.kind, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
			if calls.Load() != 1 {
				t.Errorf("error triggered retry/fallback: %d calls", calls.Load())
			}
		})
	}
}

func TestSendOverseerMessage_RejectsUnconfirmedReceipts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"malformed", "{", "invalid delivery receipt"},
		{"empty", "{}", "did not confirm"},
		{"null", "null", "did not confirm"},
		{"rejected", `{"success":false,"error":"Intervention refused"}`, "Intervention refused"},
		{"missing message ID", `{"success":true,"recipients":["BlueLake","GreenCastle"]}`, "did not confirm"},
		{"negative message ID", `{"success":true,"message_id":-1,"recipients":["BlueLake","GreenCastle"]}`, "did not confirm"},
		{"missing recipients", `{"success":true,"message_id":77}`, "message 77 delivery mismatch"},
		{"legacy partial", `{"success":true,"message_id":77,"recipients":["BlueLake"]}`, "message 77 delivery mismatch"},
		{"wrong recipients", `{"success":true,"message_id":77,"recipients":["BlueLake","RedStone"]}`, "message 77 delivery mismatch"},
		{"duplicate receipt", `{"success":true,"message_id":77,"recipients":["BlueLake","GreenCastle","BlueLake"]}`, "message 77 delivery mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c := NewClient(WithBaseURL(server.URL))
			c.SetRegistrationToken("/project/ntm", HumanOverseerAgentName, "existing-token")
			result, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
				ProjectKey: "/project/ntm", ProjectSlug: "ntm", Recipients: []string{"BlueLake", "GreenCastle"}, Subject: "Task", BodyMD: "Do this",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "not retried") {
				t.Fatalf("expected %q without retry, got %v", tc.want, err)
			}
			if tc.name == "legacy partial" && (result == nil || result.MessageID != 77 || !slices.Equal(result.Recipients, []string{"BlueLake"})) {
				t.Errorf("lost actual partial-delivery receipt: %+v", result)
			}
			if calls.Load() != 1 {
				t.Errorf("unconfirmed send was retried: %d calls", calls.Load())
			}
		})
	}
}

func TestSendOverseerMessage_RejectsRedirects(t *testing.T) {
	t.Parallel()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/different-surface" {
					t.Error("overseer request followed redirect")
					return
				}
				http.Redirect(w, r, "/different-surface", status)
			}))
			defer server.Close()
			c := NewClient(WithBaseURL(server.URL))
			_, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
				ProjectSlug: "ntm", Recipients: []string{"BlueLake"}, Subject: "Task", BodyMD: "Do this",
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status || calls.Load() != 1 {
				t.Errorf("redirect result: err=%v, requests=%d", err, calls.Load())
			}
		})
	}
}

func TestSendOverseerMessage_TransportErrorsKeepCauseWithoutRetry(t *testing.T) {
	t.Parallel()
	for _, duringBody := range []bool{false, true} {
		for _, cancelRequest := range []bool{false, true} {
			t.Run(fmt.Sprintf("body=%t/canceled=%t", duringBody, cancelRequest), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				cause := errors.New("connection lost after request write")
				var calls atomic.Int32
				c := NewClient()
				c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					err := cause
					if cancelRequest {
						cancel()
						err = context.Canceled
					}
					if duringBody {
						return &http.Response{StatusCode: http.StatusOK, Body: readErrorCloser{err: err}, Header: make(http.Header)}, nil
					}
					return nil, err
				})
				_, err := c.SendOverseerMessage(ctx, OverseerMessageOptions{
					ProjectSlug: "ntm", Recipients: []string{"BlueLake"}, Subject: "Task", BodyMD: "Do this",
				})
				if cancelRequest {
					if !errors.Is(err, context.Canceled) || IsTimeout(err) {
						t.Errorf("cancellation was misclassified: %v", err)
					}
				} else if !errors.Is(err, cause) || !IsServerUnavailable(err) {
					t.Errorf("transport cause/classification lost: %v", err)
				}
				if calls.Load() != 1 {
					t.Errorf("uncertain delivery was retried: %d calls", calls.Load())
				}
			})
		}
	}
}

func TestSendOverseerMessage_StructuredRouteErrorsNeverDowngrade(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		for _, tc := range []struct {
			name        string
			contentType string
			body        string
		}{
			{"unknown structured keys", "application/json", `{"message":"human intervention disabled","code":"FORBIDDEN"}`},
			{"empty object", "application/json", "{}"},
			{"null", "", "null"},
			{"truncated error", "application/json", `{"error":"human intervention disabled"`},
			{"truncated error without type", "", `{"error":"human intervention disabled"`},
			{"truncated array", "text/plain", `[{"error":"forbidden"}`},
			{"JSON empty body", "application/json; charset=utf-8", ""},
			{"problem JSON malformed body", "application/problem+json", "response truncated"},
		} {
			t.Run(fmt.Sprintf("%d/%s", status, tc.name), func(t *testing.T) {
				t.Parallel()
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != "/mail/project/overseer/send" {
						t.Errorf("structured rejection reached ordinary MCP: %s", r.URL.Path)
					}
					if tc.contentType != "" {
						w.Header().Set("Content-Type", tc.contentType)
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				c := NewClient(WithBaseURL(server.URL + "/mcp/"))
				c.SetRegistrationToken("/project", HumanOverseerAgentName, "existing-token")
				_, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
					ProjectKey: "/project", ProjectSlug: "project", Recipients: []string{"BlueLake"},
					Subject: "Task", BodyMD: "Do this",
				})
				var apiErr *APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != status || IsNotImplemented(err) || calls.Load() != 1 {
					t.Fatalf("structured rejection downgraded: err=%v requests=%d", err, calls.Load())
				}
			})
		}
	}
}

func TestSendOverseerMessage_MCPFallbackDoesNotReplayRedirects(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			var interventionCalls, mcpCalls, replayCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/mail/project/overseer/send":
					interventionCalls.Add(1)
					http.NotFound(w, r)
				case "/mcp/":
					mcpCalls.Add(1)
					http.Redirect(w, r, "/replayed-message", status)
				default:
					replayCalls.Add(1)
					t.Errorf("legacy overseer send followed redirect to %s", r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			c := NewClient(WithBaseURL(server.URL + "/mcp/"))
			c.SetRegistrationToken("/project", HumanOverseerAgentName, "existing-token")
			_, err := c.SendOverseerMessage(t.Context(), OverseerMessageOptions{
				ProjectKey: "/project", ProjectSlug: "project", Recipients: []string{"BlueLake"},
				Subject: "Task", BodyMD: "Do this",
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Errorf("lost original redirect status: %v", err)
			}
			if interventionCalls.Load() != 1 || mcpCalls.Load() != 1 || replayCalls.Load() != 0 {
				t.Errorf("legacy send was replayed: HTTP=%d MCP=%d redirects=%d", interventionCalls.Load(), mcpCalls.Load(), replayCalls.Load())
			}
		})
	}
}
