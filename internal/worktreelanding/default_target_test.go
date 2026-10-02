package worktreelanding

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeOrigin is a remote described by what it advertises: its default branch,
// the heads of the branches it still has, and which commits are ancestors of
// which. Nothing here runs Git.
type fakeOrigin struct {
	defaultBranch string
	defaultErr    error
	heads         map[string]string
	fetchErr      error
	ancestors     map[string]bool
	ancestryErr   map[string]error
	fetched       []string
}

func (origin *fakeOrigin) ports() DefaultTargetPorts {
	return DefaultTargetPorts{
		DefaultBranch: func(context.Context) (string, error) { return origin.defaultBranch, origin.defaultErr },
		FetchTargetHead: func(_ context.Context, branch string) (string, error) {
			origin.fetched = append(origin.fetched, branch)
			if origin.fetchErr != nil {
				return "", origin.fetchErr
			}
			return origin.heads[branch], nil
		},
		IsAncestor: func(_ context.Context, ancestor, descendant string) (bool, error) {
			key := ancestor + "<" + descendant
			return origin.ancestors[key], origin.ancestryErr[key]
		},
	}
}

const (
	mainSHA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	taskHead       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stackedBaseSHA = "cccccccccccccccccccccccccccccccccccccccc"
)

// Case 1 of the 2026-10-02 report: 21 tasks recorded base `cockpit-ux`, an
// integration branch merged into main by a merge commit and then deleted on
// origin. The task head is a plain ancestor of origin/main.
func TestDefaultTargetContainsHeadWhenRecordedIntegrationBranchIsAbsent(t *testing.T) {
	t.Parallel()
	origin := &fakeOrigin{
		defaultBranch: "main", heads: map[string]string{"main": mainSHA},
		ancestors: map[string]bool{taskHead + "<" + mainSHA: true},
	}
	target, err := ResolveDefaultTarget(context.Background(), origin.ports(), taskHead, "cockpit-ux", "")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || !target.Contained || target.Target != "main" || target.TargetSHA != mainSHA ||
		target.RecordedBase != "cockpit-ux" || target.RecordedBaseState != RecordedBaseAbsent {
		t.Fatalf("default target for an absent integration branch = %#v", target)
	}
	if got, want := target.Proof(), "contained in origin/main at aaaaaaaaaaaa, via recorded base cockpit-ux (absent)"; got != want {
		t.Fatalf("proof = %q, want %q", got, want)
	}
	if len(origin.fetched) != 1 || origin.fetched[0] != "main" {
		t.Fatalf("fetched %q, want only the default branch", origin.fetched)
	}
}

