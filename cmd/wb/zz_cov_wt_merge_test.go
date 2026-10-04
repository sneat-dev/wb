package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cwWtMergeAckConstructor describes one recovery verb so the shared error-path
// table can drive every constructor in-process.
type cwWtMergeAckConstructor struct {
	name  string
	build func() *cobra.Command
	args  func(receipt string) []string
}

func cwWtMergeAckConstructors(projects string) []cwWtMergeAckConstructor {
	return []cwWtMergeAckConstructor{
		{name: "acknowledge-missing-cleanup", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-missing-cleanup")
		}, args: func(r string) []string { return []string{r} }},
		{name: "adopt-published-candidate", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "adopt-published-candidate")
		}, args: func(r string) []string { return []string{r, "https://example.test/pr/1"} }},
		{name: "acknowledge-landed-failed", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-landed-failed")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-stranded-landing", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-stranded-landing")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-prepare-candidate", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-retired-prepare-candidate")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-absorbed-conflict", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-absorbed-conflict")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-publication", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-retired-publication")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-retired-unpublished-validation-failure", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-retired-unpublished-validation-failure")
		}, args: func(r string) []string { return []string{r} }},
		{name: "acknowledge-receipt-collision", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "acknowledge-receipt-collision")
		}, args: func(r string) []string { return []string{r} }},
		{name: "supersede-validation-failed", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "supersede-validation-failed")
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "correct-self-supersession", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "correct-self-supersession")
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-published-forward-repair", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "prepare-published-forward-repair")
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "prepare-conflict-replacement", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "prepare-conflict-replacement")
		}, args: func(r string) []string { return []string{r, r} }},
		{name: "seal-validation-failed", build: func() *cobra.Command {
			return mergeChildForTest(&invocation{projectsRoot: projects}, "seal-validation-failed")
		}, args: func(r string) []string { return []string{r} }},
	}
}

func TestCwWtMergeRecoveryCommandsRejectBadAdmission(t *testing.T) {
	projects := t.TempDir()
	receipt := filepath.Join(projects, "receipt.json")
	for _, constructor := range cwWtMergeAckConstructors(projects) {
		t.Run(constructor.name, func(t *testing.T) {
			args := append(constructor.args(receipt), "--apply", "--mode", "manual")
			stdout, _, err := cwCovExec(t, projects, constructor.build, args...)
			if err == nil {
				t.Fatalf("%s --apply --mode manual = nil error (stdout %q), want an admission refusal", constructor.name, stdout)
			}
			if !strings.Contains(err.Error(), "--initiator") {
				t.Fatalf("%s admission error = %q, want a manual-mode --initiator refusal", constructor.name, err)
			}
		})
	}
}

func TestCwWtMergeNewLandAliasMatchesWorktreeLand(t *testing.T) {
	alias := newLandCmd(&invocation{})
	direct := newWorktreeLandCmd(&invocation{})
	if alias.Use != direct.Use || alias.Short != direct.Short || alias.Long != direct.Long {
		t.Fatalf("wb land contract = %q/%q, want it identical to wb worktree land %q/%q", alias.Use, alias.Short, direct.Use, direct.Short)
	}
	if alias.Flags().Lookup("cleanup").DefValue != "true" {
		t.Fatalf("wb land --cleanup default = %q, want true", alias.Flags().Lookup("cleanup").DefValue)
	}
}

func TestCwWtMergeRootMergeCommandIsWired(t *testing.T) {
	command := newWorktreeMergeCmd(&invocation{})
	if command.Use != "merge <source-worktree...>" {
		t.Fatalf("merge Use = %q, want the documented form", command.Use)
	}
	names := map[string]bool{}
	for _, child := range command.Commands() {
		names[child.Name()] = true
	}
	for _, want := range []string{"prepare", "land", "resume", "revert", "acknowledge-landed-failed", "acknowledge-stranded-landing", "acknowledge-absorbed-conflict", "acknowledge-retired-publication", "seal-validation-failed", "supersede-validation-failed", "correct-self-supersession", "prepare-published-forward-repair", "prepare-conflict-replacement"} {
		if !names[want] {
			t.Fatalf("merge subcommands = %v, missing %q", names, want)
		}
	}
	if command.Flags().Lookup("take-over-lane") == nil {
		t.Fatal("merge is missing the shared lane takeover flag")
	}
}

