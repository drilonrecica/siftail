package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientThrottleKeyGroupsIPv6By64AndIPv4By32(t *testing.T) {
	for _, test := range []struct {
		left, right string
		same        bool
	}{
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff", true},
		{"2001:db8:1:2::1", "2001:db8:1:3::1", false},
		{"fe80::1%eth0", "fe80::2", true},
		{"192.0.2.1", "192.0.2.2", false},
		{"::ffff:192.0.2.1", "192.0.2.1", true},
		{"unknown", "unknown", true},
		{"unknown", "192.0.2.1", false},
	} {
		left, right := clientThrottleKey(test.left), clientThrottleKey(test.right)
		if (left == right) != test.same {
			t.Errorf("%q -> %q, %q -> %q, want same=%v", test.left, left, test.right, right, test.same)
		}
	}
	if got := clientThrottleKey("2001:db8:1:2:3:4:5:6"); got != "client:2001:db8:1:2::/64" {
		t.Fatalf("IPv6 key = %q", got)
	}
	if got := clientThrottleKey("192.0.2.9"); got != "client:192.0.2.9/32" {
		t.Fatalf("IPv4 key = %q", got)
	}
}

func TestThrottleCheckReservesOneInFlightAttemptPerKey(t *testing.T) {
	throttle := newLoginThrottle()
	first, decision, _ := throttle.Check("client:a", "account:x")
	if decision != throttleAllowed {
		t.Fatalf("first decision = %d", decision)
	}
	if got := throttle.entries["client:a"].failures; got != 1 {
		t.Fatalf("reservation not counted up front: failures = %d", got)
	}
	if _, decision, delay := throttle.Check("client:a", "account:y"); decision != throttleInFlight || delay <= 0 {
		t.Fatalf("same client in flight = %d %s", decision, delay)
	}
	if _, decision, _ := throttle.Check("client:b", "account:x"); decision != throttleInFlight {
		t.Fatalf("same account in flight = %d", decision)
	}
	throttle.Failure(first)
	throttle.Failure(first) // resolving twice is a no-op
	if entry := throttle.entries["client:a"]; entry.failures != 1 || entry.inFlight != 0 {
		t.Fatalf("after failure = %+v", entry)
	}

	released, _, _ := throttle.Check("client:a", "account:x")
	throttle.Release(released)
	if entry := throttle.entries["account:x"]; entry.failures != 1 || entry.inFlight != 0 {
		t.Fatalf("released attempt was counted: %+v", entry)
	}

	succeeded, _, _ := throttle.Check("client:a", "account:x")
	throttle.Success(succeeded)
	if len(throttle.entries) != 0 {
		t.Fatalf("success kept history: %+v", throttle.entries)
	}
	if _, ok := throttle.recent["client:a"]; !ok {
		t.Fatal("successful client not remembered")
	}
}

func TestThrottleFifthReservedFailureSetsDelay(t *testing.T) {
	throttle := newLoginThrottle()
	now := time.Unix(100, 0)
	throttle.now = func() time.Time { return now }
	for attempt := 1; attempt <= throttleFreeFailures; attempt++ {
		reservation, decision, _ := throttle.Check("client:a", "account:x")
		if decision != throttleAllowed {
			t.Fatalf("attempt %d decision = %d", attempt, decision)
		}
		delay := throttle.Failure(reservation)
		if want := attempt == throttleFreeFailures; (delay > 0) != want {
			t.Fatalf("attempt %d delay = %s", attempt, delay)
		}
	}
	if _, decision, delay := throttle.Check("client:a", "account:x"); decision != throttleLimited || delay != time.Second {
		t.Fatalf("sixth attempt = %d %s", decision, delay)
	}
}

