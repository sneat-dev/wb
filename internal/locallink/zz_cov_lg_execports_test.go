package locallink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lgCovFakeBin writes an executable shell script named name into a fresh temp
// directory and returns the directory, so a test can drive the real-exec seams
// (git, node, pnpm, npm, yarn) without touching the network or the machine's
// real toolchain.
func lgCovFakeBin(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// lgCovPrependPath puts dir first on PATH for the duration of the test.
func lgCovPrependPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// lgCovRestrictPath replaces PATH with dir alone, so any command the code
// looks up is guaranteed absent unless the test itself wrote it there.
func lgCovRestrictPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func lgCovRequireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// ContentHash's failure surface: an unusable temp directory, a directory that
// is not a repository at all, and a bare repository with no working tree. Each
// must be reported rather than silently returning an empty identity.
func TestLgCovContentHashFailurePaths(t *testing.T) {
	t.Parallel()
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	t.Run("unusable temp dir", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-tmp")
		t.Setenv("TMPDIR", missing)
		if _, _, err := git.ContentHash(ctx, t.TempDir()); err == nil {
			t.Fatal("ContentHash succeeded with no usable temp directory")
		}
	})

	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		lgCovRequireGit(t)
		if _, _, err := git.ContentHash(ctx, t.TempDir()); err == nil ||
			!strings.Contains(err.Error(), "prepare a temporary index") {
			t.Fatalf("error = %v, want a temporary-index preparation failure", err)
		}
	})

	t.Run("bare repository has no working tree", func(t *testing.T) {
		t.Parallel()
		lgCovRequireGit(t)
		bare := t.TempDir()
		command := exec.Command("git", "init", "--bare", ".")
		command.Dir = bare
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git init --bare: %v: %s", err, output)
		}
		_, _, err := git.ContentHash(ctx, bare)
		if err == nil || !strings.Contains(err.Error(), "stage the working tree") {
			t.Fatalf("error = %v, want a staged-working-tree failure", err)
		}
	})
}

// TrackedChanges on a directory that is not a repository must report the git
// failure rather than an empty change list.
func TestLgCovTrackedChangesReportsGitFailure(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	git := ExecGit{Timeout: 30 * time.Second}
	_, err := git.TrackedChanges(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "read tracked changes") {
		t.Fatalf("error = %v, want a wrapped git failure", err)
	}
}

