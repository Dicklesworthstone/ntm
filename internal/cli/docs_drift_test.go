package cli

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestReadmeNtmFlagsExist is the docs-drift gate from ntm-25ow: every
// `--flag` used in a README `ntm ...` example must exist on the command the
// example invokes. The originating field report was an operator following
// documented `--stagger-mode` guidance against a binary that rejected it;
// this test keeps the shipped docs and the flag registry from drifting apart
// in-tree (stale installed binaries are out of scope).
func TestReadmeNtmFlagsExist(t *testing.T) {
	readme := filepath.Join("..", "..", "README.md")
	file, err := os.Open(readme)
	if err != nil {
		t.Fatalf("open README.md: %v", err)
	}
	defer file.Close()

	flagToken := regexp.MustCompile(`--[a-z][a-z0-9-]*`)

	lookupCommand := func(fields []string) *cobra.Command {
		cmd := rootCmd
		for _, field := range fields {
			if strings.HasPrefix(field, "-") {
				break
			}
			next, _, err := cmd.Find([]string{field})
			if err != nil || next == cmd {
				break
			}
			cmd = next
		}
		return cmd
	}

	hasFlag := func(cmd *cobra.Command, name string) bool {
		name = strings.TrimPrefix(name, "--")
		found := false
		check := func(fs *pflag.FlagSet) {
			if fs.Lookup(name) != nil {
				found = true
			}
		}
		for c := cmd; c != nil; c = c.Parent() {
			check(c.Flags())
			check(c.PersistentFlags())
		}
		return found
	}

	scanner := bufio.NewScanner(file)
	lineNo := 0
	inFence := false
	var failures []string
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			continue
		}
		// Only single ntm invocations: skip pipelines, subshells, and
		// commands for other tools so their flags aren't checked against
		// ntm's registry.
		if !strings.HasPrefix(trimmed, "ntm ") {
			continue
		}
		if strings.ContainsAny(trimmed, "|$(") {
			continue
		}
		fields := strings.Fields(trimmed)[1:]
		cmd := lookupCommand(fields)
		for _, field := range fields {
			// A bare "--" terminates ntm's own flags; everything after it
			// belongs to the wrapped command (e.g. `ntm safety check -- git
			// reset --hard`).
			if field == "--" {
				break
			}
			match := flagToken.FindString(field)
			if match == "" || !strings.HasPrefix(field, match) {
				continue
			}
			// Robot-mode flags are parsed by a separate registry on the
			// root command; --robot-* names with =SESSION suffixes still
			// resolve via the base name.
			name := strings.SplitN(match, "=", 2)[0]
			if !hasFlag(cmd, name) {
				failures = append(failures, strings.TrimSpace(line)+" — unknown flag "+name+" (README.md:"+itoaTest(lineNo)+")")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan README.md: %v", err)
	}
	for _, failure := range failures {
		t.Error(failure)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// ntm-dv50 / AP-51: the robot parser must accept --no-cass-check so operator
// loops can carry one flag set across ntm send and --robot-send.
func TestRobotParserAcceptsNoCassCheck(t *testing.T) {
	if rootCmd.Flags().Lookup("no-cass-check") == nil {
		t.Fatal("--no-cass-check missing from the robot (root) flag registry")
	}
}
