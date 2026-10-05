package mergevalidation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestWorktreeMergeDeadcodeRegressionDisplaysOnlyExactTargetDelta(t *testing.T) {
	t.Parallel()
	const command = DeadcodeCommand
	inherited := make([]string, 228)
	for index := range inherited {
		inherited[index] = fmt.Sprintf("example.test/pkg.Target%03d", index+1)
	}
	baseline := deadcodeFailureReport(command, "bounded target diagnostic", inherited...)
	identities := append(append([]string(nil), inherited...), "example.test/pkg.NewFinding")
	candidate := deadcodeFailureReport(command, "bounded candidate diagnostic", identities...)
	err := worktreeMergeValidationRegression(baseline, candidate)
	if err == nil || !strings.Contains(err.Error(), "1 deadcode finding(s) absent from exact target: example.test/pkg.NewFinding") {
		t.Fatalf("229-candidate/228-target regression = %v, want the single new identity", err)
	}
	for _, identity := range inherited {
		if strings.Contains(err.Error(), identity) {
			t.Fatalf("regression diagnostic leaked inherited target identity %s: %v", identity, err)
		}
	}
	if err := worktreeMergeValidationRegression(baseline, deadcodeFailureReport(command, "different bounded detail", inherited[1:]...)); err != nil {
		t.Fatalf("target subset rejected: %v", err)
	}

	cleanTarget := quality.VerificationReport{Status: quality.StatusPassed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command, Status: quality.StatusPassed,
	}}}
	err = worktreeMergeValidationRegression(cleanTarget, deadcodeFailureReport(command, "candidate", "example.test/pkg.NewFinding"))
	if err == nil || !strings.Contains(err.Error(), "1 deadcode finding(s) absent from exact target: example.test/pkg.NewFinding") {
		t.Fatalf("clean-target regression = %v, want the candidate-only identity", err)
	}
}

func TestWorktreeMergeDeadcodeTargetDeltaRequiresCompleteUniqueEvidence(t *testing.T) {
	t.Parallel()
	const command = DeadcodeCommand
	target := deadcodeFailureReport(command, "target", "example.test/pkg.Known")
	candidate := deadcodeFailureReport(command, "candidate", "example.test/pkg.Known", "example.test/pkg.New")
	if delta, ok := worktreeMergeDeadcodeTargetDelta(target.Results, candidate.Results[0]); !ok || len(delta) != 1 || delta[0] != "example.test/pkg.New" {
		t.Fatalf("complete target delta = (%v, %t), want only New", delta, ok)
	}

	for _, test := range []struct {
		name      string
		target    quality.VerificationReport
		candidate quality.VerificationReport
	}{
		{"empty delta", target, deadcodeFailureReport(command, "candidate", "example.test/pkg.Known")},
		{"incomplete candidate", target, deadcodeFailureReport(command, "candidate", "example.test/pkg.New")},
		{"ambiguous target", quality.VerificationReport{Results: append(append([]quality.VerificationEntry(nil), target.Results...), target.Results[0])}, candidate},
		{"legacy target", quality.VerificationReport{Results: []quality.VerificationEntry{{Language: "go", Module: ".", Check: quality.CheckLint, Command: command, Status: quality.StatusFailed}}}, candidate},
		{"different command", target, deadcodeFailureReport(command+" --other", "candidate", "example.test/pkg.New")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.name == "incomplete candidate" {
				test.candidate.Results[0].Deadcode.Complete = false
			}
			if delta, ok := worktreeMergeDeadcodeTargetDelta(test.target.Results, test.candidate.Results[0]); ok || len(delta) != 0 {
				t.Fatalf("untrusted target delta = (%v, %t), want no diagnostic delta", delta, ok)
			}
			err := worktreeMergeValidationRegression(test.target, test.candidate)
			if test.name != "empty delta" && (err == nil || !strings.Contains(err.Error(), "introduced or changed deadcode failure")) {
				t.Fatalf("untrusted evidence regression = %v, want generic refusal", err)
			}
		})
	}
}

