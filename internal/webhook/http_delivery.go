package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// webhookHTTPError retains the receiver's retry deadline as structured data.
// Keeping it on the attempt error prevents one endpoint's cooldown from being
// accidentally applied to another delivery sharing the manager's HTTP client.
type webhookHTTPError struct {
	statusCode int
	body       string
	retryAfter time.Time
}

func (e *webhookHTTPError) Error() string {
	return fmt.Sprintf("webhook returned %d: %s", e.statusCode, e.body)
}

// sendWebhookRequest delivers exactly to the configured endpoint. Redirects
// are failures, not permission to forward signed events or custom credentials
// elsewhere (or to rewrite a POST into a GET and report a false success).
func sendWebhookRequest(client *http.Client, req *http.Request) (int, error) {
	// Copy the client, not its transport: preserve connection pooling and the
	// caller's timeout without racing other requests by changing shared state.
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := clientCopy.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Receipt of a 2xx acknowledges delivery. A truncated acknowledgement body
	// must not cause duplicate side effects by retrying an accepted event.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, &webhookHTTPError{
		statusCode: resp.StatusCode,
		body:       string(body),
		retryAfter: parseWebhookRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

// parseWebhookRetryAfter accepts both HTTP-date and non-negative delay-seconds.
// Malformed/overflowing values fall back to local backoff rather than wrapping
// a duration into the past. A past date likewise supplies no extra delay.
func parseWebhookRetryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	const maxSeconds = int64((1<<63 - 1) / int64(time.Second))
	if seconds, err := strconv.ParseUint(value, 10, 63); err == nil {
		if seconds > uint64(maxSeconds) {
			return time.Time{}
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline
	}
	return time.Time{}
}

// retryableWebhookFailure distinguishes receiver backpressure and transient
// failures from permanent request errors. Cancellation never creates new work.
func retryableWebhookFailure(statusCode int, err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	return statusCode == 0 || statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooManyRequests ||
		(statusCode >= http.StatusInternalServerError && statusCode <= 599)
}

// webhookRetryDeadline treats Retry-After as a lower bound, not as a replacement
// for configured exponential backoff. In particular a server cooldown must not
// be shortened to the client's local maximum backoff or by jitter.
func webhookRetryDeadline(err error, backoff time.Time) time.Time {
	var responseErr *webhookHTTPError
	if errors.As(err, &responseErr) && responseErr.retryAfter.After(backoff) {
		return responseErr.retryAfter
	}
	return backoff
}
