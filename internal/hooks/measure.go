package hooks

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/streambranch"
)

// ProfileCost is the observed cost of one hook profile.
//
// `every-profile-declares-a-measured-budget` requires a profile to carry a
// measured cold wall-time budget before it becomes a default. MaxDurationMS is
// that budget: the worst run actually observed, not an estimate.
type ProfileCost struct {
	Runs              int   `json:"runs"`
	Failures          int   `json:"failures"`
	TotalDurationMS   int64 `json:"total_duration_ms"`
	AverageDurationMS int64 `json:"average_duration_ms"`
	// MaxDurationMS is the measured budget: the slowest observed run.
	MaxDurationMS int64 `json:"max_duration_ms"`
}

func (cost *ProfileCost) observe(event Event) {
	cost.Runs++
	cost.TotalDurationMS += event.DurationMS
	if event.DurationMS > cost.MaxDurationMS {
		cost.MaxDurationMS = event.DurationMS
	}
	if event.Outcome == "failed" {
		cost.Failures++
	}
}

func (cost *ProfileCost) finish() {
	if cost.Runs > 0 {
		cost.AverageDurationMS = cost.TotalDurationMS / int64(cost.Runs)
	}
}

// ProfileDelta is the evidence behind the hook-profile claim: what a commit
// costs, what a push costs, and what pushing to a stream branch saved.
//
// The saving is measured rather than asserted, because the whole reason the
// push hook defers to CI on a stream branch is cost — and a cost claim with no
// measurement behind it is the assurance this Feature refuses to print.
//
// Implements: dependency-streams#req:commit-hook-is-fast-and-scoped,
// dependency-streams#req:push-hook-defers-to-ci-on-stream-branches,
// dependency-streams#req:every-profile-declares-a-measured-budget.
type ProfileDelta struct {
	From             string `json:"from"`
	Through          string `json:"through"`
	RepositoryFilter string `json:"repository_filter,omitempty"`
	// Commit is the pre-commit profile: formatting and static checks over the
	// files changed in that commit, never a test suite.
	Commit ProfileCost `json:"commit"`
	// StreamPush is every pre-push on a `stream/<name>` branch. These run no
	// local verification: CI on the stream pull request is the gate.
	StreamPush ProfileCost `json:"stream_push"`
	// OtherPush is every pre-push elsewhere, which runs the current full
	// profile unchanged.
	OtherPush ProfileCost `json:"other_push"`
	// SavedRuns is the number of stream-branch pushes that ran no local
	// verification.
	SavedRuns int `json:"saved_runs"`
	// SavedDurationMS is SavedRuns priced at the measured average cost of a
	// non-stream push. It is an estimate *derived from measurement*, and the
	// basis is reported alongside it so a reader can check the arithmetic.
	SavedDurationMS int64 `json:"saved_duration_ms"`
	// SavedBasisMS is the average non-stream push duration the estimate used.
	SavedBasisMS int64 `json:"saved_basis_ms"`
	// Blocks are the per-profile block costs, so a slow profile can be named
	// rather than guessed at.
	Blocks []BlockMetrics `json:"blocks,omitempty"`
	// Unmeasured names what this window could not price, so a zero saving is
	// never readable as "the stream profile saved nothing".
	Unmeasured []string `json:"unmeasured,omitempty"`
}

// Measure computes the profile delta over the same events `wb hooks metrics`
// charts, so both views are built from one recording rather than two.
// isStreamPush reports whether one push-attempt event was a push to a stream
// branch, preferring the recorded ref over the checked-out branch.
func isStreamPush(event Event) bool {
	if event.Ref != "" {
		return streambranch.Is(event.Ref)
	}
	return streambranch.Is(event.Branch)
}

func Measure(events []Event, days int, repositoryFilter string, now time.Time) ProfileDelta {
	if days < 1 {
		days = 1
	}
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	from := today.AddDate(0, 0, -(days - 1))
	delta := ProfileDelta{
		From:             from.Format("2006-01-02"),
		Through:          today.Format("2006-01-02"),
		RepositoryFilter: repositoryFilter,
	}
	byBlock := map[string]*BlockMetrics{}
	for _, event := range events {
		if repositoryFilter != "" && !strings.Contains(strings.ToLower(event.Repository), strings.ToLower(repositoryFilter)) {
			continue
		}
		day := event.Timestamp.In(location)
		if day.Before(from) || day.After(today.AddDate(0, 0, 1)) {
			continue
		}
		if event.Action == "hook-block" {
			block := byBlock[event.Block]
			if block == nil {
				block = &BlockMetrics{ID: event.Block, Profile: event.Profile, Hook: event.Hook}
				byBlock[event.Block] = block
			}
			block.Runs++
			block.TotalDurationMS += event.DurationMS
			if event.Outcome == "failed" {
				block.Failures++
			}
			continue
		}
		switch event.Action {
		case "commit-check":
			delta.Commit.observe(event)
		case "push-attempt":
			// The PUSHED ref decides, not the checked-out branch: a push can
			// target a ref other than HEAD, and keying on Branch reported
			// zero stream pushes after real ones. A recorded tier of 0 is the
			// second signal — that is the skip the stream profile produces —
			// and Branch remains the fallback for events written before the
			// ref was recorded.
			if isStreamPush(event) {
				delta.StreamPush.observe(event)
				continue
			}
			delta.OtherPush.observe(event)
		}
	}
	delta.Commit.finish()
	delta.StreamPush.finish()
	delta.OtherPush.finish()
	delta.SavedRuns = delta.StreamPush.Runs
	delta.SavedBasisMS = delta.OtherPush.AverageDurationMS
	delta.SavedDurationMS = int64(delta.SavedRuns) * delta.SavedBasisMS

	if delta.StreamPush.Runs > 0 && delta.OtherPush.Runs == 0 {
		delta.Unmeasured = append(delta.Unmeasured,
			"no non-stream push was recorded in this window, so the stream saving has no measured price to compare against")
	}
	if delta.Commit.Runs == 0 {
		delta.Unmeasured = append(delta.Unmeasured,
			"no commit-hook run was recorded in this window; the commit profile has no measured budget yet")
	}
	if delta.StreamPush.Runs == 0 {
		delta.Unmeasured = append(delta.Unmeasured,
			"no push to a stream branch was recorded in this window; the deferred-to-CI profile has not been exercised")
	}
	if delta.StreamPush.Runs > 0 && delta.StreamPush.MaxDurationMS > streamPushBudgetMS {
		delta.Unmeasured = append(delta.Unmeasured, fmt.Sprintf(
			"a stream-branch push took %d ms, which is more than a hook that runs no verification should cost; check that the tier-0 classification is reaching the template",
			delta.StreamPush.MaxDurationMS))
	}

	ids := make([]string, 0, len(byBlock))
	for id := range byBlock {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		block := byBlock[id]
		if block.Runs > 0 {
			block.AverageDurationMS = block.TotalDurationMS / int64(block.Runs)
		}
		delta.Blocks = append(delta.Blocks, *block)
	}
	return delta
}

// streamPushBudgetMS is the ceiling a push that runs NO verification should
// ever reach. Exceeding it means the tier-0 decision is not reaching the hook
// template, which would make the reported saving fictional.
const streamPushBudgetMS = 2000
