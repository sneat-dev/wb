package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestWTCoreCovBranchConfigRejectsEveryMalformedShape asserts the layered
// configuration parser refuses each documented malformed shape and accepts a
// well-formed document carrying both settings.
func TestWTCoreCovBranchConfigRejectsEveryMalformedShape(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		wantErr  string
		wantRoot string
	}{
		{name: "unsupported version", contents: "worktrees:\n  branch_prefix: wb/\n", wantErr: "has version 0"},
		{name: "unknown field", contents: "version: 1\nunexpected: true\n", wantErr: "parse worktrees config"},
		{name: "multiple documents", contents: "version: 1\n---\nversion: 1\n", wantErr: "multiple YAML documents"},
		{name: "prefix with surrounding whitespace", contents: "version: 1\nworktrees:\n  branch_prefix: ' wb/'\n", wantErr: "surrounding whitespace"},
		{name: "prefix without trailing slash", contents: "version: 1\nworktrees:\n  branch_prefix: wb\n", wantErr: "must end with /"},
		{name: "prefix that is not a valid branch", contents: "version: 1\nworktrees:\n  branch_prefix: 'wb/../'\n", wantErr: "invalid branch_prefix"},
		{name: "root with surrounding whitespace", contents: "version: 1\nworktrees:\n  root: ' /tmp/x'\n", wantErr: "root must not have surrounding whitespace"},
		{name: "empty root", contents: "version: 1\nworktrees:\n  root: ''\n", wantErr: "root must not be empty"},
		{name: "oversize document", contents: strings.Repeat("#", maxBranchConfigSize+1), wantErr: "exceeds"},
		{name: "well formed", contents: "version: 1\nworktrees:\n  branch_prefix: wb/\n  root: /tmp/wt\n", wantRoot: "/tmp/wt"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config, found, err := parseBranchConfig("test.yaml", []byte(testCase.contents))
			if testCase.wantErr == "" {
				if err != nil || !found {
					t.Fatalf("well-formed config = %#v, found=%v, err=%v", config, found, err)
				}
				if config.Worktrees.Root == nil || *config.Worktrees.Root != testCase.wantRoot {
					t.Fatalf("root = %#v, want %q", config.Worktrees.Root, testCase.wantRoot)
				}
				if config.Worktrees.BranchPrefix == nil || *config.Worktrees.BranchPrefix != "wb/" {
					t.Fatalf("prefix = %#v", config.Worktrees.BranchPrefix)
				}
				return
			}
			if err == nil {
				t.Fatalf("malformed config was accepted: %#v", config)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error %q does not mention %q", err, testCase.wantErr)
			}
			if found {
				t.Fatal("a refused config must not be reported as found")
			}
		})
	}

	// A document that is a YAML error rather than a shape error is refused too.
	if _, _, err := parseBranchConfig("test.yaml", []byte("version: [1\n")); err == nil {
		t.Fatal("unparseable YAML was accepted")
	}
}

