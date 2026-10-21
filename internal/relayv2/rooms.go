// Package relayv2 implements the first standalone WSS relay slice.
package relayv2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
	"net/netip"
)

var (
	ErrInvalid  = errors.New("relayv2: invalid allocation or configuration")
	ErrCapacity = errors.New("relayv2: capacity exhausted")
	ErrClosed   = errors.New("relayv2: closed")
	ErrConflict = errors.New("relayv2: room already exists")
	ErrGone     = errors.New("relayv2: room authority ended")
)

const (
	maxAuthenticating = 128
	maxRooms          = 256
	maxParticipants   = 16
	maxConnections    = 1024
	resumeGrace       = 30 * time.Second
)

type Config struct {
	TrustedProxies  []string
	HandshakesPerIP int
	NodeID          string
	AllowedOrigins  []string
	Now             func() time.Time
	Traffic         TrafficPolicy
	MaxRooms        int
	MaxConnections  int
}

type Server struct {
	closeOnce                  sync.Once
	clients                    map[*client]struct{}
	trustedProxies             []netip.Prefix
	handshakeIPs               *httpx.IPBudget
	authAdmission              *httpx.Admission
	instanceID                 string
	draining                   bool
	reservationMu              sync.Mutex
	reservations               map[string]reservation
	mu                         sync.Mutex
	nodeID                     string
	now                        func() time.Time
	accept                     websocket.AcceptOptions
	rooms                      map[string]*room
	tickets                    map[[32]byte]credential
	resumes                    map[[32]byte]credential
	connections                int
	ctx                        context.Context
	cancel                     context.CancelFunc
	closed                     bool
	wg                         sync.WaitGroup
	traffic                    TrafficPolicy
	ingress, egress            *rate.Limiter
	metrics                    counters
	roomLimit, connectionLimit int
}

type credential struct {
	room       *room
	member     *member
	expires    time.Time
	generation uint32
}

type room struct {
	mu                                  sync.Mutex
	id, instanceID, applicationProtocol string
	ownerID                             string
	expires                             time.Time
	reservationExpires                  time.Time
	revision                            uint64
	members                             map[string]*member
	ingress, egress                     *rate.Limiter
}

type member struct {
	id             string
	generation     uint32
	connection     *client
	state          *wire.PublishedState
	disconnectedAt time.Time
	statusChanged  time.Time
	resumeHash     [32]byte
	ticketHash     [32]byte
	left           bool
}

