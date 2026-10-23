package relay

import (
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
)

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
