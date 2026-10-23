// Package relay implements the WSS relay.
package relay

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"golang.org/x/time/rate"
)

var (
	ErrInvalid  = errors.New("relayv2: invalid allocation or configuration")
	ErrCapacity = errors.New("relayv2: capacity exhausted")
	ErrClosed   = errors.New("relayv2: closed")
	ErrConflict = errors.New("relayv2: room already exists")
	ErrGone     = errors.New("relayv2: room authority ended")
)

const (
	maxAuthenticating = 128
	maxRooms          = 256
	maxParticipants   = 16
	maxConnections    = 1024
	resumeGrace       = 30 * time.Second
)

type Config struct {
	TrustedProxies  []string
	HandshakesPerIP int
	NodeID          string
	AllowedOrigins  []string
	Now             func() time.Time
	Traffic         TrafficPolicy
	MaxRooms        int
	MaxConnections  int
}

type Server struct {
	closeOnce                  sync.Once
	clients                    map[*client]struct{}
	trustedProxies             []netip.Prefix
	handshakeIPs               *httpx.IPBudget
	authAdmission              *httpx.Admission
	instanceID                 string
	draining                   bool
	reservationMu              sync.Mutex
	reservations               map[string]reservation
	mu                         sync.Mutex
	nodeID                     string
	now                        func() time.Time
	accept                     websocket.AcceptOptions
	rooms                      map[string]*room
	tickets                    map[[32]byte]credential
	resumes                    map[[32]byte]credential
	connections                int
	ctx                        context.Context
	cancel                     context.CancelFunc
	closed                     bool
	wg                         sync.WaitGroup
	traffic                    TrafficPolicy
	ingress, egress            *rate.Limiter
	metrics                    counters
	roomLimit, connectionLimit int
}

func New(config Config) (*Server, error) {
	if config.MaxRooms == 0 {
		config.MaxRooms = maxRooms
	}
	if config.MaxConnections == 0 {
		config.MaxConnections = maxConnections
	}
	if config.MaxRooms < 1 || config.MaxRooms > 4096 || config.MaxConnections < 1 || config.MaxConnections > 8192 {
		return nil, ErrInvalid
	}
	if config.HandshakesPerIP == 0 {
		config.HandshakesPerIP = 200
	}
	if config.HandshakesPerIP < 1 || config.HandshakesPerIP > 20000 {
		return nil, ErrInvalid
	}
	trusted, err := httpx.ParseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}
	config.Traffic = defaultTraffic(config.Traffic)
	if config.Traffic.NodeIngress < 16384 || config.Traffic.NodeEgress < 131072 || config.Traffic.RoomIngress < 16384 || config.Traffic.RoomEgress < 131072 {
		return nil, ErrInvalid
	}
	if !protocol.ValidID(config.NodeID) {
		return nil, ErrInvalid
	}
	for _, origin := range config.AllowedOrigins {
		if origin == "" || origin == "*" || strings.ContainsAny(origin, " \r\n\t") {
			return nil, ErrInvalid
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	instance, err := randomToken()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{clients: make(map[*client]struct{}), trustedProxies: trusted, handshakeIPs: httpx.NewIPBudget(rate.Limit(config.HandshakesPerIP), config.HandshakesPerIP*2, 16384, time.Now), authAdmission: httpx.NewAdmission(1000, 2000, maxAuthenticating, time.Now),
		instanceID: "ni-" + instance, reservations: make(map[string]reservation),
		nodeID:    config.NodeID,
		roomLimit: config.MaxRooms, connectionLimit: config.MaxConnections,
		traffic: config.Traffic, ingress: byteLimiter(config.Traffic.NodeIngress), egress: byteLimiter(config.Traffic.NodeEgress),
		now: config.Now,
		accept: websocket.AcceptOptions{
			Subprotocols:    []string{"relay.v2"},
			OriginPatterns:  slices.Clone(config.AllowedOrigins),
			CompressionMode: websocket.CompressionDisabled,
		},
		rooms:   make(map[string]*room),
		tickets: make(map[[32]byte]credential),
		resumes: make(map[[32]byte]credential),
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

func (server *Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v2/relay" || request.URL.RawQuery != "" || request.Method != http.MethodGet {
		http.NotFound(w, request)
		return
	}
	release, ok := server.authAdmission.Enter(w)
	if !ok {
		return
	}
	authenticating := true
	defer func() {
		if authenticating {
			release()
		}
	}()
	ip, err := httpx.ClientIP(request, server.trustedProxies)
	if err != nil {
		http.Error(w, "invalid client address", 400)
		return
	}
	if !server.handshakeIPs.Allow(ip) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "handshake rate exceeded", 429)
		return
	}
	server.mu.Lock()
	if server.closed || server.connections >= server.connectionLimit+maxAuthenticating {
		server.mu.Unlock()
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	server.connections++
	server.wg.Add(1)
	server.mu.Unlock()
	defer func() { server.mu.Lock(); server.connections--; server.mu.Unlock(); server.wg.Done() }()
	conn, err := websocket.Accept(w, request, &server.accept)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "relay.v2" {
		_ = conn.Close(websocket.StatusPolicyViolation, "relay.v2 required")
		return
	}
	conn.SetReadLimit(protocol.MaxClientMessageBytes)
	ctx, cancel := context.WithCancel(server.ctx)
	defer cancel()
	c := &client{server: server, conn: conn, ctx: ctx, cancel: cancel, states: make(map[string]*wire.PublishedState), notify: make(chan struct{}, 1)}
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return
	}
	server.clients[c] = struct{}{}
	server.mu.Unlock()
	defer func() { server.mu.Lock(); delete(server.clients, c); server.mu.Unlock() }()

	authContext, authCancel := context.WithTimeout(ctx, 5*time.Second)
	kind, data, err := conn.Read(authContext)
	authCancel()
	if err != nil {
		return
	}
	if kind != websocket.MessageBinary {
		_ = conn.Close(websocket.StatusUnsupportedData, "binary required")
		return
	}
	server.received(len(data))
	envelope, code := protocol.DecodeClient(data, false, 0)
	if code != 0 {
		c.writeError(code, clientErrorStatus(code, data))
		return
	}
	welcome, code := server.authenticate(c, envelope.GetConnect())
	if code != 0 {
		c.writeError(code, errorStatus(code))
		return
	}
	authenticating = false
	release()
	defer c.disconnect()
	if c.write(&wire.ServerEnvelope{Body: &wire.ServerEnvelope_Welcome{Welcome: welcome}}) != nil {
		return
	}
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); c.writeLoop(); c.cancel() }()
	c.readLoop()
	c.cancel()
	<-writerDone
}

func (server *Server) Close() error {
	server.closeOnce.Do(func() {
		server.mu.Lock()
		server.closed = true
		clients := make([]*client, 0, len(server.clients))
		for client := range server.clients {
			clients = append(clients, client)
		}
		server.mu.Unlock()
		for _, client := range clients {
			client.terminate(wire.ErrorCode_ERROR_CODE_ROOM_UNAVAILABLE, websocket.StatusServiceRestart, "server restarting")
		}
		done := make(chan struct{})
		go func() { server.wg.Wait(); close(done) }()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		server.cancel()
		for _, client := range clients {
			_ = client.conn.CloseNow()
		}
		<-done
	})
	return nil
}
