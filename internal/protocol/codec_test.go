package protocol

import (
	"bytes"
	"math"
	"strings"
	"testing"

	relayv1 "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

var deterministicMarshal = proto.MarshalOptions{Deterministic: true}

func TestProtocolLimits(t *testing.T) {
	if Revision != 1 {
		t.Fatalf("Revision = %d, want 1", Revision)
	}
	if MaxDatagramBytes != 1200 {
		t.Fatalf("MaxDatagramBytes = %d, want 1200", MaxDatagramBytes)
	}
	if MaxPayloadBytes != 900 {
		t.Fatalf("MaxPayloadBytes = %d, want 900", MaxPayloadBytes)
	}
	if MaxIDBytes != 64 {
		t.Fatalf("MaxIDBytes = %d, want 64", MaxIDBytes)
	}
	if MinHelloBytes != 256 {
		t.Fatalf("MinHelloBytes = %d, want 256", MinHelloBytes)
	}
}

func TestDecodeClientAcceptsClientPackets(t *testing.T) {
	hello, helloWire := fittedHello(t, MinHelloBytes)
	if len(helloWire) != 256 {
		t.Fatalf("HELLO length = %d, want 256", len(helloWire))
	}

	tests := []struct {
		name     string
		envelope *relayv1.Envelope
	}{
		{name: "HELLO", envelope: hello},
		{name: "AUTH", envelope: validAuth()},
		{name: "ClientData", envelope: validClientData()},
		{name: "Ping", envelope: validPing()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := mustMarshal(t, tt.envelope)
			got, err := DecodeClient(wire)
			if err != nil {
				t.Fatalf("DecodeClient() error = %v", err)
			}
			if !proto.Equal(got, tt.envelope) {
				t.Fatalf("DecodeClient() = %v, want %v", got, tt.envelope)
			}
		})
	}
}

