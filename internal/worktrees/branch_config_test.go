package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchNamingPrecedenceReadsTargetBaseObject(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  branch_prefix: user/\n")

	canonical, base := synchronizedBranchConfigBase(t, fixture)
	defer canonical.close()
	derive := func(options branchNamingOptions) string {
		t.Helper()
		options.Task = "task"
		options.Canonical = canonical
		options.BaseRevision = base
		options.Base = "main"
		branch, err := deriveBranchName(context.Background(), options)
		if err != nil {
			t.Fatal(err)
		}
		return branch
	}
	if got := derive(branchNamingOptions{}); got != "user/task" {
		t.Fatalf("user policy branch = %q", got)
	}
	if got := derive(branchNamingOptions{CLIPrefix: "cli/", CLIPrefixChosen: true}); got != "cli/task" {
		t.Fatalf("CLI prefix branch = %q", got)
	}
	if got := derive(branchNamingOptions{CLIPrefix: "", CLIPrefixChosen: true}); got != "task" {
		t.Fatalf("explicit empty CLI prefix branch = %q", got)
	}
	if got := derive(branchNamingOptions{ExactBranch: "direct", ExactBranchChosen: true}); got != "direct" {
		t.Fatalf("exact branch = %q", got)
	}
	for _, options := range []branchNamingOptions{
		{ExactBranch: "", ExactBranchChosen: true},
		{ExactBranch: "direct", ExactBranchChosen: true, CLIPrefix: "cli/", CLIPrefixChosen: true},
	} {
		if _, err := deriveBranchName(context.Background(), options); err == nil {
			t.Fatalf("expected explicit branch validation error for %#v", options)
		}
	}

	commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: repo/\n", "repository prefix")
	base = synchronizedBranchConfigBaseValue(t, fixture, canonical)
	if got := derive(branchNamingOptions{}); got != "repo/task" {
		t.Fatalf("repository policy did not override user policy: %q", got)
	}
	commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: \"\"\n", "empty repository prefix")
	base = synchronizedBranchConfigBaseValue(t, fixture, canonical)
	if got := derive(branchNamingOptions{}); got != "task" {
		t.Fatalf("explicit empty repository policy did not override user policy: %q", got)
	}

	// A clean canonical checkout can intentionally be parked on a different
	// branch. Its filesystem policy must not leak into a create/rename derived
	// from fetched origin/main.
	gitTest(t, fixture.canonical, "checkout", "-b", "parking")
	mustWriteBranchConfig(t, filepath.Join(fixture.canonical, ".wb", "worktrees.yaml"), "version: 1\nworktrees:\n  branch_prefix: parked/\n")
	gitTest(t, fixture.canonical, "add", ".wb/worktrees.yaml")
	gitTest(t, fixture.canonical, "commit", "-m", "local parking policy")
	base = synchronizedBranchConfigBaseValue(t, fixture, canonical)
	if got := derive(branchNamingOptions{}); got != "task" {
		t.Fatalf("canonical parking policy leaked into fetched target base: %q", got)
	}
}

