package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestConfigClaimsCoverageBatchPlacementPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	if _, err := (WorktreePlacement{Root: root, relative: "../escape"}).Path("task", "acme/app"); err == nil {
		t.Fatal("unsafe canonical relative address was accepted")
	}
	if got, err := (WorktreePlacement{Root: root, RepositoryLocal: true}).Path("task", "acme/app"); err != nil || got != filepath.Join(root, "task") {
		t.Fatalf("repository-local placement = %q/%v", got, err)
	}

	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePlacementPath(loop); err == nil {
		t.Fatal("symlink loop placement was accepted")
	}
	if _, _, err := canonicalPathAddress(root, loop); err == nil {
		t.Fatal("symlink loop canonical address was accepted")
	}
	if _, err := canonicalRelativeAddress(ctx, root, loop); err == nil {
		t.Fatal("symlink loop canonical relative address was accepted")
	}

	if host := canonicalStoreHost(ctx, filepath.Join(root, "missing")); host != "" {
		t.Fatalf("missing canonical store host = %q", host)
	}
	if _, err := ResolveWorktreePlacement(ctx, root, filepath.Join(root, "missing"), strings.Repeat("a", 40)); err == nil {
		t.Fatal("missing canonical placement was accepted")
	}
	if _, err := (userStorePolicy{CentralRoot: filepath.Join(root, "store")}).placement(ctx, root, loop); err == nil {
		t.Fatal("central policy accepted a symlink-loop canonical path")
	}

	if _, err := deriveBranchName(ctx, branchNamingOptions{
		Task: "task", Base: "main", CLIPrefixChosen: true, CLIPrefix: "bad..prefix/",
	}); err == nil {
		t.Fatal("invalid explicit branch prefix was accepted")
	}
	if _, err := configuredBranchPrefix(ctx, nil, strings.Repeat("a", 40)); err == nil {
		t.Fatal("nil canonical branch policy was accepted")
	}
	if _, err := configuredWorktreePlacement(ctx, root, nil, strings.Repeat("a", 40)); err == nil {
		t.Fatal("nil canonical worktree placement was accepted")
	}

	if _, err := resolveSharedWorktreesRoot(""); err == nil {
		t.Fatal("empty shared worktree root was accepted")
	}
	if _, err := resolveSharedWorktreesRoot("relative/root"); err == nil {
		t.Fatal("relative shared worktree root was accepted")
	}
	shared, err := resolveSharedWorktreesRoot(filepath.Join(root, "missing", "store"))
	if err != nil || shared != filepath.Join(root, "missing", "store") {
		t.Fatalf("missing shared root = %q/%v", shared, err)
	}
}

//nolint:paralleltest // mutates XDG_CONFIG_HOME for machine-local policy discovery.
func TestConfigClaimsCoverageBatchUserConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath, err := defaultWorktreesConfigPath()
	if err != nil || configPath != filepath.Join(configHome, "wb", "worktrees.yaml") {
		t.Fatalf("default config path = %q/%v", configPath, err)
	}
	if config, found, path, err := configuredUserWorktreesConfig(); err != nil || found || path != configPath || config.Version != 0 {
		t.Fatalf("missing user config = %#v/%t/%q/%v", config, found, path, err)
	}
	baseLayouts := []wbhome.Layout{{WorktreesRoot: filepath.Join(t.TempDir(), "existing")}}
	if layouts, err := appendConfiguredSharedWorktreesLayout(baseLayouts); err != nil || len(layouts) != 1 {
		t.Fatalf("layouts without config = %#v/%v", layouts, err)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  root: relative\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUserStorePolicy(t.TempDir()); err == nil {
		t.Fatal("relative configured store root was accepted")
	}
	if _, err := appendConfiguredSharedWorktreesLayout(baseLayouts); err == nil {
		t.Fatal("relative configured layout root was accepted")
	}

	configuredRoot := filepath.Join(t.TempDir(), "shared")
	contents := "version: 1\nworktrees:\n  root: " + configuredRoot + "\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := resolveUserStorePolicy(t.TempDir())
	if err != nil || policy.CentralRoot != configuredRoot {
		t.Fatalf("configured user policy = %#v/%v", policy, err)
	}
	layouts, err := appendConfiguredSharedWorktreesLayout(baseLayouts)
	if err != nil || len(layouts) != 2 || layouts[1].WorktreesRoot != configuredRoot {
		t.Fatalf("configured layouts = %#v/%v", layouts, err)
	}
	layouts, err = appendConfiguredSharedWorktreesLayout(layouts)
	if err != nil || len(layouts) != 2 {
		t.Fatalf("duplicate configured layout = %#v/%v", layouts, err)
	}
}

