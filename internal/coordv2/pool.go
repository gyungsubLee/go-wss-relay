// Package coordv2 owns single-writer room placement and node reconciliation.
package coordv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/controlv2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/operatorapi"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relayv2"
)

type NodeConfig struct {
	ID           string `json:"id"`
	ControlURL   string `json:"controlUrl"`
	WebSocketURL string `json:"websocketUrl"`
	TokenFile    string `json:"tokenFile"`
	CAFile       string `json:"caFile,omitempty"`
}

type node struct {
	config    NodeConfig
	token     string
	http      *http.Client
	report    relayv2.NodeStatus
	seen      time.Time
	healthy   bool
	egressBps float64
	rooms     map[string]bool
}

type ownedRoom struct {
	operation    sync.Mutex
	nodeID       string
	instanceID   string
	fingerprint  [32]byte
	participants []string
	application  string
	allocation   *controlv2.Allocation
	confirmed    time.Time
	lastAttempt  time.Time
	ended        bool
	expires      time.Time
}

type Pool struct {
	conflicted  bool
	reconcileMu sync.Mutex
	mu          sync.Mutex
	nodes       map[string]*node
	owners      map[string]*ownedRoom
	now         func() time.Time
}

type Recovered struct {
	Allocation   controlv2.Allocation
	Participants []string
}

