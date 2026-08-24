//go:build darwin

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const sandboxExecPath = "/usr/bin/sandbox-exec"

func platformGitFilesystemCapabilityAvailable() error {
	info, err := os.Stat(sandboxExecPath)
	if err != nil {
		return fmt.Errorf("secure Git capability is unavailable: sandbox-exec: %w", err)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("secure Git capability is unavailable: %s is not executable", sandboxExecPath)
	}
	// A present binary is not sufficient: some managed macOS environments have
	// the legacy tool but prohibit its profiles. Exercise the exact mechanism
	// before Create/Cleanup prepares an operation directory or report.
	probe := exec.Command(sandboxExecPath, "-p", sandboxProfile(nil), "/usr/bin/true")
	if output, err := probe.CombinedOutput(); err != nil {
		return fmt.Errorf("secure Git capability is unavailable: sandbox-exec probe: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runPlatformGitWithFilesystemCapability(capability gitFilesystemCapability, executable string, args, environment []string) int {
	// sandbox-exec profiles accept names, not file descriptors. Freeze each
	// retained root's parent through profile parsing and Git execution so that
	// its verified name cannot be replaced in the gap before the kernel applies
	// the profile. The roots themselves remain writable; only their ancestors
	// lose rename authority, which Git does not need for these operations.
	guards, err := lockDarwinCapabilityParents(capability)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: %v\n", err)
		return 1
	}
	defer restoreDarwinCapabilityParents(guards)
	profile := sandboxProfile(capability.writeRoots)
	command := exec.Command(sandboxExecPath, "-p", profile, executable)
	command.Args = append(command.Args, args...)
	command.Env = environment
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(os.Stderr, "wb secure Git capability: start sandboxed Git: %v\n", err)
		return 1
	}
	return 0
}

type darwinCapabilityParentGuard struct {
	directory *os.File
	mode      os.FileMode
	frozen    bool
}

// lockDarwinCapabilityParents freezes each retained root's parent for the
// duration of one sandboxed Git call, under an exclusive lock on that parent.
//
// The lock is not an optimisation. A capability root's parent is a directory
// WB shares rather than owns: the parent of <projects-root>/<owner>/<repo> is
// one owner directory holding every repository that owner has. Two secure Git
// helpers under one owner — two tasks of a fleet sweep applying concurrently,
// or simply two wb processes — otherwise both enter this window, and the
// second reads the mode the first has already cleared. It then "restores"
// 0555, and the owner directory stays read-only after both succeed, so no
// clone or worktree can ever be created under it again. flock is the right
// primitive because the helpers are separate processes: the freeze happens in
// a short-lived child, so no in-process mutex could see its siblings. The
// kernel drops the lock if a helper dies.
//
// Parents are taken in sorted order. One operation can retain roots under two
// different parents, and two operations retaining the same pair in opposite
// orders would deadlock — the same resource-hierarchy argument the cleanup
// scheduler makes for its per-repository locks.
func lockDarwinCapabilityParents(capability gitFilesystemCapability) ([]darwinCapabilityParentGuard, error) {
	parentPaths := make([]string, 0, len(capability.writeRoots))
	seen := make(map[string]bool, len(capability.writeRoots))
	for _, root := range capability.writeRoots {
		parentPath := filepath.Dir(root.path)
		if parentPath == root.path {
			return nil, fmt.Errorf("secure Git capability root has no mutable parent: %s", root.path)
		}
		if !seen[parentPath] {
			seen[parentPath] = true
			parentPaths = append(parentPaths, parentPath)
		}
	}
	sort.Strings(parentPaths)

	guards := make([]darwinCapabilityParentGuard, 0, len(parentPaths))
	byPath := make(map[string]*os.File, len(parentPaths))
	for _, parentPath := range parentPaths {
		parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
		if err != nil {
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("open secure Git capability parent %s: %w", parentPath, err)
		}
		// Two spellings can name one directory. Detect that before locking:
		// flock is per open file description, so a second descriptor for the
		// same directory would block on this process's own lock forever.
		duplicate := false
		for _, guard := range guards {
			guardInfo, guardErr := guard.directory.Stat()
			parentInfo, parentErr := parent.Stat()
			if guardErr == nil && parentErr == nil && os.SameFile(guardInfo, parentInfo) {
				duplicate = true
				byPath[parentPath] = guard.directory
				break
			}
		}
		if duplicate {
			_ = parent.Close()
			continue
		}
		if err := lockCapabilityParent(parent); err != nil {
			_ = parent.Close()
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("serialize secure Git capability parent %s: %w", parentPath, err)
		}
		guards = append(guards, darwinCapabilityParentGuard{directory: parent})
		byPath[parentPath] = parent
	}

	// Verify every root against its held parent before the mutation, and again
	// after it, so a swap in either gap is refused rather than sandboxed.
	if err := verifyDarwinCapabilityRoots(capability, byPath); err != nil {
		restoreDarwinCapabilityParents(guards)
		return nil, fmt.Errorf("secure Git capability root changed before sandbox profile: %w", err)
	}
	for index := range guards {
		info, err := guards[index].directory.Stat()
		if err != nil {
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("inspect secure Git capability parent: %w", err)
		}
		originalMode := info.Mode().Perm()
		if err := unix.Fchmod(int(guards[index].directory.Fd()), uint32(originalMode&^0o222)); err != nil {
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("freeze secure Git capability parent: %w", err)
		}
		guards[index].mode = originalMode
		guards[index].frozen = true
	}
	if err := verifyDarwinCapabilityRoots(capability, byPath); err != nil {
		restoreDarwinCapabilityParents(guards)
		return nil, fmt.Errorf("secure Git capability root changed while freezing parent: %w", err)
	}
	return guards, nil
}

