package cli

import "testing"

// checkDaemons used to set Status "ok" in both arms of its if/else, so a daemon
// that was not running still rendered as a green tick reading "port 8765
// available" — including one the operator had enabled in config.
func TestDaemonVerdict(t *testing.T) {
	cases := map[string]struct {
		running  bool
		expected bool
		want     string
	}{
		"running and expected":  {running: true, expected: true, want: "ok"},
		"running, not expected": {running: true, expected: false, want: "ok"},
		"down but enabled":      {running: false, expected: true, want: "warning"},
		"down and not enabled":  {running: false, expected: false, want: "unknown"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			status, message := daemonVerdict(tc.running, tc.expected, 8765)
			if status != tc.want {
				t.Errorf("status = %q, want %q", status, tc.want)
			}
			if message == "" {
				t.Error("verdict carried no message")
			}
			if !tc.running && status == "ok" {
				t.Error("a daemon that is not running must never report ok")
			}
		})
	}
}
