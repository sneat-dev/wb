package streamsync

import (
	"fmt"
	"sort"
	"strings"
)

// PushTrigger is the justification for a push.
//
// A push costs agent time, tokens, CI minutes and money, and ten dependency
// bumps pushed one at a time cost ten of each for one landing. So a push
// happens only on one of exactly four named triggers, and a dependency bump is
// not one of them.
//
// Implements: dependency-streams#req:pushes-are-justified-and-counted.
type PushTrigger string

const (
	// TriggerLanding is a push after a green batch verification, on the way
	// to landing.
	TriggerLanding PushTrigger = "landing"
	// TriggerReview is the stream's draft pull request being made ready.
	TriggerReview PushTrigger = "review"
	// TriggerPark is a hand-off where unpushed work would otherwise be lost.
	TriggerPark PushTrigger = "park"
	// TriggerExplicit is `--push --reason "<text>"`, the only escape hatch.
	TriggerExplicit PushTrigger = "explicit"
)

// PushTriggers is the complete set, in the order a refusal lists them.
func PushTriggers() []PushTrigger {
	return []PushTrigger{TriggerLanding, TriggerReview, TriggerPark, TriggerExplicit}
}

// RefusalUnjustifiedPush is the stable code for a push with no trigger.
const RefusalUnjustifiedPush = "unjustified-push"

// PushDecision is the outcome of asking whether a push may happen.
type PushDecision struct {
	Trigger PushTrigger `json:"trigger"`
	Reason  string      `json:"reason"`
	// SHA is what origin holds after the push. It is set only once the push
	// has been verified, so a caller reading this field is reading an effect
	// rather than an intention.
	SHA string `json:"sha,omitempty"`
}

// Refusal is a guard that fired.
type Refusal struct {
	Code       string
	Message    string
	Sanctioned []string
}

func (refusal *Refusal) Error() string {
	if len(refusal.Sanctioned) == 0 {
		return refusal.Message
	}
	return refusal.Message + "; run: " + strings.Join(refusal.Sanctioned, " or ")
}

// JustifyPush decides whether a push is allowed, and why.
//
// A push with no recognised trigger is refused, listing all four — a caller
// that does not know why it is pushing has no business pushing. `explicit`
// additionally requires a reason: it is the escape hatch, so it is the one
// trigger that must say what it is for in the operator's own words.
func JustifyPush(trigger PushTrigger, reason string) (PushDecision, error) {
	switch trigger {
	case TriggerLanding, TriggerReview, TriggerPark:
		if strings.TrimSpace(reason) == "" {
			reason = defaultReason(trigger)
		}
		return PushDecision{Trigger: trigger, Reason: reason}, nil
	case TriggerExplicit:
		if strings.TrimSpace(reason) == "" {
			return PushDecision{}, &Refusal{
				Code:       RefusalUnjustifiedPush,
				Message:    "--push requires --reason: an explicit push is the only escape hatch from the push budget, so it has to say what it is for",
				Sanctioned: []string{`--push --reason "<text>"`},
			}
		}
		return PushDecision{Trigger: TriggerExplicit, Reason: reason}, nil
	}
	return PushDecision{}, &Refusal{
		Code: RefusalUnjustifiedPush,
		Message: "this push has no recognised trigger, and a push costs CI minutes and money; the only justified triggers are: " +
			renderTriggers(),
		Sanctioned: []string{
			"wb stream propagate   (landing, after a green batch)",
			"wb stream ready       (review: the draft stream pull request)",
			"wb worktree end       (park: work that would otherwise be lost)",
			`wb stream sync --push --reason "<text>"   (explicit)`,
		},
	}
}

func defaultReason(trigger PushTrigger) string {
	switch trigger {
	case TriggerLanding:
		return "landing after a green batch verification"
	case TriggerReview:
		return "making the draft stream pull request ready for review"
	case TriggerPark:
		return "parking unpushed work so a hand-off cannot lose it"
	}
	return ""
}

func renderTriggers() string {
	names := make([]string, 0, 4)
	for _, trigger := range PushTriggers() {
		names = append(names, string(trigger))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// UnpushedReport is what `stream status` prints for a member: how far the local
// stream branch has run ahead of the remote.
//
// Local commits are the normal state under this model, not a warning. The
// count is shown so the operator can see the batch accumulating and knows
// exactly what a landing push will carry.
type UnpushedReport struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commits    int    `json:"commits"`
}

// String renders the line `stream status` shows.
func (report UnpushedReport) String() string {
	if report.Commits == 0 {
		return fmt.Sprintf("%s: nothing unpushed", report.Repository)
	}
	return fmt.Sprintf("%s: %d local commit(s) not pushed on %s", report.Repository, report.Commits, report.Branch)
}
