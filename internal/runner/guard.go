package runner

import (
	"errors"
	"os"
	"testing"
)

// envHelperProcess and envHelperProcessObserve mark the child half of Go's
// helper-process re-exec pattern (`exec.Command(os.Args[0], …)`): the child
// has no *testing.T to call AllowRealProcess with, so it identifies itself
// through the environment instead, exactly as
// spec/plans/coverage-to-100/README.md task-24's runtime-guard section
// describes.
const (
	envHelperProcess        = "GO_WANT_HELPER_PROCESS"
	envHelperProcessObserve = "GO_WANT_HELPER_PROCESS_OBSERVE"
	// envAllowRealProcess is the process-wide flag runnertest.AllowRealProcess
	// sets via t.Setenv, so it is naturally restored at that test's cleanup
	// and (like every t.Setenv use) refuses to coexist with t.Parallel().
	envAllowRealProcess = "WB_RUNNER_ALLOW_REAL_PROCESS"
)

// ErrRealProcessBlocked is returned by every Runner operation when task-24's
// runtime guard refuses to start a real process during a unit test.
var ErrRealProcessBlocked = errors.New("runner: refusing to start a real process during a unit test; call runnertest.AllowRealProcess(t) if this file is on the unit-tier pending or allow list")

// isTesting is testing.Testing, held in a variable so this package's own
// tests can override it to exercise guardRealProcess's "not running under
// go test at all" branch, which no test built by `go test` can otherwise
// reach (testing.Testing() is unconditionally true there).
var isTesting = testing.Testing

// e2eBuild reports whether this binary was compiled with the `e2e` build
// tag (guard_default.go/guard_e2e.go's e2eBuildTag). Like isTesting, it is a
// variable rather than the constant read directly, so guardRealProcess's
// tests can exercise both outcomes from one ordinary (non-e2e-tagged) test
// binary; guard_default_test.go and guard_e2e_test.go separately pin that
// e2eBuildTag itself has the right value in each build.
var e2eBuild = func() bool { return e2eBuildTag }

// guardRealProcess is Real's one gate, called before every process start. It
// returns nil outside a test binary, inside an e2e-tagged build, when the
// calling test named itself with runnertest.AllowRealProcess, or when this
// process is the helper-process re-exec child.
func guardRealProcess() error {
	if !isTesting() {
		return nil
	}
	if e2eBuild() {
		return nil
	}
	if os.Getenv(envHelperProcess) == "1" || os.Getenv(envHelperProcessObserve) == "1" {
		return nil
	}
	if os.Getenv(envAllowRealProcess) == "1" {
		return nil
	}
	return ErrRealProcessBlocked
}
