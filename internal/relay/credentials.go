package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
)

type credential struct {
	room       *room
	member     *member
	expires    time.Time
	generation uint32
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
