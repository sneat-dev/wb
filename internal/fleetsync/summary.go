package fleetsync

import "sort"

// SummarySection groups related rows in both the plain and interactive sync
// summaries. Its value is the human-readable section heading.
type SummarySection string

const (
	SummaryFinalOutcomes SummarySection = "Final outcomes"
	SummaryPullActions   SummarySection = "Pull actions"
	SummaryAttention     SummarySection = "Attention"
	SummaryErrors        SummarySection = "Failures"
)

// SummaryGroup is one selectable/countable row in the final sync summary.
// Results are sorted by repository slug so every renderer is deterministic.
// A result may belong to both an action row and a final-outcome row.
type SummaryGroup struct {
	Label   string
	Section SummarySection
	Results []Result
}

// Summary builds the ordered category model shared by plain output and the
// interactive results browser.
func Summary(results []Result) []SummaryGroup {
	group := func(label string, section SummarySection, match func(Result) bool) SummaryGroup {
		matched := make([]Result, 0)
		for _, result := range results {
			if match(result) {
				matched = append(matched, result)
			}
		}
		sort.SliceStable(matched, func(i, j int) bool { return matched[i].Repo.Slug() < matched[j].Repo.Slug() })
		return SummaryGroup{Label: label, Section: section, Results: matched}
	}
	status := func(want Status) func(Result) bool {
		return func(result Result) bool { return result.Status == want }
	}
	return []SummaryGroup{
		group("Not owned", SummaryFinalOutcomes, func(result Result) bool {
			return result.Status == NoOp && !result.Repo.Remote
		}),
		group("Fork", SummaryFinalOutcomes, func(result Result) bool {
			return result.Status == NoOp && result.Repo.Remote && result.Repo.IsFork
		}),
		group("Cloned", SummaryFinalOutcomes, status(Cloned)),
		group("Pulled", SummaryFinalOutcomes, status(Pulled)),
		group("Skipped (dirty)", SummaryFinalOutcomes, status(SkippedDirty)),
		group("Skipped (ignored)", SummaryFinalOutcomes, status(SkippedIgnored)),
		group("Empty remote", SummaryFinalOutcomes, status(EmptyRemote)),
		group("Archived removed", SummaryFinalOutcomes, status(RemovedArchived)),
		group("Archived kept", SummaryFinalOutcomes, status(KeptArchived)),
		group("Archived absent", SummaryFinalOutcomes, status(AbsentArchived)),
		group("Pull planned", SummaryPullActions, func(result Result) bool { return result.PullPlanned }),
		group("Pull attempted", SummaryPullActions, func(result Result) bool { return result.PullAttempted }),
		group("Pull succeeded", SummaryPullActions, func(result Result) bool { return result.PullSucceeded }),
		group("Updated from remote", SummaryPullActions, func(result Result) bool { return result.Updated }),
		group("Already current", SummaryPullActions, func(result Result) bool { return result.PullSucceeded && !result.Updated }),
		group("Needs attention", SummaryAttention, needsAttention),
		group("Errors", SummaryErrors, status(Failed)),
	}
}

func needsAttention(result Result) bool {
	switch result.Status {
	case Diverged, NoUpstream, Unpushed, ArchivedUnlandable:
		return true
	default:
		return false
	}
}

// SummaryGroupByLabel finds a summary row by its stable display label.
func SummaryGroupByLabel(groups []SummaryGroup, label string) (SummaryGroup, bool) {
	for _, group := range groups {
		if group.Label == label {
			return group, true
		}
	}
	return SummaryGroup{}, false
}
