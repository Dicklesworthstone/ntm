package serve

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/state"
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
