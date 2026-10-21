package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	handler := allocationHandler(relay, "operator-token", "test", "wss://relay.test/v2/relay")
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
