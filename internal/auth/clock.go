package auth

import "time"

// Clock is the package's only source of time. Idle expiry, lockout windows and
// every stored timestamp read it, so tests can move time instead of sleeping.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }
