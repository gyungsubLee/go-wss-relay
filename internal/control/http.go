// Package control owns guest identity, lobby membership and FIFO match allocation.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
)

type Config struct {
	TrustedProxies []string
	Auth           *playerauth.Issuer
	Allocator      Allocator
	AllowedOrigins []string
	Now            func() time.Time
}

type Server struct {
	*Manager
	auth           *playerauth.Issuer
	origins        []string
	ips            *httpx.IPBudget
	trustedProxies []netip.Prefix
	admission      *httpx.Admission
	mux            *http.ServeMux
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
	manager, err := NewManager(config.Allocator, config.Now)
	if err != nil {
		return nil, err
	}
	server := &Server{
		Manager:        manager,
		trustedProxies: trusted,
		auth:           config.Auth,
		origins:        slices.Clone(config.AllowedOrigins),
		ips:            httpx.NewIPBudget(20, 40, 16384, config.Now),
		admission:      httpx.NewAdmission(2000, 4000, 128, config.Now),
		mux:            http.NewServeMux(),
	}
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

func writeBusinessError(w http.ResponseWriter, err error) {
	failure, ok := errors.AsType[*Error](err)
	if !ok {
		problem(w, http.StatusServiceUnavailable, "control unavailable")
		return
	}
	if failure.Kind == "invalid" {
		httpx.WriteInvalid(w)
		return
	}
	status := http.StatusServiceUnavailable
	switch failure.Kind {
	case "forbidden":
		status = http.StatusForbidden
	case "not_found":
		status = http.StatusNotFound
	case "conflict":
		status = http.StatusConflict
	case "gone":
		status = http.StatusGone
	}
	problem(w, status, failure.Message)
}

type revisionRequest struct {
	Revision uint32 `json:"revision"`
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
	result, err := server.Manager.Create(player(r), request.Capacity, request.ApplicationProtocol, request.Visibility)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusCreated, result)
}

func (server *Server) get(w http.ResponseWriter, r *http.Request) {
	result, err := server.Manager.Get(player(r), r.PathValue("id"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) preview(w http.ResponseWriter, r *http.Request) {
	result, err := server.Manager.Preview(r.PathValue("id"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "applicationProtocol" && key != "cursor" && key != "limit" {
			httpx.WriteInvalid(w)
			return
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			httpx.WriteInvalid(w)
			return
		}
	}
	result, err := server.Manager.List(query.Get("applicationProtocol"), query.Get("cursor"), limit)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) join(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	result, err := server.Manager.Join(player(r), r.PathValue("id"), request.Revision)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) ready(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Revision uint32 `json:"revision"`
		Ready    bool   `json:"ready"`
	}
	if !httpx.DecodeExact(w, r, &request, "revision", "ready") {
		return
	}
	result, err := server.Manager.Ready(player(r), r.PathValue("id"), request.Revision, request.Ready)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) leave(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	result, err := server.Manager.Leave(player(r), r.PathValue("id"), request.Revision)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	if result == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) start(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	if err := server.Manager.Start(r.Context(), player(r), r.PathValue("id"), request.Revision); err != nil {
		writeBusinessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (server *Server) assignment(w http.ResponseWriter, r *http.Request) {
	refresh := r.Method == http.MethodPost
	if refresh {
		var request struct{}
		if !httpx.DecodeExact(w, r, &request) {
			return
		}
	}
	result, err := server.Manager.Assignment(r.Context(), player(r), r.PathValue("id"), refresh)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	request, ok := readGroup(w, r)
	if !ok {
		return
	}
	result, err := server.Manager.Enqueue(r.Context(), player(r), request.Capacity, request.ApplicationProtocol)
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusCreated, result)
}

func (server *Server) ticket(w http.ResponseWriter, r *http.Request) {
	result, err := server.Manager.Ticket(r.Context(), player(r), r.PathValue("id"))
	if err != nil {
		writeBusinessError(w, err)
		return
	}
	reply(w, http.StatusOK, result)
}

func (server *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := server.Manager.Cancel(player(r), r.PathValue("id")); err != nil {
		writeBusinessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
