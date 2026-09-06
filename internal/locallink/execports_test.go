package locallink

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	run("init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	return root
}

// REQ: batch-verification-is-keyed-to-a-tree-identity — the hash covers the
// working tree including modified and untracked files, so an uncommitted
// library still has an identity; ignored build output does not change it.
func TestContentHashCoversModifiedAndUntrackedFilesButNotIgnoredOnes(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	clean, dirty, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("a committed tree reported dirty")
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified, dirty, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("a modified tree reported clean")
	}
	if modified == clean {
		t.Fatal("a modification did not move the content hash")
	}

	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withUntracked, _, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if withUntracked == modified {
		t.Fatal("an untracked file did not move the content hash")
	}

	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "build.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withIgnored, _, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if withIgnored != withUntracked {
		t.Fatal("an ignored build output changed the source identity")
	}
}

func TestTrackedChangesIgnoresUntrackedArtefacts(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := git.TrackedChanges(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("tracked changes = %v; an untracked link artefact is not a tracked change", changes)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = git.TrackedChanges(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0] != "tracked.txt" {
		t.Fatalf("tracked changes = %v, want tracked.txt", changes)
	}
}

// The exclude goes into the worktree's own exclude file, never into the
// tracked .gitignore — and the excluded file must actually stop showing up as
// untracked, which is the only thing that proves the exclude landed where Git
// reads it.
func TestExcludePathUsesTheWorktreeExcludeFileAndNotTheTrackedGitignore(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()
	gitignoreBefore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := git.ExcludePath(ctx, root, "/go.work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := gitStatus(t, root)
	if strings.Contains(status, "go.work") {
		t.Fatalf("go.work is still reported by git status:\n%s", status)
	}
	gitignoreAfter, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gitignoreAfter) != string(gitignoreBefore) {
		t.Fatal("the tracked .gitignore was modified")
	}
	patterns, err := git.ExcludedPatterns(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(patterns, "/go.work") {
		t.Fatalf("exclude file = %v", patterns)
	}
	// Excluding the same pattern twice must not duplicate it.
	if err := git.ExcludePath(ctx, root, "/go.work"); err != nil {
		t.Fatal(err)
	}
	patterns, err = git.ExcludedPatterns(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	occurrences := 0
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "/go.work" {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("exclude file carries %d copies of /go.work", occurrences)
	}
}

func gitStatus(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "status", "--porcelain")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	return string(output)
}

// Link replaces a real installed package with a symlink and keeps the original
// aside, so Unlink restores exactly what was there rather than reinstalling.
func TestExecNodeLinkAndUnlinkRestoreTheInstalledPackage(t *testing.T) {
	consumer := t.TempDir()
	installed := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	ctx := context.Background()

	result, err := node.Link(ctx, consumer, "@acme/core", dist)
	if err != nil {
		t.Fatal(err)
	}
	if result.Previous == "" {
		t.Fatal("Link did not record where the installed package went")
	}
	info, err := os.Lstat(installed)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the node_modules entry is not a symlink: %v", err)
	}
	linked, err := os.ReadFile(filepath.Join(installed, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linked), "1.1.0-dev") {
		t.Fatalf("the link does not resolve to the built dist: %s", linked)
	}

	if err := node.Unlink(ctx, consumer, "@acme/core"); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(installed, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), `"version":"1.0.0"`) {
		t.Fatalf("undo did not restore the installed package: %s", restored)
	}
}

