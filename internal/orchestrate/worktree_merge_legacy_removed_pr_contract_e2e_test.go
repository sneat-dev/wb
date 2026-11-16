//go:build e2e

package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContractLegacyRemovedPRAcknowledgementRefusesAmbiguousCanonicalRepository
// proves recovery cannot pick one of two real canonical clones for the same
// coordinate and that the refusal happens before any acknowledgement or receipt
// bytes are written. The existing malformed-repository case is refused by the
// shared pull request validator earlier, so only an actual ambiguity reaches
// this resolver.
func TestContractLegacyRemovedPRAcknowledgementRefusesAmbiguousCanonicalRepository(t *testing.T) {
	t.Parallel()
	r, _ := historicalObservedAcknowledgement(t)
	r.Route.Route = WorktreeMergeRoutePullRequest
	r.Cleanup = true
	r.PullRequest = "https://github.com/acme/app/pull/108"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate.Task = r.ID
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := t.TempDir()
	remote := filepath.Join(t.TempDir(), "app.git")
	runEngineGit(t, projectsRoot, "init", "--bare", "--initial-branch=main", remote)
	for _, host := range []string{"github.com", "gitlab.example.test"} {
		canonical := filepath.Join(projectsRoot, host, "acme", "app")
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, projectsRoot, "clone", "--no-hardlinks", remote, canonical)
	}
	o := WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: projectsRoot, Receipt: r.ReceiptPath, LegacyRemovedPR: true,
		Actor: "reviewer", Reason: "ambiguous canonical repository",
	}
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("ambiguous canonical repository accepted: %v", err)
	}
	if _, err := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("ambiguous repository wrote an acknowledgement: %v", err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("ambiguous repository changed the failed receipt: %v", err)
	}
}
