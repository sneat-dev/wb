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

func TestWorktreeMergeDeadcodeNonRegressionUsesCompleteIdentities(t *testing.T) {
	t.Parallel()
	const command = "go run ./cmd/wb deadcode"
	baseline := deadcodeFailureReport(command, "bounded baseline output", "example.test/pkg.A", "example.test/pkg.B")
	for _, test := range []struct {
		name      string
		candidate quality.VerificationReport
		fails     bool
	}{
		{"equal", deadcodeFailureReport(command, "different bounded output", "example.test/pkg.A", "example.test/pkg.B"), false},
		{"subset", deadcodeFailureReport(command, "different count and source lines", "example.test/pkg.B"), false},
		{"new identity with lower count", deadcodeFailureReport(command, "one new finding", "example.test/pkg.C"), true},
		{"different command", deadcodeFailureReport("go run ./cmd/wb deadcode --packages=./cmd/wb", "same identities", "example.test/pkg.A"), true},
		{"different module", deadcodeFailureReport(command, "same identities", "example.test/pkg.A"), true},
		{"different check", deadcodeFailureReport(command, "same identities", "example.test/pkg.A"), true},
		{"count mismatch", deadcodeFailureReport(command, "same detail", "example.test/pkg.A"), true},
		{"incomplete evidence", deadcodeFailureReport(command, "same detail", "example.test/pkg.A"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.name == "count mismatch" {
				test.candidate.Results[0].Deadcode.Count++
			}
			if test.name == "different module" {
				test.candidate.Results[0].Module = "./other"
			}
			if test.name == "different check" {
				test.candidate.Results[0].Check = quality.CheckBuild
			}
			if test.name == "incomplete evidence" {
				test.candidate.Results[0].Deadcode.Complete = false
			}
			err := worktreeMergeValidationRegression(baseline, test.candidate)
			if (err != nil) != test.fails {
				t.Fatalf("regression error = %v, want failure=%t", err, test.fails)
			}
		})
	}
}

func TestWorktreeMergeDeadcodeOldReceiptKeepsExactMatch(t *testing.T) {
	t.Parallel()
	const command = "go run ./cmd/wb deadcode"
	baseline := deadcodeFailureReport(command, "same old diagnostic", "example.test/pkg.A")
	baseline.Results[0].Deadcode = nil
	if err := worktreeMergeValidationRegression(baseline, deadcodeFailureReport(command, "same old diagnostic", "example.test/pkg.A")); err != nil {
		t.Fatalf("matching old receipt rejected: %v", err)
	}
	if err := worktreeMergeValidationRegression(baseline, deadcodeFailureReport(command, "changed diagnostic", "example.test/pkg.A")); err == nil {
		t.Fatal("old receipt accepted different diagnostic without structured evidence")
	}
}

func TestWorktreeMergeDeadcodeUnionIsBoundedToAttestedImportedMain(t *testing.T) {
	t.Parallel()
	const command = "go run ./cmd/wb deadcode"
	baseline := deadcodeFailureReport(command, "target", "target.A")
	imported := &WorktreeMergeImportedMainDeadcode{Validation: deadcodeFailureReport(command, "main", "main.B")}
	for _, test := range []struct {
		name      string
		candidate quality.VerificationReport
		wantErr   bool
	}{
		{"equal union", deadcodeFailureReport(command, "both", "target.A", "main.B"), false},
		{"subset of imported parent", deadcodeFailureReport(command, "one", "main.B"), false},
		{"candidate-only identity", deadcodeFailureReport(command, "new", "candidate.C"), true},
		{"truncated parent evidence", deadcodeFailureReport(command, "truncated", "main.B"), true},
		{"parent command mismatch", deadcodeFailureReport(command, "command", "main.B"), true},
		{"parent check mismatch", deadcodeFailureReport(command, "check", "main.B"), true},
		{"parent module mismatch", deadcodeFailureReport(command, "module", "main.B"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parent := *imported
			parent.Validation = deadcodeFailureReport(command, "main", "main.B")
			if test.name == "truncated parent evidence" {
				parent.Validation.Results[0].Deadcode.Complete = false
			}
			if test.name == "parent command mismatch" {
				parent.Validation.Results[0].Command += " --other"
			}
			if test.name == "parent check mismatch" {
				parent.Validation.Results[0].Check = quality.CheckBuild
			}
			if test.name == "parent module mismatch" {
				parent.Validation.Results[0].Module = "./other"
			}
			err := worktreeMergeValidationRegressionWithImportedMain(baseline, test.candidate, &parent)
			if (err != nil) != test.wantErr {
				t.Fatalf("regression error = %v, want failure=%t", err, test.wantErr)
			}
		})
	}
}

