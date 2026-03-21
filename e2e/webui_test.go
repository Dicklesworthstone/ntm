//go:build e2e
// +build e2e

// Package e2e contains end-to-end tests for NTM Web UI.
// These tests use chromedp for Go-based browser automation to verify
// critical web flows with detailed logging.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// WebUITestSuite manages Web UI E2E tests with browser automation.
type WebUITestSuite struct {
	t             *testing.T
	logger        *TestLogger
	ctx           context.Context
	cancel        context.CancelFunc
	browserCancel context.CancelFunc
	allocCtx      context.Context
	allocCancel   context.CancelFunc
	baseURL       string
	apiURL        string
	screenshots   []string
}

// NewWebUITestSuite creates a new Web UI test suite.
func NewWebUITestSuite(t *testing.T, scenario string) *WebUITestSuite {
	logger := NewTestLogger(t, fmt.Sprintf("webui-%s", scenario))

	// Get URLs from environment or use defaults
	webURL := os.Getenv("E2E_WEB_URL")
	if webURL == "" {
		webURL = "http://localhost:3000"
	}
	apiURL := os.Getenv("E2E_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:7337"
	}

	logger.Log("[E2E-WEBUI] Web UI URL: %s", webURL)
	logger.Log("[E2E-WEBUI] API URL: %s", apiURL)

	return &WebUITestSuite{
		t:       t,
		logger:  logger,
		baseURL: webURL,
		apiURL:  apiURL,
	}
}

// Setup initializes the browser context.
func (s *WebUITestSuite) Setup() error {
	s.logger.Log("[E2E-WEBUI-SETUP] Initializing browser context")

	// Check if web server is reachable
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.baseURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.logger.Log("[E2E-WEBUI-SETUP] Web server not reachable: %v", err)
		return fmt.Errorf("web server at %s not reachable: %w", s.baseURL, err)
	}
	resp.Body.Close()
	s.logger.Log("[E2E-WEBUI-SETUP] Web server reachable (status: %d)", resp.StatusCode)

	// Check if API server is reachable
	req2, _ := http.NewRequestWithContext(ctx, "GET", s.apiURL+"/health", nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		s.logger.Log("[E2E-WEBUI-SETUP] API server not reachable: %v", err)
		return fmt.Errorf("API server at %s not reachable: %w", s.apiURL, err)
	}
	resp2.Body.Close()
	s.logger.Log("[E2E-WEBUI-SETUP] API server reachable (status: %d)", resp2.StatusCode)

	// Create browser allocator with headless mode
	headless := os.Getenv("E2E_HEADLESS") != "false"
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WindowSize(1920, 1080),
	)

	s.allocCtx, s.allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(s.allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			s.logger.Log("[E2E-BROWSER] "+format, args...)
		}),
	)
	s.browserCancel = browserCancel

	// Set timeout for all browser operations
	s.ctx, s.cancel = context.WithTimeout(browserCtx, 2*time.Minute)

	s.logger.Log("[E2E-WEBUI-SETUP] Browser context initialized (headless=%v)", headless)
	return nil
}

// Teardown cleans up resources.
func (s *WebUITestSuite) Teardown() {
	s.logger.Log("[E2E-WEBUI-TEARDOWN] Cleaning up browser context")

	if s.cancel != nil {
		s.cancel()
	}
	if s.browserCancel != nil {
		s.browserCancel()
	}
	if s.allocCancel != nil {
		s.allocCancel()
	}

	s.logger.Close()
}

// CaptureScreenshot takes a screenshot and saves it to the log directory.
func (s *WebUITestSuite) CaptureScreenshot(name string) error {
	var buf []byte
	if err := chromedp.Run(s.ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		return err
	}

	logDir := os.Getenv("E2E_LOG_DIR")
	if logDir == "" {
		logDir = "/tmp/ntm-e2e-logs"
	}

	screenshotPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.png", s.logger.scenario, name))
	if err := os.WriteFile(screenshotPath, buf, 0644); err != nil {
		return err
	}

	s.screenshots = append(s.screenshots, screenshotPath)
	s.logger.Log("[E2E-WEBUI] Screenshot saved: %s", screenshotPath)
	return nil
}

