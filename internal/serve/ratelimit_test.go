package serve

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func rateLimitServer(t *testing.T, cfg Config) (*Server, *atomic.Int64) {
	t.Helper()
	srv := New(cfg)
	t.Cleanup(srv.Stop)
	clock := &atomic.Int64{}
	clock.Store(time.Now().UnixNano())
	srv.rateLimiter.now = func() time.Time { return time.Unix(0, clock.Load()) }
	return srv, clock
}

func rateLimitRequest(t *testing.T, srv *Server, peer, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = peer
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	response := httptest.NewRecorder()
	srv.Router().ServeHTTP(response, req)
	return response
}

func assertRateLimitResponse(t *testing.T, response *httptest.ResponseRecorder, scope string) {
	t.Helper()
	var body APIError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid rate-limit envelope: status=%d headers=%v body=%q err=%v", response.Code, response.Header(), response.Body.String(), err)
	}
	if response.Code != http.StatusTooManyRequests || body.Success || body.ErrorCode != ErrCodeRateLimited || body.Timestamp == "" || body.RequestID == "" {
		t.Fatalf("unexpected rate-limit envelope: status=%d body=%+v", response.Code, body)
	}
	if body.Details["scope"] != scope {
		t.Fatalf("scope=%v want=%s", body.Details["scope"], scope)
	}
	if response.Header().Get("X-RateLimit-Remaining") != "0" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid rate-limit headers: %v", response.Header())
	}
	for _, header := range []string{"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Reset"} {
		n, err := strconv.ParseInt(response.Header().Get(header), 10, 64)
		if err != nil || n <= 0 {
			t.Fatalf("%s=%q must be a positive integer", header, response.Header().Get(header))
		}
	}
	diagnostic, err := json.Marshal(map[string]interface{}{"scenario": t.Name(), "status": response.Code, "headers": response.Header(), "response": body})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(diagnostic))
}

func TestHTTPRateLimitPublicBurstAndRefill(t *testing.T) {
	srv, clock := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeAPIKey, APIKey: "admission-test"},
		RateLimit: config.ServeRateLimitConfig{RequestsPerSecond: 2, Burst: 2},
	})
	for i := 0; i < 2; i++ {
		response := rateLimitRequest(t, srv, "192.0.2.10:1234", "/api/v1/version", "admission-test")
		if response.Code != http.StatusOK || response.Header().Get("X-RateLimit-Remaining") != strconv.Itoa(1-i) {
			t.Fatalf("request %d: status=%d headers=%v body=%s", i, response.Code, response.Header(), response.Body.String())
		}
	}
	assertRateLimitResponse(t, rateLimitRequest(t, srv, "192.0.2.10:9876", "/api/v1/version", "admission-test"), "ip")
	assertRateLimitResponse(t, rateLimitRequest(t, srv, "192.0.2.11:9876", "/api/v1/version", "admission-test"), "principal")
	clock.Add(int64(time.Second))
	for i := 0; i < 2; i++ {
		response := rateLimitRequest(t, srv, "192.0.2.10:8888", "/api/v1/version", "admission-test")
		if response.Code != http.StatusOK {
			t.Fatalf("quota did not refill: %d %s", response.Code, response.Body.String())
		}
	}
	assertRateLimitResponse(t, rateLimitRequest(t, srv, "192.0.2.10:1", "/api/v1/version", "admission-test"), "ip")
}

func TestHTTPRateLimitRejectsBeforeAuthentication(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeAPIKey, APIKey: "real-credential"},
		RateLimit: config.ServeRateLimitConfig{RequestsPerSecond: 1, Burst: 1},
	})
	response := rateLimitRequest(t, srv, "192.0.2.20:100", "/api/v1/version", "bad-credential")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status=%d want 401: %s", response.Code, response.Body.String())
	}
	assertRateLimitResponse(t, rateLimitRequest(t, srv, "192.0.2.20:200", "/api/v1/version", "real-credential"), "ip")
	// Failed authentication must not spend the valid user's principal quota.
	response = rateLimitRequest(t, srv, "192.0.2.21:100", "/api/v1/version", "real-credential")
	if response.Code != http.StatusOK {
		t.Fatalf("unauthenticated request poisoned principal: %d %s", response.Code, response.Body.String())
	}
}