// cwWtMergeRewriteReceipt persists a mutated receipt at its own path so a
// recovery verb can be aimed at one exact immutable shape. The fixture writes
// a prepare-shaped receipt; the recovery verbs each require a different
// terminal status, and rewriting is how this test reaches them without a real
// remote landing.
func cwWtMergeRewriteReceipt(t *testing.T, path string, receipt orchestrate.WorktreeMergeReceipt) {
	t.Helper()
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCwWtMergeAcknowledgeUnpublishedValidationFailureOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-unpublished-validation-failure")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: unpublished_validation_failure_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate-worktree: " + fixture.receipt.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".unpublished-validation-failure.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".unpublished-validation-failure.ack.json"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry run wrote an acknowledgement: %v", statErr)
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-unpublished-validation-failure")
	}, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeUnpublishedValidationFailureAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode json acknowledgement %q: %v", jsonOut, err)
	}
	if decoded.ReceiptPath != fixture.receiptPath || decoded.Candidate.SHA != fixture.receipt.Candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want the fixture receipt identity", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-unpublished-validation-failure")
	},
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-retired-unpublished-validation-failure --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree merge prepare <preserved or different sources> --target main") {
		t.Fatalf("apply stdout %q missing the next-step hint", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".unpublished-validation-failure.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeLandedFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", fixture.receipt.Candidate.SHA)
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-landed-failed")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-landed-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: landed_failure_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate: " + fixture.receipt.Candidate.SHA,
		"acknowledgement: " + fixture.receiptPath + ".landed-validation-failed.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-landed-failed")
	},
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-landed-failed --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if !strings.Contains(applied, "current-target: ") {
		t.Fatalf("apply stdout %q missing the current target", applied)
	}
}

func TestCwWtMergeSupersedeValidationFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 2)
	for _, source := range fixture.sources {
		runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	}
	writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runCLIWorktreeGit(t, fixture.canonical, "add", "target.txt")
	runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: advance target for cwWt supersession")
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")
	replacement := createCLIWorktreeSource(t, fixture, "cw-wt-supersede-replacement", "feature/cw-wt-supersede-replacement", "replacement.txt", "replacement\n")
	runCLIWorktreeGit(t, replacement.WorktreeDir, "fetch", "origin")
	for _, source := range fixture.sources {
		runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/"+source.Branch)
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "supersede-validation-failed")
	}, fixture.receiptPath, replacement.WorktreeDir)
	if err != nil {
		t.Fatalf("supersede-validation-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_superseded",
		"receipt: " + fixture.receiptPath,
		"replacement: ",
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".validation-failed.superseded.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "supersede-validation-failed")
	},
		fixture.receiptPath, replacement.WorktreeDir, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("supersede-validation-failed --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".validation-failed.superseded.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the supersession acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeAbsorbedConflictOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	source := fixture.sources[0]
	// The source content must be reachable from the freshly fetched target,
	// and its worktree must be gone: this is exactly the absorbed-conflict
	// shape, where a later landing on the target carried the same content.
	runCLIWorktreeGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	runCLIWorktreeGit(t, fixture.canonical, "fetch", "origin")
	runCLIWorktreeGit(t, fixture.canonical, "merge", "--ff-only", "origin/"+source.Branch)
	// A generated spec index WB may be told to excuse, present on the target.
	writeCLIWorktreeFile(t, filepath.Join(fixture.canonical, "spec", "cw-wt", "README.md"), "generated index\n")
	runCLIWorktreeGit(t, fixture.canonical, "add", "spec/cw-wt/README.md")
	runCLIWorktreeGit(t, fixture.canonical, "commit", "-m", "test: add generated spec index for cwWt")
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt absorbed conflict"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-absorbed-conflict")
	},
		fixture.receiptPath, "--derived-path", "spec/cw-wt/README.md")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: absorbed_conflict_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate-worktree: " + conflict.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".absorbed-conflict.ack.json",
		"excused derived paths: spec/cw-wt/README.md",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-absorbed-conflict")
	},
		fixture.receiptPath, "--derived-path", "spec/cw-wt/README.md", "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeAbsorbedConflictAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode absorbed-conflict json %q: %v", jsonOut, err)
	}
	if len(decoded.ExcusedDerivedPaths) != 1 || decoded.ExcusedDerivedPaths[0] != "spec/cw-wt/README.md" {
		t.Fatalf("decoded acknowledgement excused paths = %v, want the audited spec index", decoded.ExcusedDerivedPaths)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-absorbed-conflict")
	},
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-absorbed-conflict --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree cleanup ") {
		t.Fatalf("apply stdout %q missing the cleanup hint", applied)
	}
	if !strings.Contains(applied, "status: ") {
		t.Fatalf("apply stdout %q missing the status line", applied)
	}
}

