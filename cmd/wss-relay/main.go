// Command wss-relay runs the v2 transport slice with a loopback-only allocation API.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/operatorapi"
	"github.com/gyungsubLee/go-wss-relay/internal/relayv2"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("wss-relay", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:8090", "WSS upstream listener (TLS terminates at reverse proxy)")
	management := flags.String("management-listen", "127.0.0.1:8091", "loopback-only trusted allocation listener")
	publicURL := flags.String("public-url", "", "full client WebSocket URL")
	nodeID := flags.String("node-id", "local", "relay node identifier")
	tokenFile := flags.String("operator-token-file", "", "32-byte base64url token file")
	var origins []string
	flags.Func("allowed-origin", "allowed browser origin host (repeatable)", func(value string) error { origins = append(origins, value); return nil })
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *tokenFile == "" {
		return errors.New("operator-token-file is required; positional arguments are not accepted")
	}
	host, _, err := net.SplitHostPort(*management)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("management listener must use a loopback IP address")
	}
	u, err := url.Parse(*publicURL)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Path != "/v2/relay" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("public-url must identify /v2/relay without credentials, query, or fragment")
	}
	if u.Scheme != "wss" && (u.Scheme != "ws" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback()) {
		return errors.New("public-url requires wss except for loopback development")
	}
	data, err := os.ReadFile(*tokenFile)
	if err != nil {
		return errors.New("cannot read operator token file")
	}
	encoded := strings.TrimSpace(string(data))
	if _, err := operatorapi.ParseOperatorToken(encoded); err != nil {
		return errors.New("invalid operator token file")
	}
	relay, err := relayv2.New(relayv2.Config{NodeID: *nodeID, AllowedOrigins: origins})
	if err != nil {
		return err
	}
	defer relay.Close()
	publicListener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	defer publicListener.Close()
	controlListener, err := net.Listen("tcp", *management)
	if err != nil {
		return fmt.Errorf("management listener: %w", err)
	}
	defer controlListener.Close()
	mux := http.NewServeMux()
	mux.Handle("/v2/relay", relay)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	public := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, MaxHeaderBytes: 16 << 10}
	control := httpx.NewServer(*management, allocationHandler(relay, encoded, *nodeID, *publicURL))
	done := make(chan error, 2)
	go func() { done <- public.Serve(publicListener) }()
	go func() { done <- control.Serve(controlListener) }()
	log.Printf("relay.v2 listening at %s; management at %s", publicListener.Addr(), controlListener.Addr())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var result error
	completed := 0
selectLoop:
	for {
		select {
		case <-ctx.Done():
			break selectLoop
		case result = <-done:
			completed++
			break selectLoop
		case <-ticker.C:
			relay.Expire()
		}
	}
	_ = public.Close()
	_ = control.Close()
	_ = relay.Close()
	for completed < 2 {
		<-done
		completed++
	}
	if result != nil && !errors.Is(result, http.ErrServerClosed) {
		return result
	}
	return nil
}

func allocationHandler(relay *relayv2.Server, token, nodeID, publicURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/v2/rooms" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte("Bearer "+token)) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "valid operator token required")
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
		}
		if !httpx.DecodeExact(w, r, &request, "roomId", "applicationProtocol", "participants", "ttlSeconds") {
			return
		}
		if request.TTLSeconds <= 0 || request.TTLSeconds > 7200 {
			httpx.WriteInvalid(w)
			return
		}
		allocation, err := relay.CreateRoom(request.RoomID, request.ApplicationProtocol, request.Participants, time.Duration(request.TTLSeconds)*time.Second)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, relayv2.ErrConflict) {
				status = http.StatusConflict
			}
			if errors.Is(err, relayv2.ErrCapacity) || errors.Is(err, relayv2.ErrClosed) {
				status = http.StatusServiceUnavailable
			}
			httpx.WriteError(w, status, "allocation_failed", "room allocation failed")
			return
		}
		// The trusted backend receives all grants; it must return only the caller's ticket.
		response := struct {
			RoomID              string            `json:"roomId"`
			RoomInstanceID      string            `json:"roomInstanceId"`
			NodeID              string            `json:"nodeId"`
			WebSocketURL        string            `json:"websocketUrl"`
			ProtocolRevision    uint32            `json:"protocolRevision"`
			ApplicationProtocol string            `json:"applicationProtocol"`
			ExpiresAt           string            `json:"expiresAt"`
			Tickets             map[string]string `json:"tickets"`
		}{allocation.RoomID, allocation.RoomInstanceID, nodeID, publicURL, 2, request.ApplicationProtocol, allocation.ExpiresAt.UTC().Format(time.RFC3339Nano), allocation.Tickets}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response)
	})
}
