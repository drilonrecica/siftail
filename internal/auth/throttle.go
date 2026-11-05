package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"sync"
	"time"
)

const (
	throttleCapacity = 4096
	throttleIdle     = 15 * time.Minute
	// throttleFreeFailures is the number of counted failures a key may
	// accumulate before each further failure sets a retry delay.
	throttleFreeFailures = 5
	throttleMaxDelay     = 60 * time.Second
	// recentClientCapacity matches the 64-session cap (ADR 0010).
	recentClientCapacity = 64
	recentClientLifetime = 7 * 24 * time.Hour
	// inFlightRetryAfter is the Retry-After for a second concurrent attempt
	// on a key that already has one in flight.
	inFlightRetryAfter = time.Second
)

type throttleEntry struct {
	failures   int
	retryAfter time.Time
	lastSeen   time.Time
	// inFlight counts reserved attempts that are not yet resolved. It is at
	// most one for every key except an account key reserved by
	// recent-client attempts, which are exempt from the account limit.
	inFlight int
}

// throttleDecision says why Check refused a reservation.
type throttleDecision int

const (
	throttleAllowed throttleDecision = iota
	// throttleLimited means a key is inside its retry delay.
	throttleLimited
	// throttleInFlight means a key already has an attempt in flight.
	throttleInFlight
)

// throttleReservation is one admitted sign-in attempt. Check counts it as a
// failure up front; Success releases it, Failure keeps it, and Release
// withdraws it when no password guess was evaluated.
type throttleReservation struct {
	clientKey  string
	accountKey string
	// recent is true when the client key signed in successfully within
	// recentClientLifetime. Such a client is exempt from the account
	// throttle and uses the recent-client hash lane.
	recent   bool
	resolved bool
}

type loginThrottle struct {
	mu      sync.Mutex
	entries map[string]throttleEntry
	recent  map[string]time.Time
	now     func() time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		entries: make(map[string]throttleEntry),
		recent:  make(map[string]time.Time),
		now:     time.Now,
	}
}

func accountThrottleKey(username string) string {
	hash := sha256.Sum256([]byte(username))
	return "account:" + hex.EncodeToString(hash[:])
}

// clientThrottleKey groups IPv6 clients by /64, because one subscriber or
// host usually controls a whole /64, and IPv4 clients by /32. Identities
// that are not addresses ("unknown") are used as they are.
func clientThrottleKey(identity string) string {
	address, err := netip.ParseAddr(identity)
	if err != nil {
		return "client:" + identity
	}
	address = address.Unmap().WithZone("")
	if address.Is4() {
		return "client:" + address.String() + "/32"
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return "client:" + identity
	}
	return "client:" + prefix.String()
}

// Check admits one attempt for the client and account keys or says why it
// cannot. An admitted attempt is reserved: it counts as a failure and holds
// the in-flight slot of each key until it is resolved by exactly one of
// Success, Failure, or Release. A client that signed in successfully within
// recentClientLifetime is not blocked by the account key's delay or
// in-flight attempt, but its attempt still counts toward the account key.
func (t *loginThrottle) Check(clientKey, accountKey string) (*throttleReservation, throttleDecision, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.cleanup(now)
	recent := t.recentLocked(clientKey, now)
	client := t.entries[clientKey]
	account := t.entries[accountKey]

	var delay time.Duration
	if wait := client.retryAfter.Sub(now); wait > delay {
		delay = wait
	}
	if !recent {
		if wait := account.retryAfter.Sub(now); wait > delay {
			delay = wait
		}
	}
	if delay > 0 {
		t.touchLocked(clientKey, now)
		t.touchLocked(accountKey, now)
		return nil, throttleLimited, delay
	}
	if client.inFlight > 0 || (!recent && account.inFlight > 0) {
		t.touchLocked(clientKey, now)
		t.touchLocked(accountKey, now)
		return nil, throttleInFlight, inFlightRetryAfter
	}
	for _, key := range []string{clientKey, accountKey} {
		entry := t.entries[key]
		entry.failures++
		entry.inFlight++
		entry.lastSeen = now
		t.entries[key] = entry
	}
	t.bound(now)
	return &throttleReservation{clientKey: clientKey, accountKey: accountKey, recent: recent}, throttleAllowed, 0
}

// Failure keeps the reserved attempt as a counted failure and returns the
// longest retry delay it caused, or zero.
func (t *loginThrottle) Failure(reservation *throttleReservation) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.resolveLocked(reservation) {
		return 0
	}
	now := t.now()
	var longest time.Duration
	for _, key := range reservation.keys() {
		entry, ok := t.entries[key]
		if !ok {
			continue
		}
		entry.inFlight--
		entry.lastSeen = now
		if delay := failureDelay(entry.failures); delay > 0 {
			entry.retryAfter = now.Add(delay)
			if delay > longest {
				longest = delay
			}
		}
		t.entries[key] = entry
	}
	return longest
}