func TestHTTPRateLimitLocalDefaultsAndExplicitOverrides(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{})
	for i := 0; i < 250; i++ {
		response := rateLimitRequest(t, srv, "127.0.0.1:1234", "/api/v1/version", "")
		if response.Code != http.StatusOK || response.Header().Get("X-RateLimit-Limit") != "" {
			t.Fatalf("local page-load burst throttled at %d: %d %s", i, response.Code, response.Body.String())
		}
	}
	limited, _ := rateLimitServer(t, Config{
		RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 1},
		WebUI: fstest.MapFS{
			"index.html":          {Data: []byte("<html>dashboard</html>")},
			"_next/static/app.js": {Data: []byte("console.log('ready')")},
		},
	})
	if response := rateLimitRequest(t, limited, "[::1]:5000", "/api/v1/version", ""); response.Code != http.StatusOK {
		t.Fatalf("first local request: %d %s", response.Code, response.Body.String())
	}
	assertRateLimitResponse(t, rateLimitRequest(t, limited, "[::1]:5001", "/api/v1/version", ""), "ip")
	for _, path := range []string{"/health", "/", "/_next/static/app.js", "/docs"} {
		for i := 0; i < 3; i++ {
			response := rateLimitRequest(t, limited, "[::1]:5000", path, "")
			if response.Code != http.StatusOK || response.Header().Get("X-RateLimit-Limit") != "" {
				t.Fatalf("exempt resource %s unavailable: status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
			}
		}
	}
	disabled := false
	unlimited, _ := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeAPIKey, APIKey: "disabled"},
		RateLimit: config.ServeRateLimitConfig{Enabled: &disabled, Burst: 1},
	})
	for i := 0; i < 3; i++ {
		if response := rateLimitRequest(t, unlimited, "192.0.2.1:1", "/api/v1/version", "disabled"); response.Code != http.StatusOK {
			t.Fatalf("explicit disable ignored: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestHTTPRateLimitAuthenticatedPrincipalsAcrossIPs(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeMTLS},
		RateLimit: config.ServeRateLimitConfig{Burst: 2, RequestsPerSecond: 1},
	})
	for userIndex, user := range []string{"alice", "bob", "mtls-client"} {
		for i := 0; i < 3; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
			req.RemoteAddr = fmt.Sprintf("192.0.%d.%d:1000", userIndex+2, i+1)
			// The TLS transport supplies PeerCertificates after verification;
			// these router requests exercise the same authenticated claims path.
			// A renewed certificate with the same CN must retain its quota,
			// including a CN literally equal to the historical fallback name.
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
				Subject: pkix.Name{CommonName: user}, Raw: []byte(fmt.Sprintf("certificate-%s-%d", user, i)),
			}}}
			response := httptest.NewRecorder()
			srv.Router().ServeHTTP(response, req)
			if i < 2 && response.Code != http.StatusOK {
				t.Fatalf("independent user %s request %d denied: %d %s", user, i, response.Code, response.Body.String())
			}
			if i == 2 {
				assertRateLimitResponse(t, response, "principal")
			}
		}
	}
}

