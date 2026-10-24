package control

import (
	"maps"
	"slices"
)

// Lobby is a detached snapshot of the public lobby state.
type Lobby struct {
	Revision          uint32          `json:"revision"`
	Ready             map[string]bool `json:"ready"`
	Visibility        string          `json:"visibility"`
	ID                string          `json:"id"`
	OwnerID           string          `json:"ownerId"`
	Participants      []string        `json:"participants"`
	Capacity          int             `json:"capacity"`
	Status            string          `json:"status"`
	Allocating        bool            `json:"allocating"`
	AllocationPending bool            `json:"allocationPending"`
}

type LobbyPreview struct {
	ID                  string `json:"id"`
	Revision            uint32 `json:"revision"`
	Capacity            int    `json:"capacity"`
	ParticipantCount    int    `json:"participantCount"`
	Visibility          string `json:"visibility"`
	ApplicationProtocol string `json:"applicationProtocol"`
}

type LobbySummary struct {
	ID               string `json:"id"`
	Revision         uint32 `json:"revision"`
	OwnerID          string `json:"ownerId"`
	Capacity         int    `json:"capacity"`
	ParticipantCount int    `json:"participantCount"`
}

type LobbyPage struct {
	Lobbies    []LobbySummary `json:"lobbies"`
	NextCursor string         `json:"nextCursor"`
}

type MatchTicket struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Assignment *Assignment `json:"assignment,omitempty"`
}

func (group *lobby) view() Lobby {
	result := group.Lobby
	result.Participants = slices.Clone(result.Participants)
	result.Ready = maps.Clone(result.Ready)
	return result
}

func (ticket *matchTicket) view() MatchTicket {
	result := ticket.MatchTicket
	if result.Assignment != nil {
		result.Assignment = new(*result.Assignment)
	}
	return result
}