func TestCwWtMergeAcknowledgeRetiredPublicationOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	retired := fixture.receipt
	retired.Status = orchestrate.WorktreeMergeConflict
	retired.PullRequest = "https://example.test/acme/app/pull/7"
	retired.PublishedCandidateSHA = retired.Candidate.SHA
	retired.Failure = "cwWt retired publication"
	retired.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, retired)

	cwWtMergeFakeGHPullRequest(t, retired.PullRequest, `{"state":"CLOSED","closedAt":"2026-01-02T03:04:05Z","mergedAt":"","mergeCommit":{"oid":""},"headRefName":"`+retired.Candidate.Branch+`","headRefOid":"`+retired.Candidate.SHA+`"}`)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-publication")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-retired-publication dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: retired_publication_acknowledged",
		"receipt: " + fixture.receiptPath,
		"pull-request: " + retired.PullRequest + " (CLOSED)",
		"candidate-worktree: " + retired.Candidate.Worktree,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".retired-publication.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-publication")
	}, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-retired-publication --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeRetiredPublicationAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode retired-publication json %q: %v", jsonOut, err)
	}
	if decoded.PullRequestState != "CLOSED" || decoded.CandidateSHA != retired.Candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want a CLOSED pull request bound to the candidate", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-retired-publication")
	},
		fixture.receiptPath, "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("acknowledge-retired-publication --apply = %v, want nil", err)
	}
	if !strings.Contains(applied, "next: wb worktree merge prepare <the same sources> --target main") {
		t.Fatalf("apply stdout %q missing the next-step hint", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".retired-publication.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the retired-publication acknowledgement: %v", statErr)
	}
}

// cwWtMergeFakeGHPullRequest installs a fake `gh` on PATH whose `pr view`
// prints body, so a recovery verb that proves GitHub's own pull-request state
// can be driven without a network or a real GitHub account.
func cwWtMergeFakeGHPullRequest(t *testing.T, pullRequest, viewJSON string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"pr\" ] && [ \"$2\" = \"view\" ]; then\n" +
		"  printf '%s\\n' '" + viewJSON + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' '{}'\n" +
		"exit 0\n"
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if strings.TrimSpace(pullRequest) == "" {
		t.Fatal("cwWtMergeFakeGHPullRequest needs the pull request it stands in for")
	}
}

func TestCwWtMergeSealValidationFailedOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "seal-validation-failed")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("seal-validation-failed dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_seal_planned",
		"receipt: " + fixture.receiptPath,
		"current-target: ",
		"target-tree: ",
		"candidate: ",
		"dry-run only, pass --apply to create the ancestry seal candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "seal-validation-failed")
	}, fixture.receiptPath,
		"--format", "json", "--model", "cwWt-model", "--agent-runtime", "cwWt-runtime", "--agent-id", "cwWt-agent", "--provider", "cwWt-provider", "--cli", "wb-cwwt")
	if err != nil {
		t.Fatalf("seal-validation-failed --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeValidationFailureSeal
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode seal json %q: %v", jsonOut, err)
	}
	if decoded.Status != "validation_failure_seal_planned" || decoded.CurrentTargetSHA == "" || decoded.TargetTreeSHA == "" {
		t.Fatalf("decoded seal = %+v, want a planned seal naming the target and its tree", decoded)
	}
}

func TestCwWtMergeAcknowledgeMissingCleanupOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	source := fixture.sources[0]

	landed := fixture.receipt
	landed.Phase = orchestrate.WorktreeMergePhaseLand
	landed.Status = orchestrate.WorktreeMergeLanded
	landed.Cleanup = true
	landed.LandingSHA = landed.Candidate.SHA
	landed.CanonicalSync = "not_checked_out"
	landed.Checks = orchestrate.PullRequestWaitResult{Status: orchestrate.PullRequestWaitPassed}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, landed)

	// Land the exact candidate on the remote target so the landing proof holds.
	runCLIWorktreeGit(t, fixture.canonical, "update-ref", "refs/heads/main", landed.Candidate.SHA)
	runCLIWorktreeGit(t, fixture.canonical, "push", "origin", "main")

	// Legacy shape: the cleanup ran, but no terminal Work Log evidence remains.
	// Every receipted worktree and branch must therefore already be gone.
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(landed.Candidate.Worktree); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, fixture.canonical, "worktree", "prune")
	runCLIWorktreeGit(t, fixture.canonical, "branch", "-D", source.Branch)
	runCLIWorktreeGit(t, fixture.canonical, "branch", "-D", landed.Candidate.Branch)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-missing-cleanup")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-missing-cleanup dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: missing_cleanup_acknowledged",
		"receipt: " + fixture.receiptPath,
		"landing: " + landed.LandingSHA,
		"current-target: ",
		"acknowledgement: " + fixture.receiptPath + ".missing-cleanup.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-missing-cleanup")
	}, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-missing-cleanup --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeMissingCleanupAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode missing-cleanup json %q: %v", jsonOut, err)
	}
	if decoded.LandingSHA != landed.LandingSHA || decoded.CurrentTargetSHA == "" {
		t.Fatalf("decoded missing-cleanup ack = %+v, want the receipted landing and a current target", decoded)
	}
}

