package redaction

import (
	"reflect"
	"strings"
	"testing"
)

// Baseline performance numbers (AMD EPYC 7282, Go 1.25+):
//
// BenchmarkScanAndRedact_Short_NoSecrets-64            	    2904	   417153 ns/op
// BenchmarkScanAndRedact_Short_WithSecrets-64          	    2820	   392943 ns/op
// BenchmarkScanAndRedact_Medium_NoSecrets-64           	      42	 28265091 ns/op
// BenchmarkScanAndRedact_Medium_WithSecrets-64         	      69	 16879640 ns/op
// BenchmarkScanAndRedact_Large_NoSecrets-64            	       4	299269473 ns/op
// BenchmarkScanAndRedact_Large_WithSecrets-64          	       5	217843371 ns/op
// BenchmarkScanAndRedact_Adversarial_ReDoS-64          	    2786	   441854 ns/op
// BenchmarkScanAndRedact_Adversarial_ManyMatches-64    	     867	  1366036 ns/op
// BenchmarkModeComparison/Off-64                       	65845705	      16.70 ns/op
// BenchmarkModeComparison/Warn-64                      	   36267	    32094 ns/op
// BenchmarkModeComparison/Redact-64                    	   36717	    32564 ns/op
// BenchmarkModeComparison/Block-64                     	   37938	    33853 ns/op
//
// A significant regression (>3x slowdown) warrants investigation.

// ----------------------------------------------------------------------------
// Size-based benchmarks
// ----------------------------------------------------------------------------