//nolint:paralleltest // mutates XDG_CONFIG_HOME for machine-local policy discovery.
func TestConfigClaimsCoverageBatchBrokenUserConfig(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDirectory := filepath.Join(configHome, "wb")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDirectory, "worktrees.yaml")
	if err := os.Symlink(configPath, configPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := configuredUserWorktreesConfig(); err == nil {
		t.Fatal("symlink-loop user config was accepted")
	}
	if _, err := appendConfiguredSharedWorktreesLayout(nil); err == nil {
		t.Fatal("symlink-loop layout config was accepted")
	}
	if _, err := configuredBranchPrefix(context.Background(), nil, strings.Repeat("a", 40)); err == nil {
		t.Fatal("symlink-loop branch config was accepted")
	}
}

//nolint:paralleltest // mutates HOME and XDG_CONFIG_HOME for default-path discovery.
func TestConfigClaimsCoverageBatchDefaultHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	path, err := defaultWorktreesConfigPath()
	if err != nil || path != filepath.Join(home, ".config", "wb", "worktrees.yaml") {
		t.Fatalf("home config path = %q/%v", path, err)
	}
	root, err := resolveSharedWorktreesRoot("~/shared-worktrees")
	if err != nil || root != filepath.Join(home, "shared-worktrees") {
		t.Fatalf("home-relative shared root = %q/%v", root, err)
	}
	if _, err := resolveUserStorePolicy(string([]byte{'b', 'a', 'd', 0})); err == nil {
		t.Fatal("invalid projects root was accepted as central store policy")
	}
}

func TestConfigClaimsCoverageBatchConfigFileFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.yaml")
	if _, found, err := loadBranchConfigFile(missing); err != nil || found {
		t.Fatalf("missing config = %t/%v", found, err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBranchConfigFile(directory); err == nil {
		t.Fatal("directory config was accepted")
	}
	loop := filepath.Join(root, "loop.yaml")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBranchConfigFile(loop); err == nil {
		t.Fatal("symlink-loop config was accepted")
	}
	large := filepath.Join(root, "large.yaml")
	if err := os.WriteFile(large, make([]byte, maxBranchConfigSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadBranchConfigFile(large); err == nil {
		t.Fatal("oversized config was accepted")
	}

	for name, contents := range map[string]string{
		"store whitespace": "version: 1\nworktrees:\n  store: ' central '\n",
		"bad archive":      "version: 1\nretirement:\n  archive_repository: '../bad'\n",
		"bad org":          "version: 1\nretirement:\n  organizations:\n    '..': {}\n",
		"bad org archive":  "version: 1\nretirement:\n  organizations:\n    acme:\n      archive_repository: '../bad'\n",
		"bad second doc":   "version: 1\n---\n- [unterminated\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := parseBranchConfig(name, []byte(contents)); err == nil {
				t.Fatalf("invalid config %q was accepted", name)
			}
		})
	}
}

