package checkoutsetup

import (
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBeforeCreateRefreshesExistingCloneAndRejectsInvalidOrMissingRepository(t *testing.T) {
	seed := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(projects, "private-config.yaml")
	if err := os.WriteFile(config, []byte("version: 1\nhooks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deps := DefaultHookDependencies(func() string { return exe })
	deps.Refresh = func(repo, _, exe, root string) (bool, error) {
		return hooks.RefreshManagedShims(repo, config, exe, root)
	}
	testenv.CloneWithOrigin(t, seed, "app", clone)

	if err := BeforeCreate(projects, []string{"acme/app"}, deps); err != nil {
		t.Fatalf("refreshManagedHooksBeforeWorktreeCreate: %v", err)
	}
	// A malformed slug cannot be resolved to a canonical repository.
	if err := BeforeCreate(projects, []string{"not-a-slug"}, deps); err == nil {
		t.Fatal("a malformed repository slug must fail")
	}
	// A well-formed but absent repository cannot be resolved either.
	if err := BeforeCreate(projects, []string{"acme/absent"}, deps); err == nil {
		t.Fatal("an absent canonical repository must fail")
	}
}
func TestAfterCreateMarksCanonicalAndWorktreeAndWarnsWithoutFailing(t *testing.T) {
	t.Parallel()
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)
	var errOut strings.Builder
	// markCreatedCheckouts marks both the worktree and its canonical clone,
	// and warns for a path it cannot describe.
	AfterCreate(checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}, &errOut, []worktrees.CreateResult{
		{Repository: "acme/app", WorktreeDir: clone, Base: "main"},
		{Repository: "acme/app", WorktreeDir: filepath.Join(t.TempDir(), "missing")},
	}, DefaultMarkerDependencies())
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatalf("markCreatedCheckouts did not mark the clone: %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: could not write") {
		t.Fatalf("markCreatedCheckouts warnings = %q", errOut.String())
	}

	// A repository slug with no owner is skipped for the canonical path but
	// the worktree itself is still marked.
	errOut.Reset()
	AfterCreate(checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}, &errOut, []worktrees.CreateResult{{Repository: "app", WorktreeDir: clone}}, DefaultMarkerDependencies())
	if errOut.Len() != 0 {
		t.Fatalf("markCreatedCheckouts errOut = %q", errOut.String())
	}

}
