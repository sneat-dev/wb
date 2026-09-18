package worktrees

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// storeModeFixture is one hermetic projects root with hosted canonical clones
// and a writable, initially empty user configuration directory.
type storeModeFixture struct {
	base         string
	projectsRoot string
	configHome   string
	userConfig   string
	canonicals   map[string]string
}

// newStoreModeFixture builds a projects root whose canonical clones live at
// <root>/github.com/acme/<name>, one per name, each with its own local bare
// origin. Nothing is written to the user worktrees configuration, so the store
// mode is whatever WB defaults to.
func newStoreModeFixture(t *testing.T, names ...string) *storeModeFixture {
	t.Helper()
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	configHome := filepath.Join(base, "config")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	fixture := &storeModeFixture{
		base:         base,
		projectsRoot: projectsRoot,
		configHome:   configHome,
		userConfig:   filepath.Join(configHome, "wb", "worktrees.yaml"),
		canonicals:   map[string]string{},
	}
	for _, name := range names {
		dest := filepath.Join(projectsRoot, "github.com", "acme", name)
		fixture.canonicals[name] = seedCloneAt(t, base, dest)
	}
	return fixture
}

// selectStoreMode writes the machine-local worktrees configuration that
// selects one store mode.
func (fixture *storeModeFixture) selectStoreMode(t *testing.T, mode string) {
	t.Helper()
	mustWriteBranchConfig(t, fixture.userConfig, "version: 1\nworktrees:\n  store: "+mode+"\n")
}

