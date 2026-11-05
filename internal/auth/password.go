// Package auth owns the single administrator credential boundary.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB = 32 * 1024
	argonTime      = 3
	argonThreads   = 1
	argonSaltBytes = 16
	argonKeyBytes  = 32
)

// Hash admission (ADR 0010). At most hashSlots Argon2id operations run at
// once. Sign-in attempts wait in a bounded lane for at most hashQueueWait;
// overflow and an expired wait fail fast with errHashOverloaded instead of
// queueing behind a flood.
const (
	hashSlots        = 2
	hashGeneralQueue = 8
	hashRecentQueue  = 2
	hashQueueWait    = 2 * time.Second
)

// errHashOverloaded means the attempt was not hashed because its admission
// lane was full or its wait expired.
var errHashOverloaded = errors.New("password hashing is at capacity")

type hashLane int

const (
	// hashLaneInternal serves administrator creation and password reset by
	// the local operator. It is unbounded and waits until its context ends.
	hashLaneInternal hashLane = iota
	// hashLaneRecent serves sign-in attempts from client keys that signed in
	// successfully within the last seven days.
	hashLaneRecent
	// hashLaneGeneral serves every other sign-in attempt.
	hashLaneGeneral
	hashLaneCount
)

// hashLaneOrder is the order in which a freed slot is handed to waiters:
// the recent-client lane is always served before the general lane.
var hashLaneOrder = [hashLaneCount]hashLane{hashLaneInternal, hashLaneRecent, hashLaneGeneral}

type hashLaneContextKey struct{}

func contextWithHashLane(ctx context.Context, lane hashLane) context.Context {
	return context.WithValue(ctx, hashLaneContextKey{}, lane)
}

func hashLaneFromContext(ctx context.Context) hashLane {
	if lane, ok := ctx.Value(hashLaneContextKey{}).(hashLane); ok && lane < hashLaneCount {
		return lane
	}
	return hashLaneInternal
}

type hashWaiter struct {
	ready   chan struct{}
	granted bool
}

// hashGate is a counting semaphore with per-lane bounded FIFO queues and
// strict lane priority. A released slot passes directly to the next waiter,
// so a new arrival never overtakes a queued one.
type hashGate struct {
	mu     sync.Mutex
	slots  int
	active int
	wait   time.Duration
	limits [hashLaneCount]int
	queues [hashLaneCount][]*hashWaiter
}

func newHashGate(slots int, wait time.Duration, general, recent int) *hashGate {
	gate := &hashGate{slots: slots, wait: wait}
	gate.limits[hashLaneGeneral] = general
	gate.limits[hashLaneRecent] = recent
	return gate
}

var passwordHashing = newHashGate(hashSlots, hashQueueWait, hashGeneralQueue, hashRecentQueue)

func (g *hashGate) acquire(ctx context.Context, lane hashLane) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	if g.active < g.slots && g.waitingLocked() == 0 {
		g.active++
		g.mu.Unlock()
		return nil
	}
	if limit := g.limits[lane]; lane != hashLaneInternal && len(g.queues[lane]) >= limit {
		g.mu.Unlock()
		return errHashOverloaded
	}
	waiter := &hashWaiter{ready: make(chan struct{})}
	g.queues[lane] = append(g.queues[lane], waiter)
	g.mu.Unlock()

	var expired <-chan time.Time
	if lane != hashLaneInternal {
		timer := time.NewTimer(g.wait)
		defer timer.Stop()
		expired = timer.C
	}
	var err error
	select {
	case <-waiter.ready:
		return nil
	case <-expired:
		err = errHashOverloaded
	case <-ctx.Done():
		err = ctx.Err()
	}
	g.mu.Lock()
	if waiter.granted {
		g.mu.Unlock()
		if errors.Is(err, errHashOverloaded) {
			// The slot arrived as the wait expired; use it.
			return nil
		}
		g.release()
		return err
	}
	queue := g.queues[lane]
	for index, candidate := range queue {
		if candidate == waiter {
			g.queues[lane] = append(queue[:index:index], queue[index+1:]...)
			break
		}
	}
	g.mu.Unlock()
	return err
}

func (g *hashGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, lane := range hashLaneOrder {
		if queue := g.queues[lane]; len(queue) > 0 {
			next := queue[0]
			queue[0] = nil
			g.queues[lane] = queue[1:]
			next.granted = true
			close(next.ready)
			return
		}
	}
	g.active--
}

func (g *hashGate) waitingLocked() int {
	waiting := 0
	for _, queue := range g.queues {
		waiting += len(queue)
	}
	return waiting
}

func (g *hashGate) snapshot() (active int, waiting [hashLaneCount]int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for lane, queue := range g.queues {
		waiting[lane] = len(queue)
	}
	return g.active, waiting
}

var dummyPasswordHash = "$argon2id$v=19$m=32768,t=3,p=1$c2lmdGFpbC1kdW1teS1zYWx0$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type passwordParameters struct {
	memory  uint32
	time    uint32
	threads uint8
}

func ValidateUsername(username string) error {
	if len(username) < 3 || len(username) > 64 {
		return errors.New("username must be 3 to 64 ASCII characters")
	}
	for _, char := range []byte(username) {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' {
			continue
		}
		return errors.New("username contains an unsupported character")
	}
	return nil
}

func ValidatePassword(password []byte) error {
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("password must be 12 to 1024 UTF-8 bytes")
	}
	if !utf8.Valid(password) {
		return errors.New("password must be valid UTF-8")
	}
	return nil
}

func HashPassword(ctx context.Context, password []byte) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("generate password salt")
	}
	parameters := passwordParameters{
		memory: argonMemoryKiB, time: argonTime, threads: argonThreads,
	}
	key, err := deriveKey(ctx, password, salt, parameters, argonKeyBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, parameters.memory, parameters.time, parameters.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(ctx context.Context, password []byte, encoded string) (bool, error) {
	parameters, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, errors.New("stored password hash is invalid")
	}
	key, err := deriveKey(ctx, password, salt, parameters, uint32(len(expected)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, expected) == 1, nil
}

func deriveKey(
	ctx context.Context,
	password, salt []byte,
	parameters passwordParameters,
	keyBytes uint32,
) ([]byte, error) {
	if err := passwordHashing.acquire(ctx, hashLaneFromContext(ctx)); err != nil {
		return nil, err
	}
	defer passwordHashing.release()
	return argon2.IDKey(password, salt, parameters.time, parameters.memory, parameters.threads, keyBytes), nil
}

func parsePasswordHash(encoded string) (passwordParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id encoding")
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id parameters")
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, threads) {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id parameters")
	}
	if memory < 8*1024 || memory > 64*1024 || iterations < 1 || iterations > 10 ||
		threads < 1 || threads > 4 {
		return passwordParameters{}, nil, nil, errors.New("unsafe Argon2id parameters")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id salt")
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id key")
	}
	return passwordParameters{memory: memory, time: iterations, threads: threads}, salt, key, nil
}
