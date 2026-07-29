package hooks

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SecureHooksGitHelperArgument {
		os.Exit(RunSecureHooksGitHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestLoadPolicyLayersGlobalAndRepositoryTemplates(t *testing.T) {
	repo := initRepo(t)
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	globalDir := filepath.Join(configHome, "wb")
	mustMkdirAll(t, filepath.Join(globalDir, "templates"))
	mustWrite(t, filepath.Join(globalDir, "templates", "pre-push.sh"), "#!/bin/sh\necho global\n")
	mustWrite(t, filepath.Join(globalDir, "hooks.yaml"), `version: 1
hooks:
  pre-push:
    template: templates/pre-push.sh
metrics:
  enabled: false
`)

	repoConfigDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(repoConfigDir, "templates"))
	mustWrite(t, filepath.Join(repoConfigDir, "templates", "pre-commit.sh"), "#!/bin/sh\necho repo\n")
	mustWrite(t, filepath.Join(repoConfigDir, "hooks.yaml"), `version: 1
hooks:
  pre-commit:
    template: templates/pre-commit.sh
  pre-push:
    disabled: true
metrics:
  enabled: true
  path: metrics/events.jsonl
  labels:
    developer: alex
    machine: laptop
`)

	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := policy.Hooks["pre-commit"].Template, filepath.Join(repoConfigDir, "templates", "pre-commit.sh"); got != want {
		t.Fatalf("pre-commit template = %q, want %q", got, want)
	}
	if !policy.Hooks["pre-push"].Disabled {
		t.Fatal("repository policy should disable global pre-push")
	}
	if !policy.Metrics.Enabled {
		t.Fatal("repository policy should re-enable metrics")
	}
	if got, want := policy.Metrics.Path, filepath.Join(repoConfigDir, "metrics", "events.jsonl"); got != want {
		t.Fatalf("metrics path = %q, want %q", got, want)
	}
	if policy.Metrics.Labels["developer"] != "alex" || policy.Metrics.Labels["machine"] != "laptop" {
		t.Fatalf("metrics labels = %#v", policy.Metrics.Labels)
	}
	if len(policy.ConfigPaths) != 2 {
		t.Fatalf("config paths = %v, want global + repository", policy.ConfigPaths)
	}
	if got, want := expectedHookNames(policy), []string{"post-commit", "pre-commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected hooks = %v, want %v", got, want)
	}
}

func TestLoadPolicyRejectsUnknownAndMissingTemplates(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	mustWrite(t, config, "version: 1\nunknown: true\n")
	if _, err := LoadPolicy(repo, config); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unknown field error = %v", err)
	}
	mustWrite(t, config, "version: 1\nhooks:\n  pre-commit:\n    template: missing.sh\n")
	if _, err := LoadPolicy(repo, config); err == nil || !strings.Contains(err.Error(), "missing.sh") {
		t.Fatalf("missing template error = %v", err)
	}
}

func TestLoadPolicyAutoDetectsOnlyRelevantBuiltInProfiles(t *testing.T) {
	for _, test := range []struct {
		name         string
		files        []string
		wantProfiles []string
		wantPrePush  []string
	}{
		{name: "go only", files: []string{"go.mod"}, wantProfiles: []string{"go"}, wantPrePush: []string{"base/pre-push", "go/pre-push"}},
		{name: "node only", files: []string{"package.json"}, wantProfiles: []string{"node"}, wantPrePush: []string{"base/pre-push", "node/pre-push"}},
		{name: "mixed", files: []string{"go.mod", "package.json"}, wantProfiles: []string{"go", "node"}, wantPrePush: []string{"base/pre-push", "go/pre-push", "node/pre-push"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initRepo(t)
			isolateConfig(t)
			for _, file := range test.files {
				mustWrite(t, filepath.Join(repo, file), "{}\n")
			}
			configDir := filepath.Join(repo, ".wb")
			mustMkdirAll(t, configDir)
			mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  auto: true\n")

			policy, err := LoadPolicy(repo, "")
			if err != nil {
				t.Fatal(err)
			}
			var profiles []string
			for _, profile := range policy.ActiveProfiles {
				profiles = append(profiles, profile.Name)
			}
			if !reflect.DeepEqual(profiles, test.wantProfiles) {
				t.Fatalf("active profiles = %v, want %v", profiles, test.wantProfiles)
			}
			var blocks []string
			for _, block := range hookBlocks(policy, "pre-push") {
				blocks = append(blocks, block.ID)
			}
			if !reflect.DeepEqual(blocks, test.wantPrePush) {
				t.Fatalf("pre-push blocks = %v, want %v", blocks, test.wantPrePush)
			}
		})
	}
}

func TestLoadPolicyDoesNotAutoDetectProfilesUntilEnabled(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/opt-in\n\ngo 1.26\n")
	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if policy.ProfilesAuto || len(policy.ActiveProfiles) != 0 {
		t.Fatalf("profiles should be opt-in: auto = %v, active = %#v", policy.ProfilesAuto, policy.ActiveProfiles)
	}
}

func TestWorktreeProfileIsExplicitAndCoversCheckoutCommitAndPush(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  include: [worktree]\nmetrics:\n  enabled: false\n")

	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.ActiveProfiles) != 1 || policy.ActiveProfiles[0].Name != "worktree" ||
		policy.ActiveProfiles[0].Reason != "included by policy" {
		t.Fatalf("active profiles = %#v", policy.ActiveProfiles)
	}
	for _, hook := range []string{"post-checkout", "pre-commit", "pre-push"} {
		blocks := hookBlocks(policy, hook)
		if len(blocks) == 0 || blocks[len(blocks)-1].ID != "worktree/"+hook {
			t.Fatalf("%s blocks = %#v", hook, blocks)
		}
	}
}

