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

// committedWorkspace is a repository's own multi-module workspace — the shape
// sneat-co/commitius commits at its root.
const committedWorkspace = "go 1.27\n\nuse ./backend\n"

// newCommittedWorkspaceConsumer turns the fixture's consumer into a Git
// repository whose committed HEAD carries go.work, exactly as a clone of such
// a repository arrives.
func newCommittedWorkspaceConsumer(t *testing.T, fixture fixture) {
	t.Helper()
	runGuardTestGit(t, fixture.consumer, "init", "-q")
	runGuardTestGit(t, fixture.consumer, "config", "user.name", "WB Test")
	runGuardTestGit(t, fixture.consumer, "config", "user.email", "wb-test@example.invalid")
	writeGuardTestFile(t, fixture.consumer, goWorkFile, committedWorkspace)
	commitGuardTestRepository(t, fixture.consumer)
}

func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	return string(output)
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// The 2026-11-08 data loss: `--undo` on a consumer with a committed go.work
// and no link record deleted the tracked file. Without a record there is
// nothing to undo, and a tracked file is never WB's to delete.
func TestUndoWithoutARecordLeavesACommittedGoWorkUntouched(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	newCommittedWorkspaceConsumer(t, fixture)

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if len(result.Consumers) != 1 || !result.Consumers[0].Skipped ||
		!strings.Contains(result.Consumers[0].Reason, "nothing to undo") {
		t.Fatalf("consumers = %#v, want one skipped consumer with nothing to undo", result.Consumers)
	}
	if got := readFileString(t, filepath.Join(fixture.consumer, goWorkFile)); got != committedWorkspace {
		t.Fatalf("go.work = %q, want the committed workspace untouched", got)
	}
	if status := gitStatusPorcelain(t, fixture.consumer); status != "" {
		t.Fatalf("undo left the consumer dirty:\n%s", status)
	}
	// A committed intrinsic workspace is not a link to either guard.
	links, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(links) != 0 {
		t.Fatalf("links = %#v, err = %v; a committed workspace is repository content", links, err)
	}
}