func TestWorktreeMergeDeadcodeImportedProofRejectionKeepsGenericDiagnostic(t *testing.T) {
	t.Parallel()
	const command = DeadcodeCommand
	target := deadcodeFailureReport(command, "exact target", "target.A")
	for _, test := range []struct {
		name      string
		candidate quality.VerificationReport
		malformed bool
	}{
		{"attested identity plus new finding", deadcodeFailureReport(command, "candidate", "target.A", "main.B", "candidate.C"), false},
		{"incomplete imported proof", deadcodeFailureReport(command, "candidate", "target.A", "main.B"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			imported := &ImportedMainDeadcode{Validation: deadcodeFailureReport(command, "imported main", "main.B")}
			if test.malformed {
				imported.Validation.Results[0].Deadcode.Complete = false
			}
			err := RegressionWithImportedMain(target, test.candidate, imported)
			if err == nil || err.Error() != "candidate validation introduced or changed deadcode failure: "+command {
				t.Fatalf("imported proof rejection = %v, want generic refusal without a misleading target-only delta", err)
			}
		})
	}
}

func TestWorktreeMergeDeadcodeRegressionRefusesFailedReportWithoutFailedCheck(t *testing.T) {
	t.Parallel()
	baseline := quality.VerificationReport{Status: quality.StatusPassed}
	candidate := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: DeadcodeCommand, Status: quality.StatusPassed,
	}}}
	err := worktreeMergeValidationRegression(baseline, candidate)
	if err == nil || err.Error() != "candidate validation reported failure without failed check evidence" {
		t.Fatalf("failed report without a failed check = %v, want explicit evidence refusal", err)
	}
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
	imported := &ImportedMainDeadcode{Validation: deadcodeFailureReport(command, "main", "main.B")}
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
			err := RegressionWithImportedMain(baseline, test.candidate, &parent)
			if (err != nil) != test.wantErr {
				t.Fatalf("regression error = %v, want failure=%t", err, test.wantErr)
			}
		})
	}
}

func TestWorktreeMergeDeadcodeUnionAcceptsImportedIdentityWithCleanTarget(t *testing.T) {
	t.Parallel()
	const command = DeadcodeCommand
	baseline := quality.VerificationReport{Status: quality.StatusPassed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command, Status: quality.StatusPassed,
	}}}
	imported := &ImportedMainDeadcode{Validation: deadcodeFailureReport(command, "main", "main.B")}
	if err := RegressionWithImportedMain(baseline, deadcodeFailureReport(command, "candidate", "main.B"), imported); err != nil {
		t.Fatalf("imported identity rejected with clean target: %v", err)
	}
	if err := RegressionWithImportedMain(baseline, deadcodeFailureReport(command, "candidate-only", "candidate.C"), imported); err == nil {
		t.Fatal("candidate-only identity accepted with clean target")
	}
}

func TestWorktreeMergeValidationDoesNotAttestWhenTargetAlreadyCoversDeadcode(t *testing.T) {
	t.Parallel()
	const command = DeadcodeCommand
	baseline := deadcodeFailureReport(command, "target", "target.A", "target.B")
	candidate := deadcodeFailureReport(command, "candidate", "target.B")
	attestCalls := 0
	evidence, err := WithImportedMainAttestation(baseline, candidate, func() (*ImportedMainDeadcode, error) {
		attestCalls++
		return nil, nil
	})
	if err != nil || evidence != nil || attestCalls != 0 {
		t.Fatalf("target-covered deadcode result = (%+v, %v), attest calls %d", evidence, err, attestCalls)
	}

	candidate.Results = append(candidate.Results, quality.VerificationEntry{Language: "go", Module: ".", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusFailed, Detail: "new build failure"})
	if _, err := WithImportedMainAttestation(baseline, candidate, func() (*ImportedMainDeadcode, error) {
		attestCalls++
		return nil, nil
	}); err == nil || attestCalls != 0 {
		t.Fatalf("non-deadcode regression result = %v, attest calls %d", err, attestCalls)
	}
}

func TestWorktreeMergeImportedMainDeadcodeReportRequiresExactCompleteEvidence(t *testing.T) {
	t.Parallel()
	report := deadcodeFailureReport(DeadcodeCommand, "parent", "main.A")
	if !ValidImportedMainDeadcodeReport(report) {
		t.Fatal("complete exact deadcode report rejected")
	}
	report.Results[0].Deadcode.Complete = false
	if ValidImportedMainDeadcodeReport(report) {
		t.Fatal("incomplete parent report accepted")
	}
	report = deadcodeFailureReport("go run ./cmd/wb deadcode --other", "parent", "main.A")
	if ValidImportedMainDeadcodeReport(report) {
		t.Fatal("different command accepted")
	}
}
