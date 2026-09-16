package worktrees

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The descriptor-secure Git helpers (RunSecure*GitHelper) read inherited file
// descriptors 3..9 and are normally invoked by Git as a child of WB itself.
// Calling them in this test process would re-interpret the Go runtime's own
// descriptors (notably its netpoll descriptor) as repository capabilities and
// corrupt the runtime, so every assertion below runs them in a re-executed
// copy of this test binary instead.
//
// The child is started as a *test run* (`-test.run=^TestWtLifeCovSecureHelperProcess$`)
// rather than through the package TestMain's argv dispatch. That difference is
// deliberate: a TestMain dispatch calls os.Exit before the testing framework
// reports coverage, so the child's counters would never reach the parent's
// profile. Running one real test lets the child finish normally, which makes
// the testing package flush its counters into the shared -test.gocoverdir, and
// the parent (which started that same run) merges them into the final profile.
const (
	wtLifeCovHelperMarker = "WB_WTLIFECOV_HELPER"
	wtLifeCovArgsEnv      = "WB_WTLIFECOV_ARGS"
	wtLifeCovCodeMarker   = "WB-WTLIFECOV-CODE"
	// The helper's own stdout is bracketed so it can be told apart from the
	// testing framework's trailing "PASS"/coverage chatter.
	wtLifeCovBeginMarker = "WB-WTLIFECOV-BEGIN"
	wtLifeCovEndMarker   = "WB-WTLIFECOV-END"
)

// TestWtLifeCovSecureHelperProcess is the child half of the harness. It is
// inert unless the parent's marker variable is present, so an ordinary package
// run treats it as a passing no-op test.
func TestWtLifeCovSecureHelperProcess(t *testing.T) {
	helper := os.Getenv(wtLifeCovHelperMarker)
	if helper == "" {
		return
	}
	var args []string
	if raw := os.Getenv(wtLifeCovArgsEnv); raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			t.Fatalf("decode helper arguments: %v", err)
		}
	}
	// The helpers Fchdir out of the test's working directory; restore a usable
	// cwd before the testing framework finishes so its own absolute paths (and
	// any diagnostics) keep working.
	original, err := os.Getwd()
	if err == nil {
		defer func() { _ = os.Chdir(original) }()
	}
	_, _ = fmt.Fprintln(os.Stdout, wtLifeCovBeginMarker)
	code := wtLifeCovDispatchSecureHelper(helper, args)
	_, _ = fmt.Fprintln(os.Stdout, wtLifeCovEndMarker)
	_, _ = fmt.Fprintf(os.Stdout, "%s=%d\n", wtLifeCovCodeMarker, code)
}

func wtLifeCovDispatchSecureHelper(helper string, args []string) int {
	switch helper {
	case "cleanup":
		return RunSecureCleanupGitHelper(args)
	case "stage":
		return RunSecureStageGitHelper(args)
	case "canonical":
		return RunSecureCanonicalGitHelper(args)
	case "canonical-policy":
		return RunSecureCanonicalPolicyGitHelper(args)
	case "stage-canonical":
		return RunSecureStageCanonicalGitHelper(args)
	default:
		return -1
	}
}

// wtLifeCovCoverDir returns the intermediate coverage directory this test run
// was given, so re-executed children can be pointed at the same one. It is
// empty for a plain `go test` (no -cover), in which case children must not be
// given -test.gocoverdir: an uninstrumented binary rejects that flag and exits.
func wtLifeCovCoverDir() string {
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		return dir
	}
	if entry := flag.Lookup("test.gocoverdir"); entry != nil {
		return entry.Value.String()
	}
	return ""
}

type wtLifeCovHelperResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// wtLifeCovHelperInvocation describes one child run of a descriptor-secure
// helper. drop lists environment variable names to remove from the child env
// (to reach the runtime-layout failure paths), extra appends overrides.
type wtLifeCovHelperInvocation struct {
	helper string
	files  []*os.File
	args   []string
	extra  []string
	drop   []string
}

// wtLifeCovRunSecureHelper invokes one RunSecure*GitHelper in a re-executed
// copy of this test binary with the supplied descriptors as fd 3, fd 4, ... and
// arguments, and returns the child's process result. exitCode is the integer the
// helper returned when the child could report one; otherwise it is the child's
// process exit status (which is what an exec'd Git substitutes for it).
func wtLifeCovRunSecureHelper(t *testing.T, helper string, extraFiles []*os.File, args []string, extraEnv ...string) wtLifeCovHelperResult {
	t.Helper()
	return wtLifeCovRunSecureHelperInvocation(t, wtLifeCovHelperInvocation{
		helper: helper, files: extraFiles, args: args, extra: extraEnv,
	})
}

func wtLifeCovRunSecureHelperInvocation(t *testing.T, invocation wtLifeCovHelperInvocation) wtLifeCovHelperResult {
	t.Helper()
	payload, err := json.Marshal(invocation.args)
	if err != nil {
		t.Fatalf("encode helper arguments: %v", err)
	}
	childArgs := []string{"-test.run=^TestWtLifeCovSecureHelperProcess$"}
	if dir := wtLifeCovCoverDir(); dir != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+dir)
	}
	env := omitEnv(os.Environ(), invocation.drop)
	env = append(env,
		wtLifeCovHelperMarker+"="+invocation.helper,
		wtLifeCovArgsEnv+"="+string(payload),
	)
	env = append(env, invocation.extra...)
	command := exec.Command(os.Args[0], childArgs...)
	command.Env = env
	command.ExtraFiles = invocation.files
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result := wtLifeCovHelperResult{stdout: stdout.String(), stderr: stderr.String()}
	if runErr == nil {
		result.exitCode = 0
	} else {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("run %s helper: %v", invocation.helper, runErr)
		}
		result.exitCode = exitErr.ExitCode()
	}
	if code, ok := wtLifeCovReportedCode(result.stdout); ok {
		result.exitCode = code
	}
	return result
}

func omitEnv(environment []string, names []string) []string {
	if len(names) == 0 {
		return environment
	}
	kept := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		dropped := false
		for _, candidate := range names {
			if name == candidate {
				dropped = true
				break
			}
		}
		if !dropped {
			kept = append(kept, entry)
		}
	}
	return kept
}

func wtLifeCovReportedCode(stdout string) (int, bool) {
	prefix := wtLifeCovCodeMarker + "="
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, prefix), "%d", &code); err != nil {
			return 0, false
		}
		return code, true
	}
	return 0, false
}

// wtLifeCovSection returns what the helper itself wrote to stdout. A helper
// that execs Git replaces the process, so its end marker never arrives; in that
// case everything after the begin marker is returned.
func wtLifeCovSection(stdout string) string {
	_, after, found := strings.Cut(stdout, wtLifeCovBeginMarker+"\n")
	if !found {
		return ""
	}
	if section, _, closed := strings.Cut(after, wtLifeCovEndMarker); closed {
		return section
	}
	return after
}

func wtLifeCovOpenDirectory(t *testing.T, path string) *os.File {
	t.Helper()
	directory, err := os.Open(path)
	if err != nil {
		t.Fatalf("open directory %s: %v", path, err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory
}
