package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLandAliasSharesWorktreeLandContract pins that `wb land` is not a second
// implementation: it is built from the exact same constructor as
// `wb worktree land`, so its flags, defaults, and landing-guard addressing
// can never drift from the nested command's, and it resolves under the
// AGENT WORKFLOW root group next to `wb worktree create`.
func TestLandAliasSharesWorktreeLandContract(t *testing.T) {
	alias := newLandCmd(&invocation{})
	nested := newWorktreeLandCmd(&invocation{})
	if alias.Name() != "land" || nested.Name() != "land" {
		t.Fatalf("Name() = %q / %q, want land / land", alias.Name(), nested.Name())
	}
	aliasFlags := map[string]string{}
	alias.Flags().VisitAll(func(flag *pflag.Flag) { aliasFlags[flag.Name] = flag.DefValue })
	nestedFlags := map[string]string{}
	nested.Flags().VisitAll(func(flag *pflag.Flag) { nestedFlags[flag.Name] = flag.DefValue })
	if len(aliasFlags) == 0 {
		t.Fatal("wb land defines no flags")
	}
	if !reflect.DeepEqual(aliasFlags, nestedFlags) {
		t.Fatalf("wb land flags = %v, want identical to wb worktree land: %v", aliasFlags, nestedFlags)
	}
	if alias.Annotations[landingGuardAnnotation] != landingGuardByWorktree {
		t.Fatalf("wb land landing-guard annotation = %q, want %q", alias.Annotations[landingGuardAnnotation], landingGuardByWorktree)
	}

	root := newRootCmd()
	found, _, err := root.Find([]string{"land"})
	if err != nil {
		t.Fatalf("resolve wb land: %v", err)
	}
	if found.CommandPath() != "wb land" {
		t.Fatalf("CommandPath() = %q, want %q", found.CommandPath(), "wb land")
	}
	if found.GroupID != rootGroupAgent {
		t.Fatalf("wb land GroupID = %q, want %q (AGENT WORKFLOW)", found.GroupID, rootGroupAgent)
	}
}

// TestWorktreeLandAndRootLandAcceptProjectsRoot pins #502: `wb worktree land`
// and its root alias `wb land` must consume --projects-root rather than
// reject it, since runCombinedWorktreeMerge threads the package-level
// projectsRoot into orchestrate.WorktreeMergeLandOptions, which in turn
// guards every source with worktrees.Guard(ProjectsRoot: ...).
//
// The proof is root-DEPENDENT, not merely "some orchestrator error came
// back" (a missing source worktree fails identically no matter what
// --projects-root names, which would equally pass if the flag were silently
// ignored). Instead this places a real linked worktree at
// <root>/.worktrees/task1/bad repo - a shape worktrees.Guard's managed-layout
// resolution rejects only because "bad repo" (a space is not a safe
// repository segment) fails inside the managed-worktree-path check, which is
// built entirely from the passed --projects-root. The resulting error names
// that exact <root>/.worktrees path, so a caller can see the flag was the
// one actually used.
func TestWorktreeLandAndRootLandAcceptProjectsRoot(t *testing.T) {
	for _, args := range [][]string{
		{"worktree", "land"},
		{"land"},
	} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "base")
			writeCLIWorktreeFile(t, filepath.Join(base, "initial.txt"), "initial\n")
			runCLIWorktreeGit(t, base, "init", "-b", "main")
			runCLIWorktreeGit(t, base, "config", "user.name", "WB Test")
			runCLIWorktreeGit(t, base, "config", "user.email", "wb@example.test")
			runCLIWorktreeGit(t, base, "add", "-A")
			runCLIWorktreeGit(t, base, "commit", "-m", "initial")

			worktreePath := filepath.Join(root, ".worktrees", "task1", "bad repo")
			if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
				t.Fatal(err)
			}
			runCLIWorktreeGit(t, base, "worktree", "add", worktreePath, "-b", "feature/x")

			var stdout, stderr bytes.Buffer
			command := newRootCmd()
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			cliArgs := append(append([]string{"--projects-root", root, "--non-interactive"}, args...), worktreePath, "--target", "main")
			command.SetArgs(cliArgs)
			err := command.Execute()
			if err == nil {
				t.Fatal("expected an error naming the given --projects-root's managed-worktree layout")
			}
			if strings.Contains(err.Error(), "is not supported by") {
				t.Fatalf("--projects-root was rejected instead of consumed: %v", err)
			}
			wantWorktreesRoot := filepath.Join(root, ".worktrees")
			if !strings.Contains(err.Error(), wantWorktreesRoot) {
				t.Fatalf("error = %v, want it to name the given --projects-root's worktrees root %q", err, wantWorktreesRoot)
			}
		})
	}
}

