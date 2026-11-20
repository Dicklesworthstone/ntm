package serve

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// AuthMode configures authentication for the server.
type AuthMode string

const (
	AuthModeLocal  AuthMode = "local"
	AuthModeAPIKey AuthMode = "api_key"
	AuthModeOIDC   AuthMode = "oidc"
	AuthModeMTLS   AuthMode = "mtls"
)

// AuthConfig holds server authentication configuration.
type AuthConfig struct {
	Mode   AuthMode
	APIKey string
	OIDC   OIDCConfig
	MTLS   MTLSConfig
	// Role is granted to callers authenticated by a shared credential that
	// carries no role of its own — the api_key and mtls modes. Both mean "the
	// operator's own credential", so it defaults to RoleAdmin; set it lower to
	// restrict what a shared key or client certificate may do. It does not apply
	// to oidc, where the token's own claims decide.
	Role Role
}

// sharedCredentialRole resolves the role granted to api_key and mtls callers.
func (a AuthConfig) sharedCredentialRole() Role {
	if a.Role == "" {
		return RoleAdmin
	}
	return a.Role
}

// OIDCConfig configures OIDC/JWT verification for API access.
type OIDCConfig struct {
	Issuer   string
	Audience string
	JWKSURL  string
	CacheTTL time.Duration
}

// MTLSConfig configures mutual TLS for API access.
type MTLSConfig struct {
	CertFile     string
	KeyFile      string
	ClientCAFile string
	ClientCN     string
}

func ParseAuthMode(raw string) (AuthMode, error) {
	mode := AuthMode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case "", AuthModeLocal:
		return AuthModeLocal, nil
	case AuthModeAPIKey, AuthModeOIDC, AuthModeMTLS:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid auth mode %q (valid: local, api_key, oidc, mtls)", raw)
	}
}

func (s *Server) buildMTLSConfig() (*tls.Config, error) {
	if s.auth.MTLS.CertFile == "" || s.auth.MTLS.KeyFile == "" || s.auth.MTLS.ClientCAFile == "" {
		return nil, fmt.Errorf("mtls requires cert, key, and client CA files")
	}
	caPEM, err := os.ReadFile(s.auth.MTLS.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read mtls CA: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse mtls CA: no certs found")
	}
	tlsCfg := &tls.Config{
		ClientCAs:  caPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
		MinVersion: tls.VersionTLS12,
	}
	if expectedCN := strings.TrimSpace(s.auth.MTLS.ClientCN); expectedCN != "" {
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("mtls client certificate required")
			}
			gotCN := cs.PeerCertificates[0].Subject.CommonName
			if gotCN != expectedCN {
				return fmt.Errorf("mtls client certificate CN %q does not match required %q", gotCN, expectedCN)
			}
			return nil
		}
	}
	return tlsCfg, nil
}

// corsMiddlewareFunc is the chi middleware version.
func (s *Server) corsMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			inspection := scanRequestMetadata(origin, s.requestRedactionConfig(r))
			if inspection.Blocked {
				message := "origin contains sensitive content"
				if inspection.LimitExceeded {
					message = "origin could not be completely inspected within redaction limits"
				}
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden,
					message, nil, requestIDFromContext(r.Context()))
				return
			}
			s.mu.RLock()
			allowed := originAllowed(origin, s.corsAllowedOrigins)
			s.mu.RUnlock()
			if !allowed {
				reqID := requestIDFromContext(r.Context())
				writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "origin not allowed", nil, reqID)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Idempotency-Key, "+requestIDHeader)
			w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, "+requestIDHeader)
		}

		if (s.auth.Mode == AuthModeLocal || s.auth.Mode == "") &&
			strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			reqID := requestIDFromContext(r.Context())
			writeErrorResponse(w, http.StatusForbidden, ErrCodeForbidden, "cross-site browser request rejected in local auth mode", nil, reqID)
			return
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) isPublicUnauthenticatedRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	if p == "/health" || p == "/docs" || p == "/docs/" {
		return true
	}
	if s.webUI != nil && !isReservedAPIPath(p) {
		return true
	}
	return false
}

