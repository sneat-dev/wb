//go:build darwin

package worktrees

import (
	"fmt"
	"os"
	"syscall"
)

// macOS runs WB's Git helpers WITHOUT a filesystem sandbox.
//
// Until commit e785415c3c8c79049024bc489e814f0f47a57f38 this file wrapped the
// helper's Git child in sandbox-exec with a deny-by-default write profile,
// and froze and flock-ed the parent directory of every declared write root
// to defeat the name-based profile's rename race. That machinery was removed
// on 2026-09-10 after a performance audit found it cost more than it bought:
//
//   - three production outages, each a legitimate Git write path missing from
//     the allowlist (SSH uid lookup 81e0fbd, HTTPS keychain 6574a90, the
//     shared Go cache 0a07305);
//   - machine-wide serialization of unrelated repositories through the parent
//     flock, and permanently read-only directories whenever a helper was
//     interrupted, since Go runs no deferred restore on a signal;
//   - a profile that already allowed reading every file, unrestricted
//     network, arbitrary subprocesses and keychain access, so the only thing
//     it ever confined was where a hook's writes landed during WB's own Git
//     call -- a hook the user's next plain `git commit` runs unconfined.
//
// The Linux backend keeps its Landlock confinement: it is descriptor-based
// and needs no parent freeze. If macOS ever needs a write boundary again, a
// descriptor-based mechanism is the design to reach for, not this one.
//
// To recover the removed backend and its tests:
//
//	git checkout e785415c3c8c79049024bc489e814f0f47a57f38 -- \
//	  internal/worktrees/git_capability_darwin.go \
//	  internal/worktrees/git_capability_darwin_test.go \
//	  internal/worktrees/git_capability_darwin_concurrency_test.go
//
// The helper-child plumbing that dispatches here is unchanged: callers still
// retain canonical descriptors and re-verify them before this runs, and every
// mutating verb still requires the capability, which on macOS now simply
// means "a Git helper can run".

func platformGitFilesystemCapabilityAvailable() error {
	return nil
}

// platformGitFilesystemCapabilityConfines reports whether this backend
// actually restricts where the Git child may write. macOS does not.
func platformGitFilesystemCapabilityConfines() bool {
	return false
}

func runPlatformGitWithFilesystemCapability(_ gitFilesystemCapability, executable string, args, environment []string) int {
	if err := syscall.Exec(executable, append([]string{executable}, args...), environment); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb Git helper: exec Git: %v\n", err)
		return 1
	}
	return 1
}
