package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func stripANSIForTest(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansi.ReplaceAllString(str, "")
}

func TestPrintStunningHelp(t *testing.T) {
	// Use buffer instead of stdout
	var buf bytes.Buffer

	// Run function with buffer
	PrintStunningHelp(&buf)

	// Read output
	output := stripANSIForTest(buf.String())

	// Verify key components exist
	expected := []string{
		"Named Tmux Session Manager for AI Agents", // Subtitle
		"SESSION CREATION",                         // Section 1
		"AGENT MANAGEMENT",                         // Section 2
		"spawn",                                    // Command
		"Create session and launch agents",         // Description
		"Aliases:",                                 // Footer
	}

	for _, exp := range expected {
		if !strings.Contains(output, exp) {
			t.Errorf("Expected help output to contain %q, but it didn't", exp)
		}
	}
}

func TestPrintMinimalHelp(t *testing.T) {
	// Use buffer instead of stdout
	var buf bytes.Buffer

	// Run function with buffer
	PrintMinimalHelp(&buf)

	// Read output
	output := stripANSIForTest(buf.String())

	// Verify key components exist
	expected := []string{
		"Named Tmux Session Manager for AI Agents", // Subtitle
		"ESSENTIAL COMMANDS",                       // Section header
		"spawn",                                    // Essential command
		"send",                                     // Essential command
		"status",                                   // Essential command
		"kill",                                     // Essential command
		"help",                                     // Essential command
		"Create session and launch agents",         // spawn description
		"Send prompt to agents",                    // send description
		"Show detailed session status",             // status description
		"Kill a session",                           // kill description
		"Show help information",                    // help description
		"QUICK START",                              // Quick start section
		"For all commands: ntm --full",             // Instructions for full help
	}

	for _, exp := range expected {
		if !strings.Contains(output, exp) {
			t.Errorf("Expected minimal help output to contain %q, but it didn't", exp)
		}
	}

	// Verify that non-essential commands are NOT present
	shouldNotContain := []string{
		"SESSION CREATION", // This is from full help
		"AGENT MANAGEMENT", // This is from full help
		"create",           // Non-essential command
		"quick",            // Non-essential command
		"add",              // Non-essential command
		"interrupt",        // Non-essential command
	}

	for _, notExp := range shouldNotContain {
		if strings.Contains(output, notExp) {
			t.Errorf("Expected minimal help output to NOT contain %q, but it did", notExp)
		}
	}
}

func TestPrintHelpTier1OnlyEssentialCommands(t *testing.T) {
	// Minimal help (Tier 1 Apprentice) should only show essential commands
	var buf bytes.Buffer
	PrintMinimalHelp(&buf)
	output := stripANSIForTest(buf.String())

	// Essential commands should be present
	essentialCommands := []string{"spawn", "send", "status", "kill", "help"}
	for _, cmd := range essentialCommands {
		if !strings.Contains(output, cmd) {
			t.Errorf("Expected Tier 1 help to contain essential command %q", cmd)
		}
	}

	// Journeyman/Master commands should NOT be present in minimal help
	advancedCommands := []string{"dashboard", "assign", "policy", "robot"}
	for _, cmd := range advancedCommands {
		if strings.Contains(output, cmd) {
			t.Errorf("Expected Tier 1 help to NOT contain advanced command %q", cmd)
		}
	}
}

func TestPrintHelpTier3IncludesAllCommands(t *testing.T) {
	// Full/stunning help (Tier 3 Master) should include all commands
	var buf bytes.Buffer
	PrintStunningHelp(&buf)
	output := stripANSIForTest(buf.String())

	// Master tier commands should be present
	masterCommands := []string{"spawn", "send", "status", "kill", "attach", "dashboard"}
	for _, cmd := range masterCommands {
		if !strings.Contains(output, cmd) {
			t.Errorf("Expected Tier 3 help to contain command %q", cmd)
		}
	}

	// Should include advanced sections
	advancedSections := []string{"SESSION CREATION", "AGENT MANAGEMENT", "SESSION NAVIGATION"}
	for _, section := range advancedSections {
		if !strings.Contains(output, section) {
			t.Errorf("Expected Tier 3 help to contain section %q", section)
		}
	}
}

func TestHelpAliasesShown(t *testing.T) {
	// Verify aliases are shown in help output
	var buf bytes.Buffer
	PrintStunningHelp(&buf)
	output := stripANSIForTest(buf.String())

	// Common aliases should appear
	if !strings.Contains(output, "Aliases:") {
		t.Error("Expected help output to contain 'Aliases:' section")
	}
}

func TestHelpQuickStartSection(t *testing.T) {
	// Verify quick start section exists and has useful examples
	var buf bytes.Buffer
	PrintMinimalHelp(&buf)
	output := stripANSIForTest(buf.String())

	if !strings.Contains(output, "QUICK START") {
		t.Error("Expected help output to contain 'QUICK START' section")
	}

	// Should have an example command
	if !strings.Contains(output, "ntm spawn") {
		t.Error("Expected quick start to contain 'ntm spawn' example")
	}
}

func TestHelpShellSetupSection(t *testing.T) {
	// Verify shell setup instructions are present
	var buf bytes.Buffer
	PrintMinimalHelp(&buf)
	output := stripANSIForTest(buf.String())

	if !strings.Contains(output, "Shell setup:") {
		t.Error("Expected help output to contain 'Shell setup:' section")
	}

	if !strings.Contains(output, "ntm shell") {
		t.Error("Expected shell setup to contain 'ntm shell' command")
	}
}
