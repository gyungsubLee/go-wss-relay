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
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalid  = errors.New("relayv2: invalid allocation or configuration")
	ErrCapacity = errors.New("relayv2: capacity exhausted")
	ErrClosed   = errors.New("relayv2: closed")
	ErrConflict = errors.New("relayv2: room already exists")
)

const (
	maxRooms        = 256
	maxParticipants = 16
	maxConnections  = 1024
	resumeGrace     = 30 * time.Second
)

type Config struct {
	NodeID         string
	AllowedOrigins []string
	Now            func() time.Time
}

type Server struct {
	mu          sync.Mutex
	nodeID      string
	now         func() time.Time
	accept      websocket.AcceptOptions
	rooms       map[string]*room
	tickets     map[[32]byte]credential
	resumes     map[[32]byte]credential
	connections int
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
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
	expires                             time.Time
	revision                            uint64
	members                             map[string]*member
}

type member struct {
	id             string
	generation     uint32
	connection     *client
	state          *wire.PublishedState
	disconnectedAt time.Time
	statusChanged  time.Time
	resumeHash     [32]byte
	left           bool
}

type Allocation struct {
	RoomID         string
	RoomInstanceID string
	Tickets        map[string]string
	ExpiresAt      time.Time
}

func New(config Config) (*Server, error) {
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
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		nodeID: config.NodeID,
		now:    config.Now,
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
	r := &room{id: id, instanceID: "ri-" + instance, applicationProtocol: applicationProtocol, expires: now.Add(ttl), members: members}
	allocation := Allocation{RoomID: id, RoomInstanceID: r.instanceID, Tickets: make(map[string]string), ExpiresAt: r.expires}
	pending := make(map[[32]byte]credential)
	for id, m := range members {
		token, err := randomToken()
		if err != nil {
			return Allocation{}, err
		}
		allocation.Tickets[id] = token
		pending[sha256.Sum256([]byte(token))] = credential{room: r, member: m, expires: now.Add(min(ttl, 30*time.Second))}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closed {
		return Allocation{}, ErrClosed
	}
	server.expireLocked(now)
	for _, existing := range server.rooms {
		if existing.id == id {
			return Allocation{}, ErrConflict
		}
	}
	if len(server.rooms) >= maxRooms {
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
	if !resume && m.generation != 0 {
		return nil, wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT
	}
	if m.generation == math.MaxUint32 {
		return nil, wire.ErrorCode_ERROR_CODE_SESSION_CONFLICT
	}
	delete(credentials, hash)
	delete(server.resumes, m.resumeHash)
	if m.connection != nil {
		m.connection.cancel()
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
		for _, m := range r.members {
			if !now.Before(r.expires) {
				if m.connection != nil {
					m.connection.cancel()
				}
				continue
			}
			if m.generation > 0 && !m.left && m.connection == nil && !now.Before(m.disconnectedAt.Add(resumeGrace)) {
				m.left, m.state = true, nil
				r.revision++
				for _, other := range r.members {
					if other.connection != nil {
						other.connection.clearState(m.id)
						transition := &wire.PeerLeft{
							ParticipantId: m.id,
							Generation:    m.generation,
							RoomRevision:  r.revision,
							Reason:        wire.PeerLeftReason_PEER_LEFT_REASON_RESUME_EXPIRED,
						}
						other.connection.enqueue(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_PeerLeft{PeerLeft: transition}})
					}
				}
			}
		}
		r.mu.Unlock()
		if !now.Before(r.expires) {
			delete(server.rooms, id)
		}
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
	server.mu.Lock()
	server.closed = true
	server.cancel()
	server.mu.Unlock()
	server.wg.Wait()
	return nil
}
