package cockpit

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"sync"
	"time"
)

// loginCodeLifetime is how long a login code may be exchanged for a session
// (cockpit#req:owner-session).
const loginCodeLifetime = 60 * time.Second

// maxPendingLoginCodes bounds the codes waiting to be exchanged. Only the
// owner channel mints one, so the bound is a tidiness limit, not a defence:
// the oldest code is dropped when a new one would exceed it.
const maxPendingLoginCodes = 16

// secretBytes is the entropy in a login code and in a session identifier.
const secretBytes = 32

// newSecret reads a fresh URL-safe secret from random.
func newSecret(random io.Reader) (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// LoginCode is a freshly minted login code and the moment it stops being
// exchangeable.
type LoginCode struct {
	Code      string
	ExpiresAt time.Time
}

// pendingLoginCode is one minted code, kept as a digest so the code itself
// is never held after it is handed out.
type pendingLoginCode struct {
	digest  [sha256.Size]byte
	expires time.Time
}

// loginCodes is the set of minted, not yet presented login codes.
type loginCodes struct {
	random io.Reader

	mu      sync.Mutex
	pending []pendingLoginCode
}

// mint issues a code valid for loginCodeLifetime from now, dropping the codes
// that have expired.
func (codes *loginCodes) mint(now time.Time) (LoginCode, error) {
	code, err := newSecret(codes.random)
	if err != nil {
		return LoginCode{}, err
	}
	issued := LoginCode{Code: code, ExpiresAt: now.Add(loginCodeLifetime)}
	codes.mu.Lock()
	defer codes.mu.Unlock()
	kept := codes.pending[:0]
	for _, entry := range codes.pending {
		if now.Before(entry.expires) {
			kept = append(kept, entry)
		}
	}
	if len(kept) >= maxPendingLoginCodes {
		kept = kept[len(kept)-maxPendingLoginCodes+1:]
	}
	codes.pending = append(kept, pendingLoginCode{digest: sha256.Sum256([]byte(code)), expires: issued.ExpiresAt})
	return issued, nil
}

// exchange consumes code and reports whether it was minted here, had not been
// presented before and has not expired. A code is consumed by being
// presented, whether or not it is still valid, and every pending code is
// compared in constant time.
func (codes *loginCodes) exchange(code string, now time.Time) bool {
	digest := sha256.Sum256([]byte(code))
	codes.mu.Lock()
	defer codes.mu.Unlock()
	valid := false
	kept := codes.pending[:0]
	for _, entry := range codes.pending {
		if subtle.ConstantTimeCompare(entry.digest[:], digest[:]) == 1 {
			valid = now.Before(entry.expires)
			continue
		}
		kept = append(kept, entry)
	}
	codes.pending = kept
	return valid
}
