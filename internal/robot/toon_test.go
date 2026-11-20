package robot

import (
	"strings"
	"testing"
	"time"
)

// =============================================================================
// TOON Encoder Unit Tests
// =============================================================================

func TestToonEncode_Primitives(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
	}{
		{"nil", nil},
		{"bool true", true},
		{"bool false", false},
		{"int", 42},
		{"negative int", -123},
		{"uint", uint(100)},
		{"float", 3.14159},
		{"float no trailing zeros", 1.5},
		{"float whole number", 2.0},
		{"string simple", "hello"},
		{"string with spaces", "hello world"},
		{"string with special chars", "hello\nworld"},
		{"string empty", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertToonRoundTrip(t, tc.input)
		})
	}
}

func TestToonEncode_SimpleArrays(t *testing.T) {
	t.Run("empty slice", func(t *testing.T) {
		assertToonRoundTrip(t, []int{})
	})

	t.Run("int slice", func(t *testing.T) {
		assertToonRoundTrip(t, []int{1, 2, 3})
	})

	t.Run("string slice", func(t *testing.T) {
		assertToonRoundTrip(t, []string{"a", "b", "c"})
	})
}

func TestToonEncode_TabularArrays(t *testing.T) {
	t.Run("uniform maps", func(t *testing.T) {
		input := []map[string]interface{}{
			{"id": 1, "name": "Alice"},
			{"id": 2, "name": "Bob"},
		}
		assertToonRoundTrip(t, input)
	})

	t.Run("uniform structs", func(t *testing.T) {
		type Person struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		input := []Person{
			{ID: 1, Name: "Alice"},
			{ID: 2, Name: "Bob"},
		}
		assertToonRoundTrip(t, input)
	})
}

func TestToonEncode_Objects(t *testing.T) {
	t.Run("simple map", func(t *testing.T) {
		input := map[string]int{"count": 42, "value": 100}
		assertToonRoundTrip(t, input)
	})

	t.Run("simple struct", func(t *testing.T) {
		type Config struct {
			Port    int    `json:"port"`
			Host    string `json:"host"`
			Enabled bool   `json:"enabled"`
		}
		input := Config{Port: 8080, Host: "localhost", Enabled: true}
		assertToonRoundTrip(t, input)
	})

	t.Run("empty map", func(t *testing.T) {
		input := map[string]int{}
		assertToonRoundTrip(t, input)
	})
}

func TestToonEncode_TabSafetyRoundTrip(t *testing.T) {
	input := []map[string]string{
		{"name": "Alice", "desc": "has\ttab"},
		{"name": "Bob", "desc": "normal"},
	}
	assertToonRoundTrip(t, input)
}

func TestToonEncode_NestedRoundTrip(t *testing.T) {
	input := []map[string]interface{}{
		{"id": 1, "tags": []string{"a", "b"}},
		{"id": 2, "tags": []string{"c"}},
	}
	assertToonRoundTrip(t, input)
}

func TestToonEncode_PointerHandling(t *testing.T) {
	t.Run("nil pointer", func(t *testing.T) {
		var ptr *int
		assertToonRoundTrip(t, ptr)
	})

	t.Run("non-nil pointer", func(t *testing.T) {
		val := 42
		ptr := &val
		assertToonRoundTrip(t, ptr)
	})
}

func TestToonEncode_TimeHandling(t *testing.T) {
	input := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	assertToonRoundTrip(t, input)
}

func TestToonEncode_JSONTagHandling(t *testing.T) {
	type Item struct {
		ID       int    `json:"id"`
		Name     string `json:"name"`
		internal string // unexported, should be skipped
		Ignored  string `json:"-"` // explicitly ignored
		OmitZero int    `json:"omit_zero,omitempty"`
	}

	input := Item{ID: 1, Name: "test", internal: "secret", Ignored: "skip", OmitZero: 0}
	assertToonRoundTrip(t, input)
}

func TestToonEncode_RobotPayloads(t *testing.T) {
	t.Run("RobotResponse", func(t *testing.T) {
		resp := NewRobotResponse(true)
		assertToonRoundTrip(t, resp)
	})

	t.Run("ErrorResponse", func(t *testing.T) {
		resp := NewErrorResponse(nil, ErrCodeInternalError, "test hint")
		assertToonRoundTrip(t, resp)
	})
}

func TestToonEncode_JSONMarshalError(t *testing.T) {
	ch := make(chan int)
	if _, err := toonEncode(ch, "\t"); err == nil {
		t.Fatal("expected json marshal error, got nil")
	}
}

