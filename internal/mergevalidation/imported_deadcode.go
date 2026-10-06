package mergevalidation

import "github.com/sneat-dev/wb/internal/quality"

const DeadcodeCommand = "go run ./cmd/wb deadcode"

// ImportedMainDeadcode binds the one imported main parent to the
// candidate and the exact target whose validation it may supplement.
type ImportedMainDeadcode struct {
	CandidateSHA string `json:"candidate_sha"`
	TargetSHA    string `json:"target_sha"`
	MergeSHA     string `json:"merge_sha"`
	ImportedSHA  string `json:"imported_sha"`
	// OriginMainSHA is the authoritative main head at initial validation. It
	// may be later than ImportedSHA; resumes require it to remain an ancestor
	// of the freshly fetched authoritative main head.
	OriginMainSHA string                     `json:"origin_main_sha"`
	Validation    quality.VerificationReport `json:"validation"`
}

func ValidImportedMainDeadcodeReport(report quality.VerificationReport) bool {
	entry, unique := ImportedMainDeadcodeEntry(report)
	return unique && (entry.Status == quality.StatusPassed || (entry.Status == quality.StatusFailed && entry.Deadcode.Valid()))
}

func ImportedMainDeadcodeEntry(report quality.VerificationReport) (quality.VerificationEntry, bool) {
	var match quality.VerificationEntry
	count := 0
	for _, entry := range report.Results {
		if entry.Language == "go" && entry.Check == quality.CheckLint && entry.Command == DeadcodeCommand {
			match, count = entry, count+1
		}
	}
	return match, count == 1
}

func importedMainDeadcodeIdentities(report quality.VerificationReport, check quality.Check, command, module string) (map[string]bool, bool) {
	return worktreeMergeDeadcodeIdentitySet(report.Results, "go", check, command, module)
}
