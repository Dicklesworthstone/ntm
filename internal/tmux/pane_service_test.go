package tmux

import (
	"strings"
	"testing"
)

// ACFS runs long-lived service processes (cm, cass, …) in tmux panes next to
// agent panes and tags each one with the @acfs_service pane option. The tag is
// the durable identity — unlike a title the pane's own program cannot rewrite
// it — and ntm must use it to keep services out of agent health (ntm#305).

// servicePaneLine builds a full current-format list-panes line, including the
// two service pane-option fields.
func servicePaneLine(id, index, title, command, pid, window, recordedType, acfsService, ntmService string) string {
	sep := FieldSeparator
	return strings.Join([]string{
		id, index, title, command, "80", "24", "1", pid, window,
		recordedType, acfsService, ntmService,
	}, sep)
}

func TestParsePaneLine_ACFSServiceTagMarksServicePane(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%0", "1", "cm", "cm", "4242", "0", "", "cm", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if !pane.IsServicePane() {
		t.Fatalf("IsServicePane() = false, want true for an @acfs_service-tagged pane")
	}
	if pane.Service != "cm" {
		t.Fatalf("Service = %q, want %q", pane.Service, "cm")
	}
	if pane.ServiceManager != "acfs" {
		t.Fatalf("ServiceManager = %q, want %q", pane.ServiceManager, "acfs")
	}
}

func TestParsePaneLine_VendorNeutralServiceTag(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%5", "0", "queue", "worker", "9", "0", "", "", "queue"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if !pane.IsServicePane() || pane.Service != "queue" || pane.ServiceManager != "ntm" {
		t.Fatalf("pane = %+v, want a service pane named queue managed by ntm", pane)
	}
}

func TestParsePaneLine_ACFSTagWinsOverVendorNeutralTag(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%5", "0", "cass", "cass", "9", "0", "", "cass", "other"), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.Service != "cass" || pane.ServiceManager != "acfs" {
		t.Fatalf("Service/Manager = %q/%q, want cass/acfs", pane.Service, pane.ServiceManager)
	}
}

// A service pane's command can look like anything, including an agent CLI. The
// tag must still win, otherwise the command heuristic would resurrect exactly
// the misclassification #305 reports.
func TestParsePaneLine_ServiceTagSurvivesAgentLookingCommand(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%2", "3", "watch", "claude", "77", "0", "", "watchdog", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if !pane.IsServicePane() {
		t.Fatalf("IsServicePane() = false, want true; a service tag outranks the command heuristic")
	}
}

// The deliberate act of adopting a pane as an agent (`ntm adopt` records
// @ntm_agent_type) is the documented escape hatch: it outranks the tag.
func TestParsePaneLine_AdoptionOutranksServiceTag(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%3", "0", "sess__cc_1", "bash", "5", "0", "claude", "cm", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.IsServicePane() {
		t.Fatalf("IsServicePane() = true, want false for an explicitly adopted pane")
	}
	if pane.Type != AgentClaude {
		t.Fatalf("Type = %q, want %q", pane.Type, AgentClaude)
	}
}

// An untagged pane is untouched: no service, no manager.
func TestParsePaneLine_UntaggedPaneIsNotAService(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(servicePaneLine("%9", "2", "shell", "zsh", "11", "0", "", "", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.IsServicePane() || pane.ServiceManager != "" {
		t.Fatalf("pane = %+v, want no service identity", pane)
	}
}

// The pre-#305 10-field layout must still parse so a mid-upgrade format
// mismatch degrades to "no service tags", not to a hard error.
func TestParsePaneLine_LegacyFieldCountStillParses(t *testing.T) {
	t.Parallel()

	pane, err := parsePaneLine(paneLine("%1", "0", "sess__cc_2", "claude", "0", "0", ""), FieldSeparator)
	if err != nil {
		t.Fatalf("parsePaneLine: %v", err)
	}
	if pane.IsServicePane() {
		t.Fatalf("IsServicePane() = true, want false when the tag fields are absent")
	}
}

func TestParsePaneServiceOption(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":                                      "",
		"   ":                                   "",
		"cm":                                    "cm",
		"  cass  ":                              "cass",
		"agent\x1b[31mmail":                     "", // control characters would corrupt robot output
		"tab\tname":                             "",
		strings.Repeat("x", maxServiceTagLen):   strings.Repeat("x", maxServiceTagLen),
		strings.Repeat("x", maxServiceTagLen+1): "",
	}
	for raw, want := range cases {
		if got := ParsePaneServiceOption(raw); got != want {
			t.Errorf("ParsePaneServiceOption(%q) = %q, want %q", raw, got, want)
		}
	}
}
