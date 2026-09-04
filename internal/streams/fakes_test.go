package streams

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeGit answers the Git port from in-memory tables. Every stream verb is
// exercised against it, so a refusal is proven rather than assumed reachable.
type fakeGit struct {
	defaultBranch map[string]string
	pushed        map[string]string
	pushErr       map[string]error
	remoteHeads   map[string]string
	localHeads    map[string]string
	notIn         map[string][]Commit
	notInErr      map[string]error
	deleted       []string
	deleteErr     map[string]error
	fetchErr      map[string]error
	dirty         map[string][]string
	tags          map[string][]string
	log           map[string][]string
	fetched       []string
	// calls records the order of origin-touching operations so a test can
	// prove a fetch preceded a read.
	calls []string
	// tagPatterns records the glob each Tags call used, so a test can prove
	// the tag read is scoped to the library's own module.
	tagPatterns []string
}

func newFakeGit() *fakeGit {
	return &fakeGit{
		defaultBranch: map[string]string{},
		pushed:        map[string]string{},
		pushErr:       map[string]error{},
		remoteHeads:   map[string]string{},
		localHeads:    map[string]string{},
		notIn:         map[string][]Commit{},
		notInErr:      map[string]error{},
		deleteErr:     map[string]error{},
		fetchErr:      map[string]error{},
		dirty:         map[string][]string{},
		tags:          map[string][]string{},
		log:           map[string][]string{},
	}
}

func (git *fakeGit) CurrentBranch(_ context.Context, dir string) (string, error) {
	return "stream/test", nil
}

func (git *fakeGit) DefaultBranch(_ context.Context, dir string) (string, error) {
	if branch, ok := git.defaultBranch[dir]; ok {
		return branch, nil
	}
	return "main", nil
}

func (git *fakeGit) Fetch(_ context.Context, dir string) error {
	if err := git.fetchErr[dir]; err != nil {
		return err
	}
	git.calls = append(git.calls, "fetch "+dir)
	git.fetched = append(git.fetched, dir)
	return nil
}

// fetchedBefore proves a read of origin actually happened AND was preceded by
// a fetch of the same worktree.
//
// It deliberately reports false when the read never happened at all: an
// assertion that passes because the operation under test was skipped is the
// vacuous-test failure mode, not evidence of freshness.
func (git *fakeGit) fetchedBefore(read string) bool {
	fetched := false
	for _, call := range git.calls {
		if strings.HasPrefix(call, "fetch ") && strings.TrimPrefix(call, "fetch ") == strings.Fields(read)[1] {
			fetched = true
			continue
		}
		if call == read {
			return fetched
		}
	}
	return false
}

// pushedBranches renders what was pushed, for a test that needs to prove the
// publication window was actually entered.
func (git *fakeGit) pushedBranches() []string {
	branches := make([]string, 0, len(git.pushed))
	for dir, branch := range git.pushed {
		branches = append(branches, dir+" "+branch)
	}
	sort.Strings(branches)
	return branches
}

func (git *fakeGit) PushBranch(_ context.Context, dir, branch string) (string, error) {
	if err := git.pushErr[dir]; err != nil {
		return "", err
	}
	sha := git.localHeads[dir]
	if sha == "" {
		sha = "sha-" + filepath.Base(dir)
	}
	git.pushed[dir] = branch
	git.remoteHeads[dir+" "+branch] = sha
	return sha, nil
}

func (git *fakeGit) RemoteHead(_ context.Context, dir, branch string) (string, bool, error) {
	sha, ok := git.remoteHeads[dir+" "+branch]
	return sha, ok, nil
}

func (git *fakeGit) LocalHead(_ context.Context, dir string) (string, error) {
	return git.localHeads[dir], nil
}

