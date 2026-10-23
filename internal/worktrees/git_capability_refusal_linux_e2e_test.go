//go:build linux && e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const landlockRefusalChild = "WB_LANDLOCK_REFUSAL_CHILD"
const landlockPrerequisiteSkip = "WB_LANDLOCK_PREREQUISITE_SKIP "

type landlockRefusalCase struct {
	name, operation, diagnostic string
	denial                      *landlockSeccompDenial
}

func landlockRefusalCases() []landlockRefusalCase {
	create := uint32(unix.SYS_LANDLOCK_CREATE_RULESET)
	open := landlockSeccompDenial{syscall: unix.SYS_OPENAT, argument: 2, value: unix.O_PATH, bitmask: true}
	add := landlockSeccompDenial{syscall: unix.SYS_LANDLOCK_ADD_RULE, argument: -1}
	noPrivileges := landlockSeccompDenial{syscall: unix.SYS_PRCTL, argument: 0, value: unix.PR_SET_NO_NEW_PRIVS}
	return []landlockRefusalCase{
		{"probe-version", "probe", "secure Git capability is unavailable: Landlock probe", &landlockSeccompDenial{syscall: create, argument: 2, value: unix.LANDLOCK_CREATE_RULESET_VERSION}},
		{"probe-create", "probe", "secure Git capability is unavailable: create Landlock ruleset", &landlockSeccompDenial{syscall: create, argument: 2}},
		{"probe-open", "probe", "secure Git capability is unavailable: open Landlock probe root", &open},
		{"probe-add", "probe", "secure Git capability is unavailable: add Landlock rule", &add},
		{"install-no-privileges", "install", "set no-new-privileges before Landlock", &noPrivileges},
		{"install-create", "install", "create Landlock ruleset", &landlockSeccompDenial{syscall: create, argument: 2}},
		{"install-open", "install", "open /dev/null for Landlock rule", &open},
		{"install-add", "install", "allow Landlock /dev/null", &add},
		{"install-restrict", "install", "enforce Landlock Git ruleset", &landlockSeccompDenial{syscall: unix.SYS_LANDLOCK_RESTRICT_SELF, argument: -1}},
		{"wrapper-no-privileges", "wrapper", "wb secure Git capability: set no-new-privileges before Landlock", &noPrivileges},
		{"closed-root", "install", "allow Landlock Git write root", nil},
		{"exec-missing", "wrapper", "wb secure Git capability: exec Git", nil},
	}
}

// The parent may run concurrently; marker-dispatched children remain serial
// and pinned until their goroutine exits, so irreversible policy is never
// returned to a reusable runtime thread. Only children install seccomp/Landlock.
func TestE2ELandlockKernelPolicyRefusals(t *testing.T) {
	if name := os.Getenv(landlockRefusalChild); name != "" {
		for _, tc := range landlockRefusalCases() {
			if tc.name == name {
				runLandlockRefusalChild(t, tc)
				return
			}
		}
		t.Fatalf("unknown Landlock refusal case %q", name)
	}
	t.Parallel()
	if runtime.GOARCH != "amd64" {
		t.Skip("seccomp denial fixtures currently validate only the Linux amd64 syscall ABI")
	}
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		t.Skipf("native Landlock ABI/write-rule prerequisite unavailable: %v", err)
	}
	for _, tc := range landlockRefusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runLandlockRefusalProcess(t, tc)
		})
	}
}

