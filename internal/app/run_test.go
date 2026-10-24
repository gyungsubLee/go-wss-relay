package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/coordinator"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

func tokenFile(t *testing.T) (string, string) {
	t.Helper()
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	path := filepath.Join(t.TempDir(), "operator.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, token
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func runArgs(t *testing.T, role string) ([]string, string, string, string) {
	t.Helper()
	path, token := tokenFile(t)
	public, management := freeAddress(t), freeAddress(t)
	args := []string{"--role", role, "--listen", public, "--management-listen", management, "--operator-token-file", path, "--public-url", "ws://" + public + "/v2/relay"}
	if role == "coordinator" {
		node, err := relay.New(relay.Config{NodeID: "node"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { node.Close() })
		server := httptest.NewServer(node.ManagementHandler(token, "ws://127.0.0.1/v2/relay"))
		t.Cleanup(server.Close)
		registry := filepath.Join(t.TempDir(), "nodes.json")
		body, err := json.Marshal(map[string]any{"nodes": []coordinator.NodeConfig{{ID: "node", ControlURL: server.URL, WebSocketURL: "ws://127.0.0.1/v2/relay", TokenFile: path}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry, body, 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--nodes-config", registry)
	}
	return args, public, management, token
}

func startRun(t *testing.T, args []string, public string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, args) }()
	t.Cleanup(cancel)
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("Run exited before startup: %v", err)
		default:
		}
		response, err := client.Get("http://" + public + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return cancel, done
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func awaitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func assertReleased(t *testing.T, addresses ...string) {
	t.Helper()
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("listener %s leaked: %v", address, err)
		}
		listener.Close()
	}
}

func TestRunRolesStartAndCancel(t *testing.T) {
	for _, role := range []string{"standalone", "coordinator", "relay"} {
		t.Run(role, func(t *testing.T) {
			args, public, management, _ := runArgs(t, role)
			cancel, done := startRun(t, args, public)
			cancel()
			awaitRun(t, done)
			assertReleased(t, public, management)
		})
	}
}

func TestRunManagementListenerFailureReleasesPublicListener(t *testing.T) {
	for _, role := range []string{"standalone", "coordinator", "relay"} {
		t.Run(role, func(t *testing.T) {
			args, public, management, _ := runArgs(t, role)
			blocker, err := net.Listen("tcp", management)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			err = Run(t.Context(), args)
			if err == nil || !strings.Contains(err.Error(), "management listener:") {
				t.Fatalf("unexpected startup result: %v", err)
			}
			assertReleased(t, public)
		})
	}
}

func TestRunServeFailureReleasesListeners(t *testing.T) {
	args, public, management, _ := runArgs(t, "standalone")
	args = append(args, "--tls-cert", filepath.Join(t.TempDir(), "missing.pem"), "--tls-key", filepath.Join(t.TempDir(), "missing.key"))
	if err := Run(t.Context(), args); err == nil {
		t.Fatal("missing TLS files did not fail ServeTLS")
	}
	assertReleased(t, public, management)
}

func TestRunEmptyDrainedRelayStops(t *testing.T) {
	for _, role := range []string{"standalone", "relay"} {
		t.Run(role, func(t *testing.T) {
			args, public, management, token := runArgs(t, role)
			_, done := startRun(t, args, public)
			request, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+management+"/v2/drain", strings.NewReader(`{"draining":true}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("drain status: %d", response.StatusCode)
			}
			awaitRun(t, done)
			assertReleased(t, public, management)
		})
	}
}

func TestRunCoordinatorClosesNodeConnections(t *testing.T) {
	for _, mode := range []string{"cancel", "public listener", "management listener", "control config"} {
		t.Run(mode, func(t *testing.T) {
			path, token := tokenFile(t)
			node, err := relay.New(relay.Config{NodeID: "node"})
			if err != nil {
				t.Fatal(err)
			}
			defer node.Close()
			closed := make(chan struct{}, 8)
			server := httptest.NewUnstartedServer(node.ManagementHandler(token, "ws://127.0.0.1/v2/relay"))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					closed <- struct{}{}
				}
			}
			server.Start()
			defer server.Close()
			registry := filepath.Join(t.TempDir(), "nodes.json")
			body, err := json.Marshal(map[string]any{"nodes": []coordinator.NodeConfig{{ID: "node", ControlURL: server.URL, WebSocketURL: "ws://127.0.0.1/v2/relay", TokenFile: path}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(registry, body, 0o600); err != nil {
				t.Fatal(err)
			}
			public, management := freeAddress(t), freeAddress(t)
			args := []string{"--role", "coordinator", "--listen", public, "--management-listen", management, "--nodes-config", registry, "--operator-token-file", path}
			if mode == "cancel" {
				cancel, done := startRun(t, args, public)
				cancel()
				awaitRun(t, done)
			} else {
				if mode == "control config" {
					args = append(args, "--trusted-proxy", "invalid")
				} else {
					address := public
					if mode == "management listener" {
						address = management
					}
					blocker, err := net.Listen("tcp", address)
					if err != nil {
						t.Fatal(err)
					}
					defer blocker.Close()
				}
				if err := Run(t.Context(), args); err == nil {
					t.Fatal("startup unexpectedly succeeded")
				}
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("coordinator left its node connection open after Run returned")
			}
		})
	}
}
