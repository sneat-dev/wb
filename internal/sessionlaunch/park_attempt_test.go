package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestInspectPreparedParkAttemptPreservesNativeMissingStateError(t *testing.T) {
	t.Parallel()
	fixture := newLauncherRetryFixture(t)
	id, err := InspectPreparedParkAttempt(context.Background(), fixture.options(nil))
	if id != "" || err != ErrNotReleased {
		t.Fatalf("attempt=%q error=%v; want empty attempt and exact ErrNotReleased", id, err)
	}
}

func TestInspectPreparedParkAttemptRequiresExplicitAuthenticatedAuthority(t *testing.T) {
	t.Parallel()
	fixture := newLauncherRetryFixture(t)
	state, err := openLaunchState(fixture.store.Root, fixture.request.HandoffID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	// Complete the legacy fixture plan before any attempt is published, using
	// the same real resolved authority for both inspection modes.
	options := fixture.options(nil)
	resolved, err := resolveAuthority(options)
	if err != nil {
		t.Fatal(err)
	}
	fixture.plan.PinnedBranch = resolved.launch.PinnedBranch
	fixture.plan.AuthorityFile = resolved.launch.AggregateFile
	fixture.plan.ContinuationKind = string(resolved.launch.ContinuationKind)
	fixture.plan.ContinuationDigest = sessionmove.Digest(resolved.launch.ContinuationDigest)
	if err := os.Remove(filepath.Join(fixture.store.Root, fixture.request.HandoffID, launchDirectoryName, "plan.json")); err != nil {
		t.Fatal(err)
	}
	_, fixture.planDigest, _, err = state.savePlan(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := state.createAttempt()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attempt.Close() })
	const pid = 939393
	fence, err := attempt.acquireExecFence(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	record := session.Record{PID: pid, WBSessionID: fixture.plan.SuccessorWBSessionID, Machine: fixture.plan.Machine,
		Runtime: fixture.plan.Runtime, Model: fixture.plan.Model, TmuxName: fixture.plan.TmuxName,
		PredecessorWBSessionID: fixture.plan.PredecessorWBSessionID, HandoffID: fixture.plan.HandoffID, StartedAt: fixture.deps.now()}
	if _, err := attempt.saveReady(fixture.plan, fixture.planDigest, record); err != nil {
		t.Fatal(err)
	}
	evidence, err := InspectPrepared(context.Background(), options)
	if err != nil || evidence.AttemptID != attempt.id || !evidence.Authenticates(fixture.request.HandoffID, fixture.digest) {
		t.Fatalf("native prepared evidence=%#v error=%v", evidence, err)
	}
	id, err := InspectPreparedParkAttempt(context.Background(), options)
	if id != "" || err == nil || err.Error() != "prepared local launcher evidence is not authenticated to the exact parked aggregate" {
		t.Fatalf("nil explicit authority: attempt=%q error=%v", id, err)
	}
	options.Authority, options.StoreRoot, options.Fence = &resolved.launch, resolved.storeRoot, resolved.fence
	id, err = InspectPreparedParkAttempt(context.Background(), options)
	if err != nil || id != attempt.id {
		t.Fatalf("explicit resolved authority: attempt=%q error=%v; want %q", id, err, attempt.id)
	}
}
