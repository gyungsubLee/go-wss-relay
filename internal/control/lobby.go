package control

import (
	"cmp"
	"math"
	"slices"

	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
)

func currentRevision(group *lobby, revision uint32, allowPending bool) error {
	if revision == 0 || group.Revision != revision || group.Revision == math.MaxUint32 || group.Allocating || group.AllocationPending && !allowPending || group.Status != "open" {
		return &Error{"conflict", "lobby changed; reload before retrying"}
	}
	return nil
}

func resetReady(group *lobby) {
	group.Ready = make(map[string]bool, len(group.Participants))
	for _, id := range group.Participants {
		group.Ready[id] = false
	}
}

func (manager *Manager) Preview(id string) (LobbyPreview, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	group := manager.lobbies[id]
	if group == nil || group.Status != "open" {
		return LobbyPreview{}, &Error{"not_found", "open lobby not found"}
	}
	// A private lobby code is an invite capability; exclude its roster and credentials.
	return LobbyPreview{ID: group.ID, Revision: group.Revision, Capacity: group.Capacity, ParticipantCount: len(group.Participants), Visibility: group.Visibility, ApplicationProtocol: group.application}, nil
}

func (manager *Manager) List(application, cursor string, limit int) (LobbyPage, error) {
	if !protocol.ValidID(application) || cursor != "" && !protocol.ValidID(cursor) || limit < 1 || limit > 50 {
		return LobbyPage{}, &Error{"invalid", "invalid request"}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	records := make([]LobbySummary, 0)
	for _, group := range manager.lobbies {
		if group.Visibility == "public" && group.Status == "open" && group.application == application && group.ID > cursor {
			records = append(records, LobbySummary{ID: group.ID, Revision: group.Revision, OwnerID: group.OwnerID, Capacity: group.Capacity, ParticipantCount: len(group.Participants)})
		}
	}
	slices.SortFunc(records, func(a, b LobbySummary) int { return cmp.Compare(a.ID, b.ID) })
	var next string
	if len(records) > limit {
		records = records[:limit]
		next = records[len(records)-1].ID
	}
	return LobbyPage{Lobbies: records, NextCursor: next}, nil
}

func (manager *Manager) Ready(player, id string, revision uint32, ready bool) (Lobby, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	group, err := manager.authorizedLobby(player, id)
	if err != nil {
		return Lobby{}, err
	}
	if err := currentRevision(group, revision, false); err != nil {
		return Lobby{}, err
	}
	if group.Ready[player] != ready {
		group.Ready[player] = ready
		group.Revision++
	}
	return group.view(), nil
}

func (manager *Manager) Leave(player, id string, revision uint32) (*Lobby, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.expireLocked()
	group, err := manager.authorizedLobby(player, id)
	if err != nil {
		return nil, err
	}
	if err := currentRevision(group, revision, false); err != nil {
		return nil, err
	}
	group.Participants = slices.DeleteFunc(group.Participants, func(id string) bool { return id == player })
	delete(manager.byPlayer, player)
	if len(group.Participants) == 0 {
		delete(manager.lobbies, group.ID)
		return nil, nil
	}
	if group.OwnerID == player {
		group.OwnerID = group.Participants[0]
	}
	resetReady(group)
	group.Revision++
	return new(group.view()), nil
}