// Case 2 of the same report: a task stacked on another task's branch
// (cv-t2-metrics on cv-t1-schema-v2). The base branch is still on origin and
// its tip, like the task head, is contained in origin/main.
func TestDefaultTargetContainsHeadWhenStackedBaseHasItselfLanded(t *testing.T) {
	t.Parallel()
	origin := &fakeOrigin{
		defaultBranch: "main", heads: map[string]string{"main": mainSHA},
		ancestors: map[string]bool{stackedBaseSHA + "<" + mainSHA: true, taskHead + "<" + mainSHA: true},
	}
	target, err := ResolveDefaultTarget(context.Background(), origin.ports(), taskHead, "cv-t1-schema-v2", stackedBaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || !target.Contained || target.RecordedBaseState != RecordedBaseIntegrated {
		t.Fatalf("default target for a landed stacked base = %#v", target)
	}
	if got, want := target.Proof(), "contained in origin/main at aaaaaaaaaaaa, via recorded base cv-t1-schema-v2 (integrated into origin/main)"; got != want {
		t.Fatalf("proof = %q, want %q", got, want)
	}
}

func TestDefaultTargetNeverReplacesALiveBaseThatHasNotLanded(t *testing.T) {
	t.Parallel()
	// The head reached main some other way, but its own target is a live
	// feature branch that has not: that branch is still the task's target.
	origin := &fakeOrigin{
		defaultBranch: "main", heads: map[string]string{"main": mainSHA},
		ancestors: map[string]bool{taskHead + "<" + mainSHA: true},
	}
	target, err := ResolveDefaultTarget(context.Background(), origin.ports(), taskHead, "release-2", stackedBaseSHA)
	if err != nil || target != nil {
		t.Fatalf("live unlanded base was replaced: %#v, %v", target, err)
	}
}

func TestDefaultTargetReportsAHeadTheDefaultBranchDoesNotContain(t *testing.T) {
	t.Parallel()
	origin := &fakeOrigin{defaultBranch: "main", heads: map[string]string{"main": mainSHA}}
	target, err := ResolveDefaultTarget(context.Background(), origin.ports(), taskHead, "cockpit-ux", "")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || target.Contained || target.Target != "main" || target.TargetSHA != mainSHA {
		t.Fatalf("uncontained head = %#v, want the default target named and Contained false", target)
	}
}

func TestDefaultTargetIsNotOfferedForTheDefaultBranchItself(t *testing.T) {
	t.Parallel()
	origin := &fakeOrigin{defaultBranch: "main", heads: map[string]string{"main": mainSHA}}
	target, err := ResolveDefaultTarget(context.Background(), origin.ports(), taskHead, "main", "")
	if err != nil || target != nil {
		t.Fatalf("default branch replaced itself: %#v, %v", target, err)
	}
	if len(origin.fetched) != 0 {
		t.Fatalf("fetched %q for a base that is already the default branch", origin.fetched)
	}
}

func TestDefaultTargetFailsClosedOnEveryObservationError(t *testing.T) {
	t.Parallel()
	boom := errors.New("origin unreachable")
	for _, test := range []struct {
		name   string
		origin *fakeOrigin
		base   string
	}{
		{name: "default branch unknown", origin: &fakeOrigin{defaultErr: boom}},
		{name: "default branch fetch failed", origin: &fakeOrigin{defaultBranch: "main", fetchErr: boom}},
		{name: "base ancestry unreadable", base: stackedBaseSHA, origin: &fakeOrigin{
			defaultBranch: "main", heads: map[string]string{"main": mainSHA},
			ancestryErr: map[string]error{stackedBaseSHA + "<" + mainSHA: boom},
		}},
		{name: "head ancestry unreadable", origin: &fakeOrigin{
			defaultBranch: "main", heads: map[string]string{"main": mainSHA},
			ancestryErr: map[string]error{taskHead + "<" + mainSHA: boom},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target, err := ResolveDefaultTarget(context.Background(), test.origin.ports(), taskHead, "cockpit-ux", test.base)
			if !errors.Is(err, boom) || target != nil {
				t.Fatalf("target = %#v, err = %v; want no target and the observation error", target, err)
			}
		})
	}
}

func TestNotIntegratedReasonNamesTheTargetAndThePushState(t *testing.T) {
	t.Parallel()
	pushed := NotIntegratedReason(taskHead, "cv-t1-schema-v2", stackedBaseSHA, "cv-t2-metrics", taskHead)
	if want := "current branch head bbbbbbbbbbbb is not integrated into the exact origin target origin/cv-t1-schema-v2 at cccccccccccc (pushed to origin/cv-t2-metrics, awaiting merge)"; pushed != want {
		t.Fatalf("pushed refusal = %q, want %q", pushed, want)
	}
	for name, remoteHead := range map[string]string{"branch absent on origin": "", "origin behind": mainSHA} {
		reason := NotIntegratedReason(taskHead, "main", mainSHA, "task", remoteHead)
		if !strings.HasSuffix(reason, "origin/main at aaaaaaaaaaaa (awaiting push)") {
			t.Fatalf("%s: refusal = %q", name, reason)
		}
	}
	if got, want := NotIntegratedReason(taskHead, "main", "", "task", ""), "current branch head bbbbbbbbbbbb is not integrated into the exact origin target (awaiting push)"; got != want {
		t.Fatalf("refusal without a fetched target = %q, want %q", got, want)
	}
}