// Success releases the reservation, clears both keys' failure history, and
// remembers the client key as recently successful.
func (t *loginThrottle) Success(reservation *throttleReservation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.resolveLocked(reservation) {
		return
	}
	now := t.now()
	for _, key := range reservation.keys() {
		entry, ok := t.entries[key]
		if !ok {
			continue
		}
		entry.inFlight--
		if entry.inFlight <= 0 {
			delete(t.entries, key)
			continue
		}
		// A concurrent recent-client attempt still holds this account key;
		// keep its slot and drop only the history.
		entry.failures = entry.inFlight
		entry.retryAfter = time.Time{}
		entry.lastSeen = now
		t.entries[key] = entry
	}
	t.rememberLocked(reservation.clientKey, now)
}

// Release withdraws a reservation whose password was never evaluated, such
// as an admission overflow or an infrastructure error, so it is not counted.
func (t *loginThrottle) Release(reservation *throttleReservation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.resolveLocked(reservation) {
		return
	}
	for _, key := range reservation.keys() {
		entry, ok := t.entries[key]
		if !ok {
			continue
		}
		entry.inFlight--
		if entry.failures > 0 {
			entry.failures--
		}
		t.entries[key] = entry
	}
}

func (r *throttleReservation) keys() []string {
	return []string{r.clientKey, r.accountKey}
}

func (t *loginThrottle) resolveLocked(reservation *throttleReservation) bool {
	if reservation == nil || reservation.resolved {
		return false
	}
	reservation.resolved = true
	return true
}

// failureDelay doubles from one second after the fifth counted failure and
// is capped at throttleMaxDelay.
func failureDelay(failures int) time.Duration {
	if failures < throttleFreeFailures {
		return 0
	}
	shift := failures - throttleFreeFailures
	if shift > 6 {
		shift = 6
	}
	delay := time.Second * time.Duration(1<<shift)
	if delay > throttleMaxDelay {
		delay = throttleMaxDelay
	}
	return delay
}

func (t *loginThrottle) touchLocked(key string, now time.Time) {
	if entry, ok := t.entries[key]; ok {
		entry.lastSeen = now
		t.entries[key] = entry
	}
}

func (t *loginThrottle) recentLocked(clientKey string, now time.Time) bool {
	succeeded, ok := t.recent[clientKey]
	if !ok {
		return false
	}
	if now.Sub(succeeded) >= recentClientLifetime {
		delete(t.recent, clientKey)
		return false
	}
	return true
}

func (t *loginThrottle) rememberLocked(clientKey string, now time.Time) {
	t.recent[clientKey] = now
	for len(t.recent) > recentClientCapacity {
		var oldestKey string
		var oldest time.Time
		for key, succeeded := range t.recent {
			if oldestKey == "" || succeeded.Before(oldest) ||
				(succeeded.Equal(oldest) && key < oldestKey) {
				oldestKey = key
				oldest = succeeded
			}
		}
		delete(t.recent, oldestKey)
	}
}

// cleanup drops idle entries. An entry with an attempt in flight is never
// dropped, so its in-flight limit cannot be lost while the attempt runs.
func (t *loginThrottle) cleanup(now time.Time) {
	cutoff := now.Add(-throttleIdle)
	for key, entry := range t.entries {
		if entry.inFlight == 0 && entry.lastSeen.Before(cutoff) {
			delete(t.entries, key)
		}
	}
}

// bound evicts idle entries above throttleCapacity. In-flight entries are
// kept: evicting one would let a flood of fresh keys erase another key's
// in-flight limit. Their number is bounded by the sign-in requests being
// handled at once, which hash admission keeps short.
//
// Among idle entries the least valuable state goes first: entries with no
// failure history (withdrawn or released attempts), then entries whose
// retry delay has passed, and only then entries inside an active delay, each
// tier least recently seen first. A flood of cheap, uncounted attempts on
// fresh usernames therefore evicts only its own entries and cannot erase a
// locked account's delay (a key enters the last tier only after
// throttleFreeFailures counted Argon2 failures).
func (t *loginThrottle) bound(now time.Time) {
	for len(t.entries) > throttleCapacity {
		var oldestKey string
		var oldest time.Time
		oldestTier := -1
		for key, entry := range t.entries {
			if entry.inFlight > 0 {
				continue
			}
			tier := evictionTier(entry, now)
			if oldestKey == "" || tier < oldestTier ||
				(tier == oldestTier && (entry.lastSeen.Before(oldest) ||
					(entry.lastSeen.Equal(oldest) && key < oldestKey))) {
				oldestKey = key
				oldest = entry.lastSeen
				oldestTier = tier
			}
		}
		if oldestKey == "" {
			return
		}
		delete(t.entries, oldestKey)
	}
}

// evictionTier ranks an idle entry for capacity eviction: 0 carries no
// failure history, 1 carries counted failures but no active delay, 2 is
// inside its retry delay.
func evictionTier(entry throttleEntry, now time.Time) int {
	switch {
	case entry.failures <= 0:
		return 0
	case !entry.retryAfter.After(now):
		return 1
	default:
		return 2
	}
}
