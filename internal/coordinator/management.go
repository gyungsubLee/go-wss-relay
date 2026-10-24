package coordinator

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/control"
	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

func (pool *Pool) Restore(api *control.Server) error {
	for id, instance := range pool.Ended() {
		api.EndMatch(id, instance)
	}
	for _, recovered := range pool.Recovered() {
		if err := api.Restore(recovered.Allocation, recovered.Participants); err != nil {
			return err
		}
	}
	return nil
}
func (pool *Pool) Drain(ctx context.Context, id string, draining bool) error {
	pool.mu.Lock()
	n := pool.nodes[id]
	pool.mu.Unlock()
	if n == nil {
		return relay.ErrInvalid
	}
	var status relay.NodeStatus
	if err := n.call(ctx, "POST", "/v2/drain", map[string]bool{"draining": draining}, &status); err != nil {
		return err
	}
	pool.mu.Lock()
	n.report.Draining = draining
	n.report.Accepting = !draining
	pool.mu.Unlock()
	return pool.Reconcile(ctx)
}
func (pool *Pool) status() []map[string]any {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	result := []map[string]any{}
	for _, n := range pool.nodes {
		result = append(result, map[string]any{"nodeId": n.config.ID, "nodeInstanceId": n.report.NodeInstanceID, "healthy": n.healthy && pool.now().Sub(n.seen) <= 15*time.Second,
			"draining": n.report.Draining, "goCpuReady": n.report.Stats.GoCPUReady, "goBusyFractionEstimate": n.report.Stats.GoBusyFractionEstimate, "rooms": len(n.report.Rooms), "connections": n.report.Stats.Connections, "reservedParticipants": n.report.Stats.ReservedParticipants, "egressBytesPerSecond": n.egressBps})
	}
	return result
}
func (pool *Pool) ManagementHandler(token, configPath string, api *control.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authorization := r.Header.Values("Authorization")
		if len(authorization) != 1 || subtle.ConstantTimeCompare([]byte(authorization[0]), []byte("Bearer "+token)) != 1 {
			httpx.WriteError(w, 401, "unauthorized", "operator token required")
			return
		}
		if r.URL.RawQuery != "" {
			httpx.WriteInvalid(w)
			return
		}
		var err error
		switch {
		case r.URL.Path == "/v2/nodes" && r.Method == http.MethodGet:
		case r.URL.Path == "/v2/nodes/reload" && r.Method == http.MethodPost:
			var request struct{}
			if !httpx.DecodeExact(w, r, &request) {
				return
			}
			var configs []NodeConfig
			configs, err = ReadConfig(configPath)
			if err == nil {
				for _, config := range configs {
					if err = pool.Register(config); err != nil {
						break
					}
				}
			}
			if err == nil {
				err = pool.Reconcile(r.Context())
			}
			if err == nil {
				err = pool.Restore(api)
			}
		case strings.HasPrefix(r.URL.Path, "/v2/nodes/") && strings.HasSuffix(r.URL.Path, "/drain") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/nodes/"), "/drain")
			var request struct {
				Draining bool `json:"draining"`
			}
			if !httpx.DecodeExact(w, r, &request, "draining") {
				return
			}
			err = pool.Drain(r.Context(), id, request.Draining)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			httpx.WriteError(w, 503, "node_control_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pool.status())
	})
}