// newSharedHostedOrigin creates one bare origin carrying one commit, so every
// clone of it shares an identical base SHA — the only way a portable claim ID
// can be compared across two independent creates.
func newSharedHostedOrigin(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gitTest(t, base, "init", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(base, "seed")
	gitTest(t, base, "clone", remote, seed)
	configureGitUser(t, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", "README.md")
	gitTest(t, seed, "commit", "-m", "initial")
	gitTest(t, seed, "push", "-u", "origin", "main")
	return remote
}

// cloneHostedOriginInto clones a shared origin to
// <projectsRoot>/<org>/<name> — the legacy two-level on-disk placement — and
// points its configured origin at a literal forge URL. A
// url.<local>.insteadOf alias keeps every fetch local, so the clone really does
// name a forge without the test needing the network.
func cloneHostedOriginInto(t *testing.T, remote, projectsRoot, org, name string) string {
	t.Helper()
	dest := filepath.Join(projectsRoot, org, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(remote), "clone", remote, dest)
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	forgeURL := "https://github.com/" + org + "/" + name + ".git"
	gitTest(t, resolved, "config", "url."+remote+".insteadOf", forgeURL)
	gitTest(t, resolved, "remote", "set-url", "origin", forgeURL)
	return resolved
}

// worktreeCheckoutsUnder returns every linked checkout at or below root, sorted.
// A linked worktree carries a `.git` file rather than a directory, so this is
// the same boundary `wb worktree guard` recognizes.
func worktreeCheckoutsUnder(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() || path == root {
			return nil
		}
		info, statErr := os.Stat(filepath.Join(path, ".git"))
		if statErr == nil && !info.IsDir() {
			found = append(found, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	return found
}

// TestCentralStoreIsTheDefaultForAMultiRepositoryTask encodes
// projects-root-layout#ac:central-store-is-default. With no worktrees.root (and
// no store mode) configuration at all, one task spanning two repositories must
// publish both checkouts together below <root>/.worktrees/<task>/<host>/<org>/<repo>,
// and the task directory must be a search scope that sees only that task.
func TestCentralStoreIsTheDefaultForAMultiRepositoryTask(t *testing.T) {
	fixture := newStoreModeFixture(t, "app", "lib")
	ctx := context.Background()
	taskRoot := filepath.Join(fixture.projectsRoot, ".worktrees", "two-repo-task")

	created, err := Create(ctx, []string{"acme/app", "acme/lib"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "two-repo-task", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v, want both repositories", created)
	}
	want := map[string]string{
		"app": filepath.Join(taskRoot, "github.com", "acme", "app"),
		"lib": filepath.Join(taskRoot, "github.com", "acme", "lib"),
	}
	for _, result := range created {
		name := filepath.Base(result.WorktreeDir)
		if result.WorktreeDir != want[name] {
			t.Fatalf("checkout for %s = %q, want the host-level central path %q", name, result.WorktreeDir, want[name])
		}
		if result.CanonicalDir != fixture.canonicals[name] {
			t.Fatalf("canonical for %s = %q, want %q", name, result.CanonicalDir, fixture.canonicals[name])
		}
		if _, err := os.Stat(result.WorktreeDir); err != nil {
			t.Fatalf("checkout %s was not created: %v", result.WorktreeDir, err)
		}
	}
	if checkouts := worktreeCheckoutsUnder(t, taskRoot); len(checkouts) != 2 {
		t.Fatalf("task directory holds %v, want exactly this task's two checkouts", checkouts)
	}

	// A second task must not bleed into the first task's directory: the task
	// level is what makes a task-scoped search exact.
	if _, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "other-task", WorkLog: WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	checkouts := worktreeCheckoutsUnder(t, taskRoot)
	if len(checkouts) != 2 {
		t.Fatalf("task-scoped search found %v, want only this task's two checkouts", checkouts)
	}
	for _, checkout := range checkouts {
		if !strings.HasPrefix(checkout, taskRoot+string(filepath.Separator)) {
			t.Fatalf("task-scoped search escaped into %s", checkout)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.projectsRoot, ".worktrees", "other-task", "github.com", "acme", "app")); err != nil {
		t.Fatalf("sibling task was not placed under the same store root: %v", err)
	}
}

// TestRepositoryLocalStoreModeKeepsCentralIdentity encodes
// projects-root-layout#ac:repo-local-mode-selected. One task is created for real
// in central mode and again in repository-local mode, from two clones of the
// same origin commit, and the branch, immutable claim ID and Work Log identity
// must be byte-identical between the two real results: only the checkout
// location may differ.
func TestRepositoryLocalStoreModeKeepsCentralIdentity(t *testing.T) {
	remote := newSharedHostedOrigin(t)
	type identity struct {
		branch      string
		claimID     string
		effortID    string
		runID       string
		workLogTail string
	}
	type result struct {
		created      CreateResult
		canonical    string
		projectsRoot string
		identity     identity
	}
	create := func(store string) result {
		t.Helper()
		base := t.TempDir()
		projectsRoot := filepath.Join(base, "projects")
		configHome := filepath.Join(base, "config")
		t.Setenv(wbhome.EnvOverride, projectsRoot)
		t.Setenv(wbhome.EnvMigrationCompat, "")
		t.Setenv("HOME", filepath.Join(base, "home"))
		t.Setenv("XDG_CONFIG_HOME", configHome)
		if store != "" {
			mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: "+store+"\n")
		}
		canonical := cloneHostedOriginInto(t, remote, projectsRoot, "acme", "app")
		created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
			ProjectsRoot: projectsRoot, Operation: "mode-identity",
			// An explicit Run ID keeps the Work Log run identity fixed, so only
			// the store mode can vary between the two runs.
			WorkLog: WorkLogOptions{RunID: "mode-identity-run", Model: "unknown"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(created) != 1 {
			t.Fatalf("created = %#v", created)
		}
		projection, err := readWorkLogProjection(created[0].WorktreeDir)
		if err != nil {
			t.Fatal(err)
		}
		tail, err := filepath.Rel(filepath.Join(projectsRoot, ".wb"), created[0].WorkLogPath)
		if err != nil {
			t.Fatal(err)
		}
		return result{
			created: created[0], canonical: canonical, projectsRoot: projectsRoot,
			identity: identity{
				branch: created[0].Branch, claimID: projection.ClaimID,
				effortID: projection.EffortID, runID: projection.RunID, workLogTail: tail,
			},
		}
	}

	central := create("")
	local := create("repository-local")

	// Central mode carries the literal host named by the clone's origin, even
	// though the clone itself is still at <root>/{org}/{repo}.
	wantCentral := filepath.Join(central.projectsRoot, ".worktrees", "mode-identity", "github.com", "acme", "app")
	if central.created.WorktreeDir != wantCentral {
		t.Fatalf("central checkout = %q, want %q", central.created.WorktreeDir, wantCentral)
	}
	// Repository-local mode places the checkout inside its own canonical clone.
	wantLocal := filepath.Join(local.canonical, ".worktrees", "mode-identity")
	if local.created.WorktreeDir != wantLocal {
		t.Fatalf("repository-local checkout = %q, want %q", local.created.WorktreeDir, wantLocal)
	}
	// Identity must not depend on the mode.
	if central.identity != local.identity {
		t.Fatalf("identity changed across store modes: central=%+v repository-local=%+v", central.identity, local.identity)
	}
	if central.identity.branch != "mode-identity" || central.identity.effortID != "mode-identity" || central.identity.runID != "mode-identity-run" {
		t.Fatalf("unexpected identity %+v", central.identity)
	}
	if central.identity.claimID == "" || central.identity.workLogTail == "" {
		t.Fatalf("incomplete identity %+v", central.identity)
	}
}

// TestCentralStoreEmbedsTheOriginHostForALegacyClone encodes the exact Then
// clause of projects-root-layout#ac:central-store-is-default for the case the
// store must not get wrong: a canonical clone still at the legacy on-disk
// <root>/{org}/{repo} path whose origin names a forge. Its checkouts must land
// under <root>/.worktrees/<task>/<host>/<org>/<repo> — with no clone moving —
// and guard/inventory must still recognize them there.
func TestCentralStoreEmbedsTheOriginHostForALegacyClone(t *testing.T) {
	remote := newSharedHostedOrigin(t)
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	canonical := cloneHostedOriginInto(t, remote, projectsRoot, "acme", "app")
	baseRevision := gitTestOutput(t, canonical, "rev-parse", "origin/main")

	placement, err := ResolveWorktreePlacement(context.Background(), projectsRoot, canonical, baseRevision)
	if err != nil {
		t.Fatal(err)
	}
	path, err := placement.Path("review-task", "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projectsRoot, ".worktrees", "review-task", "github.com", "acme", "app")
	if path != want {
		t.Fatalf("planned central path = %q, want %q", path, want)
	}

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "review-task", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].WorktreeDir != want || created[0].CanonicalDir != canonical {
		t.Fatalf("created = %#v, want checkout %q of canonical %q", created, want, canonical)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("hosted central checkout was not created: %v", err)
	}
	if _, err := Guard(context.Background(), want, GuardOptions{ProjectsRoot: projectsRoot}); err != nil {
		t.Fatalf("guard rejected the hosted central checkout: %v", err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: projectsRoot, Task: "review-task", Workers: 1})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != want || listed[0].CanonicalDir != canonical {
		t.Fatalf("inventory of the hosted central checkout = %#v, err=%v", listed, err)
	}
	// A checkout published before the store suffix carried a host level keeps
	// being found where it lies.
	legacyStore := filepath.Join(projectsRoot, ".worktrees", "review-task", "acme", "app")
	if _, statErr := os.Stat(legacyStore); !os.IsNotExist(statErr) {
		t.Fatalf("the two store spellings must stay distinct, but %s exists: %v", legacyStore, statErr)
	}
}

// TestRepositoryTrackedStoreModeIsRejectedNamingTheUserConfig encodes the
// store-mode-is-user-policy half of both ACs: a repository-tracked
// .wb/worktrees.yaml may not select or override the mode (or the central store
// root), and the refusal must name the machine-local configuration path.
func TestRepositoryTrackedStoreModeIsRejectedNamingTheUserConfig(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "selects repository-local mode",
			contents: "version: 1\nworktrees:\n  store: repository-local\n",
			want:     "must not set worktrees.store",
		},
		{
			name:     "overrides with central mode",
			contents: "version: 1\nworktrees:\n  store: central\n",
			want:     "must not set worktrees.store",
		},
		{
			name:     "selects the store root",
			contents: "version: 1\nworktrees:\n  root: /tmp/repository-chosen-store\n",
			want:     "must not set worktrees.root",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStoreModeFixture(t, "app")
			canonical := fixture.canonicals["app"]
			mustWriteBranchConfig(t, filepath.Join(canonical, ".wb", "worktrees.yaml"), test.contents)
			gitTest(t, canonical, "add", ".wb/worktrees.yaml")
			gitTest(t, canonical, "commit", "-m", "repository store policy")
			gitTest(t, canonical, "push", "origin", "main")

			_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot, Operation: "repo-policy", WorkLog: WorkLogOptions{Model: "unknown"},
			})
			if err == nil {
				t.Fatal("a repository-tracked worktrees policy selected the store mode")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("rejection = %v, want it to mention %q", err, test.want)
			}
			if !strings.Contains(err.Error(), fixture.userConfig) {
				t.Fatalf("rejection = %v, want it to name the user-only configuration path %s", err, fixture.userConfig)
			}
		})
	}
}

// TestStoreModeChangeNeverMovesOrReselectsAnExistingCheckout encodes the
// compatibility half of store-mode-is-user-policy: switching the configured
// mode must neither move an existing central checkout nor re-select it, and the
// task's branch, claim and Work Log stay byte-identical on resume.
func TestStoreModeChangeNeverMovesOrReselectsAnExistingCheckout(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	ctx := context.Background()

	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "mode-change", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	centralPath := created[0].WorktreeDir
	if want := filepath.Join(fixture.projectsRoot, ".worktrees", "mode-change", "github.com", "acme", "app"); centralPath != want {
		t.Fatalf("central checkout = %q, want %q", centralPath, want)
	}
	projectionBefore, err := readWorkLogProjection(centralPath)
	if err != nil {
		t.Fatal(err)
	}

	fixture.selectStoreMode(t, "repository-local")

	listed, err := List(ctx, ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "mode-change", Workers: 1})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != centralPath {
		t.Fatalf("inventory after mode change = %#v, err=%v; want the existing central checkout", listed, err)
	}
	resumed, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "mode-change", Resume: true, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].WorktreeDir != centralPath || resumed[0].Action != "resumed" {
		t.Fatalf("resume after mode change = %#v, want the unchanged central checkout", resumed)
	}
	if resumed[0].Branch != created[0].Branch || resumed[0].WorkLogPath != created[0].WorkLogPath {
		t.Fatalf("identity drifted across mode change: created=%#v resumed=%#v", created[0], resumed[0])
	}
	if _, err := os.Stat(filepath.Join(fixture.canonicals["app"], ".worktrees", "mode-change")); !os.IsNotExist(err) {
		t.Fatalf("mode change re-selected an existing checkout into the repository-local layout: %v", err)
	}
	projectionAfter, err := readWorkLogProjection(centralPath)
	if err != nil || projectionAfter != projectionBefore {
		t.Fatalf("claim identity changed across mode change: before=%#v after=%#v err=%v", projectionBefore, projectionAfter, err)
	}
}

