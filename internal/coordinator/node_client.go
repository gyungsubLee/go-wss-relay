package coordinator

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/operatortoken"
	"github.com/gyungsubLee/go-wss-relay/internal/protocol"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

type node struct {
	config    NodeConfig
	token     string
	http      *http.Client
	report    relay.NodeStatus
	seen      time.Time
	healthy   bool
	egressBps float64
	rooms     map[string]bool
}

// Register accepts explicit operator-owned configuration, never an untrusted node URL.
func (pool *Pool) Register(config NodeConfig) error {
	if !protocol.ValidID(config.ID) || !safeURL(config.ControlURL, false) || !safeURL(config.WebSocketURL, true) {
		return errors.New("invalid node identity or secure URL")
	}
	secret, err := operatortoken.ReadFile(config.TokenFile)
	if err != nil {
		return errors.New("invalid node token file")
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])

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
		return relay.ErrGone
	}
	if response.StatusCode == http.StatusServiceUnavailable {
		// Only the node's bounded structured refusal proves no reservation remains.
		var refusal struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
		if err == nil && len(body) <= 4096 && json.Unmarshal(body, &refusal) == nil && refusal.Error.Code == "allocation_failed" {
			return relay.ErrCapacity
		}
		return fmt.Errorf("node HTTP %d: reservation outcome unknown", response.StatusCode)
	}
	if response.StatusCode == 409 {
		return relay.ErrConflict
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

// Close releases idle management connections after the caller stops pool operations.
func (pool *Pool) Close() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, node := range pool.nodes {
		node.http.CloseIdleConnections()
	}
}
