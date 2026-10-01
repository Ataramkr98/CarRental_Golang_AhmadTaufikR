package middleware

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// visitor tracks the request timestamps inside the current window.
type visitor struct {
	timestamps []time.Time
	lastSeen   time.Time
}

// limiter is a small in-memory sliding-window rate limiter.
//
// It is intentionally dependency-free: for a single-instance deployment this
// is enough to blunt credential stuffing and booking spam. A horizontally
// scaled deployment should move this to a shared store such as Redis.
type limiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    int
	window   time.Duration
	stop     chan struct{}
}

var (
	registryMu sync.Mutex
	registry   = make(map[string]*limiter)
)

func newLimiter(limit int, window time.Duration) *limiter {
	instance := &limiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		window:   window,
		stop:     make(chan struct{}),
	}
	go instance.reap()
	return instance
}

// reap drops idle visitors so the map cannot grow without bound.
func (l *limiter) reap() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-l.window * 2)
			l.mu.Lock()
			for key, entry := range l.visitors {
				if entry.lastSeen.Before(cutoff) {
					delete(l.visitors, key)
				}
			}
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}

// allow records an attempt and reports whether it falls inside the limit.
func (l *limiter) allow(key string) bool {
	now := time.Now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.visitors[key]
	if !ok {
		entry = &visitor{}
		l.visitors[key] = entry
	}
	entry.lastSeen = now

	kept := entry.timestamps[:0]
	for _, stamp := range entry.timestamps {
		if stamp.After(cutoff) {
			kept = append(kept, stamp)
		}
	}
	entry.timestamps = kept

	if len(entry.timestamps) >= l.limit {
		return false
	}
	entry.timestamps = append(entry.timestamps, now)
	return true
}

// RateLimit returns a middleware that caps requests per client IP.
//
// scope lets several routes share a budget (for example all login attempts)
// while other routes keep an independent counter.
func RateLimit(scope string, limit int, window time.Duration) fiber.Handler {
	registryMu.Lock()
	key := fmt.Sprintf("%s:%d:%s", scope, limit, window)
	instance, ok := registry[key]
	if !ok {
		instance = newLimiter(limit, window)
		registry[key] = instance
	}
	registryMu.Unlock()

	retrySeconds := int(math.Ceil(window.Seconds()))
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	retryAfterStr := strconv.Itoa(retrySeconds)

	return func(c fiber.Ctx) error {
		if instance.allow(scope + "|" + c.IP()) {
			return c.Next()
		}
		c.Set("Retry-After", retryAfterStr)
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"success": false,
			"data":    nil,
			"message": "too many attempts — please wait a moment and try again",
		})
	}
}