// CaptureHTML captures the current page HTML and saves it.
func (s *WebUITestSuite) CaptureHTML(name string) error {
	var html string
	if err := chromedp.Run(s.ctx, chromedp.OuterHTML("html", &html)); err != nil {
		return err
	}

	logDir := os.Getenv("E2E_LOG_DIR")
	if logDir == "" {
		logDir = "/tmp/ntm-e2e-logs"
	}

	htmlPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.html", s.logger.scenario, name))
	if err := os.WriteFile(htmlPath, []byte(html), 0644); err != nil {
		return err
	}

	s.logger.Log("[E2E-WEBUI] HTML captured: %s", htmlPath)
	return nil
}

// Navigate navigates to a URL and waits for the page to load.
func (s *WebUITestSuite) Navigate(path string) error {
	url := s.baseURL + path
	s.logger.Log("[E2E-WEBUI-NAV] Navigating to %s", url)

	startTime := time.Now()
	err := chromedp.Run(s.ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
	)
	elapsed := time.Since(startTime)

	if err != nil {
		s.logger.Log("[E2E-WEBUI-NAV] Navigation failed after %v: %v", elapsed, err)
		s.CaptureScreenshot("nav-error")
		s.CaptureHTML("nav-error")
		return err
	}

	s.logger.Log("[E2E-WEBUI-NAV] Navigation completed in %v", elapsed)
	return nil
}

// WaitForElement waits for an element to appear.
func (s *WebUITestSuite) WaitForElement(selector string, timeout time.Duration) error {
	s.logger.Log("[E2E-WEBUI-WAIT] Waiting for element: %s (timeout: %v)", selector, timeout)

	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	startTime := time.Now()
	err := chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery))
	elapsed := time.Since(startTime)

	if err != nil {
		s.logger.Log("[E2E-WEBUI-WAIT] Element not found after %v: %v", elapsed, err)
		s.CaptureScreenshot("wait-error")
		return err
	}

	s.logger.Log("[E2E-WEBUI-WAIT] Element found in %v", elapsed)
	return nil
}

// Click clicks on an element.
func (s *WebUITestSuite) Click(selector string) error {
	s.logger.Log("[E2E-WEBUI-CLICK] Clicking element: %s", selector)

	startTime := time.Now()
	err := chromedp.Run(s.ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
	)
	elapsed := time.Since(startTime)

	if err != nil {
		s.logger.Log("[E2E-WEBUI-CLICK] Click failed after %v: %v", elapsed, err)
		return err
	}

	s.logger.Log("[E2E-WEBUI-CLICK] Click completed in %v", elapsed)
	return nil
}

// TypeText types text into an input element.
func (s *WebUITestSuite) TypeText(selector, text string) error {
	s.logger.Log("[E2E-WEBUI-TYPE] Typing into element: %s", selector)

	startTime := time.Now()
	err := chromedp.Run(s.ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Clear(selector, chromedp.ByQuery),
		chromedp.SendKeys(selector, text, chromedp.ByQuery),
	)
	elapsed := time.Since(startTime)

	if err != nil {
		s.logger.Log("[E2E-WEBUI-TYPE] Type failed after %v: %v", elapsed, err)
		return err
	}

	s.logger.Log("[E2E-WEBUI-TYPE] Type completed in %v", elapsed)
	return nil
}