func TestDecodeClientRejectsInvalidPackets(t *testing.T) {
	type rejectCase struct {
		name string
		wire []byte
		want Reason
	}
	tests := []rejectCase{
		{name: "empty wire", wire: nil, want: ReasonMalformed},
		{name: "malformed wire", wire: []byte{0x80}, want: ReasonMalformed},
		{name: "1201 byte datagram", wire: make([]byte, 1201), want: ReasonOversized},
	}
	add := func(name string, envelope *relayv1.Envelope, want Reason) {
		tests = append(tests, rejectCase{name: name, wire: mustMarshal(t, envelope), want: want})
	}

	revisionTwo := cloneEnvelope(t, validAuth())
	revisionTwo.ProtocolRevision = 2
	add("unsupported revision", revisionTwo, ReasonUnsupportedVersion)
	add("absent body", &relayv1.Envelope{
		ProtocolRevision: Revision,
		SessionId:        "session-1",
		RoomId:           "room-1",
	}, ReasonMalformed)
	add("nil nested body", &relayv1.Envelope{
		ProtocolRevision: Revision,
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body:             &relayv1.Envelope_Auth{},
	}, ReasonMalformed)
	add("server-only CHALLENGE", validChallenge(), ReasonMalformed)
	add("server-only BOUND", validBound(), ReasonMalformed)
	add("server-only ServerData", validServerData(), ReasonMalformed)

	invalidRoom := cloneEnvelope(t, validAuth())
	invalidRoom.RoomId = "room/1"
	add("invalid room ID", invalidRoom, ReasonMalformed)
	invalidSession := cloneEnvelope(t, validAuth())
	invalidSession.SessionId = "-session"
	add("invalid session ID", invalidSession, ReasonMalformed)
	emptyRoom := cloneEnvelope(t, validAuth())
	emptyRoom.RoomId = ""
	add("empty room ID", emptyRoom, ReasonMalformed)
	longSession := cloneEnvelope(t, validAuth())
	longSession.SessionId = strings.Repeat("s", 65)
	add("65 byte session ID", longSession, ReasonMalformed)
	nonASCIIID := cloneEnvelope(t, validAuth())
	nonASCIIID.RoomId = "룸"
	add("non-ASCII room ID", nonASCIIID, ReasonMalformed)

	helloSequence := fittedHelloMutation(t, func(envelope *relayv1.Envelope) {
		envelope.Sequence = 1
	})
	add("HELLO sequence mismatch", helloSequence, ReasonMalformed)
	helloTag := fittedHelloMutation(t, func(envelope *relayv1.Envelope) {
		envelope.AuthTag = []byte{1}
	})
	add("HELLO auth tag mismatch", helloTag, ReasonMalformed)
	authSequence := cloneEnvelope(t, validAuth())
	authSequence.Sequence = 1
	add("AUTH sequence mismatch", authSequence, ReasonMalformed)
	clientDataSequence := cloneEnvelope(t, validClientData())
	clientDataSequence.Sequence = 0
	add("ClientData sequence mismatch", clientDataSequence, ReasonMalformed)
	pingSequence := cloneEnvelope(t, validPing())
	pingSequence.Sequence = 0
	add("Ping sequence mismatch", pingSequence, ReasonMalformed)

	for _, size := range []int{15, 17} {
		helloGrant := fittedHelloMutation(t, func(envelope *relayv1.Envelope) {
			envelope.GetHello().GrantId = repeatedByte(size, 0x11)
		})
		add("HELLO grant ID length "+sizeLabel(size), helloGrant, ReasonMalformed)
		helloNonce := fittedHelloMutation(t, func(envelope *relayv1.Envelope) {
			envelope.GetHello().ClientNonce = repeatedByte(size, 0x22)
		})
		add("HELLO client nonce length "+sizeLabel(size), helloNonce, ReasonMalformed)

		authCandidate := cloneEnvelope(t, validAuth())
		authCandidate.GetAuth().CandidateId = repeatedByte(size, 0x33)
		add("AUTH candidate ID length "+sizeLabel(size), authCandidate, ReasonMalformed)

		clientBinding := cloneEnvelope(t, validClientData())
		clientBinding.GetClientData().BindingId = repeatedByte(size, 0x44)
		add("ClientData binding ID length "+sizeLabel(size), clientBinding, ReasonMalformed)

		pingBinding := cloneEnvelope(t, validPing())
		pingBinding.GetPing().BindingId = repeatedByte(size, 0x55)
		add("Ping binding ID length "+sizeLabel(size), pingBinding, ReasonMalformed)
	}

	for _, size := range []int{31, 33} {
		authTag := cloneEnvelope(t, validAuth())
		authTag.AuthTag = repeatedByte(size, 0xa1)
		add("AUTH tag length "+sizeLabel(size), authTag, ReasonMalformed)

		clientTag := cloneEnvelope(t, validClientData())
		clientTag.AuthTag = repeatedByte(size, 0xa2)
		add("ClientData tag length "+sizeLabel(size), clientTag, ReasonMalformed)

		pingTag := cloneEnvelope(t, validPing())
		pingTag.AuthTag = repeatedByte(size, 0xa3)
		add("Ping tag length "+sizeLabel(size), pingTag, ReasonMalformed)
	}

	oversizedPayload := cloneEnvelope(t, validClientData())
	oversizedPayload.GetClientData().Payload = make([]byte, 901)
	add("901 byte payload", oversizedPayload, ReasonOversized)
	shortHello, shortHelloWire := fittedHello(t, 255)
	if shortHello == nil || len(shortHelloWire) != 255 {
		t.Fatal("failed to construct 255-byte HELLO")
	}
	tests = append(tests, rejectCase{name: "255 byte HELLO", wire: shortHelloWire, want: ReasonMalformed})

	envelopeUnknown := mustMarshal(t, validAuth())
	envelopeUnknown = append(envelopeUnknown, unknownField()...)
	tests = append(tests, rejectCase{name: "envelope unknown field", wire: envelopeUnknown, want: ReasonMalformed})
	nestedUnknown := cloneEnvelope(t, validAuth())
	nestedUnknown.GetAuth().ProtoReflect().SetUnknown(unknownField())
	add("nested body unknown field", nestedUnknown, ReasonMalformed)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("DecodeClient() panicked: %v", recovered)
				}
			}()
			got, err := DecodeClient(tt.wire)
			if err == nil {
				t.Fatalf("DecodeClient() = %v, want error", got)
			}
			if got := ReasonOf(err); got != tt.want {
				t.Fatalf("ReasonOf(%v) = %q, want %q", err, got, tt.want)
			}
		})
	}
}

