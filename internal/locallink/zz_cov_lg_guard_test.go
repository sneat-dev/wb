package locallink

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// lgCovGitScript installs a fake git that dispatches on its argument string,
// so the guard's two git probes can be made to disagree — the only way its
// intermediate error branches are reachable.
func lgCovGitScript(t *testing.T, body string) {
	t.Helper()
	// PATH is replaced, not extended: a fake git that removes itself can only
	// prove the "the probe could not run" branch if no real git remains
	// reachable behind it.
	dir := lgCovFakeBin(t, "git", body)
	// exec.Command sets argv[0] to the bare name, so a self-removing fake has
	// to be told its own path to be able to disappear.
	t.Setenv("LGCOV_GIT_SELF", filepath.Join(dir, "git"))
	lgCovRestrictPath(t, dir)
}

// lgCovGuardWorktree builds a worktree whose go.work is a real regular file
// naming entry.
func lgCovGuardWorktree(t *testing.T, entry string) string {
	t.Helper()
	worktree := t.TempDir()
	lgCovWriteFile(t, filepath.Join(worktree, streams.GoWorkFile), "go 1.27\n\nuse (\n\t"+entry+"\n)\n")
	return worktree
}

func TestLgCovHasLiveLinkReportsAnUnreadableWorkspace(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	if err := os.Mkdir(filepath.Join(worktree, streams.GoWorkFile), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := HasLiveLink(nil, worktree)
	if err == nil || !strings.Contains(err.Error(), "read go.work in "+worktree) {
		t.Fatalf("error = %v, want the unreadable workspace reported", err)
	}
}

func TestLgCovGuardGitProbeFailures(t *testing.T) {
	t.Run("git cannot be run at all", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovRestrictPath(t, t.TempDir())
		_, err := HasLiveLink(nil, worktree)
		if err == nil || !strings.Contains(err.Error(), "inspect tracked go.work in "+worktree) {
			t.Fatalf("error = %v, want the git-probe failure reported", err)
		}
	})

	t.Run("the working tree comparison fails", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovGitScript(t, `case "$*" in
  *"diff --quiet"*) exit 2 ;;
  *) exit 0 ;;
esac`)
		_, err := HasLiveLink(nil, worktree)
		if err == nil || !strings.Contains(err.Error(), "compare go.work with HEAD in "+worktree) {
			t.Fatalf("error = %v, want the comparison failure reported", err)
		}
	})

	t.Run("a modified tracked go.work is an unconditional link", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovGitScript(t, `case "$*" in
  *"diff --quiet"*) exit 1 ;;
  *) exit 0 ;;
esac`)
		found, err := HasLiveLink(nil, worktree)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].Source != "go.work" || !strings.Contains(found[0].Detail, "./mod") {
			t.Fatalf("found = %#v, want the modified go.work reported as a live link", found)
		}
	})
}

