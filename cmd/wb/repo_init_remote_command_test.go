package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type repoInitRemoteFake struct {
	calls   []string
	failAt  string
	skip    bool
	branch  string
	commits bool
}

func (fake *repoInitRemoteFake) ops() repoInitRemoteOps {
	failed := func(step string) error {
		if fake.failAt == step {
			return errors.New(step + " failed")
		}
		return nil
	}
	return repoInitRemoteOps{
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
			fake := &repoInitRemoteFake{failAt: test.failAt, skip: test.skip, branch: test.branch, commits: test.commits}
			err := runRepoInitRemoteWithOps(path, fake.ops())
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

func TestRepoInitRemoteCommandDefaultsToCurrentDirectoryAndAcceptsExplicitPath(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		path string
	}{
		{name: "current directory", path: "."},
		{name: "explicit path", args: []string{"/fixture/repo"}, path: "/fixture/repo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &repoInitRemoteFake{branch: "main", commits: true}
			_, _, err := cwCovExec(t, t.TempDir(), func() *cobra.Command {
				return newRepoInitRemoteCmdWithOps(fake.ops())
			}, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.calls) != 5 || fake.calls[0] != "skip:"+test.path || fake.calls[4] != "push:"+test.path+":main" {
				t.Fatalf("command passed operations %v, want path %q", fake.calls, test.path)
			}
		})
	}
}

func TestRepoInitRemoteIsRegisteredUnderRepoCommand(t *testing.T) {
	root := t.TempDir()
	repo := newRepoCmd(testInvocation(t, root))
	for _, path := range [][]string{{"init-remote"}, {"status"}, {"ignore"}, {"transfer", "cleanup"}} {
		command, remaining, err := repo.Find(path)
		if err != nil || len(remaining) != 0 || command == nil || command.Name() != path[len(path)-1] {
			t.Errorf("repo command %v = (%v, %v, %v)", path, command, remaining, err)
		}
	}
}