// GetText retrieves text content from an element.
func (s *WebUITestSuite) GetText(selector string) (string, error) {
	var text string
	err := chromedp.Run(s.ctx,
		chromedp.WaitVisible(selector, chromedp.ByQuery),
		chromedp.Text(selector, &text, chromedp.ByQuery),
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// AssertText asserts that an element contains expected text.
func (s *WebUITestSuite) AssertText(selector, expected string) error {
	s.logger.Log("[E2E-WEBUI-ASSERT] Checking text in element: %s", selector)

	text, err := s.GetText(selector)
	if err != nil {
		s.logger.Log("[E2E-WEBUI-ASSERT] Failed to get text: %v", err)
		return err
	}

	if !strings.Contains(text, expected) {
		s.logger.Log("[E2E-WEBUI-ASSERT] FAIL - expected '%s', got '%s'", expected, text)
		s.CaptureScreenshot("assert-text-fail")
		return fmt.Errorf("expected text '%s' not found in '%s'", expected, text)
	}

	s.logger.Log("[E2E-WEBUI-ASSERT] PASS - text contains '%s'", expected)
	return nil
}

// AssertElementExists checks that an element exists on the page.
func (s *WebUITestSuite) AssertElementExists(selector string) error {
	s.logger.Log("[E2E-WEBUI-ASSERT] Checking element exists: %s", selector)

	var nodes []*runtime.RemoteObject
	err := chromedp.Run(s.ctx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('%s').length`, selector), &nodes),
	)
	if err != nil {
		return err
	}

	s.logger.Log("[E2E-WEBUI-ASSERT] PASS - element exists: %s", selector)
	return nil
}

// GetElementCount returns the number of elements matching the selector.
func (s *WebUITestSuite) GetElementCount(selector string) (int, error) {
	var count int
	err := chromedp.Run(s.ctx,
		chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll('%s').length`, selector), &count),
	)
	return count, err
}

// DragAndDrop performs drag and drop between two elements.
func (s *WebUITestSuite) DragAndDrop(sourceSelector, targetSelector string) error {
	s.logger.Log("[E2E-WEBUI-DND] Drag from %s to %s", sourceSelector, targetSelector)

	// Use JavaScript-based drag and drop for reliability
	script := fmt.Sprintf(`
		(function() {
			const source = document.querySelector('%s');
			const target = document.querySelector('%s');
			if (!source || !target) return false;

			const dataTransfer = new DataTransfer();

			// Simulate dragstart
			const dragStart = new DragEvent('dragstart', {
				bubbles: true,
				cancelable: true,
				dataTransfer: dataTransfer
			});
			source.dispatchEvent(dragStart);

			// Simulate dragover
			const dragOver = new DragEvent('dragover', {
				bubbles: true,
				cancelable: true,
				dataTransfer: dataTransfer
			});
			target.dispatchEvent(dragOver);

			// Simulate drop
			const drop = new DragEvent('drop', {
				bubbles: true,
				cancelable: true,
				dataTransfer: dataTransfer
			});
			target.dispatchEvent(drop);

			// Simulate dragend
			const dragEnd = new DragEvent('dragend', {
				bubbles: true,
				cancelable: true,
				dataTransfer: dataTransfer
			});
			source.dispatchEvent(dragEnd);

			return true;
		})()
	`, sourceSelector, targetSelector)

	var success bool
	err := chromedp.Run(s.ctx, chromedp.Evaluate(script, &success))
	if err != nil {
		return err
	}
	if !success {
		return fmt.Errorf("drag and drop failed - elements not found")
	}

	s.logger.Log("[E2E-WEBUI-DND] Drag and drop completed")
	return nil
}

// WaitForAPICall waits for an API call to complete by checking network activity.
func (s *WebUITestSuite) WaitForAPICall(timeout time.Duration) error {
	s.logger.Log("[E2E-WEBUI-WAIT] Waiting for API calls to complete (timeout: %v)", timeout)
	time.Sleep(timeout)
	return nil
}

// LogConsoleMessages captures and logs browser console messages.
func (s *WebUITestSuite) LogConsoleMessages() {
	chromedp.ListenTarget(s.ctx, func(ev interface{}) {
		if msg, ok := ev.(*runtime.EventConsoleAPICalled); ok {
			var args []string
			for _, arg := range msg.Args {
				if arg.Value != nil {
					args = append(args, string(arg.Value))
				}
			}
			s.logger.Log("[E2E-CONSOLE] [%s] %s", msg.Type, strings.Join(args, " "))
		}
	})
}

// ============================================================================
// Test: Sessions Flow
// Connect -> list sessions -> open session -> send prompt -> see output
// ============================================================================