func (git *fakeGit) CommitsNotIn(_ context.Context, dir, branch, base string) ([]Commit, error) {
	git.calls = append(git.calls, "commits "+dir)
	key := dir + " " + branch + " " + base
	if err := git.notInErr[key]; err != nil {
		return nil, err
	}
	return git.notIn[key], nil
}

func (git *fakeGit) DeleteRemoteBranch(_ context.Context, dir, branch string) error {
	if err := git.deleteErr[dir+" "+branch]; err != nil {
		return err
	}
	git.deleted = append(git.deleted, dir+" "+branch)
	return nil
}

func (git *fakeGit) DirtyPaths(_ context.Context, dir string) ([]string, error) {
	return git.dirty[dir], nil
}

func (git *fakeGit) Tags(_ context.Context, dir, pattern string) ([]string, error) {
	git.calls = append(git.calls, "tags "+dir)
	git.tagPatterns = append(git.tagPatterns, pattern)
	return git.tags[dir], nil
}

func (git *fakeGit) LogSubjects(_ context.Context, dir, from, to string) ([]string, error) {
	return git.log[dir+" "+from+".."+to], nil
}

// fakeHub answers the GitHub port and records every mutation, so a test can
// assert what a verb did to a pull request rather than that it did not error.
type fakeHub struct {
	nextNumber   int
	created      []PullRequest
	createErr    map[string]error
	byBranch     map[string]PullRequest
	targeting    map[string][]PullRequest
	targetingErr map[string]error
	closed       []int
	closeErr     map[int]error
	retargeted   map[int]string
	byNumber     map[int]PullRequest
	mainStatus   map[string]string
	mainErr      map[string]error
}

func newFakeHub() *fakeHub {
	return &fakeHub{
		nextNumber:   100,
		createErr:    map[string]error{},
		byBranch:     map[string]PullRequest{},
		targeting:    map[string][]PullRequest{},
		targetingErr: map[string]error{},
		closeErr:     map[int]error{},
		retargeted:   map[int]string{},
		byNumber:     map[int]PullRequest{},
		mainStatus:   map[string]string{},
		mainErr:      map[string]error{},
	}
}

func (hub *fakeHub) CreateDraftPullRequest(_ context.Context, dir, base, head, title, _ string) (PullRequest, error) {
	if err := hub.createErr[dir]; err != nil {
		return PullRequest{}, err
	}
	hub.nextNumber++
	pullRequest := PullRequest{
		Number: hub.nextNumber,
		URL:    fmt.Sprintf("https://example.test/pull/%d", hub.nextNumber),
		Title:  title, Head: head, Base: base, Draft: true, State: "OPEN",
	}
	hub.created = append(hub.created, pullRequest)
	hub.byBranch[dir+" "+head] = pullRequest
	hub.byNumber[pullRequest.Number] = pullRequest
	return pullRequest, nil
}

func (hub *fakeHub) PullRequest(_ context.Context, _ string, number int) (PullRequest, bool, error) {
	pullRequest, ok := hub.byNumber[number]
	return pullRequest, ok, nil
}

func (hub *fakeHub) PullRequestForBranch(_ context.Context, dir, branch string) (PullRequest, bool, error) {
	pullRequest, ok := hub.byBranch[dir+" "+branch]
	return pullRequest, ok, nil
}

func (hub *fakeHub) OpenPullRequestsTargeting(_ context.Context, dir, base string) ([]PullRequest, error) {
	if err := hub.targetingErr[dir+" "+base]; err != nil {
		return nil, err
	}
	found := hub.targeting[dir+" "+base]
	for _, pullRequest := range found {
		if _, ok := hub.byNumber[pullRequest.Number]; !ok {
			hub.byNumber[pullRequest.Number] = pullRequest
		}
	}
	return found, nil
}

func (hub *fakeHub) ClosePullRequest(_ context.Context, _ string, number int, _ string) error {
	if err := hub.closeErr[number]; err != nil {
		return err
	}
	hub.closed = append(hub.closed, number)
	if pullRequest, ok := hub.byNumber[number]; ok {
		pullRequest.State = "CLOSED"
		hub.byNumber[number] = pullRequest
	}
	return nil
}

