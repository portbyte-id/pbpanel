// Package secretbox seals small secrets (DNS provider credentials, the ACME
// account key) before they reach SQLite.
//
// The panel already owns data/secret.key, a random 32-byte file only root can
// read. A database backup that leaks without that file must not leak the
// credentials stored in it, so the database holds only sealed strings.
//
// Rotation is out of scope for now, but the sealed string names the key that
// made it. A later release can hold two keys and open old values with the old
// one; today an unknown key ID is a clear error instead of a failed decrypt.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	version = "v1"
	// MasterKeySize is the size of data/secret.key.
	MasterKeySize = 32
	// hkdfInfo binds the derived key to this use, so the same secret.key can
	// feed another purpose later without the two keys being equal.
	hkdfInfo = "pbpanel secretbox v1"
)

var (
	// ErrTampered means the value is malformed, was changed, or was sealed for
	// a different purpose (different aad). The three are deliberately not told
	// apart: telling them apart helps an attacker, not the operator.
	ErrTampered = errors.New("secretbox: value is corrupt or was sealed for another purpose")
	// ErrUnknownKey means the value was sealed by a key this Box does not hold,
	// usually a secret.key that was replaced.
	ErrUnknownKey = errors.New("secretbox: value was sealed with a different key")
)

// Box seals and opens values with one derived key. It is safe for concurrent use.
type Box struct {
	aead cipher.AEAD
	id   string
}

// New derives the sealing key from masterKey, which must be MasterKeySize bytes.
func New(masterKey []byte) (*Box, error) {
	if len(masterKey) != MasterKeySize {
		return nil, fmt.Errorf("secretbox: master key is %d bytes, want %d", len(masterKey), MasterKeySize)
	}
	key, err := hkdf.Key(sha256.New, masterKey, nil, hkdfInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("secretbox: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: gcm: %w", err)
	}
	sum := sha256.Sum256(key)
	return &Box{aead: aead, id: hex.EncodeToString(sum[:4])}, nil
}

// KeyID names the derived key. It is part of every sealed value and reveals
// nothing useful about the key.
func (b *Box) KeyID() string { return b.id }

// Seal encrypts plain and returns `v1:<keyid>:<base64 nonce||ciphertext>`.
//
// aad is authenticated but not stored: pass what the value belongs to, such as
// "dns_providers:cloudflare", so a sealed value copied onto another row fails
// to open instead of being used there.
func (b *Box) Seal(plain []byte, aad string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox: nonce: %w", err)
	}
	out := b.aead.Seal(nonce, nonce, plain, []byte(aad))
	return version + ":" + b.id + ":" + base64.RawStdEncoding.EncodeToString(out), nil
}

// Open reverses Seal. It returns ErrUnknownKey for a value from another key and
// ErrTampered for anything else that does not authenticate.
func (b *Box) Open(sealed, aad string) ([]byte, error) {
	parts := strings.Split(sealed, ":")
	if len(parts) != 3 || parts[0] != version {
		return nil, ErrTampered
	}
	if parts[1] != b.id {
		return nil, ErrUnknownKey
	}
	raw, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, ErrTampered
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}