func TestDecodeClientUsesLastWireValues(t *testing.T) {
	first := &relayv1.Envelope{
		ProtocolRevision: 2,
		Sequence:         99,
		AuthTag:          []byte{1},
		SessionId:        "invalid/session",
		RoomId:           "-invalid-room",
		Body: &relayv1.Envelope_Challenge{Challenge: &relayv1.Challenge{
			CandidateId: repeatedByte(15, 0x01),
			ServerNonce: repeatedByte(31, 0x02),
		}},
	}
	final := validPing()
	wire := append(mustMarshal(t, first), mustMarshal(t, final)...)

	got, err := DecodeClient(wire)
	if err != nil {
		t.Fatalf("DecodeClient() error = %v", err)
	}
	if got.GetChallenge() != nil || !proto.Equal(got.GetPing(), final.GetPing()) {
		t.Fatalf("DecodeClient() final body = %T, want Ping %v", got.GetBody(), final.GetPing())
	}
	if got.ProtocolRevision != Revision || got.Sequence != final.Sequence ||
		got.RoomId != final.RoomId || got.SessionId != final.SessionId ||
		!bytes.Equal(got.AuthTag, final.AuthTag) {
		t.Fatalf("DecodeClient() retained an earlier singular field: %v", got)
	}
}

func TestEncodeServerAcceptsServerPacketsDeterministically(t *testing.T) {
	tests := []struct {
		name     string
		envelope *relayv1.Envelope
	}{
		{name: "CHALLENGE", envelope: validChallenge()},
		{name: "BOUND", envelope: validBound()},
		{name: "ServerData", envelope: validServerData()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := mustMarshal(t, tt.envelope)
			first, err := EncodeServer(tt.envelope)
			if err != nil {
				t.Fatalf("EncodeServer() error = %v", err)
			}
			second, err := EncodeServer(tt.envelope)
			if err != nil {
				t.Fatalf("EncodeServer() second error = %v", err)
			}
			if !bytes.Equal(first, want) || !bytes.Equal(second, want) {
				t.Fatalf("EncodeServer() output is not deterministic: first=%x second=%x want=%x", first, second, want)
			}
		})
	}
	if len(validServerData().AuthTag) != 0 {
		t.Fatal("valid ServerData auth_tag must be empty")
	}
}

func TestEncodeServerAcceptsPositiveExpiryIndependentOfHostWall(t *testing.T) {
	for _, envelope := range []*relayv1.Envelope{validChallenge(), validBound()} {
		switch body := envelope.Body.(type) {
		case *relayv1.Envelope_Challenge:
			body.Challenge.ExpiresUnixMs = 1
		case *relayv1.Envelope_Bound:
			body.Bound.ExpiresUnixMs = 1
		}
		if _, err := EncodeServer(envelope); err != nil {
			t.Errorf("EncodeServer(%T, positive host-past expiry) error = %v", envelope.Body, err)
		}
	}
}

