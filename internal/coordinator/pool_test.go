package coordinator

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	"github.com/gyungsubLee/go-wss-relay/internal/control"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
	"google.golang.org/protobuf/proto"
)

type testNode struct {
	id, token, tokenFile, address string
	relay                         *relay.Server
	http                          *httptest.Server
	dropNext                      atomic.Bool
	failHeartbeat                 atomic.Bool
	proxy503                      atomic.Bool
}

func newTestNode(t *testing.T, id string) *testNode {
	t.Helper()
	secret := [32]byte{1}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	// Fixed nonzero credentials are scoped to this isolated test server.
	path := filepath.Join(t.TempDir(), "operator.token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	n := &testNode{id: id, token: token, tokenFile: path}
	n.start(t)
	t.Cleanup(func() { n.http.Close(); n.relay.Close() })
	return n
}
func (n *testNode) start(t *testing.T) {
	t.Helper()
	var err error
	n.relay, err = relay.New(relay.Config{NodeID: n.id, MaxConnections: 16})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	n.http = httptest.NewUnstartedServer(mux)
	if n.address != "" {
		_ = n.http.Listener.Close()
		n.http.Listener, err = net.Listen("tcp", n.address)
		if err != nil {
			t.Fatal(err)
		}
	}
	n.http.Start()
	n.address = n.http.Listener.Addr().String()
	handler := n.relay.ManagementHandler(n.token, "ws"+strings.TrimPrefix(n.http.URL, "http")+"/v2/relay")
	mux.Handle("/v2/relay", n.relay)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/node" && n.failHeartbeat.Load() {
			w.WriteHeader(502)
			return
		}
		if r.URL.Path == "/v2/reservations" && n.proxy503.CompareAndSwap(true, false) {
			handler.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/v2/reservations" && n.dropNext.CompareAndSwap(true, false) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		handler.ServeHTTP(w, r)
	}))
}
func (n *testNode) config() NodeConfig {
	return NodeConfig{ID: n.id, ControlURL: n.http.URL, WebSocketURL: "ws" + strings.TrimPrefix(n.http.URL, "http") + "/v2/relay", TokenFile: n.tokenFile}
}
func newPool(t *testing.T, nodes ...*testNode) *Pool {
	t.Helper()
	configs := make([]NodeConfig, len(nodes))
	for i, n := range nodes {
		configs[i] = n.config()
	}
	pool, err := New(configs)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool
}
func allocate(t *testing.T, pool *Pool, id string) control.Allocation {
	t.Helper()
	result, err := pool.Allocate(context.Background(), id, "demo.v1", []string{id + "-alice", id + "-bob"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLostReservationResponseNeverMovesRoom(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pool := newPool(t, a, b)
	a.dropNext.Store(true)
	participants := []string{"alice", "bob"}
	if _, err := pool.Allocate(context.Background(), "ambiguous", "demo.v1", participants); err == nil {
		t.Fatal("lost response unexpectedly succeeded")
	}
	result, err := pool.Allocate(context.Background(), "ambiguous", "demo.v1", participants)
	if err != nil {
		t.Fatal(err)
	}
	if result.NodeID != "a" || len(a.relay.Inventory().Rooms) != 1 || len(b.relay.Inventory().Rooms) != 0 {
		t.Fatal("ambiguous reservation duplicated across nodes")
	}
	repeated, err := pool.Allocate(context.Background(), "ambiguous", "demo.v1", participants)
	if err != nil || repeated.RoomInstanceID != result.RoomInstanceID {
		t.Fatal("idempotent reservation changed identity")
	}
	if _, err := pool.Allocate(context.Background(), "ambiguous", "other.v1", participants); !errors.Is(err, relay.ErrConflict) {
		t.Fatalf("changed body accepted: %v", err)
	}
}

func TestPlacementDrainRecoveryAndNodeRestartFencing(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pool := newPool(t, a, b)
	first := allocate(t, pool, "first")
	if first.NodeID != "a" {
		t.Fatal("deterministic initial placement failed")
	}
	alice, welcome := connectNode(t, first, "first-alice", first.Tickets["first-alice"])
	defer alice.CloseNow()
	if err := pool.Drain(context.Background(), "a", true); err != nil {
		t.Fatal(err)
	}
	second := allocate(t, pool, "second")
	if second.NodeID != "b" {
		t.Fatal("draining node received new room")
	}
	ticket, err := pool.Refresh(context.Background(), first, "first-alice")
	if err != nil {
		t.Fatal(err)
	}
	resumed, next := connectNode(t, first, "first-alice", ticket.Value)
	defer resumed.CloseNow()
	if next.Generation != welcome.Generation+1 || !next.Resumed {
		t.Fatal("drain prevented resume")
	}
	restarted := newPool(t, a, b)
	if len(restarted.Recovered()) != 2 {
		t.Fatal("active room inventory not recovered")
	}
	// The relay socket remains usable after replacing the coordinator state.
	sendNode(t, resumed, &wire.ClientEnvelope{Sequence: 1, Body: &wire.ClientEnvelope_Ping{Ping: &wire.Ping{Nonce: 7}}})
	receiveNode(t, resumed, func(message *wire.ServerEnvelope) bool { return message.GetPong().GetNonce() == 7 })
	restored, err := restarted.Allocate(context.Background(), first.RoomID, "demo.v1", []string{"first-alice", "first-bob"})
	if err != nil || restored.RoomInstanceID != first.RoomInstanceID {
		t.Fatalf("recovery moved room: %v", err)
	}
	oldInstance := a.relay.InstanceID()
	a.http.Close()
	a.relay.Close()
	a.start(t)
	if a.relay.InstanceID() == oldInstance {
		t.Fatal("node restart reused incarnation")
	}
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Allocate(context.Background(), first.RoomID, "demo.v1", []string{"first-alice", "first-bob"}); !errors.Is(err, relay.ErrGone) {
		t.Fatalf("old room resurrected: %v", err)
	}
	fresh := allocate(t, restarted, "fresh")
	if fresh.NodeID != "a" || fresh.NodeInstanceID == oldInstance {
		t.Fatal("fresh node not used for new room")
	}
}

func TestNodeRegistrationOnlyAffectsNewRooms(t *testing.T) {
	a, b, c := newTestNode(t, "a"), newTestNode(t, "b"), newTestNode(t, "c")
	pool := newPool(t, a, b)
	first := allocate(t, pool, "alpha")
	second := allocate(t, pool, "beta")
	if first.NodeID == second.NodeID {
		t.Fatal("placement ignored reserved participants")
	}
	if err := pool.Register(c.config()); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	third := allocate(t, pool, "gamma")
	if third.NodeID != "c" {
		t.Fatal("registered empty node not selected")
	}
	again := allocate(t, pool, "alpha")
	if again.NodeID != first.NodeID || again.RoomInstanceID != first.RoomInstanceID {
		t.Fatal("scale-out moved active room")
	}
}
func connectNode(t *testing.T, a control.Allocation, participant, ticket string) (*websocket.Conn, *wire.Welcome) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, a.WebSocketURL, &websocket.DialOptions{Subprotocols: []string{"relay.v2"}})
	if err != nil {
		t.Fatal(err)
	}
	sendNode(t, conn, &wire.ClientEnvelope{Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{ApplicationProtocol: a.ApplicationProtocol, Credential: &wire.Connect_ConnectTicket{ConnectTicket: ticket}}}})
	welcome := receiveNode(t, conn, func(e *wire.ServerEnvelope) bool { return e.GetWelcome() != nil }).GetWelcome()
	if welcome.ParticipantId != participant {
		t.Fatal("wrong participant")
	}
	return conn, welcome
}
func sendNode(t *testing.T, c *websocket.Conn, e *wire.ClientEnvelope) {
	t.Helper()
	e.ProtocolRevision = 2
	data, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatal(err)
	}
}
func receiveNode(t *testing.T, c *websocket.Conn, check func(*wire.ServerEnvelope) bool) *wire.ServerEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var e wire.ServerEnvelope
		if err := proto.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		if check(&e) {
			return &e
		}
	}
}

