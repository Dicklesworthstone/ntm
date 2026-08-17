package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

// =============================================================================
// Parity Gate CI Tests
//
// These tests verify CLI/robot output matches REST API output for equivalent
// operations. This ensures agents get consistent data regardless of interface.
// =============================================================================

// normalizeJSON removes volatile fields (timestamps, IDs) for comparison.
func normalizeJSON(t *testing.T, data []byte) map[string]any {
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Remove fields that change between invocations
	volatileFields := []string{
		"generated_at", "ts", "timestamp", "created_at", "updated_at",
		"request_id", "duration_ms", "duration", "elapsed",
	}

	removeVolatileFields(obj, volatileFields)
	return obj
}

// removeVolatileFields recursively removes volatile fields from a JSON object.
func removeVolatileFields(obj map[string]any, fields []string) {
	for _, field := range fields {
		delete(obj, field)
	}

	for _, v := range obj {
		switch val := v.(type) {
		case map[string]any:
			removeVolatileFields(val, fields)
		case []any:
			for _, item := range val {
				if m, ok := item.(map[string]any); ok {
					removeVolatileFields(m, fields)
				}
			}
		}
	}
}

// compareNormalizedJSON compares two normalized JSON objects.
func compareNormalizedJSON(t *testing.T, name string, cli, rest map[string]any) bool {
	cliJSON, _ := json.MarshalIndent(cli, "", "  ")
	restJSON, _ := json.MarshalIndent(rest, "", "  ")

	if !bytes.Equal(cliJSON, restJSON) {
		t.Errorf("%s: CLI and REST outputs differ", name)
		t.Logf("CLI output:\n%s", string(cliJSON))
		t.Logf("REST output:\n%s", string(restJSON))
		return false
	}
	return true
}

// =============================================================================
// Version Parity Tests
// =============================================================================

func TestParityVersionOutput(t *testing.T) {
	testutil.RequireNTMBinary(t)

	// Get CLI version output
	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-version")

	// Parse CLI output
	cliNorm := normalizeJSON(t, cliOutput)

	// Log for debugging
	t.Logf("CLI version output normalized: %+v", cliNorm)

	// Verify expected top-level fields are present
	if _, ok := cliNorm["version"]; !ok {
		t.Error("CLI version output missing 'version' field")
	}
	if _, ok := cliNorm["success"]; !ok {
		t.Error("CLI version output missing 'success' field")
	}

	// Verify system info nested fields
	system, ok := cliNorm["system"].(map[string]any)
	if !ok {
		t.Fatal("CLI version output missing 'system' object")
	}

	systemFields := []string{"go_version", "os", "arch"}
	for _, field := range systemFields {
		if _, ok := system[field]; !ok {
			t.Errorf("CLI version output missing system.%s field", field)
		}
	}
}

// =============================================================================
// Status Parity Tests
// =============================================================================

func TestParityStatusStructure(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// Get CLI status output
	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-status")

	// Parse and normalize
	cliNorm := normalizeJSON(t, cliOutput)

	// Verify expected structure
	if _, ok := cliNorm["sessions"]; !ok {
		t.Error("status output missing 'sessions' field")
	}
	if _, ok := cliNorm["summary"]; !ok {
		t.Error("status output missing 'summary' field")
	}

	// Sessions should be an array
	sessions, ok := cliNorm["sessions"].([]any)
	if !ok {
		t.Errorf("sessions should be an array, got %T", cliNorm["sessions"])
	}
	t.Logf("Found %d sessions in status output", len(sessions))
}

// =============================================================================
// Snapshot Parity Tests
// =============================================================================

