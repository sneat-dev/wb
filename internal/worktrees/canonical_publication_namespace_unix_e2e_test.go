//go:build (darwin || linux) && e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // The native repository helper pins process-wide HOME and WB/XDG roots.
func TestE2ECanonicalPublicationGuardNativeNamespaceRefusals(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	mismatch := createMismatchedWorktree(t, fixture, "guard-mismatch", "acme", "other", "other")
	before := gitTestOutput(t, mismatch, "rev-parse", "HEAD")
	// Defensive inconsistent query observations at the existing ordinary-Git boundary.
	// The replacement was captured from another actual native canonical clone;
	// all queries still execute native Git, and later identity reads remain unchanged.
	observedCommon := gitTestOutput(t, fixture.canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	fault := &canonicalPublicationFirstCommonObservation{Runner: runner.New(), directory: mismatch, common: observedCommon}
	got, err := Guard(withGitRunner(context.Background(), fault), mismatch, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if !fault.hit || err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "belongs to a different canonical clone") {
		t.Fatalf("inconsistent observed common directory = %+v, %v", got, err)
	}
	if after := gitTestOutput(t, mismatch, "rev-parse", "HEAD"); after != before {
		t.Fatalf("mismatch refusal changed HEAD: %s", after)
	}
	protected := filepath.Join(fixture.canonical, ".worktrees", "protected")
	gitTest(t, fixture.canonical, "worktree", "add", "--force", protected, "main")
	got, err = Guard(context.Background(), protected, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err == nil || !reflect.DeepEqual(got, GuardResult{}) || !strings.Contains(err.Error(), "protected base branch") {
		t.Fatalf("native protected branch = %+v, %v", got, err)
	}
	home := t.TempDir()
	if err := os.Symlink(".wb", filepath.Join(home, ".wb")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	_, cause := wbhome.Resolve(fixture.projectsRoot)
	if cause == nil {
		t.Fatal("native legacy-home loop did not refuse")
	}
	got, err = Guard(context.Background(), protected, GuardOptions{ProjectsRoot: fixture.projectsRoot, Base: "main"})
	if err == nil || !reflect.DeepEqual(got, GuardResult{}) || err.Error() != cause.Error() {
		t.Fatalf("native legacy-home propagation = %+v, %v; cause %v", got, err, cause)
	}
}

// canonicalPublicationFirstCommonObservation substitutes only one observed
// response after the genuine native command has completed successfully.
type canonicalPublicationFirstCommonObservation struct {
	runner.Runner
	directory, common string
	hit               bool
}

func (f *canonicalPublicationFirstCommonObservation) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := f.Runner.RunOpts(ctx, dir, options, name, args...)
	if err == nil && !f.hit && dir == f.directory && name == "git" && reflect.DeepEqual(args, []string{"-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir"}) {
		f.hit = true
		result.CombinedOutput = f.common + "\n"
	}
	return result, err
}
