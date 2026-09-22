package shellword

import (
	"reflect"
	"testing"
)

func TestLiteralPreservesDecodedWordsAndSourceSpans(t *testing.T) {
	command := "MODE='literal value' /opt/agent\\ bin/claude\t--model='mødel' \"literal\\q\" '' 'a'\\''b'"
	words, err := Literal(command)
	if err != nil {
		t.Fatal(err)
	}
	wantValues := []string{"MODE=literal value", "/opt/agent bin/claude", "--model=mødel", `literal\q`, "", "a'b"}
	wantSource := []string{"MODE='literal value'", `/opt/agent\ bin/claude`, "--model='mødel'", `"literal\q"`, "''", `'a'\''b'`}
	var values, sources []string
	for _, word := range words {
		values = append(values, word.Value)
		sources = append(sources, command[word.Start:word.End])
	}
	if !reflect.DeepEqual(values, wantValues) || !reflect.DeepEqual(sources, wantSource) {
		t.Fatalf("literal words changed: values=%q sources=%q", values, sources)
	}
}

func TestLiteralRejectsShellProgramsAndExpansions(t *testing.T) {
	for _, command := range []string{
		"a;b", "a && b", "a | b", "a > b", "a #comment", "a $VALUE", "a `date`", "a $(date)",
		"a *", "a ?", "a [ab]", "a {a,b}", "a ~", "a 'incomplete", "a \\", "a\x00b", "'a\nb'", "a\\\nb",
	} {
		if _, err := Literal(command); err == nil {
			t.Errorf("nonliteral program accepted: %q", command)
		}
	}
	if _, err := Literal(`a 'literal $VALUE; * ? ~' "literal;*" escaped\ space`); err != nil {
		t.Fatalf("quoted or escaped literals rejected: %v", err)
	}
}
