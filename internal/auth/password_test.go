package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	ok, err := Verify(encoded, password)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Fatal("Verify rejected the password it was given")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded, err := Hash("the right one")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	ok, err := Verify(encoded, "the wrong one")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Fatal("Verify accepted a wrong password")
	}
}

// The salt is random per call, so the same password must not produce the same
// encoded hash twice; otherwise the table would leak which accounts share a
// password.
func TestHashUsesFreshSalt(t *testing.T) {
	first, err := Hash("same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	second, err := Hash("same password")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical: the salt is not random")
	}

	for _, encoded := range []string{first, second} {
		ok, err := Verify(encoded, "same password")
		if err != nil || !ok {
			t.Fatalf("Verify(%q) = %v, %v; want true, nil", encoded, ok, err)
		}
	}
}

// The whole point of encoding the parameters: a hash made with different costs
// still verifies, and the costs are read back out of the string rather than
// assumed.
func TestEncodedParametersRoundTrip(t *testing.T) {
	// Deliberately not the package defaults, and small so the test is quick.
	want := argonParams{memory: 8 * 1024, iterations: 2, parallelism: 4, saltLen: 24, keyLen: 48}

	encoded, err := hashWith("parameter test", want)
	if err != nil {
		t.Fatalf("hashWith: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=8192,t=2,p=4$") {
		t.Fatalf("encoded hash = %q, want the parameters in the PHC prefix", encoded)
	}

	got, salt, key, err := decodeHash(encoded)
	if err != nil {
		t.Fatalf("decodeHash: %v", err)
	}
	if got != want {
		t.Errorf("decoded parameters = %+v, want %+v", got, want)
	}
	if len(salt) != int(want.saltLen) {
		t.Errorf("decoded salt is %d bytes, want %d", len(salt), want.saltLen)
	}
	if len(key) != int(want.keyLen) {
		t.Errorf("decoded key is %d bytes, want %d", len(key), want.keyLen)
	}

	ok, err := Verify(encoded, "parameter test")
	if err != nil || !ok {
		t.Fatalf("Verify with non-default parameters = %v, %v; want true, nil", ok, err)
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	valid, err := hashWith("x", argonParams{memory: 8, iterations: 1, parallelism: 1, saltLen: 16, keyLen: 32})
	if err != nil {
		t.Fatalf("hashWith: %v", err)
	}
	parts := strings.Split(valid, "$")

	cases := map[string]string{
		"empty":            "",
		"not six fields":   "$argon2id$v=19$m=8,t=1,p=1$c2FsdA",
		"wrong algorithm":  strings.Replace(valid, "argon2id", "argon2i", 1),
		"wrong version":    strings.Replace(valid, "v=19", "v=16", 1),
		"missing param":    "$argon2id$v=19$m=8,t=1$" + parts[4] + "$" + parts[5],
		"non-numeric cost": "$argon2id$v=19$m=lots,t=1,p=1$" + parts[4] + "$" + parts[5],
		"zero memory":      "$argon2id$v=19$m=0,t=1,p=1$" + parts[4] + "$" + parts[5],
		// A tampered row must fail one login rather than try to allocate 4 TiB.
		"absurd memory": "$argon2id$v=19$m=4294967295,t=1,p=1$" + parts[4] + "$" + parts[5],
		"absurd time":   "$argon2id$v=19$m=8,t=100000,p=1$" + parts[4] + "$" + parts[5],
		"bad salt":      "$argon2id$v=19$m=8,t=1,p=1$not!base64$" + parts[5],
		"empty key":     "$argon2id$v=19$m=8,t=1,p=1$" + parts[4] + "$",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := Verify(encoded, "x")
			if ok {
				t.Fatal("Verify accepted a malformed hash")
			}
			if !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("Verify error = %v, want it to wrap ErrInvalidHash", err)
			}
		})
	}
}
