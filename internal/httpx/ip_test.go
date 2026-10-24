package httpx

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestClientIPHonorsOnlyConfiguredProxyChain(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"127.0.0.1/32", "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		remote, forwarded, want string
		invalid                 bool
	}{
		{"192.0.2.1:123", "203.0.113.7", "192.0.2.1", false},
		{"127.0.0.1:123", "203.0.113.7, 10.0.0.1", "203.0.113.7", false},
		{"127.0.0.1:123", "203.0.113.7, 10.0.0.2", "10.0.0.2", false},
		{"127.0.0.1:123", "not-an-ip", "", true},
	} {
		request := httptest.NewRequest("GET", "/", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", test.forwarded)
		actual, err := ClientIP(request, trusted)
		if test.invalid {
			if err == nil {
				t.Fatal("malformed trusted chain accepted")
			}
			continue
		}
		if err != nil || actual.String() != test.want {
			t.Fatalf("%+v => %v %v", test, actual, err)
		}
	}
	if _, err := ParseTrustedProxies([]string{"0.0.0.0/0"}); err == nil {
		t.Fatal("universal trust allowed")
	}
}
func TestIPBudgetExpiresEntriesAtCapacity(t *testing.T) {
	now := time.Now()
	budget := NewIPBudget(1, 1, 1, func() time.Time { return now })
	a, b := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")
	if !budget.Allow(a) || budget.Allow(a) || budget.Allow(b) {
		t.Fatal("IP budget not enforced")
	}
	now = now.Add(time.Minute)
	if !budget.Allow(b) {
		t.Fatal("expired budget entry retained capacity")
	}
}
