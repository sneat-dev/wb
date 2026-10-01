//go:build e2e

package checkoutmarker

import (
	"path/filepath"
	"testing"
)

func TestE2EDescribeCentralHostedWorktreeCoordinates(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	hosted := filepath.Join(worktreesRoot, "host-task", "github.com", "sneat-dev", "wb")
	git(t, repositories.Canonical, "worktree", "add", "-q", "-b", "host-task", hosted)

	inspection, err := Describe(hosted, describeOptions(repositories))
	if err != nil {
		t.Fatal(err)
	}
	descriptor := inspection.Descriptor
	if descriptor.Kind != KindWorktree || descriptor.Task != "host-task" || descriptor.Repository != "sneat-dev/wb" {
		t.Fatalf("hosted worktree coordinates = %+v", descriptor)
	}
	if !sameDirectory(descriptor.WorktreesRoot, worktreesRoot) {
		t.Fatalf("hosted worktrees root = %q, want %q", descriptor.WorktreesRoot, worktreesRoot)
	}
	if descriptor.CanonicalPath != repositories.Canonical || descriptor.Branch != "host-task" {
		t.Fatalf("hosted worktree lost canonical or branch authority: %+v", descriptor)
	}
}