func TestParitySnapshotStructure(t *testing.T) {
	testutil.RequireNTMBinary(t)
	testutil.RequireTmuxThrottled(t)

	// Get CLI snapshot output
	logger := testutil.NewTestLoggerStdout(t)
	out := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-snapshot")

	// Find JSON start (may have warning messages before)
	jsonStart := strings.Index(string(out), "{")
	if jsonStart == -1 {
		t.Fatal("no JSON found in snapshot output")
	}

	// Parse and normalize
	cliNorm := normalizeJSON(t, out[jsonStart:])

	// Verify expected structure
	requiredFields := []string{"sessions", "beads_summary", "agent_mail"}
	for _, field := range requiredFields {
		if _, ok := cliNorm[field]; !ok {
			t.Errorf("snapshot output missing %q field", field)
		}
	}

	// Sessions should be an array (never nil)
	if cliNorm["sessions"] == nil {
		t.Error("sessions should not be nil")
	}

	t.Logf("Snapshot structure validated: %d top-level fields", len(cliNorm))
}

// =============================================================================
// Kernel List Parity Tests
// =============================================================================

func TestParityKernelListOutput(t *testing.T) {
	testutil.RequireNTMBinary(t)

	// Get CLI kernel list output
	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "kernel", "list", "--json")

	// Parse output
	var payload struct {
		Commands []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Category    string `json:"category"`
			REST        *struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"rest"`
		} `json:"commands"`
		Count int `json:"count"`
	}

	if err := json.Unmarshal(cliOutput, &payload); err != nil {
		t.Fatalf("failed to parse kernel list output: %v", err)
	}

	t.Logf("Kernel registry contains %d commands", payload.Count)

	// Verify we have some commands registered
	if payload.Count == 0 {
		t.Error("expected at least some kernel commands")
	}

	// Count commands with REST bindings
	restCount := 0
	for _, cmd := range payload.Commands {
		if cmd.REST != nil {
			restCount++
		}
	}
	t.Logf("Commands with REST bindings: %d/%d", restCount, payload.Count)
}

// =============================================================================
// OpenAPI Spec Drift Detection
// =============================================================================

func TestOpenAPISpecDrift(t *testing.T) {
	testutil.RequireNTMBinary(t)

	// Find repo root
	repoRoot := findRepoRoot()
	if repoRoot == "" {
		t.Skip("could not find repo root")
	}

	checkedInPath := filepath.Join(repoRoot, "docs", "openapi-kernel.json")
	if _, err := os.Stat(checkedInPath); os.IsNotExist(err) {
		t.Skip("checked-in openapi-kernel.json not found")
	}

	// Generate new spec
	logger := testutil.NewTestLoggerStdout(t)
	tmpFile := filepath.Join(t.TempDir(), "openapi-new.json")
	testutil.AssertCommandSuccess(t, logger, "ntm", "openapi", "generate", "-o", tmpFile)

	// Read both specs
	checkedIn, err := os.ReadFile(checkedInPath)
	if err != nil {
		t.Fatalf("failed to read checked-in spec: %v", err)
	}

	generated, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read generated spec: %v", err)
	}

	// Parse both
	var checkedInJSON, generatedJSON map[string]any
	if err := json.Unmarshal(checkedIn, &checkedInJSON); err != nil {
		t.Fatalf("failed to parse checked-in spec: %v", err)
	}
	if err := json.Unmarshal(generated, &generatedJSON); err != nil {
		t.Fatalf("failed to parse generated spec: %v", err)
	}

	// Compare path counts
	checkedInPaths := countPaths(checkedInJSON)
	generatedPaths := countPaths(generatedJSON)

	t.Logf("Checked-in paths: %d, Generated paths: %d", checkedInPaths, generatedPaths)

	if checkedInPaths != generatedPaths {
		t.Errorf("OpenAPI path count drift: checked-in has %d, generated has %d",
			checkedInPaths, generatedPaths)
	}

	// Note: Full byte-for-byte comparison is done in CI.
	// This test catches structural changes.
}

func countPaths(spec map[string]any) int {
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		return 0
	}
	return len(paths)
}

func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// =============================================================================
// REST vs CLI Parity for Specific Endpoints
// =============================================================================

// requireTestServer returns the NTM_TEST_SERVER base URL or skips the test.
// The hermetic-serve harness (scripts/parity_harness.sh / the CI parity-harness
// job) always sets NTM_TEST_SERVER and independently asserts executed-count > 0,
// so a skip here can never silently pass in CI.
func requireTestServer(t *testing.T) string {
	t.Helper()
	serverURL := os.Getenv("NTM_TEST_SERVER")
	if serverURL == "" {
		t.Skip("set NTM_TEST_SERVER=http://localhost:7337 to run REST parity tests (see scripts/parity_harness.sh)")
	}
	return strings.TrimRight(serverURL, "/")
}

// getREST fetches a REST endpoint and returns the response body.
func getREST(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("REST request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("REST %s returned %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read REST response: %v", err)
	}
	return body
}

// stripRESTEnvelope removes top-level fields that the REST transport envelope
// (writeSuccessResponse) adds to every 200 response but that are not part of
// the shared payload contract the CLI emits. This is an envelope difference by
// design, not payload drift — parity is asserted on the payload.
func stripRESTEnvelope(obj map[string]any) {
	delete(obj, "success")
	delete(obj, "request_id")
}

// TestParityCLIvsRESTDeps compares CLI deps output with REST /api/v1/deps.
// Both surfaces run the same kernel command (core.deps), so the payloads must
// match field-for-field after removing volatile fields and the REST envelope.
func TestParityCLIvsRESTDeps(t *testing.T) {
	serverURL := requireTestServer(t)
	testutil.RequireNTMBinary(t)

	// Get CLI output
	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "deps", "--json")

	// Get REST output
	restOutput := getREST(t, serverURL+"/api/v1/deps")

	// Normalize and compare
	cliNorm := normalizeJSON(t, cliOutput)
	restNorm := normalizeJSON(t, restOutput)
	stripRESTEnvelope(restNorm)

	if !compareNormalizedJSON(t, "deps", cliNorm, restNorm) {
		t.Error("CLI and REST deps outputs differ")
	}
}

// TestParityCLIvsRESTHealth verifies the REST /api/v1/health liveness contract.
//
// Drift repair (2026-08-16, WS4 harness promotion): this test originally
// compared `ntm health --json` against /api/v1/health, but those were never
// equivalent operations — `ntm health <session>` reports per-agent health for a
// tmux session (and errors without a session argument), while /api/v1/health is
// server liveness. The test could never have passed as written. There is no CLI
// counterpart for server liveness (the CLI does not run the server), so this
// now asserts the documented liveness contract; cross-surface payload parity is
// covered by the deps/version/capabilities tests below.
func TestParityCLIvsRESTHealth(t *testing.T) {
	serverURL := requireTestServer(t)

	restOutput := getREST(t, serverURL+"/api/v1/health")

	var health map[string]any
	if err := json.Unmarshal(restOutput, &health); err != nil {
		t.Fatalf("failed to parse health response: %v", err)
	}

	if got, ok := health["status"].(string); !ok || got != "healthy" {
		t.Errorf("expected status=healthy, got %v", health["status"])
	}
	if got, ok := health["success"].(bool); !ok || !got {
		t.Errorf("expected success=true, got %v", health["success"])
	}
	if _, ok := health["request_id"].(string); !ok {
		t.Errorf("expected request_id string, got %v", health["request_id"])
	}
}

// TestParityCLIvsRESTVersion compares `ntm version --json` with REST
// /api/v1/version on the fields both surfaces share. Each surface carries
// intentional extras (CLI: commit/built_at/platform; REST: api_version +
// envelope), so parity is asserted per shared field rather than deep-equal.
func TestParityCLIvsRESTVersion(t *testing.T) {
	serverURL := requireTestServer(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "version", "--json")
	restOutput := getREST(t, serverURL+"/api/v1/version")

	cliNorm := normalizeJSON(t, cliOutput)
	restNorm := normalizeJSON(t, restOutput)

	for _, field := range []string{"version", "go_version"} {
		cliVal, cliOK := cliNorm[field]
		restVal, restOK := restNorm[field]
		if !cliOK || !restOK {
			t.Errorf("%s: missing on a surface (CLI present=%v, REST present=%v)", field, cliOK, restOK)
			continue
		}
		if cliVal != restVal {
			t.Errorf("%s mismatch: CLI=%v REST=%v", field, cliVal, restVal)
		}
	}
}

// TestParityCLIvsRESTCapabilities compares `ntm --robot-capabilities` with REST
// /api/v1/capabilities. Both surfaces serialize robot.GetCapabilities(), so the
// full payload must match deep-equal after normalization.
func TestParityCLIvsRESTCapabilities(t *testing.T) {
	serverURL := requireTestServer(t)
	testutil.RequireNTMBinary(t)

	logger := testutil.NewTestLoggerStdout(t)
	cliOutput := testutil.AssertCommandSuccess(t, logger, "ntm", "--robot-capabilities")
	restOutput := getREST(t, serverURL+"/api/v1/capabilities")

	cliNorm := normalizeJSON(t, cliOutput)
	restNorm := normalizeJSON(t, restOutput)
	stripRESTEnvelope(restNorm)
	// The CLI envelope also carries success; strip from both sides so the
	// comparison covers the capabilities payload itself.
	delete(cliNorm, "success")

	if !compareNormalizedJSON(t, "capabilities", cliNorm, restNorm) {
		t.Error("CLI and REST capabilities outputs differ")
	}
}

// =============================================================================
// Output Format Consistency Tests
// =============================================================================

func TestRobotOutputFormatConsistency(t *testing.T) {
	testutil.RequireNTMBinary(t)

	// Test that multiple robot flags produce consistent JSON structure
	robotFlags := []string{
		"--robot-version",
		"--robot-capabilities",
	}

	for _, flag := range robotFlags {
		t.Run(flag, func(t *testing.T) {
			logger := testutil.NewTestLoggerStdout(t)
			out := testutil.AssertCommandSuccess(t, logger, "ntm", flag)

			// Should be valid JSON
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Errorf("%s output is not valid JSON: %v", flag, err)
				t.Logf("Output: %s", string(out))
			}
		})
	}
}

// TestJSONOutputFlag verifies --json flag produces valid JSON for key commands.
func TestJSONOutputFlag(t *testing.T) {
	testutil.RequireNTMBinary(t)

	commands := [][]string{
		{"version", "--json"},
		{"kernel", "list", "--json"},
	}

	for _, args := range commands {
		name := strings.Join(args, " ")
		t.Run(name, func(t *testing.T) {
			logger := testutil.NewTestLoggerStdout(t)

			cmd := exec.Command("ntm", args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Logf("Command failed (may be expected): %v", err)
				t.Logf("Output: %s", string(out))
				return
			}

			// Find JSON in output
			jsonStart := bytes.Index(out, []byte("{"))
			if jsonStart == -1 {
				jsonStart = bytes.Index(out, []byte("["))
			}
			if jsonStart == -1 {
				t.Errorf("no JSON found in output for: ntm %s", name)
				return
			}

			var obj any
			if err := json.Unmarshal(out[jsonStart:], &obj); err != nil {
				t.Errorf("invalid JSON for ntm %s: %v", name, err)
				logger.Log("Output: %s", string(out))
			}
		})
	}
}

// =============================================================================
// Benchmark: CLI JSON Parsing
// =============================================================================

func BenchmarkCLIVersionParsing(b *testing.B) {
	// Pre-generate sample output
	sample := []byte(`{
		"version": "1.0.0",
		"commit": "abc123",
		"build_date": "2025-01-01",
		"built_by": "test",
		"go_version": "go1.25",
		"os": "linux",
		"arch": "amd64"
	}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var obj map[string]any
		_ = json.Unmarshal(sample, &obj)
	}
}
