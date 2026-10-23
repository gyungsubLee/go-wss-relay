package control

import (
	"context"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

// Allocator is the control boundary shared by standalone and distributed nodes.
type Allocator interface {
	Allocate(context.Context, string, string, []string) (Allocation, error)
	Refresh(context.Context, Allocation, string) (relay.Ticket, error)
}

type Allocation struct {
	RoomID              string            `json:"roomId"`
	RoomInstanceID      string            `json:"roomInstanceId"`
	NodeInstanceID      string            `json:"nodeInstanceId"`
	NodeID              string            `json:"nodeId"`
	WebSocketURL        string            `json:"websocketUrl"`
	ApplicationProtocol string            `json:"applicationProtocol"`
	ExpiresAt           time.Time         `json:"expiresAt"`
	TicketExpiresAt     time.Time         `json:"ticketExpiresAt"`
	Tickets             map[string]string `json:"tickets"`
}

type Assignment struct {
	MatchID             string    `json:"matchId"`
	Subprotocol         string    `json:"subprotocol"`
	TicketExpiresAt     time.Time `json:"ticketExpiresAt"`
	RoomID              string    `json:"roomId"`
	RoomInstanceID      string    `json:"roomInstanceId"`
	ParticipantID       string    `json:"participantId"`
	NodeID              string    `json:"nodeId"`
	WebSocketURL        string    `json:"websocketUrl"`
	ApplicationProtocol string    `json:"applicationProtocol"`
	ProtocolRevision    uint32    `json:"protocolRevision"`
	ConnectTicket       string    `json:"connectTicket"`
	ExpiresAt           time.Time `json:"expiresAt"`
}

func (a Allocation) Assignment(participant string) Assignment {
	return Assignment{MatchID: a.RoomID, Subprotocol: "relay.v2", TicketExpiresAt: a.TicketExpiresAt, RoomID: a.RoomID, RoomInstanceID: a.RoomInstanceID, ParticipantID: participant,
		NodeID: a.NodeID, WebSocketURL: a.WebSocketURL, ApplicationProtocol: a.ApplicationProtocol,
		ProtocolRevision: 2, ConnectTicket: a.Tickets[participant], ExpiresAt: a.ExpiresAt}
}

type LocalAllocator struct {
	Relay        *relay.Server
	NodeID       string
	WebSocketURL string
}

func (local LocalAllocator) Allocate(_ context.Context, id, application string, participants []string) (Allocation, error) {
	result, err := local.Relay.Reserve(id, id, application, participants, 30*time.Minute)
	if err != nil {
		return Allocation{}, err
	}
	return Allocation{RoomID: id, RoomInstanceID: result.RoomInstanceID, NodeID: local.NodeID, NodeInstanceID: local.Relay.InstanceID(),
		WebSocketURL: local.WebSocketURL, ApplicationProtocol: application, ExpiresAt: result.ExpiresAt, TicketExpiresAt: result.TicketExpiresAt, Tickets: result.Tickets}, nil
}

func (local LocalAllocator) Refresh(_ context.Context, allocation Allocation, participant string) (relay.Ticket, error) {
	return local.Relay.RefreshTicket(allocation.RoomInstanceID, participant)
}
