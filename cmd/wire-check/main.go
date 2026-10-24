// Command wire-check exchanges deterministic v2 fixtures with the TypeScript check.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"

	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

type fixture struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Bytes string `json:"bytes"`
}

func main() {
	verify := flag.Bool("verify", false, "verify TypeScript-encoded fixtures on stdin")
	flag.Parse()
	if err := run(*verify); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(verify bool) error {
	fixtures := map[string]proto.Message{}
	var output []fixture
	for _, kind := range []string{"ClientEnvelope", "ServerEnvelope"} {
		descriptor := wire.File_relay_v2_relay_proto.Messages().ByName(protoreflect.Name(kind))
		body := descriptor.Oneofs().ByName("body")
		for i := 0; i < body.Fields().Len(); i++ {
			field := body.Fields().Get(i)
			message := dynamicpb.NewMessage(descriptor)
			message.Set(descriptor.Fields().ByName("protocol_revision"), protoreflect.ValueOfUint32(2))
			message.Set(descriptor.Fields().ByName("sequence"), protoreflect.ValueOfUint32(17))
			value := message.NewField(field)
			fill(value.Message())
			message.Set(field, value)
			data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
			if err != nil {
				return err
			}
			name := kind + "." + string(field.Name())
			fixtures[name] = message
			output = append(output, fixture{Name: name, Kind: kind, Bytes: base64.StdEncoding.EncodeToString(data)})
		}
	}
	if !verify {
		return json.NewEncoder(os.Stdout).Encode(output)
	}
	var received []fixture
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&received); err != nil {
		return err
	}
	if len(received) != len(fixtures) {
		return fmt.Errorf("fixture count mismatch")
	}
	seen := map[string]bool{}
	for _, entry := range received {
		expected := fixtures[entry.Name]
		if expected == nil || seen[entry.Name] {
			return fmt.Errorf("unknown or duplicate fixture: %s", entry.Name)
		}
		seen[entry.Name] = true
		actual := expected.ProtoReflect().New().Interface()
		data, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			return err
		}
		if err := proto.Unmarshal(data, actual); err != nil {
			return err
		}
		if !proto.Equal(expected, actual) {
			return fmt.Errorf("Go/TS semantic mismatch: %s", entry.Name)
		}
	}
	fmt.Printf("PASS: %d Go→TS→Go body variants, including uint64 max\n", len(received))
	return nil
}
func fill(message protoreflect.Message) {
	fields := message.Descriptor().Fields()
	used := map[protoreflect.FullName]bool{}
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if oneof := field.ContainingOneof(); oneof != nil {
			if used[oneof.FullName()] {
				continue
			}
			used[oneof.FullName()] = true
		}
		var value protoreflect.Value
		switch field.Kind() {
		case protoreflect.Uint32Kind:
			value = protoreflect.ValueOfUint32(17)
		case protoreflect.Uint64Kind:
			value = protoreflect.ValueOfUint64(math.MaxUint64)
		case protoreflect.StringKind:
			value = protoreflect.ValueOfString("peer-1")
		case protoreflect.BytesKind:
			value = protoreflect.ValueOfBytes([]byte{0, 1, 127, 255})
		case protoreflect.BoolKind:
			value = protoreflect.ValueOfBool(true)
		case protoreflect.EnumKind:
			value = protoreflect.ValueOfEnum(1)
		case protoreflect.MessageKind:
			nested := dynamicpb.NewMessage(field.Message())
			fill(nested)
			value = protoreflect.ValueOfMessage(nested)
		default:
			panic("fixture generator needs a value for " + field.Kind().String())
		}
		if field.IsList() {
			message.Mutable(field).List().Append(value)
		} else {
			message.Set(field, value)
		}
	}
}