type Ticket struct {
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Allocation struct {
	RoomID          string            `json:"roomId"`
	RoomInstanceID  string            `json:"roomInstanceId"`
	Tickets         map[string]string `json:"tickets"`
	TicketExpiresAt time.Time         `json:"ticketExpiresAt"`
	ExpiresAt       time.Time         `json:"expiresAt"`
}

func New(config Config) (*Server, error) {
	if config.MaxRooms == 0 {
		config.MaxRooms = maxRooms
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = maxConnections
	}
	if config.MaxRooms < 1 || config.MaxRooms > 4096 || config.MaxConnections < 1 || config.MaxConnections > 8192 {
		return nil, ErrInvalid
	}
	if config.HandshakesPerIP == 0 {
		config.HandshakesPerIP = 200
	}
	if config.HandshakesPerIP < 1 || config.HandshakesPerIP > 20000 {
		return nil, ErrInvalid
	}
	trusted, err := httpx.ParseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}
	config.Traffic = defaultTraffic(config.Traffic)
	if config.Traffic.NodeIngress < 16384 || config.Traffic.NodeEgress < 131072 || config.Traffic.RoomIngress < 16384 || config.Traffic.RoomEgress < 131072 {
		return nil, ErrInvalid
	}
	if !protocol.ValidID(config.NodeID) {
		return nil, ErrInvalid
	}
	for _, origin := range config.AllowedOrigins {
		if origin == "" || origin == "*" || strings.ContainsAny(origin, " \r\n\t") {
			return nil, ErrInvalid
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	instance, err := randomToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{clients: make(map[*client]struct{}), trustedProxies: trusted, handshakeIPs: httpx.NewIPBudget(rate.Limit(config.HandshakesPerIP), config.HandshakesPerIP*2, 16384, time.Now), authAdmission: httpx.NewAdmission(1000, 2000, maxAuthenticating, time.Now),
		instanceID: "ni-" + instance, reservations: make(map[string]reservation),
		nodeID:    config.NodeID,
		roomLimit: config.MaxRooms, connectionLimit: config.MaxConnections,
		traffic: config.Traffic, ingress: byteLimiter(config.Traffic.NodeIngress), egress: byteLimiter(config.Traffic.NodeEgress),
		now: config.Now,
		accept: websocket.AcceptOptions{
			Subprotocols:    []string{"relay.v2"},
			OriginPatterns:  slices.Clone(config.AllowedOrigins),
			CompressionMode: websocket.CompressionDisabled,
		},
		rooms:   make(map[string]*room),
		tickets: make(map[[32]byte]credential),
		resumes: make(map[[32]byte]credential),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// CreateRoom is a trusted allocation API; it is not exposed to anonymous HTTP callers.
func (server *Server) CreateRoom(id, applicationProtocol string, participants []string, ttl time.Duration) (Allocation, error) {
	if !protocol.ValidID(id) || !protocol.ValidID(applicationProtocol) || len(participants) == 0 || len(participants) > maxParticipants || ttl <= 0 || ttl > 2*time.Hour {
		return Allocation{}, ErrInvalid
	}
	members := make(map[string]*member, len(participants))
	for _, id := range participants {
		if !protocol.ValidID(id) || members[id] != nil {
			return Allocation{}, ErrInvalid
		}
		members[id] = &member{id: id}
	}
	instance, err := randomToken()
	if err != nil {
		return Allocation{}, err
	}
	now := server.now()
	r := &room{ownerID: participants[0], id: id, instanceID: "ri-" + instance, applicationProtocol: applicationProtocol, expires: now.Add(ttl), reservationExpires: now.Add(min(ttl, 30*time.Second)), members: members, ingress: byteLimiter(server.traffic.RoomIngress), egress: byteLimiter(server.traffic.RoomEgress)}
	allocation := Allocation{RoomID: id, RoomInstanceID: r.instanceID, Tickets: make(map[string]string), ExpiresAt: r.expires, TicketExpiresAt: now.Add(min(ttl, 30*time.Second))}
	pending := make(map[[32]byte]credential)
	for id, m := range members {
		token, err := randomToken()
		if err != nil {
			return Allocation{}, err
		}
		allocation.Tickets[id] = token
		m.ticketHash = sha256.Sum256([]byte(token))
		pending[m.ticketHash] = credential{room: r, member: m, expires: now.Add(min(ttl, 30*time.Second))}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return Allocation{}, ErrClosed
	}
	if server.draining {
		return Allocation{}, ErrCapacity
	}
	server.expireLocked(now)
	for _, existing := range server.rooms {
		if existing.id == id {
			return Allocation{}, ErrConflict
		}
	}
	if time.Now().UnixNano() < server.metrics.pressureUntil.Load() {
		return Allocation{}, ErrCapacity
	}
	reserved := 0
	for _, existing := range server.rooms {
		existing.mu.Lock()
		for _, member := range existing.members {
			if !member.left {
				reserved++
			}
		}
		existing.mu.Unlock()
	}
	if reserved+len(members) > server.connectionLimit {
		return Allocation{}, ErrCapacity
	}
	if len(server.rooms) >= server.roomLimit {
		return Allocation{}, ErrCapacity
	}
	server.rooms[r.instanceID] = r
	for hash, credential := range pending {
		server.tickets[hash] = credential
	}
	return allocation, nil
}

func randomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func (server *Server) authenticate(c *client, connect *wire.Connect) (*wire.Welcome, wire.ErrorCode) {
	resume := connect.GetResumeToken() != ""
	token := connect.GetConnectTicket()
	if resume {
		token = connect.GetResumeToken()
	}
	hash := sha256.Sum256([]byte(token))
	newToken, err := randomToken()
	if err != nil {
		return nil, wire.ErrorCode_ERROR_CODE_INTERNAL
	}
	now := server.now()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return nil, wire.ErrorCode_ERROR_CODE_ROOM_UNAVAILABLE
	}
	credentials := server.tickets
	if resume {
		credentials = server.resumes
	}
	grant, ok := credentials[hash]
	if !ok || !now.Before(grant.expires) {
		return nil, wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED
	}
	r := grant.room
	r.mu.Lock()
	defer r.mu.Unlock()
	m := grant.member
	if !now.Before(r.expires) || m.left {
		return nil, wire.ErrorCode_ERROR_CODE_ROOM_UNAVAILABLE
	}
	if connect.ApplicationProtocol != r.applicationProtocol {
		return nil, wire.ErrorCode_ERROR_CODE_APPLICATION_PROTOCOL_MISMATCH
	}
	if resume && (grant.generation != m.generation || m.connection == nil && !now.Before(m.disconnectedAt.Add(resumeGrace))) {
		return nil, wire.ErrorCode_ERROR_CODE_UNAUTHENTICATED
	}
	if !resume && grant.generation != m.generation {
		return nil, wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT
	}
	if m.generation == math.MaxUint32 {
		return nil, wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT
	}
	// An authenticated HTTPS reissue may replace a consumed initial ticket.
	resume = resume || m.generation > 0
	delete(credentials, hash)
	delete(server.resumes, m.resumeHash)
	if m.connection != nil {
		m.connection.terminate(wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT, websocket.StatusPolicyViolation, "session replaced")
	}
	m.generation++
	m.connection = c
	m.disconnectedAt = time.Time{}
	m.statusChanged = now
	m.state = nil
	m.resumeHash = sha256.Sum256([]byte(newToken))
	server.resumes[m.resumeHash] = credential{room: r, member: m, generation: m.generation, expires: r.expires}
	c.room, c.member = r, m
	r.revision++
	for _, other := range r.members {
		if other.connection == nil || other == m {
			continue
		}
		other.connection.clearState(m.id)
		peer := peerSnapshot(m)
		if resume {
			other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerStatusChanged{PeerStatusChanged: &wire.PeerStatusChanged{Peer: peer, RoomRevision: r.revision}}})
		} else {
			other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerJoined{PeerJoined: &wire.PeerJoined{Peer: peer, RoomRevision: r.revision}}})
		}
	}
	return &wire.Welcome{
		RoomId: r.id, RoomInstanceId: r.instanceID, NodeId: server.nodeID, ParticipantId: m.id,
		Generation: m.generation, ApplicationProtocol: r.applicationProtocol, ResumeToken: newToken,
		ResumeTokenExpiresUnixMs: uint64(r.expires.UnixMilli()), RoomExpiresUnixMs: uint64(r.expires.UnixMilli()),
		Limits: advertisedLimits(), Snapshot: r.snapshotLocked(now), Resumed: resume,
	}, 0
}

