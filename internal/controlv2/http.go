// Package controlv2 owns guest identity, lobby membership and FIFO match allocation.
package controlv2

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relayv2"
)

type Config struct {
	TrustedProxies []string
	Auth           *playerauth.Issuer
	Allocator      Allocator
	AllowedOrigins []string
	Now            func() time.Time
}

type Server struct {
	mu             sync.Mutex
	auth           *playerauth.Issuer
	allocator      Allocator
	origins        []string
	now            func() time.Time
	lobbies        map[string]*lobby
	byPlayer       map[string]string
	tickets        map[string]*matchTicket
	queue          []string
	ips            *httpx.IPBudget
	trustedProxies []netip.Prefix
	admission      *httpx.Admission
	mux            *http.ServeMux
}

type lobby struct {
	Revision          uint32          `json:"revision"`
	Ready             map[string]bool `json:"ready"`
	Visibility        string          `json:"visibility"`
	ID                string          `json:"id"`
	OwnerID           string          `json:"ownerId"`
	Participants      []string        `json:"participants"`
	Capacity          int             `json:"capacity"`
	Status            string          `json:"status"`
	application       string
	expires           time.Time
	allocation        *Allocation
	Allocating        bool `json:"allocating"`
	AllocationPending bool `json:"allocationPending"`
}

type matchTicket struct {
	ID                          string      `json:"id"`
	Status                      string      `json:"status"`
	Assignment                  *Assignment `json:"assignment,omitempty"`
	player, application, roomID string
	capacity                    int
	expires                     time.Time
}

type identityKey struct{}

func New(config Config) (*Server, error) {
	if config.Auth == nil || config.Allocator == nil {
		return nil, errors.New("controlv2: missing authentication or allocator")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	trusted, err := httpx.ParseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}
	server := &Server{trustedProxies: trusted, auth: config.Auth, allocator: config.Allocator, origins: slices.Clone(config.AllowedOrigins), now: config.Now,
		lobbies: make(map[string]*lobby), byPlayer: make(map[string]string), tickets: make(map[string]*matchTicket),
		ips: httpx.NewIPBudget(20, 40, 16384, config.Now), admission: httpx.NewAdmission(2000, 4000, 128, config.Now), mux: http.NewServeMux()}
	server.mux.HandleFunc("POST /v2/api/sessions", server.session)
	server.mux.HandleFunc("POST /v2/api/sessions/refresh", server.renewSession)
	server.mux.HandleFunc("POST /v2/api/lobbies", server.create)
	server.mux.HandleFunc("GET /v2/api/lobbies", server.list)
	server.mux.HandleFunc("GET /v2/api/lobbies/{id}/preview", server.preview)
	server.mux.HandleFunc("POST /v2/api/lobbies/{id}/ready", server.ready)
	server.mux.HandleFunc("POST /v2/api/lobbies/{id}/leave", server.leave)
	server.mux.HandleFunc("GET /v2/api/lobbies/{id}", server.get)
	server.mux.HandleFunc("POST /v2/api/lobbies/{id}/join", server.join)
	server.mux.HandleFunc("POST /v2/api/lobbies/{id}/start", server.start)
	server.mux.HandleFunc("GET /v2/api/lobbies/{id}/assignment", server.assignment)
	server.mux.HandleFunc("POST /v2/api/lobbies/{id}/assignment", server.assignment)
	server.mux.HandleFunc("POST /v2/api/matchmaking", server.enqueue)
	server.mux.HandleFunc("GET /v2/api/matchmaking/{id}", server.ticket)
	server.mux.HandleFunc("DELETE /v2/api/matchmaking/{id}", server.cancel)
	return server, nil
}