func TestCwWtMergeAcknowledgeStrandedLandingOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidateSHA := fixture.receipt.Candidate.SHA
	mergeCommitSHA := strings.Repeat("c", 40)
	pullRequestHeadSHA := strings.Repeat("b", 40)
	treeSHA := strings.Repeat("d", 40)

	stranded := fixture.receipt
	stranded.Phase = orchestrate.WorktreeMergePhaseLand
	stranded.Status = orchestrate.WorktreeMergeChecksPending
	stranded.PublishedCandidateSHA = candidateSHA
	stranded.PullRequest = "https://example.test/acme/app/pull/9"
	stranded.Failure = ""
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, stranded)

	view := `{"state":"MERGED","mergedAt":"2026-01-02T03:04:05Z","mergeCommit":{"oid":"` + mergeCommitSHA +
		`"},"headRefOid":"` + pullRequestHeadSHA + `","baseRefName":"` + stranded.Target + `"}`
	compareDiverged := candidateSHA + "..." + mergeCommitSHA
	cwWtMergeFakeGH(t, strings.Join([]string{
		"#!/bin/sh",
		`if [ "$1" = "pr" ] && [ "$2" = "view" ]; then`,
		`  printf '%s\n' '` + view + `'`,
		"  exit 0",
		"fi",
		`if [ "$1" = "api" ]; then`,
		`  ep="$2"`,
		`  case "$ep" in`,
		`    *compare/` + compareDiverged + `)`,
		`      printf 'HTTP/2 200 OK\n\n{"status":"diverged","base_commit":{"sha":"` + mergeCommitSHA + `"},"merge_base_commit":{"sha":"` + mergeCommitSHA + `"}}\n'`,
		"      ;;",
		`    *compare/*)`,
		`      rest="${ep#*compare/}"`,
		`      left="${rest%%...*}"`,
		`      printf 'HTTP/2 200 OK\n\n{"status":"ahead","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' "$left" "$left"`,
		"      ;;",
		`    */git/ref/heads/*)`,
		`      printf 'HTTP/2 200 OK\n\n{"object":{"sha":"` + mergeCommitSHA + `"}}\n'`,
		"      ;;",
		`    */git/commits/*)`,
		`      printf 'HTTP/2 200 OK\n\n{"tree":{"sha":"` + treeSHA + `"}}\n'`,
		"      ;;",
		"    *)",
		`      printf 'HTTP/2 200 OK\n\n{}\n'`,
		"      ;;",
		"  esac",
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")+"\n")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-stranded-landing")
	}, fixture.receiptPath)
	if err != nil {
		t.Fatalf("acknowledge-stranded-landing dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: stranded_landing_acknowledged",
		"receipt: " + fixture.receiptPath,
		"candidate: " + candidateSHA,
		"pull-request-head: " + pullRequestHeadSHA,
		"candidate-landing: tree-identical (tree " + treeSHA + ")",
		"proved-landing: " + mergeCommitSHA,
		"current-target: " + mergeCommitSHA,
		"acknowledgement: " + fixture.receiptPath + ".stranded-landing.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-stranded-landing")
	}, fixture.receiptPath, "--format", "json")
	if err != nil {
		t.Fatalf("acknowledge-stranded-landing --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeStrandedLandingAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode stranded-landing json %q: %v", jsonOut, err)
	}
	if decoded.PullRequestHeadSHA != pullRequestHeadSHA || decoded.ProvedLandingSHA != mergeCommitSHA || decoded.CandidateLandingTreeSHA != treeSHA {
		t.Fatalf("decoded acknowledgement = %+v, want the proved pull-request landing evidence", decoded)
	}
}

// cwWtMergeFakeGH installs script as a `gh` executable at the front of PATH.
func cwWtMergeFakeGH(t *testing.T, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCwWtMergeAdoptPublishedCandidateOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt published externally"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	// The candidate branch really is published at the exact receipted SHA.
	runCLIWorktreeGit(t, candidate.Worktree, "push", "origin", candidate.Branch)

	view := `{"number":7,"state":"open","base":{"ref":"` + conflict.Target + `","repo":{"full_name":"` + conflict.Repository +
		`"}},"head":{"ref":"` + candidate.Branch + `","sha":"` + candidate.SHA + `","repo":{"full_name":"` + conflict.Repository + `"}}}`
	cwWtMergeFakeGH(t, strings.Join([]string{
		"#!/bin/sh",
		`if [ "$1" = "api" ]; then`,
		`  printf 'HTTP/2 200 OK\n\n` + view + `\n'`,
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")+"\n")

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "adopt-published-candidate")
	}, fixture.receiptPath, "7")
	if err != nil {
		t.Fatalf("adopt-published-candidate dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: published_candidate_adopted",
		"receipt: " + fixture.receiptPath,
		"pull-request: 7",
		"candidate: " + candidate.SHA,
		"acknowledgement: " + fixture.receiptPath + ".published-candidate.adopted.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "adopt-published-candidate")
	}, fixture.receiptPath, "7", "--format", "json")
	if err != nil {
		t.Fatalf("adopt-published-candidate --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergePublishedCandidateAdoption
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode adoption json %q: %v", jsonOut, err)
	}
	if decoded.PullRequest != "7" || decoded.Candidate.SHA != candidate.SHA {
		t.Fatalf("decoded adoption = %+v, want pull request 7 bound to the receipted candidate", decoded)
	}

	applied, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "adopt-published-candidate")
	},
		fixture.receiptPath, "7", "--apply", "--actor", "cwWt operator", "--reason", "cwWt regression")
	if err != nil {
		t.Fatalf("adopt-published-candidate --apply = %v, want nil", err)
	}
	if strings.Contains(applied, "dry-run only") {
		t.Fatalf("apply stdout %q still claims a dry run", applied)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".published-candidate.adopted.ack.json"); statErr != nil {
		t.Fatalf("apply did not write the adoption acknowledgement: %v", statErr)
	}
}