func peerSnapshot(m *member) *wire.Peer {
	status := wire.ConnectionStatus_CONNECTION_STATUS_CONNECTED
	changed := m.statusChanged
	if m.connection == nil {
		status = wire.ConnectionStatus_CONNECTION_STATUS_DISCONNECTED
		changed = m.disconnectedAt
	}
	return &wire.Peer{ParticipantId: m.id, Generation: m.generation, Status: status, StatusChangedUnixMs: uint64(changed.UnixMilli())}
}

func (r *room) snapshotLocked(now time.Time) *wire.RoomSnapshot {
	snapshot := &wire.RoomSnapshot{RoomRevision: r.revision}
	for _, m := range r.members {
		if m.generation == 0 || m.left || m.connection == nil && !now.Before(m.disconnectedAt.Add(resumeGrace)) {
			continue
		}
		snapshot.Members = append(snapshot.Members, peerSnapshot(m))
		if m.state != nil {
			snapshot.States = append(snapshot.States, proto.Clone(m.state).(*wire.PublishedState))
		}
	}
	slices.SortFunc(snapshot.Members, func(a, b *wire.Peer) int { return strings.Compare(a.ParticipantId, b.ParticipantId) })
	slices.SortFunc(snapshot.States, func(a, b *wire.PublishedState) int { return strings.Compare(a.ParticipantId, b.ParticipantId) })
	return snapshot
}

func (server *Server) Snapshot(instance string) *wire.RoomSnapshot {
	server.mu.Lock()
	r := server.rooms[instance]
	server.mu.Unlock()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(server.now())
}

// Expire releases expired authority. The application owns its periodic scheduling.
func (server *Server) Expire() {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked(server.now())
}

