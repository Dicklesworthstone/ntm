package serve

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

const (
	ErrCodeRateLimited = "RATE_LIMITED"
	rateLimitShards    = 32
)

// httpRateLimiter is shared by both admission stages. IP admission precedes
// authentication so malformed credentials cannot bypass it. Principal admission
// follows authentication and RBAC so unverified subjects never become identities.
// All classes together consume at most maxEntries fixed-size bucket records.
type httpRateLimiter struct {
	config         config.ServeRateLimitConfig
	configErr      error
	localAutomatic bool
	disabled       bool
	rate           float64
	burst          int
	idleTTL        time.Duration
	trustedProxies []netip.Prefix
	shards         [rateLimitShards]httpRateLimitShard
	now            func() time.Time
}

type httpRateLimitShard struct {
	mu       sync.Mutex
	capacity int
	entries  map[[sha256.Size]byte]*list.Element
	lru      list.List
}

type httpRateLimitBucket struct {
	key      [sha256.Size]byte
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

type httpRateLimitDecision struct {
	allowed   bool
	remaining int
	retryIn   time.Duration
	resetAt   time.Time
}

func newHTTPRateLimiter(cfg config.ServeRateLimitConfig, mode AuthMode) *httpRateLimiter {
	// Caller-owned pointers and slices must not mutate live admission policy.
	if cfg.Enabled != nil {
		enabled := *cfg.Enabled
		cfg.Enabled = &enabled
	}
	cfg.TrustedProxyCIDRs = append([]string(nil), cfg.TrustedProxyCIDRs...)
	l := &httpRateLimiter{
		config:    cfg,
		configErr: config.ValidateServeRateLimitConfig(&cfg),
		rate:      cfg.RequestsPerSecond,
		burst:     cfg.Burst,
		idleTTL:   cfg.IdleTTL,
		now:       time.Now,
	}
	if l.configErr != nil {
		// New has no error return. Embedded Router users fail closed and Start
		// reports the same validation error before binding the listener.
		return l
	}
	l.disabled = cfg.Enabled != nil && !*cfg.Enabled
	l.localAutomatic = (mode == AuthModeLocal || mode == "") && cfg.Enabled == nil &&
		cfg.RequestsPerSecond == 0 && cfg.Burst == 0
	if l.rate == 0 {
		l.rate = 100
	}
	if l.burst == 0 {
		l.burst = 200
	}
	if l.idleTTL == 0 {
		l.idleTTL = 5 * time.Minute
	}
	maxEntries := cfg.MaxEntries
	if maxEntries == 0 {
		maxEntries = 8192
	}
	for i := range l.shards {
		l.shards[i].capacity = maxEntries / rateLimitShards
		if i < maxEntries%rateLimitShards {
			l.shards[i].capacity++
		}
		l.shards[i].entries = make(map[[sha256.Size]byte]*list.Element)
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		prefix, _ := netip.ParsePrefix(cidr) // Validated above.
		if prefix.Addr().Is4In6() {
			// Peers and XFF addresses are unmapped below; their trust ranges
			// must use the same address family or a mapped loopback proxy
			// would be mistaken for a directly connected local client.
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		l.trustedProxies = append(l.trustedProxies, prefix.Masked())
	}
	return l
}

func (l *httpRateLimiter) configuration() config.ServeRateLimitConfig {
	if l == nil {
		return config.ServeRateLimitConfig{}
	}
	return l.config
}

func (s *Server) ipRateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := s.rateLimiter
		if s.exemptFromRateLimit(r) {
			next.ServeHTTP(w, r)
			return
		}
		if l.configErr != nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, ErrCodeServiceUnavail,
				"invalid server rate limit configuration", nil, requestIDFromContext(r.Context()))
			return
		}
		if l.admit(w, r, "ip", l.clientIP(r).String()) {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) principalRateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.exemptFromRateLimit(r) {
			next.ServeHTTP(w, r)
			return
		}
		principal := s.rateLimitPrincipal(r)
		if principal == "" || s.rateLimiter.admit(w, r, "principal", principal) {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) exemptFromRateLimit(r *http.Request) bool {
	l := s.rateLimiter
	if l == nil || (l.configErr == nil && l.disabled) || r.Method == http.MethodOptions || s.isPublicUnauthenticatedRequest(r) {
		return true
	}
	// Health polling remains available after a caller exhausts admission
	// credit. This only exempts rate limiting; API authentication and RBAC
	// still apply to the versioned health endpoint.
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Path == "/api/v1/health" {
		return true
	}
	// A trusted reverse proxy on loopback is not a local user. In particular,
	// malformed/missing forwarding metadata must not earn its exemption.
	return l.localAutomatic && !l.trustedProxy(rateLimitPeerIP(r)) && l.clientIP(r).IsLoopback()
}