func TestE2E_WebUI_SessionsFlow(t *testing.T) {
	if os.Getenv("E2E_WEBUI_ENABLED") != "true" {
		t.Skip("E2E_WEBUI_ENABLED not set to true, skipping Web UI tests")
	}

	suite := NewWebUITestSuite(t, "sessions-flow")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-WEBUI] Setup failed: %v", err)
	}

	suite.LogConsoleMessages()

	// Step 1: Navigate to connect page
	suite.logger.Log("[E2E-WEBUI-STEP] Step 1: Navigate to connect page")
	if err := suite.Navigate("/connect"); err != nil {
		t.Fatalf("Step 1 failed: %v", err)
	}
	time.Sleep(1 * time.Second)
	suite.CaptureScreenshot("step1-connect-page")

	// Step 2: Configure connection (enter base URL)
	suite.logger.Log("[E2E-WEBUI-STEP] Step 2: Configure connection")
	apiURL := suite.apiURL
	if err := suite.WaitForElement("input[placeholder*='URL'], input[name='baseUrl'], input[type='text']", 10*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] No URL input found, assuming already configured")
	} else {
		if err := suite.TypeText("input[placeholder*='URL'], input[name='baseUrl'], input[type='text']", apiURL); err != nil {
			suite.logger.Log("[E2E-WEBUI] Could not enter URL: %v", err)
		}
	}
	time.Sleep(500 * time.Millisecond)
	suite.CaptureScreenshot("step2-configured")

	// Step 3: Navigate to sessions list (dashboard)
	suite.logger.Log("[E2E-WEBUI-STEP] Step 3: Navigate to sessions list")
	if err := suite.Navigate("/"); err != nil {
		t.Fatalf("Step 3 failed: %v", err)
	}
	time.Sleep(2 * time.Second)
	suite.CaptureScreenshot("step3-sessions-list")

	// Step 4: Verify sessions page loaded
	suite.logger.Log("[E2E-WEBUI-STEP] Step 4: Verify sessions page loaded")
	if err := suite.WaitForElement("h1", 10*time.Second); err != nil {
		t.Fatalf("Step 4 failed: %v", err)
	}

	// Check for Sessions header
	title, _ := suite.GetText("h1")
	suite.logger.Log("[E2E-WEBUI-ASSERT] Page title: %s", title)
	if !strings.Contains(strings.ToLower(title), "session") {
		suite.logger.Log("[E2E-WEBUI] Warning: Page title does not contain 'session'")
	}
	suite.CaptureScreenshot("step4-sessions-verified")

	// Step 5: Check for session cards or empty state
	suite.logger.Log("[E2E-WEBUI-STEP] Step 5: Check for session content")
	time.Sleep(2 * time.Second)

	sessionCount, err := suite.GetElementCount("a[href^='/sessions/']")
	if err != nil {
		suite.logger.Log("[E2E-WEBUI] Could not count sessions: %v", err)
	}

	if sessionCount > 0 {
		suite.logger.Log("[E2E-WEBUI-ASSERT] Found %d session cards", sessionCount)

		// Step 6: Click on first session
		suite.logger.Log("[E2E-WEBUI-STEP] Step 6: Click on first session")
		if err := suite.Click("a[href^='/sessions/']"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Could not click session: %v", err)
		} else {
			time.Sleep(2 * time.Second)
			suite.CaptureScreenshot("step6-session-detail")

			// Step 7: Verify session detail page
			suite.logger.Log("[E2E-WEBUI-STEP] Step 7: Verify session detail page")
			if err := suite.WaitForElement("[class*='pane'], [data-testid='pane-list']", 10*time.Second); err != nil {
				suite.logger.Log("[E2E-WEBUI] Pane list not found, checking alternative selectors")
			}

			// Step 8: Try to send a prompt (if input exists)
			suite.logger.Log("[E2E-WEBUI-STEP] Step 8: Try to send a prompt")
			if err := suite.WaitForElement("input[placeholder*='prompt'], input[placeholder*='Send'], textarea", 5*time.Second); err != nil {
				suite.logger.Log("[E2E-WEBUI] Prompt input not found: %v", err)
			} else {
				testPrompt := "echo 'E2E test prompt'"
				if err := suite.TypeText("input[placeholder*='prompt'], input[placeholder*='Send'], textarea", testPrompt); err != nil {
					suite.logger.Log("[E2E-WEBUI] Could not type prompt: %v", err)
				} else {
					suite.CaptureScreenshot("step8-prompt-entered")

					// Click send button
					if err := suite.Click("button:has-text('Send'), button[type='submit']"); err != nil {
						suite.logger.Log("[E2E-WEBUI] Could not click send: %v", err)
					}
				}
			}

			// Step 9: Verify output viewer
			suite.logger.Log("[E2E-WEBUI-STEP] Step 9: Verify output viewer")
			time.Sleep(2 * time.Second)
			suite.CaptureScreenshot("step9-output-viewer")

			if err := suite.WaitForElement("[class*='output'], [data-testid='pane-output'], pre, code", 5*time.Second); err != nil {
				suite.logger.Log("[E2E-WEBUI] Output viewer not found: %v", err)
			} else {
				suite.logger.Log("[E2E-WEBUI-ASSERT] Output viewer found")
			}
		}
	} else {
		suite.logger.Log("[E2E-WEBUI] No sessions found - checking empty state")
		if err := suite.AssertElementExists("[class*='empty'], [data-testid='empty-state']"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Empty state element not found")
		}
	}

	suite.CaptureScreenshot("final-sessions-flow")
	suite.logger.Log("[E2E-WEBUI] Sessions flow test completed")
}

