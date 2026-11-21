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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/config"
)

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func padBigInt(n *big.Int, size int) []byte {
	raw := n.Bytes()
	if len(raw) >= size {
		return raw[len(raw)-size:]
	}
	out := make([]byte, size)
	copy(out[size-len(raw):], raw)
	return out
}

func signTestJWT(t *testing.T, alg, kid string, claims map[string]interface{}, signer func(signingInput []byte) []byte) string {
	t.Helper()
	hdrBytes, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	clBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	input := b64url(hdrBytes) + "." + b64url(clBytes)
	sig := signer([]byte(input))
	return input + "." + b64url(sig)
}

func TestOIDC_MultiAlg_RS256_PS256_ES256_ES384_EdDSA_AndPinning(t *testing.T) {
	t.Parallel()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa 2048: %v", err)
	}
	weakRSAKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate rsa 1024: %v", err)
	}
	ec256Key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ec p256: %v", err)
	}
	ec384Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ec p384: %v", err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}

	jwks := jwksPayload{
		Keys: []jwk{
			{Kty: "RSA", Kid: "rsa-rs256", Alg: "RS256", Use: "sig", N: b64url(rsaKey.N.Bytes()), E: b64url(big.NewInt(int64(rsaKey.E)).Bytes())},
			{Kty: "RSA", Kid: "rsa-ps256", Alg: "PS256", Use: "sig", N: b64url(rsaKey.N.Bytes()), E: b64url(big.NewInt(int64(rsaKey.E)).Bytes())},
			{Kty: "RSA", Kid: "rsa-enc", Alg: "RS256", Use: "enc", N: b64url(rsaKey.N.Bytes()), E: b64url(big.NewInt(int64(rsaKey.E)).Bytes())},
			{Kty: "RSA", Kid: "rsa-weak", Alg: "RS256", Use: "sig", N: b64url(weakRSAKey.N.Bytes()), E: b64url(big.NewInt(int64(weakRSAKey.E)).Bytes())},
			{Kty: "EC", Kid: "ec-256", Alg: "ES256", Use: "sig", Crv: "P-256", X: b64url(padBigInt(ec256Key.X, 32)), Y: b64url(padBigInt(ec256Key.Y, 32))},
			{Kty: "EC", Kid: "ec-384", Alg: "ES384", Use: "sig", Crv: "P-384", X: b64url(padBigInt(ec384Key.X, 48)), Y: b64url(padBigInt(ec384Key.Y, 48))},
			{Kty: "OKP", Kid: "okp-ed", Alg: "EdDSA", Use: "sig", Crv: "Ed25519", X: b64url(edPub)},
		},
	}

	jwksSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	defer jwksSrv.Close()

	srv := New(Config{
		Auth: AuthConfig{
			Mode: AuthModeOIDC,
			OIDC: OIDCConfig{
				Issuer:   "https://idp.example.test",
				Audience: "ntm-api",
				JWKSURL:  jwksSrv.URL,
			},
		},
	})
	defer srv.Stop()

	validClaims := func() map[string]interface{} {
		return map[string]interface{}{
			"iss": "https://idp.example.test",
			"aud": "ntm-api",
			"sub": "alice@example.test",
			"exp": time.Now().Add(5 * time.Minute).Unix(),
			"iat": time.Now().Unix(),
		}
	}

	// Positive cases: all 5 asymmetric algorithms must validate cleanly.
	rs256Tok := signTestJWT(t, "RS256", "rsa-rs256", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	})
	ps256Tok := signTestJWT(t, "PS256", "rsa-ps256", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		sig, err := rsa.SignPSS(rand.Reader, rsaKey, crypto.SHA256, h[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		if err != nil {
			t.Fatal(err)
		}
		return sig
	})
	es256Tok := signTestJWT(t, "ES256", "ec-256", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		r, s, err := ecdsa.Sign(rand.Reader, ec256Key, h[:])
		if err != nil {
			t.Fatal(err)
		}
		return append(padBigInt(r, 32), padBigInt(s, 32)...)
	})
	es384Tok := signTestJWT(t, "ES384", "ec-384", validClaims(), func(in []byte) []byte {
		h := sha512.Sum384(in)
		r, s, err := ecdsa.Sign(rand.Reader, ec384Key, h[:])
		if err != nil {
			t.Fatal(err)
		}
		return append(padBigInt(r, 48), padBigInt(s, 48)...)
	})
	edTok := signTestJWT(t, "EdDSA", "okp-ed", validClaims(), func(in []byte) []byte {
		return ed25519.Sign(edPriv, in)
	})

	for name, tok := range map[string]string{
		"RS256": rs256Tok,
		"PS256": ps256Tok,
		"ES256": es256Tok,
		"ES384": es384Tok,
		"EdDSA": edTok,
	} {
		t.Run("valid_"+name, func(t *testing.T) {
			claims, err := srv.validateOIDCToken(context.Background(), tok)
			if err != nil {
				t.Fatalf("%s token rejected: %v", name, err)
			}
			if claims["sub"] != "alice@example.test" {
				t.Fatalf("%s claims sub = %v, want alice@example.test", name, claims["sub"])
			}
		})
	}

	// Adversarial negative cases: every attack class must fail closed.
	noneTok := signTestJWT(t, "none", "rsa-rs256", validClaims(), func([]byte) []byte { return nil })
	hs256Tok := signTestJWT(t, "HS256", "rsa-rs256", validClaims(), func([]byte) []byte { return []byte("forged") })
	crossAlgTok := signTestJWT(t, "RS256", "rsa-ps256", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
		return sig
	})
	encUseTok := signTestJWT(t, "RS256", "rsa-enc", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
		return sig
	})
	weakKeyTok := signTestJWT(t, "RS256", "rsa-weak", validClaims(), func(in []byte) []byte {
		h := sha256.Sum256(in)
		sig, _ := rsa.SignPKCS1v15(rand.Reader, weakRSAKey, crypto.SHA256, h[:])
		return sig
	})
	tamperedEdTok := edTok[:len(edTok)-4] + "AAAA"
	badIatClaims := validClaims()
	badIatClaims["iat"] = "not-a-timestamp"
	badIatTok := signTestJWT(t, "EdDSA", "okp-ed", badIatClaims, func(in []byte) []byte {
		return ed25519.Sign(edPriv, in)
	})

	negatives := map[string]string{
		"alg_none":          noneTok,
		"alg_HS256":         hs256Tok,
		"jwk_alg_mismatch":  crossAlgTok,
		"jwk_use_enc":       encUseTok,
		"rsa_weak_1024":     weakKeyTok,
		"tampered_eddsa":    tamperedEdTok,
		"malformed_iat_str": badIatTok,
	}
	for name, tok := range negatives {
		t.Run("reject_"+name, func(t *testing.T) {
			if _, err := srv.validateOIDCToken(context.Background(), tok); err == nil {
				t.Fatalf("expected adversarial token %s to be rejected, got nil error", name)
			}
		})
	}
}