// TestCentralDefaultStillFindsARepositoryLocalCheckout is the reverse of the
// previous test: a checkout created while repository-local mode was selected
// must still be found — not hidden and not re-created — once the machine
// returns to the central default.
func TestCentralDefaultStillFindsARepositoryLocalCheckout(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	ctx := context.Background()
	fixture.selectStoreMode(t, "repository-local")

	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-first", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	localPath := filepath.Join(fixture.canonicals["app"], ".worktrees", "local-first")
	if created[0].WorktreeDir != localPath {
		t.Fatalf("repository-local checkout = %q, want %q", created[0].WorktreeDir, localPath)
	}

	if err := os.Remove(fixture.userConfig); err != nil {
		t.Fatal(err)
	}

	listed, err := List(ctx, ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "local-first", Workers: 1})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != localPath {
		t.Fatalf("inventory under the central default = %#v, err=%v; want the repository-local checkout", listed, err)
	}
	resumed, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-first", Resume: true, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(resumed) != 1 || resumed[0].WorktreeDir != localPath || resumed[0].Action != "resumed" {
		t.Fatalf("resume under the central default = %#v, err=%v; want the repository-local checkout", resumed, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectsRoot, ".worktrees", "local-first")); !os.IsNotExist(err) {
		t.Fatalf("the central default duplicated an existing repository-local checkout: %v", err)
	}
}