func TestWorktreeMergeRecoveryApplyUsesAdmissionFlags(t *testing.T) {
	t.Run("acknowledge-landed-failed", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 1)
		originalReceipt := readCLIFile(t, fixture.receiptPath)
		runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", fixture.receipt.Candidate.SHA)
		runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "acknowledge-landed-failed", fixture.receiptPath,
			"--apply", "--actor", "test-operator", "--reason", "regression-test",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("production acknowledge apply failed: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeLandedFailureAcknowledgement
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode acknowledge output %q: %v", stdout.String(), err)
		}
		assertCLIWorktreeMergeAcknowledgement(t, fixture.receiptPath, originalReceipt, acknowledgement.AcknowledgementPath)
	})

	t.Run("supersede-validation-failed", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 2)
		originalReceipt := readCLIFile(t, fixture.receiptPath)
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
		}
		writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runCLIWorktreeGit(t, fixture.canonical, "add", "target.txt")
		runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: advance target for CLI supersession")
		runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")
		replacement := createCLIWorktreeSource(t, fixture, "supersede-replacement", "feature/supersede-replacement", "replacement.txt", "replacement\n")
		runCLIWorktreeGit(t, replacement.WorktreeDir, "fetch", "origin")
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
		}

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "supersede-validation-failed", fixture.receiptPath, replacement.WorktreeDir,
			"--apply", "--actor", "test-operator", "--reason", "regression-test",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("production supersede apply failed: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeValidationFailureSupersession
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode supersede output %q: %v", stdout.String(), err)
		}
		assertCLIWorktreeMergeAcknowledgement(t, fixture.receiptPath, originalReceipt, acknowledgement.AcknowledgementPath)
	})

	t.Run("supersede-validation-failed legacy receipt derives candidate from validation revision", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 2)
		legacy := fixture.receipt
		if legacy.Validation.Path != legacy.Candidate.Worktree || legacy.Validation.Revision != legacy.Candidate.SHA {
			t.Fatalf("fixture lacks candidate validation evidence: %#v", legacy.Validation)
		}
		legacy.Candidate.SHA = ""
		legacyContents, err := json.MarshalIndent(legacy, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		legacyContents = append(legacyContents, '\n')
		if err := os.WriteFile(fixture.receiptPath, legacyContents, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
		}
		replacement := createCLIWorktreeSource(t, fixture, "legacy-supersede-replacement", "feature/legacy-supersede-replacement", "replacement.txt", "replacement\n")
		for _, source := range fixture.sources {
			runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
		}

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "supersede-validation-failed", fixture.receiptPath, replacement.WorktreeDir,
			"--apply", "--actor", "test-operator", "--reason", "legacy-receipt-regression",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("legacy supersede apply failed: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeValidationFailureSupersession
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode legacy supersede output %q: %v", stdout.String(), err)
		}
		assertCLIWorktreeMergeAcknowledgement(t, fixture.receiptPath, legacyContents, acknowledgement.AcknowledgementPath)
		if _, err := os.Stat(fixture.receiptPath + ".legacy-validation-failed.identity.ack.json"); err != nil {
			t.Fatalf("legacy identity acknowledgement missing: %v", err)
		}
	})

	t.Run("supersede-validation-failed legacy receipt refuses validation revision drift", func(t *testing.T) {
		fixture := newCLIWorktreeMergeFixture(t, 1)
		legacy := fixture.receipt
		legacy.Candidate.SHA = ""
		legacy.Validation.Revision = "0000000000000000000000000000000000000000"
		contents, err := json.MarshalIndent(legacy, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		contents = append(contents, '\n')
		if err := os.WriteFile(fixture.receiptPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		root := newRootCmd()
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "supersede-validation-failed", fixture.receiptPath, fixture.sources[0].WorktreeDir,
		})
		err = root.Execute()
		if err == nil || !strings.Contains(err.Error(), "does not match immutable validation revision") {
			t.Fatalf("legacy drift error = %v", err)
		}
		if _, statErr := os.Stat(fixture.receiptPath + ".legacy-validation-failed.identity.ack.json"); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("legacy drift wrote an acknowledgement: %v", statErr)
		}
	})
}