func TestExecNodeBuildRefusesWithoutAContentHash(t *testing.T) {
	node := ExecNode{CacheRoot: t.TempDir()}
	if _, err := node.Build(context.Background(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("a build with no content hash to key its cache reported success")
	}
}

func TestNodeBuildCommandSelectsThePackageNxTarget(t *testing.T) {
	workspace := t.TempDir()
	packageDir := filepath.Join(workspace, "libs", "extensions", "contactus", "runtime")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "pnpm-lock.yaml"), []byte("lockfileVersion: '9.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "project.json"), []byte(`{
  "name": "ext-contactus-runtime",
  "targets": {"build": {"executor": "@nx/angular:package"}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	command, args, err := nodeBuildCommand(workspace, packageDir)
	if err != nil {
		t.Fatal(err)
	}
	if command != "pnpm" || strings.Join(args, " ") != "exec nx build ext-contactus-runtime" {
		t.Fatalf("command = %s %v, want exact Nx project build", command, args)
	}
}

func TestNodeBuildCommandFallsBackToWorkspaceBuildScript(t *testing.T) {
	workspace := t.TempDir()
	packageDir := filepath.Join(workspace, "libs", "core")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "package.json"), []byte(`{"scripts":{"build":"tsc"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	command, args, err := nodeBuildCommand(workspace, packageDir)
	if err != nil {
		t.Fatal(err)
	}
	if command != "npm" || strings.Join(args, " ") != "run build" {
		t.Fatalf("command = %s %v, want workspace build script", command, args)
	}
}

// The build cache is keyed by content hash: the same hash reuses the build,
// and a moved hash rebuilds.
func TestExecNodeBuildCacheIsKeyedByContentHash(t *testing.T) {
	cache := t.TempDir()
	library := t.TempDir()
	packageDir := filepath.Join(library, "libs", "core")
	dist := filepath.Join(packageDir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: cache, ContentHash: "hash-one", Timeout: 30 * time.Second}
	// Seed the cache exactly as a successful build would, so the reuse path is
	// exercised without needing a Node toolchain on the test machine.
	seeded := filepath.Join(cache, node.ContentHash, buildCacheKey(node.ContentHash, packageDir))
	if err := os.MkdirAll(seeded, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seeded, buildMarkerName), []byte(dist+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := node.Build(context.Background(), library, packageDir)
	if err != nil {
		t.Fatalf("a cached build was not reused: %v", err)
	}
	if got != dist {
		t.Fatalf("cached dist = %q, want %q", got, dist)
	}
	if !strings.Contains(seeded, "hash-one") {
		t.Fatalf("the cache path is not keyed by the content hash: %s", seeded)
	}
	moved := ExecNode{CacheRoot: cache, ContentHash: "hash-two", Timeout: time.Second}
	if _, err := moved.Build(context.Background(), library, packageDir); err == nil {
		t.Fatal("a moved content hash reused a stale build instead of rebuilding")
	}
}

// MF-8. Under pnpm's default isolated store, node_modules/<pkg> IS a symlink
// into .pnpm/…. That symlink used to be deleted with no backup, so `--undo`
// left the consumer with no package at all. Its target is now recorded and
// re-created.
func TestExecNodeLinkAndUnlinkRestoreAPnpmSymlink(t *testing.T) {
	consumer := t.TempDir()
	store := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	// pnpm links with a RELATIVE target; preserving it verbatim is what makes
	// the restore correct if the tree is ever relocated.
	relative, err := filepath.Rel(filepath.Dir(target), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}

	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	ctx := context.Background()

	result, err := node.Link(ctx, consumer, "@acme/core", dist)
	if err != nil {
		t.Fatal(err)
	}
	if result.Previous == "" {
		t.Fatal("Link did not record where the existing symlink pointed")
	}
	linkedTarget, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(linkedTarget) != filepath.Dir(store) || !strings.HasSuffix(linkedTarget, ".wb-locallink-stage") {
		t.Fatalf("linked target = %q, want staged sibling in the installed pnpm peer context %s", linkedTarget, filepath.Dir(store))
	}
	if len(result.Artifacts) < 4 || !containsAll(result.Artifacts,
		"node_modules/@acme/core",
		"node_modules/@acme/core"+linkAppliedMarkerSuffix,
		filepath.ToSlash(filepath.Join("node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", ".core.wb-locallink-stage")),
		"node_modules/@acme/core"+linkSymlinkBackupSuffix,
	) {
		t.Fatalf("artifacts = %v, want target, marker, peer-context stage and backup", result.Artifacts)
	}
	published, err := os.ReadFile(filepath.Join(store, "package.json"))
	if err != nil || !strings.Contains(string(published), `"version":"1.0.0"`) {
		t.Fatalf("published package was mutated: %s (err %v)", published, err)
	}
	linked, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linked), "1.1.0-dev") {
		t.Fatalf("the link does not resolve to the built dist: %s", linked)
	}

	if err := node.Unlink(ctx, consumer, "@acme/core"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the package is not a symlink again after undo: %v", err)
	}
	restoredTarget, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if restoredTarget != relative {
		t.Fatalf("restored link points at %q, want the original %q", restoredTarget, relative)
	}
	restored, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		t.Fatalf("the published package is not reachable after undo: %v", err)
	}
	if !strings.Contains(string(restored), `"version":"1.0.0"`) {
		t.Fatalf("undo did not restore the published version: %s", restored)
	}
	// No bookkeeping left behind.
	if fileExists(target + linkSymlinkBackupSuffix) {
		t.Error("the link record survived undo")
	}
	if fileExists(linkAppliedMarkerPath(consumer, "@acme/core")) {
		t.Error("the applied-link marker survived undo")
	}
	if fileExists(linkedTarget) {
		t.Error("the staged peer-context package survived undo")
	}
}

func TestExecNodeLinksTransitivePnpmSiblingsAndRetriesAfterPartialFailure(t *testing.T) {
	consumer := t.TempDir()
	packages := []struct {
		name         string
		version      string
		dependencies string
	}{
		{name: "@acme/app", version: "1.0.0", dependencies: `"dependencies":{"@acme/core":"1.0.0"},"optionalDependencies":{"@acme/auth-core":"1.0.0"},"peerDependencies":{"@angular/core":"^18.0.0"}`},
		{name: "@acme/core", version: "1.0.0", dependencies: `"peerDependencies":{"@acme/auth-core":"1.0.0"}`},
		{name: "@acme/auth-core", version: "1.0.0", dependencies: `"peerDependencies":{"@angular/core":"^18.0.0"}`},
	}
	original := make(map[string]string, len(packages))
	installed := make(map[string]string, len(packages))
	for _, pkg := range packages {
		store := filepath.Join(consumer, "node_modules", ".pnpm", pnpmStoreKey(pkg.name, pkg.version), "node_modules", filepath.FromSlash(filepath.Dir(pkg.name)), filepath.Base(pkg.name))
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf(`{"name":%q,"version":%q,%s}`, pkg.name, pkg.version, pkg.dependencies)
		if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(consumer, "node_modules", filepath.FromSlash(pkg.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(filepath.Dir(target), store)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(relative, target); err != nil {
			t.Fatal(err)
		}
		original[pkg.name] = relative
		installed[pkg.name] = store
	}

	angular := filepath.Join(consumer, "node_modules", "@angular", "core")
	if err := os.MkdirAll(angular, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(angular, "package.json"), []byte(`{"name":"@angular/core","version":"18.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	dists := make(map[string]string, len(packages))
	for _, pkg := range packages {
		dist := t.TempDir()
		manifest := fmt.Sprintf(`{"name":%q,"version":"1.0.0-dev",%s}`, pkg.name, pkg.dependencies)
		if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		dists[pkg.name] = dist
		if _, err := node.Link(context.Background(), consumer, pkg.name, dist); err != nil {
			t.Fatalf("link %s: %v", pkg.name, err)
		}
	}

	stages := make(map[string]string, len(packages))
	for _, pkg := range packages {
		marker := linkAppliedMarkerPath(consumer, pkg.name)
		contents, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		stages[pkg.name] = strings.TrimSpace(string(contents))
	}
	conflict := filepath.Join(stages["@acme/core"], "node_modules", "@acme", "auth-core")
	if err := os.MkdirAll(filepath.Dir(conflict), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflict, []byte("unexpected"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := node.LinkSiblings(context.Background(), consumer, []string{"@acme/app", "@acme/core", "@acme/auth-core"}); err == nil {
		t.Fatal("sibling reconciliation succeeded despite a conflicting staged path")
	}
	if _, err := os.Lstat(filepath.Join(stages["@acme/app"], "node_modules", "@acme", "core")); !os.IsNotExist(err) {
		t.Fatalf("failed reconciliation partially created app sibling edge: %v", err)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}
	if err := node.LinkSiblings(context.Background(), consumer, []string{"@acme/app", "@acme/core", "@acme/auth-core"}); err != nil {
		t.Fatalf("retry sibling reconciliation: %v", err)
	}

	appCore := resolveNodePackage(t, stages["@acme/app"], "@acme/core")
	consumerCore := resolveNodePackage(t, consumer, "@acme/core")
	stagedCore := resolvePath(t, stages["@acme/core"])
	if appCore != consumerCore || appCore != stagedCore {
		t.Fatalf("app/core identity = %s, consumer/core = %s, staged core = %s", appCore, consumerCore, stages["@acme/core"])
	}
	coreAuth := resolveNodePackage(t, stages["@acme/core"], "@acme/auth-core")
	consumerAuth := resolveNodePackage(t, consumer, "@acme/auth-core")
	stagedAuth := resolvePath(t, stages["@acme/auth-core"])
	if coreAuth != consumerAuth || coreAuth != stagedAuth {
		t.Fatalf("core/auth identity = %s, consumer/auth = %s, staged auth = %s", coreAuth, consumerAuth, stages["@acme/auth-core"])
	}
	appAuth := resolveNodePackage(t, stages["@acme/app"], "@acme/auth-core")
	if appAuth != consumerAuth || appAuth != stagedAuth {
		t.Fatalf("app/auth identity = %s, consumer/auth = %s, staged auth = %s", appAuth, consumerAuth, stages["@acme/auth-core"])
	}
	if got := resolveNodePackage(t, stages["@acme/auth-core"], "@angular/core"); got != resolvePath(t, angular) {
		t.Fatalf("external peer identity = %s, want consumer-installed %s", got, angular)
	}

	for _, pkg := range packages {
		if err := node.Unlink(context.Background(), consumer, pkg.name); err != nil {
			t.Fatalf("undo %s: %v", pkg.name, err)
		}
		target := filepath.Join(consumer, "node_modules", filepath.FromSlash(pkg.name))
		got, err := os.Readlink(target)
		if err != nil || got != original[pkg.name] {
			t.Fatalf("undo %s restored %q, want %q (err %v)", pkg.name, got, original[pkg.name], err)
		}
		if _, err := os.Stat(filepath.Join(installed[pkg.name], "package.json")); err != nil {
			t.Fatalf("published %s disappeared after undo: %v", pkg.name, err)
		}
	}
	for _, pkg := range packages {
		if fileExists(linkAppliedMarkerPath(consumer, pkg.name)) || fileExists(stages[pkg.name]) {
			t.Fatalf("undo left recovery artefacts for %s", pkg.name)
		}
	}
}

func pnpmStoreKey(name, version string) string {
	return strings.Replace(name, "/", "+", 1) + "@" + version
}

func resolveNodePackage(t *testing.T, start, name string) string {
	t.Helper()
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
		if _, err := os.Stat(candidate); err == nil {
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				t.Fatalf("resolve %s from %s: %v", name, start, err)
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	t.Fatalf("could not resolve %s from %s", name, start)
	return ""
}

func resolvePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve path %s: %v", path, err)
	}
	return filepath.Clean(resolved)
}

func TestExecNodeLinkRejectsInstalledPackageSymlinkOutsideConsumer(t *testing.T) {
	consumer := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash"}
	if _, err := node.Link(context.Background(), consumer, "@acme/core", dist); err == nil || !strings.Contains(err.Error(), "outside consumer npm workspace") {
		t.Fatalf("error = %v, want symlink-escape refusal", err)
	}
	if got, err := os.Readlink(target); err != nil || got != outside {
		t.Fatalf("published link changed to %q (err %v)", got, err)
	}
}

func TestExecNodeLinkPreservesUnexpectedStageAndRecoveryArtifacts(t *testing.T) {
	newConsumer := func(t *testing.T) (consumer, target, stage string) {
		t.Helper()
		consumer = t.TempDir()
		store := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		target = filepath.Join(consumer, "node_modules", "@acme", "core")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(filepath.Dir(target), store)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(relative, target); err != nil {
			t.Fatal(err)
		}
		stage = filepath.Join(filepath.Dir(store), ".core.wb-locallink-stage")
		return consumer, target, stage
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{}

	t.Run("stage", func(t *testing.T) {
		consumer, target, stage := newConsumer(t)
		if err := os.MkdirAll(stage, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(stage, "keep.txt")
		if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := node.Link(context.Background(), consumer, "@acme/core", dist); err == nil || !strings.Contains(err.Error(), "without replacing existing data") {
			t.Fatalf("error = %v, want exclusive stage-claim refusal", err)
		}
		if !fileExists(sentinel) {
			t.Fatal("failed Link deleted the pre-existing staged directory")
		}
		if contents, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(contents), `"version":"1.0.0"`) {
			t.Fatalf("published package changed: %s (err %v)", contents, err)
		}
	})

	t.Run("recovery artifact", func(t *testing.T) {
		consumer, target, stage := newConsumer(t)
		backup := target + linkSymlinkBackupSuffix
		if err := os.WriteFile(backup, []byte("do-not-replace\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := node.Link(context.Background(), consumer, "@acme/core", dist); err == nil || !strings.Contains(err.Error(), "unexpected local-link recovery artifact") {
			t.Fatalf("error = %v, want recovery-artifact refusal", err)
		}
		if contents, err := os.ReadFile(backup); err != nil || string(contents) != "do-not-replace\n" {
			t.Fatalf("recovery evidence changed: %q (err %v)", contents, err)
		}
		if fileExists(stage) {
			t.Fatal("Link claimed a stage despite pre-existing recovery evidence")
		}
		if contents, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(contents), `"version":"1.0.0"`) {
			t.Fatalf("published package changed: %s (err %v)", contents, err)
		}
	})
}

func TestCopyBuiltPackageRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "package.json"), []byte(`{"name":"@acme/core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(t.TempDir(), "dist")
	if err := os.Symlink(outside, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if err := copyBuiltPackage(symlinkRoot, filepath.Join(t.TempDir(), "stage")); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlinked dist error = %v, want a source-boundary refusal", err)
	}

	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"@acme/core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "package.json"), filepath.Join(source, "escaped.json")); err != nil {
		t.Fatal(err)
	}
	if err := copyBuiltPackage(source, filepath.Join(t.TempDir(), "stage")); err == nil || !strings.Contains(err.Error(), "unsupported symlink") {
		t.Fatalf("nested symlink error = %v, want an entry-boundary refusal", err)
	}
}

func TestUnlinkRejectsMarkerForAnotherStagedPath(t *testing.T) {
	consumer := t.TempDir()
	marker := linkAppliedMarkerPath(consumer, "@acme/core")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(consumer, "node_modules", ".another.wb-locallink-stage")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(victim+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{}
	if err := node.Unlink(context.Background(), consumer, "@acme/core"); err == nil || !strings.Contains(err.Error(), "invalid staged path") {
		t.Fatalf("error = %v, want marker/path identity refusal", err)
	}
	if !fileExists(filepath.Join(victim, "keep.txt")) {
		t.Fatal("undo removed a stage named by an invalid marker")
	}
}

// SHOULD-FIX (b). A consumer with no lockfile has no frozen baseline to prove,
// so the check reports that it could not run rather than passing silently.
func TestFrozenInstallWithNoLockfileIsReportedAsSkippedNotPassed(t *testing.T) {
	node := ExecNode{Timeout: 30 * time.Second}
	err := node.FrozenInstall(context.Background(), t.TempDir())
	skipped, wasSkipped := Skipped(err)
	if !wasSkipped {
		t.Fatalf("err = %v, want an explicit skipped check — a silent nil is indistinguishable from a pass", err)
	}
	if skipped.Check != "frozen-install" || !strings.Contains(skipped.Reason, "no lockfile baseline") {
		t.Fatalf("skipped = %#v, want it to name the check and why it could not run", skipped)
	}
}

// SHOULD-FIX (c). Restoring a symlink whose target was pruned while the link
// was live must be flagged: reporting success would say the published package
// is back when it is not.
func TestUnlinkFlagsARestoredSymlinkThatDangles(t *testing.T) {
	consumer := t.TempDir()
	store := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(target), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	ctx := context.Background()
	if _, err := node.Link(ctx, consumer, "@acme/core", dist); err != nil {
		t.Fatal(err)
	}
	// The store entry is pruned while the link is live — one `pnpm install`
	// during a stream is enough.
	if err := os.RemoveAll(filepath.Join(consumer, "node_modules", ".pnpm")); err != nil {
		t.Fatal(err)
	}
	err = node.Unlink(ctx, consumer, "@acme/core")
	if err == nil {
		t.Fatal("undo reported success while the restored link dangles")
	}
	if !strings.Contains(err.Error(), "dangling") || !strings.Contains(err.Error(), "re-install") {
		t.Fatalf("error = %v, want it to say the link dangles and how to recover", err)
	}
}