//nolint:paralleltest // mutates XDG_CONFIG_HOME while exercising layered repository policy.
func TestConfigClaimsCoverageBatchRepositoryPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	projectsRoot := t.TempDir()
	canonicalPath := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(canonicalPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)

	head := strings.Repeat("a", 40)
	blob := strings.Repeat("b", 40)
	policyKind := "absent"
	var policy []byte
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
		switch args[0] {
		case "ls-tree":
			if args[2] == strings.Repeat("f", 40) {
				return nil, errors.New("missing revision")
			}
			switch policyKind {
			case "absent":
				return nil, nil
			case "symlink":
				return []byte("120000 blob " + blob + "\t.wb/worktrees.yaml\n"), nil
			default:
				return []byte("100644 blob " + blob + "\t.wb/worktrees.yaml\n"), nil
			}
		case "cat-file":
			return []byte(strconv.Itoa(len(policy)) + "\n"), nil
		case "show":
			return append([]byte(nil), policy...), nil
		default:
			return nil, errors.New("unexpected policy query")
		}
	})
	if contents, found, err := repositoryBranchConfigAt(ctx, canonical, head); err != nil || found || contents != nil {
		t.Fatalf("absent repository config = %q/%t/%v", contents, found, err)
	}
	if config, found, err := validatedRepositoryBranchConfig(ctx, canonical, head, "user-config.yaml"); err != nil || found || config.Version != 0 {
		t.Fatalf("absent validated repository config = %#v/%t/%v", config, found, err)
	}
	if _, _, err := repositoryBranchConfigAt(ctx, canonical, strings.Repeat("f", 40)); err == nil {
		t.Fatal("missing repository revision was accepted")
	}
	if _, _, err := validatedRepositoryBranchConfig(ctx, canonical, strings.Repeat("f", 40), "user-config.yaml"); err == nil {
		t.Fatal("shared repository policy validator accepted a missing revision")
	}
	placement, err := ResolveWorktreePlacement(ctx, projectsRoot, canonicalPath, head)
	if err != nil || placement.Root == "" {
		t.Fatalf("resolved worktree placement = %#v/%v", placement, err)
	}

	policyKind = "regular"
	policy = []byte("version: 1\nworktrees:\n  root: /tmp/forbidden\n")
	policyCommit := strings.Repeat("c", 40)
	contents, found, err := repositoryBranchConfigAt(ctx, canonical, policyCommit)
	if err != nil || !found || !strings.Contains(string(contents), "forbidden") {
		t.Fatalf("repository policy = %q/%t/%v", contents, found, err)
	}
	if _, err := configuredBranchPrefix(ctx, canonical, policyCommit); err == nil {
		t.Fatal("repository placement override influenced branch policy")
	}
	if _, err := configuredWorktreePlacement(ctx, projectsRoot, canonical, policyCommit); err == nil {
		t.Fatal("repository placement override was accepted")
	}
	if _, _, err := validatedRepositoryBranchConfig(ctx, canonical, policyCommit, "user-config.yaml"); err == nil {
		t.Fatal("shared repository policy validator accepted a placement override")
	}

	policy = []byte("version: 1\nworktrees:\n  branch_prefix: repo/\n")
	prefixCommit := strings.Repeat("d", 40)
	config, found, err := validatedRepositoryBranchConfig(ctx, canonical, prefixCommit, "user-config.yaml")
	if err != nil || !found || config.Worktrees.BranchPrefix == nil || *config.Worktrees.BranchPrefix != "repo/" {
		t.Fatalf("validated repository config = %#v/%t/%v", config, found, err)
	}
	if prefix, err := configuredBranchPrefix(ctx, canonical, prefixCommit); err != nil || prefix != "repo/" {
		t.Fatalf("repository branch prefix = %q/%v", prefix, err)
	}

	policyKind = "symlink"
	symlinkCommit := strings.Repeat("e", 40)
	if _, _, err := repositoryBranchConfigAt(ctx, canonical, symlinkCommit); err == nil {
		t.Fatal("repository symlink policy was accepted")
	}

	policyKind = "regular"
	policy = []byte("version: [malformed\n")
	malformedCommit := strings.Repeat("9", 40)
	if _, _, err := validatedRepositoryBranchConfig(ctx, canonical, malformedCommit, "user-config.yaml"); err == nil {
		t.Fatal("shared repository policy validator accepted malformed YAML")
	}
}

func TestConfigClaimsCoverageBatchActiveClaimFilesystem(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := walkActiveWorkLogClaims(home, func(*os.File, string, workLogClaim) {
		t.Fatal("visitor called for empty home")
	}); err != nil {
		t.Fatal(err)
	}
	worklogs := filepath.Join(home, "worklogs")
	if err := os.Mkdir(worklogs, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(home, "real-effort")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(worklogs, "linked-effort")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(worklogs)
	if err != nil || len(entries) != 1 || safeActiveDirectoryEntry(entries[0]) {
		t.Fatalf("safe symlink entry = %#v/%v", entries, err)
	}
	if err := walkActiveWorkLogClaims(home, func(*os.File, string, workLogClaim) {
		t.Fatal("visitor called for symlinked effort")
	}); err != nil {
		t.Fatal(err)
	}
	if directory, err := openDirectDirectoryNoFollow(filepath.Join(home, "missing")); err == nil || directory != nil {
		t.Fatalf("missing direct directory = %#v/%v", directory, err)
	}
	if claims, err := ListActiveClaimSummaries(string([]byte{'b', 'a', 'd', 0}), ""); err == nil || claims != nil {
		t.Fatalf("invalid active claim root = %#v/%v", claims, err)
	}
}
