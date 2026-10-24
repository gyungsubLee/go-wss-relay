package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
)

func TestAuthenticationAdmissionRejectsBeforeConsumingIPBudget(t *testing.T) {
	server, err := New(Config{NodeID: "auth-gate"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	now := time.Unix(0, 0)
	server.authAdmission = httpx.NewAdmission(1000, 2000, 1, func() time.Time { return now })
	server.handshakeIPs = httpx.NewIPBudget(0, 1, 1, func() time.Time { return now })
	release, ok := server.authAdmission.Enter(httptest.NewRecorder())
	if !ok {
		t.Fatal("could not occupy authentication admission")
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v2/relay", nil))
	release()
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("full authentication admission returned %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v2/relay", nil))
	if recorder.Code != http.StatusUpgradeRequired {
		t.Fatalf("global rejection consumed IP budget: next upgrade returned %d, want 426", recorder.Code)
	}
}

func TestAuthenticationAdmissionPreservesOtherIPsAndGlobalCapacity(t *testing.T) {
	server, base := newAuthenticationTestServer(t, Config{TrustedProxies: []string{"127.0.0.1/32"}})
	allocation, err := server.CreateRoom("auth-room", "auth.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	var pending []*websocket.Conn
	for index := range 64 {
		forwarded := "192.0.2.1"
		if index%2 == 0 {
			forwarded = "::ffff:192.0.2.1"
		}
		pending = append(pending, authenticationSocket(t, base, forwarded, http.StatusSwitchingProtocols))
	}
	authenticationSocket(t, base, "192.0.2.1", http.StatusTooManyRequests)

	other := authenticationSocket(t, base, "198.51.100.1, 192.0.2.2", http.StatusSwitchingProtocols)
	send(t, other, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{
		ApplicationProtocol: "auth.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]},
	}}})
	receiveUntil(t, other, func(envelope *wire.ServerEnvelope) bool { return envelope.GetWelcome() != nil })

	// The untrusted rightmost hop owns the budget; its supplied leftmost IP cannot bypass it.
	for range 64 {
		pending = append(pending, authenticationSocket(t, base, "203.0.113.1, 192.0.2.2", http.StatusSwitchingProtocols))
	}
	authenticationSocket(t, base, "192.0.2.3", http.StatusTooManyRequests)

	for _, conn := range pending {
		_ = conn.CloseNow()
	}
	waitAuthenticationConnections(t, server, 1, 0)
	authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)
}

func TestAuthenticationAdmissionIgnoresUntrustedForwardedAddresses(t *testing.T) {
	_, base := newAuthenticationTestServer(t, Config{})
	for range 64 {
		authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)
	}
	authenticationSocket(t, base, "192.0.2.2", http.StatusTooManyRequests)
}

func TestAuthenticationAdmissionReleasesEveryCompletedAttempt(t *testing.T) {
	server, base := newAuthenticationTestServer(t, Config{TrustedProxies: []string{"127.0.0.1/32"}})
	allocation, err := server.CreateRoom("auth-room", "auth.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var pending []*websocket.Conn
	for range 64 {
		pending = append(pending, authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols))
	}

	send(t, pending[0], &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{
		ApplicationProtocol: "auth.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]},
	}}})
	receiveUntil(t, pending[0], func(envelope *wire.ServerEnvelope) bool { return envelope.GetWelcome() != nil })
	authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)

	send(t, pending[1], &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{
		ApplicationProtocol: "auth.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]},
	}}})
	rejected := receiveUntil(t, pending[1], func(envelope *wire.ServerEnvelope) bool { return envelope.GetError() != nil })
	if rejected.GetError().Code != wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
		t.Fatalf("reused credential returned %v", rejected)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, _, err := pending[1].Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("rejected credential close: %v", err)
	}
	waitAuthenticationConnections(t, server, 64, 63)
	authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)

	_ = pending[2].CloseNow()
	waitAuthenticationConnections(t, server, 64, 63)
	authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)

	_ = pending[3].CloseNow()
	waitAuthenticationConnections(t, server, 64, 63)
	request := httptest.NewRequest(http.MethodGet, "/v2/relay", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Forwarded-For", "192.0.2.1")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUpgradeRequired {
		t.Fatalf("missing upgrade returned %d", recorder.Code)
	}
	authenticationSocket(t, base, "192.0.2.1", http.StatusSwitchingProtocols)

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	waitAuthenticationConnections(t, server, 0, 0)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for acquired := 0; acquired < 128; {
		release, ok := server.authAdmission.Enter(httptest.NewRecorder())
		if ok {
			t.Cleanup(release)
			acquired++
			continue
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("server cancellation retained authentication admission: reacquired %d/128 slots", acquired)
		}
	}
}

func newAuthenticationTestServer(t *testing.T, config Config) (*Server, string) {
	t.Helper()
	config.NodeID = "auth-test"
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		_ = server.Close()
		httpServer.Close()
	})
	return server, httpServer.URL
}

func authenticationSocket(t *testing.T, base, forwarded string, wantStatus int) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v2/relay", &websocket.DialOptions{
		Subprotocols: []string{"relay.v2"}, HTTPHeader: http.Header{"X-Forwarded-For": []string{forwarded}},
	})
	if conn != nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
	}
	if response == nil {
		t.Fatalf("authentication upgrade returned no response: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols && response.Body != nil {
		_ = response.Body.Close()
	}
	if response.StatusCode != wantStatus || (err == nil) != (wantStatus == http.StatusSwitchingProtocols) {
		t.Fatalf("authentication upgrade returned %d (%v), want %d", response.StatusCode, err, wantStatus)
	}
	return conn
}

func waitAuthenticationConnections(t *testing.T, server *Server, wantConnections, wantAuthenticating int) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		server.mu.Lock()
		connections := server.connections
		authenticating, ipEntries := 0, len(server.authenticatingIPs)
		for _, count := range server.authenticatingIPs {
			authenticating += count
		}
		server.mu.Unlock()
		if connections == wantConnections && authenticating == wantAuthenticating && (wantAuthenticating != 0 || ipEntries == 0) {
			return
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("connections = %d, want %d; authenticating = %d, want %d; IP entries = %d",
				connections, wantConnections, authenticating, wantAuthenticating, ipEntries)
		}
	}
}
