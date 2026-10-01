//go:build e2e

package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktrees"
)

var commitCounter atomic.Int64

// commitIn makes n commits in dir and returns the SHA of each.
func commitIn(t *testing.T, dir string, n int) []string {
	t.Helper()
	var shas []string
	for range n {
		name := fmt.Sprintf("file-%d.txt", commitCounter.Add(1))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", ".")
		gitIn(t, dir, "commit", "-m", "work")
		shas = append(shas, strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD")))
	}
	return shas
}

// indexFleet is a projects root with the three repositories of the acceptance
// scenario, each with one WB task worktree.
type indexFleet struct {
	root  string
	clone map[string]string
	tree  map[string]string
	shas  map[string][]string
}

func newIndexFleet(t *testing.T) indexFleet { return newIndexFleetIn(t, false) }

// newIndexFleetIn makes the fleet in the host layout (<root>/<host>/<owner>/
// <name>) or, with flat, in the flat layout (<root>/<owner>/<name>), where the
// placement names no host and only the origin does.
func newIndexFleetIn(t *testing.T, flat bool) indexFleet {
	t.Helper()
	f := indexFleet{root: realTempDir(t), clone: map[string]string{}, tree: map[string]string{}, shas: map[string][]string{}}
	for name, commits := range map[string]int{"fresh": 1, "stale": 4, "never": 1} {
		clone := filepath.Join(f.root, "github.com", "acme", name)
		if flat {
			clone = filepath.Join(f.root, "acme", name)
		}
		if err := os.MkdirAll(clone, 0o755); err != nil {
			t.Fatal(err)
		}
		gitIn(t, clone, "init", "--initial-branch=main")
		gitIn(t, clone, "remote", "add", "origin", "https://GitHub.com/acme/"+name+".git")
		f.shas[name] = commitIn(t, clone, commits)
		tree := filepath.Join(clone, ".worktrees", "task")
		gitIn(t, clone, "worktree", "add", "-b", "task", tree)
		manifest := worktrees.Manifest{
			Version: 1, EffortID: "task", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
			Repository: "acme/" + name, Branch: "task", CreatedAt: time.Now().Add(-time.Hour).UTC(),
		}
		if err := worktrees.WriteManifest(tree, manifest); err != nil {
			t.Fatal(err)
		}
		f.clone[name], f.tree[name] = clone, tree
	}
	return f
}

func (f indexFleet) collectors(store *receiptStore, git string) Collectors {
	index := store.collectorBinary(git)
	return LocalCollectors{ProjectsRoot: f.root, Home: realTempDir(store.t), Git: git, CodeIndex: &index}.Collectors(nil)
}

// TestE2ECodeIndexFreshnessAppearsInTheReadModel proves
// cockpit#ac:code-index-freshness-appears's read-model half on real
// repositories: one checkout whose latest receipt is at HEAD, one three
// commits behind and one with no receipt, for the clone and for its worktree.
func TestE2ECodeIndexFreshnessAppearsInTheReadModel(t *testing.T) {
	t.Parallel()
	fleet := newIndexFleet(t)
	store := newReceiptStore(t)
	receiptAt := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, checkout := range []string{fleet.clone["fresh"], fleet.tree["fresh"]} {
		store.writeFor("github.com/acme/fresh", checkout, fleet.shas["fresh"][0], "succeeded", receiptAt)
	}
	for _, checkout := range []string{fleet.clone["stale"], fleet.tree["stale"]} {
		store.writeFor("github.com/acme/stale", checkout, fleet.shas["stale"][0], "succeeded", receiptAt)
	}
	snapshotter, _ := newSnapshotter(fleet.collectors(store, ""), nil)
	refreshAndSettle(t, snapshotter)
	body, _ := snapshotter.Body()
	if strings.Contains(string(body), fleet.root) {
		t.Fatalf("the document carries a path: %s", body)
	}
	got := codeIndexByName(snapshotter.Document())
	want := map[string][]CodeIndex{
		"fresh":      {{Indexer: "index", State: CodeIndexFresh, ReceiptAt: receiptAt}},
		"fresh/task": {{Indexer: "index", State: CodeIndexFresh, ReceiptAt: receiptAt}},
		"stale":      {{Indexer: "index", State: CodeIndexStale, Behind: 3, ReceiptAt: receiptAt}},
		"stale/task": {{Indexer: "index", State: CodeIndexStale, Behind: 3, ReceiptAt: receiptAt}},
		"never":      {{Indexer: "index", State: CodeIndexNever}},
		"never/task": {{Indexer: "index", State: CodeIndexNever}},
	}
	for name, states := range want {
		if !slices.Equal(got[name], states) {
			t.Errorf("%s code index = %+v, want %+v", name, got[name], states)
		}
	}
	if !strings.Contains(string(body), `"code_index":[{"indexer":"index","state":"stale","behind":3,`) {
		t.Errorf("the marshalled document lacks the stale entry: %s", body)
	}
}

// loggingGit is a Git binary that appends each command's arguments to a log
// and then runs the real Git, so a test can count what a pass ran.
func loggingGit(t *testing.T) (binary, log string) {
	t.Helper()
	log = filepath.Join(realTempDir(t), "git.log")
	return fakeGit(t, `echo "$*" >> `+log+"\nexec git \"$@\""), log
}

func gitCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// TestE2EANewReceiptIsNoticedWithoutRescanningAnUnchangedRepository measures the
// cost: an unchanged pass runs no Git command at all, and a new receipt runs
// only the commands that compare it with HEAD, none of the branch, worktree
// or default-branch reads a changed repository costs.
func TestE2EANewReceiptIsNoticedWithoutRescanningAnUnchangedRepository(t *testing.T) {
	t.Parallel()
	fleet := newIndexFleet(t)
	store := newReceiptStore(t)
	git, log := loggingGit(t)
	receiptAt := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	stale := "github.com/acme/stale"
	store.writeFor(stale, fleet.clone["stale"], fleet.shas["stale"][0], "succeeded", receiptAt)
	// A receipt that names a tree, and one that names a commit the repository
	// does not have, are certain answers: they are held like any other.
	store.writeFor("github.com/acme/never", fleet.clone["never"], strings.TrimSpace(gitIn(t, fleet.clone["never"], "rev-parse", "HEAD^{tree}")), "succeeded", receiptAt)
	store.writeFor("github.com/acme/fresh", fleet.tree["fresh"], strings.Repeat("ab", 20), "succeeded", receiptAt)
	snapshotter, _ := newSnapshotter(fleet.collectors(store, git), nil)
	refreshAndSettle(t, snapshotter)
	full := len(gitCalls(t, log))
	if full == 0 {
		t.Fatal("the first pass ran no Git command")
	}
	refreshAndSettle(t, snapshotter)
	if unchanged := len(gitCalls(t, log)); unchanged != full {
		t.Fatalf("an unchanged pass ran %d Git commands", unchanged-full)
	}
	store.writeFor(stale, fleet.clone["stale"], fleet.shas["stale"][3], "succeeded", receiptAt.Add(time.Hour))
	refreshAndSettle(t, snapshotter)
	added := gitCalls(t, log)[full:]
	if len(added) == 0 {
		t.Fatal("a new receipt was not noticed")
	}
	for _, call := range added {
		if strings.Contains(call, "for-each-ref") || strings.Contains(call, "symbolic-ref") || strings.Contains(call, "version") {
			t.Errorf("a new receipt re-read the repository: %s", call)
		}
	}
	byName := codeIndexByName(snapshotter.Document())
	got := byName["stale"]
	if len(got) != 1 || got[0].State != CodeIndexFresh || !got[0].ReceiptAt.Equal(receiptAt.Add(time.Hour)) {
		t.Fatalf("stale clone after a new receipt = %+v", got)
	}
	if byName["never"][0].State != CodeIndexDiverged || byName["fresh/task"][0].State != CodeIndexDiverged {
		t.Fatalf("a tree or a missing commit is diverged: %+v %+v", byName["never"], byName["fresh/task"])
	}
}

func TestE2ECodeIndexDivergedWhenTheReceiptIsNotAnAncestorOfHead(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	base := commitIn(t, dir, 1)[0]
	gitIn(t, dir, "checkout", "-b", "side")
	side := commitIn(t, dir, 1)[0]
	gitIn(t, dir, "checkout", "main")
	commitIn(t, dir, 1)
	blob := strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD^{tree}"))
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, sha, state string
		complete         bool
	}{
		{"on another branch", side, CodeIndexDiverged, true},
		{"a commit the repository does not have", strings.Repeat("ab", 20), CodeIndexDiverged, true},
		{"not a commit id at all", "--upload-pack=x", CodeIndexDiverged, true},
		{"an object that is not a commit", blob, CodeIndexDiverged, true},
		{"behind", base, CodeIndexStale, true},
	} {
		store := newReceiptStore(t)
		store.write(dir, test.sha, "succeeded", at)
		pass, err := store.collectorBinary("").Begin()
		if err != nil {
			t.Fatal(err)
		}
		states, complete := pass.States(t.Context(), "github.com/acme/x", []string{dir})
		if complete != test.complete {
			t.Errorf("%s: complete = %v", test.name, complete)
		}
		if test.state == "" {
			if len(states[dir]) != 0 {
				t.Errorf("%s: a state that could not be told was guessed: %+v", test.name, states[dir])
			}
			continue
		}
		if len(states[dir]) != 1 || states[dir][0].State != test.state {
			t.Errorf("%s: states = %+v, want %s", test.name, states[dir], test.state)
		}
	}
}