func TestHTTPRateLimitOIDCSubjectSurvivesTokenRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"keys": []map[string]string{{
			"kty": "RSA", "kid": "limiter", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer jwks.Close()
	srv, _ := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeOIDC, OIDC: OIDCConfig{Issuer: "rate-limit-test", JWKSURL: jwks.URL}},
		RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 1},
	})
	for i, subject := range []string{"alice", "alice", "bob"} {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"limiter"}`))
		claims, err := json.Marshal(map[string]interface{}{"iss": "rate-limit-test", "sub": subject, "role": "viewer", "exp": time.Now().Add(time.Hour).Unix(), "jti": strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1", i+1)
		req.Header.Set("Authorization", "Bearer "+input+"."+base64.RawURLEncoding.EncodeToString(signature))
		response := httptest.NewRecorder()
		srv.Router().ServeHTTP(response, req)
		if i == 1 {
			assertRateLimitResponse(t, response, "principal")
		} else if response.Code != http.StatusOK {
			t.Fatalf("verified subject %s rejected: %d %s", subject, response.Code, response.Body.String())
		}
	}
}

func TestHTTPRateLimitTrustedProxyBoundary(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{
		RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 1, TrustedProxyCIDRs: []string{"10.0.0.0/8"}},
	})
	cases := []struct {
		peer, forwarded string
		status          int
	}{
		{"192.0.2.1:1234", "198.51.100.1", http.StatusOK},
		{"192.0.2.1:1235", "198.51.100.2", http.StatusTooManyRequests},
		{"10.0.0.1:1234", "203.0.113.99, 198.51.100.3, 10.0.0.2", http.StatusOK},
		{"10.0.0.1:1235", "203.0.113.100, 198.51.100.3, 10.0.0.2", http.StatusTooManyRequests},
		{"10.0.0.1:1236", "198.51.100.4, 10.0.0.2", http.StatusOK},
		{"10.0.0.1:1237", "invalid", http.StatusOK},
		{"10.0.0.1:1238", "different-invalid", http.StatusTooManyRequests},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		req.RemoteAddr = tc.peer
		req.Header.Set("X-Forwarded-For", tc.forwarded)
		req.Header.Set("X-Real-IP", "203.0.113.77")
		response := httptest.NewRecorder()
		srv.Router().ServeHTTP(response, req)
		if response.Code != tc.status || req.RemoteAddr != tc.peer {
			t.Fatalf("peer=%q xff=%q got=%d want=%d remote_after=%q body=%s", tc.peer, tc.forwarded, response.Code, tc.status, req.RemoteAddr, response.Body.String())
		}
	}
	for _, cidr := range []string{"127.0.0.0/8", "::ffff:127.0.0.0/104"} {
		proxy, _ := rateLimitServer(t, Config{RateLimit: config.ServeRateLimitConfig{TrustedProxyCIDRs: []string{cidr}}})
		for _, forwarded := range []string{"", "invalid", "198.51.100.1", "127.0.0.1"} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("X-Forwarded-For", forwarded)
			response := httptest.NewRecorder()
			proxy.Router().ServeHTTP(response, req)
			if response.Header().Get("X-RateLimit-Limit") == "" {
				t.Fatalf("trusted proxy %q earned local exemption from %q: %v", cidr, forwarded, response.Header())
			}
			if forwarded == "198.51.100.1" && proxy.rateLimiter.clientIP(req).String() != forwarded {
				t.Fatalf("trusted proxy %q did not resolve client %s", cidr, forwarded)
			}
		}
	}
}

func TestHTTPRateLimitBoundedCardinalityDoesNotResetActiveQuota(t *testing.T) {
	srv, clock := rateLimitServer(t, Config{
		RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 0.001, MaxEntries: 64, IdleTTL: time.Nanosecond},
	})
	if response := rateLimitRequest(t, srv, "192.0.2.1:1", "/api/v1/version", ""); response.Code != http.StatusOK {
		t.Fatalf("initial request failed: %d %s", response.Code, response.Body.String())
	}
	rejected := 0
	for i := 0; i < 1024; i++ {
		response := rateLimitRequest(t, srv, fmt.Sprintf("[2001:db8::%x]:1000", i+1), "/api/v1/version", "")
		if response.Code == http.StatusTooManyRequests {
			rejected++
		} else if response.Code != http.StatusOK {
			t.Fatalf("cardinality request: %d %s", response.Code, response.Body.String())
		}
	}
	entries := 0
	for i := range srv.rateLimiter.shards {
		shard := &srv.rateLimiter.shards[i]
		shard.mu.Lock()
		entries += len(shard.entries)
		shard.mu.Unlock()
	}
	if entries > 64 || rejected == 0 {
		t.Fatalf("memory bound was not enforced: entries=%d rejected=%d", entries, rejected)
	}
	clock.Add(int64(time.Second)) // TTL expired, but credit has not replenished.
	assertRateLimitResponse(t, rateLimitRequest(t, srv, "192.0.2.1:2", "/api/v1/version", ""), "ip")
	clock.Add(int64(1000 * time.Second))
	if response := rateLimitRequest(t, srv, "[2001:db8::ffff]:1000", "/api/v1/version", ""); response.Code != http.StatusOK {
		t.Fatalf("replenished cache cannot admit a new identity: %d %s", response.Code, response.Body.String())
	}
	t.Logf(`{"scenario":%q,"entries":%d,"cap":64,"capacity_rejections":%d}`, t.Name(), entries, rejected)
}

func TestHTTPRateLimitConcurrentBurstIsAtomic(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{RateLimit: config.ServeRateLimitConfig{Burst: 8, RequestsPerSecond: 1}})
	var admitted, rejected, unexpected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := rateLimitRequest(t, srv, "192.0.2.1:1000", "/api/v1/version", "")
			switch response.Code {
			case http.StatusOK:
				admitted.Add(1)
			case http.StatusTooManyRequests:
				rejected.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 8 || rejected.Load() != 56 || unexpected.Load() != 0 {
		t.Fatalf("burst overspent: admitted=%d rejected=%d unexpected=%d", admitted.Load(), rejected.Load(), unexpected.Load())
	}
}

func TestHTTPRateLimitWebSocketHandshakeBudgetDoesNotThrottleFrames(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 1}})
	httpServer := httptest.NewServer(srv.Router())
	defer httpServer.Close()
	for i, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		response, err := httpServer.Client().Get(httpServer.URL + "/api/v1/version")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != status {
			t.Fatalf("HTTP request %d: status=%d body=%s err=%v", i, response.StatusCode, body, err)
		}
	}
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws"
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		t.Fatalf("HTTP quota blocked independent WS handshake: %v", err)
	}
	defer conn.Close()
	second, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if second != nil {
		second.Close()
	}
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("WS handshake quota not enforced: err=%v response=%v", err, response)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		id := strconv.Itoa(i)
		if err := conn.WriteJSON(WSMessage{Type: WSMsgPing, RequestID: id}); err != nil {
			t.Fatal(err)
		}
		var pong WSMessage
		if err := conn.ReadJSON(&pong); err != nil {
			t.Fatalf("active WS frame %d throttled: %v", i, err)
		}
		if pong.Type != WSMsgPong || pong.RequestID != id {
			t.Fatalf("unexpected WS frame: %+v", pong)
		}
	}
}

func TestHTTPRateLimitWebSocketTicketCanBeRedeemedWithSingleCredit(t *testing.T) {
	srv, _ := rateLimitServer(t, Config{
		Auth:      AuthConfig{Mode: AuthModeAPIKey, APIKey: "browser-test"},
		RateLimit: config.ServeRateLimitConfig{Burst: 1, RequestsPerSecond: 0.001},
	})
	httpServer := httptest.NewServer(srv.Router())
	defer httpServer.Close()
	request := func(method, path string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-API-Key", "browser-test")
		response, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	// A burst of REST requests must not prevent obtaining a ticket either.
	if status, body := request(http.MethodGet, "/api/v1/version"); status != http.StatusOK {
		t.Fatalf("initial REST request failed: %d %s", status, body)
	}
	status, body := request(http.MethodPost, "/api/v1/auth/ws-ticket")
	var ticket struct {
		Success bool   `json:"success"`
		Ticket  string `json:"ticket"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &ticket) != nil || !ticket.Success || ticket.Ticket == "" {
		t.Fatalf("ticket issuance failed: %d %s", status, body)
	}
	// A shared issuance/upgrade budget would require a 1000-second refill,
	// but this ticket expires in 30 seconds. Prove real redemption now.
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/v1/ws?ticket=" + ticket.Ticket
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		t.Fatalf("issued ticket cannot be redeemed with burst=1: %v", err)
	}
	defer conn.Close()
	if status, body := request(http.MethodPost, "/api/v1/auth/ws-ticket"); status != http.StatusTooManyRequests {
		t.Fatalf("ticket issuance is not independently bounded: %d %s", status, body)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(WSMessage{Type: WSMsgPing, RequestID: "ticket-proof"}); err != nil {
		t.Fatal(err)
	}
	var pong WSMessage
	if err := conn.ReadJSON(&pong); err != nil || pong.Type != WSMsgPong || pong.RequestID != "ticket-proof" {
		t.Fatalf("authenticated WS frames failed after admission: pong=%+v err=%v", pong, err)
	}
	t.Logf(`{"scenario":%q,"burst":1,"requests_per_second":0.001,"ticket_issued":true,"ticket_redeemed":true,"next_ticket_status":429,"pong":true}`, t.Name())
}
