package httpsrv

import (
	"fmt"
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

// ClientIP is upstream getClientIp: the first hop in X-Forwarded-For, else X-Real-IP, else
// "unknown" so a request without either header still shares one bucket instead of escaping.
func ClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if trimmed := strings.TrimSpace(first); trimmed != "" {
			return trimmed
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return "unknown"
}

// rateLimited writes the 429 response the rate-limited handlers share.
func rateLimited(w http.ResponseWriter, limit Limit) {
	w.Header().Set("Retry-After", fmt.Sprintf("%d", int(limit.Window.Seconds())))
	http.Error(w, "Too many requests", http.StatusTooManyRequests)
}
