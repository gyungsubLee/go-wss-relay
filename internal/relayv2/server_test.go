package relayv2

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"google.golang.org/protobuf/proto"
)

func TestWebSocketStateEventAndResume(t *testing.T) {
	server, err := New(Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("room", "snake.v1", []string{"alice", "bob"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	other, err := server.CreateRoom("other", "snake.v1", []string{"eve"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	alice, aliceWelcome := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}})
	defer alice.CloseNow()
	bob, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["bob"]}})
	defer bob.CloseNow()
	eve, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: other.Tickets["eve"]}})
	defer eve.CloseNow()
	send(t, alice, &wire.ClientEnvelope{Sequence: 1, Body: &wire.ClientEnvelope_StateUpdate{StateUpdate: &wire.StateUpdate{Payload: []byte("position")}}})
	update := receiveUntil(t, bob, func(e *wire.ServerEnvelope) bool { return e.GetStateBatch() != nil }).GetStateBatch()
	if len(update.States) != 1 || update.States[0].ParticipantId != "alice" || string(update.States[0].Payload) != "position" {
		t.Fatalf("unexpected state: %v", update)
	}
	if update.States[0].Generation != 1 || update.States[0].ClientSequence != 1 {
		t.Fatalf("bad state identity: %v", update)
	}
	snapshot := server.Snapshot(other.RoomInstanceID)
	if len(snapshot.States) != 0 {
		t.Fatalf("state leaked between rooms: %v", snapshot)
	}
	send(t, alice, &wire.ClientEnvelope{Sequence: 2, Body: &wire.ClientEnvelope_Event{Event: &wire.PublishEvent{Kind: 1, Payload: []byte("event")}}})
	event := receiveUntil(t, bob, func(e *wire.ServerEnvelope) bool { return e.GetEvent() != nil }).GetEvent()
	if event.ParticipantId != "alice" || string(event.Payload) != "event" {
		t.Fatalf("unexpected event: %v", event)
	}
	receipt := receiveUntil(t, alice, func(e *wire.ServerEnvelope) bool { return e.GetEventAccepted() != nil }).GetEventAccepted()
	if receipt.ClientSequence != 2 {
		t.Fatalf("bad receipt: %v", receipt)
	}
	resumed, welcome := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ResumeToken{ResumeToken: aliceWelcome.ResumeToken}})
	defer resumed.CloseNow()
	if !welcome.Resumed || welcome.Generation != 2 || welcome.ParticipantId != "alice" || welcome.ResumeToken == aliceWelcome.ResumeToken {
		t.Fatalf("unexpected resume: %v", welcome)
	}
	for _, state := range welcome.Snapshot.States {
		if state.ParticipantId == "alice" {
			t.Fatal("old generation cache survived")
		}
	}
	send(t, resumed, &wire.ClientEnvelope{Sequence: 1, Body: &wire.ClientEnvelope_Ping{Ping: &wire.Ping{Nonce: 42}}})
	pong := receiveUntil(t, resumed, func(e *wire.ServerEnvelope) bool { return e.GetPong() != nil }).GetPong()
	if pong.Nonce != 42 {
		t.Fatalf("unexpected pong: %v", pong)
	}
}

func TestTicketsAreSingleUseAndOriginIsChecked(t *testing.T) {
	server, err := New(Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("room", "snake.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	originContext, originCancel := context.WithTimeout(context.Background(), time.Second)
	defer originCancel()
	unexpected, response, originErr := websocket.Dial(originContext, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v2/relay", &websocket.DialOptions{Subprotocols: []string{"relay.v2"}, HTTPHeader: http.Header{"Origin": []string{"https://untrusted.test"}}})
	if unexpected != nil {
		unexpected.CloseNow()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if originErr == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted origin accepted: %v", originErr)
	}

	client, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}})
	defer client.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	duplicate, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v2/relay", &websocket.DialOptions{Subprotocols: []string{"relay.v2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.CloseNow()
	send(t, duplicate, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}}}})
	rejected := receiveUntil(t, duplicate, func(e *wire.ServerEnvelope) bool { return e.GetError() != nil }).GetError()
	if rejected.Code != wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
		t.Fatalf("ticket reuse: %v", rejected)
	}
}

