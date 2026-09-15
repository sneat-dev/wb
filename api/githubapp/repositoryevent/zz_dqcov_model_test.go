package repositoryevent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// dqCovEventIDs builds n distinct opaque event identifiers.
func dqCovEventIDs(n int) []string {
	ids := make([]string, 0, n)
	for index := 0; index < n; index++ {
		ids = append(ids, fmt.Sprintf("delivery-%d", index))
	}
	return ids
}

// dqCovEvents builds n distinct, individually valid events.
func dqCovEvents(n int) []Event {
	events := make([]Event, 0, n)
	for index := 0; index < n; index++ {
		event := validEvent()
		event.ID = fmt.Sprintf("delivery-%d", index)
		events = append(events, event)
	}
	return events
}

func TestDqCovEventValidateRejectsMalformedFields(t *testing.T) {
	zero := time.Time{}
	for name, mutate := range map[string]func(*Event){
		"version":                       func(event *Event) { event.Version = ContractVersion + 1 },
		"event id with a space":         func(event *Event) { event.ID = "bad id" },
		"empty event id":                func(event *Event) { event.ID = "" },
		"repository without a name":     func(event *Event) { event.Repository = "github.com/acme" },
		"repository without a host":     func(event *Event) { event.Repository = "acme/app" },
		"unsupported ref namespace":     func(event *Event) { event.Ref = "refs/tags/v1" },
		"short target sha":              func(event *Event) { event.TargetSHA = "abc" },
		"non-hex target sha":            func(event *Event) { event.TargetSHA = strings.Repeat("z", 40) },
		"zero occurred at":              func(event *Event) { event.OccurredAt = &zero },
		"previous repository on update": func(event *Event) { event.PreviousRepository = "github.com/acme/old" },
		"rename to the same identity": func(event *Event) {
			event.Reason = ReasonRepositoryRenamed
			event.PreviousRepository = "github.com/ACME/app"
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := validEvent()
			mutate(&event)
			if err := event.Validate(); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestDqCovEventValidateAcceptsARename(t *testing.T) {
	event := validEvent()
	event.Reason = ReasonRepositoryRenamed
	event.PreviousRepository = "github.com/acme/legacy"
	if err := event.Validate(); err != nil {
		t.Fatalf("Validate rejected a complete rename: %v", err)
	}
}

func TestDqCovValidateEventIDBoundsOpaqueIDs(t *testing.T) {
	if err := ValidateEventID("delivery-1:default"); err != nil {
		t.Fatalf("ValidateEventID rejected a safe id: %v", err)
	}
	for name, id := range map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", MaxEventIDBytes+1),
		"with space":   "delivery 1",
		"leading dash": "-delivery",
		"with slash":   "delivery/1",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateEventID(id); err == nil {
				t.Fatalf("ValidateEventID(%q) accepted an invalid id", id)
			}
		})
	}
}

func TestDqCovValidateRepositoryRequiresACanonicalIdentity(t *testing.T) {
	if err := ValidateRepository("github.com/acme/app"); err != nil {
		t.Fatalf("ValidateRepository rejected a canonical identity: %v", err)
	}
	for name, repository := range map[string]string{
		"empty":         "",
		"missing host":  "acme/app",
		"too long":      "github.com/acme/" + strings.Repeat("a", MaxRepositoryBytes),
		"owner only":    "github.com/acme",
		"extra segment": "github.com/acme/app/extra",
		"dotted owner":  "github.com/.acme/app",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidateRepository(repository)
			if err == nil || !strings.Contains(err.Error(), "bounded github.com/owner/repository identity") {
				t.Fatalf("ValidateRepository(%q) = %v, want a bootstrap-identity error", repository, err)
			}
		})
	}
}