// authMiddlewareFunc is the chi middleware version.
func (s *Server) authMiddlewareFunc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || s.isPublicUnauthenticatedRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		claims, err := s.authenticateRequest(r)
		if err != nil {
			reqID := requestIDFromContext(r.Context())
			cfg := s.requestRedactionConfig(r)
			log.Printf("auth failed mode=%s path=%s remote=%s request_id=%s err=%s", s.auth.Mode,
				redactRequestDiagnostic(r.URL.Path, cfg), redactRequestDiagnostic(r.RemoteAddr, cfg),
				reqID, redactRequestDiagnostic(err.Error(), cfg))
			writeErrorResponse(w, http.StatusUnauthorized, ErrCodeUnauthorized, "unauthorized", nil, reqID)
			return
		}

		if claims != nil {
			ctx := context.WithValue(r.Context(), authContextKey, claims)
			r = r.WithContext(ctx)
		}

		next.ServeHTTP(w, r)
	})
}

const (
	wsTicketTTL        = 30 * time.Second
	maxActiveWSTickets = 4096
)

type wsTicketRecord struct {
	claims    map[string]interface{}
	principal string
	expiresAt time.Time
}

func cloneAuthClaims(src map[string]interface{}) map[string]interface{} {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (s *Server) handleIssueWSTicketV1(w http.ResponseWriter, r *http.Request) {
	reqID := requestIDFromContext(r.Context())
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeErrorResponse(w, http.StatusInternalServerError, ErrCodeInternalError, "failed to generate websocket ticket", nil, reqID)
		return
	}
	ticket := hex.EncodeToString(raw[:])

	var claims map[string]interface{}
	if existing, ok := r.Context().Value(authContextKey).(map[string]interface{}); ok {
		claims = cloneAuthClaims(existing)
	}
	principal := "local"
	if rc := RoleFromContext(r.Context()); rc != nil {
		if rc.UserID != "" {
			principal = rc.UserID
		}
		if claims == nil {
			claims = map[string]interface{}{
				"role": string(rc.Role),
				"sub":  principal,
			}
		}
	}
	if claims == nil {
		claims = map[string]interface{}{"role": string(RoleAdmin)}
	}

	now := time.Now().UTC()
	s.wsTicketsMu.Lock()
	if s.wsTickets == nil {
		s.wsTickets = make(map[string]wsTicketRecord)
	}
	for k, rec := range s.wsTickets {
		if !now.Before(rec.expiresAt) {
			delete(s.wsTickets, k)
		}
	}
	if len(s.wsTickets) >= maxActiveWSTickets {
		for k := range s.wsTickets {
			delete(s.wsTickets, k)
			if len(s.wsTickets) < maxActiveWSTickets {
				break
			}
		}
	}
	s.wsTickets[ticket] = wsTicketRecord{
		claims:    claims,
		principal: principal,
		expiresAt: now.Add(wsTicketTTL),
	}
	s.wsTicketsMu.Unlock()

	slog.Info("ws ticket issued", "auth_mode", s.auth.Mode, "principal", principal, "expires_in_sec", int(wsTicketTTL/time.Second), "request_id", reqID)
	writeSuccessResponse(w, http.StatusOK, map[string]interface{}{
		"ticket":         ticket,
		"expires_in_sec": int(wsTicketTTL / time.Second),
	}, reqID)
}

func (s *Server) redeemWSTicket(ticket string) (wsTicketRecord, bool) {
	now := time.Now().UTC()
	s.wsTicketsMu.Lock()
	defer s.wsTicketsMu.Unlock()
	if len(s.wsTickets) == 0 {
		return wsTicketRecord{}, false
	}
	rec, ok := s.wsTickets[ticket]
	delete(s.wsTickets, ticket)
	if !ok || !now.Before(rec.expiresAt) {
		return wsTicketRecord{}, false
	}
	return rec, true
}

