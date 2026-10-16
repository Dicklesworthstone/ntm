package redaction

import "testing"

// Regression for GitHub issue #353: the PASSWORD rule matched the shell's
// PWD= working-directory variable in pane archives.
func TestPasswordRule_IgnoresWorkingDirectoryVariables(t *testing.T) {
	resetPatternsForTest(t)
	inputs := []string{
		"PWD=/home/someone/projects/example",
		"OLDPWD=/home/someone/projects/previous",
		"export PWD=/data/projects/example_repo",
		"declare -x OLDPWD=\"/var/tmp/worktree-1\"",
		"PWD=$HOME/projects/example",
		"PWD=~/projects/example",
		"pwd: /Users/someone/projects/example",
		`PWD=C:\Users\someone\example`,
		"SHLVL=1\nPWD=/srv/app/current\nHOME=/home/someone",
	}
	for _, in := range inputs {
		for _, f := range Scan(in, Config{Mode: ModeWarn}) {
			if f.Category == CategoryPassword {
				t.Errorf("Scan(%q) reported PASSWORD for %q", in, f.Match)
			}
		}
	}
}

func TestPasswordRule_IgnoresPlaceholderValues(t *testing.T) {
	resetPatternsForTest(t)
	inputs := []string{
		"password=<redacted>",
		`DB_Password=\u003cset\u003e`,
		"password: ************",
		`passwd="<hidden>"`,
	}
	for _, in := range inputs {
		for _, f := range Scan(in, Config{Mode: ModeWarn}) {
			if f.Category == CategoryPassword {
				t.Errorf("Scan(%q) reported PASSWORD for %q", in, f.Match)
			}
		}
	}
}

func TestPasswordRule_StillReportsRealPasswords(t *testing.T) {
	resetPatternsForTest(t)
	inputs := []string{
		"password=secretpass123",
		"MYSQL_PWD=/hunter2isnotapath",          // key only ends in PWD
		"DB_PWD=correcthorsebattery",            // key only ends in PWD
		"Driver={x};Uid=sa;Pwd=MyPassw0rd!;",    // ODBC connection string
		"PWD=notapathbutsecret",                 // bare PWD with non-path value
		"password: <script>alert(1)</script>xx", // angle brackets, not a placeholder
	}
	for _, in := range inputs {
		found := false
		for _, f := range Scan(in, Config{Mode: ModeWarn}) {
			if f.Category == CategoryPassword {
				found = true
			}
		}
		if !found {
			t.Errorf("Scan(%q) did not report PASSWORD", in)
		}
	}
}
