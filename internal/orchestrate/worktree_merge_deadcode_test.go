package orchestrate

import (
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