func TestDqCovValidateRefRejectsMalformedBranchRefs(t *testing.T) {
	if err := validateRef("refs/heads/feature/dashboard"); err != nil {
		t.Fatalf("validateRef rejected a valid branch ref: %v", err)
	}
	for name, ref := range map[string]string{
		"empty":            "",
		"prefix only":      "refs/heads/",
		"not a branch":     "refs/tags/v1",
		"too long":         "refs/heads/" + strings.Repeat("b", MaxRefBytes),
		"control char":     "refs/heads/feat\x01ure",
		"delete char":      "refs/heads/feat\x7fure",
		"at brace":         "refs/heads/feat@{1}",
		"double slash":     "refs/heads/feat//ure",
		"double dot":       "refs/heads/feat..ure",
		"trailing dot":     "refs/heads/feat.",
		"trailing slash":   "refs/heads/feat/",
		"lone at":          "refs/heads/@",
		"dotted component": "refs/heads/.feat",
		"lock suffix":      "refs/heads/feat.lock",
		"space":            "refs/heads/feat ure",
		"tilde":            "refs/heads/feat~ure",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRef(ref); err == nil {
				t.Fatalf("validateRef(%q) accepted an invalid branch ref", ref)
			}
		})
	}
}

func TestDqCovValidateCursorRequiresAndBoundsTheCursor(t *testing.T) {
	if err := ValidateCursor("opaque-cursor", true); err != nil {
		t.Fatalf("ValidateCursor rejected a safe cursor: %v", err)
	}
	if err := ValidateCursor("", false); err != nil {
		t.Fatalf("ValidateCursor rejected an optional empty cursor: %v", err)
	}
	for name, test := range map[string]struct {
		cursor   string
		required bool
	}{
		"required empty": {"", true},
		"too long":       {strings.Repeat("c", MaxCursorBytes+1), false},
		"newline":        {"cur\nsor", false},
		"nul byte":       {"cur\x00sor", false},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCursor(test.cursor, test.required); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("ValidateCursor = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestDqCovPollResponseValidateBoundsThePollContract(t *testing.T) {
	valid := func() PollResponse {
		return PollResponse{
			Version:    ContractVersion,
			Cursor:     "opaque-before",
			NextCursor: "opaque-after",
			Events:     dqCovEvents(1),
		}
	}
	if err := valid().Validate("opaque-before"); err != nil {
		t.Fatalf("Validate rejected a complete poll response: %v", err)
	}
	for name, mutate := range map[string]func(*PollResponse){
		"version":               func(response *PollResponse) { response.Version = ContractVersion + 1 },
		"cursor mismatch":       func(response *PollResponse) { response.Cursor = "other" },
		"missing next cursor":   func(response *PollResponse) { response.NextCursor = "" },
		"oversized next cursor": func(response *PollResponse) { response.NextCursor = strings.Repeat("c", MaxCursorBytes+1) },
		"too many events":       func(response *PollResponse) { response.Events = dqCovEvents(MaxLimit + 1) },
		"invalid event":         func(response *PollResponse) { response.Events = []Event{{Version: ContractVersion}} },
	} {
		t.Run(name, func(t *testing.T) {
			response := valid()
			mutate(&response)
			if err := response.Validate("opaque-before"); err == nil {
				t.Fatal("Validate accepted a malformed poll response")
			}
		})
	}
}

func TestDqCovAckRequestValidateRequiresUniqueEventIDs(t *testing.T) {
	if err := (AckRequest{Version: ContractVersion, Cursor: "opaque-after", EventIDs: []string{"delivery-1"}}).Validate(); err != nil {
		t.Fatalf("Validate rejected a complete acknowledgement: %v", err)
	}
	for name, mutate := range map[string]func(*AckRequest){
		"version":            func(request *AckRequest) { request.Version = ContractVersion + 1 },
		"missing cursor":     func(request *AckRequest) { request.Cursor = "" },
		"oversized cursor":   func(request *AckRequest) { request.Cursor = strings.Repeat("c", MaxCursorBytes+1) },
		"no event ids":       func(request *AckRequest) { request.EventIDs = nil },
		"too many event ids": func(request *AckRequest) { request.EventIDs = dqCovEventIDs(MaxLimit + 1) },
		"invalid event id":   func(request *AckRequest) { request.EventIDs = []string{"bad id"} },
		"duplicate event id": func(request *AckRequest) { request.EventIDs = []string{"delivery-1", "delivery-1"} },
	} {
		t.Run(name, func(t *testing.T) {
			request := AckRequest{Version: ContractVersion, Cursor: "opaque-after", EventIDs: []string{"delivery-1"}}
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate accepted a malformed acknowledgement")
			}
		})
	}
}
