package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockOllamaServer creates a test server that mimics Ollama API
func mockOllamaServer(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := handlers[r.URL.Path]; ok {
			handler(w, r)
			return
		}
		http.NotFound(w, r)
	}))
}

// newConnectedAdapter builds an adapter connected to the given test server host.
func newConnectedAdapter(t *testing.T, host string) *Adapter {
	t.Helper()
	a := NewAdapter()
	if err := a.Connect(host); err != nil {
		t.Fatalf("Connect(%q) failed: %v", host, err)
	}
	return a
}

func TestNewAdapter(t *testing.T) {
	a := NewAdapter()
	if a == nil {
		t.Fatal("NewAdapter returned nil")
	}
	if a.client == nil {
		t.Error("client should be initialized")
	}
	if a.connected {
		t.Error("should not be connected initially")
	}
}

func TestConnect(t *testing.T) {
	tests := []struct {
		name        string
		serverFunc  func(w http.ResponseWriter, r *http.Request)
		wantErr     bool
		errContains string
	}{
		{
			name: "successful connection",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(ollamaTagsResponse{Models: []ollamaModel{}})
			},
			wantErr: false,
		},
		{
			name: "server error",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr:     true,
			errContains: "server returned 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := mockOllamaServer(t, map[string]http.HandlerFunc{
				"/api/tags": tt.serverFunc,
			})
			defer server.Close()

			a := NewAdapter()
			err := a.Connect(server.URL)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error should contain %q, got %q", tt.errContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if !a.connected {
					t.Error("should be connected after successful Connect")
				}
			}
		})
	}
}

func TestConnect_InvalidHost(t *testing.T) {
	a := NewAdapter()
	err := a.Connect("http://localhost:99999")
	if err == nil {
		t.Error("expected error for invalid host")
	}
}

func TestConnect_NormalizesHost(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{Models: []ollamaModel{}})
		},
	})
	defer server.Close()

	// Remove http:// prefix to test normalization
	host := strings.TrimPrefix(server.URL, "http://")

	a := NewAdapter()
	if err := a.Connect(host); err != nil {
		t.Errorf("failed to connect with normalized host: %v", err)
	}
}

func TestListModels(t *testing.T) {
	testModels := []ollamaModel{
		{
			Name:       "llama3:latest",
			Size:       4500000000,
			Digest:     "abc123",
			ModifiedAt: time.Now(),
			Details: ModelDetails{
				Family:        "llama",
				ParameterSize: "8B",
			},
		},
		{
			Name:       "mistral:7b",
			Size:       3800000000,
			Digest:     "def456",
			ModifiedAt: time.Now(),
		},
	}

	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{Models: testModels})
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	ctx := context.Background()

	models, err := a.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	if len(models) != len(testModels) {
		t.Errorf("expected %d models, got %d", len(testModels), len(models))
	}

	if models[0].Name != "llama3:latest" {
		t.Errorf("expected model name 'llama3:latest', got %q", models[0].Name)
	}

	if models[0].Details.Family != "llama" {
		t.Errorf("expected family 'llama', got %q", models[0].Details.Family)
	}
}

