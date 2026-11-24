package httpapi

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portbyte-id/server-panel/internal/guard"
)

// loginDifficulty is the number of leading zero bits a login check asks for. It
// is about 2^18 hashes: a second or so in a desktop browser, and a real cost for a
// script that keeps guessing.
const loginDifficulty = 18

// errChallengeRequired is the error code for a login that came without a solved
// check while its address has a failed login on record.
const errChallengeRequired = "challenge_required"

// checkDetail is the one sentence a refused check gets. It does not say why the
// check failed, so a client cannot learn whether its answer was stale, wrong or
// already spent.
const checkDetail = "complete the check to continue"

// errCheckSpent is returned when a solved check is offered a second time.
var errCheckSpent = errors.New("login: check already used")

// spentFor is how long a redeemed check is remembered. It is longer than the
// puzzle lives (guard's five minutes), so once the record is forgotten the
// puzzle it answers has expired too, and no answer can be replayed.
const spentFor = 10 * time.Minute

// loginCheck issues the browser checks a login needs and makes each answer
// usable once. The key lives in memory: a restart invalidates outstanding
// checks, and the login page asks for a new one.
type loginCheck struct {
	key []byte
	now func() time.Time

	mu    sync.Mutex
	spent map[string]time.Time // answered "token:nonce" -> when it may be forgotten
}

func newLoginCheck() (*loginCheck, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("login check: random key: %w", err)
	}
	return &loginCheck{key: key, now: time.Now, spent: make(map[string]time.Time)}, nil
}

// issue returns a puzzle for one client. It is valid for five minutes and only
// for that address and user agent.
func (c *loginCheck) issue(ip, ua string) (string, error) {
	return guard.IssuePuzzle(c.key, ip, ua, loginDifficulty, c.now())
}

// redeem spends a solved check. It returns nil only for an answer that solves a
// puzzle issued to this client and has not been spent before.
func (c *loginCheck) redeem(ip, ua, challenge, nonce string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	for k, until := range c.spent {
		if now.After(until) {
			delete(c.spent, k)
		}
	}
	if err := guard.CheckPuzzle(c.key, ip, ua, challenge, nonce, now); err != nil {
		return err
	}
	answer := challenge + ":" + nonce
	if _, used := c.spent[answer]; used {
		return errCheckSpent
	}
	c.spent[answer] = now.Add(spentFor)
	return nil
}
