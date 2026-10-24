package relay

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
)

func TestPacedStateUpdatesSurviveReaderPause(t *testing.T) {
	server, err := New(Config{NodeID: "reader-pause"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	allocation, err := server.CreateRoom("paused-room", "demo.v1", []string{"sender", "receiver"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	sender, _ := connect(t, httpServer.URL, &wire.Connect{
		ApplicationProtocol: "demo.v1",
		Credential:          &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["sender"]},
	})
	defer sender.CloseNow()
	receiver, _ := connect(t, httpServer.URL, &wire.Connect{
		ApplicationProtocol: "demo.v1",
		Credential:          &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets["receiver"]},
	})
	defer receiver.CloseNow()

	server.mu.Lock()
	r := server.rooms[allocation.RoomInstanceID]
	server.mu.Unlock()
	r.mu.Lock()
	unlock := sync.OnceFunc(r.mu.Unlock)
	defer unlock()

	// A 250ms reader pause must tolerate five queued updates from a 20Hz publisher.
	before := server.metrics.receivedMessages.Load()
	send(t, sender, &wire.ClientEnvelope{
		Sequence: 1,
		Body: &wire.ClientEnvelope_StateUpdate{
			StateUpdate: &wire.StateUpdate{Payload: []byte("state-1")},
		},
	})
	deadline := time.Now().Add(time.Second)
	for server.metrics.receivedMessages.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("server did not read the first state before the pause")
		}
		time.Sleep(time.Millisecond)
	}
	for sequence := uint32(2); sequence <= 6; sequence++ {
		time.Sleep(50 * time.Millisecond)
		send(t, sender, &wire.ClientEnvelope{
			Sequence: sequence,
			Body: &wire.ClientEnvelope_StateUpdate{
				StateUpdate: &wire.StateUpdate{Payload: []byte("latest-state")},
			},
		})
	}
	unlock()

	// The Pong follows all preceding state decisions on the same ordered socket.
	send(t, sender, &wire.ClientEnvelope{
		Sequence: 7,
		Body:     &wire.ClientEnvelope_Ping{Ping: &wire.Ping{Nonce: 1}},
	})
	receiveUntil(t, sender, func(message *wire.ServerEnvelope) bool {
		if problem := message.GetError(); problem != nil {
			t.Fatalf("20Hz publisher was rejected after a reader pause: %v", problem)
		}
		return message.GetPong() != nil
	})
	receiveUntil(t, receiver, func(message *wire.ServerEnvelope) bool {
		for _, state := range message.GetStateBatch().GetStates() {
			if state.ParticipantId == "sender" && state.ClientSequence == 6 {
				return true
			}
		}
		return false
	})
	if rejected := server.metrics.rateRejected.Load(); rejected != 0 {
		t.Fatalf("20Hz publisher caused %d rate rejections", rejected)
	}
}
