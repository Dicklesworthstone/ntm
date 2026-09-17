package robot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSemanticReadBufferCapsMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &semanticReadBuffer{cancel: cancel}
	if n, err := buf.Write(make([]byte, semanticReadByteCap)); n != semanticReadByteCap || err != nil {
		t.Fatalf("exact cap: n=%d err=%v", n, err)
	}
	if n, err := buf.Write([]byte("x")); n != 0 || !errors.Is(err, errSemanticReadLimit) {
		t.Fatalf("overflow: n=%d err=%v", n, err)
	}
	if ctx.Err() == nil || !buf.overflow || buf.buffer.Len() != semanticReadByteCap {
		t.Fatal("overflow must cancel the process without growing the buffer")
	}
}

func TestSemanticCommandInheritsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// No child can start: the nonexistent executable would otherwise cause
	// an exec error rather than context.Canceled.
	if raw, err := semanticCommandOutput(ctx, "", "ntm-nonexistent-evidence-command"); raw != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %q, %v", raw, err)
	}
}

func TestSemanticCommandTimeoutAndInheritedPipes(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("subprocess fixture requires /bin/sh")
	}
	for _, script := range []string{
		"sleep 2",          // parent and child keep stdout open past cancellation
		"sleep 2 & exit 0", // parent exits, descendant retains stdout
	} {
		t.Run(script, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			start := time.Now()
			raw, err := semanticCommandOutput(ctx, "", "/bin/sh", "-c", script)
			if err == nil || raw != nil {
				t.Fatalf("incomplete subprocess read reported success: %q, %v", raw, err)
			}
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("inherited pipe defeated deadline: %s", elapsed)
			}
		})
	}
}

func TestSemanticContextCollectorPreservesPartialProgress(t *testing.T) {
	dir := installSemanticSchemaCommands(t)
	t.Setenv("NTM_TEST_BR_JSON", `[{"status":"in_progress","updated_at":"2026-09-17T11:59:00Z"}]`)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	start := time.Now()
	sp, complete := paneSemanticProgressWithContext(ctx, PaneAddr{Session: "test", Window: 2, Pane: 3}, dir, time.Minute, true, now)
	if complete || sp.EvidenceComplete || sp.ClaimsInWindow != 1 || sp.Source != "token" || sp.SuspectedWedge != "" {
		t.Fatalf("one slow source must not discard the other source's work: %+v complete=%v", sp, complete)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("collector did not inherit caller deadline: %s", elapsed)
	}
}

func TestSemanticCommandRejectsOversizedOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("subprocess fixture requires /bin/sh")
	}
	// Shell builtins only; no optional external data generator is needed.
	script := "chunk='" + strings.Repeat("x", 4096) + "'; i=0; while [ $i -lt 2200 ]; do printf '%s' \"$chunk\"; i=$((i+1)); done"
	raw, err := semanticCommandOutput(context.Background(), "", "/bin/sh", "-c", script)
	if raw != nil || !errors.Is(err, errSemanticReadLimit) {
		t.Fatalf("oversized read = %d bytes, %v; want no partial output and limit error", len(raw), err)
	}
}