// rateLimitPrincipal uses only authenticated identity. Never key OIDC by the
// token string (refresh would replenish the quota) or an unverified claim/header.
// API-key mode has one configured shared key; hash it rather than retaining it
// in the limiter. mTLS without a CN falls back to the verified certificate.
func (s *Server) rateLimitPrincipal(r *http.Request) string {
	rc := RoleFromContext(r.Context())
	if rc == nil {
		return ""
	}
	switch s.auth.Mode {
	case AuthModeAPIKey:
		digest := sha256.Sum256([]byte(s.auth.APIKey))
		return "api_key:" + hex.EncodeToString(digest[:])
	case AuthModeOIDC:
		subject, ok := claimString(rc.ClaimsRaw, "sub")
		if !ok || subject == "" {
			// A verified token without a stable subject still has the IP
			// budget. Do not silently merge all such tokens into one user.
			return ""
		}
		return "oidc:" + s.auth.OIDC.Issuer + "\x00" + subject
	case AuthModeMTLS:
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			certificate := r.TLS.PeerCertificates[0]
			if cn := strings.TrimSpace(certificate.Subject.CommonName); cn != "" {
				// Even the literal CN "mtls-client" is a real identity. It
				// must not be confused with mtlsClientSubject's no-CN fallback
				// or certificate renewal could replenish this user's quota.
				return "mtls:" + cn
			}
			digest := sha256.Sum256(certificate.Raw)
			return "mtls-certificate:" + hex.EncodeToString(digest[:])
		}
		if rc.UserID != "" && rc.UserID != "mtls-client" {
			return "mtls:" + rc.UserID
		}
	}
	return "" // Local mode has no authenticated user; its IP budget is sufficient.
}

