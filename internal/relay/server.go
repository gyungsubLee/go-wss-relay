package relay

import (
	"context"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
)

const maxReliableBytes = 256 << 10

type queuedEnvelope struct {
	envelope *wire.ServerEnvelope
	at       time.Time
}

type closeRequest struct {
	code   wire.ErrorCode
	status websocket.StatusCode
	reason string
}

type client struct {
	terminal atomic.Pointer[closeRequest]
	server   *Server
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	room     *room
	member   *member
	sequence uint32

	mu            sync.Mutex
	reliable      []queuedEnvelope
	stateTimes    map[string]time.Time
	reliableBytes int
	states        map[string]*wire.PublishedState
	notify        chan struct{}
}

func advertisedLimits() *wire.Limits {
	return &wire.Limits{
		MaxClientMessageBytes: protocol.MaxClientMessageBytes, MaxServerMessageBytes: protocol.MaxServerMessageBytes,
		MaxStatePayloadBytes: protocol.MaxStatePayloadBytes, MaxEventPayloadBytes: protocol.MaxEventPayloadBytes,
		MaxStateUpdatesPerSecond: 20, MaxEventsPerSecond: 5, StateBatchIntervalMs: 50,
		HeartbeatIntervalMs: 15000, HeartbeatTimeoutMs: 45000, ResumeGraceMs: 30000,
	}
}

func (server *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v2/relay" || request.URL.RawQuery != "" || request.Method != http.MethodGet {
		http.NotFound(w, request)
		return
	}
	release, ok := server.authAdmission.Enter(w)
	if !ok {
		return
	}
	authenticating := true
	defer func() {
		if authenticating {
			release()
		}
	}()
	ip, err := httpx.ClientIP(request, server.trustedProxies)
	if err != nil {
		http.Error(w, "invalid client address", 400)
		return
	}
	if !server.handshakeIPs.Allow(ip) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "handshake rate exceeded", 429)
		return
	}
	server.mu.Lock()
	if server.closed || server.connections >= server.connectionLimit+maxAuthenticating {
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
	conn.SetReadLimit(protocol.MaxClientMessageBytes)
	ctx, cancel := context.WithCancel(server.ctx)
	defer cancel()
	c := &client{server: server, conn: conn, ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return
	}
	server.clients[c] = struct{}{}
	server.mu.Unlock()
	defer func() { server.mu.Lock(); delete(server.clients, c); server.mu.Unlock() }()

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
	server.received(len(data))
	envelope, code := protocol.DecodeClient(data, false, 0)
	if code != 0 {
		c.writeError(code, clientErrorStatus(code, data))
		return
	}
	welcome, code := server.authenticate(c, envelope.GetConnect())
	if code != 0 {
		c.writeError(code, errorStatus(code))
		return
	}
	authenticating = false
	release()
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
	if len(data) > protocol.MaxServerMessageBytes {
		return ErrCapacity
	}
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	if c.server.egress.Tokens() < float64(len(data)) {
		c.server.pressure()
	}
	if err := c.server.egress.WaitN(ctx, len(data)); err != nil {
		return err
	}
	if c.room != nil {
		if err := c.room.egress.WaitN(ctx, len(data)); err != nil {
			return err
		}
	}
	started := time.Now()
	err = c.conn.Write(ctx, websocket.MessageBinary, data)
	c.server.metrics.writeDelay.observe(time.Since(started))
	if err != nil {
		c.server.metrics.writeErrors.Add(1)
	} else {
		c.server.metrics.sentBytes.Add(uint64(len(data)))
		c.server.metrics.sentMessages.Add(1)
	}
	return err
}

func (c *client) writeError(code wire.ErrorCode, status websocket.StatusCode) {
	_ = c.write(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Error{Error: &wire.Error{Code: code, Fatal: true, Message: "connection rejected"}}})
	_ = c.conn.Close(status, "connection rejected")
}

func (c *client) enqueue(envelope *wire.ServerEnvelope) {
	c.mu.Lock()
	defer c.mu.Unlock()
	size := proto.Size(envelope) + 12
	if len(c.reliable) >= 32 || c.reliableBytes+size > maxReliableBytes {
		if c.server != nil {
			c.server.metrics.queueOverflows.Add(1)
		}
		c.terminate(wire.ErrorCode_ERROR_CODE_SLOW_CONSUMER, websocket.StatusTryAgainLater, "send queue exceeded")
		return
	}
	c.reliable = append(c.reliable, queuedEnvelope{envelope: envelope, at: time.Now()})
	c.reliableBytes += size
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *client) clearState(id string) {
	c.mu.Lock()
	delete(c.states, id)
	delete(c.stateTimes, id)
	c.mu.Unlock()
}

