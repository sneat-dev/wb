package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// hkCovWithoutUsableCwd runs fn from a working directory that has been removed.
// A deleted working directory makes filepath.Abs fail only where the kernel's
// getcwd refuses to report an unlinked directory (Linux); macOS still reports
// the deleted path, so the second result says whether the platform actually
// removed the working directory out from under the process.
func hkCovWithoutUsableCwd(t *testing.T, fn func() error) (error, bool) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(original) }()
	_, getwdErr := os.Getwd()
	return fn(), getwdErr != nil
}

func hkCovInstall(t *testing.T, repo, executable string) ApplyResult {
	t.Helper()
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func hkCovManagedDir(t *testing.T, repo string) string {
	t.Helper()
	managed, err := managedPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	return managed
}

func hkCovSwapRepo(t *testing.T, repo string) func() {
	t.Helper()
	moved := filepath.Join(t.TempDir(), "moved-repo")
	return func() {
		if err := os.Rename(repo, moved); err != nil {
			t.Fatalf("move repository during hook regression: %v", err)
		}
		mustMkdirAll(t, repo)
	}
}

func TestHkCovManagedPathRejectsNonRepository(t *testing.T) {
	t.Parallel()
	if _, err := managedPath(t.TempDir()); err == nil {
		t.Fatal("managedPath(non-repo) should fail")
	}
}

func TestHkCovShimManagedSectionEmbedsConfigWithoutLegacyMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// wbHomeAllowsLegacy is false in production now: resolvedWBHome no longer
	// reports a legacy <projects-root>/.wb read layout to be compatible with,
	// so a managed shim must not pin WB_HOME_MIGRATION_COMPAT. WB_HOME itself
	// is retired but a shim still pins it until the fleet migration regenerates
	// the installed shims (plan task 8); the retirement diagnostic suppresses
	// the machine hook-execution path, so the pin is not noisy in the meantime.
	section := shimManagedSection("", "pre-commit", "~/hooks.yaml", "/projects root", filepath.Join(home, ".wb"), false)
	if !strings.Contains(section, "--projects-root '/projects root'") {
		t.Fatalf("section = %q, want the projects root embedded", section)
	}
	if !strings.Contains(section, "--config '"+filepath.Join(home, "hooks.yaml")+"'") {
		t.Fatalf("section = %q, want the expanded explicit config path embedded", section)
	}
	if !strings.Contains(section, "export WB_HOME='"+filepath.Join(home, ".wb")+"'") {
		t.Fatalf("section = %q, want WB_HOME pinned", section)
	}
	if strings.Contains(section, "export "+wbhome.EnvMigrationCompat+"=") {
		t.Fatalf("section = %q, want no legacy migration marker pinned", section)
	}
	plain := shimManagedSection("", "pre-commit", "", "", "", false)
	if strings.Contains(plain, "WB_HOME") || strings.Contains(plain, "--config") || strings.Contains(plain, "--projects-root") {
		t.Fatalf("plain section = %q, want no optional exports", plain)
	}
}

func TestHkCovAbsoluteProjectsRootRejectsUnresolvablePath(t *testing.T) {
	err, platformSupportsIt := hkCovWithoutUsableCwd(t, func() error {
		_, err := absoluteProjectsRoot("relative/projects")
		return err
	})
	if !platformSupportsIt {
		if err != nil {
			t.Fatalf("absoluteProjectsRoot error = %v, want nil while getcwd still works", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), "resolve projects root") {
		t.Fatalf("absoluteProjectsRoot error = %v", err)
	}
}

func TestHkCovCheckErrorPaths(t *testing.T) {
	executable := testWBExecutable(t, "wb")

	if _, err := Check(t.TempDir(), "", executable, ""); err == nil {
		t.Fatal("Check(non-repo) should fail")
	}

	repo := initRepo(t)
	isolateConfig(t)
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	// The projects root selects the state home now, so the unusable root is
	// passed where the API accepts one instead of through WB_HOME.
	if _, err := Check(repo, "", executable, filepath.Join(blocker, "projects")); err == nil {
		t.Fatal("Check with an unusable projects root should fail")
	}

	isolateConfig(t)
	err, platformSupportsIt := hkCovWithoutUsableCwd(t, func() error {
		_, err := Check(repo, "", executable, "relative/projects")
		return err
	})
	if platformSupportsIt {
		if err == nil || !strings.Contains(err.Error(), "resolve projects root") {
			t.Fatalf("Check with an unresolvable projects root error = %v", err)
		}
	}

	// A symlinked Git common directory makes managedPath refuse to name a
	// managed location at all.
	isolateConfig(t)
	symlinked := initRepo(t)
	external := filepath.Join(t.TempDir(), "external-git")
	gitDir := filepath.Join(symlinked, ".git")
	if err := os.Rename(gitDir, external); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, gitDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(symlinked, "", executable, ""); err == nil || !strings.Contains(err.Error(), "symlinked Git common directory") {
		t.Fatalf("Check with a symlinked Git common directory error = %v", err)
	}
}

func TestHkCovCheckReportsMissingHooksPathAndHooks(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	managed := hkCovManagedDir(t, repo)
	mustMkdirAll(t, managed)

	report, err := Check(repo, "", executable, "")
	if err != nil {
		t.Fatal(err)
	}
	foundPath := false
	for _, finding := range report.Findings {
		if finding.Code == "hooks-path" && finding.Message == "core.hooksPath is not configured" {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("findings = %#v, want a hooks-path finding", report.Findings)
	}
	for _, name := range report.Hooks {
		if !hasFinding(report.Findings, "hook-missing") {
			t.Fatalf("findings = %#v, want hook-missing entries for %v", report.Findings, report.Hooks)
		}
		_ = name
	}

	// A hooksPath pointing somewhere else names that path in the finding.
	other := t.TempDir()
	git(t, repo, "config", "--local", "core.hooksPath", other)
	report, err = Check(repo, "", executable, "")
	if err != nil {
		t.Fatal(err)
	}
	foundOther := false
	for _, finding := range report.Findings {
		if finding.Code == "hooks-path" && strings.Contains(finding.Message, other) {
			foundOther = true
		}
	}
	if !foundOther {
		t.Fatalf("findings = %#v, want the configured hooks path named", report.Findings)
	}
}

func TestHkCovCheckReportsNonExecutableHook(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed := hkCovInstall(t, repo, executable)
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	if err := os.Chmod(preCommit, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Check(repo, "", executable, "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasFinding(report.Findings, "hook-not-executable") {
		t.Fatalf("findings = %#v, want hook-not-executable", report.Findings)
	}
}

func TestHkCovApplyRejectsMissingExecutableAndBadInputs(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	if _, err := Apply(ApplyOptions{RepoPath: repo}); err == nil || !strings.Contains(err.Error(), "without a WB executable") {
		t.Fatalf("Apply without an executable error = %v", err)
	}
	if _, err := Apply(ApplyOptions{RepoPath: t.TempDir(), WBExecutable: testWBExecutable(t, "wb")}); err == nil {
		t.Fatal("Apply(non-repo) should fail")
	}

	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	// The projects root selects the state home now, so the unusable root is
	// passed where the API accepts one instead of through WB_HOME.
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: testWBExecutable(t, "wb"), ProjectsRoot: filepath.Join(blocker, "projects")}); err == nil {
		t.Fatal("Apply with an unusable projects root should fail")
	}

	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	err, platformSupportsIt := hkCovWithoutUsableCwd(t, func() error {
		_, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, ProjectsRoot: "relative/projects"})
		return err
	})
	if platformSupportsIt {
		if err == nil || !strings.Contains(err.Error(), "resolve projects root") {
			t.Fatalf("Apply with an unresolvable projects root error = %v", err)
		}
	}

	if _, err := normalizedWBLauncher("   "); err == nil || !strings.Contains(err.Error(), "without a WB executable") {
		t.Fatalf("normalizedWBLauncher(blank) error = %v", err)
	}
	err, platformSupportsIt = hkCovWithoutUsableCwd(t, func() error {
		_, err := normalizedWBLauncher("relative-wb")
		return err
	})
	if platformSupportsIt && (err == nil || !strings.Contains(err.Error(), "resolve WB executable")) {
		t.Fatalf("normalizedWBLauncher with no cwd error = %v", err)
	}
}

