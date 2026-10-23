package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/mergevalidation"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
)

func deadcodeFailureReport(command, detail string, identities ...string) quality.VerificationReport {
	return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command,
		Status: quality.StatusFailed, Detail: detail,
		Deadcode: &quality.DeadcodeFailureEvidence{Count: len(identities), Identities: identities, Complete: true},
	}}}
}

func TestWorktreeMergeSavedReceiptPassesReuseAndPublishGuards(t *testing.T) {
	t.Parallel()
	const candidateSHA = "candidate-sha"
	receipt := WorktreeMergeReceipt{
		Status:     WorktreeMergePrepared,
		TargetSHA:  "target-sha",
		Candidate:  WorktreeMergeCandidate{SHA: candidateSHA, Worktree: t.TempDir()},
		Validation: quality.VerificationReport{Revision: candidateSHA, WorkspaceClean: true, Status: quality.StatusPassed},
	}
	identity, ok := worktreeMergeValidationIdentity(receipt)
	if !ok {
		t.Fatal("prepared validation identity was not fingerprintable")
	}
	receipt.ValidationIdentity = &identity
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var loaded WorktreeMergeReceipt
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if reusable, err := preparedValidationStillValidContext(context.Background(), loaded, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil || !reusable {
		t.Fatalf("loaded receipt reuse = (%t, %v)", reusable, err)
	}
	if err := requireWorktreeMergePublishedValidationContext(context.Background(), loaded, worktreeMergeValidationPlan{}, 0, 0, 0); err != nil {
		t.Fatalf("loaded receipt publish guard rejected exact validation: %v", err)
	}
}

func TestWorktreeMergeSavedImportedMainReceiptReusesAndPublishesWithCleanTarget(t *testing.T) {
	repository, targetSHA, importedSHA, mergeSHA, candidateSHA, fakeBin := importedMainReceiptFixture(t)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gitMergeGraphTest(t, repository, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repository, "first-main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMergeGraphTest(t, repository, "add", "first-main.go")
	gitMergeGraphTest(t, repository, "commit", "-m", "advance origin main before prepare")
	gitMergeGraphTest(t, repository, "push", "origin", "main")
	initialMainSHA, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, "", 0)
	if err != nil || initialMainSHA == importedSHA {
		t.Fatalf("initial descendant attestation = (%s, %v), imported %s", initialMainSHA, err, importedSHA)
	}
	if err := os.WriteFile(filepath.Join(repository, "later-main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitMergeGraphTest(t, repository, "add", "later-main.go")
	gitMergeGraphTest(t, repository, "commit", "-m", "advance origin main")
	advancedMainSHA := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	gitMergeGraphTest(t, repository, "push", "origin", "main")
	gitMergeGraphTest(t, repository, "checkout", "integration")
	if current, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, initialMainSHA, 0); err != nil || current != advancedMainSHA {
		t.Fatalf("fast-forward lineage recheck = (%s, %v), want %s", current, err, advancedMainSHA)
	}
	const command = mergevalidation.DeadcodeCommand
	target := quality.VerificationReport{Repository: "sneat-dev/wb", Path: "git:" + targetSHA, Revision: targetSHA, WorkspaceClean: true, Status: quality.StatusPassed,
		Results: []quality.VerificationEntry{{Language: "go", Module: ".", Check: quality.CheckLint, Command: command, Status: quality.StatusPassed}}}
	candidate := deadcodeFailureReport(command, "candidate inherited main deadcode", "main.B")
	candidate.Repository, candidate.Path, candidate.Revision, candidate.WorkspaceClean = "sneat-dev/wb", "git:"+candidateSHA, candidateSHA, true
	parent := deadcodeFailureReport(command, "imported main deadcode", "main.B")
	parent.Repository, parent.Path, parent.Revision, parent.WorkspaceClean = "sneat-dev/wb", "git:"+importedSHA, importedSHA, true
	receipt := WorktreeMergeReceipt{
		Status: WorktreeMergePrepared, Repository: "sneat-dev/wb", Target: "cov/integration", TargetSHA: targetSHA,
		Candidate:          WorktreeMergeCandidate{SHA: candidateSHA, Worktree: repository},
		BaselineValidation: target, Validation: candidate,
		ImportedMainDeadcode: &mergevalidation.ImportedMainDeadcode{
			CandidateSHA: candidateSHA, TargetSHA: targetSHA, MergeSHA: mergeSHA, ImportedSHA: importedSHA, OriginMainSHA: initialMainSHA, Validation: parent,
		},
	}
	identity, ok := worktreeMergeValidationIdentity(receipt)
	if !ok {
		t.Fatal("candidate validation identity was not fingerprintable")
	}
	receipt.ValidationIdentity = &identity
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var loaded WorktreeMergeReceipt
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if reusable, err := preparedValidationStillValidContext(ctx, loaded, worktreeMergeValidationPlan{}, 10*time.Second, 0, 15*time.Second); err != nil || !reusable {
		t.Fatalf("loaded imported receipt reuse = (%t, %v)", reusable, err)
	}
	if err := requireWorktreeMergePublishedValidationContext(ctx, loaded, worktreeMergeValidationPlan{}, 10*time.Second, 0, 15*time.Second); err != nil {
		t.Fatalf("loaded imported receipt publish guard rejected evidence: %v", err)
	}
}

func TestWorktreeMergeImportedMainLineageRejectsRewindDivergenceAndBadLookup(t *testing.T) {
	t.Parallel()
	repository, _, importedSHA, mergeSHA, _, _ := importedMainReceiptFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	initial, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, "", 0)
	if err != nil || initial != importedSHA {
		t.Fatalf("initial lineage = (%s, %v)", initial, err)
	}
	gitMergeGraphTest(t, repository, "checkout", "main")
	for _, name := range []string{"one.go", "two.go"} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitMergeGraphTest(t, repository, "add", name)
		gitMergeGraphTest(t, repository, "commit", "-m", "advance "+name)
		gitMergeGraphTest(t, repository, "push", "origin", "main")
		if name == "one.go" {
			initial = gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
		}
	}
	secondAdvance := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	if current, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, initial, 0); err != nil || current != secondAdvance {
		t.Fatalf("second fast-forward = (%s, %v), want %s", current, err, secondAdvance)
	}
	gitMergeGraphTest(t, repository, "push", "--force", "origin", initial+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, secondAdvance, 0); err == nil {
		t.Fatal("rewind to the previously attested head was accepted")
	}
	baseSHA := gitMergeGraphTest(t, repository, "rev-parse", importedSHA+"^")
	gitMergeGraphTest(t, repository, "push", "--force", "origin", baseSHA+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, initial, 0); err == nil {
		t.Fatal("rewound origin/main accepted")
	}
	targetSHA := gitMergeGraphTest(t, repository, "rev-parse", mergeSHA+"^1")
	gitMergeGraphTest(t, repository, "push", "--force", "origin", targetSHA+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, initial, 0); err == nil {
		t.Fatal("diverged origin/main accepted")
	}
	if _, err := matchedFetchedOriginMain("", ""); err == nil {
		t.Fatal("missing fetched and remote heads accepted")
	}
	if _, err := matchedFetchedOriginMain(importedSHA, "different refs/heads/main"); err == nil {
		t.Fatal("fetch/ls-remote mismatch accepted")
	}
	if err := requireGitAncestor(ctx, defaultRunner, repository, "missing-commit", targetSHA); err == nil {
		t.Fatal("missing ancestry commit accepted")
	}
	gitMergeGraphTest(t, repository, "push", "--force", "origin", ":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, defaultRunner, repository, importedSHA, initial, 0); err == nil {
		t.Fatal("missing origin/main accepted")
	}
}