func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Origin")
	if r.URL.RawQuery != "" && !(r.URL.Path == "/v2/api/lobbies" && r.Method == http.MethodGet) {
		httpx.WriteInvalid(w)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || !slices.Contains(server.origins, parsed.Host) {
			httpx.WriteError(w, 403, "origin_denied", "origin not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	release, ok := server.admission.Enter(w)
	if !ok {
		return
	}
	defer release()
	if r.URL.Path != "/v2/api/sessions" {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			httpx.WriteError(w, 401, "unauthorized", "player token required")
			return
		}
		claims, err := server.auth.Verify(strings.TrimPrefix(values[0], "Bearer "))
		if err != nil {
			httpx.WriteError(w, 401, "unauthorized", "player token rejected")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, claims.PlayerID))
	}
	server.mux.ServeHTTP(w, r)
}

func player(r *http.Request) string {
	value, _ := r.Context().Value(identityKey{}).(string)
	return value
}
func randomID(prefix string) (string, error) {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data[:]), nil
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, message string) {
	httpx.WriteError(w, status, "control_failed", message)
}

func (server *Server) session(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !httpx.DecodeExact(w, r, &request) {
		return
	}
	ip, err := httpx.ClientIP(r, server.trustedProxies)
	if err != nil {
		problem(w, 400, "invalid client address")
		return
	}
	allowed := server.ips.Allow(ip)
	if !allowed {
		problem(w, 429, "guest rate limit")
		return
	}
	id, err := randomID("p-")
	if err != nil {
		problem(w, 503, "identity unavailable")
		return
	}
	token, claims, err := server.auth.Issue(id)
	if err != nil {
		problem(w, 503, "identity unavailable")
		return
	}
	reply(w, 201, map[string]any{"participantId": id, "token": token, "expiresAt": claims.ExpiresAt, "expiresInSeconds": max(1, int(claims.ExpiresAt.Sub(server.now())/time.Second))})
}

type groupRequest struct {
	Capacity            int    `json:"capacity"`
	ApplicationProtocol string `json:"applicationProtocol"`
}

func readGroup(w http.ResponseWriter, r *http.Request) (groupRequest, bool) {
	var request groupRequest
	if !httpx.DecodeExact(w, r, &request, "capacity", "applicationProtocol") {
		return request, false
	}
	if request.Capacity < 2 || request.Capacity > 16 || !protocol.ValidID(request.ApplicationProtocol) {
		httpx.WriteInvalid(w)
		return request, false
	}
	return request, true
}

func (server *Server) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Capacity            int    `json:"capacity"`
		ApplicationProtocol string `json:"applicationProtocol"`
		Visibility          string `json:"visibility"`
	}
	if !httpx.DecodeExact(w, r, &request, "capacity", "applicationProtocol", "visibility") {
		return
	}
	if request.Capacity < 2 || request.Capacity > 16 || !protocol.ValidID(request.ApplicationProtocol) || request.Visibility != "public" && request.Visibility != "private" {
		httpx.WriteInvalid(w)
		return
	}
	id, err := randomID("l-")
	if err != nil {
		problem(w, 503, "random source failed")
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	who := player(r)
	if server.byPlayer[who] != "" {
		problem(w, 409, "already in lobby or queue")
		return
	}
	if len(server.lobbies) >= 4096 {
		problem(w, 503, "lobby capacity exhausted")
		return
	}
	lobby := &lobby{Revision: 1, Visibility: request.Visibility, ID: id, OwnerID: who, Participants: []string{who}, Capacity: request.Capacity, Status: "open", application: request.ApplicationProtocol, expires: server.now().Add(30 * time.Minute)}
	resetReady(lobby)
	server.lobbies[id] = lobby
	server.byPlayer[who] = id
	reply(w, 201, lobby)
}

