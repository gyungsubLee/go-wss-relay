package relayv2

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/httpx"
)

type NodeAllocation struct {
	RemainingTTLMillis int64 `json:"remainingTtlMillis"`
	Allocation
	NodeID              string `json:"nodeId"`
	NodeInstanceID      string `json:"nodeInstanceId"`
	WebSocketURL        string `json:"websocketUrl"`
	ProtocolRevision    uint32 `json:"protocolRevision"`
	ApplicationProtocol string `json:"applicationProtocol"`
}

func (server *Server) ManagementHandler(token, publicURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		values := r.Header.Values("Authorization")
		if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+token)) != 1 {
			httpx.WriteError(w, 401, "unauthorized", "operator token required")
			return
		}
		if r.URL.RawQuery != "" {
			httpx.WriteInvalid(w)
			return
		}
		encode := func(status int, value any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(value)
		}
		if r.URL.Path == "/v2/status" || r.URL.Path == "/v2/node" {
			if r.Method != http.MethodGet {
				httpx.WriteMethodNotAllowed(w, "GET")
				return
			}
			if r.URL.Path == "/v2/status" {
				encode(200, server.Stats())
			} else {
				encode(200, server.Inventory())
			}
			return
		}
		if r.URL.Path == "/v2/drain" {
			if r.Method != http.MethodPost {
				httpx.WriteMethodNotAllowed(w, "POST")
				return
			}
			var request struct {
				Draining bool `json:"draining"`
			}
			if !httpx.DecodeExact(w, r, &request, "draining") {
				return
			}
			server.SetDraining(request.Draining)
			encode(200, server.Inventory())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v2/rooms/") {
			instance := strings.TrimPrefix(r.URL.Path, "/v2/rooms/")
			if r.Method == http.MethodDelete {
				if err := server.EndRoom(instance); err != nil {
					httpx.WriteError(w, 404, "not_found", "room not found")
					return
				}
				w.WriteHeader(204)
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(instance, "/tickets") {
				instance = strings.TrimSuffix(instance, "/tickets")
				var request struct {
					ParticipantID  string `json:"participantId"`
					NodeInstanceID string `json:"nodeInstanceId"`
				}
				if !httpx.DecodeExact(w, r, &request, "participantId", "nodeInstanceId") {
					return
				}
				if request.NodeInstanceID != server.instanceID {
					httpx.WriteError(w, 410, "instance_changed", "node restarted")
					return
				}
				ticket, err := server.RefreshTicket(instance, request.ParticipantID)
				if err != nil {
					httpx.WriteError(w, 410, "room_ended", "room authority expired")
					return
				}
				encode(200, ticket)
				return
			}
			httpx.WriteMethodNotAllowed(w, "POST, DELETE")
			return
		}
		if r.URL.Path != "/v2/rooms" && r.URL.Path != "/v2/reservations" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			httpx.WriteMethodNotAllowed(w, "POST")
			return
		}
		var request struct {
			RoomID              string   `json:"roomId"`
			ApplicationProtocol string   `json:"applicationProtocol"`
			Participants        []string `json:"participants"`
			TTLSeconds          int64    `json:"ttlSeconds"`
			RequestID           string   `json:"requestId"`
			NodeInstanceID      string   `json:"nodeInstanceId"`
		}
		keys := []string{"roomId", "applicationProtocol", "participants", "ttlSeconds"}
		if r.URL.Path == "/v2/reservations" {
			keys = append(keys, "requestId", "nodeInstanceId")
		}
		if !httpx.DecodeExact(w, r, &request, keys...) {
			return
		}
		if request.TTLSeconds <= 0 || request.TTLSeconds > 7200 {
			httpx.WriteInvalid(w)
			return
		}
		var allocation Allocation
		var err error
		if r.URL.Path == "/v2/reservations" {
			if request.NodeInstanceID != server.instanceID {
				httpx.WriteError(w, 410, "instance_changed", "node restarted")
				return
			}
			allocation, err = server.Reserve(request.RequestID, request.RoomID, request.ApplicationProtocol, request.Participants, time.Duration(request.TTLSeconds)*time.Second)
		} else {
			allocation, err = server.CreateRoom(request.RoomID, request.ApplicationProtocol, request.Participants, time.Duration(request.TTLSeconds)*time.Second)
		}
		if err != nil {
			status := 400
			if errors.Is(err, ErrConflict) {
				status = 409
			}
			if errors.Is(err, ErrGone) {
				status = 410
			}
			if errors.Is(err, ErrCapacity) || errors.Is(err, ErrClosed) {
				status = 503
			}
			httpx.WriteError(w, status, "allocation_failed", "room allocation failed")
			return
		}
		encode(201, NodeAllocation{RemainingTTLMillis: max(int64(0), allocation.ExpiresAt.Sub(server.now()).Milliseconds()), Allocation: allocation, NodeID: server.nodeID, NodeInstanceID: server.instanceID, WebSocketURL: publicURL, ProtocolRevision: 2, ApplicationProtocol: request.ApplicationProtocol})
	})
}
