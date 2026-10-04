package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestRemotePublishByHandRetriesWithoutOptionalFieldsAndSaysSo(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{errs: []error{refusedWith400{}}}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	var out, progress bytes.Buffer
	if err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, &progress); err != nil {
		t.Fatal(err)
	}
	if len(provider.seen) != 2 || !provider.seen[0].HasOptional() || provider.seen[1].HasOptional() || !strings.Contains(progress.String(), "published without them") {
		t.Fatalf("attempts %d, progress %q", len(provider.seen), progress.String())
	}
}

func TestRemotePublishByHandSaysOnceThatHardwareIsNowIncluded(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	run := func() string {
		var out, progress bytes.Buffer
		if err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, &progress); err != nil {
			t.Fatal(err)
		}
		return progress.String()
	}
	first := run()
	if strings.Count(first, "\n") != 1 || !strings.Contains(first, "os, arch, cpu_count and boot_time") || !strings.Contains(first, "never sent by hand") {
		t.Fatalf("first publish note = %q", first)
	}
	if second := run(); strings.Contains(second, "boot_time") {
		t.Fatalf("the note was repeated: %q", second)
	}
	// A dry run prints the snapshot, says nothing, and does not use up the note.
	fresh := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	fresh.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	var out, progress bytes.Buffer
	if err := testPublish(fresh, t.TempDir(), "", 1, true, true, &out, &progress); err != nil || strings.Contains(progress.String(), "boot_time") {
		t.Fatalf("dry run: %v %q", err, progress.String())
	}
	// Without a config path or an unwritable marker nothing breaks.

}

func TestAFailedPublishDoesNotUseUpTheHardwareNote(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{errs: []error{errors.New("store down")}}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	run := func() (string, error) {
		var out, progress bytes.Buffer
		err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, &progress)
		return progress.String(), err
	}
	first, err := run()
	if err == nil || !strings.Contains(first, "boot_time") {
		t.Fatalf("the failing publish: %v, note %q", err, first)
	}
	second, err := run() // succeeds: the note is shown again, and only now recorded
	if err != nil || !strings.Contains(second, "boot_time") {
		t.Fatalf("the retry after a failure did not repeat the note: %v %q", err, second)
	}
	third, err := run()
	if err != nil || strings.Contains(third, "boot_time") {
		t.Fatalf("the note was shown after a success: %v %q", err, third)
	}
}