// A real link written over a committed go.work: undo restores the tracked
// file from HEAD and removes only the untracked artefacts the record names.
func TestUndoRestoresATrackedGoWorkALinkOverwrote(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	newCommittedWorkspaceConsumer(t, fixture)
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(fixture.consumer, goWorkFile)
	if got := readFileString(t, workspace); !strings.Contains(got, fixture.library) {
		t.Fatalf("the link did not write the workspace: %q", got)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if member, _ := stream.Member("acme/app"); len(member.Links) == 0 {
		t.Fatal("the link was not recorded")
	}
	// The Go toolchain writes go.work.sum beside a live workspace; the record
	// names it. An unrelated untracked file is not the link's to remove.
	writeGuardTestFile(t, fixture.consumer, goWorkSum, "sum\n")
	writeGuardTestFile(t, fixture.consumer, "notes.txt", "mine\n")

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if got := readFileString(t, workspace); got != committedWorkspace {
		t.Fatalf("go.work = %q, want it restored to HEAD", got)
	}
	if fileExists(filepath.Join(fixture.consumer, goWorkSum)) {
		t.Error("the recorded untracked go.work.sum survived undo")
	}
	if !fileExists(filepath.Join(fixture.consumer, "notes.txt")) {
		t.Error("undo removed an untracked file no record names")
	}
	if status := gitStatusPorcelain(t, fixture.consumer); status != "?? notes.txt\n" {
		t.Fatalf("status after undo = %q, want only the unrelated file", status)
	}
	if notes := strings.Join(result.Consumers[0].Notes, "\n"); !strings.Contains(notes, "restored tracked go.work from HEAD") {
		t.Errorf("notes = %q, want the restore reported", notes)
	}
	stream, err = fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if member, _ := stream.Member("acme/app"); len(member.Links) != 0 {
		t.Fatalf("links survived undo: %#v", member.Links)
	}
}

// The link guard's refusal for a go.work no stream records never names
// `--undo`, which cannot clear it, and never names a command that deletes a
// tracked file.
func TestHasLiveLinkNamesAHonestRemedyForAnUnrecordedGoWork(t *testing.T) {
	t.Parallel()

	t.Run("untracked", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
		links, err := HasLiveLink(nil, worktree)
		if err != nil || len(links) != 1 {
			t.Fatalf("links = %#v, err = %v", links, err)
		}
		if strings.Contains(links[0].Sanctioned, "--undo") || !strings.Contains(links[0].Sanctioned, "rm ") ||
			strings.Contains(links[0].Sanctioned, goWorkSum) {
			t.Fatalf("sanctioned = %q, want the manual removal of an untracked file", links[0].Sanctioned)
		}
	})

	t.Run("tracked and modified", func(t *testing.T) {
		t.Parallel()
		worktree := initGuardTestRepository(t)
		writeGuardTestFile(t, worktree, "backend/go.mod", "module example.test/backend\n\ngo 1.27\n")
		writeGuardTestFile(t, worktree, goWorkFile, committedWorkspace)
		commitGuardTestRepository(t, worktree)
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library\n)\n")
		links, err := HasLiveLink(nil, worktree)
		if err != nil || len(links) != 1 {
			t.Fatalf("links = %#v, err = %v", links, err)
		}
		sanctioned := links[0].Sanctioned
		if strings.Contains(sanctioned, "--undo") || strings.Contains(sanctioned, "rm ") ||
			!strings.Contains(sanctioned, "restore --source=HEAD") {
			t.Fatalf("sanctioned = %q, want commit-or-restore, never a deletion", sanctioned)
		}
	})

	t.Run("committed with an entry outside the repository", func(t *testing.T) {
		t.Parallel()
		worktree := initGuardTestRepository(t)
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
		commitGuardTestRepository(t, worktree)
		links, err := HasLiveLink(nil, worktree)
		if err != nil || len(links) != 1 {
			t.Fatalf("links = %#v, err = %v", links, err)
		}
		sanctioned := links[0].Sanctioned
		if strings.Contains(sanctioned, "--undo") || strings.Contains(sanctioned, "rm ") ||
			!strings.Contains(sanctioned, "in a commit") {
			t.Fatalf("sanctioned = %q, want a committed fix, never a deletion", sanctioned)
		}
	})

	t.Run("recorded", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		library := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse "+library+"\n")
		store := fakeLiveLinkStore{links: []streams.StreamLink{{
			Stream: "s", Repository: "acme/app", Link: lgCovGoWorkLink(library),
		}}}
		links, err := HasLiveLink(store, worktree)
		if err != nil || len(links) != 2 {
			t.Fatalf("links = %#v, err = %v", links, err)
		}
		if links[1].Source != "go.work" || links[1].Sanctioned != "wb deps propagate local --to "+worktree+" --undo" {
			t.Fatalf("go.work link = %#v, want the recorded link's undo", links[1])
		}
	})
}

type fakeLiveLinkStore struct{ links []streams.StreamLink }

func (store fakeLiveLinkStore) LiveLinksForWorktree(string) ([]streams.StreamLink, error) {
	return store.links, nil
}

func TestClassifyGoWorkReportsProvenanceAndGitFailures(t *testing.T) {
	t.Parallel()
	failed := errors.New("git is gone")
	yes := func(string, string) (bool, error) { return true, nil }
	no := func(string, string) (bool, error) { return false, nil }
	broken := func(string, string) (bool, error) { return false, failed }
	cases := []struct {
		name      string
		files     headFiles
		want      GoWorkProvenance
		wantError bool
	}{
		{"untracked", headFiles{exists: no}, GoWorkUntracked, false},
		{"committed", headFiles{exists: yes, unchanged: yes}, GoWorkCommitted, false},
		{"modified", headFiles{exists: yes, unchanged: no}, GoWorkModified, false},
		{"presence unknown", headFiles{exists: broken}, "", true},
		{"difference unknown", headFiles{exists: yes, unchanged: broken}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.files.classify("/worktree", goWorkFile)
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("classify = (%q, %v), want (%q, error %v)", got, err, tc.want, tc.wantError)
			}
		})
	}
	// The exported entry point reads real Git: a plain directory has no HEAD.
	if got, err := ClassifyGoWork(t.TempDir()); err != nil || got != GoWorkUntracked {
		t.Fatalf("ClassifyGoWork(non-repository) = (%q, %v), want untracked", got, err)
	}
}

