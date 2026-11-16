package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Cost parameters. 64 MiB over three passes is RFC 9106's second recommended
// argon2id setting: a login stays around a tenth of a second on the kind of
// small server the panel targets, while an offline attacker pays that cost for
// every guess.
const (
	argonMemory      uint32 = 64 * 1024 // KiB
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonSaltLen            = 16
	argonKeyLen             = 32
)

// Bounds applied when decoding. The encoded string comes out of the database,
// so it is only as trustworthy as the database; without these a corrupted or
// tampered row asking for 4 TiB of memory would take the daemon down instead of
// failing one login.
const (
	maxArgonMemory      = 1 << 20 // KiB, 1 GiB
	maxArgonIterations  = 16
	maxArgonParallelism = 64
	maxArgonSaltLen     = 1024
	maxArgonKeyLen      = 1024
)

// ErrInvalidHash reports an encoded hash this package cannot read: wrong
// algorithm, unknown version, out-of-range parameters, or bad base64.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// argonParams are the costs recorded alongside a hash. They travel in the
// encoded string, so raising the defaults never invalidates an existing
// password: verification uses whatever the stored hash was made with.
type argonParams struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	saltLen     uint32
	keyLen      uint32
}

// DefaultUsername is the single admin account's name (decision 3: one account,
// no roles). Install creates it and `panel reset-password` keeps it.
const DefaultUsername = "admin"

// PasswordAlphabet leaves out the characters an operator reading a console
// confuses: 0 and O, 1 and I, l.
const PasswordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratedPasswordLen characters from a 57-symbol alphabet is about 116 bits,
// well past the point where the password itself is the weak link.
const GeneratedPasswordLen = 20

// GeneratePassword returns a random password drawn from PasswordAlphabet. It is
// the one generator: install and `panel reset-password` both call it, so a
// password from either path has the same shape.
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(PasswordAlphabet)))
	out := make([]byte, GeneratedPasswordLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("auth: generate password: %w", err)
		}
		out[i] = PasswordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// Hash derives an argon2id hash of password with the package's current costs and
// returns it as a PHC string:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
//
// The salt and the parameters are part of the string, which is what lets Verify
// work without any other stored state.
func Hash(password string) (string, error) {
	return hashWith(password, argonParams{
		memory:      argonMemory,
		iterations:  argonIterations,
		parallelism: argonParallelism,
		saltLen:     argonSaltLen,
		keyLen:      argonKeyLen,
	})
}

func hashWith(password string, p argonParams) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.parallelism, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		p.memory, p.iterations, p.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches encoded. The cost parameters are read
// back out of encoded, so a hash made with older parameters still verifies.
//
// The error return is for a hash that cannot be parsed at all; a wrong password
// is (false, nil), not an error, because it is an expected outcome rather than a
// fault.
func Verify(encoded, password string) (bool, error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.iterations, p.memory, p.parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// decodeHash parses the PHC string. It is strict on purpose: a lenient parser
// would let a malformed row verify against something it was never meant to.
func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var zero argonParams

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return zero, nil, nil, fmt.Errorf("%w: expected six $-separated fields", ErrInvalidHash)
	}
	if parts[1] != "argon2id" {
		return zero, nil, nil, fmt.Errorf("%w: algorithm %q", ErrInvalidHash, parts[1])
	}

	version, err := paramValue(parts[2], "v=")
	if err != nil || version != uint64(argon2.Version) {
		return zero, nil, nil, fmt.Errorf("%w: version %q", ErrInvalidHash, parts[2])
	}

	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return zero, nil, nil, fmt.Errorf("%w: parameters %q", ErrInvalidHash, parts[3])
	}
	memory, err := paramValue(fields[0], "m=")
	if err != nil {
		return zero, nil, nil, fmt.Errorf("%w: parameters %q", ErrInvalidHash, parts[3])
	}
	iterations, err := paramValue(fields[1], "t=")
	if err != nil {
		return zero, nil, nil, fmt.Errorf("%w: parameters %q", ErrInvalidHash, parts[3])
	}
	parallelism, err := paramValue(fields[2], "p=")
	if err != nil {
		return zero, nil, nil, fmt.Errorf("%w: parameters %q", ErrInvalidHash, parts[3])
	}
	if memory == 0 || memory > maxArgonMemory ||
		iterations == 0 || iterations > maxArgonIterations ||
		parallelism == 0 || parallelism > maxArgonParallelism {
		return zero, nil, nil, fmt.Errorf("%w: parameters %q are out of range", ErrInvalidHash, parts[3])
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) == 0 || len(salt) > maxArgonSaltLen {
		return zero, nil, nil, fmt.Errorf("%w: salt", ErrInvalidHash)
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) == 0 || len(key) > maxArgonKeyLen {
		return zero, nil, nil, fmt.Errorf("%w: key", ErrInvalidHash)
	}

	return argonParams{
		memory:      uint32(memory),
		iterations:  uint32(iterations),
		parallelism: uint8(parallelism),
		saltLen:     uint32(len(salt)),
		keyLen:      uint32(len(key)),
	}, salt, key, nil
}

func paramValue(field, prefix string) (uint64, error) {
	if !strings.HasPrefix(field, prefix) {
		return 0, ErrInvalidHash
	}
	return strconv.ParseUint(field[len(prefix):], 10, 64)
}
