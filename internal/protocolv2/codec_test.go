package protocolv2

import (
	"testing"

	relayv2 "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"google.golang.org/protobuf/proto"
)

func TestDecodeClientBoundaries(t *testing.T) {
	cases := []struct {
		name          string
		envelope      *relayv2.ClientEnvelope
		authenticated bool
		previous      uint32
		want          relayv2.ErrorCode
	}{
		{name: "connect", envelope: &relayv2.ClientEnvelope{ProtocolRevision: 2, Body: &relayv2.ClientEnvelope_Connect{Connect: &relayv2.Connect{ApplicationProtocol: "snake.v1", Credential: &relayv2.Connect_ConnectTicket{ConnectTicket: "ticket"}}}}},
		{name: "wrong version", envelope: &relayv2.ClientEnvelope{ProtocolRevision: 1}, want: relayv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH},
		{name: "missing body", envelope: &relayv2.ClientEnvelope{ProtocolRevision: 2}, want: relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE},
		{name: "state before auth", envelope: stateEnvelope(1, []byte("x")), want: relayv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED},
		{name: "state boundary", envelope: stateEnvelope(2, make([]byte, MaxStatePayloadBytes)), authenticated: true, previous: 1},
		{name: "state over boundary", envelope: stateEnvelope(2, make([]byte, MaxStatePayloadBytes+1)), authenticated: true, previous: 1, want: relayv2.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE},
		{name: "empty state", envelope: stateEnvelope(2, nil), authenticated: true, previous: 1, want: relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE},
		{name: "duplicate", envelope: stateEnvelope(2, []byte("x")), authenticated: true, previous: 2, want: relayv2.ErrorCode_ERROR_CODE_STALE_SEQUENCE},
		{name: "sequence gap", envelope: stateEnvelope(8, []byte("x")), authenticated: true, previous: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire, err := proto.Marshal(tc.envelope)
			if err != nil {
				t.Fatal(err)
			}
			_, code := DecodeClient(wire, tc.authenticated, tc.previous)
			if code != tc.want {
				t.Fatalf("code = %v, want %v", code, tc.want)
			}
		})
	}
	if _, code := DecodeClient(make([]byte, MaxClientMessageBytes+1), false, 0); code != relayv2.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE {
		t.Fatalf("oversized envelope: %v", code)
	}
}

func stateEnvelope(sequence uint32, payload []byte) *relayv2.ClientEnvelope {
	return &relayv2.ClientEnvelope{ProtocolRevision: 2, Sequence: sequence, Body: &relayv2.ClientEnvelope_StateUpdate{StateUpdate: &relayv2.StateUpdate{Payload: payload}}}
}