func importedMainReceiptFixture(t *testing.T) (repository, targetSHA, importedSHA, mergeSHA, candidateSHA, fakeBin string) {
	t.Helper()
	root := t.TempDir()
	repository = filepath.Join(root, "repo")
	remote := filepath.Join(root, "origin.git")
	fakeBin = filepath.Join(root, "bin")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitMergeGraphTest(t, root, "init", "--bare", remote)
	gitMergeGraphTest(t, root, "--git-dir="+remote, "config", "receive.denyDeleteCurrent", "ignore")
	gitMergeGraphTest(t, repository, "init", "-b", "main")
	gitMergeGraphTest(t, repository, "config", "user.email", "test@example.invalid")
	gitMergeGraphTest(t, repository, "config", "user.name", "WB test")
	writeFile := func(name, content string) {
		t.Helper()
		path := filepath.Join(repository, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitMergeGraphTest(t, repository, "add", name)
	}
	commit := func(message string) string {
		t.Helper()
		gitMergeGraphTest(t, repository, "commit", "-m", message)
		return gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	}
	writeFile("go.mod", "module example.test/wbtest\n\ngo 1.24.0\n")
	writeFile(".wb/quality.yaml", "version: 1\ngo_lint:\n  commands:\n    - [go, run, ./cmd/wb, deadcode]\n")
	commit("base")
	gitMergeGraphTest(t, repository, "checkout", "-b", "integration")
	writeFile("target.go", "package main\n")
	targetSHA = commit("target")
	gitMergeGraphTest(t, repository, "checkout", "main")
	writeFile("main.go", "package main\n")
	importedSHA = commit("imported main")
	gitMergeGraphTest(t, repository, "remote", "add", "origin", remote)
	gitMergeGraphTest(t, repository, "push", "origin", "main")
	gitMergeGraphTest(t, repository, "checkout", "integration")
	gitMergeGraphTest(t, repository, "merge", "--no-ff", "main", "-m", "import main")
	mergeSHA = gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	writeFile("fix.go", "package main\n")
	candidateSHA = commit("linear validation fix")
	output := "New unreachable functions (1):\n  main.go:1: main.B\nerror: 1 function(s) are unreachable from main and are not in .wb/deadcode-baseline.txt; wire them up, delete them, or record them with --update-baseline\nexit status 1\n"
	goScript := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\nexit 1\n"
	if err := testenv.WriteExecutableFile(filepath.Join(fakeBin, "go"), []byte(goScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return repository, targetSHA, importedSHA, mergeSHA, candidateSHA, fakeBin
}

//nolint:paralleltest // importedMainReceiptFixture changes the process environment with t.Setenv
func TestValidateWorktreeMergeCandidateContinuesForAttestedImportedMainDeadcode(t *testing.T) {
	repository, targetSHA, _, _, candidateSHA, fakeBin := importedMainReceiptFixture(t)
	const deadcodeOutput = "New unreachable functions (1):\n  main.go:1: main.B\nerror: 1 function(s) are unreachable from main and are not in .wb/deadcode-baseline.txt; wire them up, delete them, or record them with --update-baseline\nexit status 1\n"
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"run) if [ -f main.go ]; then printf '%s' '" + strings.ReplaceAll(deadcodeOutput, "'", "'\\''") + "'; exit 1; fi ;;\n" +
		"test) : > \"$WB_TEST_MARKER\" ;;\n" +
		"esac\nexit 0\n"
	if err := testenv.WriteExecutableFile(filepath.Join(fakeBin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "candidate-test-ran")
	t.Setenv("WB_TEST_MARKER", marker)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_VALIDATION_CACHE", filepath.Join(t.TempDir(), "cache"))
	receipt := WorktreeMergeReceipt{Repository: "sneat-dev/wb", TargetSHA: targetSHA,
		Candidate: WorktreeMergeCandidate{SHA: candidateSHA, Worktree: repository}}
	if err := validateWorktreeMergeCandidate(context.Background(), &receipt, 10*time.Second, 0, 0, 0, nil); err != nil {
		t.Fatalf("imported main deadcode rejected before full validation: %v, candidate %+v baseline %+v", err, receipt.Validation, receipt.BaselineValidation)
	}
	if receipt.ImportedMainDeadcode == nil || receipt.BaselineValidation.Revision != targetSHA {
		t.Fatalf("full imported-main comparison missing: %+v", receipt)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("candidate tests did not run after imported-main deadcode attestation: %v", err)
	}
	checkedTests := false
	for _, entry := range receipt.Validation.Results {
		checkedTests = checkedTests || entry.Check == quality.CheckTest
	}
	if !checkedTests {
		t.Fatalf("candidate test result missing after imported-main deadcode attestation: %+v", receipt.Validation)
	}
}

func TestWorktreeMergeImportedMainDeadcodeReceiptRoundTrips(t *testing.T) {
	t.Parallel()
	evidence := mergevalidation.ImportedMainDeadcode{
		CandidateSHA: "candidate", TargetSHA: "target", MergeSHA: "merge", ImportedSHA: "main", OriginMainSHA: "main",
		Validation: deadcodeFailureReport(mergevalidation.DeadcodeCommand, "complete", "main.A"),
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var restored mergevalidation.ImportedMainDeadcode
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !mergevalidation.ValidImportedMainDeadcodeReport(restored.Validation) || restored.Validation.Results[0].Deadcode.Identities[0] != "main.A" {
		t.Fatalf("round-tripped imported evidence lost complete identities: %+v", restored)
	}
}

func TestWorktreeMergeImportedMainGraphRequiresExactLinearMerge(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	gitMergeGraphTest(t, repository, "init", "-b", "main")
	gitMergeGraphTest(t, repository, "config", "user.email", "test@example.invalid")
	gitMergeGraphTest(t, repository, "config", "user.name", "WB test")
	writeAndCommit := func(name, content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		gitMergeGraphTest(t, repository, "add", name)
		gitMergeGraphTest(t, repository, "commit", "-m", content)
		return gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	}
	writeAndCommit("base", "base")
	gitMergeGraphTest(t, repository, "checkout", "-b", "integration")
	target := writeAndCommit("target", "target")
	gitMergeGraphTest(t, repository, "checkout", "main")
	gitMergeGraphTest(t, repository, "checkout", "-b", "imported")
	imported := writeAndCommit("main", "main")
	gitMergeGraphTest(t, repository, "checkout", "integration")
	gitMergeGraphTest(t, repository, "merge", "--no-ff", "imported", "-m", "import main")
	merge := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	candidate := writeAndCommit("fix", "fix")
	gotMerge, gotImported, found, err := worktreeMergeImportedMainGraph(context.Background(), defaultRunner, repository, candidate, target)
	if err != nil || !found || gotMerge != merge || gotImported != imported {
		t.Fatalf("valid graph = (%s, %s, %t, %v)", gotMerge, gotImported, found, err)
	}

	gitMergeGraphTest(t, repository, "checkout", "-b", "reverse", imported)
	gitMergeGraphTest(t, repository, "merge", "--no-ff", target, "-m", "reverse parents")
	reversed := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	if _, _, _, err := worktreeMergeImportedMainGraph(context.Background(), defaultRunner, repository, reversed, target); err == nil {
		t.Fatal("wrong parent order accepted")
	}

	gitMergeGraphTest(t, repository, "checkout", "-b", "extra", candidate)
	gitMergeGraphTest(t, repository, "checkout", "-b", "side", target)
	writeAndCommit("side", "side")
	gitMergeGraphTest(t, repository, "checkout", "extra")
	gitMergeGraphTest(t, repository, "merge", "--no-ff", "side", "-m", "extra merge")
	extra := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	if _, _, _, err := worktreeMergeImportedMainGraph(context.Background(), defaultRunner, repository, extra, target); err == nil {
		t.Fatal("extra merge accepted")
	}
}

func gitMergeGraphTest(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
