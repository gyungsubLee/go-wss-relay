package relayv2

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/protocolv2"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
)

const maxReliableBytes = 256 << 10

type client struct {
	server   *Server
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	room     *room
	member   *member
	sequence uint32

	mu            sync.Mutex
	reliable      []*wire.ServerEnvelope
	reliableBytes int
	states        map[string]*wire.PublishedState
	notify        chan struct{}
}

func advertisedLimits() *wire.Limits {
	return &wire.Limits{
		MaxClientMessageBytes: protocolv2.MaxClientMessageBytes, MaxServerMessageBytes: protocolv2.MaxServerMessageBytes,
		MaxStatePayloadBytes: protocolv2.MaxStatePayloadBytes, MaxEventPayloadBytes: protocolv2.MaxEventPayloadBytes,
		MaxStateUpdatesPerSecond: 20, MaxEventsPerSecond: 5, StateBatchIntervalMs: 50,
		HeartbeatIntervalMs: 15000, HeartbeatTimeoutMs: 45000, ResumeGraceMs: 30000,
	}
}

func (server *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v2/relay" || request.URL.RawQuery != "" || request.Method != http.MethodGet {
		http.NotFound(w, request)
		return
	}
	server.mu.Lock()
	if server.closed || server.connections >= maxConnections {
		server.mu.Unlock()
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	server.connections++
	server.wg.Add(1)
	server.mu.Unlock()
	defer func() { server.mu.Lock(); server.connections--; server.mu.Unlock(); server.wg.Done() }()
	conn, err := websocket.Accept(w, request, &server.accept)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "relay.v2" {
		_ = conn.Close(websocket.StatusPolicyViolation, "relay.v2 required")
		return
	}
	conn.SetReadLimit(protocolv2.MaxClientMessageBytes)
	ctx, cancel := context.WithCancel(server.ctx)
	defer cancel()
	c := &client{server: server, conn: conn, ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
	authContext, authCancel := context.WithTimeout(ctx, 5*time.Second)
	kind, data, err := conn.Read(authContext)
	authCancel()
	if err != nil {
		return
	}
	if kind != websocket.MessageBinary {
		_ = conn.Close(websocket.StatusUnsupportedData, "binary required")
		return
	}
	envelope, code := protocolv2.DecodeClient(data, false, 0)
	if code != 0 {
		c.writeError(code)
		return
	}
	welcome, code := server.authenticate(c, envelope.GetConnect())
	if code != 0 {
		c.writeError(code)
		return
	}
	defer c.disconnect()
	if c.write(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Welcome{Welcome: welcome}}) != nil {
		return
	}
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); c.writeLoop(); c.cancel() }()
	c.readLoop()
	c.cancel()
	<-writerDone
}

func (c *client) write(envelope *wire.ServerEnvelope) error {
	if c.sequence == math.MaxUint32 {
		return ErrClosed
	}
	c.sequence++
	envelope.ProtocolRevision, envelope.Sequence = 2, c.sequence
	data, err := proto.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(data) > protocolv2.MaxServerMessageBytes {
		return ErrCapacity
	}
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, data)
}

func (c *client) writeError(code wire.ErrorCode) {
	_ = c.write(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Error{Error: &wire.Error{Code: code, Fatal: true, Message: "connection rejected"}}})
	_ = c.conn.Close(websocket.StatusPolicyViolation, "connection rejected")
}

func (c *client) enqueue(envelope *wire.ServerEnvelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := proto.Size(envelope) + 12
	if len(c.reliable) >= 32 || c.reliableBytes+size > maxReliableBytes {
		c.cancel()
		return
	}
	c.reliable = append(c.reliable, envelope)
	c.reliableBytes += size
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *client) clearState(id string) {
	c.mu.Lock()
	delete(c.states, id)
	c.mu.Unlock()
}

func (c *client) queueState(state *wire.PublishedState) {
	c.mu.Lock()
	c.states[state.ParticipantId] = state
	c.mu.Unlock()
}

func (c *client) popReliable() *wire.ServerEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reliable) == 0 {
		return nil
	}
	envelope := c.reliable[0]
	c.reliable[0] = nil
	c.reliable = c.reliable[1:]
	c.reliableBytes -= proto.Size(envelope) + 12
	return envelope
}

func (c *client) takeStates() *wire.ServerEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reliable) != 0 || len(c.states) == 0 {
		return nil
	}
	batch := &wire.StateBatch{}
	for id, state := range c.states {
		batch.States = append(batch.States, state)
		batch.RoomRevision = max(batch.RoomRevision, state.RoomRevision)
		delete(c.states, id)
	}
	slices.SortFunc(batch.States, func(a, b *wire.PublishedState) int { return strings.Compare(a.ParticipantId, b.ParticipantId) })
	return &wire.ServerEnvelope{Body: &wire.ServerEnvelope_StateBatch{StateBatch: batch}}
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		flush := false
		select {
		case <-c.ctx.Done():
			return
		case <-c.notify:
		case <-ticker.C:
			flush = true
			if !c.server.now().Before(c.room.expires) {
				return
			}
		}
		for envelope := c.popReliable(); envelope != nil; envelope = c.popReliable() {
			if c.write(envelope) != nil {
				return
			}
			if envelope.GetError().GetFatal() || envelope.GetLeft() != nil {
				return
			}
		}
		if flush {
			if envelope := c.takeStates(); envelope != nil && c.write(envelope) != nil {
				return
			}
		}
	}
}

