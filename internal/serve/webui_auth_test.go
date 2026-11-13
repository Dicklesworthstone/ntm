package serve

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestParallelServersProjectionStoreIsolation(t *testing.T) {
	prepareRobotAPIContractEnv(t)

	const serverCount = 4
	servers := make([]*Server, serverCount)
	stores := make([]*state.Store, serverCount)

	for i := 0; i < serverCount; i++ {
		dbPath := filepath.Join(t.TempDir(), fmt.Sprintf("srv-%d.db", i))
		st, err := state.Open(dbPath)
		if err != nil {
			t.Fatalf("open store %d: %v", i, err)
		}
		if err := st.Migrate(); err != nil {
			st.Close()
			t.Fatalf("migrate store %d: %v", i, err)
		}
		idx := i
		t.Cleanup(func() { _ = st.Close() })

		now := time.Now().UTC()
		staleAfter := now.Add(10 * time.Minute)
		sessionName := fmt.Sprintf("isolated-session-%d", idx)
		if err := st.UpsertRuntimeSession(&state.RuntimeSession{
			Name:        sessionName,
			ProjectPath: t.TempDir(),
			Attached:    idx%2 == 0,
			WindowCount: 1,
			PaneCount:   1,
			AgentCount:  1,
			CollectedAt: now,
			StaleAfter:  staleAfter,
		}); err != nil {
			t.Fatalf("seed runtime session %d: %v", idx, err)
		}
		if err := st.CreateIncident(&state.Incident{
			ID:           fmt.Sprintf("inc-srv-%d", idx),
			Title:        fmt.Sprintf("Incident %d", idx),
			Fingerprint:  fmt.Sprintf("fp-srv-%d", idx),
			Family:       "agent",
			Category:     "stall",
			Status:       state.IncidentStatusInvestigating,
			Severity:     state.SeverityError,
			SessionNames: fmt.Sprintf("[%q]", sessionName),
			StartedAt:    now,
			LastEventAt:  now,
		}); err != nil {
			t.Fatalf("seed incident %d: %v", idx, err)
		}

		srv := New(Config{
			Port:       0,
			EventBus:   events.NewEventBus(16),
			StateStore: st,
		})
		servers[i] = srv
		stores[i] = st
	}

	var wg sync.WaitGroup
	errCh := make(chan error, serverCount*24)

	for i := 0; i < serverCount; i++ {
		srvIdx := i
		srv := servers[srvIdx]
		wantSession := fmt.Sprintf("isolated-session-%d", srvIdx)
		wantIncident := fmt.Sprintf("inc-srv-%d", srvIdx)

		for iter := 0; iter < 8; iter++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				for _, path := range []string{"/api/v1/robot/status", "/api/robot/status"} {
					req := httptest.NewRequest(http.MethodGet, path, nil)
					rec := httptest.NewRecorder()
					srv.Router().ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						errCh <- fmt.Errorf("server %d %s status=%d body=%s", srvIdx, path, rec.Code, rec.Body.String())
						return
					}
					body := rec.Body.String()
					if !strings.Contains(body, wantSession) {
						errCh <- fmt.Errorf("server %d %s missing own session %q in %s", srvIdx, path, wantSession, body)
						return
					}
					for other := 0; other < serverCount; other++ {
						if other == srvIdx {
							continue
						}
						otherSess := fmt.Sprintf("isolated-session-%d", other)
						if strings.Contains(body, otherSess) {
							errCh <- fmt.Errorf("server %d %s leaked server %d session %q", srvIdx, path, other, otherSess)
							return
						}
					}
				}

				reqSnap := httptest.NewRequest(http.MethodGet, "/api/v1/robot/snapshot", nil)
				recSnap := httptest.NewRecorder()
				srv.Router().ServeHTTP(recSnap, reqSnap)
				if recSnap.Code != http.StatusOK {
					errCh <- fmt.Errorf("server %d snapshot status=%d", srvIdx, recSnap.Code)
					return
				}
				snapBody := recSnap.Body.String()
				for other := 0; other < serverCount; other++ {
					if other == srvIdx {
						continue
					}
					otherInc := fmt.Sprintf("inc-srv-%d", other)
					if strings.Contains(snapBody, otherInc) {
						errCh <- fmt.Errorf("server %d snapshot leaked server %d incident %q (wanted only %q)", srvIdx, other, otherInc, wantIncident)
						return
					}
				}
			}()
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	_ = stores
}