func (s *Server) authenticateRequest(r *http.Request) (map[string]interface{}, error) {
	if r != nil && r.URL != nil && (r.Method == http.MethodGet || r.Method == "") && r.URL.Path == "/api/v1/ws" {
		if ticket := strings.TrimSpace(r.URL.Query().Get("ticket")); ticket != "" {
			q := r.URL.Query()
			q.Del("ticket")
			r.URL.RawQuery = q.Encode()
			rec, ok := s.redeemWSTicket(ticket)
			if !ok {
				return nil, errors.New("invalid or expired websocket ticket")
			}
			slog.Info("ws ticket redeemed",
				"auth_mode", s.auth.Mode,
				"principal", rec.principal,
				"ticket_redeemed", true,
				"request_id", requestIDFromContext(r.Context()),
			)
			return cloneAuthClaims(rec.claims), nil
		}
	}
	// Every successful path must return claims carrying a role. Returning nil
	// claims left RBAC with an empty claim set, and extractRoleFromClaims only
	// short-circuits to admin for local mode — so api_key and mtls callers fell
	// through to RoleViewer and every mutating endpoint answered 403. Enabling
	// authentication must not disable the write API.
	switch s.auth.Mode {
	case AuthModeAPIKey:
		if err := s.authenticateAPIKey(r); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"role": string(s.auth.sharedCredentialRole()),
			"sub":  "api-key",
		}, nil
	case AuthModeOIDC:
		return s.authenticateOIDC(r)
	case AuthModeMTLS:
		if err := s.authenticateMTLS(r); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"role": string(s.auth.sharedCredentialRole()),
			"sub":  mtlsClientSubject(r),
		}, nil
	case AuthModeLocal, "":
		return map[string]interface{}{"role": string(RoleAdmin)}, nil
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", s.auth.Mode)
	}
}

// mtlsClientSubject names the authenticated client for audit and RBAC context.
func mtlsClientSubject(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "mtls-client"
	}
	if cn := strings.TrimSpace(r.TLS.PeerCertificates[0].Subject.CommonName); cn != "" {
		return cn
	}
	return "mtls-client"
}

func (s *Server) authenticateAPIKey(r *http.Request) error {
	if s.auth.APIKey == "" {
		return errors.New("api key not configured")
	}
	key := extractAPIKey(r)
	if key == "" {
		return errors.New("missing api key")
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.auth.APIKey)) != 1 {
		return errors.New("invalid api key")
	}
	return nil
}

func (s *Server) authenticateOIDC(r *http.Request) (map[string]interface{}, error) {
	token := extractBearerToken(r)
	if token == "" {
		return nil, errors.New("missing bearer token")
	}
	return s.validateOIDCToken(r.Context(), token)
}

func (s *Server) authenticateMTLS(r *http.Request) error {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return errors.New("missing client certificate")
	}
	return nil
}

// extractAuthClaims extracts auth claims from the request context.
func extractAuthClaims(r *http.Request) map[string]interface{} {
	// If using OIDC, extract claims from verified token
	claims := make(map[string]interface{})
	if authCtx := r.Context().Value(authContextKey); authCtx != nil {
		if m, ok := authCtx.(map[string]interface{}); ok {
			claims = m
		}
	}
	return claims
}

// authContextKey is the context key for auth claims.
type ctxKeyAuth struct{}

var authContextKey = ctxKeyAuth{}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.Fields(auth)
	if len(parts) != 2 {
		return ""
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func extractAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return extractBearerToken(r)
}

