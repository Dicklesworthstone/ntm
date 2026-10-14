package pipeline

import (
	"sync"
	"testing"
)

// Direct executor users may leave the transport unset. Resolving that default
// is a read operation: simultaneous child lookups must not lazily write the
// shared interface field. Explicitly configured transports keep their identity.
func TestExecutorClientLookupDoesNotMutateDefault(t *testing.T) {
	e := &Executor{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 32; j++ {
				if _, ok := e.tmuxClient().(realTmuxClient); !ok {
					t.Error("unset transport did not resolve to the real client")
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if e.tmux != nil {
		t.Fatal("client lookup mutated the shared executor")
	}

	// Embedding the interface is sufficient here: lookup must return the
	// configured object without invoking any transport methods.
	configured := &struct{ TmuxClient }{}
	e.tmux = configured
	if e.tmuxClient() != configured {
		t.Fatal("client lookup replaced the configured transport")
	}
}