func TestThrottleAccountLockoutExemptsRecentClient(t *testing.T) {
	throttle := newLoginThrottle()
	now := time.Unix(100, 0)
	throttle.now = func() time.Time { return now }
	const account = "account:admin"
	proven, _, _ := throttle.Check("client:home", account)
	throttle.Success(proven)

	// Attackers from many clients lock the account key.
	for i := 0; i < 10; i++ {
		reservation, decision, _ := throttle.Check(fmt.Sprintf("client:attacker-%d", i), account)
		if decision == throttleLimited {
			break
		}
		throttle.Failure(reservation)
	}
	if _, decision, _ := throttle.Check("client:unproven", account); decision != throttleLimited {
		t.Fatalf("unproven client decision = %d, want account lockout", decision)
	}
	home, decision, _ := throttle.Check("client:home", account)
	if decision != throttleAllowed || !home.recent {
		t.Fatalf("recent client decision = %d recent=%v", decision, home.recent)
	}
	// A recent client also ignores another client's in-flight attempt on
	// the account and still counts toward it.
	before := throttle.entries[account].failures
	throttle.Failure(home)
	if after := throttle.entries[account].failures; after != before {
		t.Fatalf("recent failure not kept on the account: %d -> %d", before, after)
	}

	// The recent client remains subject to its own client throttle.
	for i := 0; i < throttleFreeFailures; i++ {
		reservation, decision, _ := throttle.Check("client:home", account)
		if decision != throttleAllowed {
			break
		}
		throttle.Failure(reservation)
	}
	if _, decision, _ := throttle.Check("client:home", account); decision != throttleLimited {
		t.Fatalf("recent client escaped its own throttle: %d", decision)
	}

	// The exemption lasts seven days.
	now = now.Add(recentClientLifetime)
	throttle.entries = map[string]throttleEntry{account: {failures: 9, retryAfter: now.Add(time.Minute), lastSeen: now}}
	if _, decision, _ := throttle.Check("client:home", account); decision != throttleLimited {
		t.Fatalf("expired recent client decision = %d", decision)
	}
}

func TestThrottleRecentClientsAndInFlightEntriesAreBounded(t *testing.T) {
	throttle := newLoginThrottle()
	now := time.Unix(100, 0)
	throttle.now = func() time.Time { return now }
	for i := 0; i < recentClientCapacity+10; i++ {
		reservation, _, _ := throttle.Check(fmt.Sprintf("client:%d", i), fmt.Sprintf("account:%d", i))
		throttle.Success(reservation)
		now = now.Add(time.Second)
	}
	if len(throttle.recent) != recentClientCapacity {
		t.Fatalf("recent clients = %d", len(throttle.recent))
	}
	if _, ok := throttle.recent["client:0"]; ok {
		t.Fatal("oldest recent client was not evicted")
	}

	// An in-flight entry survives both capacity eviction and idle cleanup.
	held, _, _ := throttle.Check("client:held", "account:held")
	for i := 0; i < throttleCapacity+10; i++ {
		reservation, _, _ := throttle.Check(fmt.Sprintf("client:f%d", i), fmt.Sprintf("account:f%d", i))
		throttle.Failure(reservation)
	}
	now = now.Add(throttleIdle + time.Second)
	if _, decision, _ := throttle.Check("client:held", "account:other"); decision != throttleInFlight {
		t.Fatalf("in-flight entry lost: decision = %d", decision)
	}
	if len(throttle.entries) > throttleCapacity {
		t.Fatalf("entries = %d", len(throttle.entries))
	}
	throttle.Release(held)
}

