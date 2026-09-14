package robot

import "testing"

func TestZZScratchCheck(t *testing.T) {
	cases := []struct {
		name    string
		s       string
		wantErr bool
	}{
		{"handoff prose #297", "... it failed naming exactly the seven against the HEAD ci.yml before the edit,\nand 19/19 real-tree switches discriminate after.\n\n❯ \n", false},
		{"planted negative", "the planted negative failed as expected\n❯ \n", false},
		{"baseexception prose", "a review finding said cleanup catches BaseException\n❯ \n", false},
		{"wait timeout prose", "service status: wait timeout 20000ms\n❯ \n", false},
		{"sha hash 429", "6f764f0c7344e437767e61f189f493065dca56cadc7048610948be7a5d42963f\n❯ \n", false},
		{"real failed to", "Failed to connect to api.anthropic.com\n", true},
		{"real error line", "  ⎿  API Error: 500 overloaded_error\n", true},
		{"real panic", "some line\npanic: runtime error: index out of range\n", true},
		{"real traceback", "Traceback (most recent call last):\n  File \"x.py\"\n", true},
		{"real 429", "HTTP/1.1 429 Too Many Requests\n", true},
		{"real rate limit banner", "Rate limit exceeded. Try again in 60s.\n", true},
		{"nonzero exit", "command exited with code 2\n", true},
		{"zero exit", "the command exited with code 0\n", false},
	}
	for _, c := range cases {
		got := DefaultLibrary.HasError(c.s, "claude")
		if got != c.wantErr {
			t.Errorf("%-24s hasError=%v want=%v  matches=%v", c.name, got, c.wantErr, DefaultLibrary.MatchByCategory(c.s, "claude", CategoryError))
		}
	}
}
