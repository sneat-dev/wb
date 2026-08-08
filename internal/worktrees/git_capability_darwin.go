//go:build darwin

package worktrees

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
}

func lockDarwinCapabilityParents(capability gitFilesystemCapability) ([]darwinCapabilityParentGuard, error) {
	guards := make([]darwinCapabilityParentGuard, 0, len(capability.writeRoots))
	for _, root := range capability.writeRoots {
		parentPath := filepath.Dir(root.path)
		if parentPath == root.path {
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("secure Git capability root has no mutable parent: %s", root.path)
		}
		parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
		if err != nil {
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("open secure Git capability parent %s: %w", parentPath, err)
		}
		if !directoryEntryStillMatches(parent, filepath.Base(root.path), root.directory) {
			_ = parent.Close()
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("secure Git capability root changed before sandbox profile: %s", root.path)
		}
		duplicate := false
		for _, guard := range guards {
			guardInfo, guardErr := guard.directory.Stat()
			parentInfo, parentErr := parent.Stat()
			if guardErr == nil && parentErr == nil && os.SameFile(guardInfo, parentInfo) {
				duplicate = true
				break
			}
		}
		if duplicate {
			_ = parent.Close()
			continue
		}
		info, err := parent.Stat()
		if err != nil {
			_ = parent.Close()
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("inspect secure Git capability parent %s: %w", parentPath, err)
		}
		originalMode := info.Mode().Perm()
		lockedMode := originalMode &^ 0o222
		if err := unix.Fchmod(int(parent.Fd()), uint32(lockedMode)); err != nil {
			_ = parent.Close()
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("freeze secure Git capability parent %s: %w", parentPath, err)
		}
		if !directoryEntryStillMatches(parent, filepath.Base(root.path), root.directory) {
			_ = unix.Fchmod(int(parent.Fd()), uint32(originalMode))
			_ = parent.Close()
			restoreDarwinCapabilityParents(guards)
			return nil, fmt.Errorf("secure Git capability root changed while freezing parent: %s", root.path)
		}
		guards = append(guards, darwinCapabilityParentGuard{directory: parent, mode: originalMode})
	}
	return guards, nil
}

func restoreDarwinCapabilityParents(guards []darwinCapabilityParentGuard) {
	for index := len(guards) - 1; index >= 0; index-- {
		_ = unix.Fchmod(int(guards[index].directory.Fd()), uint32(guards[index].mode))
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
	}
	for _, root := range writeRoots {
		clauses = append(clauses, "(allow file-write* (subpath "+strconv.Quote(root.path)+"))")
	}
	return strings.Join(clauses, " ")
}
