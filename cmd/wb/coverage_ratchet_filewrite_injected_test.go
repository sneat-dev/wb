package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

// --- changedCoverageProfilePathInjected (runChangedCoverage) ---

func TestChangedCoverageProfilePathInjectedKeepsAnExplicitProfile(t *testing.T) {
	t.Parallel()
	explicit := filepath.Join(t.TempDir(), "explicit.out")
	path, removeProfile, err := changedCoverageProfilePathInjected(explicit, nil)
	if err != nil || path != explicit || removeProfile {
		t.Fatalf("changedCoverageProfilePathInjected(explicit) = (%q, %v, %v), want (%q, false, nil)", path, removeProfile, err, explicit)
	}
}

func TestChangedCoverageProfilePathInjectedReservesAScratchPath(t *testing.T) {
	t.Parallel()
	path, removeProfile, err := changedCoverageProfilePathInjected("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !removeProfile {
		t.Fatal("removeProfile = false, want true for a reserved scratch path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("reserved scratch path missing: %v", statErr)
	}
	_ = os.Remove(path)
}

func TestChangedCoverageProfilePathInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, removeProfile, err := changedCoverageProfilePathInjected("", inj)
	if path != "" || removeProfile || !errors.Is(err, errBoomPR9) {
		t.Fatalf("changedCoverageProfilePathInjected with injected create failure = (%q, %v, %v), want (\"\", false, errBoomPR9)", path, removeProfile, err)
	}
}

func TestChangedCoverageProfilePathInjectedIgnoresAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	// runChangedCoverage's original inline sequence discarded Close's error
	// entirely (`_ = file.Close()`): the reserved path is about to be
	// overwritten wholesale by `go test -coverprofile` regardless.
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, removeProfile, err := changedCoverageProfilePathInjected("", inj)
	if path == "" || !removeProfile || err != nil {
		t.Fatalf("changedCoverageProfilePathInjected with injected close failure = (%q, %v, %v), want (non-empty, true, nil)", path, removeProfile, err)
	}
	_ = os.Remove(path)
}

// --- defaultBranchReportPathInjected ---

func TestDefaultBranchReportPathInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomPR9}
	path, err := defaultBranchReportPathInjected(&invocation{}, t.TempDir(), inj)
	if path != "" || !errors.Is(err, errBoomPR9) {
		t.Fatalf("defaultBranchReportPathInjected with injected create failure = (%q, %v), want (\"\", errBoomPR9)", path, err)
	}
}

func TestDefaultBranchReportPathInjectedLeavesTheReservationOnAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomPR9}
	path, err := defaultBranchReportPathInjected(&invocation{}, dir, inj)
	if path != "" || !errors.Is(err, errBoomPR9) {
		t.Fatalf("defaultBranchReportPathInjected with injected close failure = (%q, %v), want (\"\", errBoomPR9)", path, err)
	}
	// The original inline sequence never removed the reservation on a Close
	// failure (only the later, deliberate os.Remove(path) freed the name on
	// the success path); migrating to filewrite.CreateScratch preserves that
	// exact on-disk-failure behaviour rather than tightening it.
	matches, globErr := filepath.Glob(filepath.Join(dir, "default-branch-*.json"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("reservation(s) after an injected close failure = %v, want exactly one left in place (original behaviour)", matches)
	}
}

// --- applyClassicProtectionWithoutLinearHistoryInjected ---

// TestApplyClassicProtectionWithoutLinearHistoryInjectedHonoursInjectedFailures
// does not run its subtests under t.Parallel(): applyClassicProtectionWithoutLinearHistoryInjected
// has no seam to give each case its own directory (it always reserves its
// scratch file in the shared, package-wide os.TempDir()), and every case
// globs that same "wb-merge-policy-protection-*.json" namespace to prove
// what each failure leaves on disk. Run in parallel, one case's own live
// scratch file is visible to a sibling case's glob and produces a false
// "leftover scratch file" failure — the same hazard
// internal/locallink/execports_contenthash_filewrite_injected_test.go
// documents and avoids the same way.
func TestApplyClassicProtectionWithoutLinearHistoryInjectedHonoursInjectedFailures(t *testing.T) {
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			err := applyClassicProtectionWithoutLinearHistoryInjected(context.Background(), "repos/acme/app/branches/main/protection", []byte("{}"), inj)
			if !errors.Is(err, errBoomPR9) {
				t.Fatalf("applyClassicProtectionWithoutLinearHistoryInjected(%s failure) = %v, want errBoomPR9", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-merge-policy-protection-*.json"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover scratch file(s) after an injected %s failure: %v", step, matches)
			}
		})
	}
}

// --- applySharedRulesetInjected ---

func TestApplySharedRulesetInjectedHonoursInjectedFailures(t *testing.T) {
	originalRead, originalExecute := mergePolicyRead, mergePolicyExecute
	t.Cleanup(func() { mergePolicyRead, mergePolicyExecute = originalRead, originalExecute })
	mergePolicyRead = func(context.Context, string) ([]byte, error) {
		return []byte(`{"id":7,"name":"default","target":"branch","enforcement":"active","rules":[{"type":"required_linear_history"}]}`), nil
	}
	mergePolicyExecute = func(context.Context, ...string) githubobserver.CommandResponse {
		t.Fatal("mergePolicyExecute must not run when the scratch write itself fails")
		return githubobserver.CommandResponse{}
	}
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite, filewrite.StepClose} {
		inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
		err := applySharedRulesetInjected(context.Background(), mergePolicyRulesetChange{SourceType: "Repository", Source: "acme/app", ID: 7}, inj)
		if !errors.Is(err, errBoomPR9) {
			t.Fatalf("applySharedRulesetInjected(%s failure) = %v, want errBoomPR9", step, err)
		}
		matches, globErr := filepath.Glob(filepath.Join(os.TempDir(), "wb-merge-policy-ruleset-*.json"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("leftover scratch file(s) after an injected %s failure: %v", step, matches)
		}
	}
}

// --- fetchPolicyInjected ---

func TestFetchPolicyInjectedHonoursInjectedFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(testPolicyDocument))
	}))
	// t.Cleanup, not defer: the subtests below run t.Parallel(), which
	// pauses them until this outer function's synchronous body returns --
	// a deferred server.Close() would already have fired by then, and every
	// subtest would see a closed server instead of the injected failure.
	// t.Cleanup runs only once this test and every paused parallel subtest
	// have actually finished.
	t.Cleanup(server.Close)
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepWrite} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR9}
			path, err := fetchPolicyInjected(server.URL+"/policy.yaml", inj)
			if path != "" || !errors.Is(err, errBoomPR9) {
				t.Fatalf("fetchPolicyInjected(%s failure) = (%q, %v), want (\"\", errBoomPR9)", step, path, err)
			}
		})
	}
}
