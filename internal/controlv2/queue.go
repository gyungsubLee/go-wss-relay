package controlv2

import (
	"context"
	"net/http"
	"slices"
	"time"
)

func (server *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	request, ok := readGroup(w, r)
	if !ok {
		return
	}
	id, err := randomID("q-")
	if err != nil {
		problem(w, 503, "random source failed")
		return
	}
	server.mu.Lock()
	server.expireLocked()
	who := player(r)
	if server.byPlayer[who] != "" {
		server.mu.Unlock()
		problem(w, 409, "already in lobby or queue")
		return
	}
	if len(server.tickets) >= 4096 {
		server.mu.Unlock()
		problem(w, 503, "queue capacity exhausted")
		return
	}
	ticket := &matchTicket{ID: id, Status: "queued", player: who, application: request.ApplicationProtocol, capacity: request.Capacity, expires: server.now().Add(2 * time.Minute)}
	server.tickets[id] = ticket
	server.byPlayer[who] = id
	server.queue = append(server.queue, id)
	var candidates []*matchTicket
	for _, queuedID := range server.queue {
		candidate := server.tickets[queuedID]
		if candidate != nil && candidate.roomID == "" && candidate.application == ticket.application && candidate.capacity == ticket.capacity {
			candidates = append(candidates, candidate)
		}
		if len(candidates) == ticket.capacity {
			break
		}
	}
	var grouped *lobby
	if len(candidates) == ticket.capacity && len(server.lobbies) < 4096 {
		roomID, err := randomID("l-")
		if err != nil {
			server.mu.Unlock()
			problem(w, 503, "random source failed")
			return
		}
		participants := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			candidate.roomID = roomID
			server.byPlayer[candidate.player] = roomID
			participants = append(participants, candidate.player)
		}
		grouped = &lobby{Revision: 1, Visibility: "private", Ready: map[string]bool{}, ID: roomID, OwnerID: participants[0], Participants: participants, Capacity: ticket.capacity, Status: "open", application: ticket.application, expires: server.now().Add(2 * time.Minute)}
		server.lobbies[roomID] = grouped
	}
	server.mu.Unlock()
	if grouped != nil {
		server.allocateQueue(r.Context(), grouped.ID)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	reply(w, 201, ticket)
}

// A formed group keeps the same room ID on retries, including ambiguous network outcomes.
func (server *Server) allocateQueue(ctx context.Context, id string) {
	server.mu.Lock()
	group := server.lobbies[id]
	if group == nil || group.Allocating || group.Status == "matched" {
		server.mu.Unlock()
		return
	}
	group.Allocating = true
	participants := slices.Clone(group.Participants)
	application := group.application
	server.mu.Unlock()
	allocation, err := server.allocator.Allocate(ctx, id, application, participants)
	server.mu.Lock()
	defer server.mu.Unlock()
	group.Allocating = false
	if err != nil {
		return
	}
	group.allocation = &allocation
	group.expires = allocation.ExpiresAt
	group.Status = "matched"
	for _, ticket := range server.tickets {
		if ticket.roomID == id {
			assignment := allocation.Assignment(ticket.player)
			ticket.Assignment = &assignment
			ticket.Status = "matched"
			ticket.expires = allocation.ExpiresAt
		}
	}
}

func (server *Server) ticket(w http.ResponseWriter, r *http.Request) {
	server.mu.Lock()
	server.expireLocked()
	ticket := server.tickets[r.PathValue("id")]
	if ticket == nil {
		server.mu.Unlock()
		problem(w, 404, "ticket not found")
		return
	}
	if ticket.player != player(r) {
		server.mu.Unlock()
		problem(w, 403, "not your ticket")
		return
	}
	roomID := ticket.roomID
	server.mu.Unlock()
	if roomID != "" {
		server.allocateQueue(r.Context(), roomID)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	reply(w, 200, ticket)
}
func (server *Server) cancel(w http.ResponseWriter, r *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	ticket := server.tickets[r.PathValue("id")]
	if ticket == nil {
		problem(w, 404, "ticket not found")
		return
	}
	if ticket.player != player(r) {
		problem(w, 403, "not your ticket")
		return
	}
	if ticket.roomID != "" {
		problem(w, 409, "match already forming")
		return
	}
	delete(server.tickets, ticket.ID)
	delete(server.byPlayer, ticket.player)
	server.queue = slices.DeleteFunc(server.queue, func(id string) bool { return id == ticket.ID })
	w.WriteHeader(204)
}
