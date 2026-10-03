package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/spf13/cobra"
)

// TestLifecycleCheckoutUpdatedDefaultsToRealDispatch confirms the production
// wiring: lifecycleCheckoutUpdated (used unmodified by pr.go, pr_create.go
// and worktree_merge.go) still builds its handler from the real
// lifecyclehooks.Dispatch. The returned handler is never invoked here --
// invoking it would call the real Dispatch and touch the developer's real
// config/state/receipt paths (#620); the handler's own behavior is covered
// through a fake dispatch by TestCwCovLifecycleCheckoutUpdatedWarnsInsteadOfFailing.

func TestCwCovBrowserTargetAndCommand(t *testing.T) {
	for _, target := range []string{"https://example.test/x", "http://example.test"} {
		resolved, err := browserTarget(target)
		if err != nil || resolved != target {
			t.Errorf("browserTarget(%q) = (%q, %v), want it unchanged", target, resolved, err)
		}
	}
	relative, err := browserTarget("some/relative/path")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(relative) {
		t.Errorf("browserTarget(relative) = %q, want an absolute path", relative)
	}

	darwinName, darwinArgs, err := browserCommand("darwin", "/x")
	if err != nil || darwinName != "open" || len(darwinArgs) != 1 {
		t.Errorf("darwin browser = (%q, %v, %v)", darwinName, darwinArgs, err)
	}
	linuxName, _, err := browserCommand("linux", "/x")
	if err != nil || linuxName != "xdg-open" {
		t.Errorf("linux browser = (%q, %v)", linuxName, err)
	}
	windowsName, windowsArgs, err := browserCommand("windows", "/x")
	if err != nil || windowsName != "rundll32" || len(windowsArgs) != 2 {
		t.Errorf("windows browser = (%q, %v, %v)", windowsName, windowsArgs, err)
	}
	if _, _, err := browserCommand("plan9", "/x"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("unsupported GOOS error = %v", err)
	}

	// With no launcher on PATH the failure is reported, never swallowed.
	t.Setenv("PATH", t.TempDir())
	if err := openBrowser(t.TempDir()); err == nil {
		t.Error("openBrowser succeeded with no browser launcher on PATH")
	}
}

func TestCwCovSessionPruneCommandRemovesOnlyExitedRecords(t *testing.T) {
	// sessionDir and the prune command must resolve the same state home, which
	// now derives from the projects root, so both are given the same root.
	root := t.TempDir()
	dir, err := sessionDir(&invocation{projectsRoot: root})
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

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newSessionPruneCmd(&invocation{projectsRoot: root}) })
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
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newSessionPruneCmd(&invocation{projectsRoot: root}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "removed 0 exited session record(s)") {
		t.Fatalf("second prune output = %q", stdout)
	}
}