func TestE2ECodeIndexLeavesOutWhatGitCannotAnswer(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	shas := commitIn(t, dir, 2)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, git, checkout, sha string
	}{
		{"a failing rev-list", `case "$*" in *rev-list*) exit 2;; esac` + "\nexec git \"$@\"", dir, shas[0]},
		{"an unparsable count", `case "$*" in *rev-list*) echo many; exit 0;; esac` + "\nexec git \"$@\"", dir, shas[0]},
		{"a zero count", `case "$*" in *rev-list*) echo 0; exit 0;; esac` + "\nexec git \"$@\"", dir, shas[0]},
		{"a head that is not an id", `case "$*" in *rev-parse*) echo nonsense; exit 0;; esac` + "\nexec git \"$@\"", dir, shas[0]},
	} {
		store := newReceiptStore(t)
		store.write(test.checkout, test.sha, "succeeded", at)
		git := ""
		if test.git != "" {
			git = fakeGit(t, test.git)
		}
		pass, err := store.collectorBinary(git).Begin()
		if err != nil {
			t.Fatal(err)
		}
		states, complete := pass.States(t.Context(), "github.com/acme/x", []string{test.checkout})
		if complete || len(states[test.checkout]) != 0 {
			t.Errorf("%s: states = %+v complete = %v, want a gap", test.name, states, complete)
		}
	}
}

