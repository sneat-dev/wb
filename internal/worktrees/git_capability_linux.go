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
// an existing device node, never a create or remove. Landlock needs its own
// explicit rule because it has no notion of a profile-wide default path.
const landlockDevNullAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE

// platformGitFilesystemCapabilityConfines reports whether this backend
// actually restricts where the Git child may write. Landlock does.
func platformGitFilesystemCapabilityConfines() bool {
	return true
}

func platformGitFilesystemCapabilityAvailable() error {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return fmt.Errorf("secure Git capability is unavailable: Landlock probe: %w", errno)
	}
	return probeLandlockRuleShape(version)
}

// probeLandlockRuleShape admits the ABI policy before validating the real
// ruleset and path-rule shape, without installing an irreversible restriction.
func probeLandlockRuleShape(version uintptr) error {
	if err := validateLandlockABI(version); err != nil {
		return err
	}
	// Validate the complete rule shape before Create/Cleanup creates an
	// operation or report. Enforcing a Landlock ruleset is irreversible for a
	// process, so this probe stops just before RESTRICT_SELF.
	rulesetFD, err := createLandlockRuleset()
	if err != nil {
		return fmt.Errorf("secure Git capability is unavailable: create Landlock ruleset: %w", err)
	}
	defer func() { _ = unix.Close(int(rulesetFD)) }()
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("secure Git capability is unavailable: open Landlock probe root: %w", err)
	}
	defer func() { _ = unix.Close(rootFD) }()
	if err := addLandlockPathRule(rulesetFD, rootFD, landlockWriteAccess); err != nil {
		return fmt.Errorf("secure Git capability is unavailable: add Landlock rule: %w", err)
	}
	return nil
}

func runPlatformGitWithFilesystemCapability(capability gitFilesystemCapability, executable string, args, environment []string) int {
	if err := restrictWithLandlock(capability); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: %v\n", err)
		return 1
	}
	err := unix.Exec(executable, append([]string{executable}, args...), environment)
	_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: exec Git: %v\n", err)
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
	rulesetFD, err := createLandlockRuleset()
	if err != nil {
		return fmt.Errorf("create Landlock ruleset: %w", err)
	}
	defer func() { _ = unix.Close(int(rulesetFD)) }()
	devNullFD, err := unix.Open("/dev/null", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/null for Landlock rule: %w", err)
	}
	defer func() { _ = unix.Close(devNullFD) }()
	if err := addLandlockPathRule(rulesetFD, devNullFD, landlockDevNullAccess); err != nil {
		return fmt.Errorf("allow Landlock /dev/null: %w", err)
	}
	for _, root := range capability.writeRoots {
		// Landlock rules bind the retained directory object. Do not reopen
		// root.path here: an attacker can replace that spelling after the
		// helper's final validation but before this policy is installed.
		if err := addLandlockPathRule(rulesetFD, int(root.directory.Fd()), landlockWriteAccess); err != nil {
			return fmt.Errorf("allow Landlock Git write root %s: %w", root.path, err)
		}
	}
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0)
	if errno != 0 {
		return fmt.Errorf("enforce Landlock Git ruleset: %w", errno)
	}
	return nil
}

func validateLandlockABI(version uintptr) error {
	if version < 3 {
		return fmt.Errorf("secure Git capability is unavailable: Landlock ABI %d lacks required write controls", version)
	}
	return nil
}

func createLandlockRuleset() (int, error) {
	attr := landlockRulesetAttr{handledAccessFS: landlockWriteAccess}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func addLandlockPathRule(rulesetFD, parentFD int, allowedAccess uint64) error {
	attr := landlockPathBeneathAttr{allowedAccess: allowedAccess, parentFD: int32(parentFD)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