func TestHasLiveLinkReportsAGoWorkWhoseProvenanceCannotBeRead(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
	failed := errors.New("git is gone")
	_, err := hasLiveLinkWith(nil, worktree, headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if !errors.Is(err, failed) {
		t.Fatalf("err = %v, want the provenance failure", err)
	}
}

func TestRestoreLinkArtefactsNeverDeletesTrackedContent(t *testing.T) {
	t.Parallel()
	failed := errors.New("git is gone")
	yes := func(string, string) (bool, error) { return true, nil }
	no := func(string, string) (bool, error) { return false, nil }
	broken := func(string, string) (bool, error) { return false, failed }

	t.Run("untracked artefacts are removed, absent ones ignored", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n")
		notes, err := headFiles{exists: no}.restoreLinkArtefacts(worktree, []string{goWorkFile, goWorkSum})
		if err != nil || len(notes) != 0 || fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("notes = %v, err = %v; want the untracked workspace removed silently", notes, err)
		}
	})

	t.Run("an unchanged tracked artefact is left alone", func(t *testing.T) {
		t.Parallel()
		restored := false
		files := headFiles{exists: yes, unchanged: yes, restore: func(string, string) error { restored = true; return nil }}
		notes, err := files.restoreLinkArtefacts(t.TempDir(), []string{goWorkSum})
		if err != nil || restored || len(notes) != 1 || !strings.Contains(notes[0], "unchanged from HEAD") {
			t.Fatalf("notes = %v, err = %v, restored = %v", notes, err, restored)
		}
	})

	t.Run("a committed linked go.work is refused, not cleared", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "linked\n")
		_, err := headFiles{exists: yes, unchanged: yes}.restoreLinkArtefacts(worktree, []string{goWorkFile})
		if err == nil || !strings.Contains(err.Error(), "revert that commit") || !fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("err = %v; want the committed link refused and the file kept", err)
		}
	})

	t.Run("a modified tracked artefact is restored, not removed", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "linked\n")
		var restoredPath string
		files := headFiles{exists: yes, unchanged: no, restore: func(_, path string) error { restoredPath = path; return nil }}
		notes, err := files.restoreLinkArtefacts(worktree, []string{goWorkFile})
		if err != nil || restoredPath != goWorkFile || !fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("notes = %v, err = %v, restored %q", notes, err, restoredPath)
		}
	})

	t.Run("a recorded path outside the worktree is refused", func(t *testing.T) {
		t.Parallel()
		_, err := headFiles{exists: no}.restoreLinkArtefacts(t.TempDir(), []string{"../go.work"})
		if err == nil || !strings.Contains(err.Error(), "outside the consumer worktree") {
			t.Fatalf("err = %v, want the escaping path refused", err)
		}
	})

	for _, tc := range []struct {
		name  string
		files headFiles
	}{
		{"presence unknown", headFiles{exists: broken}},
		{"difference unknown", headFiles{exists: yes, unchanged: broken}},
		{"restore failed", headFiles{exists: yes, unchanged: no, restore: func(string, string) error { return failed }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tc.files.restoreLinkArtefacts(t.TempDir(), []string{goWorkFile}); !errors.Is(err, failed) {
				t.Fatalf("err = %v, want the Git failure", err)
			}
		})
	}

	t.Run("an untracked artefact that cannot be removed is reported", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, filepath.Join(goWorkSum, "keep.txt"), "keep\n")
		if _, err := (headFiles{exists: no}).restoreLinkArtefacts(worktree, []string{goWorkSum}); err == nil ||
			!strings.Contains(err.Error(), "remove "+goWorkSum) {
			t.Fatalf("err = %v, want the failed removal reported", err)
		}
	})

	t.Run("a record without artefacts falls back to the Go workspace pair", func(t *testing.T) {
		t.Parallel()
		if got := goWorkArtefacts(streams.Link{}); strings.Join(got, ",") != goWorkFile+","+goWorkSum {
			t.Fatalf("artefacts = %v", got)
		}
	})
}

