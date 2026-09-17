package context

import (
	"encoding/json"
	"encoding/xml"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestJSONBudgetBoundaries(t *testing.T) {
	quoted, _ := json.Marshal(strings.Repeat("<&\"日本語🙂\n", 100))
	object, _ := json.Marshal(map[string]string{"a": strings.Repeat("large", 100), "z": "small"})
	array, _ := json.Marshal([]string{strings.Repeat("x", 100), "second", "third"})
	for _, data := range []json.RawMessage{quoted, object, array, json.RawMessage(`invalid`), json.RawMessage(`123456789`), json.RawMessage(`true`), json.RawMessage(`null`)} {
		for budget := -1; budget < 260; budget++ {
			got := truncateJSON(data, budget)
			if len(got) > tokenByteBudget(budget) {
				t.Fatalf("budget %d: %d bytes for %s", budget, len(got), data)
			}
			if budget > 0 && (!json.Valid(got) || !utf8.Valid(got)) {
				t.Fatalf("invalid output at budget %d: %q", budget, got)
			}
		}
	}
	if tokenByteBudget(math.MaxInt) != math.MaxInt {
		t.Fatal("budget multiplication overflow")
	}
}

func TestJSONBudgetDeterministicAndKeepsSmallFields(t *testing.T) {
	data, _ := json.Marshal(map[string]string{"a": strings.Repeat("x", 1000), "b": "keep", "c": "also"})
	want := string(truncateJSON(data, 20))
	if !strings.Contains(want, `"b":"keep"`) || !strings.Contains(want, `"_truncated":true`) {
		t.Fatalf("large first field hid useful fields: %s", want)
	}
	for i := 0; i < 100; i++ {
		if got := string(truncateJSON(data, 20)); got != want {
			t.Fatalf("nondeterministic truncation: %s != %s", got, want)
		}
	}
}

func TestTextBudgetBoundaries(t *testing.T) {
	text := strings.Repeat("日本語🙂\n", 50)
	for budget := 0; budget < len(text); budget++ {
		got := truncateTextBytes(text, budget)
		if len(got) > budget || !utf8.ValidString(got) {
			t.Fatalf("invalid text at byte budget %d: %q", budget, got)
		}
	}
}

func TestXMLTextRoundTrip(t *testing.T) {
	if got := escapeXMLText(`{"source":"ms"}`); got != `{"source":"ms"}` {
		t.Fatalf("XML element data needlessly escaped JSON quotes: %s", got)
	}
	text := "<&>\"' 日本語🙂\n</context_pack><injected/>"
	var got string
	if err := xml.Unmarshal([]byte("<root>"+escapeXMLText(text)+"</root>"), &got); err != nil || got != text {
		t.Fatalf("XML text did not round-trip: %q, %v", got, err)
	}
}
