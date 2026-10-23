package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gyungsubLee/go-wss-relay/internal/control"
	"github.com/gyungsubLee/go-wss-relay/internal/coordinator"
	"github.com/gyungsubLee/go-wss-relay/internal/httpx"
	"github.com/gyungsubLee/go-wss-relay/internal/operatortoken"
	"github.com/gyungsubLee/go-wss-relay/internal/playerauth"
	relayserver "github.com/gyungsubLee/go-wss-relay/internal/relay"
)

// Run owns the configured listeners and relay until cancellation, failure, or drain.
func Run(ctx context.Context, args []string) error {
	config, err := parseConfig(args)
	if err != nil {
		return err
	}
	secret, err := operatortoken.ReadFile(config.tokenFile)
	if err != nil {
		return errors.New("invalid operator token file")
	}
	encoded := base64.RawURLEncoding.EncodeToString(secret[:])

	traffic, err := readTrafficPolicy(config.trafficFile)
	if err != nil {
		return err
	}

	var relay *relayserver.Server
	var pool *coordinator.Pool
	var allocator control.Allocator
	var api *control.Server
	var managementHandler http.Handler
	if config.role == "coordinator" {
		configs, err := coordinator.ReadConfig(config.nodesFile)
		if err != nil {
			return err
		}
		pool, err = coordinator.New(configs)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err = pool.Reconcile(ctx); err != nil {
			return err
		}
		allocator = pool
	} else {
		relay, err = relayserver.New(relayserver.Config{
			Traffic:         traffic,
			TrustedProxies:  config.proxies,
			HandshakesPerIP: config.handshakeRate,
			NodeID:          config.nodeID,
			AllowedOrigins:  config.origins,
			MaxRooms:        config.roomLimit,
			MaxConnections:  config.participantLimit,
		})
		if err != nil {
			return err
		}
		defer relay.Close()
		allocator = control.LocalAllocator{Relay: relay, NodeID: config.nodeID, WebSocketURL: config.publicURL}
		managementHandler = relay.ManagementHandler(encoded, config.publicURL)
	}
	if config.role != "relay" {
		auth, err := playerauth.New(playerauth.Config{OperatorSecret: secret, TokenTTL: playerauth.HardTokenTTL})
		if err != nil {
			return err
		}
		api, err = control.New(control.Config{TrustedProxies: config.proxies, Auth: auth, Allocator: allocator, AllowedOrigins: config.origins})
		if err != nil {
			return err
		}
		if pool != nil {
			if err = pool.Restore(api); err != nil {
				return err
			}
			managementHandler = pool.ManagementHandler(encoded, config.nodesFile, api)
		}
	}

	publicListener, err := net.Listen("tcp", config.listen)
	if err != nil {
		return fmt.Errorf("public listener: %w", err)
	}
	defer publicListener.Close()
	controlListener, err := net.Listen("tcp", config.management)
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
	public := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	control := httpx.NewServer(config.management, managementHandler)
	done := make(chan error, 2)
	go func() {
		if config.tlsCert != "" {
			done <- public.ServeTLS(publicListener, config.tlsCert, config.tlsKey)
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
