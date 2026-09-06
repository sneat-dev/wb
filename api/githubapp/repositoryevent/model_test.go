package repositoryevent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validEvent() Event {
	occurredAt := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return Event{Version: ContractVersion, ID: "delivery-1:default", Repository: "github.com/acme/app", Ref: "refs/heads/main", Reason: ReasonDefaultBranchUpdated, TargetSHA: "0123456789abcdef0123456789abcdef01234567", OccurredAt: &occurredAt}
}

func TestEventContractAllowsOnlySafeRepositoryMetadata(t *testing.T) {
	if err := validEvent().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Event){
		"path":                    func(event *Event) { event.Repository = "/Users/alice/private" },
		"ref":                     func(event *Event) { event.Ref = "main" },
		"reason":                  func(event *Event) { event.Reason = "webhook_payload" },
		"rename without previous": func(event *Event) { event.Reason = ReasonRepositoryRenamed },
	} {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestOptionalAuditFieldsAreOmitted(t *testing.T) {
	event := validEvent()
	event.TargetSHA = ""
	event.OccurredAt = nil
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "target_sha") || strings.Contains(string(raw), "occurred_at") {
		t.Fatalf("optional fields were serialized: %s", raw)
	}
}

func TestPollAndAckContractsBindOpaqueCursorAndUniqueEvents(t *testing.T) {
	event := validEvent()
	response := PollResponse{Version: ContractVersion, Cursor: "opaque-before", NextCursor: "opaque-after", Events: []Event{event}}
	if err := response.Validate("opaque-before"); err != nil {
		t.Fatal(err)
	}
	response.Events = append(response.Events, event)
	if err := response.Validate("opaque-before"); err == nil {
		t.Fatal("duplicate event ID was accepted")
	}
	ack := AckRequest{Version: ContractVersion, Cursor: "opaque-after", EventIDs: []string{event.ID}}
	if err := ack.Validate(); err != nil {
		t.Fatal(err)
	}
}
