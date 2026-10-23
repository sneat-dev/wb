package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// indexerConfig is a hooks configuration with one executor, named index, bound
// to every repository.
const indexerConfig = "hooks:\n  version: 1\n  executors:\n    index:\n      run: /bin/true\n      cwd: repository\n      mode: coalesced\n      failure: warn\n" +

	"  bindings:\n    - on: [checkout-updated]\n      match:\n        repositories:\n          include: [github.com/*/*]\n      execute: [index]\n"

// receiptStore is a temporary lifecycle-hook configuration, receipt stream and
// queue, written the way the worker writes them.
type receiptStore struct {
	t                        *testing.T
	config, receipts, queued string
	count                    int
}

func newReceiptStore(t *testing.T) *receiptStore {
	t.Helper()
	dir := realTempDir(t)
	store := &receiptStore{t: t, config: filepath.Join(dir, "wb.yaml"), receipts: filepath.Join(dir, "receipts.jsonl"), queued: filepath.Join(dir, "state")}
	if err := os.WriteFile(store.config, []byte(indexerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return store
}

func (s *receiptStore) collector(run runner.Runner) LocalCodeIndex {
	return LocalCodeIndex{Runner: run, Reader: lifecyclehooks.NewFreshnessReader(lifecyclehooks.Dispatcher{ConfigPath: s.config, ReceiptPath: s.receipts, StateDir: s.queued})}
}

// write appends a receipt of status for executor index on checkout, for the
// repository github.com/acme/x.
func (s *receiptStore) write(checkout, sha, status string, finished time.Time) {
	s.t.Helper()
	s.writeFor("github.com/acme/x", checkout, sha, status, finished)
}

// writeFor is write for a repository named by its host/owner/name identity.
func (s *receiptStore) writeFor(repository, checkout, sha, status string, finished time.Time) {
	s.t.Helper()
	s.count++
	receipt := lifecyclehooks.Receipt{
		SchemaVersion: 2, ID: fmt.Sprintf("r%d", s.count), Event: lifecyclehooks.EventCheckoutUpdated, Repository: repository,
		Checkout: checkout, NewSHA: sha, Cause: "pull", Executor: "index", FinishedAt: finished, Status: status,
	}
	line, err := json.Marshal(receipt)
	if err != nil {
		s.t.Fatal(err)
	}
	file, err := os.OpenFile(s.receipts, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
}

// queue puts a pending job for checkout in the queue, in the file format the
// dispatcher writes.
func (s *receiptStore) queue(checkout string) {
	s.t.Helper()
	directory := filepath.Join(s.queued, "pending")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		s.t.Fatal(err)
	}
	job := map[string]any{
		"schema_version": 1, "key": "index\x00" + checkout, "executor": "index", "coalesced_count": 1,
		"event": map[string]string{"name": lifecyclehooks.EventCheckoutUpdated, "checkout": checkout, "new_sha": "a", "repository": "github.com/acme/x"},
	}
	data, err := json.Marshal(job)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "job.json"), data, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// codeIndexOf maps each repository name to its repository entry's code index,
// and "<name>/task" to its worktree entry's.
func codeIndexByName(document Document) map[string][]CodeIndex {
	byName := map[string][]CodeIndex{}
	names := map[string]string{}
	for _, repository := range document.Repositories {
		name := strings.TrimPrefix(repository.Name, "acme/")
		names[repository.ID] = name
		byName[name] = repository.CodeIndex
	}
	for _, worktree := range document.Worktrees {
		byName[names[worktree.Repository]+"/"+worktree.Task] = worktree.CodeIndex
	}
	return byName
}

func TestCodeIndexStatesPendingFailedAndNeverNeedNoGit(t *testing.T) {
	t.Parallel()
	store := newReceiptStore(t)
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	root := realTempDir(t)
	pending, failed, never := filepath.Join(root, "pending"), filepath.Join(root, "failed"), filepath.Join(root, "never")
	for _, dir := range []string{pending, failed, never} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store.write(pending, "aaa", "succeeded", at)
	store.queue(pending)
	store.write(failed, "aaa", "succeeded", at)
	store.write(failed, "bbb", "failed", at.Add(time.Hour))
	git := newFakeGit(t).reply(gitReply{Exit: 9})
	pass, err := store.collector(git).Begin()
	if err != nil {
		t.Fatal(err)
	}
	states, complete := pass.States(t.Context(), "github.com/acme/x", []string{pending, failed, never})
	want := map[string][]CodeIndex{
		pending: {{Indexer: "index", State: CodeIndexPending, ReceiptAt: at, receiptKey: "aaa\x00succeeded"}},
		failed:  {{Indexer: "index", State: CodeIndexFailed, ReceiptAt: at.Add(time.Hour), receiptKey: "bbb\x00failed"}},
		never:   {{Indexer: "index", State: CodeIndexNever}},
	}
	if !complete {
		t.Error("states that need no Git are complete")
	}
	for checkout, expected := range want {
		if !slices.Equal(states[checkout], expected) {
			t.Errorf("%s = %+v, want %+v", checkout, states[checkout], expected)
		}
	}
	if states, complete := pass.States(t.Context(), "gitlab.com/acme/x", []string{pending}); len(states) != 0 || !complete {
		t.Errorf("a repository with no bound indexer has no states: %+v", states)
	}
	if len(git.running()) != 0 {
		t.Errorf("states that need no Git ran %v", git.running())
	}
}

func TestCodeIndexBeginFailsWhenTheReceiptsCannotBeRead(t *testing.T) {
	t.Parallel()
	store := newReceiptStore(t)
	if err := os.WriteFile(store.config, []byte("hooks: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.collector(nil).Begin(); err == nil {
		t.Fatal("an invalid hooks configuration must not read as nothing indexed")
	}
}

func TestLocalCollectorsCarryTheCodeIndexOnlyWhenOneIsSet(t *testing.T) {
	t.Parallel()
	if (LocalCollectors{}).Collectors(nil).CodeIndex != nil {
		t.Error("a collector with no code index must leave the interface nil")
	}
	if (LocalCollectors{CodeIndex: &LocalCodeIndex{}}).Collectors(nil).CodeIndex == nil {
		t.Error("a configured code index was dropped")
	}
}

// TestCodeIndexCarriesNothingOfAReceiptButItsStateCountAndTime fills every
// field of a receipt, the paths included, with a sentinel, and requires the
// document to hold none of them.
func TestCodeIndexCarriesNothingOfAReceiptButItsStateCountAndTime(t *testing.T) {
	t.Parallel()
	fleet := newFakeIndexFleet(t, false)
	store := newReceiptStore(t)
	receipt := filled[lifecyclehooks.Receipt]()
	receipt.SchemaVersion, receipt.ID, receipt.Executor, receipt.Status = 2, "r1", "index", "succeeded"
	receipt.Repository, receipt.Checkout, receipt.NewSHA = "github.com/acme/stale", fleet.clone["stale"], fleet.shas["stale"][0]
	receipt.FinishedAt = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	line, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.receipts, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotter, _ := newSnapshotter(fleet.collectors(store), nil)
	refreshAndSettle(t, snapshotter)
	body, _ := snapshotter.Body()
	if strings.Contains(string(body), sentinel) || strings.Contains(string(body), strings.TrimPrefix(fleet.root, "/")) {
		t.Fatalf("the document carries a receipt field or a path: %s", body)
	}
	if got := codeIndexByName(snapshotter.Document())["stale"]; len(got) != 1 || got[0].State != CodeIndexStale || got[0].Behind != 3 {
		t.Fatalf("the state did not arrive: %+v", got)
	}
}

// fakeCodeIndex is a code-index source the test controls. Its pass counts what
// the snapshotter asks.
type fakeCodeIndex struct {
	mu         sync.Mutex
	begins     int
	keys       int
	asked      int
	key        string
	states     func(identity string, checkouts []string) map[string][]CodeIndex
	incomplete bool
	beginErr   error
	panicIn    string
}

func (f *fakeCodeIndex) Begin() (CodeIndexPass, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins++
	if f.panicIn == "begin" {
		panic("begin")
	}
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return f, nil
}

func (f *fakeCodeIndex) Key(string, []string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys++
	return f.key
}

func (f *fakeCodeIndex) States(_ context.Context, identity string, checkouts []string) (map[string][]CodeIndex, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	if f.panicIn == "states" {
		panic("states")
	}
	return f.states(identity, checkouts), !f.incomplete
}

func (f *fakeCodeIndex) setKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key = key
}

func (f *fakeCodeIndex) counts() (begins, asked int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.begins, f.asked
}

func freshAt(path string) func(string, []string) map[string][]CodeIndex {
	return func(_ string, checkouts []string) map[string][]CodeIndex {
		states := map[string][]CodeIndex{}
		for _, checkout := range checkouts {
			states[checkout] = []CodeIndex{{Indexer: "index", State: CodeIndexFresh}}
		}
		states[path] = []CodeIndex{{Indexer: "index", State: CodeIndexStale, Behind: 3}}
		return states
	}
}

func codeIndexSources(t *testing.T, index *fakeCodeIndex) (*Snapshotter, *fakeSources, *constFingerprint) {
	t.Helper()
	sources := oneRepoSources(t.TempDir())
	collectors := sources.collectors()
	collectors.CodeIndex = index
	fingerprint := newConstFingerprint("one")
	snapshotter, _ := newSnapshotter(collectors, func(options *Options) { options.Fingerprint = fingerprint.get })
	return snapshotter, sources, fingerprint
}

func TestSnapshotterAsksForCodeIndexStatesOnlyWhenTheyCouldHaveMoved(t *testing.T) {
	t.Parallel()
	index := &fakeCodeIndex{key: "k1"}
	snapshotter, sources, fingerprint := codeIndexSources(t, index)
	index.states = freshAt(sources.repos[0].Path)
	refreshAndSettle(t, snapshotter)
	if _, asked := index.counts(); asked != 1 {
		t.Fatalf("first pass asked %d times, want 1", asked)
	}
	document := snapshotter.Document()
	var repository Repository
	for _, candidate := range document.Repositories {
		if candidate.Route == RouteLocal {
			repository = candidate
		}
	}
	if len(repository.CodeIndex) != 1 || repository.CodeIndex[0].State != CodeIndexStale || repository.CodeIndex[0].Behind != 3 {
		t.Fatalf("repository code index = %+v", repository.CodeIndex)
	}
	var tasks int
	for _, worktree := range document.Worktrees {
		if worktree.Route == RouteLocal {
			tasks++
			if len(worktree.CodeIndex) != 1 || worktree.CodeIndex[0].State != CodeIndexFresh {
				t.Errorf("worktree %s code index = %+v", worktree.Task, worktree.CodeIndex)
			}
		}
		if worktree.Route == RouteCached && worktree.CodeIndex != nil {
			t.Errorf("a cached worktree carries a code index: %+v", worktree)
		}
	}
	if tasks != 2 {
		t.Fatalf("local worktrees = %d", tasks)
	}
	for _, cached := range document.Repositories {
		if cached.Route == RouteCached && cached.CodeIndex != nil {
			t.Errorf("a cached repository carries a code index: %+v", cached)
		}
	}

	// Nothing moved: the held state is reused.
	refreshAndSettle(t, snapshotter)
	if _, asked := index.counts(); asked != 1 {
		t.Fatalf("an unchanged pass asked %d times", asked)
	}
	worktreeReads := sources.worktreeCalls.Load()

	// A new receipt (a new key) re-asks without re-reading the repository.
	index.setKey("k2")
	refreshAndSettle(t, snapshotter)
	if _, asked := index.counts(); asked != 2 {
		t.Fatalf("a new receipt key asked %d times, want 2", asked)
	}
	if sources.worktreeCalls.Load() != worktreeReads {
		t.Fatal("a new receipt re-read the repository's worktrees")
	}

	// Git state moved: the states are read again for the new HEAD.
	fingerprint.value.Store("two")
	refreshAndSettle(t, snapshotter)
	if _, asked := index.counts(); asked != 3 {
		t.Fatalf("a moved fingerprint asked %d times, want 3", asked)
	}
}

func TestSnapshotterAsksAgainWhileTheCodeIndexStateHasAGap(t *testing.T) {
	t.Parallel()
	index := &fakeCodeIndex{key: "k", incomplete: true, states: func(string, []string) map[string][]CodeIndex { return map[string][]CodeIndex{} }}
	snapshotter, _, _ := codeIndexSources(t, index)
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	if _, asked := index.counts(); asked != 2 {
		t.Fatalf("a gap was kept: asked %d times, want 2", asked)
	}
}

func TestCodeIndexIsOffWhenItsSourceFailsOrPanicsOrGitIsTooOld(t *testing.T) {
	t.Parallel()
	empty := func(string, []string) map[string][]CodeIndex { return map[string][]CodeIndex{} }
	for _, test := range []struct {
		name  string
		index *fakeCodeIndex
		old   bool
	}{
		{"begin fails", &fakeCodeIndex{beginErr: errors.New("receipts unreadable"), states: empty}, false},
		{"begin panics", &fakeCodeIndex{panicIn: "begin", states: empty}, false},
		{"states panics", &fakeCodeIndex{panicIn: "states", states: freshAt("")}, false},
		{"git is too old", &fakeCodeIndex{states: freshAt("")}, true},
	} {
		sources := oneRepoSources(t.TempDir())
		collectors := sources.collectors()
		collectors.CodeIndex = test.index
		collectors.Git = &fakeGate{usable: !test.old}
		var logs []string
		snapshotter, _ := newSnapshotter(collectors, func(options *Options) {
			options.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
		})
		refreshAndSettle(t, snapshotter)
		refreshAndSettle(t, snapshotter)
		for _, repository := range snapshotter.Document().Repositories {
			if repository.CodeIndex != nil {
				t.Errorf("%s: repository carries a code index %+v", test.name, repository.CodeIndex)
			}
		}
		if begins, _ := test.index.counts(); test.old && begins != 0 {
			t.Errorf("%s: the receipts were read although Git cannot compare them", test.name)
		}
		failures := 0
		for _, line := range logs {
			if strings.Contains(line, "code-index freshness is off") {
				failures++
			}
		}
		if (test.index.beginErr != nil || test.index.panicIn == "begin") && failures != 1 {
			t.Errorf("%s: the failure was logged %d times, want once", test.name, failures)
		}
	}
}

func TestRefreshRepositoryOutsideAPassReadsTheReceiptsAnew(t *testing.T) {
	t.Parallel()
	index := &fakeCodeIndex{key: "k"}
	snapshotter, sources, _ := codeIndexSources(t, index)
	index.states = freshAt(sources.repos[0].Path)
	refreshAndSettle(t, snapshotter)
	begins, _ := index.counts()
	for id := range snapshotter.repoIDsForTest() {
		if err := snapshotter.RefreshRepository(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := index.counts(); after != begins+1 {
		t.Fatalf("a refresh outside a pass began %d code-index reads, want 1", after-begins)
	}
}

func (s *Snapshotter) repoIDsForTest() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := map[string]bool{}
	for id := range s.repos {
		ids[id] = true
	}
	return ids
}

// fakeIndexFleet is a projects root with the three repositories of the
// acceptance scenario, each with one WB task worktree, over a scripted Git:
// fresh has one commit, stale four, never one. The directories are bare
// stand-ins (an empty .git, the worktree's administrative files and its
// manifest); the commits exist only in the scripted Git.
type fakeIndexFleet struct {
	root  string
	clone map[string]string
	tree  map[string]string
	shas  map[string][]string
	git   *fakeGitRunner
}

func newFakeIndexFleet(t *testing.T, flat bool) fakeIndexFleet {
	t.Helper()
	f := fakeIndexFleet{root: realTempDir(t), clone: map[string]string{}, tree: map[string]string{}, shas: map[string][]string{}, git: newFakeGit(t)}
	f.git.add(".", newFakeRepo(0, ""))
	for name, commits := range map[string]int{"fresh": 1, "stale": 4, "never": 1} {
		clone := filepath.Join(f.root, "github.com", "acme", name)
		if flat {
			clone = filepath.Join(f.root, "acme", name)
		}
		tree := filepath.Join(clone, ".worktrees", "task")
		admin := filepath.Join(clone, ".git", "worktrees", "task")
		for _, dir := range []string{admin, tree, filepath.Join(clone, ".git", "refs")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for file, content := range map[string]string{"gitdir": filepath.Join(tree, ".git") + "\n", "HEAD": "ref: refs/heads/task\n"} {
			if err := os.WriteFile(filepath.Join(admin, file), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		manifest := worktrees.Manifest{
			Version: 1, EffortID: "task", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
			Repository: "acme/" + name, Branch: "task", CreatedAt: time.Now().Add(-time.Hour).UTC(),
		}
		writeManifestFile(t, tree, manifest)
		repo := newFakeRepo(commits, "https://GitHub.com/acme/"+name+".git")
		f.shas[name] = repo.history
		f.git.add(clone, repo)
		f.git.add(tree, repo)
		f.clone[name], f.tree[name] = clone, tree
	}
	return f
}

func (f fakeIndexFleet) collectors(store *receiptStore) Collectors {
	index := store.collector(f.git)
	return LocalCollectors{ProjectsRoot: f.root, Home: realTempDir(store.t), Runner: f.git, CodeIndex: &index}.Collectors(nil)
}

// TestCodeIndexFreshnessAppearsInTheReadModel proves
// cockpit#ac:code-index-freshness-appears's read-model half over a scripted
// Git, in the host layout and the flat one (where only the origin names the
// host): one checkout whose latest receipt is at HEAD, one three commits behind
// and one with no receipt, for the clone and for its worktree. The origin URL
// never reaches the document.
func TestCodeIndexFreshnessAppearsInTheReadModel(t *testing.T) {
	t.Parallel()
	for _, flat := range []bool{false, true} {
		fleet := newFakeIndexFleet(t, flat)
		store := newReceiptStore(t)
		receiptAt := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
		for _, name := range []string{"fresh", "stale"} {
			for _, checkout := range []string{fleet.clone[name], fleet.tree[name]} {
				store.writeFor("github.com/acme/"+name, checkout, fleet.shas[name][0], "succeeded", receiptAt)
			}
		}
		snapshotter, _ := newSnapshotter(fleet.collectors(store), nil)
		refreshAndSettle(t, snapshotter)
		body, _ := snapshotter.Body()
		// remote_url_web is the one https:// address the document may carry; it is
		// built from the host and name, never from the origin URL.
		if text := webURLField.ReplaceAllString(string(body), ""); strings.Contains(text, fleet.root) || strings.Contains(text, "https://") {
			t.Fatalf("flat=%v: the document carries a path or the origin: %s", flat, body)
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
				t.Errorf("flat=%v: %s code index = %+v, want %+v", flat, name, got[name], states)
			}
		}
		for _, repository := range snapshotter.Document().Repositories {
			if repository.Host != "github.com" {
				t.Errorf("flat=%v: clone %s has host %q, want the origin's", flat, repository.Name, repository.Host)
			}
		}
		if !strings.Contains(string(body), `"code_index":[{"indexer":"index","state":"stale","behind":3,`) {
			t.Errorf("flat=%v: the marshalled document lacks the stale entry: %s", flat, body)
		}
	}
}

// TestANewReceiptIsNoticedWithoutRescanningAnUnchangedRepository measures the
// cost: an unchanged pass runs no Git command at all, and a new receipt runs
// only the commands that compare it with HEAD, none of the branch, worktree
// or default-branch reads a changed repository costs.
func TestANewReceiptIsNoticedWithoutRescanningAnUnchangedRepository(t *testing.T) {
	t.Parallel()
	fleet := newFakeIndexFleet(t, false)
	store := newReceiptStore(t)
	receiptAt := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	stale := "github.com/acme/stale"
	store.writeFor(stale, fleet.clone["stale"], fleet.shas["stale"][0], "succeeded", receiptAt)
	// A receipt that names a tree, and one that names a commit the repository
	// does not have, are certain answers: they are held like any other.
	tree := objectID(900)
	fleet.git.repos[fleet.clone["never"]].trees[tree] = true
	store.writeFor("github.com/acme/never", fleet.clone["never"], tree, "succeeded", receiptAt)
	store.writeFor("github.com/acme/fresh", fleet.tree["fresh"], strings.Repeat("ab", 20), "succeeded", receiptAt)
	snapshotter, _ := newSnapshotter(fleet.collectors(store), nil)
	refreshAndSettle(t, snapshotter)
	full := len(fleet.git.running())
	if full == 0 {
		t.Fatal("the first pass ran no Git command")
	}
	refreshAndSettle(t, snapshotter)
	if unchanged := len(fleet.git.running()); unchanged != full {
		t.Fatalf("an unchanged pass ran %d Git commands", unchanged-full)
	}
	store.writeFor(stale, fleet.clone["stale"], fleet.shas["stale"][3], "succeeded", receiptAt.Add(time.Hour))
	refreshAndSettle(t, snapshotter)
	added := fleet.git.running()[full:]
	if len(added) == 0 {
		t.Fatal("a new receipt was not noticed")
	}
	for _, call := range added {
		if call.Args[0] == "for-each-ref" || call.Args[0] == "symbolic-ref" || call.Args[0] == "version" {
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

// statesOf asks a pass built from store, over git, about checkout and returns
// the one state it holds (empty when it holds none) and whether the pass is
// complete.
func statesOf(t *testing.T, store *receiptStore, git runner.Runner, checkout string) (string, bool) {
	t.Helper()
	pass, err := store.collector(git).Begin()
	if err != nil {
		t.Fatal(err)
	}
	states, complete := pass.States(t.Context(), "github.com/acme/x", []string{checkout})
	if len(states[checkout]) > 1 {
		t.Fatalf("more than one state: %+v", states[checkout])
	}
	if len(states[checkout]) == 0 {
		return "", complete
	}
	return states[checkout][0].State, complete
}

// indexedCheckout is a directory that exists, registered as a repository with
// commits commits in git.
func indexedCheckout(t *testing.T, git *fakeGitRunner, commits int) (string, *fakeRepo) {
	t.Helper()
	dir := realTempDir(t)
	return dir, git.add(dir, newFakeRepo(commits, ""))
}

func TestCodeIndexDivergedWhenTheReceiptIsNotAnAncestorOfHead(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir, repo := indexedCheckout(t, git, 3)
	side, tree := objectID(500), objectID(501)
	repo.elsewhere[side], repo.trees[tree] = true, true
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct{ name, sha, state string }{
		{"on another branch", side, CodeIndexDiverged},
		{"a commit the repository does not have", strings.Repeat("ab", 20), CodeIndexDiverged},
		{"not a commit id at all", "--upload-pack=x", CodeIndexDiverged},
		{"an object that is not a commit", tree, CodeIndexDiverged},
		{"behind", repo.history[0], CodeIndexStale},
	} {
		store := newReceiptStore(t)
		store.write(dir, test.sha, "succeeded", at)
		if state, complete := statesOf(t, store, git, dir); state != test.state || !complete {
			t.Errorf("%s: state %q complete %v, want %q", test.name, state, complete, test.state)
		}
	}
	for _, call := range git.running() {
		if strings.Contains(call.String(), "--upload-pack") {
			t.Errorf("a receipt that is no object id reached Git: %s", call)
		}
	}
}

func TestCodeIndexLeavesOutWhatGitCannotAnswer(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		match func(gitCall) bool
		reply gitReply
	}{
		{"a failing rev-list", commandIs("rev-list"), gitReply{Exit: 2}},
		{"an unparsable count", commandIs("rev-list"), gitReply{Out: "many\n"}},
		{"a zero count", commandIs("rev-list"), gitReply{Out: "0\n"}},
		{"a head that is not an id", commandIs("rev-parse"), gitReply{Out: "nonsense\n"}},
		{"a head that cannot be read", commandIs("rev-parse"), gitReply{Exit: 128}},
	} {
		git := newFakeGit(t)
		dir, repo := indexedCheckout(t, git, 2)
		git.when(test.match, test.reply)
		store := newReceiptStore(t)
		store.write(dir, repo.history[0], "succeeded", at)
		if state, complete := statesOf(t, store, git, dir); complete || state != "" {
			t.Errorf("%s: state %q complete %v, want a gap", test.name, state, complete)
		}
	}
}

func TestCodeIndexOmitsWhatIsCertainlyNotThereAndStaysComplete(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	unborn := realTempDir(t)
	git.add(unborn, newFakeRepo(0, ""))
	gone := filepath.Join(realTempDir(t), "removed")
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	store := newReceiptStore(t)
	store.write(unborn, strings.Repeat("ab", 20), "succeeded", at)
	store.write(gone, strings.Repeat("ab", 20), "succeeded", at)
	pass, err := store.collector(git).Begin()
	if err != nil {
		t.Fatal(err)
	}
	states, complete := pass.States(t.Context(), "github.com/acme/x", []string{unborn, gone})
	if len(states) != 0 || !complete {
		t.Fatalf("an unborn HEAD and a removed directory report nothing, for certain: %+v complete %v", states, complete)
	}
}

// TestCodeIndexInAShallowCloneReportsOnlyWhatItCanProve scripts a shallow
// clone: a receipt commit older than the history held is not there to compare
// (no state), a commit that is present and not an ancestor is diverged, and
// HEAD itself is fresh.
func TestCodeIndexInAShallowCloneReportsOnlyWhatItCanProve(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir := realTempDir(t)
	repo := git.add(dir, newFakeRepo(1, ""))
	repo.shallow = true
	side := objectID(500)
	repo.elsewhere[side] = true
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct{ name, sha, state string }{
		{"head", repo.head, CodeIndexFresh},
		{"older than the history held", objectID(77), ""},
		{"present and not an ancestor", side, CodeIndexDiverged},
	} {
		store := newReceiptStore(t)
		store.write(dir, test.sha, "succeeded", at)
		if state, complete := statesOf(t, store, git, dir); state != test.state || !complete {
			t.Errorf("%s: state %q complete %v, want %q held", test.name, state, complete, test.state)
		}
	}
}

func TestCodeIndexLeavesOutWhatItCannotTellWhenTheShallowCheckFails(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		receipt func(repo *fakeRepo) string
		match   func(gitCall) bool
		reply   gitReply
	}{
		{"the shallow check fails", func(*fakeRepo) string { return objectID(77) }, commandMentions("--is-shallow-repository"), gitReply{Exit: 2}},
		{"git cannot be run for the commit check", func(*fakeRepo) string { return objectID(77) }, commandMentions("^{commit}"), gitReply{Err: errBoom}},
		{"the commit exists but the ancestor check fails oddly", func(repo *fakeRepo) string { return repo.history[0] }, commandIs("merge-base"), gitReply{Exit: 128}},
	} {
		git := newFakeGit(t)
		dir, repo := indexedCheckout(t, git, 2)
		git.when(test.match, test.reply)
		store := newReceiptStore(t)
		store.write(dir, test.receipt(repo), "succeeded", at)
		if state, complete := statesOf(t, store, git, dir); complete || state != "" {
			t.Errorf("%s: state %q complete %v, want a gap", test.name, state, complete)
		}
	}
}

func TestACloneWithNoOriginHasNoIndexersAndNoError(t *testing.T) {
	t.Parallel()
	fleet := newFakeIndexFleet(t, true)
	fleet.git.repos[fleet.clone["fresh"]].origin = ""
	store := newReceiptStore(t)
	store.writeFor("github.com/acme/fresh", fleet.clone["fresh"], fleet.shas["fresh"][0], "succeeded", time.Now())
	snapshotter, _ := newSnapshotter(fleet.collectors(store), nil)
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

func TestIdentityReadsTheOriginThroughGitAndKeepsTheURLInside(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir, repo := indexedCheckout(t, git, 1)
	repo.origin = ""
	collectors := LocalCollectors{Runner: git}
	found := discover.Repo{Org: "acme", Name: "x", Path: dir}
	if id, err := collectors.Identity(t.Context(), found); id != "" || err != nil {
		t.Fatalf("no origin = %q, %v", id, err)
	}
	repo.origin = "git@GitHub.com:Acme/X.git"
	if id, err := collectors.Identity(t.Context(), found); id != "github.com/acme/x" || err != nil {
		t.Fatalf("scp origin = %q, %v", id, err)
	}
	repo.origin = "not a url at all"
	if id, err := collectors.Identity(t.Context(), found); id != "" || err != nil {
		t.Fatalf("an origin that names no forge = %q, %v", id, err)
	}
	git.reply(gitReply{Exit: 5})
	if _, err := collectors.Identity(t.Context(), found); err == nil {
		t.Fatal("a Git failure is an error, not an empty identity")
	}
}

func TestACommitCheckThatFailsWithGenericFatalIsNotReadAsMissing(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	missing := strings.Repeat("ab", 20)
	for _, test := range []struct {
		name  string
		setup func(*fakeGitRunner)
		state string
		whole bool
	}{
		{"the commit check itself says missing", func(g *fakeGitRunner) {
			g.when(commandMentions("^{commit}"), gitReply{Exit: 1})
		}, CodeIndexDiverged, true},
		{"plain check says missing", func(*fakeGitRunner) {}, CodeIndexDiverged, true},
		{"plain check finds a non-commit object", func(g *fakeGitRunner) {
			g.when(commandIs("cat-file", "-e", missing), gitReply{})
		}, CodeIndexDiverged, true},
		{"plain check fails with the same fatal", func(g *fakeGitRunner) {
			g.when(commandIs("cat-file", "-e", missing), gitReply{Exit: 128})
		}, "", false},
		{"plain check cannot run", func(g *fakeGitRunner) {
			g.when(commandIs("cat-file", "-e", missing), gitReply{Err: errBoom})
		}, "", false},
		{"the object store is unreadable", func(g *fakeGitRunner) {
			g.when(commandMentions("cat-file"), gitReply{Exit: 128})
		}, "", false},
	} {
		git := newFakeGit(t)
		dir, _ := indexedCheckout(t, git, 2)
		test.setup(git)
		store := newReceiptStore(t)
		store.write(dir, missing, "succeeded", at)
		if state, complete := statesOf(t, store, git, dir); state != test.state || complete != test.whole {
			t.Errorf("%s: state %q complete %v, want %q %v", test.name, state, complete, test.state, test.whole)
		}
	}
}

// webURLField matches a repository's remote_url_web member.
var webURLField = regexp.MustCompile(`"remote_url_web":"https://[A-Za-z0-9./_-]+"`)
