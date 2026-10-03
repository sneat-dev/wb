package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR9 is task-9 PR-9's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR9 = errors.New("pr9 boom")

// --- defaultBranchReportPathInjected ---

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

// --- applySharedRulesetInjected ---

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