func TestLgCovUnpublishedGoWorkEntriesModuleProbes(t *testing.T) {
	t.Run("a module directory that does not resolve", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./missing")
		lgCovGitScript(t, "exit 0")
		entries, err := unpublishedGoWorkEntries(worktree, []string{"./missing"})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0] != "./missing" {
			t.Fatalf("entries = %#v, want the unresolvable module reported", entries)
		}
	})

	t.Run("a module whose go.mod is not a regular file", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		if err := os.MkdirAll(filepath.Join(worktree, "mod", "go.mod"), 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovGitScript(t, "exit 0")
		entries, err := unpublishedGoWorkEntries(worktree, []string{"./mod"})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0] != "./mod" {
			t.Fatalf("entries = %#v, want the directory-shaped go.mod reported", entries)
		}
	})

	t.Run("a module whose go.mod is not tracked", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovWriteFile(t, filepath.Join(worktree, "mod", "go.mod"), "module example.test/mod\n")
		lgCovGitScript(t, `case "$*" in
  *"cat-file"*"go.work"*) exit 0 ;;
  *"cat-file"*) exit 1 ;;
  *) exit 0 ;;
esac`)
		entries, err := unpublishedGoWorkEntries(worktree, []string{"./mod"})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0] != "./mod" {
			t.Fatalf("entries = %#v, want the untracked module reported", entries)
		}
	})

	t.Run("a tracked and unchanged module is intrinsic", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovWriteFile(t, filepath.Join(worktree, "mod", "go.mod"), "module example.test/mod\n")
		lgCovGitScript(t, "exit 0")
		entries, err := unpublishedGoWorkEntries(worktree, []string{"./mod"})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("entries = %#v, want an intrinsic multi-module workspace accepted", entries)
		}
		if found, err := HasLiveLink(nil, worktree); err != nil || len(found) != 0 {
			t.Fatalf("HasLiveLink = %#v (err %v), want an intrinsic workspace accepted", found, err)
		}
	})

	t.Run("a module probe that cannot run is reported", func(t *testing.T) {
		worktree := lgCovGuardWorktree(t, "./mod")
		lgCovWriteFile(t, filepath.Join(worktree, "mod", "go.mod"), "module example.test/mod\n")
		// The fake git removes itself while answering the change comparison,
		// so the later module probe fails to start at all.
		lgCovGitScript(t, `case "$*" in
  *"diff --quiet"*) /bin/rm -f "$LGCOV_GIT_SELF"; exit 0 ;;
  *) exit 0 ;;
esac`)
		_, err := unpublishedGoWorkEntries(worktree, []string{"./mod"})
		if err == nil || !strings.Contains(err.Error(), "inspect workspace module ./mod in "+worktree) {
			t.Fatalf("error = %v, want the module-probe failure reported", err)
		}
	})
}

func TestLgCovGuardGitProbeHelpers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := initRepository(t)

	// A path that is not in HEAD answers "absent" rather than erroring.
	if exists, err := gitPathExistsAtHEAD(root, "go.work"); err != nil || exists {
		t.Fatalf("exists = %v, err = %v, want false for a path outside HEAD", exists, err)
	}
	if exists, err := gitPathExistsAtHEAD(root, "tracked.txt"); err != nil || !exists {
		t.Fatalf("exists = %v, err = %v, want true for a committed path", exists, err)
	}

	if unchanged, err := gitPathUnchangedFromHEAD(root, "tracked.txt"); err != nil || !unchanged {
		t.Fatalf("unchanged = %v, err = %v, want an untouched path to compare equal", unchanged, err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if unchanged, err := gitPathUnchangedFromHEAD(root, "tracked.txt"); err != nil || unchanged {
		t.Fatalf("unchanged = %v, err = %v, want a modified path reported as changed", unchanged, err)
	}

	// With no git binary at all the probes see a failure that is not an exit
	// status, and must report it rather than reading it as "absent".
	lgCovRestrictPath(t, t.TempDir())
	if _, err := gitPathExistsAtHEAD(root, "tracked.txt"); err == nil {
		t.Fatal("probing with no git binary reported success")
	}
	if _, err := gitPathUnchangedFromHEAD(root, "tracked.txt"); err == nil {
		t.Fatal("comparing with no git binary reported success")
	}
}

func TestLgCovRefusalErrorAndRefused(t *testing.T) {
	t.Parallel()
	plain := &Refusal{Code: RefusalNotRecordable, Message: "cannot record"}
	if got := plain.Error(); got != "cannot record" {
		t.Fatalf("Error() = %q, want the bare message", got)
	}
	sanctioned := &Refusal{Message: "cannot record", Sanctioned: []string{"wb stream join", "wb stream start"}}
	if got := sanctioned.Error(); !strings.Contains(got, "run: wb stream join or wb stream start") {
		t.Fatalf("Error() = %q, want the sanctioned commands named", got)
	}

	wrapped := errors.New("just an error")
	if refusal, ok := Refused(wrapped); ok || refusal != nil {
		t.Fatalf("Refused = %#v, %v, want an ordinary error rejected", refusal, ok)
	}
	if refusal, ok := Refused(sanctioned); !ok || refusal != sanctioned {
		t.Fatalf("Refused = %#v, %v, want the refusal recognised", refusal, ok)
	}
}

func TestLgCovLinkGoFailurePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	declarations := []streams.Declaration{{
		Identity: streams.Identity{Ecosystem: streams.EcosystemGo, Name: "github.com/acme/library/backend"},
		Version:  "v0.4.0",
	}}
	goodLibrary := t.TempDir()
	lgCovWriteFile(t, filepath.Join(goodLibrary, "backend", "go.mod"), goLibraryModule)

	engine := func(git Git) *Engine {
		return &Engine{Git: git, Node: newFakeNode(), CacheRoot: t.TempDir()}
	}

	t.Run("the consumer modules cannot be scanned", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		if err := os.Symlink(filepath.Join(consumer, "nowhere"), filepath.Join(consumer, "go.mod")); err != nil {
			t.Fatal(err)
		}
		_, err := engine(newFakeGit()).linkGo(ctx, goodLibrary, consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "scan Go modules in "+consumer) {
			t.Fatalf("error = %v, want the scan failure reported", err)
		}
	})

	t.Run("the consumer has no module", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		_, err := engine(newFakeGit()).linkGo(ctx, goodLibrary, consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "contains no go.mod") {
			t.Fatalf("error = %v, want the missing-consumer-module refusal", err)
		}
	})

	t.Run("the library modules cannot be scanned", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, "backend", "go.mod"), "module github.com/acme/app/backend\n")
		library := t.TempDir()
		if err := os.Symlink(filepath.Join(library, "nowhere"), filepath.Join(library, "go.mod")); err != nil {
			t.Fatal(err)
		}
		_, err := engine(newFakeGit()).linkGo(ctx, library, consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "scan Go modules in "+library) {
			t.Fatalf("error = %v, want the library scan failure reported", err)
		}
	})

	t.Run("the library has no module", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, "backend", "go.mod"), "module github.com/acme/app/backend\n")
		_, err := engine(newFakeGit()).linkGo(ctx, t.TempDir(), consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "contains no go.mod to place in the workspace") {
			t.Fatalf("error = %v, want the missing-library-module refusal", err)
		}
	})

	t.Run("the exclude cannot be written", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, "backend", "go.mod"), "module github.com/acme/app/backend\n")
		git := lgCovNewGit()
		git.excludeErr = errors.New("exclude is read-only")
		_, err := engine(git).linkGo(ctx, goodLibrary, consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "exclude is read-only") {
			t.Fatalf("error = %v, want the exclude failure reported", err)
		}
		if fileExists(filepath.Join(consumer, goWorkFile)) {
			t.Fatal("a failed exclude still wrote go.work")
		}
	})

	t.Run("the workspace cannot be written", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, "backend", "go.mod"), "module github.com/acme/app/backend\n")
		if err := os.Mkdir(filepath.Join(consumer, goWorkFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := engine(newFakeGit()).linkGo(ctx, goodLibrary, consumer, declarations, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "write go.work") {
			t.Fatalf("error = %v, want the workspace write failure reported", err)
		}
	})
}

