package hooks

import (
	"bytes"
	"path/filepath"
	"testing"
)

// These pin wb#993: `git commit -a`, `git commit <paths>` and
// `git commit --only` stage the commit in a temporary index Git hands the
// hook through GIT_INDEX_FILE. hooks.Run keeps clearing GIT_INDEX_FILE from
// every block, and passes the index on as WB_GIT_INDEX_FILE, which only the
// built-in staged-file listings read.

func TestHookIndexFileResolvesGitsRelativeIndexAgainstTheRepositoryRoot(t *testing.T) {
	t.Parallel()
	if got, want := resolveHookIndexFile("/repo", filepath.Join(".git", "index.lock")), filepath.Join("/repo", ".git", "index.lock"); got != want {
		t.Fatalf("relative index = %q, want %q", got, want)
	}
	absolute := filepath.Join(t.TempDir(), "next-index-1.lock")
	if got := resolveHookIndexFile("/repo", absolute); got != absolute {
		t.Fatalf("absolute index = %q, want %q", got, absolute)
	}
	if got := resolveHookIndexFile("/repo", ""); got != "" {
		t.Fatalf("no index = %q, want empty", got)
	}
}

//nolint:paralleltest // Git hands the hook its index in the process environment (GIT_INDEX_FILE), which hooks.Run reads; t.Setenv forbids t.Parallel.
func TestRunHandsTheHooksIndexToBlocksAsWBGitIndexFileOnly(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(configDir, "templates"))
	mustWrite(t, filepath.Join(configDir, "templates", "pre-commit.sh"),
		"#!/bin/sh\nprintf 'GIT_INDEX_FILE=%s\\nWB_GIT_INDEX_FILE=%s\\n' \"${GIT_INDEX_FILE-unset}\" \"${WB_GIT_INDEX_FILE-unset}\"\n")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-commit:\n    template: templates/pre-commit.sh\nmetrics:\n  enabled: false\n")

	run := func() string {
		t.Helper()
		var stdout bytes.Buffer
		result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &stdout, Stderr: &bytes.Buffer{}})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("run result = %#v, error = %v", result, err)
		}
		return stdout.String()
	}

	t.Setenv("GIT_INDEX_FILE", filepath.Join(".git", "index.lock"))
	if got, want := run(), "GIT_INDEX_FILE=unset\nWB_GIT_INDEX_FILE="+filepath.Join(repo, ".git", "index.lock")+"\n"; got != want {
		t.Fatalf("block environment\n got %q\nwant %q", got, want)
	}

	// A WB_GIT_INDEX_FILE inherited from an outer hook never describes this
	// commit: without GIT_INDEX_FILE the block sees it empty.
	t.Setenv("GIT_INDEX_FILE", "")
	t.Setenv("WB_GIT_INDEX_FILE", "/outer/repo/.git/index.lock")
	if got, want := run(), "GIT_INDEX_FILE=unset\nWB_GIT_INDEX_FILE=\n"; got != want {
		t.Fatalf("block environment with an inherited index\n got %q\nwant %q", got, want)
	}
}
