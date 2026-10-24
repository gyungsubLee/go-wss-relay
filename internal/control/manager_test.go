package control

import (
	"testing"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	node, err := relay.New(relay.Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	manager, err := NewManager(LocalAllocator{Relay: node, NodeID: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestReturnedLobbyCannotMutateManager(t *testing.T) {
	manager := newTestManager(t)
	created, err := manager.Create("alice", 3, "demo.v1", "public")
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID

	// Every operation returning a lobby must detach both mutable collections.
	check := func(view Lobby, owner string, count int, ready bool) {
		t.Helper()
		view.Participants[0] = "intruder"
		view.Ready[owner] = !ready
		view.Ready["intruder"] = true
		current, err := manager.Get(owner, id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Participants[0] != owner || len(current.Participants) != count || current.Ready[owner] != ready || current.Ready["intruder"] {
			t.Fatalf("returned lobby changed stored roster or readiness: %+v", current)
		}
	}
	check(created, "alice", 1, false)
	got, err := manager.Get("alice", id)
	if err != nil {
		t.Fatal(err)
	}
	check(got, "alice", 1, false)
	joined, err := manager.Join("bob", id, 1)
	if err != nil {
		t.Fatal(err)
	}
	check(joined, "alice", 2, false)
	ready, err := manager.Ready("alice", id, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	check(ready, "alice", 2, true)
	left, err := manager.Leave("alice", id, 3)
	if err != nil || left == nil {
		t.Fatalf("leave: %v, %v", left, err)
	}
	check(*left, "bob", 1, false)

	page, err := manager.List("demo.v1", "", 50)
	if err != nil || len(page.Lobbies) != 1 {
		t.Fatalf("list: %+v, %v", page, err)
	}
	page.Lobbies[0].ID = "intruder"
	next, err := manager.List("demo.v1", "", 50)
	if err != nil || len(next.Lobbies) != 1 || next.Lobbies[0].ID != id {
		t.Fatalf("page changed manager: %+v, %v", next, err)
	}
	deleted, err := manager.Leave("bob", id, left.Revision)
	if err != nil || deleted != nil {
		t.Fatalf("last leave: %v, %v", deleted, err)
	}
}

func TestReturnedTicketCannotMutateManager(t *testing.T) {
	manager := newTestManager(t)
	alice, err := manager.Enqueue(t.Context(), "alice", 2, "demo.v1")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := manager.Enqueue(t.Context(), "bob", 2, "demo.v1")
	if err != nil || bob.Assignment == nil {
		t.Fatalf("formed ticket: %+v, %v", bob, err)
	}
	original := *bob.Assignment
	bob.Assignment.ConnectTicket = "intruder"
	bob.Assignment.ParticipantID = "alice"
	current, err := manager.Ticket(t.Context(), "bob", bob.ID)
	if err != nil || current.Assignment == nil || *current.Assignment != original {
		t.Fatalf("enqueue returned shared assignment: %+v, %v", current, err)
	}
	current.Assignment.ConnectTicket = "intruder"
	again, err := manager.Ticket(t.Context(), "bob", bob.ID)
	if err != nil || again.Assignment == nil || *again.Assignment != original {
		t.Fatalf("ticket returned shared assignment: %+v, %v", again, err)
	}
	other, err := manager.Ticket(t.Context(), "alice", alice.ID)
	if err != nil || other.Assignment == nil || other.Assignment.ParticipantID != "alice" || other.Assignment.ConnectTicket == original.ConnectTicket {
		t.Fatalf("private assignment isolation: %+v, %v", other, err)
	}
}

func TestRestoreCopiesAllocationAndParticipants(t *testing.T) {
	manager := newTestManager(t)
	participants := []string{"alice", "bob"}
	allocation := Allocation{RoomID: "room", RoomInstanceID: "instance", ApplicationProtocol: "demo.v1", ExpiresAt: time.Now().Add(time.Hour), Tickets: map[string]string{"alice": "private-alice", "bob": "private-bob"}}
	if err := manager.Restore(allocation, participants); err != nil {
		t.Fatal(err)
	}
	participants[0] = "intruder"
	allocation.Tickets["alice"] = "intruder"
	group, err := manager.Get("alice", "room")
	if err != nil || group.Participants[0] != "alice" {
		t.Fatalf("restore shared roster: %+v, %v", group, err)
	}
	assignment, err := manager.Assignment(t.Context(), "alice", "room", false)
	if err != nil || assignment.ConnectTicket != "private-alice" {
		t.Fatalf("restore shared tickets: %+v, %v", assignment, err)
	}
}
