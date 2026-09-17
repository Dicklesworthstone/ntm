package robot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// Bound memory as well as runtime: even a capped git log may contain enormous
// commit bodies, and a br wrapper can accidentally emit unbounded diagnostics.
const semanticReadByteCap = 8 << 20

var errSemanticReadLimit = errors.New("semantic evidence exceeds output limit")

// Do not embed bytes.Buffer: its promoted ReadFrom would let io.Copy bypass
// Write and defeat the cap on the real exec path.
type semanticReadBuffer struct {
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (b *semanticReadBuffer) Write(p []byte) (int, error) {
	if len(p) > semanticReadByteCap-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, errSemanticReadLimit
	}
	return b.buffer.Write(p)
}

// semanticCommandOutput never returns partial output as a successful read.
// WaitDelay also bounds waits for inherited pipe descriptors after a wrapper
// exits or is killed, which CommandContext alone does not guarantee.
func semanticCommandOutput(parent context.Context, dir, name string, args ...string) ([]byte, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, semanticReadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := &semanticReadBuffer{cancel: cancel}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	err := cmd.Run()
	if out.overflow {
		return nil, errSemanticReadLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return out.buffer.Bytes(), nil
}