func waitForGate(t *testing.T, gate *hashGate, condition func(active int, waiting [hashLaneCount]int) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		active, waiting := gate.snapshot()
		if condition(active, waiting) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate never reached the condition: active=%d waiting=%v", active, waiting)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHashGateBoundedLanesFailFastAndServeRecentFirst(t *testing.T) {
	gate := newHashGate(2, time.Hour, 3, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := gate.acquire(ctx, hashLaneInternal); err != nil {
			t.Fatal(err)
		}
	}
	order := make(chan string, 8)
	var group sync.WaitGroup
	enqueue := func(name string, lane hashLane, queued int) {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := gate.acquire(ctx, lane); err != nil {
				t.Error(name, err)
				return
			}
			order <- name
		}()
		waitForGate(t, gate, func(_ int, waiting [hashLaneCount]int) bool {
			return waiting[lane] == queued
		})
	}
	enqueue("general-1", hashLaneGeneral, 1)
	enqueue("general-2", hashLaneGeneral, 2)
	enqueue("general-3", hashLaneGeneral, 3)
	enqueue("recent-1", hashLaneRecent, 1)
	enqueue("recent-2", hashLaneRecent, 2)

	started := time.Now()
	if err := gate.acquire(ctx, hashLaneGeneral); !errors.Is(err, errHashOverloaded) {
		t.Fatalf("general overflow = %v", err)
	}
	if err := gate.acquire(ctx, hashLaneRecent); !errors.Is(err, errHashOverloaded) {
		t.Fatalf("recent overflow = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("overflow took %s", elapsed)
	}

	want := []string{"recent-1", "recent-2", "general-1", "general-2", "general-3"}
	for _, name := range want {
		gate.release()
		if got := <-order; got != name {
			t.Fatalf("granted %q, want %q", got, name)
		}
	}
	group.Wait()
	// The last two grantees now hold the two slots.
	gate.release()
	gate.release()
	if active, waiting := gate.snapshot(); active != 0 || waiting != [hashLaneCount]int{} {
		t.Fatalf("gate not drained: active=%d waiting=%v", active, waiting)
	}
}

func TestHashGateExpiredWaitAndCancellationLeaveNoWaiter(t *testing.T) {
	gate := newHashGate(1, 20*time.Millisecond, 8, 2)
	if err := gate.acquire(context.Background(), hashLaneInternal); err != nil {
		t.Fatal(err)
	}
	if err := gate.acquire(context.Background(), hashLaneGeneral); !errors.Is(err, errHashOverloaded) {
		t.Fatalf("expired wait = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.acquire(ctx, hashLaneInternal); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	if _, waiting := gate.snapshot(); waiting != [hashLaneCount]int{} {
		t.Fatalf("waiters left behind: %v", waiting)
	}
	gate.release()
	if active, _ := gate.snapshot(); active != 0 {
		t.Fatalf("active = %d", active)
	}
}

// holdHashSlots occupies every process-wide hash slot until the returned
// function runs, so sign-in requests queue deterministically.
func holdHashSlots(t *testing.T) func() {
	t.Helper()
	for i := 0; i < hashSlots; i++ {
		if err := passwordHashing.acquire(context.Background(), hashLaneInternal); err != nil {
			t.Fatal(err)
		}
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := 0; i < hashSlots; i++ {
				passwordHashing.release()
			}
		})
	}
	t.Cleanup(release)
	return release
}

// throttleEntry reads one entry under the throttle lock.
func (f *browserFixture) throttleEntry(key string) throttleEntry {
	f.browser.throttle.mu.Lock()
	defer f.browser.throttle.mu.Unlock()
	return f.browser.throttle.entries[key]
}

func loginFrom(username, password, remoteAddr string) *http.Request {
	request := loginRequest(username, password, "/logs")
	request.RemoteAddr = remoteAddr
	return request
}

