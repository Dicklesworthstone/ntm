// Package canary is the G6 contracts-lint canary fixture (bd-ws0-guards-klz98.7).
// It intentionally re-implements the label-split and manifest-writer contracts
// with stray raw literals; scripts/guards/contracts_lint.sh must fire on this
// directory before every real scan or it aborts as a placebo guard.
// Living under testdata/, the Go toolchain ignores this file.
package canary

import "strings"

// extractCanaryLabel is a stray label split on a raw "__" literal.
func extractCanaryLabel(name string) string {
	if idx := strings.Index(name, "__"); idx > 0 {
		return name[:idx]
	}
	return name
}

// makeCanaryLabel is a stray label join on a raw "--" literal.
func makeCanaryLabel(base, label string) string {
	return base + "--" + label
}

type spawnManifestShadow struct{ Session string }

// writeCanaryManifest duplicates the manifest-writer contract.
func writeCanaryManifest() any {
	m := resilience.SpawnManifest{Session: "canary"}
	_ = spawnManifestShadow{Session: "canary"}
	return m
}
