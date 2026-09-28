package main

import (
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

// TestPrintRunQueueReportsWriteFailureOnEachHeaderLine drives each of
// printRunQueue's (cmd/wb/run.go) three header Fprintf error-return
// branches in turn, using an empty runqueue state (a fresh, empty projects
// root) so there are no running/waiting entries to iterate — only the three
// header lines execute, in order: the budget line, the running-count line,
// and the waiting-count line. failAfterNWriter is defined in
// zz_rwi00_ci_test.go in this same package.
func TestPrintRunQueueReportsWriteFailureOnEachHeaderLine(t *testing.T) {
	t.Parallel()
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("failAt=%d", failAt), func(t *testing.T) {
			t.Parallel()
			inv := &invocation{projectsRoot: t.TempDir()}
			cmd := &cobra.Command{}
			writer := &failAfterNWriter{failAt: failAt}
			cmd.SetOut(writer)
			err := printRunQueue(inv, cmd, false)
			if err == nil {
				t.Fatalf("printRunQueue with a write failure at call %d = nil error, want one", failAt)
			}
			if writer.writes != failAt {
				t.Fatalf("printRunQueue made %d writes before failing, want exactly %d", writer.writes, failAt)
			}
		})
	}
}
