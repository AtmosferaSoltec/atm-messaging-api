package admin

import (
	"sync"
	"time"
)

const (
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
	maxTrackedKeys   = 10_000
)

// loginLimiter blocks a key (IP or email) after too many failed logins within
// a window, to slow down password guessing. It lives in memory, which is enough
// for a single instance; a restart clears it.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: make(map[string][]time.Time), now: time.Now}
}

// Blocked reports whether the key has used up its attempts.
func (l *loginLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recentLocked(key)) >= maxLoginFailures
}

// Fail records a failed attempt.
func (l *loginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) >= maxTrackedKeys {
		clear(l.failures) // bounded memory beats perfect accounting under an attack
	}
	l.failures[key] = append(l.recentLocked(key), l.now())
}

// Reset forgets the failures of a key after a successful login.
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *loginLimiter) recentLocked(key string) []time.Time {
	cutoff := l.now().Add(-loginWindow)
	attempts := l.failures[key]
	kept := attempts[:0]
	for _, at := range attempts {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}
