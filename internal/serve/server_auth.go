package serve

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported jwt alg %q", header.Alg)
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
	if time.Now().After(time.Unix(exp, 0).Add(30 * time.Second)) {
		return nil, fmt.Errorf("token expired")
	}
	// nbf is optional, but a present-and-unparseable nbf is a malformed token,
	// not an absent claim; do not silently skip the check for it.
	if raw, present := claims["nbf"]; present {
		nbf, ok := claimInt64(claims, "nbf")
		if !ok {
			return nil, fmt.Errorf("token has an unusable nbf claim of type %T", raw)
		}
		if time.Now().Before(time.Unix(nbf, 0).Add(-30 * time.Second)) {
			return nil, fmt.Errorf("token not yet valid")
		}
	}
	key, err := s.jwksCache.getKey(ctx, s.auth.OIDC.JWKSURL, header.Kid)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature); err != nil {
		return nil, fmt.Errorf("invalid token signature")
	}
	return claims, nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
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
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case json.Number:
		val, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return val, true
	default:
		return 0, false
	}
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

type jwksCache struct {
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	ttl       time.Duration
}

func newJWKSCache(ttl time.Duration) *jwksCache {
	if ttl <= 0 {
		ttl = defaultJWKSCacheTTL
	}
	return &jwksCache{
		keys: make(map[string]*rsa.PublicKey),
		ttl:  ttl,
	}
}

func (c *jwksCache) getKey(ctx context.Context, jwksURL, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	if time.Since(c.fetchedAt) < c.ttl && len(c.keys) > 0 {
		if kid == "" && len(c.keys) == 1 {
			for _, key := range c.keys {
				c.mu.Unlock()
				return key, nil
			}
		}
		if key, ok := c.keys[kid]; ok {
			c.mu.Unlock()
			return key, nil
		}
	}
	c.mu.Unlock()

	keys, err := fetchJWKSKeys(ctx, jwksURL)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.keys = keys
	c.fetchedAt = time.Now()
	c.mu.Unlock()

	if kid == "" && len(keys) == 1 {
		for _, key := range keys {
			return key, nil
		}
	}
	key, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("jwt kid not found in jwks")
	}
	return key, nil
}

type jwksPayload struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func fetchJWKSKeys(ctx context.Context, jwksURL string) (map[string]*rsa.PublicKey, error) {
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read jwks: %w", err)
	}
	var payload jwksPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, key := range payload.Keys {
		if key.Kty != "RSA" || key.N == "" || key.E == "" {
			continue
		}
		pub, err := parseRSAPublicKey(key.N, key.E)
		if err != nil {
			continue
		}
		kid := key.Kid
		if kid == "" {
			kid = "default"
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid RSA keys in jwks")
	}
	return keys, nil
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
	if e.Sign() <= 0 {
		return nil, fmt.Errorf("invalid jwk exponent")
	}
	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}