func TestLocalModeSecurityLockdown(t *testing.T) {
	srv, store := setupTestServer(t)

	// 1. Runtime allowed_origins mutation in AuthModeLocal must fail closed (403) and record audit decision.
	reqOrigins := httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(`{"allowed_origins":["https://attacker.invalid"]}`))
	reqOrigins.Header.Set("Content-Type", "application/json")
	recOrigins := httptest.NewRecorder()
	srv.Router().ServeHTTP(recOrigins, reqOrigins)
	if recOrigins.Code != http.StatusForbidden {
		t.Fatalf("local mode allowed_origins PATCH status = %d, want 403 (%s)", recOrigins.Code, recOrigins.Body.String())
	}

	// 2. Runtime auth mutation must fail closed (403) even when authenticated.
	srv.auth = AuthConfig{Mode: AuthModeAPIKey, APIKey: "admin-test-secret"}
	reqAuthMut := httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(`{"auth":{"mode":"local"}}`))
	reqAuthMut.Header.Set("Content-Type", "application/json")
	reqAuthMut.Header.Set("Authorization", "Bearer admin-test-secret")
	recAuthMut := httptest.NewRecorder()
	srv.Router().ServeHTTP(recAuthMut, reqAuthMut)
	if recAuthMut.Code != http.StatusForbidden {
		t.Fatalf("runtime auth PATCH status = %d, want 403 (%s)", recAuthMut.Code, recAuthMut.Body.String())
	}
	srv.auth = AuthConfig{}

	decisions, err := store.GetDecisionHistory("server_config", "", 10)
	if err != nil {
		t.Fatalf("GetDecisionHistory: %v", err)
	}
	if len(decisions) < 2 {
		t.Fatalf("expected >= 2 audit decisions for rejected config patches, got %d", len(decisions))
	}

	// 3. Cross-origin Origin header and Sec-Fetch-Site: cross-site in AuthModeLocal must fail with 403.
	reqEvilOrigin := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"session":"test"}`))
	reqEvilOrigin.Header.Set("Origin", "https://attacker.invalid")
	recEvilOrigin := httptest.NewRecorder()
	srv.Router().ServeHTTP(recEvilOrigin, reqEvilOrigin)
	if recEvilOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d, want 403", recEvilOrigin.Code)
	}

	reqCrossSite := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"session":"test"}`))
	reqCrossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	recCrossSite := httptest.NewRecorder()
	srv.Router().ServeHTTP(recCrossSite, reqCrossSite)
	if recCrossSite.Code != http.StatusForbidden {
		t.Fatalf("Sec-Fetch-Site: cross-site POST status = %d, want 403", recCrossSite.Code)
	}
}