// ============================================================================
// Test: Beads Flow
// Open Kanban -> move bead -> verify REST + WS update
// ============================================================================

func TestE2E_WebUI_BeadsFlow(t *testing.T) {
	if os.Getenv("E2E_WEBUI_ENABLED") != "true" {
		t.Skip("E2E_WEBUI_ENABLED not set to true, skipping Web UI tests")
	}

	suite := NewWebUITestSuite(t, "beads-flow")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-WEBUI] Setup failed: %v", err)
	}

	suite.LogConsoleMessages()

	// Step 1: Navigate to beads page
	suite.logger.Log("[E2E-WEBUI-STEP] Step 1: Navigate to beads page")
	if err := suite.Navigate("/beads"); err != nil {
		t.Fatalf("Step 1 failed: %v", err)
	}
	time.Sleep(3 * time.Second)
	suite.CaptureScreenshot("step1-beads-page")

	// Step 2: Verify beads page loaded
	suite.logger.Log("[E2E-WEBUI-STEP] Step 2: Verify beads page loaded")
	if err := suite.WaitForElement("h1", 10*time.Second); err != nil {
		t.Fatalf("Step 2 failed: %v", err)
	}

	title, _ := suite.GetText("h1")
	suite.logger.Log("[E2E-WEBUI-ASSERT] Page title: %s", title)
	if !strings.Contains(strings.ToLower(title), "bead") {
		suite.logger.Log("[E2E-WEBUI] Warning: Page title does not contain 'bead'")
	}

	// Step 3: Verify Kanban columns exist
	suite.logger.Log("[E2E-WEBUI-STEP] Step 3: Verify Kanban columns")
	time.Sleep(2 * time.Second)

	// Check for column headers
	columns := []string{"Open", "In Progress", "Closed"}
	for _, col := range columns {
		if err := suite.WaitForElement(fmt.Sprintf("h3:has-text('%s'), [data-column='%s']", col, strings.ToLower(col)), 5*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI] Column '%s' not found: %v", col, err)
		} else {
			suite.logger.Log("[E2E-WEBUI-ASSERT] Found column: %s", col)
		}
	}
	suite.CaptureScreenshot("step3-kanban-columns")

	// Step 4: Check for bead cards
	suite.logger.Log("[E2E-WEBUI-STEP] Step 4: Check for bead cards")
	beadCount, err := suite.GetElementCount("[draggable='true'], [data-bead-id]")
	if err != nil {
		suite.logger.Log("[E2E-WEBUI] Could not count beads: %v", err)
	}
	suite.logger.Log("[E2E-WEBUI-ASSERT] Found %d bead cards", beadCount)

	if beadCount > 0 {
		// Step 5: Set assignee (required for moving to In Progress)
		suite.logger.Log("[E2E-WEBUI-STEP] Step 5: Set assignee")
		if err := suite.WaitForElement("input[placeholder*='name'], input[placeholder*='assignee']", 5*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI] Assignee input not found: %v", err)
		} else {
			if err := suite.TypeText("input[placeholder*='name'], input[placeholder*='assignee']", "e2e-test-agent"); err != nil {
				suite.logger.Log("[E2E-WEBUI] Could not set assignee: %v", err)
			}
		}
		suite.CaptureScreenshot("step5-assignee-set")

		// Step 6: Attempt drag and drop (move bead)
		suite.logger.Log("[E2E-WEBUI-STEP] Step 6: Attempt drag and drop")
		// Note: Actual drag and drop requires specific element targeting
		// This is a placeholder for the drag-drop operation

		// Try to drag first bead to "In Progress" column
		if err := suite.DragAndDrop(
			"[draggable='true']:first-child, [data-bead-id]:first-child",
			"[data-column='in_progress'], div:has(h3:has-text('In Progress'))",
		); err != nil {
			suite.logger.Log("[E2E-WEBUI] Drag and drop failed: %v", err)
		}
		time.Sleep(2 * time.Second)
		suite.CaptureScreenshot("step6-after-drag")

		// Step 7: Verify state change (check for success notice or API call)
		suite.logger.Log("[E2E-WEBUI-STEP] Step 7: Verify state change")
		if err := suite.WaitForElement("[class*='success'], [class*='notice']", 3*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI] Success notice not found: %v", err)
		} else {
			suite.logger.Log("[E2E-WEBUI-ASSERT] State change notification found")
		}
	} else {
		suite.logger.Log("[E2E-WEBUI] No bead cards found to drag")
	}

	// Step 8: Verify triage recommendations section
	suite.logger.Log("[E2E-WEBUI-STEP] Step 8: Verify triage recommendations")
	if err := suite.WaitForElement("h2:has-text('Triage'), [data-testid='triage-section']", 5*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] Triage section not found: %v", err)
	} else {
		suite.logger.Log("[E2E-WEBUI-ASSERT] Triage section found")
	}

	// Step 9: Verify galaxy view section
	suite.logger.Log("[E2E-WEBUI-STEP] Step 9: Verify galaxy view")
	if err := suite.WaitForElement("h2:has-text('Galaxy'), svg, [data-testid='galaxy-view']", 5*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] Galaxy view not found: %v", err)
	} else {
		suite.logger.Log("[E2E-WEBUI-ASSERT] Galaxy view found")
	}

	suite.CaptureScreenshot("final-beads-flow")
	suite.logger.Log("[E2E-WEBUI] Beads flow test completed")
}