func originAllowed(origin string, allowlist []string) bool {
	if origin == "" {
		return true
	}
	if len(allowlist) == 0 {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			return true
		}
		if strings.Contains(allowed, "://") {
			allowedURL, err := url.Parse(allowed)
			if err != nil {
				continue
			}
			if strings.EqualFold(allowedURL.Scheme, u.Scheme) && strings.EqualFold(allowedURL.Hostname(), host) {
				if allowedURL.Port() == "" || allowedURL.Port() == u.Port() {
					return true
				}
			}
			continue
		}
		if strings.Contains(allowed, ":") {
			if strings.EqualFold(allowed, u.Host) {
				return true
			}
			continue
		}
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return true
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = strings.TrimPrefix(strings.TrimSuffix(h, "]"), "[")
	}
	if strings.Contains(h, ":") {
		if hostOnly, _, err := net.SplitHostPort(h); err == nil {
			h = hostOnly
		}
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func (s *Server) validateOIDCToken(ctx context.Context, token string) (map[string]interface{}, error) {
	if s.auth.OIDC.JWKSURL == "" || s.auth.OIDC.Issuer == "" {
		return nil, errors.New("oidc config incomplete")
	}
	header, claims, signingInput, signature, err := parseJWT(token)
	if err != nil {
		return nil, err
	}
	switch header.Alg {
	case "RS256", "PS256", "ES256", "ES384", "EdDSA":
	default:
		return nil, fmt.Errorf("unsupported jwt alg %q", header.Alg)
	}
	if len(header.Crit) != 0 || (header.B64 != nil && !*header.B64) {
		return nil, errors.New("unsupported jwt header extension")
	}
	if iss, ok := claimString(claims, "iss"); !ok || iss != s.auth.OIDC.Issuer {
		return nil, fmt.Errorf("invalid issuer")
	}
	if s.auth.OIDC.Audience != "" && !claimAudienceContains(claims, s.auth.OIDC.Audience) {
		return nil, fmt.Errorf("invalid audience")
	}
	// exp is mandatory and must be fail-CLOSED. Treating it as optional made a
	// token with no exp claim — or one whose exp an IdP serialized as a JSON
	// string rather than a number, which claimInt64 reports identically to
	// "absent" — authenticate forever. Only JWKS keys are re-fetched, so
	// revocation at the IdP never reaches such a token: it stays a permanent
	// credential for every write route on this server.
	exp, ok := claimInt64(claims, "exp")
	if !ok {
		return nil, fmt.Errorf("token has no usable exp claim")
	}
	// Compare Unix seconds directly. Converting an arbitrary NumericDate
	// through time.Unix can overflow Time's internal epoch for large values.
	now := time.Now()
	if exp <= now.Add(-30*time.Second).Unix() {
		return nil, fmt.Errorf("token expired")
	}
	// nbf is optional, but a present-and-unparseable nbf is a malformed token,
	// not an absent claim; do not silently skip the check for it.
	if raw, present := claims["nbf"]; present {
		nbf, ok := claimInt64(claims, "nbf")
		if !ok {
			return nil, fmt.Errorf("token has an unusable nbf claim of type %T", raw)
		}
		if nbf > now.Add(30*time.Second).Unix() {
			return nil, fmt.Errorf("token not yet valid")
		}
	}
	// iat is optional, but when supplied it must be a NumericDate just like
	// exp and nbf. Preserve the provider's issuance time without accepting
	// malformed claim types as though the claim were absent.
	if raw, present := claims["iat"]; present {
		if _, ok := claimInt64(claims, "iat"); !ok {
			return nil, fmt.Errorf("token has an unusable iat claim of type %T", raw)
		}
	}
	key, err := s.jwksCache.getKey(ctx, s.auth.OIDC.JWKSURL, header.Kid)
	if err != nil {
		return nil, err
	}
	if err := key.verify(header.Alg, []byte(signingInput), signature); err != nil {
		return nil, fmt.Errorf("invalid token signature")
	}
	return claims, nil
}

type jwtHeader struct {
	Alg  string   `json:"alg"`
	Kid  string   `json:"kid"`
	Crit []string `json:"crit"`
	B64  *bool    `json:"b64"`
}

func parseJWT(token string) (jwtHeader, map[string]interface{}, string, []byte, error) {
	var header jwtHeader
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return header, nil, "", nil, fmt.Errorf("invalid jwt format")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt header: %w", err)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return header, nil, "", nil, fmt.Errorf("decode jwt signature: %w", err)
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return header, nil, "", nil, fmt.Errorf("parse jwt header: %w", err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return header, nil, "", nil, fmt.Errorf("parse jwt payload: %w", err)
	}
	return header, claims, parts[0] + "." + parts[1], signature, nil
}

func claimString(claims map[string]interface{}, key string) (string, bool) {
	raw, ok := claims[key]
	if !ok {
		return "", false
	}
	str, ok := raw.(string)
	return str, ok
}

func claimInt64(claims map[string]interface{}, key string) (int64, bool) {
	raw, ok := claims[key]
	if !ok {
		return 0, false
	}
	var number float64
	switch v := raw.(type) {
	case float64:
		number = v
	case json.Number:
		if val, err := v.Int64(); err == nil {
			return val, true
		}
		// NumericDate permits fractional seconds. Accept those without an
		// unchecked float-to-int conversion, including if decoding uses
		// json.Decoder.UseNumber in the future.
		var err error
		number, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	// The upper bound is exclusive: float64 rounds MaxInt64 up to 2^63.
	// Both infinities are rejected by these bounds; NaN needs its own check.
	if math.IsNaN(number) || number < -0x1p63 || number >= 0x1p63 {
		return 0, false
	}
	return int64(number), true
}

func claimAudienceContains(claims map[string]interface{}, expected string) bool {
	raw, ok := claims["aud"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case string:
		return v == expected
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	}
	return false
}

// jwksKey keeps the provider's algorithm restriction alongside the key. The
// token's untrusted alg header can select only an algorithm allowed by BOTH the
// JWK metadata and the public key's concrete type/curve.
type jwksKey struct {
	publicKey crypto.PublicKey
	alg       string
}

func (k *jwksKey) verify(alg string, message, signature []byte) error {
	if k == nil || k.alg != alg {
		return errors.New("jwt algorithm does not match jwk")
	}
	switch alg {
	case "RS256", "PS256":
		key, ok := k.publicKey.(*rsa.PublicKey)
		if !ok {
			return errors.New("jwt requires an RSA key")
		}
		digest := sha256.Sum256(message)
		if alg == "PS256" {
			// JOSE requires a salt exactly as long as the hash (RFC 7518 3.5),
			// whereas Go's default verifier accepts arbitrary salt lengths.
			return rsa.VerifyPSS(key, crypto.SHA256, digest[:], signature, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		}
		return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature)
	case "ES256", "ES384":
		key, ok := k.publicKey.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("jwt requires an EC key")
		}
		var digest []byte
		var size int
		if alg == "ES256" && key.Curve == elliptic.P256() {
			hash := sha256.Sum256(message)
			digest, size = hash[:], 32
		} else if alg == "ES384" && key.Curve == elliptic.P384() {
			hash := sha512.Sum384(message)
			digest, size = hash[:], 48
		} else {
			return errors.New("jwt algorithm does not match EC curve")
		}
		// JWS uses fixed-width R || S, not the ASN.1 encoding used by TLS.
		if len(signature) != 2*size {
			return errors.New("invalid ECDSA signature size")
		}
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])
		if !ecdsa.Verify(key, digest, r, s) {
			return errors.New("invalid ECDSA signature")
		}
		return nil
	case "EdDSA":
		key, ok := k.publicKey.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return errors.New("jwt requires an Ed25519 key")
		}
		if !ed25519.Verify(key, message, signature) {
			return errors.New("invalid Ed25519 signature")
		}
		return nil
	default:
		return errors.New("unsupported jwt algorithm")
	}
}