func TestUndoReportsAGoWorkItCouldNotRestore(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	library := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse "+library+"\n")
	engine := &Engine{}
	failed := errors.New("git is gone")
	outcome, remaining := engine.undoLinksWith(context.Background(), worktree, []streams.Link{lgCovGoWorkLink(library)},
		headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if len(outcome.Errors) != 1 || len(remaining) != 1 {
		t.Fatalf("outcome = %#v, remaining = %#v; a failed restore keeps its record", outcome, remaining)
	}
}

func TestUndoWithoutARecordReportsAGoWorkItCouldNotClassify(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
	failed := errors.New("git is gone")
	outcome := undoUnrecordedWith(worktree, headFiles{exists: func(string, string) (bool, error) { return false, failed }})
	if len(outcome.Errors) != 1 || !fileExists(filepath.Join(worktree, goWorkFile)) {
		t.Fatalf("outcome = %#v; want the failure reported and the file kept", outcome)
	}
}

func TestGitRestoreFromHEADReportsAFailure(t *testing.T) {
	t.Parallel()
	if err := gitRestoreFromHEAD(t.TempDir(), goWorkFile); err == nil {
		t.Fatal("restoring outside a repository reported success")
	}
}

// The `wb worktree end` half: a committed, unchanged workspace is repository
// content; untracked or modified ones still refuse with an honest remedy.
func TestRetirementGoWorkSignalIgnoresOnlyACommittedUnchangedWorkspace(t *testing.T) {
	t.Parallel()

	t.Run("no go.work", func(t *testing.T) {
		t.Parallel()
		signal, err := RetirementGoWorkSignal(t.TempDir(), false)
		if err != nil || signal != nil {
			t.Fatalf("signal = %#v, err = %v, want none", signal, err)
		}
	})

	t.Run("unreadable go.work", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		if err := os.Mkdir(filepath.Join(worktree, goWorkFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := RetirementGoWorkSignal(worktree, false); err == nil {
			t.Fatal("a go.work directory reported no error")
		}
	})

	t.Run("committed and unchanged", func(t *testing.T) {
		t.Parallel()
		worktree := initGuardTestRepository(t)
		writeGuardTestFile(t, worktree, "backend/go.mod", "module example.test/backend\n\ngo 1.27\n")
		writeGuardTestFile(t, worktree, goWorkFile, committedWorkspace)
		commitGuardTestRepository(t, worktree)
		signal, err := RetirementGoWorkSignal(worktree, false)
		if err != nil || signal != nil {
			t.Fatalf("signal = %#v, err = %v; a committed workspace is not a link", signal, err)
		}
	})

	t.Run("untracked", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
		signal, err := RetirementGoWorkSignal(worktree, false)
		if err != nil || signal == nil || !strings.Contains(signal.Detail, "(untracked)") || !strings.Contains(signal.Sanctioned, "rm ") {
			t.Fatalf("signal = %#v, err = %v", signal, err)
		}
	})

	t.Run("provenance unknown", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
		failed := errors.New("git is gone")
		_, err := retirementGoWorkSignalWith(worktree, false, headFiles{exists: func(string, string) (bool, error) { return false, failed }})
		if !errors.Is(err, failed) {
			t.Fatalf("err = %v, want the provenance failure", err)
		}
	})
}

