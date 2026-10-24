package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
	"google.golang.org/protobuf/proto"
)

type loadShutdownReport struct {
	LeftAcknowledgements   uint64  `json:"leftAcknowledgements"`
	NormalClosures         uint64  `json:"normalClosures"`
	ShutdownFailures       uint64  `json:"shutdownFailures"`
	ShutdownForcedClosures uint64  `json:"shutdownForcedClosures"`
	ShutdownDurationMs     float64 `json:"shutdownDurationMs"`
}

func TestRunWaitsForLeaveAndNormalClosure(t *testing.T) {
	node, err := relay.New(relay.Config{NodeID: "load-shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	socketServer := httptest.NewTLSServer(node)
	t.Cleanup(socketServer.Close)
	management := httptest.NewServer(node.ManagementHandler("test-token", "wss"+strings.TrimPrefix(socketServer.URL, "https")+"/v2/relay"))
	t.Cleanup(management.Close)
	c := loadTestConfig(t, socketServer, management.URL)
	before := node.Stats().WriteErrors
	if err := run(c); err != nil {
		t.Fatal(err)
	}
	result := readShutdownReport(t, c.output)
	if result.LeftAcknowledgements != 2 || result.NormalClosures != 2 {
		t.Fatalf("run returned without completing both relay leave handshakes: %+v", result)
	}
	if result.ShutdownFailures != 0 || result.ShutdownForcedClosures != 0 || result.ShutdownDurationMs <= 0 {
		t.Fatalf("healthy shutdown failed: %+v", result)
	}
	deadline := time.Now().Add(time.Second)
	for node.Stats().Connections != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	after := node.Stats()
	if after.WriteErrors != before {
		t.Fatalf("healthy shutdown produced socket write errors: before=%d after=%d", before, after.WriteErrors)
	}
	if after.Rooms != 0 || after.Connections != 0 || after.ReservedParticipants != 0 {
		t.Fatalf("load resources remain after shutdown: %+v", after)
	}
}

func TestRunRejectsIncompleteShutdown(t *testing.T) {
	for _, condition := range []string{"missing-left", "wrong-left-sequence", "abnormal-close", "timeout"} {
		t.Run(condition, func(t *testing.T) {
			node, err := relay.New(relay.Config{NodeID: "load-shutdown-failure"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = node.Close() })
			var leaveRequests atomic.Int64
			problems := make(chan error, 8)
			socketServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{"relay.v2"}})
				if err != nil {
					problems <- err
					return
				}
				defer conn.CloseNow()
				var previous uint32
				for {
					_, data, err := conn.Read(t.Context())
					if err != nil {
						return
					}
					var message wire.ClientEnvelope
					if err := proto.Unmarshal(data, &message); err != nil {
						problems <- err
						return
					}
					if message.GetConnect() == nil && message.Sequence <= previous {
						problems <- fmt.Errorf("client sequence did not advance: previous=%d message=%v", previous, &message)
						return
					}
					previous = message.Sequence
					var response *wire.ServerEnvelope
					switch {
					case message.GetConnect() != nil:
						response = &wire.ServerEnvelope{Body: &wire.ServerEnvelope_Welcome{Welcome: &wire.Welcome{}}}
					case message.GetStateUpdate() != nil:
						response = &wire.ServerEnvelope{Body: &wire.ServerEnvelope_StateBatch{StateBatch: &wire.StateBatch{States: []*wire.PublishedState{{Payload: message.GetStateUpdate().Payload}}}}}
					case message.GetLeave() != nil:
						leaveRequests.Add(1)
						if condition == "missing-left" {
							_ = conn.Close(websocket.StatusNormalClosure, "left missing")
							return
						}
						if condition == "timeout" {
							continue
						}
						acknowledged := message.Sequence
						if condition == "wrong-left-sequence" {
							acknowledged++
						}
						response = &wire.ServerEnvelope{Body: &wire.ServerEnvelope_Left{Left: &wire.Left{RequestSequence: acknowledged}}}
					default:
						problems <- fmt.Errorf("unexpected client message: %v", &message)
						return
					}
					data, err = proto.Marshal(response)
					if err != nil {
						problems <- err
						return
					}
					writeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
					err = conn.Write(writeCtx, websocket.MessageBinary, data)
					cancel()
					if err != nil {
						return
					}
					if message.GetLeave() != nil {
						status := websocket.StatusPolicyViolation
						if condition == "wrong-left-sequence" {
							status = websocket.StatusNormalClosure
						}
						_ = conn.Close(status, "unexpected shutdown")
						return
					}
				}
			}))
			t.Cleanup(socketServer.Close)
			management := httptest.NewServer(node.ManagementHandler("test-token", "wss"+strings.TrimPrefix(socketServer.URL, "https")+"/v2/relay"))
			t.Cleanup(management.Close)
			c := loadTestConfig(t, socketServer, management.URL)
			started := time.Now()
			if err := run(c); err == nil {
				t.Fatal("run accepted an incomplete relay shutdown")
			}
			if time.Since(started) > 12*time.Second {
				t.Fatal("load shutdown exceeded its bounded wait")
			}
			if leaveRequests.Load() != 2 {
				t.Fatalf("stopping aborted sockets before sending Leave: requests=%d", leaveRequests.Load())
			}
			select {
			case err := <-problems:
				t.Fatal(err)
			default:
			}
			result := readShutdownReport(t, c.output)
			if result.ShutdownFailures == 0 {
				t.Fatalf("shutdown failure was omitted from report: %+v", result)
			}
			if condition == "timeout" && result.ShutdownForcedClosures != 2 {
				t.Fatalf("unresponsive sockets did not require forced cleanup: %+v", result)
			}
		})
	}
}

func readShutdownReport(t *testing.T, path string) loadShutdownReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result loadShutdownReport
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func loadTestConfig(t *testing.T, socketServer *httptest.Server, management string) config {
	t.Helper()
	folder := t.TempDir()
	c := config{
		management:   management,
		tokenFile:    filepath.Join(folder, "operator.token"),
		caFile:       filepath.Join(folder, "ca.pem"),
		output:       filepath.Join(folder, "report.json"),
		participants: 2,
		roomSize:     2,
		hz:           20,
		payload:      8,
		seconds:      1,
	}
	if err := os.WriteFile(c.tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: socketServer.Certificate().Raw})
	if err := os.WriteFile(c.caFile, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	return c
}
