package relayv2

import (
	"runtime"
	runtimemetrics "runtime/metrics"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Byte budgets include encoded Protobuf bytes, separately for ingress and egress.
// WebSocket/TLS/TCP overhead must still be included in host network capacity planning.
type TrafficPolicy struct {
	NodeIngress int `json:"nodeIngressBytesPerSecond"`
	NodeEgress  int `json:"nodeEgressBytesPerSecond"`
	RoomIngress int `json:"roomIngressBytesPerSecond"`
	RoomEgress  int `json:"roomEgressBytesPerSecond"`
}

func defaultTraffic(policy TrafficPolicy) TrafficPolicy {
	if policy.NodeIngress == 0 {
		policy.NodeIngress = 8 << 20
	}
	if policy.NodeEgress == 0 {
		policy.NodeEgress = 64 << 20
	}
	if policy.RoomIngress == 0 {
		policy.RoomIngress = 512 << 10
	}
	if policy.RoomEgress == 0 {
		policy.RoomEgress = 4 << 20
	}
	return policy
}
func byteLimiter(bytesPerSecond int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(bytesPerSecond), max(128<<10, bytesPerSecond/4))
}

type counters struct {
	cpuMu             sync.Mutex
	cpuTotal, cpuIdle float64
	receivedBytes     atomic.Uint64
	sentBytes         atomic.Uint64
	receivedMessages  atomic.Uint64
	sentMessages      atomic.Uint64
	rateRejected      atomic.Uint64
	queueOverflows    atomic.Uint64
	writeErrors       atomic.Uint64
	pressureUntil     atomic.Int64
	queueDelay        histogram
	writeDelay        histogram
}

var delayBounds = [...]time.Duration{time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, time.Second, 5 * time.Second}

type histogram struct{ buckets [11]atomic.Uint64 }

func (h *histogram) observe(delay time.Duration) {
	for i, bound := range delayBounds {
		if delay <= bound {
			h.buckets[i].Add(1)
			return
		}
	}
	h.buckets[len(delayBounds)].Add(1)
}
func (h *histogram) percentile(percent uint64) int64 {
	var counts [11]uint64
	var total uint64
	for i := range counts {
		counts[i] = h.buckets[i].Load()
		total += counts[i]
	}
	if total == 0 {
		return 0
	}
	threshold := (total*percent + 99) / 100
	var seen uint64
	for i, count := range counts {
		seen += count
		if seen >= threshold {
			if i >= len(delayBounds) {
				return -1
			}
			return delayBounds[i].Milliseconds()
		}
	}
	return 0
}

type Stats struct {
	GoCPUReady             bool          `json:"goCpuReady"`
	SystemBytes            uint64        `json:"systemBytes"`
	GoBusyFractionEstimate float64       `json:"goBusyFractionEstimate"`
	NodeID                 string        `json:"nodeId"`
	Rooms                  int           `json:"rooms"`
	Connections            int           `json:"connections"`
	ReservedParticipants   int           `json:"reservedParticipants"`
	IngressBytes           uint64        `json:"ingressBytes"`
	EgressBytes            uint64        `json:"egressBytes"`
	ReceivedMessages       uint64        `json:"receivedMessages"`
	SentMessages           uint64        `json:"sentMessages"`
	RateRejected           uint64        `json:"rateRejected"`
	QueueOverflows         uint64        `json:"queueOverflows"`
	WriteErrors            uint64        `json:"writeErrors"`
	QueueP95UpperMs        int64         `json:"queueP95UpperMs"`
	QueueP99UpperMs        int64         `json:"queueP99UpperMs"`
	WriteP95UpperMs        int64         `json:"writeP95UpperMs"`
	WriteP99UpperMs        int64         `json:"writeP99UpperMs"`
	OldestQueuedMs         int64         `json:"oldestQueuedMs"`
	HeapAllocBytes         uint64        `json:"heapAllocBytes"`
	GCCycles               uint32        `json:"gcCycles"`
	Goroutines             int           `json:"goroutines"`
	Traffic                TrafficPolicy `json:"traffic"`
}

func (server *Server) Stats() Stats {
	stats := Stats{NodeID: server.nodeID, Traffic: server.traffic}
	now := time.Now()
	server.mu.Lock()
	stats.Rooms = len(server.rooms)
	stats.Connections = server.connections
	for _, r := range server.rooms {
		r.mu.Lock()
		for _, member := range r.members {
			if !member.left {
				stats.ReservedParticipants++
			}
		}
		for _, member := range r.members {
			if c := member.connection; c != nil {
				c.mu.Lock()
				for _, queued := range c.reliable {
					stats.OldestQueuedMs = max(stats.OldestQueuedMs, now.Sub(queued.at).Milliseconds())
				}
				for _, at := range c.stateTimes {
					stats.OldestQueuedMs = max(stats.OldestQueuedMs, now.Sub(at).Milliseconds())
				}
				c.mu.Unlock()
			}
		}
		r.mu.Unlock()
	}
	server.mu.Unlock()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	stats.SystemBytes = memory.Sys
	sample := []runtimemetrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}, {Name: "/cpu/classes/idle:cpu-seconds"}}
	runtimemetrics.Read(sample)
	if sample[0].Value.Kind() == runtimemetrics.KindFloat64 && sample[1].Value.Kind() == runtimemetrics.KindFloat64 {
		total, idle := sample[0].Value.Float64(), sample[1].Value.Float64()
		server.metrics.cpuMu.Lock()
		delta := total - server.metrics.cpuTotal
		if delta > 0 && server.metrics.cpuTotal > 0 {
			stats.GoCPUReady = true
			stats.GoBusyFractionEstimate = max(0, min(1, 1-(idle-server.metrics.cpuIdle)/delta))
		}
		server.metrics.cpuTotal = total
		server.metrics.cpuIdle = idle
		server.metrics.cpuMu.Unlock()
	}
	stats.IngressBytes = server.metrics.receivedBytes.Load()
	stats.EgressBytes = server.metrics.sentBytes.Load()
	stats.ReceivedMessages = server.metrics.receivedMessages.Load()
	stats.SentMessages = server.metrics.sentMessages.Load()
	stats.RateRejected = server.metrics.rateRejected.Load()
	stats.QueueOverflows = server.metrics.queueOverflows.Load()
	stats.WriteErrors = server.metrics.writeErrors.Load()
	stats.QueueP95UpperMs = server.metrics.queueDelay.percentile(95)
	stats.QueueP99UpperMs = server.metrics.queueDelay.percentile(99)
	stats.WriteP95UpperMs = server.metrics.writeDelay.percentile(95)
	stats.WriteP99UpperMs = server.metrics.writeDelay.percentile(99)
	stats.HeapAllocBytes = memory.HeapAlloc
	stats.GCCycles = memory.NumGC
	stats.Goroutines = runtime.NumGoroutine()
	return stats
}
func (server *Server) received(size int) {
	server.metrics.receivedBytes.Add(uint64(size))
	server.metrics.receivedMessages.Add(1)
}
func (server *Server) pressure() {
	server.metrics.pressureUntil.Store(time.Now().Add(time.Second).UnixNano())
}
