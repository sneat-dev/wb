package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targetFixture is a fake Git worktree rooted in a temp directory.
type targetFixture struct {
	dir     string
	files   map[string]string
	git     map[string]string
	gitErr  map[string]error
	slug    string
	slugErr error
}

func newTargetFixture(t *testing.T) *targetFixture {
	t.Helper()
	return &targetFixture{
		dir: t.TempDir(), files: map[string]string{}, git: map[string]string{},
		gitErr: map[string]error{}, slug: "acme/app",
	}
}

func (fixture *targetFixture) deps() TargetDeps {
	return TargetDeps{
		Git: func(_ context.Context, _ string, args ...string) (string, error) {
			key := strings.Join(args, " ")
			if err, ok := fixture.gitErr[key]; ok {
				return "", err
			}
			if value, ok := fixture.git[key]; ok {
				return value, nil
			}
			return "", errors.New("unexpected git " + key)
		},
		ReadFile: func(path string) ([]byte, error) {
			// The resolver reads through the symlink-resolved root, so match on
			// the file name rather than the temp-dir spelling.
			for candidate, value := range fixture.files {
				if filepath.Base(candidate) == filepath.Base(path) {
					return []byte(value), nil
				}
			}
			return nil, os.ErrNotExist
		},
		OriginSlug: func(context.Context, string) (string, error) {
			return fixture.slug, fixture.slugErr
		},
	}
}

func TestResolveTargetRecordsAWorktreeFactSheet(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	fixture.git["rev-parse --show-toplevel"] = fixture.dir + "\n"
	fixture.git["rev-parse --abbrev-ref HEAD"] = "feature\n"
	fixture.git["rev-parse HEAD"] = "abc123\n"
	fixture.git["status --porcelain"] = ""
	fixture.files[filepath.Join(fixture.dir, ".worktree.md")] = "---\nkind: worktree\nwritable: true\nbase_branch: main\ntask: task-one\n---\n"

	target, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps())
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	resolvedDir, err := filepath.EvalSymlinks(fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	if target.Dir != resolvedDir || target.Repository != "acme/app" || target.Branch != "feature" || target.HeadSHA != "abc123" {
		t.Fatalf("target = %#v", target)
	}
	if !target.Managed || target.Canonical || target.Dirty {
		t.Fatalf("marker facts = %#v", target)
	}
	if target.BaseSHA != "abc123" || target.WorktreeName != "task-one" || target.Base != "main" {
		t.Fatalf("base facts = %#v", target)
	}
}

func TestResolveTargetRefusesACanonicalCloneAndADirtyCheckout(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	fixture.git["rev-parse --show-toplevel"] = fixture.dir
	fixture.git["rev-parse --abbrev-ref HEAD"] = "main"
	fixture.git["rev-parse HEAD"] = "abc123"
	fixture.git["status --porcelain"] = " M a.go\n?? b.go\n"
	fixture.files[filepath.Join(fixture.dir, ".worktree.md")] = "---\nkind: canonical\nwritable: false\n---\n"

	_, err := ResolveTarget(context.Background(), fixture.dir, false, true, fixture.deps())
	if err == nil || !IsRequestError(err) || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("canonical refusal = %v", err)
	}
	if !strings.Contains(err.Error(), "wb worktree create") {
		t.Fatalf("the refusal must name the safe remedy: %v", err)
	}

	fixture.files[filepath.Join(fixture.dir, ".worktree.md")] = "---\nkind: worktree\nwritable: true\n---\n"
	_, err = ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps())
	if err == nil || !strings.Contains(err.Error(), "--allow-dirty") {
		t.Fatalf("dirty refusal = %v", err)
	}

	fixture.git["stash create"] = "snapshot123"
	target, err := ResolveTarget(context.Background(), fixture.dir, false, true, fixture.deps())
	if err != nil {
		t.Fatalf("allowed dirty target: %v", err)
	}
	if !target.Dirty || len(target.DirtyFiles) != 2 || target.SnapshotSHA != "snapshot123" || target.BaseSHA != "snapshot123" {
		t.Fatalf("dirty target = %#v", target)
	}
}

func TestResolveTargetRefusesPathsThatAreNotGitWorktrees(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	if _, err := ResolveTarget(context.Background(), filepath.Join(t.TempDir(), "absent"), false, false, fixture.deps()); err == nil {
		t.Fatal("a missing path must be refused")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTarget(context.Background(), file, false, false, fixture.deps()); err == nil {
		t.Fatal("a file must be refused")
	}
	fixture.gitErr["rev-parse --show-toplevel"] = errors.New("not a git repository")
	if _, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps()); err == nil || !strings.Contains(err.Error(), "not a Git worktree") {
		t.Fatalf("non-repository refusal = %v", err)
	}
	if _, err := ResolveTarget(context.Background(), " ", false, false, fixture.deps()); err == nil {
		t.Fatal("an empty path must be refused")
	}
}

func TestResolveTargetToleratesAnUnmanagedCheckoutAndUnknownOrigin(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t)
	fixture.git["rev-parse --show-toplevel"] = fixture.dir
	fixture.git["rev-parse --abbrev-ref HEAD"] = "main"
	fixture.git["rev-parse HEAD"] = "abc123"
	fixture.git["status --porcelain"] = ""
	fixture.slugErr = errors.New("no origin")
	target, err := ResolveTarget(context.Background(), fixture.dir, false, false, fixture.deps())
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target.Managed || target.Repository != "" || target.Canonical {
		t.Fatalf("an unmanaged checkout must be recorded as such: %#v", target)
	}
}

func TestReadCheckoutMarkerParsesOnlyTheMarkersItNeeds(t *testing.T) {
	t.Parallel()
	marker, err := readCheckoutMarker("ignored", func(string) ([]byte, error) {
		return []byte("# comment\n\n---\nkind: \"worktree\"\nwritable: true\ntask: t\nbase_branch: main\nnonsense line\n---\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if marker.kind != "worktree" || !marker.writable || marker.task != "t" || marker.baseBranch != "main" {
		t.Fatalf("marker = %#v", marker)
	}
	if _, err := readCheckoutMarker("ignored", func(string) ([]byte, error) { return nil, os.ErrNotExist }); err == nil {
		t.Fatal("a missing marker must be an error, not a safe default")
	}
}

func TestBoundListTrimsLongRefusals(t *testing.T) {
	t.Parallel()
	if got := boundList([]string{"a", "b"}, 5); len(got) != 2 {
		t.Fatalf("short list = %#v", got)
	}
	got := boundList([]string{"a", "b", "c"}, 2)
	if len(got) != 3 || !strings.Contains(got[2], "more") {
		t.Fatalf("bounded list = %#v", got)
	}
}
