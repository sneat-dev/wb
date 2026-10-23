package cmdworker

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/workerrun"
)

type testExit struct {
	code    int
	message string
}

func (err *testExit) Error() string { return err.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects"} }, ExitError: func(code int, message string) error { return &testExit{code, message} }}
}
func testExitCode(t *testing.T, err error) int {
	t.Helper()
	var exit *testExit
	if !errors.As(err, &exit) {
		t.Fatalf("not coded usage: %v", err)
	}
	return exit.code
}
func testDependencies() Dependencies {
	return Dependencies{
		CanonicalRoots: func(roots []string) ([]string, error) {
			if len(roots) == 0 {
				return nil, errors.New("at least one --root is required")
			}
			for _, root := range roots {
				if !strings.HasPrefix(root, "/") {
					return nil, errors.New("--root must be absolute")
				}
				if root == "/absent" {
					return nil, errors.New("missing root")
				}
			}
			return roots, nil
		},
		Budget: func() int { return 2 }, Connect: func(context.Context, workerrun.ConnectRequest, func(workerrun.ConnectResult) error, io.Writer) error {
			panic("invalid arguments delegated")
		},
	}
}
