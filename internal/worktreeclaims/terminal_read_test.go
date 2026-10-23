package worktreeclaims

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestTerminalReaderDistinguishesReadOnlyAndRepairs(t *testing.T) {
	t.Parallel()
	missing := errors.New("projection absent")
	projection := Projection{EffortID: "task", RunID: "run", ClaimID: "claim", Lifecycle: "terminal"}
	readOnlyCalls, repairCalls := 0, 0
	ports := TerminalReadPorts{
		ReadProjectionForClaim: func(string, string) (Projection, error) { repairCalls++; return projection, nil },
		ReadProjectionReadOnly: func(string) (Projection, error) { readOnlyCalls++; return projection, nil },
		ProjectionMissing:      func(err error) bool { return errors.Is(err, missing) },
		Corroborate:            func(string, string, Projection) error { return nil },
		OpenRun: func(string, string, string, bool) (*os.File, string, error) {
			directory, err := os.Open(t.TempDir())
			return directory, "", err
		},
		ReadTerminalAt: func(*os.File, string) (TerminalRecord, error) {
			return TerminalRecord{Claim: Claim{ClaimID: "claim"}}, nil
		},
	}
	for _, readOnly := range []bool{true, false} {
		terminal, err := ports.ReadTerminal("home", "worktree", readOnly)
		if err != nil || terminal == nil || terminal.ClaimID != "claim" {
			t.Fatalf("readOnly=%t: terminal=%#v error=%v", readOnly, terminal, err)
		}
	}
	if readOnlyCalls != 1 || repairCalls != 1 {
		t.Fatalf("projection readers called read-only=%d repair=%d", readOnlyCalls, repairCalls)
	}
}

func TestTerminalReaderRefusesBrokenAuthorityWithoutRepairOnReadOnly(t *testing.T) {
	t.Parallel()
	missing := errors.New("projection absent")
	failure := errors.New("broken authority")
	for _, stage := range []string{"absent", "projection", "active", "corroborate", "open run", "terminal"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ports := TerminalReadPorts{
				ReadProjectionForClaim: func(string, string) (Projection, error) {
					t.Fatal("read-only path repaired projection")
					return Projection{}, nil
				},
				ReadProjectionReadOnly: func(string) (Projection, error) {
					switch stage {
					case "absent":
						return Projection{}, missing
					case "projection":
						return Projection{}, failure
					case "active":
						return Projection{Lifecycle: "active"}, nil
					default:
						return Projection{Lifecycle: "terminal"}, nil
					}
				},
				ProjectionMissing: func(err error) bool { return errors.Is(err, missing) },
				Corroborate: func(string, string, Projection) error {
					if stage == "corroborate" {
						return failure
					}
					return nil
				},
				OpenRun: func(string, string, string, bool) (*os.File, string, error) {
					if stage == "open run" {
						return nil, "", failure
					}
					file, err := os.Open(t.TempDir())
					return file, "", err
				},
				ReadTerminalAt: func(*os.File, string) (TerminalRecord, error) { return TerminalRecord{}, failure },
			}
			terminal, err := ports.ReadTerminal("home", "worktree", true)
			if stage == "absent" || stage == "active" {
				if terminal != nil || err != nil {
					t.Fatalf("%s = %#v, %v", stage, terminal, err)
				}
			} else if terminal != nil || !errors.Is(err, failure) {
				t.Fatalf("%s = %#v, %v", stage, terminal, err)
			}
			if stage == "corroborate" && !strings.Contains(err.Error(), "corroborate terminal work-log claim") {
				t.Fatalf("missing corroboration context: %v", err)
			}
		})
	}
}
