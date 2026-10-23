package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

type apiFixture struct {
	t       *testing.T
	server  *httptest.Server
	control *Server
	relay   *relay.Server
}

func newFixture(t *testing.T) apiFixture {
	t.Helper()
	relay, err := relay.New(relay.Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	auth, err := playerauth.New(playerauth.Config{OperatorSecret: [32]byte{1}, TokenTTL: playerauth.HardTokenTTL, StableKey: true})
	if err != nil {
		t.Fatal(err)
	}
	control, err := New(Config{Auth: auth, Allocator: LocalAllocator{Relay: relay, NodeID: "test", WebSocketURL: "ws" + server.URL[4:] + "/v2/relay"}, AllowedOrigins: []string{"localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/v2/api/", control)
	mux.Handle("/v2/relay", relay)
	t.Cleanup(func() { relay.Close(); server.Close() })
	return apiFixture{t: t, server: server, control: control, relay: relay}
}
func (f apiFixture) request(method, path, token string, body any, status int, target any) {
	f.t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	request, err := http.NewRequest(method, f.server.URL+"/v2/api"+path, bytes.NewReader(data))
	if err != nil {
		f.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		f.t.Fatalf("%s %s: status %d, want %d", method, path, response.StatusCode, status)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			f.t.Fatal(err)
		}
	}
}
func (f apiFixture) guest() (string, string) {
	var response struct{ Token, ParticipantID string }
	f.request("POST", "/sessions", "", struct{}{}, 201, &response)
	return response.Token, response.ParticipantID
}

func TestLobbyFlowAndPrivateAssignments(t *testing.T) {
	f := newFixture(t)
	alice, aliceID := f.guest()
	bob, bobID := f.guest()
	eve, _ := f.guest()
	var room lobby
	f.request("POST", "/lobbies", alice, map[string]any{"capacity": 2, "applicationProtocol": "demo.v1", "visibility": "private"}, 201, &room)
	base := "/lobbies/" + room.ID
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 1}, 409, nil)
	f.request("GET", base, eve, nil, 403, nil)
	f.request("POST", base+"/join", bob, revisionRequest{Revision: 1}, 200, &room)
	if len(room.Participants) != 2 {
		t.Fatal("join lost participant")
	}
	f.request("POST", base+"/start", bob, revisionRequest{Revision: 2}, 403, nil)
	f.request("POST", base+"/ready", alice, map[string]any{"revision": 2, "ready": true}, 200, nil)
	f.request("POST", base+"/ready", bob, map[string]any{"revision": 3, "ready": true}, 200, nil)
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 204, nil)
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 204, nil)
	var a, b Assignment
	f.request("GET", base+"/assignment", alice, nil, 200, &a)
	f.request("GET", base+"/assignment", bob, nil, 200, &b)
	if a.RoomInstanceID != b.RoomInstanceID || a.ParticipantID != aliceID || b.ParticipantID != bobID || a.ConnectTicket == b.ConnectTicket {
		t.Fatal("assignment isolation failed")
	}
	var raw map[string]json.RawMessage
	f.request("GET", base+"/assignment", alice, nil, 200, &raw)
	if _, ok := raw["tickets"]; ok {
		t.Fatal("all participant tickets exposed")
	}
	var refreshed Assignment
	f.request("POST", base+"/assignment", alice, struct{}{}, 200, &refreshed)
	if refreshed.ConnectTicket == a.ConnectTicket || refreshed.RoomInstanceID != a.RoomInstanceID {
		t.Fatal("reissue failed")
	}
	f.request("POST", base+"/assignment", eve, struct{}{}, 403, nil)
}

func TestFIFOQueueAndCancel(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.guest()
	bob, _ := f.guest()
	eve, _ := f.guest()
	var a, b, c matchTicket
	request := groupRequest{Capacity: 2, ApplicationProtocol: "demo.v1"}
	f.request("POST", "/matchmaking", alice, request, 201, &a)
	if a.Status != "queued" {
		t.Fatal("first participant not queued")
	}
	f.request("POST", "/matchmaking", bob, request, 201, &b)
	f.request("GET", "/matchmaking/"+a.ID, alice, nil, 200, &a)
	if a.Assignment == nil || b.Assignment == nil || a.Assignment.RoomInstanceID != b.Assignment.RoomInstanceID {
		t.Fatal("FIFO group not formed")
	}
	f.request("GET", "/matchmaking/"+a.ID, eve, nil, 403, nil)
	f.request("DELETE", "/matchmaking/"+a.ID, alice, nil, 409, nil)
	f.request("POST", "/matchmaking", eve, request, 201, &c)
	f.request("DELETE", "/matchmaking/"+c.ID, eve, nil, 204, nil)
	f.request("POST", "/matchmaking", eve, request, 201, &c)
}