func TestWebUIMountedAuthAndSingleUseWSTickets(t *testing.T) {
	mockUI := fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><html><body>NTM Dashboard</body></html>")},
		"connect/index.html":  {Data: []byte("<!doctype html><html><body>Connect</body></html>")},
		"_next/static/app.js": {Data: []byte("console.log('ntm');")},
		"404.html":            {Data: []byte("<!doctype html><html><body>Not Found</body></html>")},
	}

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	const secretKey = "super-secret-webui-key-9912"
	srv := New(Config{
		Port:     0,
		EventBus: events.NewEventBus(16),
		Auth: AuthConfig{
			Mode:   AuthModeAPIKey,
			APIKey: secretKey,
		},
		WebUI: mockUI,
	})
	httpSrv := httptest.NewServer(srv.Router())
	t.Cleanup(httpSrv.Close)

	// 1. Static Web UI pages + /health + /docs load with 200 OK without credentials.
	for _, pubPath := range []string{"/", "/connect", "/connect/", "/_next/static/app.js", "/health", "/docs"} {
		resp, err := http.Get(httpSrv.URL + pubPath)
		if err != nil {
			t.Fatalf("GET %s: %v", pubPath, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unauthenticated GET %s status = %d, want 200", pubPath, resp.StatusCode)
		}
	}

	// 2. Protected API & WS routes return 401 without credentials.
	for _, protPath := range []string{"/api/v1/health", "/api/v1/sessions", "/api/v1/kernel/commands", "/api/v1/ws", "/events"} {
		resp, err := http.Get(httpSrv.URL + protPath)
		if err != nil {
			t.Fatalf("GET %s: %v", protPath, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated GET %s status = %d, want 401", protPath, resp.StatusCode)
		}
	}

	// Unauthenticated ws-ticket issuance must fail with 401.
	respUnauthTicket, err := http.Post(httpSrv.URL+"/api/v1/auth/ws-ticket", "application/json", nil)
	if err != nil {
		t.Fatalf("POST ws-ticket unauth: %v", err)
	}
	_ = respUnauthTicket.Body.Close()
	if respUnauthTicket.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /api/v1/auth/ws-ticket status = %d, want 401", respUnauthTicket.StatusCode)
	}

	// 3. Authenticated POST /api/v1/auth/ws-ticket issues a single-use 30s ticket.
	ticketReq, err := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/v1/auth/ws-ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	ticketReq.Header.Set("Authorization", "Bearer "+secretKey)
	ticketResp, err := http.DefaultClient.Do(ticketReq)
	if err != nil {
		t.Fatalf("POST ws-ticket auth: %v", err)
	}
	defer ticketResp.Body.Close()
	if ticketResp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated POST /api/v1/auth/ws-ticket status = %d, want 200", ticketResp.StatusCode)
	}
	var ticketPayload struct {
		Success      bool   `json:"success"`
		Ticket       string `json:"ticket"`
		ExpiresInSec int    `json:"expires_in_sec"`
	}
	if err := json.NewDecoder(ticketResp.Body).Decode(&ticketPayload); err != nil {
		t.Fatalf("decode ws-ticket: %v", err)
	}
	if !ticketPayload.Success || len(ticketPayload.Ticket) != 32 || ticketPayload.ExpiresInSec != 30 {
		t.Fatalf("unexpected ws-ticket payload: %+v", ticketPayload)
	}

	// 4. Redeem ticket on WebSocket upgrade WITHOUT Authorization header -> 101 Switching Protocols!
	wsBase := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"
	conn, upgradeResp, err := websocket.DefaultDialer.Dial(wsBase+"?ticket="+ticketPayload.Ticket, nil)
	if err != nil {
		t.Fatalf("WS upgrade with valid ticket failed: %v", err)
	}
	_ = conn.Close()
	if upgradeResp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("WS upgrade status = %d, want 101", upgradeResp.StatusCode)
	}

	// 5. Replaying the same single-use ticket a second time MUST fail with 401!
	_, replayResp, replayErr := websocket.DefaultDialer.Dial(wsBase+"?ticket="+ticketPayload.Ticket, nil)
	if replayErr == nil {
		t.Fatal("expected replayed single-use WS ticket to fail")
	}
	if replayResp == nil || replayResp.StatusCode != http.StatusUnauthorized {
		code := 0
		if replayResp != nil {
			code = replayResp.StatusCode
		}
		t.Fatalf("replayed WS ticket status = %d, want 401", code)
	}

	// 6. Verify structured logs contain redacted principal + ticket_redeemed=true and NEVER leak secretKey or raw ticket.
	logs := logBuf.String()
	if !strings.Contains(logs, "ticket_redeemed=true") {
		t.Fatalf("expected ticket_redeemed=true in slog output, got:\n%s", logs)
	}
	if strings.Contains(logs, secretKey) || strings.Contains(logs, ticketPayload.Ticket) {
		t.Fatalf("slog leaked raw credential or ticket:\n%s", logs)
	}

	// 7. Also verify mTLS WebSocket ticket issuance & redemption end-to-end.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ntm-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	makeCert := func(cn string, serial int64, extKeyUsage x509.ExtKeyUsage, ips bool) tls.Certificate {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{extKeyUsage},
			DNSNames:     []string{"localhost"},
		}
		if ips {
			tmpl.IPAddresses = nil
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
	}

	clientCert := makeCert("mtls-operator", 2, x509.ExtKeyUsageClientAuth, false)
	mtlsSrv := New(Config{
		Port:     0,
		EventBus: events.NewEventBus(16),
		Auth:     AuthConfig{Mode: AuthModeMTLS},
		WebUI:    mockUI,
	})
	tlsTestSrv := httptest.NewUnstartedServer(mtlsSrv.Router())
	tlsTestSrv.TLS = &tls.Config{
		ClientCAs:  caPool,
		ClientAuth: tls.VerifyClientCertIfGiven,
	}
	tlsTestSrv.StartTLS()
	t.Cleanup(tlsTestSrv.Close)

	rootPool := x509.NewCertPool()
	rootPool.AddCert(tlsTestSrv.Certificate())
	mtlsTLSConfig := &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{clientCert},
	}
	mtlsHTTP := &http.Client{Transport: &http.Transport{TLSClientConfig: mtlsTLSConfig}}
	mtlsTicketResp, err := mtlsHTTP.Post(tlsTestSrv.URL+"/api/v1/auth/ws-ticket", "application/json", nil)
	if err != nil {
		t.Fatalf("mTLS POST ws-ticket: %v", err)
	}
	defer mtlsTicketResp.Body.Close()
	if mtlsTicketResp.StatusCode != http.StatusOK {
		t.Fatalf("mTLS ws-ticket status = %d, want 200", mtlsTicketResp.StatusCode)
	}
	var mtlsTicket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(mtlsTicketResp.Body).Decode(&mtlsTicket); err != nil {
		t.Fatal(err)
	}
	wssDialer := websocket.Dialer{TLSClientConfig: mtlsTLSConfig}
	wssURL := "wss" + strings.TrimPrefix(tlsTestSrv.URL, "https") + "/api/v1/ws?ticket=" + mtlsTicket.Ticket
	wssConn, _, err := wssDialer.Dial(wssURL, nil)
	if err != nil {
		t.Fatalf("mTLS WSS upgrade failed: %v", err)
	}
	_ = wssConn.Close()
}

