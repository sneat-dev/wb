package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

// TestValidatePrivateParkPlanRejectsNonCanonicalEnvelope drives
// validatePrivateParkPlan's canonicalization check (private.go): the
// admitted bytes on disk decode successfully and match the plan's exact
// digest (so the earlier digest-match gate passes), but re-encoding the
// decoded envelope does not reproduce those exact bytes byte-for-byte
// (here: a single extra space inside the outer object, which
// encoding/json's decoder ignores but EncodeEnvelope's canonical writer
// would never itself produce).
func TestValidatePrivateParkPlanRejectsNonCanonicalEnvelope(t *testing.T) {
	state, plan, _ := slCovParkEnvelopeFixture(t, "envelope continuation\n", "envelope continuation\n")

	aggregate := filepath.Join(plan.StoreRoot, plan.HandoffID)
	envelopePath := filepath.Join(aggregate, sessionpark.EnvelopeFileName)
	original, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(original), "{", "{ ", 1)
	if tampered == string(original) {
		t.Fatal("test fixture assumption broken: envelope JSON has no leading '{' to pad")
	}
	if err := os.WriteFile(envelopePath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	plan.RequestDigest = sessionmove.DigestBytes([]byte(tampered))

	if _, err := validatePrivateParkPlan(state, plan); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("validatePrivateParkPlan(non-canonical envelope) = %v, want a not-canonical error", err)
	}
}

// TestInspectPreparedRejectsWorktreeWhenCurrentDirectoryIsGone drives
// InspectPrepared's own worktree-resolution gate (launch.go): its
// filepath.Abs(options.WorktreeDir) can only fail (making the "clean
// absolute path" branch reachable at all -- filepath.Abs already returns a
// cleaned path, so the Clean(...) != ... half of that condition is
// otherwise dead) when os.Getwd itself fails, which happens when the
// process's current directory has been removed out from under it. This
// test reproduces that for a relative WorktreeDir without any new
// production seam: it chdirs into a scratch directory it then deletes.
//
// Not run in parallel: os.Chdir is process-wide.
func TestInspectPreparedRejectsWorktreeWhenCurrentDirectoryIsGone(t *testing.T) {
	fixture := slCovNewAuthorityFixture(t)
	options := fixture.options()
	options.WorktreeDir = "relative/worktree"

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectPrepared(context.Background(), options); err == nil ||
		!strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("InspectPrepared(worktree resolution with no current directory) = %v, want a clean-absolute-path error", err)
	}
}