func TestEncodeServerRejectsInvalidPackets(t *testing.T) {
	type rejectCase struct {
		name     string
		envelope *relayv1.Envelope
		want     Reason
	}
	tests := []rejectCase{
		{name: "nil envelope", envelope: nil, want: ReasonMalformed},
		{name: "client-only HELLO", envelope: mustFittedHello(t, MinHelloBytes), want: ReasonMalformed},
		{name: "client-only AUTH", envelope: validAuth(), want: ReasonMalformed},
		{name: "client-only ClientData", envelope: validClientData(), want: ReasonMalformed},
		{name: "client-only Ping", envelope: validPing(), want: ReasonMalformed},
		{name: "absent body", envelope: &relayv1.Envelope{
			ProtocolRevision: Revision,
			SessionId:        "session-1",
			RoomId:           "room-1",
		}, want: ReasonMalformed},
		{name: "nil nested body", envelope: &relayv1.Envelope{
			ProtocolRevision: Revision,
			SessionId:        "session-1",
			RoomId:           "room-1",
			Body:             &relayv1.Envelope_Challenge{},
		}, want: ReasonMalformed},
	}
	add := func(name string, envelope *relayv1.Envelope, want Reason) {
		tests = append(tests, rejectCase{name: name, envelope: envelope, want: want})
	}

	unsupported := cloneEnvelope(t, validBound())
	unsupported.ProtocolRevision = 2
	add("unsupported revision", unsupported, ReasonUnsupportedVersion)
	invalidRoom := cloneEnvelope(t, validBound())
	invalidRoom.RoomId = "room/1"
	add("invalid room ID", invalidRoom, ReasonMalformed)
	invalidSession := cloneEnvelope(t, validBound())
	invalidSession.SessionId = ""
	add("invalid session ID", invalidSession, ReasonMalformed)

	challengeSequence := cloneEnvelope(t, validChallenge())
	challengeSequence.Sequence = 1
	add("CHALLENGE sequence mismatch", challengeSequence, ReasonMalformed)
	challengeTag := cloneEnvelope(t, validChallenge())
	challengeTag.AuthTag = []byte{1}
	add("CHALLENGE auth tag mismatch", challengeTag, ReasonMalformed)
	challengeZeroExpiry := cloneEnvelope(t, validChallenge())
	challengeZeroExpiry.GetChallenge().ExpiresUnixMs = 0
	add("CHALLENGE zero expiry", challengeZeroExpiry, ReasonMalformed)
	challengeNegativeExpiry := cloneEnvelope(t, validChallenge())
	challengeNegativeExpiry.GetChallenge().ExpiresUnixMs = -1
	add("CHALLENGE negative expiry", challengeNegativeExpiry, ReasonMalformed)
	for _, size := range []int{15, 17} {
		candidate := cloneEnvelope(t, validChallenge())
		candidate.GetChallenge().CandidateId = repeatedByte(size, 0x61)
		add("CHALLENGE candidate ID length "+sizeLabel(size), candidate, ReasonMalformed)
		binding := cloneEnvelope(t, validBound())
		binding.GetBound().BindingId = repeatedByte(size, 0x62)
		add("BOUND binding ID length "+sizeLabel(size), binding, ReasonMalformed)
	}
	for _, size := range []int{31, 33} {
		nonce := cloneEnvelope(t, validChallenge())
		nonce.GetChallenge().ServerNonce = repeatedByte(size, 0x63)
		add("CHALLENGE server nonce length "+sizeLabel(size), nonce, ReasonMalformed)
		tag := cloneEnvelope(t, validBound())
		tag.AuthTag = repeatedByte(size, 0x64)
		add("BOUND auth tag length "+sizeLabel(size), tag, ReasonMalformed)
	}

	boundSequence := cloneEnvelope(t, validBound())
	boundSequence.Sequence = 1
	add("BOUND sequence mismatch", boundSequence, ReasonMalformed)
	boundZeroExpiry := cloneEnvelope(t, validBound())
	boundZeroExpiry.GetBound().ExpiresUnixMs = 0
	add("BOUND zero expiry", boundZeroExpiry, ReasonMalformed)
	boundNegativeExpiry := cloneEnvelope(t, validBound())
	boundNegativeExpiry.GetBound().ExpiresUnixMs = -1
	add("BOUND negative expiry", boundNegativeExpiry, ReasonMalformed)
	serverDataSequence := cloneEnvelope(t, validServerData())
	serverDataSequence.Sequence = 0
	add("ServerData sequence mismatch", serverDataSequence, ReasonMalformed)
	serverDataTag := cloneEnvelope(t, validServerData())
	serverDataTag.AuthTag = repeatedByte(32, 0x65)
	add("ServerData auth tag must be empty", serverDataTag, ReasonMalformed)
	invalidSender := cloneEnvelope(t, validServerData())
	invalidSender.GetServerData().SenderParticipantId = "sender/1"
	add("invalid sender participant ID", invalidSender, ReasonMalformed)
	oversizedPayload := cloneEnvelope(t, validServerData())
	oversizedPayload.GetServerData().Payload = make([]byte, 901)
	add("901 byte payload", oversizedPayload, ReasonOversized)

	envelopeUnknown := cloneEnvelope(t, validBound())
	envelopeUnknown.ProtoReflect().SetUnknown(unknownField())
	add("envelope unknown field", envelopeUnknown, ReasonMalformed)
	nestedUnknown := cloneEnvelope(t, validBound())
	nestedUnknown.GetBound().ProtoReflect().SetUnknown(unknownField())
	add("nested body unknown field", nestedUnknown, ReasonMalformed)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("EncodeServer() panicked: %v", recovered)
				}
			}()
			wire, err := EncodeServer(tt.envelope)
			if err == nil {
				t.Fatalf("EncodeServer() = %x, want error", wire)
			}
			if got := ReasonOf(err); got != tt.want {
				t.Fatalf("ReasonOf(%v) = %q, want %q", err, got, tt.want)
			}
		})
	}
}

