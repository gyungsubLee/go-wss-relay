package control

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

// Manager owns lobby, membership, queue, allocation and expiry state.
type Manager struct {
	mu        sync.Mutex
	allocator Allocator
	now       func() time.Time
	lobbies   map[string]*lobby
	byPlayer  map[string]string
	tickets   map[string]*matchTicket
	queue     []string
}

type lobby struct {
	Lobby
	application string
	expires     time.Time
	allocation  *Allocation
}

type matchTicket struct {
	MatchTicket
	player, application, roomID string
	capacity                    int
	expires                     time.Time
}

func NewManager(allocator Allocator, now func() time.Time) (*Manager, error) {
	if allocator == nil {
		return nil, errors.New("control: missing allocator")
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{
		allocator: allocator,
		now:       now,
		lobbies:   make(map[string]*lobby),
		byPlayer:  make(map[string]string),
		tickets:   make(map[string]*matchTicket),
	}, nil
}

func randomID(prefix string) (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func (manager *Manager) Expire() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
}

func (manager *Manager) expireLocked() {
	now := manager.now()
	for id, lobby := range manager.lobbies {
		if !lobby.Allocating && !now.Before(lobby.expires) {
			for _, who := range lobby.Participants {
				if manager.byPlayer[who] == id {
					delete(manager.byPlayer, who)
				}
			}
			delete(manager.lobbies, id)
		}
	}
	for id, ticket := range manager.tickets {
		if ticket.roomID != "" {
			group := manager.lobbies[ticket.roomID]
			if group != nil && group.Allocating {
				continue
			}
			if group == nil {
				delete(manager.tickets, id)
				continue
			}
		}
		if !now.Before(ticket.expires) {
			if manager.byPlayer[ticket.player] == id {
				delete(manager.byPlayer, ticket.player)
			}
			delete(manager.tickets, id)
		}
	}

	manager.queue = slices.DeleteFunc(manager.queue, func(id string) bool { return manager.tickets[id] == nil || manager.tickets[id].Status != "queued" })
}

// Restore imports node-owned active matches after coordinator restart.
// It never creates a replacement room or changes existing room authority.
func (manager *Manager) Restore(allocation Allocation, participants []string) error {
	if !protocol.ValidID(allocation.RoomID) || !protocol.ValidID(allocation.RoomInstanceID) || len(participants) == 0 || len(participants) > 16 {
		return &Error{"invalid", "invalid recovered match"}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	if existing := manager.lobbies[allocation.RoomID]; existing != nil {
		if existing.allocation != nil && existing.allocation.RoomInstanceID == allocation.RoomInstanceID {
			return nil
		}
		return &Error{"conflict", "recovered room conflicts with coordinator state"}
	}
	for _, participant := range participants {
		if !protocol.ValidID(participant) || manager.byPlayer[participant] != "" {
			return &Error{"conflict", "recovered participant conflicts with coordinator state"}
		}
	}
	copy := allocation
	copy.Tickets = maps.Clone(allocation.Tickets)
	manager.lobbies[allocation.RoomID] = &lobby{
		Lobby: Lobby{
			Revision:     1,
			Visibility:   "private",
			Ready:        map[string]bool{},
			ID:           allocation.RoomID,
			OwnerID:      participants[0],
			Participants: slices.Clone(participants),
			Capacity:     len(participants),
			Status:       "matched",
		},
		application: allocation.ApplicationProtocol,
		expires:     allocation.ExpiresAt,
		allocation:  &copy,
	}
	for _, participant := range participants {
		manager.byPlayer[participant] = allocation.RoomID
	}
	return nil
}

// EndMatch releases lobby/queue ownership only after its exact node authority has ended.
func (manager *Manager) EndMatch(id, instance string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.endMatchLocked(id, instance)
}

func (manager *Manager) endMatchLocked(id, instance string) {
	lobby := manager.lobbies[id]
	if lobby == nil || lobby.Status == "ended" {
		return
	}
	if lobby.allocation != nil && lobby.allocation.RoomInstanceID != instance {
		return
	}
	if lobby.Status != "matched" && !lobby.AllocationPending {
		return
	}
	lobby.Status = "ended"
	lobby.AllocationPending = false
	lobby.Allocating = false
	lobby.expires = manager.now().Add(2 * time.Minute)
	for _, participant := range lobby.Participants {
		if manager.byPlayer[participant] == id {
			delete(manager.byPlayer, participant)
		}
	}
	for _, ticket := range manager.tickets {
		if ticket.roomID == id {
			ticket.Status = "ended"
			ticket.Assignment = nil
			ticket.expires = lobby.expires
		}
	}
}

func (manager *Manager) ReconcileLocal(exists func(string) bool) {
	manager.mu.Lock()
	var ended []struct{ id, instance string }
	for id, lobby := range manager.lobbies {
		if lobby.allocation != nil && lobby.Status == "matched" && !exists(lobby.allocation.RoomInstanceID) {
			ended = append(ended, struct{ id, instance string }{id, lobby.allocation.RoomInstanceID})
		}
	}
	manager.mu.Unlock()
	for _, room := range ended {
		manager.EndMatch(room.id, room.instance)
	}
}

func (manager *Manager) Create(player string, capacity int, application, visibility string) (Lobby, error) {
	if capacity < 2 || capacity > 16 || !protocol.ValidID(application) || !protocol.ValidID(player) || visibility != "public" && visibility != "private" {
		return Lobby{}, &Error{"invalid", "invalid request"}
	}
	id, err := randomID("l-")
	if err != nil {
		return Lobby{}, &Error{"unavailable", "random source failed"}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	if manager.byPlayer[player] != "" {
		return Lobby{}, &Error{"conflict", "already in lobby or queue"}
	}
	if len(manager.lobbies) >= 4096 {
		return Lobby{}, &Error{"unavailable", "lobby capacity exhausted"}
	}
	group := &lobby{
		Lobby: Lobby{
			Revision:     1,
			Visibility:   visibility,
			ID:           id,
			OwnerID:      player,
			Participants: []string{player},
			Capacity:     capacity,
			Status:       "open",
		},
		application: application,
		expires:     manager.now().Add(30 * time.Minute),
	}
	resetReady(group)
	manager.lobbies[id] = group
	manager.byPlayer[player] = id
	return group.view(), nil
}

func (manager *Manager) authorizedLobby(player, id string) (*lobby, error) {
	group := manager.lobbies[id]
	if group == nil {
		return nil, &Error{"not_found", "lobby not found"}
	}
	if !slices.Contains(group.Participants, player) {
		return nil, &Error{"forbidden", "not a participant"}
	}
	return group, nil
}

func (manager *Manager) Get(player, id string) (Lobby, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	group, err := manager.authorizedLobby(player, id)
	if err != nil {
		return Lobby{}, err
	}
	return group.view(), nil
}

func (manager *Manager) Join(player, id string, revision uint32) (Lobby, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	group := manager.lobbies[id]
	if group == nil {
		return Lobby{}, &Error{"not_found", "lobby not found"}
	}
	if slices.Contains(group.Participants, player) {
		return group.view(), nil
	}
	if err := currentRevision(group, revision, false); err != nil {
		return Lobby{}, err
	}
	if manager.byPlayer[player] != "" || group.Status != "open" || group.Allocating || len(group.Participants) >= group.Capacity {
		return Lobby{}, &Error{"conflict", "cannot join lobby"}
	}
	group.Participants = append(group.Participants, player)
	resetReady(group)
	group.Revision++
	manager.byPlayer[player] = group.ID
	return group.view(), nil
}

func (manager *Manager) Start(ctx context.Context, player, id string, revision uint32) error {
	manager.mu.Lock()
	manager.expireLocked()
	group, err := manager.authorizedLobby(player, id)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	if group.OwnerID != player {
		manager.mu.Unlock()
		return &Error{"forbidden", "owner required"}
	}
	if group.Status == "matched" {
		manager.mu.Unlock()
		return nil
	}
	if err := currentRevision(group, revision, true); err != nil {
		manager.mu.Unlock()
		return err
	}
	allReady := true
	for _, id := range group.Participants {
		allReady = allReady && group.Ready[id]
	}
	if !allReady || group.Allocating || len(group.Participants) != group.Capacity {
		manager.mu.Unlock()
		return &Error{"conflict", "lobby not ready"}
	}
	participants := manager.prepareAllocationLocked(group)
	manager.mu.Unlock()
	if err := manager.allocate(ctx, group, participants); err != nil {
		return &Error{"unavailable", "allocation unavailable; retry the same lobby"}
	}
	return nil
}

func (manager *Manager) Assignment(ctx context.Context, player, id string, refresh bool) (Assignment, error) {
	manager.mu.Lock()
	manager.expireLocked()
	group, err := manager.authorizedLobby(player, id)
	if err != nil {
		manager.mu.Unlock()
		return Assignment{}, err
	}
	if group.Status == "ended" {
		manager.mu.Unlock()
		return Assignment{}, &Error{"gone", "match ended"}
	}
	if group.allocation == nil {
		manager.mu.Unlock()
		return Assignment{}, &Error{"conflict", "match not formed"}
	}
	allocation := *group.allocation
	allocation.Tickets = maps.Clone(allocation.Tickets)
	result := allocation.Assignment(player)
	manager.mu.Unlock()
	if refresh || result.ConnectTicket == "" {
		ticket, err := manager.allocator.Refresh(ctx, allocation, player)
		if err != nil {
			kind := "unavailable"
			if errors.Is(err, relay.ErrGone) || errors.Is(err, relay.ErrInvalid) {
				kind = "gone"
			}
			return Assignment{}, &Error{kind, "room unavailable"}
		}
		result.ConnectTicket = ticket.Value
		result.TicketExpiresAt = ticket.ExpiresAt
	}
	return result, nil
}
