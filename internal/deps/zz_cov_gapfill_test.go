package deps

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// depsCovGapEnforcesPermissions reports whether the current platform actually
// denies filesystem access from POSIX permission bits. Two environments do
// not: root bypasses the permission checks entirely, and Windows does not
// implement them. The tests below assert the honest outcome for whichever
// environment they run in rather than skipping.
func depsCovGapEnforcesPermissions() bool {
	return runtime.GOOS != "windows" && os.Geteuid() != 0
}

// TestDepsCovGapRepositoryContainsLocalManifestUnreadableTree drives the
// WalkDir callback's walkErr arm: os.Stat sees the checkout, but the tree
// cannot be listed, so the scan must surface the filesystem error instead of
// silently reporting "no manifest here".
func TestDepsCovGapRepositoryContainsLocalManifestUnreadableTree(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		scan func(string) (bool, error)
	}{
		{name: "go", scan: repositoryContainsLocalGoManifest},
		{name: "npm", scan: repositoryContainsLocalNpmManifest},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "checkout")
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

			found, err := testCase.scan(root)
			if depsCovGapEnforcesPermissions() {
				if err == nil {
					t.Fatalf("scan of an unlistable checkout reported found=%v and no error", found)
				}
				if found {
					t.Fatalf("scan reported found=true alongside error %v", err)
				}
				return
			}
			// Root and Windows ignore the mode: the walk succeeds and the
			// empty checkout simply contains no manifest.
			if err != nil {
				t.Fatalf("scan = %v, want a clean walk when permissions are not enforced", err)
			}
			if found {
				t.Fatal("scan reported found=true for an empty checkout")
			}
		})
	}
}

// TestDepsCovGapGitHubActionsApplySurfacesFilesystemRefusals proves the
// adapter reports a directory it cannot list and a directory it cannot write
// to, instead of returning a silently empty decision set.
func TestDepsCovGapGitHubActionsApplySurfacesFilesystemRefusals(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0", Resolved: strings.Repeat("2", 40)}
	body := "jobs:\n  ci:\n    uses: acme/cicd/.github/workflows/go.yml@" + strings.Repeat("1", 40) + " # v1.0.0\n"

	t.Run("unlistable workflows directory", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		workflows := filepath.Join(worktree, ".github", "workflows")
		writeTestFile(t, filepath.Join(workflows, "ci.yml"), body)
		if err := os.Chmod(workflows, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(workflows, 0o755) })

		_, err := (githubActionsAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
		if depsCovGapEnforcesPermissions() {
			if err == nil {
				t.Fatal("apply succeeded although the workflows directory cannot be listed")
			}
			return
		}
		if err != nil {
			t.Fatalf("apply = %v, want a clean walk when permissions are not enforced", err)
		}
	})

	t.Run("unwritable workflows directory", func(t *testing.T) {
		t.Parallel()
		worktree := t.TempDir()
		workflows := filepath.Join(worktree, ".github", "workflows")
		writeTestFile(t, filepath.Join(workflows, "ci.yml"), body)
		if err := os.Chmod(workflows, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(workflows, 0o755) })

		_, err := (githubActionsAdapter{}).apply(context.Background(), worktree, target, Options{Timeout: time.Minute})
		if depsCovGapEnforcesPermissions() {
			if err == nil {
				t.Fatal("apply succeeded although the workflows directory is not writable")
			}
			if got := mustReadFile(t, filepath.Join(workflows, "ci.yml")); got != body {
				t.Fatalf("a failed write still modified ci.yml:\n%s", got)
			}
			return
		}
		if err != nil {
			t.Fatalf("apply = %v, want a successful write when permissions are not enforced", err)
		}
	})
}

// TestDepsCovGapNpmApplySurfacesUnwritableManifest proves an npm manifest
// rewrite that cannot be written is reported as an error rather than dropped.
func TestDepsCovGapNpmApplySurfacesUnwritableManifest(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		files map[string]string
	}{
		{name: "package.json", files: map[string]string{"package.json": npmPackageJSONWithDependency("@sneat/app", "@sneat/core", "1.0.0")}},
		{name: "pnpm-workspace.yaml", files: map[string]string{"pnpm-workspace.yaml": "overrides:\n  \"@sneat/core\": \"1.0.0\"\n"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			worktree := t.TempDir()
			for relative, contents := range testCase.files {
				writeTestFile(t, filepath.Join(worktree, relative), contents)
			}
			if err := os.Chmod(worktree, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(worktree, 0o755) })

			_, err := (npmAdapter{}).apply(context.Background(), worktree, depsCovNpmTarget("@sneat/core", "1.3.0"), Options{Timeout: time.Minute})
			if depsCovGapEnforcesPermissions() {
				if err == nil {
					t.Fatalf("apply succeeded although %s is not writable", testCase.name)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply = %v, want a successful write when permissions are not enforced", err)
			}
		})
	}
}

// TestDepsCovGapInstalledNpmVersionsReportsUnreadableManifest covers the
// manifest read that fails after the walk has already listed the path: a
// dangling symlink is a directory entry the walk accepts and the read rejects.
func TestDepsCovGapInstalledNpmVersionsReportsUnreadableManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "missing-target.json"), filepath.Join(root, "package.json")); err != nil {
		t.Skipf("this platform cannot create the dangling-symlink fixture: %v", err)
	}
	installed, source, err := installedNpmVersions(root)
	if err == nil {
		t.Fatal("installedNpmVersions succeeded although package.json cannot be read")
	}
	if installed != nil || source != "" {
		t.Fatalf("installedNpmVersions = (%v, %q) alongside error %v, want the zero result", installed, source, err)
	}
}

// TestDepsCovGapInspectPeersReportsWorkingDirectoryFailure proves the
// --against path resolution reports a process whose working directory no
// longer exists instead of resolving a relative checkout against nothing.
func TestDepsCovGapInspectPeersReportsWorkingDirectoryFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot remove a process working directory, so filepath.Abs cannot fail this way")
	}
	parent := t.TempDir()
	vanished := filepath.Join(parent, "vanished")
	if err := os.Mkdir(vanished, 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(vanished); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restoring the working directory: %v", err)
		}
	}()
	if err := os.Remove(vanished); err != nil {
		t.Skipf("this platform cannot remove the process working directory: %v", err)
	}

	if _, err := InspectPeers(context.Background(), PeerOptions{Package: "@sneat/core", Against: "relative-checkout"}); err == nil {
		t.Fatal("InspectPeers succeeded although the working directory no longer exists")
	}
}