func TestWorktreeProfileInvokesSameWBExecutableWithProjectsRoot(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  include: [worktree]\nmetrics:\n  enabled: false\n")
	logPath := filepath.Join(t.TempDir(), "wb.log")
	fakeWB := filepath.Join(t.TempDir(), "wb")
	mustWriteExecutable(t, fakeWB, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WB_TEST_LOG\"\n")
	t.Setenv("WB_TEST_LOG", logPath)
	projects := filepath.Join(t.TempDir(), "projects")

	result, err := Run(RunOptions{
		RepoPath: repo, Hook: "post-checkout",
		Args: []string{"old", "new", "1"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		WBExecutable: fakeWB, ProjectsRoot: projects,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("run result = %#v, error = %v", result, err)
	}
	output, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--projects-root " + projects + " worktree guard --quiet " + repo
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("guard invocation = %q, want %q", got, want)
	}
}

func TestManagedShimPersistsWBHomeAndRefreshesPriorReleaseShim(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  include: [worktree]\nmetrics:\n  enabled: false\n")
	projects := filepath.Join(t.TempDir(), "projects")
	home := filepath.Join(t.TempDir(), "explicit-home")
	resolvedHomeParent, err := filepath.EvalSymlinks(filepath.Dir(home))
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome := filepath.Join(resolvedHomeParent, filepath.Base(home))
	logPath := filepath.Join(t.TempDir(), "wb.log")
	fakeWB := filepath.Join(t.TempDir(), "wb")
	mustWriteExecutable(t, fakeWB, "#!/bin/sh\nprintf '%s|%s\\n' \"$WB_HOME\" \"$*\" >> \"$WB_TEST_LOG\"\n")
	t.Setenv("WB_TEST_LOG", logPath)
	t.Setenv("WB_HOME", home)

	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: fakeWB, ProjectsRoot: projects}); err != nil {
		t.Fatal(err)
	}
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	// This is the prior release's managed section: it stores the projects root
	// but did not store WB_HOME.
	for _, hook := range []string{"post-checkout", "pre-commit", "pre-push"} {
		if err := writeExecutable(filepath.Join(managed, hook), []byte(shimContent(fakeWB, hook, "", projects, "", false))); err != nil {
			t.Fatal(err)
		}
	}
	refreshed, err := RefreshManagedShims(repo, "", fakeWB, projects)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("prior-release shim was not refreshed")
	}
	if report, err := Check(repo, "", fakeWB, projects); err != nil || len(report.Findings) != 0 {
		t.Fatalf("refreshed hook report = %#v, %v", report, err)
	}
	shim, err := os.ReadFile(filepath.Join(managed, "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shim), "export WB_HOME='"+resolvedHome+"'") {
		t.Fatalf("refreshed shim does not persist WB_HOME:\n%s", shim)
	}
	command := exec.Command(filepath.Join(managed, "pre-commit"))
	command.Dir = repo
	command.Env = append(os.Environ(), "WB_TEST_LOG="+logPath, "WB_HOME=wrong-home")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run persisted shim: %v\n%s", err, output)
	}
	output, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(output)), resolvedHome+"|--projects-root "+projects+" hooks run pre-commit --"; got != want {
		t.Fatalf("persisted shim invocation = %q, want %q", got, want)
	}
}

func TestApplyRejectsTransientGoRunExecutable(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	transient := filepath.Join(t.TempDir(), "go-build123456", "exe", "wb")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: transient}); err == nil || !strings.Contains(err.Error(), "transient go run executable") {
		t.Fatalf("transient executable error = %v", err)
	}
}

func TestDurableWBExecutableRejectsInvalidOrTransientTargets(t *testing.T) {
	root := t.TempDir()
	nonExecutable := filepath.Join(root, "not-executable")
	mustWrite(t, nonExecutable, "#!/bin/sh\nexit 0\n")
	directory := filepath.Join(root, "directory")
	mustMkdirAll(t, directory)
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing-target"), dangling); err != nil {
		t.Fatal(err)
	}
	transientTarget := filepath.Join(root, "go-build999", "b001", "exe", "wb")
	mustMkdirAll(t, filepath.Dir(transientTarget))
	mustWriteExecutable(t, transientTarget, "#!/bin/sh\nexit 0\n")
	transientLink := filepath.Join(root, "wb-link")
	if err := os.Symlink(transientTarget, transientLink); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		executable string
		want       string
	}{
		{name: "missing", executable: filepath.Join(root, "missing"), want: "resolve WB executable"},
		{name: "directory", executable: directory, want: "regular file"},
		{name: "not executable", executable: nonExecutable, want: "not executable"},
		{name: "dangling symlink", executable: dangling, want: "resolve WB executable"},
		{name: "direct transient path", executable: transientTarget, want: "transient go run executable"},
		{name: "symlink to transient path", executable: transientLink, want: "transient go run executable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := durableWBExecutable(test.executable); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("durableWBExecutable(%q) error = %v, want %q", test.executable, err, test.want)
			}
		})
	}
}