func TestServerMTLSAndHTTP2Qualification(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	socketRoot, err := os.MkdirTemp("", "ntmmtls")
	if err != nil {
		t.Fatalf("create tmux socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	t.Setenv("TMUX_TMPDIR", socketRoot)

	now := time.Now().UTC()

	newRootCA := func(commonName string, serial int64) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate CA key %s: %v", commonName, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber:          big.NewInt(serial),
			Subject:               pkix.Name{CommonName: commonName},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.Add(time.Hour),
			KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			BasicConstraintsValid: true,
			IsCA:                  true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("create CA cert %s: %v", commonName, err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatalf("parse CA cert %s: %v", commonName, err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		return key, cert, pemBytes
	}

	newLeafCert := func(
		caKey *ecdsa.PrivateKey,
		caCert *x509.Certificate,
		commonName string,
		serial int64,
		notBefore, notAfter time.Time,
		extKeyUsage x509.ExtKeyUsage,
		ips []net.IP,
		dnsNames []string,
	) (tls.Certificate, []byte, []byte) {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate leaf key %s: %v", commonName, err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: commonName},
			NotBefore:    notBefore,
			NotAfter:     notAfter,
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{extKeyUsage},
			IPAddresses:  ips,
			DNSNames:     dnsNames,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create leaf cert %s: %v", commonName, err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal leaf key %s: %v", commonName, err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		return tls.Certificate{
			Certificate: [][]byte{der},
			PrivateKey:  key,
		}, certPEM, keyPEM
	}

	// Ephemeral ECDSA P-256 test PKI:
	// - Trusted Root CA #1
	// - Server leaf cert (127.0.0.1 + localhost, ExtKeyUsageServerAuth)
	// - Valid Client leaf cert (CN=operator-alice, ExtKeyUsageClientAuth)
	// - Expired Client leaf cert (signed by Trusted CA #1, NotBefore=-2h, NotAfter=-1h)
	// - Untrusted Client leaf cert (signed by separate Untrusted Root CA #2)
	trustedCAKey, trustedCACert, trustedCAPEM := newRootCA("ntm-qual-trusted-root-ca-1", 1001)
	untrustedCAKey, untrustedCACert, _ := newRootCA("ntm-qual-untrusted-root-ca-2", 2001)

	_, serverCertPEM, serverKeyPEM := newLeafCert(
		trustedCAKey,
		trustedCACert,
		"127.0.0.1",
		1002,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		x509.ExtKeyUsageServerAuth,
		[]net.IP{net.ParseIP("127.0.0.1")},
		[]string{"localhost"},
	)
	validClientCert, _, _ := newLeafCert(
		trustedCAKey,
		trustedCACert,
		"operator-alice",
		1003,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		x509.ExtKeyUsageClientAuth,
		nil,
		nil,
	)
	expiredClientCert, _, _ := newLeafCert(
		trustedCAKey,
		trustedCACert,
		"operator-expired",
		1004,
		now.Add(-2*time.Hour),
		now.Add(-time.Hour),
		x509.ExtKeyUsageClientAuth,
		nil,
		nil,
	)
	untrustedClientCert, _, _ := newLeafCert(
		untrustedCAKey,
		untrustedCACert,
		"operator-untrusted",
		2002,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		x509.ExtKeyUsageClientAuth,
		nil,
		nil,
	)

	pkiDir := t.TempDir()
	certFile := filepath.Join(pkiDir, "server.crt")
	keyFile := filepath.Join(pkiDir, "server.key")
	caFile := filepath.Join(pkiDir, "trusted-ca.crt")
	if err := os.WriteFile(certFile, serverCertPEM, 0o600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyFile, serverKeyPEM, 0o600); err != nil {
		t.Fatalf("write server key: %v", err)
	}
	if err := os.WriteFile(caFile, trustedCAPEM, 0o600); err != nil {
		t.Fatalf("write trusted CA: %v", err)
	}

	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const seededSession = "mtls-qual-session"
	if err := store.CreateSession(&state.Session{
		ID:          seededSession,
		Name:        seededSession,
		ProjectPath: t.TempDir(),
		CreatedAt:   now,
		Status:      state.SessionActive,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	port := reserveQualificationPort(t)
	srv := New(Config{
		Host:       "127.0.0.1",
		Port:       port,
		ProjectDir: t.TempDir(),
		Version:    "mtls-qual-test",
		EventBus:   events.NewEventBus(16),
		StateStore: store,
		Auth: AuthConfig{
			Mode: AuthModeMTLS,
			MTLS: MTLSConfig{
				CertFile:     certFile,
				KeyFile:      keyFile,
				ClientCAFile: caFile,
			},
		},
	})
	srv.mailClient = deadAgentMailClient(t)
	srv.listLiveSessions = func(context.Context) ([]tmux.Session, error) {
		return []tmux.Session{{Name: seededSession, Windows: 1, Attached: true}}, nil
	}

	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- srv.Start(srvCtx)
	}()
	t.Cleanup(func() {
		srvCancel()
		select {
		case err := <-srvErrCh:
			if err != nil {
				t.Errorf("mTLS server shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for mTLS server shutdown")
		}
	})

	trustedCAPool := x509.NewCertPool()
	trustedCAPool.AddCert(trustedCACert)

	newMTLSClient := func(certs []tls.Certificate, forceSendCert *tls.Certificate) *http.Client {
		tlsCfg := &tls.Config{
			RootCAs:      trustedCAPool,
			Certificates: certs,
		}
		if forceSendCert != nil {
			certCopy := *forceSendCert
			tlsCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				return &certCopy, nil
			}
		}
		tr := &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig:   tlsCfg,
		}
		t.Cleanup(tr.CloseIdleConnections)
		return &http.Client{
			Transport: tr,
			Timeout:   5 * time.Second,
		}
	}

	baseURL := fmt.Sprintf("https://127.0.0.1:%d", port)
	validClient := newMTLSClient([]tls.Certificate{validClientCert}, nil)

	// Wait for the real ServeTLS listener to accept connections.
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		resp, reqErr := validClient.Get(baseURL + "/api/v1/health")
		if reqErr == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("mTLS server at %s did not become ready (last err: %v)", baseURL, reqErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 1. Valid Client Cert over HTTP/2 + TLS 1.3 on /api/v1/health and /api/v1/sessions.
	for _, endpoint := range []string{"/api/v1/health", "/api/v1/sessions"} {
		resp, err := validClient.Get(baseURL + endpoint)
		if err != nil {
			t.Fatalf("valid client cert GET %s failed: %v", endpoint, err)
		}
		bodyBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read body GET %s: %v", endpoint, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (body=%s)", endpoint, resp.StatusCode, string(bodyBytes))
		}
		if resp.Proto != "HTTP/2.0" || resp.ProtoMajor != 2 {
			t.Fatalf("GET %s negotiated HTTP proto = %q (major=%d), want HTTP/2.0 (major=2)", endpoint, resp.Proto, resp.ProtoMajor)
		}
		if resp.TLS == nil {
			t.Fatalf("GET %s missing TLS connection state", endpoint)
		}
		if resp.TLS.Version != tls.VersionTLS13 {
			t.Fatalf("GET %s negotiated TLS version = %s (0x%04x), want TLS 1.3 (0x%04x)",
				endpoint, tls.VersionName(resp.TLS.Version), resp.TLS.Version, tls.VersionTLS13)
		}
		if resp.TLS.NegotiatedProtocol != "h2" {
			t.Fatalf("GET %s ALPN NegotiatedProtocol = %q, want %q", endpoint, resp.TLS.NegotiatedProtocol, "h2")
		}
		if endpoint == "/api/v1/sessions" && !strings.Contains(string(bodyBytes), seededSession) {
			t.Fatalf("GET %s body missing seeded session %q: %s", endpoint, seededSession, string(bodyBytes))
		}
		t.Logf("verified mTLS GET %s -> status=%d proto=%s alpn=%s tls=%s cipher=%s",
			endpoint,
			resp.StatusCode,
			resp.Proto,
			resp.TLS.NegotiatedProtocol,
			tls.VersionName(resp.TLS.Version),
			tls.CipherSuiteName(resp.TLS.CipherSuite),
		)
	}

	// 2, 3, 4. Negative mTLS handshake enforcement: missing cert, untrusted CA cert, expired cert.
	negativeCases := []struct {
		name          string
		certs         []tls.Certificate
		forceSendCert *tls.Certificate
	}{
		{name: "missing_client_cert", certs: nil, forceSendCert: nil},
		{name: "untrusted_ca_client_cert", certs: []tls.Certificate{untrustedClientCert}, forceSendCert: &untrustedClientCert},
		{name: "expired_client_cert", certs: []tls.Certificate{expiredClientCert}, forceSendCert: &expiredClientCert},
	}
	for _, tc := range negativeCases {
		t.Run(tc.name, func(t *testing.T) {
			badClient := newMTLSClient(tc.certs, tc.forceSendCert)
			resp, err := badClient.Get(baseURL + "/api/v1/health")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				t.Fatalf("%s unexpectedly succeeded with status=%d proto=%s", tc.name, resp.StatusCode, resp.Proto)
			}
			t.Logf("verified mTLS rejection for %s: %v", tc.name, err)
		})
	}
}
