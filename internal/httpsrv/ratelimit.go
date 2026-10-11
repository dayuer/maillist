package httpsrv

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxMemoryKeys bounds the limiter's memory (upstream MAX_MEMORY_KEYS).
const MaxMemoryKeys = 10_000

// Limit is one named allowance: Count hits per Window, per key.
type Limit struct {
	Name   string
	Count  int
	Window time.Duration
}

// MemoryLimiter counts hits per limit and key in this process. Upstream used Upstash Redis with a
// memory fallback; the design (D4) drops Redis, and one instance means memory *is* the global limit
// — more accurate than the old per-serverless-instance counting, not less.
type MemoryLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

// NewMemoryLimiter returns an empty limiter.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{hits: map[string][]time.Time{}}
}

// Limited records a hit and reports whether the key is over its allowance. A refused hit is not
// recorded, so a client that keeps trying does not extend its own window.
func (l *MemoryLimiter) Limited(limit Limit, key string, now time.Time) bool {
	id := limit.Name + ":" + key
	l.mu.Lock()
	defer l.mu.Unlock()

	within := make([]time.Time, 0, len(l.hits[id]))
	for _, at := range l.hits[id] {
		if now.Sub(at) < limit.Window {
			within = append(within, at)
		}
	}
	limited := len(within) >= limit.Count
	if !limited {
		within = append(within, now)
	}
	// Re-inserting keeps the map ordered by last use, so the oldest key goes first when it is full.
	delete(l.hits, id)
	l.hits[id] = within
	for len(l.hits) > MaxMemoryKeys {
		for oldest := range l.hits {
			delete(l.hits, oldest)
			break
		}
	}
	return limited
}

// Size is how many keys are being counted.
func (l *MemoryLimiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

// ClientIP is the address a request is counted against.
//
// X-Forwarded-For is a header, so anybody can send it: trusting it unconditionally made every
// per-visitor rate limit bypassable with a curl loop (the phase 1-3 audit's S-I2). It is only read
// when the request's own peer is in the trusted set — the reverse proxy in front of this process.
// Without TRUSTED_PROXIES nothing is trusted and the peer address is used, which is right for a
// process serving traffic directly and for `docker compose` with no proxy.
//
// When it is trusted, the first hop of the forwarded chain is the client. A request with no usable
// peer still shares one bucket rather than escaping (upstream's "unknown").
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := peerIP(r.RemoteAddr)
	if peer == nil || !trustedPeer(peer, trusted) {
		if peer == nil {
			return "unknown"
		}
		return peer.String()
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if client := parseIP(first); client != nil {
			return client.String()
		}
	}
	if client := parseIP(r.Header.Get("X-Real-IP")); client != nil {
		return client.String()
	}
	return peer.String()
}

// ParseTrustedProxies turns "10.0.0.0/8, 172.16.0.0/12, 192.168.1.5" into networks. A bare address
// is a /32 or /128. Anything unparsable is an error: a mistyped proxy range silently stops the
// header being trusted, which looks like the limit working until somebody notices a shared bucket.
func ParseTrustedProxies(values []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "/") {
			ip := parseIP(value)
			if ip == nil {
				return nil, fmt.Errorf("trusted proxy %q is not an address or a network", value)
			}
			bits := 8 * net.IPv6len
			if ip.To4() != nil {
				bits = 8 * net.IPv4len
			}
			value = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not an address or a network", value)
		}
		nets = append(nets, network)
	}
	return nets, nil
}

func trustedPeer(peer net.IP, trusted []*net.IPNet) bool {
	for _, network := range trusted {
		if network.Contains(peer) {
			return true
		}
	}
	return false
}

// peerIP is the address the connection came from, with the port removed.
func peerIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return parseIP(host)
}

// parseIP accepts an address or a host:port, and strips the brackets an IPv6 literal arrives in.
func parseIP(value string) net.IP {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	if zone := strings.LastIndex(value, "%"); zone >= 0 {
		value = value[:zone] // an IPv6 zone is local to the peer and not part of its identity
	}
	return net.ParseIP(value)
}

// rateLimited writes the 429 response the rate-limited handlers share.
func rateLimited(w http.ResponseWriter, limit Limit) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", int(limit.Window.Seconds())))
	http.Error(w, "Too many requests", http.StatusTooManyRequests)
}