func TestHkCovApplyReportsDefaultHookDirectoryProblems(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	gitDir := filepath.Join(repo, ".git")

	// Git's default hooks path being a regular file is an unreadable state.
	if err := os.RemoveAll(filepath.Join(gitDir, "hooks")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(gitDir, "hooks"), "not a directory\n")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err == nil {
		t.Fatal("Apply with an unreadable default hooks directory should fail")
	}

	// A real active hook in the default directory blocks installation unless
	// forced.
	other := initRepo(t)
	isolateConfig(t)
	mustMkdirAll(t, filepath.Join(other, ".git", "hooks"))
	mustWriteExecutable(t, filepath.Join(other, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexit 0\n")
	if _, err := Apply(ApplyOptions{RepoPath: other, WBExecutable: executable}); err == nil || !strings.Contains(err.Error(), "active hooks already exist") {
		t.Fatalf("Apply over an active default hook error = %v", err)
	}
	result, err := Apply(ApplyOptions{RepoPath: other, WBExecutable: executable, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Report.Findings) != 0 {
		t.Fatalf("forced install findings = %#v", result.Report.Findings)
	}
}

func TestHkCovApplyRefusesUnmanagedHookWithoutForce(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	managed := hkCovManagedDir(t, repo)
	mustMkdirAll(t, managed)
	mustWriteExecutable(t, filepath.Join(managed, "pre-commit"), "#!/bin/sh\necho user\n")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged hook") {
		t.Fatalf("Apply over an unmanaged hook error = %v", err)
	}
	result, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	backedUp := false
	for _, action := range result.Actions {
		if strings.Contains(action, "backed up") {
			backedUp = true
		}
	}
	if !backedUp {
		t.Fatalf("actions = %v, want a backup action", result.Actions)
	}
}

func TestHkCovApplyRejectsOutOfOrderManagedMarkers(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed := hkCovInstall(t, repo, executable)
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	mustWrite(t, preCommit, "#!/bin/sh\n"+managedEndMarker+"\n")
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err == nil || !strings.Contains(err.Error(), "markers are incomplete or out of order") {
		t.Fatalf("Apply over out-of-order markers error = %v", err)
	}
}

func TestHkCovApplyReportsSymlinkedManagedHook(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	installed := hkCovInstall(t, repo, executable)
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	if err := os.Remove(preCommit); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	mustWrite(t, target, "#!/bin/sh\necho target\n")
	if err := os.Symlink(target, preCommit); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ApplyOptions{RepoPath: repo, WBExecutable: executable}); err == nil {
		t.Fatal("Apply over a symlinked managed hook should fail")
	}
	if content := mustReadFile(t, target); content != "#!/bin/sh\necho target\n" {
		t.Fatalf("symlink target was mutated: %q", content)
	}
}

// TestHkCovApplySeamFailures drives every test-only seam Apply exposes, so each
// late-swap refusal is asserted rather than assumed.
func TestHkCovApplySeamFailures(t *testing.T) {
	t.Parallel()
	t.Run("managed hook swapped mid loop", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		executable := testWBExecutable(t, "wb")
		hkCovInstall(t, repo, executable)
		swap := hkCovSwapRepo(t, repo)
		_, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: executable,
			afterManagedHookRead: func(name string) {
				if name == "post-commit" {
					swap()
				}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
			t.Fatalf("mid-loop swap error = %v", err)
		}
	})

	t.Run("repair validate fails after the loop", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		executable := testWBExecutable(t, "wb")
		hkCovInstall(t, repo, executable)
		swap := hkCovSwapRepo(t, repo)
		_, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: executable, Repair: true,
			afterManagedHookRead: func(name string) {
				if name == "pre-push" {
					swap()
				}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
			t.Fatalf("repair swap error = %v", err)
		}
	})

	t.Run("hooks path validate fails after the loop", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		executable := testWBExecutable(t, "wb")
		hkCovInstall(t, repo, executable)
		// Unconfigure core.hooksPath so Apply still has to configure it, which
		// is the branch that revalidates the directory after the loop.
		git(t, repo, "config", "--local", "--unset", "core.hooksPath")
		swap := hkCovSwapRepo(t, repo)
		_, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: executable,
			afterManagedHookRead: func(name string) {
				if name == "pre-push" {
					swap()
				}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
			t.Fatalf("post-loop swap error = %v", err)
		}
	})

	t.Run("before hooks path configuration runs", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		called := false
		result, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: testWBExecutable(t, "wb"),
			beforeHooksPathConfiguration: func() { called = true },
		})
		if err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("beforeHooksPathConfiguration was not invoked")
		}
		if configured := git(t, repo, "config", "--local", "--get", "core.hooksPath"); configured != result.Report.ManagedPath {
			t.Fatalf("core.hooksPath = %q, want %q", configured, result.Report.ManagedPath)
		}
	})

	t.Run("repository swapped before hooks path configuration", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		swap := hkCovSwapRepo(t, repo)
		_, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: testWBExecutable(t, "wb"),
			beforeHooksPathConfiguration: swap,
		})
		if err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
			t.Fatalf("pre-configuration swap error = %v", err)
		}
	})

	t.Run("git becomes unavailable before hooks path configuration", func(t *testing.T) {
		repo := initRepo(t)
		isolateConfig(t)
		_, err := Apply(ApplyOptions{
			RepoPath: repo, WBExecutable: testWBExecutable(t, "wb"),
			beforeHooksPathConfiguration: func() { t.Setenv("PATH", "") },
		})
		if err == nil || !strings.Contains(err.Error(), "locate Git") {
			t.Fatalf("missing git error = %v", err)
		}
	})

	t.Run("config removed before the final check", func(t *testing.T) {
		repo := initRepo(t)
		isolateEnvironment(t)
		config := hkCovRepoConfig(t, repo, "version: 1\n")
		_, err := Apply(ApplyOptions{
			RepoPath: repo, ConfigPath: config, WBExecutable: testWBExecutable(t, "wb"),
			afterHooksPathConfigurationAuthorization: func() {
				if removeErr := os.Remove(config); removeErr != nil {
					t.Fatalf("remove config during regression: %v", removeErr)
				}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "read hooks config") {
			t.Fatalf("removed config error = %v", err)
		}
	})

}

// TestHkCovApplyCorruptionIsSeenByTheFinalCheck prepares the managed directory
// first so the authorization seam knows exactly which hook to corrupt.
func TestHkCovApplyCorruptionIsSeenByTheFinalCheck(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	managed := hkCovManagedDir(t, repo)
	_, err := Apply(ApplyOptions{
		RepoPath: repo, WBExecutable: testWBExecutable(t, "wb"),
		afterHooksPathConfigurationAuthorization: func() {
			mustWrite(t, filepath.Join(managed, "pre-commit"), "#!/bin/sh\necho corrupted\n")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "managed hooks remain unhealthy") {
		t.Fatalf("corrupted hook error = %v", err)
	}
	if !strings.Contains(mustReadFile(t, filepath.Join(managed, "pre-commit")), "corrupted") {
		t.Fatal("the corrupted hook should be left in place for the operator to inspect")
	}
}

func TestHkCovWriteExecutableErrors(t *testing.T) {
	t.Parallel()
	missingDir := filepath.Join(t.TempDir(), "missing", "hooks")
	if err := writeExecutable(filepath.Join(missingDir, "pre-commit"), []byte("#!/bin/sh\n")); err == nil {
		t.Fatal("writeExecutable into a missing directory should fail")
	}

	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	mustWrite(t, target, "#!/bin/sh\necho target\n")
	link := filepath.Join(dir, "pre-commit")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutable(link, []byte("#!/bin/sh\n")); err == nil || !strings.Contains(err.Error(), "symlinked managed hook") {
		t.Fatalf("writeExecutable over a symlink error = %v", err)
	}

	good := filepath.Join(t.TempDir(), "pre-commit")
	if err := writeExecutable(good, []byte("#!/bin/sh\necho installed\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(good)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 || mustReadFile(t, good) != "#!/bin/sh\necho installed\n" {
		t.Fatalf("installed hook = %v, %q", info.Mode(), mustReadFile(t, good))
	}
}

func TestHkCovManagedHookIdentityAt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	for _, name := range []string{"", ".", "sub/hook"} {
		if _, err := managedHookIdentityAt(handle, name); err == nil || !strings.Contains(err.Error(), "invalid managed hook name") {
			t.Fatalf("managedHookIdentityAt(%q) error = %v", name, err)
		}
	}
	identity, err := managedHookIdentityAt(handle, "missing")
	if err != nil || identity.exists {
		t.Fatalf("managedHookIdentityAt(missing) = %#v, %v; want an absent identity", identity, err)
	}

	target := filepath.Join(t.TempDir(), "target")
	mustWrite(t, target, "#!/bin/sh\n")
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := managedHookIdentityAt(handle, "link"); err == nil || !strings.Contains(err.Error(), "symlinked managed hook") {
		t.Fatalf("managedHookIdentityAt(symlink) error = %v", err)
	}

	mustMkdirAll(t, filepath.Join(dir, "subdir"))
	if _, err := managedHookIdentityAt(handle, "subdir"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("managedHookIdentityAt(directory) error = %v", err)
	}

	mustWrite(t, filepath.Join(dir, "pre-commit"), "#!/bin/sh\n")
	identity, err = managedHookIdentityAt(handle, "pre-commit")
	if err != nil || !identity.exists || identity.inode == 0 {
		t.Fatalf("managedHookIdentityAt(file) = %#v, %v", identity, err)
	}
}

func TestHkCovVerifyManagedHookIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	other := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	broken := managedHooksDirectory{path: dir, commonPath: other, common: handle, directory: handle}
	if err := verifyManagedHookIdentity(broken, "pre-commit", absentManagedHookIdentity()); err == nil || !strings.Contains(err.Error(), "git common directory path changed") {
		t.Fatalf("verifyManagedHookIdentity(swapped path) error = %v", err)
	}

	// A directory descriptor that is not a directory fails with ENOTDIR
	// rather than being reported as an absent hook.
	regular := filepath.Join(t.TempDir(), "regular")
	mustWrite(t, regular, "plain\n")
	regularHandle, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularHandle.Close() }()
	if identity, err := managedHookIdentityAt(regularHandle, "pre-commit"); err == nil || identity.exists {
		t.Fatalf("managedHookIdentityAt(file descriptor) = %#v, %v; want an error", identity, err)
	}

	healthy := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}
	if err := verifyManagedHookIdentity(healthy, "sub/hook", absentManagedHookIdentity()); err == nil || !strings.Contains(err.Error(), "invalid managed hook name") {
		t.Fatalf("verifyManagedHookIdentity(bad name) error = %v", err)
	}
	if err := verifyManagedHookIdentity(healthy, "missing", absentManagedHookIdentity()); err != nil {
		t.Fatalf("verifyManagedHookIdentity(absent) = %v, want nil", err)
	}
}

func TestHkCovManagedHooksDirectoryValidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	other := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	if err := (managedHooksDirectory{repoPath: other, repo: handle, commonPath: dir, common: handle}).validate(); err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
		t.Fatalf("validate(repo swap) error = %v", err)
	}
	if err := (managedHooksDirectory{commonPath: other, common: handle}).validate(); err == nil || !strings.Contains(err.Error(), "git common directory path changed") {
		t.Fatalf("validate(common swap) error = %v", err)
	}
	if err := (managedHooksDirectory{path: other, commonPath: dir, common: handle, directory: handle}).validate(); err == nil || !strings.Contains(err.Error(), "managed hooks directory path changed") {
		t.Fatalf("validate(directory swap) error = %v", err)
	}
	if err := (managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}).validate(); err != nil {
		t.Fatalf("validate(consistent) = %v, want nil", err)
	}
	if managedDirectoryPathMatches(dir, nil) {
		t.Fatal("managedDirectoryPathMatches with a nil descriptor should be false")
	}
}

func TestHkCovInspectHooksDirectoryPath(t *testing.T) {
	t.Parallel()
	if _, err := inspectHooksDirectoryPath(filepath.Join(t.TempDir(), "missing"), "test directory"); err == nil || !strings.Contains(err.Error(), "inspect test directory") {
		t.Fatalf("inspectHooksDirectoryPath(missing) error = %v", err)
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "file"), "plain\n")
	if _, err := inspectHooksDirectoryPath(filepath.Join(dir, "file"), "test directory"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("inspectHooksDirectoryPath(file) error = %v", err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectHooksDirectoryPath(filepath.Join(dir, "link"), "test directory"); err == nil || !strings.Contains(err.Error(), "refusing symlinked test directory") {
		t.Fatalf("inspectHooksDirectoryPath(symlink) error = %v", err)
	}
	if _, err := inspectHooksDirectoryPath(dir, "test directory"); err != nil {
		t.Fatalf("inspectHooksDirectoryPath(dir) = %v, want nil", err)
	}
}

func TestHkCovOpenAbsoluteHooksDirectoryNoFollow(t *testing.T) {
	t.Parallel()
	if _, err := openAbsoluteHooksDirectoryNoFollow("relative/path"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("openAbsoluteHooksDirectoryNoFollow(relative) error = %v", err)
	}
	root, err := openAbsoluteHooksDirectoryNoFollow(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()

	dir := t.TempDir()
	handle, err := openAbsoluteHooksDirectoryNoFollow(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	info, err := handle.Stat()
	if err != nil || !info.IsDir() {
		t.Fatalf("opened descriptor = %#v, %v; want the directory", info, err)
	}
}

func TestHkCovValidateManagedHooksDirectory(t *testing.T) {
	t.Parallel()
	if err := validateManagedHooksDirectory(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "inspect managed hooks directory") {
		t.Fatalf("validateManagedHooksDirectory(missing) error = %v", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	mustWrite(t, file, "plain\n")
	if err := validateManagedHooksDirectory(file); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("validateManagedHooksDirectory(file) error = %v", err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedHooksDirectory(filepath.Join(dir, "link")); err == nil || !strings.Contains(err.Error(), "refusing symlinked managed hooks directory") {
		t.Fatalf("validateManagedHooksDirectory(symlink) error = %v", err)
	}
	if err := validateManagedHooksDirectory(dir); err != nil {
		t.Fatalf("validateManagedHooksDirectory(dir) = %v, want nil", err)
	}
}

func TestHkCovOpenManagedHooksDirectoryErrors(t *testing.T) {
	t.Parallel()
	repo := initRepo(t)
	managed := hkCovManagedDir(t, repo)

	if _, err := openManagedHooksDirectory(filepath.Join(t.TempDir(), "missing-repo"), managed, nil); err == nil || !strings.Contains(err.Error(), "open repository directory") {
		t.Fatalf("openManagedHooksDirectory(missing repo) error = %v", err)
	}
	if _, err := openManagedHooksDirectory(repo, filepath.Join(t.TempDir(), "missing", "wb-hooks"), nil); err == nil || !strings.Contains(err.Error(), "inspect Git common directory") {
		t.Fatalf("openManagedHooksDirectory(missing common) error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "wb-hooks")
	mustWrite(t, file, "plain\n")
	if _, err := openManagedHooksDirectory(repo, file, nil); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("openManagedHooksDirectory(common is a file) error = %v", err)
	}

	// The common directory is swapped for a different real directory between
	// its first inspection and its descriptor-anchored open.
	swapped := initRepo(t)
	swappedCommon := hkCovManagedDir(t, swapped)
	_, err := openManagedHooksDirectory(swapped, swappedCommon, func() {
		if renameErr := os.Rename(filepath.Join(swapped, ".git"), filepath.Join(t.TempDir(), "moved-git")); renameErr != nil {
			t.Fatalf("move common directory: %v", renameErr)
		}
		mustMkdirAll(t, filepath.Join(swapped, ".git"))
	})
	if err == nil || !strings.Contains(err.Error(), "git common directory path changed") {
		t.Fatalf("swapped common directory error = %v", err)
	}

	// Swapping only the checked-out worktree is caught by the descriptor
	// validation after the common directory opens: a linked worktree's Git
	// common directory lives in the canonical clone, so it survives the swap.
	canonical := initRepo(t)
	worktree := filepath.Join(t.TempDir(), "linked")
	git(t, canonical, "worktree", "add", "-b", "hk-cov-linked", worktree)
	worktreeManaged := hkCovManagedDir(t, worktree)
	movedRepo := filepath.Join(t.TempDir(), "moved-repo")
	_, err = openManagedHooksDirectory(worktree, worktreeManaged, func() {
		if renameErr := os.Rename(worktree, movedRepo); renameErr != nil {
			t.Fatalf("move worktree: %v", renameErr)
		}
		mustMkdirAll(t, worktree)
	})
	if err == nil || !strings.Contains(err.Error(), "repository directory path changed") {
		t.Fatalf("swapped repository directory error = %v", err)
	}

	// A regular file where the managed directory belongs is refused.
	occupied := initRepo(t)
	mustWrite(t, hkCovManagedDir(t, occupied), "plain\n")
	if _, err := openManagedHooksDirectory(occupied, hkCovManagedDir(t, occupied), nil); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("occupied managed directory error = %v", err)
	}

	// A Git common directory that cannot accept the managed directory is
	// reported rather than silently skipped.
	if os.Geteuid() != 0 {
		shared := initRepo(t)
		sharedManaged := hkCovManagedDir(t, shared)
		if err := os.Chmod(filepath.Join(shared, ".git"), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(shared, ".git"), 0o755) })
		if _, err := openManagedHooksDirectory(shared, sharedManaged, nil); err == nil || !strings.Contains(err.Error(), "create managed hooks directory") {
			t.Fatalf("read-only common directory error = %v", err)
		}
	}
}

func TestHkCovMoveExpectedManagedHookNoReplace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	managed := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}

	if err := moveExpectedManagedHookNoReplace(managed, "missing", "backup", absentManagedHookIdentity(), nil); err == nil || !strings.Contains(err.Error(), "cannot quarantine absent managed hook") {
		t.Fatalf("moveExpectedManagedHookNoReplace(absent) error = %v", err)
	}

	mustWrite(t, filepath.Join(dir, "pre-commit"), "#!/bin/sh\n")
	identity, err := managedHookIdentityAt(handle, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "taken"), "already here\n")
	if err := moveExpectedManagedHookNoReplace(managed, "pre-commit", "taken", identity, nil); err == nil || !strings.Contains(err.Error(), "quarantine destination") {
		t.Fatalf("moveExpectedManagedHookNoReplace(occupied destination) error = %v", err)
	}

	if err := moveExpectedManagedHookNoReplace(managed, "pre-commit", "no/such/backup", identity, nil); err == nil {
		t.Fatal("moveExpectedManagedHookNoReplace with an unusable destination should fail")
	}

	authorized := false
	if err := moveExpectedManagedHookNoReplace(managed, "pre-commit", "backup", identity, func(name string) {
		authorized = name == "pre-commit"
	}); err != nil {
		t.Fatal(err)
	}
	if !authorized {
		t.Fatal("afterAuthorization was not invoked with the hook name")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "pre-commit")); !os.IsNotExist(statErr) {
		t.Fatalf("source hook still present: %v", statErr)
	}
	if mustReadFile(t, filepath.Join(dir, "backup")) != "#!/bin/sh\n" {
		t.Fatal("quarantined hook content changed")
	}
}

