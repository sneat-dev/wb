package secretscan

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Fingerprint identifies a matched secret without ever reproducing any of
// its characters: a scanner that prints the secret it found has moved the
// leak, not stopped it. It is a truncated SHA-256 digest of the exact
// matched bytes plus their length, e.g. "sha256:4f9c2a1b len=41" -- stable
// across runs (so the same finding produces the same fingerprint, letting
// --override-secret name it exactly) and useless for reconstructing the
// input.
//
// Deliberately not "first 4 chars + length": for several of the named
// patterns this gate blocks on, the first characters ARE the identifying
// public prefix (e.g. "AKIA", "ghp_"), but for others they overlap the
// secret material itself, and a reviewer of refusal output should never
// have to reason about which case applies. A hash carries zero raw
// characters in every case.
func Fingerprint(secret []byte) string {
	sum := sha256.Sum256(secret)
	return "sha256:" + hex.EncodeToString(sum[:4]) + " len=" + strconv.Itoa(len(secret))
}
