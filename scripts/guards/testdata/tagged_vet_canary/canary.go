// Package canary is the tagged-vet guard fixture (bd-xj28x). The untagged
// build must stay green; the tagged test file must fail to compile.
package canary

// Live is referenced by nothing on purpose; the fixture only needs to build.
func Live() int { return 1 }