func (server *Server) authorizedLobby(w http.ResponseWriter, r *http.Request) *lobby {
	lobby := server.lobbies[r.PathValue("id")]
	if lobby == nil {
		problem(w, 404, "lobby not found")
		return nil
	}
	if !slices.Contains(lobby.Participants, player(r)) {
		problem(w, 403, "not a participant")
		return nil
	}
	return lobby
}
func (server *Server) get(w http.ResponseWriter, r *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	lobby := server.authorizedLobby(w, r)
	if lobby != nil {
		reply(w, 200, lobby)
	}
}
func (server *Server) join(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	lobby := server.lobbies[r.PathValue("id")]
	if lobby == nil {
		problem(w, 404, "lobby not found")
		return
	}
	who := player(r)
	if slices.Contains(lobby.Participants, who) {
		reply(w, 200, lobby)
		return
	}
	if !server.currentRevision(w, lobby, request.Revision, false) {
		return
	}
	if server.byPlayer[who] != "" || lobby.Status != "open" || lobby.Allocating || len(lobby.Participants) >= lobby.Capacity {
		problem(w, 409, "cannot join lobby")
		return
	}
	lobby.Participants = append(lobby.Participants, who)
	resetReady(lobby)
	lobby.Revision++
	server.byPlayer[who] = lobby.ID
	reply(w, 200, lobby)
}
func (server *Server) start(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	server.mu.Lock()
	server.expireLocked()
	lobby := server.authorizedLobby(w, r)
	if lobby == nil {
		server.mu.Unlock()
		return
	}
	if lobby.OwnerID != player(r) {
		server.mu.Unlock()
		problem(w, 403, "owner required")
		return
	}
	if lobby.Status == "matched" {
		server.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if !server.currentRevision(w, lobby, request.Revision, true) {
		server.mu.Unlock()
		return
	}
	allReady := true
	for _, id := range lobby.Participants {
		allReady = allReady && lobby.Ready[id]
	}
	if !allReady || lobby.Allocating || len(lobby.Participants) != lobby.Capacity {
		server.mu.Unlock()
		problem(w, 409, "lobby not ready")
		return
	}
	lobby.Allocating = true
	lobby.AllocationPending = true
	participants := slices.Clone(lobby.Participants)
	server.mu.Unlock()
	allocation, err := server.allocator.Allocate(r.Context(), lobby.ID, lobby.application, participants)
	server.mu.Lock()
	defer server.mu.Unlock()
	lobby.Allocating = false
	if err != nil {
		if errors.Is(err, relayv2.ErrCapacity) || errors.Is(err, relayv2.ErrInvalid) || errors.Is(err, relayv2.ErrClosed) {
			lobby.AllocationPending = false
		}
		problem(w, 503, "allocation unavailable; retry the same lobby")
		return
	}
	lobby.AllocationPending = false
	lobby.allocation = &allocation
	lobby.expires = allocation.ExpiresAt
	lobby.Status = "matched"
	lobby.Revision++
	w.WriteHeader(204)
}
func (server *Server) assignment(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var request struct{}
		if !httpx.DecodeExact(w, r, &request) {
			return
		}
	}
	server.mu.Lock()
	server.expireLocked()
	lobby := server.authorizedLobby(w, r)
	if lobby == nil {
		server.mu.Unlock()
		return
	}
	if lobby.Status == "ended" {
		server.mu.Unlock()
		problem(w, 410, "match ended")
		return
	}
	if lobby.allocation == nil {
		server.mu.Unlock()
		problem(w, 409, "match not formed")
		return
	}
	allocation := *lobby.allocation
	result := allocation.Assignment(player(r))
	server.mu.Unlock()
	if r.Method == http.MethodPost || result.ConnectTicket == "" {
		ticket, err := server.allocator.Refresh(r.Context(), allocation, player(r))
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, relayv2.ErrGone) || errors.Is(err, relayv2.ErrInvalid) {
				status = http.StatusGone
			}
			problem(w, status, "room unavailable")
			return
		}
		result.ConnectTicket = ticket.Value
		result.TicketExpiresAt = ticket.ExpiresAt
	}
	// Never expose the map containing the other participants' credentials.
	reply(w, 200, result)
}