func TestE2ECodeIndexOmitsWhatIsCertainlyNotThereAndStaysComplete(t *testing.T) {
	t.Parallel()
	empty := realTempDir(t)
	gitIn(t, empty, "init", "--initial-branch=main")
	gone := filepath.Join(realTempDir(t), "removed")
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	store := newReceiptStore(t)
	store.write(empty, strings.Repeat("ab", 20), "succeeded", at)
	store.write(gone, strings.Repeat("ab", 20), "succeeded", at)
	pass, err := store.collectorBinary("").Begin()
	if err != nil {
		t.Fatal(err)
	}
	states, complete := pass.States(t.Context(), "github.com/acme/x", []string{empty, gone})
	if len(states) != 0 || !complete {
		t.Fatalf("an unborn HEAD and a removed directory report nothing, for certain: %+v complete %v", states, complete)
	}
}

// TestE2ECodeIndexInAShallowCloneReportsOnlyWhatItCanProve makes a real shallow
// clone through file://, with the test's own Git. A receipt commit older than
// the clone's history is not there to compare: no state. A commit that is
// present and not an ancestor is diverged. HEAD itself is fresh.
func TestE2ECodeIndexInAShallowCloneReportsOnlyWhatItCanProve(t *testing.T) {
	t.Parallel()
	parent := realTempDir(t)
	origin := filepath.Join(parent, "origin")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "--initial-branch=main")
	history := commitIn(t, origin, 5)
	gitIn(t, origin, "checkout", "-b", "side")
	side := commitIn(t, origin, 1)[0]
	gitIn(t, origin, "checkout", "main")
	clone := filepath.Join(parent, "clone")
	gitIn(t, parent, "clone", "--depth", "1", "--no-single-branch", "file://"+origin, clone)
	if strings.TrimSpace(gitIn(t, clone, "rev-parse", "--is-shallow-repository")) != "true" {
		t.Fatal("the fixture is not a shallow clone")
	}
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, sha, state string
	}{
		{"head", history[4], CodeIndexFresh},
		{"older than the history held", history[0], ""},
		{"present and not an ancestor", side, CodeIndexDiverged},
	} {
		store := newReceiptStore(t)
		store.write(clone, test.sha, "succeeded", at)
		pass, err := store.collectorBinary("").Begin()
		if err != nil {
			t.Fatal(err)
		}
		states, complete := pass.States(t.Context(), "github.com/acme/x", []string{clone})
		if !complete {
			t.Errorf("%s: a shallow clone's answer is certain, so it is held", test.name)
		}
		got := ""
		if len(states[clone]) == 1 {
			got = states[clone][0].State
		}
		if got != test.state {
			t.Errorf("%s: state = %q, want %q", test.name, got, test.state)
		}
	}
}

