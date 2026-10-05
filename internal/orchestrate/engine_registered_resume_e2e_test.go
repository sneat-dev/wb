//go:build e2e

package orchestrate

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EEngineRunResumesActualRegisteredOperationCheckout(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	options := fixture.options()
	initial, err := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if err != nil || len(initial) != 1 || initial[0].Status != "changed" {
		t.Fatalf("initial native engine creation: results=%+v err=%v", initial, err)
	}
	checkout := initial[0].WorktreeDir
	registration := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain")
	if !strings.Contains(registration, "worktree "+checkout+"\n") || !strings.Contains(registration, "branch refs/heads/"+options.Branch+"\n") {
		t.Fatalf("actual operation checkout was not registered: checkout=%q registration=%s", checkout, registration)
	}
	manifestBefore, err := worktrees.ReadManifest(checkout)
	if err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(checkout, "dependency.txt"), "old\n")
	writeEngineFile(t, filepath.Join(checkout, "unrelated.txt"), "retained scratch\n")
	canonicalBefore := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"))
	options.Resume = true
	resumed, resumeErr := Run(context.Background(), []Repository{fixture.repository}, textHandler{}, options)
	if after := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt")); after != canonicalBefore {
		t.Fatalf("resume mutated canonical: before=%q after=%q", canonicalBefore, after)
	}
	if after := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain"); after != registration {
		t.Fatalf("resume changed Git registration: before=%s after=%s", registration, after)
	}
	if after := mustReadEngineFile(t, filepath.Join(checkout, "unrelated.txt")); after != "retained scratch\n" {
		t.Fatalf("resume changed unrelated local contents: %q", after)
	}
	manifestAfter, err := worktrees.ReadManifest(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifestAfter, manifestBefore) {
		t.Fatal("resume changed immutable manifest")
	}
	if resumeErr != nil || len(resumed) != 1 || resumed[0].Status != "changed" || resumed[0].WorktreeDir != checkout {
		t.Fatalf("registered checkout must resume through public engine: results=%+v err=%v", resumed, resumeErr)
	}
	if after := mustReadEngineFile(t, filepath.Join(checkout, "dependency.txt")); after != "new\n" {
		t.Fatalf("resume did not apply in registered checkout: %q", after)
	}
}
