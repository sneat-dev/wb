package recipe

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"

	"github.com/sneat-dev/wb/internal/gitops"
)

// Preview is what dry-run mode reports for one repo.
type Preview struct {
	Summary string
	Changed bool
}

// previewCommand runs r.DryRunCommand read-only in repoPath's current local
// working tree (no worktree is created — nothing is mutated) and reports
// what r.Command would do. A non-zero DryRunCommand exit is treated as "the
// command has something to report" (mirroring how lint tools signal
// findings via exit code), not a hard failure — except exit code 127
// (POSIX "command not found"), which means DryRunCommand itself is
// misconfigured (a typo, or the tool isn't installed) and must surface as a
// real error, not get misreported as "found something." Every command here
// runs via `sh -c`, so `sh` itself always launches successfully even when
// the command it's asked to run does not exist — the failure shows up as
// sh's own exit 127, not as a Go-level failure to start a process.
func previewCommand(r Recipe, repoPath string) (Preview, error) {
	if r.DryRunCommand == "" {
		return Preview{Summary: "run: " + r.Command, Changed: true}, nil
	}
	out, runErr := runShell(repoPath, r.DryRunCommand)
	if runErr != nil && (!isExitError(runErr) || isCommandNotFound(runErr)) {
		return Preview{}, fmt.Errorf("dry_run_command failed: %w", runErr)
	}
	changed := runErr != nil
	summary := "clean"
	if changed {
		summary = "run: " + r.Command
	}
	if r.CountRegex != "" {
		re, err := regexp.Compile(r.CountRegex)
		if err != nil {
			return Preview{}, fmt.Errorf("invalid count_regex %q: %w", r.CountRegex, err)
		}
		if m := re.FindStringSubmatch(out); len(m) > 1 {
			summary = m[1] + " match(es)"
		}
	}
	return Preview{Summary: summary, Changed: changed}, nil
}

// commandMutator returns a gitops.Land-compatible mutator that runs
// r.Command in the worktree and reports whether it changed anything.
func commandMutator(r Recipe) func(worktreePath string) (bool, string, error) {
	return func(wt string) (bool, string, error) {
		if _, err := runShell(wt, r.Command); err != nil && (!isExitError(err) || isCommandNotFound(err)) {
			return false, "", err
		}
		changed, err := gitops.WorktreeChanged(wt)
		if err != nil {
			return false, "", err
		}
		if !changed {
			return false, "clean", nil
		}
		return true, "applied " + r.Name, nil
	}
}

// runShell runs command via `sh -c` in dir, returning combined output.
func runShell(dir, command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// isExitError reports whether err is a non-zero process exit (the command
// ran but returned non-zero), as opposed to a failure to launch it at all.
func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// isCommandNotFound reports whether err is a shell exit code 127 — the
// POSIX convention for "command not found." Since every command here runs
// via `sh -c`, this is how a misconfigured (nonexistent) command surfaces,
// not as a Go-level failure to launch a process.
func isCommandNotFound(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() == 127
	}
	return false
}