func TestE2ECodeIndexLeavesOutWhatItCannotTellWhenTheShallowCheckFails(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	commitIn(t, dir, 1)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct{ name, script string }{
		{"the shallow check fails", `case "$*" in *is-shallow*) exit 2;; esac` + "\nexec git \"$@\""},
		{"git cannot be run for the commit check", `case "$*" in *cat-file*) kill -9 $$;; esac` + "\nexec git \"$@\""},
		{"the commit exists but the ancestor check fails oddly", `case "$*" in *is-ancestor*) exit 128;; esac` + "\nexec git \"$@\""},
	} {
		store := newReceiptStore(t)
		store.write(dir, strings.Repeat("ab", 20), "succeeded", at)
		if test.name == "the commit exists but the ancestor check fails oddly" {
			store = newReceiptStore(t)
			// A receipt for a commit that exists and is not HEAD needs a second commit.
			commitIn(t, dir, 1)
			store.write(dir, strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD~1")), "succeeded", at)
		}
		pass, err := store.collectorBinary(fakeGit(t, test.script)).Begin()
		if err != nil {
			t.Fatal(err)
		}
		states, complete := pass.States(t.Context(), "github.com/acme/x", []string{dir})
		if complete || len(states[dir]) != 0 {
			t.Errorf("%s: states %+v complete %v, want a gap", test.name, states, complete)
		}
	}
}

// TestE2EFlatLayoutClonesGetTheirIdentityFromTheirOrigin runs the acceptance
// scenario through clones with no host directory: fresh, stale by three, and
// never, each for the clone and its worktree. The worker names a repository by
// its origin (lower-cased, whatever the placement), and so does the read model.
// The host of such a clone, empty in its placement, is filled from the same
// identity so the code-browser link works; the origin URL itself never reaches
// the document.
func TestE2EFlatLayoutClonesGetTheirIdentityFromTheirOrigin(t *testing.T) {
	t.Parallel()
	fleet := newIndexFleetIn(t, true)
	store := newReceiptStore(t)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, name := range []string{"fresh", "stale"} {
		for _, checkout := range []string{fleet.clone[name], fleet.tree[name]} {
			store.writeFor("github.com/acme/"+name, checkout, fleet.shas[name][0], "succeeded", at)
		}
	}
	snapshotter, _ := newSnapshotter(fleet.collectors(store, ""), nil)
	refreshAndSettle(t, snapshotter)
	body, _ := snapshotter.Body()
	if strings.Contains(string(body), "https://") || strings.Contains(strings.ToLower(string(body)), ".git\"") {
		t.Fatalf("the origin URL reached the document: %s", body)
	}
	got := codeIndexByName(snapshotter.Document())
	for name, want := range map[string]CodeIndex{
		"fresh": {Indexer: "index", State: CodeIndexFresh, ReceiptAt: at}, "fresh/task": {Indexer: "index", State: CodeIndexFresh, ReceiptAt: at},
		"stale": {Indexer: "index", State: CodeIndexStale, Behind: 3, ReceiptAt: at}, "stale/task": {Indexer: "index", State: CodeIndexStale, Behind: 3, ReceiptAt: at},
		"never": {Indexer: "index", State: CodeIndexNever}, "never/task": {Indexer: "index", State: CodeIndexNever},
	} {
		if len(got[name]) != 1 || got[name][0] != want {
			t.Errorf("%s = %+v, want %+v", name, got[name], want)
		}
	}
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Host != "github.com" {
			t.Errorf("flat clone %s has host %q, want the origin's", repository.Name, repository.Host)
		}
	}
}

func TestE2EACloneWithNoOriginHasNoIndexersAndNoError(t *testing.T) {
	t.Parallel()
	fleet := newIndexFleetIn(t, true)
	gitIn(t, fleet.clone["fresh"], "remote", "remove", "origin")
	store := newReceiptStore(t)
	store.writeFor("github.com/acme/fresh", fleet.clone["fresh"], fleet.shas["fresh"][0], "succeeded", time.Now())
	snapshotter, _ := newSnapshotter(fleet.collectors(store, ""), nil)
	refreshAndSettle(t, snapshotter)
	for _, repository := range snapshotter.Document().Repositories {
		if repository.Name == "acme/fresh" && (repository.CodeIndex != nil || repository.Error != "" || repository.Host != "") {
			t.Errorf("a clone with no origin = %+v", repository)
		}
	}
	if snapshotter.Document().Error != "" {
		t.Errorf("document error %q", snapshotter.Document().Error)
	}
}

