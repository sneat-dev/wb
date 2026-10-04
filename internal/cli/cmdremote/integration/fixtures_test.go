package integration

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdremote"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/spf13/cobra"
)

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

type remoteFixture struct {
	projectsRoot, origin, configPath string
	t                                *testing.T
}

type slowStatusProvider struct {
	delay                               time.Duration
	statusCalls, listCalls, claimsCalls int
}

func (provider *slowStatusProvider) Publish(context.Context, remotestate.Snapshot) (remotestate.PublishResult, error) {
	return remotestate.PublishResult{}, errors.New("unexpected Publish call")
}

func (provider *slowStatusProvider) List(context.Context) ([]remotestate.Entry, error) {
	provider.listCalls++
	return nil, errors.New("unexpected List call")
}

func (provider *slowStatusProvider) Claim(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
	return remotestate.ClaimOutcome{}, errors.New("unexpected Claim call")
}

func (provider *slowStatusProvider) Release(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
	return remotestate.ReleaseOutcome{}, errors.New("unexpected Release call")
}

func (provider *slowStatusProvider) Claims(context.Context) ([]remotestate.ClaimEntry, error) {
	provider.claimsCalls++
	return nil, errors.New("unexpected Claims call")
}

func (provider *slowStatusProvider) Status(context.Context) (remotestate.StatusSnapshot, error) {
	provider.statusCalls++
	time.Sleep(provider.delay)
	return remotestate.StatusSnapshot{}, nil
}

