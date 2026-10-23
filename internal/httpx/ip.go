package httpx

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, err := netip.ParseAddr(value)
			if err != nil {
				return nil, errors.New("invalid trusted proxy")
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Bits() == 0 {
			return nil, errors.New("universal proxy trust is forbidden")
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}
func ClientIP(request *http.Request, trusted []netip.Prefix) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	current, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, err
	}
	current = current.Unmap().WithZone("")
	isTrusted := func(address netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	if !isTrusted(current) {
		return current, nil
	}
	headers := request.Header.Values("X-Forwarded-For")
	if len(headers) == 0 {
		return current, nil
	}
	if len(headers) != 1 || len(headers[0]) > 512 {
		return netip.Addr{}, errors.New("invalid forwarded chain")
	}
	chain := strings.Split(headers[0], ",")
	if len(chain) > 8 {
		return netip.Addr{}, errors.New("forwarded chain too long")
	}
	for i := len(chain) - 1; i >= 0 && isTrusted(current); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil || next.Zone() != "" {
			return netip.Addr{}, errors.New("invalid forwarded address")
		}
		current = next.Unmap()
	}
	return current, nil
}

type ipEntry struct {
	limiter *rate.Limiter
	touched time.Time
}
type IPBudget struct {
	mu            sync.Mutex
	entries       map[netip.Addr]*ipEntry
	rate          rate.Limit
	burst, maxIPs int
	now           func() time.Time
}

func NewIPBudget(limit rate.Limit, burst, maxIPs int, now func() time.Time) *IPBudget {
	if now == nil {
		now = time.Now
	}
	return &IPBudget{entries: make(map[netip.Addr]*ipEntry), rate: limit, burst: burst, maxIPs: maxIPs, now: now}
}
func (budget *IPBudget) Allow(ip netip.Addr) bool {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	now := budget.now()
	entry := budget.entries[ip]
	if entry == nil {
		if len(budget.entries) >= budget.maxIPs {
			for key, old := range budget.entries {
				if now.Sub(old.touched) >= time.Minute {
					delete(budget.entries, key)
				}
			}
		}
		if len(budget.entries) >= budget.maxIPs {
			return false
		}
		entry = &ipEntry{limiter: rate.NewLimiter(budget.rate, budget.burst)}
		budget.entries[ip] = entry
	}
	entry.touched = now
	return entry.limiter.AllowN(now, 1)
}
