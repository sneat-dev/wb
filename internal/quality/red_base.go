package quality

import (
	"errors"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// RedBaseline records that a per-change ratchet measured its merge base while
// tests were failing there. A baseline that carries it is usable, but its
// counts are an upper bound: a statement only a failed test reaches may be
// recorded as uncovered, so the count-rise rule is looser by that margin for
// the packages named in FailedTests. The changed-statement rule is unaffected.
type RedBaseline struct {
	SHA         string   `yaml:"sha" json:"sha"`
	FailedTests []string `yaml:"failed_tests" json:"failed_tests"`
}

// redBaseRecorder is how a merge-base measurement survives a red base. A base
// branch with one failing test must still be measurable, otherwise the pull
// request that repairs the test can never pass the ratchet that measures it.
//
// It accepts a failed `go test -coverprofile` command only when every package
// the command reports as failed ran its test binary to a normal exit: the
// package names at least one failed test and printed its coverage line. A
// build or setup failure, a panic or os.Exit before coverage was written, a
// timeout, a killed process and a missing or unreadable profile are all still
// failures, so a package never reaches the baseline without measured data.
type redBaseRecorder struct {
	mu     sync.Mutex
	failed []string
}

func (recorder *redBaseRecorder) accept(exitCode int, output, profilePath string) bool {
	if recorder == nil || exitCode != 1 {
		return false
	}
	failed, ok := redBaseFailedTests(output)
	if !ok {
		return false
	}
	if _, _, err := readCoverageProfile(profilePath); err != nil {
		return false
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.failed = append(recorder.failed, failed...)
	return true
}

// baseline returns nil when the base was green.
func (recorder *redBaseRecorder) baseline(sha string) *RedBaseline {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.failed) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(recorder.failed))
	tests := make([]string, 0, len(recorder.failed))
	for _, name := range recorder.failed {
		if !seen[name] {
			seen[name] = true
			tests = append(tests, name)
		}
	}
	sort.Strings(tests)
	return &RedBaseline{SHA: sha, FailedTests: tests}
}

// goTestExitCode is 1 when `go test` itself exited reporting failures, and -1
// for anything that is not a plain exit: a timeout this package replaced the
// error for, a cancelled context, or a process killed by a signal.
func goTestExitCode(err error) int {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1
	}
	return exitErr.ExitCode()
}

// redBaseFailedTests reads `go test` output package by package. It returns
// the failed tests as "<package>.<test>" and true only when at least one
// package failed and every failed package both named a failed test and
// printed a coverage line before its `FAIL\t<package>\t<elapsed>` summary.
func redBaseFailedTests(output string) ([]string, bool) {
	var failed, names []string
	covered := false
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "--- FAIL: "):
			names = append(names, strings.Fields(strings.TrimPrefix(trimmed, "--- FAIL: "))[0])
		case strings.HasPrefix(line, "coverage: "):
			covered = true
		case strings.HasPrefix(line, "FAIL\t"):
			// A build or setup failure has no elapsed-time field:
			// "FAIL\t<package> [build failed]".
			fields := strings.Split(line, "\t")
			if len(fields) < 3 || len(names) == 0 || !covered {
				return nil, false
			}
			for _, name := range names {
				failed = append(failed, fields[1]+"."+name)
			}
			names, covered = nil, false
		case strings.HasPrefix(line, "ok  \t"), strings.HasPrefix(line, "?   \t"):
			names, covered = nil, false
		}
	}
	return failed, len(failed) > 0
}