func TestLgCovGoDirectiveAndVersionComparison(t *testing.T) {
	t.Parallel()
	t.Run("an unreadable manifest is skipped", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		modules := []streams.GoModule{
			{Manifest: "missing/go.mod", Directory: "missing"},
			{Manifest: "present/go.mod", Directory: "present"},
		}
		lgCovWriteFile(t, filepath.Join(root, "present", "go.mod"), "module example.test/present\n\ngo 1.23.4\n")
		if got := goDirective(root, modules); got != "1.23.4" {
			t.Fatalf("goDirective = %q, want the readable module's directive", got)
		}
	})

	t.Run("a manifest without a go directive is skipped", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		lgCovWriteFile(t, filepath.Join(root, "mod", "go.mod"), "module example.test/mod\n")
		if got := goDirective(root, []streams.GoModule{{Manifest: "mod/go.mod", Directory: "mod"}}); got != "" {
			t.Fatalf("goDirective = %q, want no directive", got)
		}
	})

	t.Run("the newest directive wins", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		lgCovWriteFile(t, filepath.Join(root, "old", "go.mod"), "module example.test/old\n\ngo 1.21\n")
		lgCovWriteFile(t, filepath.Join(root, "new", "go.mod"), "module example.test/new\n\ngo 1.27\n")
		modules := []streams.GoModule{{Manifest: "old/go.mod", Directory: "old"}, {Manifest: "new/go.mod", Directory: "new"}}
		if got := goDirective(root, modules); got != "1.27" {
			t.Fatalf("goDirective = %q, want the newest directive", got)
		}
	})

	if compareGoVersions("1.10", "1.9") <= 0 {
		t.Fatal("compareGoVersions did not order 1.10 above 1.9")
	}
	if compareGoVersions("1.9", "1.10") >= 0 {
		t.Fatal("compareGoVersions did not order 1.9 below 1.10")
	}
	if compareGoVersions("1.27", "1.27") != 0 {
		t.Fatal("compareGoVersions did not report equal versions")
	}
	if compareGoVersions("1.27.3", "1.27") <= 0 {
		t.Fatal("compareGoVersions did not order a longer version above its prefix")
	}
}

