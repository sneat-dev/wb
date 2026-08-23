package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
)

func remoteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setGitIdentity gives Publish's commits (made through the process env, not
// remoteGit's explicit env) a valid author/committer so tests work under a
// HOME with no git config.
func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

// remoteFixture builds a projects root holding one dirty fleet repo, a bare
// state-repo origin, and a wb.yaml pointing at it.
type remoteFixture struct {
	projectsRoot, origin, configPath string
}

func newRemoteFixture(t *testing.T, machine string) remoteFixture {
	t.Helper()
	setGitIdentity(t)
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	repo := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, repo, "init", "-q", "-b", "main")
	remoteGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", origin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", filepath.Join(base, "wbhome"))
	return remoteFixture{projectsRoot: projectsRoot, origin: origin, configPath: configPath}
}

func (f remoteFixture) deps(login string, at time.Time) remoteDeps {
	return remoteDeps{
		configPath: f.configPath,
		login:      func() (string, error) { return login, nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
		},
		now: func() time.Time { return at },
	}
}

func TestRemotePublishUnconfiguredIsUsageError(t *testing.T) {
	deps := defaultRemoteDeps()
	deps.configPath = filepath.Join(t.TempDir(), "none.yaml")
	var out bytes.Buffer
	err := runRemotePublish(deps, t.TempDir(), "", 2, false, false, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("err = %v, want usage error with snippet", err)
	}
}

func TestRemotePublishWritesSnapshotToStore(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, true, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Key                 string `json:"key"`
		RepositoriesScanned int    `json:"repositories_scanned"`
		Attention           int    `json:"attention"`
		Worktrees           int    `json:"worktrees"`
		Location            string `json:"location"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if report.Key != "alice/laptop" || report.RepositoriesScanned != 1 || report.Attention != 1 || len(report.Location) != 40 {
		t.Fatalf("report = %+v", report)
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "repository: acme/widgets") || !strings.Contains(stored, "- dirty.txt") {
		t.Fatalf("stored snapshot = %s", stored)
	}
	if !strings.Contains(stored, "status: attention") {
		t.Fatalf("stored snapshot should report attention (untracked file, no upstream), not error: %s", stored)
	}
}

func TestRemotePublishDryRunTouchesNothing(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", time.Now()), f.projectsRoot, "", 2, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme/widgets") {
		t.Fatalf("dry-run should print the snapshot: %s", out.String())
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "machines/") {
		t.Fatalf("dry-run published: %s", files)
	}
	if _, err := os.Stat(filepath.Join(f.projectsRoot, "team", "wb-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run must not clone the state repo")
	}
}

var _ = context.Background
