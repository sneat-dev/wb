// Package specscore shells out to the `specscore` CLI to lint a SpecScore
// spec tree and, optionally, apply its autofixes.
package specscore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
)

// IsManaged reports whether dir is a SpecScore-managed repo (carries a
// specscore.yaml at its root).
func IsManaged(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "specscore.yaml"))
	return err == nil
}

// violationCountRe matches the CLI's trailing summary, e.g. "12 violation(s)
// found" or "0 violations found".
var violationCountRe = regexp.MustCompile(`(\d+)\s+violation`)

// Lint runs `specscore spec lint` (read-only) in dir and returns the number of
// violations reported. A non-zero lint exit caused by violations is NOT an
// error; err is non-nil only when the CLI cannot be executed at all.
func Lint(dir string) (violations int, output string, err error) {
	out, runErr := lintCmd(dir)
	if runErr != nil && !isExitError(runErr) {
		return 0, out, runErr
	}
	return parseViolations(out), out, nil
}

// Fix runs `specscore spec lint --fix` in dir, applying autofixes in place. A
// non-zero exit caused by remaining (non-autofixable) violations is NOT an
// error; err is non-nil only when the CLI cannot be executed at all.
func Fix(dir string) (output string, err error) {
	out, runErr := lintCmd(dir, "--fix")
	if runErr != nil && !isExitError(runErr) {
		return out, runErr
	}
	return out, nil
}

func lintCmd(dir string, extra ...string) (string, error) {
	args := append([]string{"spec", "lint"}, extra...)
	cmd := exec.Command("specscore", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func parseViolations(output string) int {
	// The last matching summary line wins (fix output may print a per-file
	// summary before the final violation count).
	matches := violationCountRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(matches[len(matches)-1][1])
	return n
}

// isExitError reports whether err is a non-zero process exit (the CLI ran but
// returned a violations exit code), as opposed to a failure to launch it.
func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}
