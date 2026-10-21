// Command relay-load measures a separate live WSS node; it never disables TLS verification.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	wire "github.com/gyungsubLee/go-wss-relay/gen/go/relay/v2"
	"github.com/gyungsubLee/go-wss-relay/internal/relayv2"
	"google.golang.org/protobuf/proto"
)

type config struct {
	management, tokenFile, caFile, output                                   string
	participants, roomSize, hz, payload, seconds, warmup, slowMs, serverPID int
}
type runner struct {
	config             config
	token              string
	http               *http.Client
	connections        []*websocket.Conn
	instances          []string
	samplesMu          sync.Mutex
	samples            []int64
	sampleCounter      atomic.Uint64
	received           atomic.Uint64
	failures           atomic.Uint64
	rssKiB, cpuPercent float64
	peakHeap           uint64
	peakGC             uint32
	errorsMu           sync.Mutex
	errorMessages      []string
}

type report struct {
	RecordedAt            time.Time     `json:"recordedAt"`
	Runtime               string        `json:"runtime"`
	Participants          int           `json:"participants"`
	RoomSize              int           `json:"roomSize"`
	PublishHz             int           `json:"publishHz"`
	StatePayloadBytes     int           `json:"statePayloadBytes"`
	MeasurementSeconds    int           `json:"measurementSeconds"`
	SlowReadMs            int           `json:"slowReadMs"`
	Connected             int           `json:"connected"`
	ReceivedStates        uint64        `json:"receivedStates"`
	RollingSampleCount    int           `json:"rollingSampleCount"`
	P95Ms                 float64       `json:"p95Ms"`
	P99Ms                 float64       `json:"p99Ms"`
	IngressBytesPerSecond float64       `json:"ingressBytesPerSecond"`
	EgressBytesPerSecond  float64       `json:"egressBytesPerSecond"`
	PeakRSSKiB            float64       `json:"peakRssKiB"`
	PeakCPUPercent        float64       `json:"peakCpuPercent"`
	PeakHeapBytes         uint64        `json:"peakHeapBytes"`
	GCCycles              uint32        `json:"gcCycles"`
	Failures              uint64        `json:"failures"`
	Errors                []string      `json:"errors,omitempty"`
	NodeBefore            relayv2.Stats `json:"nodeBefore"`
	NodeAfter             relayv2.Stats `json:"nodeAfter"`
}

