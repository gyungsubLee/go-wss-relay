package control

import (
	"context"
	"slices"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
)

func (manager *Manager) Enqueue(ctx context.Context, player string, capacity int, application string) (MatchTicket, error) {
	if capacity < 2 || capacity > 16 || !protocol.ValidID(application) || !protocol.ValidID(player) {
		return MatchTicket{}, &Error{"invalid", "invalid request"}
	}
	id, err := randomID("q-")
	if err != nil {
		return MatchTicket{}, &Error{"unavailable", "random source failed"}
	}
	manager.mu.Lock()
	manager.expireLocked()
	if manager.byPlayer[player] != "" {
		manager.mu.Unlock()
		return MatchTicket{}, &Error{"conflict", "already in lobby or queue"}
	}
	if len(manager.tickets) >= 4096 {
		manager.mu.Unlock()
		return MatchTicket{}, &Error{"unavailable", "queue capacity exhausted"}
	}
	ticket := &matchTicket{
		MatchTicket: MatchTicket{ID: id, Status: "queued"},
		player:      player,
		application: application,
		capacity:    capacity,
		expires:     manager.now().Add(2 * time.Minute),
	}
	manager.tickets[id] = ticket
	manager.byPlayer[player] = id
	manager.queue = append(manager.queue, id)
	var candidates []*matchTicket
	for _, queuedID := range manager.queue {
		candidate := manager.tickets[queuedID]
		if candidate != nil && candidate.roomID == "" && candidate.application == ticket.application && candidate.capacity == ticket.capacity {
			candidates = append(candidates, candidate)
		}
		if len(candidates) == ticket.capacity {
			break
		}
	}
	var grouped *lobby
	if len(candidates) == ticket.capacity && len(manager.lobbies) < 4096 {
		roomID, err := randomID("l-")
		if err != nil {
			manager.mu.Unlock()
			return MatchTicket{}, &Error{"unavailable", "random source failed"}
		}
		expires := manager.now().Add(2 * time.Minute)
		participants := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			candidate.roomID = roomID
			candidate.expires = expires
			manager.byPlayer[candidate.player] = roomID
			participants = append(participants, candidate.player)
		}
		grouped = &lobby{
			Lobby: Lobby{
				Revision:     1,
				Visibility:   "private",
				Ready:        map[string]bool{},
				ID:           roomID,
				OwnerID:      participants[0],
				Participants: participants,
				Capacity:     ticket.capacity,
				Status:       "open",
			},
			application: ticket.application,
			expires:     expires,
		}
		manager.lobbies[roomID] = grouped
	}
	manager.mu.Unlock()
	if grouped != nil {
		manager.allocateQueue(ctx, grouped.ID)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return ticket.view(), nil
}

// A formed group keeps the same room ID on retries, including ambiguous network outcomes.
func (manager *Manager) allocateQueue(ctx context.Context, id string) {
	manager.mu.Lock()
	group := manager.lobbies[id]
	if group == nil || group.Allocating || group.Status != "open" {
		manager.mu.Unlock()
		return
	}
	participants := manager.prepareAllocationLocked(group)
	manager.mu.Unlock()
	_ = manager.allocate(ctx, group, participants)
}

func (manager *Manager) Ticket(ctx context.Context, player, id string) (MatchTicket, error) {
	manager.mu.Lock()
	manager.expireLocked()
	ticket := manager.tickets[id]
	if ticket == nil {
		manager.mu.Unlock()
		return MatchTicket{}, &Error{"not_found", "ticket not found"}
	}
	if ticket.player != player {
		manager.mu.Unlock()
		return MatchTicket{}, &Error{"forbidden", "not your ticket"}
	}
	roomID := ticket.roomID
	manager.mu.Unlock()
	if roomID != "" {
		manager.allocateQueue(ctx, roomID)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.tickets[ticket.ID] != ticket {
		return MatchTicket{}, &Error{"not_found", "ticket not found"}
	}
	return ticket.view(), nil
}

func (manager *Manager) Cancel(player, id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	ticket := manager.tickets[id]
	if ticket == nil {
		return &Error{"not_found", "ticket not found"}
	}
	if ticket.player != player {
		return &Error{"forbidden", "not your ticket"}
	}
	if ticket.roomID != "" {
		return &Error{"conflict", "match already forming"}
	}
	delete(manager.tickets, ticket.ID)
	delete(manager.byPlayer, ticket.player)
	manager.queue = slices.DeleteFunc(manager.queue, func(id string) bool { return id == ticket.ID })
	return nil
}
