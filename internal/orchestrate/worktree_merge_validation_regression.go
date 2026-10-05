package orchestrate

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
)

func worktreeMergeValidationRegression(baseline, candidate quality.VerificationReport) error {
	return worktreeMergeValidationRegressionWithImportedMain(baseline, candidate, nil)
}

func worktreeMergeValidationRegressionWithImportedMain(baseline, candidate quality.VerificationReport, imported *WorktreeMergeImportedMainDeadcode) error {
	baselineFailures := failedWorktreeMergeVerificationEntries(baseline)
	candidateFailures := failedWorktreeMergeVerificationEntries(candidate)
	if candidate.Status == quality.StatusFailed && len(candidateFailures) == 0 {
		return errors.New("candidate validation reported failure without failed check evidence")
	}
	matched := make([]bool, len(baselineFailures))
	for _, candidateFailure := range candidateFailures {
		if candidateFailure.Deadcode != nil {
			if matchDeadcodeBaselineFailure(baselineFailures, candidateFailure) || matchImportedMainDeadcodeFailure(baseline.Results, candidateFailure, imported) {
				continue
			}
			if imported == nil {
				if delta, ok := worktreeMergeDeadcodeTargetDelta(baseline.Results, candidateFailure); ok {
					return fmt.Errorf("candidate validation has %d deadcode finding(s) absent from exact target: %s", len(delta), strings.Join(delta, ", "))
				}
			}
			return fmt.Errorf("candidate validation introduced or changed deadcode failure: %s", candidateFailure.Command)
		}
		if candidateFailure.Language == "go" && candidateFailure.Check == quality.CheckTest {
			if matchGoCoverageBaselineFailure(baselineFailures, candidateFailure) {
				continue
			}
		}
		if candidateFailure.Language == "specscore" {
			if matchSpecScoreBaselineFailure(baselineFailures, candidateFailure) {
				continue
			}
			return fmt.Errorf("candidate validation introduced or changed failure: %s %s %s", candidateFailure.Language, candidateFailure.Check, candidateFailure.Command)
		}
		found := false
		for index, baselineFailure := range baselineFailures {
			if !matched[index] && sameWorktreeMergeFailure(baselineFailure, candidateFailure) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("candidate validation introduced or changed failure: %s %s %s", candidateFailure.Language, candidateFailure.Check, candidateFailure.Command)
		}
	}
	return nil
}

func hasWorktreeMergeDeadcodeFailure(report quality.VerificationReport) bool {
	for _, entry := range report.Results {
		if entry.Deadcode != nil {
			return true
		}
	}
	return false
}

func worktreeMergeNonDeadcodeRegression(baseline, candidate quality.VerificationReport) error {
	withoutDeadcode := func(report quality.VerificationReport) quality.VerificationReport {
		results := make([]quality.VerificationEntry, 0, len(report.Results))
		for _, entry := range report.Results {
			if entry.Deadcode == nil {
				results = append(results, entry)
			}
		}
		report.Results = results
		return report
	}
	baseline, candidate = withoutDeadcode(baseline), withoutDeadcode(candidate)
	if candidate.Status == quality.StatusFailed && len(failedWorktreeMergeVerificationEntries(candidate)) == 0 {
		candidate.Status = quality.StatusPassed
	}
	return worktreeMergeValidationRegression(baseline, candidate)
}

func worktreeMergeValidationWithImportedMainAttestation(baseline, candidate quality.VerificationReport, attest func() (*WorktreeMergeImportedMainDeadcode, error)) (*WorktreeMergeImportedMainDeadcode, error) {
	targetErr := worktreeMergeValidationRegression(baseline, candidate)
	if targetErr == nil {
		return nil, nil
	}
	if !hasWorktreeMergeDeadcodeFailure(candidate) || worktreeMergeNonDeadcodeRegression(baseline, candidate) != nil {
		return nil, targetErr
	}
	evidence, err := attest()
	if err != nil {
		return nil, fmt.Errorf("attest imported main deadcode baseline: %w", err)
	}
	if err := worktreeMergeValidationRegressionWithImportedMain(baseline, candidate, evidence); err != nil {
		return evidence, err
	}
	return evidence, nil
}

func matchImportedMainDeadcodeFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry, imported *WorktreeMergeImportedMainDeadcode) bool {
	if imported == nil || candidate.Language != "go" || candidate.Check != quality.CheckLint || candidate.Command != worktreeMergeDeadcodeCommand || !candidate.Deadcode.Valid() {
		return false
	}
	target, valid := worktreeMergeDeadcodeIdentitySet(baseline, candidate.Language, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return false
	}
	parent, valid := importedMainDeadcodeIdentities(imported.Validation, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return false
	}
	for id := range parent {
		target[id] = true
	}
	return worktreeMergeDeadcodeIdentitiesKnown(candidate.Deadcode.Identities, target)
}

func worktreeMergeDeadcodeIdentitySet(entries []quality.VerificationEntry, language string, check quality.Check, command, module string) (map[string]bool, bool) {
	var matching *quality.VerificationEntry
	for index := range entries {
		entry := &entries[index]
		if entry.Language != language || entry.Check != check || entry.Command != command || entry.Module != module {
			continue
		}
		if matching != nil {
			return nil, false
		}
		matching = entry
	}
	if matching == nil {
		return nil, false
	}
	if matching.Status == quality.StatusPassed && matching.Deadcode == nil {
		return map[string]bool{}, true
	}
	if matching.Status != quality.StatusFailed || !matching.Deadcode.Valid() {
		return nil, false
	}
	identities := make(map[string]bool, len(matching.Deadcode.Identities))
	for _, identity := range matching.Deadcode.Identities {
		identities[identity] = true
	}
	return identities, true
}

// worktreeMergeDeadcodeTargetDelta is diagnostic only. An incomplete or
// ambiguous exact-target report cannot supply a trustworthy difference.
func worktreeMergeDeadcodeTargetDelta(target []quality.VerificationEntry, candidate quality.VerificationEntry) ([]string, bool) {
	if !candidate.Deadcode.Valid() {
		return nil, false
	}
	known, valid := worktreeMergeDeadcodeIdentitySet(target, candidate.Language, candidate.Check, candidate.Command, candidate.Module)
	if !valid {
		return nil, false
	}
	var delta []string
	for _, identity := range candidate.Deadcode.Identities {
		if !known[identity] {
			delta = append(delta, identity)
		}
	}
	return delta, len(delta) > 0
}

// matchDeadcodeBaselineFailure compares complete function identities, not the
// bounded human diagnostic or its count alone. An older baseline receipt
// without structured evidence retains the strict historical detail match.
func matchDeadcodeBaselineFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry) bool {
	if candidate.Language != "go" || candidate.Check != quality.CheckLint || candidate.Command != "go run ./cmd/wb deadcode" || !candidate.Deadcode.Valid() {
		return false
	}
	for _, previous := range baseline {
		if previous.Language != candidate.Language || previous.Module != candidate.Module || previous.Check != candidate.Check || previous.Command != candidate.Command {
			continue
		}
		if previous.Deadcode == nil {
			return sameWorktreeMergeFailure(previous, candidate)
		}
		if !previous.Deadcode.Valid() {
			return false
		}
		known := make(map[string]bool, len(previous.Deadcode.Identities))
		for _, identity := range previous.Deadcode.Identities {
			known[identity] = true
		}
		return worktreeMergeDeadcodeIdentitiesKnown(candidate.Deadcode.Identities, known)
	}
	return false
}

func failedWorktreeMergeVerificationEntries(report quality.VerificationReport) []quality.VerificationEntry {
	entries := make([]quality.VerificationEntry, 0, len(report.Results))
	for _, entry := range report.Results {
		if entry.Status == quality.StatusFailed {
			entries = append(entries, entry)
		}
	}
	return entries
}

