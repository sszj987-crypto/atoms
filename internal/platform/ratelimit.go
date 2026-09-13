package platform

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a sliding-window limiter keyed by an arbitrary string (client IP).
type rateLimiter struct {
	mu    sync.Mutex
	hits  map[string][]time.Time
	limit int
	per   time.Duration
}

func newRateLimiter(limit int, per time.Duration) *rateLimiter {
	return &rateLimiter{hits: make(map[string][]time.Time), limit: limit, per: per}
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-l.per)
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.hits[key]
	n := 0
	for _, t := range hits {
		if t.After(cutoff) {
			hits[n] = t
			n++
		}
	}
	hits = hits[:n]
	if len(hits) >= l.limit {
		l.hits[key] = hits
		return false
	}
	l.hits[key] = append(hits, now)
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authLimiter guards login/register against brute-force.
var authLimiter = newRateLimiter(10, time.Minute)

func authRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authLimiter.allow(clientIP(r)) {
			apiError(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS")
			return
		}
		next.ServeHTTP(w, r)
	})
}
