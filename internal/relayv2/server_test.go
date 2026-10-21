package relayv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	select {
	case <-ctx.Done():
	default:
		t.Fatal("slow consumer not stopped at queue limit")
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
			if resume && len(server.Snapshot(allocation.RoomInstanceID).Members) != 0 {
				t.Fatal("expired participant survived snapshot")
			}
		})
	}
}
