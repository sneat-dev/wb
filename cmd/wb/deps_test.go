package main

import "testing"

func TestGitHubSlugSupportsSSHAndHTTPS(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"git@github.com:strongo/cicd.git":       "strongo/cicd",
		"https://github.com/sneat-dev/wb.git":   "sneat-dev/wb",
		"ssh://git@github.com/dal-go/dalgo.git": "dal-go/dalgo",
	}
	for remote, want := range tests {
		if got := githubSlug(remote); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestDepsCommandExposesCumulativeLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newDepsSetCmd()
	for _, name := range []string{"commit", "push", "pr", "merge", "parallel", "resume", "retry", "timeout"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("deps set is missing --%s", name)
		}
	}
}
