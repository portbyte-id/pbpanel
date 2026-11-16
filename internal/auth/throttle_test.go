package auth

import (
	"fmt"
	"testing"
	"time"
)

const (
	testMaxAttempts = 3
	testLockout     = 15 * time.Minute
)

func newThrottle(t *testing.T, clock Clock) *Throttle {
	t.Helper()
	return NewThrottle(clock,
		func() int { return testMaxAttempts },
		func() time.Duration { return testLockout },
	)
}

// allowed is the common assertion: Check reports no lockout for ip.
func allowed(th *Throttle, ip string) bool {
	_, locked := th.Check(ip)
	return !locked
}

func TestThrottleLocksOutAfterMaxAttempts(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)
	const ip = "203.0.113.7"

	for i := 1; i < testMaxAttempts; i++ {
		if _, locked := th.Fail(ip); locked {
			t.Fatalf("locked out after %d failures, want %d", i, testMaxAttempts)
		}
		if !allowed(th, ip) {
			t.Fatalf("not allowed after %d failures, want allowed", i)
		}
	}

	until, locked := th.Fail(ip)
	if !locked {
		t.Fatalf("not locked out after %d failures", testMaxAttempts)
	}
	if want := clock.Now().Add(testLockout); !until.Equal(want) {
		t.Errorf("lockout until %v, want %v", until, want)
	}
	if allowed(th, ip) {
		t.Fatal("Check reported allowed while locked out")
	}
}

func TestThrottleRecoversAfterTheLockoutWindow(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)
	const ip = "203.0.113.7"

	for range testMaxAttempts {
		th.Fail(ip)
	}
	if allowed(th, ip) {
		t.Fatal("Check reported allowed inside the lockout window")
	}

	// One nanosecond short of the window, still locked.
	clock.advance(testLockout - time.Nanosecond)
	if allowed(th, ip) {
		t.Fatal("Check reported allowed before the lockout expired")
	}

	clock.advance(time.Nanosecond)
	if !allowed(th, ip) {
		t.Fatal("Check reported locked after the lockout expired")
	}
}

// The lockout is fixed, not extended: a client that keeps knocking must not be
// able to push its own deadline out.
func TestThrottleDoesNotExtendAnActiveLockout(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)
	const ip = "203.0.113.7"

	for range testMaxAttempts {
		th.Fail(ip)
	}
	first, _ := th.LockedUntil(ip)

	clock.advance(time.Minute)
	th.Fail(ip)
	th.Fail(ip)

	got, locked := th.LockedUntil(ip)
	if !locked {
		t.Fatal("LockedUntil reports no lockout")
	}
	if !got.Equal(first) {
		t.Fatalf("lockout moved from %v to %v; it must not be extended", first, got)
	}
}

func TestThrottleCountsPerIPIndependently(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)

	const offender, bystander = "203.0.113.7", "203.0.113.8"
	for range testMaxAttempts {
		th.Fail(offender)
	}
	if allowed(th, offender) {
		t.Fatal("the offender is not locked out")
	}
	if !allowed(th, bystander) {
		t.Fatal("an unrelated IP was locked out by another IP's failures")
	}
}

// The global counter is what stops an attacker who rotates source addresses;
// without it, per-IP limits alone are defeated by any pool of addresses.
func TestThrottleLocksGlobally(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)

	// Distinct IPs each stay below the per-IP limit, so only the global counter
	// can be what locks.
	for i := range testMaxAttempts * globalMultiplier {
		th.Fail(fmt.Sprintf("198.51.%d.%d", i/250, i%250+1))
	}

	if allowed(th, "192.0.2.99") {
		t.Fatal("Check reported allowed while the global counter is locked")
	}
	if _, locked := th.LockedUntil("192.0.2.99"); !locked {
		t.Fatal("LockedUntil did not report the global lockout")
	}

	clock.advance(testLockout)
	if !allowed(th, "192.0.2.99") {
		t.Fatal("the global lockout did not expire")
	}
}

// The global limit must not be reachable by one ordinary client: with a single
// admin account, a global lockout at the per-IP threshold would let a handful of
// bad requests from anywhere deny the operator access to their own server.
func TestGlobalLimitIsLooserThanPerIP(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)

	for i := range testMaxAttempts {
		th.Fail(fmt.Sprintf("198.51.100.%d", i+1))
	}
	if _, locked := th.Check("192.0.2.99"); locked {
		t.Fatalf("%d failures across distinct IPs locked the global counter; it must be looser than the per-IP limit", testMaxAttempts)
	}
}

// A successful login clears that IP's count but not the global one: one success
// must not erase the failures every other client produced.
func TestThrottleResetClearsOnlyThatIP(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)
	const ip = "203.0.113.7"

	th.Fail(ip)
	th.Fail(ip)
	th.Reset(ip)

	// One more failure now leaves the count at one; without the reset it would
	// already be at three and locked.
	if _, locked := th.Fail(ip); locked {
		t.Fatal("locked out after a reset plus one failure")
	}
	if !allowed(th, ip) {
		t.Fatal("not allowed after a reset plus one failure")
	}
}

func TestThrottleGCForgetsExpiredEntries(t *testing.T) {
	clock := newFakeClock()
	th := newThrottle(t, clock)

	for _, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		th.Fail(ip)
	}
	if got := len(th.perIP); got != 3 {
		t.Fatalf("tracked %d IPs, want 3", got)
	}

	// Past the GC interval and past the lockout, the next call drops all three.
	clock.advance(th.gcEvery + testLockout + time.Minute)
	th.Check("203.0.113.4")
	if got := len(th.perIP); got != 0 {
		t.Fatalf("tracked %d IPs after GC, want 0", got)
	}

	// A recent count survives the same sweep, so GC ages entries rather than
	// clearing the map.
	th.Fail("203.0.113.5")
	clock.advance(time.Minute)
	th.Check("203.0.113.6")
	if got := len(th.perIP); got != 1 {
		t.Fatalf("tracked %d IPs after a second GC, want 1 (the recent entry)", got)
	}
}
