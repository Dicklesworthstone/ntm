package cli

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	testAgentCatCommandTemplate    = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}cat`
	testAgentBinCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
)

func TestMain(m *testing.M) {
	// Clean up any orphan test sessions from previous runs before starting.
	// This catches sessions left behind when tests are interrupted (Ctrl+C, timeout, etc.)
	testutil.KillAllTestSessionsSilent()

	code := m.Run()

	// Clean up after all tests complete
	testutil.KillAllTestSessionsSilent()

	os.Exit(code)
}
