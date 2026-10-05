package integration

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/remotepublishview"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/testenv"
)

type testDependencies struct {
	remotepublish.Dependencies
	Stderr io.Writer
}
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string                   { return e.message }
func testExitFactory(code int, message string) error { return &exitError{code: code, message: message} }

type remoteFixture struct{ projectsRoot, origin, ConfigPath string }

func testOpen(cfg remotestate.Config, root string) (remotestate.Provider, error) {
	return remotepublish.Open(cfg, root, testExitFactory)
}
func publishDeps(provider remotestate.Provider, login func() (string, error), opened *int) testDependencies {
	deps := remotepublish.DefaultDependencies("", testExitFactory)
	deps.Login = login
	deps.Now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	deps.Open = func(remotestate.Config, string) (remotestate.Provider, error) { *opened++; return provider, nil }
	return testDependencies{Dependencies: deps}
}
func runRemotePublish(deps testDependencies, root, filter string, parallel int, dry, jsonOut bool, out io.Writer) error {
	return testPublish(deps, root, filter, parallel, dry, jsonOut, out, nil)
}
func testPublish(deps testDependencies, root, filter string, parallel int, dry, jsonOut bool, out, notes io.Writer) error {
	if notes == nil {
		notes = deps.Stderr
	}
	result, err := remotepublish.New(deps.Dependencies).Publish(remotepublish.Request{ProjectsRoot: root, Filter: filter, Parallel: parallel, DryRun: dry}, remotepublish.Progress{}, notes)
	if err != nil {
		return err
	}
	return remotepublishview.Write(out, result, jsonOut)
}

// capturingProvider is the fake remote store of the periodic publisher: it
// counts publishes and keeps what it was given, and touches no network or Git.
type capturingProvider struct {
	remotestate.Provider
	mu   sync.Mutex
	seen []remotestate.Snapshot
	errs []error
}

func (p *capturingProvider) Publish(_ context.Context, snapshot remotestate.Snapshot) (remotestate.PublishResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, snapshot)
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return remotestate.PublishResult{}, err
	}
	return remotestate.PublishResult{Location: "sha"}, nil
}

// fixedSource is a fake publish source.
type refusedWith400 struct{}

func (refusedWith400) Error() string   { return "hub returned HTTP 400" }
func (refusedWith400) HTTPStatus() int { return 400 }

func remoteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	testenv.SetGitAutoMaintenanceOff(t)
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
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", origin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(base, "wbhome"))
	return remoteFixture{projectsRoot: projectsRoot, origin: origin, ConfigPath: configPath}
}
func (f remoteFixture) deps(login string, at time.Time) testDependencies {
	deps := remotepublish.DefaultDependencies(f.ConfigPath, testExitFactory)
	deps.Login = func() (string, error) { return login, nil }
	deps.Now = func() time.Time { return at }
	deps.Open = func(cfg remotestate.Config, root string) (remotestate.Provider, error) {
		return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(root, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
	}
	return testDependencies{Dependencies: deps}
}
func cockpitConfigFile(t *testing.T, content string) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return path }
}
