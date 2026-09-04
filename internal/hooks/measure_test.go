package hooks

import (
	"strings"
	"testing"
	"time"
)

func pushEvent(branch string, duration time.Duration, at time.Time) Event {
	return Event{
		SchemaVersion: EventSchemaVersion, Timestamp: at, Repository: "acme/app",
		Hook: "pre-push", Action: "push-attempt", Outcome: "passed",
		DurationMS: duration.Milliseconds(), Branch: branch,
	}
}

func commitEvent(duration time.Duration, at time.Time, outcome string) Event {
	return Event{
		SchemaVersion: EventSchemaVersion, Timestamp: at, Repository: "acme/app",
		Hook: "pre-commit", Action: "commit-check", Outcome: outcome,
		DurationMS: duration.Milliseconds(), Branch: "stream/checkout",
	}
}

// AC: hooks-are-cheap-on-a-stream-branch — `wb hooks measure` shows the
// recorded durations for both the stream-branch push and the other-branch push,
// and prices the saving from the measured average rather than an estimate.
func TestMeasureSeparatesStreamPushesFromEveryOtherPush(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	events := []Event{
		commitEvent(900*time.Millisecond, now, "passed"),
		commitEvent(1100*time.Millisecond, now, "failed"),
		pushEvent("stream/checkout", 40*time.Millisecond, now),
		pushEvent("stream/checkout", 60*time.Millisecond, now),
		pushEvent("stream/checkout", 50*time.Millisecond, now),
		pushEvent("feature/other", 30*time.Second, now),
		pushEvent("main", 50*time.Second, now),
	}
	delta := Measure(events, 7, "", now)

	if delta.Commit.Runs != 2 || delta.Commit.Failures != 1 {
		t.Fatalf("commit = %#v, want two runs and one failure", delta.Commit)
	}
	if delta.Commit.MaxDurationMS != 1100 {
		t.Errorf("commit measured budget = %d ms, want the slowest observed run", delta.Commit.MaxDurationMS)
	}
	if delta.StreamPush.Runs != 3 {
		t.Fatalf("stream pushes = %d, want 3", delta.StreamPush.Runs)
	}
	if delta.OtherPush.Runs != 2 {
		t.Fatalf("other pushes = %d, want 2", delta.OtherPush.Runs)
	}
	if delta.OtherPush.AverageDurationMS != 40000 {
		t.Errorf("other push average = %d ms, want 40000", delta.OtherPush.AverageDurationMS)
	}
	if delta.SavedRuns != 3 || delta.SavedBasisMS != 40000 || delta.SavedDurationMS != 120000 {
		t.Errorf("saving = %d runs × %d ms = %d ms; want 3 × 40000 = 120000",
			delta.SavedRuns, delta.SavedBasisMS, delta.SavedDurationMS)
	}
	if len(delta.Unmeasured) != 0 {
		t.Errorf("unmeasured = %v, want nothing outstanding", delta.Unmeasured)
	}
}

// A zero saving must never be readable as "the stream profile saved nothing".
func TestMeasureNamesWhatItCouldNotPrice(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	delta := Measure([]Event{pushEvent("stream/only", 10*time.Millisecond, now)}, 7, "", now)
	if delta.SavedDurationMS != 0 {
		t.Fatalf("saving = %d ms with no basis to price it", delta.SavedDurationMS)
	}
	joined := strings.Join(delta.Unmeasured, " | ")
	if !strings.Contains(joined, "no non-stream push") {
		t.Errorf("unmeasured = %v, want the missing basis named", delta.Unmeasured)
	}
	if !strings.Contains(joined, "no commit-hook run") {
		t.Errorf("unmeasured = %v, want the missing commit budget named", delta.Unmeasured)
	}
}

func TestMeasureRespectsTheRepositoryFilterAndWindow(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -30)
	events := []Event{
		pushEvent("feature/a", time.Second, now),
		pushEvent("feature/a", time.Second, old),
	}
	events[0].Repository = "acme/app"
	events[1].Repository = "acme/app"
	if delta := Measure(events, 7, "", now); delta.OtherPush.Runs != 1 {
		t.Errorf("runs = %d, want only the event inside the window", delta.OtherPush.Runs)
	}
	if delta := Measure(events, 7, "elsewhere", now); delta.OtherPush.Runs != 0 {
		t.Errorf("runs = %d, want the repository filter applied", delta.OtherPush.Runs)
	}
}

func TestMeasureKeysStreamPushesOnThePushedRef(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	event := pushEvent("feature/checked-out", 40*time.Millisecond, now)
	event.Ref = "refs/heads/stream/x"
	delta := Measure([]Event{event, pushEvent("feature/other", 30*time.Second, now)}, 7, "", now)
	if delta.StreamPush.Runs != 1 {
		t.Fatalf("stream pushes = %d, want 1 from the pushed ref even though the checkout was feature/checked-out", delta.StreamPush.Runs)
	}
	if delta.OtherPush.Runs != 1 {
		t.Fatalf("other pushes = %d, want the non-stream event", delta.OtherPush.Runs)
	}
}

func TestMeasureCollectsPerBlockCost(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	block := Event{
		SchemaVersion: EventSchemaVersion, Timestamp: now, Repository: "acme/app",
		Hook: "pre-push", Action: "hook-block", Block: "go/pre-push", Profile: "go",
		Outcome: "failed", DurationMS: 12345,
	}
	delta := Measure([]Event{block, block}, 7, "", now)
	if len(delta.Blocks) != 1 || delta.Blocks[0].Runs != 2 || delta.Blocks[0].Failures != 2 {
		t.Fatalf("blocks = %#v", delta.Blocks)
	}
	if delta.Blocks[0].AverageDurationMS != 12345 {
		t.Errorf("block average = %d", delta.Blocks[0].AverageDurationMS)
	}
}