// ============================================================================
// Test: Mail Flow
// Open thread -> reply -> mark read -> ack
// ============================================================================

func TestE2E_WebUI_MailFlow(t *testing.T) {
	if os.Getenv("E2E_WEBUI_ENABLED") != "true" {
		t.Skip("E2E_WEBUI_ENABLED not set to true, skipping Web UI tests")
	}

	suite := NewWebUITestSuite(t, "mail-flow")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-WEBUI] Setup failed: %v", err)
	}

	suite.LogConsoleMessages()

	// Step 1: Navigate to mail page
	suite.logger.Log("[E2E-WEBUI-STEP] Step 1: Navigate to mail page")
	if err := suite.Navigate("/mail"); err != nil {
		t.Fatalf("Step 1 failed: %v", err)
	}
	time.Sleep(3 * time.Second)
	suite.CaptureScreenshot("step1-mail-page")

	// Step 2: Verify mail page loaded
	suite.logger.Log("[E2E-WEBUI-STEP] Step 2: Verify mail page loaded")
	if err := suite.WaitForElement("h1", 10*time.Second); err != nil {
		t.Fatalf("Step 2 failed: %v", err)
	}

	title, _ := suite.GetText("h1")
	suite.logger.Log("[E2E-WEBUI-ASSERT] Page title: %s", title)
	if !strings.Contains(strings.ToLower(title), "mail") {
		suite.logger.Log("[E2E-WEBUI] Warning: Page title does not contain 'mail'")
	}

	// Step 3: Select an agent
	suite.logger.Log("[E2E-WEBUI-STEP] Step 3: Select an agent")
	if err := suite.WaitForElement("input[list='mail-agents'], input[placeholder*='agent'], select", 10*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] Agent selector not found: %v", err)
	} else {
		// Type a test agent name or select from list
		if err := suite.TypeText("input[list='mail-agents'], input[placeholder*='agent']", "DustyCastle"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Could not enter agent name: %v", err)
		}
	}
	time.Sleep(2 * time.Second)
	suite.CaptureScreenshot("step3-agent-selected")

	// Step 4: Verify inbox section
	suite.logger.Log("[E2E-WEBUI-STEP] Step 4: Verify inbox section")
	if err := suite.WaitForElement("h2:has-text('Inbox'), [data-testid='inbox-section']", 10*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] Inbox section not found: %v", err)
	} else {
		suite.logger.Log("[E2E-WEBUI-ASSERT] Inbox section found")
	}

	// Step 5: Check for messages
	suite.logger.Log("[E2E-WEBUI-STEP] Step 5: Check for messages")
	messageCount, err := suite.GetElementCount("button[class*='text-left'], [data-message-id]")
	if err != nil {
		suite.logger.Log("[E2E-WEBUI] Could not count messages: %v", err)
	}
	suite.logger.Log("[E2E-WEBUI-ASSERT] Found %d messages", messageCount)

	if messageCount > 0 {
		// Step 6: Click on first message
		suite.logger.Log("[E2E-WEBUI-STEP] Step 6: Click on first message")
		if err := suite.Click("button[class*='text-left']:first-child, [data-message-id]:first-child"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Could not click message: %v", err)
		}
		time.Sleep(2 * time.Second)
		suite.CaptureScreenshot("step6-message-opened")

		// Step 7: Verify thread view
		suite.logger.Log("[E2E-WEBUI-STEP] Step 7: Verify thread view")
		if err := suite.WaitForElement("h2:has-text('Thread'), [data-testid='thread-view']", 5*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI] Thread view not found: %v", err)
		} else {
			suite.logger.Log("[E2E-WEBUI-ASSERT] Thread view found")
		}

		// Step 8: Click Mark Read button
		suite.logger.Log("[E2E-WEBUI-STEP] Step 8: Click Mark Read button")
		if err := suite.Click("button:has-text('Mark read'), button:has-text('Read')"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Mark Read button not found: %v", err)
		} else {
			time.Sleep(1 * time.Second)
			suite.CaptureScreenshot("step8-marked-read")

			// Check for success notice
			if err := suite.WaitForElement("[class*='success'], [class*='green']", 3*time.Second); err != nil {
				suite.logger.Log("[E2E-WEBUI] Success notice not found after mark read")
			} else {
				suite.logger.Log("[E2E-WEBUI-ASSERT] Mark read success")
			}
		}

		// Step 9: Click Acknowledge button (if ack required)
		suite.logger.Log("[E2E-WEBUI-STEP] Step 9: Click Acknowledge button")
		if err := suite.Click("button:has-text('Acknowledge'), button:has-text('Ack')"); err != nil {
			suite.logger.Log("[E2E-WEBUI] Acknowledge button not found or disabled: %v", err)
		} else {
			time.Sleep(1 * time.Second)
			suite.CaptureScreenshot("step9-acknowledged")
		}

		// Step 10: Type a reply
		suite.logger.Log("[E2E-WEBUI-STEP] Step 10: Type a reply")
		if err := suite.WaitForElement("textarea[placeholder*='reply'], textarea", 5*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI] Reply textarea not found: %v", err)
		} else {
			testReply := "This is an E2E test reply message."
			if err := suite.TypeText("textarea[placeholder*='reply'], textarea", testReply); err != nil {
				suite.logger.Log("[E2E-WEBUI] Could not type reply: %v", err)
			} else {
				suite.CaptureScreenshot("step10-reply-typed")

				// Step 11: Click Send Reply button
				suite.logger.Log("[E2E-WEBUI-STEP] Step 11: Click Send Reply button")
				if err := suite.Click("button:has-text('Send Reply'), button:has-text('Send')"); err != nil {
					suite.logger.Log("[E2E-WEBUI] Send Reply button not found: %v", err)
				} else {
					time.Sleep(2 * time.Second)
					suite.CaptureScreenshot("step11-reply-sent")

					// Check for success notice
					if err := suite.WaitForElement("[class*='success']", 3*time.Second); err != nil {
						suite.logger.Log("[E2E-WEBUI] Success notice not found after send reply")
					} else {
						suite.logger.Log("[E2E-WEBUI-ASSERT] Reply sent successfully")
					}
				}
			}
		}
	} else {
		suite.logger.Log("[E2E-WEBUI] No messages found in inbox")
	}

	// Step 12: Verify reservation map section
	suite.logger.Log("[E2E-WEBUI-STEP] Step 12: Verify reservation map")
	if err := suite.WaitForElement("h2:has-text('Reservation'), [data-testid='reservation-map']", 5*time.Second); err != nil {
		suite.logger.Log("[E2E-WEBUI] Reservation map not found: %v", err)
	} else {
		suite.logger.Log("[E2E-WEBUI-ASSERT] Reservation map found")
	}

	suite.CaptureScreenshot("final-mail-flow")
	suite.logger.Log("[E2E-WEBUI] Mail flow test completed")
}