func TestLgCovRemoveGoWorkAndStillReferences(t *testing.T) {
	t.Parallel()
	t.Run("a workspace sum that cannot be removed is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, goWorkFile), "go 1.27\n")
		sumPath := filepath.Join(consumer, goWorkSum)
		if err := os.MkdirAll(sumPath, 0o755); err != nil {
			t.Fatal(err)
		}
		lgCovWriteFile(t, filepath.Join(sumPath, "keep.txt"), "keep\n")
		err := removeGoWork(consumer)
		if err == nil || !strings.Contains(err.Error(), "remove "+goWorkSum) {
			t.Fatalf("error = %v, want the failed sum removal reported", err)
		}
	})

	t.Run("an empty library never looks referenced", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, goWorkFile), "go 1.27\n")
		still, err := goWorkStillReferencesLibrary(consumer, "  ")
		if err != nil || still {
			t.Fatalf("stillUsed = %v, err = %v, want false with no library", still, err)
		}
	})

	t.Run("an unreadable workspace is reported", func(t *testing.T) {
		t.Parallel()
		consumer := t.TempDir()
		if err := os.Mkdir(filepath.Join(consumer, goWorkFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := goWorkStillReferencesLibrary(consumer, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "read go.work") {
			t.Fatalf("error = %v, want the unreadable workspace reported", err)
		}
	})

	t.Run("a subdirectory of the library counts as a reference", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		if err := os.MkdirAll(filepath.Join(library, "backend"), 0o755); err != nil {
			t.Fatal(err)
		}
		consumer := t.TempDir()
		lgCovWriteFile(t, filepath.Join(consumer, goWorkFile), "go 1.27\n\nuse (\n\t"+filepath.Join(library, "backend")+"\n)\n")
		still, err := goWorkStillReferencesLibrary(consumer, library)
		if err != nil || !still {
			t.Fatalf("stillUsed = %v, err = %v, want a subdirectory entry to count", still, err)
		}
	})
}

// lgCovNodeStub is a Node port whose every method can be made to fail
// independently, which is what linkNpm's error branches need.
type lgCovNodeStub struct {
	installErr  error
	buildErr    error
	buildDist   string
	linkErr     error
	linkResult  NodeLinkResult
	unlinkErr   error
	note        string
	siblingsErr error
}

func (node *lgCovNodeStub) FrozenInstall(context.Context, string) error { return node.installErr }
func (node *lgCovNodeStub) Build(context.Context, string, string) (string, error) {
	return node.buildDist, node.buildErr
}
func (node *lgCovNodeStub) Link(context.Context, string, string, string) (NodeLinkResult, error) {
	return node.linkResult, node.linkErr
}
func (node *lgCovNodeStub) Unlink(context.Context, string, string) (string, error) {
	return node.note, node.unlinkErr
}
func (node *lgCovNodeStub) LinkSiblings(context.Context, string, []string) error {
	return node.siblingsErr
}

func TestLgCovLinkNpmFailurePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	library := t.TempDir()
	consumer := t.TempDir()

	declaration := streams.Declaration{
		Identity: streams.Identity{Ecosystem: streams.EcosystemNpm, Name: "@acme/core", Directory: "libs/core", Workspace: ""},
		Version:  "1.0.0",
	}

	t.Run("no Node toolchain", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{}
		_, err := engine.linkNpm(ctx, library, consumer, declaration, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "no Node toolchain available") {
			t.Fatalf("error = %v, want the missing-toolchain refusal", err)
		}
	})

	t.Run("an unusable library workspace", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Node: &lgCovNodeStub{}}
		bad := declaration
		bad.Identity.Workspace = string(filepath.Separator) + "absolute"
		_, err := engine.linkNpm(ctx, library, consumer, bad, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "is not inside worktree") {
			t.Fatalf("error = %v, want the library workspace refusal", err)
		}
	})

	t.Run("an unusable consumer workspace", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Node: &lgCovNodeStub{}}
		bad := declaration
		bad.Workspace = ".."
		_, err := engine.linkNpm(ctx, library, consumer, bad, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "is not inside worktree") {
			t.Fatalf("error = %v, want the consumer workspace refusal", err)
		}
	})

	t.Run("a link failure is reported", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Node: &lgCovNodeStub{linkErr: errors.New("symlink refused")}}
		_, err := engine.linkNpm(ctx, library, consumer, declaration, "acme/library", "hash")
		if err == nil || !strings.Contains(err.Error(), "symlink refused") {
			t.Fatalf("error = %v, want the link failure reported", err)
		}
	})

	t.Run("a restored previous package is added to the artefacts", func(t *testing.T) {
		t.Parallel()
		node := &lgCovNodeStub{
			linkResult: NodeLinkResult{
				Previous:  "node_modules/@acme/core.wb-locallink-backup",
				Artifacts: []string{"node_modules/@acme/core/.wb-locallink-stage"},
			},
		}
		engine := &Engine{Node: node}
		link, err := engine.linkNpm(ctx, library, consumer, declaration, "acme/library", "hash")
		if err != nil {
			t.Fatal(err)
		}
		if len(link.Artifacts) != 2 {
			t.Fatalf("artifacts = %#v, want the restored previous path recorded once more", link.Artifacts)
		}
		if link.Workspace != declaration.Workspace || link.LibraryRepository != "acme/library" || link.State != streams.LinkStateApplied {
			t.Fatalf("link = %#v, want a complete applied record", link)
		}
	})

	t.Run("an empty artefact list falls back to the package path", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Node: &lgCovNodeStub{}}
		link, err := engine.linkNpm(ctx, library, consumer, declaration, "acme/library", "hash")
		if err != nil {
			t.Fatal(err)
		}
		if len(link.Artifacts) != 1 || link.Artifacts[0] != "node_modules/@acme/core" {
			t.Fatalf("artifacts = %#v, want the conventional package path", link.Artifacts)
		}
	})

	if !containsString([]string{"a", "b"}, "a") {
		t.Fatal("containsString missed a present value")
	}
	if containsString([]string{"a", "b"}, "c") {
		t.Fatal("containsString reported an absent value")
	}
}

func TestLgCovWorkspacePathRejections(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()

	if _, err := workspacePath(worktree, string(filepath.Separator)+"absolute"); err == nil || !strings.Contains(err.Error(), "is not inside worktree") {
		t.Fatalf("error = %v, want the absolute-path refusal", err)
	}
	if _, err := workspacePath(worktree, ".."); err == nil || !strings.Contains(err.Error(), "is not inside worktree") {
		t.Fatalf("error = %v, want the parent-path refusal", err)
	}
	if _, err := workspacePath(filepath.Join(worktree, "missing"), "."); err != nil {
		t.Fatalf("a dot workspace must not need the worktree to resolve: %v", err)
	}
	if _, err := workspacePath(filepath.Join(worktree, "missing"), "libs/app"); err == nil || !strings.Contains(err.Error(), "resolve worktree") {
		t.Fatalf("error = %v, want the unresolvable-worktree report", err)
	}
	if _, err := workspacePath(worktree, "libs/app"); err == nil || !strings.Contains(err.Error(), "resolve npm workspace") {
		t.Fatalf("error = %v, want the unresolvable-workspace report", err)
	}
}

func TestLgCovSkippedCheckError(t *testing.T) {
	t.Parallel()
	skipped := &SkippedCheck{Check: "frozen-install", Reason: "no lockfile"}
	if got := skipped.Error(); got != "frozen-install could not be evaluated: no lockfile" {
		t.Fatalf("Error() = %q, want the check and reason", got)
	}
	if _, ok := Skipped(errors.New("ordinary")); ok {
		t.Fatal("Skipped recognised an ordinary error")
	}
}
