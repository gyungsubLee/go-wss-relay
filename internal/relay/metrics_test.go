package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQueuePercentileOverflowPreservesJSONSentinel(t *testing.T) {
	server, err := New(Config{NodeID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.metrics.queueDelay.observe(5*time.Second + time.Nanosecond)
	stats := server.Stats()
	if stats.QueueP95UpperMs != -1 || stats.QueueP99UpperMs != -1 {
		t.Fatalf("overflow p95=%d p99=%d", stats.QueueP95UpperMs, stats.QueueP99UpperMs)
	}
	encoded, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"queueP95UpperMs":-1`) || !strings.Contains(string(encoded), `"queueP99UpperMs":-1`) {
		t.Fatalf("overflow sentinel changed in JSON: %s", encoded)
	}
}