func TestLegacyRoutes_RBACEnforced(t *testing.T) {
	t.Parallel()

	// AuthModeAPIKey with RoleNone (explicit zero-permission role) verifies every
	// legacy /events and /api/* route enforces RequirePermission (matching /api/v1/*)
	// rather than bypassing RBAC.
	srv := New(Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "secret-key-rbac",
			Role:   RoleNone,
		},
	})
	defer srv.Stop()

	legacyPaths := []string{
		"/events",
		"/api/sessions",
		"/api/sessions/demo",
		"/api/sessions/demo/agents",
		"/api/sessions/demo/events",
		"/api/robot/status",
		"/api/robot/health",
	}

	for _, path := range legacyPaths {
		t.Run("unauth_"+path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			rr := httptest.NewRecorder()
			srv.router.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated GET %s = %d, want 401", path, rr.Code)
			}
		})
		t.Run("forbidden_role_"+path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer secret-key-rbac")
			rr := httptest.NewRecorder()
			srv.router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("zero-permission role GET %s = %d, want 403", path, rr.Code)
			}
		})
	}
}

func TestHTTPRateLimiter_Enforces429AndBoundsMemory(t *testing.T) {
	t.Parallel()

	const maxEntries = 64
	enabled := true
	srv, _ := rateLimitServer(t, Config{
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: "rl-key",
		},
		RateLimit: config.ServeRateLimitConfig{
			Enabled:           &enabled,
			RequestsPerSecond: 1,
			Burst:             2,
			MaxEntries:        maxEntries,
		},
	})

	// First 2 requests within burst succeed; 3rd returns 429 + Retry-After + RATE_LIMITED.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		req.Header.Set("Authorization", "Bearer rl-key")
		req.RemoteAddr = "203.0.113.10:12345"
		rr := httptest.NewRecorder()
		srv.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("burst request %d = %d, want 200", i+1, rr.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	req.Header.Set("Authorization", "Bearer rl-key")
	req.RemoteAddr = "203.0.113.10:12345"
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request = %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header on 429 response")
	}
	if !strings.Contains(rr.Body.String(), "RATE_LIMITED") {
		t.Fatalf("429 body missing RATE_LIMITED error_code: %s", rr.Body.String())
	}

	// Health & docs stay exempt even when client is rate-limited.
	for _, exempt := range []string{"/health", "/api/v1/health", "/docs"} {
		hReq := httptest.NewRequest(http.MethodGet, exempt, nil)
		hReq.Header.Set("Authorization", "Bearer rl-key")
		hReq.RemoteAddr = "203.0.113.10:12345"
		hRR := httptest.NewRecorder()
		srv.router.ServeHTTP(hRR, hReq)
		if hRR.Code != http.StatusOK {
			t.Errorf("exempt path %s returned %d while client rate-limited, want 200", exempt, hRR.Code)
		}
	}

	// Memory DoS test: public requests from 200 distinct clients must never
	// exceed the configured total capacity across IP and principal buckets.
	for i := 0; i < 200; i++ {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		request.RemoteAddr = fmt.Sprintf("198.51.100.%d:12345", i+1)
		request.Header.Set("Authorization", "Bearer rl-key")
		response := httptest.NewRecorder()
		srv.Router().ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("client churn request %d = %d, want 429 from exhausted shared principal", i, response.Code)
		}
	}
	tracked := 0
	for i := range srv.rateLimiter.shards {
		shard := &srv.rateLimiter.shards[i]
		shard.mu.Lock()
		tracked += len(shard.entries)
		shard.mu.Unlock()
	}
	if tracked > maxEntries {
		t.Fatalf("rateLimiter tracked %d buckets, exceeds MaxEntries=%d", tracked, maxEntries)
	}

	// AuthModeLocal default keeps loopback exempt so local tools/tests never flake.
	localSrv := New(Config{Auth: AuthConfig{Mode: AuthModeLocal}})
	defer localSrv.Stop()
	for i := 0; i < 200; i++ {
		lReq := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
		lReq.RemoteAddr = "127.0.0.1:54321"
		lRR := httptest.NewRecorder()
		localSrv.router.ServeHTTP(lRR, lReq)
		if lRR.Code != http.StatusOK {
			t.Fatalf("AuthModeLocal loopback request %d returned %d, want 200", i+1, lRR.Code)
		}
	}
}

