package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
)

func TestUnknownFlagJSONGuidanceUsesFailingCommand(t *testing.T) {
	send, _, err := rootCmd.Find([]string{"send"})
	if err != nil || send == rootCmd { t.Fatalf("send command: %v", err) }
	for _, rejected := range []string{"op-id", "prompt"} {
		code, hint := classifyRobotExecuteErrorForCommand(errors.New("unknown flag: --"+rejected), send)
		if code != robot.ErrCodeInvalidFlag { t.Fatalf("%s code: %s", rejected, code) }
		if strings.Contains(hint, "Did you mean --"+rejected+"?") { t.Fatalf("self-referential suggestion: %s", hint) }
	}
	_, hint := classifyRobotExecuteErrorForCommand(errors.New("unknown flag: --projec"), send)
	if !strings.Contains(hint, "--project") { t.Fatalf("missing real inherited flag suggestion: %s", hint) }
	_, rootHint := classifyRobotExecuteErrorForCommand(errors.New("unknown flag: --op-id"), rootCmd)
	if strings.Contains(rootHint, "Did you mean --op-id?") { t.Fatalf("root suggested rejected flag: %s", rootHint) }
}
