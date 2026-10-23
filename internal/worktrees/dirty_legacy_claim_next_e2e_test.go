//go:build e2e

package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

//nolint:paralleltest // Existing native legacy fixture configures process-wide Git/PR environment.
func TestE2EDirtyLegacyPlannerValidatesResealedStaticIdentity(t *testing.T) {
	f := newLegacyMissingClaimNativeFixture(t, "legacy-static-identity", false)
	good, err := planLegacyMissingClaimRecovery(f.git.home, f.options, f.entry)
	if err != nil {
		t.Fatalf("native valid plan prerequisite: %v", err)
	}
	manifest, err := ReadManifest(f.entry.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := readWorkLogProjection(f.entry.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.BaseSHA = "invalid-base-sha"
	manifest.ClaimID = workLogClaimID(manifest.EffortID, CreateResult{Repository: manifest.Repository, WorktreeDir: manifest.Worktree, Branch: manifest.Branch, Base: manifest.Base, BaseSHA: manifest.BaseSHA})
	f.changeManifest(t, func(m *Manifest) { *m = manifest })
	projection.ClaimID = manifest.ClaimID
	wtLifeCovWriteJSON(t, f.projectionPath, projection)
	raw, err := os.ReadFile(f.outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	var event workLogPublicEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	event.ClaimID, event.BaseSHA = manifest.ClaimID, manifest.BaseSHA
	outboxPath := filepath.Join(filepath.Dir(f.outboxPath), manifest.RunID+"-"+manifest.ClaimID+"-claimed.json")
	wtLifeCovWriteJSON(t, outboxPath, event)
	// Every earlier digest/projection/outbox binding is resealed. Static BaseSHA admission remains independent.
	controlClaim := good.claim
	controlClaim.BaseSHA, controlClaim.ClaimID = manifest.BaseSHA, manifest.ClaimID
	cause := validateStaticWorkLogClaim(controlClaim, projection.EffortID, projection.RunID)
	if cause == nil {
		t.Fatal("actual static validator prerequisite accepted malformed object ID")
	}
	unchanged := assertLegacyEvidenceBytesUnchanged(t, f.manifestPath, f.projectionPath, f.outboxPath, outboxPath)
	got, err := planLegacyMissingClaimRecovery(f.git.home, f.options, f.entry)
	if err == nil || err.Error() != "reconstructed claim is invalid: "+cause.Error() || !reflect.DeepEqual(got, legacyMissingClaimPlan{}) {
		t.Fatalf("static reconstruction refusal: %+v %v control=%v", got, err, cause)
	}
	unchanged()
	for _, path := range []string{filepath.Join(f.runPath, "claims", manifest.ClaimID+".json"), filepath.Join(f.runPath, "recoveries", manifest.ClaimID+"-missing-claim.json")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refused resealed identity published private evidence: %v", err)
		}
	}
	f.assertNoRecoveryPublication(t)
}
