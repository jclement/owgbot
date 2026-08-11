package bot

import (
	"sync"
	"time"
)

// rateLimiter is a per-user token bucket. Limits come from config on every
// call so live reloads take effect immediately.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	// noticed marks that the user has been told they're over the limit;
	// further over-limit messages are dropped silently until they refill.
	noticed bool
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket)}
}

// allow consumes a token for user. The second return is true when this is
// the first rejection since the user last had tokens (send one terse notice,
// then go silent).
func (r *rateLimiter) allow(user string, perMinute, burst int) (ok, firstReject bool) {
	if perMinute <= 0 {
		return true, false
	}
	if burst < 1 {
		burst = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, exists := r.buckets[user]
	if !exists {
		b = &bucket{tokens: float64(burst), last: now}
		r.buckets[user] = b
	}
	b.tokens += now.Sub(b.last).Minutes() * float64(perMinute)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		b.noticed = false
		return true, false
	}
	first := !b.noticed
	b.noticed = true
	return false, first
}