func TestListModels_NotConnected(t *testing.T) {
	a := NewAdapter()
	_, err := a.ListModels(context.Background())
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestListModels_DecodeError(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	_, err := a.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "failed to decode response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPullModel(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/pull": func(w http.ResponseWriter, r *http.Request) {
			var req ollamaPullRequest
			json.NewDecoder(r.Body).Decode(&req)

			if req.Name != "mistral:latest" {
				t.Errorf("expected model 'mistral:latest', got %q", req.Name)
			}

			flusher, _ := w.(http.Flusher)

			// Simulate pull progress
			statuses := []string{
				"pulling manifest",
				"downloading sha256:abc123",
				"verifying sha256:abc123",
				"success",
			}

			for _, status := range statuses {
				json.NewEncoder(w).Encode(ollamaPullResponse{Status: status})
				flusher.Flush()
			}
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)

	err := a.PullModel(context.Background(), "mistral:latest")
	if err != nil {
		t.Errorf("PullModel failed: %v", err)
	}
}

func TestPullModelWithProgress(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/pull": func(w http.ResponseWriter, r *http.Request) {
			var req ollamaPullRequest
			_ = json.NewDecoder(r.Body).Decode(&req)

			flusher, _ := w.(http.Flusher)
			updates := []ollamaPullResponse{
				{Status: "pulling manifest"},
				{Status: "downloading", Total: 100, Completed: 50},
				{Status: "success"},
			}
			for _, u := range updates {
				_ = json.NewEncoder(w).Encode(u)
				flusher.Flush()
			}
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)

	var progress []ModelPullProgress
	err := a.PullModelWithProgress(context.Background(), "mistral:latest", func(p ModelPullProgress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatalf("PullModelWithProgress failed: %v", err)
	}
	if len(progress) < 3 {
		t.Fatalf("expected at least 3 progress updates, got %d", len(progress))
	}
	if progress[1].Completed != 50 || progress[1].Total != 100 {
		t.Fatalf("expected mid-progress 50/100, got %d/%d", progress[1].Completed, progress[1].Total)
	}
	if !progress[len(progress)-1].Done {
		t.Fatalf("expected final progress update to be done")
	}
}

func TestPullModelWithProgress_HTTPError(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/pull": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"pull failed"}`))
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	err := a.PullModelWithProgress(context.Background(), "mistral:latest", nil)
	if err == nil {
		t.Fatal("expected pull error")
	}
}

func TestPullModelWithProgress_FailedStatus(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/pull": func(w http.ResponseWriter, r *http.Request) {
			flusher, _ := w.(http.Flusher)
			_ = json.NewEncoder(w).Encode(ollamaPullResponse{Status: "failed"})
			flusher.Flush()
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	err := a.PullModelWithProgress(context.Background(), "mistral:latest", nil)
	if err == nil {
		t.Fatal("expected failed final status error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPullModel_NotConnected(t *testing.T) {
	a := NewAdapter()
	err := a.PullModel(context.Background(), "mistral:latest")
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestDeleteModel(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/delete": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete {
				t.Fatalf("expected DELETE, got %s", r.Method)
			}
			var req ollamaDeleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Name != "mistral:latest" {
				t.Fatalf("expected model mistral:latest, got %q", req.Name)
			}
			w.WriteHeader(http.StatusOK)
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	if err := a.DeleteModel(context.Background(), "mistral:latest"); err != nil {
		t.Fatalf("DeleteModel failed: %v", err)
	}
}

func TestDeleteModel_NotConnected(t *testing.T) {
	a := NewAdapter()
	err := a.DeleteModel(context.Background(), "mistral:latest")
	if err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestDeleteModel_HTTPError(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
		"/api/delete": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"model not found"}`))
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	err := a.DeleteModel(context.Background(), "missing:latest")
	if err == nil {
		t.Fatal("expected delete error")
	}
	if !strings.Contains(err.Error(), ErrModelNotFound.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClose(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	if !a.connected {
		t.Fatal("should be connected")
	}

	if err := a.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	if a.connected {
		t.Error("should not be connected after Close")
	}
}

func TestHost(t *testing.T) {
	server := mockOllamaServer(t, map[string]http.HandlerFunc{
		"/api/tags": func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(ollamaTagsResponse{})
		},
	})
	defer server.Close()

	a := newConnectedAdapter(t, server.URL)
	if a.Host() != server.URL {
		t.Errorf("expected host %q, got %q", server.URL, a.Host())
	}
}

func TestClassifyError(t *testing.T) {
	t.Parallel()

	a := NewAdapter()

	tests := []struct {
		name        string
		err         error
		wantNil     bool
		wantContain string
	}{
		{
			name:    "nil error",
			err:     nil,
			wantNil: true,
		},
		{
			name:        "connection refused",
			err:         fmt.Errorf("dial tcp: connection refused"),
			wantContain: "is Ollama running?",
		},
		{
			name:        "timeout error",
			err:         fmt.Errorf("request timeout"),
			wantContain: "timed out",
		},
		{
			name:        "deadline exceeded",
			err:         fmt.Errorf("context deadline exceeded"),
			wantContain: "timed out",
		},
		{
			name:        "other error passthrough",
			err:         fmt.Errorf("some other error"),
			wantContain: "some other error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := a.classifyError(tc.err)
			if tc.wantNil {
				if result != nil {
					t.Errorf("classifyError(nil) = %v, want nil", result)
				}
				return
			}
			if result == nil {
				t.Fatal("classifyError returned nil, want error")
			}
			if !strings.Contains(result.Error(), tc.wantContain) {
				t.Errorf("classifyError(%v) = %q, want to contain %q",
					tc.err, result.Error(), tc.wantContain)
			}
		})
	}
}