func TestEncodeServerRejectsOversizedPayloadEnvelopeAboveDatagramCap(t *testing.T) {
	envelope := validServerData()
	envelope.RoomId = strings.Repeat("r", MaxIDBytes)
	envelope.SessionId = strings.Repeat("s", MaxIDBytes)
	envelope.GetServerData().SenderParticipantId = strings.Repeat("p", MaxIDBytes)
	envelope.GetServerData().Payload = make([]byte, 1100)

	wire := mustMarshal(t, envelope)
	if len(wire) <= MaxDatagramBytes {
		t.Fatalf("fixture marshaled to %d bytes, want more than %d", len(wire), MaxDatagramBytes)
	}
	t.Logf("oversized ServerData fixture encoded length: %d bytes", len(wire))

	// V1 rejects this at the 900-byte payload check before the defensive
	// post-marshal cap; this public regression does not claim that branch is reachable.
	if output, err := EncodeServer(envelope); err == nil {
		t.Fatalf("EncodeServer() = %x, want oversized error", output)
	} else if got := ReasonOf(err); got != ReasonOversized {
		t.Fatalf("ReasonOf(%v) = %q, want %q", err, got, ReasonOversized)
	}
}

func TestWorstCaseEnvelopeSizes(t *testing.T) {
	client := validClientData()
	client.RoomId = strings.Repeat("r", MaxIDBytes)
	client.SessionId = strings.Repeat("s", MaxIDBytes)
	client.Sequence = math.MaxUint64
	client.GetClientData().Payload = make([]byte, MaxPayloadBytes)
	clientWire := mustMarshal(t, client)
	t.Logf("worst-case ClientData encoded length: %d bytes", len(clientWire))
	if len(clientWire) > MaxDatagramBytes {
		t.Fatalf("worst-case ClientData length = %d, max %d", len(clientWire), MaxDatagramBytes)
	}
	if _, err := DecodeClient(clientWire); err != nil {
		t.Fatalf("DecodeClient(worst-case ClientData) error = %v", err)
	}

	server := validServerData()
	server.RoomId = strings.Repeat("r", MaxIDBytes)
	server.SessionId = strings.Repeat("s", MaxIDBytes)
	server.Sequence = math.MaxUint64
	server.GetServerData().SenderParticipantId = strings.Repeat("p", MaxIDBytes)
	server.GetServerData().Payload = make([]byte, MaxPayloadBytes)
	serverWire, err := EncodeServer(server)
	if err != nil {
		t.Fatalf("EncodeServer(worst-case ServerData) error = %v", err)
	}
	t.Logf("worst-case ServerData encoded length: %d bytes", len(serverWire))
	if len(serverWire) > MaxDatagramBytes {
		t.Fatalf("worst-case ServerData length = %d, max %d", len(serverWire), MaxDatagramBytes)
	}
}