func TestWorktreeMergeDeadcodeUnionAcceptsImportedIdentityWithCleanTarget(t *testing.T) {
	t.Parallel()
	const command = worktreeMergeDeadcodeCommand
	baseline := quality.VerificationReport{Status: quality.StatusPassed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command, Status: quality.StatusPassed,
	}}}
	imported := &WorktreeMergeImportedMainDeadcode{Validation: deadcodeFailureReport(command, "main", "main.B")}
	if err := worktreeMergeValidationRegressionWithImportedMain(baseline, deadcodeFailureReport(command, "candidate", "main.B"), imported); err != nil {
		t.Fatalf("imported identity rejected with clean target: %v", err)
	}
	if err := worktreeMergeValidationRegressionWithImportedMain(baseline, deadcodeFailureReport(command, "candidate-only", "candidate.C"), imported); err == nil {
		t.Fatal("candidate-only identity accepted with clean target")
	}
}

func TestWorktreeMergeValidationDoesNotAttestWhenTargetAlreadyCoversDeadcode(t *testing.T) {
	t.Parallel()
	const command = worktreeMergeDeadcodeCommand
	baseline := deadcodeFailureReport(command, "target", "target.A", "target.B")
	candidate := deadcodeFailureReport(command, "candidate", "target.B")
	attestCalls := 0
	evidence, err := worktreeMergeValidationWithImportedMainAttestation(baseline, candidate, func() (*WorktreeMergeImportedMainDeadcode, error) {
		attestCalls++
		return nil, nil
	})
	if err != nil || evidence != nil || attestCalls != 0 {
		t.Fatalf("target-covered deadcode result = (%+v, %v), attest calls %d", evidence, err, attestCalls)
	}

	candidate.Results = append(candidate.Results, quality.VerificationEntry{Language: "go", Module: ".", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusFailed, Detail: "new build failure"})
	if _, err := worktreeMergeValidationWithImportedMainAttestation(baseline, candidate, func() (*WorktreeMergeImportedMainDeadcode, error) {
		attestCalls++
		return nil, nil
	}); err == nil || attestCalls != 0 {
		t.Fatalf("non-deadcode regression result = %v, attest calls %d", err, attestCalls)
	}
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
	initialMainSHA, err := verifyImportedMainLineage(ctx, repository, importedSHA, "", 0)
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
	if current, err := verifyImportedMainLineage(ctx, repository, importedSHA, initialMainSHA, 0); err != nil || current != advancedMainSHA {
		t.Fatalf("fast-forward lineage recheck = (%s, %v), want %s", current, err, advancedMainSHA)
	}
	const command = worktreeMergeDeadcodeCommand
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
		ImportedMainDeadcode: &WorktreeMergeImportedMainDeadcode{
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
	initial, err := verifyImportedMainLineage(ctx, repository, importedSHA, "", 0)
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
	if current, err := verifyImportedMainLineage(ctx, repository, importedSHA, initial, 0); err != nil || current != secondAdvance {
		t.Fatalf("second fast-forward = (%s, %v), want %s", current, err, secondAdvance)
	}
	gitMergeGraphTest(t, repository, "push", "--force", "origin", initial+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, repository, importedSHA, secondAdvance, 0); err == nil {
		t.Fatal("rewind to the previously attested head was accepted")
	}
	baseSHA := gitMergeGraphTest(t, repository, "rev-parse", importedSHA+"^")
	gitMergeGraphTest(t, repository, "push", "--force", "origin", baseSHA+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, repository, importedSHA, initial, 0); err == nil {
		t.Fatal("rewound origin/main accepted")
	}
	targetSHA := gitMergeGraphTest(t, repository, "rev-parse", mergeSHA+"^1")
	gitMergeGraphTest(t, repository, "push", "--force", "origin", targetSHA+":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, repository, importedSHA, initial, 0); err == nil {
		t.Fatal("diverged origin/main accepted")
	}
	if _, err := matchedFetchedOriginMain("", ""); err == nil {
		t.Fatal("missing fetched and remote heads accepted")
	}
	if _, err := matchedFetchedOriginMain(importedSHA, "different refs/heads/main"); err == nil {
		t.Fatal("fetch/ls-remote mismatch accepted")
	}
	if err := requireGitAncestor(ctx, repository, "missing-commit", targetSHA); err == nil {
		t.Fatal("missing ancestry commit accepted")
	}
	gitMergeGraphTest(t, repository, "push", "--force", "origin", ":refs/heads/main")
	if _, err := verifyImportedMainLineage(ctx, repository, importedSHA, initial, 0); err == nil {
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

func TestWorktreeMergeImportedMainDeadcodeReceiptRoundTrips(t *testing.T) {
	t.Parallel()
	evidence := WorktreeMergeImportedMainDeadcode{
		CandidateSHA: "candidate", TargetSHA: "target", MergeSHA: "merge", ImportedSHA: "main", OriginMainSHA: "main",
		Validation: deadcodeFailureReport(worktreeMergeDeadcodeCommand, "complete", "main.A"),
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var restored WorktreeMergeImportedMainDeadcode
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !validImportedMainDeadcodeReport(restored.Validation) || restored.Validation.Results[0].Deadcode.Identities[0] != "main.A" {
		t.Fatalf("round-tripped imported evidence lost complete identities: %+v", restored)
	}
}

func TestWorktreeMergeImportedMainDeadcodeReportRequiresExactCompleteEvidence(t *testing.T) {
	t.Parallel()
	report := deadcodeFailureReport(worktreeMergeDeadcodeCommand, "parent", "main.A")
	if !validImportedMainDeadcodeReport(report) {
		t.Fatal("complete exact deadcode report rejected")
	}
	report.Results[0].Deadcode.Complete = false
	if validImportedMainDeadcodeReport(report) {
		t.Fatal("incomplete parent report accepted")
	}
	report = deadcodeFailureReport("go run ./cmd/wb deadcode --other", "parent", "main.A")
	if validImportedMainDeadcodeReport(report) {
		t.Fatal("different command accepted")
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
	gotMerge, gotImported, found, err := worktreeMergeImportedMainGraph(context.Background(), repository, candidate, target)
	if err != nil || !found || gotMerge != merge || gotImported != imported {
		t.Fatalf("valid graph = (%s, %s, %t, %v)", gotMerge, gotImported, found, err)
	}

	gitMergeGraphTest(t, repository, "checkout", "-b", "reverse", imported)
	gitMergeGraphTest(t, repository, "merge", "--no-ff", target, "-m", "reverse parents")
	reversed := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	if _, _, _, err := worktreeMergeImportedMainGraph(context.Background(), repository, reversed, target); err == nil {
		t.Fatal("wrong parent order accepted")
	}

	gitMergeGraphTest(t, repository, "checkout", "-b", "extra", candidate)
	gitMergeGraphTest(t, repository, "checkout", "-b", "side", target)
	writeAndCommit("side", "side")
	gitMergeGraphTest(t, repository, "checkout", "extra")
	gitMergeGraphTest(t, repository, "merge", "--no-ff", "side", "-m", "extra merge")
	extra := gitMergeGraphTest(t, repository, "rev-parse", "HEAD")
	if _, _, _, err := worktreeMergeImportedMainGraph(context.Background(), repository, extra, target); err == nil {
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
