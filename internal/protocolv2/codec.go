// Package protocolv2 validates the browser-compatible relay wire contract.
package protocolv2

import (
	"unicode/utf8"

	relayv2 "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"google.golang.org/protobuf/proto"
)

const (
	Revision              = 2
	MaxClientMessageBytes = 16 << 10
	MaxServerMessageBytes = 128 << 10
	MaxStatePayloadBytes  = 4 << 10
	MaxEventPayloadBytes  = 8 << 10
)

func DecodeClient(data []byte, authenticated bool, previous uint32) (*relayv2.ClientEnvelope, relayv2.ErrorCode) {
	if len(data) > MaxClientMessageBytes {
		return nil, relayv2.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE
	}
	var envelope relayv2.ClientEnvelope
	if len(data) == 0 || (proto.UnmarshalOptions{RecursionLimit: 16}).Unmarshal(data, &envelope) != nil {
		return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
	}
	if envelope.ProtocolRevision != Revision {
		return nil, relayv2.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH
	}
	if envelope.Body == nil {
		return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
	}
	if !authenticated {
		connect := envelope.GetConnect()
		if connect == nil {
			return nil, relayv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED
		}
		credential := connect.GetConnectTicket()
		if credential == "" {
			credential = connect.GetResumeToken()
		}
		if envelope.Sequence != 0 || !protocol.ValidID(connect.ApplicationProtocol) || credential == "" || len(credential) > 2048 || !utf8.ValidString(credential) {
			return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
		}
		return &envelope, 0
	}
	if envelope.GetConnect() != nil {
		return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
	}
	if envelope.Sequence <= previous {
		return nil, relayv2.ErrorCode_ERROR_CODE_STALE_SEQUENCE
	}
	switch body := envelope.Body.(type) {
	case *relayv2.ClientEnvelope_StateUpdate:
		if len(body.StateUpdate.Payload) == 0 {
			return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
		}
		if len(body.StateUpdate.Payload) > MaxStatePayloadBytes {
			return nil, relayv2.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE
		}
	case *relayv2.ClientEnvelope_Event:
		if body.Event.Kind == 0 || body.Event.Kind > 65535 {
			return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
		}
		if len(body.Event.Payload) > MaxEventPayloadBytes {
			return nil, relayv2.ErrorCode_ERROR_CODE_PAYLOAD_TOO_LARGE
		}
	case *relayv2.ClientEnvelope_Ping, *relayv2.ClientEnvelope_Resync, *relayv2.ClientEnvelope_Leave:
	default:
		return nil, relayv2.ErrorCode_ERROR_CODE_INVALID_MESSAGE
	}
	return &envelope, 0
}