func (c *client) queueState(state *wire.PublishedState) {
	c.mu.Lock()
	c.states[state.ParticipantId] = state
	if c.stateTimes == nil {
		c.stateTimes = make(map[string]time.Time)
	}
	c.stateTimes[state.ParticipantId] = time.Now()
	c.mu.Unlock()
}

func (c *client) popReliable() *wire.ServerEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reliable) == 0 {
		return nil
	}
	queued := c.reliable[0]
	envelope := queued.envelope
	if c.server != nil {
		c.server.metrics.queueDelay.observe(time.Since(queued.at))
	}
	c.reliable[0] = queuedEnvelope{}
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
		if c.server != nil && !c.stateTimes[id].IsZero() {
			c.server.metrics.queueDelay.observe(time.Since(c.stateTimes[id]))
		}
		batch.RoomRevision = max(batch.RoomRevision, state.RoomRevision)
		delete(c.states, id)
		delete(c.stateTimes, id)
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
				c.terminate(wire.ErrorCode_ERROR_CODE_ROOM_ENDED, websocket.StatusNormalClosure, "room expired")
				if c.finishTermination() {
					return
				}
			}
		}
		if c.finishTermination() {
			return
		}
		for envelope := c.popReliable(); envelope != nil; envelope = c.popReliable() {
			if c.finishTermination() {
				return
			}
			if c.write(envelope) != nil {
				_ = c.conn.Close(websocket.StatusTryAgainLater, "write deadline exceeded")
				return
			}
			if envelope.GetError().GetFatal() {
				_ = c.conn.Close(errorStatus(envelope.GetError().Code), "fatal relay error")
				return
			}
			if envelope.GetLeft() != nil {
				_ = c.conn.Close(websocket.StatusNormalClosure, "left")
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
			c.terminate(wire.ErrorCode_ERROR_CODE_INVALID_MESSAGE, websocket.StatusUnsupportedData, "binary required")
			<-c.ctx.Done()
			return
		}
		c.server.received(len(data))
		envelope, code := protocol.DecodeClient(data, true, previous)
		if code != 0 {
			c.terminate(code, clientErrorStatus(code, data), "invalid message")
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
		allowed := total.Allow() && (limiter == total || limiter.Allow())
		if allowed {
			allowed = c.server.ingress.AllowN(time.Now(), len(data))
			if !allowed {
				c.server.pressure()
			}
		}
		if allowed {
			allowed = c.room.ingress.AllowN(time.Now(), len(data))
		}
		if !allowed {
			c.server.metrics.rateRejected.Add(1)
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
			if c.terminal.Load() != nil {
				<-c.ctx.Done()
			}
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
		clear(c.stateTimes)
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

func errorStatus(code wire.ErrorCode) websocket.StatusCode {
	switch code {
	case wire.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE:
		return websocket.StatusMessageTooBig
	case wire.ErrorCode_ERROR_CODE_SLOW_CONSUMER:
		return websocket.StatusTryAgainLater
	case wire.ErrorCode_ERROR_CODE_ROOM_ENDED:
		return websocket.StatusNormalClosure
	case wire.ErrorCode_ERROR_CODE_INTERNAL:
		return websocket.StatusInternalError
	default:
		return websocket.StatusPolicyViolation
	}
}
func clientErrorStatus(code wire.ErrorCode, data []byte) websocket.StatusCode {
	if code == wire.ErrorCode_ERROR_CODE_INVALID_MESSAGE {
		var probe wire.ClientEnvelope
		if err := (proto.UnmarshalOptions{RecursionLimit: 16}).Unmarshal(data, &probe); err != nil {
			return websocket.StatusProtocolError
		}
	}
	return errorStatus(code)
}
func (c *client) terminate(code wire.ErrorCode, status websocket.StatusCode, reason string) {
	if c.terminal.CompareAndSwap(nil, &closeRequest{code: code, status: status, reason: reason}) {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

// Only the socket writer sends terminal messages; room/control code never writes the network.
func (c *client) finishTermination() bool {
	request := c.terminal.Load()
	if request == nil {
		return false
	}
	_ = c.write(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Error{Error: &wire.Error{Code: request.code, Fatal: true, Message: request.reason}}})
	_ = c.conn.Close(request.status, request.reason)
	return true
}
