package relay

import (
	"context"
	"net"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
)

type pausedWriteConn struct {
	net.Conn
	pause     atomic.Bool
	writing   chan struct{}
	resume    chan struct{}
	closed    chan struct{}
	closeGate func()
}

func (c *pausedWriteConn) Write(data []byte) (int, error) {
	if c.pause.CompareAndSwap(true, false) {
		close(c.writing)
		select {
		case <-c.resume:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(data)
}

func (c *pausedWriteConn) Close() error {
	c.closeGate()
	return c.Conn.Close()
}

type pausedWriteListener struct {
	net.Listener
	latest atomic.Pointer[pausedWriteConn]
}

func (l *pausedWriteListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &pausedWriteConn{
		Conn:    conn,
		writing: make(chan struct{}),
		resume:  make(chan struct{}),
		closed:  make(chan struct{}),
	}
	wrapped.closeGate = sync.OnceFunc(func() { close(wrapped.closed) })
	l.latest.Store(wrapped)
	return wrapped, nil
}

func TestLeaveWaitsForPendingWriteBeforeClosing(t *testing.T) {
	server, err := New(Config{NodeID: "leave"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("leave-room", "demo.v1", []string{"observer", "leaver"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(server)
	listener := &pausedWriteListener{Listener: httpServer.Listener}
	httpServer.Listener = listener
	httpServer.Start()
	defer httpServer.Close()
	observer, _ := connect(t, httpServer.URL, &wire.Connect{
		ApplicationProtocol: "demo.v1",
		Credential:          &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["observer"]},
	})
	defer observer.CloseNow()
	leaver, welcome := connect(t, httpServer.URL, &wire.Connect{
		ApplicationProtocol: "demo.v1",
		Credential:          &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["leaver"]},
	})
	defer leaver.CloseNow()

	server.mu.Lock()
	r := server.rooms[allocation.RoomInstanceID]
	server.mu.Unlock()
	r.mu.Lock()
	leaving := r.members["leaver"].connection
	r.mu.Unlock()

	transport := listener.latest.Load()
	resume := sync.OnceFunc(func() { close(transport.resume) })
	defer resume()
	transport.pause.Store(true)
	send(t, leaver, &wire.ClientEnvelope{Sequence: 1, Body: &wire.ClientEnvelope_Ping{Ping: &wire.Ping{Nonce: 1}}})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case <-transport.writing:
	case <-ctx.Done():
		t.Fatal("server did not start the pending write")
	}

	// Keep Left behind a real pending write while the next client frame arrives.
	send(t, leaver, &wire.ClientEnvelope{Sequence: 2, Body: &wire.ClientEnvelope_Leave{Leave: &wire.Leave{}}})
	send(t, leaver, &wire.ClientEnvelope{Sequence: 3, Body: &wire.ClientEnvelope_Ping{Ping: &wire.Ping{Nonce: 2}}})
	transition := receiveUntil(t, observer, func(message *wire.ServerEnvelope) bool {
		return message.GetPeerLeft() != nil
	}).GetPeerLeft()
	if transition.ParticipantId != "leaver" || transition.Generation != welcome.Generation || transition.Reason != wire.PeerLeftReason_PEER_LEFT_REASON_VOLUNTARY {
		t.Fatalf("unexpected leave transition: %v", transition)
	}

	writerPause := time.NewTimer(100 * time.Millisecond)
	defer writerPause.Stop()
	select {
	case <-leaving.ctx.Done():
		t.Fatal("following frame canceled Leave acknowledgement before the pending write finished")
	case <-writerPause.C:
	}
	resume()

	left := receiveUntil(t, leaver, func(message *wire.ServerEnvelope) bool {
		return message.GetLeft() != nil
	}).GetLeft()
	if left.RequestSequence != 2 {
		t.Fatalf("Left request sequence = %d, want 2", left.RequestSequence)
	}
	if _, _, err := leaver.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("leave close: %v", err)
	}
	if writeErrors := server.Stats().WriteErrors; writeErrors != 0 {
		t.Fatalf("normal Leave caused %d write errors", writeErrors)
	}
}