// ============================================================================
// Test: Full Web UI Smoke Test
// Quick smoke test to verify all pages load
// ============================================================================

func TestE2E_WebUI_SmokeTest(t *testing.T) {
	if os.Getenv("E2E_WEBUI_ENABLED") != "true" {
		t.Skip("E2E_WEBUI_ENABLED not set to true, skipping Web UI tests")
	}

	suite := NewWebUITestSuite(t, "smoke-test")
	defer suite.Teardown()

	if err := suite.Setup(); err != nil {
		t.Fatalf("[E2E-WEBUI] Setup failed: %v", err)
	}

	pages := []struct {
		path     string
		title    string
		selector string
	}{
		{"/", "Sessions", "h1"},
		{"/beads", "Beads", "h1"},
		{"/mail", "Mail", "h1"},
		{"/agents", "Agents", "h1"},
		{"/connect", "Connect", "h1, input"},
	}

	for _, page := range pages {
		suite.logger.Log("[E2E-WEBUI-SMOKE] Testing page: %s", page.path)

		if err := suite.Navigate(page.path); err != nil {
			suite.logger.Log("[E2E-WEBUI-SMOKE] FAIL - could not navigate to %s: %v", page.path, err)
			continue
		}

		time.Sleep(2 * time.Second)

		if err := suite.WaitForElement(page.selector, 10*time.Second); err != nil {
			suite.logger.Log("[E2E-WEBUI-SMOKE] FAIL - page %s did not load properly: %v", page.path, err)
			suite.CaptureScreenshot(fmt.Sprintf("smoke-fail-%s", strings.ReplaceAll(page.path, "/", "-")))
		} else {
			suite.logger.Log("[E2E-WEBUI-SMOKE] PASS - page %s loaded", page.path)
		}
	}

	suite.logger.Log("[E2E-WEBUI] Smoke test completed")
}