// TestWTCoreCovLoadBranchConfigFileBoundaries asserts the on-disk loader
// distinguishes absence, a non-regular file, an oversize file, and a resolvable
// symlink to a regular file.
func TestWTCoreCovLoadBranchConfigFileBoundaries(t *testing.T) {
	root := t.TempDir()
	absent, found, err := loadBranchConfigFile(filepath.Join(root, "absent.yaml"))
	if err != nil || found {
		t.Fatalf("absent config = %#v, found=%v, err=%v", absent, found, err)
	}
	broken := filepath.Join(root, "broken.yaml")
	if err := os.Symlink(filepath.Join(root, "nowhere.yaml"), broken); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadBranchConfigFile(broken); err != nil || found {
		t.Fatalf("dangling symlink = found=%v, err=%v", found, err)
	}
	directory := filepath.Join(root, "directory.yaml")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBranchConfigFile(directory); err == nil {
		t.Fatal("a directory was accepted as a config file")
	} else if !strings.Contains(err.Error(), "must resolve to a regular file") {
		t.Fatalf("error %q does not name the file-type rule", err)
	}
	oversize := filepath.Join(root, "oversize.yaml")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("#", maxBranchConfigSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBranchConfigFile(oversize); err == nil {
		t.Fatal("an oversize config was accepted")
	}
	real := filepath.Join(root, "real.yaml")
	if err := os.WriteFile(real, []byte("version: 1\nworktrees:\n  branch_prefix: wb/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	config, found, err := loadBranchConfigFile(link)
	if err != nil || !found || config.Worktrees.BranchPrefix == nil || *config.Worktrees.BranchPrefix != "wb/" {
		t.Fatalf("symlinked config = %#v, found=%v, err=%v", config, found, err)
	}
}

// TestWTCoreCovResolveSharedWorktreesRootExpandsAndValidates asserts the shared
// root is expanded, must be absolute, and is resolved through a symlink to its
// physical path.
func TestWTCoreCovResolveSharedWorktreesRootExpandsAndValidates(t *testing.T) {
	if _, err := resolveSharedWorktreesRoot(""); err == nil {
		t.Fatal("an empty root was accepted")
	} else if !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("error %q does not name the empty rule", err)
	}
	if _, err := resolveSharedWorktreesRoot("relative/path"); err == nil {
		t.Fatal("a relative root was accepted")
	} else if !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("error %q does not name the absolute-path rule", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if got, err := resolveSharedWorktreesRoot("~"); err != nil || got != home {
		t.Fatalf("root ~ = %q, err=%v, want %q", got, err, home)
	}
	if got, err := resolveSharedWorktreesRoot("~/trees"); err != nil || got != filepath.Join(home, "trees") {
		t.Fatalf("root ~/trees = %q, err=%v", got, err)
	}

	physicalRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(physicalRoot, "trees")
	if err := os.Mkdir(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveSharedWorktreesRoot(alias); err != nil || got != physical {
		t.Fatalf("symlinked root = %q, err=%v, want %q", got, err, physical)
	}
	// A path that does not exist yet is resolved through its nearest existing
	// ancestor rather than refused.
	if got, err := resolveSharedWorktreesRoot(filepath.Join(physical, "not-yet")); err != nil || got != filepath.Join(physical, "not-yet") {
		t.Fatalf("future root = %q, err=%v", got, err)
	}
}

// TestWTCoreCovPlacementAndNamingRejections asserts the placement/naming policy
// surfaces each refusal instead of deriving a branch or path that violates it.
func TestWTCoreCovPlacementAndNamingRejections(t *testing.T) {
	if _, err := ResolveUserWorktreePlacement(t.TempDir(), "relative/canonical"); err == nil {
		t.Fatal("a relative canonical path was accepted")
	}
	placement := WorktreePlacement{Root: "/trees", RepositoryLocal: true}
	if _, err := placement.Path("bad task", "acme/app"); err == nil {
		t.Fatal("an unsafe task name was accepted")
	}
	if _, err := placement.Path("task", "unqualified"); err == nil {
		t.Fatal("an unqualified repository slug was accepted")
	}
	local, err := placement.Path("task", "acme/app")
	if err != nil || local != filepath.Join("/trees", "task") {
		t.Fatalf("repository-local path = %q, err=%v", local, err)
	}
	shared := WorktreePlacement{Root: "/trees"}
	sharedPath, err := shared.Path("task", "acme/app")
	if err != nil || sharedPath != filepath.Join("/trees", "task", "acme", "app") {
		t.Fatalf("shared path = %q, err=%v", sharedPath, err)
	}

	ctx := context.Background()
	if _, err := deriveBranchName(ctx, branchNamingOptions{ExactBranchChosen: true, CLIPrefixChosen: true}); err == nil {
		t.Fatal("--branch together with --branch-prefix was accepted")
	}
	if _, err := deriveBranchName(ctx, branchNamingOptions{ExactBranchChosen: true, ExactBranch: " "}); err == nil {
		t.Fatal("an empty explicit branch was accepted")
	}
	if _, err := deriveBranchName(ctx, branchNamingOptions{CLIPrefixChosen: true, CLIPrefix: "wb", Task: "task"}); err == nil {
		t.Fatal("a prefix without a trailing slash was accepted")
	}
	if _, err := deriveBranchName(ctx, branchNamingOptions{CLIPrefixChosen: true, CLIPrefix: "wb/..", Task: "task"}); err == nil {
		t.Fatal("an invalid branch prefix was accepted")
	}
	derived, err := deriveBranchName(ctx, branchNamingOptions{CLIPrefixChosen: true, CLIPrefix: "wb/", Task: "task"})
	if err != nil || derived != "wb/task" {
		t.Fatalf("derived branch = %q, err=%v", derived, err)
	}
	if _, err := deriveBranchName(ctx, branchNamingOptions{ExactBranchChosen: true, ExactBranch: "main", Base: "main"}); err == nil {
		t.Fatal("a branch equal to the base was accepted")
	}
	if _, err := deriveBranchName(ctx, branchNamingOptions{Canonical: nil, BaseRevision: "not-a-sha"}); err == nil {
		t.Fatal("branch policy without a fetched base revision was accepted")
	}
}

// TestWTCoreCovAppendConfiguredSharedWorktreesLayout asserts the configured
// shared root is appended once and never duplicates a layout root that is
// already present.
func TestWTCoreCovAppendConfiguredSharedWorktreesLayout(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared-trees")
	// Absent configuration leaves the layouts untouched.
	layouts := []wbhome.Layout{{WorktreesRoot: filepath.Join(root, "one")}}
	unchanged, err := appendConfiguredSharedWorktreesLayout(layouts)
	if err != nil || len(unchanged) != 1 {
		t.Fatalf("absent config = %#v, err=%v", unchanged, err)
	}
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+shared+"\n")
	appended, err := appendConfiguredSharedWorktreesLayout(layouts)
	if err != nil || len(appended) != 2 || filepath.Clean(appended[1].WorktreesRoot) != filepath.Clean(shared) {
		t.Fatalf("appended = %#v, err=%v", appended, err)
	}
	again, err := appendConfiguredSharedWorktreesLayout(appended)
	if err != nil || len(again) != 2 {
		t.Fatalf("duplicate append = %#v, err=%v", again, err)
	}
	// A malformed root inside the configuration is surfaced, not ignored.
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: relative\n")
	if _, err := appendConfiguredSharedWorktreesLayout(layouts); err == nil {
		t.Fatal("a malformed configured root was ignored")
	}
}

// TestWTCoreCovRemoveEmptyTaskDirectoryBoundaries asserts the retirement helper
// refuses every handle it cannot prove, and retires an empty held directory.
func TestWTCoreCovRemoveEmptyTaskDirectoryBoundaries(t *testing.T) {
	if removeEmptyTaskDirectory(nil) {
		t.Fatal("a nil cleanup task was accepted")
	}
	if removeEmptyTaskDirectory(&cleanupTaskHandle{}) {
		t.Fatal("a task handle without descriptors was accepted")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktrees, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worktrees.Close() }()
	// A task path whose base is not a safe segment cannot be retired by name.
	if removeEmptyTaskDirectory(&cleanupTaskHandle{worktrees: worktrees, task: worktrees, taskPath: root + "/"}) {
		t.Fatal("an unsafe task directory name was retired")
	}
	// A directory that no longer matches its held descriptor is left alone.
	path := filepath.Join(root, "held")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if removeEmptyTaskDirectory(&cleanupTaskHandle{worktrees: worktrees, task: held, taskPath: path}) {
		t.Fatal("a replaced task directory was retired")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the replacement was removed: %v", err)
	}

	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	emptyHandle, err := os.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = emptyHandle.Close() }()
	if !removeEmptyTaskDirectory(&cleanupTaskHandle{worktrees: worktrees, task: emptyHandle, taskPath: empty}) {
		t.Fatal("an empty held task directory was not retired")
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("the empty task directory remains: %v", err)
	}
}

// TestWTCoreCovEmptyTaskNamespacesSelectsAndReports asserts discovery skips
// everything that is not an empty task namespace and marks a filtered match
// ineligible rather than acting outside the selection.
func TestWTCoreCovEmptyTaskNamespacesSelectsAndReports(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), ".wb")

	makeTask := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	makeTask("empty-one")
	makeTask("empty-two")
	makeTask("live-namespace")
	occupied := makeTask("occupied")
	if err := os.WriteFile(filepath.Join(occupied, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	makeTask(".hidden")
	makeTask("bad name")
	if err := os.WriteFile(filepath.Join(root, "stray-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	layouts := []wbhome.Layout{{WorktreesRoot: root}, {WorktreesRoot: root}, {WorktreesRoot: filepath.Join(root, "absent")}}
	artifacts, err := emptyTaskNamespaces(layouts, map[string]bool{"empty-one": true, "empty-two": true}, "", home, map[string]bool{"live-namespace": true})
	if err != nil {
		t.Fatalf("emptyTaskNamespaces: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifacts = %#v, want the two empty namespaces", artifacts)
	}
	for _, artifact := range artifacts {
		if artifact.Kind != lifecycleArtifactKindTaskNamespace || !artifact.Eligible || artifact.State != taskNamespaceEmptyState {
			t.Fatalf("artifact = %#v", artifact)
		}
	}
	filtered, err := emptyTaskNamespaces(layouts, map[string]bool{"empty-one": true}, "acme", home, nil)
	if err != nil {
		t.Fatalf("filtered discovery: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Eligible || !strings.Contains(filtered[0].Reason, "--filter") {
		t.Fatalf("filtered artifacts = %#v", filtered)
	}
	// A worktrees root that is a regular file is a hard read failure.
	if _, err := emptyTaskNamespaces([]wbhome.Layout{{WorktreesRoot: filepath.Join(root, "stray-file")}}, map[string]bool{"stray-file": true}, "", home, nil); err == nil {
		t.Fatal("a non-directory worktrees root was accepted")
	}
}

// TestWTCoreCovRetireEmptyTaskNamespacesAppliesOnlyWhatItCanProve asserts the
// apply path honors eligibility, reports a namespace that stopped being empty,
// and retires a genuinely empty one.
func TestWTCoreCovRetireEmptyTaskNamespacesAppliesOnlyWhatItCanProve(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notEmpty := filepath.Join(root, "not-empty")
	if err := os.Mkdir(notEmpty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notEmpty, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "retire-me")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "already-gone")

	artifacts := []LifecycleArtifact{
		{Task: "not-empty", WorktreesRoot: root, Path: notEmpty, Kind: "other_kind", Eligible: true},
		{Task: "not-empty", WorktreesRoot: root, Path: notEmpty, Kind: lifecycleArtifactKindTaskNamespace, Eligible: false},
		{Task: "already-gone", WorktreesRoot: root, Path: missing, Kind: lifecycleArtifactKindTaskNamespace, Eligible: true},
		{Task: "not-empty", WorktreesRoot: root, Path: notEmpty, Kind: lifecycleArtifactKindTaskNamespace, Eligible: true},
		{Task: "retire-me", WorktreesRoot: root, Path: empty, Kind: lifecycleArtifactKindTaskNamespace, Eligible: true},
	}
	retireEmptyTaskNamespaces(artifacts)
	if artifacts[0].Applied || artifacts[1].Applied {
		t.Fatalf("an ineligible or foreign artifact was applied: %#v", artifacts[:2])
	}
	if !artifacts[2].Applied {
		t.Fatal("an already-retired namespace must be reported as applied")
	}
	if artifacts[3].Applied || !strings.Contains(artifacts[3].Reason, "no longer empty") {
		t.Fatalf("a namespace that is not empty = %#v", artifacts[3])
	}
	if !artifacts[4].Applied {
		t.Fatalf("an empty namespace was not retired: %#v", artifacts[4])
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("the empty namespace remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(notEmpty, "keep.txt")); err != nil {
		t.Fatalf("retirement removed content: %v", err)
	}
}

// TestWTCoreCovResidueHelpersStayAnchored asserts the descriptor-anchored
// residue walk removes nested read-only content by granting owner write to the
// exact directory it holds, refuses a directory it cannot search, and refuses a
// path that does not exist when asked.
func TestWTCoreCovResidueHelpersStayAnchored(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	rootHandle, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootHandle.Close() }()
	if err := removeDirectoryContentsAt(rootHandle, root, 0); err != nil {
		t.Fatalf("removeDirectoryContentsAt: %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("root after removal = %#v, err=%v", entries, err)
	}

	// The depth bound refuses rather than recursing forever.
	if err := removeDirectoryContentsAt(rootHandle, root, residueRemovalMaxDepth); err == nil {
		t.Fatal("the residue depth bound was not enforced")
	}
	// A directory WB cannot search is refused with the remedy named.
	unsearchable := filepath.Join(root, "unsearchable")
	if err := os.Mkdir(unsearchable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unsearchable, "inner"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(unsearchable, "inner"), 0o700) })
	if err := removeDirectoryContentsAt(rootHandle, root, 0); err == nil {
		t.Fatal("an unsearchable residue directory was accepted")
	} else if !strings.Contains(err.Error(), "denies WB the read and search permission") {
		t.Fatalf("error %q does not name the permission remedy", err)
	}
	if err := os.Chmod(filepath.Join(unsearchable, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(unsearchable); err != nil {
		t.Fatal(err)
	}

	// A closed descriptor cannot be listed.
	closed, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryEntryNames(closed, root); err == nil {
		t.Fatal("a closed descriptor was listed")
	}
	// A name that vanished before the unlink is not an error.
	if err := removeResidueEntry(rootHandle, root, "already-gone", 0); err != nil {
		t.Fatalf("removing a vanished entry = %v", err)
	}

	// A nil handle is refused, and a path that is already gone needs no work.
	if err := removeWorktreeResidue(nil); err == nil {
		t.Fatal("a nil cleanup worktree handle was accepted")
	}
	removed, err := removeUnregisteredWorktreeResidue(nil, filepath.Join(root, "absent"))
	if err != nil || removed {
		t.Fatalf("absent residue = %v, err=%v", removed, err)
	}
	// A canonical directory that does not exist cannot be inspected.
	if _, err := worktreeStillRegistered(ctx, filepath.Join(root, "no-repository"), filepath.Join(root, "no-worktree")); err == nil {
		t.Fatal("an absent canonical repository was accepted")
	}
}