func runLandlockRefusalProcess(t *testing.T, tc landlockRefusalCase) {
	t.Helper()
	root := t.TempDir()
	evidence := filepath.Join(root, "evidence")
	if err := os.WriteFile(evidence, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestE2ELandlockKernelPolicyRefusals$", "-test.v"}
	coverDir := wtLifeCovCoverDir()
	if testing.CoverMode() != "" && coverDir == "" {
		t.Fatal("instrumented refusal fixture has no shared child counter directory")
	}
	before := map[string]bool{}
	if coverDir != "" {
		args = append(args, "-test.gocoverdir="+coverDir)
		entries, err := os.ReadDir(coverDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			before[entry.Name()] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], args...)
	command.Env = append(os.Environ(), landlockRefusalChild+"="+tc.name, "WB_LANDLOCK_REFUSAL_ROOT="+root)
	if coverDir != "" {
		command.Env = append(command.Env, "GOCOVERDIR="+coverDir)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Landlock %s child = %v\n%s", tc.name, err, output)
	}
	receipt := "WB_LANDLOCK_EXECUTED " + tc.name + " pid="
	if !strings.Contains(string(output), receipt) {
		if strings.Contains(string(output), landlockPrerequisiteSkip+tc.name+" reason=") {
			t.Skipf("measured native child prerequisite for %s:\n%s", tc.name, output)
		}
		t.Fatalf("child returned without execution or a measured prerequisite skip:\n%s", output)
	}
	diagnostic := tc.diagnostic
	if tc.operation == "wrapper" {
		errno := unix.EPERM
		if tc.name == "exec-missing" {
			errno = unix.ENOENT
		}
		diagnostic += ": " + errno.Error() + "\n"
	}
	if !strings.Contains(string(output), diagnostic) {
		t.Fatalf("child lost complete stage diagnostic %q:\n%s", diagnostic, output)
	}
	if contents, err := os.ReadFile(evidence); err != nil || string(contents) != "retained" {
		t.Fatalf("retained evidence = %q, %v", contents, err)
	}
	if coverDir != "" {
		pidText := strings.SplitN(strings.SplitN(string(output), receipt, 2)[1], "\n", 2)[0]
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil {
			t.Fatalf("invalid child counter receipt: %s", output)
		}
		entries, err := os.ReadDir(coverDir)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "covcounters.") && !before[entry.Name()] && strings.Contains(entry.Name(), "."+strconv.Itoa(pid)+".") {
				info, err := entry.Info()
				if err != nil || info.Size() == 0 {
					t.Fatalf("invalid fresh child counters: %v, %v", info, err)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("executed child %d emitted no fresh covcounters receipt in %s", pid, coverDir)
		}
	}
}

func runLandlockRefusalChild(t *testing.T, tc landlockRefusalCase) {
	t.Helper()
	if runtime.GOARCH != "amd64" {
		skipLandlockRefusalPrerequisite(t, tc.name, "unsupported syscall ABI")
	}
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		skipLandlockRefusalPrerequisite(t, tc.name, fmt.Sprintf("native Landlock prerequisite: %v", err))
	}
	runtime.LockOSThread()
	// Never unlock the fixture's outer pin: this disposable goroutine exits
	// with its filtered/restricted task. This is policy isolation, not proof of
	// the production helper's nested failure-unlock balance.
	rootPath := os.Getenv("WB_LANDLOCK_REFUSAL_ROOT")
	held, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	roots := []gitFilesystemCapabilityRoot{{path: rootPath, directory: held}}
	if tc.name == "exec-missing" && wtLifeCovCoverDir() != "" {
		coverPath := wtLifeCovCoverDir()
		cover, err := os.Open(coverPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cover.Close() })
		roots = append(roots, gitFilesystemCapabilityRoot{path: coverPath, directory: cover})
	}
	capability, err := newGitFilesystemCapability(roots...)
	if err != nil {
		t.Fatal(err)
	}
	if tc.name == "closed-root" {
		if err := held.Close(); err != nil {
			t.Fatal(err)
		}
		if held.Fd() != ^uintptr(0) {
			t.Fatal("closed retained file did not expose its invalid-FD sentinel")
		}
	}
	if tc.denial != nil {
		if _, err := os.ReadFile("/proc/sys/kernel/seccomp/actions_avail"); err != nil {
			skipLandlockRefusalPrerequisite(t, tc.name, fmt.Sprintf("seccomp-filter kernel prerequisite unavailable: %v", err))
		}
		if err := installLandlockDenialFilter(*tc.denial); err != nil {
			if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) {
				skipLandlockRefusalPrerequisite(t, tc.name, fmt.Sprintf("host policy prevents disposable seccomp fixture: %v", err))
			}
			t.Fatalf("install validated seccomp filter: %v", err)
		}
	}
	switch tc.operation {
	case "probe":
		err = platformGitFilesystemCapabilityAvailable()
	case "install":
		err = restrictWithLandlock(capability)
	case "wrapper":
		missing := filepath.Join(rootPath, "missing-git")
		if code := runPlatformGitWithFilesystemCapability(capability, missing, []string{"status"}, os.Environ()); code != 1 {
			t.Fatalf("refused Git wrapper status = %d", code)
		}
	}
	if tc.operation != "wrapper" {
		wantErrno := error(unix.EPERM)
		if tc.name == "closed-root" {
			wantErrno = unix.EBADF
		}
		if !errors.Is(err, wantErrno) || !strings.Contains(err.Error(), tc.diagnostic) {
			t.Fatalf("native refusal = %v, want %s wrapping %v", err, tc.diagnostic, wantErrno)
		}
		if tc.name == "closed-root" && !strings.Contains(err.Error(), rootPath) {
			t.Fatalf("closed retained-root error omitted authority path: %v", err)
		}
		fmt.Println(err)
	}
	fmt.Printf("WB_LANDLOCK_EXECUTED %s pid=%d\n", tc.name, os.Getpid())
}

// Structured skips are emitted only after the child measured an unsupported
// architecture, actual capability probe failure, or seccomp host prerequisite.
func skipLandlockRefusalPrerequisite(t *testing.T, name, reason string) {
	t.Helper()
	fmt.Printf("%s%s reason=%s\n", landlockPrerequisiteSkip, name, reason)
	t.Skip(reason)
}
