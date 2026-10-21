package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gyungsubLee/go-wss-relay/internal/relayv2"
)

func TestAllocationRequiresOperatorAndRejectsDuplicateRoom(t *testing.T) {
	relay, err := relayv2.New(relayv2.Config{NodeID: "test"})
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
				var server *relayv2.Server
				server, err = relayv2.New(relayv2.Config{NodeID: "test", Traffic: policy})
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
