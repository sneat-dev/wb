package worktrees

import (
	"crypto/rand"
	"encoding/hex"
)

// crypto/rand.Read fills the entire buffer and never returns an error in the
// Go version this module requires; it terminates the process if entropy fails.
func randomHexToken(byteCount int) string {
	token := make([]byte, byteCount)
	_, _ = rand.Read(token)
	return hex.EncodeToString(token)
}
