package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/kernel"
	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestKernelCatalogConvergedWithCapabilitiesAndServedRoutes(t *testing.T) {
	srv := New(Config{
		Port:     0,
		EventBus: events.NewEventBus(16),
	})
	defer srv.Stop()

	EnsureKernelCatalogRegistered()
	commands := kernel.List()

	caps, err := robot.GetCapabilities()
	if err != nil {
		t.Fatalf("GetCapabilities: %v", err)
	}
	if len(commands) < len(caps.Commands) {
		t.Fatalf("converged kernel catalog count = %d, want >= %d robot capability surfaces", len(commands), len(caps.Commands))
	}

	byName := make(map[string]kernel.Command, len(commands))
	restCount := 0
	for _, cmd := range commands {
		byName[cmd.Name] = cmd
		if cmd.REST != nil && cmd.REST.Path != "" {
			restCount++
		}
	}
	if restCount < 150 {
		t.Fatalf("converged kernel REST bindings = %d, want >= 150 (/api/v1/* surface)", restCount)
	}

	for _, rc := range caps.Commands {
		wantName := "robot." + strings.ReplaceAll(rc.Name, "-", "_")
		if _, ok := byName[wantName]; !ok {
			t.Errorf("robot capability %q missing from converged kernel catalog as %q", rc.Name, wantName)
		}
	}

	// Verify /api/v1/kernel/commands serves the converged catalog over HTTP.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/kernel/commands", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/kernel/commands status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Success  bool             `json:"success"`
		Count    int              `json:"count"`
		Commands []kernel.Command `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal /api/v1/kernel/commands: %v", err)
	}
	if !payload.Success || payload.Count != len(commands) {
		t.Fatalf("/api/v1/kernel/commands success=%v count=%d, want count=%d", payload.Success, payload.Count, len(commands))
	}
	t.Logf("Converged kernel catalog: %d total commands, %d REST-bound commands", len(commands), restCount)
}