func connect(t *testing.T, base string, body *wire.Connect) (*websocket.Conn, *wire.Welcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v2/relay", &websocket.DialOptions{Subprotocols: []string{"relay.v2"}})
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: body}})
	return conn, receiveUntil(t, conn, func(e *wire.ServerEnvelope) bool { return e.GetWelcome() != nil }).GetWelcome()
}

func send(t *testing.T, conn *websocket.Conn, envelope *wire.ClientEnvelope) {
	t.Helper()
	envelope.ProtocolRevision = 2
	data, err := proto.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatal(err)
	}
}

func receiveUntil(t *testing.T, conn *websocket.Conn, predicate func(*wire.ServerEnvelope) bool) *wire.ServerEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageBinary {
			t.Fatal("non-binary message")
		}
		var envelope wire.ServerEnvelope
		if err := proto.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		if predicate(&envelope) {
			return &envelope
		}
	}
}

func TestPendingSnapshotIsNotOvertakenByState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
	c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_SyncSnapshot{SyncSnapshot: &wire.SyncSnapshot{Snapshot: &wire.RoomSnapshot{RoomRevision: 4}}}})
	c.queueState(&wire.PublishedState{ParticipantId: "alice", RoomRevision: 5, ClientSequence: 2, Payload: []byte("new")})
	if batch := c.takeStates(); batch != nil {
		t.Fatal("state overtook queued snapshot")
	}
	if c.popReliable().GetSyncSnapshot() == nil {
		t.Fatal("snapshot missing")
	}
	if c.takeStates().GetStateBatch().States[0].RoomRevision != 5 {
		t.Fatal("newer state lost")
	}
}

func TestStateCoalescingKeepsEveryPublisher(t *testing.T) {
	c := &client{states: make(map[string]*wire.PublishedState)}
	c.queueState(&wire.PublishedState{ParticipantId: "alice", ClientSequence: 1, Payload: []byte("old")})
	c.queueState(&wire.PublishedState{ParticipantId: "bob", ClientSequence: 1, Payload: []byte("bob")})
	c.queueState(&wire.PublishedState{ParticipantId: "alice", ClientSequence: 2, Payload: []byte("new")})
	states := c.takeStates().GetStateBatch().States
	if len(states) != 2 || states[0].ParticipantId != "alice" || string(states[0].Payload) != "new" || states[1].ParticipantId != "bob" {
		t.Fatalf("bad coalescing: %v", states)
	}
}