func main() {
	var c config
	flag.StringVar(&c.management, "management", "http://127.0.0.1:8092", "trusted loopback management URL")
	flag.StringVar(&c.tokenFile, "token-file", ".cache/wss-demo.token", "management token file")
	flag.StringVar(&c.caFile, "ca-file", ".cache/dev-tls/cert.pem", "trusted test CA PEM")
	flag.StringVar(&c.output, "out", "", "JSON report output")
	flag.IntVar(&c.participants, "participants", 100, "concurrent participants")
	flag.IntVar(&c.roomSize, "room-size", 8, "participants per room")
	flag.IntVar(&c.hz, "hz", 20, "state publications per second")
	flag.IntVar(&c.payload, "payload", 100, "full state payload bytes")
	flag.IntVar(&c.seconds, "seconds", 10, "measurement window seconds")
	flag.IntVar(&c.warmup, "warmup", 2, "warmup seconds")
	flag.IntVar(&c.slowMs, "slow-read-ms", 0, "delay one receiver in each room")
	flag.IntVar(&c.serverPID, "server-pid", 0, "separate server PID for OS CPU/RSS sampling")
	flag.Parse()
	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(c config) error {
	if c.participants < 2 || c.participants > 4096 || c.roomSize < 2 || c.roomSize > 16 || c.hz < 1 || c.hz > 20 || c.payload < 8 || c.payload > 4096 || c.seconds < 1 || c.seconds > 600 || c.warmup < 0 || c.slowMs < 0 {
		return errors.New("invalid load profile")
	}
	token, err := os.ReadFile(c.tokenFile)
	if err != nil {
		return err
	}
	pem, err := os.ReadFile(c.caFile)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return errors.New("invalid CA")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 5 * time.Second}
	r := &runner{config: c, token: strings.TrimSpace(string(token)), http: client}
	defer r.cleanup()
	if err := r.provision(); err != nil {
		return err
	}
	before, err := r.stats()
	if err != nil {
		return err
	}
	var begin atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var work sync.WaitGroup
	for index, conn := range r.connections {
		work.Add(2)
		go func() { defer work.Done(); r.publish(ctx, conn) }()
		go func() { defer work.Done(); r.read(ctx, conn, c.slowMs > 0 && index%c.roomSize == 0, &begin) }()
	}
	time.Sleep(time.Duration(c.warmup) * time.Second)
	before, err = r.stats()
	if err != nil {
		cancel()
		work.Wait()
		return err
	}
	begin.Store(time.Now().UnixNano())
	started := time.Now()
	for second := 0; second < c.seconds; second++ {
		time.Sleep(time.Second)
		stats, err := r.stats()
		if err != nil {
			r.failure(err)
		} else {
			r.peakHeap = max(r.peakHeap, stats.HeapAllocBytes)
			r.peakGC = max(r.peakGC, stats.GCCycles)
		}
		r.sampleProcess()
	}
	elapsed := time.Since(started).Seconds()
	after, err := r.stats()
	if err != nil {
		cancel()
		work.Wait()
		return err
	}
	cancel()
	work.Wait()
	r.samplesMu.Lock()
	samples := slices.Clone(r.samples)
	r.samplesMu.Unlock()
	slices.Sort(samples)
	percentile := func(q float64) float64 {
		if len(samples) == 0 {
			return -1
		}
		return float64(samples[min(len(samples)-1, int(math.Ceil(float64(len(samples))*q))-1)]) / 1e6
	}
	result := report{RecordedAt: time.Now().UTC(), Runtime: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + fmt.Sprintf(" CPUs=%d; same-host loopback TLS; 1/16 rolling latency samples", runtime.NumCPU()),
		Participants: c.participants, RoomSize: c.roomSize, PublishHz: c.hz, StatePayloadBytes: c.payload, MeasurementSeconds: c.seconds, SlowReadMs: c.slowMs, Connected: len(r.connections),
		ReceivedStates: r.received.Load(), RollingSampleCount: len(samples), P95Ms: percentile(.95), P99Ms: percentile(.99), IngressBytesPerSecond: float64(after.IngressBytes-before.IngressBytes) / elapsed, EgressBytesPerSecond: float64(after.EgressBytes-before.EgressBytes) / elapsed,
		PeakRSSKiB: r.rssKiB, PeakCPUPercent: r.cpuPercent, PeakHeapBytes: r.peakHeap, GCCycles: r.peakGC - before.GCCycles, Failures: r.failures.Load(), Errors: r.errorMessages, NodeBefore: before, NodeAfter: after}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if c.output != "" {
		if err := os.WriteFile(c.output, data, 0644); err != nil {
			return err
		}
	}
	fmt.Print(string(data))
	if result.Failures > 0 || result.P95Ms < 0 {
		return errors.New("load profile failed; inspect report")
	}
	return nil
}
func (r *runner) control(method, path string, body, target any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, r.config.management+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("management %s: HTTP %d", path, response.StatusCode)
	}
	if target == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}
