package worktrees

import (
	"encoding/hex"
	"testing"
)

func TestRandomHexTokenPreservesNameEntropyLengths(t *testing.T) {
	t.Parallel()
	for _, byteCount := range []int{12, 16} {
		token := randomHexToken(byteCount)
		decoded, err := hex.DecodeString(token)
		if err != nil || len(decoded) != byteCount || len(token) != byteCount*2 {
			t.Fatalf("%d-byte name token %q: decoded length %d, error %v", byteCount, token, len(decoded), err)
		}
	}
}