func TestPreflightBeforeAuthAndOriginRejection(t *testing.T) {
	f := newFixture(t)
	for _, entry := range []struct {
		origin string
		status int
	}{{"http://localhost:5173", 204}, {"https://evil.test", 403}} {
		request, err := http.NewRequest("OPTIONS", f.server.URL+"/v2/api/lobbies", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", entry.origin)
		response, err := f.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != entry.status {
			t.Fatalf("preflight %s: %d", entry.origin, response.StatusCode)
		}
	}
}

func TestLobbyRevisionReadinessVisibilityAndOwnerTransfer(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.guest()
	bob, bobID := f.guest()
	carol, _ := f.guest()
	eve, _ := f.guest()
	var room lobby
	f.request("POST", "/lobbies", alice, map[string]any{"capacity": 3, "applicationProtocol": "demo.v1", "visibility": "public"}, 201, &room)
	base := "/lobbies/" + room.ID
	var preview map[string]json.RawMessage
	f.request("GET", base+"/preview", bob, nil, 200, &preview)
	if _, exists := preview["participants"]; exists {
		t.Fatal("preview leaked roster")
	}
	f.request("POST", base+"/join", bob, revisionRequest{Revision: 1}, 200, &room)
	f.request("POST", base+"/ready", alice, map[string]any{"revision": 2, "ready": true}, 200, &room)
	f.request("POST", base+"/join", carol, revisionRequest{Revision: 3}, 200, &room)
	for _, ready := range room.Ready {
		if ready {
			t.Fatal("membership change did not reset readiness")
		}
	}
	f.request("POST", base+"/ready", bob, map[string]any{"revision": 3, "ready": true}, 409, nil)
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 409, nil)
	f.request("POST", base+"/leave", alice, revisionRequest{Revision: 4}, 200, &room)
	if room.OwnerID != bobID || room.Revision != 5 {
		t.Fatal("owner not transferred to earliest remaining participant")
	}
	var private lobby
	f.request("POST", "/lobbies", eve, map[string]any{"capacity": 2, "applicationProtocol": "demo.v1", "visibility": "private"}, 201, &private)
	var page struct {
		Lobbies []struct {
			ID string `json:"id"`
		} `json:"lobbies"`
	}
	f.request("GET", "/lobbies?applicationProtocol=demo.v1&limit=50", eve, nil, 200, &page)
	if len(page.Lobbies) != 1 || page.Lobbies[0].ID != room.ID {
		t.Fatal("private lobby appeared in public listing")
	}
}

func TestSessionRenewalKeepsAuthenticatedIdentity(t *testing.T) {
	f := newFixture(t)
	token, id := f.guest()
	var renewed struct {
		Token, ParticipantID string
		ExpiresInSeconds     int
	}
	f.request("POST", "/sessions/refresh", token, struct{}{}, 200, &renewed)
	if renewed.ParticipantID != id || renewed.Token == "" || renewed.ExpiresInSeconds < 1 || renewed.ExpiresInSeconds > 900 {
		t.Fatal("renewal changed identity or TTL")
	}
	f.request("POST", "/sessions/refresh", "", struct{}{}, 401, nil)
}

// Simulate a node committing its reservation before its HTTP response is lost.
type lostAllocationReply struct {
	Allocator
	lost bool
}

func (a *lostAllocationReply) Allocate(ctx context.Context, id, application string, participants []string) (Allocation, error) {
	allocation, err := a.Allocator.Allocate(ctx, id, application, participants)
	if err == nil && !a.lost {
		a.lost = true
		return Allocation{}, context.DeadlineExceeded
	}
	return allocation, err
}

func TestLostReservationReplyFreezesRosterAndRetriesSameRoom(t *testing.T) {
	f := newFixture(t)
	f.control.allocator = &lostAllocationReply{Allocator: f.control.allocator}
	alice, _ := f.guest()
	bob, _ := f.guest()
	var room lobby
	create := map[string]any{"capacity": 2, "applicationProtocol": "demo.v1", "visibility": "private"}
	f.request("POST", "/lobbies", alice, create, 201, &room)
	base := "/lobbies/" + room.ID
	f.request("POST", base+"/join", bob, revisionRequest{Revision: 1}, 200, nil)
	f.request("POST", base+"/ready", alice, map[string]any{"revision": 2, "ready": true}, 200, nil)
	f.request("POST", base+"/ready", bob, map[string]any{"revision": 3, "ready": true}, 200, nil)
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 503, nil)
	f.request("GET", base, alice, nil, 200, &room)
	if !room.AllocationPending || room.Allocating || room.Revision != 4 {
		t.Fatal("uncertain allocation did not retain the original request")
	}
	f.request("POST", base+"/leave", bob, revisionRequest{Revision: 4}, 409, nil)
	f.request("POST", base+"/ready", bob, map[string]any{"revision": 4, "ready": false}, 409, nil)
	f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 204, nil)
	var assignment Assignment
	f.request("GET", base+"/assignment", alice, nil, 200, &assignment)
	if f.relay.Stats().Rooms != 1 {
		t.Fatal("retry created a second reservation")
	}
	f.control.EndMatch(room.ID, "stale-incarnation")
	f.request("GET", base+"/assignment", alice, nil, 200, nil)
	f.relay.EndRoom(assignment.RoomInstanceID)
	f.control.ReconcileLocal(f.relay.HasRoom)
	f.request("GET", base, alice, nil, 200, &room)
	if room.Status != "ended" {
		t.Fatal("ended relay room retained control ownership")
	}
	f.request("GET", base+"/assignment", alice, nil, 410, nil)
	f.request("POST", "/lobbies", alice, create, 201, nil)
	f.request("POST", "/lobbies", bob, create, 201, nil)
}