func TestTLSWebSocketConnection(t *testing.T) {
	server, err := New(Config{NodeID: "tls-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("tls-room", "snake.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewTLSServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v2/relay", &websocket.DialOptions{Subprotocols: []string{"relay.v2"}, HTTPClient: httpServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	send(t, conn, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}}}})
	welcome := receiveUntil(t, conn, func(e *wire.ServerEnvelope) bool { return e.GetWelcome() != nil }).GetWelcome()
	if welcome.ParticipantId != "alice" {
		t.Fatal("wrong participant over TLS")
	}
}

func TestDuplicateRoomAndReliableQueueLimit(t *testing.T) {
	server, err := New(Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if _, err := server.CreateRoom("room", "snake.v1", []string{"alice"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := server.CreateRoom("room", "snake.v1", []string{"bob"}, time.Minute); err != ErrConflict {
		t.Fatalf("duplicate room: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	for range 33 {
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Pong{Pong: &wire.Pong{}}})
	}
	if request := c.terminal.Load(); request == nil || request.code != wire.ErrorCode_ERROR_CODE_SLOW_CONSUMER {
		t.Fatal("slow consumer was not marked for termination")
	}
}

func TestCredentialExpiryBoundaries(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "connect ticket", true: "resume grace"}[resume], func(t *testing.T) {
			now := time.Unix(1800000000, 0)
			server, err := New(Config{NodeID: "test", Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			allocation, err := server.CreateRoom("expiry", "snake.v1", []string{"alice"}, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &client{server: server, ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
			connect := &wire.Connect{ApplicationProtocol: "snake.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}}
			if resume {
				welcome, code := server.authenticate(c, connect)
				if code != 0 {
					t.Fatal(code)
				}
				c.disconnect()
				connect.Credential = &wire.Connect_ResumeToken{ResumeToken: welcome.ResumeToken}
			}
			now = now.Add(30 * time.Second)
			if _, code := server.authenticate(c, connect); code != wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
				t.Fatalf("credential accepted at expiry: %v", code)
			}
			server.Expire()
			if snapshot := server.Snapshot(allocation.RoomInstanceID); resume && snapshot != nil && len(snapshot.Members) != 0 {
				t.Fatal("expired participant survived snapshot")
			}
		})
	}
}

func TestTrustedReissueReplacesConsumedTicket(t *testing.T) {
	now := time.Unix(1800000000, 0)
	server, err := New(Config{NodeID: "test", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("reissue", "demo.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	newClient := func() *client {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		return &client{server: server, ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
	}
	first := newClient()
	connect := &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}}
	if _, code := server.authenticate(first, connect); code != 0 {
		t.Fatal(code)
	}
	first.disconnect()
	ticket, err := server.RefreshTicket(allocation.RoomInstanceID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := server.RefreshTicket(allocation.RoomInstanceID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	connect.Credential = &wire.Connect_ConnectTicket{ConnectTicket: ticket.Value}
	if _, code := server.authenticate(newClient(), connect); code != wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
		t.Fatal("superseded ticket accepted")
	}
	connect.Credential = &wire.Connect_ConnectTicket{ConnectTicket: replacement.Value}
	second := newClient()
	welcome, code := server.authenticate(second, connect)
	if code != 0 || welcome.Generation != 2 || !welcome.Resumed {
		t.Fatalf("reissue welcome: %v %v", welcome, code)
	}
	second.disconnect()
	now = now.Add(30 * time.Second)
	if _, err := server.RefreshTicket(allocation.RoomInstanceID, "alice"); err == nil {
		t.Fatal("expired grace reissued")
	}
}

func TestPublisherRateDoesNotBlockNewRoomsAndMetricsCountTraffic(t *testing.T) {
	server, err := New(Config{NodeID: "metrics"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("busy", "demo.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	conn, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}})
	defer conn.CloseNow()
	for sequence := uint32(1); sequence <= 8; sequence++ {
		send(t, conn, &wire.ClientEnvelope{Sequence: sequence, Body: &wire.ClientEnvelope_StateUpdate{StateUpdate: &wire.StateUpdate{Payload: []byte("full-state")}}})
	}
	receiveUntil(t, conn, func(message *wire.ServerEnvelope) bool {
		return message.GetError().GetCode() == wire.ErrorCode_ERROR_CODE_RATE_LIMITED
	})
	stats := server.Stats()
	if stats.IngressBytes == 0 || stats.EgressBytes == 0 || stats.RateRejected == 0 || stats.Connections != 1 {
		t.Fatalf("bad metrics: %+v", stats)
	}
	if _, err := server.CreateRoom("unrelated", "demo.v1", []string{"bob"}, time.Minute); err != nil {
		t.Fatalf("one publisher blocked unrelated admission: %v", err)
	}
}

func TestReservationExpiryReleasesCapacityAndDoesNotResurrect(t *testing.T) {
	now := time.Unix(1800000000, 0)
	server, err := New(Config{NodeID: "reservation", MaxConnections: 2, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	first, err := server.Reserve("request", "room", "demo.v1", []string{"alice", "bob"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first.Tickets["alice"] = "tampered"
	again, err := server.Reserve("request", "room", "demo.v1", []string{"bob", "alice"}, time.Minute)
	if err != nil || again.RoomInstanceID != first.RoomInstanceID || again.Tickets["alice"] == "tampered" {
		t.Fatal("reservation cache changed")
	}
	if _, err := server.CreateRoom("other", "demo.v1", []string{"eve"}, time.Minute); err != ErrCapacity {
		t.Fatalf("unclaimed slots not reserved: %v", err)
	}
	now = now.Add(30 * time.Second)
	if _, err := server.RefreshTicket(first.RoomInstanceID, "alice"); err == nil {
		t.Fatal("expired reservation reissued")
	}
	server.Expire()
	if server.Stats().ReservedParticipants != 0 || len(server.Inventory().Rooms) != 0 {
		t.Fatal("expired reservation retained capacity")
	}
	if _, err := server.Reserve("request", "room", "demo.v1", []string{"alice", "bob"}, time.Minute); err != ErrGone {
		t.Fatalf("expired reservation resurrected: %v", err)
	}
	if _, err := server.CreateRoom("other", "demo.v1", []string{"eve"}, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestFirstCPUSampleIsUncalibrated(t *testing.T) {
	server, err := New(Config{NodeID: "calibration"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	first := server.Stats()
	if first.GoCPUReady || first.GoBusyFractionEstimate != 0 {
		t.Fatal("first cumulative sample was treated as an interval")
	}
}

func TestFullParticipantCapacityStillAllowsConnectionReplacement(t *testing.T) {
	server, err := New(Config{NodeID: "full", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("one", "demo.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	first, welcome := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}})
	defer first.CloseNow()
	// The original TCP socket stays open, as can happen during a Wi-Fi/cellular change.
	replacement, resumed := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ResumeToken{ResumeToken: welcome.ResumeToken}})
	defer replacement.CloseNow()
	if resumed.Generation != 2 || !resumed.Resumed {
		t.Fatal("full node blocked replacement authority")
	}
	if server.Stats().ReservedParticipants != 1 {
		t.Fatal("replacement consumed another participant slot")
	}
}

func TestSessionReplacementAndRoomEndHaveExplicitTerminalCodes(t *testing.T) {
	server, err := New(Config{NodeID: "terminal"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("terminal-room", "demo.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	original, welcome := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["alice"]}})
	defer original.CloseNow()
	replacement, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ResumeToken{ResumeToken: welcome.ResumeToken}})
	defer replacement.CloseNow()
	failure := receiveUntil(t, original, func(e *wire.ServerEnvelope) bool { return e.GetError() != nil }).GetError()
	if failure.Code != wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT || !failure.Fatal {
		t.Fatal("old connection did not receive session conflict")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := original.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("replacement close: %v", err)
	}
	if err := server.EndRoom(allocation.RoomInstanceID); err != nil {
		t.Fatal(err)
	}
	ended := receiveUntil(t, replacement, func(e *wire.ServerEnvelope) bool { return e.GetError() != nil }).GetError()
	if ended.Code != wire.ErrorCode_ERROR_CODE_ROOM_ENDED {
		t.Fatal("room end not announced")
	}
	if _, _, err := replacement.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("room end close: %v", err)
	}
}

func TestReliableByteBudgetClosesBeforeMessageCountLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1)}
	for range 4 {
		snapshot := &wire.RoomSnapshot{}
		for range 16 {
			snapshot.States = append(snapshot.States, &wire.PublishedState{Payload: make([]byte, 4096)})
		}
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_SyncSnapshot{SyncSnapshot: &wire.SyncSnapshot{Snapshot: snapshot}}})
	}
	if c.terminal.Load() == nil || len(c.reliable) >= 32 || c.reliableBytes > maxReliableBytes {
		t.Fatal("byte budget did not bound the reliable queue")
	}
}

// This transport blocks one selected writer until WebSocket deadline handling closes it.
// A tiny TCP receive buffer cannot reliably force a stall on autotuning kernels.
type blockedWriteConn struct {
	net.Conn
	blocked atomic.Bool
	closed  chan struct{}
	once    sync.Once
}

func (c *blockedWriteConn) Write(data []byte) (int, error) {
	if c.blocked.Load() {
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(data)
}

func (c *blockedWriteConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type blockedWriteListener struct {
	net.Listener
	latest atomic.Pointer[blockedWriteConn]
}

func (l *blockedWriteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &blockedWriteConn{Conn: conn, closed: make(chan struct{})}
	l.latest.Store(wrapped)
	return wrapped, nil
}

func TestBlockedSocketWriteDoesNotStallHealthyPeer(t *testing.T) {
	server, err := New(Config{NodeID: "slow-socket"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("slow-room", "demo.v1", []string{"sender", "healthy", "slow"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(server)
	listener := &blockedWriteListener{Listener: httpServer.Listener}
	httpServer.Listener = listener
	httpServer.Start()
	defer httpServer.Close()
	sender, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["sender"]}})
	healthy, _ := connect(t, httpServer.URL, &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["healthy"]}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	slow, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v2/relay", &websocket.DialOptions{Subprotocols: []string{"relay.v2"}})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	send(t, slow, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{ApplicationProtocol: "demo.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["slow"]}}}})
	receiveUntil(t, slow, func(e *wire.ServerEnvelope) bool { return e.GetWelcome() != nil })
	// Block the transport after authentication, independent of OS TCP autotuning.
	listener.latest.Load().blocked.Store(true)
	var workers sync.WaitGroup
	var received, maxGap atomic.Int64
	workers.Add(2)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		var sequence uint32
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			sequence++
			data, err := proto.Marshal(&wire.ClientEnvelope{ProtocolRevision: 2, Sequence: sequence, Body: &wire.ClientEnvelope_StateUpdate{StateUpdate: &wire.StateUpdate{Payload: make([]byte, 4096)}}})
			if err != nil {
				return
			}
			if err := sender.Write(ctx, websocket.MessageBinary, data); err != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		var previous time.Time
		for {
			_, data, err := healthy.Read(ctx)
			if err != nil {
				return
			}
			var message wire.ServerEnvelope
			if proto.Unmarshal(data, &message) != nil {
				return
			}
			if message.GetStateBatch() != nil {
				now := time.Now()
				if !previous.IsZero() {
					gap := now.Sub(previous).Milliseconds()
					for {
						previousMax := maxGap.Load()
						if gap <= previousMax || maxGap.CompareAndSwap(previousMax, gap) {
							break
						}
					}
				}
				previous = now
				received.Add(1)
			}
		}
	}()
	defer func() {
		cancel()
		_ = sender.CloseNow()
		_ = healthy.CloseNow()
		_ = slow.CloseNow()
		workers.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	disconnected := false
	for time.Now().Before(deadline) {
		snapshot := server.Snapshot(allocation.RoomInstanceID)
		if snapshot != nil {
			for _, peer := range snapshot.Members {
				if peer.ParticipantId == "slow" && peer.Status == wire.ConnectionStatus_CONNECTION_STATUS_DISCONNECTED {
					disconnected = true
				}
			}
		}
		if disconnected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !disconnected {
		stats := server.Stats()
		t.Fatalf("blocked socket remained present: healthyReceived=%d, stats=%+v", received.Load(), stats)
	}
	if server.Stats().WriteErrors == 0 {
		t.Fatal("blocked transport did not exercise the socket write deadline")
	}
	before := received.Load()
	deadline = time.Now().Add(time.Second)
	for received.Load() < before+3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if received.Load() < before+3 || maxGap.Load() > 400 {
		t.Fatalf("healthy peer stalled: received=%d, gap=%dms", received.Load(), maxGap.Load())
	}
}