func TestApplyRetainsStableLauncherAcrossRetarget(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	root := t.TempDir()
	launcherDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "launcher.log")
	t.Setenv("WB_TEST_LOG", logPath)
	releaseV1 := filepath.Join(root, "Caskroom", "wb", "1.0.0", "wb")
	releaseV2 := filepath.Join(root, "Caskroom", "wb", "1.1.0", "wb")
	for version, release := range map[string]string{"v1": releaseV1, "v2": releaseV2} {
		if err := os.MkdirAll(filepath.Dir(release), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWriteExecutable(t, release, "#!/bin/sh\nprintf '%s\\n' '"+version+"' >> \"$WB_TEST_LOG\"\n")
	}
	launcher := filepath.Join(launcherDir, "wb")
	if err := os.Symlink(releaseV1, launcher); err != nil {
		t.Fatal(err)
	}
	absLauncher, err := filepath.Abs(launcher)
	if err != nil {
		t.Fatal(err)
	}
	absLauncher = filepath.Clean(absLauncher)

	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: launcher})
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(result.Report.ManagedPath, "pre-commit")
	shim, err := os.ReadFile(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shim), shellQuote(absLauncher)+" hooks run 'pre-commit'") || strings.Contains(string(shim), releaseV1) {
		t.Fatalf("shim did not retain stable launcher:\n%s", shim)
	}

	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(releaseV2, launcher); err != nil {
		t.Fatal(err)
	}
	if report, checkErr := Check(repo, "", launcher, ""); checkErr != nil || len(report.Findings) != 0 {
		t.Fatalf("retargeted launcher check = %#v, %v", report, checkErr)
	}
	command := exec.Command(preCommit)
	command.Dir = repo
	command.Env = os.Environ()
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("run retargeted managed hook: %v\n%s", runErr, output)
	}
	if got := strings.TrimSpace(mustReadFile(t, logPath)); got != "v2" {
		t.Fatalf("retargeted hook launcher = %q, want v2", got)
	}
}

func TestApplyRefusesSymlinkedManagedHooksDirectoryWithoutExternalMutation(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	sentinel := filepath.Join(external, "keep.txt")
	mustWrite(t, sentinel, "outside\n")
	if err := os.Symlink(external, managed); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ApplyOptions{RepoPath: repo, WBExecutable: testWBExecutable(t, "wb")})
	if err == nil || !strings.Contains(err.Error(), "symlinked managed hooks directory") {
		t.Fatalf("symlinked managed directory install error = %v", err)
	}
	if report, checkErr := Check(repo, "", testWBExecutable(t, "check-wb"), ""); checkErr != nil || !hasFinding(report.Findings, "managed-hooks-path") {
		t.Fatalf("symlinked managed directory check = %#v, %v", report, checkErr)
	}
	entries, err := os.ReadDir(external)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("external target was mutated: %v", entries)
	}
	if content := mustReadFile(t, sentinel); content != "outside\n" {
		t.Fatalf("external sentinel changed: %q", content)
	}
}

func TestApplyRefusesSymlinkedGitCommonDirectoryWithoutExternalMutation(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	gitDirectory := filepath.Join(repo, ".git")
	external := filepath.Join(t.TempDir(), "external-git")
	if err := os.Rename(gitDirectory, external); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, gitDirectory); err != nil {
		t.Fatal(err)
	}

	_, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: testWBExecutable(t, "wb")})
	if err == nil || !strings.Contains(err.Error(), "symlinked Git common directory") {
		t.Fatalf("symlinked Git common directory install error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(external, "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("external Git directory received managed hooks: %v", statErr)
	}
}