func TestHkCovQuarantineManagedHook(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	managed := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}

	// An unexpected file where the hook is supposed to be absent is refused.
	mustWrite(t, filepath.Join(dir, "pre-commit"), "#!/bin/sh\n")
	if _, err := quarantineManagedHook(managed, "pre-commit", absentManagedHookIdentity(), nil); err == nil || !strings.Contains(err.Error(), "changed after inspection") {
		t.Fatalf("quarantineManagedHook(unexpected file) error = %v", err)
	}

	authorized := false
	if name, err := quarantineManagedHook(managed, "pre-push", absentManagedHookIdentity(), func(string) { authorized = true }); err != nil || name != "" {
		t.Fatalf("quarantineManagedHook(absent) = %q, %v; want empty and nil", name, err)
	}
	if !authorized {
		t.Fatal("quarantineManagedHook did not run the authorization callback for an absent hook")
	}

	identity, err := managedHookIdentityAt(handle, "pre-commit")
	if err != nil {
		t.Fatal(err)
	}
	name, err := quarantineManagedHook(managed, "pre-commit", identity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, ".wb-backup-") {
		t.Fatalf("quarantine name = %q, want a backup-suffixed name", name)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "pre-commit")); !os.IsNotExist(statErr) {
		t.Fatalf("hook was not moved aside: %v", statErr)
	}
	if mustReadFile(t, filepath.Join(dir, name)) != "#!/bin/sh\n" {
		t.Fatal("quarantined content changed")
	}
}

func TestHkCovReadManagedHookErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	if _, err := readManagedHook(handle, "sub/hook"); err == nil || !strings.Contains(err.Error(), "invalid managed hook name") {
		t.Fatalf("readManagedHook(bad name) error = %v", err)
	}
	mustMkdirAll(t, filepath.Join(dir, "subdir"))
	if _, err := readManagedHook(handle, "subdir"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("readManagedHook(directory) error = %v", err)
	}
	if _, err := readManagedHook(handle, "missing"); err == nil {
		t.Fatal("readManagedHook(missing) should fail")
	}
	mustWrite(t, filepath.Join(dir, "pre-commit"), "#!/bin/sh\n")
	snapshot, err := readManagedHook(handle, "pre-commit")
	if err != nil || string(snapshot.content) != "#!/bin/sh\n" || !snapshot.identity.exists {
		t.Fatalf("readManagedHook = %#v, %v", snapshot, err)
	}
}

func TestHkCovWriteExecutableAt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	managed := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}

	if err := writeExecutableAt(managed, "sub/hook", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), nil); err == nil || !strings.Contains(err.Error(), "invalid managed hook name") {
		t.Fatalf("writeExecutableAt(bad name) error = %v", err)
	}
	if err := writeExecutableAt(managed, "pre-commit", []byte("#!/bin/sh\necho ok\n"), absentManagedHookIdentity(), nil); err != nil {
		t.Fatal(err)
	}
	if mustReadFile(t, filepath.Join(dir, "pre-commit")) != "#!/bin/sh\necho ok\n" {
		t.Fatal("installed hook content changed")
	}

	// A target planted by the authorization callback makes activation fail
	// without a quarantined hook to restore.
	if err := writeExecutableAt(managed, "pre-push", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), func(name string) {
		mustWrite(t, filepath.Join(dir, name), "planted\n")
	}); err == nil || !strings.Contains(err.Error(), "activate hook pre-push") {
		t.Fatalf("writeExecutableAt(planted target) error = %v", err)
	}

	if os.Geteuid() != 0 {
		locked := t.TempDir()
		lockedHandle, err := os.Open(locked)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = lockedHandle.Close() }()
		if err := os.Chmod(locked, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		lockedManaged := managedHooksDirectory{path: locked, commonPath: locked, common: lockedHandle, directory: lockedHandle}
		if err := writeExecutableAt(lockedManaged, "pre-commit", []byte("#!/bin/sh\n"), absentManagedHookIdentity(), nil); err == nil || !strings.Contains(err.Error(), "create temporary hook") {
			t.Fatalf("writeExecutableAt(read-only directory) error = %v", err)
		}
	}
}

