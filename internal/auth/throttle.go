package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const (
	throttleCapacity = 4096
	throttleIdle     = 15 * time.Minute
)

type throttleEntry struct {
	failures   int
	retryAfter time.Time
	lastSeen   time.Time
}

type loginThrottle struct {
	mu      sync.Mutex
	entries map[string]throttleEntry
	now     func() time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{entries: make(map[string]throttleEntry), now: time.Now}
}

func accountThrottleKey(username string) string {
	hash := sha256.Sum256([]byte(username))
	return "account:" + hex.EncodeToString(hash[:])
}

func clientThrottleKey(identity string) string {
	return "client:" + identity
}

func (t *loginThrottle) Check(keys ...string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanup(now)
	var longest time.Duration
	for _, key := range keys {
		entry, ok := t.entries[key]
		if !ok {
			continue
		}
		entry.lastSeen = now
		t.entries[key] = entry
		if delay := entry.retryAfter.Sub(now); delay > longest {
			longest = delay
		}
	}
	return longest
}

func (t *loginThrottle) Failure(keys ...string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanup(now)
	var longest time.Duration
	for _, key := range keys {
		entry := t.entries[key]
		entry.failures++
		entry.lastSeen = now
		if entry.failures >= 5 {
			shift := entry.failures - 5
			if shift > 6 {
				shift = 6
			}
			delay := time.Second * time.Duration(1<<shift)
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			entry.retryAfter = now.Add(delay)
			if delay > longest {
				longest = delay
			}
		}
		t.entries[key] = entry
	}
	t.bound()
	return longest
}

func (t *loginThrottle) Success(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range keys {
		delete(t.entries, key)
	}
}

func (t *loginThrottle) cleanup(now time.Time) {
	cutoff := now.Add(-throttleIdle)
	for key, entry := range t.entries {
		if entry.lastSeen.Before(cutoff) {
			delete(t.entries, key)
		}
	}
}

func (t *loginThrottle) bound() {
	for len(t.entries) > throttleCapacity {
		var oldestKey string
		var oldest time.Time
		for key, entry := range t.entries {
			if oldestKey == "" || entry.lastSeen.Before(oldest) ||
				(entry.lastSeen.Equal(oldest) && key < oldestKey) {
				oldestKey = key
				oldest = entry.lastSeen
			}
		}
		delete(t.entries, oldestKey)
	}
}