func TestRecoveryPreservesOriginalLobbyOwner(t *testing.T) {
	node := newTestNode(t, "owner-test")
	pool := newPool(t, node)
	result, err := pool.Allocate(context.Background(), "owner-room", "demo.v1", []string{"z-owner", "a-guest"})
	if err != nil {
		t.Fatal(err)
	}
	recovered := newPool(t, node).Recovered()
	if len(recovered) != 1 || recovered[0].Participants[0] != "z-owner" || recovered[0].Allocation.RoomInstanceID != result.RoomInstanceID {
		t.Fatal("recovery changed the original owner")
	}
}

func TestConflictingOwnershipBlocksPlacementUntilResolved(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	first, err := a.relay.CreateRoom("collision", "demo.v1", []string{"alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.relay.CreateRoom("collision", "demo.v1", []string{"bob"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := New([]NodeConfig{a.config(), b.config()})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := pool.Reconcile(context.Background()); err == nil || pool.Ready() {
			t.Fatal("conflicting inventory was considered ready")
		}
		if _, err := pool.Allocate(context.Background(), "fresh", "demo.v1", []string{"eve"}); !errors.Is(err, relay.ErrConflict) {
			t.Fatal("placement proceeded during conflicting ownership")
		}
	}
	if err := a.relay.EndRoom(first.RoomInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := b.relay.EndRoom(second.RoomInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !pool.Ready() {
		t.Fatal("resolved registry did not recover")
	}
}

func TestRegisterRejectsUnsafeNodeTokenFiles(t *testing.T) {
	secret := [32]byte{1}
	encoded := base64.RawURLEncoding.EncodeToString(secret[:])
	for _, name := range []string{"mode 0644", "symlink", "trailing space", "oversized whitespace"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.token")
			body := encoded
			if name == "trailing space" {
				body += " "
			}
			if name == "oversized whitespace" {
				body += strings.Repeat("\n", 1<<20)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "mode 0644" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if name == "symlink" {
				link := filepath.Join(t.TempDir(), "token-link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			_, err := New([]NodeConfig{{ID: "node-a", ControlURL: "http://127.0.0.1:8091", WebSocketURL: "ws://127.0.0.1:8090/v2/relay", TokenFile: path}})
			if err == nil {
				t.Fatal("unsafe node token file was accepted")
			}
			if strings.Contains(err.Error(), encoded) {
				t.Fatal("registration error disclosed the node token")
			}
		})
	}
}

func TestRegisterAcceptsCanonicalNodeTokenFiles(t *testing.T) {
	const encoded = "QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE"
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("suffix %q", suffix), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.token")
			if err := os.WriteFile(path, []byte(encoded+suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			pool, err := New([]NodeConfig{{ID: "node-a", ControlURL: "http://127.0.0.1:8091", WebSocketURL: "ws://127.0.0.1:8090/v2/relay", TokenFile: path}})
			if err != nil {
				t.Fatal(err)
			}
			if pool.nodes["node-a"].token != encoded {
				t.Fatal("node credential retained its file terminator")
			}
		})
	}
}

func controlRequest(t *testing.T, api *control.Server, method, path, token string, body any, status int, target any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/v2/api"+path, bytes.NewReader(data))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, recorder.Code, status, recorder.Body)
	}
	if target != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
}

func readyControlLobby(t *testing.T, pool *Pool) (*control.Server, string, string, string) {
	t.Helper()
	auth, err := playerauth.New(playerauth.Config{OperatorSecret: [32]byte{1}, TokenTTL: playerauth.HardTokenTTL, StableKey: true})
	if err != nil {
		t.Fatal(err)
	}
	api, err := control.New(control.Config{Auth: auth, Allocator: pool})
	if err != nil {
		t.Fatal(err)
	}
	alice, _, err := auth.Issue("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, _, err := auth.Issue("bob")
	if err != nil {
		t.Fatal(err)
	}
	var room struct{ ID string }
	controlRequest(t, api, "POST", "/lobbies", alice, map[string]any{"capacity": 2, "applicationProtocol": "demo.v1", "visibility": "private"}, 201, &room)
	base := "/lobbies/" + room.ID
	controlRequest(t, api, "POST", base+"/join", bob, map[string]any{"revision": 1}, 200, nil)
	controlRequest(t, api, "POST", base+"/ready", alice, map[string]any{"revision": 2, "ready": true}, 200, nil)
	controlRequest(t, api, "POST", base+"/ready", bob, map[string]any{"revision": 3, "ready": true}, 200, nil)
	return api, base, alice, bob
}

func TestLostReplyThenUnhealthyNodeKeepsRosterFrozen(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pool := newPool(t, a, b)
	api, base, alice, bob := readyControlLobby(t, pool)
	a.dropNext.Store(true)
	controlRequest(t, api, "POST", base+"/start", alice, map[string]any{"revision": 4}, 503, nil)
	original := a.relay.Inventory().Rooms[0].RoomInstanceID
	a.failHeartbeat.Store(true)
	if err := pool.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	controlRequest(t, api, "POST", base+"/start", alice, map[string]any{"revision": 4}, 503, nil)
	controlRequest(t, api, "POST", base+"/leave", bob, map[string]any{"revision": 4}, 409, nil)
	controlRequest(t, api, "POST", base+"/ready", bob, map[string]any{"revision": 4, "ready": false}, 409, nil)
	a.failHeartbeat.Store(false)
	if err := pool.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	controlRequest(t, api, "POST", base+"/start", alice, map[string]any{"revision": 4}, 204, nil)
	var assignment control.Assignment
	controlRequest(t, api, "GET", base+"/assignment", alice, nil, 200, &assignment)
	if assignment.RoomInstanceID != original || assignment.NodeID != "a" || len(b.relay.Inventory().Rooms) != 0 {
		t.Fatal("recovery changed reservation owner")
	}
}

func TestProxy503KeepsReservationOwner(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	pool := newPool(t, a, b)
	a.proxy503.Store(true)
	participants := []string{"alice", "bob"}
	if _, err := pool.Allocate(t.Context(), "proxy-room", "demo.v1", participants); err == nil || errors.Is(err, relay.ErrCapacity) {
		t.Fatalf("empty proxy 503 treated as definitive capacity refusal: %v", err)
	}
	// Make another node preferable if the ambiguous owner was incorrectly discarded.
	if _, err := a.relay.CreateRoom("other", "demo.v1", []string{"eve"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := pool.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := pool.Allocate(t.Context(), "proxy-room", "demo.v1", participants)
	if err != nil || result.NodeID != "a" || len(b.relay.Inventory().Rooms) != 0 {
		t.Fatalf("proxy response moved reservation: %+v %v", result, err)
	}
}

func TestDefinitiveCapacityRefusalUnlocksLobby(t *testing.T) {
	n := newTestNode(t, "a")
	pool := newPool(t, n)
	api, base, alice, bob := readyControlLobby(t, pool)
	participants := make([]string, 16)
	for i := range participants {
		participants[i] = fmt.Sprintf("occupied-%d", i)
	}
	if _, err := n.relay.CreateRoom("occupied", "demo.v1", participants, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Pool telemetry predates this reservation, so the refusal comes from the real node.
	controlRequest(t, api, "POST", base+"/start", alice, map[string]any{"revision": 4}, 503, nil)
	controlRequest(t, api, "POST", base+"/ready", bob, map[string]any{"revision": 4, "ready": false}, 200, nil)
	controlRequest(t, api, "POST", base+"/leave", bob, map[string]any{"revision": 5}, 200, nil)
	if len(pool.owners) != 0 {
		t.Fatal("definitive refusal retained a reservation owner")
	}
}

func TestNode503RequiresBoundedAllocationRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		definitive bool
	}{
		{"node refusal", `{"error":{"code":"allocation_failed","message":"full"}}`, true},
		{"empty proxy", "", false},
		{"other error", `{"error":{"code":"overloaded"}}`, false},
		{"malformed", `{"error":{"code":"allocation_failed"}`, false},
		{"trailing JSON", `{"error":{"code":"allocation_failed"}} {}`, false},
		{"oversized", `{"error":{"code":"allocation_failed","message":"` + strings.Repeat("x", 4096) + `"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503); _, _ = w.Write([]byte(tc.body)) }))
			defer server.Close()
			n := node{config: NodeConfig{ControlURL: server.URL}, http: server.Client()}
			err := n.call(t.Context(), "POST", "/v2/reservations", nil, nil)
			if err == nil || errors.Is(err, relay.ErrCapacity) != tc.definitive {
				t.Fatalf("definitive=%t, error=%v", tc.definitive, err)
			}
		})
	}
}