func TestWorktreeMergePrepareSelectsSuccessorAfterSupersededConflictReceipt(t *testing.T) {
	prepareSupersededConflict := func(t *testing.T) (cliWorktreeMergeFixture, orchestrate.WorktreeMergeValidationFailureSupersession) {
		t.Helper()
		fixture := newCLIWorktreeMergeFixture(t, 1)
		receipt := fixture.receipt
		receipt.Status = orchestrate.WorktreeMergeConflict
		receipt.Failure = "historical merge conflict"
		receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.receiptPath, append(receiptBytes, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		runCLIWorktreeGit(t, fixture.sources[0].WorktreeDir, "push", "origin", fixture.sources[0].Branch)
		writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runCLIWorktreeGit(t, fixture.canonical, "add", "target.txt")
		runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: advance target for conflict supersession")
		runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")
		replacement := createCLIWorktreeSource(t, fixture, "conflict-supersede-replacement", "feature/conflict-supersede-replacement", "replacement.txt", "replacement\n")
		runCLIWorktreeGit(t, replacement.WorktreeDir, "fetch", "origin")
		runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+fixture.sources[0].Branch)

		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "supersede-validation-failed", fixture.receiptPath, replacement.WorktreeDir,
			"--apply", "--actor", "test-operator", "--reason", "prepare successor after conflict supersession",
			"--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("supersede conflict receipt: %v\nstderr: %s", err, stderr.String())
		}
		var acknowledgement orchestrate.WorktreeMergeValidationFailureSupersession
		if err := json.Unmarshal(stdout.Bytes(), &acknowledgement); err != nil {
			t.Fatalf("decode supersession output %q: %v", stdout.String(), err)
		}
		return fixture, acknowledgement
	}

	t.Run("valid acknowledgement permits a fresh prepare with the original source", func(t *testing.T) {
		fixture, acknowledgement := prepareSupersededConflict(t)
		var stdout, stderr bytes.Buffer
		root := newRootCmd()
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "prepare", fixture.sources[0].WorktreeDir,
			"--target", "main", "--model", "test-model", "--agent-runtime", "test", "--format", "json",
		})
		if err := root.Execute(); err != nil {
			t.Fatalf("fresh prepare rejected valid supersession %s: %v\nstderr: %s", acknowledgement.AcknowledgementPath, err, stderr.String())
		}
		var successor orchestrate.WorktreeMergeReceipt
		if err := json.Unmarshal(stdout.Bytes(), &successor); err != nil {
			t.Fatalf("decode prepare output %q: %v", stdout.String(), err)
		}
		if successor.Status != orchestrate.WorktreeMergePrepared || successor.ReceiptPath == fixture.receiptPath || successor.Candidate.SHA == "" {
			t.Fatalf("fresh prepare did not create a distinct prepared successor: %+v", successor)
		}

		var retryOutput, retryError bytes.Buffer
		retry := newRootCmd()
		retry.SetOut(&retryOutput)
		retry.SetErr(&retryError)
		retry.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "prepare", fixture.sources[0].WorktreeDir,
			"--target", "main", "--model", "test-model", "--agent-runtime", "test", "--format", "json",
		})
		if err := retry.Execute(); err != nil {
			t.Fatalf("retry prepare rejected existing successor %s: %v\nstderr: %s", successor.ReceiptPath, err, retryError.String())
		}
		var retried orchestrate.WorktreeMergeReceipt
		if err := json.Unmarshal(retryOutput.Bytes(), &retried); err != nil {
			t.Fatalf("decode retry output %q: %v", retryOutput.String(), err)
		}
		if retried.ID != successor.ID || retried.ReceiptPath != successor.ReceiptPath || retried.Candidate != successor.Candidate {
			t.Fatalf("retry did not return the unchanged successor: got=%+v want=%+v", retried, successor)
		}
	})

	t.Run("tampered acknowledgement leaves the conflict receipt blocking", func(t *testing.T) {
		fixture, acknowledgement := prepareSupersededConflict(t)
		contents, err := os.ReadFile(acknowledgement.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		tampered := strings.Replace(string(contents), acknowledgement.ID, "tampered", 1)
		if tampered == string(contents) {
			t.Fatal("fixture acknowledgement did not contain its ID")
		}
		if err := os.WriteFile(acknowledgement.AcknowledgementPath, []byte(tampered), 0o600); err != nil {
			t.Fatal(err)
		}

		root := newRootCmd()
		root.SetArgs([]string{
			"--projects-root", fixture.projectsRoot, "--non-interactive",
			"worktree", "merge", "prepare", fixture.sources[0].WorktreeDir,
			"--target", "main", "--model", "test-model", "--agent-runtime", "test",
		})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
			t.Fatalf("tampered acknowledgement released the lane: %v", err)
		}
	})
}