func validAuth() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		AuthTag:          repeatedByte(32, 0xa1),
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_Auth{Auth: &relayv1.Auth{
			CandidateId: repeatedByte(16, 0x31),
		}},
	}
}

func validClientData() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		Sequence:         1,
		AuthTag:          repeatedByte(32, 0xa2),
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_ClientData{ClientData: &relayv1.ClientData{
			BindingId: repeatedByte(16, 0x41),
			Payload:   []byte("payload"),
		}},
	}
}

func validPing() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		Sequence:         1,
		AuthTag:          repeatedByte(32, 0xa3),
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_Ping{Ping: &relayv1.Ping{
			BindingId: repeatedByte(16, 0x51),
		}},
	}
}

func validChallenge() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_Challenge{Challenge: &relayv1.Challenge{
			CandidateId:   repeatedByte(16, 0x61),
			ServerNonce:   repeatedByte(32, 0x62),
			ExpiresUnixMs: math.MaxInt64,
		}},
	}
}

func validBound() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		AuthTag:          repeatedByte(32, 0xa4),
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_Bound{Bound: &relayv1.Bound{
			BindingId:     repeatedByte(16, 0x71),
			ExpiresUnixMs: math.MaxInt64,
		}},
	}
}

func validServerData() *relayv1.Envelope {
	return &relayv1.Envelope{
		ProtocolRevision: Revision,
		Sequence:         1,
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_ServerData{ServerData: &relayv1.ServerData{
			SenderParticipantId: "participant-1",
			Payload:             []byte("payload"),
		}},
	}
}

func fittedHelloMutation(t testing.TB, mutate func(*relayv1.Envelope)) *relayv1.Envelope {
	t.Helper()
	envelope := mustFittedHello(t, MinHelloBytes)
	mutate(envelope)
	fitHelloPadding(t, envelope, MinHelloBytes)
	return envelope
}

func fittedHello(t testing.TB, target int) (*relayv1.Envelope, []byte) {
	t.Helper()
	envelope := &relayv1.Envelope{
		ProtocolRevision: Revision,
		SessionId:        "session-1",
		RoomId:           "room-1",
		Body: &relayv1.Envelope_Hello{Hello: &relayv1.Hello{
			GrantId:     repeatedByte(16, 0x11),
			ClientNonce: repeatedByte(16, 0x21),
		}},
	}
	return envelope, fitHelloPadding(t, envelope, target)
}

func mustFittedHello(t testing.TB, target int) *relayv1.Envelope {
	t.Helper()
	envelope, _ := fittedHello(t, target)
	return envelope
}

func fitHelloPadding(t testing.TB, envelope *relayv1.Envelope, target int) []byte {
	t.Helper()
	hello := envelope.GetHello()
	if hello == nil {
		t.Fatal("HELLO body is nil")
	}
	for size := 0; size <= target; size++ {
		hello.Padding = make([]byte, size)
		wire := mustMarshal(t, envelope)
		if len(wire) == target {
			return wire
		}
	}
	t.Fatalf("could not fit HELLO to %d bytes", target)
	return nil
}

func mustMarshal(t testing.TB, message proto.Message) []byte {
	t.Helper()
	wire, err := deterministicMarshal.Marshal(message)
	if err != nil {
		t.Fatalf("deterministic marshal: %v", err)
	}
	return wire
}

func cloneEnvelope(t testing.TB, envelope *relayv1.Envelope) *relayv1.Envelope {
	t.Helper()
	clone, ok := proto.Clone(envelope).(*relayv1.Envelope)
	if !ok {
		t.Fatal("proto.Clone returned the wrong envelope type")
	}
	return clone
}

func repeatedByte(size int, value byte) []byte {
	return bytes.Repeat([]byte{value}, size)
}

func sizeLabel(size int) string {
	if size == 15 || size == 31 {
		return "N-1"
	}
	return "N+1"
}

func unknownField() []byte {
	wire := protowire.AppendTag(nil, 99, protowire.VarintType)
	return protowire.AppendVarint(wire, 1)
}