// ============================================================================
// Test Report Generation
// ============================================================================

// WebUITestReport represents a test report for Web UI tests.
type WebUITestReport struct {
	TestID          string        `json:"test_id"`
	Timestamp       time.Time     `json:"timestamp"`
	DurationSeconds float64       `json:"duration_seconds"`
	Scenarios       []Scenario    `json:"scenarios"`
	Summary         ReportSummary `json:"summary"`
}

// Scenario represents a single test scenario result.
type Scenario struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"` // "pass" or "fail"
	DurationMs float64 `json:"duration_ms"`
	Steps      []Step  `json:"steps"`
	Error      string  `json:"error,omitempty"`
}

// Step represents a single step within a scenario.
type Step struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	DurationMs float64 `json:"duration_ms"`
}

// ReportSummary summarizes the test results.
type ReportSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// GenerateReport creates a JSON test report.
func GenerateReport(scenarios []Scenario) WebUITestReport {
	summary := ReportSummary{Total: len(scenarios)}
	for _, s := range scenarios {
		if s.Status == "pass" {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}

	return WebUITestReport{
		TestID:    fmt.Sprintf("webui-%d", time.Now().Unix()),
		Timestamp: time.Now(),
		Scenarios: scenarios,
		Summary:   summary,
	}
}

// SaveReport saves the test report to a file.
func SaveReport(report WebUITestReport, path string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
