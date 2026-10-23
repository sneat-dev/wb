package orchestrate

import (
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

type createEventRecorder struct{ events []streams.Event }

func (recorder *createEventRecorder) Append(event streams.Event) error {
	recorder.events = append(recorder.events, event)
	return nil
}

func TestCreatePullRequestAuditEventRecordsOutcomeAndBoundedEvidence(t *testing.T) {
	t.Parallel()
	started := time.Now().Add(-time.Second)
	result := PullRequestCreateResult{Repository: "acme/app", Task: "task-7", ApprovedBy: "alice", Reason: "exact head verified", Mechanical: true, Adopted: true, AutoMergeArmed: true}
	result.Outcome = CreateSuccess
	recorder := &createEventRecorder{}
	appendCreateEvent(recorder, "stream", result, started, nil)
	if len(recorder.events) != 1 {
		t.Fatalf("events=%+v", recorder.events)
	}
	event := recorder.events[0]
	if event.Stream != "stream" || event.Verb != "pr create" || event.Repository != "acme/app" || event.Outcome != string(CreateSuccess) || event.Detail != "exact head verified" || event.Evidence["task"] != "task-7" || event.Evidence["approved_by"] != "alice" || event.Evidence["mechanical"] != "true" || event.Evidence["adopted"] != "true" || event.Evidence["auto_merge"] != "true" || event.DurationMS < 1000 {
		t.Fatalf("audit event=%+v", event)
	}
	appendCreateEvent(nil, "stream", result, started, nil)
}

func TestCreatePullRequestAuditEventRecordsFailureWithoutOptionalIdentity(t *testing.T) {
	t.Parallel()
	recorder := &createEventRecorder{}
	result := PullRequestCreateResult{Repository: "acme/app", Reason: "old reason"}
	appendCreateEvent(recorder, "stream", result, time.Now(), errors.New("publication refused"))
	if len(recorder.events) != 1 {
		t.Fatalf("events=%+v", recorder.events)
	}
	event := recorder.events[0]
	if event.Outcome != string(CreateFindings) || event.Detail != "publication refused" || event.Evidence["task"] != "" || event.Evidence["approved_by"] != "" || len(event.Evidence) != 3 {
		t.Fatalf("failure event=%+v", event)
	}
}
