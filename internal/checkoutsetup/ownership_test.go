package checkoutsetup

import (
	"errors"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOwnershipPreservesAbsolutePathIdentityPrecedenceAndErrorOrder(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"abs", "undeclared", "record", "overrides", "registered", "ambient"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			refusal := errors.New(stage)
			var calls []string
			ambient := worktrees.AgentIdentity{Runtime: "ambient", Model: "ambient model", AgentID: "ambient id", PID: 1}
			overrides := worktrees.AgentIdentity{Runtime: "flag", Model: "flag model", AgentID: "flag id", PID: 2}
			if stage == "undeclared" {
				ambient = worktrees.AgentIdentity{}
				overrides = worktrees.AgentIdentity{}
			}
			if stage == "ambient" {
				overrides = worktrees.AgentIdentity{}
			}
			admitted := worktrees.AgentIdentity{}
			if stage == "registered" {
				admitted = worktrees.AgentIdentity{Registered: true, Runtime: "registered", Model: "registered model", AgentID: "registered id", PID: 3}
			}
			want := overrides
			if stage == "registered" {
				want = admitted
			}
			if stage == "ambient" {
				want = ambient
			}
			service := NewOwnership(OwnershipDependencies{
				Abs: func(path string) (string, error) {
					calls = append(calls, "abs")
					if path != "relative" {
						t.Fatal(path)
					}
					if stage == "abs" {
						return "", refusal
					}
					return filepath.Join("absolute", "checkout"), nil
				},
				IdentityFromEnv: func() worktrees.AgentIdentity { calls = append(calls, "ambient"); return ambient },
				RecordCustody: func(path, task, operation string, id worktrees.AgentIdentity) error {
					calls = append(calls, "record")
					if path != filepath.Join("absolute", "checkout") || task != "" || operation != "worktree own" || !reflect.DeepEqual(id, want) {
						t.Fatalf("record: %s %s %s %+v", path, task, operation, id)
					}
					if stage == "record" {
						return refusal
					}
					return nil
				},
			})
			result, err := service.Record(OwnershipRequest{Path: "relative", Admitted: admitted, Overrides: overrides})
			if stage == "abs" || stage == "record" {
				if err != refusal {
					t.Fatalf("error=%v", err)
				}
			} else if stage == "undeclared" {
				if err == nil || len(calls) != 2 {
					t.Fatalf("err=%v calls=%v", err, calls)
				}
			} else if err != nil || !reflect.DeepEqual(result.Identity, want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if stage == "abs" && len(calls) != 1 {
				t.Fatal(calls)
			}
			if stage != "abs" && stage != "undeclared" && !reflect.DeepEqual(calls, []string{"abs", "ambient", "record"}) {
				t.Fatal(calls)
			}
		})
	}
}

func TestOwnershipKeepsEmptyOverridesAndBindsActualDefaults(t *testing.T) {
	t.Parallel()
	defaults := DefaultOwnershipDependencies()
	path, err := defaults.Abs(t.TempDir())
	if err != nil || !filepath.IsAbs(path) {
		t.Fatal(path, err)
	}
	// Actual native default custody write refuses an absent private checkout.
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := NewOwnership(defaults).Record(OwnershipRequest{Path: missing, Overrides: worktrees.AgentIdentity{PID: 9}}); err == nil {
		t.Fatal("missing checkout accepted")
	}
}