func TestHkCovRemoveStaleManagedHooksAt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	other := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()

	actions := []string{}
	if err := removeStaleManagedHooksAt(managedHooksDirectory{path: other, commonPath: dir, common: handle, directory: handle}, nil, &actions, nil, nil); err == nil || !strings.Contains(err.Error(), "managed hooks directory path changed") {
		t.Fatalf("removeStaleManagedHooksAt(swapped path) error = %v", err)
	}

	// A directory descriptor that is not a directory fails with ENOTDIR
	// rather than being reported as an absent hook.
	regular := filepath.Join(t.TempDir(), "regular")
	mustWrite(t, regular, "plain\n")
	regularHandle, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularHandle.Close() }()
	if identity, err := managedHookIdentityAt(regularHandle, "pre-commit"); err == nil || identity.exists {
		t.Fatalf("managedHookIdentityAt(file descriptor) = %#v, %v; want an error", identity, err)
	}

	healthy := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}
	// An unmanaged file, a symlink, and a directory are all left alone.
	mustWrite(t, filepath.Join(dir, "unmanaged"), "#!/bin/sh\necho user\n")
	mustMkdirAll(t, filepath.Join(dir, "adir"))
	target := filepath.Join(t.TempDir(), "target")
	mustWrite(t, target, "#!/bin/sh\n")
	if err := os.Symlink(target, filepath.Join(dir, "alink")); err != nil {
		t.Fatal(err)
	}
	// A stale managed hook with no user content is quarantined.
	mustWrite(t, filepath.Join(dir, "stale"), shimManagedSection("", "stale", "", "", "", false))
	// A stale managed hook with user content keeps the user's commands.
	mustWrite(t, filepath.Join(dir, "userful"), shimManagedSection("", "userful", "", "", "", false)+"echo user\n")
	// Out-of-order markers are an error, not a silent removal.
	mustWrite(t, filepath.Join(dir, "broken"), managedEndMarker+"\n")

	if err := removeStaleManagedHooksAt(healthy, nil, &actions, nil, nil); err == nil || !strings.Contains(err.Error(), "markers are incomplete or out of order") {
		t.Fatalf("removeStaleManagedHooksAt(broken markers) error = %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "broken"))

	actions = nil
	if err := removeStaleManagedHooksAt(healthy, nil, &actions, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "stale")); !os.IsNotExist(statErr) {
		t.Fatalf("stale managed hook was not quarantined: %v", statErr)
	}
	userful := mustReadFile(t, filepath.Join(dir, "userful"))
	if strings.Contains(userful, managedStartMarker) || !strings.Contains(userful, "echo user") {
		t.Fatalf("user commands were not preserved: %q", userful)
	}
	quarantined, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	foundBackup := false
	for _, entry := range quarantined {
		if strings.Contains(entry.Name(), ".wb-backup-") {
			foundBackup = true
		}
	}
	if !foundBackup {
		t.Fatalf("directory = %v, want a quarantined backup", quarantined)
	}
	for _, action := range actions {
		if !strings.Contains(action, "quarantined stale managed hook stale") && !strings.Contains(action, "preserved user commands") {
			t.Fatalf("unexpected action %q", action)
		}
	}
}

func TestHkCovRemoveStaleManagedHooksAtReportsUnwritablePreservation(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		return
	}
	dir := t.TempDir()
	handle, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	mustWrite(t, filepath.Join(dir, "userful"), shimManagedSection("", "userful", "", "", "", false)+"echo user\n")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	actions := []string{}
	// A directory descriptor that is not a directory fails with ENOTDIR
	// rather than being reported as an absent hook.
	regular := filepath.Join(t.TempDir(), "regular")
	mustWrite(t, regular, "plain\n")
	regularHandle, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularHandle.Close() }()
	if identity, err := managedHookIdentityAt(regularHandle, "pre-commit"); err == nil || identity.exists {
		t.Fatalf("managedHookIdentityAt(file descriptor) = %#v, %v; want an error", identity, err)
	}

	healthy := managedHooksDirectory{path: dir, commonPath: dir, common: handle, directory: handle}
	if err := removeStaleManagedHooksAt(healthy, nil, &actions, nil, nil); err == nil {
		t.Fatal("preserving user commands in a read-only directory should fail")
	}
}

