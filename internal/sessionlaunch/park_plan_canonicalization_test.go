package sessionlaunch

import (
	"context"
	"errors"
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

	if _, err := validatePrivateParkPlanWithDirectory(state, plan, os.Getwd, os.Stat); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("validatePrivateParkPlan(non-canonical envelope) = %v, want a not-canonical error", err)
	}
}

// TestInspectPreparedWithRemovedCurrentDirectory pins both native outcomes
// for a relative WorktreeDir. On Linux, Getwd fails after removal and the
// clean-absolute-path gate rejects it. On Darwin, Getwd can still name the
// unlinked directory; resolution succeeds, but the resolved path must not
// match the immutable plan's pinned worktree.
//
// Not run in parallel: t.Chdir is process-wide (and panics if combined with
// t.Parallel).
func TestInspectPreparedWithRemovedCurrentDirectory(t *testing.T) {
	fixture := slCovNewAuthorityFixture(t)
	options := fixture.options()
	options.WorktreeDir = "relative/worktree"

	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	cwd, cwdErr := os.Getwd()
	resolved, resolveErr := filepath.Abs(options.WorktreeDir)
	if cwdErr != nil {
		if !errors.Is(cwdErr, os.ErrNotExist) || resolveErr == nil {
			t.Fatalf("removed current directory: Getwd = %v, Abs = %q, %v; want matching ENOENT resolution failure", cwdErr, resolved, resolveErr)
		}
		if _, err := InspectPrepared(context.Background(), options); err == nil ||
			!strings.Contains(err.Error(), "clean absolute path") {
			t.Fatalf("InspectPrepared(unresolvable worktree) = %v, want a clean-absolute-path error", err)
		}
		return
	}
	if !filepath.IsAbs(cwd) || resolveErr != nil || resolved != filepath.Join(cwd, options.WorktreeDir) {
		t.Fatalf("removed current directory: Getwd = %q, Abs = %q, %v; want an absolute child of the unlinked directory", cwd, resolved, resolveErr)
	}
	if resolved == fixture.worktree {
		t.Fatal("relative worktree unexpectedly resolved to the admitted worktree")
	}
	if _, err := InspectPrepared(context.Background(), options); err == nil ||
		!strings.Contains(err.Error(), "immutable launch plan conflicts with the admitted request or pinned worktree") {
		t.Fatalf("InspectPrepared(resolvable but unadmitted worktree) = %v, want immutable-plan refusal", err)
	}
}
