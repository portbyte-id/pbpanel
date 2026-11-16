package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentLoginsCannotOutrunTheThrottle is the regression for the 0.15
// finding that Check and Fail were separate steps: N parallel guesses all passed
// Check before any had been counted, so the lockout bounded only sequential
// attempts. The stub verify sleeps so the guesses genuinely overlap; with the
// login serialised, exactly testMaxAttempts of them reach verification and the
// rest are refused before any argon2 work.
func TestConcurrentLoginsCannotOutrunTheThrottle(t *testing.T) {
	svc, _, _ := seeded(t)

	var verifies atomic.Int64
	svc.verify = func(string, string) (bool, error) {
		verifies.Add(1)
		time.Sleep(5 * time.Millisecond)
		return false, nil
	}

	const parallel = 40
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.VerifyLogin(context.Background(), testUser, "guess", "203.0.113.50", "test-agent")
		}()
	}
	wg.Wait()

	if got := verifies.Load(); got != int64(testMaxAttempts) {
		t.Fatalf("%d parallel guesses reached verification %d times, want exactly %d (the throttle limit)",
			parallel, got, testMaxAttempts)
	}
}
