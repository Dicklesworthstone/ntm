package cli

import (
	"strings"
	"testing"
)

func TestServeCmdRejectsUnexpectedArguments(t *testing.T) {
	cmd := newServeCmd()
	cmd.SetArgs([]string{"unexpected"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("serve accepted an unexpected positional argument")
	}
	// serve declares cobra.NoArgs, whose message is `unknown command %q for %q`.
	// "accepts 0 arg(s)" is ExactArgs(0)'s wording and never applied here; the
	// contract under test is that the positional is rejected at all.
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %q, want Cobra no-arguments error", err)
	}
}
