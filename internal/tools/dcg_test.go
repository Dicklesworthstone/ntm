package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCheckCommandLeadingDashCommand is the regression guard for prompts whose
// extracted command begins with "-" (e.g. a markdown bullet line): dcg must
// receive the command after the "--" terminator, otherwise clap parses it as a
// flag and exits 2, which failed the whole send instead of returning a verdict.
func TestCheckCommandLeadingDashCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake dcg uses a shell script")
	}
	dir := t.TempDir()
	// Emulates clap's argv handling: any pre-terminator operand that starts
	// with "-" and is not a known flag is a parse error (exit 2).
	script := "#!/bin/sh\n" +
		"seen_sep=0\n" +
		"skip_next=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$skip_next\" = \"1\" ]; then skip_next=0; continue; fi\n" +
		"  if [ \"$seen_sep\" = \"1\" ]; then continue; fi\n" +
		"  case \"$a\" in\n" +
		"    --) seen_sep=1 ;;\n" +
		"    --robot|test) ;;\n" +
		"    --format) skip_next=1 ;;\n" +
		"    -*) echo \"error: unexpected argument '$a' found\" 1>&2; exit 2 ;;\n" +
		"    *) ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "dcg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake dcg: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewDCGAdapter()
	blocked, err := adapter.CheckCommand(context.Background(), "- run br list && git status")
	if err != nil {
		t.Fatalf("CheckCommand returned error for leading-dash command: %v", err)
	}
	if blocked != nil {
		t.Fatalf("CheckCommand unexpectedly blocked leading-dash command: %+v", blocked)
	}
}

func TestInferSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		expected string
	}{
		// Critical patterns
		{"rm -rf root", "rm -rf /", "critical"},
		{"rm -rf root wildcard", "rm -rf /*", "critical"},
		{"rm -rf root path", "rm -rf /etc", "critical"},
		{"rm -rf with relative excluded", "rm -rf ./build", "medium"}, // relative is medium, not critical
		{"dd zero to device", "dd if=/dev/zero of=/dev/sda", "critical"},
		{"dd urandom to device", "dd if=/dev/urandom of=/dev/nvme0n1", "critical"},
		{"drop database", "DROP DATABASE production;", "critical"},
		{"drop table", "drop table users;", "critical"},

		// High patterns
		{"git reset hard", "git reset --hard HEAD~5", "high"},
		{"git push force", "git push --force origin main", "high"},
		{"git push force short", "git push -f origin develop", "high"},
		{"chmod 777 recursive R", "chmod -R 777 /var/www", "high"},
		{"chmod 777 recursive r", "chmod -r 777 /home", "high"},

		// Medium patterns
		{"rm -r directory", "rm -r ./tmp", "medium"},
		{"rm -rf local", "rm -rf ./node_modules", "medium"},
		{"git stash drop", "git stash drop", "medium"},

		// Low patterns
		{"rm single file", "rm file.txt", "low"},
		{"rm multiple files", "rm a.txt b.txt c.txt", "low"},

		// Default (blocked but no specific pattern)
		{"unknown dangerous cmd", "some-dangerous-command", "medium"},
		{"echo harmless", "echo hello", "medium"}, // blocked commands get medium by default
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := inferSeverity(tc.command)
			if got != tc.expected {
				t.Errorf("inferSeverity(%q) = %q, want %q", tc.command, got, tc.expected)
			}
		})
	}
}

func TestInferRuleCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		expected string
	}{
		// Root recursive delete
		{"rm -rf root", "rm -rf /", "RECURSIVE_DELETE_ROOT"},
		{"rm -rf root wildcard", "rm -rf /*", "RECURSIVE_DELETE_ROOT"},

		// Outside project recursive delete
		{"rm -rf absolute path", "rm -rf /var/log", "RECURSIVE_DELETE_OUTSIDE_PROJECT"},
		{"rm -rf home", "rm -rf /home/user/data", "RECURSIVE_DELETE_OUTSIDE_PROJECT"},

		// Git patterns
		{"git reset hard", "git reset --hard", "HARD_RESET"},
		{"git push force main", "git push --force origin main", "FORCE_PUSH_PROTECTED"},
		{"git push force main short flag", "git push -f origin main", "FORCE_PUSH_PROTECTED"},
		{"git push force other branch", "git push --force origin feature", "BLOCKED_COMMAND"}, // not protected

		// Database patterns
		{"drop database", "DROP DATABASE mydb;", "DROP_DATABASE"},
		{"drop table", "drop table users;", "DROP_TABLE"},

		// Disk overwrite
		{"dd to device", "dd if=/dev/zero of=/dev/sda bs=1M", "DISK_OVERWRITE"},

		// Chmod patterns
		{"chmod 777 recursive", "chmod -R 777 /var", "CHMOD_RECURSIVE_777"},
		{"chmod 777 recursive lowercase", "chmod -r 777 /tmp", "CHMOD_RECURSIVE_777"},

		// Default
		{"unknown command", "some-blocked-command", "BLOCKED_COMMAND"},
		{"rm local", "rm -rf ./build", "BLOCKED_COMMAND"}, // relative path, no specific rule
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := inferRuleCode(tc.command)
			if got != tc.expected {
				t.Errorf("inferRuleCode(%q) = %q, want %q", tc.command, got, tc.expected)
			}
		})
	}
}

func TestExtractRCHInnerCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		command  string
		expected string
		ok       bool
	}{
		{
			name:     "rch exec with separator",
			command:  "rch exec -- cargo build",
			expected: "cargo build",
			ok:       true,
		},
		{
			name:     "rch exec without separator",
			command:  "rch exec go test ./...",
			expected: "go test ./...",
			ok:       true,
		},
		{
			name:     "legacy rch build with separator only",
			command:  "rch build -- cargo build",
			expected: "cargo build",
			ok:       true,
		},
		{
			name:     "legacy rch intercept passthrough",
			command:  "rch intercept go test ./...",
			expected: "go test ./...",
			ok:       true,
		},
		{
			name:     "legacy rch offload passthrough",
			command:  "rch offload go build ./cmd/ntm",
			expected: "go build ./cmd/ntm",
			ok:       true,
		},
		{
			name:     "rch with separator and no subcommand",
			command:  "rch -- go test ./...",
			expected: "go test ./...",
			ok:       true,
		},
		{
			name:    "rch status no inner",
			command: "rch status",
			ok:      false,
		},
		{
			name:    "non-rch command",
			command: "go build ./cmd/ntm",
			ok:      false,
		},
		// Edge cases for coverage
		{
			name:    "empty command",
			command: "",
			ok:      false,
		},
		{
			name:    "whitespace only",
			command: "   \t  ",
			ok:      false,
		},
		{
			name:    "rch alone",
			command: "rch",
			ok:      false,
		},
		{
			name:    "rch exec alone",
			command: "rch exec",
			ok:      false,
		},
		{
			name:    "legacy rch build alone",
			command: "rch build",
			ok:      false,
		},
		{
			name:    "legacy rch intercept alone",
			command: "rch intercept",
			ok:      false,
		},
		{
			name:    "legacy rch offload alone",
			command: "rch offload",
			ok:      false,
		},
		{
			name:    "rch with separator at end",
			command: "rch --",
			ok:      false,
		},
		{
			name:    "legacy rch build with separator at end",
			command: "rch build --",
			ok:      false,
		},
		{
			name:    "rch unknown subcommand",
			command: "rch unknown something",
			ok:      false,
		},
		{
			name:     "rch exec with multiple args",
			command:  "rch exec cargo build --release --target x86_64-unknown-linux-gnu",
			expected: "cargo build --release --target x86_64-unknown-linux-gnu",
			ok:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := extractRCHInnerCommand(tc.command)
			if ok != tc.ok {
				t.Fatalf("extractRCHInnerCommand(%q) ok=%v, want %v", tc.command, ok, tc.ok)
			}
			if ok && got != tc.expected {
				t.Fatalf("extractRCHInnerCommand(%q)=%q, want %q", tc.command, got, tc.expected)
			}
		})
	}
}
