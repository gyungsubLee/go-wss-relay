package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"

	relayserver "github.com/gyungsubLee/go-wss-relay/internal/relay"
)

type config struct {
	role             string
	nodesFile        string
	listen           string
	management       string
	publicURL        string
	trafficFile      string
	tlsCert          string
	tlsKey           string
	nodeID           string
	tokenFile        string
	roomLimit        int
	participantLimit int
	handshakeRate    int
	origins          []string
	proxies          []string
}

func parseConfig(args []string) (config, error) {
	var config config
	flags := flag.NewFlagSet("wss-relay", flag.ContinueOnError)
	flags.StringVar(&config.role, "role", "standalone", "standalone, coordinator, or relay")
	flags.StringVar(&config.nodesFile, "nodes-config", "", "coordinator node registry JSON")
	flags.StringVar(&config.listen, "listen", "127.0.0.1:8090", "WSS upstream listener (TLS terminates at reverse proxy)")
	flags.StringVar(&config.management, "management-listen", "127.0.0.1:8091", "loopback-only trusted allocation listener")
	flags.StringVar(&config.publicURL, "public-url", "", "full client WebSocket URL")
	flags.StringVar(&config.trafficFile, "traffic-config", "", "optional JSON byte budgets; zero fields use defaults")
	flags.IntVar(&config.roomLimit, "max-rooms", 512, "maximum reserved rooms")
	flags.IntVar(&config.participantLimit, "max-participants", 200, "maximum reserved participant slots and connections")
	flags.StringVar(&config.tlsCert, "tls-cert", "", "optional TLS certificate PEM")
	flags.StringVar(&config.tlsKey, "tls-key", "", "optional TLS private key PEM")
	flags.StringVar(&config.nodeID, "node-id", "local", "relay node identifier")
	flags.StringVar(&config.tokenFile, "operator-token-file", "", "32-byte base64url token file")
	flags.Func("trusted-proxy", "explicit trusted proxy IP/CIDR (repeatable)", func(value string) error {
		config.proxies = append(config.proxies, value)
		return nil
	})
	flags.IntVar(&config.handshakeRate, "handshakes-per-ip", 200, "handshake admissions per second for one client IP")
	flags.Func("allowed-origin", "allowed browser origin host (repeatable)", func(value string) error {
		config.origins = append(config.origins, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return config, err
	}
	if config.role != "standalone" && config.role != "coordinator" && config.role != "relay" {
		return config, errors.New("invalid role")
	}
	if config.role == "coordinator" && config.nodesFile == "" {
		return config, errors.New("coordinator requires nodes-config")
	}
	if (config.tlsCert == "") != (config.tlsKey == "") {
		return config, errors.New("tls-cert and tls-key must be provided together")
	}
	if flags.NArg() != 0 || config.tokenFile == "" {
		return config, errors.New("operator-token-file is required; positional arguments are not accepted")
	}
	host, _, err := net.SplitHostPort(config.management)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return config, errors.New("management listener must use a loopback IP address")
	}
	if config.role != "coordinator" {
		u, err := url.Parse(config.publicURL)
		if err != nil || u.User != nil || u.Hostname() == "" || u.Path != "/v2/relay" || u.RawQuery != "" || u.Fragment != "" {
			return config, errors.New("public-url must identify /v2/relay without credentials, query, or fragment")
		}
		if u.Scheme != "wss" && (u.Scheme != "ws" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback()) {
			return config, errors.New("public-url requires wss except for loopback development")
		}
	}

	return config, nil
}

func readTrafficPolicy(path string) (relayserver.TrafficPolicy, error) {
	var policy relayserver.TrafficPolicy
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
