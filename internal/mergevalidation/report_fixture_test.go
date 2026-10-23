package mergevalidation

import "github.com/sneat-dev/wb/internal/quality"

// Literal report setup shared by this owner's tests; no comparison policy or native fixture.
func deadcodeFailureReport(command, detail string, identities ...string) quality.VerificationReport {
	return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
		Language: "go", Module: ".", Check: quality.CheckLint, Command: command,
		Status: quality.StatusFailed, Detail: detail,
		Deadcode: &quality.DeadcodeFailureEvidence{Count: len(identities), Identities: identities, Complete: true},
	}}}
}
