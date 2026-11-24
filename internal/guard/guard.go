// Package guard is the challenge page a site can put in front of its visitors,
// the way a CDN's "checking your browser" screen works, but served by this host.
//
// The work is split so nginx never waits on the panel for a visitor who already
// passed:
//
//   - nginx checks the clearance cookie itself with the secure_link module: an
//     MD5 over the expiry, the client address, the user agent and a per-site
//     secret. A valid cookie costs nginx one hash and nothing else.
//   - A visitor without one gets a static page nginx serves from disk (status
//     403), so a flood of clients that do not run JavaScript never reaches the
//     panel either.
//   - Only a browser that runs the page talks to the panel, over a unix socket:
//     it fetches a proof-of-work puzzle, or passes a Cloudflare Turnstile token,
//     and gets the cookie back.
//
// The per-site secrets are derived from the panel's data/secret.key, so nothing
// new is stored and a site's secret never changes between restarts.
package guard

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // nginx's secure_link module only speaks MD5; the secret makes it a keyed hash.
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Provider is what a visitor has to pass.
type Provider string

// Providers.
const (
	// ProviderPoW is a SHA-256 proof of work the browser solves in JavaScript.
	// Nothing leaves the server.
	ProviderPoW Provider = "pow"
	// ProviderTurnstile is Cloudflare Turnstile. The visitor's browser talks to
	// Cloudflare, and the panel checks the token with Cloudflare's siteverify.
	ProviderTurnstile Provider = "turnstile"
)

// Paths under the site that belong to the challenge. nginx routes the prefix to
// the panel and serves the page at PagePath; neither is ever challenged.
const (
	Prefix     = "/.pb-challenge/"
	NewPath    = Prefix + "new"
	VerifyPath = Prefix + "verify"
	// ConfigPath returns the Turnstile site key the page renders the widget with.
	ConfigPath = Prefix + "config"
	PagePath   = "/.pb-guard.html"
)

// Headers nginx sets on what it passes to the panel. The socket is reachable
// from this host only, so they are trusted.
const (
	HeaderSite   = "X-PB-Site"
	HeaderClient = "X-PB-Client"
	HeaderScheme = "X-PB-Scheme"
)

const (
	// puzzleTTL is how long a fetched puzzle can be solved for.
	puzzleTTL = 5 * time.Minute
	// maxNonce bounds the nonce a browser sends back.
	maxNonce = 20
)

// ErrNoKey is returned when the panel has no secret key to derive keys from.
var ErrNoKey = errors.New("guard: the panel has no secret key (data/secret.key)")

// Keyring derives the per-site secrets from the panel's master key.
type Keyring struct {
	master []byte
}

// NewKeyring takes the 32-byte data/secret.key.
func NewKeyring(master []byte) (*Keyring, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("guard: master key is %d bytes, want 32", len(master))
	}
	return &Keyring{master: append([]byte(nil), master...)}, nil
}

func (k *Keyring) derive(purpose, domain string) []byte {
	key, err := hkdf.Key(sha256.New, k.master, nil, "pbpanel guard v1 "+purpose+" "+domain, 32)
	if err != nil {
		// hkdf.Key fails only for a length above 255 hash lengths.
		panic(err)
	}
	return key
}

// CookieSecret is the secret nginx's secure_link_md5 line holds for a site. It
// is hex, so it needs no quoting in a configuration file.
func (k *Keyring) CookieSecret(domain string) string {
	return hex.EncodeToString(k.derive("cookie", domain)[:16])
}

// puzzleKey signs the proof-of-work puzzles of a site.
func (k *Keyring) puzzleKey(domain string) []byte { return k.derive("puzzle", domain) }

// SecureLinkMD5 is the expression nginx hashes, with the site's secret. Clearance
// computes the same string, so the two must change together.
func SecureLinkMD5(secret string) string {
	return "$secure_link_expires|$remote_addr|$http_user_agent|" + secret
}

// Clearance is the cookie value nginx's secure_link accepts until expires: the
// URL-safe base64 MD5 of the SecureLinkMD5 expression, a comma, the expiry.
func Clearance(secret string, expires int64, ip, ua string) string {
	sum := md5.Sum([]byte(strconv.FormatInt(expires, 10) + "|" + ip + "|" + ua + "|" + secret)) //nolint:gosec // see the import
	return base64.RawURLEncoding.EncodeToString(sum[:]) + "," + strconv.FormatInt(expires, 10)
}

// puzzleMAC signs a puzzle for one client, so it cannot be solved once and shared.
func puzzleMAC(key []byte, ip, ua, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("puzzle|" + ip + "|" + ua + "|" + payload))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// newPuzzle returns a stateless puzzle token: "<expires>.<random>.<difficulty>.<mac>".
func newPuzzle(key []byte, ip, ua string, difficulty int, now time.Time, random string) string {
	payload := fmt.Sprintf("%d.%s.%d", now.Add(puzzleTTL).Unix(), random, difficulty)
	return payload + "." + puzzleMAC(key, ip, ua, payload)
}

// Puzzle errors, worded for the visitor.
var (
	errPuzzleInvalid = errors.New("the check was not issued to this browser; reload the page")
	errPuzzleExpired = errors.New("the check expired; reload the page")
	errPuzzleWrong   = errors.New("the answer to the check is wrong; reload the page")
)

// checkPuzzle verifies a solved puzzle: sha256(token + ":" + nonce) has to start
// with the token's number of zero bits.
func checkPuzzle(key []byte, ip, ua, token, nonce string, now time.Time) error {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return errPuzzleInvalid
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(parts[3]), []byte(puzzleMAC(key, ip, ua, payload))) {
		return errPuzzleInvalid
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return errPuzzleInvalid
	}
	if now.Unix() > exp {
		return errPuzzleExpired
	}
	difficulty, err := strconv.Atoi(parts[2])
	if err != nil || difficulty < 1 || difficulty > 32 {
		return errPuzzleInvalid
	}
	if nonce == "" || len(nonce) > maxNonce {
		return errPuzzleWrong
	}
	for i := 0; i < len(nonce); i++ {
		if nonce[i] < '0' || nonce[i] > '9' {
			return errPuzzleWrong
		}
	}
	if LeadingZeroBits(sha256.Sum256([]byte(token+":"+nonce))) < difficulty {
		return errPuzzleWrong
	}
	return nil
}

// IssuePuzzle returns a fresh proof-of-work puzzle for one client. key is a
// secret the caller keeps for the life of the process. The puzzle is valid for
// five minutes and only for this address and user agent.
func IssuePuzzle(key []byte, ip, ua string, difficulty int, now time.Time) (string, error) {
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("guard: random puzzle: %w", err)
	}
	return newPuzzle(key, ip, ua, difficulty, now, hex.EncodeToString(rnd[:])), nil
}

// CheckPuzzle reports whether nonce solves a puzzle IssuePuzzle returned for the
// same key, address and user agent. It does not remember answers: the caller
// decides whether a solved puzzle may be used again.
func CheckPuzzle(key []byte, ip, ua, token, nonce string, now time.Time) error {
	return checkPuzzle(key, ip, ua, token, nonce, now)
}

// LeadingZeroBits counts the zero bits a hash starts with.
func LeadingZeroBits(sum [32]byte) int {
	n := 0
	for _, b := range sum {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}
