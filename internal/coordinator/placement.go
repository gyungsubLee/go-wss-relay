package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/control"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

type ownedRoom struct {
	operation    sync.Mutex
	nodeID       string
	instanceID   string
	fingerprint  [32]byte
	participants []string
	application  string
	allocation   *control.Allocation
	confirmed    time.Time
	lastAttempt  time.Time
	ended        bool
	expires      time.Time
}

type Pool struct {
	conflicted  bool
	reconcileMu sync.Mutex
	mu          sync.Mutex
	nodes       map[string]*node
	owners      map[string]*ownedRoom
	now         func() time.Time
}

func fingerprint(id, application string, participants []string) [32]byte {
	sorted := slices.Clone(participants)
	slices.Sort(sorted)
	data, _ := json.Marshal(struct {
		ID, Application string
		Participants    []string
	}{id, application, sorted})
	return sha256.Sum256(data)
}

func (pool *Pool) Allocate(ctx context.Context, id, application string, participants []string) (control.Allocation, error) {
	sum := fingerprint(id, application, participants)
	pool.mu.Lock()
	if pool.conflicted {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrConflict
	}
	owner := pool.owners[id]
	if owner != nil && owner.fingerprint != sum {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrConflict
	}
	if owner != nil && owner.ended {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrGone
	}
	if owner != nil && owner.allocation != nil {
		result := cloneAllocation(*owner.allocation)
		pool.mu.Unlock()
		return result, nil
	}
	if owner == nil {
		var selected *node
		best := 2.0
		for _, candidate := range pool.nodes {
			report := candidate.report
			if !candidate.healthy || pool.now().Sub(candidate.seen) > 15*time.Second ||
				!report.Accepting || report.Draining || report.MaxParticipants <= 0 ||
				report.Stats.QueueP95UpperMs < 0 || report.Stats.QueueP95UpperMs > 100 ||
				report.Stats.SystemBytes > 512<<20 ||
				report.Stats.GoCPUReady && report.Stats.GoBusyFractionEstimate >= .7 {
				continue
			}
			reserved := report.Stats.ReservedParticipants
			roomCount := len(report.Rooms)
			for roomID, pending := range pool.owners {
				if pending.nodeID == candidate.config.ID && !pending.ended && !candidate.rooms[roomID] {
					reserved += len(pending.participants)
					roomCount++
				}
			}
			limit := report.MaxParticipants
			if !report.Stats.GoCPUReady {
				limit = min(limit, 16)
			}
			if reserved+len(participants) > limit || roomCount >= report.MaxRooms {
				continue
			}
			byteLoad := candidate.egressBps / float64(max(1, report.Stats.Traffic.NodeEgress))
			if byteLoad >= .8 {
				continue
			}
			score := max(float64(reserved)/float64(report.MaxParticipants), byteLoad)
			if score < best || score == best && selected != nil && candidate.config.ID < selected.config.ID {
				selected = candidate
				best = score
			}
		}
		if selected == nil {
			pool.mu.Unlock()
			return control.Allocation{}, relay.ErrCapacity
		}
		owner = &ownedRoom{nodeID: selected.config.ID, instanceID: selected.report.NodeInstanceID, fingerprint: sum, participants: slices.Clone(participants), application: application, expires: pool.now().Add(32 * time.Minute)}
		pool.owners[id] = owner
	}
	pool.mu.Unlock()
	return pool.reserve(ctx, owner, id, application, participants)
}

func (pool *Pool) reserve(ctx context.Context, owner *ownedRoom, id, application string, participants []string) (control.Allocation, error) {
	owner.operation.Lock()
	defer owner.operation.Unlock()
	pool.mu.Lock()
	if pool.owners[id] != owner {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrCapacity
	}
	if owner.ended {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrGone
	}
	if owner.allocation != nil {
		result := cloneAllocation(*owner.allocation)
		pool.mu.Unlock()
		return result, nil
	}

	n := pool.nodes[owner.nodeID]
	if n == nil || n.report.NodeInstanceID != owner.instanceID {
		pool.mu.Unlock()
		return control.Allocation{}, relay.ErrGone
	}
	if !n.healthy {
		pool.mu.Unlock()
		return control.Allocation{}, errors.New("reservation owner is unhealthy; outcome unknown")
	}
	expectedInstance := owner.instanceID
	pool.mu.Unlock()
	request := map[string]any{"requestId": id, "roomId": id, "applicationProtocol": application, "participants": participants, "ttlSeconds": 1800, "nodeInstanceId": expectedInstance}
	started := pool.now()
	var response relay.NodeAllocation
	err := n.call(ctx, "POST", "/v2/reservations", request, &response)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if owner.ended || n.report.NodeInstanceID != expectedInstance {
		return control.Allocation{}, relay.ErrGone
	}
	if err != nil {
		owner.lastAttempt = pool.now()
		if errors.Is(err, relay.ErrGone) {
			owner.ended = true
		}
		if errors.Is(err, relay.ErrCapacity) && pool.owners[id] == owner {
			delete(pool.owners, id)
		}
		// Ambiguous RPC failures keep the original owner. They never trigger placement elsewhere.
		return control.Allocation{}, err
	}
	if response.RoomID != id || response.NodeID != n.config.ID || response.NodeInstanceID != expectedInstance || response.WebSocketURL != n.config.WebSocketURL || response.ApplicationProtocol != application || !protocol.ValidID(response.RoomInstanceID) || response.RemainingTTLMillis <= 0 || len(response.Tickets) != len(participants) {
		return control.Allocation{}, errors.New("invalid allocation response")
	}
	for _, participant := range participants {
		if response.Tickets[participant] == "" {
			return control.Allocation{}, errors.New("incomplete node grants")
		}
	}
	result := control.Allocation{RoomID: id, RoomInstanceID: response.RoomInstanceID, NodeID: n.config.ID, NodeInstanceID: expectedInstance, WebSocketURL: response.WebSocketURL, ApplicationProtocol: application, Tickets: response.Tickets, TicketExpiresAt: response.TicketExpiresAt,
		ExpiresAt: started.Add(time.Duration(response.RemainingTTLMillis) * time.Millisecond)}
	owner.allocation = &result
	owner.confirmed = pool.now()
	owner.expires = result.ExpiresAt.Add(2 * time.Minute)
	return cloneAllocation(result), nil
}

func cloneAllocation(a control.Allocation) control.Allocation {
	copy := make(map[string]string, len(a.Tickets))
	for id, ticket := range a.Tickets {
		copy[id] = ticket
	}
	a.Tickets = copy
	return a
}

func (pool *Pool) Refresh(ctx context.Context, allocation control.Allocation, participant string) (relay.Ticket, error) {
	pool.mu.Lock()
	owner := pool.owners[allocation.RoomID]
	if owner == nil || owner.ended || owner.instanceID != allocation.NodeInstanceID || !slices.Contains(owner.participants, participant) {
		pool.mu.Unlock()
		return relay.Ticket{}, relay.ErrGone
	}
	n := pool.nodes[owner.nodeID]
	pool.mu.Unlock()
	var ticket relay.Ticket
	err := n.call(ctx, "POST", "/v2/rooms/"+allocation.RoomInstanceID+"/tickets", map[string]string{"participantId": participant, "nodeInstanceId": allocation.NodeInstanceID}, &ticket)
	return ticket, err
}
