package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func deadcodeFailureReport(command, detail string, identities ...string) quality.VerificationReport {
	return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command,
		Status: quality.StatusFailed, Detail: detail,
		Deadcode: &quality.DeadcodeFailureEvidence{Count: len(identities), Identities: identities, Complete: true},
	}}}
}

func TestWorktreeMergeDeadcodeNonRegressionUsesCompleteIdentities(t *testing.T) {
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
	if reusable, err := preparedValidationStillValid(loaded, worktreeMergeValidationPlan{}); err != nil || !reusable {
		t.Fatalf("loaded receipt reuse = (%t, %v)", reusable, err)
	}
	if err := requireWorktreeMergePublishedValidation(loaded, worktreeMergeValidationPlan{}); err != nil {
		t.Fatalf("loaded receipt publish guard rejected exact validation: %v", err)
	}
}

func TestWorktreeMergeImportedMainDeadcodeReceiptRoundTrips(t *testing.T) {
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