func TestCwWtMergeAcknowledgeReceiptCollisionOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	collision := fixture.receipt
	collision.Status = orchestrate.WorktreeMergePreparing
	collision.SourceRefreshes = []orchestrate.WorktreeMergeSourceRefresh{{
		RecordedAt: time.Now().UTC(),
		Sources:    append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
	}}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, collision)

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" {
		t.Fatalf("candidate Work Log view has no active claim: %+v", view.Claim)
	}
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	sourceSHA := collision.Sources[0].SHA
	args := []string{
		fixture.receiptPath,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-target", collision.TargetSHA,
		"--expected-candidate", candidate.SHA,
		"--expected-current-source", sourceSHA,
		"--expected-historical-refresh-source", sourceSHA,
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-receipt-collision")
	}, args...)
	if err != nil {
		t.Fatalf("acknowledge-receipt-collision dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: receipt_collision_acknowledged",
		"receipt: " + fixture.receiptPath,
		"acknowledgement: " + fixture.receiptPath + ".receipt-collision.ack.json",
		"candidate: " + candidate.SHA,
		"current-target: " + collision.TargetSHA,
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "acknowledge-receipt-collision")
	},
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("acknowledge-receipt-collision --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeReceiptCollisionAcknowledgement
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode receipt-collision json %q: %v", jsonOut, err)
	}
	if decoded.ExpectedTargetSHA != collision.TargetSHA || decoded.ExpectedCandidateSHA != candidate.SHA {
		t.Fatalf("decoded acknowledgement = %+v, want the pinned target and candidate", decoded)
	}
	if _, statErr := os.Stat(fixture.receiptPath + ".receipt-collision.ack.json"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry run wrote an acknowledgement: %v", statErr)
	}
}

func TestCwWtMergePrepareConflictReplacementOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 2)
	candidate := fixture.receipt.Candidate

	conflict := fixture.receipt
	conflict.Status = orchestrate.WorktreeMergeConflict
	conflict.Failure = "cwWt observed conflict"
	conflict.Validation = quality.VerificationReport{}
	cwWtMergeRewriteReceipt(t, fixture.receiptPath, conflict)

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	args := []string{
		fixture.receiptPath,
	}
	expectedSources := make([]string, 0, len(fixture.sources))
	for _, source := range conflict.Sources {
		args = append(args, source.Worktree)
		expectedSources = append(expectedSources, source.SHA)
	}
	args = append(args,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-current-target", conflict.TargetSHA,
		"--expected-source-sha", strings.Join(expectedSources, ","),
	)

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "prepare-conflict-replacement")
	}, args...)
	if err != nil {
		t.Fatalf("prepare-conflict-replacement dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"conflict_candidate_refresh_planned",
		"conflict receipt:",
		"current target:",
		"dry-run only, pass --apply to create the receipt-bound replacement candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "prepare-conflict-replacement")
	},
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("prepare-conflict-replacement --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeConflictCandidateRefresh
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode conflict replacement json %q: %v", jsonOut, err)
	}
	if decoded.Status != "conflict_candidate_refresh_planned" || decoded.CurrentTargetSHA != conflict.TargetSHA {
		t.Fatalf("decoded replacement plan = %+v, want a planned refresh at the current target", decoded)
	}
}