type jwksCache struct {
	mu          sync.Mutex
	keys        map[string]*jwksKey
	sourceURL   string
	fetchedAt   time.Time
	ttl         time.Duration
	refreshDone chan struct{}
	attemptURL  string
	lastAttempt time.Time
	lastError   error
}

func newJWKSCache(ttl time.Duration) *jwksCache {
	if ttl <= 0 {
		ttl = defaultJWKSCacheTTL
	}
	return &jwksCache{
		keys: make(map[string]*jwksKey),
		ttl:  ttl,
	}
}

func (c *jwksCache) getKey(ctx context.Context, jwksURL, kid string) (*jwksKey, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		fresh := c.sourceURL == jwksURL && time.Since(c.fetchedAt) < c.ttl
		if fresh {
			if key, err := selectJWKSKey(c.keys, kid); err == nil {
				c.mu.Unlock()
				return key, nil
			}
		}
		if done := c.refreshDone; done != nil {
			c.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// Unknown kids must not turn arbitrary tokens into an unbounded stream
		// of requests to the identity provider. A short refresh cooldown still
		// permits key rotation without waiting for the full cache TTL.
		if c.attemptURL == jwksURL && (fresh || c.lastError != nil) && time.Since(c.lastAttempt) < min(5*time.Second, c.ttl) {
			err := c.lastError
			if err == nil {
				_, err = selectJWKSKey(c.keys, kid)
			}
			c.mu.Unlock()
			return nil, err
		}
		done := make(chan struct{})
		c.refreshDone = done
		c.mu.Unlock()

		keys, err := fetchJWKSKeys(ctx, jwksURL)
		c.mu.Lock()
		if ctx.Err() == nil {
			c.attemptURL = jwksURL
			c.lastAttempt = time.Now()
			c.lastError = err
		}
		if err == nil {
			c.keys = keys
			c.sourceURL = jwksURL
			c.fetchedAt = time.Now()
		}
		c.refreshDone = nil
		close(done)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return selectJWKSKey(keys, kid)
	}
}

func selectJWKSKey(keys map[string]*jwksKey, kid string) (*jwksKey, error) {
	if kid == "" {
		if len(keys) != 1 {
			return nil, errors.New("jwt kid required for ambiguous jwks")
		}
		for _, key := range keys {
			return key, nil
		}
	}
	key, ok := keys[kid]
	if !ok {
		return nil, errors.New("jwt kid not found in jwks")
	}
	return key, nil
}