// =============================================================================
// Pure Helper Function Tests
// =============================================================================

func TestFilepathBase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple filename", "file.txt", "file.txt"},
		{"unix path", "/usr/bin/tru", "tru"},
		{"windows path", `C:\Program Files\tru.exe`, "tru.exe"},
		{"mixed slashes", `C:\Users/bin\tru`, "tru"},
		{"trailing slash unix", "/usr/bin/", ""},
		{"just filename no path", "binary", "binary"},
		{"empty string", "", ""},
		{"root slash", "/", ""},
		{"relative path", "foo/bar/baz", "baz"},
		{"double slashes", "//foo//bar", "bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filepathBase(tt.input)
			if got != tt.want {
				t.Errorf("filepathBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLooksLikeToonRustVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"tru version", "tru 0.1.0", true},
		{"tru version 1.x", "tru 1.2.3", true},
		{"tru with suffix", "tru 0.1.0-beta", true},
		{"coreutils tr", "tr (gnu coreutils) 9.1", false},
		{"empty string", "", false},
		{"just tru", "tru", false},
		{"tru space no version", "tru ", false},
		{"toon_rust prefix", "toon_rust 0.1.0", false},
		{"wrong format", "version 1.0", false},
		{"tru with letter after space", "tru abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeToonRustVersion(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeToonRustVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestToonDelimiterArg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ","},
		{"comma", ",", ","},
		{"tab character", "\t", "tab"},
		{"pipe", "|", "|"},
		{"tab keyword", "tab", "tab"},
		{"comma keyword", "comma", "comma"},
		{"pipe keyword", "pipe", "pipe"},
		{"whitespace trimmed", "  custom  ", "custom"},
		{"semicolon", ";", ";"},
		{"colon", ":", ":"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toonDelimiterArg(tt.input)
			if got != tt.want {
				t.Errorf("toonDelimiterArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestToonDelimiterChar(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ","},
		{",", ","},
		{"comma", ","},
		{"\t", "\t"},
		{"tab", "\t"},
		{"|", "|"},
		{"pipe", "|"},
	}
	for _, tt := range tests {
		if got := toonDelimiterChar(tt.input); got != tt.want {
			t.Errorf("toonDelimiterChar(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestToonEncodePureGo_DelimitersAndEmbeddedFlattening(t *testing.T) {
	t.Run("tab delimiter emits real tab not literal word tab", func(t *testing.T) {
		payload := struct {
			RobotResponse
			Recommendations []string `json:"recommendations"`
			EmptyOptional   string   `json:"empty_optional,omitempty"`
		}{
			RobotResponse:   NewRobotResponse(true),
			Recommendations: []string{"codellama:13b", "deepseek-coder:6.7b-instruct"},
		}
		for _, delim := range []string{"\t", "tab"} {
			out, err := toonEncodePureGo(payload, delim)
			if err != nil {
				t.Fatalf("toonEncodePureGo(%q) error = %v", delim, err)
			}
			if strings.Contains(out, `"tab"`) || strings.Contains(out, `13b"tab"`) {
				t.Fatalf("corrupted literal 'tab' delimiter in pure-Go TOON output:\n%s", out)
			}
			if strings.Contains(out, "RobotResponse:") {
				t.Fatalf("embedded RobotResponse was not flattened:\n%s", out)
			}
			if strings.Contains(out, "empty_optional") || strings.Contains(out, "error:") {
				t.Fatalf("omitempty zero fields were not skipped:\n%s", out)
			}
			wantLine := "recommendations[2\t]:\"codellama:13b\"\t\"deepseek-coder:6.7b-instruct\""
			if !strings.Contains(out, wantLine) {
				t.Fatalf("missing expected tab-delimited line %q in output:\n%s", wantLine, out)
			}
			if !strings.Contains(out, "success: true\n") {
				t.Fatalf("missing flattened success: true in output:\n%s", out)
			}
		}
	})

	t.Run("comma and pipe delimiters in tabular structs", func(t *testing.T) {
		type Row struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		rows := []Row{{ID: 1, Name: "Alice"}, {ID: 2, Name: "Bob"}}

		pipeOut, err := toonEncodePureGo(rows, "pipe")
		if err != nil {
			t.Fatalf("pipe encode error = %v", err)
		}
		if !strings.Contains(pipeOut, "[2|]{id|name}:\n 1|Alice\n 2|Bob\n") {
			t.Fatalf("unexpected pipe tabular output:\n%s", pipeOut)
		}

		commaOut, err := toonEncodePureGo(rows, "comma")
		if err != nil {
			t.Fatalf("comma encode error = %v", err)
		}
		if !strings.Contains(commaOut, "[2]{id,name}:\n 1,Alice\n 2,Bob\n") {
			t.Fatalf("unexpected comma tabular output:\n%s", commaOut)
		}
	})

	t.Run("time.Time encodes as RFC3339 string not empty struct", func(t *testing.T) {
		ts := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		out, err := toonEncodePureGo(ts, "\t")
		if err != nil {
			t.Fatalf("time encode error = %v", err)
		}
		if !strings.Contains(out, "2026-11-20T12:00:00Z") {
			t.Fatalf("expected RFC3339 timestamp, got:\n%s", out)
		}
	})

	t.Run("tab fallback to comma when value contains literal tab", func(t *testing.T) {
		rows := []map[string]string{
			{"desc": "has\ttab", "name": "Alice"},
			{"desc": "clean", "name": "Bob"},
		}
		out, err := toonEncodePureGo(rows, "\t")
		if err != nil {
			t.Fatalf("encode error = %v", err)
		}
		if !strings.Contains(out, "[2]{desc,name}:\n") {
			t.Fatalf("expected tab-unsafe tabular array to fall back to comma header, got:\n%s", out)
		}
	})
}

// TestMR3_ToonEncodePureGo_MetamorphicInvariants proves delimiter-keyword
// equivalence ("tab" == "\t", "comma" == ",", "pipe" == "|"), embedded
// RobotResponse flattening, and absence of literal keyword delimiter corruption.
func TestMR3_ToonEncodePureGo_MetamorphicInvariants(t *testing.T) {
	t.Parallel()

	type item struct {
		ID    int    `json:"id"`
		Label string `json:"label"`
	}
	payload := struct {
		RobotResponse
		Items []item   `json:"items"`
		Tags  []string `json:"tags"`
	}{
		RobotResponse: NewRobotResponse(true),
		Items:         []item{{ID: 1, Label: "alpha"}, {ID: 2, Label: "beta:v2"}},
		Tags:          []string{"x:1", "y:2", "z"},
	}

	pairs := [][2]string{
		{"tab", "\t"},
		{"comma", ","},
		{"pipe", "|"},
	}
	for _, p := range pairs {
		outA, err := toonEncodePureGo(payload, p[0])
		if err != nil {
			t.Fatalf("toonEncodePureGo(%q) error: %v", p[0], err)
		}
		outB, err := toonEncodePureGo(payload, p[1])
		if err != nil {
			t.Fatalf("toonEncodePureGo(%q) error: %v", p[1], err)
		}
		if outA != outB {
			t.Fatalf("MR-3 keyword equivalence failed (%q vs %q):\n--- %s ---\n%s\n--- %s ---\n%s", p[0], p[1], p[0], outA, p[1], outB)
		}
		if strings.Contains(outA, "RobotResponse:") {
			t.Fatalf("MR-3 flattening failed for %q:\n%s", p[0], outA)
		}
		if p[0] == "tab" && strings.Contains(outA, `"x:1"tab"y:2"`) {
			t.Fatalf("MR-3 literal tab-word corruption:\n%s", outA)
		}
	}
}

// FuzzToonEncodePureGo fuzzes pure-Go TOON encoding over arbitrary keys,
// values, and delimiters to prove panic-freedom and delimiter hygiene.
func FuzzToonEncodePureGo(f *testing.F) {
	f.Add("alpha", "beta:1", "tab")
	f.Add("has\ttab", "has,comma", "\t")
	f.Add("pipe|val", "quote\"val", "pipe")
	f.Add("", "line1\nline2", ",")

	f.Fuzz(func(t *testing.T, a, b, delim string) {
		payload := struct {
			RobotResponse
			Values []string          `json:"values"`
			Meta   map[string]string `json:"meta"`
		}{
			RobotResponse: NewRobotResponse(true),
			Values:        []string{a, b},
			Meta:          map[string]string{"a": a, "b": b},
		}
		out, err := toonEncodePureGo(payload, delim)
		if err != nil {
			t.Fatalf("toonEncodePureGo unexpected error: %v", err)
		}
		if strings.Contains(out, "RobotResponse:") {
			t.Fatalf("embedded RobotResponse leaked into output:\n%s", out)
		}
	})
}