func TestCwWtMergePreparePublishedForwardRepairOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" || view.Claim.BaseSHA == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	// The historical self-supersession this repair verb exists to resolve: an
	// older writer bound the failed candidate as its own replacement.
	supersessionPath := fixture.receiptPath + ".validation-failed.superseded.ack.json"
	supersession := orchestrate.WorktreeMergeValidationFailureSupersession{
		SchemaVersion: 1, Status: "validation_failure_superseded",
		ReceiptPath: fixture.receiptPath, AcknowledgementPath: supersessionPath,
		ReceiptID: fixture.receipt.ID, ReceiptSHA256: hex.EncodeToString(receiptDigest[:]),
		ReceiptStatus: fixture.receipt.Status, Lane: fixture.receipt.Lane,
		Repository: fixture.receipt.Repository, Target: fixture.receipt.Target, ReceiptTargetSHA: fixture.receipt.TargetSHA,
		CurrentTargetSHA:  fixture.receipt.TargetSHA,
		OriginalCandidate: candidate, OriginalClaimBaseSHA: view.Claim.BaseSHA,
		Replacement: candidate, ReplacementClaimBaseSHA: view.Claim.BaseSHA,
		Sources: append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
		Actor:   "historical operator", Reason: "historical self-supersession", RecordedAt: time.Now().UTC(),
	}
	supersession.ID = cwWtMergeSelfSupersessionID(supersession)
	cwWtMergeWriteJSON(t, supersessionPath, supersession)
	supersessionBytes, err := os.ReadFile(supersessionPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionDigest := sha256.Sum256(supersessionBytes)

	source := fixture.receipt.Sources[0]
	args := []string{
		fixture.receiptPath, source.Worktree,
		"--expected-receipt-sha256", hex.EncodeToString(receiptDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
		"--expected-supersession-sha256", hex.EncodeToString(supersessionDigest[:]),
		"--expected-current-target", fixture.receipt.TargetSHA,
		"--expected-source-sha", source.SHA,
		"--model", "cwWt-model", "--agent-runtime", "cwWt-runtime", "--agent-id", "cwWt-agent",
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "prepare-published-forward-repair")
	}, args...)
	if err != nil {
		t.Fatalf("prepare-published-forward-repair dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: published_forward_repair_planned",
		"failed-receipt: " + fixture.receiptPath,
		"candidate: ",
		"current-target: " + fixture.receipt.TargetSHA,
		"dry-run only, pass --apply to create the distinct forward-repair candidate",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "prepare-published-forward-repair")
	},
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("prepare-published-forward-repair --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergePublishedForwardRepair
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode forward-repair json %q: %v", jsonOut, err)
	}
	if decoded.Status != "published_forward_repair_planned" || decoded.SupersessionPath != supersessionPath {
		t.Fatalf("decoded forward-repair plan = %+v, want a plan bound to the historical supersession", decoded)
	}
}