func sameWorktreeMergeFailure(baseline, candidate quality.VerificationEntry) bool {
	// The discriminating comparison is the exact check identity plus the
	// normalized diagnostic. Any identity mismatch or remaining normalized
	// detail difference falsifies equivalence.
	return baseline.Language == candidate.Language && baseline.Module == candidate.Module && baseline.Check == candidate.Check &&
		baseline.Command == candidate.Command && normalizeWorktreeMergeFailureDetail(baseline.Detail) == normalizeWorktreeMergeFailureDetail(candidate.Detail)
}

var (
	worktreeMergeFailureTimestampPattern     = regexp.MustCompile(`(?m)^((?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9])(\s+\[[^\r\n]*\]|\s+[✓├])`)
	worktreeMergeFailureGeneratedPattern     = regexp.MustCompile(`(?i)\b(Generated)\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\b`)
	worktreeMergeFailureBuiltPattern         = regexp.MustCompile(`(?i)\b(built in|Completed in)\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\b`)
	worktreeMergeFailureParenthesizedPattern = regexp.MustCompile(`\(\+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)\)`)
	worktreeMergeFailureTruncationPattern    = regexp.MustCompile(`(?s)(… output truncated; final [0-9]+ bytes:\r?\n)([^\r\n]*)`)
	worktreeMergeFailurePartialTimingPattern = regexp.MustCompile(`(?i)^([[:alpha:]]+)\s+in\s+[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h)$`)
)

func normalizeWorktreeMergeFailureDetail(detail string) string {
	// Quality command output can include the ephemeral checkout path. It is not
	// behavior, so compare a whitespace-normalized form after erasing absolute
	// paths. The path may be quoted or wrapped in diagnostic punctuation, so
	// normalize it inside the field rather than requiring the whole field to be
	// an absolute path. All command, check, module, and error text still has to
	// match.
	detail = worktreeMergeFailureTimestampPattern.ReplaceAllString(detail, `<timestamp>${2}`)
	detail = worktreeMergeFailureGeneratedPattern.ReplaceAllString(detail, `${1} <duration>`)
	detail = worktreeMergeFailureBuiltPattern.ReplaceAllString(detail, `${1} <duration>`)
	detail = worktreeMergeFailureParenthesizedPattern.ReplaceAllString(detail, `(+<duration>)`)
	detail = worktreeMergeFailureTruncationPattern.ReplaceAllStringFunc(detail, normalizeWorktreeMergeFailureTruncatedTail)
	fields := strings.Fields(detail)
	for index, field := range fields {
		fields[index] = normalizeWorktreeMergeFailureField(field)
	}
	return strings.Join(fields, " ")
}

func normalizeWorktreeMergeFailureTruncatedTail(match string) string {
	const markerEnd = "\n"
	lineStart := strings.Index(match, markerEnd)
	if lineStart < 0 || lineStart+len(markerEnd) >= len(match) {
		return match
	}
	line := strings.TrimSpace(match[lineStart+len(markerEnd):])
	partial := worktreeMergeFailurePartialTimingPattern.FindStringSubmatch(line)
	if len(partial) != 2 {
		return match
	}
	word := strings.ToLower(partial[1])
	for _, complete := range []string{"built", "completed"} {
		if strings.HasSuffix(complete, word) {
			return match[:lineStart+len(markerEnd)] + complete + " in <duration>"
		}
	}
	return match
}

const worktreeMergeFailurePathPunctuation = "\"'`()[]{}<>,;."

func normalizeWorktreeMergeFailureField(field string) string {
	start := 0
	for start < len(field) && strings.ContainsRune(worktreeMergeFailurePathPunctuation, rune(field[start])) {
		start++
	}
	end := len(field)
	for end > start && strings.ContainsRune(worktreeMergeFailurePathPunctuation, rune(field[end-1])) {
		end--
	}
	if start != end && filepath.IsAbs(field[start:end]) {
		field = field[:start] + "<workspace>" + field[end:]
	}
	return field
}

func worktreeMergeDeadcodeIdentitiesKnown(identities []string, known map[string]bool) bool {
	for _, identity := range identities {
		if !known[identity] {
			return false
		}
	}
	return true
}
