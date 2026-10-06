package mergevalidation

import (
	"regexp"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// matchGoCoverageBaselineFailure compares the failing-test identities emitted
// by WB's compact coverage index. Process-isolated shard numbers are scheduler
// placement, not failure identity, and can change when the package inventory
// changes. A candidate may remove baseline failures but must not add a failing
// test that was absent from the exact target baseline.
func matchGoCoverageBaselineFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry) bool {
	candidateIDs := goCoverageFailureIdentities(candidate.Detail)
	if len(candidateIDs) == 0 {
		return false
	}
	for _, baselineFailure := range baseline {
		if baselineFailure.Language != candidate.Language || baselineFailure.Module != candidate.Module || baselineFailure.Check != candidate.Check || normalizeGoCoverageCommand(baselineFailure.Command) != normalizeGoCoverageCommand(candidate.Command) {
			continue
		}
		baselineIDs := goCoverageFailureIdentities(baselineFailure.Detail)
		if len(baselineIDs) == 0 {
			continue
		}
		if worktreeMergeFailureIdentitySubset(candidateIDs, baselineIDs) {
			return true
		}
	}
	return false
}

var (
	goCoverageShardPlacementPattern = regexp.MustCompile(`\s+shard\s+[0-9]+/[0-9]+$`)
	goCoverageCommandShardsPattern  = regexp.MustCompile(`\s+\([0-9]+\s+process-isolated shards for [^)]*\)$`)
	// A raw-output section header: either the unsharded group or one package
	// path, optionally carrying its scheduler shard placement.
	goCoveragePlacementHeaderPattern = regexp.MustCompile(`^\[(unsharded packages|[^\[\] \t]+)(\s+shard\s+[0-9]+/[0-9]+)?\]$`)
)

// goCoverageTimeoutIdentity is the failure identity of a group that ran out of
// time instead of failing a named test.
const goCoverageTimeoutIdentity = "timed out"

func normalizeGoCoverageCommand(command string) string {
	return goCoverageCommandShardsPattern.ReplaceAllString(command, " (<process-isolated shards>)")
}

func goCoverageFailureIdentities(detail string) map[string]struct{} {
	const (
		failureIndexHeader = "WB coverage failure index:\n"
	)
	identities := make(map[string]struct{})
	indexStart := strings.Index(detail, failureIndexHeader)
	if indexStart < 0 {
		return identities
	}
	index := detail[indexStart+len(failureIndexHeader):]
	rawOutput := ""
	for _, header := range []string{"WB coverage raw output:\n", "WB coverage raw output\n"} {
		if raw := strings.Index(index, header); raw >= 0 {
			rawOutput = index[raw+len(header):]
			index = index[:raw]
			break
		}
	}
	// A test-binary timeout kills the group wherever it happens to be, so the
	// test it names is incidental: the same pre-existing timeout names a
	// different test, or none at all, on the next run. Collapse a timed-out
	// placement to one identity so a repeated timeout is recognised as the
	// baseline failure it is instead of reading as a newly introduced one.
	timedOut := goCoverageTimedOutPlacements(rawOutput)
	for _, rawLine := range strings.Split(index, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		closing := strings.Index(line, "] ")
		if closing < 0 {
			continue
		}
		placement := strings.TrimPrefix(line[:closing], "- [")
		placement = goCoverageShardPlacementPattern.ReplaceAllString(placement, "")
		testName := strings.TrimSpace(line[closing+2:])
		if placement == "" || testName == "" {
			continue
		}
		stableTimeout, hasSource := goCoverageTimeoutFailureIdentity(testName)
		if hasSource {
			// Elapsed time is diagnostic data; the timeout source is part of
			// the failure identity and must survive raw-output timeout folding.
			testName = stableTimeout
		} else if _, ok := timedOut[placement]; ok {
			testName = goCoverageTimeoutIdentity
		}
		identities[placement+"\x00"+testName] = struct{}{}
	}
	return identities
}