type jwksPayload struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Alg    string   `json:"alg"`
	Use    string   `json:"use"`
	KeyOps []string `json:"key_ops"`
	Crv    string   `json:"crv"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
}

func fetchJWKSKeys(ctx context.Context, jwksURL string) (map[string]*jwksKey, error) {
	if jwksURL == "" {
		return nil, fmt.Errorf("jwks url missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build jwks request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024)) // Read small error snippet
		return nil, fmt.Errorf("fetch jwks: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Limit JWKS to 1MB to prevent memory exhaustion DoS
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	if len(body) > 1024*1024 {
		return nil, errors.New("jwks exceeds maximum size")
	}
	var payload jwksPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	keys := make(map[string]*jwksKey)
	for _, key := range payload.Keys {
		pub, err := parseSigningJWK(key)
		if err != nil {
			continue
		}
		if _, duplicate := keys[key.Kid]; duplicate {
			return nil, errors.New("ambiguous duplicate kid in jwks")
		}
		keys[key.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("no valid signing keys in jwks")
	}
	return keys, nil
}

func parseSigningJWK(key jwk) (*jwksKey, error) {
	if key.Use != "" && key.Use != "sig" {
		return nil, errors.New("jwk is not a signing key")
	}
	if key.KeyOps != nil {
		canVerify, canSign := false, false
		for _, op := range key.KeyOps {
			switch op {
			case "verify":
				if canVerify {
					return nil, errors.New("duplicate jwk key operation")
				}
				canVerify = true
			case "sign":
				if canSign {
					return nil, errors.New("duplicate jwk key operation")
				}
				canSign = true
			default:
				return nil, errors.New("jwk key operation is not for signing")
			}
		}
		if !canVerify {
			return nil, errors.New("jwk does not allow verification")
		}
	}
	var publicKey crypto.PublicKey
	alg := key.Alg
	switch key.Kty {
	case "RSA":
		if key.Alg != "" && key.Alg != "RS256" && key.Alg != "PS256" {
			return nil, errors.New("jwk algorithm does not match RSA key")
		}
		// Bind even unannotated keys to one algorithm (RFC 8725 3.1).
		// RS256 is the established OIDC default here; a provider using PSS
		// must publish alg=PS256 so the token cannot choose the key's mode.
		if alg == "" {
			alg = "RS256"
		}
		pub, err := parseRSAPublicKey(key.N, key.E)
		if err != nil {
			return nil, err
		}
		publicKey = pub
	case "EC":
		var curve elliptic.Curve
		var curveAlg string
		switch key.Crv {
		case "P-256":
			curve, curveAlg = elliptic.P256(), "ES256"
		case "P-384":
			curve, curveAlg = elliptic.P384(), "ES384"
		default:
			return nil, errors.New("unsupported jwk curve")
		}
		if alg != "" && alg != curveAlg {
			return nil, errors.New("jwk algorithm does not match EC curve")
		}
		alg = curveAlg
		x, errX := base64.RawURLEncoding.DecodeString(key.X)
		y, errY := base64.RawURLEncoding.DecodeString(key.Y)
		size := (curve.Params().BitSize + 7) / 8
		if errX != nil || errY != nil || len(x) != size || len(y) != size {
			return nil, errors.New("invalid jwk EC coordinates")
		}
		pub := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !curve.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("jwk EC point is not on curve")
		}
		publicKey = pub
	case "OKP":
		if key.Crv != "Ed25519" || (key.Alg != "" && key.Alg != "EdDSA") {
			return nil, errors.New("unsupported jwk OKP curve or algorithm")
		}
		alg = "EdDSA"
		x, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("invalid jwk Ed25519 public key")
		}
		publicKey = ed25519.PublicKey(x)
	default:
		return nil, errors.New("unsupported jwk key type")
	}
	return &jwksKey{publicKey: publicKey, alg: alg}, nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode jwk n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode jwk e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if len(eBytes) == 0 || eBytes[0] == 0 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 || e.Bit(0) == 0 {
		return nil, fmt.Errorf("invalid jwk exponent")
	}
	if len(nBytes) == 0 || nBytes[0] == 0 || n.BitLen() < 2048 || n.Bit(0) == 0 {
		return nil, errors.New("invalid jwk RSA modulus (at least 2048 bits required)")
	}
	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}
