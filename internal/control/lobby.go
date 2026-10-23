package control

import (
	"math"
	"net/http"
	"slices"
	"strconv"

	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
)

type revisionRequest struct {
	Revision uint32 `json:"revision"`
}

func (server *Server) currentRevision(w http.ResponseWriter, lobby *lobby, revision uint32, allowPending bool) bool {
	if revision == 0 || lobby.Revision != revision || lobby.Revision == math.MaxUint32 || lobby.Allocating || lobby.AllocationPending && !allowPending || lobby.Status != "open" {
		problem(w, 409, "lobby changed; reload before retrying")
		return false
	}
	return true
}
func resetReady(lobby *lobby) {
	lobby.Ready = make(map[string]bool, len(lobby.Participants))
	for _, id := range lobby.Participants {
		lobby.Ready[id] = false
	}
}
func (server *Server) preview(w http.ResponseWriter, r *http.Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	lobby := server.lobbies[r.PathValue("id")]
	if lobby == nil || lobby.Status != "open" {
		problem(w, 404, "open lobby not found")
		return
	}
	// A private lobby code is an invite capability; never expose its roster or credentials.
	reply(w, 200, map[string]any{"id": lobby.ID, "revision": lobby.Revision, "capacity": lobby.Capacity, "participantCount": len(lobby.Participants), "visibility": lobby.Visibility, "applicationProtocol": lobby.application})
}
func (server *Server) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "applicationProtocol" && key != "cursor" && key != "limit" {
			httpx.WriteInvalid(w)
			return
		}
	}
	application, cursor := query.Get("applicationProtocol"), query.Get("cursor")
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			httpx.WriteInvalid(w)
			return
		}
	}
	if !protocol.ValidID(application) || cursor != "" && !protocol.ValidID(cursor) || limit < 1 || limit > 50 {
		httpx.WriteInvalid(w)
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	var records []*lobby
	for _, lobby := range server.lobbies {
		if lobby.Visibility == "public" && lobby.Status == "open" && lobby.application == application && lobby.ID > cursor {
			records = append(records, lobby)
		}
	}
	slices.SortFunc(records, func(a, b *lobby) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	var next string
	if len(records) > limit {
		records = records[:limit]
		next = records[len(records)-1].ID
	}
	summaries := make([]map[string]any, 0, len(records))
	for _, lobby := range records {
		summaries = append(summaries, map[string]any{"id": lobby.ID, "revision": lobby.Revision, "ownerId": lobby.OwnerID, "capacity": lobby.Capacity, "participantCount": len(lobby.Participants)})
	}
	reply(w, 200, map[string]any{"lobbies": summaries, "nextCursor": next})
}
func (server *Server) ready(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Revision uint32 `json:"revision"`
		Ready    bool   `json:"ready"`
	}
	if !httpx.DecodeExact(w, r, &request, "revision", "ready") {
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	lobby := server.authorizedLobby(w, r)
	if lobby == nil {
		return
	}
	if !server.currentRevision(w, lobby, request.Revision, false) {
		return
	}
	if lobby.Ready[player(r)] != request.Ready {
		lobby.Ready[player(r)] = request.Ready
		lobby.Revision++
	}
	reply(w, 200, lobby)
}
func (server *Server) leave(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !httpx.DecodeExact(w, r, &request, "revision") {
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.expireLocked()
	lobby := server.authorizedLobby(w, r)
	if lobby == nil {
		return
	}
	if !server.currentRevision(w, lobby, request.Revision, false) {
		return
	}
	who := player(r)
	lobby.Participants = slices.DeleteFunc(lobby.Participants, func(id string) bool { return id == who })
	delete(server.byPlayer, who)
	if len(lobby.Participants) == 0 {
		delete(server.lobbies, lobby.ID)
		w.WriteHeader(204)
		return
	}
	if lobby.OwnerID == who {
		lobby.OwnerID = lobby.Participants[0]
	}
	resetReady(lobby)
	lobby.Revision++
	reply(w, 200, lobby)
}