func TestApplyRefusesGitCommonDirectoryAncestorSwapWithoutExternalMutation(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	gitDirectory := filepath.Join(repo, ".git")
	movedGitDirectory := filepath.Join(t.TempDir(), "moved-git")
	external := t.TempDir()
	sentinel := filepath.Join(external, "keep.txt")
	mustWrite(t, sentinel, "outside\n")

	_, err := Apply(ApplyOptions{
		RepoPath:     repo,
		WBExecutable: testWBExecutable(t, "wb"),
		afterManagedHooksValidation: func() {
			if renameErr := os.Rename(gitDirectory, movedGitDirectory); renameErr != nil {
				t.Fatalf("move Git common directory during hook regression: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, gitDirectory); symlinkErr != nil {
				t.Fatalf("substitute Git common directory during hook regression: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "git common directory path changed") {
		t.Fatalf("ancestor-swapped Git common directory install error = %v", err)
	}
	if content := mustReadFile(t, sentinel); content != "outside\n" {
		t.Fatalf("external sentinel changed: %q", content)
	}
	if _, statErr := os.Stat(filepath.Join(external, "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("external Git directory received managed hooks: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedGitDirectory, "wb-hooks", "pre-commit")); !os.IsNotExist(statErr) {
		t.Fatalf("moved Git directory received a hook after the failed operation: %v", statErr)
	}
}

func TestApplyRefusesGitCommonAncestorSwapBeforeFirstCommonDirectoryOpen(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	gitDirectory := filepath.Join(repo, ".git")
	movedGitDirectory := filepath.Join(t.TempDir(), "moved-git")
	external := t.TempDir()
	sentinel := filepath.Join(external, "keep.txt")
	mustWrite(t, sentinel, "outside\n")

	_, err := Apply(ApplyOptions{
		RepoPath:     repo,
		WBExecutable: testWBExecutable(t, "wb"),
		afterManagedHooksPathValidation: func() {
			if renameErr := os.Rename(gitDirectory, movedGitDirectory); renameErr != nil {
				t.Fatalf("move Git common directory before first open: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, gitDirectory); symlinkErr != nil {
				t.Fatalf("substitute Git common directory before first open: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "open Git common directory") {
		t.Fatalf("pre-open Git common ancestor swap error = %v", err)
	}
	if content := mustReadFile(t, sentinel); content != "outside\n" {
		t.Fatalf("external sentinel changed: %q", content)
	}
	if _, statErr := os.Stat(filepath.Join(external, "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("external Git directory received managed hooks: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedGitDirectory, "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("moved Git directory received managed hooks: %v", statErr)
	}
}

func TestApplyRefusesRepositoryAncestorSwapBeforeFirstCommonDirectoryOpen(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	movedRepo := filepath.Join(t.TempDir(), "moved-repo")
	external := t.TempDir()
	sentinel := filepath.Join(external, "keep.txt")
	mustWrite(t, sentinel, "outside\n")

	_, err := Apply(ApplyOptions{
		RepoPath:     repo,
		WBExecutable: testWBExecutable(t, "wb"),
		afterManagedHooksPathValidation: func() {
			if renameErr := os.Rename(repo, movedRepo); renameErr != nil {
				t.Fatalf("move repository before first common open: %v", renameErr)
			}
			if symlinkErr := os.Symlink(external, repo); symlinkErr != nil {
				t.Fatalf("substitute repository before first common open: %v", symlinkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "open Git common directory") {
		t.Fatalf("pre-open repository ancestor swap error = %v", err)
	}
	if content := mustReadFile(t, sentinel); content != "outside\n" {
		t.Fatalf("external sentinel changed: %q", content)
	}
	if _, statErr := os.Stat(filepath.Join(external, ".git", "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("external repository target received managed hooks: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedRepo, ".git", "wb-hooks")); !os.IsNotExist(statErr) {
		t.Fatalf("moved repository received managed hooks: %v", statErr)
	}
}

func TestApplyRefusesManagedHookReplacementAfterRead(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	oldExecutable := testWBExecutable(t, "old-wb")
	newExecutable := testWBExecutable(t, "new-wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: oldExecutable})
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	replacement := "#!/bin/sh\necho replacement\n"
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: newExecutable,
		afterManagedHookRead: func(name string) {
			if name == "pre-commit" {
				replaceTestFile(t, preCommit, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("managed hook replacement error = %v", err)
	}
	if content := mustReadFile(t, preCommit); content != replacement {
		t.Fatalf("replacement hook was overwritten: %q", content)
	}
}

func TestApplyRefusesManagedHookReplacementAfterFinalAuthorization(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	oldExecutable := testWBExecutable(t, "old-wb")
	newExecutable := testWBExecutable(t, "new-wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: oldExecutable})
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	replacement := "#!/bin/sh\necho replacement-after-final-check\n"
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: newExecutable,
		afterManagedHookAuthorization: func(name string) {
			if name == "pre-commit" {
				replaceTestFile(t, preCommit, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("managed hook late-authorization replacement error = %v", err)
	}
	if content := mustReadFile(t, preCommit); content != replacement {
		t.Fatalf("late replacement hook was overwritten: %q", content)
	}
}

func TestApplyRefusesUnmanagedHookReplacementAfterReadBeforeBackup(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	replaceTestFile(t, preCommit, "#!/bin/sh\necho unmanaged-original\n")
	replacement := "#!/bin/sh\necho unmanaged-replacement\n"
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, Force: true, Now: func() time.Time { return now },
		afterManagedHookRead: func(name string) {
			if name == "pre-commit" {
				replaceTestFile(t, preCommit, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("unmanaged hook backup replacement error = %v", err)
	}
	if content := mustReadFile(t, preCommit); content != replacement {
		t.Fatalf("replacement unmanaged hook was overwritten: %q", content)
	}
	backup := preCommit + ".wb-backup-20260729T120000Z"
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("backup created from replacement hook: %v", statErr)
	}
}

func TestApplyRefusesUnmanagedHookReplacementAfterFinalAuthorizationBeforeBackup(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	mustWrite(t, preCommit, "#!/bin/sh\necho unmanaged-original\n")
	replacement := "#!/bin/sh\necho unmanaged-replacement-after-final-check\n"
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, Force: true, Now: func() time.Time { return now },
		afterManagedHookAuthorization: func(name string) {
			if name == "pre-commit" {
				replaceTestFile(t, preCommit, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("unmanaged hook late-authorization backup error = %v", err)
	}
	if content := mustReadFile(t, preCommit); content != replacement {
		t.Fatalf("late replacement unmanaged hook was overwritten: %q", content)
	}
	backup := preCommit + ".wb-backup-20260729T120000Z"
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("backup created from late replacement hook: %v", statErr)
	}
}

func TestRepairRefusesStaleHookReplacementAfterReadBeforeRemoval(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(installed.Report.ManagedPath, "commit-msg")
	mustWrite(t, stale, shimContent(executable, "commit-msg", "", "", "", false))
	replacement := "#!/bin/sh\necho stale-replacement\n"
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, Repair: true,
		afterManagedHookRead: func(name string) {
			if name == "commit-msg" {
				replaceTestFile(t, stale, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("stale hook removal replacement error = %v", err)
	}
	if content := mustReadFile(t, stale); content != replacement {
		t.Fatalf("replacement stale hook was removed: %q", content)
	}
}

func TestRepairRefusesStaleHookReplacementAfterFinalAuthorization(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(installed.Report.ManagedPath, "commit-msg")
	mustWrite(t, stale, shimContent(executable, "commit-msg", "", "", "", false))
	replacement := "#!/bin/sh\necho stale-replacement-after-final-check\n"
	_, err = Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, Repair: true,
		afterManagedHookAuthorization: func(name string) {
			if name == "commit-msg" {
				replaceTestFile(t, stale, replacement)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("stale hook late-authorization removal error = %v", err)
	}
	if content := mustReadFile(t, stale); content != replacement {
		t.Fatalf("late replacement stale hook was removed: %q", content)
	}
}

func TestSetHooksPathAtUsesRetainedRepositoryDescriptor(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := openManagedHooksDirectory(repo, managed, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.close()
	movedRepo := filepath.Join(t.TempDir(), "moved-repo")
	if err := os.Rename(repo, movedRepo); err != nil {
		t.Fatal(err)
	}
	if err := setHooksPathAt(directory.repo, managed); err != nil {
		t.Fatal(err)
	}
	if got := git(t, movedRepo, "config", "--local", "--get", "core.hooksPath"); got != managed {
		t.Fatalf("descriptor-anchored core.hooksPath = %q, want %q", got, managed)
	}
}

func TestApplyDoesNotFollowPlantedPredictableHookTempSymlink(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "outside.txt")
	mustWrite(t, external, "outside\n")
	if err := os.Symlink(external, filepath.Join(managed, "pre-commit.tmp")); err != nil {
		t.Fatal(err)
	}
	executable := testWBExecutable(t, "wb")
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	if content := mustReadFile(t, external); content != "outside\n" {
		t.Fatalf("planted external temp target was mutated: %q", content)
	}
	preCommit := filepath.Join(managed, "pre-commit")
	info, err := os.Lstat(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("installed pre-commit hook is not a regular file: %v", info.Mode())
	}
	if report, checkErr := Check(repo, "", executable, ""); checkErr != nil || len(report.Findings) != 0 || len(result.Report.Findings) != 0 {
		t.Fatalf("post-install hook report = %#v, result = %#v, error = %v", report, result.Report, checkErr)
	}
}

func TestRefreshManagedShimsRepairsNonExecutableShim(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err != nil {
		t.Fatal(err)
	}
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	preCommit := filepath.Join(managed, "pre-commit")
	if err := os.Chmod(preCommit, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := RefreshManagedShims(repo, "", executable, "")
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("non-executable managed shim was not refreshed")
	}
	info, err := os.Stat(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("refreshed shim is not executable: %v", info.Mode())
	}
	if report, checkErr := Check(repo, "", executable, ""); checkErr != nil || len(report.Findings) != 0 {
		t.Fatalf("refreshed hook report = %#v, %v", report, checkErr)
	}
}

func TestRefreshManagedShimsRefusesLexicallyConfiguredSymlinkedManagedDirectory(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	sentinel := filepath.Join(external, "sentinel.txt")
	mustWrite(t, sentinel, "outside\n")
	if err := os.Symlink(external, managed); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "config", "--local", "core.hooksPath", managed)
	if _, err := RefreshManagedShims(repo, "", testWBExecutable(t, "wb"), ""); err == nil || !strings.Contains(err.Error(), "symlinked managed hooks directory") {
		t.Fatalf("symlinked lexical managed refresh error = %v", err)
	}
	if content := mustReadFile(t, sentinel); content != "outside\n" {
		t.Fatalf("refresh mutated external managed-directory target: %q", content)
	}
}

func TestLoadPolicyCustomProductProfileAndBuiltInOverride(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/profile-test\n\ngo 1.26\n")
	mustMkdirAll(t, filepath.Join(repo, "config"))
	mustWrite(t, filepath.Join(repo, "config", "sneat.project.yaml"), "product: gameboard\n")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(configDir, "templates"))
	mustWrite(t, filepath.Join(configDir, "templates", "go.sh"), "#!/bin/sh\necho custom-go\n")
	mustWrite(t, filepath.Join(configDir, "templates", "product.sh"), "#!/bin/sh\necho product\n")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
profiles:
  auto: true
  definitions:
    go:
      hooks:
        pre-push:
          template: templates/go.sh
    sneat-product:
      order: 150
      detect:
        any_files:
          - config/*.yaml
          - firebase.json
      hooks:
        pre-push:
          template: templates/product.sh
`)

	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{policy.ActiveProfiles[0].Name, policy.ActiveProfiles[1].Name}; !reflect.DeepEqual(got, []string{"go", "sneat-product"}) {
		t.Fatalf("active profiles = %v", got)
	}
	blocks := hookBlocks(policy, "pre-push")
	if got := []string{blocks[0].ID, blocks[1].ID, blocks[2].ID}; !reflect.DeepEqual(got, []string{"base/pre-push", "go/pre-push", "sneat-product/pre-push"}) {
		t.Fatalf("blocks = %v", got)
	}
	if got, want := blocks[1].Hook.Template, filepath.Join(configDir, "templates", "go.sh"); got != want {
		t.Fatalf("overridden Go template = %q, want %q", got, want)
	}
}

func TestProfileSelectionCanOverrideEarlierLayerAndDisableWholeHook(t *testing.T) {
	repo := initRepo(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	mustWrite(t, filepath.Join(repo, "package.json"), "{}\n")
	globalDir := filepath.Join(configHome, "wb")
	mustMkdirAll(t, globalDir)
	mustWrite(t, filepath.Join(globalDir, "hooks.yaml"), "version: 1\nprofiles:\n  auto: true\n  exclude: [node]\n")
	repoConfigDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, repoConfigDir)
	mustWrite(t, filepath.Join(repoConfigDir, "hooks.yaml"), "version: 1\nprofiles:\n  include: [node]\nhooks:\n  pre-push:\n    disabled: true\n")

	policy, err := LoadPolicy(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.ActiveProfiles) != 1 || policy.ActiveProfiles[0].Name != "node" || policy.ActiveProfiles[0].Reason != "included by policy" {
		t.Fatalf("active profiles = %#v", policy.ActiveProfiles)
	}
	if blocks := hookBlocks(policy, "pre-push"); len(blocks) != 0 {
		t.Fatalf("disabled pre-push should suppress every block: %#v", blocks)
	}
}

func TestLoadPolicyRejectsInvalidProfileDefinitions(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	config := filepath.Join(t.TempDir(), "hooks.yaml")
	for _, content := range []string{
		"version: 1\nprofiles:\n  include: [missing]\n",
		"version: 1\nprofiles:\n  auto: true\n  definitions:\n    unsafe:\n      detect:\n        all_files: [../outside]\n",
	} {
		mustWrite(t, config, content)
		if _, err := LoadPolicy(repo, config); err == nil {
			t.Fatalf("expected profile validation error for:\n%s", content)
		}
	}
}

func TestApplyCheckAndRepairManagedHooks(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb test")
	projectsRoot := "/tmp/projects root"
	result, err := Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, ProjectsRoot: projectsRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 0 {
		t.Fatalf("install findings = %#v", result.Report.Findings)
	}
	wantHooks := []string{"post-commit", "pre-commit", "pre-push"}
	if !reflect.DeepEqual(result.Report.Hooks, wantHooks) {
		t.Fatalf("installed hooks = %v, want %v", result.Report.Hooks, wantHooks)
	}
	configured := git(t, repo, "config", "--local", "--get", "core.hooksPath")
	if configured != result.Report.ManagedPath {
		t.Fatalf("core.hooksPath = %q, want %q", configured, result.Report.ManagedPath)
	}
	preCommit := filepath.Join(result.Report.ManagedPath, "pre-commit")
	data, err := os.ReadFile(preCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), managedStartMarker) || !strings.Contains(string(data), managedEndMarker) ||
		!strings.Contains(string(data), shellQuote(executable)+" --projects-root '/tmp/projects root' hooks run 'pre-commit' -- \"$@\"") ||
		strings.Contains(string(data), "exec ") {
		t.Fatalf("unexpected shim:\n%s", data)
	}
	info, _ := os.Stat(preCommit)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("pre-commit shim is not executable")
	}
	syntax := exec.Command("/bin/sh", "-n", preCommit)
	if output, syntaxErr := syntax.CombinedOutput(); syntaxErr != nil {
		t.Fatalf("generated hook syntax: %v\n%s", syntaxErr, output)
	}

	mustWrite(t, preCommit, strings.Replace(string(data), executable, "/old/wb", 1))
	stale := filepath.Join(result.Report.ManagedPath, "commit-msg")
	mustWrite(t, stale, shimContent(executable, "commit-msg", "", projectsRoot, "", false))
	report, err := Check(repo, "", executable, projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(report.Findings, "hook-stale") || !hasFinding(report.Findings, "hook-unexpected") {
		t.Fatalf("drift findings = %#v", report.Findings)
	}
	repaired, err := Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, ProjectsRoot: projectsRoot, Repair: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired.Report.Findings) != 0 {
		t.Fatalf("repair findings = %#v", repaired.Report.Findings)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale managed hook still exists: %v", err)
	}
}

func TestApplyEmbedsAbsoluteProjectsRootInManagedHooks(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	relativeRoot := filepath.Join(".", "projects root")
	absoluteRoot, err := filepath.Abs(relativeRoot)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: executable, ProjectsRoot: relativeRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	preCommit, err := os.ReadFile(filepath.Join(result.Report.ManagedPath, "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	expected := shellQuote(executable) + " --projects-root " + shellQuote(absoluteRoot) + " hooks run 'pre-commit'"
	if !strings.Contains(string(preCommit), expected) {
		t.Fatalf("managed hook does not preserve the absolute projects root %q:\n%s", absoluteRoot, preCommit)
	}
	if report, checkErr := Check(repo, "", executable, relativeRoot); checkErr != nil || len(report.Findings) != 0 {
		t.Fatalf("relative projects root should check cleanly: report = %#v, error = %v", report, checkErr)
	}
}

func TestRepairPreservesUserSectionsOutsideManagedDelimiter(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	oldExecutable := testWBExecutable(t, "old-wb")
	newExecutable := testWBExecutable(t, "new-wb")
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: oldExecutable})
	if err != nil {
		t.Fatal(err)
	}
	prePush := filepath.Join(result.Report.ManagedPath, "pre-push")
	installed, err := os.ReadFile(prePush)
	if err != nil {
		t.Fatal(err)
	}
	withUserSections := strings.Replace(string(installed), "#!/bin/sh\nset -eu\n\n", "#!/bin/sh\nset -eu\necho before\n", 1) + "echo after\n"
	mustWrite(t, prePush, withUserSections)
	if report, checkErr := Check(repo, "", oldExecutable, ""); checkErr != nil || len(report.Findings) != 0 {
		t.Fatalf("user sections should not cause drift: report = %#v, error = %v", report, checkErr)
	}

	repaired, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: newExecutable, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired.Report.Findings) != 0 {
		t.Fatalf("repair findings = %#v", repaired.Report.Findings)
	}
	prePushData, err := os.ReadFile(prePush)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"set -eu", "echo before", "echo after", shellQuote(newExecutable) + " hooks run 'pre-push'"} {
		if !strings.Contains(string(prePushData), wanted) {
			t.Fatalf("repaired pre-push lost %q:\n%s", wanted, prePushData)
		}
	}
	if strings.Contains(string(prePushData), oldExecutable) {
		t.Fatalf("old managed dispatcher remains:\n%s", prePushData)
	}
}

func TestRepairRemovingHookPreservesOuterUserCommands(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "commit-msg.sh"), "#!/bin/sh\nexit 0\n")
	configPath := filepath.Join(configDir, "hooks.yaml")
	mustWrite(t, configPath, "version: 1\nhooks:\n  commit-msg:\n    template: commit-msg.sh\nmetrics:\n  enabled: false\n")
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	commitMsg := filepath.Join(result.Report.ManagedPath, "commit-msg")
	data, err := os.ReadFile(commitMsg)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, commitMsg, string(data)+"echo keep-user-command\n")
	mustWrite(t, configPath, "version: 1\nhooks:\n  commit-msg:\n    disabled: true\nmetrics:\n  enabled: false\n")

	repaired, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(repaired.Report.Findings) != 0 {
		t.Fatalf("repair findings = %#v", repaired.Report.Findings)
	}
	preserved, err := os.ReadFile(commitMsg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(preserved), "echo keep-user-command") || isManagedContent(string(preserved)) {
		t.Fatalf("user-only hook was not preserved correctly:\n%s", preserved)
	}
}

func TestApplyProtectsConflictingHooksAndForceBacksUp(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	legacy := filepath.Join(repo, ".git-hooks")
	mustMkdirAll(t, legacy)
	mustWrite(t, filepath.Join(legacy, "pre-commit"), "#!/bin/sh\necho legacy\n")
	git(t, repo, "config", "--local", "core.hooksPath", ".git-hooks")

	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err == nil || !strings.Contains(err.Error(), "migrate those hooks") {
		t.Fatalf("conflict error = %v", err)
	}
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Repair: true, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "config", "--local", "--get", "core.hooksPath"); got != result.Report.ManagedPath {
		t.Fatalf("forced core.hooksPath = %q, want %q", got, result.Report.ManagedPath)
	}
	if _, err := os.Stat(filepath.Join(legacy, "pre-commit")); err != nil {
		t.Fatalf("legacy hook should be preserved: %v", err)
	}

	unmanaged := filepath.Join(result.Report.ManagedPath, "pre-commit")
	mustWrite(t, unmanaged, "#!/bin/sh\necho unmanaged\n")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Repair: true}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged hook") {
		t.Fatalf("unmanaged collision error = %v", err)
	}
	now := time.Date(2026, 7, 20, 12, 34, 56, 0, time.UTC)
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Repair: true, Force: true, Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	backup := unmanaged + ".wb-backup-20260720T123456Z"
	if data, err := os.ReadFile(backup); err != nil || !strings.Contains(string(data), "echo unmanaged") {
		t.Fatalf("unmanaged hook backup = %q, error = %v", data, err)
	}
}

func TestRunCustomTemplatePassesContextAndRecordsEvents(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	git(t, repo, "remote", "add", "origin", "git@github.com:acme/widgets.git")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, filepath.Join(configDir, "templates"))
	template := filepath.Join(configDir, "templates", "pre-commit.sh")
	mustWrite(t, template, `#!/bin/sh
printf '%s|%s|%s|%s|%s|%s\n' "$WB_HOOK" "$1" "$WB_REPO_SLUG" "$WB_BRANCH" "$WB_PROFILE" "$WB_BLOCK"
cat
`)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
hooks:
  pre-commit:
    template: templates/pre-commit.sh
metrics:
  enabled: true
  path: metrics/events.jsonl
  labels:
    developer: dev-17
    machine: laptop-a
`)
	var stdout bytes.Buffer
	result, err := Run(RunOptions{
		RepoPath: repo,
		Hook:     "pre-commit",
		Args:     []string{"hello"},
		Stdin:    strings.NewReader("payload\n"),
		Stdout:   &stdout,
		Stderr:   &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.MetricsError != nil {
		t.Fatalf("run result = %#v", result)
	}
	if got := stdout.String(); !strings.Contains(got, "pre-commit|hello|acme/widgets|main|base|base/pre-commit\npayload") {
		t.Fatalf("template output = %q", got)
	}
	events, err := ReadEvents(filepath.Join(configDir, "metrics", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Action != "hook-block" || events[0].Block != "base/pre-commit" || events[1].Action != "commit-check" || events[1].Outcome != "passed" || events[1].Repository != "acme/widgets" {
		t.Fatalf("events = %#v", events)
	}
	if events[1].OS == "" || events[1].Arch == "" || events[1].Labels["developer"] != "dev-17" || events[1].Labels["machine"] != "laptop-a" {
		t.Fatalf("event environment = %#v", events[1])
	}
}

func TestRunComposedProfilesInOrderAndReplicatesPrePushInput(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	templatesDir := filepath.Join(configDir, "templates")
	mustMkdirAll(t, templatesDir)
	for _, name := range []string{"base", "language", "product"} {
		mustWrite(t, filepath.Join(templatesDir, name+".sh"), "#!/bin/sh\nprintf '%s:' \"$WB_BLOCK\"\ncat\n")
	}
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
hooks:
  pre-push:
    template: templates/base.sh
profiles:
  include: [language, product]
  definitions:
    language:
      order: 100
      hooks:
        pre-push:
          template: templates/language.sh
    product:
      order: 200
      hooks:
        pre-push:
          template: templates/product.sh
metrics:
  enabled: true
  path: events.jsonl
`)
	var stdout bytes.Buffer
	result, err := Run(RunOptions{
		RepoPath: repo,
		Hook:     "pre-push",
		Stdin:    strings.NewReader("refs\n"),
		Stdout:   &stdout,
		Stderr:   &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "base/pre-push:refs\nlanguage/pre-push:refs\nproduct/pre-push:refs\n"; got != want {
		t.Fatalf("composed output = %q, want %q", got, want)
	}
	if len(result.Blocks) != 3 || result.Blocks[0].ID != "base/pre-push" || result.Blocks[1].ID != "language/pre-push" || result.Blocks[2].ID != "product/pre-push" {
		t.Fatalf("block results = %#v", result.Blocks)
	}
	events, readErr := ReadEvents(filepath.Join(configDir, "events.jsonl"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 4 || events[0].Block != "base/pre-push" || events[1].Block != "language/pre-push" || events[2].Block != "product/pre-push" || events[3].Action != "push-attempt" {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunStopsAfterFirstFailingProfileBlock(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "fail.sh"), "#!/bin/sh\nexit 9\n")
	mustWrite(t, filepath.Join(configDir, "never.sh"), "#!/bin/sh\necho should-not-run\n")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), `version: 1
profiles:
  include: [fail, never]
  definitions:
    fail:
      order: 100
      hooks:
        pre-push:
          template: fail.sh
    never:
      order: 200
      hooks:
        pre-push:
          template: never.sh
metrics:
  enabled: false
`)
	var stdout bytes.Buffer
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}})
	if err == nil || result.ExitCode != 9 || len(result.Blocks) != 2 || result.Blocks[1].ID != "fail/pre-push" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if strings.Contains(stdout.String(), "should-not-run") {
		t.Fatalf("later block ran after failure: %q", stdout.String())
	}
}

func TestBuiltInGoPreCommitChecksOnlyStagedGoFiles(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	mustWrite(t, filepath.Join(repo, "go.mod"), "module example.invalid/hooks-test\n\ngo 1.26\n")
	goFile := filepath.Join(repo, "main.go")
	mustWrite(t, goFile, "package main\nfunc main(){ }\n")
	git(t, repo, "add", "go.mod", "main.go")
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nprofiles:\n  auto: true\nmetrics:\n  enabled: false\n")

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || result.ExitCode == 0 || len(result.Blocks) != 2 || result.Blocks[1].ID != "go/pre-commit" {
		t.Fatalf("unformatted result = %#v, error = %v", result, err)
	}
	command := exec.Command("gofmt", "-w", goFile)
	if output, formatErr := command.CombinedOutput(); formatErr != nil {
		t.Fatalf("gofmt: %v\n%s", formatErr, output)
	}
	git(t, repo, "add", "main.go")
	result, err = Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("formatted result = %#v, error = %v", result, err)
	}
}

func TestRunMetricsFailureNeverBlocksSuccessfulHook(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	blockedParent := filepath.Join(repo, "blocked")
	mustWrite(t, blockedParent, "not a directory\n")
	config := filepath.Join(repo, ".wb", "hooks.yaml")
	mustMkdirAll(t, filepath.Dir(config))
	mustWrite(t, config, "version: 1\nmetrics:\n  enabled: true\n  path: ../blocked/events.jsonl\n")

	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-commit", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("successful hook was blocked: result = %#v, error = %v", result, err)
	}
	if result.MetricsError == nil {
		t.Fatal("expected a non-blocking metrics error")
	}
}

func TestRunFailurePreservesExitCodeAndRecordsFailure(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	configDir := filepath.Join(repo, ".wb")
	mustMkdirAll(t, configDir)
	template := filepath.Join(configDir, "fail.sh")
	mustWrite(t, template, "#!/bin/sh\nexit 7\n")
	metricsPath := filepath.Join(configDir, "events.jsonl")
	mustWrite(t, filepath.Join(configDir, "hooks.yaml"), "version: 1\nhooks:\n  pre-push:\n    template: fail.sh\nmetrics:\n  enabled: true\n  path: events.jsonl\n")
	result, err := Run(RunOptions{RepoPath: repo, Hook: "pre-push", Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || result.ExitCode != 7 {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	events, readErr := ReadEvents(metricsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 2 || events[0].Action != "hook-block" || events[0].Outcome != "failed" || events[1].Action != "push-attempt" || events[1].Outcome != "failed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestAppendReadAndSummarizeMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	zone := time.FixedZone("test", 2*60*60)
	now := time.Date(2026, 7, 20, 18, 0, 0, 0, zone)
	events := []Event{
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 19, 10, 0, 0, 0, zone), Repository: "acme/widgets", Action: "commit", Outcome: "passed", DurationMS: 10},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 10, 0, 0, 0, zone), Repository: "acme/widgets", Action: "push-attempt", Outcome: "passed", DurationMS: 30},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 11, 0, 0, 0, zone), Repository: "acme/widgets", Action: "commit-check", Outcome: "failed", DurationMS: 20},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 11, 0, 0, 0, zone), Repository: "acme/widgets", Hook: "pre-push", Profile: "go", Block: "go/pre-push", Action: "hook-block", Outcome: "passed", DurationMS: 42},
		{SchemaVersion: 1, Timestamp: time.Date(2026, 7, 20, 12, 0, 0, 0, zone), Repository: "other/repo", Action: "commit", Outcome: "passed", DurationMS: 999},
	}
	for _, event := range events {
		if err := AppendEvent(path, event); err != nil {
			t.Fatal(err)
		}
	}
	read, err := ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != len(events) {
		t.Fatalf("read %d events, want %d", len(read), len(events))
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metrics permissions = %v, want 0600", info.Mode().Perm())
	}
	summary := Summarize(read, 3, "widgets", now)
	if summary.Commits != 1 || summary.PushAttempts != 1 || summary.CommitChecks != 1 || summary.HookFailures != 1 || summary.HookRuns != 3 || summary.AverageDurationMS != 20 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.Days) != 3 || summary.Days[1].Commits != 1 || summary.Days[2].PushAttempts != 1 {
		t.Fatalf("daily summary = %#v", summary.Days)
	}
	if len(summary.Blocks) != 1 || summary.Blocks[0].ID != "go/pre-push" || summary.Blocks[0].Runs != 1 || summary.Blocks[0].AverageDurationMS != 42 {
		t.Fatalf("block summary = %#v", summary.Blocks)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	var first Event
	if err := decoder.Decode(&first); err != nil || first.SchemaVersion != EventSchemaVersion {
		t.Fatalf("first event = %#v, error = %v", first, err)
	}
}

func TestReadEventsRejectsUnsupportedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	mustWrite(t, path, `{"schema_version":99,"timestamp":"2026-07-20T00:00:00Z"}`+"\n")
	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "schema version 99") {
		t.Fatalf("schema error = %v", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(repo); err == nil {
		repo = resolved
	}
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "WB Tests")
	git(t, repo, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(repo, "README.md"), "test\n")
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceTestFile(t *testing.T, path, content string) {
	t.Helper()
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hook-replacement-*")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func testWBExecutable(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	mustWriteExecutable(t, path, "#!/bin/sh\nexit 0\n")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func hasFinding(findings []Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
