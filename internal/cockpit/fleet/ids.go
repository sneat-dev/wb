package fleet

import (
	"crypto/sha256"
	"encoding/hex"
)

// The id prefixes, one per collection.
const (
	kindMachine    = "mach"
	kindRepository = "repo"
	kindWorktree   = "wt"
	kindBranch     = "br"
	kindPR         = "pr"
	kindAgent      = "ag"
)

// entryID derives a stable id from an entry's identity: the machine, the
// repository and the task, branch, number or run it names. It is a hash, so it
// is the same on every refresh and every restart, safe in a URL, and carries no
// filesystem path; the same identity on two machines gives two ids because the
// machine is part of the identity.
func entryID(kind string, identity ...string) string {
	sum := sha256.New()
	for _, part := range identity {
		_, _ = sum.Write([]byte(part))
		_, _ = sum.Write([]byte{0})
	}
	return kind + "-" + hex.EncodeToString(sum.Sum(nil)[:10])
}