func (server *Server) expireLocked(now time.Time) {
	for id, r := range server.rooms {
		r.mu.Lock()
		expired := !now.Before(r.expires)
		allLeft := true
		for _, m := range r.members {
			if expired || m.generation == 0 && !now.Before(r.reservationExpires) {
				m.left = true
				m.state = nil
				delete(server.tickets, m.ticketHash)
				delete(server.resumes, m.resumeHash)
				if m.connection != nil {
					m.connection.terminate(wire.ErrorCode_ERROR_CODE_ROOM_ENDED, websocket.StatusNormalClosure, "room expired")
				}
			} else if m.generation > 0 && !m.left && m.connection == nil && !now.Before(m.disconnectedAt.Add(resumeGrace)) {
				m.left = true
				m.state = nil
				delete(server.resumes, m.resumeHash)
				r.revision++
				transition := &wire.PeerLeft{ParticipantId: m.id, Generation: m.generation, RoomRevision: r.revision, Reason: wire.PeerLeftReason_PEER_LEFT_REASON_RESUME_EXPIRED}
				for _, other := range r.members {
					if other.connection != nil {
						other.connection.clearState(m.id)
						other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerLeft{PeerLeft: transition}})
					}
				}
			}
			if !m.left {
				allLeft = false
			}
		}
		if expired || allLeft {
			delete(server.rooms, id)
		}
		r.mu.Unlock()
	}
	for hash, grant := range server.tickets {
		if !now.Before(grant.expires) {
			delete(server.tickets, hash)
		}
	}
	for hash, grant := range server.resumes {
		if !now.Before(grant.expires) {
			delete(server.resumes, hash)
		}
	}
}

func (server *Server) Close() error {
	server.closeOnce.Do(func() {
		server.mu.Lock()
		server.closed = true
		clients := make([]*client, 0, len(server.clients))
		for client := range server.clients {
			clients = append(clients, client)
		}
		server.mu.Unlock()
		for _, client := range clients {
			client.terminate(wire.ErrorCode_ERROR_CODE_ROOM_UNAVAILABLE, websocket.StatusServiceRestart, "server restarting")
		}
		done := make(chan struct{})
		go func() { server.wg.Wait(); close(done) }()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		server.cancel()
		for _, client := range clients {
			_ = client.conn.CloseNow()
		}
		<-done
	})
	return nil
}

// RefreshTicket is trusted control-plane authority, never a public unauthenticated operation.
// Issuing a ticket does not evict the active connection; consuming it rotates the generation.
func (server *Server) RefreshTicket(instance, participant string) (Ticket, error) {
	token, err := randomToken()
	if err != nil {
		return Ticket{}, err
	}
	now := server.now()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return Ticket{}, ErrClosed
	}
	r := server.rooms[instance]
	if r == nil {
		return Ticket{}, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.members[participant]
	if m == nil || m.left || !now.Before(r.expires) {
		return Ticket{}, ErrInvalid
	}
	expires := minTime(now.Add(30*time.Second), r.expires)
	if m.generation == 0 {
		if !now.Before(r.reservationExpires) {
			return Ticket{}, ErrGone
		}
		expires = minTime(expires, r.reservationExpires)
	}
	if m.generation > 0 && m.connection == nil {
		deadline := m.disconnectedAt.Add(resumeGrace)
		if !now.Before(deadline) {
			return Ticket{}, ErrInvalid
		}
		expires = minTime(expires, deadline)
	}
	delete(server.tickets, m.ticketHash)
	m.ticketHash = sha256.Sum256([]byte(token))
	server.tickets[m.ticketHash] = credential{room: r, member: m, generation: m.generation, expires: expires}
	return Ticket{Value: token, ExpiresAt: expires}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// EndRoom invalidates control authority before cancelling active transports.
func (server *Server) EndRoom(instance string) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	r := server.rooms[instance]
	if r == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(server.rooms, instance)
	for _, m := range r.members {
		m.left = true
		m.state = nil
		delete(server.tickets, m.ticketHash)
		delete(server.resumes, m.resumeHash)
		if m.connection != nil {
			m.connection.terminate(wire.ErrorCode_ERROR_CODE_ROOM_ENDED, websocket.StatusNormalClosure, "room ended")
		}
	}
	return nil
}

func (server *Server) HasRoom(instance string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	room := server.rooms[instance]
	return !server.closed && room != nil && server.now().Before(room.expires)
}
