package agents

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestReviewSidecarAppendsAndKeepsHistory(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	review, err := store.LoadReview(record.AgentID)
	if err != nil {
		t.Fatalf("a missing sidecar is not an error: %v", err)
	}
	if review.Status != ReviewPending || review.Reviewed() {
		t.Fatalf("unreviewed = %#v", review)
	}
	first, err := store.AppendReview(record.AgentID, ReviewEntry{
		Status: ReviewNeedsChange, Evaluator: "alex", Note: "missing error path", At: time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("AppendReview: %v", err)
	}
	if first.Status != ReviewNeedsChange || len(first.History) != 1 {
		t.Fatalf("first review = %#v", first)
	}
	second, err := store.AppendReview(record.AgentID, ReviewEntry{
		Status: ReviewAccepted, Evaluator: "alex", At: time.Unix(200, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != ReviewAccepted || len(second.History) != 2 {
		t.Fatalf("second review = %#v", second)
	}
	if latest := second.Latest(); len(latest.History) != 0 || latest.Status != ReviewAccepted {
		t.Fatalf("Latest = %#v", latest)
	}
	// Render attaches the latest decision to the execution result.
	result := store.Render(record)
	if result.Review.Status != ReviewAccepted {
		t.Fatalf("rendered review = %#v", result.Review)
	}
}

func TestReviewRefusesAnUnknownStatusOrTimestamp(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: "maybe", At: time.Unix(1, 0)}); err == nil || !IsRequestError(err) {
		t.Fatalf("invalid status = %v", err)
	}
	if _, err := store.AppendReview(record.AgentID, ReviewEntry{Status: ReviewAccepted}); err == nil {
		t.Fatal("a review without a timestamp must be refused")
	}
	if _, err := store.AppendReview("agt-00000000000000000000000000000000", ReviewEntry{Status: ReviewAccepted, At: time.Unix(1, 0)}); !IsUnknownAgent(err) {
		t.Fatalf("unknown run = %v", err)
	}
}

func TestReviewSidecarReportsCorruptAndFutureDocuments(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	path := store.ReviewPath(record.AgentID)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReview(record.AgentID); err == nil || !strings.Contains(err.Error(), "parse review") {
		t.Fatalf("corrupt review = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"agent_id":"`+record.AgentID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadReview(record.AgentID); err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("future review = %v", err)
	}
	if _, err := store.LoadReview("bad-id"); err == nil {
		t.Fatal("an invalid run ID must be refused")
	}
}

func TestRenderedResultWithoutASidecarIsExplicitlyPending(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	record := sampleRecord(t)
	record.State = StateCompleted
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if result := store.Render(record); result.Review.Status != ReviewPending {
		t.Fatalf("review = %#v", result.Review)
	}
}
