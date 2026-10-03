package main

import (
	"path/filepath"
	"strings"
	"testing"
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