func (hub *fakeHub) RetargetPullRequest(_ context.Context, _ string, number int, base string) error {
	hub.retargeted[number] = base
	if pullRequest, ok := hub.byNumber[number]; ok {
		pullRequest.Base = base
		hub.byNumber[number] = pullRequest
	}
	return nil
}

func (hub *fakeHub) DefaultBranchStatus(_ context.Context, dir, branch string) (string, error) {
	if err := hub.mainErr[dir]; err != nil {
		return "", err
	}
	return hub.mainStatus[dir], nil
}

// fakeWorktrees stands in for the existing worktree creation and cleanup path.
type fakeWorktrees struct {
	root      string
	planErr   error
	createErr error
	created   []CreatedWorktree
	removed   []string
	removeErr map[string]error
}

func (worktrees *fakeWorktrees) PlannedWorktree(task, repository string) (string, error) {
	if worktrees.planErr != nil {
		return "", worktrees.planErr
	}
	owner, name, _ := strings.Cut(repository, "/")
	return filepath.Join(worktrees.root, "worktrees", task, owner, name), nil
}

func (worktrees *fakeWorktrees) Create(_ context.Context, task, branch string, repositories []string) ([]CreatedWorktree, error) {
	if worktrees.createErr != nil {
		return nil, worktrees.createErr
	}
	var results []CreatedWorktree
	for _, repository := range repositories {
		owner, name, _ := strings.Cut(repository, "/")
		path := filepath.Join(worktrees.root, "worktrees", task, owner, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, err
		}
		results = append(results, CreatedWorktree{
			Repository: repository,
			Worktree:   path,
			Canonical:  filepath.Join(worktrees.root, owner, name),
			Branch:     branch,
			Base:       "main",
		})
	}
	worktrees.created = append(worktrees.created, results...)
	return results, nil
}

func (worktrees *fakeWorktrees) Remove(_ context.Context, _, repository, worktree string) error {
	if worktrees.removeErr != nil {
		if err := worktrees.removeErr[repository]; err != nil {
			return err
		}
	}
	worktrees.removed = append(worktrees.removed, worktree)
	return os.RemoveAll(worktree)
}

// newTestEngine wires an engine over a temporary home, a temporary projects
// root, and the fakes above.
func newTestEngine(t *testing.T) (*Engine, *fakeGit, *fakeHub, *fakeWorktrees) {
	t.Helper()
	base := t.TempDir()
	store := OpenAt(filepath.Join(base, "wb-home", "streams"))
	fixed := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return fixed }
	git := newFakeGit()
	hub := newFakeHub()
	worktrees := &fakeWorktrees{root: base, removeErr: map[string]error{}}
	engine := &Engine{
		Store: store, Git: git, GitHub: hub, Worktrees: worktrees,
		ProjectsRoot: base,
		HooksCheck:   func(string) ([]string, error) { return nil, nil },
		Login:        "octocat", Machine: "workstation", Session: "wbs-1",
		Now: func() time.Time { return fixed },
	}
	return engine, git, hub, worktrees
}

// writeCanonical creates a canonical clone shaped enough for the preflight
// checks to answer, with a healthy pull_request workflow by default.
func writeCanonical(t *testing.T, root, repository string, files map[string]string) string {
	t.Helper()
	path := canonicalPath(root, repository)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		full := filepath.Join(path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const cancellingWorkflow = `name: CI
on:
  pull_request:
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
`

// writeFiles populates an existing checkout.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func findingFor(findings []PreflightFinding, repository, check string) (PreflightFinding, bool) {
	for _, finding := range findings {
		if finding.Repository == repository && finding.Check == check {
			return finding, true
		}
	}
	return PreflightFinding{}, false
}

func sortedStrings(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return sorted
}