// allocationHook runs deterministic lifecycle changes while allocation is outside the lock.
type allocationHook struct {
	Allocator
	allocate func(context.Context, string, string, []string) (Allocation, error)
}

func (a allocationHook) Allocate(ctx context.Context, id, application string, participants []string) (Allocation, error) {
	return a.allocate(ctx, id, application, participants)
}

func TestFormedQueueRetainsNearExpiryTicket(t *testing.T) {
	f := newFixture(t)
	var elapsed atomic.Int64
	now := time.Now()
	f.control.now = func() time.Time { return now.Add(time.Duration(elapsed.Load()) * time.Second) }
	alice, aliceID := f.guest()
	bob, bobID := f.guest()
	request := groupRequest{Capacity: 2, ApplicationProtocol: "demo.v1"}
	var a, b matchTicket
	f.request("POST", "/matchmaking", alice, request, 201, &a)
	elapsed.Store(119)
	local := f.control.allocator
	f.control.allocator = allocationHook{Allocator: local, allocate: func(ctx context.Context, id, application string, participants []string) (Allocation, error) {
		elapsed.CompareAndSwap(119, 121)
		f.control.Expire()
		return local.Allocate(ctx, id, application, participants)
	}}
	f.request("POST", "/matchmaking", bob, request, 201, &b)
	f.request("GET", "/matchmaking/"+a.ID, alice, nil, 200, &a)
	if a.Assignment == nil || b.Assignment == nil || a.Assignment.ParticipantID != aliceID || b.Assignment.ParticipantID != bobID || a.Assignment.ConnectTicket == b.Assignment.ConnectTicket {
		t.Fatal("formed queue lost a private participant assignment")
	}
}

func TestFormedQueueExpiryReleasesAllParticipants(t *testing.T) {
	f := newFixture(t)
	var elapsed atomic.Int64
	now := time.Now()
	f.control.now = func() time.Time { return now.Add(time.Duration(elapsed.Load()) * time.Second) }
	alice, _ := f.guest()
	bob, _ := f.guest()
	request := groupRequest{Capacity: 2, ApplicationProtocol: "demo.v1"}
	var a, b matchTicket
	f.request("POST", "/matchmaking", alice, request, 201, &a)
	elapsed.Store(119)
	f.control.allocator = allocationHook{Allocator: f.control.allocator, allocate: func(context.Context, string, string, []string) (Allocation, error) {
		elapsed.CompareAndSwap(119, 121)
		f.control.Expire()
		return Allocation{}, relay.ErrCapacity
	}}
	f.request("POST", "/matchmaking", bob, request, 201, &b)
	elapsed.Store(238)
	f.request("GET", "/matchmaking/"+a.ID, alice, nil, 200, nil)
	elapsed.Store(239)
	f.control.Expire()
	f.request("GET", "/matchmaking/"+a.ID, alice, nil, 404, nil)
	f.request("GET", "/matchmaking/"+b.ID, bob, nil, 404, nil)
	f.request("POST", "/matchmaking", alice, request, 201, nil)
	f.request("POST", "/matchmaking", bob, request, 201, nil)
}