// capabilityParentLockTimeout bounds how long one helper waits for a parent
// another helper is inside. Legitimate contention is one Git call — well under
// a second — so this never fires for it. What it does bound is the one shape a
// plain blocking lock could not survive: the sandbox permits Git hooks to
// invoke WB itself, so a hook that reached a WB worktree operation on this same
// owner directory would wait on its own ancestor forever, holding a task lock
// and stranding a transaction. A bounded wait turns that into a reported error.
//
// It is a var so tests can shorten it.
var capabilityParentLockTimeout = 2 * time.Minute

func lockCapabilityParent(parent *os.File) error {
	deadline := time.Now().Add(capabilityParentLockTimeout)
	for {
		err := unix.Flock(int(parent.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("another WB Git helper held it for %s", capabilityParentLockTimeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func verifyDarwinCapabilityRoots(capability gitFilesystemCapability, byPath map[string]*os.File) error {
	for _, root := range capability.writeRoots {
		parent, ok := byPath[filepath.Dir(root.path)]
		if !ok {
			return fmt.Errorf("%s has no held parent", root.path)
		}
		if !directoryEntryStillMatches(parent, filepath.Base(root.path), root.directory) {
			return fmt.Errorf("%s", root.path)
		}
	}
	return nil
}

// restoreDarwinCapabilityParents puts every frozen mode back and releases the
// locks in reverse acquisition order. Closing the descriptor releases its
// flock, so a guard that never reached the freeze still unlocks correctly.
func restoreDarwinCapabilityParents(guards []darwinCapabilityParentGuard) {
	for index := len(guards) - 1; index >= 0; index-- {
		if guards[index].frozen {
			_ = unix.Fchmod(int(guards[index].directory.Fd()), uint32(guards[index].mode))
		}
		_ = guards[index].directory.Close()
	}
}

func sandboxProfile(writeRoots []gitFilesystemCapabilityRoot) string {
	clauses := []string{
		"(version 1)",
		"(deny default)",
		"(allow process*)",
		// Git hooks can invoke WB itself. Go reads the hardware page size through
		// sysctl during runtime initialization; this is read-only system metadata,
		// not filesystem authority.
		"(allow sysctl-read)",
		"(allow file-read*)",
		"(allow network*)",
		"(allow file-write* (literal \"/dev/null\"))",
		// getpwuid(3) resolves the current user through opendirectoryd over Mach
		// IPC, not by reading /etc/passwd — file-read* does not cover it. OpenSSH
		// calls getpwuid() before it can locate ~/.ssh, so without this exception
		// `(deny default)` makes every SSH remote fail with "No user exists for
		// uid <n>" although file reads and the network are already wide open.
		// This grants only that one directory-service lookup, not filesystem or
		// network authority, so the write confinement below remains the real
		// security boundary.
		"(allow mach-lookup (global-name \"com.apple.system.opendirectoryd.libinfo\"))",
		// The same failure one layer up: an HTTPS remote authenticates through
		// `credential.helper`, whose default on macOS is osxkeychain, and that
		// helper reaches the keychain over Mach IPC to SecurityServer. Denied,
		// it returns nothing, Git falls back to prompting, and prompting is
		// disabled here — so every HTTPS remote fails with "could not read
		// Username for 'https://github.com'" while the credential sits in the
		// keychain and the network is already wide open. The opendirectoryd
		// exception above fixed SSH remotes and could not fix these, which is
		// why part of the fleet worked and part did not.
		//
		// This grants the keychain lookup and nothing else. Reads were already
		// permitted and the write confinement below is still the real security
		// boundary, so the only new authority is a credential this process
		// could not otherwise obtain.
		"(allow mach-lookup (global-name \"com.apple.SecurityServer\"))",
	}
	for _, root := range writeRoots {
		clauses = append(clauses, "(allow file-write* (subpath "+strconv.Quote(root.path)+"))")
	}
	return strings.Join(clauses, " ")
}
