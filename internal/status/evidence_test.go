package status

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRecoverPiRepaintRequiresUniqueNewTurn(t *testing.T) {
	prompt := "Review the intake charter and registers.\nReport the next action."
	normalized := NormalizeAgentEcho(prompt)
	digest := sha256.Sum256([]byte(normalized))
	hash := hex.EncodeToString(digest[:])
	echo := "> Review the intake charter\nand registers.\n\nReport the next action."
	for _, tc := range []struct {
		name, before, after, want string
		ok                        bool
	}{
		{"same-frame answer", "Pi v1.3.0\nOld conversation", "Pi v1.3.0\n\n" + echo + "\n\nThe next action is to reconcile the register.", echo + "\n\nThe next action is to reconcile the register.", true},
		{"echo only", "Old conversation", echo, echo, true},
		{"ANSI prompt", "Old conversation", "\x1b[32m" + prompt + "\x1b[0m\nResponse", "\x1b[32m" + prompt + "\x1b[0m\nResponse", true},
		{"old prompt in history", prompt + "\nEarlier answer", echo + "\nWrong turn", "", false},
		{"repeated prompt", "Old conversation", echo + "\nEarlier answer\n" + echo + "\nLater answer", "", false},
		{"partial prompt", "Old conversation", "Review the intake charter", "", false},
		{"mid-line match", "Old conversation", "Quoted request: " + normalized + "\nAnswer", "", false},
		{"extra text on last line", "Old conversation", prompt + " and do something else", "", false},
		{"unrelated replacement", "Old conversation", "Another task\nAn unrelated answer", "", false},
		{"empty repaint", "Old conversation", "\n\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RecoverPiRepaint(tc.before, tc.after, hash, len(normalized))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("RecoverPiRepaint = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRecoverPiRepaintRefusesMissingOrExcessiveEvidence(t *testing.T) {
	const prompt = "Perform the requested review."
	digest := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name, before, after, hash string
		length                    int
	}{
		{"legacy receipt", "Old text", prompt, hash, 0},
		{"negative length", "Old text", prompt, hash, -1},
		{"oversized length", "Old text", prompt, hash, (1 << 20) + 1},
		{"invalid hash", "Old text", prompt, "not-a-digest", len(prompt)},
		{"oversized baseline", strings.Repeat("x", (1<<20)+1), prompt, hash, len(prompt)},
		{"oversized replacement", "Old text", prompt + strings.Repeat("x", 1<<20), hash, len(prompt)},
		{"too many baseline rows", strings.Repeat("\n", 4097), prompt, hash, len(prompt)},
		{"too many replacement rows", "Old text", prompt + strings.Repeat("\n", 4097), hash, len(prompt)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := RecoverPiRepaint(tc.before, tc.after, tc.hash, tc.length); ok || got != "" {
				t.Fatalf("insufficient evidence recovered %q", got)
			}
		})
	}
}

func TestNormalizeAgentEchoRetainsExistingMarkerContract(t *testing.T) {
	for _, input := range []string{
		"review this\nthen report",
		"❯ review this\n› then report",
		"\x1b[32m> review   this\x1b[0m\n\n  » then\treport",
	} {
		if got := NormalizeAgentEcho(input); got != "review this then report" {
			t.Fatalf("normalization changed for %q: %q", input, got)
		}
	}
}
