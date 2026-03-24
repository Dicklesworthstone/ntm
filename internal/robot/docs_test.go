package robot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGetDocs_Index(t *testing.T) {
	// Test getting topic index (empty topic)
	output, err := GetDocs("")
	if err != nil {
		t.Fatalf("GetDocs failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected Success=true, got false")
	}

	if output.Version == "" {
		t.Errorf("expected non-empty version")
	}

	if output.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("expected schema version %s, got %s", CurrentSchemaVersion, output.SchemaVersion)
	}

	if len(output.Topics) == 0 {
		t.Errorf("expected topics list, got empty")
	}

	// Verify all expected topics are present
	expectedTopics := map[string]bool{
		"quickstart": false,
		"commands":   false,
		"examples":   false,
		"exit-codes": false,
	}

	for _, topic := range output.Topics {
		if _, exists := expectedTopics[topic.Name]; exists {
			expectedTopics[topic.Name] = true
		}
	}

	for name, found := range expectedTopics {
		if !found {
			t.Errorf("expected topic %q not found", name)
		}
	}
}

func TestGetDocs_Quickstart(t *testing.T) {
	output, err := GetDocs("quickstart")
	if err != nil {
		t.Fatalf("GetDocs(quickstart) failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected Success=true, got false")
	}

	if output.Topic != "quickstart" {
		t.Errorf("expected topic 'quickstart', got %q", output.Topic)
	}

	if output.Content == nil {
		t.Fatal("expected content, got nil")
	}

	if output.Content.Title == "" {
		t.Errorf("expected non-empty title")
	}

	if len(output.Content.Sections) == 0 {
		t.Errorf("expected sections, got empty")
	}

	if len(output.Content.Examples) == 0 {
		t.Errorf("expected examples, got empty")
	}
}

func TestGetDocs_Commands(t *testing.T) {
	output, err := GetDocs("commands")
	if err != nil {
		t.Fatalf("GetDocs(commands) failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected Success=true, got false")
	}

	if output.Content == nil {
		t.Fatal("expected content, got nil")
	}

	if len(output.Content.Sections) == 0 {
		t.Errorf("expected sections for commands topic")
	}
}

func TestGetDocs_Examples(t *testing.T) {
	output, err := GetDocs("examples")
	if err != nil {
		t.Fatalf("GetDocs(examples) failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected Success=true, got false")
	}

	if output.Content == nil {
		t.Fatal("expected content, got nil")
	}

	if len(output.Content.Examples) == 0 {
		t.Errorf("expected examples, got empty")
	}

	// Verify example structure
	for _, ex := range output.Content.Examples {
		if ex.Name == "" {
			t.Errorf("expected example name, got empty")
		}
		if ex.Command == "" {
			t.Errorf("expected example command, got empty")
		}
		if ex.Description == "" {
			t.Errorf("expected example description, got empty")
		}
	}

	for _, ex := range output.Content.Examples {
		if strings.Contains(ex.Command, "--ack-timeout") || strings.Contains(ex.Command, "--ack-poll") || strings.Contains(ex.Command, "--wait-timeout") || strings.Contains(ex.Command, "--wait-poll") || strings.Contains(ex.Command, "--spawn-timeout") || strings.Contains(ex.Command, "--ready-timeout") {
			t.Fatalf("examples topic still uses deprecated shared modifiers: %q", ex.Command)
		}
	}

	foundCanonicalTrack := false
	for _, ex := range output.Content.Examples {
		if ex.Name == "send_and_track" && strings.Contains(ex.Command, "--timeout=60s") {
			foundCanonicalTrack = true
			break
		}
	}
	if !foundCanonicalTrack {
		t.Fatal("examples topic missing canonical send_and_track timeout example")
	}

	foundCanonicalWait := false
	for _, ex := range output.Content.Examples {
		if ex.Name == "wait_for_attention" && strings.Contains(ex.Command, "--attention-cursor=42") && strings.Contains(ex.Command, "--timeout=2m") {
			foundCanonicalWait = true
			break
		}
	}
	if !foundCanonicalWait {
		t.Fatal("examples topic missing canonical wait_for_attention cursor/timeout example")
	}
}

func TestGetDocs_ExitCodes(t *testing.T) {
	output, err := GetDocs("exit-codes")
	if err != nil {
		t.Fatalf("GetDocs(exit-codes) failed: %v", err)
	}

	if !output.Success {
		t.Errorf("expected Success=true, got false")
	}

	if output.Content == nil {
		t.Fatal("expected content, got nil")
	}

	if len(output.Content.ExitCodes) == 0 {
		t.Errorf("expected exit codes, got empty")
	}

	// Verify exit code 0 is SUCCESS
	found := false
	for _, ec := range output.Content.ExitCodes {
		if ec.Code == 0 {
			found = true
			if ec.Name != "SUCCESS" {
				t.Errorf("expected exit code 0 name 'SUCCESS', got %q", ec.Name)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected exit code 0, not found")
	}
}

func TestGetDocs_InvalidTopic(t *testing.T) {
	output, err := GetDocs("invalid-topic")
	if err != nil {
		t.Fatalf("GetDocs(invalid-topic) should not return error, got: %v", err)
	}

	if output.Success {
		t.Errorf("expected Success=false for invalid topic")
	}

	if output.ErrorCode != ErrCodeInvalidFlag {
		t.Errorf("expected error code %s, got %s", ErrCodeInvalidFlag, output.ErrorCode)
	}

	if output.Content != nil {
		t.Errorf("expected nil content for invalid topic")
	}
}

func TestDocsOutputJSON(t *testing.T) {
	output, err := GetDocs("quickstart")
	if err != nil {
		t.Fatalf("GetDocs failed: %v", err)
	}

	// Verify JSON serialization roundtrip
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("failed to marshal output: %v", err)
	}

	var decoded DocsOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	if decoded.Topic != output.Topic {
		t.Errorf("topic mismatch: got %q, want %q", decoded.Topic, output.Topic)
	}

	if decoded.SchemaVersion != output.SchemaVersion {
		t.Errorf("schema_version mismatch: got %q, want %q", decoded.SchemaVersion, output.SchemaVersion)
	}

	if decoded.Content == nil {
		t.Fatal("decoded content is nil")
	}

	if decoded.Content.Title != output.Content.Title {
		t.Errorf("content.title mismatch: got %q, want %q", decoded.Content.Title, output.Content.Title)
	}
}

func TestDocsExitCodeRecoverability(t *testing.T) {
	output, err := GetDocs("exit-codes")
	if err != nil {
		t.Fatalf("GetDocs(exit-codes) failed: %v", err)
	}

	// Verify that certain codes are marked as non-recoverable
	nonRecoverableCodes := []int{20, 30, 50} // TOOL_NOT_FOUND, TMUX_NOT_FOUND, INTERNAL_ERROR

	for _, ec := range output.Content.ExitCodes {
		for _, nrc := range nonRecoverableCodes {
			if ec.Code == nrc && ec.Recoverable {
				t.Errorf("exit code %d (%s) should be non-recoverable", ec.Code, ec.Name)
			}
		}
	}
}
