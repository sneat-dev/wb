package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestPublicationRootUsesCurrentProjectsRootAndNativeClaimCustody(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stale := filepath.Join(root, "not-selected")
	inv := &invocation{projectsRoot: stale, nonInteractive: true}
	family := newDepsCmd(inv)
	child, _, err := family.Find([]string{"publish"})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(child)
	inv.projectsRoot = root
	var out, notes bytes.Buffer
	child.SetOut(&out)
	child.SetErr(&notes)
	child.SilenceErrors = true
	child.SilenceUsage = true
	child.SetArgs([]string{"npm", "--fleet", "--repo", "acme/provider", "--workflow", "release.yml", "--package", "@acme/provider", "--version", "1.0.0", "--format", "toml"})
	err = child.Execute()
	if err == nil || err.Error() != `unknown --format "toml" (want markdown, yaml, or json)` {
		t.Fatalf("late format refusal=%v", err)
	}
	if out.Len() != 0 || strings.Contains(notes.String(), "1.0.0") {
		t.Fatalf("premature output=%q notes=%q", out.String(), notes.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("captured constructor root used: %v", err)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	releases, err := npmrelease.Normalize([]npmrelease.Release{{Repository: "acme/provider", Workflow: "release.yml", Package: "@acme/provider", Version: "1.0.0", Ref: "main"}}, "main")
	if err != nil {
		t.Fatal(err)
	}
	operations := append([]string{npmrelease.OperationIDFor(releases)}, npmrelease.PublicationClaimOperationIDs(releases)...)
	for _, operation := range operations {
		if _, err := os.Stat(filepath.Join(home, "worktrees", operation)); err != nil {
			t.Fatalf("native claim absent: %v", err)
		}
		lock, err := orchestrate.AcquireOperationLock(root, operation, false)
		if err != nil {
			t.Fatalf("failed format leaked claim: %v", err)
		}
		if err := lock.Release(); err != nil {
			t.Fatal(err)
		}
	}
}
