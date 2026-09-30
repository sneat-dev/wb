package worktreeclaims

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var testCorrectionClaimID = strings.Repeat("a", 64)

func correctionFixture(t *testing.T, failure string) (CorrectionPorts, CorrectionOptions) {
	t.Helper()
	directory := testDirectory(t)
	claim := Claim{Version: 2, EffortID: "effort", RunID: "run", ClaimID: testCorrectionClaimID, Model: "old", Lifecycle: "active"}
	model := "new"
	options := CorrectionOptions{EffortID: "effort", RunID: "run", ClaimID: testCorrectionClaimID, EventID: "event", Actor: "actor", Reason: "reason", Model: &model}
	history := map[string]IdentityCorrection{}
	now := time.Unix(100, 0).UTC()
	ports := CorrectionPorts{
		OpenRun: func(string, string, string, bool) (*os.File, string, error) {
			if failure == "open-run" {
				return nil, "", errors.New(failure)
			}
			opened, err := os.Open(directory.Name())
			return opened, directory.Name(), err
		},
		LockClaim: func(*os.File, string) (func(), error) {
			if failure == "lock" {
				return nil, errors.New(failure)
			}
			return func() {}, nil
		},
		OpenPrivateChild: func(_ *os.File, name string, create bool) (*os.File, error) {
			if (failure == "open-claims" && name == "claims") || (failure == "open-corrections" && name == "corrections" && create) {
				return nil, errors.New(failure)
			}
			return os.Open(directory.Name())
		},
		ReadJSONAt: func(_ *os.File, name string, target any) error {
			switch value := target.(type) {
			case *Claim:
				if failure == "read-claim" {
					return errors.New(failure)
				}
				*value = claim
				return nil
			case *IdentityCorrection:
				if failure == "appeared" {
					*value = IdentityCorrection{CorrectionID: options.EventID}
					return nil
				}
				if failure == "read-existing" {
					return errors.New(failure)
				}
				event, ok := history[name]
				if !ok {
					return os.ErrNotExist
				}
				*value = event
				return nil
			}
			return errors.New("target")
		},
		WriteJSONImmutableAt: func(_ *os.File, name string, value any, _ bool) error {
			if _, ok := value.(CorrectionOutboxEvent); ok {
				if failure == "write-outbox" {
					return errors.New(failure)
				}
				return nil
			}
			if failure == "write-correction" {
				return errors.New(failure)
			}
			history[name] = value.(IdentityCorrection)
			return nil
		},
		OpenOutbox: func(string, string, bool) (*os.File, error) {
			if failure == "open-outbox" {
				return nil, errors.New(failure)
			}
			return os.Open(directory.Name())
		},
		ValidSafeSegment: func(value string) bool { return value != "bad" },
		ReadNames: func(*os.File) ([]string, error) {
			if failure == "project-after" && len(history) > 0 {
				return nil, errors.New(failure)
			}
			names := make([]string, 0, len(history))
			for name := range history {
				names = append(names, name)
			}
			return names, nil
		},
		Now: func() time.Time { return now },
	}
	return ports, options
}

func TestCorrectionOperationFaultBoundaries(t *testing.T) {
	stages := []string{"open-run", "lock", "open-claims", "read-claim", "open-corrections", "appeared", "read-existing", "write-correction", "project-after", "open-outbox", "write-outbox", "success"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			ports, options := correctionFixture(t, stage)
			result, err := ports.CorrectExecutionIdentity("home", options)
			if stage == "success" {
				if err != nil || result.CorrectionID != "event" || result.Identity.Model != "new" || !strings.HasSuffix(result.OutboxPath, "event.json") {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatalf("%s accepted", stage)
			}
		})
	}
}

func TestCorrectionReadAndProjectionFaultBoundaries(t *testing.T) {
	ports, options := correctionFixture(t, "")
	if _, err := ports.CurrentExecutionIdentity("home", Claim{EffortID: "effort", RunID: "run", ClaimID: testCorrectionClaimID}); err != nil {
		t.Fatal(err)
	}
	ports.OpenRun = func(string, string, string, bool) (*os.File, string, error) { return nil, "", errors.New("open") }
	if _, err := ports.CurrentExecutionIdentity("home", Claim{}); err == nil {
		t.Fatal("current identity open failure accepted")
	}
	ports, options = correctionFixture(t, "")
	if _, err := ports.OpenWorkLogCorrections(testDirectory(t), "invalid", false); err == nil {
		t.Fatal("invalid claim accepted")
	}
	for _, stage := range []string{"open", "names", "read", "no-field", "bad-model", "bad-route", "fork"} {
		t.Run(stage, func(t *testing.T) {
			p, _ := correctionFixture(t, "")
			event := IdentityCorrection{Version: 1, Type: "worktree.execution_identity_corrected", CorrectionID: "event", ClaimID: testCorrectionClaimID, Sequence: 1, At: time.Unix(1, 0).UTC(), Actor: "actor", Reason: "reason", Model: options.Model}
			if stage == "no-field" {
				event.Model = nil
			}
			if stage == "bad-model" {
				bad := "bad token"
				event.Model = &bad
			}
			if stage == "bad-route" {
				event.Model = nil
				bad := "secret"
				event.CLI = &bad
			}
			if stage == "fork" {
				event.Sequence = 2
			}
			if stage == "open" {
				p.OpenPrivateChild = func(*os.File, string, bool) (*os.File, error) { return nil, errors.New(stage) }
			}
			if stage == "names" {
				p.ReadNames = func(*os.File) ([]string, error) { return nil, errors.New(stage) }
			} else {
				p.ReadNames = func(*os.File) ([]string, error) { return []string{"event.json"}, nil }
			}
			p.ReadJSONAt = func(_ *os.File, _ string, target any) error {
				if stage == "read" {
					return errors.New(stage)
				}
				*target.(*IdentityCorrection) = event
				return nil
			}
			_, _, err := p.ProjectExecutionIdentity(testDirectory(t), Claim{ClaimID: testCorrectionClaimID, Model: "old"})
			if err == nil {
				t.Fatalf("%s accepted", stage)
			}
		})
	}
	ports, options = correctionFixture(t, "")
	if _, err := ports.WriteCorrectionOutbox("home", Claim{EffortID: "effort"}, IdentityCorrection{CorrectionID: "event"}, ExecutionIdentity{}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"open-outbox", "write-outbox"} {
		p, _ := correctionFixture(t, stage)
		if _, err := p.WriteCorrectionOutbox("home", Claim{EffortID: "effort"}, IdentityCorrection{CorrectionID: "event"}, ExecutionIdentity{}); err == nil {
			t.Fatalf("%s accepted", stage)
		}
	}
}