func (r *runner) stats() (relayv2.Stats, error) {
	var stats relayv2.Stats
	err := r.control("GET", "/v2/status", nil, &stats)
	return stats, err
}
func (r *runner) provision() error {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	prefix := "load-" + hex.EncodeToString(id[:])
	for offset := 0; offset < r.config.participants; offset += r.config.roomSize {
		size := min(r.config.roomSize, r.config.participants-offset)
		participants := make([]string, size)
		for i := range participants {
			participants[i] = fmt.Sprintf("p-%d", offset+i)
		}
		var allocation struct {
			RoomInstanceID string
			WebSocketURL   string
			Tickets        map[string]string
		}
		request := map[string]any{"roomId": fmt.Sprintf("%s-%d", prefix, offset), "applicationProtocol": "load.v1", "participants": participants, "ttlSeconds": 1800}
		if err := r.control("POST", "/v2/rooms", request, &allocation); err != nil {
			return err
		}
		r.instances = append(r.instances, allocation.RoomInstanceID)
		// Connect each allocated room before requesting the next room, staying within ticket TTL.
		for _, participant := range participants {
			// Keep setup below the default 200/s shared-IP admission policy.
			time.Sleep(5 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			conn, _, err := websocket.Dial(ctx, allocation.WebSocketURL, &websocket.DialOptions{HTTPClient: r.http, Subprotocols: []string{"relay.v2"}})
			if err != nil {
				cancel()
				return err
			}
			r.connections = append(r.connections, conn)
			data, err := proto.Marshal(&wire.ClientEnvelope{ProtocolRevision: 2, Body: &wire.ClientEnvelope_Connect{Connect: &wire.Connect{ApplicationProtocol: "load.v1", Credential: &wire.Connect_ConnectTicket{ConnectTicket: allocation.Tickets[participant]}}}})
			if err == nil {
				err = conn.Write(ctx, websocket.MessageBinary, data)
			}
			var response []byte
			if err == nil {
				_, response, err = conn.Read(ctx)
			}
			cancel()
			if err != nil {
				return err
			}
			var envelope wire.ServerEnvelope
			if err := proto.Unmarshal(response, &envelope); err != nil {
				return err
			}
			if envelope.GetWelcome() == nil {
				return errors.New("Welcome missing during load setup")
			}
		}
	}
	return nil
}
func (r *runner) publish(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(time.Second / time.Duration(r.config.hz))
	defer ticker.Stop()
	payload := make([]byte, r.config.payload)
	var sequence uint32
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		binary.LittleEndian.PutUint64(payload, uint64(time.Now().UnixNano()))
		sequence++
		envelope := &wire.ClientEnvelope{ProtocolRevision: 2, Sequence: sequence, Body: &wire.ClientEnvelope_StateUpdate{StateUpdate: &wire.StateUpdate{Payload: payload}}}
		data, err := proto.Marshal(envelope)
		if err == nil {
			writeCtx, cancel := context.WithTimeout(ctx, time.Second)
			err = conn.Write(writeCtx, websocket.MessageBinary, data)
			cancel()
		}
		if err != nil {
			if ctx.Err() == nil {
				r.failure(err)
			}
			return
		}
	}
}
func (r *runner) read(ctx context.Context, conn *websocket.Conn, slow bool, begin *atomic.Int64) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.failure(err)
			}
			return
		}
		var envelope wire.ServerEnvelope
		if err := proto.Unmarshal(data, &envelope); err != nil {
			r.failure(err)
			return
		}
		if problem := envelope.GetError(); problem != nil {
			r.failure(fmt.Errorf("server error %s", problem.Code))
			if problem.Fatal {
				return
			}
		}
		if batch := envelope.GetStateBatch(); batch != nil && begin.Load() != 0 {
			now := time.Now().UnixNano()
			for _, state := range batch.States {
				if len(state.Payload) < 8 {
					r.failure(errors.New("short load state"))
					continue
				}
				sent := int64(binary.LittleEndian.Uint64(state.Payload))
				if sent < begin.Load() {
					continue
				}
				r.received.Add(1)
				if !slow && r.sampleCounter.Add(1)%16 == 0 {
					delay := now - sent
					if delay < 0 {
						r.failure(errors.New("clock moved backwards"))
						continue
					}
					r.samplesMu.Lock()
					if len(r.samples) < 65536 {
						r.samples = append(r.samples, delay)
					} else {
						r.samples[int(r.sampleCounter.Load()/16)%len(r.samples)] = delay
					}
					r.samplesMu.Unlock()
				}
			}
		}
		if slow && r.config.slowMs > 0 {
			timer := time.NewTimer(time.Duration(r.config.slowMs) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
func (r *runner) failure(err error) {
	r.failures.Add(1)
	r.errorsMu.Lock()
	defer r.errorsMu.Unlock()
	if len(r.errorMessages) < 10 {
		r.errorMessages = append(r.errorMessages, err.Error())
	}
}
func (r *runner) sampleProcess() {
	if r.config.serverPID <= 0 {
		return
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(r.config.serverPID), "-o", "rss=", "-o", "%cpu=").Output()
	if err != nil {
		return
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return
	}
	rss, _ := strconv.ParseFloat(fields[0], 64)
	cpu, _ := strconv.ParseFloat(fields[1], 64)
	r.rssKiB = max(r.rssKiB, rss)
	r.cpuPercent = max(r.cpuPercent, cpu)
}
func (r *runner) cleanup() {
	for _, conn := range r.connections {
		_ = conn.CloseNow()
	}
	for _, instance := range r.instances {
		_ = r.control("DELETE", "/v2/rooms/"+instance, nil, nil)
	}
}
