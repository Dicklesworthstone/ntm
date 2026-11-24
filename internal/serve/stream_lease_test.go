package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestPaneStreamLeaseParameter(t *testing.T) {
	const lease = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		query string
		want  string
		valid bool
	}{
		{"", "", true},
		{"?lease=" + strings.ToUpper(lease), lease, true},
		{"?lease=", "", false},
		{"?lease=garbage", "", false},
		{"?lease=%zz", "", false},
		{"?lease=" + lease + "&lease=" + lease, "", false},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/pane/stream"+tc.query, nil)
		got, valid := paneStreamLeaseFromRequest(w, req, "test")
		if got != tc.want || valid != tc.valid || (!valid && w.Code != http.StatusBadRequest) {
			t.Errorf("query %q: lease=%q valid=%v status=%d", tc.query, got, valid, w.Code)
		}
	}
}

func TestPaneStreamLeaseHTTPViewersShareWorker(t *testing.T) {
	cfg := tmux.DefaultPaneStreamerConfig()
	cfg.FallbackPollInterval = time.Hour
	manager := tmux.NewStreamManager(tmux.NewClient("lease-test-host"), func(tmux.StreamEvent) {}, cfg)
	t.Cleanup(manager.StopAll)
	s := &Server{
		streamManager: manager,
		resolvePane: func(context.Context, string, tmux.PaneSelector) (tmux.Pane, error) {
			return tmux.Pane{ID: "%42", Index: 1}, nil
		},
	}
	r := chi.NewRouter()
	const route = "/sessions/{sessionId}/panes/{paneIdx}/stream"
	r.Post(route, s.handleStartPaneStreamV1)
	r.Delete(route, s.handleStopPaneStreamV1)
	call := func(method, owner string, wantActive int) {
		t.Helper()
		path := "/sessions/demo/panes/%2542/stream"
		if owner != "" {
			path += "?lease=" + owner
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		if got := len(manager.ListActive()); got != wantActive {
			t.Fatalf("%s %s left %d workers, want %d", method, path, got, wantActive)
		}
		if method == http.MethodPost && owner != "" {
			var response struct {
				Topic string `json:"topic"`
				TTL   int    `json:"lease_ttl_seconds"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Topic != "panes:%42" || response.TTL <= 0 {
				t.Fatalf("invalid lease response: %s (%v)", w.Body, err)
			}
		}
	}
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	call(http.MethodPost, first, 1)
	call(http.MethodPost, second, 1)
	call(http.MethodPost, first, 1)
	call(http.MethodPost, "", 1)
	call(http.MethodDelete, first, 1)
	call(http.MethodDelete, "", 1)
	call(http.MethodDelete, first, 1)
	call(http.MethodDelete, second, 0)
}