func TestRemotePublishSaysThatHardwareIsIncludedWithoutAProgressWriter(t *testing.T) {
	t.Parallel()
	provider := &capturingProvider{}
	opened := 0
	deps := publishDeps(provider, func() (string, error) { return "alice", nil }, &opened)
	deps.ConfigPath = cockpitConfigFile(t, "remote:\n  repo: acme/wb-state\n  machine: mac\n")()
	// Neither writer: the publish is made and says nothing.
	var out bytes.Buffer
	if err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, nil); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	deps.Stderr = &stderr
	if err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, nil); err != nil {
		t.Fatal(err)
	}
	if said := stderr.String(); strings.Count(said, "\n") != 1 || !strings.Contains(said, "os, arch, cpu_count and boot_time") {
		t.Fatalf("with no progress writer the first publish said %q on stderr, want the note once", said)
	}
	stderr.Reset()
	if err := testPublish(deps, t.TempDir(), "", 1, false, true, &out, nil); err != nil || stderr.Len() != 0 {
		t.Fatalf("the note was repeated: %v %q", err, stderr.String())
	}
	// A publish that had to leave the optional fields out says so there too.
	refusing := publishDeps(&capturingProvider{errs: []error{refusedWith400{}}}, func() (string, error) { return "alice", nil }, &opened)
	refusing.ConfigPath, refusing.Stderr = deps.ConfigPath, &stderr
	if err := testPublish(refusing, t.TempDir(), "", 1, false, true, &out, nil); err != nil || !strings.Contains(stderr.String(), "published without them") {
		t.Fatalf("a publish without the optional fields: %v, stderr %q", err, stderr.String())
	}

}
func TestOpenRemoteSelectsHTTPSHubProvider(t *testing.T) {
	provider, err := testOpen(remotestate.Config{
		Provider: "hub", URL: "https://hub.example", TokenFile: "/private/token", Machine: "laptop",
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*hub.Provider); !ok {
		t.Fatalf("provider = %T, want *hub.Provider", provider)
	}
}

func TestRemotePublishUnconfiguredIsUsageError(t *testing.T) {
	deps := testDependencies{Dependencies: remotepublish.DefaultDependencies("", testExitFactory)}
	deps.ConfigPath = filepath.Join(t.TempDir(), "none.yaml")
	var out bytes.Buffer
	err := runRemotePublish(deps, t.TempDir(), "", 2, false, false, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != 2 || !strings.Contains(err.Error(), "remote:\n  provider: git") {
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

func TestRemotePublishIncludesOrphanedWorktrees(t *testing.T) {
	setGitIdentity(t)
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real origin remote with a resolvable origin/main ref: worktree
	// inspection checks whether the worktree's HEAD is an ancestor of
	// origin/<base>, which fails outright without one.
	upstream := filepath.Join(base, "widgets-origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", upstream)
	testenv.ConfigureGitAutoMaintenanceOff(t, upstream)
	remoteGit(t, base, "clone", "-q", upstream, canonical)
	remoteGit(t, canonical, "commit", "-q", "--allow-empty", "-m", "seed")
	remoteGit(t, canonical, "push", "-q", "-u", "origin", "main")

	t.Setenv("WB_PROJECTS_ROOT", projectsRoot)
	orphanWorktree := filepath.Join(projectsRoot, ".worktrees", "orphan-task", "acme", "widgets")
	remoteGit(t, canonical, "worktree", "add", "-q", "-b", "agent/orphan-task", orphanWorktree, "main")

	stateOrigin := filepath.Join(base, "origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", stateOrigin)
	testenv.ConfigureGitAutoMaintenanceOff(t, stateOrigin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", stateOrigin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: laptop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := remoteFixture{projectsRoot: projectsRoot, origin: stateOrigin, ConfigPath: configPath}

	var out bytes.Buffer
	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, true, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Worktrees int `json:"worktrees"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if report.Worktrees != 1 {
		t.Fatalf("report.Worktrees = %d, want 1 (an orphaned worktree with no live owner must still be published)", report.Worktrees)
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "task: orphan-task") {
		t.Fatalf("stored snapshot is missing the orphaned worktree: %s", stored)
	}
	if !strings.Contains(stored, "owner_state: unknown") {
		t.Fatalf("stored snapshot does not record the worktree's owner_state (want unknown for an ownerless worktree): %s", stored)
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

func TestRemotePublishFilterNoMatchStillErrors(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out bytes.Buffer
	err := runRemotePublish(f.deps("alice", time.Now().UTC()), f.projectsRoot, "definitely-no-match", 2, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "no local repositories match") {
		t.Fatalf("err = %v, want unmatched-filter error (a typo must not publish a false clean snapshot)", err)
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "machines/") {
		t.Fatalf("unmatched filter must not publish: %s", files)
	}
}
func TestOpenRemoteRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := testOpen(remotestate.Config{Provider: "ftp"}, t.TempDir())
	if err == nil {
		t.Fatal("testOpen(ftp) returned nil error, want a refusal")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("testOpen(ftp) error type = %T, want *exitError", err)
	}
	if exitErr.code != 2 {
		t.Fatalf("testOpen(ftp) exit code = %d, want %d", exitErr.code, 2)
	}
	const want = "remote.provider ftp is not supported"
	if exitErr.message != want {
		t.Fatalf("testOpen(ftp) message = %q, want %q", exitErr.message, want)
	}
}
