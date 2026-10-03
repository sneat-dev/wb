package main

import (
	"errors"

	"github.com/sneat-dev/wb/internal/streams"
)

// exitCodeOfSafe reports the WB exit code implied by an error, treating a
// plain error as findings.
func exitCodeOfSafe(err error) int {
	if err == nil {
		return exitOK
	}
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return exitFindings
}

// TestCwDepsStreamCommandRefusalsInProcess drives the stream verbs' guard
// paths, which fail before any worktree, branch, or GitHub call is made.

func cwDepsStreamMember(repository string, pullRequest int, pullRequestError string) streams.Member {
	return streams.Member{Repository: repository, Role: streams.RoleConsumer, Worktree: "/tmp/" + repository,
		Branch: "stream/cw", PullRequest: pullRequest, PullRequestError: pullRequestError}
}