func ReadConfig(path string) ([]NodeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var config struct {
		Nodes []NodeConfig `json:"nodes"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("invalid trailing node configuration")
	}
	if len(config.Nodes) == 0 || len(config.Nodes) > 64 {
		return nil, errors.New("configure 1..64 nodes")
	}
	return config.Nodes, nil
}
func New(configs []NodeConfig) (*Pool, error) {
	pool := &Pool{nodes: make(map[string]*node), owners: make(map[string]*ownedRoom), now: time.Now}
	for _, config := range configs {
		if err := pool.Register(config); err != nil {
			return nil, err
		}
	}
	return pool, nil
}

// Register accepts explicit operator-owned configuration, never an untrusted node URL.
func (pool *Pool) Register(config NodeConfig) error {
	if !protocol.ValidID(config.ID) || !safeURL(config.ControlURL, false) || !safeURL(config.WebSocketURL, true) {
		return errors.New("invalid node identity or secure URL")
	}
	data, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return errors.New("cannot read node token file")
	}
	token := strings.TrimSpace(string(data))
	if _, err := operatorapi.ParseOperatorToken(token); err != nil {
		return errors.New("invalid node token file")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if config.CAFile != "" {
		pem, err := os.ReadFile(config.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid node CA file")
		}
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("node redirects are forbidden") }}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if old := pool.nodes[config.ID]; old != nil {
		if old.config != config || old.token != token {
			return errors.New("node configuration changes require a restart; register a new node ID")
		}
		return nil
	}
	if len(pool.nodes) >= 64 {
		return errors.New("node registry full")
	}
	pool.nodes[config.ID] = &node{config: config, token: token, http: client, rooms: make(map[string]bool)}
	return nil
}
func safeURL(raw string, websocket bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()
	if websocket {
		return (u.Scheme == "wss" || loopback && u.Scheme == "ws") && u.Path == "/v2/relay"
	}
	return (u.Scheme == "https" || loopback && u.Scheme == "http") && (u.Path == "" || u.Path == "/")
}
func (n *node) call(ctx context.Context, method, path string, body, target any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(n.config.ControlURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+n.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := n.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == 410 {
		return relayv2.ErrGone
	}
	if response.StatusCode == 503 {
		return relayv2.ErrCapacity
	}
	if response.StatusCode == 409 {
		return relayv2.ErrConflict
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("node HTTP %d", response.StatusCode)
	}
	if target == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(target)
}
func fingerprint(id, application string, participants []string) [32]byte {
	sorted := slices.Clone(participants)
	slices.Sort(sorted)
	data, _ := json.Marshal(struct {
		ID, Application string
		Participants    []string
	}{id, application, sorted})
	return sha256.Sum256(data)
}

// Reconcile is a pull heartbeat. Unknown nodes remain excluded until inventory is recovered.
func (pool *Pool) Reconcile(ctx context.Context) error {
	pool.reconcileMu.Lock()
	defer pool.reconcileMu.Unlock()
	pool.mu.Lock()
	for id, owner := range pool.owners {
		if !owner.expires.IsZero() && !pool.now().Before(owner.expires) {
			delete(pool.owners, id)
		}
	}
	nodes := make([]*node, 0, len(pool.nodes))
	for _, n := range pool.nodes {
		nodes = append(nodes, n)
	}
	pool.mu.Unlock()
	var work sync.WaitGroup
	var resultMu sync.Mutex
	var result error
	for _, n := range nodes {
		work.Add(1)
		go func() {
			defer work.Done()
			started := pool.now()
			var report relayv2.NodeStatus
			err := n.call(ctx, "GET", "/v2/node", nil, &report)
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if err != nil || report.NodeID != n.config.ID || report.NodeInstanceID == "" || report.ProtocolRevision != 2 {
				n.healthy = false
				return
			}
			previous := n.report
			elapsed := pool.now().Sub(n.seen).Seconds()
			if elapsed > 0 && previous.NodeInstanceID == report.NodeInstanceID && report.Stats.EgressBytes >= previous.Stats.EgressBytes {
				n.egressBps = float64(report.Stats.EgressBytes-previous.Stats.EgressBytes) / elapsed
			} else {
				n.egressBps = 0
			}
			n.report = report
			n.seen = pool.now()
			n.healthy = true
			n.rooms = make(map[string]bool)
			for _, room := range report.Rooms {
				if !protocol.ValidID(room.RoomID) || !protocol.ValidID(room.RoomInstanceID) || len(room.Participants) > 16 || room.RemainingTTLMillis <= 0 {
					continue
				}
				n.rooms[room.RoomID] = true
				owner := pool.owners[room.RoomID]
				if owner != nil && owner.nodeID != n.config.ID && !owner.ended {
					n.healthy = false
					pool.conflicted = true
					if other := pool.nodes[owner.nodeID]; other != nil {
						other.healthy = false
					}
					resultMu.Lock()
					result = errors.New("conflicting room ownership; node placement disabled")
					resultMu.Unlock()
					continue
				}
				if owner == nil {
					ordered := slices.Clone(room.Participants)
					if index := slices.Index(ordered, room.OwnerID); index > 0 {
						ordered[0], ordered[index] = ordered[index], ordered[0]
					}
					allocation := controlv2.Allocation{RoomID: room.RoomID, RoomInstanceID: room.RoomInstanceID, NodeID: n.config.ID, NodeInstanceID: report.NodeInstanceID, WebSocketURL: n.config.WebSocketURL, ApplicationProtocol: room.ApplicationProtocol,
						ExpiresAt: started.Add(time.Duration(room.RemainingTTLMillis) * time.Millisecond), Tickets: make(map[string]string)}
					for _, participant := range room.Participants {
						allocation.Tickets[participant] = ""
					}
					pool.owners[room.RoomID] = &ownedRoom{nodeID: n.config.ID, instanceID: report.NodeInstanceID, fingerprint: fingerprint(room.RoomID, room.ApplicationProtocol, room.Participants), participants: ordered, application: room.ApplicationProtocol, allocation: &allocation, confirmed: pool.now(), expires: allocation.ExpiresAt.Add(2 * time.Minute)}
				}
			}
			for id, owner := range pool.owners {
				if owner.nodeID != n.config.ID {
					continue
				}
				if owner.confirmed.IsZero() && !owner.lastAttempt.IsZero() && started.Sub(owner.lastAttempt) > 30*time.Second && !n.rooms[id] {
					owner.ended = true
				}
				if owner.instanceID != report.NodeInstanceID || !owner.confirmed.IsZero() && owner.confirmed.Before(started) && !n.rooms[id] {
					owner.ended = true
				}
			}
		}()
	}
	work.Wait()
	pool.mu.Lock()
	pool.conflicted = result != nil
	pool.mu.Unlock()
	return result
}

func (pool *Pool) Allocate(ctx context.Context, id, application string, participants []string) (controlv2.Allocation, error) {
	sum := fingerprint(id, application, participants)
	pool.mu.Lock()
	if pool.conflicted {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrConflict
	}
	owner := pool.owners[id]
	if owner != nil && owner.fingerprint != sum {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrConflict
	}
	if owner != nil && owner.ended {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrGone
	}
	if owner != nil && owner.allocation != nil {
		result := cloneAllocation(*owner.allocation)
		pool.mu.Unlock()
		return result, nil
	}
	if owner == nil {
		var selected *node
		best := 2.0
		for _, candidate := range pool.nodes {
			report := candidate.report
			if !candidate.healthy || pool.now().Sub(candidate.seen) > 15*time.Second || !report.Accepting || report.Draining || report.MaxParticipants <= 0 || report.Stats.QueueP95UpperMs > 100 || report.Stats.SystemBytes > 512<<20 || report.Stats.GoCPUReady && report.Stats.GoBusyFractionEstimate >= .7 {
				continue
			}
			reserved := report.Stats.ReservedParticipants
			roomCount := len(report.Rooms)
			for roomID, pending := range pool.owners {
				if pending.nodeID == candidate.config.ID && !pending.ended && !candidate.rooms[roomID] {
					reserved += len(pending.participants)
					roomCount++
				}
			}
			limit := report.MaxParticipants
			if !report.Stats.GoCPUReady {
				limit = min(limit, 16)
			}
			if reserved+len(participants) > limit || roomCount >= report.MaxRooms {
				continue
			}
			byteLoad := candidate.egressBps / float64(max(1, report.Stats.Traffic.NodeEgress))
			if byteLoad >= .8 {
				continue
			}
			score := max(float64(reserved)/float64(report.MaxParticipants), byteLoad)
			if score < best || score == best && selected != nil && candidate.config.ID < selected.config.ID {
				selected = candidate
				best = score
			}
		}
		if selected == nil {
			pool.mu.Unlock()
			return controlv2.Allocation{}, relayv2.ErrCapacity
		}
		owner = &ownedRoom{nodeID: selected.config.ID, instanceID: selected.report.NodeInstanceID, fingerprint: sum, participants: slices.Clone(participants), application: application, expires: pool.now().Add(32 * time.Minute)}
		pool.owners[id] = owner
	}
	pool.mu.Unlock()
	return pool.reserve(ctx, owner, id, application, participants)
}

func (pool *Pool) reserve(ctx context.Context, owner *ownedRoom, id, application string, participants []string) (controlv2.Allocation, error) {
	owner.operation.Lock()
	defer owner.operation.Unlock()
	pool.mu.Lock()
	if pool.owners[id] != owner {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrCapacity
	}
	if owner.ended {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrGone
	}
	if owner.allocation != nil {
		result := cloneAllocation(*owner.allocation)
		pool.mu.Unlock()
		return result, nil
	}

	n := pool.nodes[owner.nodeID]
	if n == nil || n.report.NodeInstanceID != owner.instanceID {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrGone
	}
	if !n.healthy {
		pool.mu.Unlock()
		return controlv2.Allocation{}, relayv2.ErrCapacity
	}
	expectedInstance := owner.instanceID
	pool.mu.Unlock()
	request := map[string]any{"requestId": id, "roomId": id, "applicationProtocol": application, "participants": participants, "ttlSeconds": 1800, "nodeInstanceId": expectedInstance}
	started := pool.now()
	var response relayv2.NodeAllocation
	err := n.call(ctx, "POST", "/v2/reservations", request, &response)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if owner.ended || n.report.NodeInstanceID != expectedInstance {
		return controlv2.Allocation{}, relayv2.ErrGone
	}
	if err != nil {
		owner.lastAttempt = pool.now()
		if errors.Is(err, relayv2.ErrGone) {
			owner.ended = true
		}
		if errors.Is(err, relayv2.ErrCapacity) && pool.owners[id] == owner {
			delete(pool.owners, id)
		}
		// Ambiguous RPC failures keep the original owner. They never trigger placement elsewhere.
		return controlv2.Allocation{}, err
	}
	if response.RoomID != id || response.NodeID != n.config.ID || response.NodeInstanceID != expectedInstance || response.WebSocketURL != n.config.WebSocketURL || response.ApplicationProtocol != application || !protocol.ValidID(response.RoomInstanceID) || response.RemainingTTLMillis <= 0 || len(response.Tickets) != len(participants) {
		return controlv2.Allocation{}, errors.New("invalid allocation response")
	}
	for _, participant := range participants {
		if response.Tickets[participant] == "" {
			return controlv2.Allocation{}, errors.New("incomplete node grants")
		}
	}
	result := controlv2.Allocation{RoomID: id, RoomInstanceID: response.RoomInstanceID, NodeID: n.config.ID, NodeInstanceID: expectedInstance, WebSocketURL: response.WebSocketURL, ApplicationProtocol: application, Tickets: response.Tickets, TicketExpiresAt: response.TicketExpiresAt,
		ExpiresAt: started.Add(time.Duration(response.RemainingTTLMillis) * time.Millisecond)}
	owner.allocation = &result
	owner.confirmed = pool.now()
	owner.expires = result.ExpiresAt.Add(2 * time.Minute)
	return cloneAllocation(result), nil
}
func cloneAllocation(a controlv2.Allocation) controlv2.Allocation {
	copy := make(map[string]string, len(a.Tickets))
	for id, ticket := range a.Tickets {
		copy[id] = ticket
	}
	a.Tickets = copy
	return a
}

func (pool *Pool) Refresh(ctx context.Context, allocation controlv2.Allocation, participant string) (relayv2.Ticket, error) {
	pool.mu.Lock()
	owner := pool.owners[allocation.RoomID]
	if owner == nil || owner.ended || owner.instanceID != allocation.NodeInstanceID || !slices.Contains(owner.participants, participant) {
		pool.mu.Unlock()
		return relayv2.Ticket{}, relayv2.ErrGone
	}
	n := pool.nodes[owner.nodeID]
	pool.mu.Unlock()
	var ticket relayv2.Ticket
	err := n.call(ctx, "POST", "/v2/rooms/"+allocation.RoomInstanceID+"/tickets", map[string]string{"participantId": participant, "nodeInstanceId": allocation.NodeInstanceID}, &ticket)
	return ticket, err
}
func (pool *Pool) Recovered() []Recovered {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	var result []Recovered
	for _, owner := range pool.owners {
		if !owner.ended && owner.allocation != nil {
			result = append(result, Recovered{Allocation: cloneAllocation(*owner.allocation), Participants: slices.Clone(owner.participants)})
		}
	}
	return result
}
func (pool *Pool) Ready() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.conflicted {
		return false
	}
	for _, n := range pool.nodes {
		if n.healthy && pool.now().Sub(n.seen) <= 15*time.Second && n.report.Accepting && !n.report.Draining {
			return true
		}
	}
	return false
}

func (pool *Pool) Ended() map[string]string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	result := make(map[string]string)
	for id, owner := range pool.owners {
		if owner.ended {
			instance := ""
			if owner.allocation != nil {
				instance = owner.allocation.RoomInstanceID
			}
			result[id] = instance
		}
	}
	return result
}