func (l *httpRateLimiter) admit(w http.ResponseWriter, r *http.Request, scope, identity string) bool {
	class := "http"
	if r.URL.Path == "/api/v1/ws" {
		class = "websocket_handshake"
	} else if r.URL.Path == "/api/v1/auth/ws-ticket" {
		// A single-use ticket expires quickly. Sharing its bucket with the
		// upgrade can make Burst=1 impossible to use at low refill rates:
		// issuance would spend the credit needed to redeem it before expiry.
		class = "websocket_ticket"
	}
	key := sha256.Sum256([]byte(class + "\x00" + scope + "\x00" + identity))
	decision := l.take(key, l.now())
	// Publish the tighter of IP/principal budgets on successful responses.
	previous, err := strconv.Atoi(w.Header().Get("X-RateLimit-Remaining"))
	if !decision.allowed || err != nil || decision.remaining < previous {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.burst))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.resetAt.Unix(), 10))
	}
	if decision.allowed {
		return true
	}
	retrySeconds := max(1, int(math.Ceil(decision.retryIn.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
	w.Header().Set("Cache-Control", "no-store")
	writeErrorResponse(w, http.StatusTooManyRequests, ErrCodeRateLimited,
		"request rate limit exceeded", map[string]interface{}{
			"scope": scope, "retry_after_seconds": retrySeconds,
		}, requestIDFromContext(r.Context()))
	return false
}

func (l *httpRateLimiter) take(key [sha256.Size]byte, now time.Time) httpRateLimitDecision {
	shard := &l.shards[int(key[0])%len(l.shards)]
	shard.mu.Lock()
	defer shard.mu.Unlock()

	// Reclaim a bounded number of expired records on ordinary traffic, with
	// no sweeper goroutine or stop lifecycle. A short TTL cannot reset credit:
	// only fully replenished buckets qualify for expiration.
	for pruned := 0; pruned < 4; pruned++ {
		oldest := shard.lru.Back()
		if oldest == nil {
			break
		}
		bucket := oldest.Value.(*httpRateLimitBucket)
		if now.Sub(bucket.lastSeen) < l.idleTTL {
			break
		}
		l.refill(bucket, now)
		if bucket.tokens < float64(l.burst) {
			break
		}
		delete(shard.entries, bucket.key)
		shard.lru.Remove(oldest)
	}

	element, ok := shard.entries[key]
	if !ok {
		if len(shard.entries) >= shard.capacity {
			oldest := shard.lru.Back()
			bucket := oldest.Value.(*httpRateLimitBucket)
			l.refill(bucket, now)
			// Churning identities must not erase partially spent quotas.
			// IdleTTL is only a retention hint: even an expired bucket must
			// fully replenish before it can be evicted without minting credit.
			if bucket.tokens < float64(l.burst) {
				retry := time.Duration(math.Ceil((float64(l.burst) - bucket.tokens) / l.rate * float64(time.Second)))
				return httpRateLimitDecision{retryIn: retry, resetAt: now.Add(retry)}
			}
			delete(shard.entries, bucket.key)
			shard.lru.Remove(oldest)
		}
		bucket := &httpRateLimitBucket{key: key, tokens: float64(l.burst), updated: now, lastSeen: now}
		element = shard.lru.PushFront(bucket)
		shard.entries[key] = element
	}
	bucket := element.Value.(*httpRateLimitBucket)
	l.refill(bucket, now)
	bucket.lastSeen = now
	shard.lru.MoveToFront(element)
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	retry := time.Duration(0)
	if !allowed {
		retry = time.Duration(math.Ceil((1 - bucket.tokens) / l.rate * float64(time.Second)))
	}
	reset := time.Duration(math.Ceil((float64(l.burst) - bucket.tokens) / l.rate * float64(time.Second)))
	return httpRateLimitDecision{
		allowed: allowed, remaining: int(bucket.tokens), retryIn: retry, resetAt: now.Add(reset),
	}
}

func (l *httpRateLimiter) refill(bucket *httpRateLimitBucket, now time.Time) {
	if elapsed := now.Sub(bucket.updated); elapsed > 0 {
		bucket.tokens = math.Min(float64(l.burst), bucket.tokens+elapsed.Seconds()*l.rate)
		bucket.updated = now
	}
}

// clientIP leaves RemoteAddr intact for truthful audits. The rightmost
// untrusted XFF address wins only when the actual socket peer is trusted; a
// client-supplied leftmost address can never mint new buckets through a proxy.
func (l *httpRateLimiter) clientIP(r *http.Request) netip.Addr {
	peer := rateLimitPeerIP(r)
	if !l.trustedProxy(peer) {
		return peer
	}
	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded == "" || len(forwarded) > 4096 || strings.Count(forwarded, ",") >= 64 {
		return peer
	}
	addresses := strings.Split(forwarded, ",")
	current := peer
	for i := len(addresses) - 1; i >= 0; i-- {
		if !l.trustedProxy(current) {
			return current
		}
		next, err := netip.ParseAddr(strings.TrimSpace(addresses[i]))
		if err != nil || next.Zone() != "" {
			return peer // Malformed proxy metadata never provides an exemption.
		}
		current = next.Unmap()
	}
	return current
}

func rateLimitPeerIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}
	}
	return peer.Unmap().WithZone("")
}

func (l *httpRateLimiter) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