func goCoverageTimeoutFailureIdentity(detail string) (string, bool) {
	if !strings.HasSuffix(detail, ")") {
		return "", false
	}
	start := strings.LastIndex(detail, " (")
	if start <= 0 {
		return "", false
	}
	cause, elapsed, ok := strings.Cut(strings.TrimSuffix(detail[start+2:], ")"), "; elapsed ")
	if !ok {
		return "", false
	}
	switch cause {
	case "attempt timeout", "check timeout", "caller timeout", "caller-cancelled cancellation":
	default:
		return "", false
	}
	if _, err := time.ParseDuration(elapsed); err != nil {
		return "", false
	}
	return detail[:start] + " (" + cause + ")", true
}

// goCoverageTimedOutPlacements reports which check placements recorded a
// test-binary timeout in WB's coverage raw output.
func goCoverageTimedOutPlacements(rawOutput string) map[string]struct{} {
	placements := make(map[string]struct{})
	placement := ""
	for _, rawLine := range strings.Split(rawOutput, "\n") {
		line := strings.TrimSpace(rawLine)
		if goCoveragePlacementHeaderPattern.MatchString(line) {
			placement = goCoverageShardPlacementPattern.ReplaceAllString(strings.Trim(line, "[]"), "")
			continue
		}
		if placement != "" && strings.Contains(line, "timed out after") {
			placements[placement] = struct{}{}
		}
	}
	return placements
}

// matchSpecScoreBaselineFailure treats the exact violation identity set as the
// authoritative comparison for SpecScore. A candidate may remove a legacy
// finding (or report the same finding from a different checkout path) but may
// never introduce an identity absent from the exact target baseline. Counts
// and rendered diagnostics are not identities, so a strict subset is a safe
// improvement while a new rule remains a hard failure.
func matchSpecScoreBaselineFailure(baseline []quality.VerificationEntry, candidate quality.VerificationEntry) bool {
	candidateIDs := specScoreViolationIdentities(candidate.Detail)
	for _, baselineFailure := range baseline {
		if baselineFailure.Language != candidate.Language || baselineFailure.Module != candidate.Module || baselineFailure.Check != candidate.Check || baselineFailure.Command != candidate.Command {
			continue
		}
		baselineIDs := specScoreViolationIdentities(baselineFailure.Detail)
		if len(candidateIDs) == 0 || len(baselineIDs) == 0 {
			// Some SpecScore failures describe an environment/configuration
			// problem rather than a rule violation (for example, a missing
			// configured spec root). There is no structured identity to compare;
			// use the same normalized diagnostic comparison as other checks so
			// paths and other approved volatile tokens do not become behavior.
			if len(candidateIDs) == 0 && len(baselineIDs) == 0 &&
				normalizeWorktreeMergeFailureDetail(baselineFailure.Detail) == normalizeWorktreeMergeFailureDetail(candidate.Detail) {
				return true
			}
			continue
		}
		if worktreeMergeFailureIdentitySubset(candidateIDs, baselineIDs) {
			return true
		}
	}
	return false
}

var specScoreViolationIdentityPattern = regexp.MustCompile(`^(.+?):[0-9]+(?:-[0-9]+)?\s+([^:]+):`)

func specScoreViolationIdentities(detail string) map[string]struct{} {
	identities := make(map[string]struct{})
	for _, rawLine := range strings.Split(detail, "\n") {
		line := strings.TrimSpace(normalizeWorktreeMergeFailureDetail(rawLine))
		match := specScoreViolationIdentityPattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		path := strings.TrimSpace(match[1])
		rule := strings.TrimSpace(match[2])
		identities[strings.Join(strings.Fields(path+" "+rule), " ")] = struct{}{}
	}
	return identities
}

// worktreeMergeFailureIdentitySubset compares already-qualified identity sets.
// Each caller retains its own nonempty and metadata policy before this stage.
func worktreeMergeFailureIdentitySubset(candidate, baseline map[string]struct{}) bool {
	for identity := range candidate {
		if _, ok := baseline[identity]; !ok {
			return false
		}
	}
	return true
}
