package relay

import (
	"crypto/sha256"
	"encoding/json"
	"slices"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
)

type reservation struct {
	fingerprint [32]byte
	allocation  Allocation
	expires     time.Time
	ended       bool
}

type RoomInfo struct {
	OwnerID             string    `json:"ownerId"`
	RoomID              string    `json:"roomId"`
	RoomInstanceID      string    `json:"roomInstanceId"`
	ApplicationProtocol string    `json:"applicationProtocol"`
	Participants        []string  `json:"participants"`
	ExpiresAt           time.Time `json:"expiresAt"`
	RemainingTTLMillis  int64     `json:"remainingTtlMillis"`
	Connected           int       `json:"connected"`
}

type NodeStatus struct {
	ProtocolRevision uint32     `json:"protocolRevision"`
	Accepting        bool       `json:"accepting"`
	NodeID           string     `json:"nodeId"`
	NodeInstanceID   string     `json:"nodeInstanceId"`
	Draining         bool       `json:"draining"`
	MaxRooms         int        `json:"maxRooms"`
	MaxParticipants  int        `json:"maxParticipants"`
	Stats            Stats      `json:"stats"`
	Rooms            []RoomInfo `json:"rooms"`
}

// Reserve commits once for a stable request ID. Repeated bodies cannot change a room.
// The control lock never covers gameplay fan-out or socket writes.
func (server *Server) Reserve(requestID, id, application string, participants []string, ttl time.Duration) (Allocation, error) {
	if !protocol.ValidID(requestID) {
		return Allocation{}, ErrInvalid
	}
	sorted := slices.Clone(participants)
	slices.Sort(sorted)
	data, err := json.Marshal(struct {
		ID, Application string
		Participants    []string
		TTL             int64
	}{id, application, sorted, int64(ttl)})
	if err != nil {
		return Allocation{}, err
	}
	fingerprint := sha256.Sum256(data)
	server.reservationMu.Lock()
	defer server.reservationMu.Unlock()
	now := server.now()
	server.mu.Lock()
	live := make(map[string]bool, len(server.rooms))
	for instance := range server.rooms {
		live[instance] = true
	}
	server.mu.Unlock()
	// ponytail: scan a bounded control-plane cache (active rooms + 4096 tombstones).
	// Replace this with expiry indexing only if reservation throughput requires it.
	for key, record := range server.reservations {
		if !live[record.allocation.RoomInstanceID] && !record.ended {
			record.ended = true
			record.expires = minTime(record.expires, now.Add(2*time.Minute))
			server.reservations[key] = record
		}
		if !now.Before(record.expires) {
			delete(server.reservations, key)
		}
	}
	if record, ok := server.reservations[requestID]; ok {
		if record.fingerprint != fingerprint {
			return Allocation{}, ErrConflict
		}
		if record.ended {
			return Allocation{}, ErrGone
		}
		result := record.allocation
		result.Tickets = cloneTickets(result.Tickets)
		return result, nil
	}
	if len(server.reservations) >= server.roomLimit+4096 {
		return Allocation{}, ErrCapacity
	}
	result, err := server.CreateRoom(id, application, participants, ttl)
	if err != nil {
		return Allocation{}, err
	}
	cached := result
	cached.Tickets = cloneTickets(result.Tickets)
	server.reservations[requestID] = reservation{fingerprint: fingerprint, allocation: cached, expires: result.ExpiresAt}
	return result, nil
}
func cloneTickets(tickets map[string]string) map[string]string {
	clone := make(map[string]string, len(tickets))
	for id, ticket := range tickets {
		clone[id] = ticket
	}
	return clone
}
func (server *Server) InstanceID() string { return server.instanceID }
func (server *Server) SetDraining(draining bool) {
	server.mu.Lock()
	server.draining = draining
	server.mu.Unlock()
}
func (server *Server) Inventory() NodeStatus {
	result := NodeStatus{ProtocolRevision: 2, NodeID: server.nodeID, NodeInstanceID: server.instanceID, MaxRooms: server.roomLimit, MaxParticipants: server.connectionLimit, Stats: server.Stats(), Rooms: []RoomInfo{}}
	server.mu.Lock()
	defer server.mu.Unlock()
	result.Draining = server.draining
	result.Accepting = !server.draining && !server.closed && time.Now().UnixNano() >= server.metrics.pressureUntil.Load()
	for _, room := range server.rooms {
		room.mu.Lock()
		entry := RoomInfo{OwnerID: room.ownerID, RoomID: room.id, RoomInstanceID: room.instanceID, ApplicationProtocol: room.applicationProtocol, ExpiresAt: room.expires, RemainingTTLMillis: max(int64(0), room.expires.Sub(server.now()).Milliseconds()), Participants: []string{}}
		for _, member := range room.members {
			entry.Participants = append(entry.Participants, member.id)
			if member.connection != nil {
				entry.Connected++
			}
		}
		slices.Sort(entry.Participants)
		result.Rooms = append(result.Rooms, entry)
		room.mu.Unlock()
	}
	slices.SortFunc(result.Rooms, func(a, b RoomInfo) int {
		if a.RoomID < b.RoomID {
			return -1
		}
		if a.RoomID > b.RoomID {
			return 1
		}
		return 0
	})
	return result
}

func (server *Server) Drained() bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.draining && len(server.rooms) == 0
}
