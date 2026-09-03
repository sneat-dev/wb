package streamsync

import (
	"context"
	"fmt"
	"strings"
)

// fakeGit records the ORDER of operations, because the ordering is the
// mechanism under test: rebase before bump is what makes sync idempotent.
type fakeGit struct {
	calls           []string
	heads           map[string]string
	rebaseErr       map[string]error
	conflicts       map[string][]string
	commits         map[string]string
	nothingToDo     map[string]bool
	clean           bool
	cherryErr       map[string]error
	ahead           int
	aheadErr        error
	createdBranches []string
	deletedBranches []string
	checkedOut      []string
	resets          []string
}

func newFakeGit() *fakeGit {
	return &fakeGit{
		heads:       map[string]string{},
		rebaseErr:   map[string]error{},
		conflicts:   map[string][]string{},
		commits:     map[string]string{},
		nothingToDo: map[string]bool{},
		cherryErr:   map[string]error{},
		clean:       true,
	}
}

func (git *fakeGit) Fetch(_ context.Context, dir string) error {
	git.calls = append(git.calls, "fetch")
	return nil
}

func (git *fakeGit) CurrentBranch(context.Context, string) (string, error) { return "stream/x", nil }

func (git *fakeGit) Rebase(_ context.Context, _, branch, upstream string) ([]string, error) {
	git.calls = append(git.calls, "rebase "+branch+" onto "+upstream)
	if err := git.rebaseErr[branch]; err != nil {
		return nil, err
	}
	return git.conflicts[branch], nil
}

func (git *fakeGit) AbortRebase(context.Context, string) error {
	git.calls = append(git.calls, "abort-rebase")
	return nil
}

func (git *fakeGit) Head(_ context.Context, _, revision string) (string, error) {
	if sha, ok := git.heads[revision]; ok {
		return sha, nil
	}
	return "sha-" + revision, nil
}

func (git *fakeGit) CommitsAhead(context.Context, string, string, string) (int, error) {
	return git.ahead, git.aheadErr
}

func (git *fakeGit) CommitAll(_ context.Context, _, message string) (string, bool, error) {
	git.calls = append(git.calls, "commit "+message)
	if git.nothingToDo[message] {
		return "", false, nil
	}
	sha, ok := git.commits[message]
	if !ok {
		sha = fmt.Sprintf("commit-%d", len(git.calls))
	}
	return sha, true, nil
}

func (git *fakeGit) CreateBranch(_ context.Context, _, branch, revision string) error {
	git.calls = append(git.calls, "create-branch "+branch+" at "+revision)
	git.createdBranches = append(git.createdBranches, branch)
	return nil
}

func (git *fakeGit) Checkout(_ context.Context, _, branch string) error {
	git.calls = append(git.calls, "checkout "+branch)
	git.checkedOut = append(git.checkedOut, branch)
	return nil
}

func (git *fakeGit) ResetHard(_ context.Context, _, revision string) error {
	git.calls = append(git.calls, "reset "+revision)
	git.resets = append(git.resets, revision)
	return nil
}

func (git *fakeGit) CherryPick(_ context.Context, _, sha string) error {
	git.calls = append(git.calls, "cherry-pick "+sha)
	return git.cherryErr[sha]
}

func (git *fakeGit) DeleteBranch(_ context.Context, _, branch string) error {
	git.calls = append(git.calls, "delete-branch "+branch)
	git.deletedBranches = append(git.deletedBranches, branch)
	return nil
}

func (git *fakeGit) IsClean(context.Context, string) (bool, error) { return git.clean, nil }

// pushed reports whether anything in this fake could have reached the remote.
// It is deliberately exhaustive: the whole point of the local model is that
// sync never pushes, so the test asserts on the absence of ANY push verb.
func (git *fakeGit) pushed() bool {
	for _, call := range git.calls {
		if strings.HasPrefix(call, "push") {
			return true
		}
	}
	return false
}

// fakeBumper answers required versions from a table and records what it applied.
type fakeBumper struct {
	required map[string]string
	missing  map[string]bool
	applied  []string
	applyErr map[string]error
	// afterApply lets a test model a bump that changes nothing on disk.
	changesNothing map[string]bool
}

func newFakeBumper() *fakeBumper {
	return &fakeBumper{
		required: map[string]string{}, missing: map[string]bool{},
		applyErr: map[string]error{}, changesNothing: map[string]bool{},
	}
}

func (bumper *fakeBumper) Required(_ context.Context, _ string, library Library) (string, bool, error) {
	if bumper.missing[library.Name] {
		return "", false, nil
	}
	return bumper.required[library.Name], true, nil
}

func (bumper *fakeBumper) Apply(_ context.Context, _ string, library Library) error {
	if err := bumper.applyErr[library.Name]; err != nil {
		return err
	}
	bumper.applied = append(bumper.applied, library.Name)
	// A real bump moves the required version; modelling that is what lets a
	// second sync in the same test prove idempotence.
	if !bumper.changesNothing[library.Name] {
		bumper.required[library.Name] = library.Target
	}
	return nil
}

// fakeVerifier returns a scripted sequence of runs.
type fakeVerifier struct {
	runs  []VerificationRun
	calls int
}

func (verifier *fakeVerifier) Verify(context.Context, string) (VerificationRun, error) {
	index := verifier.calls
	verifier.calls++
	if index < len(verifier.runs) {
		return verifier.runs[index], nil
	}
	return VerificationRun{Passed: true}, nil
}

type fakeCI struct{ present map[string]bool }

func (ci fakeCI) Present(string) (map[string]bool, error) { return ci.present, nil }

type fakeEvents struct{ events []Event }

func (events *fakeEvents) Append(event Event) error {
	events.events = append(events.events, event)
	return nil
}

func (events *fakeEvents) withPhase(phase string) []Event {
	var matched []Event
	for _, event := range events.events {
		if event.Phase == phase {
			matched = append(matched, event)
		}
	}
	return matched
}

func newTestEngine() (*Engine, *fakeGit, *fakeBumper, *fakeVerifier, *fakeEvents) {
	git, bumper, verifier, events := newFakeGit(), newFakeBumper(), &fakeVerifier{}, &fakeEvents{}
	engine := &Engine{Git: git, Bumper: bumper, Verifier: verifier, Events: events}
	return engine, git, bumper, verifier, events
}

func baseOptions() Options {
	return Options{
		Stream: "checkout", Worktree: "/wt/app", Repository: "acme/app",
		Branch: "stream/checkout", Base: "main",
	}
}