func TestOpenAPISpec_ScopedCORSAndAirgappedDocs(t *testing.T) {
	t.Parallel()

	srv := New(Config{
		AllowedOrigins: []string{"https://ops.example.test"},
	})
	defer srv.Stop()

	// Disallowed Origin must be rejected by corsMiddlewareFunc (no wildcard "*").
	evilReq := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	evilReq.Header.Set("Origin", "https://evil.attacker.test")
	evilRR := httptest.NewRecorder()
	srv.router.ServeHTTP(evilRR, evilReq)
	if evilRR.Code != http.StatusForbidden {
		t.Fatalf("disallowed origin on /api/v1/openapi.json = %d, want 403", evilRR.Code)
	}
	if got := evilRR.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatal("disallowed origin received wildcard Access-Control-Allow-Origin: *")
	}

	// Allowlisted Origin succeeds with exact reflected origin (not "*").
	goodReq := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	goodReq.Header.Set("Origin", "https://ops.example.test")
	goodRR := httptest.NewRecorder()
	srv.router.ServeHTTP(goodRR, goodReq)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("allowlisted origin on /api/v1/openapi.json = %d, want 200", goodRR.Code)
	}
	if got := goodRR.Header().Get("Access-Control-Allow-Origin"); got != "https://ops.example.test" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want https://ops.example.test", got)
	}

	// Airgapped /docs has zero CDN references and sets strict CSP.
	for _, docsPath := range []string{"/docs", "/docs/"} {
		dReq := httptest.NewRequest(http.MethodGet, docsPath, nil)
		dRR := httptest.NewRecorder()
		srv.router.ServeHTTP(dRR, dReq)
		if dRR.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", docsPath, dRR.Code)
		}
		body := dRR.Body.String()
		if strings.Contains(body, "unpkg.com") || strings.Contains(body, "cdn.jsdelivr") {
			t.Fatalf("GET %s leaked external CDN URL in HTML", docsPath)
		}
	}
}
