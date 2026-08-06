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
	if !strings.Contains(err.Error(), "accepts 0 arg(s)") {
		t.Fatalf("error = %q, want Cobra no-arguments error", err)
	}
}