// Provenance is judged at the worktree's own path: a committed go.work at the
// repository root says nothing about an untracked one in a subdirectory.
func TestClassifyGoWorkJudgesTheWorktreePathNotTheRepositoryRoot(t *testing.T) {
	t.Parallel()
	repository := initGuardTestRepository(t)
	writeGuardTestFile(t, repository, goWorkFile, committedWorkspace)
	commitGuardTestRepository(t, repository)
	nested := filepath.Join(repository, "app")
	writeGuardTestFile(t, nested, goWorkFile, "go 1.27\n\nuse /elsewhere/library\n")
	if got, err := ClassifyGoWork(nested); err != nil || got != GoWorkUntracked {
		t.Fatalf("ClassifyGoWork(nested) = (%q, %v), want untracked", got, err)
	}
	if got, err := ClassifyGoWork(repository); err != nil || got != GoWorkCommitted {
		t.Fatalf("ClassifyGoWork(root) = (%q, %v), want committed", got, err)
	}
}

// Git failing is never read as "not committed": undo would remove a tracked
// file. An unborn HEAD and a directory outside any checkout are the only
// failures that genuinely mean nothing is tracked.
func TestGitPathExistsAtHEADNeverReadsAGitFailureAsAbsent(t *testing.T) {
	t.Parallel()

	t.Run("outside any checkout", func(t *testing.T) {
		t.Parallel()
		if exists, err := gitPathExistsAtHEAD(t.TempDir(), goWorkFile); err != nil || exists {
			t.Fatalf("exists = %v, err = %v, want absent", exists, err)
		}
	})

	t.Run("unborn HEAD", func(t *testing.T) {
		t.Parallel()
		repository := initGuardTestRepository(t)
		writeGuardTestFile(t, repository, goWorkFile, committedWorkspace)
		if exists, err := gitPathExistsAtHEAD(repository, goWorkFile); err != nil || exists {
			t.Fatalf("exists = %v, err = %v, want absent before the first commit", exists, err)
		}
	})

	t.Run("an unreadable repository", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		writeGuardTestFile(t, worktree, ".git", "gitdir: "+filepath.Join(t.TempDir(), "pruned")+"\n")
		writeGuardTestFile(t, worktree, goWorkFile, committedWorkspace)
		if _, err := gitPathExistsAtHEAD(worktree, goWorkFile); err == nil || !strings.Contains(err.Error(), "git cannot say") {
			t.Fatalf("err = %v, want the Git failure reported", err)
		}
		// Undo keeps the file and the record rather than guessing.
		if _, err := gitHeadFiles.restoreLinkArtefacts(worktree, []string{goWorkFile}); err == nil || !fileExists(filepath.Join(worktree, goWorkFile)) {
			t.Fatalf("err = %v; want the restore refused and go.work kept", err)
		}
	})
}

// An inherited GIT_DIR or index would aim the HEAD probes at another
// repository, where a tracked go.work reads as absent; they are stripped.
func TestHeadGitCommandIgnoresInheritedRepositoryOverrides(t *testing.T) {
	t.Parallel()
	kept := withoutRepositoryOverrides([]string{
		"PATH=/bin", "GIT_DIR=/elsewhere/.git", "GIT_INDEX_FILE=/elsewhere/index", "GIT_WORK_TREE=/elsewhere", "HOME=/home/x",
	})
	if strings.Join(kept, " ") != "PATH=/bin HOME=/home/x" {
		t.Fatalf("environment = %v, want only the non-repository variables", kept)
	}
	command := headGitCommand("/worktree", "status")
	for _, entry := range command.Env {
		if strings.HasPrefix(entry, "GIT_DIR=") {
			t.Fatalf("headGitCommand kept %s", entry)
		}
	}
	if strings.Join(command.Args, " ") != "git -C /worktree status" {
		t.Fatalf("args = %v", command.Args)
	}
}

// The Git diagnostic of the failed listing survives into the error.
func TestGitPathExistsAtHEADKeepsTheGitDiagnostic(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeGuardTestFile(t, worktree, ".git", "gitdir: "+filepath.Join(t.TempDir(), "pruned")+"\n")
	_, err := gitPathExistsAtHEAD(worktree, goWorkFile)
	// Git's wording differs between versions; its "fatal:" prefix does not.
	if err == nil || !strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("err = %v, want git's own diagnostic kept", err)
	}
}