func (c *client) readLoop() {
	total := rate.NewLimiter(40, 40)
	states := rate.NewLimiter(20, 2)
	events := rate.NewLimiter(5, 10)
	resyncs := rate.NewLimiter(1, 1)
	pings := rate.NewLimiter(1, 2)
	var previous uint32
	for {
		ctx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
		kind, data, err := c.conn.Read(ctx)
		cancel()
		if err != nil {
			return
		}
		if kind != websocket.MessageBinary {
			return
		}
		envelope, code := protocolv2.DecodeClient(data, true, previous)
		if code != 0 {
			c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Error{Error: &wire.Error{Code: code, Fatal: true, Message: "invalid message"}}})
			<-c.ctx.Done()
			return
		}
		previous = envelope.Sequence
		limiter := total
		switch envelope.Body.(type) {
		case *wire.ClientEnvelope_StateUpdate:
			limiter = states
		case *wire.ClientEnvelope_Event:
			limiter = events
		case *wire.ClientEnvelope_Resync:
			limiter = resyncs
		case *wire.ClientEnvelope_Ping:
			limiter = pings
		}
		if !total.Allow() || limiter != total && !limiter.Allow() {
			failure := &wire.Error{
				Code:                  wire.ErrorCode_ERROR_CODE_RATE_LIMITED,
				RelatedClientSequence: envelope.Sequence,
				RetryAfterMs:          1000,
				Message:               "rate limited",
			}
			c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Error{Error: failure}})
			continue
		}
		if !c.handle(envelope) {
			return
		}
	}
}

func (c *client) handle(envelope *wire.ClientEnvelope) bool {
	r := c.room
	now := c.server.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.member.connection != c || !now.Before(r.expires) || c.member.left {
		return false
	}
	switch body := envelope.Body.(type) {
	case *wire.ClientEnvelope_StateUpdate:
		r.revision++
		state := &wire.PublishedState{
			ParticipantId:  c.member.id,
			Generation:     c.member.generation,
			ClientSequence: envelope.Sequence,
			RoomRevision:   r.revision,
			ReceivedUnixMs: uint64(now.UnixMilli()),
			Payload:        body.StateUpdate.Payload,
		}
		c.member.state = state
		for _, other := range r.members {
			if other != c.member && other.connection != nil {
				other.connection.queueState(state)
			}
		}
	case *wire.ClientEnvelope_Event:
		r.revision++
		event := &wire.RelayedEvent{
			ParticipantId:  c.member.id,
			Generation:     c.member.generation,
			ClientSequence: envelope.Sequence,
			RoomRevision:   r.revision,
			ReceivedUnixMs: uint64(now.UnixMilli()),
			Kind:           body.Event.Kind,
			Payload:        body.Event.Payload,
		}
		for _, other := range r.members {
			if other != c.member && other.connection != nil {
				other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Event{Event: event}})
			}
		}
		receipt := &wire.EventAccepted{ClientSequence: envelope.Sequence, RoomRevision: r.revision}
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_EventAccepted{EventAccepted: receipt}})
	case *wire.ClientEnvelope_Ping:
		pong := &wire.Pong{
			Nonce:                body.Ping.Nonce,
			ClientTimeUnixMs:     body.Ping.ClientTimeUnixMs,
			ServerReceivedUnixMs: uint64(now.UnixMilli()),
			ServerSentUnixMs:     uint64(c.server.now().UnixMilli()),
		}
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Pong{Pong: pong}})
	case *wire.ClientEnvelope_Resync:
		c.mu.Lock()
		clear(c.states)
		c.mu.Unlock()
		snapshot := &wire.SyncSnapshot{RequestSequence: envelope.Sequence, Snapshot: r.snapshotLocked(now)}
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_SyncSnapshot{SyncSnapshot: snapshot}})
	case *wire.ClientEnvelope_Leave:
		c.member.left, c.member.state = true, nil
		r.revision++
		for _, other := range r.members {
			if other != c.member && other.connection != nil {
				other.connection.clearState(c.member.id)
				transition := &wire.PeerLeft{
					ParticipantId: c.member.id,
					Generation:    c.member.generation,
					RoomRevision:  r.revision,
					Reason:        wire.PeerLeftReason_PEER_LEFT_REASON_VOLUNTARY,
				}
				other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerLeft{PeerLeft: transition}})
			}
		}
		c.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Left{Left: &wire.Left{RequestSequence: envelope.Sequence}}})
		return true
	}
	return true
}

func (c *client) disconnect() {
	r := c.room
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.member.connection != c {
		return
	}
	c.member.connection = nil
	c.member.disconnectedAt = c.server.now()
	c.member.statusChanged = c.member.disconnectedAt
	if c.member.left {
		return
	}
	r.revision++
	for _, other := range r.members {
		if other.connection != nil {
			other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerStatusChanged{PeerStatusChanged: &wire.PeerStatusChanged{Peer: peerSnapshot(c.member), RoomRevision: r.revision}}})
		}
	}
}
