// Package shellword reads literal shell words without executing or expanding
// them. Source offsets let callers change one argument without rewriting the
// quoting of the arguments that must remain unchanged.
package shellword

import (
	"errors"
	"strings"
)

// Word is a decoded literal argument and its half-open byte span in the source.
type Word struct {
	Value      string
	Start, End int
}

// Literal accepts a single simple command consisting entirely of literal words.
// Shell operators, expansions, comments, globbing and incomplete quoting fail
// closed. This is deliberately not a parser for arbitrary shell programs.
func Literal(command string) ([]Word, error) {
	var words []Word
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	wordStart := 0
	for i, r := range command {
		if r < ' ' && r != '\t' || r == 127 {
			return nil, errors.New("shell control sequence is present")
		}
		if !started && r != ' ' && r != '\t' {
			wordStart = i
		}
		if escaped {
			if quote == '"' && !strings.ContainsRune("$`\"\\", r) {
				word.WriteRune('\\')
			}
			word.WriteRune(r)
			escaped, started = false, true
			continue
		}
		if quote == '\'' {
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '$' || r == '`' {
			return nil, errors.New("shell expansion is present")
		}
		if r == '\\' {
			escaped, started = true, true
			continue
		}
		if quote == '"' {
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
			continue
		}
		if strings.ContainsRune(";&|<>()#*?[]{}~", r) {
			return nil, errors.New("shell operator, expansion, or comment is present")
		}
		if r == ' ' || r == '\t' {
			if started {
				words = append(words, Word{Value: word.String(), Start: wordStart, End: i})
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("shell quoting is incomplete")
	}
	if started {
		words = append(words, Word{Value: word.String(), Start: wordStart, End: len(command)})
	}
	return words, nil
}
