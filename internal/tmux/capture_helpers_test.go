//go:build integration

package tmux

import (
	"strings"
	"testing"
	"time"
)

// ============== Optimized Capture Helper Tests ==============

func TestCaptureLineBudgetConstants(t *testing.T) {
	// Verify constants are ordered correctly (increasing line counts)
	if LinesStatusDetection >= LinesHealthCheck {
		t.Errorf("LinesStatusDetection (%d) should be < LinesHealthCheck (%d)",
			LinesStatusDetection, LinesHealthCheck)
	}
	if LinesHealthCheck >= LinesFullContext {
		t.Errorf("LinesHealthCheck (%d) should be < LinesFullContext (%d)",
			LinesHealthCheck, LinesFullContext)
	}
	if LinesFullContext >= LinesCheckpoint {
		t.Errorf("LinesFullContext (%d) should be < LinesCheckpoint (%d)",
			LinesFullContext, LinesCheckpoint)
	}

	// Verify reasonable ranges
	if LinesStatusDetection < 10 || LinesStatusDetection > 30 {
		t.Errorf("LinesStatusDetection (%d) should be in range 10-30", LinesStatusDetection)
	}
	if LinesHealthCheck < 30 || LinesHealthCheck > 100 {
		t.Errorf("LinesHealthCheck (%d) should be in range 30-100", LinesHealthCheck)
	}
	if LinesFullContext < 200 || LinesFullContext > 1000 {
		t.Errorf("LinesFullContext (%d) should be in range 200-1000", LinesFullContext)
	}
	if LinesCheckpoint < 1000 || LinesCheckpoint > 5000 {
		t.Errorf("LinesCheckpoint (%d) should be in range 1000-5000", LinesCheckpoint)
	}
}

func TestCaptureForStatusDetection(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate output
	SendKeys(target, "echo STATUS_TEST", true)
	time.Sleep(200 * time.Millisecond)

	output, err := CaptureForStatusDetection(target)
	if err != nil {
		t.Fatalf("CaptureForStatusDetection failed: %v", err)
	}

	if !strings.Contains(output, "STATUS_TEST") {
		t.Errorf("CaptureForStatusDetection should capture recent output")
	}
}

func TestCaptureForHealthCheck(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate output
	SendKeys(target, "echo HEALTH_TEST", true)
	time.Sleep(200 * time.Millisecond)

	output, err := DefaultClient.CaptureForHealthCheck(target)
	if err != nil {
		t.Fatalf("CaptureForHealthCheck failed: %v", err)
	}

	if !strings.Contains(output, "HEALTH_TEST") {
		t.Errorf("CaptureForHealthCheck should capture health output")
	}
}

func TestCaptureForFullContext(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate some output
	SendKeys(target, "echo CONTEXT_TEST", true)
	time.Sleep(200 * time.Millisecond)

	output, err := DefaultClient.CaptureForFullContext(target)
	if err != nil {
		t.Fatalf("CaptureForFullContext failed: %v", err)
	}

	if !strings.Contains(output, "CONTEXT_TEST") {
		t.Errorf("CaptureForFullContext should capture full context")
	}
}

func TestCaptureForCheckpoint(t *testing.T) {
	skipIfNoTmux(t)

	session := createTestSession(t)
	panes, _ := GetPanes(session)
	target := panes[0].ID

	// Generate output
	SendKeys(target, "echo CHECKPOINT_TEST", true)
	time.Sleep(200 * time.Millisecond)

	output, err := DefaultClient.CaptureForCheckpoint(target)
	if err != nil {
		t.Fatalf("CaptureForCheckpoint failed: %v", err)
	}

	if !strings.Contains(output, "CHECKPOINT_TEST") {
		t.Errorf("CaptureForCheckpoint should capture checkpoint output")
	}
}
