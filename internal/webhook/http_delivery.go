package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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

// ValidateWebhookTargetURL enforces safe webhook destination URL rules:
// only http/https schemes are accepted, and link-local / cloud-metadata /
// unspecified addresses (e.g. 169.254.169.254, fe80::, 0.0.0.0, ::) are
// rejected to prevent SSRF metadata theft. When allowLoopback is false,
// loopback targets (127.0.0.0/8, ::1, localhost) are also rejected.
func ValidateWebhookTargetURL(rawURL string, allowLoopback bool) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported webhook URL scheme %q (only http and https are allowed)", parsed.Scheme)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return errors.New("webhook URL missing host")
	}
	lowerHost := strings.TrimRight(strings.ToLower(host), ".")
	if lowerHost == "metadata.google.internal" || lowerHost == "metadata.goog" {
		return fmt.Errorf("webhook target %q blocked by SSRF policy", host)
	}
	if !allowLoopback && (lowerHost == "localhost" || strings.HasSuffix(lowerHost, ".localhost")) {
		return fmt.Errorf("webhook loopback target %q blocked by SSRF policy", host)
	}
	// Use netip.ParseAddr on lowerHost (after stripping trailing dots) so
	// trailing-dot IP literals (169.254.169.254.) and IPv6 literals carrying
	// zone IDs ([fe80::1%25eth0] / [fd00:ec2::254%25eth0]) cannot bypass checks.
	if addr, err := netip.ParseAddr(lowerHost); err == nil {
		if err := validateWebhookIP(net.IP(addr.AsSlice()), allowLoopback); err != nil {
			return err
		}
	} else if ip := net.ParseIP(lowerHost); ip != nil {
		if err := validateWebhookIP(ip, allowLoopback); err != nil {
			return err
		}
	} else if _, ok := parseLegacyIPv4Literal(lowerHost); ok {
		return fmt.Errorf("webhook non-canonical IPv4 literal %q blocked by SSRF policy", host)
	}
	return nil
}

// parseLegacyIPv4Literal parses inet_aton-style non-canonical IPv4 literals
// (decimal integer, hex 0x..., octal 0..., or 2-/3-part shorthand like
// 169.254.43518) so they cannot bypass pre-dial SSRF URL validation.
func parseLegacyIPv4Literal(host string) (net.IP, bool) {
	if host == "" || strings.Contains(host, ":") {
		return nil, false
	}
	parts := strings.Split(host, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return nil, false
	}
	vals := make([]uint64, len(parts))
	for i, part := range parts {
		if part == "" || strings.Contains(part, "_") {
			return nil, false
		}
		var (
			v   uint64
			err error
		)
		switch {
		case strings.HasPrefix(part, "0x") || strings.HasPrefix(part, "0X"):
			if len(part) <= 2 {
				return nil, false
			}
			v, err = strconv.ParseUint(part[2:], 16, 64)
		case len(part) > 1 && part[0] == '0':
			v, err = strconv.ParseUint(part[1:], 8, 64)
		default:
			v, err = strconv.ParseUint(part, 10, 64)
		}
		if err != nil {
			return nil, false
		}
		vals[i] = v
	}
	var ipUint uint32
	switch len(vals) {
	case 1:
		if vals[0] > 0xFFFFFFFF {
			return nil, false
		}
		ipUint = uint32(vals[0])
	case 2:
		if vals[0] > 0xFF || vals[1] > 0xFFFFFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | vals[1])
	case 3:
		if vals[0] > 0xFF || vals[1] > 0xFF || vals[2] > 0xFFFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | (vals[1] << 16) | vals[2])
	case 4:
		if vals[0] > 0xFF || vals[1] > 0xFF || vals[2] > 0xFF || vals[3] > 0xFF {
			return nil, false
		}
		ipUint = uint32((vals[0] << 24) | (vals[1] << 16) | (vals[2] << 8) | vals[3])
	}
	return net.IPv4(byte(ipUint>>24), byte(ipUint>>16), byte(ipUint>>8), byte(ipUint)), true
}

var (
	webhookBlockedMetadataIPs = []net.IP{
		net.ParseIP("fd00:ec2::254"),   // AWS IMDSv2 IPv6 endpoint
		net.ParseIP("100.100.100.200"), // Alibaba Cloud metadata endpoint
	}
	webhookCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")
)

func validateWebhookIP(ip net.IP, allowLoopback bool) error {
	if addr, ok := netip.AddrFromSlice(ip); ok {
		addr = addr.Unmap()
		if addr.IsUnspecified() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
			addr.IsMulticast() || addr.IsPrivate() || webhookCGNATPrefix.Contains(addr) {
			return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
		}
		if !allowLoopback && addr.IsLoopback() {
			return fmt.Errorf("webhook loopback IP %s blocked by SSRF policy", ip.String())
		}
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
	}
	for _, blocked := range webhookBlockedMetadataIPs {
		if blocked != nil && ip.Equal(blocked) {
			return fmt.Errorf("webhook target IP %s blocked by SSRF policy", ip.String())
		}
	}
	if !allowLoopback && ip.IsLoopback() {
		return fmt.Errorf("webhook loopback IP %s blocked by SSRF policy", ip.String())
	}
	return nil
}

func safeWebhookDialContext(allowLoopback bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return func(dctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(dctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IP addresses resolved for webhook host %q", host)
		}
		// Validate every resolved IP up front so mixed-record responses cannot
		// smuggle a metadata/link-local address past fallback dialing.
		for _, ipAddr := range ips {
			if err := validateWebhookIP(ipAddr.IP, allowLoopback); err != nil {
				return nil, err
			}
		}
		var lastErr error
		for _, ipAddr := range ips {
			conn, dialErr := dialer.DialContext(dctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// DeliverJSON delivers a JSON webhook payload through internal/webhook's
// redirect-blocking, SSRF-validated HTTP transport with optional HMAC-SHA256
// signing (X-NTM-Signature: sha256=...) and bounded response reading.
func DeliverJSON(ctx context.Context, client *http.Client, targetURL, secret string, payload []byte, headers map[string]string) (int, error) {
	targetURL = strings.TrimSpace(targetURL)
	if err := ValidateWebhookTargetURL(targetURL, true); err != nil {
		return 0, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// Resolve DNS safely before dialing so a public hostname cannot resolve
	// into cloud metadata / link-local / unspecified space (DNS rebinding guard).
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	safeClient := *client
	if tr, ok := baseTransport.(*http.Transport); ok {
		trCopy := tr.Clone()
		defer trCopy.CloseIdleConnections()
		trCopy.Proxy = nil
		trCopy.Dial = nil
		trCopy.DialTLS = nil
		trCopy.DialTLSContext = nil
		trCopy.DialContext = safeWebhookDialContext(true)
		safeClient.Transport = trCopy
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("creating webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ntm-webhook/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-NTM-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return sendWebhookRequest(&safeClient, req)
}