func TestHkCovExtractManagedSectionBranches(t *testing.T) {
	t.Parallel()
	if _, managed, valid := extractManagedSection("#!/bin/sh\necho plain\n"); managed || valid {
		t.Fatalf("extractManagedSection(plain) = %v, %v; want false, false", managed, valid)
	}
	if _, managed, valid := extractManagedSection("#!/bin/sh\n" + managedEndMarker + "\n" + managedStartMarker + "\n"); !managed || valid {
		t.Fatalf("extractManagedSection(out of order) = %v, %v; want true, false", managed, valid)
	}
	section, managed, valid := extractManagedSection("#!/bin/sh\n" + managedStartMarker + "\nbody\n" + managedEndMarker + "\n")
	if !managed || !valid || !strings.Contains(section, "body") {
		t.Fatalf("extractManagedSection(valid) = %q, %v, %v", section, managed, valid)
	}
}

func TestHkCovReplaceManagedSectionBranches(t *testing.T) {
	t.Parallel()
	if _, err := replaceManagedSectionWith("#!/bin/sh\necho plain\n", "replacement"); err == nil || !strings.Contains(err.Error(), "markers are missing") {
		t.Fatalf("replaceManagedSectionWith(no markers) error = %v", err)
	}
	if _, err := replaceManagedSectionWith("#!/bin/sh\n"+managedEndMarker+"\n"+managedStartMarker+"\n", "replacement"); err == nil || !strings.Contains(err.Error(), "incomplete or out of order") {
		t.Fatalf("replaceManagedSectionWith(out of order) error = %v", err)
	}
	content := "#!/bin/sh\n" + managedStartMarker + "\nold\n" + managedEndMarker + "\necho user\n"
	replaced, err := replaceManagedSection(content, managedStartMarker+"\nnew\n"+managedEndMarker+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(replaced, "new") || strings.Contains(replaced, "old") || !strings.Contains(replaced, "echo user") {
		t.Fatalf("replaceManagedSection = %q", replaced)
	}
	removed, err := removeManagedSection(content)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(removed, managedStartMarker) || !strings.Contains(removed, "echo user") {
		t.Fatalf("removeManagedSection = %q", removed)
	}
}

func TestHkCovRepositoryHeadCommitTimeAndSourceModule(t *testing.T) {
	t.Parallel()
	if _, err := repositoryHeadCommitTime(t.TempDir()); err == nil {
		t.Fatal("repositoryHeadCommitTime(non-repo) should fail")
	}
	repo := initRepo(t)
	commitTime, err := repositoryHeadCommitTime(repo)
	if err != nil || commitTime.IsZero() {
		t.Fatalf("repositoryHeadCommitTime = %v, %v", commitTime, err)
	}
	if repositoryIsWBSourceModule(t.TempDir()) {
		t.Fatal("repositoryIsWBSourceModule(no go.mod) should be false")
	}
	mustWrite(t, filepath.Join(repo, "go.mod"), "// no module directive\nrequire example.invalid/x v1.0.0\n")
	if repositoryIsWBSourceModule(repo) {
		t.Fatal("repositoryIsWBSourceModule(go.mod without a module line) should be false")
	}
	mustWrite(t, filepath.Join(repo, "go.mod"), "module "+wbSourceModulePath+"\n")
	if !repositoryIsWBSourceModule(repo) {
		t.Fatal("repositoryIsWBSourceModule(wb module) should be true")
	}
}

func TestHkCovActiveDefaultHooks(t *testing.T) {
	t.Parallel()
	if _, err := activeDefaultHooks(t.TempDir()); err == nil {
		t.Fatal("activeDefaultHooks(non-repo) should fail")
	}
	repo := initRepo(t)
	active, err := activeDefaultHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("activeDefaultHooks(fresh repo) = %v, want none", active)
	}

	hooksDir := filepath.Join(repo, ".git", "hooks")
	mustWriteExecutable(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\nexit 0\n")
	mustWrite(t, filepath.Join(hooksDir, "pre-push.sample"), "#!/bin/sh\nexit 0\n")
	mustWrite(t, filepath.Join(hooksDir, "not-executable"), "#!/bin/sh\nexit 0\n")
	mustMkdirAll(t, filepath.Join(hooksDir, "adir"))
	active, err = activeDefaultHooks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0] != "pre-commit" {
		t.Fatalf("activeDefaultHooks = %v, want [pre-commit]", active)
	}

	// A missing default hooks directory is an empty, error-free answer.
	gone := initRepo(t)
	if err := os.RemoveAll(filepath.Join(gone, ".git", "hooks")); err != nil {
		t.Fatal(err)
	}
	active, err = activeDefaultHooks(gone)
	if err != nil || len(active) != 0 {
		t.Fatalf("activeDefaultHooks(missing hooks dir) = %v, %v; want none and nil", active, err)
	}

	broken := initRepo(t)
	if err := os.RemoveAll(filepath.Join(broken, ".git", "hooks")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(broken, ".git", "hooks"), "not a directory\n")
	if _, err := activeDefaultHooks(broken); err == nil {
		t.Fatal("activeDefaultHooks with an unreadable hooks path should fail")
	}
}

func TestHkCovRefreshManagedShimsErrorPaths(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)

	if _, err := RefreshManagedShims(repo, "", "", ""); err == nil || !strings.Contains(err.Error(), "without a WB executable") {
		t.Fatalf("RefreshManagedShims without an executable error = %v", err)
	}
	if _, err := RefreshManagedShims(t.TempDir(), "", testWBExecutable(t, "wb"), ""); err == nil {
		t.Fatal("RefreshManagedShims(non-repo) should fail")
	}
	err, platformSupportsIt := hkCovWithoutUsableCwd(t, func() error {
		_, err := RefreshManagedShims(repo, "", testWBExecutable(t, "wb"), "relative/projects")
		return err
	})
	if platformSupportsIt && (err == nil || !strings.Contains(err.Error(), "resolve projects root")) {
		t.Fatalf("RefreshManagedShims with an unresolvable projects root error = %v", err)
	}

	// A symlinked Git common directory cannot name a managed location.
	symlinked := initRepo(t)
	isolateConfig(t)
	external := filepath.Join(t.TempDir(), "external-git")
	if err := os.Rename(filepath.Join(symlinked, ".git"), external); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(symlinked, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshManagedShims(symlinked, "", testWBExecutable(t, "wb"), ""); err == nil || !strings.Contains(err.Error(), "symlinked Git common directory") {
		t.Fatalf("RefreshManagedShims with a symlinked Git common directory error = %v", err)
	}

	changed, err := RefreshManagedShims(repo, "", testWBExecutable(t, "wb"), "")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("RefreshManagedShims on a repository without managed hooks should report no change")
	}
}

