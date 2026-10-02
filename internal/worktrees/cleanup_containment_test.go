package worktrees

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

const (
	judgedHead    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	judgedMain    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	judgedBaseTip = "cccccccccccccccccccccccccccccccccccccccc"
)

// judgedOrigin is a remote described by its facts; nothing here runs Git.
type judgedOrigin struct {
	fetchErr  error
	ancestors map[string]bool
	ancestry  error
}

func (origin judgedOrigin) ports() worktreelanding.DefaultTargetPorts {
	return worktreelanding.DefaultTargetPorts{
		DefaultBranch: func(context.Context) (string, error) { return "main", nil },
		FetchTargetHead: func(context.Context, string) (string, error) {
			return judgedMain, origin.fetchErr
		},
		IsAncestor: func(_ context.Context, ancestor, descendant string) (bool, error) {
			return origin.ancestors[ancestor+"<"+descendant], origin.ancestry
		},
	}
}

// stackedInspection is a candidate recorded against a live base branch whose
// freshly fetched tip is judgedBaseTip.
func stackedInspection(explicit bool) *lifecycleInspection {
	return &lifecycleInspection{
		ctx: context.Background(), head: judgedHead, base: "cv-t1-schema-v2",
		policy: inspectPolicy{explicitBase: explicit},
		result: ListResult{Base: "cv-t1-schema-v2", RemoteTargetSHA: judgedBaseTip},
	}
}

// absentBaseInspection is a candidate whose recorded base origin no longer
// has, already being judged against the fetched default branch.
func absentBaseInspection() *lifecycleInspection {
	return &lifecycleInspection{
		ctx: context.Background(), head: judgedHead, base: "main",
		result: ListResult{
			Base: "main", RemoteTargetSHA: judgedMain,
			RecordedBase: "cockpit-ux", RecordedBaseState: worktreelanding.RecordedBaseAbsent,
		},
	}
}

func TestStackedTaskIsProvedOnlyWhenTheDefaultBranchContainsItsHead(t *testing.T) {
	t.Parallel()
	baseLanded := judgedBaseTip + "<" + judgedMain
	headLanded := judgedHead + "<" + judgedMain
	for _, test := range []struct {
		name      string
		origin    judgedOrigin
		contained bool
	}{
		{name: "base landed and head contained", contained: true, origin: judgedOrigin{ancestors: map[string]bool{baseLanded: true, headLanded: true}}},
		// The most dangerous refusal to lose: the base is in main, the task's
		// own commits are not.
		{name: "base landed but head not contained", origin: judgedOrigin{ancestors: map[string]bool{baseLanded: true}}},
		{name: "head contained but live base has not landed", origin: judgedOrigin{ancestors: map[string]bool{headLanded: true}}},
		{name: "default branch fetch failed", origin: judgedOrigin{fetchErr: errors.New("origin unreachable"), ancestors: map[string]bool{baseLanded: true, headLanded: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			inspection := stackedInspection(false)
			contained, err := inspection.judgeContainment(test.origin.ports())
			if err != nil || contained != test.contained {
				t.Fatalf("contained = %t, err = %v; want %t", contained, err, test.contained)
			}
			result := inspection.result
			if !test.contained {
				if result.Base != "cv-t1-schema-v2" || result.RemoteTargetSHA != judgedBaseTip || result.RecordedBase != "" ||
					result.RecordedBaseState != "" || result.IntegrationProof != "" {
					t.Fatalf("an unproved candidate was moved to another target: %#v", result)
				}
				return
			}
			want := "contained in origin/main at aaaaaaaaaaaa, via recorded base cv-t1-schema-v2 (integrated into origin/main)"
			if result.Base != "main" || inspection.base != "main" || result.RemoteTargetSHA != judgedMain ||
				result.RecordedBase != "cv-t1-schema-v2" || result.IntegrationProof != want {
				t.Fatalf("proved stacked candidate = %#v, want proof %q", result, want)
			}
		})
	}
}

func TestAbsentRecordedBaseIsProvedOnlyByContainmentInTheDefaultBranch(t *testing.T) {
	t.Parallel()
	refused := absentBaseInspection()
	if contained, err := refused.judgeContainment(judgedOrigin{}.ports()); err != nil || contained || refused.result.IntegrationProof != "" {
		t.Fatalf("contained = %t, err = %v, proof = %q; want an unproved candidate", contained, err, refused.result.IntegrationProof)
	}
	proved := absentBaseInspection()
	contained, err := proved.judgeContainment(judgedOrigin{ancestors: map[string]bool{judgedHead + "<" + judgedMain: true}}.ports())
	if want := "contained in origin/main at aaaaaaaaaaaa, via recorded base cockpit-ux (absent)"; err != nil || !contained || proved.result.IntegrationProof != want {
		t.Fatalf("contained = %t, err = %v, proof = %q; want %q", contained, err, proved.result.IntegrationProof, want)
	}
}

func TestContainmentThatCannotBeReadFailsTheInspection(t *testing.T) {
	t.Parallel()
	boom := errors.New("git: bad object")
	inspection := absentBaseInspection()
	if contained, err := inspection.judgeContainment(judgedOrigin{ancestry: boom}.ports()); !errors.Is(err, boom) || contained {
		t.Fatalf("contained = %t, err = %v; want the read failure", contained, err)
	}
}

// A base the operator named is the target and nothing else: it is never
// replaced by the default branch, and when it proves the head the result says
// so by name and SHA.
func TestExplicitBaseIsNeverReplacedAndNamesItselfAsTheProof(t *testing.T) {
	t.Parallel()
	everythingLanded := map[string]bool{judgedBaseTip + "<" + judgedMain: true, judgedHead + "<" + judgedMain: true}
	refused := stackedInspection(true)
	if contained, err := refused.judgeContainment(judgedOrigin{ancestors: everythingLanded}.ports()); err != nil || contained ||
		refused.result.Base != "cv-t1-schema-v2" || refused.result.IntegrationProof != "" {
		t.Fatalf("contained = %t, err = %v, result = %#v; want the explicit base kept and refused", contained, err, refused.result)
	}
	proved := stackedInspection(true)
	proved.result.RecordedBase = "cockpit-ux"
	contained, err := proved.judgeContainment(judgedOrigin{ancestors: map[string]bool{judgedHead + "<" + judgedBaseTip: true}}.ports())
	want := "contained in origin/cv-t1-schema-v2 at cccccccccccc, the base named with --base (recorded base cockpit-ux)"
	if err != nil || !contained || proved.result.IntegrationProof != want {
		t.Fatalf("contained = %t, err = %v, proof = %q; want %q", contained, err, proved.result.IntegrationProof, want)
	}
}
