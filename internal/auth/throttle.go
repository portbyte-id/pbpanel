package auth

import (
	"sync"
	"time"
)

// globalMultiplier scales the global limit relative to the per-IP one. It is not
// 1 on purpose: the panel has exactly one admin, so a global lockout at the same
// threshold would let any three bad requests from anywhere lock the operator out
// of their own server — a denial of service that costs the attacker nothing. A
// looser global limit still catches the case per-IP limits cannot: one attacker
// rotating through many source addresses.
const globalMultiplier = 10

// Throttle counts failed logins per client IP and globally, and locks an
// exhausted counter out for a fixed window.
//
// The counters live in memory. They are lost on a daemon restart, which is
// acceptable: a restart is root-only, and the alternative — a table written on
// every failed login — would let an unauthenticated client force database writes
// for free.
type Throttle struct {
	clock Clock

	// Limits are read per call rather than captured at construction, so the
	// settings registry can change them without a restart.
	maxAttempts func() int
	lockout     func() time.Duration

	mu      sync.Mutex
	perIP   map[string]*counter
	global  counter
	lastGC  time.Time
	gcEvery time.Duration
}

type counter struct {
	failures    int
	lockedUntil time.Time
	// lastFailure is what GC ages an entry by. Without it a partial count would
	// never be dropped, and a slow scan over many addresses would grow the map
	// for as long as the daemon ran.
	lastFailure time.Time
}

// NewThrottle returns a throttle reading its limits through maxAttempts and
// lockout. Both must be positive; a zero would either lock every client out
// immediately or never lock at all, and neither is a sane default to discover
// at runtime.
func NewThrottle(clock Clock, maxAttempts func() int, lockout func() time.Duration) *Throttle {
	return &Throttle{
		clock:       clock,
		maxAttempts: maxAttempts,
		lockout:     lockout,
		perIP:       make(map[string]*counter),
		gcEvery:     time.Hour,
	}
}

// Check reports whether ip may attempt a login now, and when the lockout ends if
// it may not. It reads once, so the two answers cannot disagree the way separate
// Allowed and LockedUntil calls could across a lockout boundary.
//
// It is checked before the password is verified, so a locked-out client costs no
// argon2 work.
func (t *Throttle) Check(ip string) (retryAt time.Time, locked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.gcLocked(now)
	return t.lockedUntilLocked(ip, now)
}

// Fail records one failed attempt for ip and returns the lockout deadline that
// applies afterwards, or the zero time when neither counter is locked. A client
// that is already locked out is not counted again: the lockout is fixed, not
// extended, so a legitimate user's wait is bounded by the settings and not by
// how often an attacker keeps knocking.
func (t *Throttle) Fail(ip string) (until time.Time, locked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	t.gcLocked(now)

	max := t.maxAttempts()
	window := t.lockout()

	c := t.perIP[ip]
	if c == nil {
		c = &counter{}
		t.perIP[ip] = c
	}
	if !c.locked(now) {
		c.failures++
		c.lastFailure = now
		if c.failures >= max {
			c.lockedUntil = now.Add(window)
			c.failures = 0
		}
	}

	if !t.global.locked(now) {
		t.global.failures++
		if t.global.failures >= max*globalMultiplier {
			t.global.lockedUntil = now.Add(window)
			t.global.failures = 0
		}
	}

	until, locked = c.lockedUntil, c.locked(now)
	if t.global.locked(now) && (!locked || t.global.lockedUntil.After(until)) {
		until, locked = t.global.lockedUntil, true
	}
	return until, locked
}

// Reset clears ip's counter after a successful login. The global counter is left
// alone: one success does not undo the failures every other client caused.
func (t *Throttle) Reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.perIP, ip)
}

// Clear forgets every counter and lockout, per address and global. It is what an
// operator-issued unlock (`pbp unlock-login`) ends in: the lockout is a real
// control, but the one person it can lock out must have a way back in.
func (t *Throttle) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.perIP = make(map[string]*counter)
	t.global = counter{}
}

// LockedUntil returns when ip or the global counter unlocks, and false when
// neither is locked. The HTTP layer uses it for the Retry-After header.
func (t *Throttle) LockedUntil(ip string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.lockedUntilLocked(ip, t.clock.Now())
}

// lockedUntilLocked is the read shared by Check and LockedUntil. The caller
// holds t.mu and has already read the clock, so both callers agree on "now".
func (t *Throttle) lockedUntilLocked(ip string, now time.Time) (time.Time, bool) {
	until, locked := time.Time{}, false
	if c := t.perIP[ip]; c != nil && c.locked(now) {
		until, locked = c.lockedUntil, true
	}
	if t.global.locked(now) && (!locked || t.global.lockedUntil.After(until)) {
		until, locked = t.global.lockedUntil, true
	}
	return until, locked
}

func (c *counter) locked(now time.Time) bool {
	return now.Before(c.lockedUntil)
}

// gcLocked drops per-IP entries whose lockout has expired, so a scan over many
// source addresses cannot grow the map without bound. It runs on a timer rather
// than on every call: walking the map on each login would make the cost of a
// login depend on how many IPs have ever failed.
func (t *Throttle) gcLocked(now time.Time) {
	if now.Sub(t.lastGC) < t.gcEvery {
		return
	}
	t.lastGC = now
	for ip, c := range t.perIP {
		// A count older than the GC interval is not worth keeping: failures an
		// hour apart should not add up to a lockout, and dropping them is what
		// bounds the map.
		if !c.locked(now) && now.Sub(c.lastFailure) >= t.gcEvery {
			delete(t.perIP, ip)
		}
	}
}
