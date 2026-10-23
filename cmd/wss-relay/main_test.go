package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/operatortoken"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	"github.com/gyungsubLee/go-wss-relay/internal/relay"
)

func TestAllocationRequiresOperatorAndRejectsDuplicateRoom(t *testing.T) {
	relay, err := relay.New(relay.Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	handler := relay.ManagementHandler("operator-token", "wss://relay.test/v2/relay")
	body := `{"roomId":"demo","applicationProtocol":"snake.v1","participants":["alice","bob"],"ttlSeconds":60}`
	allocate := func(authorized bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v2/rooms", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if authorized {
			request.Header.Set("Authorization", "Bearer operator-token")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if got := allocate(false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %d", got)
	}
	response := allocate(true)
	if response.Code != http.StatusCreated {
		t.Fatalf("allocation: %d %s", response.Code, response.Body)
	}
	var result struct {
		RoomInstanceID string            `json:"roomInstanceId"`
		WebSocketURL   string            `json:"websocketUrl"`
		Tickets        map[string]string `json:"tickets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RoomInstanceID == "" || result.WebSocketURL != "wss://relay.test/v2/relay" || len(result.Tickets) != 2 || result.Tickets["alice"] == result.Tickets["bob"] {
		t.Fatalf("invalid private allocation response")
	}
	if got := allocate(true).Code; got != http.StatusConflict {
		t.Fatalf("duplicate: %d", got)
	}
}

func TestTrafficPolicyFile(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"defaults", `{}`, true},
		{"explicit", `{"nodeIngressBytesPerSecond":1048576,"nodeEgressBytesPerSecond":8388608,"roomIngressBytesPerSecond":131072,"roomEgressBytesPerSecond":1048576}`, true},
		{"unknown", `{"nodeEgress":12}`, false},
		{"trailing", `{} {}`, false},
		{"invalid", `{"nodeIngressBytesPerSecond":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "traffic.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			policy, err := readTrafficPolicy(path)
			if err == nil {
				var server *relay.Server
				server, err = relay.New(relay.Config{NodeID: "test", Traffic: policy})
				if server != nil {
					server.Close()
				}
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

// Missing permission and symlink checks must fail before either listener starts.
func TestRunRejectsUnsafeOperatorTokenFiles(t *testing.T) {
	secret := [32]byte{1}
	encoded := base64.RawURLEncoding.EncodeToString(secret[:])
	for _, name := range []string{"mode 0644", "symlink", "trailing space", "oversized whitespace"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.token")
			body := encoded
			if name == "trailing space" {
				body += " "
			}
			if name == "oversized whitespace" {
				body += strings.Repeat("\n", 1<<20)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "mode 0644" {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if name == "symlink" {
				link := filepath.Join(t.TempDir(), "token-link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := run(ctx, []string{"--operator-token-file", path, "--public-url", "ws://127.0.0.1/v2/relay", "--listen", "127.0.0.1:0", "--management-listen", "127.0.0.1:0"})
			if err == nil {
				t.Fatal("unsafe operator token file was accepted")
			}
			if strings.Contains(err.Error(), encoded) {
				t.Fatal("startup error disclosed the operator token")
			}
		})
	}
}

func TestRunAcceptsCanonicalOperatorTokenFiles(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("suffix %q", suffix), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.token")
			if err := os.WriteFile(path, []byte("QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE"+suffix), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := run(ctx, []string{"--operator-token-file", path, "--public-url", "ws://127.0.0.1/v2/relay", "--listen", "127.0.0.1:0", "--management-listen", "127.0.0.1:0"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOperatorTokenPreservesStablePlayerToken(t *testing.T) {
	// Frozen from the existing v2 issuer: 32 ASCII A bytes, player-a, 15-minute TTL.
	const token = "ARjhAFbsMSgACHBsYXllci1hAuqVORlY-LBk1dfW1xzFYkoVqiefSstP4X5U-1j8F3w"
	path := filepath.Join(t.TempDir(), "operator.token")
	if err := os.WriteFile(path, []byte("QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := operatortoken.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	config := playerauth.Config{OperatorSecret: secret, StableKey: true, TokenTTL: playerauth.HardTokenTTL, Now: func() time.Time { return now }}
	issuer, err := playerauth.New(config)
	if err != nil {
		t.Fatal(err)
	}
	issued, claims, err := issuer.Issue("player-a")
	if err != nil || issued != token || claims.PlayerID != "player-a" || !claims.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("existing v2 token contract changed: %v", err)
	}
	restarted, err := playerauth.New(config)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := restarted.Verify(token)
	if err != nil || verified != claims {
		t.Fatalf("restart rejected existing v2 player token: %v", err)
	}
	now = claims.ExpiresAt
	if _, err := restarted.Verify(token); !errors.Is(err, playerauth.ErrExpired) {
		t.Fatalf("token remained valid at expiry: %v", err)
	}
}
