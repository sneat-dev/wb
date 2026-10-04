package integration

import (
	"github.com/sneat-dev/wb/internal/cli/cmdsession"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"testing"
)

func TestCwCovSessionPruneCommandRemovesOnlyExitedRecords(t *testing.T) {
	t.Parallel()
	// sessionDir and the prune command must resolve the same state home, which
	// now derives from the projects root, so both are given the same root.
	root := t.TempDir()
	dir, err := sessionrun.DirForWrite(root)
	if err != nil {
		t.Fatal(err)
	}
	// A PID that cannot be running: the record is derived as "gone".
	if _, err := session.Register(dir, session.Record{PID: 1 << 30, Runtime: "test"}); err != nil {
		t.Fatal(err)
	}
	// The test process itself is live and must survive the prune.
	if _, err := session.Register(dir, session.Record{PID: os.Getpid(), Runtime: "test"}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := executeCommand(t, root, func() *cobra.Command {
		return cmdsession.NewPrune(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdsession.Dependencies{Prune: sessionrun.NewPrune(sessionrun.DefaultPruneDependencies()).Prune})
	})
	if err != nil {
		t.Fatalf("session prune: %v", err)
	}
	if !strings.Contains(stdout, "removed 1 exited session record(s)") {
		t.Fatalf("session prune output = %q", stdout)
	}
	views, err := session.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].PID != os.Getpid() {
		t.Fatalf("remaining sessions = %+v, want only the live one", views)
	}

	// A second prune is a no-op.
	stdout, _, err = executeCommand(t, root, func() *cobra.Command {
		return cmdsession.NewPrune(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdsession.Dependencies{Prune: sessionrun.NewPrune(sessionrun.DefaultPruneDependencies()).Prune})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "removed 0 exited session record(s)") {
		t.Fatalf("second prune output = %q", stdout)
	}
}
