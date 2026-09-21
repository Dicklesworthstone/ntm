package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookDeliveryRejectsRedirectsWithoutForwardingSecrets(t *testing.T) {
	t.Parallel()
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var forwarded atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL, status)
			}))
			defer origin.Close()

			var policyCalls atomic.Int32
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				policyCalls.Add(1)
				return nil
			}}
			req, err := http.NewRequest(http.MethodPost, origin.URL, strings.NewReader(`{"secret":"event"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-API-Key", "private-credential")
			req.Header.Set("X-NTM-Signature", "sha256=private-signature")
			got, err := sendWebhookRequest(client, req)
			if got != status || err == nil {
				t.Fatalf("redirect returned (%d, %v), want (%d, error)", got, err, status)
			}
			if forwarded.Load() != 0 || policyCalls.Load() != 0 {
				t.Fatal("webhook followed a redirect or consulted a permissive redirect policy")
			}
			if retryableWebhookFailure(got, err) {
				t.Fatal("permanent redirect must not be retried")
			}
			// The shared client's redirect policy must remain untouched.
			if err := client.CheckRedirect(req, nil); err != nil || policyCalls.Load() != 1 {
				t.Fatal("shared client redirect policy was mutated")
			}
		})
	}
}

func TestWebhookDeliveryAcknowledgementAndRetryPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status         int
		success, retry bool
	}{
		{200, true, false}, {201, true, false}, {202, true, false}, {204, true, false},
		{299, true, false}, {300, false, false}, {304, false, false},
		{400, false, false}, {401, false, false}, {403, false, false}, {404, false, false},
		{408, false, true}, {409, false, false}, {422, false, false}, {429, false, true},
		{500, false, true}, {502, false, true}, {503, false, true}, {504, false, true},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("X-NTM-Delivery-ID") != "stable-id" {
					t.Error("delivery method or identity lost")
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("event"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-NTM-Delivery-ID", "stable-id")
			status, err := sendWebhookRequest(srv.Client(), req)
			if status != tc.status || (err == nil) != tc.success {
				t.Fatalf("got (%d, %v), want status %d, success %v", status, err, tc.status, tc.success)
			}
			if got := retryableWebhookFailure(status, err); got != tc.retry {
				t.Fatalf("retry=%v, want %v", got, tc.retry)
			}
		})
	}
}

func TestWebhookDeliveryHonorsReceiverCooldown(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, value := range []string{"120", now.Add(2 * time.Minute).UTC().Format(http.TimeFormat)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", value)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "try later")
		}))
		req, err := http.NewRequest(http.MethodPost, srv.URL, nil)
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		status, err := sendWebhookRequest(srv.Client(), req)
		srv.Close()
		if status != http.StatusTooManyRequests || !retryableWebhookFailure(status, err) {
			t.Fatalf("got (%d, %v), want retryable 429", status, err)
		}
		deadline := webhookRetryDeadline(fmt.Errorf("delivery: %w", err), now.Add(time.Second))
		if deadline.Before(now.Add(119*time.Second)) || deadline.After(time.Now().Add(121*time.Second)) {
			t.Fatalf("Retry-After %q produced deadline %s", value, deadline)
		}
		longBackoff := now.Add(5 * time.Minute)
		if got := webhookRetryDeadline(err, longBackoff); !got.Equal(longBackoff) {
			t.Fatalf("server cooldown shortened local backoff: %s", got)
		}
	}
}

func TestParseWebhookRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Minute)
	for _, tc := range []struct {
		value string
		want  time.Time
	}{
		{"", time.Time{}}, {"0", now}, {" 120 ", future},
		{future.Format(http.TimeFormat), future},
		{now.Add(-time.Second).Format(http.TimeFormat), time.Time{}},
		{"-1", time.Time{}}, {"+1", time.Time{}}, {"1.5", time.Time{}},
		{"later", time.Time{}}, {"9223372036854775807", time.Time{}},
		{"18446744073709551616", time.Time{}}, {"9223372037", time.Time{}},
	} {
		t.Run(tc.value, func(t *testing.T) {
			if got := parseWebhookRetryAfter(tc.value, now); !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

type webhookBodyProbe struct {
	reads  int
	closed bool
	fail   bool
}

func (b *webhookBodyProbe) Read(p []byte) (int, error) {
	if b.fail {
		return 0, io.ErrUnexpectedEOF
	}
	for i := range p {
		p[i] = 'x'
	}
	b.reads += len(p)
	return len(p), nil
}
func (b *webhookBodyProbe) Close() error { b.closed = true; return nil }

type webhookTransportFunc func(*http.Request) (*http.Response, error)

func (f webhookTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebhookDeliveryBoundsAndClosesResponseBodies(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 500} {
		for _, fail := range []bool{false, true} {
			body := &webhookBodyProbe{fail: fail}
			client := &http.Client{Transport: webhookTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
			})}
			req, err := http.NewRequest(http.MethodPost, "https://example.test/hook", nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := sendWebhookRequest(client, req)
			if got != status || (err == nil) != (status == 200) {
				t.Fatalf("status %d, broken body %v: got (%d, %v)", status, fail, got, err)
			}
			if !body.closed || body.reads > 4096 {
				t.Fatalf("unbounded or unclosed body: %+v", body)
			}
		}
	}
}

func TestWebhookDeliveryCancellationAndTransportErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		err   error
		retry bool
	}{
		{errors.New("connection reset"), true}, {context.DeadlineExceeded, true},
		{context.Canceled, false},
	} {
		client := &http.Client{Transport: webhookTransportFunc(func(*http.Request) (*http.Response, error) {
			return nil, tc.err
		})}
		req, err := http.NewRequest(http.MethodPost, "https://example.test/hook", nil)
		if err != nil {
			t.Fatal(err)
		}
		status, err := sendWebhookRequest(client, req)
		if status != 0 || !errors.Is(err, tc.err) || retryableWebhookFailure(status, err) != tc.retry {
			t.Fatalf("%v: got (%d, %v), retry=%v", tc.err, status, err, retryableWebhookFailure(status, err))
		}
	}
}
