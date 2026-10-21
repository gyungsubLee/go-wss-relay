package protocol

import (
	"bytes"
	"testing"

	relayv1 "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v1"
	"google.golang.org/protobuf/proto"
)

func TestGoldenEnvelopeCompatibility(t *testing.T) {
	golden := loadAuthGolden(t)
	payload := goldenHex(t, golden.PayloadHex)

	tests := []struct {
		name string
		wire string
		want *relayv1.Envelope
	}{
		{
			name: "ClientData",
			wire: golden.ClientDataEnvelopeHex,
			want: &relayv1.Envelope{
				ProtocolRevision: golden.Revision,
				Sequence:         golden.Sequence,
				AuthTag:          goldenHex(t, golden.ClientDataTagHex),
				SessionId:        golden.SessionID,
				RoomId:           golden.RoomID,
				Body: &relayv1.Envelope_ClientData{ClientData: &relayv1.ClientData{
					BindingId: goldenHex(t, golden.BindingIDHex),
					Payload:   payload,
				}},
			},
		},
		{
			name: "ServerData",
			wire: golden.ServerDataEnvelopeHex,
			want: &relayv1.Envelope{
				ProtocolRevision: golden.Revision,
				Sequence:         golden.Sequence,
				SessionId:        golden.SessionID,
				RoomId:           golden.RoomID,
				Body: &relayv1.Envelope_ServerData{ServerData: &relayv1.ServerData{
					SenderParticipantId: "player-a",
					Payload:             payload,
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := goldenHex(t, test.wire)
			got := new(relayv1.Envelope)
			if err := proto.Unmarshal(wire, got); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(got, test.want) {
				t.Fatalf("decoded envelope = %v, want %v", got, test.want)
			}

			encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(test.want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, wire) {
				t.Fatalf("encoded envelope = %x, want %x", encoded, wire)
			}
		})
	}
}