// TestUserStoreConfigurationValidationRejectsUnsupportedAndConflictingModes
// pins the parse boundary of the user-only file so a typo cannot silently
// select a mode.
func TestUserStoreConfigurationValidationRejectsUnsupportedAndConflictingModes(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	canonical := fixture.canonicals["app"]
	base := gitTestOutput(t, canonical, "rev-parse", "origin/main")

	for _, test := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "unsupported mode", contents: "version: 1\nworktrees:\n  store: somewhere\n", want: "store"},
		{name: "mode and root together", contents: "version: 1\nworktrees:\n  store: repository-local\n  root: " + fixture.base + "\n", want: "store"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mustWriteBranchConfig(t, fixture.userConfig, test.contents)
			_, err := ResolveWorktreePlacement(context.Background(), fixture.projectsRoot, canonical, base)
			if err == nil {
				t.Fatal("invalid user store configuration was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to mention %q", err, test.want)
			}
			if !strings.Contains(err.Error(), fixture.userConfig) {
				t.Fatalf("error = %v, want it to name %s", err, fixture.userConfig)
			}
		})
	}
}

// TestCentralStoreRootIsTheProjectsRootStore proves the default central store
// is the root-derived store, not a second knob.
func TestCentralStoreRootIsTheProjectsRootStore(t *testing.T) {
	fixture := newStoreModeFixture(t, "app")
	canonical := fixture.canonicals["app"]
	base := gitTestOutput(t, canonical, "rev-parse", "origin/main")
	storeRoot, err := wbhome.StoreRoot(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	placement, err := ResolveWorktreePlacement(context.Background(), fixture.projectsRoot, canonical, base)
	if err != nil {
		t.Fatal(err)
	}
	if placement.RepositoryLocal || filepath.Clean(placement.Root) != filepath.Clean(storeRoot) {
		t.Fatalf("default placement = %#v, want the central store root %q", placement, storeRoot)
	}
}

// TestStoreModeHelpersUnderstandTheHostLevel pins the path arithmetic every
// caller relies on: a hosted clone keeps its literal host level in the central
// layout, a legacy clone keeps the two-level suffix, and an address that is not
// a safe relative clone path is refused rather than joined.
func TestStoreModeHelpersUnderstandTheHostLevel(t *testing.T) {
	for _, test := range []struct {
		relative string
		ok       bool
	}{
		{relative: "github.com/acme/app", ok: true},
		{relative: "git.example.test/acme/app", ok: true},
		{relative: "acme/app", ok: true},
		{relative: "not-a-host/acme/app", ok: false},
		{relative: "acme/app/extra", ok: false},
		{relative: "acme", ok: false},
		{relative: "../acme/app", ok: false},
		{relative: "acme/..", ok: false},
	} {
		if got := validCloneRelative(test.relative); got != test.ok {
			t.Fatalf("validCloneRelative(%q) = %t, want %t", test.relative, got, test.ok)
		}
	}
	for _, test := range []struct {
		relative string
		parent   string
		repo     string
	}{
		{relative: "github.com/acme/app", parent: "github.com/acme", repo: "app"},
		{relative: "acme/app", parent: "acme", repo: "app"},
		{relative: "app", parent: "", repo: "app"},
	} {
		parent, repo := splitCloneRelative(test.relative)
		if parent != test.parent || repo != test.repo {
			t.Fatalf("splitCloneRelative(%q) = (%q, %q), want (%q, %q)", test.relative, parent, repo, test.parent, test.repo)
		}
	}
	if _, _, err := splitCloneRelativeOrError("not-a-host/acme/app"); err == nil {
		t.Fatal("an unsafe clone address was accepted")
	}

	store := filepath.Join(string(filepath.Separator), "store")
	for _, test := range []struct {
		name        string
		destination string
		relative    string
		to          string
		want        string
	}{
		{
			name: "central", to: "shared", relative: "github.com/acme/app",
			destination: filepath.Join(store, "task", "github.com", "acme", "app"), want: store,
		},
		{
			name: "legacy", to: "shared", relative: "acme/app",
			destination: filepath.Join(store, "task", "acme", "app"), want: store,
		},
		{
			name: "local", to: "local", relative: "github.com/acme/app",
			destination: filepath.Join(store, "canonical", ".worktrees", "task"), want: filepath.Join(store, "canonical", ".worktrees"),
		},
	} {
		if got := relocationDestinationRoot(test.destination, test.relative, test.to); filepath.Clean(got) != filepath.Clean(test.want) {
			t.Fatalf("%s relocationDestinationRoot = %q, want %q", test.name, got, test.want)
		}
	}

	// A private claim must corroborate a central checkout at its host level
	// exactly as it corroborates a legacy shared one.
	for _, test := range []struct {
		name string
		path string
		ok   bool
	}{
		{name: "legacy", path: filepath.Join(store, "task", "acme", "app"), ok: true},
		{name: "central", path: filepath.Join(store, "task", "github.com", "acme", "app"), ok: true},
		{name: "wrong task", path: filepath.Join(store, "other", "github.com", "acme", "app"), ok: false},
		{name: "wrong repository", path: filepath.Join(store, "task", "acme", "other"), ok: false},
	} {
		layout, err := claimedSharedWorktreeLayout(test.path, workLogClaim{Task: "task", Repository: "acme/app"})
		if test.ok && (err != nil || filepath.Clean(layout.WorktreesRoot) != filepath.Clean(store)) {
			t.Fatalf("%s claimedSharedWorktreeLayout = %#v, %v; want root %q", test.name, layout, err, store)
		}
		if !test.ok && err == nil {
			t.Fatalf("%s claimedSharedWorktreeLayout accepted %s", test.name, test.path)
		}
	}

	// An unsafe parent segment is refused before anything is created.
	root := t.TempDir()
	base := wtLifeCovOpenDirectory(t, root)
	if _, _, err := openRelativeParentDirectory(base, root, ".."); err == nil {
		t.Fatal("an escaping parent segment was accepted")
	}
	direct, directPath, err := openRelativeParentDirectory(base, root, "")
	if err != nil || filepath.Clean(directPath) != filepath.Clean(root) {
		t.Fatalf("empty parent = %q, %v", directPath, err)
	}
	_ = direct.Close()
}