func countAuditRows(t *testing.T, fixture *browserFixture) int {
	t.Helper()
	var count int
	if err := fixture.db.Reader().QueryRow("SELECT count(*) FROM security_audit_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLoginParallelWrongPasswordsCannotMultiplyGuesses(t *testing.T) {
	admin := func(int) string { return "Admin" }
	distinct := func(i int) string { return fmt.Sprintf("guess-%d", i) }
	for _, test := range []struct {
		name     string
		remote   func(int) string
		username func(int) string
		key      string
	}{
		{"one client many accounts", func(int) string { return "192.0.2.10:1234" }, distinct,
			clientThrottleKey("192.0.2.10")},
		{"one IPv6 /64 many accounts", func(i int) string { return fmt.Sprintf("[2001:db8:5:6::%x]:1234", i+1) },
			distinct, clientThrottleKey("2001:db8:5:6::1")},
		{"many clients one account", func(i int) string { return fmt.Sprintf("198.51.100.%d:1234", i+1) },
			admin, accountThrottleKey("Admin")},
	} {
		t.Run(test.name, func(t *testing.T) {
			const parallel = 16
			fixture := newBrowserFixture(t, "https://logs.example.test", true)
			release := holdHashSlots(t)
			auditBefore := countAuditRows(t, fixture)
			codes := make(chan int, parallel)
			var group sync.WaitGroup
			for i := 0; i < parallel; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					response := httptest.NewRecorder()
					fixture.handler().ServeHTTP(response, loginFrom(test.username(i), "wrong-password", test.remote(i)))
					if response.Code == http.StatusTooManyRequests && response.Header().Get("Retry-After") == "" {
						t.Error("429 without Retry-After")
					}
					codes <- response.Code
				}()
			}
			// Exactly one attempt reaches the hash queue; every other one is
			// refused at once while the slots are held.
			rejected := 0
			for rejected < parallel-1 {
				if code := <-codes; code != http.StatusTooManyRequests {
					t.Fatalf("concurrent attempt status = %d, want 429", code)
				}
				rejected++
			}
			waitForGate(t, passwordHashing, func(_ int, waiting [hashLaneCount]int) bool {
				return waiting[hashLaneGeneral] == 1
			})
			if got := countAuditRows(t, fixture); got != auditBefore {
				t.Fatalf("in-flight rejections wrote %d audit rows", got-auditBefore)
			}
			release()
			group.Wait()
			if code := <-codes; code != http.StatusUnauthorized {
				t.Fatalf("admitted attempt status = %d", code)
			}
			if failures := fixture.throttleEntry(test.key).failures; failures != 1 {
				t.Fatalf("counted guesses = %d, want 1", failures)
			}
		})
	}
}

func TestLoginHashQueueOverflowIsFast429WithoutAudit(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	release := holdHashSlots(t)
	var group sync.WaitGroup
	for i := 0; i < hashGeneralQueue; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response := httptest.NewRecorder()
			fixture.handler().ServeHTTP(response,
				loginFrom(fmt.Sprintf("queued-%d", i), "wrong-password", fmt.Sprintf("203.0.113.%d:1234", i+1)))
		}()
	}
	waitForGate(t, passwordHashing, func(_ int, waiting [hashLaneCount]int) bool {
		return waiting[hashLaneGeneral] == hashGeneralQueue
	})
	auditBefore := countAuditRows(t, fixture)
	started := time.Now()
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginFrom("overflow", "wrong-password", "192.0.2.99:1234"))
	elapsed := time.Since(started)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "2" {
		t.Fatalf("overflow = %d Retry-After %q", response.Code, response.Header().Get("Retry-After"))
	}
	if elapsed > time.Second {
		t.Fatalf("overflow reply took %s", elapsed)
	}
	if got := countAuditRows(t, fixture); got != auditBefore {
		t.Fatalf("overflow wrote %d audit rows", got-auditBefore)
	}
	if entry := fixture.throttleEntry(clientThrottleKey("192.0.2.99")); entry.failures != 0 || entry.inFlight != 0 {
		t.Fatalf("overflow was counted as a guess: %+v", entry)
	}
	release()
	group.Wait()
}

