//go:build e2e

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // The native Git fixture and HOME failure select process-wide environment.
func TestE2ESourceInventoryPreservesAdoptedOwnershipAndRefusesMissingOrContradictoryEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/source-adopted")
	adopted, err := Adopt(t.Context(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: path, Apply: true})
	if err != nil || len(adopted) != 1 || adopted[0].Action != AdoptAdopted {
		t.Fatalf("adopt: %+v %v", adopted, err)
	}
	head := strings.TrimSpace(gitTestOutput(t, path, "rev-parse", "HEAD"))
	options := SourceListOptions{ProjectsRoot: fixture.projectsRoot, Repository: "ACME/APP", Branch: "feature/source-adopted", HeadSHA: head}
	listed, err := ListSourceWithDiagnostics(t.Context(), options)
	if err != nil || len(listed.Diagnostics) != 0 || len(listed.Results) != 1 || !listed.Results[0].External {
		t.Fatalf("adopted producer: %+v %v", listed, err)
	}
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	malformed := claim
	malformed.AcquiredVia = "forged-acquisition"
	if _, _, err := sourceClaimLayout(fixture.projectsRoot, fixture.home, path, identity, malformed); err == nil {
		t.Fatal("forged source claim lineage granted placement")
	}
	common, err := os.Stat(filepath.Join(fixture.canonical, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	mismatched := options
	mismatched.Repository = "other/app"
	if _, _, err := inspectSourceRegistration(t.Context(), mismatched, fixture.projectsRoot, fixture.home, fixture.canonical, common, sourceRegistration{path: path, head: head, branch: options.Branch}); err == nil {
		t.Fatal("source claim for another repository accepted")
	}
	pointer := filepath.Join(fixture.home, "worktrees", claim.Task, "acme", "app", adoptedWorktreePointerName)
	if err := os.Rename(pointer, pointer+".retained"); err != nil {
		t.Fatal(err)
	}
	listed, err = ListSourceWithDiagnostics(t.Context(), options)
	if err != nil || len(listed.Diagnostics) != 1 || !strings.Contains(listed.Diagnostics[0].Message, "adoption registration") {
		t.Fatalf("missing adoption pointer: %+v %v", listed, err)
	}
	if err := os.Rename(pointer+".retained", pointer); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(t.TempDir(), "home-loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", loop)
	if _, err := ListSourceWithDiagnostics(t.Context(), options); err == nil {
		t.Fatal("unreadable WB home accepted")
	}
}
