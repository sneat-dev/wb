package gitops

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

type repoInitRemoteFake struct {
	calls   []string
	failAt  string
	skip    bool
	branch  string
	commits bool
}

func (fake *repoInitRemoteFake) ops() initRemoteOps {
	failed := func(step string) error {
		if fake.failAt == step {
			return errors.New(step + " failed")
		}
		return nil
	}
	return initRemoteOps{
		skipSync: func(path string) (bool, error) {
			fake.calls = append(fake.calls, "skip:"+path)
			return fake.skip, failed("skip")
		},
		originURL: func(path string) (string, error) {
			fake.calls = append(fake.calls, "origin:"+path)
			return "file:///origin.git", failed("origin")
		},
		currentBranch: func(path string) (string, error) {
			fake.calls = append(fake.calls, "branch:"+path)
			return fake.branch, failed("branch")
		},
		hasCommits: func(path string) (bool, error) {
			fake.calls = append(fake.calls, "commits:"+path)
			return fake.commits, failed("commits")
		},
		commitEmpty: func(path, message string) error {
			fake.calls = append(fake.calls, "commit:"+path+":"+message)
			return failed("commit")
		},
		pushSetUpstream: func(path, branch string) error {
			fake.calls = append(fake.calls, "push:"+path+":"+branch)
			return failed("push")
		},
	}
}

func TestRepoInitRemoteValidatesBeforeMutationAndStopsAtEachFailure(t *testing.T) {
	t.Parallel()
	const path = "fixture"
	steps := []string{"skip:fixture", "origin:fixture", "branch:fixture", "commits:fixture", "commit:fixture:Initial commit", "push:fixture:main"}
	for _, test := range []struct {
		name, failAt, wantError string
		skip, commits           bool
		branch                  string
		wantCalls               int
	}{
		{name: "unborn branch publishes initial commit", branch: "main", wantCalls: 6},
		{name: "existing history does not gain empty commit", branch: "main", commits: true, wantCalls: 5},
		{name: "skip check fails before mutation", failAt: "skip", branch: "main", wantError: "skip failed", wantCalls: 1},
		{name: "marked repository is refused", skip: true, branch: "main", wantError: "ignore --unset", wantCalls: 1},
		{name: "missing origin is refused", failAt: "origin", branch: "main", wantError: "has no origin remote", wantCalls: 2},
		{name: "branch inspection failure is returned", failAt: "branch", branch: "main", wantError: "branch failed", wantCalls: 3},
		{name: "detached head is refused", wantError: "detached HEAD", wantCalls: 3},
		{name: "history inspection failure is returned", failAt: "commits", branch: "main", wantError: "commits failed", wantCalls: 4},
		{name: "initial commit failure stops push", failAt: "commit", branch: "main", wantError: "commit failed", wantCalls: 5},
		{name: "push failure is returned", failAt: "push", branch: "main", wantError: "push failed", wantCalls: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &repoInitRemoteFake{failAt: test.failAt, skip: test.skip, branch: test.branch, commits: test.commits}
			err := initRemoteWith(path, fake.ops(), nil)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("init remote: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("init remote error = %v, want %q", err, test.wantError)
			}
			want := append([]string(nil), steps[:test.wantCalls]...)
			if test.commits {
				want = []string{steps[0], steps[1], steps[2], steps[3], steps[5]}
			}
			if !reflect.DeepEqual(fake.calls, want) {
				t.Fatalf("operations = %v, want %v", fake.calls, want)
			}
		})
	}
}

func TestInitRemoteNotificationsFollowCompletedMutations(t *testing.T) {
	t.Parallel()
	fake := &repoInitRemoteFake{branch: "main"}
	var notices []InitRemoteEvent
	err := initRemoteWith("fixture", fake.ops(), func(event InitRemoteEvent) {
		notices = append(notices, event)
		if event.Kind == CreatedInitialCommit && len(fake.calls) != 5 {
			t.Fatalf("commit notice calls=%v", fake.calls)
		}
		if event.Kind == Published && len(fake.calls) != 6 {
			t.Fatalf("published notice calls=%v", fake.calls)
		}
	})
	want := []InitRemoteEvent{{Kind: CreatedInitialCommit, Path: "fixture", Branch: "main"}, {Kind: Published, Path: "fixture", Branch: "main"}}
	if err != nil || !reflect.DeepEqual(notices, want) {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
}
func TestInitRemoteActualGitPublishesAndSetsUpstream(t *testing.T) {
	t.Parallel()
	origin, repo := t.TempDir(), t.TempDir()
	git(t, origin, "init", "--bare", "-q")
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "config", "user.name", "wb-test")
	git(t, repo, "config", "user.email", "wb-test@example.com")
	git(t, repo, "remote", "add", "origin", origin)
	var notices []InitRemoteEvent
	if err := InitRemote(repo, func(event InitRemoteEvent) { notices = append(notices, event) }); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command("git", "-C", repo, "rev-parse", "--abbrev-ref", "main@{upstream}").CombinedOutput()
	if err != nil {
		t.Fatalf("upstream: %v: %s", err, raw)
	}
	if len(notices) != 2 || notices[0].Kind != CreatedInitialCommit || notices[1].Kind != Published || strings.TrimSpace(string(raw)) != "origin/main" {
		t.Fatal(notices)
	}
}