func TestAllocationCompletionDoesNotReviveEndedMatch(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue=%t", queue), func(t *testing.T) {
			f := newFixture(t)
			local := f.control.allocator
			f.control.allocator = allocationHook{Allocator: local, allocate: func(ctx context.Context, id, application string, participants []string) (Allocation, error) {
				allocation, err := local.Allocate(ctx, id, application, participants)
				if err == nil {
					f.control.EndMatch(id, allocation.RoomInstanceID)
				}
				return allocation, err
			}}
			alice, aliceID := f.guest()
			bob, bobID := f.guest()
			var id string
			if queue {
				var a, b matchTicket
				request := groupRequest{Capacity: 2, ApplicationProtocol: "demo.v1"}
				f.request("POST", "/matchmaking", alice, request, 201, &a)
				f.request("POST", "/matchmaking", bob, request, 201, &b)
				f.control.mu.Lock()
				id = f.control.tickets[a.ID].roomID
				f.control.mu.Unlock()
			} else {
				var room lobby
				f.request("POST", "/lobbies", alice, map[string]any{"capacity": 2, "applicationProtocol": "demo.v1", "visibility": "private"}, 201, &room)
				id = room.ID
				base := "/lobbies/" + id
				f.request("POST", base+"/join", bob, revisionRequest{Revision: 1}, 200, nil)
				f.request("POST", base+"/ready", alice, map[string]any{"revision": 2, "ready": true}, 200, nil)
				f.request("POST", base+"/ready", bob, map[string]any{"revision": 3, "ready": true}, 200, nil)
				// Ending the authority during the RPC must win over its late success.
				f.request("POST", base+"/start", alice, revisionRequest{Revision: 4}, 503, nil)
			}
			f.control.mu.Lock()
			defer f.control.mu.Unlock()
			if f.control.lobbies[id].Status != "ended" || f.control.byPlayer[aliceID] != "" || f.control.byPlayer[bobID] != "" {
				t.Fatal("late allocation success revived an ended match or membership")
			}
			for _, ticket := range f.control.tickets {
				if ticket.roomID == id && (ticket.Status != "ended" || ticket.Assignment != nil) {
					t.Fatal("late success revived queue assignment")
				}
			}
		})
	}
}

func TestQueueFailureAfterFormedDeadlineExpiresTogether(t *testing.T) {
	f := newFixture(t)
	var elapsed atomic.Int64
	now := time.Now()
	f.control.now = func() time.Time { return now.Add(time.Duration(elapsed.Load()) * time.Second) }
	alice, aliceID := f.guest()
	bob, bobID := f.guest()
	var a, b matchTicket
	request := groupRequest{Capacity: 2, ApplicationProtocol: "demo.v1"}
	f.request("POST", "/matchmaking", alice, request, 201, &a)
	f.control.allocator = allocationHook{Allocator: f.control.allocator, allocate: func(context.Context, string, string, []string) (Allocation, error) {
		elapsed.Store(120)
		f.control.Expire()
		f.control.mu.Lock()
		retained := len(f.control.tickets) == 2 && len(f.control.lobbies) == 1
		f.control.mu.Unlock()
		if !retained {
			t.Error("in-flight allocation lost its group or tickets")
		}
		return Allocation{}, context.DeadlineExceeded
	}}
	f.request("POST", "/matchmaking", bob, request, 201, &b)
	f.control.mu.Lock()
	defer f.control.mu.Unlock()
	if len(f.control.tickets) != 0 || len(f.control.lobbies) != 0 || f.control.byPlayer[aliceID] != "" || f.control.byPlayer[bobID] != "" {
		t.Fatal("failed allocation retained an expired group, ticket or membership")
	}
}

// A stalled client must not hold the lock needed by independent lobby changes.
func TestBlockedHTTPResponseDoesNotBlockManager(t *testing.T) {
	f := newFixture(t)
	alice, _ := f.guest()
	bob, _ := f.guest()
	writer := &blockedResponseWriter{header: make(http.Header), entered: make(chan struct{}), release: make(chan struct{})}
	firstDone := make(chan struct{})
	request := httptest.NewRequest("POST", "/v2/api/lobbies", bytes.NewBufferString(`{"capacity":2,"applicationProtocol":"demo.v1","visibility":"private"}`))
	request.Header.Set("Authorization", "Bearer "+alice)
	request.Header.Set("Content-Type", "application/json")
	go func() { f.control.ServeHTTP(writer, request); close(firstDone) }()
	select {
	case <-writer.entered:
	case <-time.After(2 * time.Second):
		close(writer.release)
		t.Fatal("first response did not reach writer")
	}
	secondDone := make(chan int, 1)
	go func() {
		request := httptest.NewRequest("POST", "/v2/api/lobbies", bytes.NewBufferString(`{"capacity":2,"applicationProtocol":"demo.v1","visibility":"private"}`))
		request.Header.Set("Authorization", "Bearer "+bob)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.control.ServeHTTP(response, request)
		secondDone <- response.Code
	}()
	var status int
	select {
	case status = <-secondDone:
	case <-time.After(2 * time.Second):
		close(writer.release)
		<-firstDone
		<-secondDone
		t.Fatal("blocked HTTP response prevented independent lobby change")
	}
	close(writer.release)
	<-firstDone
	if status != http.StatusCreated {
		t.Fatalf("independent create: status %d", status)
	}
}

type blockedResponseWriter struct {
	header           http.Header
	entered, release chan struct{}
}

func (w *blockedResponseWriter) Header() http.Header { return w.header }
func (w *blockedResponseWriter) WriteHeader(int)     {}
func (w *blockedResponseWriter) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(data), nil
}
