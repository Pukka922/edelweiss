package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-crypt/crypt"
	"github.com/go-crypt/crypt/algorithm/bcrypt"
	"github.com/go-crypt/crypt/algorithm/md5crypt"
	"github.com/go-crypt/crypt/algorithm/scrypt"
	"github.com/go-crypt/crypt/algorithm/shacrypt"
)

// ErrInvalidCredentials is returned for unknown users, wrong passwords and
// locked or expired accounts alike.
var ErrInvalidCredentials = errors.New("invalid username or password")

// PasswordVerifier checks a password for a user.
type PasswordVerifier interface {
	Verify(name, password string) error
}

// dummyHash is the fallback hash verified for unknown, locked or expired
// accounts when the shadow file holds no other usable hash to imitate (see
// dummyFor). It is a sha512-crypt hash of a random, discarded password.
const dummyHash = "$6$8a6ed4e71c2df637$UVUVVoovCHe5XfcRM7bF3ZKtrf4Nxx1ns7BtJQHBaQndBgfMgY.0iv3tYJj6VYzBneBxYQRV2dPgGpHZMkutH1"

// Verifier verifies passwords against a shadow file. Supported hashes:
// md5crypt ($1$), sha256/512-crypt ($5$/$6$), bcrypt ($2a$/$2b$/$2y$), yescrypt ($y$).
type Verifier struct {
	ShadowPath string
	Now        func() time.Time
	decoder    *crypt.Decoder
}

// NewVerifier creates a Verifier for the given shadow file.
func NewVerifier(shadowPath string) (*Verifier, error) {
	d := crypt.NewDecoder()
	for _, register := range []func(*crypt.Decoder) error{
		func(d *crypt.Decoder) error { return shacrypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return bcrypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return md5crypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return scrypt.RegisterDecoderYescrypt(d) },
	} {
		if err := register(d); err != nil {
			return nil, fmt.Errorf("register hash decoder: %w", err)
		}
	}
	return &Verifier{ShadowPath: shadowPath, Now: time.Now, decoder: d}, nil
}

// Verify returns nil if password is correct for name. It returns
// ErrInvalidCredentials for unknown, locked and expired accounts and for wrong
// passwords; any other error is operational (unreadable shadow file, or the
// account's password hash is unsupported or invalid).
//
// For unknown, locked and expired accounts a cost-representative dummy hash is
// verified (and the result discarded) so that response times do not reveal
// whether an account exists.
func (v *Verifier) Verify(name, password string) error {
	entries, err := readShadowFile(v.ShadowPath)
	if err != nil {
		return fmt.Errorf("read shadow file: %w", err)
	}
	e, ok := entries[name]
	if !ok || !usableHash(e.Hash) || v.expired(e) {
		_, _ = v.match(dummyFor(entries, name), password)
		return ErrInvalidCredentials
	}
	match, err := v.match(e.Hash, password)
	if err != nil {
		return fmt.Errorf("verify password of %s: unsupported or invalid password hash: %w", name, err)
	}
	if !match {
		return ErrInvalidCredentials
	}
	return nil
}

func (v *Verifier) expired(e ShadowEntry) bool {
	return e.Expire >= 0 && v.Now().Unix()/86400 >= e.Expire
}

func (v *Verifier) match(hash, password string) (bool, error) {
	digest, err := v.decoder.Decode(hash)
	if err != nil {
		return false, err
	}
	return digest.MatchAdvanced(password)
}

// dummyFor picks the hash to verify when name cannot be authenticated. To make
// the work comparable to a real verification (yescrypt costs far more than
// sha512-crypt), it uses the hash of a usable entry other than name, the one
// with the lexicographically smallest user name so that the choice is
// deterministic. It falls back to dummyHash if there is no such entry.
func dummyFor(entries map[string]ShadowEntry, name string) string {
	var bestName, bestHash string
	found := false
	for n, e := range entries {
		if n == name || !usableHash(e.Hash) {
			continue
		}
		if !found || n < bestName {
			bestName, bestHash, found = n, e.Hash, true
		}
	}
	if !found {
		return dummyHash
	}
	return bestHash
}

func usableHash(h string) bool {
	return h != "" && !strings.HasPrefix(h, "!") && !strings.HasPrefix(h, "*")
}