// BenchmarkScanAndRedact_Short_NoSecrets measures performance on small inputs (1KB)
// with no secrets - the happy path.
func BenchmarkScanAndRedact_Short_NoSecrets(b *testing.B) {
	ResetPatterns()
	// 1KB of plain text without secrets.
	input := strings.Repeat("Hello world, this is a test message. ", 30) // ~1110 bytes
	if len(input) > 1024 {
		input = input[:1024]
	}

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Short_WithSecrets measures performance on small inputs (1KB)
// with embedded secrets.
func BenchmarkScanAndRedact_Short_WithSecrets(b *testing.B) {
	ResetPatterns()
	// 1KB with embedded secrets.
	secret := "gh" + "p_" + strings.Repeat("x", 40)
	base := "API token: " + secret + " used here. "
	input := strings.Repeat(base, 20)
	if len(input) > 1024 {
		input = input[:1024]
	}

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Medium_NoSecrets measures performance on medium inputs (50KB)
// without secrets.
func BenchmarkScanAndRedact_Medium_NoSecrets(b *testing.B) {
	ResetPatterns()
	// 50KB of plain text.
	input := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 1000)
	if len(input) > 50*1024 {
		input = input[:50*1024]
	}

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Medium_WithSecrets measures performance on medium inputs (50KB)
// with scattered secrets.
func BenchmarkScanAndRedact_Medium_WithSecrets(b *testing.B) {
	ResetPatterns()
	// 50KB with secrets scattered throughout.
	secret := "gh" + "p_" + strings.Repeat("y", 40)
	cleanText := strings.Repeat("Normal text without secrets. ", 100)
	secretText := "Token: " + secret + " "
	// Insert secrets at ~10 positions.
	var builder strings.Builder
	for i := 0; i < 10; i++ {
		builder.WriteString(cleanText)
		builder.WriteString(secretText)
	}
	input := builder.String()
	if len(input) > 50*1024 {
		input = input[:50*1024]
	}

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Large_NoSecrets measures performance on large inputs (500KB)
// without secrets.
func BenchmarkScanAndRedact_Large_NoSecrets(b *testing.B) {
	ResetPatterns()
	// 500KB of plain text.
	input := strings.Repeat("This is a long document with no sensitive content whatsoever. ", 8500)
	input = input[:500*1024]

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Large_WithSecrets measures performance on large inputs (500KB)
// with secrets.
func BenchmarkScanAndRedact_Large_WithSecrets(b *testing.B) {
	ResetPatterns()
	// 500KB with secrets scattered throughout.
	ghToken := "gh" + "p_" + strings.Repeat("z", 40)
	awsKey := "AKIA" + strings.Repeat("A", 16)
	cleanText := strings.Repeat("Normal application output with logs. ", 200)
	secretText := "gh=" + ghToken + " aws=" + awsKey + " "

	var builder strings.Builder
	for i := 0; i < 50; i++ {
		builder.WriteString(cleanText)
		builder.WriteString(secretText)
	}
	input := builder.String()
	if len(input) > 500*1024 {
		input = input[:500*1024]
	}

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// ----------------------------------------------------------------------------
// Adversarial benchmarks (ReDoS resistance)
// ----------------------------------------------------------------------------

// BenchmarkScanAndRedact_Adversarial_ReDoS tests regex performance with
// pathological input patterns that could cause catastrophic backtracking
// in naive regex implementations.
func BenchmarkScanAndRedact_Adversarial_ReDoS(b *testing.B) {
	ResetPatterns()
	// Patterns that could cause backtracking in poorly written regexes:
	// - Long sequences of 'a' followed by something that almost matches.
	// - Near-matches that require the regex engine to retry many positions.
	input := strings.Repeat("a", 1000) + "password=" + strings.Repeat("a", 100)

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Adversarial_ManyMatches tests performance when
// there are many overlapping potential matches.
func BenchmarkScanAndRedact_Adversarial_ManyMatches(b *testing.B) {
	ResetPatterns()
	// Many password-like patterns that all match.
	var builder strings.Builder
	for i := 0; i < 100; i++ {
		builder.WriteString("password=secret")
		builder.WriteString(strings.Repeat("x", 10))
		builder.WriteString(" ")
	}
	input := builder.String()

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// BenchmarkScanAndRedact_Adversarial_NearMisses tests performance with
// input that looks like secrets but just misses the pattern.
func BenchmarkScanAndRedact_Adversarial_NearMisses(b *testing.B) {
	ResetPatterns()
	// Near-misses: patterns that almost match but don't quite.
	// ghp_ followed by too few characters.
	nearMisses := []string{
		"ghp_short",      // too short
		"ghp_" + "x",     // way too short
		"AKIA" + "SHORT", // too short
		"sk-" + "short",  // too short
		"password=",      // empty value
		"api_key=short",  // too short
		"Bearer x",       // too short
		"eyJhbGci",       // incomplete JWT
	}
	input := strings.Join(nearMisses, " ")
	input = strings.Repeat(input+" ", 100)

	cfg := Config{Mode: ModeRedact}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanAndRedact(input, cfg)
	}
}

// ----------------------------------------------------------------------------
// Mode comparison benchmarks
// ----------------------------------------------------------------------------

// BenchmarkModeComparison compares performance across different modes.
func BenchmarkModeComparison(b *testing.B) {
	ResetPatterns()
	secret := "gh" + "p_" + strings.Repeat("a", 40)
	input := "Normal text. Token: " + secret + " More text."

	modes := []struct {
		name string
		mode Mode
	}{
		{"Off", ModeOff},
		{"Warn", ModeWarn},
		{"Redact", ModeRedact},
		{"Block", ModeBlock},
	}

	for _, m := range modes {
		b.Run(m.name, func(b *testing.B) {
			cfg := Config{Mode: m.mode}
			for i := 0; i < b.N; i++ {
				_ = ScanAndRedact(input, cfg)
			}
		})
	}
}

// BenchmarkAllowlist compares performance with and without allowlist.
func BenchmarkAllowlist(b *testing.B) {
	ResetPatterns()
	secret := "gh" + "p_" + strings.Repeat("b", 40)
	input := "Token: " + secret + " used for testing."

	b.Run("NoAllowlist", func(b *testing.B) {
		cfg := Config{Mode: ModeRedact}
		for i := 0; i < b.N; i++ {
			_ = ScanAndRedact(input, cfg)
		}
	})

	b.Run("WithAllowlist", func(b *testing.B) {
		cfg := Config{
			Mode:      ModeRedact,
			Allowlist: []string{secret},
		}
		for i := 0; i < b.N; i++ {
			_ = ScanAndRedact(input, cfg)
		}
	})
}

// BenchmarkPatternCount measures overhead of pattern matching.
func BenchmarkPatternCount(b *testing.B) {
	ResetPatterns()
	input := strings.Repeat("Normal text without any matches. ", 100)

	b.Run("AllPatterns", func(b *testing.B) {
		cfg := Config{Mode: ModeRedact}
		for i := 0; i < b.N; i++ {
			_ = ScanAndRedact(input, cfg)
		}
	})

	b.Run("DisabledMostPatterns", func(b *testing.B) {
		cfg := Config{
			Mode: ModeRedact,
			DisabledCategories: []Category{
				CategoryOpenAIKey,
				CategoryAnthropicKey,
				CategoryGitHubToken,
				CategoryAWSAccessKey,
				CategoryAWSSecretKey,
				CategoryJWT,
				CategoryBearerToken,
				CategoryPrivateKey,
				CategoryDatabaseURL,
				CategoryGenericAPIKey,
				CategoryGenericSecret,
			},
		}
		for i := 0; i < b.N; i++ {
			_ = ScanAndRedact(input, cfg)
		}
	})
}

var dedupBenchmarkResult []match

// BenchmarkDeduplicateMatchesPaired runs the incumbent and coverage index in the
// same invocation. The sparse case exposes fixed overhead; dense and surrounding
// credentials exercise the formerly quadratic work. Both include the same input
// copy because each implementation sorts its candidates in place.
func BenchmarkDeduplicateMatchesPaired(b *testing.B) {
	fixtures := []struct {
		name    string
		matches []match
	}{
		{"Sparse8", dedupBenchmarkDisjoint(8, 128, false)},
		{"Dense1024", dedupBenchmarkDisjoint(1024, 1, false)},
		{"Dense8192", dedupBenchmarkDisjoint(8192, 1, false)},
		{"Interleaved8192", dedupBenchmarkDisjoint(8192, 1, true)},
		{"Residual512", dedupBenchmarkSurrounding(512, 1)},
		{"RepeatedSurrounding128x16", dedupBenchmarkSurrounding(128, 16)},
	}
	implementations := []struct {
		name string
		run  func([]match) []match
	}{
		{"Incumbent", deduplicateMatchesIncumbent},
		{"Coverage", deduplicateMatches},
	}
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			want := deduplicateMatchesIncumbent(cloneDedupMatches(fixture.matches))
			got := deduplicateMatches(cloneDedupMatches(fixture.matches))
			if !reflect.DeepEqual(got, want) {
				b.Fatal("candidate changed the incumbent's findings or metadata")
			}
			for _, implementation := range implementations {
				b.Run(implementation.name, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						dedupBenchmarkResult = implementation.run(cloneDedupMatches(fixture.matches))
					}
				})
			}
		})
	}
}

func dedupBenchmarkDisjoint(count, gap int, interleaved bool) []match {
	token := "ghp_" + strings.Repeat("a", 40)
	unit := token + strings.Repeat(" ", gap)
	source := strings.Repeat(unit, count)
	matches := make([]match, count)
	for i := range matches {
		position := i
		priority := 100
		if interleaved {
			position = count - 1 - i
			priority -= (i % 4) * 10
		}
		start := position * len(unit)
		matches[i] = match{
			category: CategoryGitHubToken,
			match:    source[start : start+len(token)],
			start:    start,
			end:      start + len(token),
			priority: priority,
		}
	}
	return matches
}

func dedupBenchmarkSurrounding(providers, wrappers int) []match {
	token := "ghp_" + strings.Repeat("b", 40)
	prefix := "api_token=\""
	unit := "left" + token + "right"
	source := prefix + strings.Repeat(unit, providers) + "\""
	matches := make([]match, 0, providers+wrappers)
	for i := 0; i < providers; i++ {
		start := len(prefix) + i*len(unit) + len("left")
		matches = append(matches, match{
			category: CategoryGitHubToken,
			match:    source[start : start+len(token)],
			start:    start,
			end:      start + len(token),
			priority: 100,
		})
	}
	for i := 0; i < wrappers; i++ {
		matches = append(matches, match{
			category:          CategoryGenericAPIKey,
			match:             source,
			start:             0,
			end:               len(source),
			priority:          50 - i,
			valueStart:        len(prefix),
			valueEnd:          len(source) - 1,
			allowlistContexts: []string{"surrounding assignment"},
		})
	}
	return matches
}