// The exclude file is resolved through git; each failure of that resolution,
// of reading the file, and of writing it must be reported.
func TestLgCovExcludePathFailurePaths(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		if err := git.ExcludePath(ctx, t.TempDir(), "/go.work"); err == nil ||
			!strings.Contains(err.Error(), "resolve the exclude file") {
			t.Fatalf("error = %v, want an exclude-file resolution failure", err)
		}
	})

	t.Run("git reports no exclude file", func(t *testing.T) {
		fake := lgCovFakeBin(t, "git", "exit 0")
		lgCovRestrictPath(t, fake)
		err := git.ExcludePath(ctx, t.TempDir(), "/go.work")
		if err == nil || !strings.Contains(err.Error(), "reported no exclude file") {
			t.Fatalf("error = %v, want the empty-path refusal", err)
		}
	})

	t.Run("relative exclude path is joined to the worktree", func(t *testing.T) {
		fake := lgCovFakeBin(t, "git", "printf 'info/exclude\\n'")
		lgCovRestrictPath(t, fake)
		worktree := t.TempDir()
		// The fake git answers every invocation, so the write lands in a real
		// file at <worktree>/info/exclude and can be asserted.
		if err := git.ExcludePath(ctx, worktree, "/go.work"); err != nil {
			t.Fatalf("ExcludePath with a relative exclude path: %v", err)
		}
		contents, err := os.ReadFile(filepath.Join(worktree, "info", "exclude"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "/go.work") {
			t.Fatalf("exclude file = %q, want the pattern", contents)
		}
		patterns, err := git.ExcludedPatterns(ctx, worktree)
		if err != nil {
			t.Fatal(err)
		}
		if len(patterns) == 0 || strings.TrimSpace(patterns[0]) != "/go.work" {
			t.Fatalf("patterns = %v, want /go.work", patterns)
		}
	})

	t.Run("absolute exclude path is used verbatim", func(t *testing.T) {
		worktree := t.TempDir()
		absolute := filepath.Join(t.TempDir(), "exclude")
		fake := lgCovFakeBin(t, "git", "printf '%s\\n' '"+absolute+"'")
		lgCovRestrictPath(t, fake)
		if err := git.ExcludePath(ctx, worktree, "/go.work"); err != nil {
			t.Fatalf("ExcludePath with an absolute exclude path: %v", err)
		}
		contents, err := os.ReadFile(absolute)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "/go.work") {
			t.Fatalf("exclude file = %q, want the pattern", contents)
		}
	})

	t.Run("exclude path parent cannot be created", func(t *testing.T) {
		t.Parallel()
		root := initRepository(t)
		info := filepath.Join(root, ".git", "info")
		if err := os.RemoveAll(info); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "nowhere"), info); err != nil {
			t.Fatal(err)
		}
		err := git.ExcludePath(ctx, root, "/go.work")
		if err == nil || !strings.Contains(err.Error(), "create "+info) {
			t.Fatalf("error = %v, want a directory-creation failure naming %s", err, info)
		}
	})

	t.Run("exclude path cannot be opened", func(t *testing.T) {
		t.Parallel()
		root := initRepository(t)
		exclude := filepath.Join(root, ".git", "info", "exclude")
		if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "missing-dir", "exclude"), exclude); err != nil {
			t.Fatal(err)
		}
		err := git.ExcludePath(ctx, root, "/go.work")
		if err == nil || !strings.Contains(err.Error(), "open "+exclude) {
			t.Fatalf("error = %v, want an open failure naming %s", err, exclude)
		}
	})

	t.Run("no trailing newline in an existing exclude file", func(t *testing.T) {
		t.Parallel()
		root := initRepository(t)
		exclude := filepath.Join(root, ".git", "info", "exclude")
		if err := os.WriteFile(exclude, []byte("first"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := git.ExcludePath(ctx, root, "/go.work"); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(exclude)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "first") || !strings.Contains(string(contents), "/go.work") {
			t.Fatalf("exclude file = %q, want the original line and the appended pattern", contents)
		}
	})
}

