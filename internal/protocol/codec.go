package protocol

import (
	"errors"

	relayv1 "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v1"
	"google.golang.org/protobuf/proto"
)

const (
	Revision         uint32 = 1
	MaxDatagramBytes        = 1200
	MaxPayloadBytes         = 900
	MinHelloBytes           = 256
)

type Reason string

const (
	ReasonMalformed          Reason = "malformed"
	ReasonOversized          Reason = "oversized"
	ReasonUnsupportedVersion Reason = "unsupported_version"
)

var (
	errMalformed          = errors.New(string(ReasonMalformed))
	errOversized          = errors.New(string(ReasonOversized))
	errUnsupportedVersion = errors.New(string(ReasonUnsupportedVersion))
)

func ReasonOf(err error) Reason {
	switch {
	case errors.Is(err, errOversized):
		return ReasonOversized
	case errors.Is(err, errUnsupportedVersion):
		return ReasonUnsupportedVersion
	default:
		return ReasonMalformed
	}
}

func DecodeClient(datagram []byte) (*relayv1.Envelope, error) {
	if len(datagram) == 0 {
		return nil, errMalformed
	}
	if len(datagram) > MaxDatagramBytes {
		return nil, errOversized
	}

	envelope := new(relayv1.Envelope)
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(datagram, envelope); err != nil {
		return nil, errMalformed
	}
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := validateClient(envelope, len(datagram)); err != nil {
		return nil, err
	}
	return envelope, nil
}

func EncodeServer(envelope *relayv1.Envelope) ([]byte, error) {
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	if err := validateServer(envelope); err != nil {
		return nil, err
	}

	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(envelope)
	if err != nil {
		return nil, errMalformed
	}
	if len(wire) > MaxDatagramBytes {
		return nil, errOversized
	}
	return wire, nil
}

func validateEnvelope(envelope *relayv1.Envelope) error {
	if envelope == nil || len(envelope.ProtoReflect().GetUnknown()) != 0 {
		return errMalformed
	}
	if envelope.ProtocolRevision != Revision {
		return errUnsupportedVersion
	}
	if !ValidID(envelope.RoomId) || !ValidID(envelope.SessionId) {
		return errMalformed
	}
	return nil
}

func validateClient(envelope *relayv1.Envelope, datagramBytes int) error {
	switch body := envelope.Body.(type) {
	case *relayv1.Envelope_Hello:
		if body == nil || body.Hello == nil || len(body.Hello.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence != 0 || len(envelope.AuthTag) != 0 ||
			len(body.Hello.GrantId) != 16 || len(body.Hello.ClientNonce) != 16 ||
			datagramBytes < MinHelloBytes {
			return errMalformed
		}
	case *relayv1.Envelope_Auth:
		if body == nil || body.Auth == nil || len(body.Auth.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence != 0 || len(envelope.AuthTag) != 32 ||
			len(body.Auth.CandidateId) != 16 {
			return errMalformed
		}
	case *relayv1.Envelope_ClientData:
		if body == nil || body.ClientData == nil || len(body.ClientData.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence == 0 || len(envelope.AuthTag) != 32 ||
			len(body.ClientData.BindingId) != 16 {
			return errMalformed
		}
		if len(body.ClientData.Payload) > MaxPayloadBytes {
			return errOversized
		}
	case *relayv1.Envelope_Ping:
		if body == nil || body.Ping == nil || len(body.Ping.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence == 0 || len(envelope.AuthTag) != 32 ||
			len(body.Ping.BindingId) != 16 {
			return errMalformed
		}
	default:
		return errMalformed
	}
	return nil
}

func validateServer(envelope *relayv1.Envelope) error {
	switch body := envelope.Body.(type) {
	case *relayv1.Envelope_Challenge:
		if body == nil || body.Challenge == nil || len(body.Challenge.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence != 0 || len(envelope.AuthTag) != 0 ||
			len(body.Challenge.CandidateId) != 16 || len(body.Challenge.ServerNonce) != 32 ||
			body.Challenge.ExpiresUnixMs <= 0 {
			return errMalformed
		}
	case *relayv1.Envelope_Bound:
		if body == nil || body.Bound == nil || len(body.Bound.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence != 0 || len(envelope.AuthTag) != 32 ||
			len(body.Bound.BindingId) != 16 || body.Bound.ExpiresUnixMs <= 0 {
			return errMalformed
		}
	case *relayv1.Envelope_ServerData:
		if body == nil || body.ServerData == nil || len(body.ServerData.ProtoReflect().GetUnknown()) != 0 ||
			envelope.Sequence == 0 || len(envelope.AuthTag) != 0 ||
			!ValidID(body.ServerData.SenderParticipantId) {
			return errMalformed
		}
		if len(body.ServerData.Payload) > MaxPayloadBytes {
			return errOversized
		}
	default:
		return errMalformed
	}
	return nil
}