// cwWtMergeWriteJSON persists any receipt-shaped artifact as indented JSON,
// so a test can stand in for a historical file WB would have written.
func cwWtMergeWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// cwWtMergeSelfSupersessionID reproduces the immutable identity hash the
// engine binds onto a supersession acknowledgement, so a test can stand in for
// the historical artifact an older writer left behind.
func cwWtMergeSelfSupersessionID(ack orchestrate.WorktreeMergeValidationFailureSupersession) string {
	hash := sha256.New()
	write := func(value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, value := range []string{ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptStatus), ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.OriginalClaimBaseSHA, ack.ReplacementClaimBaseSHA, ack.Replacement.Task, ack.Replacement.Worktree, ack.Replacement.Branch, ack.Replacement.SHA} {
		write(value)
	}
	if ack.ObservedCandidateDescendantSHA != "" {
		write("observed_candidate_descendant_sha")
		write(ack.ObservedCandidateDescendantSHA)
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			write(value)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestCwWtMergeCorrectSelfSupersessionOutput(t *testing.T) {
	fixture := newCLIWorktreeMergeFixture(t, 1)
	candidate := fixture.receipt.Candidate

	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: candidate.Worktree,
	})
	if err != nil {
		t.Fatalf("load failed candidate Work Log: %v", err)
	}
	if view.Claim == nil || view.Claim.ClaimPath == "" || view.Claim.BaseSHA == "" {
		t.Fatalf("failed candidate Work Log view has no active claim: %+v", view.Claim)
	}

	receiptBytes, err := os.ReadFile(fixture.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptDigest := sha256.Sum256(receiptBytes)
	claimBytes, err := os.ReadFile(view.Claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)

	supersessionPath := fixture.receiptPath + ".validation-failed.superseded.ack.json"
	supersession := orchestrate.WorktreeMergeValidationFailureSupersession{
		SchemaVersion: 1, Status: "validation_failure_superseded",
		ReceiptPath: fixture.receiptPath, AcknowledgementPath: supersessionPath,
		ReceiptID: fixture.receipt.ID, ReceiptSHA256: hex.EncodeToString(receiptDigest[:]),
		ReceiptStatus: fixture.receipt.Status, Lane: fixture.receipt.Lane,
		Repository: fixture.receipt.Repository, Target: fixture.receipt.Target, ReceiptTargetSHA: fixture.receipt.TargetSHA,
		CurrentTargetSHA:  fixture.receipt.TargetSHA,
		OriginalCandidate: candidate, OriginalClaimBaseSHA: view.Claim.BaseSHA,
		Replacement: candidate, ReplacementClaimBaseSHA: view.Claim.BaseSHA,
		Sources: append([]orchestrate.WorktreeMergeSource(nil), fixture.receipt.Sources...),
		Actor:   "historical operator", Reason: "historical self-supersession", RecordedAt: time.Now().UTC(),
	}
	supersession.ID = cwWtMergeSelfSupersessionID(supersession)
	cwWtMergeWriteJSON(t, supersessionPath, supersession)
	supersessionBytes, err := os.ReadFile(supersessionPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionDigest := sha256.Sum256(supersessionBytes)

	// The corrected replacement is a distinct clean claimed candidate that
	// still contains every immutable receipted source.
	replacement := createCLIWorktreeSource(t, fixture, "cw-wt-correct-replacement", "feature/cw-wt-correct-replacement", "replacement.txt", "replacement\n")
	for _, source := range fixture.receipt.Sources {
		runCLIWorktreeGit(t, replacement.WorktreeDir, "merge", "--no-edit", source.Branch)
	}

	args := []string{
		fixture.receiptPath, replacement.WorktreeDir,
		"--expected-supersession-sha256", hex.EncodeToString(supersessionDigest[:]),
		"--expected-immutable-claim-sha256", hex.EncodeToString(claimDigest[:]),
	}

	stdout, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "correct-self-supersession")
	}, args...)
	if err != nil {
		t.Fatalf("correct-self-supersession dry run = %v, want nil", err)
	}
	for _, want := range []string{
		"status: validation_failure_self_supersession_corrected",
		"receipt: " + fixture.receiptPath,
		"replacement: ",
		"correction: " + fixture.receiptPath + ".validation-failed.self-supersession.corrected.ack.json",
		"dry-run only, pass --apply to write",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("dry-run stdout %q missing %q", stdout, want)
		}
	}

	jsonOut, _, err := cwCovExec(t, fixture.projectsRoot, func() *cobra.Command {
		return mergeChildForTest(&invocation{projectsRoot: fixture.projectsRoot}, "correct-self-supersession")
	},
		append(append([]string{}, args...), "--format", "json")...)
	if err != nil {
		t.Fatalf("correct-self-supersession --format json = %v, want nil", err)
	}
	var decoded orchestrate.WorktreeMergeSelfSupersessionCorrection
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode correction json %q: %v", jsonOut, err)
	}
	if decoded.Status != "validation_failure_self_supersession_corrected" || decoded.SupersessionPath != supersessionPath {
		t.Fatalf("decoded correction = %+v, want a correction bound to the historical supersession", decoded)
	}
}
