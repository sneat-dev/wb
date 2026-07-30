//go:build linux

package worktrees

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
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
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no-new-privileges before Landlock: %w", err)
	}
	rulesetAttr := landlockRulesetAttr{handledAccessFS: landlockWriteAccess}
	rulesetFD, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&rulesetAttr)), unsafe.Sizeof(rulesetAttr), 0)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(rulesetFD)) }()
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