func (server *Server) Expire() { server.mu.Lock(); defer server.mu.Unlock(); server.expireLocked() }
func (server *Server) expireLocked() {
	now := server.now()
	for id, lobby := range server.lobbies {
		if !lobby.Allocating && !now.Before(lobby.expires) {
			for _, who := range lobby.Participants {
				if server.byPlayer[who] == id {
					delete(server.byPlayer, who)
				}
			}
			delete(server.lobbies, id)
		}
	}
	for id, ticket := range server.tickets {
		if !now.Before(ticket.expires) {
			if server.byPlayer[ticket.player] == id {
				delete(server.byPlayer, ticket.player)
			}
			delete(server.tickets, id)
		}
	}
	server.queue = slices.DeleteFunc(server.queue, func(id string) bool { return server.tickets[id] == nil || server.tickets[id].Status != "queued" })
}

// Restore imports node-owned active matches after coordinator restart.
// It never creates a replacement room or changes existing room authority.
func (server *Server) Restore(allocation Allocation, participants []string) error {
	if !protocol.ValidID(allocation.RoomID) || !protocol.ValidID(allocation.RoomInstanceID) || len(participants) == 0 || len(participants) > 16 {
		return errors.New("invalid recovered match")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	if existing := server.lobbies[allocation.RoomID]; existing != nil {
		if existing.allocation != nil && existing.allocation.RoomInstanceID == allocation.RoomInstanceID {
			return nil
		}
		return errors.New("recovered room conflicts with coordinator state")
	}
	for _, participant := range participants {
		if !protocol.ValidID(participant) || server.byPlayer[participant] != "" {
			return errors.New("recovered participant conflicts with coordinator state")
		}
	}
	copy := allocation
	server.lobbies[allocation.RoomID] = &lobby{Revision: 1, Visibility: "private", Ready: map[string]bool{}, ID: allocation.RoomID, OwnerID: participants[0], Participants: slices.Clone(participants), Capacity: len(participants), Status: "matched", application: allocation.ApplicationProtocol, expires: allocation.ExpiresAt, allocation: &copy}
	for _, participant := range participants {
		server.byPlayer[participant] = allocation.RoomID
	}
	return nil
}

func (server *Server) renewSession(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	if !httpx.DecodeExact(w, r, &request) {
		return
	}
	token, claims, err := server.auth.Issue(player(r))
	if err != nil {
		problem(w, 503, "session renewal unavailable")
		return
	}
	reply(w, 200, map[string]any{"participantId": claims.PlayerID, "token": token, "expiresAt": claims.ExpiresAt, "expiresInSeconds": max(1, int(claims.ExpiresAt.Sub(server.now())/time.Second))})
}

// EndMatch releases lobby/queue ownership only after its exact node authority has ended.
func (server *Server) EndMatch(id, instance string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	lobby := server.lobbies[id]
	if lobby == nil || lobby.Status == "ended" {
		return
	}
	if lobby.allocation != nil && lobby.allocation.RoomInstanceID != instance {
		return
	}
	if lobby.Status != "matched" && !lobby.AllocationPending {
		return
	}
	lobby.Status = "ended"
	lobby.AllocationPending = false
	lobby.Allocating = false
	lobby.expires = server.now().Add(2 * time.Minute)
	for _, participant := range lobby.Participants {
		if server.byPlayer[participant] == id {
			delete(server.byPlayer, participant)
		}
	}
	for _, ticket := range server.tickets {
		if ticket.roomID == id {
			ticket.Status = "ended"
			ticket.Assignment = nil
			ticket.expires = lobby.expires
		}
	}
}

func (server *Server) ReconcileLocal(exists func(string) bool) {
	server.mu.Lock()
	var ended []struct{ id, instance string }
	for id, lobby := range server.lobbies {
		if lobby.allocation != nil && lobby.Status == "matched" && !exists(lobby.allocation.RoomInstanceID) {
			ended = append(ended, struct{ id, instance string }{id, lobby.allocation.RoomInstanceID})
		}
	}
	server.mu.Unlock()
	for _, room := range ended {
		server.EndMatch(room.id, room.instance)
	}
}