type cliWorktreeMergeFixture struct {
	projectsRoot string
	canonical    string
	receiptPath  string
	receipt      orchestrate.WorktreeMergeReceipt
	sources      []worktrees.CreateResult
}

// TestWorktreeMergeRevertLandsAForwardRevertAfterASuccessfulPrepare proves
// `wb worktree merge revert` reaches its land call (worktree_merge.go:590)
// after a successful PrepareWorktreeMergeRevert, not just the missing- or
// malformed-receipt refusals every other revert test exercises. It fabricates
// a "landed" receipt by fast-forwarding the canonical clone's target branch
// to a real candidate commit, so PreviousTargetSHA/LandingSHA name real,
// diffable commits, exactly as a genuinely landed merge would.
func TestWorktreeMergeRevertLandsAForwardRevertAfterASuccessfulPrepare(t *testing.T) {
	// Built directly, not through newCLIWorktreeMergeFixture: that helper
	// writes its own receipt for this exact repository+target lane, and a
	// second PrepareWorktreeMerge call for the same lane is refused as an
	// invalid resume once one exists.
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	writeCLIWorktreeFile(t, filepath.Join(seed, "initial.txt"), "initial\n")
	runCLIWorktreeGit(t, seed, "init", "-b", "main")
	runCLIWorktreeGit(t, seed, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, seed, "config", "user.email", "wb@example.test")
	runCLIWorktreeGit(t, seed, "add", "-A")
	runCLIWorktreeGit(t, seed, "commit", "-m", "initial")
	runCLIWorktreeGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, root, "clone", remote, canonical)
	runCLIWorktreeGit(t, canonical, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, canonical, "config", "user.email", "wb@example.test")
	fixture := cliWorktreeMergeFixture{projectsRoot: projectsRoot, canonical: canonical}
	source := createCLIWorktreeSource(t, fixture, "revert-source", "feature/revert-source", "revert-source.txt", "revert-source\n")

	previousTargetSHA := strings.TrimSpace(runCLIWorktreeGit(t, canonical, "rev-parse", "main"))

	receipt, err := orchestrate.PrepareWorktreeMerge(context.Background(), orchestrate.WorktreeMergePrepareOptions{
		ProjectsRoot: projectsRoot, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate landing: fast-forward the canonical clone's target to the
	// prepared candidate and push it, so the candidate is really reachable.
	runCLIWorktreeGit(t, canonical, "checkout", "main")
	runCLIWorktreeGit(t, canonical, "reset", "--hard", receipt.Candidate.SHA)
	runCLIWorktreeGit(t, canonical, "push", "origin", "main")

	receipt.Status = orchestrate.WorktreeMergeLanded
	receipt.PreviousTargetSHA = previousTargetSHA
	receipt.LandingSHA = receipt.Candidate.SHA
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(receipt.ReceiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	command := mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "revert")
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--format", "json", receipt.ReceiptPath})
	// This environment has no real `gh`, so the land phase this test
	// fabricates is refused once it reaches GitHub evidence - what matters is
	// that PrepareWorktreeMergeRevert succeeded and the land call was
	// actually reached (the receipt's phase advances from "revert" prepare
	// to "land"), not that the whole journey completes.
	if err := command.Execute(); err == nil {
		t.Fatal("revert land without a real gh must be refused")
	}
	if !strings.Contains(stdout.String(), `"phase": "land"`) {
		t.Fatalf("revert stdout = %q, want the land call to have been reached", stdout.String())
	}
}

func newCLIWorktreeMergeFixture(t *testing.T, sourceCount int) cliWorktreeMergeFixture {
	t.Helper()
	// spec/plans/coverage-to-100 task-17: this fixture's real git repo now
	// reaches orchestrate's runCommand through task-24's guarded runner.Real,
	// so every caller needs the escape hatch once, here.
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "projects"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	writeCLIWorktreeFile(t, filepath.Join(seed, "initial.txt"), "initial\n")
	runCLIWorktreeGit(t, seed, "init", "-b", "main")
	runCLIWorktreeGit(t, seed, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, seed, "config", "user.email", "wb@example.test")
	runCLIWorktreeGit(t, seed, "add", "-A")
	runCLIWorktreeGit(t, seed, "commit", "-m", "initial")
	runCLIWorktreeGit(t, root, "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, root, "clone", remote, canonical)
	runCLIWorktreeGit(t, canonical, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, canonical, "config", "user.email", "wb@example.test")

	fixture := cliWorktreeMergeFixture{projectsRoot: projectsRoot, canonical: canonical}
	for i := 0; i < sourceCount; i++ {
		task := "cli-supersede-source-" + string(rune('a'+i))
		branch := "feature/cli-supersede-" + string(rune('a'+i))
		fixture.sources = append(fixture.sources, createCLIWorktreeSource(t, fixture, task, branch, task+".txt", task+"\n"))
	}
	sourcePaths := make([]string, 0, len(fixture.sources))
	for _, source := range fixture.sources {
		sourcePaths = append(sourcePaths, source.WorktreeDir)
	}
	receipt, err := orchestrate.PrepareWorktreeMerge(context.Background(), orchestrate.WorktreeMergePrepareOptions{
		ProjectsRoot: projectsRoot, Sources: sourcePaths, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = orchestrate.WorktreeMergeValidationFailed
	receipt.Failure = "historical validation failure"
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(receipt.ReceiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.receipt = receipt
	fixture.receiptPath = receipt.ReceiptPath
	return fixture
}

func createCLIWorktreeSource(t *testing.T, fixture cliWorktreeMergeFixture, task, branch, name, contents string) worktrees.CreateResult {
	t.Helper()
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("CLI recovery command fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, Branch: branch, BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{
			EffortID: task, RunID: task + "-run", Initiator: "test", AgentID: task, AgentRuntime: "test", Model: "test-model",
			OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	writeCLIWorktreeFile(t, filepath.Join(result.WorktreeDir, name), contents)
	runCLIWorktreeGit(t, result.WorktreeDir, "add", name)
	runCLIWorktreeGit(t, result.WorktreeDir, "commit", "-m", "feat: add "+name)
	return result
}

func assertCLIWorktreeMergeAcknowledgement(t *testing.T, receiptPath string, originalReceipt []byte, acknowledgementPath string) {
	t.Helper()
	if strings.TrimSpace(acknowledgementPath) == "" {
		t.Fatal("production command returned an empty acknowledgement path")
	}
	if _, err := os.Stat(acknowledgementPath); err != nil {
		t.Fatalf("acknowledgement artifact missing: %v", err)
	}
	if got := readCLIFile(t, receiptPath); string(got) != string(originalReceipt) {
		t.Fatal("production command rewrote the historical receipt")
	}
}

func writeCLIWorktreeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCLIFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func runCLIWorktreeGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

// TestWorktreeMergePrepareForcesLocalValidationExceptExplicitPRRoute is
// Minor 13's regression test (sneat-dev/wb#591 round 3 red-team follow-up):
// a standalone `wb worktree merge prepare` must validate locally by default
// (auto, or an explicit --route direct), since dependent agents consume its
// candidate SHA directly and may need it validated before any land call
// ever runs. Only an explicit --route pr (this call itself intends to land
// through the pull-request route) may still defer.

func mergeChildForTest(inv *invocation, name string) *cobra.Command {
	parent := newWorktreeMergeCmd(inv)
	child, _, err := parent.Find([]string{name})
	if err != nil {
		panic(err)
	}
	parent.RemoveCommand(child)
	return child
}
