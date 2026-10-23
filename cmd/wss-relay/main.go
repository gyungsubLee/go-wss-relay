// Command wss-relay runs the v2 transport slice with a loopback-only allocation API.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/controlv2"
	"github.com/gyungsubLee/go-wss-relay/internal/coordv2"
	"github.com/gyungsubLee/go-wss-relay/internal/httpapi/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/operatortoken"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
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
	role := flags.String("role", "standalone", "standalone, coordinator, or relay")
	nodesFile := flags.String("nodes-config", "", "coordinator node registry JSON")
	listen := flags.String("listen", "127.0.0.1:8090", "WSS upstream listener (TLS terminates at reverse proxy)")
	management := flags.String("management-listen", "127.0.0.1:8091", "loopback-only trusted allocation listener")
	publicURL := flags.String("public-url", "", "full client WebSocket URL")
	trafficFile := flags.String("traffic-config", "", "optional JSON byte budgets; zero fields use defaults")
	roomLimit := flags.Int("max-rooms", 512, "maximum reserved rooms")
	participantLimit := flags.Int("max-participants", 200, "maximum reserved participant slots and connections")
	tlsCert := flags.String("tls-cert", "", "optional TLS certificate PEM")
	tlsKey := flags.String("tls-key", "", "optional TLS private key PEM")
	nodeID := flags.String("node-id", "local", "relay node identifier")
	tokenFile := flags.String("operator-token-file", "", "32-byte base64url token file")
	var origins, proxies []string
	flags.Func("trusted-proxy", "explicit trusted proxy IP/CIDR (repeatable)", func(value string) error { proxies = append(proxies, value); return nil })
	handshakeRate := flags.Int("handshakes-per-ip", 200, "handshake admissions per second for one client IP")
	flags.Func("allowed-origin", "allowed browser origin host (repeatable)", func(value string) error { origins = append(origins, value); return nil })
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *role != "standalone" && *role != "coordinator" && *role != "relay" {
		return errors.New("invalid role")
	}
	if *role == "coordinator" && *nodesFile == "" {
		return errors.New("coordinator requires nodes-config")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("tls-cert and tls-key must be provided together")
	}
	if flags.NArg() != 0 || *tokenFile == "" {
		return errors.New("operator-token-file is required; positional arguments are not accepted")
	}
	host, _, err := net.SplitHostPort(*management)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("management listener must use a loopback IP address")
	}
	if *role != "coordinator" {
		u, err := url.Parse(*publicURL)
		if err != nil || u.User != nil || u.Hostname() == "" || u.Path != "/v2/relay" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("public-url must identify /v2/relay without credentials, query, or fragment")
		}
		if u.Scheme != "wss" && (u.Scheme != "ws" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback()) {
			return errors.New("public-url requires wss except for loopback development")
		}
	}

	secret, err := operatortoken.ReadFile(*tokenFile)
	if err != nil {
		return errors.New("invalid operator token file")
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret[:])

	traffic, err := readTrafficPolicy(*trafficFile)
	if err != nil {
		return err
	}

	var relay *relayv2.Server
	var pool *coordv2.Pool
	var allocator controlv2.Allocator
	var api *controlv2.Server
	var managementHandler http.Handler
	if *role == "coordinator" {
		configs, err := coordv2.ReadConfig(*nodesFile)
		if err != nil {
			return err
		}
		pool, err = coordv2.New(configs)
		if err != nil {
			return err
		}
		if err = pool.Reconcile(ctx); err != nil {
			return err
		}
		allocator = pool
	} else {
		relay, err = relayv2.New(relayv2.Config{Traffic: traffic, TrustedProxies: proxies, HandshakesPerIP: *handshakeRate, NodeID: *nodeID, AllowedOrigins: origins, MaxRooms: *roomLimit, MaxConnections: *participantLimit})
		if err != nil {
			return err
		}
		defer relay.Close()
		allocator = controlv2.LocalAllocator{Relay: relay, NodeID: *nodeID, WebSocketURL: *publicURL}
		managementHandler = relay.ManagementHandler(encoded, *publicURL)
	}
	if *role != "relay" {
		auth, err := playerauth.New(playerauth.Config{OperatorSecret: secret, TokenTTL: playerauth.HardTokenTTL, StableKey: true})
		if err != nil {
			return err
		}
		api, err = controlv2.New(controlv2.Config{TrustedProxies: proxies, Auth: auth, Allocator: allocator, AllowedOrigins: origins})
		if err != nil {
			return err
		}
		if pool != nil {
			if err = pool.Restore(api); err != nil {
				return err
			}
			managementHandler = pool.ManagementHandler(encoded, *nodesFile, api)
		}
	}

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
	if relay != nil {
		mux.Handle("/v2/relay", relay)
	}
	if api != nil {
		mux.Handle("/v2/api/", api)
	}
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if pool != nil && !pool.Ready() {
			http.Error(w, "nodes unavailable", 503)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	public := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	control := httpx.NewServer(*management, managementHandler)
	done := make(chan error, 2)
	go func() {
		if *tlsCert != "" {
			done <- public.ServeTLS(publicListener, *tlsCert, *tlsKey)
		} else {
			done <- public.Serve(publicListener)
		}
	}()
	go func() { done <- control.Serve(controlListener) }()
	log.Printf("relay.v2 listening at %s; management at %s", publicListener.Addr(), controlListener.Addr())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
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
			if relay != nil {
				relay.Expire()
				if api != nil {
					api.ReconcileLocal(relay.HasRoom)
				}
				if relay.Drained() {
					break selectLoop
				}
			}
			if api != nil {
				api.Expire()
			}
			ticks++
			if pool != nil && ticks%5 == 0 {
				if err := pool.Reconcile(ctx); err != nil {
					log.Printf("node reconciliation: %v", err)
				} else if err := pool.Restore(api); err != nil {
					log.Printf("node recovery: %v", err)
				}
			}
		}
	}
	_ = public.Close()
	_ = control.Close()
	if relay != nil {
		_ = relay.Close()
	}
	for completed < 2 {
		<-done
		completed++
	}
	if result != nil && !errors.Is(result, http.ErrServerClosed) {
		return result
	}
	return nil
}

func readTrafficPolicy(path string) (relayv2.TrafficPolicy, error) {
	var policy relayv2.TrafficPolicy
	if path == "" {
		return policy, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return policy, fmt.Errorf("traffic config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("traffic config: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return policy, errors.New("traffic config: expected one JSON object")
	}
	return policy, nil
}
