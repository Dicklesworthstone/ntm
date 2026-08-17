package config

import (
	"fmt"
	"reflect"
	"sync"
)

// WS0-G2 config-key liveness claims (bd-ws0-guards-klz98.3).
//
// A knob with no reader must not merge silently. Every leaf key of the parsed
// Config struct must be CLAIMED by the code that actually reads it:
//
//	func init() {
//		config.RegisterReader("tmux.palette_key", palette.HandleKey)
//	}
//
// The second argument is a REAL function reference from the consuming package
// (compile-checked and greppable — not a comment or string annotation).
// TestConfigKeyLiveness (liveness_test.go) reflection-walks the Config
// struct's leaf keys and fails, naming the key, for any key that is neither
// claimed here nor allowlisted in ci/allowlists/config.txt (bead-first waiver
// protocol; both-direction ratchet).
//
// Blind spot, stated honestly: a claim proves a reader EXISTS, not that the
// value changes behavior. The WS6 acceptance bar compensates — each wired
// knob ships a behavior test that flips the knob and observes the change.

var (
	readerMu       sync.Mutex
	readerRegistry = map[string]any{}
)

// RegisterReader claims a config leaf key (dotted path, e.g. "swarm.enabled")
// on behalf of reader, which must be a non-nil function from the consuming
// package. It panics on misuse so a bad claim fails at init time, loudly.
func RegisterReader(key string, reader any) {
	if key == "" {
		panic("config.RegisterReader: empty key")
	}
	v := reflect.ValueOf(reader)
	if !v.IsValid() || v.Kind() != reflect.Func || v.IsNil() {
		panic(fmt.Sprintf("config.RegisterReader(%q): reader must be a non-nil function reference, got %T", key, reader))
	}
	readerMu.Lock()
	defer readerMu.Unlock()
	readerRegistry[key] = reader
}
