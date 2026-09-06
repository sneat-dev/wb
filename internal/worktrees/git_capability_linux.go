//go:build linux

package worktrees

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

type landlockRulesetAttr struct {
	handledAccessFS uint64
}

type landlockPathBeneathAttr struct {
	allowedAccess uint64
	parentFD      int32
	reserved      uint32
}

const landlockWriteAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
	unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
	unix.LANDLOCK_ACCESS_FS_MAKE_REG |
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
	unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
	unix.LANDLOCK_ACCESS_FS_REFER |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE

// landlockDevNullAccess is deliberately narrower than landlockWriteAccess:
// Git routinely opens /dev/null to discard output, a plain write/truncate on
// an existing device node, never a create or remove. The macOS sandbox
// backend grants the equivalent literal allowance for the same reason (see
// sandboxProfile in git_capability_darwin.go); Landlock needs its own
// explicit rule because it has no notion of a profile-wide default path.
const landlockDevNullAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE

func platformGitFilesystemCapabilityAvailable() error {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return fmt.Errorf("secure Git capability is unavailable: Landlock probe: %w", errno)
	}
	if version < 3 {
		return fmt.Errorf("secure Git capability is unavailable: Landlock ABI %d lacks required write controls", version)
	}
	// Validate the complete rule shape before Create/Cleanup creates an
	// operation or report. Enforcing a Landlock ruleset is irreversible for a
	// process, so this probe stops just before RESTRICT_SELF.
	rulesetAttr := landlockRulesetAttr{handledAccessFS: landlockWriteAccess}
	rulesetFD, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&rulesetAttr)), unsafe.Sizeof(rulesetAttr), 0)
	if errno != 0 {
		return fmt.Errorf("secure Git capability is unavailable: create Landlock ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(rulesetFD)) }()
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("secure Git capability is unavailable: open Landlock probe root: %w", err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	probeRule := landlockPathBeneathAttr{allowedAccess: landlockWriteAccess, parentFD: int32(rootFD)}
	_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, rulesetFD, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&probeRule)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("secure Git capability is unavailable: add Landlock rule: %w", errno)
	}
	return nil
}

func runPlatformGitWithFilesystemCapability(capability gitFilesystemCapability, executable string, args, environment []string) int {
	if err := restrictWithLandlock(capability); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: %v\n", err)
		return 1
	}
	if err := unix.Exec(executable, append([]string{executable}, args...), environment); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: exec Git: %v\n", err)
		return 1
	}
	return 1
}

func restrictWithLandlock(capability gitFilesystemCapability) error {
	return pinOSThreadThroughLandlock(func() error {
		return installLandlockRuleset(capability)
	})
}

// pinOSThreadThroughLandlock keeps PR_SET_NO_NEW_PRIVS, RESTRICT_SELF, and the
// caller's subsequent exec on one Linux task. no_new_privs and Landlock apply
// to the calling thread; a Go goroutine may otherwise migrate between those
// syscalls and make RESTRICT_SELF fail with EPERM, or exec Git from a thread
// that never received the intended restriction. A successful restriction is
// irreversible, so the helper process deliberately remains pinned until its
// immediate exec or exit. An error leaves no usable capability and unlocks the
// goroutine before returning the diagnostic.
func pinOSThreadThroughLandlock(install func() error) error {
	runtime.LockOSThread()
	if err := install(); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	return nil
}

func installLandlockRuleset(capability gitFilesystemCapability) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no-new-privileges before Landlock: %w", err)
	}
	rulesetAttr := landlockRulesetAttr{handledAccessFS: landlockWriteAccess}
	rulesetFD, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&rulesetAttr)), unsafe.Sizeof(rulesetAttr), 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(rulesetFD)) }()
	devNullFD, err := unix.Open("/dev/null", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/null for Landlock rule: %w", err)
	}
	defer func() { _ = unix.Close(devNullFD) }()
	devNullAttr := landlockPathBeneathAttr{allowedAccess: landlockDevNullAccess, parentFD: int32(devNullFD)}
	if _, _, addErrno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, rulesetFD, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&devNullAttr)), 0, 0, 0); addErrno != 0 {
		return fmt.Errorf("allow Landlock /dev/null: %w", addErrno)
	}
	for _, root := range capability.writeRoots {
		// Landlock rules bind the retained directory object. Do not reopen
		// root.path here: an attacker can replace that spelling after the
		// helper's final validation but before this policy is installed.
		attr := landlockPathBeneathAttr{allowedAccess: landlockWriteAccess, parentFD: int32(root.directory.Fd())}
		_, _, addErrno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, rulesetFD, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
		if addErrno != 0 {
			return fmt.Errorf("allow Landlock Git write root %s: %w", root.path, addErrno)
		}
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rulesetFD, 0, 0)
	if errno != 0 {
		return fmt.Errorf("enforce Landlock Git ruleset: %w", errno)
	}
	return nil
}