func TestE2EIdentityReadsTheOriginThroughGitAndKeepsTheURLInside(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	repo := discover.Repo{Org: "acme", Name: "x", Path: dir}
	collectors := LocalCollectors{}
	if id, err := collectors.Identity(t.Context(), repo); id != "" || err != nil {
		t.Fatalf("no origin = %q, %v", id, err)
	}
	gitIn(t, dir, "remote", "add", "origin", "git@GitHub.com:Acme/X.git")
	if id, err := collectors.Identity(t.Context(), repo); id != "github.com/acme/x" || err != nil {
		t.Fatalf("scp origin = %q, %v", id, err)
	}
	gitIn(t, dir, "remote", "set-url", "origin", "not a url at all")
	if id, err := collectors.Identity(t.Context(), repo); id != "" || err != nil {
		t.Fatalf("an origin that names no forge = %q, %v", id, err)
	}
	if _, err := (LocalCollectors{Git: fakeGit(t, "exit 5")}).Identity(t.Context(), repo); err == nil {
		t.Fatal("a Git failure is an error, not an empty identity")
	}
}

func TestE2EACommitCheckThatFailsWithGenericFatalIsNotReadAsMissing(t *testing.T) {
	t.Parallel()
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	commitIn(t, dir, 2)
	missing := strings.Repeat("ab", 20)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	fatal := `case "$*" in *is-ancestor*|*"^{commit}"*) echo fatal >&2; exit 128;; esac` + "\n"
	for _, test := range []struct {
		name, script string
		state        string
		complete     bool
	}{
		{"the commit check itself says missing", `case "$*" in *is-ancestor*) exit 128;; *"^{commit}"*) exit 1;; esac` + "\nexec git \"$@\"", CodeIndexDiverged, true},
		{"plain check says missing", fatal + `case "$*" in *cat-file*) exit 1;; esac` + "\nexec git \"$@\"", CodeIndexDiverged, true},
		{"plain check finds a non-commit object", fatal + `case "$*" in *cat-file*) exit 0;; esac` + "\nexec git \"$@\"", CodeIndexDiverged, true},
		{"plain check fails with the same fatal", fatal + `case "$*" in *cat-file*) exit 128;; esac` + "\nexec git \"$@\"", "", false},
		{"plain check cannot run", fatal + `case "$*" in *cat-file*) kill -9 $$;; esac` + "\nexec git \"$@\"", "", false},
	} {
		store := newReceiptStore(t)
		store.write(dir, missing, "succeeded", at)
		pass, err := store.collectorBinary(fakeGit(t, test.script)).Begin()
		if err != nil {
			t.Fatal(err)
		}
		states, complete := pass.States(t.Context(), "github.com/acme/x", []string{dir})
		got := ""
		if len(states[dir]) == 1 {
			got = states[dir][0].State
		}
		if got != test.state || complete != test.complete {
			t.Errorf("%s: state %q complete %v, want %q %v", test.name, got, complete, test.state, test.complete)
		}
	}
}

// TestE2EACorruptObjectStoreIsAGapNotADivergedState breaks a real repository's
// object store after HEAD is known: Git then fails with its generic fatal for a
// commit it cannot look up, which must not be held as diverged.
func TestE2EACorruptObjectStoreIsAGapNotADivergedState(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	dir := realTempDir(t)
	gitIn(t, dir, "init", "--initial-branch=main")
	commitIn(t, dir, 1)
	store := newReceiptStore(t)
	store.write(dir, strings.Repeat("cd", 20), "succeeded", time.Now())
	objects := filepath.Join(dir, ".git", "objects")
	if err := os.Chmod(objects, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(objects, 0o755) })
	pass, err := store.collectorBinary("").Begin()
	if err != nil {
		t.Fatal(err)
	}
	states, complete := pass.States(t.Context(), "github.com/acme/x", []string{dir})
	for _, state := range states[dir] {
		if state.State == CodeIndexDiverged {
			t.Fatalf("an unreadable object store was held as diverged: %+v", states)
		}
	}
	if len(states[dir]) == 0 && complete {
		t.Fatal("a gap that may pass must be retried, not held")
	}
}

// collectorBinary is collector over the real runner and the Git binary named by
// git (empty means "git").
func (s *receiptStore) collectorBinary(git string) LocalCodeIndex {
	index := s.collector(nil)
	index.Git = git
	return index
}