func newRemoteFixture(t *testing.T, machine string) remoteFixture {
	t.Helper()
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
	return remoteFixture{t: t, projectsRoot: projectsRoot, origin: origin, configPath: configPath}
}
func (f remoteFixture) deps(login string, at time.Time) remoteDeps {
	return remoteDeps{
		configPath: f.configPath,
		login:      func() (string, error) { return login, nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			path := filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName())
			if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					f.t.Fatal(err)
				}
				remoteGit(f.t, f.t.TempDir(), "clone", "-q", f.origin, path)
			}
			remoteGit(f.t, path, "config", "user.name", "t")
			remoteGit(f.t, path, "config", "user.email", "t@t")
			return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
		},
		now: func() time.Time { return at },
	}
}
func publishTwo(t *testing.T) (remoteFixture, time.Time) {
	t.Helper()
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	// Second machine: same store, a different projects root and machine name.
	g := remoteFixture{t: t, projectsRoot: filepath.Join(t.TempDir(), "projects"), origin: f.origin, configPath: filepath.Join(t.TempDir(), "wb.yaml")}
	if err := os.MkdirAll(g.projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.configPath, []byte("remote:\n  repo: team/wb-state\n  machine: vm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRemotePublish(g.deps("bob", at.Add(-48*time.Hour)), g.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	return f, at
}
func secondMachine(t *testing.T, f remoteFixture, machine string) remoteFixture {
	t.Helper()
	g := remoteFixture{t: t, projectsRoot: filepath.Join(t.TempDir(), "projects"), origin: f.origin, configPath: filepath.Join(t.TempDir(), "wb.yaml")}
	if err := os.MkdirAll(g.projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return g
}
func pushCorruptClaim(t *testing.T, f remoteFixture, task string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	remoteGit(t, t.TempDir(), "clone", "-q", f.origin, other)
	bad := filepath.Join(other, "claims", task+".yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, other, "add", "-A")
	remoteGit(t, other, "commit", "-q", "-m", "corrupt claim")
	remoteGit(t, other, "push", "-q", "origin", "main")
}
func unreachableRemoteDeps(t *testing.T, machine string) remoteDeps {
	t.Helper()
	base := t.TempDir()
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return remoteDeps{
		configPath: configPath,
		login:      func() (string, error) { return "alice", nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			return gitrepo.New(gitrepo.Options{
				ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()),
				CloneURL:  filepath.Join(base, "does-not-exist"),
			}), nil
		},
		now: func() time.Time { return time.Now().UTC() },
	}
}

type remoteDeps struct {
	configPath        string
	login             func() (string, error)
	open              func(remotestate.Config, string) (remotestate.Provider, error)
	now               func() time.Time
	progressHeartbeat time.Duration
	stderr            io.Writer
}
type remoteEnrollDeps struct {
	configPath func() string
	verify     func(context.Context, string, string, string) error
	restart    func(context.Context, string) error
}
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

const (
	exitOK              = 0
	exitFindings        = 1
	exitUsage           = 2
	defaultRemoteHubURL = "https://wb-github-app.sneat.dev"
)

func testExit(code int, message string) error { return &exitError{code, message} }
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitError
	if errors.As(err, &coded) {
		return coded.code
	}
	return -1
}
func exitCodeOfSafe(err error) int { return exitCodeOf(err) }
func testService(deps remoteDeps) *remoterun.Service {
	return remoterun.New(remoterun.Dependencies{ConfigPath: func() string { return deps.configPath }, Login: deps.login, Open: deps.open, Now: deps.now, ExitError: testExit})
}
func commands(deps remoteDeps, root string) *cobra.Command {
	service := testService(deps)
	heartbeat := deps.progressHeartbeat
	if heartbeat <= 0 {
		heartbeat = 10 * time.Second
	}
	pub := remotepublish.DefaultDependencies(deps.configPath, testExit)
	pub.Login, pub.Open, pub.Now = deps.login, deps.open, deps.now
	cmd := cmdremote.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdremote.Operations{Claim: service.Claim, Release: service.Release, Machines: service.Machines, Claims: service.Claims, Status: service.Status, Heartbeat: heartbeat, Publish: remotepublish.New(pub).Publish})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd
}
func execute(deps remoteDeps, root string, out, errOut io.Writer, args ...string) error {
	cmd := commands(deps, root)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	return cmd.Execute()
}
func runRemoteClaim(deps remoteDeps, root, task, note string, takeOver, force, jsonOut bool, stale time.Duration, out io.Writer) error {
	return execute(deps, root, out, io.Discard, "claim", task, "--note", note, "--take-over="+strconv.FormatBool(takeOver), "--force="+strconv.FormatBool(force), "--json="+strconv.FormatBool(jsonOut), "--stale", stale.String())
}
func runRemoteRelease(deps remoteDeps, root, task string, force, jsonOut bool, out io.Writer) error {
	return execute(deps, root, out, io.Discard, "release", task, "--force="+strconv.FormatBool(force), "--json="+strconv.FormatBool(jsonOut))
}
func runRemoteClaims(deps remoteDeps, root string, stale time.Duration, jsonOut bool, out io.Writer) error {
	return execute(deps, root, out, io.Discard, "claims", "--stale", stale.String(), "--json="+strconv.FormatBool(jsonOut))
}
func runRemoteMachines(deps remoteDeps, root string, stale time.Duration, jsonOut bool, out io.Writer) error {
	return execute(deps, root, out, io.Discard, "machines", "--stale", stale.String(), "--json="+strconv.FormatBool(jsonOut))
}
func runRemoteStatus(deps remoteDeps, root string, stale time.Duration, machine string, jsonOut bool, out, errOut io.Writer) error {
	return execute(deps, root, out, errOut, "status", "--stale", stale.String(), "--machine", machine, "--json="+strconv.FormatBool(jsonOut))
}
func runRemotePublish(deps remoteDeps, root, filter string, parallel int, dryRun, jsonOut bool, out io.Writer) error {
	pub := remotepublish.DefaultDependencies(deps.configPath, testExit)
	pub.Login, pub.Open, pub.Now = deps.login, deps.open, deps.now
	cmd := cmdremote.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root, Filter: filter, NonInteractive: true} }}, cmdremote.Operations{Publish: remotepublish.New(pub).Publish})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"publish", "--parallel", strconv.Itoa(parallel), "--dry-run=" + strconv.FormatBool(dryRun), "--json=" + strconv.FormatBool(jsonOut)})
	return cmd.Execute()
}
func tryAutoClaim(deps remoteDeps, root, task string, stale time.Duration, out io.Writer) worktreerun.RemoteClaimOutcome {
	return testService(deps).AutoClaim(root, task, stale, out)
}
func tryAutoRelease(deps remoteDeps, root, task string, out io.Writer) remoterun.ReleaseAdvisory {
	return testService(deps).AutoRelease(root, task, out)
}
func runRemoteEnroll(ctx context.Context, deps remoteEnrollDeps, root, machine, url, file string, stdin, restart, jsonOut bool, in io.Reader, out io.Writer) error {
	svc := remoterun.NewEnroll(remoterun.EnrollDependencies{ConfigPath: deps.configPath, Verify: deps.verify, Restart: deps.restart}, testExit)
	cmd := cmdremote.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, cmdremote.Operations{Enroll: svc.Enroll})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(ctx)
	cmd.SetIn(in)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"enroll", "--machine", machine, "--url", url, "--token-file", file, "--token-stdin=" + strconv.FormatBool(stdin), "--restart-daemon=" + strconv.FormatBool(restart), "--json=" + strconv.FormatBool(jsonOut)})
	return cmd.Execute()
}
func defaultRemoteEnrollDeps() remoteEnrollDeps {
	d := remoterun.DefaultEnrollDependencies()
	return remoteEnrollDeps{d.ConfigPath, d.Verify, d.Restart}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
func cwCovWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
func cwDepsEnrollDeps(t *testing.T, verifyErr error) (remoteEnrollDeps, string) {
	t.Helper()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "wb.yaml")
	deps := remoteEnrollDeps{
		configPath: func() string { return configPath },
		verify:     func(context.Context, string, string, string) error { return verifyErr },
		restart:    func(context.Context, string) error { return nil },
	}
	return deps, configPath
}

func defaultRemoteDeps() remoteDeps {
	d := remoterun.DefaultDependencies(testExit)
	return remoteDeps{configPath: d.ConfigPath(), login: d.Login, open: d.Open, now: d.Now, progressHeartbeat: 10 * time.Second, stderr: os.Stderr}
}