// ExcludedPatterns surfaces a missing exclude file as "no patterns" and an
// unreadable one as an error — the two must not be spelled the same way.
func TestLgCovExcludedPatternsMissingVersusUnreadable(t *testing.T) {
	t.Parallel()
	lgCovRequireGit(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	root := initRepository(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	patterns, err := git.ExcludedPatterns(ctx, root)
	if err != nil || len(patterns) != 0 {
		t.Fatalf("patterns = %v, err = %v, want an empty result with no error", patterns, err)
	}

	if err := os.RemoveAll(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(exclude, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.ExcludedPatterns(ctx, root); err == nil {
		t.Fatal("an unreadable exclude file reported no patterns instead of an error")
	}
}

// FrozenInstall must select the lockfile's own manager, refuse when the
// manager is absent, report a failed install, and pass a clean one.
func TestLgCovFrozenInstallDrivesTheSelectedManager(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	node := ExecNode{Timeout: 30 * time.Second}

	t.Run("pnpm success", func(t *testing.T) {
		consumer := t.TempDir()
		if err := os.WriteFile(filepath.Join(consumer, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := lgCovFakeBin(t, "pnpm", "printf 'install %s\\n' \"$*\" > pnpm-ran.txt")
		lgCovPrependPath(t, bin)
		if err := node.FrozenInstall(ctx, consumer); err != nil {
			t.Fatalf("FrozenInstall: %v", err)
		}
		ran, err := os.ReadFile(filepath.Join(consumer, "pnpm-ran.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(ran), "install --frozen-lockfile") {
			t.Fatalf("pnpm was invoked as %q, want install --frozen-lockfile", ran)
		}
	})

	t.Run("yarn success", func(t *testing.T) {
		consumer := t.TempDir()
		if err := os.WriteFile(filepath.Join(consumer, "yarn.lock"), []byte("# yarn lockfile\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := lgCovFakeBin(t, "yarn", "printf 'install %s\\n' \"$*\" > yarn-ran.txt")
		lgCovPrependPath(t, bin)
		if err := node.FrozenInstall(ctx, consumer); err != nil {
			t.Fatalf("FrozenInstall: %v", err)
		}
		ran, err := os.ReadFile(filepath.Join(consumer, "yarn-ran.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(ran), "install --immutable") {
			t.Fatalf("yarn was invoked as %q, want install --immutable", ran)
		}
	})

	t.Run("npm success", func(t *testing.T) {
		consumer := t.TempDir()
		if err := os.WriteFile(filepath.Join(consumer, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := lgCovFakeBin(t, "npm", "printf 'install %s\\n' \"$*\" > npm-ran.txt")
		lgCovPrependPath(t, bin)
		if err := node.FrozenInstall(ctx, consumer); err != nil {
			t.Fatalf("FrozenInstall: %v", err)
		}
		ran, err := os.ReadFile(filepath.Join(consumer, "npm-ran.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(ran), "install ci") {
			t.Fatalf("npm was invoked as %q, want ci", ran)
		}
	})

	t.Run("manager absent", func(t *testing.T) {
		consumer := t.TempDir()
		if err := os.WriteFile(filepath.Join(consumer, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		lgCovRestrictPath(t, t.TempDir())
		err := node.FrozenInstall(ctx, consumer)
		if err == nil || !strings.Contains(err.Error(), "required to prove a frozen install") {
			t.Fatalf("error = %v, want the missing-manager refusal", err)
		}
	})

	t.Run("install fails", func(t *testing.T) {
		consumer := t.TempDir()
		if err := os.WriteFile(filepath.Join(consumer, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := lgCovFakeBin(t, "pnpm", "printf 'lockfile mismatch\\n' >&2; exit 1")
		lgCovPrependPath(t, bin)
		err := node.FrozenInstall(ctx, consumer)
		if err == nil || !strings.Contains(err.Error(), "lockfile mismatch") {
			t.Fatalf("error = %v, want the failed install reported", err)
		}
	})
}

func TestLgCovFrozenInstallCommandSelectsByLockfile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file    string
		manager string
		args    string
	}{
		{file: "pnpm-lock.yaml", manager: "pnpm", args: "install --frozen-lockfile"},
		{file: "yarn.lock", manager: "yarn", args: "install --immutable"},
		{file: "package-lock.json", manager: "npm", args: "ci"},
	}
	for _, testCase := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, testCase.file), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		manager, args := frozenInstallCommand(dir)
		if manager != testCase.manager || strings.Join(args, " ") != testCase.args {
			t.Fatalf("%s selected %s %v, want %s %s", testCase.file, manager, args, testCase.manager, testCase.args)
		}
	}
	manager, args := frozenInstallCommand(t.TempDir())
	if manager != "" || args != nil {
		t.Fatalf("no lockfile selected %q %v, want no command", manager, args)
	}
}

func TestLgCovPackageManagerDefaultsToNpm(t *testing.T) {
	t.Parallel()
	if got := packageManager(t.TempDir()); got != "npm" {
		t.Fatalf("packageManager with no lockfile = %q, want npm", got)
	}
}

// lgCovBuildLibrary lays out a library workspace whose own `build` script is
// the selected build target, with a pre-existing dist so the real builtDist
// locator has something to find.
func lgCovBuildLibrary(t *testing.T, distRel string) (library, dist string) {
	t.Helper()
	library = t.TempDir()
	if err := os.WriteFile(filepath.Join(library, "package.json"), []byte(`{"name":"@acme/library","scripts":{"build":"tsc"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dist = filepath.Join(library, filepath.FromSlash(distRel))
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/library"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return library, dist
}

// Build runs the repository's own build target, caches the dist by content
// hash, and reports each way that can fail.
func TestLgCovBuildRunsAndRecordsTheCachedDist(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("success caches the dist", func(t *testing.T) {
		library, dist := lgCovBuildLibrary(t, "dist")
		bin := lgCovFakeBin(t, "npm", "printf 'built %s\\n' \"$*\" > build-ran.txt; exit 0")
		lgCovPrependPath(t, bin)
		cache := t.TempDir()
		node := ExecNode{CacheRoot: cache, ContentHash: "hash-one", Timeout: 30 * time.Second}
		got, err := node.Build(ctx, library, library)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got != dist {
			t.Fatalf("dist = %q, want %q", got, dist)
		}
		ran, err := os.ReadFile(filepath.Join(library, "build-ran.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(ran), "run build") {
			t.Fatalf("build command = %q, want the workspace build script", ran)
		}
		marker := filepath.Join(cache, "hash-one", buildCacheKey("hash-one", library), buildMarkerName)
		contents, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("the cached build marker was not written: %v", err)
		}
		if strings.TrimSpace(string(contents)) != dist {
			t.Fatalf("cached marker = %q, want %q", contents, dist)
		}
	})

	t.Run("build command failure is reported", func(t *testing.T) {
		library, _ := lgCovBuildLibrary(t, "dist")
		bin := lgCovFakeBin(t, "npm", "printf 'tsc exploded\\n' >&2; exit 1")
		lgCovPrependPath(t, bin)
		node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
		_, err := node.Build(ctx, library, library)
		if err == nil || !strings.Contains(err.Error(), "tsc exploded") {
			t.Fatalf("error = %v, want the build failure reported", err)
		}
	})

	t.Run("missing build command is reported", func(t *testing.T) {
		library, _ := lgCovBuildLibrary(t, "dist")
		lgCovRestrictPath(t, t.TempDir())
		node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
		_, err := node.Build(ctx, library, library)
		if err == nil || !strings.Contains(err.Error(), "required to build") {
			t.Fatalf("error = %v, want the missing-build-command refusal", err)
		}
	})

	t.Run("no build output is reported", func(t *testing.T) {
		library := t.TempDir()
		if err := os.WriteFile(filepath.Join(library, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		bin := lgCovFakeBin(t, "npm", "exit 0")
		lgCovPrependPath(t, bin)
		node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
		_, err := node.Build(ctx, library, library)
		if err == nil || !strings.Contains(err.Error(), "produced no output") {
			t.Fatalf("error = %v, want the no-output report", err)
		}
	})

	t.Run("cache directory cannot be created", func(t *testing.T) {
		library, _ := lgCovBuildLibrary(t, "dist")
		bin := lgCovFakeBin(t, "npm", "exit 0")
		lgCovPrependPath(t, bin)
		cacheFile := filepath.Join(t.TempDir(), "cache-file")
		if err := os.WriteFile(cacheFile, []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		node := ExecNode{CacheRoot: cacheFile, ContentHash: "hash", Timeout: 30 * time.Second}
		_, err := node.Build(ctx, library, library)
		if err == nil || !strings.Contains(err.Error(), "create the build cache directory") {
			t.Fatalf("error = %v, want the cache-directory failure", err)
		}
	})

	t.Run("cache marker cannot be written", func(t *testing.T) {
		library, _ := lgCovBuildLibrary(t, "dist")
		bin := lgCovFakeBin(t, "npm", "exit 0")
		lgCovPrependPath(t, bin)
		cache := t.TempDir()
		cached := filepath.Join(cache, "hash", buildCacheKey("hash", library))
		if err := os.MkdirAll(filepath.Join(cached, buildMarkerName), 0o755); err != nil {
			t.Fatal(err)
		}
		node := ExecNode{CacheRoot: cache, ContentHash: "hash", Timeout: 30 * time.Second}
		_, err := node.Build(ctx, library, library)
		if err == nil || !strings.Contains(err.Error(), "record the cached build") {
			t.Fatalf("error = %v, want the cache-record failure", err)
		}
	})
}

func TestLgCovNodeBuildCommandFailureAndManagerBranches(t *testing.T) {
	t.Parallel()
	newWorkspace := func(t *testing.T) (workspace, packageDir string) {
		t.Helper()
		workspace = t.TempDir()
		packageDir = filepath.Join(workspace, "libs", "core")
		if err := os.MkdirAll(packageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		return workspace, packageDir
	}
	writeProject := func(t *testing.T, packageDir, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(packageDir, "project.json"), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("malformed project.json", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		writeProject(t, packageDir, `{"name":`)
		if _, _, err := nodeBuildCommand(workspace, packageDir); err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("error = %v, want a project.json parse failure", err)
		}
	})

	t.Run("build target without a project name", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		writeProject(t, packageDir, `{"targets":{"build":{"executor":"@nx/angular:package"}}}`)
		if _, _, err := nodeBuildCommand(workspace, packageDir); err == nil || !strings.Contains(err.Error(), "without a project name") {
			t.Fatalf("error = %v, want the unnamed-project refusal", err)
		}
	})

	t.Run("yarn nx target", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		if err := os.WriteFile(filepath.Join(workspace, "yarn.lock"), []byte("# lock\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeProject(t, packageDir, `{"name":"core","targets":{"build":{}}}`)
		command, args, err := nodeBuildCommand(workspace, packageDir)
		if err != nil {
			t.Fatal(err)
		}
		if command != "yarn" || strings.Join(args, " ") != "nx build core" {
			t.Fatalf("command = %s %v, want yarn nx build core", command, args)
		}
	})

	t.Run("npm nx target", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		writeProject(t, packageDir, `{"name":"core","targets":{"build":{}}}`)
		command, args, err := nodeBuildCommand(workspace, packageDir)
		if err != nil {
			t.Fatal(err)
		}
		if command != "npm" || strings.Join(args, " ") != "exec nx -- build core" {
			t.Fatalf("command = %s %v, want npm exec nx -- build core", command, args)
		}
	})

	t.Run("unreadable project.json", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		if err := os.Mkdir(filepath.Join(packageDir, "project.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := nodeBuildCommand(workspace, packageDir); err == nil || !strings.Contains(err.Error(), "read") {
			t.Fatalf("error = %v, want an unreadable project.json reported", err)
		}
	})

	t.Run("malformed workspace manifest", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := nodeBuildCommand(workspace, packageDir); err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("error = %v, want a manifest parse failure", err)
		}
	})

	t.Run("no build script at all", func(t *testing.T) {
		t.Parallel()
		workspace, packageDir := newWorkspace(t)
		if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := nodeBuildCommand(workspace, packageDir); err == nil || !strings.Contains(err.Error(), "neither an Nx project build target nor a workspace build script") {
			t.Fatalf("error = %v, want the no-build-target refusal", err)
		}
	})
}

func TestLgCovBuiltDistLocatesAndReports(t *testing.T) {
	t.Parallel()
	t.Run("package dist wins", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		packageDir := filepath.Join(library, "libs", "core")
		dist := filepath.Join(packageDir, "dist")
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"core"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := builtDist(library, packageDir)
		if err != nil || got != dist {
			t.Fatalf("builtDist = %q, %v, want %q", got, err, dist)
		}
	})

	t.Run("library dist by relative package path", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		packageDir := filepath.Join(library, "libs", "core")
		dist := filepath.Join(library, "dist", "libs", "core")
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"core"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := builtDist(library, packageDir)
		if err != nil || got != dist {
			t.Fatalf("builtDist = %q, %v, want %q", got, err, dist)
		}
	})

	t.Run("library dist by package base name", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		packageDir := filepath.Join(library, "elsewhere", "core")
		dist := filepath.Join(library, "dist", "core")
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"core"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := builtDist(library, packageDir)
		if err != nil || got != dist {
			t.Fatalf("builtDist = %q, %v, want %q", got, err, dist)
		}
	})

	t.Run("a directory without a manifest still counts", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		packageDir := filepath.Join(library, "libs", "core")
		dist := filepath.Join(library, "dist", "libs", "core")
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := builtDist(library, packageDir)
		if err != nil || got != dist {
			t.Fatalf("builtDist = %q, %v, want the manifestless output directory %q", got, err, dist)
		}
	})

	t.Run("no output reports every candidate", func(t *testing.T) {
		t.Parallel()
		library := t.TempDir()
		packageDir := filepath.Join(library, "libs", "core")
		_, err := builtDist(library, packageDir)
		if err == nil || !strings.Contains(err.Error(), "produced no output") {
			t.Fatalf("error = %v, want the no-output report", err)
		}
	})

	t.Run("an unresolvable relative path falls back to the base name", func(t *testing.T) {
		t.Parallel()
		library := filepath.Join("relative-library")
		packageDir := filepath.Join(t.TempDir(), "core")
		if err := os.MkdirAll(packageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := builtDist(library, packageDir)
		if err == nil || !strings.Contains(err.Error(), filepath.Join("relative-library", "dist", "core")) {
			t.Fatalf("error = %v, want the base-name candidate in the report", err)
		}
	})
}