func TestDirectBranchNamingOptionsKeepNonemptyValuesWithoutPresenceBits(t *testing.T) {
	projectsRoot := t.TempDir()
	create, err := normalizeCreateOptions(CreateOptions{
		ProjectsRoot: projectsRoot,
		Operation:    "direct-create",
		Branch:       "feature/direct-create", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || !create.BranchChosen || create.Branch != "feature/direct-create" {
		t.Fatalf("direct create branch normalization = %#v, err=%v", create, err)
	}
	create, err = normalizeCreateOptions(CreateOptions{
		ProjectsRoot: projectsRoot,
		Operation:    "direct-prefix",
		BranchPrefix: "feature/", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || !create.BranchPrefixChosen || create.BranchPrefix != "feature/" {
		t.Fatalf("direct create prefix normalization = %#v, err=%v", create, err)
	}
	rename, err := normalizeRenameOptions(RenameOptions{
		ProjectsRoot: projectsRoot,
		OldTask:      "old",
		NewTask:      "new",
		Branch:       "feature/direct-rename", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || !rename.BranchChosen || rename.Branch != "feature/direct-rename" {
		t.Fatalf("direct rename branch normalization = %#v, err=%v", rename, err)
	}
	rename, err = normalizeRenameOptions(RenameOptions{
		ProjectsRoot: projectsRoot,
		OldTask:      "old",
		NewTask:      "new",
		BranchPrefix: "feature/", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || !rename.BranchPrefixChosen || rename.BranchPrefix != "feature/" {
		t.Fatalf("direct rename prefix normalization = %#v, err=%v", rename, err)
	}
}

func TestCreateUsesConfiguredSharedWorktreesRoot(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "shared-root", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create shared-root = %#v, err=%v", created, err)
	}
	resolvedRoot, resolveErr := resolveSharedWorktreesRoot(sharedRoot)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	want := filepath.Join(resolvedRoot, "shared-root", "acme", "app")
	if created[0].WorktreeDir != want {
		t.Fatalf("shared worktree = %q, want %q", created[0].WorktreeDir, want)
	}
	if _, err := Guard(context.Background(), want, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard explicit shared worktree: %v", err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "shared-root", Workers: 1})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != want || listed[0].External {
		t.Fatalf("list explicit shared worktree = %#v, err=%v", listed, err)
	}
}

func TestSharedWorktreeRootRejectsRelativePath(t *testing.T) {
	if _, err := resolveSharedWorktreesRoot("relative/worktrees"); err == nil {
		t.Fatal("relative shared root was accepted")
	}
}

func TestCreateResumeRecoversClaimBranchAcrossNamingPolicyDrift(t *testing.T) {
	tests := []struct {
		name          string
		initialPolicy string
		driftPolicy   string
		wantBranch    string
		explicitRun   string
	}{
		{name: "no prefix to feature prefix", driftPolicy: "feature/", wantBranch: "policy-resume"},
		{name: "feature prefix to no prefix", initialPolicy: "feature/", driftPolicy: "", wantBranch: "feature/policy-resume", explicitRun: "stable-explicit-run"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			if test.initialPolicy != "" {
				commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: "+test.initialPolicy+"\n", "initial branch policy")
			}
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "policy-resume",
				WorkLog:      WorkLogOptions{RunID: test.explicitRun, Model: "unknown"},
			})
			if err != nil || len(created) != 1 || created[0].Branch != test.wantBranch {
				t.Fatalf("initial create = %#v err=%v", created, err)
			}
			projectionBefore, err := readWorkLogProjection(created[0].WorktreeDir)
			if err != nil {
				t.Fatal(err)
			}
			runDir := filepath.Join(fixture.home, "worklogs", projectionBefore.EffortID, "runs", projectionBefore.RunID)
			claimsBefore, err := os.ReadDir(filepath.Join(runDir, "claims"))
			if err != nil {
				t.Fatal(err)
			}
			commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: \""+test.driftPolicy+"\"\n", "drifted branch policy")

			resumed, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
				ProjectsRoot: fixture.projectsRoot,
				Operation:    "policy-resume",
				Resume:       true,
				WorkLog:      WorkLogOptions{RunID: test.explicitRun, Model: "unknown"},
			})
			if err != nil || len(resumed) != 1 {
				t.Fatalf("resume after policy drift = %#v err=%v", resumed, err)
			}
			if resumed[0].Action != "resumed" || resumed[0].Branch != test.wantBranch || resumed[0].WorkLogPath != created[0].WorkLogPath {
				t.Fatalf("resume did not preserve active claim: created=%#v resumed=%#v", created[0], resumed[0])
			}
			projectionAfter, err := readWorkLogProjection(created[0].WorktreeDir)
			if err != nil || projectionAfter != projectionBefore {
				t.Fatalf("resume replaced work-log projection: before=%#v after=%#v err=%v", projectionBefore, projectionAfter, err)
			}
			claimsAfter, err := os.ReadDir(filepath.Join(runDir, "claims"))
			if err != nil || len(claimsAfter) != len(claimsBefore) {
				t.Fatalf("resume claim cardinality changed from %d to %d: %v", len(claimsBefore), len(claimsAfter), err)
			}
		})
	}
}

func TestRepositoryBranchPolicyRejectsUnsafeOrInvalidBlob(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		symlink  bool
		want     string
	}{
		{name: "malformed", contents: "version: [\n", want: "parse worktrees config"},
		{name: "oversize", contents: strings.Repeat("x", maxBranchConfigSize+1), want: "repository worktrees policy blob"},
		{name: "multiple documents", contents: "version: 1\n---\nversion: 1\n", want: "multiple YAML documents"},
		{name: "symlink", contents: "ignored", symlink: true, want: "regular blob"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := filepath.Join(fixture.canonical, ".wb", "worktrees.yaml")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.symlink {
				mustWriteBranchConfig(t, filepath.Join(fixture.canonical, ".wb", "elsewhere"), test.contents)
				if err := os.Symlink("elsewhere", path); err != nil {
					t.Fatal(err)
				}
			} else {
				mustWriteBranchConfig(t, path, test.contents)
			}
			gitTest(t, fixture.canonical, "add", ".wb")
			gitTest(t, fixture.canonical, "commit", "-m", "invalid worktrees policy")
			gitTest(t, fixture.canonical, "push", "origin", "main")
			canonical, base := synchronizedBranchConfigBase(t, fixture)
			defer canonical.close()
			_, err := configuredBranchPrefix(context.Background(), canonical, base)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid repository policy error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUserBranchPolicyAllowsSymlinkToBoundedRegularFile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	target := filepath.Join(t.TempDir(), "shared-worktrees.yaml")
	mustWriteBranchConfig(t, target, "version: 1\nworktrees:\n  branch_prefix: user-link/\n")
	policyPath := filepath.Join(configHome, "wb", "worktrees.yaml")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, policyPath); err != nil {
		t.Fatal(err)
	}
	config, found, err := loadBranchConfigFile(policyPath)
	if err != nil || !found || config.Worktrees.BranchPrefix == nil || *config.Worktrees.BranchPrefix != "user-link/" {
		t.Fatalf("trusted user policy symlink = config=%#v found=%v err=%v", config, found, err)
	}
}

func TestRenameRefusesTargetPolicyMovementAfterPlanning(t *testing.T) {
	fixture := newGitFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: first/\n", "first branch policy")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "old", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].Branch != "first/old" {
		t.Fatalf("create did not apply target-base branch policy: %#v", created)
	}
	planned, err := Rename(context.Background(), RenameOptions{ProjectsRoot: fixture.projectsRoot, OldTask: "old", NewTask: "planned", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || planned.Results[0].NewBranch != "first/planned" {
		t.Fatalf("rename plan did not apply target-base branch policy: %#v", planned.Results)
	}
	prompt := filepath.Join(t.TempDir(), "rename-prompt.txt")
	mustWriteBranchConfig(t, prompt, "recycle the exact managed checkout\n")
	_, err = Rename(context.Background(), RenameOptions{
		ProjectsRoot: fixture.projectsRoot,
		OldTask:      "old",
		NewTask:      "new",
		Apply:        true,
		DeleteRemote: true,
		WorkLog:      WorkLogOptions{Model: "unknown", OriginalPrompt: prompt, RequireOriginalPrompt: true},
		beforeRenamePreflight: func() {
			commitRepositoryBranchConfig(t, fixture, "version: 1\nworktrees:\n  branch_prefix: second/\n", "moved branch policy")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "origin/main advanced") {
		t.Fatalf("rename after target policy move error = %v", err)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, "first/new"); branchErr != nil || exists {
		t.Fatalf("rename applied stale branch after target movement: exists=%t err=%v", exists, branchErr)
	}
}

func synchronizedBranchConfigBase(t *testing.T, fixture *gitFixture) (*canonicalRepository, string) {
	t.Helper()
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	base := synchronizedBranchConfigBaseValue(t, fixture, canonical)
	return canonical, base
}

func synchronizedBranchConfigBaseValue(t *testing.T, fixture *gitFixture, canonical *canonicalRepository) string {
	t.Helper()
	base, err := synchronizeCanonical(context.Background(), canonical, "acme/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func commitRepositoryBranchConfig(t *testing.T, fixture *gitFixture, contents, message string) {
	t.Helper()
	mustWriteBranchConfig(t, filepath.Join(fixture.canonical, ".wb", "worktrees.yaml"), contents)
	gitTest(t, fixture.canonical, "add", ".wb/worktrees.yaml")
	gitTest(t, fixture.canonical, "commit", "-m", message)
	gitTest(t, fixture.canonical, "push", "origin", "main")
}

func mustWriteBranchConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