func TestHkCovRefreshManagedShimsValidations(t *testing.T) {
	executable := testWBExecutable(t, "wb")

	// A managed hooks path that is a regular file is refused.
	occupied := initRepo(t)
	isolateConfig(t)
	mustWrite(t, hkCovManagedDir(t, occupied), "plain\n")
	git(t, occupied, "config", "--local", "core.hooksPath", filepath.Join(".git", "wb-hooks"))
	if _, err := RefreshManagedShims(occupied, "", executable, ""); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("RefreshManagedShims(occupied managed path) error = %v", err)
	}

	// A symlinked hooksPath that resolves onto the managed location is still
	// validated as the managed directory, even when that path is not a
	// directory.
	missing := initRepo(t)
	isolateConfig(t)
	managedFile := hkCovManagedDir(t, missing)
	mustWrite(t, managedFile, "plain\n")
	link := filepath.Join(missing, ".git", "wb-hooks-link")
	if err := os.Symlink(managedFile, link); err != nil {
		t.Fatal(err)
	}
	git(t, missing, "config", "--local", "core.hooksPath", link)
	if _, err := RefreshManagedShims(missing, "", executable, ""); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("RefreshManagedShims with a symlinked managed path error = %v", err)
	}

	// A stale managed hook is repaired in place.
	stale := initRepo(t)
	isolateConfig(t)
	installed := hkCovInstall(t, stale, executable)
	preCommit := filepath.Join(installed.Report.ManagedPath, "pre-commit")
	original := mustReadFile(t, preCommit)
	mustWrite(t, preCommit, strings.Replace(original, "command -v wb", "command -v wb-stale", 1))
	changed, err := RefreshManagedShims(stale, "", executable, "")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("RefreshManagedShims should report that it repaired a stale shim")
	}
	if mustReadFile(t, preCommit) != original {
		t.Fatal("refresh did not restore the expected shim text")
	}

	// A malformed managed hook is refused with guidance.
	malformed := initRepo(t)
	isolateConfig(t)
	malformedInstall := hkCovInstall(t, malformed, executable)
	mustWrite(t, filepath.Join(malformedInstall.Report.ManagedPath, "pre-commit"), "#!/bin/sh\necho user\n")
	if _, err := RefreshManagedShims(malformed, "", executable, ""); err == nil || !strings.Contains(err.Error(), "is malformed") {
		t.Fatalf("RefreshManagedShims(malformed hook) error = %v", err)
	}

	// A missing managed hook is reported with the hook name.
	missingHook := initRepo(t)
	isolateConfig(t)
	missingInstall := hkCovInstall(t, missingHook, executable)
	if err := os.Remove(filepath.Join(missingInstall.Report.ManagedPath, "pre-commit")); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshManagedShims(missingHook, "", executable, ""); err == nil || !strings.Contains(err.Error(), "before worktree creation") {
		t.Fatalf("RefreshManagedShims(missing hook) error = %v", err)
	}

	// A healthy installation reports no change.
	healthy := initRepo(t)
	isolateConfig(t)
	hkCovInstall(t, healthy, executable)
	changed, err = RefreshManagedShims(healthy, "", executable, "")
	if err != nil || changed {
		t.Fatalf("RefreshManagedShims(healthy) = %v, %v; want false, nil", changed, err)
	}

	// A repair that cannot write is reported.
	if os.Geteuid() != 0 {
		locked := initRepo(t)
		isolateConfig(t)
		lockedInstall := hkCovInstall(t, locked, executable)
		lockedPreCommit := filepath.Join(lockedInstall.Report.ManagedPath, "pre-commit")
		mustWrite(t, lockedPreCommit, strings.Replace(mustReadFile(t, lockedPreCommit), "command -v wb", "command -v wb-stale", 1))
		if err := os.Chmod(lockedInstall.Report.ManagedPath, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(lockedInstall.Report.ManagedPath, 0o755) })
		if _, err := RefreshManagedShims(locked, "", executable, ""); err == nil || !strings.Contains(err.Error(), "refresh incompatible managed hooks") {
			t.Fatalf("RefreshManagedShims(unwritable repair) error = %v", err)
		}
	}
}

func TestHkCovRefreshManagedShimsReportsHomeFailure(t *testing.T) {
	repo := initRepo(t)
	isolateConfig(t)
	executable := testWBExecutable(t, "wb")
	hkCovInstall(t, repo, executable)
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	// The projects root selects the state home now, so the unusable root is
	// passed where the API accepts one instead of through WB_HOME.
	if _, err := RefreshManagedShims(repo, "", executable, filepath.Join(blocker, "projects")); err == nil {
		t.Fatal("RefreshManagedShims with an unusable projects root should fail")
	}
}

func TestHkCovApplyReportsStaleExecutableTargets(t *testing.T) {
	t.Parallel()
	if _, err := durableWBExecutable(filepath.Join(t.TempDir(), "missing-wb")); err == nil {
		t.Fatal("durableWBExecutable(missing) should fail")
	}
	directory := t.TempDir()
	if _, err := durableWBExecutable(directory); err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("durableWBExecutable(directory) error = %v", err)
	}
	notExecutable := filepath.Join(t.TempDir(), "wb")
	mustWrite(t, notExecutable, "#!/bin/sh\n")
	if _, err := durableWBExecutable(notExecutable); err == nil || !strings.Contains(err.Error(), "is not executable") {
		t.Fatalf("durableWBExecutable(not executable) error = %v", err)
	}
}