func TestLoginFloodLeavesRecentAdministratorAbleToSignIn(t *testing.T) {
	fixture := newBrowserFixture(t, "https://logs.example.test", true)
	const home = "192.0.2.50:1234"
	first := httptest.NewRecorder()
	fixture.handler().ServeHTTP(first, loginFrom("Admin", "browser-password", home))
	if first.Code != http.StatusSeeOther {
		t.Fatalf("initial sign-in = %d", first.Code)
	}

	// An attacker has already locked the account key from elsewhere.
	throttle := fixture.browser.throttle
	throttle.mu.Lock()
	throttle.entries[accountThrottleKey("Admin")] = throttleEntry{
		failures: 11, retryAfter: time.Now().Add(throttleMaxDelay), lastSeen: time.Now(),
	}
	throttle.mu.Unlock()

	// A flood from rotating IPv6 /64s guesses the administrator account and
	// random usernames, saturating the general hash queue.
	stop := make(chan struct{})
	var flood sync.WaitGroup
	var sequence, overloaded atomic.Int64
	for worker := 0; worker < 3*hashGeneralQueue; worker++ {
		flood.Add(1)
		go func() {
			defer flood.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n := sequence.Add(1)
				username := "Admin"
				if n%2 == 0 {
					username = fmt.Sprintf("guess-%d", n)
				}
				response := httptest.NewRecorder()
				fixture.handler().ServeHTTP(response,
					loginFrom(username, "wrong-password", fmt.Sprintf("[2001:db8:%x:%x::1]:1234", n>>16, n&0xffff)))
				if response.Code == http.StatusTooManyRequests && response.Header().Get("Retry-After") == "2" {
					overloaded.Add(1)
				}
			}
		}()
	}
	defer func() {
		close(stop)
		flood.Wait()
	}()
	waitForGate(t, passwordHashing, func(_ int, waiting [hashLaneCount]int) bool {
		return waiting[hashLaneGeneral] == hashGeneralQueue
	})
	deadline := time.Now().Add(30 * time.Second)
	for overloaded.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("flood never overloaded the hash queue")
		}
		time.Sleep(time.Millisecond)
	}

	// An unproven client is locked out of the account by the flood ...
	unproven, unprovenDecision, _ := throttle.Check(
		clientThrottleKey("192.0.2.77"), accountThrottleKey("Admin"),
	)
	if unprovenDecision == throttleAllowed {
		throttle.Release(unproven)
		t.Fatal("unproven client was admitted to a flooded, locked account")
	}

	// ... while the recently successful client signs in. A 303 proves its
	// wait stayed inside hashQueueWait: an expired wait would be a 429.
	started := time.Now()
	response := httptest.NewRecorder()
	fixture.handler().ServeHTTP(response, loginFrom("Admin", "browser-password", home))
	elapsed := time.Since(started)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("recent administrator during flood = %d after %s", response.Code, elapsed)
	}
	t.Logf("recent administrator signed in after %s during a flood of %d requests (%d overloaded)",
		elapsed, sequence.Load(), overloaded.Load())
}

func TestThrottleCapacityEvictionKeepsThrottledKeys(t *testing.T) {
	throttle := newLoginThrottle()
	now := time.Unix(100, 0)
	throttle.now = func() time.Time { return now }
	// Five counted failures from different clients lock the account key.
	for i := 0; i < throttleFreeFailures; i++ {
		reservation, decision, _ := throttle.Check(fmt.Sprintf("client:a%d", i), "account:admin")
		if decision != throttleAllowed {
			t.Fatalf("attempt %d: decision = %d", i, decision)
		}
		throttle.Failure(reservation)
	}
	if _, decision, _ := throttle.Check("client:fresh", "account:admin"); decision != throttleLimited {
		t.Fatalf("account was not locked: decision = %d", decision)
	}

	// A flood of withdrawn reservations on fresh usernames fills the map
	// with idle, zero-failure entries. Capacity eviction must drop those,
	// never a key that still carries a failure history or a retry delay.
	for i := 0; i < throttleCapacity+10; i++ {
		reservation, decision, _ := throttle.Check("client:flood", fmt.Sprintf("account:z%d", i))
		if decision != throttleAllowed {
			t.Fatalf("flood attempt %d: decision = %d", i, decision)
		}
		throttle.Release(reservation)
		now = now.Add(time.Microsecond)
	}
	if len(throttle.entries) > throttleCapacity {
		t.Fatalf("entries = %d", len(throttle.entries))
	}
	if _, decision, _ := throttle.Check("client:fresh", "account:admin"); decision != throttleLimited {
		t.Fatalf("account lock was evicted by the flood: decision = %d", decision)
	}
	for i := 0; i < throttleFreeFailures; i++ {
		if entry, ok := throttle.entries[fmt.Sprintf("client:a%d", i)]; !ok || entry.failures != 1 {
			t.Fatalf("failure history of client a%d was evicted: %+v, present=%v", i, entry, ok)
		}
	}
}
