package coordinator

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/control"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

type Recovered struct {
	Allocation   control.Allocation
	Participants []string
}

// Reconcile is a pull heartbeat. Unknown nodes remain excluded until inventory is recovered.
func (pool *Pool) Reconcile(ctx context.Context) error {
	pool.reconcileMu.Lock()
	defer pool.reconcileMu.Unlock()
	pool.mu.Lock()
	for id, owner := range pool.owners {
		if !owner.expires.IsZero() && !pool.now().Before(owner.expires) {
			delete(pool.owners, id)
		}
	}
	nodes := make([]*node, 0, len(pool.nodes))
	for _, n := range pool.nodes {
		nodes = append(nodes, n)
	}
	pool.mu.Unlock()
	var work sync.WaitGroup
	var resultMu sync.Mutex
	var result error
	for _, n := range nodes {
		work.Go(func() {
			started := pool.now()
			var report relay.NodeStatus
			err := n.call(ctx, "GET", "/v2/node", nil, &report)
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if err != nil || report.NodeID != n.config.ID || report.NodeInstanceID == "" || report.ProtocolRevision != 2 {
				n.healthy = false
				return
			}
			previous := n.report
			elapsed := pool.now().Sub(n.seen).Seconds()
			if elapsed > 0 && previous.NodeInstanceID == report.NodeInstanceID && report.Stats.EgressBytes >= previous.Stats.EgressBytes {
				n.egressBps = float64(report.Stats.EgressBytes-previous.Stats.EgressBytes) / elapsed
			} else {
				n.egressBps = 0
			}
			n.report = report
			n.seen = pool.now()
			n.healthy = true
			n.rooms = make(map[string]bool)
			for _, room := range report.Rooms {
				if !protocol.ValidID(room.RoomID) || !protocol.ValidID(room.RoomInstanceID) || len(room.Participants) > 16 || room.RemainingTTLMillis <= 0 {
					continue
				}
				n.rooms[room.RoomID] = true
				owner := pool.owners[room.RoomID]
				if owner != nil && owner.nodeID != n.config.ID && !owner.ended {
					n.healthy = false
					pool.conflicted = true
					if other := pool.nodes[owner.nodeID]; other != nil {
						other.healthy = false
					}
					resultMu.Lock()
					result = errors.New("conflicting room ownership; node placement disabled")
					resultMu.Unlock()
					continue
				}
				if owner == nil {
					ordered := slices.Clone(room.Participants)
					if index := slices.Index(ordered, room.OwnerID); index > 0 {
						ordered[0], ordered[index] = ordered[index], ordered[0]
					}
					allocation := control.Allocation{RoomID: room.RoomID, RoomInstanceID: room.RoomInstanceID, NodeID: n.config.ID, NodeInstanceID: report.NodeInstanceID, WebSocketURL: n.config.WebSocketURL, ApplicationProtocol: room.ApplicationProtocol,
						ExpiresAt: started.Add(time.Duration(room.RemainingTTLMillis) * time.Millisecond), Tickets: make(map[string]string)}
					for _, participant := range room.Participants {
						allocation.Tickets[participant] = ""
					}
					pool.owners[room.RoomID] = &ownedRoom{nodeID: n.config.ID, instanceID: report.NodeInstanceID, fingerprint: fingerprint(room.RoomID, room.ApplicationProtocol, room.Participants), participants: ordered, application: room.ApplicationProtocol, allocation: &allocation, confirmed: pool.now(), expires: allocation.ExpiresAt.Add(2 * time.Minute)}
				}
			}
			for id, owner := range pool.owners {
				if owner.nodeID != n.config.ID {
					continue
				}
				if owner.confirmed.IsZero() && !owner.lastAttempt.IsZero() && started.Sub(owner.lastAttempt) > 30*time.Second && !n.rooms[id] {
					owner.ended = true
				}
				if owner.instanceID != report.NodeInstanceID || !owner.confirmed.IsZero() && owner.confirmed.Before(started) && !n.rooms[id] {
					owner.ended = true
				}
			}
		})
	}
	work.Wait()
	pool.mu.Lock()
	pool.conflicted = result != nil
	pool.mu.Unlock()
	return result
}

func (pool *Pool) Recovered() []Recovered {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	var result []Recovered
	for _, owner := range pool.owners {
		if !owner.ended && owner.allocation != nil {
			result = append(result, Recovered{Allocation: cloneAllocation(*owner.allocation), Participants: slices.Clone(owner.participants)})
		}
	}
	return result
}

func (pool *Pool) Ready() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.conflicted {
		return false
	}
	for _, n := range pool.nodes {
		if n.healthy && pool.now().Sub(n.seen) <= 15*time.Second && n.report.Accepting && !n.report.Draining {
			return true
		}
	}
	return false
}

func (pool *Pool) Ended() map[string]string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	result := make(map[string]string)
	for id, owner := range pool.owners {
		if owner.ended {
			instance := ""
			if owner.allocation != nil {
				instance = owner.allocation.RoomInstanceID
			}
			result[id] = instance
		}
	}
	return result
}
